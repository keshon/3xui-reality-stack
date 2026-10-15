#!/usr/bin/env bash
# Backup of everything needed to rebuild THIS host. Safe to re-run;
# rotation bounds the disk use.
#
# Packs (skipping whatever is absent on this role): .env, users.txt,
# nodes.txt, CLIENT-CREDENTIALS.txt, the panel database (xui/db) and the
# issued certificates (shared/certs). Reality private keys live only in the
# panel database - that is why xui/db is the load-bearing item here.
#
#   sudo ./scripts/backup.sh
#   BACKUP_KEEP=14 BACKUP_DIR=/var/backups/3xui ./scripts/backup.sh
#
# BACKUP_KEEP defaults to 7 newest archives. Archives are mode 600 (secrets).
#
# Restore is manual on purpose: stop the stack, unpack over the repo,
# re-run ./install.sh (idempotent) to converge. The panel database is copied
# live; stop 3x-ui first if you want it quiesced
# (docker stop 3x-ui ... tar ... docker start 3x-ui).
set -euo pipefail
ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$ROOT"

KEEP="${BACKUP_KEEP:-7}"
DIR="${BACKUP_DIR:-backups}"
case "$KEEP" in ''|*[!0-9]*|0) echo "BACKUP_KEEP must be a positive integer" >&2; exit 1;; esac
mkdir -p "$DIR"
TS="$(date -u +%Y%m%d-%H%M%S)"
OUT="$DIR/backup-$TS.tar.gz"

HAVE=()
for f in .env users.txt nodes.txt CLIENT-CREDENTIALS.txt xui/db shared/certs; do
  [ -e "$f" ] && HAVE+=("$f")
done
[ "${#HAVE[@]}" -gt 0 ] || { echo "nothing to back up" >&2; exit 1; }

tar -czf "$OUT" "${HAVE[@]}"
chmod 600 "$OUT"

# Rotation: keep the newest $KEEP archives.
# (One archive always exists - we just made it - so the glob never misfires.)
ls -1t "$DIR"/backup-*.tar.gz | tail -n "+$((KEEP + 1))" | xargs -r rm -f

echo "wrote $OUT ($(du -h "$OUT" | awk '{print $1}')):"
tar -tzf "$OUT" | sed 's/^/  /'
echo "kept: $(ls -1 "$DIR"/backup-*.tar.gz | wc -l) archive(s)"
