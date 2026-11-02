# Implementation notes

Historical evidence preserved during the 2026-11-02 documentation revision.
The measurements below apply to the investigated deployments and versions.
Use [Troubleshooting](../troubleshooting.md) for current diagnostic procedures;
these examples do not establish universal filtering or compatibility rules.

## Observed Reality version mismatch

Reality is not backward compatible across major Xray versions. A client on
Xray 25.x cannot complete a handshake against a 26.x server — measured
in-datacenter with no censor in the path, failing identically with and without
Vision. If Reality "does not work" while other lanes do, check the client's
core version before blaming the network.

## Partial ClientHello observation

One lane dead, every other lane on the same address and port fine, no error
anywhere — not in the Xray log, not in the reverse proxy's, not on the client
beyond a timeout. Reality only.

```bash
tcpdump -i <wan> -n -s 300 -w /tmp/c.pcap 'tcp port 443'
tcpdump -nr /tmp/c.pcap -X 'tcp and host <client> and greater 100' | head -20
```

Read the first data packet the client sends. A TLS record header is
`16 03 01 <len16>`, so `16 03 01 06 f6` declares 1782 bytes of ClientHello
plus the 5-byte header. Compare that against what actually arrived:

```
client → 1382 bytes    record header says 1782
server → ack 1383      and then nothing, ever
```

The server ACKs what it got and waits for a continuation that never comes.
There is no RST, no FIN and no client retransmission — the flow simply stops,
and the client opens another one a second later and repeats.

**Why only Reality.** `fp=chrome` makes uTLS emit Chrome's post-quantum key
share, X25519MLKEM768, about 1.2 KB by itself. That pushes the ClientHello
past 1700 bytes, so it no longer fits in one segment. Nothing else here sends
one that large: trojan's TLS is the client's own stack, and Hysteria2 is QUIC.
A middlebox then only has to forward the first segment and drop the second.

**What to change.** `REALITY_FINGERPRINT` in `.env`, or on a live inbound:

```bash
python3 scripts/provision-inbounds.py --fingerprint safari
docker restart 3x-ui
```

`safari` and `ios` carry no post-quantum key share and fit in a single
segment. It is a client-side setting — the server only publishes it in the
subscription as `fp=` and never checks it — so clients must re-read the
subscription for it to take effect.

A fingerprint change does not clear already retained handshakes. See
[resilience.md](../resilience.md) for finite fallback deadlines and snapshots.

Two things this is *not*, and the same capture rules both out. If the address
were blocked, the SYN would not be answered and the other lanes would die
with it. If Reality were rejecting the handshake, the connection would fall
through to `target` and the client would receive a certificate chain — several
kilobytes back, not zero.


## ICMP and TCP observation

A worked example of getting this wrong. On one server:

```
ping -M do -s 1400 <telegram-ip>   ->  100% loss, no ICMP error   "black hole!"
curl -4 https://telegram.org/...   ->  96940 bytes in 0.053s at 1.7 MB/s
```

Both true at the same moment on the same path. The packet capture explains it:

```
us       -> host:  Flags [S],  options [mss 1460, ...]
host     -> us:    Flags [S.], options [mss 1240, ...]
```

The peer advertises a small MSS in its own SYN-ACK. **MSS is negotiated during
the handshake, before any data flows** — TCP obeys it from the first segment and
never consults ICMP. Zero ICMP packets appeared during the transfer because none
were needed. The failing ping was the peer dropping oversized ICMP echo as
policy, which says nothing about TCP.

`scripts/triage.sh` therefore tests with a real transfer and reads the
negotiated MSS; its ping-based section is kept only as a labelled footnote.


## Earlier integration findings

The following notes are preserved as evidence, including historical statements
that were later superseded. Their operational commands are not current guidance.

### Original integration notes

Things that cost real time to find. Most are cases where a component accepts a
configuration it cannot actually run.

---

## The panel's validation is weaker than Xray's runtime

