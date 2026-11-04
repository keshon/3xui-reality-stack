# Go controller prerelease

[`v0.1.0-go.1` is published](https://github.com/keshon/3xui-reality-stack/releases/tag/controller-v0.1.0-go.1).
The public bootstrap installation and rerun were verified on Linux amd64 on KZ.

`v0.1.0-go.1` provides compiled Linux controllers and a Linux/Windows SSH manager.
Neither Go nor Python is needed on the target. The existing Python console
remains the default for installations that need adoption, optional transports,
traffic nodes or broader configuration changes.

This prerelease supports standalone fresh installation with Reality, Trojan
HTTPUpgrade and Hysteria2, unified accounts and subscriptions. Installation is
local CLI only. The fullscreen TUI supports existing-host inspection and the
separate experimental panel-password workflow, locally or over SSH.

## Install the controller

On a Linux amd64/arm64 VPS:

```bash
curl -fsSL https://raw.githubusercontent.com/keshon/3xui-reality-stack/controller-v0.1.0-go.1/scripts/install-controller.sh \
  | sudo bash -s -- --version v0.1.0-go.1
```

The bootstrap downloads the architecture-specific binary and checksum manifest,
checks its hash and version, and installs it at
`/opt/stackctl-go/v0.1.0-go.1/stackctl-go`. It installs no application services and
does not alter a deployment. Repeating it verifies the same release without
overwriting changed files. Checksums detect corruption; they are supplied by the
same release publisher as the binaries.

## Deploy a standalone stack

Run as root on a host with Docker Engine, Compose v2 and systemd already working.
Fresh installation refuses existing containers, volumes, custom networks and
conflicting listeners, including stopped applications. It does not migrate an
existing stack. The target directory must be a new absolute path with an
existing parent.

Point public and panel DNS records directly at the server. Allow inbound TCP 80
and TCP/UDP 443 in the provider firewall; HTTP-01 validation needs public port 80.
The controller does not configure the host or provider firewall. Management HTTPS
uses loopback port 2053. Outbound HTTPS is needed for images, certificates and
authenticated transfer checks.

Save a private configuration file, for example `/root/fresh-install.json`:

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
chmod 600 /root/fresh-install.json
controller=/opt/stackctl-go/v0.1.0-go.1/stackctl-go
"$controller" initialize --root /opt/new-stack --config-stdin < /root/fresh-install.json
"$controller" preflight --root /opt/new-stack
"$controller" plan --root /opt/new-stack --experimental-install
"$controller" review --root /opt/new-stack --experimental-install
"$controller" apply --root /opt/new-stack --experimental-install --confirm REVIEW_ID
"$controller" runtime --root /opt/new-stack
```

Replace `REVIEW_ID` with the returned review identifier. Review expires after
15 minutes and binds the configuration, host and installed controller. A changed
draft or resource conflict requires correction and a new review. Initialization
alone creates no running deployment and returns `can_apply: false`.

The workflow obtains trusted certificates, provisions accounts, checks native
and JSON subscriptions and transfers through all three transports, then commits
the applied baseline. Generated administrator credentials and private panel and
subscription paths are stored in `/opt/new-stack/.stack/config.json`; keep that
file private. The initial decoy is the bundled example site. Russian destinations
are allowed. DNS-01 additionally accepts `acme_mode: "dns"` and `dns_token` for
Cloudflare; public/panel hosts must be directly below the base domain.

After an interruption, inspect the saved operation and resume its exact ID:

```bash
"$controller" operation --root /opt/new-stack
"$controller" resume --root /opt/new-stack --experimental-install --operation OPERATION_ID
```

Recovery keeps the original reviewed inputs. Repeated approval returns the same
operation. `cancel-preparation` is limited to operations before application
effects; it does not roll back a deployment. Preserve the versioned controller
directory: the certificate service uses that binary's physical path.

## Open the TUI

```bash
/opt/stackctl-go/v0.1.0-go.1/stackctl-go tui --root /opt/new-stack
```

For remote use, download the matching `stack-manager` asset and `SHA256SUMS` from
the [prerelease](https://github.com/keshon/3xui-reality-stack/releases/tag/controller-v0.1.0-go.1),
verify its checksum, and configure SSH key/agent access through an SSH alias:

```bash
stack-manager tui --ssh my-vps --root /opt/new-stack --controller /opt/stackctl-go/v0.1.0-go.1/stackctl-go
```

The manager uses SSH stdio and opens no management listener. Sessions default to
read-only. F9 opens menus, F1 shows help, F5 refreshes and F10 exits; navigation
and dialogs also accept mouse input. See the
[controller plan](internal/go-controller-plan.md#fullscreen-tui) for the separate
password opt-in and current acceptance scope.
