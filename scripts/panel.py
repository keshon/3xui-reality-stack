#!/usr/bin/env python3
"""Shared helpers for talking to the 3x-ui panel API.

Everything the other scripts learned the hard way lives here:

  * The v3.7 endpoints are under /panel/api/... and want JSON bodies.
    The v2 form endpoints (/panel/inbound/add) are gone and 404.
  * The API is CSRF-protected: a <meta name="csrf-token"> on any panel page,
    plus a session cookie. A plain POST gets 403.
  * `list` is a GET. Passing a body turns it into a POST, which 404s.
  * Inbound `settings`/`streamSettings` are JSON *strings* when you send them,
    but come back already parsed when you read them.
  * On a client object, `id` is the UUID as a string - NOT the numeric row id.
    Sending the row id writes that number into `uuid` and destroys the account.
  * `allowedIPs` is returned as a comma-joined string but must be sent as a
    list, so a client object cannot be round-tripped verbatim.
"""
import http.cookiejar
import json
import os
import re
import subprocess
import urllib.error
import urllib.parse
import urllib.request
import uuid as uuidlib

ROOT = os.path.dirname(os.path.dirname(os.path.abspath(__file__)))
ENV_PATH = os.path.join(ROOT, ".env")
DB_PATH = os.path.join(ROOT, "xui", "db", "x-ui.db")


def env_file(path=ENV_PATH):
    out = {}
    for line in open(path):
        line = line.strip()
        if line and not line.startswith("#") and "=" in line:
            k, v = line.split("=", 1)
            out[k] = v.strip('"').strip("'")
    return out


def users(env=None):
    env = env or env_file()
    return [x.strip() for x in env.get("USERS", "user1").split(",") if x.strip()]


def container_ip(name="3x-ui"):
    return subprocess.run(
        ["docker", "inspect", "-f",
         "{{range .NetworkSettings.Networks}}{{.IPAddress}}{{end}}", name],
        capture_output=True, text=True).stdout.strip()


def xray(*args):
    """Run the xray binary inside the 3x-ui container."""
    return subprocess.run(
        ["docker", "exec", "3x-ui", "/app/bin/xray-linux-amd64", *args],
        capture_output=True, text=True).stdout.strip()


def mtg(*args):
    return subprocess.run(
        ["docker", "exec", "3x-ui", "/app/bin/mtg-linux-amd64", *args],
        capture_output=True, text=True).stdout.strip()


def valid_uuid(s):
    try:
        uuidlib.UUID(str(s))
        return True
    except (ValueError, AttributeError, TypeError):
        return False


def normalize_client(c):
    """Coerce a client object from `get` into the shape `update` accepts."""
    c = dict(c)
    c["id"] = c.get("uuid") or str(uuidlib.uuid4())   # `id` IS the uuid
    a = c.get("allowedIPs")
    if isinstance(a, str):
        c["allowedIPs"] = [x.strip() for x in a.split(",") if x.strip()]
    return c


def as_dict(v):
    """Inbound sub-objects come back parsed from `list`, as strings from `get`."""
    return json.loads(v) if isinstance(v, str) else (v or {})


class Panel:
    def __init__(self, env=None):
        env = env or env_file()
        ip = container_ip()
        if not ip:
            raise SystemExit("3x-ui container is not running")
        self.base = f"http://{ip}:2053{env['PANEL_PATH'].rstrip('/')}"
        self.op = urllib.request.build_opener(
            urllib.request.HTTPCookieProcessor(http.cookiejar.CookieJar()))

    def _csrf(self):
        html = self.op.open(self.base + "/", timeout=20).read().decode("utf-8", "replace")
        m = re.search(r'name="csrf-token"\s+content="([^"]+)"', html)
        if not m:
            raise SystemExit("no csrf-token on the panel page")
        return m.group(1)

    def req(self, path, data=None, as_json=False):
        h = {"Referer": self.base + "/",
             "Origin": self.base.rsplit("/", 1)[0],
             "X-CSRF-TOKEN": self._csrf()}
        body = None
        if data is not None:
            body = (json.dumps(data).encode() if as_json
                    else urllib.parse.urlencode(data).encode())
            h["Content-Type"] = ("application/json" if as_json
                                 else "application/x-www-form-urlencoded")
        return self.op.open(
            urllib.request.Request(self.base + path, body, h), timeout=25)

    def post(self, path, body=None, as_json=True):
        try:
            return json.loads(self.req(path, body if body is not None else {},
                                       as_json).read())
        except urllib.error.HTTPError as ex:
            return {"success": False,
                    "msg": f"HTTP {ex.code}: {ex.read().decode()[:250]}"}

    def get(self, path):
        try:
            return json.loads(self.req(path).read())
        except urllib.error.HTTPError:
            return {"success": False, "obj": None}

    def login(self, user=None, password=None):
        user = user or os.environ.get("PANEL_USER")
        password = password or os.environ.get("PANEL_PASS")
        d = json.loads(self.req("/login", {"username": user,
                                           "password": password}).read())
        if not d.get("success"):
            raise SystemExit(f"panel login failed: {d}")
        return self

    # ── convenience ─────────────────────────────────────────────────────────
    def inbounds(self):
        return self.get("/panel/api/inbounds/list").get("obj") or []

    def inbound(self, remark):
        return next((i for i in self.inbounds() if i["remark"] == remark), None)

    def add_inbound(self, fields):
        return self.post("/panel/api/inbounds/add", fields)

    def update_inbound(self, inbound, **overrides):
        f = {k: inbound[k] for k in ("remark", "listen", "port", "protocol",
                                     "expiryTime", "up", "down", "total")}
        f["enable"] = inbound.get("enable", True)
        for k in ("settings", "streamSettings", "sniffing", "allocate"):
            v = inbound.get(k)
            f[k] = v if isinstance(v, str) else json.dumps(v or {})
        f.update(overrides)
        return self.post(f"/panel/api/inbounds/update/{inbound['id']}", f)

    def client(self, email):
        return self.get(f"/panel/api/clients/get/{email}").get("obj")

    def update_client(self, email, client):
        return self.post(f"/panel/api/clients/update/{email}", client)

    def attach(self, email, inbound_ids):
        return self.post(f"/panel/api/clients/{email}/attach",
                         {"inboundIds": inbound_ids})

    def settings(self):
        # POST, despite reading. (inbounds/list is the opposite - a GET that
        # 404s if you give it a body. The API is not consistent about this.)
        return self.post("/panel/api/setting/all").get("obj") or {}

    def update_settings(self, new):
        return self.post("/panel/api/setting/update", new)


def xray_healthy(tail=40):
    """True unless Xray is refusing to start.

    Worth checking after ANY inbound change: the panel's validation is weaker
    than Xray's runtime, it is a single Xray process, and one bad inbound takes
    every lane down with it.
    """
    r = subprocess.run(["docker", "logs", "--tail", str(tail), "3x-ui"],
                       capture_output=True, text=True)
    out = r.stdout + r.stderr
    return "Failed to start" not in out and "Failure in running xray-core" not in out
