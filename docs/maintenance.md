# Maintenance

Run maintenance on the deployed Linux host as root. Configuration changes use
[Review / Apply](running.md#routine-operations); updating source uses the separate
upgrade procedure below. Backups contain credentials and require private storage.

## Source upgrades

Use source upgrades to update the stack's scripts and Compose definitions
without running account provisioning. Run on the deployed Linux host as root.
The command uses only Python 3.11+ and the existing Docker/Compose installation.

### Preview and apply

Stage a separate clean checkout at the release commit you intend to deploy.
Keep the running checkout intact; do not pull changes directly over it.

```bash
git clone https://github.com/keshon/3xui-reality-stack.git /opt/3xui-release
git -C /opt/3xui-release checkout --detach <commit>
cd /opt/3xui-reality-stack  # use your actual deployment directory
python3 -m stackctl upgrade --from /opt/3xui-release
python3 -m stackctl upgrade --from /opt/3xui-release --yes
```

If the deployed source predates this command, invoke the tool from the trusted
staged release, pointing `--root` at the live deployment:

```bash
cd /opt/3xui-release
python3 -m stackctl --root /opt/3xui-reality-stack upgrade --from /opt/3xui-release
# Add --yes to the same command after reviewing its plan.
```

The default is a preview. It lists changed source files and services that would
be recreated. `--yes` applies the displayed plan. Releases can also be staged
directories with a `RELEASE-MANIFEST.json` containing SHA-256 hashes of each
source file. A manifest verifies file consistency, not publisher identity;
obtain the release from a trusted source.

Before replacing anything, the command checks the tested panel/core versions,
validates Compose, checks that required images are present, and verifies the
existing authenticated tunnels. It never pulls images or runs the installer.
Service config hashes are compared with running containers, so pending Compose
changes from a prior source update are included in the preview.

The upgrade then creates a private backup under `.stack/upgrades/`, replaces
changed source files, recreates affected services, and verifies authenticated
transfers and fallback HTTPS. Recreating Xray interrupts all its transports;
recreating Traefik interrupts Trojan and fallback HTTPS. Read the service list
before applying. Source changes that need no service restart cause none.

Accounts, `.env`, user/node lists, managed settings and certificates are never
reconciled or overwritten. The live SQLite database is backed up consistently
using SQLite's backup API. Backups contain credentials and must stay private.
They are not automatically deleted; remove older backups only after deciding
you no longer need their recovery point.

### Roll back

Verification failure automatically restores the source and affected services.
The database is not rolled back: doing so could discard user changes made
after the snapshot. Database restoration is a separate recovery operation.

For a manual rollback, use the backup path printed during the upgrade:

```bash
python3 -m stackctl rollback --backup /path/to/.stack/upgrades/<backup>
python3 -m stackctl rollback --backup /path/to/.stack/upgrades/<backup> --yes
```

Only the latest upgrade can be rolled back, and later source edits must be
resolved first. An interrupted upgrade blocks new source/configuration applies
until rollback succeeds. Each backup also retains a standalone recovery entry
point and a copy of the upgrader, in case the prior release lacks this command:

```bash
python3 /path/to/.stack/upgrades/<backup>/rollback.py
```

### Supported boundary

`compatibility.json` records the runtime versions tested for this release.
This command handles source upgrades on standalone/master hosts. Dependency,
Python requirement, TLS-template and source-removal changes require a separate
migration. Traffic-node upgrades need coordinated checks from the master.

The live source must match its last release manifest or be a clean Git
checkout. A legacy deployment with neither needs a reviewed baseline before
using this command. A complete update can require manual work outside this
boundary; the upgrader stops rather than guessing a migration.

## State backups

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
