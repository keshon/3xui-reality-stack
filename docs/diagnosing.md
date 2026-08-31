# Diagnosing

Three failure classes, opposite fixes. One measurement separates them. Run it
**from inside the restricted network** — from anywhere else it tells you
nothing about the censor, including from behind another VPN.

```bash
openssl s_client -connect <SERVER_IP>:443 -servername <PUBLIC_DOMAIN> -tls1_3 -msg
```

| What you see | Class | What to fix |
|---|---|---|
| RST immediately after ClientHello | SNI / IP filtering | domain and IP choice; protocol changes will not help |
| Handshake OK, RST after ~5–50 KB | TLS-in-TLS behavioural detection | VLESS Encryption, padding, or move to QUIC |
| Handshake OK, no RST, throughput strangles | ASN / IP-range throttling | change hosting provider; nothing on the server helps |

Note this only exercises Reality's **fallback** path — `openssl`'s ClientHello
has no browser fingerprint and carries no Reality auth, so the server relays it
to `target`. To exercise the real path, run an Xray client with
`"log": {"loglevel": "debug"}` and read where it stops.

## Post-deploy checks

```bash
# the decoy answers on the hostname clients use
curl -sI https://<PUBLIC_DOMAIN>/ | head -1

# THE regression test: Reality's fallback must serve YOUR certificate.
# If it serves someone else's, target and serverNames have drifted apart and
# the server is identifiable in a single connection.
openssl s_client -connect <SERVER_IP>:443 -servername <PUBLIC_DOMAIN> </dev/null 2>/dev/null \
  | openssl x509 -noout -subject -issuer

# a bare-IP scan gets a valid certificate, not the proxy's default self-signed one
openssl s_client -connect <SERVER_IP>:443 </dev/null 2>/dev/null \
  | openssl x509 -noout -subject

# DNS agrees with the IP being dialled
dig +short <PUBLIC_DOMAIN>
```

## Tunnel lifetime — the "latency" that is not latency

Short-lived tunnel connections read as lag but are actually constant teardown
and re-handshake. At 50 ms RTT every new connection costs ~150 ms before a byte
moves, and a page load opens dozens.

```bash
awk '/trojan-hu@docker/ {print $NF}' traefik/data/logs/access.log \
  | tr -d 'ms' | sort -n | awk '{a[NR]=$1} END {print "n="NR, "median="a[int(NR/2)]"ms"}'
```

A median in the low seconds means something is cutting connections. Check the
reverse proxy's `respondingTimeouts` first, then whether client-side mux is on.

**Mux, and the direction matters:**

- **Trojan / httpupgrade / ws / xhttp** → mux **ON**. Without it every browser
  connection pays a full TLS handshake.
- **VLESS + `xtls-rprx-vision`** → mux **OFF**. Vision does its own stream
  handling; mux.cool on top defeats the record shaping it exists to provide.

## `ping -M do` does not measure what TCP does

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

## Comparing servers

Run `scripts/triage.sh` on each and diff. If two servers run an identical
configuration and behave differently, the difference is **network** — IP
reputation, peering, or routing — not configuration, and no amount of protocol
work will fix it.
