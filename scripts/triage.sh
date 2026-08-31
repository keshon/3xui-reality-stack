#!/usr/bin/env bash
# Run ON THE VPS as root. Read-only, changes nothing. ~1 minute.
#
#   WinSCP: drag into /tmp/, then in the terminal:
#     bash /tmp/triage.sh 2>&1 | tee /tmp/triage-$(hostname).txt
#
# Diagnoses "Telegram text works but photos/video do not", and the usual
# suspects behind a slow tunnel. Run on every server and diff the output.
#
# NOTE ON METHOD: an earlier version of this script diagnosed path MTU with
# `ping -M do` and got it badly wrong. Telegram silently drops oversized ICMP
# echo as policy, which looks identical to a PMTU black hole but has no effect
# on TCP whatsoever - because Telegram advertises `mss 1240` in its SYN-ACK,
# and MSS negotiation settles the segment size in the handshake before any
# data flows. ICMP is never consulted. So this script now tests what actually
# carries your traffic: a real TCP transfer, plus the negotiated MSS.
# Ping-based MTU numbers are printed last, clearly marked as unreliable.

set -u
b()    { printf '\n\033[1m── %s\033[0m\n' "$1"; }
ok()   { printf '  \033[32mOK\033[0m    %s\n' "$1"; }
bad()  { printf '  \033[31mBAD\033[0m   %s\n' "$1"; }
warn() { printf '  \033[33mWARN\033[0m  %s\n' "$1"; }
inf()  { printf '        %s\n' "$1"; }

TG_WEB_URL='https://telegram.org/js/widget-frame.js'   # ~95 KB, real file
TG_HOST=telegram.org
CF_URL='https://speed.cloudflare.com/__down?bytes=10000000'
DCS="149.154.167.51 149.154.164.250 91.108.56.130"

IFACE=$(ip route show default 2>/dev/null | awk '{print $5; exit}')
GW=$(ip route show default 2>/dev/null | awk '{print $3; exit}')

# ─────────────────────────────────────────────────────────────────────────────
b "1. TELEGRAM: does bulk data actually move?  (the real test)"
inf "Text messages are tiny; photos and video are bulk. If bulk moves, the"
inf "'text works but media does not' symptom is NOT on this leg of the path."
echo

got4=0; got6=0
for af in 4 6; do
  out=$(curl -$af -sL -m 25 -o /dev/null \
        -w '%{http_code} %{size_download} %{time_total} %{speed_download} %{remote_ip}' \
        "$TG_WEB_URL" 2>/dev/null) || out="000 0 0 0 -"
  set -- $out; code=$1; sz=$2; t=$3; sp=$4; rip=$5
  [ "$af" = 4 ] && got4=$sz || got6=$sz
  if [ "$code" = "200" ] && [ "${sz:-0}" -gt 50000 ]; then
    ok  "IPv$af  ${sz} bytes in ${t}s  ($(( ${sp%.*} / 1024 )) KiB/s)  via $rip"
  elif [ "$code" = "000" ]; then
    bad "IPv$af  no connection / timed out  (this WOULD be a real problem)"
  else
    warn "IPv$af  http=$code size=$sz  - unexpected, check the URL"
  fi
done
echo
if [ "${got4:-0}" -gt 50000 ] || [ "${got6:-0}" -gt 50000 ]; then
  ok "bulk transfer to Telegram works. Media failures are NOT a path problem here."
  inf "Look at the client side instead: mux, and the tunnel's own MTU."
else
  bad "bulk transfer to Telegram fails on both families - this IS your bug."
fi

# ─────────────────────────────────────────────────────────────────────────────
b "2. NEGOTIATED MSS  (what TCP actually agreed to, not what ping guesses)"
if command -v tcpdump >/dev/null 2>&1; then
  TIP=$(getent ahostsv4 "$TG_HOST" 2>/dev/null | awk '{print $1; exit}')
  if [ -n "${TIP:-}" ]; then
    tcpdump -i "${IFACE:-any}" -n -c 20 "host $TIP and tcp[tcpflags] & tcp-syn != 0" \
            -w /tmp/.mss.pcap >/dev/null 2>&1 &
    td=$!; sleep 1
    curl -4 -sL -m 15 -o /dev/null "$TG_WEB_URL" >/dev/null 2>&1
    sleep 1; kill $td 2>/dev/null; wait $td 2>/dev/null
    tcpdump -r /tmp/.mss.pcap -n 2>/dev/null | grep -oE 'mss [0-9]+' | sort -u |
      while read -r _ m; do inf "advertised mss $m  (= MTU $((m+40)))"; done
    rm -f /tmp/.mss.pcap
    inf ""
    inf "Telegram advertising a small mss is NORMAL and self-correcting."
    inf "TCP obeys it from the first data packet. Nothing to fix."
  fi
else
  warn "tcpdump not installed - skipping.  apt -y install tcpdump"
fi

