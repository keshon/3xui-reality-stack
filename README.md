# 3xui-reality-stack

A one-command, self-hosted proxy stack: **Xray owns `:443`, and the reverse
proxy sits behind it.** Five transports, one account per person, one
subscription URL each, and a real website as cover.

It is built for a handful of people you know — a household, a few friends —
where you control the server and the domain, and want the thing to keep working
without anyone becoming an administrator. It is not a multi-tenant service:
there is no billing, no signup, no quota enforcement, and accounts are added by
editing a text file and re-running the installer.

```
:443/tcp ──> Xray (VLESS + Reality + Vision)
               ├── valid Reality client ──> tunnel
               └── anything else ────────> traefik:8443   (internal, PROXY v2)
                                             ├── PUBLIC_DOMAIN/           -> decoy site
                                             ├── PUBLIC_DOMAIN/TROJAN_PATH -> trojan inbound
                                             ├── PUBLIC_DOMAIN/SUB_PATH   -> subscriptions
                                             ├── PANEL_DOMAIN             -> 3x-ui panel
                                             ├── TOOLS_DOMAIN             -> dashboard
                                             └── catch-all                -> decoy site
:443/udp ──> Xray Hysteria2 inbound (native since Xray 26.x)
:MTPROTO_PORT ─> MTProto (mtg, FakeTLS) — run by 3x-ui, not by Xray
:AWG_PORT/udp ─> AmneziaWG (amneziawg-go, in-process) — also not Xray
:80/tcp  ──> Traefik (ACME + redirect)
```

Subscriptions are served from `PUBLIC_DOMAIN`, not the admin hostname. The
AmneziaWG client config takes its endpoint from whichever host the subscription
was fetched from, so serving them from `PANEL_DOMAIN` would put the admin
hostname into every user's config.

The reverse proxy keeps doing everything it normally does — the panel, ACME,
whatever else you host. It just no longer sits in Reality's data path. To put
your own sites behind it, see [docs/other-apps.md](docs/other-apps.md).

## Why this shape

**Reality must own the socket.** XTLS-Vision's whole job is making the record
layer match a browser. Behind a TCP passthrough the proxy reassembles and
re-emits the stream through its own buffers, destroying the packet sizing
Vision constructs — you pay its cost and get none of its benefit.

**Reality falls back to your own reverse proxy.** `target: traefik:8443` with
`serverNames: [PUBLIC_DOMAIN]`, where that name really resolves here and really
has a Let's Encrypt certificate and a real site. DNS, certificate and content
all agree, so probing gets a coherent answer. Pointing `target` at a third
party makes the fallback hand out *their* certificate for *your* SNI — a
one-connection giveaway. See [docs/reality.md](docs/reality.md).

**One wildcard certificate.** Every certificate you issue is public in
Certificate Transparency logs, so per-hostname certs let anyone enumerate your
infrastructure at `crt.sh`. A wildcard leaks only the zone name — and lets you
rotate the tunnel hostname in seconds with no new CT entry.

**A real site at the catch-all.** Unknown Host headers and bare-IP scans get a
page, not a 404 and not a default self-signed certificate.

## Requirements

- A fresh Debian or Ubuntu host with root. 1 GB RAM is enough; the panel plus
  the geo lists sit around 200 MB.
- A domain whose DNS is on Cloudflare, and an API token scoped to
  **Zone → DNS → Edit** on that one zone. DNS-01 is what issues the wildcard.
- A records for `PUBLIC_DOMAIN`, `PANEL_DOMAIN` and `TOOLS_DOMAIN` pointing at
  the server, **DNS-only — grey cloud, not orange.** A CDN proxy terminates TLS
  and does not forward UDP, which breaks every lane at once and looks like a
  configuration bug rather than a DNS setting.
- Ports open in **your provider's firewall**, which is separate from the host's
  own. `443/udp` is the one most often closed by default, and Hysteria2 then
  fails with no error on either side.

## Install

```bash
git clone https://github.com/keshon/3xui-reality-stack.git /opt/3xui-reality-stack
cd /opt/3xui-reality-stack
cp .env.example .env
cp users.txt.example users.txt
$EDITOR .env          # domains, email, Cloudflare token
$EDITOR users.txt     # one account per line
sudo ./install.sh
```

Before the first run, **replace the contents of `decoy/site/`**. That site is
load-bearing, not decoration: it is what Reality's fallback serves, what an
active prober sees, what Hysteria2 masquerades as, and what unknown-host and
bare-IP scans get. The shipped pages are placeholder prose, and a site that
reads as filler undoes the rest of the design. A static export of anything you
genuinely have works; what matters is that it does not look synthetic and stops
changing once deployed.

`install.sh` is idempotent — re-run it after editing `.env`. It generates any
secrets you left blank and writes them back, installs Docker if missing, tunes
the kernel, opens the firewall, brings up the stack, waits for the wildcard
certificate, provisions every lane, collapses the accounts, verifies the result,
and prints one subscription URL per person. Most of the wait is DNS-01
propagation. If a check fails it stops there rather than continuing into a
broken state.

Give one person the subscription for a day before handing it to everyone. A
single clear report beats five people describing different symptoms.

## Configuration

Everything lives in `.env`, which is documented inline in
[`.env.example`](.env.example). The values worth knowing about:

