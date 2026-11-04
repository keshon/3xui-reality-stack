# Conventions

Each console and CLI uses its controller for configuration validation, locking
and progress. The Python controller delegates application actions to deployment
scripts; the Go controller uses stack adapters. Keep UI state separate from
deployment rules.

## Development

Use Python 3.11+ and work from the repository root:

```bash
python3 -m venv .venv
.venv/bin/pip install -e '.[test]'
.venv/bin/python -m pytest -q
```

On Windows, use `.venv\Scripts\python.exe -m pip install -e ".[test]"` and
`.venv\Scripts\python.exe -m pytest -q`. Bash adapters also need Bash in the test
environment. Tests use temporary deployment roots and fake actions rather than
configuring the developer's host or contacting a real panel. Read skip output
when a platform cannot exercise an adapter or filesystem feature.

## Go controller development

The Go controller and Python CLI share managed configuration and locking; their
supported operations differ. Keep each entrypoint connected to its own controller.
Its package boundaries and recovery contracts are in
[Go controller internals](internal/go-controller-plan.md). With the Go version in
[go.mod](../go.mod), run `go test ./...` and `go vet ./...` from the repository
root. Format changed Go files with `gofmt`. Build local binaries under `bin/`.
Continue running the Python suite while both implementations coexist.

**Practice:** follow GEKTOR's root module, `cmd/` entrypoints, responsibility-based
`internal/` packages and colocated tests. Keep terminal dependencies outside
deployment logic; prefer the standard library and cgo-free dependencies.

**Enforced — `internal/deployment/lock_test.go`, `tests/test_go_coexistence.py`:**
Go and Python use the same deployment lock. Python refuses unfinished or unknown
Go operation markers between invocations. Interoperability tests need a Python
interpreter; see the internals reference for `STACK_TEST_PYTHON`. Installation
and password changes use separate execution adapters with explicit review and
confirmation.

**Enforced — `cmd/stack-manager/application_test.go`:** the manager's production
dependency graph excludes `internal/xui` and `internal/testkit`. Its read commands
use the same controller API and rendering as the local CLI. The protocol and
simulated-stack tests cover framing, rejected mutation/path requests, mismatched
responses, cancellation and process-exit recovery. They do not replace SSH host
integration or real-container tests.

**Enforced — `internal/testkit/review_test.go`, `internal/control/operations_test.go`,
`internal/xui/review_test.go`:** reviews bind private input/runtime observations to
random, expiring IDs. Execution requires exact confirmation; duplicate delivery
does not repeat an action. Recovery targets an exact operation ID. The simulated
adapter exercises execution across reconnects. The 3x-ui installation and
password adapters accept only their own reviewed workflow kinds.

## Rule labels

- **Enforced:** names a guard or existing check. A check only covers the cases
  it exercises; it does not establish live network behavior.
- **Invariant:** names a failure to prevent; review may be the only guard.
- **Practice:** a maintainability convention.

## Configuration and operations

**Enforced — `tests/test_entrypoints.py`:** managed deployments enter installer
stages through the engine. Direct legacy `install.sh` and `bootstrap.sh` runs
refuse managed state before mutating the host. Do not bypass ownership by
setting the engine's internal environment marker in operator instructions.

**Enforced — `tests/test_config.py`, `tests/test_legacy.py`:** secrets are literal
values. Use `stackctl.envio` and the shared shell adapter. Never source `.env`
as shell or interpolate credentials into a command string.

**Enforced — `tests/test_engine.py`:** keep the deployment lock and operation
journal across configuration actions. Reviewed plans must refer to the inputs
being applied. Interrupted work resumes or enters explicit recovery; deleting
the journal/lock is not a recovery method.

**Enforced — `tests/test_adopt.py`:** adoption verifies live state and records a
baseline without provisioning, rewriting inputs or restarting services. Keep
adoption distinct from a fresh Apply.

**Enforced — `tests/test_upgrade.py`:** source upgrades validate the baseline,
compatibility and service plan before replacement. Preserve deployment data;
source/service rollback does not restore a live database over later writes.

**Enforced — `tests/test_backup.py`:** use the shared SQLite snapshot routine
for live state backups. A raw database copy can omit committed WAL state.
Backup retention must only remove archives owned by this tool.

**Invariant:** preserve existing keys and account credentials when reconciling.
After an ambiguous API response, read state before creating again. Node token
plaintext may be available only once; losing it requires explicit rotation.

**Invariant:** `.stack/`, `.env`, account/node inputs, certificate keys and
client exports are deployment data. Keep them out of commits and diagnostics.
Redaction reduces accidental disclosure; review logs before sharing them.

## Extending the stack

**Practice:** put panel response/API normalization in `scripts/panel.py`,
configuration validation in `stackctl/config.py`, operation selection in
`stackctl/engine.py`, and shared health behavior in `stackctl/health.py`.
Add code to the existing owner before introducing another layer.

**Invariant:** a transport change must account for listener ownership, provider
firewall rules, subscription output, client settings and restart effects.
See [Architecture](architecture.md) and [Clients](clients.md).

**Enforced — `tests/test_health.py`:** keep process state, fallback HTTPS and
authenticated transfer results separate. A timeout or skipped protocol is
not a successful handshake. Document the vantage point of a live measurement.

**Practice:** use meaningful existing behavioral checks for the affected
boundary. Bash syntax checks and local Compose rendering complement them;
neither proves an authenticated tunnel works on an access network. Live
procedures belong to a separately authorized deployment/diagnostic task.

## Documentation

**Practice:** the README owns orientation and first launch;
[Running](running.md) owns setup/configuration;
[Architecture](architecture.md) owns components, state and failure boundaries;
this document owns contributor rules. Specialist guides own their named tasks
and are linked from the README. Give each fact one canonical home.

Preserve the README's useful product vocabulary and introduction. Improve
navigation without replacing specific descriptions with generic summaries.
Keep defaults in step with [.env.example](../.env.example), package requirements
with [pyproject.toml](../pyproject.toml), and tested runtime versions with
[compatibility.json](../compatibility.json). Name concrete evidence for a
diagnosis; avoid turning a past incident into a universal cause.

Retired document paths currently remain short forwarding pages: source upgrades
reject source removals. Keep maintained contracts in the technical reference;
use Git history for superseded implementation notes. Check relative links,
fragments and command flags after documentation changes. Wizard SVG examples
are generated from the real UI and checked by `cmd/stack-preview` tests. Run
`go run ./cmd/stack-preview --render-docs docs/assets/wizard` after changing those
screens. Relative documentation links need a separate check.

The reusable [documentation pattern](../.agents/skills/docs-pattern/SKILL.md)
is versioned in this repository. Apply it when restructuring docs or adding a
new guide; adapt specialist documents to the project rather than copying a
fixed file list.
