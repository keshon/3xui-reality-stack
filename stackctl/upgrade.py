"""Source-only upgrades. Deployment data and account reconciliation stay separate."""
from __future__ import annotations

from datetime import datetime, timezone
import hashlib
import json
import os
from pathlib import Path, PurePosixPath
import re
import shutil
import stat
import subprocess
import tempfile
import time
import tomllib
import uuid

from .config import Store, fingerprint
from .envio import atomic_write, read_env
from .health import measure
from .backup import CONFIG_FILES, snapshot_data


class UpgradeError(ValueError):
    """Messages are safe for output: never include subprocess stderr or secrets."""


PROTECTED = CONFIG_FILES
SOURCE_ROOTS = {"stackctl", "scripts", "tests", "docs", "traefik", "decoy", "xui"}
SOURCE_FILES = {".env.example", ".gitattributes", ".gitignore", "README.md", "LICENSE", "pyproject.toml",
                "install.sh", "bootstrap.sh", "stack.sh", "stack.cmd", "users.txt.example", "nodes.txt.example", "compatibility.json"}
FORBIDDEN = ("traefik/data", "traefik/dynamic/tls.yml", "xui/db", "decoy/site")
PROJECTS = ("traefik", "decoy", "xui")


def digest(path):
    return hashlib.sha256(path.read_bytes()).hexdigest() if path.is_file() else None


def source_name(name):
    if not isinstance(name, str):
        raise UpgradeError("Release path must be text.")
    p = PurePosixPath(name)
    if ("\\" in name or p.is_absolute() or str(p) != name
            or any(not re.fullmatch(r"[A-Za-z0-9_.-]+", part) for part in p.parts)
            or any(part in ("..", ".") for part in p.parts)
            or any(name == x or name.startswith(x + "/") for x in FORBIDDEN)
            or not (name in SOURCE_FILES or len(p.parts) > 1 and p.parts[0] in SOURCE_ROOTS)):
        raise UpgradeError("Release contains an invalid or deployment-owned path.")
    return name


def local_path(root, name):
    target = root / name
    # Refuse symlinks even when they happen to resolve inside the checkout.
    for path in (target, *target.parents):
        if path == root:
            break
        if path.is_symlink():
            raise UpgradeError("Source paths must not traverse symlinks.")
    if not target.resolve().is_relative_to(root.resolve()):
        raise UpgradeError("Source path escapes the checkout.")
    return target


def run(args, root, *, env=None, timeout=30):
    try:
        r = subprocess.run(args, cwd=root, env=env, capture_output=True, text=True, timeout=timeout)
    except (OSError, subprocess.TimeoutExpired):
        raise UpgradeError("Upgrade command unavailable or timed out; no raw service response was logged.") from None
    if r.returncode:
        raise UpgradeError("Upgrade command failed; inspect the relevant service locally.")
    return r.stdout


class Release:
    def __init__(self, directory):
        self.root = Path(directory).resolve()
        path = self.root / "RELEASE-MANIFEST.json"
        if path.is_file():
            try:
                manifest = json.loads(path.read_text(encoding="utf-8"))
                self.hashes = manifest["files"]
                self.revision = manifest.get("base_commit", "manifest")
            except (KeyError, ValueError):
                raise UpgradeError("Invalid release manifest.") from None
        else:
            if run(["git", "status", "--porcelain"], self.root).strip():
                raise UpgradeError("Release checkout must be clean. Commit changes or use a verified release manifest.")
            self.revision = run(["git", "rev-parse", "HEAD"], self.root).strip()
            names = run(["git", "ls-files", "-z"], self.root).split("\0")
            self.hashes = {name: digest(local_path(self.root, source_name(name))) for name in names if name}
        if not isinstance(self.hashes, dict) or not self.hashes:
            raise UpgradeError("Release has no source inventory.")
        for name, expected in self.hashes.items():
            path = local_path(self.root, source_name(name))
            if not isinstance(expected, str) or not re.fullmatch(r"[0-9a-f]{64}", expected) or digest(path) != expected:
                raise UpgradeError("Release source hash does not match its inventory.")
        required = {"compatibility.json", "pyproject.toml", "install.sh", "scripts/probe-tunnels.py", "scripts/panel.py"}
        required |= {f"{p}/docker-compose.yml" for p in PROJECTS} | {"traefik/docker-compose.dns01.yml"}
        if not required <= self.hashes.keys():
            raise UpgradeError("Release is incomplete or predates supported source upgrades.")
        try:
            self.contract = json.loads((self.root / "compatibility.json").read_text(encoding="utf-8"))
            if self.contract["schema"] != 1 or not isinstance(self.contract["xray_versions"], list) or not self.contract["xray_versions"]:
                raise ValueError()
            if not all(isinstance(v, str) and re.fullmatch(r"\d+\.\d+\.\d+", v) for v in self.contract["xray_versions"]):
                raise ValueError()
            for name in ("panel_image", "traefik_image"):
                if not isinstance(self.contract[name], str):
                    raise ValueError()
        except (KeyError, ValueError):
            raise UpgradeError("Unsupported compatibility contract.") from None
        self.token = fingerprint({"files": self.hashes, "contract": self.contract})


