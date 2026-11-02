#!/usr/bin/env bash
# Small host policy. --print previews it without changing kernel settings.
set -euo pipefail
case "${1:-}" in
  ""|--print) ;;
  *) echo 'usage: host-tune.sh [--print]' >&2; exit 1;;
esac

if [ "${1:-}" != --print ]; then
  [ "$(id -u)" = 0 ] || { echo 'run as root'; exit 1; }
fi
available=$(sysctl -n net.ipv4.tcp_available_congestion_control 2>/dev/null || true)
if [ "${1:-}" != --print ] && [[ " $available " != *' bbr '* ]]; then
  modprobe tcp_bbr 2>/dev/null || true
  available=$(sysctl -n net.ipv4.tcp_available_congestion_control 2>/dev/null || true)
fi

render() {
  cat <<'EOF'
# Managed by 3xui-reality-stack. Kernel defaults govern TCP memory,
# per-socket default buffers, connection tracking, queues and file limits.
# Buffer ceilings let QUIC request space; they do not allocate it per socket.
net.core.rmem_max = 33554432
net.core.wmem_max = 33554432
net.ipv4.tcp_mtu_probing = 1
EOF
  if [[ " $available " == *' bbr '* ]]; then
    printf '%s\n' 'net.core.default_qdisc = fq' 'net.ipv4.tcp_congestion_control = bbr'
  fi
}

if [ "${1:-}" = --print ]; then
  render
  exit 0
fi
CONF=/etc/sysctl.d/99-proxy-tuning.conf
render > "$CONF"
sysctl -p "$CONF" >/dev/null
echo "applied $CONF; settings removed from an older policy keep their current values until reset or reboot"
