# Which lane to use, and when

All five lanes are in one subscription, so switching costs nothing and needs no
new credentials. What follows is how to choose between them, and what it means
when one of them stops.

## What actually differs

| Lane | Socket | Path inside the box | Independent of |
|---|---|---|---|
| VLESS + Reality + Vision | `IP:443/tcp` | Xray, directly | — |
| Trojan + httpupgrade | `IP:443/tcp` | Xray fallback → Traefik → :12812 | Reality-specific detection |
| Hysteria2 | `IP:443/udp` | Xray, directly | anything TCP |
| AmneziaWG | `IP:<AWG_PORT>/udp` | userspace netstack in the panel | UDP/443 blocking |
| MTProto | `IP:<MTPROTO_PORT>/tcp` | `mtg`, not Xray | Telegram only |

The column that matters is the last one. **Reality and Trojan share a socket** —
same address, same port, same SNI, same certificate. They are not two chances
against a blocked address; they are two chances against something that inspects
the TLS handshake. If the address itself is blocked, both die together, and so
does Hysteria2, and so does AmneziaWG. Surviving *that* takes a second server,
not a fifth protocol.

## Everyday use

**Reality is the default.** Xray owns `:443` and speaks to the client directly,
so it has no proxy hop and XTLS-Vision's record shaping survives intact. On a
healthy path nothing here beats it.

**Hysteria2 on mobile and on bad wifi.** Its congestion control treats packet
loss as noise rather than as a signal to back off, which is exactly right on a
lossy radio link or a long congested path. On a clean wired connection Reality
usually wins; the worse the path, the more Hysteria2 pulls ahead.

**Trojan is the TCP fallback, and it is the slowest of the three.** It rides
Reality's fallback into the reverse proxy and out again, so every byte crosses
two extra hops inside the box and its TLS is terminated by Traefik rather than
by Xray. Use it when Reality is being interfered with — not by preference.

**AmneziaWG is the last resort, and the only real VPN of the four.** The others
are proxies: apps that ignore system proxy settings are not covered. AmneziaWG
is a whole-device tunnel, so use it when you need traffic that a proxy cannot
carry — or when both `443/tcp` and `443/udp` are dead. It is also the slowest,
because it runs as an in-process userspace netstack rather than in the kernel.

**MTProto for Telegram, always, alongside whatever else you use.** It needs no
VPN client, and it keeps Telegram's traffic out of the tunnel entirely.

## Client settings that are not optional

- **Mux off for VLESS + `xtls-rprx-vision`.** Vision does its own stream
  handling; mux.cool on top destroys the record shaping it exists to provide.
- **Mux on for Trojan/httpupgrade.** Without it every browser connection pays a
  fresh TLS handshake through two hops.
- **Do not trust a latency test.** Clients measure by fetching through the
  profile, so a blank or `N/A` reading can mean the test target is unreachable
  on a working tunnel — and a good reading can sit on a lane that carries no
  data. Open a real page before believing either.

Automatic selection (`urltest`) is fine across Reality, Trojan and Hysteria2.
Keep AmneziaWG out of it — it is a separate app, and it is the lane you want
held in reserve rather than chosen for being briefly quickest.

## When something breaks, the pattern names the cause

| What still works | What it means |
|---|---|
| nothing, on every network | the address is blocked, or the server is down |
| everything on wifi, nothing on mobile | the carrier, not the server |
| Trojan yes, Reality no | Reality-specific detection — try another `fp` first, see [diagnosing.md](diagnosing.md) |
| both TCP lanes yes, Hysteria2 no | UDP/443 blocked or throttled |
| no UDP lane at all | UDP blocked broadly; TCP lanes only |
| AmneziaWG yes, everything else no | deep interference on `:443` itself |
| all lanes up but slow and stalling | MTU, not blocking — see [diagnosing.md](diagnosing.md) |

The last row is worth internalising, because it is the one that does not look
like a network problem. Blocking fails fast and completely. An MTU that is too
large fails only for packets big enough to hit it, so small requests succeed,
the tunnel appears healthy, and everything merely feels fragile.
