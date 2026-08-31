#!/usr/bin/env bash
# Run ON THE VPS, once, as root. Idempotent.
#
# Kernel tuning for a proxy server on a long, lossy path. This is the cheapest
# meaningful win available for "slow as fuck" - BBR treats packet loss as noise
# rather than as a congestion signal, which is exactly right when the loss is
# being induced by a middlebox rather than by a full queue.

set -euo pipefail
[ "$(id -u)" = 0 ] || { echo "run as root"; exit 1; }

CONF=/etc/sysctl.d/99-proxy-tuning.conf
cat > "$CONF" <<'EOF'
# ── congestion control ───────────────────────────────────────────────────────
net.core.default_qdisc = fq
net.ipv4.tcp_congestion_control = bbr

# ── buffers: a 50ms RTT at 1Gbit needs ~6MB in flight ────────────────────────
net.core.rmem_max = 33554432
net.core.wmem_max = 33554432
net.ipv4.tcp_rmem = 4096 87380 33554432
net.ipv4.tcp_wmem = 4096 65536 33554432
net.ipv4.tcp_mem  = 786432 1048576 26777216

# UDP buffers - Hysteria2/QUIC will complain in its logs without these
net.core.rmem_default = 1048576
net.core.wmem_default = 1048576

# ── connection churn ─────────────────────────────────────────────────────────
# A proxy opens and closes a great many short connections.
net.ipv4.tcp_fin_timeout = 15
net.ipv4.tcp_max_syn_backlog = 8192
net.core.somaxconn = 8192
net.ipv4.tcp_max_tw_buckets = 500000
net.ipv4.tcp_tw_reuse = 1
net.ipv4.ip_local_port_range = 10000 65000
net.ipv4.tcp_slow_start_after_idle = 0

# ── path MTU ─────────────────────────────────────────────────────────────────
# Do not trust ICMP frag-needed to arrive; probe instead. Directly relevant to
# "text works, media does not".
net.ipv4.tcp_mtu_probing = 1

# ── misc ─────────────────────────────────────────────────────────────────────
net.ipv4.tcp_fastopen = 3
net.ipv4.tcp_notsent_lowat = 16384
fs.file-max = 1000000
EOF

sysctl -p "$CONF" >/dev/null
echo "applied $CONF"
echo "  tcp_congestion_control = $(sysctl -n net.ipv4.tcp_congestion_control)"
echo "  default_qdisc          = $(sysctl -n net.core.default_qdisc)"

if [ "$(sysctl -n net.ipv4.tcp_congestion_control)" != "bbr" ]; then
  echo
  echo "!! BBR did not take. Load the module and re-run:"
  echo "     modprobe tcp_bbr && echo tcp_bbr >> /etc/modules-load.d/bbr.conf"
fi

# file descriptors - Xray with many concurrent streams will hit the default
if ! grep -q '^\* *soft *nofile' /etc/security/limits.conf; then
  printf '* soft nofile 1000000\n* hard nofile 1000000\n' >> /etc/security/limits.conf
  echo "raised nofile limits (re-login or reboot to take effect)"
fi
