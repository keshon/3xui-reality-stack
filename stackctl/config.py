"""One versioned, literal configuration; legacy files are deployment outputs."""
from __future__ import annotations

from contextlib import contextmanager
from copy import deepcopy
import hashlib
import ipaddress
import json
import math
import os
from pathlib import Path
import re
import secrets
from urllib.parse import urlsplit, urlunsplit

from .envio import atomic_write, read_env, render_env

NAME = re.compile(r"^[a-z0-9_]{1,24}$")
DOMAIN = re.compile(r"^(?=.{1,253}$)(?:[a-z0-9](?:[a-z0-9-]{0,61}[a-z0-9])?\.)+[a-z]{2,63}$")
PATH_KEYS = ("PANEL_PATH", "SUB_PATH", "JSON_PATH", "TROJAN_PATH")
SECRET_KEYS = {"PANEL_PASS", "PANEL_USER", "CF_DNS_API_TOKEN", "NODE_API_TOKEN", "TELEGRAM_BOT_TOKEN", *PATH_KEYS[:3]}
LANES = ("vless-reality", "trojan-httpupgrade", "hysteria2", "mtproto", "amneziawg")


def integer(value, label):
    try:
        return int(value)
    except (TypeError, ValueError):
        raise ValueError(f"{label}: enter a whole number.") from None


def number(value, label):
    try:
        return float(value)
    except (TypeError, ValueError):
        raise ValueError(f"{label}: enter a number.") from None


def path_prefix(value: str) -> str:
    value = value.strip()
    if not value:
        return ""
    value = "/" + value.strip("/")
    if value == "/" or not re.fullmatch(r"/(?:[A-Za-z0-9._~-]+/)*[A-Za-z0-9._~-]+", value):
        raise ValueError("Use a path such as /panel-abc; no URL, spaces, query or fragment.")
    if any(part in (".", "..") for part in value.split("/")):
        raise ValueError("Paths cannot contain . or .. segments.")
    return value


def domain(value: str) -> str:
    value = value.strip().lower().rstrip(".")
    if not DOMAIN.fullmatch(value):
        raise ValueError("Use a DNS hostname, such as vpn.example.com (no https:// or path).")
    return value


def node_url(value: str) -> str:
    value = value.strip()
    if "://" not in value:
        value = "https://" + value
    parsed = urlsplit(value)
    if parsed.scheme != "https" or not parsed.hostname or parsed.username or parsed.password or parsed.query or parsed.fragment:
        raise ValueError("Node panel URL must be HTTPS, without credentials, query or fragment.")
    host = domain(parsed.hostname)
    port = parsed.port
    if port is not None and not 1 <= port <= 65535:
        raise ValueError("Invalid node panel port.")
    path = path_prefix(parsed.path)
    if not path:
        raise ValueError("Include the node's panel path in its URL.")
    return urlunsplit(("https", host + (f":{port}" if port and port != 443 else ""), path, "", ""))


def fingerprint(value) -> str:
    return hashlib.sha256(json.dumps(value, sort_keys=True, ensure_ascii=False).encode()).hexdigest()


