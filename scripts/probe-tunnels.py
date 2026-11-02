#!/usr/bin/env python3
"""Authenticated transfers using published links; never modifies panel state.

On the VPS, reads an existing account's subscription through the panel. On an
external Linux probe host, use --links-file and --xray with a protected copy of
that subscription's decoded links. Credentials never appear in argv or output.
"""
import argparse
import importlib.util
import json
import os
from pathlib import Path
import platform
import socket
import subprocess
import tempfile
import time
from urllib.parse import parse_qs, unquote, urlsplit

from panel import Panel, env_file

LANES = {"vless-reality": "reality", "trojan-httpupgrade": "trojan", "hysteria2": "hysteria2"}
DEFAULT_URL = "https://telegram.org/js/widget-frame.js"


def outbound(link):
    u = urlsplit(link.strip())
    q = {k: v[0] for k, v in parse_qs(u.query).items()}
    if not u.hostname or not u.username:
        raise ValueError("link has no address or credential")
    address, port = u.hostname, u.port or 443
    credential = unquote(u.username)
    tls = {"serverName": q.get("sni") or address,
           "allowInsecure": False, "alpn": ["http/1.1"]}
    if u.scheme == "vless":
        if q.get("security") != "reality" or q.get("type", "tcp") not in ("tcp", "raw"):
            raise ValueError("probe supports only RAW REALITY VLESS links")
        if not q.get("pbk"):
            raise ValueError("REALITY link has no public key")
        return "reality", {"protocol": "vless", "settings": {"vnext": [{
            "address": address, "port": port, "users": [{"id": credential,
            "encryption": q.get("encryption", "none"), "flow": q.get("flow", "")}]}]},
            "streamSettings": {"network": "tcp", "security": "reality",
                "realitySettings": {"serverName": tls["serverName"],
                    "fingerprint": q.get("fp", "safari"), "publicKey": q["pbk"],
                    "shortId": q.get("sid", ""), "spiderX": q.get("spx", "/")}}}
    if u.scheme == "trojan":
        if q.get("type") != "httpupgrade" or q.get("security", "tls") != "tls":
            raise ValueError("probe supports only TLS HTTPUpgrade Trojan links")
        return "trojan", {"protocol": "trojan", "settings": {"servers": [{
            "address": address, "port": port, "password": credential}]},
            "streamSettings": {"network": "httpupgrade", "security": "tls",
                "tlsSettings": tls, "httpupgradeSettings": {
                    "host": q.get("host") or tls["serverName"], "path": q.get("path", "/")}}}
    if u.scheme in ("hy2", "hysteria2"):
        if q.get("obfs"):
            raise ValueError("obfuscated Hysteria links need a separate probe implementation")
        tls["alpn"] = ["h3"]
        auth = unquote(u.username + (":" + u.password if u.password is not None else ""))
        return "hysteria2", {"protocol": "hysteria", "settings": {
            "version": 2, "address": address, "port": port},
            "streamSettings": {"network": "hysteria", "security": "tls",
                "tlsSettings": tls, "hysteriaSettings": {"version": 2, "auth": auth}}}
    raise ValueError("unsupported link")


def subscription_links(account=None):
    e = env_file()
    p = Panel(e).login()
    now_ms = time.time() * 1000
    rows = [c for c in p.clients() if c.get("subId") and c.get("enable", True)
            and (not c.get("expiryTime") or c["expiryTime"] > now_ms)]
    if account:
        rows = [c for c in rows if c.get("email") == account]
    if not rows:
        raise ValueError("no active probe account; configure PROBE_ACCOUNT")
    # Reuse the subscription reader, not the private database schema.
    path = Path(__file__).with_name("client-links.py")
    spec = importlib.util.spec_from_file_location("client_links", path)
    module = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(module)
    return module.fetch_subscription(e, rows[0]["subId"])


