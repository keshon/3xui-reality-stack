"""Bounded deployment steps, durable checkpoints, and exclusive ownership.

This is a deployment plan, not a distributed transaction. An interrupted step
is reconciled again; completed steps are kept. Final checks must pass before
the new configuration is marked applied.
"""
from __future__ import annotations

from dataclasses import dataclass
from datetime import datetime, timezone
import os
from pathlib import Path
import re
import signal
import subprocess
import sys
import threading
import time
import uuid

from .config import Store, SECRET_KEYS, fingerprint, normalize
from .envio import read_env


@dataclass(frozen=True)
class Step:
    name: str
    label: str
    timeout: int = 300


STEPS = {
    s.name: s for s in (
        Step("dns", "Check DNS points at this host", 60),
        Step("host", "Prepare Docker, kernel settings and firewall", 1800),
        Step("stack", "Render Compose configuration and start containers", 300),
        Step("certificate", "Wait for a valid certificate", 360),
        Step("panel", "Set panel credentials and path (restarts panel)", 180),
        Step("subscription", "Configure public subscriptions"),
        Step("token", "Ensure the node API token exists"),
        Step("nodes", "Register nodes and check connectivity"),
        Step("lanes", "Ensure protocol lanes exist (may restart Xray)", 600),
        Step("accounts", "Consolidate legacy per-lane accounts"),
        Step("clients", "Reconcile managed users and explicit revocations", 600),
        Step("verify", "Verify website, certificate and Xray", 180),
        Step("acceptance", "Verify desired users, nodes and subscriptions", 600),
    )
}


def now():
    return datetime.now(timezone.utc).isoformat(timespec="seconds")


def redactor(config):
    values = [v for k, v in config["env"].items() if k in SECRET_KEYS and v]
    values += [n[k] for n in config["nodes"] for k in ("token", "url") if n[k]]
    values.sort(key=len, reverse=True)

    def redact(text):
        for value in values:
            text = text.replace(value, "[hidden]")
        text = re.sub(r"(?:https?://|vless://|trojan://|tg://|vpn://|hy2://|hysteria2://)\S+", "[link hidden]", text)
        # Do not allow a service response to insert terminal control sequences.
        return re.sub(r"[\x00-\x08\x0b-\x1f\x7f]", "", text)
    return redact