def normalize(config: dict, *, ready: bool = True) -> dict:
    c = deepcopy(config)
    if c.get("schema_version") != 1 or not isinstance(c.get("env"), dict):
        raise ValueError("Unsupported configuration schema.")
    env = c["env"]
    # render_env also checks key names, types and embedded newlines.
    render_env(env)
    for key in PATH_KEYS:
        env[key] = path_prefix(env.get(key, ""))
    for key in ("BASE_DOMAIN", "PUBLIC_DOMAIN", "PANEL_DOMAIN"):
        if env.get(key):
            env[key] = domain(env[key])
        elif ready:
            raise ValueError(f"{key} is required.")
    role = env.setdefault("ROLE", "standalone")
    if role not in ("standalone", "master", "node"):
        raise ValueError("ROLE must be standalone, master or node.")
    env["MANAGEMENT_BIND"] = env.get("MANAGEMENT_BIND") or ("0.0.0.0" if role == "node" else "127.0.0.1")
    ipaddress.IPv4Address(env["MANAGEMENT_BIND"])
    env["MANAGEMENT_PORT"] = str(integer(env.get("MANAGEMENT_PORT") or 2053, "Management HTTPS port"))
    if not 1 <= int(env["MANAGEMENT_PORT"]) <= 65535:
        raise ValueError("Management HTTPS port must be between 1 and 65535.")
    if role == "node" and env["MANAGEMENT_BIND"].startswith("127."):
        raise ValueError("A traffic node needs a management bind address reachable by the master.")
    if env.get("ACME_MODE", "dns") not in ("dns", "http"):
        raise ValueError("ACME_MODE must be dns or http.")
    if env.get("REALITY_FINGERPRINT", "chrome") not in {"chrome", "firefox", "safari", "ios", "android", "edge", "360", "qq", "random", "randomized"}:
        raise ValueError("Unknown Reality fingerprint.")
    for key, default, low, high in (("MTPROTO_PORT", "8443", 1, 65535), ("AWG_PORT", "28844", 1, 65535), ("AWG_MTU", "1280", 576, 9000)):
        env[key] = str(integer(env.get(key) or default, key))
        if not low <= int(env[key]) <= high:
            raise ValueError(f"{key} must be between {low} and {high}.")
    if int(env["MTPROTO_PORT"]) in (80, 443) or int(env["AWG_PORT"]) == 443:
        raise ValueError("MTProto / AmneziaWG ports overlap the stack's reserved ports.")
    if int(env["MANAGEMENT_PORT"]) in (80, 443, int(env["MTPROTO_PORT"])):
        raise ValueError("Management HTTPS port overlaps an existing TCP service.")
    for key in ("USER_LIMIT_GB", "USER_EXPIRY_DAYS"):
        parsed_number = number(env.get(key) or 0, key)
        if not math.isfinite(parsed_number) or parsed_number < 0:
            raise ValueError(f"{key} must be a finite non-negative number.")
    ipaddress.ip_network(env.get("PROXY_SUBNET", "172.28.0.0/16"), strict=True)
    ipaddress.IPv4Address(env.get("AWG_SUBNET", "10.8.1.0"))
    lanes = [s.strip() for s in env.get("LANES", ",".join(LANES)).split(",") if s.strip()]
    # The current provisioner creates the three core lanes together.
    if not set(LANES[:3]) <= set(lanes) or set(lanes) - set(LANES):
        raise ValueError("Keep the three core lanes enabled; MTProto and AmneziaWG are optional.")
    env["LANES"] = ",".join(dict.fromkeys(lanes))
    people = c.get("users", [])
    if not isinstance(people, list) or any(not isinstance(s, str) or not NAME.fullmatch(s) for s in people) or len(set(people)) != len(people):
        raise ValueError("Users must be unique names: 1-24 lowercase letters, digits or underscore.")
    nodes = c.get("nodes", [])
    if not isinstance(nodes, list):
        raise ValueError("Nodes must be a list.")
    seen, endpoints, domains = set(), set(), set()
    for node in nodes:
        if not isinstance(node, dict) or set(node) != {"name", "url", "token", "domain"} or any(not isinstance(v, str) for v in node.values()):
            raise ValueError("Each node needs name, URL, token and public domain.")
        if not NAME.fullmatch(node["name"]) or node["name"] in seen:
            raise ValueError("Node names must be unique: 1-24 lowercase letters, digits or underscore.")
        node["url"] = node_url(node["url"])
        node["domain"] = domain(node["domain"])
        if not node["token"] or any(ch.isspace() or ch == "#" for ch in node["token"]):
            raise ValueError("Node API token must be a non-empty single token.")
        if node["url"] in endpoints or node["domain"] in domains or node["domain"] == env.get("PUBLIC_DOMAIN"):
            raise ValueError("Each node needs a distinct endpoint and public hostname.")
        if node["domain"] == urlsplit(node["url"]).hostname:
            raise ValueError("Node public and admin hostnames must differ.")
        seen.add(node["name"]); endpoints.add(node["url"]); domains.add(node["domain"])
    if role != "master" and nodes:
        raise ValueError("Only the master role can have managed nodes.")
    if nodes and env.get("ACME_MODE", "dns") != "dns":
        raise ValueError("Managed nodes require the shared DNS-01 wildcard certificate convention.")
    if role == "node" and people:
        raise ValueError("Users belong on the master, not a traffic node.")
    if ready:
        if env["PUBLIC_DOMAIN"] == env["PANEL_DOMAIN"]:
            raise ValueError("Public and admin hostnames must differ.")
        if env.get("ACME_MODE", "dns") == "dns":
            if not env.get("CF_DNS_API_TOKEN"):
                raise ValueError("Cloudflare DNS token is required for DNS-01.")
            base = env["BASE_DOMAIN"]
            for host in [env["PUBLIC_DOMAIN"], env["PANEL_DOMAIN"], *[n["domain"] for n in nodes]]:
                if not host.endswith("." + base) or host.count(".") != base.count(".") + 1:
                    raise ValueError("DNS mode requires hostnames directly under BASE_DOMAIN (wildcard coverage).")
        if not re.fullmatch(r"[^\s@]+@[^\s@]+\.[^\s@]+", env.get("LETSENCRYPT_EMAIL", "")):
            raise ValueError("Enter a valid ACME email address.")
        for key in PATH_KEYS[:3]:
            if not env[key]:
                env[key] = "/" + {"PANEL_PATH": "panel", "SUB_PATH": "sub", "JSON_PATH": "json"}[key] + "-" + secrets.token_hex(6)
        if not env["TROJAN_PATH"]:
            env["TROJAN_PATH"] = "/assets/stream"
        paths = [env[k] for k in PATH_KEYS]
        if any(a == b or a.startswith(b + "/") or b.startswith(a + "/") for i, a in enumerate(paths) for b in paths[i + 1:]):
            raise ValueError("Panel, subscription, JSON and Trojan paths must not overlap.")
        env["PANEL_USER"] = env.get("PANEL_USER") or "adm" + secrets.token_hex(3)
        env["PANEL_PASS"] = env.get("PANEL_PASS") or secrets.token_urlsafe(24)
        env["CERT_BASENAME"] = env.get("CERT_BASENAME") or (env["BASE_DOMAIN"] if env.get("ACME_MODE", "dns") == "dns" else env["PUBLIC_DOMAIN"])
        domain(env["CERT_BASENAME"])
        if role != "node" and not people:
            raise ValueError("Keep at least one user. Revoking everyone requires a separate explicit operation.")
    # Generated files have fixed locations; custom paths are imported, not perpetuated.
    env["USERS_FILE"] = "users.txt"
    env["NODES_FILE"] = "nodes.txt"
    env["USERS"] = ",".join(people)
    return c


