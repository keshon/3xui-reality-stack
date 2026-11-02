# Running the stack

This guide covers fresh installs, adoption of running deployments and routine
configuration changes. The console edits configuration without dotenv or shell escaping.
Its UI is English. It runs locally in this checkout; it is not a remote agent
or a replacement for 3x-ui.

## Requirements

- Debian/Ubuntu with root access for deployment and live checks.
- Python 3.11+; the Linux launcher can install Python/venv packages with apt
  when running as root. The launcher installs its UI dependencies into `.venv`.
- A domain, DNS records for separate public and admin hostnames, and an ACME
  email. The default DNS-01 setup uses a Cloudflare token scoped to DNS editing
  for the zone. Keep traffic hostnames DNS-only.
- Provider firewall access for `80/tcp`, `443/tcp`, `443/udp` and enabled
  MTProto/AmneziaWG ports. Traffic nodes also need their management TCP port.
  See the [port map](architecture.md#listeners).
- Docker/Compose; infrastructure preparation installs Docker when missing.
  RAM requirements depend on enabled transports and concurrent connections.

Clone the source on the host before opening the console:

```bash
git clone https://github.com/keshon/3xui-reality-stack.git /opt/3xui-reality-stack
cd /opt/3xui-reality-stack
```

Before exposing the deployment, place your own static site in `decoy/site/`.
Bootstrap seeds it from `decoy/site.example/` if missing. That seed is placeholder
content; replace it. The directory is deployment data and is gitignored.

## Install and open

On the Debian/Ubuntu host, using the same account that owns the checkout
(root for applying infrastructure changes):

```bash
cd /opt/3xui-reality-stack
bash stack.sh
```

The first launch automatically creates a private `.venv` and installs the
console dependencies. On a Debian/Ubuntu host running as root, missing Python
or its venv support package is installed with apt. Python 3.11+ is required;
an older interpreter needs upgrading before launch. Other platforms/accounts
get an actionable error if Python or venv support is missing.

Later launches reuse the installed environment without pip/network calls.
Dependency changes in `pyproject.toml` trigger setup again. Interrupted or
failed setup can be retried with the same command. No activation is needed.
`bash stack.sh resume` continues an interrupted deployment.

Source updates use `stack upgrade --from /path/to/release`; the default is a
preview. See [source upgrades and rollback](maintenance.md#source-upgrades). Configuration Apply
and source Upgrade are separate operations and share the same deployment lock.

On Windows, editing and previewing plans also works:

```powershell
.\stack.cmd
```

Install Python 3.11+ once if it is absent. The launcher uses `py -3`, falling
back to `python` on PATH. It prepares the environment automatically.

Apply and live checks run on the Linux host as root. The tool never SSHs into
another machine. Access the host over SSH, then open the console there.
This checkout must stay present: the package invokes its Compose files and
deployment scripts. To select another checkout, use `stack --root /path`.

## Your first visit

For an already running installation, first preview adoption:

```bash
python3 -m stackctl adopt
python3 -m stackctl adopt --yes
```

Adoption verifies the existing panel/subscriptions and authenticated transfers
before recording a managed baseline. It does not provision accounts, rewrite
`.env` or user/node files, restart services, or tune the host. Only users in the
existing account inputs become managed; other panel accounts stay untouched.
Interrupted adoption can be resumed with the same command. If existing literal
settings require normalization, the command stops for review.

For a fresh installation, use the following setup:

1. Open **Server settings**. Choose Single server, Master + traffic, or Traffic
   node. Enter DNS zone, public/admin hostnames, ACME email and Cloudflare token.
2. Under **Access**, paste the login/password literally. `$`, spaces, quotes,
   and backslashes are preserved. Do not add shell escapes. Empty credentials
   and secret paths are generated on Save. Use **Show access values** to read
   the resulting credentials on a later visit.
3. Paths such as `panel-abc` become `/panel-abc`. URLs, whitespace, `..`, query
   strings and overlapping service paths are rejected. Admin and public
   hostnames must differ. DNS-01 hostnames must fit the wildcard certificate.
4. Save, then open **Users**. A new single-server configuration starts with
   `user1`; replace it with your actual account names before applying.
5. For a master, open **Nodes** to add existing traffic nodes. The form asks
   for the panel URL including its path, its node-sync token, and public host.
6. Open **Review changes / resume**. Review affected steps and any explicit
   revocations, then apply. The first managed deployment includes host tuning,
   firewall rules, Docker preparation, Compose changes and restarts.

Settings drafts are saved while typing. Escape/Back keeps the draft. Save
validates the complete configuration; it does not change the running stack.
Users/nodes are saved with their explicit Save buttons. Apply has a separate
review screen, and the displayed plan is tied to the reviewed configuration.
All users are attached to every configured lane and node, as before.

For a fresh traffic node, acceptance verifies local panel access and token
storage, and reaches the independent management HTTPS endpoint with certificate
verification. The node has no traffic lanes until the master provisions them; the
console does not incorrectly require a public Xray fallback at this point.

Traffic nodes expose Traefik HTTPS on `MANAGEMENT_PORT` (2053 by default),
independent of Xray. Add `https://PANEL_DOMAIN:2053/PANEL_PATH` on the master.
Open that TCP port in the provider firewall, preferably restricted to the
master's IP. Single-server/master hosts bind this listener to loopback by
default; nodes bind to `0.0.0.0`. It has the same panel authentication and TLS
as the normal admin route. The public traffic socket remains Xray's `:443`.

## Configuration and secrets

The source of truth after the first Save is `.stack/config.json` (versioned
schema, literal JSON strings). The console initially imports existing `.env`,
`users.txt`, and `nodes.txt`; the original standard input files are retained
under `.stack/imported/`. Unknown environment settings are preserved.

Apply generates `.env`, `users.txt`, and `nodes.txt` from that configuration.
Direct `install.sh` and `bootstrap.sh` runs are blocked on managed installations;
the engine invokes their internal stages. Do not alternate manual edits with
managed console runs.
Changes to generated files after an apply stop the next operation rather than
being silently overwritten. To move back to legacy/manual management, use a
separate checkout and the saved original files; review live panel state first.

All new files containing secrets are atomically written with owner-only
permissions on Linux. `.stack/`, `.env` and generated files are gitignored.
They are **not encrypted**. On Windows the parent directory's ACL controls
access; POSIX permission bits do not provide Windows ACL isolation. Keep this
checkout in your private account directory.

Generated dotenv uses quoted literal values and escaped dollar signs for
Compose. Bash and Python use one shared parser, never `source .env` or `eval`.
Multiline/NUL credentials are rejected. Existing escaping that was already
corrupted cannot be reconstructed: enter the intended value once in Access.

Do not post configuration files in chat or bug reports. Logs hide known
credentials and links, but panel error bodies should still be treated as
potentially sensitive and reviewed before sharing.

## Interrupted operations

```bash
.venv/bin/stack status
.venv/bin/stack resume
```

Or reopen the console and select **Review changes / resume**. It shows the
remaining steps. **Stop safely**, Ctrl+C, or Ctrl+Q stops the active child
process group, saves progress, and releases the operation lock.

The journal is `.stack/operation.json`, the bounded-step log is
`.stack/operation.log`. Configuration editing is blocked while an operation
is unfinished. Finished steps are retained; the interrupted step is repeated
using the existing scripts' read-before-write behavior. Generated keys are
reused when their inbounds already exist. There is no automatic rollback of
external actions. Timeouts stop the operation instead of marking it complete.

If an input is wrong (for example, a mistyped node token), select **Unlock
settings for recovery** on the review screen, or run `stack recover`.
This explicitly keeps changes already made, archives the old journal under
`.stack/history/`, and lets you correct settings. The next Apply checks all
deployment stages again. It is not rollback. Deployed topology remains
protected against accidental node/domain replacement. A preflight-only failure
on a new installation can be corrected before any lanes were created.

An ambiguous create response stops the step: resume reads the panel state
before deciding to create again. One-time API-token creation has an unavoidable
window: if the panel created the token but the process died before persisting
its plaintext, the tool fails with recovery instructions instead of claiming
success. On that node, explicitly rotate it with
`python3 scripts/mint-node-token.py --rotate`, then resume. On the master,
update the stored token through Nodes and apply. Never automatically rotate
tokens simply because a request timed out.

On Linux the active child inherits the file lock. If the Python controller is
killed abruptly, a still-running step continues to hold the lock until it
exits. Do not delete the lock file or the journal to bypass it.

## Routine operations

```bash
.venv/bin/stack plan
.venv/bin/stack apply
.venv/bin/stack doctor
```

`plan` compares local desired configuration with the last successfully applied
revision. It lists deployment steps, setting names (without values), additions,
and managed revocations. It is **not** an exhaustive API diff. `apply --yes`
supports unattended use after printing the plan.

User-only edits reconcile users without Docker/host setup or panel restart.
After a successful apply, applying the same configuration again is a no-op;
use Doctor to check live health separately. Doctor reads the deployed files,
not pending configuration edits, and holds the same operation lock.

Final acceptance checks required lanes, users and attachments, subscriptions,
distinct Reality keys and expected fallback certificates. Doctor additionally
checks the core process, verified fallback HTTPS and authenticated Reality,
Trojan and Hysteria2 transfers from the server. These do not prove operation
on your access network. MTProto/AmneziaWG handshakes remain untested; generic
UDP timeouts remain unverified.

## Supported changes

- Node addition and explicit token updates are supported. Node replacement,
  renaming, removal, deployed-domain changes, MTProto port changes, ACME-mode
  changes and host-role changes require migration; unsafe edits are rejected
  before saving. See [multi-node maintenance](multinode.md#replacement-and-removal) before planning a migration.
- Every node still needs the same fleet zone/certificate basename, Trojan path
  and pinned 3x-ui version as the master. The console validates its own host
  and the node's URL/domain, but cannot read remote Traefik configuration
  through the node-sync API. Check those shared settings when preparing nodes.
- Fleet-wide user revocation is blocked: the current integration does not
  confirm each remote node's revocation state. Revoke/check on each node
  separately during maintenance. Single-host revocations affect only users
  explicitly removed from the previous managed revision, not unrelated panel
  accounts. Revoking the last user is also blocked.
- Disabling deployed lanes/clearing existing limits is blocked; neither is a revocation mechanism.
  Existing attachments/inbounds are not automatically deleted. Core Reality,
  Trojan and Hysteria lanes stay enabled; MTProto/AmneziaWG are optional on
  initial deployment. MTProto/AmneziaWG are still local to the master.
- Tokens added to the master cannot bootstrap a remote VPS. Install each node
  on that VPS, then register it on the master.
- The console is not high availability. Master failure still stops
  subscription updates. SSH is the independent repair path when Xray fails.
- Source upgrades retain consistent SQLite snapshots and support source/service
  rollback; see [source upgrades](maintenance.md#source-upgrades). `scripts/backup.sh` uses the same state
  snapshot routine and includes managed settings. Database restoration stays
  manual; see [state backups](maintenance.md#state-backups).


## Client handout

```bash
python3 scripts/client-links.py --write --awg-dir awg
```

This writes `CLIENT-LINKS.txt` and AmneziaWG exports. Treat the output as
credentials; share only the intended account's links. See [Clients](clients.md)
for subscription import and a baseline configuration. Test one account on the
intended access network before distributing it to everyone.

## Legacy unmanaged installation

Use this only for a checkout without managed `.stack/config.json`:

```bash
cp .env.example .env
cp users.txt.example users.txt
$EDITOR .env
$EDITOR users.txt
sudo ./install.sh
```

The [.env example](../.env.example) documents legacy inputs. Account names use
`a-z`, `0-9` and `_`, up to 24 characters. Validate the account list with
`python3 scripts/resolve-users.py --list`.

The installer generates missing secrets, checks DNS, prepares Docker, host
policy and firewall rules, starts services, waits for certificates, provisions
inbounds and accounts, then verifies the result. It changes infrastructure and
may restart services; use source upgrades for source-only updates.

Removing a legacy user input does not itself revoke the panel account. Review
live accounts before reconciliation or pruning. Adopt an existing deployment
using the earlier procedure to manage future changes through Review / Apply.
