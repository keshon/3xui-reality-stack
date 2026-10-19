#!/usr/bin/env python3
"""Idempotent API steps used by the journaled deployment runner."""
from pathlib import Path
import subprocess
import sys
import urllib.request

sys.path.insert(0, str(Path(__file__).resolve().parent))
from panel import Panel, env_file, users, read_nodes
from nodes import payload, same_endpoint
from stackctl.config import Store


def require(response, action):
    if not isinstance(response, dict) or not response.get("success"):
        # API response bodies may contain credentials. Keep errors structural.
        raise RuntimeError(f"{action}: panel rejected the request; inspect panel logs locally.")
    return response.get("obj")


def nodes(p, env):
    registered = {n["name"]: n for n in p.node_list()}
    for entry in read_nodes(env=env):
        old = registered.get(entry["name"])
        if old and not same_endpoint(old, entry):
            raise ValueError(f"Node {entry['name']} already exists at a different endpoint. Use the replacement runbook.")
        fields = payload(entry)
        if old:
            # Tokens are write-only: explicitly update, then probe. Do not
            # pretend endpoint equality proves that the token is current.
            fields["id"] = old["id"]
            require(p.post(f"/panel/api/nodes/update/{old['id']}", fields), "Update node " + entry["name"])
            node_id = old["id"]
        else:
            require(p.post("/panel/api/nodes/add", fields), "Add node " + entry["name"])
            node_id = next(n["id"] for n in p.node_list() if n["name"] == entry["name"])
        state = require(p.post(f"/panel/api/nodes/probe/{node_id}"), "Probe node " + entry["name"]) or {}
        if state.get("status") != "online":
            raise RuntimeError(f"Node {entry['name']} is not online. Fix connectivity and resume.")
        print(f"Node {entry['name']}: online")


def clients(p, env, store):
    # Creation is idempotent through read-before-write; an ambiguous network
    # failure stops the step, so resume reads the committed state first.
    revoke = store.json(store.journal_path, {}).get("revoke", [])
    if revoke and read_nodes(env=env):
        raise RuntimeError("Remote revocation cannot be verified; refusing partial revocation.")
    subprocess.run([sys.executable, "scripts/reconcile-clients.py", "--apply"], check=True)
    if "mtproto" in env.get("LANES", "").split(","):
        # New unified users need FakeTLS secrets fronting our public domain,
        # rather than the panel's default domain. Existing secrets are kept.
        subprocess.run([sys.executable, "scripts/add-mtproto.py"], check=True)
    known = set(users(env))
    current = {c["email"] for c in p.clients() if c.get("email")}
    for who in revoke:
        if who in known:
            raise ValueError("Refusing to revoke a user still present in the configuration.")
        # Only users explicitly removed from our previous managed revision.
        # Never prune unrelated panel users as a side effect.
        for email in sorted(current):
            if email == who or email.split("-", 1)[-1] == who:
                require(p.post(f"/panel/api/clients/del/{email}"), "Revoke " + email)
                print(f"Revoked managed account: {email}")
    remaining = {c["email"] for c in p.clients() if c.get("email")}
    if any(email == who or email.split("-", 1)[-1] == who for who in revoke for email in remaining):
        raise RuntimeError("Revocation is not confirmed on the master.")


def acceptance(p, env):
    if env.get("ROLE") == "node":
        if not env.get("NODE_API_TOKEN"):
            raise RuntimeError("Node token is missing.")
        url = f"https://{env['PANEL_DOMAIN']}:{env.get('MANAGEMENT_PORT') or '2053'}{env['PANEL_PATH'].rstrip('/')}/"
        with urllib.request.urlopen(url, timeout=20) as response:
            if response.status != 200:
                raise RuntimeError("Independent management HTTPS endpoint is not ready.")
        print("Panel login, token storage and independent management HTTPS verified. Register this node on the master to install traffic lanes.")
        return
    from importlib.util import spec_from_file_location, module_from_spec
    spec = spec_from_file_location("reconcile", Path(__file__).with_name("reconcile-clients.py"))
    module = module_from_spec(spec); spec.loader.exec_module(module)
    target, missing = module.target_lanes(p, env, [n["name"] for n in read_nodes(env=env)])
    if missing:
        raise RuntimeError("Missing required lanes: " + ", ".join(missing))
    for who in users(env):
        row = p.client(who)
        if not row or not row.get("client", {}).get("enable") or not set(target) <= set(row.get("inboundIds") or []):
            raise RuntimeError(f"User {who} is missing, disabled or missing protocol lanes.")
    # Checks actual published node domains, unique keys, certificates, and
    # subscriptions. A real client handshake remains a separate manual check.
    subprocess.run(["bash", "scripts/verify.sh"], check=True)


def main():
    env = env_file()
    p = Panel(env).login()
    mode = sys.argv[1]
    if mode == "nodes":
        nodes(p, env)
    elif mode == "clients":
        clients(p, env, Store(Path(__file__).resolve().parents[1]))
    elif mode == "acceptance":
        acceptance(p, env)
    else:
        raise ValueError("Unknown managed step")


if __name__ == "__main__":
    try:
        main()
    except (ValueError, RuntimeError, subprocess.CalledProcessError) as error:
        sys.exit(str(error))
