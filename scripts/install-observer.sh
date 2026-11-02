#!/usr/bin/env bash
# Install read-only measurements. No recovery actions, account creation or alerts.
set -euo pipefail
ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
[ "$(id -u)" = 0 ] || { echo 'run as root'; exit 1; }
case "$ROOT" in *[[:space:]%]*) echo 'observer install needs a path without whitespace or percent signs'; exit 1;; esac
cat > /etc/systemd/system/3xui-observe.service <<EOF
[Unit]
Description=3xui diagnostics and authenticated tunnel probes
After=docker.service network-online.target

[Service]
Type=oneshot
WorkingDirectory=$ROOT
ExecStart=/usr/bin/python3 $ROOT/scripts/observe.py
TimeoutStartSec=240
UMask=0077
Nice=10
EOF
cat > /etc/systemd/system/3xui-observe.timer <<'EOF'
[Unit]
Description=Measure 3xui health every five minutes

[Timer]
OnBootSec=3min
OnUnitActiveSec=5min
RandomizedDelaySec=15s

[Install]
WantedBy=timers.target
EOF
# copytruncate keeps the existing file descriptor usable without restarting
# Traefik. A small amount of logging can be lost during the copy/truncate race.
cat > /etc/logrotate.d/3xui-traefik <<EOF
$ROOT/traefik/data/logs/*.log {
    daily
    maxsize 20M
    rotate 5
    compress
    delaycompress
    missingok
    notifempty
    copytruncate
    su root root
}
EOF
systemctl daemon-reload
systemctl enable --now 3xui-observe.timer
echo 'Observer enabled; retains 288 snapshots (about 24 hours). No automatic restarts.'
