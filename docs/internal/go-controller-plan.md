# Go controller internals

For installation and terminal operation, see [Go controller](../go-controller.md).
This reference describes controller boundaries and recovery contracts.

## Package boundaries

| Package | Responsibility |
|---|---|
| `cmd/stackctl-go` | Local CLI, adapter selection and local TUI |
| `cmd/stack-manager` | SSH manager; no stack adapter dependency |
| `cmd/stack-preview` | In-memory UI examples and SVG rendering |
| `cmd/stack-release` | Cross-platform binary builds and checksums |
| `internal/deployment` | Shared lock, reviews, durable writes and resumable operations |
| `internal/control` | Controller API and private stdio protocol |
| `internal/console` | Batch command validation and JSON output |
| `internal/sshclient` | OpenSSH process transport |
| `internal/tui` | Screens, input, confirmations and operation polling |
| `internal/host` | Host discovery and certificate-helper installation |
| `internal/xui` | 3x-ui configuration, Docker ownership, provisioning and verification |
| `internal/testkit` | Simulated stack and fault injection for tests |
| `internal/release`, `internal/buildinfo` | Release files and embedded version |

The deployment engine imports no stack adapter or terminal library. Adapters
own application rules. The TUI and CLI call the same controller operations.
Compatibility policy and initial site assets are embedded in controller binaries.
The manager contains neither application adapters nor test backends.

## Adapter selection

`Stack` supports inspection, preflight, initialization and bound reviews.
Execution is supplied separately:

- `InstallStack` opens only `xui-install-v1` workflows. The local batch CLI
  selects it with `--experimental-install`. Local TUI selects it for a new
  absolute root, an unapplied Go draft or an unfinished Go installation.
- `PasswordStack` opens only `xui-password-v1` workflows and accepts the isolated
  `PANEL_PASS` edit. It requires `--experimental-password-writes`.

Existing unmarked directories are not automatically adopted. An execution
adapter cannot open a different workflow kind. Installation remains local;
the SSH manager does not enable it. Initializing a draft creates private files
but does not create an applied baseline or start application services.

## Installation

The initializer validates bounded JSON, generates missing credentials and paths,
renders configuration/assets and atomically publishes a private target directory.
An identical request can reopen an unchanged draft. Edited files, symlinks,
active operations or an applied baseline prevent initialization replay.

Preflight requires a supported Linux/systemd host with Docker/Compose and a
conflict-free runtime. Existing containers, including stopped containers,
volumes, custom networks and required listeners block fresh deployment.
The installer does not stop other applications or switch Docker data directories.

The reviewed operation runs these checkpoints:

| Step | Effect |
|---|---|
| `validate-compose` | Validate all generated Compose projects with a frozen environment |
| `network` | Create the owned proxy network and record its full identity |
| `certificate-service` | Install the owned Go certificate-export systemd unit |
| `services` | Start pinned web images and wait for trusted certificates and HTTPS |
| `panel` | Bootstrap the isolated panel, administrator, transports and unified users |
| `verify` | Promote reviewed public routes; verify routing, subscriptions and authenticated transfers |
| `commit` | Repeat verification and publish the frozen applied baseline |

Traefik dynamic configuration uses a `.yml` filename, including when the contents
are JSON. Image tags resolve to immutable digests; runtime inspection checks the
pinned local image identity and repository digest. Resource receipts bind full
Docker IDs, installation labels and operation ownership. An ambiguous response
is resolved through observation before another mutation.

The certificate helper uses the physical path of the installed controller.
Review requires a non-development version, matching adjacent `SHA256SUMS`, and
on Linux a hash matching `/proc/self/exe`. Existing foreign units, files,
symlinks and drop-ins block helper installation. Preserve the controller release
directory while its helper is in use.

## Review and recovery

Reviews hold the shared deployment lock and bind exact private inputs, host
facts, controller identity and owned runtime state. Public output contains effects
and random identifiers; credentials and private bindings stay on the host.

A review expires after 15 minutes. Apply requires its exact confirmed ID and
revalidates the binding before effects. Repeating approval returns the same
operation receipt. It does not retry a failed step.

Recovery uses an exact operation ID and the original review/snapshot. Review
expiry does not prevent recovery of an already started operation. Steps recheck
ownership and intended effects before replay. Commit accepts only its exact
frozen applied record; replay does not rotate identities or replace timestamps.

Preparation cancellation is allowed only before application effects. It is not
a deployment rollback. Keep operation artifacts for diagnosis and explicit
recovery; deleting journals or lock files is not a recovery procedure.