def transfer(binary, ob, url, timeout, minimum, rate=None):
    with socket.socket() as listener:
        listener.bind(("127.0.0.1", 0))
        port = listener.getsockname()[1]
    config = {"log": {"loglevel": "none"}, "inbounds": [{"listen": "127.0.0.1",
        "port": port, "protocol": "socks", "settings": {"auth": "noauth", "udp": False}}],
        "outbounds": [ob]}
    process = subprocess.Popen([str(binary), "run", "-config", "stdin:"],
        stdin=subprocess.PIPE, stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL)
    try:
        process.stdin.write(json.dumps(config).encode())
        process.stdin.close()
        deadline = time.monotonic() + 8
        while True:
            if process.poll() is not None:
                return {"ok": False, "reason": "probe core failed to start"}
            try:
                with socket.create_connection(("127.0.0.1", port), timeout=0.2):
                    break
            except OSError:
                if time.monotonic() >= deadline:
                    return {"ok": False, "reason": "probe core startup timeout"}
                time.sleep(0.1)
        cmd = ["curl", "--silent", "--show-error", "--fail", "--noproxy", "",
               "--proxy", f"socks5h://127.0.0.1:{port}", "--max-time", str(timeout),
               "--output", os.devnull, "--write-out", "%{http_code} %{size_download} %{time_total}", url]
        if rate:
            cmd += ["--limit-rate", rate]
        r = subprocess.run(cmd, capture_output=True, text=True, timeout=timeout + 5)
        fields = r.stdout.split()
        code, size, elapsed = (int(fields[0]), int(float(fields[1])), float(fields[2])) if len(fields) == 3 else (0, 0, 0)
        return {"ok": r.returncode == 0 and code == 200 and size >= minimum,
                "http": code, "bytes": size, "seconds": elapsed, "curl_exit": r.returncode}
    finally:
        if process.poll() is None:
            process.terminate()
            try:
                process.wait(timeout=5)
            except subprocess.TimeoutExpired:
                process.kill()
                process.wait(timeout=5)


def main():
    ap = argparse.ArgumentParser(description=__doc__)
    ap.add_argument("--account")
    ap.add_argument("--links-file", type=Path)
    ap.add_argument("--xray", type=Path)
    ap.add_argument("--lane", action="append", choices=tuple(LANES.values()))
    ap.add_argument("--url", default=DEFAULT_URL)
    ap.add_argument("--timeout", type=int, default=30)
    ap.add_argument("--min-bytes", type=int, default=65536)
    ap.add_argument("--rate", help="curl rate limit, useful for long-session tests")
    ap.add_argument("--connect-address", help="override dial address, preserving SNI")
    ap.add_argument("--connect-port", type=int)
    args = ap.parse_args()
    if not 1 <= args.timeout <= 600 or args.min_bytes < 1 or urlsplit(args.url).scheme != "https":
        ap.error("use HTTPS, a positive byte threshold and a timeout from 1 to 600 seconds")
    e = env_file()
    expected = set(args.lane or [LANES[x] for x in e.get("LANES", ",".join(LANES)).split(",") if x in LANES])
    if not expected:
        ap.error("no supported lanes selected")
    try:
        links = args.links_file.read_text().splitlines() if args.links_file else subscription_links(args.account or e.get("PROBE_ACCOUNT"))
        selected = []
        for link in links:
            if not link.startswith(("vless://", "trojan://", "hysteria2://", "hy2://")):
                continue
            lane, ob = outbound(link)
            if lane not in expected:
                continue
            server = (ob["settings"].get("vnext") or ob["settings"].get("servers") or [ob["settings"]])[0]
            if args.connect_address:
                server["address"] = args.connect_address
            if args.connect_port:
                server["port"] = args.connect_port
            selected.append((lane, ob))
        results = []
        with tempfile.TemporaryDirectory(prefix="stack-probe-") as directory:
            binary = args.xray
            if not binary:
                arch = {"x86_64": "amd64", "AMD64": "amd64", "aarch64": "arm64"}.get(platform.machine())
                if not arch:
                    raise ValueError("specify --xray for this architecture")
                binary = Path(directory) / "xray"
                subprocess.run(["docker", "cp", f"3x-ui:/app/bin/xray-linux-{arch}", str(binary)],
                               check=True, stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL, timeout=15)
                binary.chmod(0o700)
            for lane, ob in selected:
                result = transfer(binary, ob, args.url, args.timeout, args.min_bytes, args.rate)
                results.append({"lane": lane, **result})
        for lane in sorted(expected - {lane for lane, _ in selected}):
            results.append({"lane": lane, "ok": False, "reason": "missing published link"})
        print(json.dumps({"scope": "authenticated transfer from this host; MTProto/AmneziaWG untested",
                          "results": results}))
        return 0 if results and all(r["ok"] for r in results) else 1
    except (Exception, SystemExit) as ex:
        # API responses, URIs and core errors can contain credentials.
        print(json.dumps({"ok": False, "reason": "probe setup failed", "error_type": type(ex).__name__}))
        return 1


if __name__ == "__main__":
    raise SystemExit(main())
