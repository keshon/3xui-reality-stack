# 3xui-reality-stack

Deploys 3x-ui with Xray owning `:443` and the reverse proxy behind it. VLESS +
Reality + Vision, Trojan + HTTPUpgrade and Hysteria2 share the public TLS/QUIC
port. MTProto FakeTLS and AmneziaWG provide additional transport options. Each
person gets one account and one subscription URL, with a website served through
the TLS fallback.

The stack is for a small self-hosted group: accounts, transports, certificates
and subscriptions on a VPS you control. A terminal console handles configuration
and deployment; the 3x-ui panel remains available for inspection and maintenance.
A master can also publish connections for additional traffic nodes.

## What it runs

| Transport | Public port | Runtime |
|---|---|---|
| VLESS + Reality + Vision | `443/tcp` | Xray, directly on the public socket |
| Trojan + HTTPUpgrade | `443/tcp` | Xray fallback → Traefik → internal Trojan inbound |
| Hysteria2 | `443/udp` | Xray |
| MTProto FakeTLS | `MTPROTO_PORT/tcp` | Panel-supervised `mtg`, for Telegram |
| AmneziaWG | `AWG_PORT/udp` | Panel's embedded userspace implementation |

Reality falls back to your own Traefik instance, which presents a certificate
for the public hostname and serves your site. Traefik also handles the panel,
subscriptions and other HTTP apps. The default DNS-01 setup uses a wildcard
certificate, so covered hostname changes do not require separate certificate
entries. See [Architecture](docs/architecture.md) for traffic paths and listeners.

## Get it running

Use a Debian/Ubuntu VPS with root access, Python 3.11+, a domain and public/admin
DNS records pointing at the host. The default certificate setup needs a
Cloudflare DNS API token scoped to the zone. Keep traffic records DNS-only and
open the required ports in the provider firewall.

On the VPS:

```bash
git clone https://github.com/keshon/3xui-reality-stack.git /opt/3xui-reality-stack
cd /opt/3xui-reality-stack
sudo bash stack.sh
```

The first launch prepares a Python environment. Enter server settings, access
values and users, Save, then Review / Apply. Applying a fresh configuration
prepares Docker, firewall rules and host policy, starts services and provisions
accounts. Replace the seed content in `decoy/site/` with your own static site
before exposing the deployment.

The [running guide](docs/running.md) covers requirements, setup and validation.
On Windows, `.\stack.cmd` supports editing and plan previews; deployment runs
on the Linux host over your own SSH session.

## Already running this stack?

For an unmanaged deployment, preview adoption before applying configuration:

```bash
python3 -m stackctl adopt
python3 -m stackctl adopt --yes
```

Adoption verifies the running deployment and records its managed baseline
without provisioning, rewriting inputs or restarting services. Afterwards,
`.stack/config.json` owns configuration; use the console for changes. See
[Running](docs/running.md#your-first-visit) for prerequisites and recovery.

Update source through the separate [maintenance procedure](docs/maintenance.md),
which previews affected services and preserves deployment data. Running the
installer is not the source-update procedure.

## Using and checking it

Share only the intended person's subscription. Start with the generated client
profile and [client baseline](docs/clients.md), then test a real transfer on the
intended network. For HAPP Reality, start with Mux and fragmentation disabled.

```bash
python3 -m stackctl doctor
python3 scripts/client-links.py --write --awg-dir awg
```

Doctor checks deployed configuration and authenticated Xray transfers from the
server. It cannot prove reachability through a particular ISP; MTProto and
AmneziaWG handshakes remain outside its probe coverage. Client exports contain
credentials and must stay private.

Multiple transports still share a VPS, address and some runtime components.
This stack does not guarantee availability under filtering. Compare client,
network and server measurements using [Troubleshooting](docs/troubleshooting.md).

## Documentation

| Guide | Contents |
|---|---|
| [Running](docs/running.md) | Installation, adoption, configuration, console/CLI and interrupted operations |
| [Go controller prerelease](docs/go-controller.md) | Compiled installer, standalone deployment and local/SSH TUI |
| [Clients](docs/clients.md) | Subscriptions, transport choice, HAPP baseline and AmneziaWG exports |
| [Maintenance](docs/maintenance.md) | Source upgrades, rollback, consistent state backups and restore boundaries |
| [Troubleshooting](docs/troubleshooting.md) | Measurements, authenticated probes, observation and recovery |
| [Routing](docs/routing.md) | Server refusal policy, client bypass rules and verification |
| [Multi-node](docs/multinode.md) | Fleet setup, shared requirements and migration limitations |
| [Other apps](docs/other-apps.md) | Hosting HTTP services behind the same Traefik entrypoint |
| [Architecture](docs/architecture.md) | Components, ports, state ownership and failure boundaries |
| [Conventions](docs/conventions.md) | Development, invariants and their checks |

[.env.example](.env.example) documents deployment inputs;
[compatibility.json](compatibility.json) records the tested runtime versions.
Historical investigations are retained under `docs/internal/` and linked from
the relevant guides.

## License

[MIT](LICENSE).
