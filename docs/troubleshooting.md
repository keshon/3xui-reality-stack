# Troubleshooting

Start with the transport, client/core version, ISP and approximate failure time.
Compare a real transfer from the affected network with another transport and
network. SSH reachability does not establish the state of another port,
protocol or service.

## Narrow the failure

| Symptom | Possible causes | Next check |
|---|---|---|
| One person fails while another works | Client settings/version, destination or access-network path | Same profile baseline and target on both networks |
| Trojan works, Reality fails | Reality profile/flow/key/fingerprint, client version, handshake filtering | Disable Reality Mux/fragmentation; compare authenticated probes and captures |
| TCP works, Hysteria2 fails | UDP firewall, filtering/loss, Hysteria configuration | Provider rules, process/listener state and authenticated UDP transfer |
| All public transports fail, SSH works | Runtime/resource failure, firewall, selective port/protocol filtering | Independent management endpoint, Doctor, snapshots and another network |
| Small transfers work, larger ones stall | Loss, MTU/MSS issues, timeout, congestion or filtering | Sustained transfer, negotiated MSS, captures and host pressure |
| Client latency test fails but pages work | The test target or test method | Another real HTTPS target |

These patterns select the next measurement; they do not prove a cause. An
outage limited to one ISP can coexist with a later server-side failure.

## Server checks

Run from the deployed checkout on Linux as root:

```bash
python3 -m stackctl doctor
python3 scripts/diagnostics.py --write
python3 scripts/probe-tunnels.py
```

Doctor checks deployed panel/subscription configuration and shares process,
verified fallback HTTPS and authenticated-transfer checks with the observer
and source upgrader. It reads deployed inputs, not unapplied settings drafts.
The diagnostic command writes a private snapshot under `.stack/diagnostics/`.

The probe reads an existing enabled account's published subscription, starts a
temporary loopback Xray client and downloads at least 64 KiB over each expected
Reality, Trojan/HTTPUpgrade and Hysteria2 link. `PROBE_ACCOUNT` selects an existing
account; no account is created. Transfers consume that account's quota. Secrets
go through stdin rather than command arguments or output.

Local probes reuse a panel session in `.stack/probe-panel-session.cookies`
instead of logging in on every timer run. The cache is private (mode 600 on
Linux) and contains an authentication credential; do not share it. Expired
sessions are reauthenticated, so enabled panel login notifications can still
appear on the first check or after session expiry. Ordinary administrative
logins keep their existing behavior and notifications.

The default target is Telegram's widget script. Compare another target before
attributing a failure to the tunnel:

```bash
python3 scripts/probe-tunnels.py --url https://example.com/large-file --min-bytes 65536
python3 scripts/probe-tunnels.py --lane trojan --timeout 120 --rate 1k
bash scripts/healthcheck.sh
```

Choose a real HTTPS file large enough for `--min-bytes`; the example URL is a
placeholder. The slow Trojan probe checks a session longer than the fallback
read deadline. An open socket, fallback page and panel status are different
measurements from an authenticated transfer. MTProto/AmneziaWG handshakes remain
untested by these probes. Generic UDP timeouts are unverified.

## Compare network vantage points

A passing VPS-local probe cannot test the path through the affected ISP.
An external Linux host can use:

```bash
python3 scripts/probe-tunnels.py --links-file /private/links --xray /path/to/xray
```

The file contains decoded subscription URIs and must be protected like a
password. With both flags, Docker and panel access are unnecessary. Nothing
uploads the links automatically. Run from the affected network where possible;
record the client/core version and compare another network and target.

## Fallback TLS and DNS

```bash
curl -sI https://<PUBLIC_DOMAIN>/
openssl s_client -connect <SERVER_IP>:443 -servername <PUBLIC_DOMAIN> </dev/null 2>/dev/null \
  | openssl x509 -noout -subject -issuer
dig +short <PUBLIC_DOMAIN>
```

Confirm that the public name resolves to the intended host, the certificate
covers it and the site answers. `openssl s_client` exercises unauthenticated
fallback TLS, not Reality authentication. A reset or stall can originate in
filtering, a middlebox, a timeout or the server; correlate both ends.

## Reality-only failures

Start with the [client baseline](clients.md#happ-baseline-for-reality). Check
flow, key, short ID, name, app/core versions, Mux and fragmentation before
changing server configuration.

If a capture shows an incomplete TLS record, compare its declared record
length with the received TCP payload and retransmissions. Some fingerprints
produce larger ClientHellos; size and segmentation depend on the client/uTLS
version and path. A fingerprint experiment is evidence only when tested with
the same client/path. Refresh the subscription to receive a changed published
`REALITY_FINGERPRINT`; it is a client setting, not server authentication policy.

Packet captures contain peer addresses and traffic metadata. Keep them private
and bound the capture duration/size. Earlier measurements are preserved in
[implementation notes](internal/implementation-notes.md), including an observed
version mismatch; that observation is not a universal Xray version rule.

## Stalls and resource pressure

Collect process, memory, descriptor, connection tracking, disk and socket state
before restarting. Diagnostics record bounded counts and known error-message
counts without copying raw application logs or peer addresses. Missing tools
are reported as failed measurements.

`bash scripts/triage.sh` compares real transfers and negotiated MSS. ICMP echo
loss alone does not establish a TCP MTU failure. Avoid raising connection
tracking or memory limits without measuring exhaustion and available RAM.

A restart restoring access narrows the investigation but does not prove a
descriptor leak or kernel limit. Restart only the diagnosed component and
record which clients/protocols recovered. Restarting host networking over SSH
can remove the repair path; it is not a routine diagnostic step.

## Continuous observation

```bash
sudo bash scripts/install-observer.sh
systemctl status 3xui-observe.timer
journalctl -u 3xui-observe.service
```

The optional timer records diagnostics and authenticated probes every five
minutes, retaining 288 private JSON snapshots (about 24 hours). It never
restarts services or sends messages. A failed probe makes the service fail;
the target website can also be responsible, so compare another target.

## Host policy and deadlines

Traefik uses a 30-second read timeout, unlimited write timeout and 180-second
HTTP keep-alive idle timeout. The read deadline also bounds incomplete TLS
ClientHellos. HTTPUpgrade tunnels leave ordinary request handling after upgrade;
validate a long authenticated Trojan transfer when changing deadlines.

`bash scripts/host-tune.sh --print` previews QUIC buffer ceilings, TCP MTU
probing and BBR/fq where available. The policy leaves connection tracking,
TCP memory budgets, default socket buffers and global descriptor limits to the
kernel. A source upgrade does not apply sysctls. Removing old overrides from
the policy file leaves their current kernel values until reset or reboot.

The installer configures daily Traefik file-log rotation with a 20 MiB size
trigger and five rotations. Size is checked when logrotate runs; it is not a
hard cap. `copytruncate` can lose logging during rotation. Compose bounds
stdout logs to three 10 MiB files per configured service after recreation.

For updates, rollback and state recovery, use [Maintenance](maintenance.md).