## State

| Path | Contents |
|---|---|
| `.stack/config.json` | Desired configuration and literal secrets |
| `.stack/go-initialization.json` | Draft identity and asset hashes |
| `.stack/go-reviews/` | Private bound review records |
| `.stack/go-operation.json` | Current operation journal |
| `.stack/go-operations/` | Frozen inputs, receipts and recovery snapshots |
| `.stack/applied.json` | Verified configuration and output hashes |
| `.stack/lock` | Lock shared with the Python controller |

Go and Python use the same managed configuration schema and deployment lock.
Their operation markers prevent simultaneous ownership of unfinished work.
The Python controller rejects unapplied Go drafts. Adoption and broader
configuration changes remain separate operations.

## Local and remote control

```text
local TUI / CLI -> controller -> selected adapter -> deployment engine
manager -> OpenSSH -> controller serve --stdio -> selected adapter
```

SSH supplies authentication and host-key verification. The manager uses an
existing key/agent configuration, disables interactive prompts and forwarding,
and starts no public management listener. It neither installs nor updates the
remote controller. Routine inspection contains no application credentials.
The explicit Access request returns only the adapter's selected applied login
details over the private SSH channel; unrelated secrets, such as DNS API tokens,
are excluded.

Protocol version 1 uses request IDs, a capability handshake and bounded
newline-delimited JSON frames. The deployment root is fixed at controller startup;
requests cannot select arbitrary paths or run shell commands. Errors and remote
stderr are sanitized. Requests do not reconnect or retry automatically.

## Fullscreen TUI

The local installation wizard delegates initialization, preflight and review to
the controller. A separate Deploy confirmation starts execution. Progress polls
the saved operation; reopening an unfinished target selects recovery. Input
fields are cleared on cancellation, interruption and successful preparation.
Completed installation opens Access. The same page remains available in
navigation and Workspace after reopening an installed stack. Passwords are
masked until explicitly revealed and hidden again on navigation, help or exit.

Installation apply/resume has a 20-minute request budget. Other write requests
use two minutes; refresh uses 45 seconds. A terminal interruption cancels the
request, leaving the operation journal as the recovery authority.

Menus and dialogs use shared render/hit regions. Windows input supports native
console records and VT sequences. Synthetic compatibility tests do not replace
checking the operator's actual terminal.
The shell has a menu row, workspace, status line and bottom target/mode line.
It has no brand header or persistent function-key bar. Help and menus retain
keyboard shortcuts; context actions belong to the active page or dialog.

## Development and checks

Use the Go version from [go.mod](../../go.mod), format with `gofmt`, then run:

```bash
go test -race -tags integration ./... -timeout 120s
go vet -tags integration ./...
go run ./cmd/stack-preview --scenario install
go run ./cmd/stack-preview --scenario installed
go run ./cmd/stack-preview --render-docs docs/assets/wizard
```

`STACK_TEST_PYTHON` selects an optional interpreter for schema/lock interoperability
tests. Python is not a controller runtime dependency. `internal/testkit` covers
cross-adapter behavior; panel simulators cover API-specific failures. UI tests
cover forms, secrets, confirmations, recovery, mouse input and bounded layouts.

Pinned-container checks require a disposable local Linux Docker environment,
`sqlite3`, and the image pins from [compatibility.json](../../compatibility.json):

```bash
STACK_TEST_CONTAINERS=1 go test -tags integration ./internal/xui -run '^Test(PinnedPanelBackupRestore|PinnedPanelIsolation|RenderedComposeConfig)$' -v -timeout 5m
```

`TestLiveFreshInstallation` additionally requires explicit
`STACK_TEST_FRESH_INSTALL=1`, `STACK_TEST_FRESH_ROOT`, `STACK_TEST_CONTROLLER` and
`STACK_TEST_FRESH_PHASE=start|commit|finish|running`. These acceptance tests can
create host resources; they are excluded from ordinary test runs. Test fixtures
and SVG examples contain sample data and do not establish client-ISP reachability.

Build release assets into a new directory:

```bash
go run ./cmd/stack-release --version v0.1.0-go.2 --out /tmp/controller-release
```

The builder creates five cgo-free binaries and `SHA256SUMS`; it never publishes
or deploys them. The bootstrap selects a Linux architecture, verifies downloaded
bytes/version and installs a protected versioned directory outside the deployment.
It refuses changed installations. Release tags use `controller-VERSION`.