class Engine:
    def __init__(self, store: Store, runner=None):
        self.store = store
        self.runner = runner or self.run_process
        self.cancel = threading.Event()
        self.lock_fd = None

    def plan(self, candidate=None):
        if self.store.json(self.store.directory / "adoption.json", {}).get("status") == "preparing":
            raise ValueError("An interrupted adoption needs stack adopt before configuration apply.")
        if self.store.json(self.store.directory / "upgrade.json", {}).get("status") in ("applying", "rolling_back", "rollback_failed"):
            raise ValueError("An interrupted source upgrade needs stack rollback --backup PATH before configuration apply.")
        if candidate is None and not self.store.config_path.exists():
            raise ValueError("Save your configuration with 'stack wizard' first.")
        config = normalize(candidate if candidate is not None else self.store.load())
        previous = self.store.json(self.store.applied_path, {})
        pending = self.store.json(self.store.journal_path, {})
        if pending.get("status") == "editable":
            old = pending["recovery_config"]
            if old["env"]["ROLE"] != config["env"]["ROLE"]:
                raise ValueError("Recovery cannot change the host role.")
            protected = {"BASE_DOMAIN", "PUBLIC_DOMAIN", "PANEL_DOMAIN", "TROJAN_PATH", "CERT_BASENAME", "MTPROTO_PORT", "ACME_MODE", "MANAGEMENT_PORT", "MANAGEMENT_BIND"}
            if pending.get("protect_topology") and any(old["env"].get(k) != config["env"].get(k) for k in protected):
                raise ValueError("Recovery preserves deployed topology. Fix credentials/users or connectivity; domain migration requires the runbook.")
            if set(old["env"]["LANES"].split(",")) - set(config["env"]["LANES"].split(",")):
                raise ValueError("Recovery cannot remove active lanes.")
            if float(old["env"].get("USER_LIMIT_GB") or 0) > 0 and float(config["env"].get("USER_LIMIT_GB") or 0) == 0:
                raise ValueError("Recovery cannot silently clear existing traffic limits.")
            if old["env"]["ROLE"] == "node" and pending.get("protect_topology") and old["env"]["PANEL_PATH"] != config["env"]["PANEL_PATH"]:
                raise ValueError("Node panel path changes require a coordinated master migration.")
            old_nodes = {n["name"]: n for n in old["nodes"]}
            new_nodes = {n["name"]: n for n in config["nodes"]}
            if any(name not in new_nodes or any(node[k] != new_nodes[name][k] for k in ("url", "domain")) for name, node in old_nodes.items()):
                raise ValueError("Recovery can update node tokens, but cannot replace or remove nodes.")
            revoked = sorted(set(pending["recovery_users"]) - set(config["users"]))
            if revoked and config["nodes"]:
                raise ValueError("Fleet-wide revocation remains unverified; refusing partial changes.")
            role = config["env"]["ROLE"]
            names = ["dns", "host", "stack", "certificate", "panel"]
            names += ["token"] if role == "node" else ["subscription"] + (["nodes"] if config["nodes"] else []) + ["lanes", "accounts", "clients", "verify"]
            return config, [STEPS[n] for n in names + ["acceptance"]], revoked
        if pending and pending.get("status") != "complete":
            runtime = pending.get("runtime", {})
            effective = {**config, "env": {**config["env"], **runtime}}
            if pending.get("config_digest") != fingerprint(config) and pending.get("effective_digest") != fingerprint(effective):
                raise ValueError("Configuration differs from the unfinished operation. Restore it before resuming.")
            return effective, [STEPS[name] for name in pending["steps"] if name not in pending["completed"]], pending.get("revoke", [])
        if previous and previous["output_hashes"] != self.store.output_hashes():
            raise ValueError("Generated files changed outside stack. Restore them from .stack/applied.json or explicitly re-import in a separate checkout; refusing to overwrite them.")
        old = previous.get("config")
        if old and old == config:
            return config, [], []
        if old and old["env"].get("ROLE") != config["env"]["ROLE"]:
            raise ValueError("Changing an applied host role requires a migration; use a separate host for now.")
        old_nodes = {n["name"]: n for n in old["nodes"]} if old else {}
        new_nodes = {n["name"]: n for n in config["nodes"]}
        for name, node in old_nodes.items():
            if name not in new_nodes or any(node[k] != new_nodes[name][k] for k in ("url", "domain")):
                raise ValueError(f"Replacing/removing node '{name}' needs the replacement runbook. This release supports adding nodes and rotating their tokens.")
        # Existing inbounds only have targeted reconcilers. Do not silently
        # publish new addresses while the listener still has the old settings.
        topology = {"BASE_DOMAIN", "PUBLIC_DOMAIN", "PANEL_DOMAIN", "TROJAN_PATH", "CERT_BASENAME", "MTPROTO_PORT", "ACME_MODE", "MANAGEMENT_PORT", "MANAGEMENT_BIND"}
        if old and any(old["env"].get(k) != config["env"].get(k) for k in topology):
            raise ValueError("Changing deployed domains, Trojan path, MTProto/management ports, management binding or ACME mode requires migration. Automatic live migration is not supported yet.")
        if old and config["env"]["ROLE"] == "node" and old["env"].get("PANEL_PATH") != config["env"].get("PANEL_PATH"):
            raise ValueError("Changing a traffic node's panel path requires a coordinated master endpoint migration.")
        if old and set(old["env"]["LANES"].split(",")) - set(config["env"]["LANES"].split(",")):
            raise ValueError("Removing active lanes requires explicit detachment and deletion. Automatic lane removal is not supported yet.")
        if old and float(old["env"].get("USER_LIMIT_GB") or 0) > 0 and float(config["env"].get("USER_LIMIT_GB") or 0) == 0:
            raise ValueError("Clearing existing traffic limits requires an explicit panel operation; the legacy reconciler preserves them.")
        revoked = sorted(set(old["users"]) - set(config["users"])) if old else []
        if revoked and config["nodes"]:
            raise ValueError("Fleet-wide revocation is not yet verifiable through the master API. Revoke on each node using the runbook; this console cannot confirm it yet.")
        # User-only edits never run host setup, Compose, or restart the panel.
        old_env = {k: v for k, v in old["env"].items() if k != "USERS"} if old else None
        new_env = {k: v for k, v in config["env"].items() if k != "USERS"}
        if old and old_env == new_env:
            names = (["nodes", "lanes"] if old["nodes"] != config["nodes"] else []) + ["clients", "verify", "acceptance"]
        else:
            names = ["dns"] + (["host"] if not old or old["env"].get("AWG_PORT") != config["env"].get("AWG_PORT") else []) + ["stack", "certificate", "panel"]
            names += ["token"] if config["env"]["ROLE"] == "node" else ["subscription"]
            if config["env"]["ROLE"] != "node":
                names += (["nodes"] if config["nodes"] else []) + ["lanes", "accounts", "clients"]
            names += (["verify"] if config["env"]["ROLE"] != "node" else []) + ["acceptance"]
        return config, [STEPS[n] for n in names], revoked

    def recover(self):
        """Explicitly retire checkpoints, keeping live changes and an audit copy.

        This is not rollback. The operator can correct input before a full
        recheck, e.g. replacing a mistyped node token after registration failed.
        """
        with self.store.lock():
            journal = self.store.json(self.store.journal_path, {})
            if not journal or journal.get("status") == "complete":
                raise ValueError("No unfinished operation to recover.")
            config = normalize(self.store.load())
            self.store.write_json(self.store.directory / "history" / (uuid.uuid4().hex + ".json"), journal)
            # Preserve a token generated just before a crash, even if the
            # normal step checkpoint was never committed.
            actual = read_env(self.store.root / ".env")
            for key in ("NODE_API_TOKEN", "PROXY_SUBNET"):
                if actual.get(key):
                    config["env"][key] = actual[key]
            config = normalize(config)
            applied = self.store.json(self.store.applied_path, {})
            old = journal.get("recovery_config", config)
            touched = set(journal.get("completed", [])) | {journal.get("current")}
            protect = bool(applied or journal.get("protect_topology") or touched & {"lanes", "accounts", "clients", "verify", "acceptance"})
            journal.update(status="editable", completed=[], recovery_config=old, recovery_users=sorted(set(journal.get("recovery_users", [])) | set(config["users"]) | set(applied.get("config", {}).get("users", []))), protect_topology=protect, updated_at=now())
            journal.pop("runtime", None)
            journal.pop("effective_digest", None)
            self.store.write_json(self.store.config_path, config)
            self.store.write_json(self.store.journal_path, journal)

    def command(self, step, config):
        if step.name in ("nodes", "clients", "acceptance"):
            return [sys.executable, "scripts/managed-apply.py", step.name]
        return ["bash", "install.sh", "--stage", step.name]

    def apply(self, emit=print, expected=None):
        with self.store.lock() as fd:
            self.lock_fd = fd
            config, steps, revoke = self.plan()
            if expected is not None and expected != fingerprint(config):
                raise ValueError("Configuration changed since this plan was reviewed. Review it again.")
            existing = self.store.json(self.store.journal_path, {})
            if not steps and existing.get("status", "complete") == "complete":
                emit("Configuration is already applied. Run 'stack doctor' to check live health.")
                return
            if self.runner == self.run_process and (sys.platform != "linux" or os.geteuid() != 0):
                raise ValueError("Apply runs on the Linux host as root. Configuration editing works on Windows too.")
            journal = self.store.json(self.store.journal_path, {})
            if journal.get("status") == "editable":
                journal.update(status="rendering", config_digest=fingerprint(config), steps=[s.name for s in steps], completed=[], revoke=revoke)
                self.store.write_json(self.store.journal_path, journal)
            if not journal or journal.get("status") == "complete":
                journal = {"version": 1, "config_digest": fingerprint(config), "steps": [s.name for s in steps], "completed": [], "revoke": revoke, "status": "ready", "started_at": now(), "output_hashes": self.store.output_hashes()}
                self.store.write_json(self.store.journal_path, journal)
            elif journal.get("status") != "rendering" and journal.get("output_hashes") != self.store.output_hashes():
                # A killed process can have committed a generated output before
                # it committed its journal entry. Only known installer outputs
                # may be adopted below; arbitrary edits must stop the resume.
                self.adopt_runtime(config, journal)
            try:
                # Rendering is repeatable, including a crash between file writes.
                journal["status"] = "rendering"
                self.store.write_json(self.store.journal_path, journal)
                self.store.render(config)
                journal["output_hashes"] = self.store.output_hashes()
                self.store.write_json(self.store.journal_path, journal)
                for step in steps:
                    if self.cancel.is_set():
                        raise InterruptedError("Stopped before the next step.")
                    journal.update(status="running", current=step.name, updated_at=now())
                    self.store.write_json(self.store.journal_path, journal)
                    emit(step.label + "…")
                    self.runner(step, config, emit)
                    self.adopt_runtime(config, journal)
                    journal["completed"].append(step.name)
                    journal.update(status="ready", updated_at=now())
                    journal.pop("error", None)
                    self.store.write_json(self.store.journal_path, journal)
                    emit("Done: " + step.label)
                self.store.write_json(self.store.config_path, config)
                self.store.write_json(self.store.applied_path, {"version": 1, "config": config, "output_hashes": self.store.output_hashes(), "applied_at": now()})
                journal.update(status="complete", finished_at=now())
                self.store.write_json(self.store.journal_path, journal)
                emit("Configuration applied. A real client connection is still the final protocol check.")
            except BaseException as error:
                journal.update(status="interrupted" if isinstance(error, (KeyboardInterrupt, InterruptedError)) else "failed", error=redactor(config)(str(error)), updated_at=now())
                self.store.write_json(self.store.journal_path, journal)
                emit("Progress saved. Fix the cause, then run 'stack resume'.")
                raise
            finally:
                self.lock_fd = None

    def adopt_runtime(self, config, journal):
        actual = read_env(self.store.root / ".env")
        allowed = {"PROXY_SUBNET", "NODE_API_TOKEN"}
        expected = config["env"]
        for key in set(actual) | set(expected):
            if actual.get(key) != expected.get(key):
                if key not in allowed or key not in actual:
                    raise ValueError(f"Generated .env changed unexpectedly ({key}); refusing to continue.")
                expected[key] = actual[key]
        outputs = self.store.outputs(config)
        for name in ("users.txt", "nodes.txt"):
            path = self.store.root / name
            if not path.exists() or path.read_text(encoding="utf-8") != outputs[name]:
                raise ValueError(f"Generated {name} changed unexpectedly; refusing to continue.")
        normalize(config)
        journal["runtime"] = {k: expected[k] for k in allowed if k in expected}
        journal["effective_digest"] = fingerprint(config)
        journal["output_hashes"] = self.store.output_hashes()

    def run_process(self, step, config, emit):
        redact = redactor(config)
        env = os.environ.copy()
        env.update(config["env"])
        env["PYTHONUNBUFFERED"] = "1"
        env["STACK_MANAGED_APPLY"] = "1"
        # Use the venv interpreter for Bash's python3 calls as well.
        env["PATH"] = str(Path(sys.executable).parent) + os.pathsep + env.get("PATH", "")
        command = self.command(step, config)
        log = self.store.directory / "operation.log"
        fd = os.open(log, os.O_WRONLY | os.O_CREAT | os.O_APPEND, 0o600)
        with os.fdopen(fd, "a", encoding="utf-8") as stream:
            stream.write(f"\n{now()} {step.name}\n"); stream.flush()
            process = subprocess.Popen(command, cwd=self.store.root, env=env, stdout=subprocess.PIPE, stderr=subprocess.STDOUT, text=True, encoding="utf-8", errors="replace", start_new_session=True, pass_fds=(self.lock_fd,) if self.lock_fd is not None else ())
            tail = []

            def drain():
                for line in process.stdout:
                    safe = redact(line.rstrip())
                    stream.write(safe + "\n"); stream.flush()
                    tail.append(safe)
                    del tail[:-12]

            reader = threading.Thread(target=drain, daemon=True)
            reader.start()
            started = time.monotonic()
            try:
                while process.poll() is None:
                    if self.cancel.is_set():
                        raise InterruptedError("Stopped by operator.")
                    if time.monotonic() - started > step.timeout:
                        raise TimeoutError(f"{step.label} exceeded {step.timeout}s.")
                    time.sleep(0.15)
            except BaseException:
                self.stop_process(process)
                raise
            finally:
                reader.join(timeout=5)
            if process.returncode:
                raise RuntimeError(f"{step.label} failed (exit {process.returncode}).\n" + "\n".join(tail))

    @staticmethod
    def stop_process(process):
        try:
            os.killpg(process.pid, signal.SIGTERM)
        except ProcessLookupError:
            return
        try:
            process.wait(timeout=5)
        except subprocess.TimeoutExpired:
            os.killpg(process.pid, signal.SIGKILL)
            process.wait()