3x-ui will happily save an inbound that Xray then refuses to start on. Because
it is a **single Xray process**, one bad inbound takes *every* lane down with
it — Reality, trojan, Hysteria2 and the decoy site all stop at once.

Validate risky changes offline first. Note that `run -test` only checks
**parsing** — it will report `Configuration OK` for a config that then fails at
server creation. Use a real `run` on a spare port:

```bash
docker exec 3x-ui /app/bin/xray-linux-amd64 run -c /tmp/candidate.json
```

After any inbound change, check:

```bash
docker logs --tail 40 3x-ui | grep -i 'failed to start'
```

---

## A routing rule that is present but can never fire

The panel's default template already carries

```json
{"type": "field", "protocol": ["bittorrent"], "outboundTag": "blocked"}
```

so the config reads as though torrents are blocked. They are not, unless
**sniffing is enabled on the inbound**. Xray decides `protocol` by reading the
first payload of a connection; with sniffing off there is nothing for the rule
to match, and it silently never applies.

This is the worst shape a gotcha can take — not a component refusing a
configuration, but one accepting a configuration that *looks* enforced and
does nothing. Nothing logs, and the rule is right there in the file.

`provision-inbounds.py` enables sniffing with `routeOnly`, which supplies the
routing decision without letting a sniffed name replace the address the client
asked for:

```bash
python3 scripts/provision-inbounds.py --sniffing && docker restart 3x-ui
```

The same trap applies to any protocol-matching rule. Verify by asking Xray
where it would actually send something rather than by reading the rules back —
`scripts/routing.py --test` does that. See [routing.md](../routing.md).

---

## amneziawg: works, but only once two conditions are met

Xray has no amneziawg implementation — put `protocol: amneziawg` into an Xray
config directly and it will not start. **But 3x-ui never puts it there.** The
panel embeds `github.com/amnezia-vpn/amneziawg-go` and runs instances
in-process, so the inbound never reaches the Xray config and cannot stop the
other lanes. Verified with an inbound created *and enabled*.

The settings shape is **nested**, and different from Xray's own wireguard
inbound:

```jsonc
"settings": {
  "server": {
    "privateKey": "...", "publicKey": "...",
    "subnetIp": "10.8.1.0", "subnetCidr": 24,
    "primaryDns": "1.1.1.1", "secondaryDns": "1.0.0.1",
    "externalInterface": "eth0",
    "jc": 4, "jmin": 88, "jmax": 211,
    "s1": 139, "s2": 26, "s3": 45, "s4": 21,       // s3/s4 are AWG v1.5
    "h1": "73443325-186265349", ...                // ranges, not scalars
    "i1": "<r 78>", "i2": "", ...                  // junk packet templates
    "headerProtectionKey": "<base64 32 bytes>",
    "contentPaddingAddition": "13-38",
    "rekeyAfterTime": "106-139", "rekeyTimeout": "3-5",
    "rejectAfterTime": "198-257", "keepaliveTimeout": "10-15",
    "maxHandshakeAttempts": "20-45",
    "randomTrailers": true, "disableCookies": true
  },
  "clients": []
}
```

**Two conditions gate it starting, and neither reports an error:**

1. `externalInterface` must be set (e.g. `eth0`).
2. At least one client must be attached.

With either missing, the panel stores the inbound, returns success, and
silently does nothing — no listener, no interface, no log line. With both
present:

```
INFO - amneziawgnet: started embedded interface awg9 for inbound 9
```

**It needs no `NET_ADMIN` and no `/dev/net/tun.`** "Embedded interface" means an
in-process userspace netstack — verified by removing both from the container and
confirming it still starts and listens. Only the published UDP port is required.

Attach accounts through the client API rather than hand-writing peers: each one
gets a keypair and a `/32` assigned automatically, and `settings.clients` is
repopulated from the attachment. Conversely, writing `"clients": []` wipes that
list and the generated `vpn://` link with it.

