# Clients

Each person receives one subscription URL containing the enabled transports.
Import it into a compatible client and refresh it after configuration changes.
Keep the URL private: it grants access to the account's connection details.

## Choosing a transport

| Transport | Public endpoint | Use and dependency |
|---|---|---|
| VLESS + Reality + Vision | `443/tcp` | Start here; connects directly to Xray |
| Trojan + HTTPUpgrade | `443/tcp` | Alternative TCP path through Xray fallback and Traefik |
| Hysteria2 | `443/udp` | Compare on mobile/lossy paths; requires usable UDP |
| AmneziaWG | `AWG_PORT/udp` | Separate VPN client and whole-device tunnel |
| MTProto FakeTLS | `MTPROTO_PORT/tcp` | Telegram's own proxy setting |

Reality and Trojan share the external TCP socket; Trojan's `12812` is internal.
Transport diversity can help with protocol-specific failures. It does not
provide a second host or address. Performance depends on the client, path and
workload; compare real transfers on the intended network.

## HAPP baseline for Reality

For the Reality profile, start with:

- VLESS, Reality security and `flow=xtls-rprx-vision` from the subscription.
- Mux disabled.
- Fragmentation disabled.
- The published fingerprint, public key, short ID and server name unchanged.

Enabling fragmentation and Mux caused a failure in the reported Android HAPP
setup; disabling them restored the connection. This establishes a useful
baseline, not a claim that every client's fragmentation implementation fails.
Change one setting at a time and test a real transfer. Automatic profile
selection does not make an incompatible transport configuration work.

Refresh the subscription after an operator changes the published fingerprint.
App names alone do not identify the embedded Xray/uTLS versions. If Reality
alone fails, report both app and core versions where available.

## Trojan and Hysteria2

Keep HTTPUpgrade on HTTP/1.1. Adding `h2` to a custom Trojan profile's ALPN can
negotiate a protocol that this upgrade path does not support. Start from the
subscription-generated profile. Treat Mux as a client-specific setting to
evaluate after establishing a working baseline.

Hysteria2 needs UDP access to the published port. A client timeout alone cannot
distinguish a firewall, access-network filtering and a server-side failure.
Compare TCP transports and another network before choosing a recovery action.

## AmneziaWG and Telegram

AmneziaWG is published as a `vpn://` entry. Clients that do not support it may
omit it. The operator can export ordinary configuration files with:

```bash
python3 scripts/client-links.py --write --awg-dir awg
```

Import the intended account's export into an AmneziaWG-compatible client.
`AWG_MTU` controls the exported MTU; compare packet-size-sensitive failures
before changing it. MTProto uses the account's `tg://proxy` link in Telegram
and is not a general-purpose browser tunnel.

## Routing and reporting a failure

The server's [routing policy](routing.md) can refuse destinations that the
device should reach directly. Configure the client bypass rules there;
otherwise those sites can fail while other traffic works.

A latency-test result is not a complete transfer test. Open a real page or
download a file, then report the transport, ISP, approximate failure time,
app/core version, exact error and whether another transport/network works.
Leave account keys, subscription URLs and passwords out of reports.
Operators can correlate these observations using [Troubleshooting](troubleshooting.md).
