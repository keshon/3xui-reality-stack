# Deployment-state backups

```bash
sudo bash scripts/backup.sh
BACKUP_KEEP=14 BACKUP_DIR=/var/backups/3xui sudo -E bash scripts/backup.sh
# Explicit options also work without forwarding environment variables:
sudo bash scripts/backup.sh --directory /var/backups/3xui --keep 14
```

The shell entry point calls `stackctl.backup`, which is also used by source
upgrades. It takes the deployment lock, copies the deployed inputs, managed
configuration and checkpoints, credentials, certificates and ACME state, and
uses SQLite's backup API for a consistent panel database snapshot. The database
can stay running. Live WAL/SHM files, traffic logs, diagnostic history and prior
upgrade backups are excluded. Additional regular files in `xui/db/` are kept.

Archives are mode 600 in a mode-700 directory. They contain credentials. The
default directory is `backups/` and retention is seven archives; command-line
options take precedence over environment values, then the deployed `.env`.
Rotation only removes archive names created by this tool or its legacy shell
version. The new archive is completed before rotation begins.

A state backup does not contain the application source or Docker images.
`RELEASE-MANIFEST.json`, when present, identifies the expected source contents.
Keep the corresponding trusted release and pinned images available separately.

Restoration is manual: prepare the matching source/runtime, stop writers before
restoring database or configuration files, unpack into a private staging
directory, and review the restored settings before starting services. Avoid
replaying a full installation merely to restore files. Replacing the database
discards changes newer than the snapshot. Source-only rollback during an
upgrade deliberately leaves the live database and current settings intact.