**The Endpoint leak.** The Endpoint in generated client configs is taken from
the host the *subscription* was fetched from, and `externalProxy` does **not**
override it. Serving subscriptions from the admin hostname therefore puts that
hostname into every user's config. This is why subscriptions are routed on
`PUBLIC_DOMAIN`, not `PANEL_DOMAIN`.

AmneziaWG is the WireGuard worth having: plain WG has a fixed-size handshake
with recognisable constants, which is exactly what the junk packets, magic
headers and randomised timings above exist to break up. Randomise those
parameters per deployment — shared constants across many servers would
themselves become the signature.

---

## The protocol dropdown lists more than Xray can run

The panel offers `vmess, vless, trojan, shadowsocks, hysteria, wireguard,
amneziawg, mtproto`. Three of those — `hysteria`, `wireguard` and the vless /
vmess / trojan / shadowsocks family — run inside Xray. Two do not:
`amneziawg` (panel-embedded, see above) and `mtproto` (the bundled `mtg`
binary). Both are excluded from the generated Xray config, so neither can crash
the core.

Check what your Xray build actually implements:

```bash
docker exec 3x-ui sh -c 'strings /app/bin/xray-linux-amd64' \
  | grep -oE 'xray-core/proxy/[a-z0-9]+' | sort -u
```

and what the panel runs out-of-band:

```bash
docker exec 3x-ui ls /app/bin/
docker exec 3x-ui sh -c 'strings /app/x-ui' | grep -o 'github.com/amnezia-vpn/[a-z-]*'
```

---

## Hysteria2: `settings.version` gets silently stripped

Xray's hysteria inbound needs **two** integer `version` fields:

```jsonc
"settings":       { "version": 2, "users": [...] }
"streamSettings": { "network": "hysteria",
                    "hysteriaSettings": { "version": 2 } }
```

3x-ui does not model `settings.version` in its web UI, so it drops the field
whenever you open and save the inbound there. Historically Xray then died with

```
proxy/hysteria: not hysteria transport
```

and took every other lane with it.

**On v3.7 that is survivable.** The panel now re-inserts the field when it
generates the Xray config — `HealHysteriaVersion` sets it unconditionally, for
both `settings` and `streamSettings.hysteriaSettings` — so a stripped value can
no longer stop the core. The stored row is still wrong, which matters if you
ever read it back or move to a panel without that healing:

```bash
python3 scripts/provision-inbounds.py --repair-hysteria
```

No restart needed: the reconciler goes through the panel API, and the panel
restarts Xray itself after an inbound update.

`network: "hysteria"` in streamSettings is also mandatory — without it you get
the same "not hysteria transport" error.

---

## One account per person, not one per protocol

3x-ui v3.7 has a unified client model. A single client row carries every
protocol's credential:

| field | used by |
|---|---|
| `uuid` | vless |
| `password` | trojan, shadowsocks |
| `auth` | hysteria |
| `privateKey` / `publicKey` / `allowedIPs` | wireguard |
| `secret` / `adTag` | mtproto |

`client_inbounds` maps that one client to as many lanes as you like, and
per-lane `flow` lives in `client_inbounds.flow_override` — so Vision stays on
for Reality and off elsewhere automatically.

This matters beyond tidiness: **with clients attached this way, one subscription
URL emits links for every lane.** Per-protocol clones give one link per URL, and
your users need four subscriptions instead of one.

---

## Client API quirks

- **`id` is the UUID**, as a string — *not* the numeric row id. Sending the row
  id writes that number into `uuid` and destroys the account.
- **`allowedIPs`** comes back from `get` as a comma-joined string but must be
  sent to `update` as a list. A client object cannot be round-tripped verbatim.
- **`list` is a GET.** Passing a body makes it a POST, which 404s.
- Inbound `settings` / `streamSettings` are JSON **strings** when you send them,
  but come back **already parsed** when you read them.
