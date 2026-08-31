# xray-stack

A one-command, self-hosted proxy stack: **Xray owns `:443`, and the reverse
proxy sits behind it.** Five transports, one account per person, one
subscription URL, and a real website as cover.

```
:443/tcp ──> Xray (VLESS + Reality + Vision)
               ├── valid Reality client ──> tunnel
               └── anything else ────────> traefik:8443   (internal, PROXY v2)
                                             ├── PUBLIC_DOMAIN/        -> decoy site
                                             ├── PUBLIC_DOMAIN/<path>  -> trojan inbound
                                             ├── PANEL_DOMAIN          -> 3x-ui
                                             ├── TOOLS_DOMAIN          -> dashboard
                                             └── catch-all             -> decoy site
:443/udp ──> Xray Hysteria2 inbound (native since Xray 26.x)
:8443/tcp ─> MTProto (mtg, FakeTLS) — run by 3x-ui, not by Xray
:51820/udp > AmneziaWG (amneziawg-go, in-process) — also not Xray
:80/tcp  ──> Traefik (ACME + redirect)
```

The reverse proxy keeps doing everything it normally does — the panel, ACME,
whatever else you host. It just no longer sits in Reality's data path.

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

## Install

Requires a fresh Debian/Ubuntu host, a domain on Cloudflare (for DNS-01), and
A records for `PUBLIC_DOMAIN`, `PANEL_DOMAIN` and `TOOLS_DOMAIN` pointing at the
server — **DNS-only, not proxied.** A CDN proxy terminates TLS and does not
forward UDP, which breaks all four lanes.

```bash
git clone <this repo> /opt/xray-stack && cd /opt/xray-stack
cp .env.example .env
$EDITOR .env          # domains, email, Cloudflare token, USERS
sudo ./install.sh
```

`install.sh` is idempotent — re-run it after editing `.env`. It generates any
secrets you left blank and writes them back, installs Docker if missing, tunes
the kernel, opens the firewall, brings up the stack, waits for the wildcard
certificate, provisions every lane, collapses the accounts, verifies the result,
and prints one subscription URL per person.

## Lanes

| Lane | Protocol | Port | Notes |
|---|---|---|---|
| fast, TCP | VLESS + Reality + Vision | 443/tcp | Xray owns the socket |
| reliable, TCP | Trojan + httpupgrade | 12812 | behind the reverse proxy |
| fast, UDP | Hysteria2 | 443/udp | native Xray inbound since 26.x |
| Telegram | MTProto (FakeTLS) | 8443/tcp | `tg://proxy?…`, no VPN client needed |
| obfuscated UDP | AmneziaWG v1.5 | 51820/udp | run by the panel, not Xray; no NET_ADMIN needed |

Hand out one subscription URL per person; it carries every lane and updates
itself. Let the client (Happ, Hiddify, v2rayN) pick between them with `urltest`.

## Layout

```
install.sh                one-shot installer
bootstrap.sh              compose orchestration (idempotent)
traefik/                  reverse proxy, ACME, cert extraction
decoy/                    the site that fronts everything
xui/                      3x-ui + Xray
scripts/
  panel.py                3x-ui API client (all the quirks live here)
  provision-inbounds.py   creates the Xray lanes; --repair-hysteria
  add-mtproto.py          MTProto lane, fronting your own domain
  add-amneziawg.py        AmneziaWG v1.5 lane, obfuscation randomised per host
  configure-subscription.py
  consolidate-clients.py  one account per person, all lanes
  host-tune.sh            BBR + fq, buffers, tcp_mtu_probing
  triage.sh               read-only network diagnostics
docs/
  gotchas.md              things that accept config they cannot run
  reality.md              choosing target and serverNames
  diagnosing.md           three failure classes, one measurement
```

## Operating

```bash
# add a person: append to USERS in .env, then
sudo ./install.sh

# after ANY panel edit to the hysteria inbound (3x-ui strips a required field)
python3 scripts/provision-inbounds.py --repair-hysteria && docker restart 3x-ui

# rotate the tunnel hostname (wildcard cert means no new CT entry)
#   1. add an A record for the new name
#   2. change PUBLIC_DOMAIN in .env
#   3. ./bootstrap.sh, then update serverNames + host in the panel

# network diagnostics
bash scripts/triage.sh
```

**Read [docs/gotchas.md](docs/gotchas.md) before editing anything in the panel.**
Several components will accept a configuration they cannot run, and because it
is one Xray process, one bad inbound stops every lane at once.

## Not included

**Plain WireGuard.** Xray supports it, but the handshake is fixed-size with
recognisable constants and is widely blocked. AmneziaWG — which *is* included —
exists to fix exactly that, so use that instead.

## License

MIT. See [LICENSE](LICENSE).

This is infrastructure for running your own proxy server. What you do with it
is your responsibility and subject to your local law.
