# Connection retention and outage diagnosis

The HTTPS entrypoint uses a 30-second read timeout, unlimited write timeout,
and 180-second HTTP keep-alive idle timeout. The read deadline also applies
while Traefik reads an incomplete TLS ClientHello. Removing it allows failed
handshakes to hold both Xray and fallback sockets indefinitely. HTTPUpgrade
tunnels leave HTTP request handling after the upgrade; validate long sessions
when changing timeouts.

## Authenticated checks

On the VPS:

```bash
python3 scripts/probe-tunnels.py
python3 scripts/probe-tunnels.py --lane trojan --timeout 120 --rate 1k
bash scripts/healthcheck.sh
```

The probe reads an existing enabled account's published subscription, starts a
temporary Xray client on loopback, and downloads at least 64 KiB over each
published Reality, Trojan/httpupgrade and Hysteria2 link. It checks TLS normally
and exits nonzero for failed or missing expected lanes. Credentials go through
stdin, never command arguments or output. Set `PROBE_ACCOUNT` in `.env` to pick
an existing account; no account is created. Probes consume that account's quota.
The default target is Telegram's widget script; use `--url` and `--min-bytes`
to compare another HTTPS target before blaming a failed transfer on the tunnel.

An external Linux host can run the same probe with `--links-file /private/links`
and `--xray /path/to/xray`. The links file contains the decoded subscription
URIs and must be protected like a password. It is never uploaded automatically.
The external host does not need Docker or panel access when both flags are used.

These checks do not implement MTProto or AmneziaWG clients. An open TCP port,
a UDP timeout, a panel status, and fallback HTTPS are distinct from a successful
authenticated transfer. A probe from the VPS cannot test a Russian ISP path.

## Observe before recovering

```bash
python3 scripts/diagnostics.py --write
sudo bash scripts/install-observer.sh
systemctl status 3xui-observe.timer
journalctl -u 3xui-observe.service
```

The optional systemd timer records a snapshot and authenticated probes every
five minutes. It retains 288 private JSON files in `.stack/diagnostics/`, about
24 hours. Snapshots include host and container connection tracking counts,
socket inactivity, process state, memory pressure, disk use, and counts of
known error messages. They exclude credentials and peer addresses. Raw
application logs are not copied. Missing diagnostic tools are recorded with a
failed exit status, rather than interpreted as a healthy measurement.

The observer never restarts services or sends messages. Its service exits
nonzero on a failed probe. A target website failure can also fail the probe;
compare another target and network before taking recovery action.

The installer also configures daily Traefik file-log rotation with a 20 MiB
size trigger and five rotations. `copytruncate` avoids restarting Traefik but
can lose a small amount of logging during rotation. The size is checked when
logrotate runs, not a hard cap. Compose bounds Docker stdout logs to three
10 MiB files per configured service, effective when that container is recreated.

## Upgrade an older deployment

Back up the old source, `.env`, account lists, certificate state and panel
database before replacing source files. Use SQLite's backup API for a live
database. Preserve all deployment data and custom Compose settings. Stage the
new source separately, validate Compose against the live environment, and test
an isolated Traefik instance with both a partial ClientHello and a Trojan
transfer longer than the deadline.

Recreate only Traefik for the timeout change. This briefly interrupts fallback
HTTPS and Trojan connections; authenticated Reality and UDP lanes should not
need a core restart. Do not run the full installer merely to update files: its
provisioning and account reconciliation are separate operations. Check account
and inbound inventories before and after. Keep the prior source and Compose
configuration available for rollback.