- `tgId` must be an integer.
- The API is CSRF-protected: `<meta name="csrf-token">` plus a session cookie.
  A plain POST gets 403.
- **Verify a merge before deleting the duplicate.** If the update fails and the
  duplicate is already gone, the credential is unrecoverable.

---

## Subscription URLs are wrong out of the box

`subURI` is empty by default, so 3x-ui builds links from `subPort` + `subPath`:

```
http://panel.example.com:2096/sub/<subId>
```

Unusable — port 2096 is container-internal, the scheme has no TLS, and `/sub/`
is not the path the reverse proxy forwards. `scripts/configure-subscription.py`
sets `subPath`/`subJsonPath` (what the panel serves) and `subURI`/`subJsonURI`
(what it prints).

Related: the panel's `webBasePath` **must** match the path prefix your reverse
proxy forwards, or every request 404s.

---

## httpupgrade breaks over HTTP/2

Traefik negotiates `h2` when a client offers it, and the httpupgrade path then
returns **502**; over `http/1.1` it returns **101**. Measured:

| client ALPN | result |
|---|---|
| none (what 3x-ui generates) | works — Xray defaults to http/1.1 |
| `http/1.1` | works |
| `h2, http/1.1` | dead |

The default is fine. But if anyone hand-edits a trojan config and adds `h2`,
that lane dies silently.

---

## MTProto fronting domain

3x-ui defaults the FakeTLS fronting domain to `www.cloudflare.com`, so clients
present `SNI=www.cloudflare.com` while connecting to your IP — an SNI/IP
mismatch that makes the server stand out. Issue secrets against your own
domain instead:

```bash
docker exec 3x-ui /app/bin/mtg-linux-amd64 generate-secret --hex www.example.com
```

The `ee` prefix marks FakeTLS. `scripts/add-mtproto.py` does this.

---

## Reality and the reverse proxy

`realitySettings.target` must be a host that genuinely serves the name in
`serverNames`. Point it at a third party and the fallback hands out *their*
certificate for *your* SNI — a one-connection giveaway. Point it at your own
reverse proxy, which holds a real certificate for that name.

Reality also dials `target` on **every** incoming connection, so a local target
is far faster than a remote one.

`xver: 2` sends PROXY protocol v2 to the target so real client IPs reach the
proxy's logs. It must match `proxyProtocol.trustedIPs` on the Traefik
entrypoint — change one without the other and handshakes break.

---

## Traefik details

- `entryPoints.<name>.http.redirections.entryPoint` has **no `port` field** in
  v3; `to` takes the port itself (`to=:443`).
- A **router-level** `tls.certresolver` overrides the entrypoint's
  `tls.domains`, so a wildcard declared at the entrypoint is silently ignored
  and you get one certificate per hostname. Configure TLS only at the
  entrypoint.
- Keep a finite `respondingTimeouts.readTimeout` (30s here): it also bounds
  incomplete TLS ClientHellos before HTTP routing. `writeTimeout=0` allows
  streaming responses; `idleTimeout=180s` bounds idle HTTP keep-alive sockets.
  HTTPUpgrade tunnels are hijacked after upgrade. Verify a transfer lasting
  longer than the read timeout before changing these settings.
- `ldez/traefik-certs-dumper` panics on Traefik 3.7 acme.json
  (`unsupported key type: ''` — it wants an `Account.KeyType` field Traefik no
  longer writes). `traefik/dump-certs.py` replaces it.

---

## Shell / deploy

- Do not `source` the `.env` file. Sourcing executes it as shell, so a value
  containing `$` — an apr1 password hash, for instance — gets expanded as a
  variable and either mangles the value or aborts under `set -u`. Parse it.
- Xray owns `:443`, and the panel is reached *through* Xray's Reality fallback.
  On a fresh box that is a chicken-and-egg: you cannot use the web UI to create
  the inbound that makes the web UI reachable. `scripts/provision-inbounds.py`
  talks to the panel over the container network instead.
