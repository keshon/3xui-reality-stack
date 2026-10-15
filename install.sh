#!/usr/bin/env bash
# One-shot installer. Idempotent: safe to re-run after editing .env.
#
#   cp .env.example .env && $EDITOR .env && sudo ./install.sh
#
# Brings up the whole stack, issues a wildcard certificate, provisions every
# lane, and collapses the accounts into one client per person. Prints the
# subscription URLs at the end.

set -euo pipefail
ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
cd "$ROOT"

say()  { printf '\n\033[1m== %s\033[0m\n' "$1"; }
ok()   { printf '   \033[32mok\033[0m   %s\n' "$1"; }
warn() { printf '   \033[33mwarn\033[0m %s\n' "$1"; }
die()  { printf '\n\033[31mfatal\033[0m %s\n' "$1" >&2; exit 1; }

[ "$(id -u)" = 0 ] || die "run as root (docker, sysctl and ufw all need it)"
[ -f .env ] || die "no .env - copy .env.example to .env and fill it in"

# Parse .env WITHOUT sourcing it. Sourcing executes the file as shell, so any
# value containing '$' - an apr1 password hash, for instance - gets expanded as
# a variable and either mangles the value or aborts under `set -u`.
load_env() {
  while IFS= read -r line || [ -n "$line" ]; do
    case "$line" in ''|'#'*) continue;; *'='*) ;; *) continue;; esac
    local k="${line%%=*}" v="${line#*=}"
    case "$v" in '"'*'"') v="${v#\"}"; v="${v%\"}";; "'"*"'") v="${v#\'}"; v="${v%\'}";; esac
    export "$k=$v"
  done < .env
}
set_env() {  # set_env KEY VALUE - update .env in place
  local k=$1 v=$2
  if grep -q "^${k}=" .env; then
    python3 - "$k" "$v" <<'PY'
import sys
k, v = sys.argv[1], sys.argv[2]
lines = open(".env").read().split("\n")
out = [f"{k}={v}" if l.startswith(k + "=") else l for l in lines]
open(".env", "w", newline="\n").write("\n".join(out))
PY
  else
    printf '%s=%s\n' "$k" "$v" >> .env
  fi
  export "$k=$v"
}

load_env
: "${BASE_DOMAIN:?set BASE_DOMAIN in .env}"
: "${PUBLIC_DOMAIN:?set PUBLIC_DOMAIN in .env}"
: "${PANEL_DOMAIN:?set PANEL_DOMAIN in .env}"
: "${LETSENCRYPT_EMAIL:?set LETSENCRYPT_EMAIL in .env}"
[ "${ACME_MODE:-dns}" != dns ] || [ -n "${CF_DNS_API_TOKEN:-}" ] \
  || die "ACME_MODE=dns needs CF_DNS_API_TOKEN (or set ACME_MODE=http)"

ROLE="${ROLE:-standalone}"
case "$ROLE" in
  standalone|master|node) ;;
  *) die "ROLE='$ROLE' is not usable - set standalone, master or node in .env";;
esac
set_env ROLE "$ROLE"
say "role: $ROLE"

# ── 1. fill in anything left blank ──────────────────────────────────────────
say "secrets"
NEW_CREDS=""
[ -n "${PANEL_PATH:-}" ] || { set_env PANEL_PATH "/panel-$(openssl rand -hex 6)"; ok "generated PANEL_PATH"; }
[ -n "${SUB_PATH:-}"   ] || { set_env SUB_PATH   "/sub-$(openssl rand -hex 6)";   ok "generated SUB_PATH"; }
[ -n "${JSON_PATH:-}"  ] || { set_env JSON_PATH  "/json-$(openssl rand -hex 6)";  ok "generated JSON_PATH"; }
[ -n "${PANEL_USER:-}" ] || { set_env PANEL_USER "adm$(openssl rand -hex 3)"; NEW_CREDS=1; }
if [ -z "${PANEL_PASS:-}" ]; then set_env PANEL_PASS "$(openssl rand -base64 15)"; NEW_CREDS=1; fi
: "${CERT_BASENAME:=$BASE_DOMAIN}"; set_env CERT_BASENAME "$CERT_BASENAME"
: "${PROXY_SUBNET:=172.28.0.0/16}"; set_env PROXY_SUBNET "$PROXY_SUBNET"
: "${MTPROTO_PORT:=8443}";          set_env MTPROTO_PORT "$MTPROTO_PORT"
: "${AWG_PORT:=51820}";             set_env AWG_PORT "$AWG_PORT"
# .env holds the Cloudflare token, panel credentials and (on a node) the API
# token - it is never committed (.gitignore) and stays owner-only.
chmod 600 .env