class Store:
    def __init__(self, root: Path):
        self.root = Path(root).resolve()
        self.directory = self.root / ".stack"
        self.config_path = self.directory / "config.json"
        self.journal_path = self.directory / "operation.json"
        self.applied_path = self.directory / "applied.json"

    @contextmanager
    def lock(self):
        self.directory.mkdir(mode=0o700, parents=True, exist_ok=True)
        # Unbuffered writes must fail inside the lock error handler, rather
        # than being retried by close() after another process denied access.
        with open(self.directory / "lock", "a+b", buffering=0) as stream:
            try:
                if os.name == "nt":
                    import msvcrt
                    stream.seek(0); stream.write(b"0"); stream.flush(); stream.seek(0)
                    msvcrt.locking(stream.fileno(), msvcrt.LK_NBLCK, 1)
                else:
                    import fcntl
                    fcntl.flock(stream, fcntl.LOCK_EX | fcntl.LOCK_NB)
            except OSError:
                raise ValueError("Another stack process is saving or applying. Wait for it to finish.") from None
            try:
                # Go and Python share the OS lock. Between Go invocations its
                # journal also prevents Python from taking over partial state.
                if (self.directory / "go-initialization.json").exists() and not self.applied_path.exists():
                    raise ValueError("A Go initialization draft is not deployed. Continue with the Go controller.")
                go_marker = self.directory / "go-operation.json"
                if go_marker.exists():
                    try:
                        go_state = self.json(go_marker)
                    except (OSError, ValueError):
                        raise ValueError("Cannot read Go operation state; preserve it for recovery.") from None
                    if not isinstance(go_state, dict) or go_state.get("version") != 1 or go_state.get("status") not in ("complete", "cancelled"):
                        raise ValueError("A Go controller operation needs recovery. Resume with its original adapter.")
                yield stream.fileno()
            finally:
                if os.name == "nt":
                    stream.seek(0); msvcrt.locking(stream.fileno(), msvcrt.LK_UNLCK, 1)
                else:
                    fcntl.flock(stream, fcntl.LOCK_UN)

    def json(self, path: Path, default=None):
        return json.loads(path.read_text(encoding="utf-8")) if path.exists() else default

    def write_json(self, path: Path, value):
        atomic_write(path, json.dumps(value, ensure_ascii=False, indent=2) + "\n")

    def load(self) -> dict:
        if self.config_path.exists():
            return self.json(self.config_path)
        env = read_env(self.root / ".env.example")
        live = read_env(self.root / ".env")
        env.update(live)
        if not live:
            for key in ("BASE_DOMAIN", "PUBLIC_DOMAIN", "PANEL_DOMAIN", "CERT_BASENAME", "LETSENCRYPT_EMAIL"):
                env[key] = ""
            env["USERS"] = "user1"
        users_path = self.root / env.get("USERS_FILE", "users.txt") if env.get("USERS_FILE", "users.txt") else None
        text = users_path.read_text(encoding="utf-8-sig") if users_path and users_path.is_file() else env.get("USERS", "")
        people = list(dict.fromkeys(word.lower() for line in text.splitlines() for word in line.split("#", 1)[0].replace(",", " ").split()))
        node_path = self.root / (env.get("NODES_FILE") or "nodes.txt")
        nodes = []
        if node_path.is_file():
            for number, line in enumerate(node_path.read_text(encoding="utf-8-sig").splitlines(), 1):
                parts = line.split("#", 1)[0].split()
                if parts and len(parts) != 4:
                    raise ValueError(f"nodes.txt line {number}: expected name URL token public-domain.")
                if parts:
                    nodes.append(dict(zip(("name", "url", "token", "domain"), parts)))
        if env.get("ROLE") == "node":
            people = []
        return {"schema_version": 1, "env": env, "users": people, "nodes": nodes}

    def save(self, config: dict, expected: str | None = None) -> dict:
        config = normalize(config)
        with self.lock():
            pending = self.json(self.journal_path, {})
            if pending and pending.get("status") not in ("complete", "editable"):
                raise ValueError("An operation is unfinished. Resume it, or use 'stack recover' to correct its configuration.")
            if expected is not None and fingerprint(self.load()) != expected:
                raise ValueError("Configuration changed in another session. Reopen the editor.")
            from .engine import Engine
            Engine(self).plan(candidate=config)
            # Preserve the original inputs exactly once for migration recovery.
            if not self.config_path.exists():
                self.preserve_inputs()
            self.write_json(self.config_path, config)
        return config

    def preserve_inputs(self):
        for name in (".env", "users.txt", "nodes.txt"):
            original = self.root / name
            saved = self.directory / "imported" / name
            if original.is_file() and not saved.exists():
                atomic_write(saved, original.read_text(encoding="utf-8-sig"))

    def outputs(self, config: dict) -> dict[str, str]:
        return {
            ".env": render_env(config["env"]),
            "users.txt": "".join(s + "\n" for s in config["users"]),
            "nodes.txt": "".join(" ".join(n[k] for k in ("name", "url", "token", "domain")) + "\n" for n in config["nodes"]),
        }

    def render(self, config: dict):
        for name, text in self.outputs(config).items():
            atomic_write(self.root / name, text)

    def output_hashes(self) -> dict:
        return {name: hashlib.sha256((self.root / name).read_bytes()).hexdigest() if (self.root / name).exists() else None for name in (".env", "users.txt", "nodes.txt")}