class Docker:
    def __init__(self, root):
        self.root = root
        self.values = read_env(root / ".env")
        self.env = {**os.environ, **self.values}

    def compose(self, project, source=None):
        filename = "docker-compose.dns01.yml" if project == "traefik" and self.values.get("ACME_MODE", "dns") == "dns" else "docker-compose.yml"
        return ["docker", "compose", "--project-directory", str(self.root / project),
                "-f", str((source or self.root) / project / filename), "--env-file", str(self.root / ".env")]

    def check(self, release, changed):
        actual_panel = run(["docker", "inspect", "-f", "{{.Config.Image}}", "3x-ui"], self.root).strip()
        core = run(["docker", "exec", "3x-ui", "/app/bin/xray-linux-amd64", "version"], self.root)
        version = re.search(r"^Xray ([\d.]+)", core)
        if actual_panel != release.contract["panel_image"] or not version or version[1] not in release.contract["xray_versions"]:
            raise UpgradeError("Running panel/core is outside this release's tested compatibility contract.")
        affected = {}
        for project in PROJECTS:
            cmd = self.compose(project, release.root)
            try:
                config = json.loads(run(cmd + ["config", "--format", "json"], self.root, env=self.env))
                hashes = dict(line.split() for line in run(cmd + ["config", "--hash", "*"], self.root, env=self.env).splitlines())
            except (ValueError, TypeError):
                raise UpgradeError("Could not validate Compose services and hashes.") from None
            for service, fields in config["services"].items():
                if service not in {"traefik", "certs-dumper", "decoy", "3x-ui"}:
                    raise UpgradeError("Custom Compose services require a separate migration.")
                image = fields.get("image")
                if service == "3x-ui" and image != release.contract["panel_image"] or service == "traefik" and image != release.contract["traefik_image"]:
                    raise UpgradeError("Compose image and compatibility contract disagree.")
                # No automatic pulls, builds or application image upgrades.
                container = fields["container_name"]
                desired_image = run(["docker", "image", "inspect", "-f", "{{.Id}}", image], self.root).strip()
                current_image = run(["docker", "inspect", "-f", "{{.Image}}", container], self.root).strip()
                if desired_image != current_image:
                    raise UpgradeError("An application image would change; source upgrades require a separate runtime migration.")
                actual = run(["docker", "inspect", "-f", '{{index .Config.Labels "com.docker.compose.config-hash"}}', container], self.root).strip()
                mounted_change = service == "certs-dumper" and "traefik/dump-certs.py" in changed or service == "decoy" and "decoy/nginx.conf" in changed
                if actual != hashes.get(service) or mounted_change:
                    affected.setdefault(project, []).append(service)
        return affected

    def recreate(self, affected):
        for project in PROJECTS:
            if project in affected:
                run(self.compose(project) + ["up", "-d", "--no-deps", "--no-build", "--pull", "never", "--force-recreate", *affected[project]],
                    self.root, env=self.env, timeout=120)

    def verify(self):
        # Give restarted services bounded time to become ready.
        for attempt in range(3):
            try:
                if not measure(self.root, self.values)["ok"]:
                    raise UpgradeError("Process, fallback HTTPS or authenticated verification failed.")
                return
            except (UpgradeError, ValueError):
                if attempt == 2:
                    raise UpgradeError("Authenticated verification failed after three attempts.") from None
                time.sleep(3)


def write_bytes(path, data, mode):
    path.parent.mkdir(parents=True, exist_ok=True)
    fd, name = tempfile.mkstemp(prefix=".upgrade-", dir=path.parent)
    try:
        with os.fdopen(fd, "wb") as stream:
            stream.write(data)
            stream.flush()
            os.fsync(stream.fileno())
        os.chmod(name, mode)
        os.replace(name, path)
    finally:
        if os.path.exists(name):
            os.unlink(name)