# ── 1b. the account list ────────────────────────────────────────────────────
# users.txt is the editable input, USERS= in .env is what every script reads.
# Resolve both into one answer HERE, so an unusable name fails now rather than
# half way through provisioning with some lanes already created.
say "users"
if [ "$ROLE" = node ]; then
  warn "node role: no local users - users.txt lives on the master, clients"
  warn "arrive from there (stages 3-4), so nothing is resolved here"
else
  USERS="$(python3 scripts/resolve-users.py)" || die "fix the account list, then re-run"
  set_env USERS "$USERS"
  python3 scripts/resolve-users.py --list | sed 's/^/   /'
fi

# ── 2. DNS must resolve here before ACME will work ──────────────────────────
say "dns"
MYIP="$(curl -fsS -m 10 https://api.ipify.org 2>/dev/null || hostname -I | awk '{print $1}')"
for h in "$PUBLIC_DOMAIN" "$PANEL_DOMAIN"; do
  got="$(getent ahostsv4 "$h" 2>/dev/null | awk '{print $1; exit}')"
  if [ "$got" = "$MYIP" ]; then ok "$h -> $got"
  elif [ -z "$got" ];      then die "$h does not resolve. Add an A record to $MYIP first."
  else warn "$h resolves to $got, not $MYIP - ACME may fail"; fi
done

# ── 3. host packages and kernel tuning ──────────────────────────────────────
say "host"
if ! command -v docker >/dev/null 2>&1; then
  curl -fsSL https://get.docker.com | sh >/dev/null 2>&1 || die "docker install failed"
fi
ok "docker $(docker --version | awk '{print $3}' | tr -d ,)"
# openssl is the only host binary the scripts shell out to: secrets here, and
# the AmneziaWG Curve25519 keypair in add-amneziawg.py. wireguard-tools is
# deliberately NOT required - no kernel module or interface is involved in
# generating a key, and openssl produces byte-identical ones.
if ! command -v openssl >/dev/null 2>&1; then
  apt-get -qq update >/dev/null 2>&1 || true
  # Show why, if it fails. A half-configured dpkg is a common cause and is
  # invisible when the output goes to /dev/null.
  apt-get -qq -y install openssl 2>&1 | tail -3 | sed 's/^/        /'     || warn "could not install openssl"
fi
command -v openssl >/dev/null || die "openssl is required and could not be installed"
bash scripts/host-tune.sh >/dev/null 2>&1 || warn "host-tune.sh reported a problem"
ok "congestion control: $(sysctl -n net.ipv4.tcp_congestion_control)"

