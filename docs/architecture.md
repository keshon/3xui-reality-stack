# Architecture

The stack runs 3x-ui, its Xray core, Traefik and an nginx static site on one
Docker network named `proxy`. A host can be standalone, a master with local
traffic, or a traffic node managed by a master.

## Traffic paths

```text
443/tcp -> Xray
             |-- authenticated Reality -> outbound routing
             `-- fallback -> Traefik:8443 (TLS, PROXY v2)
                              |-- public hostname / -> static site
                              |-- Trojan path -> Xray:12812
                              |-- subscription paths -> panel:2096
                              |-- admin hostname/path -> panel:2053
                              `-- catch-all -> static site
443/udp -> Xray Hysteria2
MTProto TCP port -> mtg, supervised by the panel
AmneziaWG UDP port -> panel's embedded userspace implementation
80/tcp -> Traefik, redirect to public HTTPS
management TCP port -> Traefik:8443, independent of Xray
```

Reality owns the public TCP socket directly. Trojan uses the same public socket
and depends on Xray's fallback, Traefik and the internal Trojan inbound.
Hysteria2 uses a separate UDP socket, but shares the Xray process with Reality
and Trojan. MTProto and AmneziaWG are managed by the panel outside Xray.

## Listeners

| Listener | Owner | Exposure |
|---|---|---|
| `443/tcp` | Xray | Public Reality and TLS fallback |
| `443/udp` | Xray | Public Hysteria2 |
| `80/tcp` | Traefik | Public HTTP redirect/ACME entrypoint |
| `MTPROTO_PORT/tcp` | 3x-ui/mtg | Published MTProto port |
| `AWG_PORT/udp` | 3x-ui | Published AmneziaWG port |
| `MANAGEMENT_BIND:MANAGEMENT_PORT/tcp` | Traefik | Independent HTTPS; loopback by default on standalone/master, all interfaces on nodes |
| `8443/tcp` | Traefik | Container-side TLS entrypoint, also mapped through the management binding |
| `12812/tcp` | Xray | Internal Trojan HTTPUpgrade endpoint |
| `2053/tcp`, `2096/tcp` | 3x-ui | Internal panel and subscription services |

Port defaults and environment settings are in [.env.example](../.env.example).
The panel's internal `2053` and the default host management port `2053` are
different endpoints. Provider firewall rules must allow the external listeners.

## TLS and fallback

The Reality inbound uses `target: traefik:8443`,
`serverNames: [PUBLIC_DOMAIN]` and `xver: 2`. Traefik trusts PROXY protocol from
the configured Docker subnet. Changing one side of that contract can break
fallback TLS.

`PUBLIC_DOMAIN` resolves to this host and must be covered by the certificate
Traefik presents. The default DNS-01 configuration requests a wildcard for the
zone. The wildcard still publishes the zone in certificate transparency; it
does not create a separate certificate entry for every covered hostname.

Traefik terminates fallback HTTPS and routes HTTP to the site, subscriptions,
panel and Trojan service. Additional HTTP applications use the same entrypoint;
see [Other apps](other-apps.md). A valid fallback certificate and a working site
do not prove that an authenticated Reality connection works or guarantee
resistance to filtering.

## Accounts and subscriptions

3x-ui's unified client model attaches one account to multiple inbounds. The
stack applies Vision's flow to the Reality attachment and not the other lanes.
Subscriptions are served on `PUBLIC_DOMAIN`. In particular, AmneziaWG exports
derive their endpoint host from the subscription request; using the admin
hostname would expose it in client configuration.

The master publishes local and node links in one subscription. A subscription
is a credential and a configuration distribution mechanism; selecting or
failing over between transports is a client responsibility.

## State and control

The [Go controller](go-controller.md) uses the same runtime traffic paths and
managed configuration schema. Its reviews, operation journal and certificate
helper are described in [controller internals](internal/go-controller-plan.md).
The Python controller retains adoption, fleet management and broader changes.

| State | Owner |
|---|---|
| `.stack/config.json` | Desired managed configuration; literal secret values |
| `.stack/applied.json` | Last applied/adopted revision and generated-file hashes |
| `.stack/operation.json`, `operation.log` | Configuration progress and step log |
| `.stack/adoption.json` | Adoption checkpoint |
| `.env`, `users.txt`, `nodes.txt` | Legacy inputs, or generated deployment inputs under managed operation |
| `xui/db/` | Live panel database and associated panel state |
| `shared/certs/`, `traefik/data/` | Extracted certificates, ACME state and Traefik logs |
| `decoy/site/` | Operator-owned website |
| `.stack/upgrades/` | Source/service recovery points and consistent state snapshots |
| `.stack/diagnostics/` | Bounded diagnostic snapshots |

The UI and CLI share `stackctl.config.Store` and `stackctl.engine.Engine`.
Save validates local configuration. Plan compares it with the applied revision;
Apply renders deployment inputs and executes selected existing scripts.
Configuration, adoption, source upgrades, backups and Doctor coordinate through
the deployment lock. A saved draft is not deployed state.

Adoption verifies existing service/account state and records a managed baseline
without provisioning or restarting services. Source upgrades replace reviewed
source independently of account reconciliation. See [Running](running.md) and
[Maintenance](maintenance.md) for the supported operations and recovery paths.

## Panel integration contracts

`scripts/panel.py` owns panel authentication, response normalization and client
attachment operations. Integration details that matter when extending scripts:

- Client API `id` means the UUID string, not the numeric database row ID.
- `allowedIPs` is read as a comma-separated string and sent as a list.
- Inbound `settings` and `streamSettings` are serialized JSON on writes; reads
  can contain parsed objects. Use the shared normalization helpers.
- Password-authenticated panel writes require a session cookie and CSRF token.
  Traffic-node calls use scoped node-sync tokens.
- Preserve existing credentials and verify a merged account before deleting a
  duplicate. Account creation and token minting can have ambiguous outcomes
  after transport failures; read state before retrying.
- Hysteria2 requires version `2` in settings and the Hysteria transport settings.
  The pinned panel heals missing version fields in generated runtime config;
  reconciliation also repairs the stored representation.
- AmneziaWG requires a nested server block, an external interface and at least
  one attached client before it listens. The panel generates its server/peer
  parameters. This deployment uses the embedded netstack, without `NET_ADMIN`
  or `/dev/net/tun`.

The tested runtime boundary is recorded in
[compatibility.json](../compatibility.json).

## Failure boundaries

- An Xray failure stops Reality, Trojan's public entry path, Hysteria2 and
  public fallback HTTPS. The panel process can remain alive; the independent
  management listener is the repair path alongside SSH.
- A Traefik failure affects fallback HTTPS, panel HTTPS, subscriptions and
  Trojan. Diagnose authenticated Reality independently rather than inferring
  its state from the website.
- A panel/container failure can also affect its supervised MTProto and
  embedded AmneziaWG services.
- A shared host, address or network path remains a common failure domain across
  transports. Different ports/protocols can still be filtered differently.
- A master outage stops subscription updates. Already configured node tunnels
  can continue, subject to node health. See [Multi-node](multinode.md).

Checks distinguish the core process, fallback HTTPS and authenticated transfers.
Their scope and network vantage points are documented in
[Troubleshooting](troubleshooting.md).