def plan_text(engine):
    config, steps, revoked = engine.plan()
    lines = ["Deployment plan · local configuration comparison", ""]
    previous = engine.store.json(engine.store.applied_path, {})
    if not previous:
        lines.append("First managed deployment: host setup, container changes and restarts are included.")
    else:
        old = previous["config"]
        changed = sorted(k for k in set(config["env"]) | set(old["env"]) if config["env"].get(k) != old["env"].get(k) and k != "USERS")
        lines += ["Settings changed: " + (", ".join(changed) or "none")]
        lines += ["Users added: " + (", ".join(sorted(set(config["users"]) - set(old["users"]))) or "none")]
        lines += ["Nodes added/updated: " + (", ".join(n["name"] for n in config["nodes"] if n not in old["nodes"]) or "none")]
    if engine.store.json(engine.store.journal_path, {}).get("status") == "editable":
        lines.append("Recovery: existing server changes are kept; all deployment stages will be checked again.")
    if revoked:
        lines.append("REVOKE access: " + ", ".join(revoked))
    lines += ["", *[f"{i}. {s.label}" for i, s in enumerate(steps, 1)]]
    if not steps:
        lines.append("No configuration changes. Live drift is checked separately with 'stack doctor'.")
    lines += ["", "No remote changes have been made. Apply runs on the Linux host as root."]
    return "\n".join(lines)