class Upgrade:
    def __init__(self, root, backend=None):
        self.root = Path(root).resolve()
        self.store = Store(self.root)
        self.backend = backend or Docker(self.root)
        self.journal = self.store.directory / "upgrade.json"

    def guard(self):
        if self.store.json(self.store.directory / "adoption.json", {}).get("status") == "preparing":
            raise UpgradeError("Finish the interrupted stack adopt before upgrading source.")
        if self.store.json(self.store.journal_path, {}).get("status", "complete") != "complete":
            raise UpgradeError("Finish or recover the configuration apply before upgrading source.")
        if self.store.json(self.journal, {}).get("status") in ("applying", "rolling_back", "rollback_failed"):
            raise UpgradeError("An interrupted source upgrade needs stack rollback --backup PATH.")
        if not (self.root / ".env").is_file() or not (self.root / "scripts/probe-tunnels.py").is_file():
            raise UpgradeError("Source upgrades require an existing deployment with authenticated probes.")
        if not (self.root / "RELEASE-MANIFEST.json").is_file() and not (self.root / ".git").exists():
            raise UpgradeError("This legacy deployment needs a reviewed source baseline before automated upgrades.")
        if read_env(self.root / ".env").get("ROLE") == "node":
            raise UpgradeError("Traffic nodes require coordinated verification from the master; upgrade them separately.")

    def plan(self, source):
        self.guard()
        release = Release(source)
        if release.root == self.root:
            raise UpgradeError("Use a separate release checkout; do not git pull over the live deployment.")
        baseline = self.root / "RELEASE-MANIFEST.json"
        if baseline.exists():
            old = json.loads(baseline.read_text(encoding="utf-8"))["files"]
            for name, expected in old.items():
                if digest(local_path(self.root, source_name(name))) != expected:
                    raise UpgradeError("Deployed source changed outside the last release; preserve and review those edits first.")
            if old.keys() - release.hashes.keys():
                raise UpgradeError("This release removes source files and needs an explicit migration.")
        else:
            if run(["git", "status", "--porcelain"], self.root).strip():
                raise UpgradeError("Live checkout has local source edits; preserve and review them first.")
            names = run(["git", "ls-files", "-z"], self.root).split("\0")
            if {source_name(name) for name in names if name} - release.hashes.keys():
                raise UpgradeError("This release removes source files and needs an explicit migration.")
        changed = sorted(name for name, expected in release.hashes.items() if digest(local_path(self.root, name)) != expected)
        for name in changed:
            target = local_path(self.root, name)
            if target.exists() and not target.is_file():
                raise UpgradeError("A release file conflicts with a deployment directory.")
        if "traefik/dynamic/tls.yml.tmpl" in changed and (self.root / "traefik/dynamic/tls.yml").exists():
            raise UpgradeError("TLS template changes require an explicit generated-config migration.")
        if (self.root / "pyproject.toml").exists():
            old = tomllib.loads((self.root / "pyproject.toml").read_text(encoding="utf-8"))["project"]
            new = tomllib.loads((release.root / "pyproject.toml").read_text(encoding="utf-8"))["project"]
            if any(old.get(k) != new.get(k) for k in ("dependencies", "requires-python")):
                raise UpgradeError("Dependency or Python requirement changes need a separate migration.")
        protected = {name: digest(self.root / name) for name in PROTECTED}
        before = {name: digest(self.root / name) for name in changed}
        affected = self.backend.check(release, changed)
        identity = fingerprint({"release": release.token, "before": before, "protected": protected, "services": affected})
        return {"release": release, "changed": changed, "services": affected, "protected": protected, "before": before, "token": identity}

    def describe(self, plan):
        services = [f"{p}/{s}" for p, names in plan["services"].items() for s in names]
        return "\n".join([
            f"Source files to replace/add: {len(plan['changed'])}",
            *("  " + name for name in plan["changed"]),
            "Services to recreate: " + (", ".join(services) or "none"),
            "Preserve: accounts, .env, user/node lists, certificates and managed settings.",
            "Backup: source rollback files, consistent SQLite snapshot and deployment secrets.",
            "Verify: authenticated transfers. Failure restores source/services automatically.",
        ])

    def backup(self, plan):
        stamp = datetime.now(timezone.utc).strftime("%Y%m%dT%H%M%SZ") + "-" + uuid.uuid4().hex[:8]
        directory = self.store.directory / "upgrades" / stamp
        directory.mkdir(parents=True, mode=0o700)
        directory.parent.chmod(0o700)
        names = [*plan["changed"], "RELEASE-MANIFEST.json"]
        modes = {}
        for name in names:
            path = local_path(self.root, name)
            if path.is_file():
                dest = directory / "source" / name
                dest.parent.mkdir(parents=True, exist_ok=True)
                shutil.copy2(path, dest)
                modes[name] = stat.S_IMODE(path.stat().st_mode)
        for name in plan["changed"]:
            data = local_path(plan["release"].root, name).read_bytes()
            if hashlib.sha256(data).hexdigest() != plan["release"].hashes[name]:
                raise UpgradeError("Release changed while preparing the backup; no source was replaced.")
            dest = directory / "release" / name
            dest.parent.mkdir(parents=True, exist_ok=True)
            dest.write_bytes(data)
        snapshot_data(self.root, directory / "data")
        record = {"root": str(self.root), "changed": plan["changed"], "services": plan["services"], "before": plan["before"],
                  "after": {name: plan["release"].hashes[name] for name in plan["changed"]}, "modes": modes}
        atomic_write(directory / "rollback.json", json.dumps(record, indent=2))
        # The prior source may not have the upgrade CLI. Keep a private copy
        # of this tool so recovery still works after removing newly added files.
        recovery = directory / "recovery" / "stackctl"
        recovery.mkdir(parents=True)
        for name in ("__init__.py", "upgrade.py", "config.py", "envio.py", "health.py", "backup.py"):
            shutil.copyfile(Path(__file__).with_name(name), recovery / name)
        atomic_write(directory / "rollback.py", '''import json
from pathlib import Path
import sys
directory = Path(__file__).resolve().parent
sys.path.insert(0, str(directory / "recovery"))
from stackctl.upgrade import Upgrade
record = json.loads((directory / "rollback.json").read_text())
Upgrade(Path(record["root"])).rollback(directory)
print("Previous source/services restored and verified.")
''')
        return directory

    def apply(self, source, expected, emit=print):
        with self.store.lock():
            plan = self.plan(source)
            if plan["token"] != expected:
                raise UpgradeError("Source, configuration or services changed after preview. Review the new plan.")
            if not plan["changed"] and not plan["services"]:
                emit("Already current; no changes or restarts.")
                return None
            self.backend.verify()
            directory = self.backup(plan)
            emit("Backup: " + str(directory))
            emit("Recovery command: python3 " + str(directory / "rollback.py"))
            self.store.write_json(self.journal, {"status": "applying", "backup": str(directory)})
            try:
                for name in plan["changed"]:
                    path = local_path(self.root, name)
                    mode = stat.S_IMODE(path.stat().st_mode) if path.exists() else (0o755 if name.endswith(".sh") else 0o644)
                    write_bytes(path, (directory / "release" / name).read_bytes(), mode)
                atomic_write(self.root / "RELEASE-MANIFEST.json", json.dumps({"base_commit": plan["release"].revision, "files": plan["release"].hashes}, indent=2))
                self.backend.recreate(plan["services"])
                self.backend.verify()
                if any(digest(local_path(self.root, name)) != h for name, h in plan["release"].hashes.items()):
                    raise UpgradeError("Source changed while the upgrade was running.")
                if {name: digest(self.root / name) for name in PROTECTED} != plan["protected"]:
                    raise UpgradeError("Deployment settings changed during the upgrade; inspect concurrent changes.")
                self.store.write_json(self.journal, {"status": "complete", "backup": str(directory)})
                emit("Source upgrade verified. Account provisioning was not run.")
                return directory
            except BaseException:
                emit("Upgrade failed; restoring source and affected services.")
                self._rollback(directory)
                raise UpgradeError("Upgrade failed; previous source/services restored and verified.") from None

    def _rollback(self, directory):
        record = json.loads((directory / "rollback.json").read_text(encoding="utf-8"))
        if record["root"] != str(self.root):
            raise UpgradeError("Backup belongs to another deployment.")
        self.store.write_json(self.journal, {"status": "rolling_back", "backup": str(directory)})
        try:
            for name in record["changed"]:
                path = local_path(self.root, source_name(name))
                if digest(path) not in (record["before"][name], record["after"][name]):
                    raise UpgradeError("Source was edited after the upgrade; refusing to overwrite it.")
                if digest(directory / "source" / name) != record["before"][name]:
                    raise UpgradeError("Rollback source backup was changed or damaged.")
            for name in [*record["changed"], "RELEASE-MANIFEST.json"]:
                path = local_path(self.root, name)
                saved = directory / "source" / name
                if saved.is_file():
                    write_bytes(path, saved.read_bytes(), record["modes"][name])
                elif path.is_file():
                    path.unlink()
            self.backend.recreate(record["services"])
            self.backend.verify()
            self.store.write_json(self.journal, {"status": "rolled_back", "backup": str(directory)})
        except BaseException:
            self.store.write_json(self.journal, {"status": "rollback_failed", "backup": str(directory)})
            raise UpgradeError("Rollback is incomplete. Preserve the backup and run stack rollback --backup PATH after fixing the cause.") from None

    def rollback(self, directory):
        directory = Path(directory).resolve()
        if not directory.is_relative_to((self.store.directory / "upgrades").resolve()):
            raise UpgradeError("Choose a backup under this deployment's .stack/upgrades directory.")
        with self.store.lock():
            latest = self.store.json(self.journal, {})
            if latest.get("backup") != str(directory) or latest.get("status") == "rolled_back":
                raise UpgradeError("Only the latest unrolled source upgrade can be rolled back.")
            self._rollback(directory)