# ─────────────────────────────────────────────────────────────────────────────
b "3. ADDRESS FAMILY: which is faster, and which gets picked?"
for af in 4 6; do
  sp=$(curl -$af -s -m 40 -o /dev/null -w '%{speed_download}' "$CF_URL" 2>/dev/null)
  [ -n "${sp:-}" ] && [ "${sp%.*}" -gt 0 ] 2>/dev/null \
    && printf '  IPv%s  %s MB/s\n' "$af" "$(( ${sp%.*} / 1048576 ))" \
    || printf '  IPv%s  no throughput\n' "$af"
done
picked=$(curl -sL -m 10 -o /dev/null -w '%{remote_ip}' "$TG_WEB_URL" 2>/dev/null)
inf "unforced, the resolver picks: ${picked:-unknown}"
case "${picked:-}" in
  *:*) warn "IPv6 is preferred. If it is the slower column above, force IPv4:"
       inf "  freedom outbound  domainStrategy: UseIPv4   (see docs/inbounds.md)" ;;
  *)   ok "IPv4 is preferred." ;;
esac

# ─────────────────────────────────────────────────────────────────────────────
b "4. MTProto DC reachability  (the connections text messages ride on)"
for ip in $DCS; do
  ct=$(curl -4 -s -m 6 -o /dev/null -w '%{time_connect}' "http://$ip:443/" 2>/dev/null)
  if [ -n "${ct:-}" ] && [ "${ct%.*}" -lt 5 ] 2>/dev/null; then
    printf '  %-16s connect %ss\n' "$ip" "$ct"
  else
    printf '  %-16s \033[31mno answer\033[0m\n' "$ip"
  fi
done

# ─────────────────────────────────────────────────────────────────────────────
b "5. CONGESTION CONTROL  (biggest cheap win for a slow tunnel)"
cc=$(sysctl -n net.ipv4.tcp_congestion_control 2>/dev/null)
qd=$(sysctl -n net.core.default_qdisc 2>/dev/null)
[ "$cc" = bbr ] && ok "tcp_congestion_control=bbr" \
  || bad "tcp_congestion_control=$cc  (want bbr - it does not read middlebox-induced loss as congestion). Run host-tune.sh"
[ "$qd" = fq ] && ok "default_qdisc=fq" || warn "default_qdisc=$qd (want fq; BBR needs it to pace)"
inf "tcp_mtu_probing=$(sysctl -n net.ipv4.tcp_mtu_probing 2>/dev/null) (1 is a cheap safety net on any path)"

# ─────────────────────────────────────────────────────────────────────────────
b "6. PORTS the deploy needs"
busy=$(ss -lntu 2>/dev/null | awk '$5 ~ /:(80|443|2053|2096)$/ {print $5}' | sort -u)
[ -z "$busy" ] && ok "80, 443/tcp, 443/udp, 2053, 2096 all free" \
  || { warn "already in use:"; printf '        %s\n' $busy; }

# ─────────────────────────────────────────────────────────────────────────────
b "7. IDENTITY"
inf "$(sed -n 's/^PRETTY_NAME="\(.*\)"/\1/p' /etc/os-release 2>/dev/null)  kernel $(uname -r)"
inf "iface=${IFACE:-?} gw=${GW:-?} link MTU=$(cat /sys/class/net/${IFACE:-lo}/mtu 2>/dev/null)"
for ip in $(hostname -I 2>/dev/null); do
  case "$ip" in 127.*|10.*|192.168.*|172.1[6-9].*|172.2*|172.3[01].*) continue;; esac
  inf "$ip"
done

# ─────────────────────────────────────────────────────────────────────────────
b "8. ping-based MTU  (INFORMATIONAL ONLY - do not act on this)"
inf "Kept because it is occasionally useful, but it misdiagnosed this exact"
inf "server: it reported a 1280 'black hole' toward Telegram while TCP was"
inf "moving 96 KB at 5 MB/s over that same path. Hosts drop big ICMP for"
inf "reasons that have nothing to do with MTU. Section 1 is the truth."
echo
fits() { local d=$1 s=$2 i; for i in 1 2 3; do ping -c1 -W2 -M do -s "$s" "$d" >/dev/null 2>&1 && return 0; done; return 1; }
for pair in "gateway:${GW:-}" "telegram:149.154.167.51" "cloudflare:1.1.1.1"; do
  n=${pair%%:*}; d=${pair##*:}
  [ -z "$d" ] && continue
  lo=1000; hi=1472
  if ! fits "$d" $lo; then printf '  %-11s %-16s no ICMP reply\n' "$n" "$d"; continue; fi
  if fits "$d" $hi; then printf '  %-11s %-16s 1500\n' "$n" "$d"; continue; fi
  while [ $((hi-lo)) -gt 1 ]; do m=$(((lo+hi)/2)); if fits "$d" $m; then lo=$m; else hi=$m; fi; done
  printf '  %-11s %-16s %s\n' "$n" "$d" "$((lo+28))"
done

printf '\n\033[1mdone.\033[0m Run on every server and diff.\n'
