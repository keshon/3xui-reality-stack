# Routing policy

`scripts/routing.py` manages the local panel's Xray routing template and optional
HAPP subscription routing headers. Applying it can restart the shared runtime;
back up and review the current template before changing routing.

```bash
python3 scripts/routing.py --show
python3 scripts/routing.py --test
python3 scripts/routing.py --apply
python3 scripts/routing.py --remove
```

`--show` inspects the template. `--test` asks Xray's routing API for decisions;
it does not perform a real connection to every destination. `--apply` validates
category tokens, saves `xray-template.backup.json`, changes the template and
checks runtime startup. If startup fails, it attempts to restore the template
and restart the container. A failed recovery still requires operator attention.

These rules are local Xray policy. Do not assume applying them to the master
updates every remote node. MTProto runs under `mtg` and is outside Xray routing.
AmneziaWG routing depends on the panel's embedded forwarding integration; test
an actual client path before relying on policy enforcement for that transport.

## Server rules

| Policy | Behavior |
|---|---|
| BitTorrent | A protocol-matching rule sends detected traffic to `blocked` |
| Stack's own zone | `domain:BASE_DOMAIN` is exempted from Russian-destination refusal |
| Listed sites/IPs blocked in Russia | Exempted first and sent to Xray's `direct` outbound |
| Remaining configured Russian domain/IP categories | Sent to `blocked` |

Here, `direct` means the VPS connects to the destination. It does not mean the
user's device bypasses the tunnel. `blocked` means refusal, not a redirect to
the device's own network. Category lists are classification data, not proof of
an exhaustive geographic boundary.

Protocol matching requires inbound sniffing. Provisioning enables it with
`routeOnly`, so classification can guide routing without replacing the
requested destination address. An installed rule alone does not demonstrate
that real BitTorrent traffic is detected.

The policy uses `IPIfNonMatch` by default: unmatched names are resolved for IP
classification. `--apply --domain-strategy AsIs` changes that behavior. Review
the effect on destinations outside the domain categories before applying it.

## Category data

The script uses standard `geoip` categories and the panel's bundled
`geosite_RU.dat`/`geoip_RU.dat`, produced by
[runetfreedom/russia-v2ray-rules-dat](https://github.com/runetfreedom/russia-v2ray-rules-dat).
The actual bundled content depends on the deployed image/data files.
`--apply` validates requested tokens against the deployed files. Avoid copying
historical list sizes or memory measurements into a sizing guarantee.

## Client bypass rules

If the server refuses a destination, the client must send it directly from the
device for that site to work. This is separate from server routing.

For HAPP, preview and apply the subscription routing profile:

```bash
python3 scripts/routing.py --happ
python3 scripts/routing.py --happ --apply
```

The profile configures Russian/private categories and the deployment zone for
direct access, with other traffic through the proxy. Applying subscription
headers restarts the panel so it rereads settings. Refresh or re-add the
subscription, then verify the profile was imported in the actual client.
The script's [HAPP routing format](https://www.happ.su/main/dev-docs/routing)
is client-specific; do not copy its field names into an Xray config.

For clients that accept Xray-style category rules, place matching direct rules
above the proxy catch-all, using their supported UI/config format:

```text
geosite:tld-ru
geosite:category-ru
geoip:ru
```

Also bypass the stack's own DNS zone so subscription refresh and entrypoint
connections do not recurse through the tunnel. Category availability depends
on the client's data files; verify it rather than assuming another core uses
the same names.

AmneziaWG exports use full-tunnel `AllowedIPs`; client-side split tunnelling
must be configured separately. Telegram's MTProto proxy is also separate.

## Verify after a change

Run `--test` and inspect the requested versus actual routing decisions. Check
the deployment's public/admin hostnames, an intended refused destination and
an intended allowed destination. Then test actual transfers through the
relevant client profiles, including client-direct access. If the core does
not start, preserve logs and check the attempted template rollback before
making further changes.
