#!/usr/bin/env bash
# Compatible entry point; Python owns state selection, SQLite snapshots and rotation.
# BACKUP_DIR and BACKUP_KEEP remain supported. Restore is a separate operation.
set -euo pipefail
ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$ROOT"
exec python3 -m stackctl.backup "$@"