| Setting | What it decides |
|---|---|
| `USERS`, `USERS_FILE` | Who gets an account. `users.txt` wins if present; `install.sh` writes the resolved list back into `USERS=` so the two cannot disagree. |
| `LANES` | Which lanes each account is attached to. |
| `REALITY_FINGERPRINT` | Which browser's ClientHello Reality clients imitate. `chrome` blends in best in principle, but current Chrome sends a post-quantum key share that pushes the ClientHello past one TCP segment — and a middlebox that drops the second segment kills Reality alone while every other lane keeps working. `safari` and `ios` fit in one segment. See [docs/diagnosing.md](docs/diagnosing.md). |
| `AWG_MTU` | Tunnel MTU written into every AmneziaWG client config. Defaults to 1280. Too high does not fail cleanly — small requests fit and large ones stall. |
| `AWG_PORT` | Not 51820. The AmneziaWG payload is obfuscated; the port number is not. |

Account names are `a-z`, `0-9` and `_`, up to 24 characters — no `-`, because
client emails are namespaced per lane as `r-<name>` / `t-<name>` / `h-<name>`
and the consolidation step splits on the first `-` to recover the person. Check
a list without deploying:

```bash
python3 scripts/resolve-users.py --list
```

## Lanes

| Lane | Protocol | Port | Notes |
|---|---|---|---|
| fast, TCP | VLESS + Reality + Vision | 443/tcp | Xray owns the socket |
| reliable, TCP | Trojan + httpupgrade | 12812 | behind the reverse proxy |
| fast, UDP | Hysteria2 | 443/udp | native Xray inbound since 26.x |
| Telegram | MTProto (FakeTLS) | `MTPROTO_PORT` | `tg://proxy?…`, no VPN client needed |
| obfuscated UDP | AmneziaWG v1.5 | `AWG_PORT`/udp | run by the panel, not Xray; no `NET_ADMIN`, no `/dev/net/tun` |

Hand out one subscription URL per person; it carries every lane and updates
itself. Which lane to actually use, and what it means when one stops, is in
[docs/choosing-a-lane.md](docs/choosing-a-lane.md).

AmneziaWG is the exception worth knowing about. It rides in the subscription as
a `vpn://` entry, which only the Amnezia clients understand — Happ, v2rayN and
Hiddify skip it silently, so the lane looks absent. `scripts/client-links.py`
decodes it back into an ordinary WireGuard-style `.conf` for the AmneziaWG app.

## Operating

```bash
# add a person: append a line to users.txt, then
sudo ./install.sh
# (removing a line only stops re-creation; delete the client in the panel
#  to actually revoke access)

# after ANY panel edit to the hysteria inbound (3x-ui strips a required field)
python3 scripts/provision-inbounds.py --repair-hysteria && docker restart 3x-ui

# rotate the tunnel hostname (wildcard cert means no new CT entry)
#   1. add an A record for the new name
#   2. change PUBLIC_DOMAIN in .env
#   3. ./bootstrap.sh, then update serverNames + host in the panel

# what to hand each person (writes CLIENT-LINKS.txt, mode 600)
python3 scripts/client-links.py --write --awg-dir awg

# routing policy: no BitTorrent, no Russian destinations
python3 scripts/routing.py --apply
python3 scripts/routing.py --test

# network diagnostics
bash scripts/triage.sh
```

**Read [docs/gotchas.md](docs/gotchas.md) before editing anything in the panel.**
Several components will accept a configuration they cannot run, and because it
is one Xray process, one bad inbound stops every lane at once.

## Documentation

| | |
|---|---|
| [docs/choosing-a-lane.md](docs/choosing-a-lane.md) | Which lane to use, and what the pattern of failures tells you |
| [docs/routing.md](docs/routing.md) | The routing policy: no BitTorrent, no Russian destinations |
| [docs/gotchas.md](docs/gotchas.md) | Components that accept configuration they cannot run |
| [docs/reality.md](docs/reality.md) | Choosing `target` and `serverNames` |
| [docs/diagnosing.md](docs/diagnosing.md) | Post-deploy checks, and reading failures that log nothing |
| [docs/other-apps.md](docs/other-apps.md) | Putting your own sites behind the same proxy |

## Layout

```
install.sh                one-shot installer
bootstrap.sh              compose orchestration (idempotent)
.env.example              every setting, documented inline; copy to .env
users.txt.example         the account list; copy to users.txt
traefik/                  reverse proxy, ACME, cert extraction
decoy/                    the site that fronts everything
xui/                      3x-ui + Xray
scripts/
  panel.py                3x-ui API client (all the quirks live here)
  resolve-users.py        users.txt + USERS= -> one validated list
  client-links.py         a subscription, unpacked into per-lane entries
  routing.py              the routing policy; --test asks Xray to prove it
  provision-inbounds.py   creates the Xray lanes; --repair-hysteria
  add-mtproto.py          MTProto lane, fronting your own domain
  add-amneziawg.py        AmneziaWG v1.5 lane, parameters generated by the panel
  configure-subscription.py
  consolidate-clients.py  one account per person, all lanes
  host-tune.sh            BBR + fq, buffers, tcp_mtu_probing
  triage.sh               read-only network diagnostics
```

## Not included

**Plain WireGuard.** Xray supports it, but the handshake is fixed-size with
recognisable constants and is widely blocked. AmneziaWG — which *is* included —
exists to fix exactly that, so use that instead.

## License

MIT. See [LICENSE](LICENSE).

This is infrastructure for running your own proxy server. What you do with it
is your responsibility and subject to your local law.
