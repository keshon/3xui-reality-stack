#!/usr/bin/env bash
# Acceptance checks for stage 3 (run on the master):
#   1. Reality privateKeys and shortIds differ across every reality lane
#      (local vless-reality + {node}-reality) - the UI clone copies them
#      verbatim, so this is the check that we did NOT clone.
#   2. serverNames / externalProxy / target point at each lane's own domain
#      and its own traefik:8443.
#   3. No PANEL_DOMAIN leaks into any subscription; every node domain shows
#      up in at least one subscription (the address clients dial).
#   4. Each node's fallback serves its own certificate (data-path coherence,
#      same probe as install.sh).
#
#   PANEL_USER=... PANEL_PASS=... ./scripts/verify.sh
#
# Exit non-zero on the first failed group. A live client handshake per node
# (one device, one Reality client each) stays manual - see docs/replace-node.md.
set -euo pipefail
ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$ROOT"

# .env values for the shell-side checks (no-source parse, as everywhere).
while IFS= read -r line || [ -n "$line" ]; do
  case "$line" in ''|'#'*) continue;; *'='*) ;; *) continue;; esac
  k=${line%%=*}; v=${line#*=}
  case "$v" in '"'*'"') v=${v#\"}; v=${v%\"};; "'"*"'") v=${v#'}; v=${v%'};; esac
  case "$k" in BASE_DOMAIN|PUBLIC_DOMAIN|PANEL_DOMAIN) export "$k=$v";; esac
done < .env

echo "== reality keys and addresses (panel API) =="
PANEL_USER="${PANEL_USER:?set PANEL_USER}" PANEL_PASS="${PANEL_PASS:?set PANEL_PASS}" \
python3 - <<'PY'
import base64
import json
import sys
import urllib.request

sys.path.insert(0, "scripts")
from panel import Panel, as_dict, container_ip, env_file, read_nodes

e = env_file()
try:
    nodes = read_nodes(env=e)
except SystemExit:
    nodes = []
expect = {"vless-reality": e["PUBLIC_DOMAIN"]}
for n in nodes:
    expect[f"{n['name']}-reality"] = n["domain"]

p = Panel(e).login()
have = {i["remark"]: i for i in p.inbounds()}
bad = 0
keys, shorts = {}, set()
for remark, dom in expect.items():
    ib = have.get(remark)
    if ib is None:
        print(f"  FAIL {remark}: inbound missing")
        bad += 1
        continue
    ss = as_dict(ib.get("streamSettings"))
    rs = ss.get("realitySettings") or {}
    ok = True
    if rs.get("serverNames") != [dom]:
        print(f"  FAIL {remark}: serverNames={rs.get('serverNames')} want [{dom}]")
        ok = False
    xp = ss.get("externalProxy") or [{}]
    if xp[0].get("dest") != dom:
        print(f"  FAIL {remark}: externalProxy dest={xp[0].get('dest')} want {dom}")
        ok = False
    if rs.get("target") != "traefik:8443":
        print(f"  FAIL {remark}: target={rs.get('target')} want traefik:8443")
        ok = False
    pk = rs.get("privateKey") or ""
    if not pk or pk in keys:
        print(f"  FAIL {remark}: privateKey empty or reused from {keys.get(pk)}")
        ok = False
    keys[pk] = remark
    for s in rs.get("shortIds") or []:
        if s in shorts:
            print(f"  FAIL {remark}: shortId {s} reused")
            ok = False
        shorts.add(s)
    print(f"  {'ok' if ok else 'FAIL'} {remark}: keys unique, domain {dom}")
    bad += not ok

print("== subscriptions: no PANEL_DOMAIN, all node domains present ==")


def fetch(sub_id):
    ip = container_ip()
    url = f"http://{ip}:2096{e['SUB_PATH'].rstrip('/')}/{sub_id}"
    req = urllib.request.Request(url, headers={"Host": e["PUBLIC_DOMAIN"]})
    with urllib.request.urlopen(req, timeout=20) as r:
        raw = r.read().decode()
    s = raw.strip().replace("-", "+").replace("_", "/")
    return base64.b64decode(s + "=" * (-len(s) % 4)).decode("utf-8", "replace")


rows = [(c["email"], c.get("subId") or "") for c in p.clients() if c.get("email")]
seen_domains, bad_sub = set(), 0
for email, sub in rows:
    if not sub:
        print(f"  FAIL {email}: no subId")
        bad_sub += 1
        continue
    try:
        body = fetch(sub)
    except Exception as ex:  # noqa: BLE001 - one bad sub must not hide the rest
        print(f"  FAIL {email}: unreadable subscription ({ex})")
        bad_sub += 1
        continue
    if e["PANEL_DOMAIN"] in body:
        print(f"  FAIL {email}: PANEL_DOMAIN leaks into the subscription")
        bad_sub += 1
    for n in nodes:
        if n["domain"] in body:
            seen_domains.add(n["domain"])
if not bad_sub:
    print(f"  ok {len(rows)} subscription(s): no PANEL_DOMAIN")
bad += bad_sub
for n in nodes:
    if n["domain"] not in seen_domains:
        print(f"  FAIL {n['domain']}: in no subscription (clients can't reach {n['name']})")
        bad += 1
    else:
        print(f"  ok {n['domain']}: advertised")
sys.exit(1 if bad else 0)
PY

echo "== fallback certificates per node =="
fail=0
check_cert() {  # $1 = domain
  subj=$(echo | openssl s_client -connect "$1:443" -servername "$1" 2>/dev/null \
         | openssl x509 -noout -subject 2>/dev/null || true)
  case "$subj" in
    *"$BASE_DOMAIN"*) echo "  ok $1: fallback serves your own certificate";;
    *) echo "  FAIL $1: unexpected certificate: ${subj:-none}"; fail=1;;
  esac
}
check_cert "$PUBLIC_DOMAIN"
while IFS= read -r raw || [ -n "$raw" ]; do
  line=${raw%%#*}
  set -- $line
  [ "$#" -eq 0 ] && continue
  check_cert "${4:?nodes.txt line without public-domain}"
done < nodes.txt
[ "$fail" = 0 ] || exit 1
echo "ALL CHECKS PASSED"
