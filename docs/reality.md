# Reality: choosing `target` and `serverNames`

Reality's camouflage depends on three things agreeing: DNS, the certificate,
and the content behind it. Get any one wrong and the server is identifiable.

## `target` must serve the name in `serverNames`

Reality relays the client's ClientHello to `target` and relays the response
back. If `target` does not serve the name in `serverNames`, the client — and
anyone probing — gets a certificate for the wrong domain.

A real example of the failure, from a server configured with
`target: google.com:443` and `serverNames: [somedomain.example]`:

```
>>> ClientHello  SNI = somedomain.example
<<< Certificate  subject=CN=*.google.com   issuer=Google Trust Services
    Verification: OK
```

One connection, no heuristics, no false positives. Anyone scanning the address
space can confirm the host runs Reality.

## Prefer "steal from yourself"

Older guidance says `serverNames` should resolve somewhere other than your
server. That creates an SNI↔IP inconsistency, which passive DNS correlation
looks for:

```
serverNames domain -> 203.0.113.10   (someone else's host)
clients connect to -> 198.51.100.20  (yours)
```

Instead, point `serverNames` at a domain that genuinely resolves to this
server, and `target` at a local TLS terminator holding a real certificate for
it and serving real content. DNS, certificate and content all agree.

This deployment does that: `target: traefik:8443`, `serverNames:
[PUBLIC_DOMAIN]`, and a real static site behind it.

Two bonuses:

- **Latency.** Reality dials `target` on *every* incoming connection. A local
  target is ~0.1 ms instead of a round trip to a third party.
- **Probe resistance.** An active prober gets a valid certificate and a real
  page, from an IP that DNS says should serve exactly that.

The trade-off, stated honestly: you no longer borrow a third party's TLS
*server* fingerprint — probers see your terminator's stack. That is a far
weaker signal than a certificate/DNS mismatch.

## Hostname choice

- A generated-looking name (`a7f2-x9k`, `srv-4471`) is itself a signal. Pick
  something a human would plausibly pick.
- Issue a **wildcard** certificate via DNS-01. Every certificate you issue is
  published in Certificate Transparency logs, so per-hostname certificates let
  anyone enumerate your infrastructure at `crt.sh`. A wildcard leaks only the
  zone name — and lets you rotate the tunnel hostname in seconds without
  creating a new CT entry.
- Check what your zone already exposes: `https://crt.sh/?q=example.com`

## Still true

- `network: tcp` is the only transport compatible with `xtls-rprx-vision`.
- Vision makes the record layer match a browser — **but only if Xray owns the
  socket.** Behind a TCP passthrough, the proxy reassembles and re-emits the
  stream through its own buffers, destroying the on-wire packet sizing Vision
  constructs. You pay Vision's cost and get none of its benefit.
- `flow` must be set on the client too. A blank `flow` on a tcp+reality client
  silently disables Vision.
- Keep `shortIds` few. An unusual number of them is itself countable.

## Version compatibility

Reality is not backward compatible across major Xray versions. A client on
Xray 25.x cannot complete a handshake against a 26.x server — measured
in-datacenter with no censor in the path, failing identically with and without
Vision. If Reality "does not work" while other lanes do, check the client's
core version before blaming the network.
