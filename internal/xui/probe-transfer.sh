#!/bin/sh
# Fixed private probe inside the owned panel container; JSON arrives on stdin.
set -eu
case "$1" in amd64|arm64) ;; *) exit 1 ;; esac
umask 077
work=$(mktemp -d /tmp/stackctl-probe.XXXXXX)
pid=
cleanup() {
    if [ -n "$pid" ]; then kill "$pid" 2>/dev/null || :; wait "$pid" 2>/dev/null || :; fi
    rm -f "$work/config.json"
    rmdir "$work"
}
trap cleanup EXIT
trap 'exit 1' HUP INT TERM
cat > "$work/config.json"
"/app/bin/xray-linux-$1" run -config "$work/config.json" >/dev/null 2>&1 &
pid=$!
# Include refused SOCKS connections in curl's bounded startup retries.
curl --silent --fail --retry 5 --retry-connrefused --retry-delay 1 \
    --retry-max-time 35 --max-time 30 --connect-timeout 5 --noproxy '' \
    --proxy socks5h://127.0.0.1:39080 --output /dev/null \
    --write-out '%{http_code} %{size_download}' \
    https://telegram.org/js/widget-frame.js 2>/dev/null
kill -0 "$pid"
