#!/usr/bin/env python3
"""Mint the Bearer token the master uses against this panel's API.

3x-ui v3.7+ has scoped API tokens (sources: release v3.7.0 notes,
"feat(api): scoped, optionally expiring API tokens (#6201)";
`internal/web/controller/setting.go:initRouter` on main;
`internal/web/service/panel/api_token.go:NormalizeScope/Create` on main):

  GET  /panel/api/setting/apiTokens            list (metadata only, no secret)
  POST /panel/api/setting/apiTokens/create     {name, scope, expiresAt} ->
                                               obj carries `token` plaintext
                                               exactly once
  POST /panel/api/setting/apiTokens/delete/:id {expectedScope}

Valid scopes are 'admin', 'monitor' and 'node-sync' (anything else fails
closed); expiresAt is Unix milliseconds, 0 means never. A node-sync token
reaches exactly the node-sync route allowlist (`api.go:nodeSyncScopeAllow`),
not the admin surface - which is why the node role uses it and never hands
out the panel password.

Idempotent: re-running keeps the existing enabled token with this name and
prints nothing secret (the plaintext is unrecoverable after creation).
Rotation is destructive and needs the explicit flag.

Run on the node (install.sh does this for ROLE=node):

    PANEL_USER=... PANEL_PASS=... python3 scripts/mint-node-token.py [--rotate]
"""
import json
import os
import sys
import urllib.error

from panel import ROOT, Panel, env_file

TOKEN_NAME = os.environ.get("NODE_TOKEN_NAME", "master-node-sync")
SCOPE = "node-sync"
ENV_KEY = "NODE_API_TOKEN"


def die(msg):
    sys.exit(f"mint-node-token: {msg}")


def list_tokens(p):
    """GET the token list. Raises SystemExit(2) if the panel has no such
    endpoint (older 3x-ui without scoped API tokens - needs v3.7+)."""
    try:
        raw = p.req("/panel/api/setting/apiTokens").read()
    except urllib.error.HTTPError as ex:
        if ex.code == 404:
            die("this panel has no /apiTokens endpoints - need 3x-ui v3.7+ "
                "with scoped API tokens ('node-sync'); refusing to guess")
        die(f"token list failed: HTTP {ex.code}")
    try:
        return json.loads(raw).get("obj") or []
    except ValueError:
        die("token list returned non-JSON")


def store_env_token(value):
    path = os.path.join(ROOT, ".env")
    key = ENV_KEY + "="
    lines = open(path, encoding="utf-8").read().split("\n")
    out, found = [], False
    for line in lines:
        if line.startswith(key):
            out.append(key + value)
            found = True
        else:
            out.append(line)
    if not found:
        out.append(key + value)
    with open(path, "w", newline="\n") as f:
        f.write("\n".join(out))
    os.chmod(path, 0o600)


def main():
    rotate = "--rotate" in sys.argv
    p = Panel(env_file()).login()

    rows = list_tokens(p)
    ours = [r for r in rows if r.get("name") == TOKEN_NAME]

    if rotate:
        for r in ours:
            p.post(f"/panel/api/setting/apiTokens/delete/{r['id']}",
                   {"expectedScope": SCOPE})
            print(f"  deleted token id={r['id']} (scope was {r.get('scope')})")
        ours = []

    live = [r for r in ours if r.get("enabled") and r.get("scope") == SCOPE]
    if live and not rotate:
        have = bool(env_file().get(ENV_KEY))
        print(f"  token '{TOKEN_NAME}' already exists "
              f"(id={live[0]['id']}, scope={SCOPE}, never expires) - keeping")
        if not have:
            print(f"  !! {ENV_KEY} is empty in .env but the plaintext is shown "
                  f"only at creation and cannot be read back - enter it "
                  f"manually or re-run with --rotate")
        return

    if ours and not rotate:
        die(f"token '{TOKEN_NAME}' exists but is "
            f"{'disabled' if not ours[0].get('enabled') else 'scope ' + str(ours[0].get('scope'))} "
            f"- enable it in the panel or re-run with --rotate")

    r = p.post("/panel/api/setting/apiTokens/create",
               {"name": TOKEN_NAME, "scope": SCOPE, "expiresAt": 0})
    obj = (r or {}).get("obj") or {}
    if not r.get("success") or not obj.get("token"):
        die(f"token not created: {r.get('msg')}")
    store_env_token(obj["token"])
    print(f"  minted token '{TOKEN_NAME}' (id={obj.get('id')}, scope={SCOPE}, "
          f"never expires) - secret stored in .env as {ENV_KEY} (mode 600)")


if __name__ == "__main__":
    main()
