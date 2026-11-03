# Go deployment controller plan

Status: implementation started on `codex/go-deployment-engine`. The Python
controller remains the operational entrypoint. The Go CLI reads local managed
metadata, checks runtime compatibility, saves bound reviews and reports operation
status. Its password editor updates desired configuration through the shared lock,
locally or over SSH, without Python. A separate manager CLI uses the same
controller API over SSH stdio. The
execution/confirmation/recovery path is exercised through the simulated site
adapter and the simulated panel. Default 3x-ui composition remains read-only;
`--experimental-password-writes` explicitly selects its narrow password adapter
for the eventual German-server rehearsal. Live/container validation is pending.
Implementation continues locally before that rehearsal. Nothing has been deployed.

## Outcome

A fresh Linux VPS can install the controller with `curl -fsSL <release-url> |
bash`, complete a short wizard, and deploy a verified stack. Repeating setup
opens the existing installation. A configuration file supports unattended
installation through the same operations. Routine changes affect only their
owned resources, and interrupted changes have an explicit recovery path.

The first supported stack is this repository's 3x-ui deployment. Existing
account credentials, keys, subscription hosts, paths and IDs survive adoption.
Other active web applications and infrastructure remain operator-owned unless
explicitly included in a reviewed migration.

## Scope and boundaries

Keep the engine independent of the deployed application. It owns operation
locking, reviewed-input binding, checkpoints, cancellation, events and execution
of adapter actions. Stack adapters own their configuration schemas, inspection,
resource ownership, planning, backups, mutation and verification. For example,
the engine knows that an action needs a recovery point; only the 3x-ui adapter
knows how to capture a consistent panel database and preserve subscriptions.

This is an architectural boundary, not a plugin platform. Start with a compiled
3x-ui adapter and Docker Compose. Do not introduce a plugin loader, generic
workflow language, stack marketplace, Kubernetes abstraction or arbitrary shell
hooks. A simulated site adapter exercises the shared contracts in tests; it is
excluded from production commands. Add another production adapter when a real
second stack justifies it.
Supporting any Compose file alone would not provide safe application upgrades,
credential changes or migrations.

Use one versioned desired configuration per installation, with stack-specific
settings. Applied state, ownership records and an operation journal are separate
from desired configuration. Generated files are outputs. During development,
read the existing schema directly without rewriting or normalizing it. Specify
and test a versioned import before changing that schema or taking write ownership.

## Go layout

Follow the relevant patterns from GEKTOR: one root module, executable wiring in
`cmd/`, implementation packages in `internal/`, colocated `_test.go` files,
standard library first and no cgo. Use small packages named for responsibilities;
do not add `pkg/`, `utils`, `common`, or an interface for every concrete type.

Current packages:

```text
cmd/stackctl-go/       experimental CLI wiring and output
cmd/stack-manager/     adapter-free remote CLI, ahead of the shared TUI
cmd/stack-release/     local release builder; no publishing or deployment
internal/deployment/  stack-independent plan vocabulary
                      reviews, status, locking, durable writes and resumable execution
internal/control/     local controller API, stdio protocol and protocol client
internal/console/     command output and exact confirmation shared by both CLIs
internal/sshclient/   OpenSSH process transport, without stack logic
internal/buildinfo/   version metadata injected into release binaries
internal/release/     cgo-free release builder and checksum manifest
internal/bootstrap/   tests for the Bash controller binary installer
internal/host/        bounded read-only Linux and local Docker discovery
internal/xui/         3x-ui state reader, planner, HTTP adapter and password workflow
internal/testkit/     simulated site stack and fault injection, for tests only
```

Introduce a TUI package when its milestone needs it. The engine must not import
an adapter or terminal library. CLI wiring
selects the adapter; adapters may import engine contracts. Keep future Bubble
Tea rendering and keyboard state outside deployment logic. Reuse GEKTOR's
structural approach and terminal experience, not its agent/model machinery.
The experimental executable name avoids replacing the existing `stack` command.

## Local and remote management

The future TUI uses the controller API in either location. On the VPS it can
call the local controller. On a workstation it uses the protocol client over
SSH. Stack adapters and saved deployment credentials remain on the VPS; the
manager binary imports no stack adapter or simulated stack. Future user-entered
secret changes may cross the encrypted channel without being saved locally.

The implemented client/controller path is:

```text
local CLI -> controller -> selected stack adapter
manager CLI -> OpenSSH -> controller serve --stdio -> selected stack adapter
```