if command -v ufw >/dev/null 2>&1; then
  # Find the port sshd is ACTUALLY on. Enabling ufw with a hardcoded 22/tcp on
  # a host whose sshd moved locks you out of it, and the next thing this script
  # does is restart the networking stack.
  SSH_PORTS="$(ss -lntp 2>/dev/null | awk '/sshd/ {n=split($4,a,":"); print a[n]}' | sort -un)"
  SSH_PORTS="${SSH_PORTS:-$(awk '/^[[:space:]]*Port[[:space:]]+[0-9]+/{print $2}' /etc/ssh/sshd_config /etc/ssh/sshd_config.d/*.conf 2>/dev/null | sort -un)}"
  SSH_PORTS="${SSH_PORTS:-22}"
  for p in $SSH_PORTS; do ufw allow "${p}/tcp" >/dev/null 2>&1 || true; done
  for p in 80/tcp 443/tcp 443/udp "${MTPROTO_PORT}/tcp" "${AWG_PORT}/udp"; do ufw allow "$p" >/dev/null 2>&1 || true; done
  ufw --force enable >/dev/null 2>&1 || true
  ok "ufw: ssh $(echo $SSH_PORTS | tr '
' ' '), 80, 443/tcp, 443/udp, ${MTPROTO_PORT}/tcp, ${AWG_PORT}/udp"
fi
warn "if your provider has its own firewall, open the same ports there - 443/udp especially"

# ── 4. bring the stack up ───────────────────────────────────────────────────
say "stack"
# The 'proxy' network usually already exists on a host that was running
# something behind a reverse proxy. Its subnet is exactly what Traefik trusts
# for PROXY protocol v2, so take the real value rather than leaving a guess in
# .env: if the two disagree, Traefik rejects the PROXY header Xray prepends and
# EVERY fallback request dies - the decoy, the panel, and any other site here.
EXISTING="$(docker network inspect proxy -f '{{range .IPAM.Config}}{{.Subnet}}{{end}}' 2>/dev/null || true)"
if [ -n "$EXISTING" ] && [ "$EXISTING" != "$PROXY_SUBNET" ]; then
  set_env PROXY_SUBNET "$EXISTING"
  ok "adopted the existing 'proxy' subnet: $EXISTING"
fi
./bootstrap.sh

say "certificate"
for i in $(seq 1 60); do
  [ -f "shared/certs/certs/${CERT_BASENAME}.crt" ] && break
  sleep 5
done
[ -f "shared/certs/certs/${CERT_BASENAME}.crt" ] \
  || die "no certificate after 5 min. Check: docker logs traefik; tail traefik/data/logs/traefik.log"
ok "$(openssl x509 -in "shared/certs/certs/${CERT_BASENAME}.crt" -noout -subject -ext subjectAltName 2>/dev/null | tr '\n' ' ' | cut -c1-100)"

# ── 5. panel: base path must match what Traefik forwards ────────────────────
say "panel"
docker exec 3x-ui /app/x-ui setting -webBasePath "${PANEL_PATH}/" >/dev/null 2>&1
docker exec 3x-ui /app/x-ui setting -username "$PANEL_USER" -password "$PANEL_PASS" >/dev/null 2>&1
docker restart 3x-ui >/dev/null; sleep 10
ok "panel at https://${PANEL_DOMAIN}${PANEL_PATH}/"

# subscription URLs must be the public https ones, not the panel's internal
# host:2096 default. A node serves no subscriptions at all - the master does.
if [ "$ROLE" = node ]; then
  warn "node role: skipping subscription settings - the master serves them"
else
  python3 scripts/configure-subscription.py || warn "subscription settings not applied"
fi

# The node role reaches its panel only through a node-sync-scoped Bearer
# token (scripts/mint-node-token.py): exactly the node-sync allowlist, never
# the admin surface. The panel password stays a local bootstrap secret.
if [ "$ROLE" = node ]; then
  PANEL_USER="$PANEL_USER" PANEL_PASS="$PANEL_PASS" \
    python3 scripts/mint-node-token.py || die "node API token not minted - the master cannot manage this node"
fi

# ── 6. lanes and accounts ───────────────────────────────────────────────────
# A node gets no local lanes: the master pushes them onto it (stage 3), with
# per-node Reality keys. Provisioning them here would bake in a second copy
# of this host's keys and collide with the sync.
say "lanes"
if [ "$ROLE" = node ]; then
  warn "node role: no local lanes - the master pushes them (stage 3)"
