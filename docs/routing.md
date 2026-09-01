# Routing policy

Two rules, applied in Xray's routing table so they cover every lane at once —
including AmneziaWG, which reaches the internet through a loopback SOCKS5
inbound into Xray rather than around it.

```bash
python3 scripts/routing.py --show    # what is installed
python3 scripts/routing.py --test    # ask Xray where it would send things
python3 scripts/routing.py --apply   # install (validates, then rolls back on failure)
python3 scripts/routing.py --remove  # take it out again
```

## BitTorrent

Refused on every lane.

3x-ui's default template already ships `{protocol: [bittorrent], outboundTag:
blocked}`, but a rule like that can only fire if **sniffing is on** — Xray
classifies the protocol by reading the first payload. Every inbound here was
created with sniffing disabled, so for a while the config read as though
torrents were blocked while they ran untouched. `provision-inbounds.py` now
enables sniffing with `routeOnly`, which supplies the routing decision without
letting a sniffed name replace the address the client asked for.

## Russian destinations

**Refused**, so the tunnel provably never carries them and this server never
appears in a connection to a Russian host. Each device is expected to reach
them directly instead.

Rule order matters, and the first rule exists to protect the second from
itself:

| Rule | Matches | Goes to |
|---|---|---|
| `ru-blocked-proxy` | `ru-blocked`, `refilter` domains | `direct` — through the tunnel |
| `ru-blocked-proxy-ip` | `ru-blocked`, `ru-blocked-community` IPs | `direct` — through the tunnel |
| `ru-refuse` | `tld-ru`, the `category-*-ru` sets, `ru-available-only-inside` | `blocked` |
| `ru-refuse-ip` | `geoip:ru` | `blocked` |

Sites *blocked in* Russia are exempted first, so a later "this looks Russian"
rule can never swallow the traffic the tunnel exists to carry.

`tld-ru` does most of the work: `.ru`, `.su`, `.moscow`, `.tatar` and the
Cyrillic TLDs. The curated per-sector lists are worth having but are not
sufficient on their own — `geosite:category-ru` is 1104 domains and contains
neither `sberbank.ru` nor `gosuslugi.ru`.

`domainStrategy` is set to `IPIfNonMatch`, so a domain that matches no rule by
name is resolved and re-checked against `geoip:ru`. That catches a Russian
service on a non-Russian domain, at the cost of a DNS lookup for each new
name. `--apply --domain-strategy AsIs` turns that off.

### The data

`geosite_RU.dat` and `geoip_RU.dat` ship inside the panel image; they are the
[runetfreedom/russia-v2ray-rules-dat](https://github.com/runetfreedom/russia-v2ray-rules-dat)
build, which upstream refreshes every six hours. The bundled copies are only as
fresh as the panel image, so replace them in `/app/bin/` if you want current
lists.

Do **not** reach for `ru-blocked-all` or `antifilter-download`: 1.37 million
domains each. `ru-blocked` and `refilter` are ~75 000 and already cost this
panel about 100 MB of resident memory.

## The client side

The server refusing Russian destinations is only half of it — each device has
to send them somewhere. Without a bypass rule, a Russian site simply fails to
load while the VPN is on.

**Happ needs nothing done in the app.** It reads a routing profile straight
off the subscription response — 3x-ui sets `Routing-Enable: true` and
`Routing: happ://routing/onadd/<base64 json>` headers, and Happ applies the
profile when the subscription is added or refreshed. Install it once:

```bash
python3 scripts/routing.py --happ           # preview the profile
python3 scripts/routing.py --happ --apply   # install and verify the headers
```

Everyone then gets it by re-adding their subscription. The profile sends
`geosite:tld-ru`, `geosite:category-ru`, `geoip:ru` and the server's own zone
to `direct`, and everything else through the tunnel. Field names and
types are Happ's own, not Xray's — booleans are the strings `"true"`/`"false"`
([spec](https://www.happ.su/main/dev-docs/routing)).

The settings are read once when the subscription server starts, so `--apply`
restarts the panel; without that the headers keep coming back missing.

**v2rayN, Hiddify and anything else on Xray core** take the same three tokens
by hand, routed to `direct`, above any catch-all proxy rule:

```
geosite:tld-ru
geosite:category-ru
geoip:ru
```

They come from the standard `geosite.dat`/`geoip.dat` those clients already
bundle, so nothing extra needs installing.

**AmneziaWG** is the exception. Its config carries `AllowedIPs = 0.0.0.0/0,
::/0`, and excluding Russia would mean expanding that into several thousand
CIDR entries. Use the Amnezia client's own split-tunnelling, or simply accept
that Russian sites do not load while that tunnel is up — it is the reserve
lane, not the everyday one.

**Telegram** is unaffected. MTProto runs under `mtg`, not Xray, so no routing
rule applies to it.

## Do not block your own zone

If `BASE_DOMAIN` sits inside a category you block — any `.ru` zone is matched
by `geosite:tld-ru` — then the RU rules block the hostnames the deployment is
*reached by*: the subscription URL, the panel, and the MTProto proxy's FakeTLS
fronting name. A client already inside the tunnel then cannot reach the tunnel's own
endpoints — subscription refreshes fail and the Telegram proxy hangs on
connect, forever, with no error.

`routing.py` puts a `domain:BASE_DOMAIN` exemption ahead of every RU rule, and
`--test` asserts all four hostnames still resolve to `direct`. The Happ profile
sends that zone direct as well, so a client never reaches the entry point
through the tunnel at all.

## What to tell people

> If a Russian site will not open, the VPN is doing that on purpose. Turn it
> off for that site, or add the bypass rules once and forget about it.

## Verifying

`--test` asks Xray itself where it would send a destination, rather than
reading the rules back:

```
sberbank.ru      want=blocked  got=blocked    ok
meduza.io        want=direct   got=direct     ok
google.com       want=direct   got=direct*    ok
bittorrent       want=blocked  got=blocked    ok
```

A `*` means no rule matched, so the first outbound — `direct` — applies. That
is the normal path for most of the internet.

## Safety

`routing.py` writes the whole Xray template, and it is one process for every
lane: an unknown category name does not disable its own rule, it stops Xray and
takes all five lanes down. So `--apply` validates every token against the
bundled files first, backs the template up to `xray-template.backup.json`,
and — if Xray does not come back within ~30 seconds — restores the backup and
restarts the container by itself.