There is no public RPC listener, HTTP management daemon or extra management
certificate. OpenSSH provides authentication and host verification; the stdio
protocol supplies neither by itself. The manager uses a configured SSH alias,
requires a known host key, disables prompts/PTY/forwarding, and quotes the remote
binary and deployment paths independently. Encrypted keys must already be usable
through the operator's SSH setup. See the upstream [SSH command](https://man.openbsd.org/ssh.1)
and [configuration](https://man.openbsd.org/ssh_config.5) references.

Protocol version 1 uses newline-delimited JSON with a 64 KiB frame limit,
request IDs and a capability handshake. The 3x-ui adapter offers `inspect`,
`plan`, `runtime`, `preflight`, `initialize`, `review` and `operation`. `review` saves private controller
metadata; it does not change the application. The capability flag `read_only`
means deployment writes are unavailable, not that reviews cannot be saved.
`apply`, `resume` and `cancel-preparation` are advertised only by adapters that
implement the separate execution contract: the test-only site adapter and the
explicitly selected experimental 3x-ui password adapter.
That password adapter also advertises `configure`, which accepts stack-specific
field edits and saves desired state only. The initial implementation accepts
only `PANEL_PASS`; it cannot apply changes, choose a deployment path or edit
generated files. Configuration parameters are bounded protocol input and are
never returned in the public plan or error. The SSH manager keeps no local copy.
The deployment root is fixed at controller startup, not chosen by
RPC requests. The protocol offers no arbitrary shell or file operation. Adapter
errors and remote stderr do not cross into public output. Connection cancellation
closes the session; the client does not reconnect or retry automatically.
Execution requires explicit confirmation of an exact review ID. Recovery names
an exact operation ID. Duplicate apply delivery returns the existing operation
receipt and never retries a failed step; recovery uses the explicit resume path.
These contracts are exercised through the simulated adapter, including a lost
reply after server-side completion and reconnection to the same journal.

This separates the management client from stack implementation. It does not
remove the panel APIs used internally by the 3x-ui adapter. Hiding the browser
panel or replacing it with a headless runtime is a separate deployment change.

### Simulated stacks and failures

`internal/testkit` models a site with desired, applied and running configuration
under a temporary `.fixture/` directory. It uses the same controller reports,
CLI rendering, protocol and execution engine as the 3x-ui adapter. It never
invokes Docker or changes a host. Run `go test ./internal/testkit ./internal/control`
to exercise this boundary; the CLI packages also test composition with this
second adapter. The shipping manager dependency graph is checked in its tests.

The failure matrix covers a rejected write, a reply lost after persistence,
verification failure, stale review inputs and external drift. A subprocess test
exits after persisting the simulated effect but before the engine checkpoints
it; a fresh process resumes from the intent journal without a duplicate effect.
The 3x-ui HTTP simulator remains separate and tests its API-specific behavior.
These mocks complement real-container and existing-host validation.

## Bound reviews and confirmation

`review` takes the deployment lock, captures the adapter's proposed effects and
private observation, and writes `.stack/go-reviews/<random-id>.json`. The private
record contains the resolved checkout path, versioned workflow kind, exact input
bytes and runtime identity. The public response contains only the random ID,
plan, creation/expiry times and `can_apply`. IDs are independent of credentials;
secret-derived digests and private binding data are not sent to the manager.

A review lasts 15 minutes by the server clock. Initial execution re-observes
inputs/runtime under the same lock and rejects changed effects, configuration,
runtime identity, adapter kind or deployment root. The 3x-ui observation binds
the original desired/applied/generated files, compatibility policy, container
IDs, image IDs, startup times, mounts, port bindings and panel version. Runtime
version probes target the inspected immutable container ID rather than its name.
Default reviews return `can_apply: false`. The experimental password adapter can
issue executable reviews after validating the configured management endpoint;
that review does not authenticate or prove the HTTPS endpoint is healthy.

The local and remote CLI require `apply --confirm REVIEW_ID`; there is no generic
`--yes` that can approve a different plan. Resume requires
`resume --operation OPERATION_ID`. A consumed review cannot start another
operation. A repeated apply returns the current receipt while it still names
that operation; after another operation replaces the active journal, the old
review is rejected. Review expiry never blocks recovery of an already started
operation. Workflows still recheck their frozen inputs before effects and replay.

The review reservation is persisted before the operation's intent journal.
An interruption between these two local writes consumes the review without
starting application effects: inspect status and create a new review. Do not
automatically replay the old request. An interruption after intent registration
can resume using the frozen review, including before workflow preparation
completed. The status API reads the atomically replaced journal without waiting
for the execution lock, and exposes progress/identifiers without recovery secrets.
`cancel-preparation --operation OPERATION_ID` only retires a preparation before
any workflow step starts; it is not rollback of an attempted action.

Review and operation history retention, backup integration and production
execution validation remain part of the rollout work. Private review records
contain credentials and must be included in the eventual Go backup policy.

## Operation contract

1. Inspect live resources and local state under a deployment lock. Identify
   owned resources and stop on conflicting ownership or interrupted operations.
2. Validate desired configuration and produce a plan with affected resources,
   restart effects and verification/recovery steps. Public output omits secret
   values, subscription credentials and secret-derived review hashes.
3. Bind approval to the exact inspected inputs, desired state, adapter version
   and plan. Revalidate before mutation; a stale plan must stop.
4. Capture required recovery state before the first write, including a consistent
   SQLite snapshot where applicable. Checkpoint action intent before execution.
5. Perform the smallest action. After an ambiguous API result, inspect the
   resource before retrying. Never infer ownership from a username suffix.
6. Verify actual behavior and preserved identities, then commit outputs and the
   applied revision. Verification failure leaves an explicit recoverable
   operation, not a successful baseline.

Recovery is action-specific. A credential change can reconcile the old/new
authentication state; it must not blindly restore an old database over subsequent
user activity. Cancellation stops at a documented safe boundary. Do not promise
a general transaction or automatic rollback of arbitrary host changes.

## Milestones and acceptance

### 1. Inspect and plan (local implementation complete)

- Read existing managed configuration, applied revision and operation markers.
- Check generated-file hashes and block unknown/incomplete state.
- Report local metadata separately from live health.
- Plan only an isolated administrator password change; block all other edits.
- Test secret-free output, drift, malformed state and narrow planning scope.

Acceptance: the experimental command performs no writes, invokes no legacy
installer, and cannot mistake a broad configuration change for a password edit.
Local reads are best-effort observations, not a locked live-state review.

### 2. Complete the password-change slice (local prototype implemented)

- Add shared locking compatible with the Python controller, or explicitly
  refuse concurrent ownership during a versioned handover.
- Inspect the pinned panel API and implement its Go authentication adapter.
- Snapshot before mutation; journal intent, read after ambiguous responses,
  verify authentication and preserved account/subscription identities.
- Commit generated credentials and applied state only after verification.
- Resume failures before/after API mutation, verification and local commit.

Acceptance: a password edit never provisions users/transports, changes routing,
recreates unrelated containers or touches host firewall policy. Test against the
pinned panel, including interrupted requests and partially committed local state.

Implemented locally: the shared lock, Python handover guard, private recovery
files, intent/checkpoint journal, API authentication/CSRF, database download,
preservation checks, password mutation and two-file commit/replay. Tests exercise
a dropped mutation response, failed authentication verification, changed remote
identities, stale local inputs, interrupted preparation and partial local commit.
Two-factor authentication and traffic-node password changes are explicitly
unsupported by this workflow. Nodes use a credential-derived `panel-ready`
marker to control management exposure during bootstrap; its migration needs
separate coverage before Go can change node credentials.

The engine writes `.stack/go-operation.json` and retains recovery artifacts in
`.stack/go-operations/<id>/`: reviewed input bytes, original/target file bytes,
client/settings preservation data and `panel.db`. These contain credentials.
There is no retention/removal automation yet. The Python controller refuses write
ownership until the Go marker is complete or preparation has been cancelled.
Cancellation is permitted only before workflow actions start; it does not restore
a database or undo a password change. Resume uses the frozen inputs and checks
the original endpoint, recovery database hash and allowed local file states.
The private review also freezes compatibility policy bytes and observed container
IDs, image IDs, start times, mounts and management bindings. Preparation, password
mutation, verification and each local commit recheck that runtime. Recovery accepts
only the original or this operation's target local files; it does not refresh the
review to accept a restarted or replaced container. These checks detect observed
drift; they cannot make Docker inspection and an HTTP write atomic.

The HTTP adapter follows the pinned [credential endpoint](https://github.com/MHSanaei/3x-ui/blob/v3.7.0/internal/web/controller/setting.go)
and [SQLite backup implementation](https://github.com/MHSanaei/3x-ui/blob/v3.7.0/internal/web/service/server.go).
It verifies the downloaded SQLite header, not database integrity. Public plans
remain metadata-only. Runtime discovery checks local container state, pinned image
references, database bind-mount ownership, the independent management binding and
the panel's version command. It uses the VPS's local Docker socket rather than
the operator's Docker context. It queries selected inspect fields, without
environment variables or labels, and invokes the version command only after
confirming the panel's image reference and database mount. Runtime compatibility
is separate from API authentication, image provenance and actual tunnel health.

The generic review confirmation and status/resume contracts are implemented and
tested with the simulated stack. The internal 3x-ui workflow now rechecks its
private runtime/input binding before writes, including recovery and partial local
commit. Default 3x-ui wiring still has no execution capability. The separate
`PasswordStack` adapter is selected only with `--experimental-password-writes`
on password configuration, review, apply, recovery or stdio serve. The SSH manager forwards this explicit
choice when launching the remote controller. A default read-only review cannot
be upgraded into an executable review by adding the flag later.

`configure-password --password-stdin` now saves desired `PANEL_PASS` directly in
Go. It holds the shared lock, rejects incomplete legacy/Go operations and drift,
and refuses unrelated pending changes or unsupported fields. It never logs in to
the panel or invokes Docker. Generated credentials and applied state remain old
until the separate review/apply workflow verifies the remote change. Repeating
the same edit is a no-op; replacing/reverting an unapplied password edit is
supported. Existing executable reviews become stale when desired input changes.
Input is valid UTF-8, at most 4096 bytes, with no controls or Unicode line
separators. One final LF/CRLF is stripped; other whitespace remains literal.

The password adapter derives its HTTPS URL from the frozen applied panel domain,
path and management port. Its transport dials the local IPv4 listener directly,
retaining the panel hostname for HTTP Host and ordinary system-trust TLS
validation. It does not use environment proxies, public DNS or disabled
certificate verification. A specific public-only management bind, traffic nodes,
unsupported endpoint syntax and non-password edits are rejected. Local TLS tests
cover untrusted certificates, hostname mismatch and successful local routing
with a test-scoped trust store. No custom CA or insecure CLI option is exposed.

The opt-in container harness creates two synthetic clients attached to disabled
VLESS/Trojan inbounds, a panel and a Traefik management route. A test-only adapter
opens the original private review through the engine. It injects an interruption
after the remote password mutation and another after the first local commit,
then resumes by operation ID and checks the final baseline and duplicate apply
receipt. The downloaded API backup and engine recovery database undergo independent
SQLite integrity checks; a second panel starts from the engine's backup and must
retain the original credentials, clients, attachments and settings. The same
engine/review recovery wiring runs against the HTTP simulator in ordinary tests.

The real-container harness has been compiled but not executed: Docker and WSL
are unavailable on the development machine. Its Traefik fixture uses loopback
HTTP; production HTTPS, authenticated transport transfers, real subscription
responses and abrupt process termination against containers remain untested.
Those Linux checks remain validation work before routine production use, rather
than a prerequisite for continuing implementation. The German installation is
the intended existing-host test ground once the implementation is ready. Use
disposable resources for fresh installation and destructive crash tests; Windows
HTTP and filesystem fixtures do not establish Linux compatibility.

`.github/workflows/go-controller.yml` defines a disposable Ubuntu amd64 job for
unit/race/interoperability tests, vet, cgo-free cross-builds and the opt-in pinned
container harness. It runs on relevant pull requests, pushes to main/the rewrite
branch and manual dispatch. It has read-only repository permissions and no VPS
credentials or deployment steps. This workflow has not run yet; adding the file
does not establish a passing Linux result. Arm64 is cross-built, not exercised.

### 3. Fresh-host installation

`preflight` is now available locally and through the SSH manager without enabling
writes. It classifies the target root as absent, empty, draft, managed, legacy,
unmanaged or unknown. Only an unchanged, unused Go initialization draft joins
absent/empty directories as a fresh-host candidate. Other existing state blocks
fresh deployment; this does not block the separate password workflow for an
existing installation. An incomplete `.stack` directory remains blocked.
No target directory, lock, desired configuration or journal is created by preflight.

Host discovery checks Linux amd64/arm64 and Ubuntu/Debian identity, then reads
IPv4/IPv6 socket tables for the first core-transport deployment: TCP 80/443/2053
and UDP 443. Optional MTProto/AWG ports are not checked for this draft. Bound
connections, including TCP TIME_WAIT, are conservatively
treated as occupied. Missing/malformed tables are unknown, not free. Docker
discovery selects only full container IDs and the Compose project label, network
names, volume names and the Compose plugin version. Queries use the local Docker
socket with bounded output and command timeouts, ignoring alternate Docker
contexts. Public reports contain counts, not private resource names or raw errors.
Existing containers, custom networks or volumes block fresh deployment even if
they are stopped or unused; integration and cleanup are never inferred.

`candidate` means those observed checks found no conflicts. It does not reserve
ports, approve deployment or validate distribution versions, disk capacity, DNS,
certificates, provider/firewall policy or authenticated transport traffic. It also
does not assess an existing stack's health; use runtime/transport checks for that.
The socket parser follows the kernel's [proc socket table layout](https://docs.kernel.org/networking/proc_net_tcp.html),
and inventory uses selected [Docker list fields](https://docs.docker.com/reference/cli/docker/container/ls/).
These observations still need a German-host rehearsal; current checks use local
fixtures and the shared protocol. Fresh deployment itself is not implemented.

Release packaging and controller binary bootstrap are implemented locally.
Private fresh-install configuration preparation is implemented as well;
provisioning and its interactive wizard remain pending.
`cmd/stack-release` builds Linux amd64/arm64 controller and manager binaries,
a Windows amd64 manager, and `SHA256SUMS`. It injects the explicit version,
disables cgo and automatic VCS metadata, and writes the checksum manifest last.
Output must be a new directory; failed or existing output is never overwritten.
Both controller and manager support `version` without configuration or SSH.

`scripts/install-controller.sh` installs only the Linux controller binary. It requires an
explicit version and downloads assets from the matching `controller-v…` GitHub
release. The target needs no Go compiler, Python, pip, git clone or source build;
compilation happens locally or in CI. HTTPS downloads and redirects, checksums and the reported binary version
are checked before installation. It installs into `/opt/stackctl-go/<version>/`,
keeps earlier versions, rejects changed existing releases and symlinked prefixes,
and uses a separate directory lock. It does not switch a global executable,
install Docker, run the legacy installer or modify a deployment checkout.
Optional `--inspect` runs only read-only inspection against the explicit root.
A killed bootstrap can leave `.install-lock`; inspect for an active installer
before manually removing that empty directory. It is never cleared automatically.

The release builder has built all five real assets locally; their checksums and
the Windows manager's embedded version were verified. Bootstrap tests run the
real script under Bash with local download/executable fixtures, covering failed
downloads, bad/duplicate/missing checksums, version mismatch, platform selection,
reruns, conflicts and deployment-state preservation. They do not establish that
the Linux binaries run on a fresh VPS. CI now uses the release builder, checks
every checksum and runs the two Linux amd64 version commands on its runner.
That updated CI job has not run yet.

#### Preparing a fresh-install draft

`initialize --config-stdin` accepts bounded UTF-8 JSON locally or through the SSH
manager. It creates a private standalone configuration and deployment-file draft
for the three core transports. An example request is:

```json
{
  "schema_version": 1,
  "base_domain": "example.com",
  "public_domain": "www.example.com",
  "panel_domain": "ops.example.com",
  "acme_email": "admin@example.com",
  "acme_mode": "http",
  "users": ["alice", "bob"]
}
```

```bash
stackctl-go initialize --root /opt/new-stack --config-stdin < fresh-install.json
```

The parent must exist and the target must be a new absolute path. Existing empty
directories, checkouts and deployed installations are refused. HTTP-01 is the
default; DNS-01 additionally requires `acme_mode: "dns"` and `dns_token` with
public/panel hosts directly below the base domain. Optional `panel_user` and
`panel_password` are literal input; absent values and private panel/subscription
paths are generated cryptographically. Keep input files private when supplying
credentials. No secret, account name or secret-derived fingerprint is returned.

The initializer renders `.stack/config.json`, `.env`, `users.txt`, `nodes.txt`
and the embedded compatibility policy, plus private initialization metadata.
It also renders `xui/compose.json`, `traefik/compose.json`, `decoy/compose.json`,
Traefik's dynamic TLS JSON and the bundled nginx/example-site files. Compose
documents use JSON with literal values and Compose dollar escaping, not shell
templates or environment-file substitutions. The three projects retain the
external `proxy` network, Xray's public TCP/UDP 443 ownership, Traefik's internal
8443 listener and loopback management HTTPS. Optional transport ports are omitted.
Image pins come from the embedded compatibility policy; the existing nginx
example image remains `nginx:1.29-alpine`. The example site must be replaced
before a public deployment. Files ship inside the binary; no checkout is needed.
Management defaults to loopback, Russian destinations are allowed, and optional
MTProto/AmneziaWG lanes and master/node roles are outside this first slice.
No applied baseline, running deployment, service or certificate is created.
The Compose drafts deliberately omit the old Python certificate sidecar. The Go
exporter and its systemd unit renderer are implemented. Runtime directories,
asset permissions and an empty ACME state are prepared inside the private draft.
The private runtime workflow implements service installation/activation, but is
not connected to a public execution adapter. Web startup and certificate
readiness and isolated panel bootstrap are implemented privately; transport/user
provisioning, public route activation and final verification must be added before
install apply is available. The dynamic TLS
file describes future certificate paths; those certificates do not exist yet.
The report lists future provisioning stages with `can_apply: false`.

Files are staged privately, synced and published as a complete directory. The
same normalized request can reopen an unchanged draft without rotating any saved
credentials or paths. Changed input, file drift, active operations, symlinks or
an applied baseline stop the rerun. A crash before publication can leave a private
`.stack-initialize-*` directory beside the target; cleanup is not automated.
The Python controller refuses ownership of an unapplied Go draft. Python is used
only in optional tests confirming schema and literal dotenv interoperability.
Generated Compose/TLS/site files participate in the same ownership hashes, so
reruns preserve local edits by refusing to overwrite them. Earlier drafts with
the smaller file set are refused rather than silently expanded.

Unit tests cover socket ownership, routing, ACME mode separation, literal DNS
tokens, image pins, deterministic output and drift refusal. The opt-in Linux
`TestRenderedComposeConfig` test loads both modes with real `docker compose
config`, checks relative bind resolution and guards against environment
interpolation. It runs in the integration CI job; Docker is unavailable locally,
so real Compose validation has not yet run on this workstation.

The SSH manager uses the same `initialize --config-stdin` command with its usual
SSH/root/controller options. Input crosses the private channel and remains on
the controller host. Initialization cannot select a root in its RPC request.
Default `read_only` capability still means application effects are unavailable;
reviews and initialization can write private controller/configuration metadata.
Ordinary `plan` remains blocked on a draft until fresh-install execution exists;
this is not an adoption request or a working deployed stack.

#### Fresh-install review

`review` recognizes an unapplied Go draft and creates an `xui-install-v1` preview
through the existing local/SSH controller and review engine. It still returns
`can_apply: false`; no installation execution adapter is registered. The optional
password adapter refuses this review, so enabling password writes cannot enable
installation. Saving a review creates private review metadata, not an operation.

The shared draft reader checks bounded regular files, initialization hashes,
directory types/permissions, absent applied state and idle operation journals.
Database, log and certificate leaf directories must remain empty. Review also
checks that the files reproduce this controller's configuration/assets. It
rechecks inputs after observing the host to catch changes during review.

The private binding contains every draft file, the initialization marker, host
facts, the controller's physical path/version/SHA256 and the exact exporter unit.
Public output contains effects and review identifiers, without credentials,
domains, file contents, paths or binary fingerprints. Existing containers,
networks, volumes, occupied ports or unavailable prerequisites block review.
Docker/Compose must already be available for this preview; automatic Docker
installation remains unfinished.

The controller must be a release build with an adjacent `SHA256SUMS` entry
matching its bytes. A Linux controller also compares those bytes with its own
running `/proc/self/exe` image. Development builds or incomplete/changed release
directories are refused. This checks local release consistency, not independent
signature provenance; the bootstrap's verified HTTPS download remains the
installation source.

The service observer queries only LoadState, ActiveState, FragmentPath,
DropInPaths and UnitFileState from the local system manager. It clears inherited
bus/systemd overrides, bounds command time/output, and accepts an absent unit
only when inactive with no fragment, drop-ins or enabled file state. It neither
overwrites nor adopts an existing unit. A probe result is a snapshot, not a unit
name reservation; execution must revalidate ownership and the loaded unit before
activation. The Linux CI systemd checks include an unused random service query.

Tests simulate fresh Linux hosts locally and exercise review bindings, release
changes, blocked services, unavailable managers, populated database directories,
input races, checksum mismatch/duplicates and the password/installation boundary.
The preview makes no runtime changes. The private workflow described below begins
to execute the frozen review through the same operation engine. Web bootstrap and
certificate readiness and isolated panel bootstrap are implemented; transport/user
provisioning, public route activation, subscription/transfer verification and
applied-state commit remain unfinished.

#### Install runtime stages and recovery

An unexported `installRuntime` workflow now freezes the review in the operation's
`installation.json` and uses the shared engine's checkpoints. It is not registered
as a production execution adapter: the CLI and SSH API continue to report
`can_apply: false`. Local tests supply simulated Docker and systemd boundaries;
no live host is changed by the tests.

Preparation writes private recovery artifacts only. The first executable stage
validates all three Compose documents with an explicit, frozen empty environment
file. Network creation follows validation. The `proxy` bridge has the reviewed
subnet/gateway and installation/operation ownership labels. Inventory uses full
network IDs and selected fields, following the [Docker network list](https://docs.docker.com/reference/cli/docker/network/ls/)
and [creation](https://docs.docker.com/reference/cli/docker/network/create/) contracts.
An ambiguous create reply is resolved by inspecting the exact owned resource.
The network ID is then saved in `network.json`; a missing/replaced recorded ID,
foreign labels, changed subnet/driver or unexpected attached containers stops
recovery. No foreign resource is adopted or deleted.

The helper installer publishes an operation-marked unit without replacing a
competing file: it stages and syncs the bytes, then links the complete file to
the final name. It rejects foreign files, loaded units, symlinks and drop-ins.
After daemon reload it checks the loaded fragment and reviewed on-disk bytes
before enabling/starting that unit. A matching active/enabled unit is left
running on retry. An ambiguous enable reply is resolved from the loaded state.
Interrupted publication can leave a staging file; cleanup is not automated.

Each implemented stage rechecks frozen inputs, controller identity, host
conflicts and allowed owned resources. Recovery reads the original snapshot,
not a fresh review. The engine tests interrupt after network creation before
its receipt, and after network/service effects before the engine checkpoint.
They confirm single resource creation/activation and rejection of changed
inputs, replacement network IDs and changed controller binaries. Service tests
also cover publication/reload interruption and modified bytes before activation.

The `services` stage bootstraps web services and the `panel` stage initializes an
isolated panel as described below. The remaining `verify` and `commit` stages
deliberately stop with execution unavailable. Transport/user provisioning and
promotion to the final public deployment must precede final verification.
Their identifiers stay stable for future recovery. The partial workflow cannot
mark an install complete or create an applied baseline. Runtime stages still
need real Linux Docker/systemd acceptance before controlled deployment; only
simulated host boundaries have run locally.

#### Web bootstrap and certificate readiness

The private `services` stage obtains the pinned nginx/Traefik images if absent,
then records both repository digests and local image IDs in `web-intent.json`.
Operation-owned Compose overrides select immutable [repository digests](https://docs.docker.com/reference/compose-file/services/#image)
and attach installation/operation labels. Replays reuse those references rather
than follow mutable tags. Only the decoy and Traefik start, with build, dependency
startup, pulling and container recreation disabled in the startup command. The
panel is not started in this phase.

Before and after startup, selected Docker metadata must match the owned service,
image, mounts, network and published ports. Unexpected containers or network
endpoints block progress. Only Traefik's reviewed 80/TCP and loopback 2053/TCP
bindings are allowed to account for occupied ports; public 443 remains unused.
Per-service receipts freeze container IDs and start timestamps. Lost startup
replies are reconciled from owned resources; recorded replacement, restart,
stoppage or disappearance requires inspection rather than recreation.

Once startup intent is frozen, ACME state, certificate outputs and Traefik logs
may change. All other reviewed files retain their hashes and permissions, and
the panel database directory must still be empty. Ordinary initialization and
preflight do not gain this exception; it belongs only to the active private
installation workflow.

HTTP-01 explicitly requests a certificate with the public hostname as main name
and the panel hostname as SAN, so certificate issuance does not require starting
an unconfigured panel. DNS-01 retains the existing base/wildcard certificate.
Readiness requires a currently valid ACME pair covering both hosts and matching
the exported certificate/key bytes. It then probes both hostnames over local
HTTPS with TLS 1.3, system trust and hostname verification, preserving SNI/Host
while dialing loopback directly. Redirects are refused and the returned page must
match the reviewed decoy index. No insecure TLS setting or public DNS lookup is
used for these probes.

Pending issuance/export leaves the operation at `services`. Explicit resume
checks readiness again without restarting healthy recorded containers. The
exporter may take its polling interval to publish a newly issued certificate;
interactive waiting/progress handling remains future UI work. The manager's
current request timeout also needs revisiting before exposing long fresh installs.

Tests cover lost startup replies/checkpoints, certificate waits, image-tag
changes, mount/identity drift, unrelated containers and unchanged panel state.
Synthetic certificate tests cover pending/stale/mixed exports and required
hostnames; real loopback HTTPS tests cover trust, redirects and page integrity.
Linux Compose config validation also includes the digest/ownership overrides.
Real Docker startup, public ACME issuance and end-to-end provisioning have not
been exercised on this workstation or a VPS.

#### Isolated panel bootstrap

The private `panel` stage freezes the pinned panel repository digest and image
ID, then creates an operation-owned bootstrap container. It uses Docker's default
bridge rather than the Traefik proxy network, disables Traefik discovery and
publishes only the panel API on a random IPv4 loopback port. No public transport
port is published. The reviewed database and read-only certificate directory are
the only mounts; the binary runs directly with the fixed UTC timezone.

Container creation and initial startup have separate receipts. The database
must remain empty until the first owned start; while running, only the SQLite
database and its regular, bounded sidecar files are accepted. Ordinary draft
inspection retains its empty-database requirement. Selected Docker inspection
checks image, operation labels, entrypoint, mounts, network and listener layout
without reading container environment values. Unrelated containers remain a
conflict. The panel stays isolated after bootstrap; promotion to the final public
deployment is still pending.

The helper tries the desired credentials first. Only an explicit authentication
rejection allows a fallback to the pinned upstream's fresh `admin/admin` account.
Credentials travel in HTTP JSON bodies, followed by a new authenticated session.
A lost write response is reconciled by proving the desired credentials rather
than repeating the write. Transport failures and unsupported 2FA do not trigger
fallback writes.

Settings use the pinned v3.7.0 [`/panel/api/setting/` routes](https://github.com/mhsanaei/3x-ui/blob/f727d04f6522bb94a8fb52e8352fdcafb51c11e1/internal/web/controller/setting.go). This also corrects
outdated settings paths in the password-change client. Initialization reads the
complete settings view, preserves unrelated fields and redacted-secret flags,
then updates the panel path and native/JSON subscription paths and public URLs.
Read-back must confirm the submitted view. The workflow records restart intent,
restarts the same immutable container ID, then records its new start timestamp
and loopback port. An interrupted restart reply is reconciled without issuing
another restart. Readiness requires fresh authentication at the configured path
and matching settings; an API readiness delay resumes without restarting again.
Recorded disappearance, replacement, unexpected restart, listener changes or
bridge identity changes stop recovery.

Local HTTP fixtures cover interrupted replies, replay before/after a simulated
restart, foreign credentials, unavailable authentication, 2FA, incomplete settings
and ignored writes. Engine tests cover interrupted creation/start/restart and
lost stage checkpoints, readiness waits, unchanged write counts, foreign database
files and container/route/mount/network drift. The opt-in `TestPinnedPanelIsolation`
uses the production Docker creation recipe and metadata checks, initializes the
API and verifies the changed path after restart. The pinned backup/restore test
also exercises API bootstrap. These Docker tests have not run on this workstation;
public installation execution remains disabled.

The next provisioning slice should create one native unified client per person
and attach it to all three core inbounds through the pinned clients API. The
legacy per-lane clone/consolidation sequence is unnecessary for a fresh database.
Credentials and Reality keys must be frozen before the first API write, and
recovery must match existing rows against that intent instead of regenerating
identities or repairing unrelated rows. Reality's `traefik:8443` fallback becomes
reachable only after the configured panel is promoted onto the proxy network;
bootstrap readiness alone does not prove working transports.

#### Certificate export

`stackctl-go export-certificates` replaces the Python certificate extraction loop
for the new installer. It is a local host utility, outside the SSH RPC API, and
does not restart containers or modify managed application state. The intended
supervised process uses the same precompiled controller binary; no Python image
or separate Go runtime is needed. Unit rendering and owned installation exist in
the private workflow; public activation remains disabled.
The legacy Compose projects retain their current exporter until migration.

The command requires absolute source/destination paths, an explicit DNS filename
basename and an optional resolver name (default `le`). The destination must
already exist when a certificate is ready. A future installed invocation is:

```bash
stackctl-go export-certificates --source /opt/xray-stack/traefik/data/acme/acme.json --destination /opt/xray-stack/shared/certs --domain www.example.com --watch
```

Without `--watch`, it performs one export. A `waiting` report means issuance is
pending, not successful certificate readiness; installation must require `ready`.
Polling defaults to 60 seconds, runs immediately and exits on SIGINT/SIGTERM.
The watcher reports status transitions and changed files without hostnames,
paths, account credentials or keys. Failed attempts report `retrying`; a one-shot
invocation provides a static diagnostic without quoting source contents.

Only the selected resolver and main-domain basename are exported. A wildcard
main name uses its base domain for filenames. The exporter bounds ACME input at
16 MiB, validates PEM/key matching, leaf validity dates and declared hostname
coverage, and refuses ambiguous selected certificates. This is local pair
validation, not public certificate-chain trust verification. ACME account key
type fields are irrelevant. Partial/invalid source state preserves the last
exported bytes; empty or absent state waits for issuance. Old/unselected files
are retained, not pruned.

Output remains `certs/<basename>.crt` (0644) and `private/<basename>.key` (0600),
with private-directory mode 0700. A shared OS lock in the destination's `.stack`
directory serializes cooperating exporters. Output operations use `os.Root`
confinement, reject symlink output entries and stage both files before committing.
Each replacement is atomic and synchronized on Linux; the pair is not atomic.
Key-first publication can briefly expose a mixed pair. A retry repairs a stop
between replacements, without replacing unchanged bytes or restarting readers.
A killed exporter can leave private staging files; automatic cleanup is deferred.

Synthetic ECDSA tests cover renewal, wildcard names, missing account key-type
metadata, mismatch/expiry/hostname failures, malformed and oversized JSON,
duplicate certificates, symlinks, output locking, interrupted-pair recovery,
unchanged timestamps, Unix permissions and watcher recovery/cancellation.
No real ACME account or deployed certificate is used.

#### Runtime files and helper supervision

Initialization now creates empty database/log/certificate bind directories and
`traefik/data/acme/acme.json` inside the unpublished staging tree. Private
directories use 0700, ACME/config/Compose files use 0600, and the public decoy
files and dynamic TLS configuration use 0644 with their mounted directories
set to 0755. This lets nginx workers read the static site without exposing
controller credentials. The installation root itself remains private. Empty
directory entries are synchronized before publication on Linux.

Draft reopen checks directory types, symlinks, expected Unix permissions and
file hashes. Populated ACME state is treated as drift and never reset by
initialization. Installation recovery must use its operation workflow rather
than rerun initialization after ACME begins. Older drafts missing the runtime
file set are refused rather than expanded. No Docker, firewall or service-manager
command is executed while preparing these paths.

The host package renders a systemd unit for a command with one writable
directory. The 3x-ui adapter binds it to an explicit installed controller binary,
the selected certificate basename and this installation's ACME/output paths.
Its stable `stackctl-certs-<root-hash>.service` identity distinguishes installation
roots. Nothing is written to `/etc/systemd/system` by the renderer.

The unit runs the executable directly with literal, quoted arguments. It escapes
percent specifiers and uses systemd's `:` command prefix to disable environment
substitution, following the [systemd command-line rules](https://www.freedesktop.org/software/systemd/man/latest/systemd.service.html#Command%20lines).
It restarts failed processes, uses a private umask and a read-only filesystem view
with the certificate output directory writable. The executable cannot reside
inside that writable directory. Its root process has no extra capabilities;
it can read ACME and write the root-owned certificate files. It has no dependency
on Docker startup, because the exporter already waits for pending issuance.

The private runtime workflow binds the selected release, checks unit ownership
and installs/enables the helper. Certificate readiness is checked by the private
web stage; complete installation still needs the later provisioning stages and
Linux acceptance. Service
startup alone is not certificate readiness.
Unit rendering and runtime preparation do not turn on fresh-install apply.
Tests cover exact root/release bindings, literal path characters, unit identity,
public/private permissions, redirected binds and ACME-state preservation. The
opt-in Linux `TestServiceSystemdSyntax` runs `systemd-analyze verify` in CI without
installing or starting a service. It has not run locally on Windows.

#### Publishing and remaining installation work

No release assets have been published. The bootstrap download URL is therefore
an implementation contract, not a working public installation command. A checksum
from the same release detects corruption; it does not authenticate a compromised
publisher. Stack provisioning, existing-host adoption, interactive launch and the
fresh-install wizard remain unimplemented; binary bootstrap is not a replacement
for those operations. The remaining installation work is:

- Publish reviewed release artifacts only after the Linux rehearsal; the local
  builder itself never uploads assets or creates releases.
- Add interactive launch when the TUI exists. It must open `/dev/tty` when stdin
  belongs to the download pipe; unattended invocation must not require a terminal.
- Extend the implemented preflight with installation-specific version/capacity
  requirements and revalidate reviewed resource ownership before provisioning.
- Collect essential domain/certificate/admin settings; show the concrete plan.
  Support HTTP-01 and existing DNS-01 configuration without assuming a DNS API.
- Prepare Docker only if needed, validate the rendered Compose files, obtain certificates, create
  empty lanes and unified users directly, and verify real authenticated transfers.
- Make host firewall changes explicit in the plan; do not enable UFW as a side
  effect of changing an application port. Provider firewalls and DNS remain
  explicit prerequisites unless the operator configures an integration.
- Resume installation and ensure a second run creates no new credentials,
  duplicate accounts or unnecessary restarts. No Python dependency on this path.

Acceptance: disposable supported Ubuntu/Debian hosts pass install, rerun and
interrupted-install tests. Both architectures compile; runtime tests must state
which architectures were actually exercised. No public curl command is advertised
until release artifacts and the tested bootstrap exist.

### 4. Routine changes and explicit migration

- Add exact operations for users, subscriptions, lanes, routing and node setup.
- Preserve imported identities; bound every cleanup to reviewed owned IDs.
- Keep legacy account consolidation and node replacement out of routine apply.
- Implement import previews with collision checks, recovery backups and
  before/after identity and subscription verification.
- Inspect existing Traefik and web apps and support explicit integration rather
  than silently replacing their routing, certificates or ports.

Acceptance: adding a user cannot change host networking; routing changes cannot
rotate credentials; migration retains the original subscription URLs and all
working transport credentials. Test unmanaged users/inbounds and multiple
subscription IDs, including conflicting legacy identities.

### 5. TUI, upgrades and cutover

- Build a thin Bubble Tea TUI over tested CLI operations and shared events.
- Add release upgrade/rollback with explicit application-data restore boundaries.
- Exercise fresh install, adoption, routine edits, failure recovery and upgrades
  on disposable Linux hosts through the same engine used unattended.
- Rehearse against private copies of deployed state, then deploy to a test VPS.
  Live production cutover is a separate reviewed rollout with verified backups.
- Retire Python and shell paths only after replacement coverage exists. Leave
  one small release bootstrap and remove duplicated operational implementations.

## Validation environments

The existing German VPS is the intended integration test host for adoption,
routine changes and upgrades. It already serves users. Preserve its current
installation, account credentials, subscription URLs, certificates and other
applications. Do not reinstall it or use it to test destructive failure cases.
Server addresses and access details remain outside this document.

After the local implementation is ready, the rollout sequence is:

1. Inspect the deployed version and topology without changing them. Capture
   authenticated-transfer and subscription baselines.
2. Take verified configuration and consistent database backups, with a recovery
   procedure for the existing installation.
3. Run the Go inspector and planner against that installation; review ownership
   and compatibility before handing over write control.
4. Exercise one narrow operation, beginning with administrator password rotation,
   then check authentication, preserved identities/subscriptions and transports.
   Keep the existing controller available for recovery under the shared lock.
5. Expand to routine changes and upgrades only after each operation has passed
   its local and isolated integration checks.

The existing-host tests do not validate installation from scratch. Fresh install,
bootstrap reruns and intentional crash/recovery scenarios require a disposable
Linux VM or separate VPS. Container API tests complement that environment but
cannot establish host provisioning, firewall or reboot behavior. Deployment to
Germany remains deferred until the implementation is ready for review.

### First German-server password rehearsal

Prepare this workflow before transferring or running a binary on the server:

1. Record the existing topology and working subscription/transport baselines.
   Take configuration and consistent database backups using the existing backup
   procedure; confirm how to recover that installation. Do not reinstall or
   replace its Compose projects to accommodate the Go tool.
2. Build the matching Linux binary and place it at a separate versioned path.
   Keep the legacy controller and deployed release available. Inspect, plan and
   runtime checks run in the default read-only mode first.
3. Use Go's `configure-password --password-stdin` with the experimental option
   to save only the intended `PANEL_PASS` edit in desired configuration. Do not
   run legacy apply. Generated `.env` and the applied baseline must still
   describe the old password. Broader Go configuration editing is not implemented.
4. Create a fresh review with `--experimental-password-writes`; inspect the
   effects and confirm that it is only a password operation. Apply that exact
   review ID with the same option. The engine saves its own pre-write recovery
   database and verifies fresh authentication and preserved identities before
   committing generated credentials and applied state.
5. Check operation status, both old/new administrator authentication outcomes,
   unchanged account/subscription identities and real client transfers. Do not
   deliberately interrupt or replace live containers for fault testing. On a
   failure, inspect status and use explicit resume with its exact operation ID;
   do not rerun the legacy installer or restore a database blindly.

The flag is a temporary experimental access path for a controlled rehearsal,
not a claim that live validation has passed. No rehearsal has started yet.

## Development commands

From the repository root, using the Go version in `go.mod`:

```bash
go test ./...
go vet ./...
go run ./cmd/stackctl-go inspect --root /path/to/existing/checkout
go run ./cmd/stackctl-go plan --root /path/to/existing/checkout
go run ./cmd/stackctl-go runtime --root /path/to/existing/checkout
go run ./cmd/stackctl-go preflight --root /path/to/target
go run ./cmd/stackctl-go review --root /path/to/existing/checkout
go run ./cmd/stackctl-go operation --root /path/to/existing/checkout
```

Build experimental release assets into a new directory:

```bash
go run ./cmd/stack-release --version v0.1.0-go.1 --out /tmp/controller-release-v0.1.0-go.1
bash scripts/install-controller.sh --help
```

The release directory contains five raw binaries plus `SHA256SUMS`; no separate
archives, generated deployment files or credentials are included. Compatibility
policy and example decoy assets are embedded in the controller binary. The future GitHub release tag is
`controller-v0.1.0-go.1`. Nothing is uploaded by the build command. The bootstrap
cannot download that version until its matching assets are explicitly published.

`review` writes private controller metadata. Default 3x-ui composition reports
`can_apply: false`; saving that review does not enable its write path. Once the
implementation is ready for the controlled existing-host rehearsal, the explicit
local commands are:

```bash
read -r -s -p 'New panel password: ' stack_password
printf '\n'
printf '%s' "$stack_password" | stackctl-go configure-password --password-stdin --root /opt/xray-stack --experimental-password-writes
unset stack_password
stackctl-go review --root /opt/xray-stack --experimental-password-writes
stackctl-go apply --root /opt/xray-stack --experimental-password-writes --confirm REVIEW_ID
stackctl-go operation --root /opt/xray-stack
stackctl-go resume --root /opt/xray-stack --experimental-password-writes --operation OPERATION_ID
```

Replace identifiers with values returned by the controller. These commands are
preparation instructions; they have not been run against Germany.
The password command consumes stdin through EOF and does not hide terminal input
itself. The Bash example uses `read -s` to avoid echoing the password, and passes
it through a pipe rather than a process argument or command history literal.

The remote CLI is experimental and requires a separately installed controller
binary on the target. No target has received it yet. After an approved deployment,
an existing verified SSH alias can be used as follows:

```bash
go run ./cmd/stack-manager plan --ssh test-vps --root /opt/xray-stack --controller /opt/stack/bin/stackctl-go
```

The manager starts that binary with `serve --stdio`; it never installs or updates
it. Local `serve` requires the explicit `--stdio` flag and reserves stdout for
protocol frames. The SSH process transport is tested with a local child process;
the tests do not establish an SSH connection to a live host. The manager CLI
uses a 45-second connection/request budget. The TUI is not implemented yet.
For a password rehearsal through SSH, add `--experimental-password-writes` to the
manager's configure-password/review/apply/resume command; it starts the remote stdio controller with
that same option. Remote metadata and credentials remain on the controller host.

Go tests can run the real Python lock and dotenv parser for interoperability.
Set `STACK_TEST_PYTHON` to a Python interpreter path when it is not discoverable
as `python3` or `python`. Those checks skip if Python is absent; it is a test
dependency only. Production controller binaries remain cgo-free and use no
third-party Go dependencies in this milestone.

On a disposable Linux host with a local Docker daemon and `sqlite3`, explicitly
pull the pinned image and opt into the container test:

```bash
docker --host unix:///var/run/docker.sock pull ghcr.io/mhsanaei/3x-ui:v3.7.0
docker --host unix:///var/run/docker.sock pull traefik:v3.7.12
STACK_TEST_CONTAINERS=1 go test -tags integration ./internal/xui -run '^TestPinnedPanelBackupRestore$' -v -timeout 5m
```

The test uses randomly named, ownership-labelled containers, temporary database
and routing directories and ephemeral loopback HTTP ports. It selects the local Linux Docker
socket explicitly, never pulls automatically, and removes only its own containers.
It resolves the adapter's fixed discovery names to its own immutable container
IDs; it does not inspect or modify an existing stack. Image initialization,
synthetic managed metadata and restore checks use test credentials; no deployed
configuration is required. Container runs require both pinned images already
present locally and `sqlite3` on the host.

Commands emit JSON. `plan`, `runtime` and `preflight` exit unsuccessfully when blocked.
The `version` command emits a single version string instead of JSON.
`plan` reports proposed actions without executing them. An unchanged result refers to the local
baseline only, not live service health. Missing managed state is reported rather
than adopted. Continue using the Python CLI for all operational actions.
