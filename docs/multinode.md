# Multi-node

A master owns accounts and subscriptions and provisions three Xray transports
on registered traffic nodes: Reality, Trojan/HTTPUpgrade and Hysteria2. Each
host keeps Xray on public `443` and Traefik behind the TLS fallback. MTProto
and AmneziaWG remain local to the master in this integration.

## Roles and boundaries

| Role | Accounts | Traffic | Configuration endpoint |
|---|---|---|---|
| `standalone` | Local | Local | Local password-authenticated panel |
| `master` | Master-owned | Local plus node inbounds | Local panel and registered node-sync API endpoints |
| `node` | Supplied by master | Master-provisioned Xray lanes | Scoped node-sync token over independent management HTTPS |

Every node has its own Reality keys. The stack does not clone one Reality
private key across the fleet. The master publishes node links alongside its
local links in each person's subscription.

A master outage stops subscription updates. Clients with existing node
profiles can continue using healthy nodes. Nodes are not a distributed
subscription service, and this integration does not implement automatic
address replacement or client failover.

## Shared requirements

Before applying, check these settings on every host:

- Matching `BASE_DOMAIN`/`CERT_BASENAME` and certificate paths for the fleet.
- The same `TROJAN_PATH`, matching each node's Traefik route.
- The pinned panel/core versions in [compatibility.json](../compatibility.json).
- Distinct public/admin hostnames for each node and DNS-only traffic records.
- Public traffic ports and an independent management port reachable from the
  master. Restrict management access to the master's address where possible.

The console validates local configuration and node URL/domain inputs. It
cannot inspect remote Traefik configuration through the node-sync API; the
operator must verify the shared settings.

## Add a fresh node

Use the [running guide](running.md) to clone the repository on the new VPS.
Open `bash stack.sh`, choose **Traffic node**, enter its hostnames and shared
fleet settings, then Save, Review and Apply.

The node setup starts the panel, site and Traefik, obtains certificate state
and records a node-sync token. The master supplies traffic lanes later.
Management HTTPS is independent of Xray, so it can work before those lanes
exist. On nodes, `MANAGEMENT_BIND` defaults to `0.0.0.0` and
`MANAGEMENT_PORT` to `2053`.

Register this endpoint on the master, including the node's actual panel path:

```text
https://<PANEL_DOMAIN>:2053/<PANEL_PATH>
```

Use the generated node token from the private deployment inputs; transfer it
through a password manager or another private channel. Treat the URL path and
token as credentials. Keep the node's management port open to the master in
both the host and provider firewalls.

On the master, open **Nodes**, add the name, management URL, token and public
domain, Save, then Review / Apply. The engine registers the node, creates its
lanes, attaches accounts and verifies published configuration. Each VPS must
be installed independently; adding a master token cannot bootstrap a host.

## Verify

Run on the master:

```bash
python3 scripts/nodes.py list
python3 scripts/nodes.py test <name>
python3 -m stackctl doctor
bash scripts/healthcheck.sh
```

Node status and management HTTPS verify control-plane access. Authenticated
probes exercise the links published for the selected account; inspect their
per-link results rather than inferring that every protocol/node was tested.
A UDP timeout alone remains unverified. MTProto and AmneziaWG protocol
handshakes are outside the current probe coverage.

Test from the affected access network as well. A master-local transfer does
not demonstrate reachability from another ISP. See
[Troubleshooting](troubleshooting.md#compare-network-vantage-points).

## Account changes

Managed account additions fan out through Review / Apply. All configured users
are attached to all configured nodes; there is no per-node assignment policy.
The stack does not implement a separate aggregate fleet quota coordinator.

Fleet-wide user removal is blocked by the managed controller because remote
revocation is not confirmed end to end. Perform and verify revocation on each
relevant node during planned maintenance. Removing a name from a legacy input
list alone does not revoke an existing account.

## Replacement and removal

Managed node removal, renaming, endpoint replacement, deployed role changes
and domain migrations are not supported by automatic Apply. Source upgrades
also stop for traffic-node hosts; coordinated node updates require separate
maintenance. Do not edit generated `nodes.txt` to bypass these guards.

Plan a replacement as a migration: preserve current configuration/state, bring
up a replacement with a distinct node name and fresh keys, register it, then
verify its subscriptions and real transfers before retiring any old path.
Retirement requires explicit panel/inbound cleanup and confirmation that
clients have refreshed. Registration alone is not a complete replacement.

Reusing a name can leave `<name>-*` inbounds attached to the old panel node ID.
`provision-inbounds.py` refuses that ownership conflict. Removing and re-adding
a registration is not guaranteed to restore the original IDs or attachments;
there is no automatic rollback for this migration.

The [historical replacement procedure](internal/legacy-node-replacement.md)
is retained as implementation evidence and must not be used as a current
managed-deployment runbook. Use [Maintenance](maintenance.md) for supported
source rollback and consistent state backups. Full master recovery and fleet
migration remain manual operations.
