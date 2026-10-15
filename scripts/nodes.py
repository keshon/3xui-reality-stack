#!/usr/bin/env python3
"""Manage 3x-ui nodes from the master panel. Read-only except where noted.

The node list lives in nodes.txt (see nodes.txt.example); this script only
ever talks to the MASTER panel API over the container network
(scripts/panel.py) - never to a node directly, and never to x-ui.db.

  python3 scripts/nodes.py list              registered nodes + health,
                                             cross-checked against nodes.txt
  python3 scripts/nodes.py add [name...]     register missing nodes
                                             (idempotent: identical re-run
                                             is a no-op)
  python3 scripts/nodes.py test [name...]    probe reachability; for a
                                             registered node refreshes its
                                             cached health, otherwise probes
                                             the nodes.txt details unsaved
  python3 scripts/nodes.py remove <name> --yes
                                             unregister (dry-run by default)

Run on the master:  PANEL_USER=... PANEL_PASS=... python3 scripts/nodes.py list

Sources for the request shape: NodeMutationRequest
(internal/web/controller/node.go, frontend NodeFormSchema) -
{name, remark, scheme, address, port, basePath, apiToken, enable,
allowPrivateAddress, tlsVerifyMode, pinnedCertSha256, inboundSyncMode,
inboundTags, outboundTag, id}; endpoints POST /panel/api/nodes/{add,
update/:id, del/:id, setEnable/:id, probe/:id, test} (useNodeMutations.ts).
"""
import sys
import urllib.parse

from panel import Panel, env_file, read_nodes

DEFAULT_PORTS = {"https": 443, "http": 80}


def split_url(url):
    """Split a panel base URL into a NodeMutationRequest connection part."""
    u = urllib.parse.urlparse(url if "://" in url else "https://" + url)
    if u.scheme not in ("http", "https"):
        raise SystemExit(f"bad panel url {url!r}: scheme must be http(s)")
    if not u.hostname:
        raise SystemExit(f"bad panel url {url!r}: no host")
    base = "/" + u.path.strip("/")
    return {
        "scheme": u.scheme,
        "address": u.hostname,
        "port": u.port or DEFAULT_PORTS[u.scheme],
        "basePath": base,
    }


def payload(entry):
    """Full NodeMutationRequest for registering this file entry."""
    p = {"id": 0, "name": entry["name"], "remark": "",
         "apiToken": entry["token"], "clearApiToken": False, "enable": True,
         "allowPrivateAddress": False, "tlsVerifyMode": "verify",
         "pinnedCertSha256": "", "inboundSyncMode": "all", "inboundTags": [],
         "outboundTag": ""}
    p.update(split_url(entry["url"]))
    return p


def same_endpoint(reg, entry):
    want = split_url(entry["url"])
    return all(str(reg.get(k, "")) == str(v) for k, v in want.items())


def fmt_status(n):
    return (f"{n.get('name'):12} {n.get('status', '?'):8} "
            f"{n.get('address', '')}:{n.get('port', '')} "
            f"lat={n.get('latencyMs', '-')}ms "
            f"xray={n.get('xrayVersion', '-') or '-'} "
            f"panel={n.get('panelVersion', '-') or '-'} "
            f"{(n.get('lastError') or '')[:60]}")


def main():
    args = [a for a in sys.argv[1:] if not a.startswith("--")]
    cmd = args[0] if args else "list"
    want = args[1:]
    yes = "--yes" in sys.argv
    if cmd not in ("list", "add", "test", "remove"):
        raise SystemExit(f"unknown command {cmd!r}: list|add|test|remove")

    env = env_file()
    entries = {e["name"]: e for e in read_nodes(env=env)}
    if want:
        unknown = [w for w in want if w not in entries]
        if unknown:
            raise SystemExit(f"not in nodes.txt: {', '.join(unknown)}")
        entries = {k: entries[k] for k in want}

    p = Panel(env).login()
    reg = {n["name"]: n for n in p.node_list()}

    if cmd == "list":
        for n in p.node_list():
            flag = "" if n["name"] in entries else "   !! not in nodes.txt"
            print(fmt_status(n) + flag)
        for name in entries:
            if name not in reg:
                print(f"{name:12} missing    (in nodes.txt, not registered - "
                      f"run: nodes.py add {name})")
        return

    if cmd == "add":
        bad = 0
        for name, entry in entries.items():
            if name in reg:
                if same_endpoint(reg[name], entry):
                    print(f"  {name:12} already registered - keeping")
                else:
                    print(f"  {name:12} CONFLICT: panel has "
                          f"{reg[name].get('scheme')}://{reg[name].get('address')}"
                          f":{reg[name].get('port')}{reg[name].get('basePath', '')} "
                          f"but nodes.txt says {entry['url']} - fix the file, "
                          f"or remove + re-add")
                    bad += 1
                continue
            r = p.post("/panel/api/nodes/add", payload(entry))
            obj = (r or {}).get("obj") or {}
            ok = bool(r.get("success"))
            bad += not ok
            print(f"  {name:12} {'registered' if ok else 'FAILED: ' + str(r.get('msg'))} "
                  f"status={obj.get('status', '-')} lat={obj.get('latencyMs', '-')}ms")
        sys.exit(1 if bad else 0)

    if cmd == "test":
        bad = 0
        for name, entry in entries.items():
            if name in reg:
                r = p.post(f"/panel/api/nodes/probe/{reg[name]['id']}")
                obj = (r or {}).get("obj") or {}
                ok = bool(r.get("success")) and obj.get("status") == "online"
                bad += not ok
                print(f"  {name:12} probe: status={obj.get('status', '?')} "
                      f"lat={obj.get('latencyMs', '-')}ms "
                      f"xray={obj.get('xrayState', '-') or '-'} "
                      f"{(obj.get('error') or r.get('msg') or '')[:80]}")
            else:
                r = p.post("/panel/api/nodes/test", payload(entry))
                obj = (r or {}).get("obj") or {}
                ok = bool(r.get("success")) and obj.get("status") == "online"
                bad += not ok
                print(f"  {name:12} test (unsaved): status={obj.get('status', '?')} "
                      f"lat={obj.get('latencyMs', '-')}ms "
                      f"{(obj.get('error') or r.get('msg') or '')[:80]}")
        sys.exit(1 if bad else 0)

    # cmd == "remove"
    name = want[0] if want else None
    if not name:
        raise SystemExit("usage: nodes.py remove <name> --yes")
    if name not in reg:
        print(f"  {name:12} not registered - nothing to do")
        return
    if not yes:
        print(f"  {name:12} would unregister node id={reg[name]['id']} "
              f"(node inbounds are NOT auto-migrated upstream) - re-run with --yes")
        return
    r = p.post(f"/panel/api/nodes/del/{reg[name]['id']}")
    if not r.get("success"):
        sys.exit(f"  {name:12} FAILED: {r.get('msg')}")
    print(f"  {name:12} unregistered (its inbounds stay in place - "
          f"delete them in the panel if the host is gone)")


if __name__ == "__main__":
    main()
