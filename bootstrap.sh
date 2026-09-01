#!/usr/bin/env bash
# Idempotent first-run setup. Safe to re-run.
set -euo pipefail
cd "$(dirname "$0")"

[ -f .env ] || { echo "!! copy .env.example to .env and fill it in first"; exit 1; }

# Load .env WITHOUT sourcing it. Sourcing runs the file as shell, so any value
# containing '$' - an apr1/bcrypt password hash, for instance - gets expanded
# as a variable and either mangles the value or aborts under `set -u`.
while IFS= read -r line || [ -n "$line" ]; do
  case "$line" in ''|'#'*) continue;; *'='*) ;; *) continue;; esac
  k=${line%%=*}; v=${line#*=}
  case "$v" in '"'*'"') v=${v#\"}; v=${v%\"};; "'"*"'") v=${v#'}; v=${v%'};; esac
  export "$k=$v"
done < .env

# Fixed subnet so Traefik's proxyProtocol trustedIPs is deterministic.
if ! docker network inspect proxy >/dev/null 2>&1; then
  echo ">> creating docker network 'proxy' (${PROXY_SUBNET})"
  docker network create --subnet "${PROXY_SUBNET}" proxy
else
  have=$(docker network inspect proxy -f '{{range .IPAM.Config}}{{.Subnet}}{{end}}')
  echo ">> network 'proxy' already exists (${have})"
  # This is not cosmetic. Traefik trusts PROXY protocol v2 only from
  # PROXY_SUBNET, and Xray prepends that header on every fallback connection.
  # If the two disagree, Traefik reads the header as a malformed request and
  # every non-Reality connection - the decoy, the panel, every other site on
  # this box - fails. install.sh adopts the real value; refuse to guess.
  if [ "$have" != "${PROXY_SUBNET}" ]; then
    echo "!! PROXY_SUBNET=${PROXY_SUBNET} does not match it."
    echo "   Set PROXY_SUBNET=${have} in .env, or recreate the network."
    exit 1
  fi
fi

# traefik's file provider does not expand env vars - render the template
sed "s/CERT_BASENAME/${CERT_BASENAME}/g" traefik/dynamic/tls.yml.tmpl > traefik/dynamic/tls.yml

# Seed the decoy site if it is not there yet. It is deliberately not in the
# repo: an identical site on every deployment would identify all of them at
# once, which is precisely what it exists to prevent.
if [ ! -e decoy/site/index.html ]; then
  mkdir -p decoy/site
  cp -r decoy/site.example/. decoy/site/
  echo "!! decoy/site/ seeded from the example - REPLACE IT before this is"
  echo "   reachable. It is what probes, bare-IP scans and Reality's fallback"
  echo "   all see, and the shipped pages are placeholder prose."
fi

mkdir -p traefik/data/logs traefik/data/acme shared/certs xui/db
[ -f traefik/data/acme/acme.json ] || : > traefik/data/acme/acme.json
chmod 600 traefik/data/acme/acme.json

# HTTP-01 and DNS-01 are separate compose files rather than an override,
# because compose REPLACES a command list instead of merging it.
if [ "${ACME_MODE:-dns}" = "dns" ]; then
  TRAEFIK_FILE=docker-compose.dns01.yml
  [ -n "${CF_DNS_API_TOKEN:-}" ] || { echo "!! ACME_MODE=dns but CF_DNS_API_TOKEN is empty"; exit 1; }
else
  TRAEFIK_FILE=docker-compose.yml
fi
echo ">> acme mode: ${ACME_MODE:-dns}  (traefik/${TRAEFIK_FILE})"

echo
echo ">> starting: traefik -> decoy -> xui"
( cd traefik && docker compose -f "$TRAEFIK_FILE" --env-file ../.env up -d )
for d in decoy xui; do
  ( cd "$d" && docker compose --env-file ../.env up -d )
done

echo
echo ">> containers up. install.sh continues from here; if you ran bootstrap.sh"
echo "   directly, wait for the wildcard cert then provision the lanes:"
echo "     ls shared/certs/certs/"
echo "     python3 scripts/provision-inbounds.py     # see docs/gotchas.md first"
