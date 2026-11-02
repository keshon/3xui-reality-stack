# Source upgrades

Use source upgrades to update the stack's scripts and Compose definitions
without running account provisioning. Run on the deployed Linux host as root.
The command uses only Python 3.11+ and the existing Docker/Compose installation.

## Preview and apply

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

## Roll back

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

## Supported boundary

`compatibility.json` records the runtime versions tested for this release.
This command handles source upgrades on standalone/master hosts. Dependency,
Python requirement, TLS-template and source-removal changes require a separate
migration. Traffic-node upgrades need coordinated checks from the master.

The live source must match its last release manifest or be a clean Git
checkout. A legacy deployment with neither needs a reviewed baseline before
using this command. A complete update can require manual work outside this
boundary; the upgrader stops rather than guessing a migration.
