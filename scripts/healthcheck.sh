#!/usr/bin/env bash
# Per-node health: TCP 443, UDP 443, certificate lifetime, master status.
# Exit code is non-zero on ANY problem, so cron/uptimerobot can just watch it.
#
#   PANEL_USER=... PANEL_PASS=... ./scripts/healthcheck.sh
#   (PANEL_USER/PASS come from the master's .env, parsed literally.)
#
# nodes.txt columns used: name (label), public-domain (data path). Panel-url
# and token are nodes.py's business (master-status step calls it).
#
# UDP semantics, stated honestly: QUIC answers nothing without a valid
# handshake, so all we can observe is ICMP port-unreachable surfacing as
# ECONNREFUSED on a connected socket. Refused = FAIL (nothing listens);
# timeout = unverified (listener or filtered are indistinguishable).
# Authenticated Xray transfers are checked separately below.
#
# Certificate: FAIL if expired or <14 days left, warn if <30.
# Master status: `nodes.py test <name>` (probe) must report online.
#
# Optional Telegram alert (both vars set or no alert):
#   TELEGRAM_BOT_TOKEN=... TELEGRAM_CHAT_ID=... ./scripts/healthcheck.sh
set -uo pipefail
ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$ROOT"

source "$ROOT/scripts/env.sh"
load_stack_env || exit 1

CERT_WARN_DAYS=30
CERT_FAIL_DAYS=14
fail=0
report=""

say() { printf '%-4s %-12s %s\n' "$1" "$2" "$3"; report="$report$1 $2 $3
"; }

if ! python3 -c 'import sys; sys.path.insert(0, "scripts"); from panel import xray_healthy; sys.exit(0 if xray_healthy() else 1)'; then
  say FAIL local "Xray process unavailable"; fail=1
fi

if [ ! -f "${NODES_FILE:-nodes.txt}" ]; then
  if [ "${ROLE:-standalone}" = master ]; then
    echo "FAIL: master has no nodes.txt"; exit 1
  fi
fi

node_records() {
  if [ -f "${NODES_FILE:-nodes.txt}" ]; then
    cat "${NODES_FILE:-nodes.txt}"
  else
    printf 'local - - %s\n' "${PUBLIC_DOMAIN:?set PUBLIC_DOMAIN}"
  fi
}

while IFS= read -r raw || [ -n "$raw" ]; do
  line=${raw%%#*}
  set -- $line
  [ "$#" -eq 0 ] && continue
  name=$1; domain=${4:-}
  [ -n "$domain" ] || { say FAIL "$name" "nodes.txt line has no public-domain"; fail=1; continue; }

  # ── TCP 443 ──
  if timeout 10 bash -c 'cat < /dev/null > /dev/tcp/$1/443' _ "$domain" 2>/dev/null; then
    say OK "$name" "tcp/443 open"
  else
    say FAIL "$name" "tcp/443 unreachable"; fail=1
  fi

  # ── UDP 443 (refused = fail, timeout = pass; see header) ──
  udp_out=$(python3 - "$domain" 2>&1 <<'PY'
import socket, sys
host = sys.argv[1]
s = socket.socket(socket.AF_INET, socket.SOCK_DGRAM)
s.settimeout(3)
try:
    s.connect((host, 443))
    for _ in range(2):
        try:
            s.send(b"\x00")
            s.recv(1)
        except socket.timeout:
            continue
    print("udp/443 no refusal")
except OSError as ex:
    print(f"udp/443 REFUSED ({ex})")
    sys.exit(1)
except Exception as ex:  # noqa: BLE001 - DNS etc: a failed check, not a crash
    print(f"udp/443 ERROR ({ex})")
    sys.exit(2)
PY
) && say WARN "$name" "$udp_out (unverified; protocol handshake required)" || { say FAIL "$name" "$udp_out"; fail=1; }

  # ── certificate lifetime on the data path ──
  end=$(echo | timeout 15 openssl s_client -connect "$domain:443" -servername "$domain" 2>/dev/null \
        | openssl x509 -noout -enddate 2>/dev/null | cut -d= -f2)
  if [ -z "$end" ]; then
    say FAIL "$name" "no certificate on $domain:443"; fail=1
  else
    days=$(( ($(date -d "$end" +%s) - $(date +%s)) / 86400 ))
    if [ "$days" -lt 0 ]; then
      say FAIL "$name" "certificate EXPIRED ($end)"; fail=1
    elif [ "$days" -lt "$CERT_FAIL_DAYS" ]; then
      say FAIL "$name" "certificate expires in ${days}d ($end)"; fail=1
    elif [ "$days" -lt "$CERT_WARN_DAYS" ]; then
      say WARN "$name" "certificate expires in ${days}d ($end)"
    else
      say OK "$name" "certificate ok (${days}d left)"
    fi
  fi

  # ── master status ──
  if [ "$name" != local ] && [ -n "${PANEL_USER:-}" ] && [ -n "${PANEL_PASS:-}" ]; then
    if timeout 60 python3 scripts/nodes.py test "$name" >/dev/null 2>&1; then
      say OK "$name" "master: online"
    else
      say FAIL "$name" "master: not online (see nodes.py test $name)"; fail=1
    fi
  elif [ "$name" != local ]; then
    say WARN "$name" "master status skipped (no PANEL_USER/PANEL_PASS)"
  fi
done < <(node_records)

if [ "${ROLE:-standalone}" != node ]; then
  if timeout 225 python3 -m stackctl.health; then
    say OK local "core, fallback HTTPS and published Xray lanes passed"
  else
    say FAIL local "authenticated transfer failed; compare another vantage point and target URL"; fail=1
  fi
else
  say WARN local "node lanes need authenticated probes from master/external host"
fi

echo "---"
if [ "$fail" = 0 ]; then
  echo "checked paths reachable; MTProto and AmneziaWG protocol health remain unverified"
else
  echo "PROBLEMS FOUND"
  if [ -n "${TELEGRAM_BOT_TOKEN:-}" ] && [ -n "${TELEGRAM_CHAT_ID:-}" ]; then
    curl -fsS -m 15 -o /dev/null \
      -d "chat_id=${TELEGRAM_CHAT_ID}" --data-urlencode "text=3xui healthcheck FAILED:
$report" \
      "https://api.telegram.org/bot${TELEGRAM_BOT_TOKEN}/sendMessage" \
      && echo "(telegram alert sent)" || echo "(telegram alert FAILED)"
  fi
fi
exit "$fail"
