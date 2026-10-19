#!/usr/bin/env bash
set -euo pipefail
ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
if ! command -v python3 >/dev/null 2>&1; then
  if [ "$(id -u)" = 0 ] && command -v apt-get >/dev/null 2>&1 && [ -f /etc/debian_version ]; then
    echo "Installing Python and environment support…"
    apt-get update
    apt-get install -y python3 python3-venv
  else
    echo "Python 3.11+ is required. Install it, then run this command again." >&2
    exit 1
  fi
fi
exec python3 "$ROOT/scripts/launch.py" "$@"