else
  KEYS="$(docker exec 3x-ui /app/bin/xray-linux-amd64 x25519)"
  REALITY_PRIV="$(echo "$KEYS" | awk -F': *' '/[Pp]rivate/{print $2; exit}')"
  REALITY_PUB="$(echo "$KEYS"  | awk -F': *' '/Password|[Pp]ublic/{print $2; exit}')"
  [ -n "$REALITY_PRIV" ] && [ -n "$REALITY_PUB" ] || die "could not generate a Reality keypair"

  PANEL_USER="$PANEL_USER" PANEL_PASS="$PANEL_PASS" \
    REALITY_PRIV="$REALITY_PRIV" REALITY_PUB="$REALITY_PUB" \
    python3 scripts/provision-inbounds.py

  PANEL_USER="$PANEL_USER" PANEL_PASS="$PANEL_PASS" \
    python3 scripts/add-mtproto.py || warn "mtproto lane not added"

  PANEL_USER="$PANEL_USER" PANEL_PASS="$PANEL_PASS" \
    python3 scripts/add-amneziawg.py || warn "amneziawg lane not added"

  docker restart 3x-ui >/dev/null; sleep 10
fi

say "accounts"
if [ "$ROLE" = node ]; then
  warn "node role: no local accounts - clients arrive from the master (stage 4)"
else
  PANEL_USER="$PANEL_USER" PANEL_PASS="$PANEL_PASS" \
    python3 scripts/consolidate-clients.py --apply
fi

# ── 7. verify ───────────────────────────────────────────────────────────────
say "verify"
fail=0
code=$(curl -s -m 10 -o /dev/null -w '%{http_code}' "https://${PUBLIC_DOMAIN}/" || echo 000)
[ "$code" = 200 ] && ok "decoy site answers 200" || { warn "decoy returned $code"; fail=1; }

subj=$(echo | openssl s_client -connect "${PUBLIC_DOMAIN}:443" -servername "$PUBLIC_DOMAIN" 2>/dev/null \
       | openssl x509 -noout -subject 2>/dev/null || true)
case "$subj" in
  *"$BASE_DOMAIN"*) ok "Reality fallback serves your own certificate";;
  *google*)         warn "fallback serves a Google cert - target/serverNames have drifted apart"; fail=1;;
  *)                warn "unexpected certificate: ${subj:-none}"; fail=1;;
esac

docker logs --tail 40 3x-ui 2>&1 | grep -q 'Failed to start' \
  && { warn "Xray failed to start - check: docker logs 3x-ui"; fail=1; } \
  || ok "Xray running"

say "done"
echo "  role       ${ROLE}"
echo "  panel      https://${PANEL_DOMAIN}${PANEL_PATH}/"
[ -n "$NEW_CREDS" ] && echo "  login      ${PANEL_USER} / ${PANEL_PASS}"
if [ "$ROLE" = node ]; then
  echo
  echo "  no subscriptions here - users attach to the master's URL."
  echo "  the master reaches this panel with NODE_API_TOKEN from .env"
  echo "  (mode 600); hand it over at 'nodes.py add' time (stage 2)."
  echo "  lanes and clients arrive from the master (stages 3-4)."
else
  echo
  echo "  subscriptions (one URL per person, all lanes):"
  # PUBLIC_DOMAIN, not PANEL_DOMAIN: the subscription router is bound to the
  # public hostname (see xui/docker-compose.yml), and configure-subscription.py
  # writes the same host into subURI. Printing the admin hostname here would hand
  # out a URL that lands on the decoy.
  PANEL_USER="$PANEL_USER" PANEL_PASS="$PANEL_PASS"   python3 scripts/client-links.py --urls || warn "could not list subscriptions"
  echo
  echo "  credentials: ./CLIENT-CREDENTIALS.txt (mode 600)"
fi
[ "$fail" = 0 ] || echo -e "\n  \033[33msome checks did not pass - see above\033[0m"
