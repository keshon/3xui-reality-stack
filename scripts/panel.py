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
  * Nothing here reads or writes x-ui.db. The schema is 3x-ui's private
    business and has changed shape between releases; a write that bypasses the
    panel also bypasses the validation that catches a bad inbound before it
    becomes one that cannot start.
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


def env_file(path=ENV_PATH):
    """Parse .env into a dict. Missing file -> {}, so the helpers that only
    need users.txt still run before .env exists. install.sh checks for .env
    itself; nothing here should turn a missing file into a traceback."""
    out = {}
    if not os.path.exists(path):
        return out
    for line in open(path, encoding="utf-8"):
        line = line.strip()
        if line and not line.startswith("#") and "=" in line:
            k, v = line.split("=", 1)
            out[k] = v.strip('"').strip("'")
    return out


class BadUsername(ValueError):
    pass


# Client emails are namespaced per lane as "r-<who>", "t-<who>", "h-<who>", and
# consolidate-clients.py recovers <who> with split("-", 1)[-1]. A '-' inside the
# name itself would make that recover the wrong person, so it is rejected here
# rather than discovered later as a silently mis-merged account.
USERNAME_RE = re.compile(r"^[a-z0-9_]{1,24}$")


def parse_users(text):
    """Parse a username list. One name per line; '#' starts a comment; blank
    lines are ignored. Commas and spaces also separate, so a USERS= value can
    be pasted in verbatim. Returns lowercased names in first-seen order."""
    out, seen = [], set()
    for lineno, raw in enumerate(text.splitlines(), 1):
        for name in raw.split("#", 1)[0].replace(",", " ").split():
            n = name.lower()
            if not USERNAME_RE.match(n):
                raise BadUsername(
                    f"line {lineno}: {name!r} is not a usable username - use "
                    f"1-24 chars of a-z, 0-9 or '_' (no '-', it collides with "
                    f"the per-lane email prefix)")
            if n in seen:
                continue
            seen.add(n)
            out.append(n)
    return out


def users_file(env=None):
    """Path of the username list, or None if this deployment does not use one."""
    env = env or env_file()
    name = env.get("USERS_FILE", "users.txt").strip()
    if not name:
        return None
    path = name if os.path.isabs(name) else os.path.join(ROOT, name)
    return path if os.path.exists(path) else None


def users(env=None):
    """The people to provision.

    A users.txt file wins over USERS= in .env, so the list can be kept and
    edited as a plain list. install.sh writes the resolved list back into
    USERS= after reading it, so both stay in agreement.
    """
    env = env or env_file()
    path = users_file(env)
    if path:
        found = parse_users(open(path, encoding="utf-8").read())
        if found:
            return found
    return parse_users(env.get("USERS", "user1"))


def nodes_file(env=None):
    """Path of the node list (master side), or None if not configured."""
    env = env or env_file()
    name = env.get("NODES_FILE", "nodes.txt").strip()
    if not name:
        return None
    path = name if os.path.isabs(name) else os.path.join(ROOT, name)
    return path if os.path.exists(path) else None


def read_nodes(path=None, env=None):
    """Parse nodes.txt into [{name, url, token, domain}, ...].

    One node per line: `<name> <panel-url> <api-token> <public-domain>`;
    '#' starts a comment, blank lines are ignored. The panel URL carries
    the node's secret PANEL_PATH, so the file must stay mode 600.
    """
    env = env or env_file()
    path = path or nodes_file(env)
    if not path:
        raise SystemExit("no nodes.txt - copy nodes.txt.example and fill it in")
    out, seen = [], set()
    for lineno, raw in enumerate(open(path, encoding="utf-8"), 1):
        line = raw.split("#", 1)[0].split()
        if not line:
            continue
        if len(line) != 4:
            raise SystemExit(
                f"{path} line {lineno}: want "
                f"'<name> <panel-url> <api-token> <public-domain>'")
        name, url, token, domain = line
        if not re.match(r"^[a-z0-9_]{1,24}$", name):
            raise SystemExit(
                f"{path} line {lineno}: bad node name {name!r} - use "
                f"1-24 chars of a-z, 0-9 or '_' (it becomes the remark prefix)")
        if name in seen:
            raise SystemExit(f"{path} line {lineno}: duplicate node {name!r}")
        seen.add(name)
        out.append({"name": name, "url": url, "token": token,
                    "domain": domain})
    return out


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

    def clients(self):
        """Every client, as the panel sees them.

        Carries email, subId, uuid, password, auth and inboundIds, which is
        everything the scripts here used to read straight out of x-ui.db.
        Preferred over the database: the schema is 3x-ui's private business and
        has changed shape between releases, whereas this is the panel's own
        published view of it.
        """
        return self.get("/panel/api/clients/list").get("obj") or []

    def emails(self):
        return [c["email"] for c in self.clients() if c.get("email")]

    def client(self, email):
        return self.get(f"/panel/api/clients/get/{email}").get("obj")

    def update_client(self, email, client):
        return self.post(f"/panel/api/clients/update/{email}", client)

    def attach(self, email, inbound_ids):
        return self.post(f"/panel/api/clients/{email}/attach",
                         {"inboundIds": inbound_ids})

    def add_client(self, client, inbound_ids):
        """Create one unified client attached to the given inbounds.

        Sources: ClientController.create binds service.ClientCreatePayload
        {client, inboundIds} (client.go); Create fills protocol credentials
        and mints subId when empty, and requires at least one inbound
        (client_crud.go). Minimal {email} is enough - the rest is defaulted.
        """
        return self.post("/panel/api/clients/add",
                         {"client": client, "inboundIds": inbound_ids})

    def node_list(self):
        return self.get("/panel/api/nodes/list").get("obj") or []

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
