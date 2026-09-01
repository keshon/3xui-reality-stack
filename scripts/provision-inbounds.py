#!/usr/bin/env python3
"""Create the Xray inbounds in 3x-ui. Idempotent - only adds what is missing.

Lanes:
  vless-reality        443/tcp   Reality falls back to the local reverse proxy
  trojan-httpupgrade   12812     behind the reverse proxy at TROJAN_PATH
  hysteria2            443/udp   native Xray inbound (Xray 26.x, proxy/hysteria)

MTProto is added by add-mtproto.py instead: it runs under `mtg`, not Xray.

This creates one client per person PER LANE, because that is how the add API
works. consolidate-clients.py then collapses them into a single account per
person attached to every lane - which is 3x-ui v3.7's real model, and what
makes one subscription URL emit all lanes.

amneziawg is not created here either. Xray has no implementation of it, but the
panel embeds amnezia-vpn/amneziawg-go and runs it out-of-band, so an amneziawg
inbound does NOT reach the Xray config and cannot crash the core - verified.
Configure it through the panel UI; see docs/gotchas.md.

Run on the server:
    PANEL_USER=... PANEL_PASS=... REALITY_PRIV=... REALITY_PUB=... \
      python3 scripts/provision-inbounds.py
    python3 scripts/provision-inbounds.py --repair-hysteria
"""
import json
import os
import secrets
import sys
import uuid

from panel import ROOT, Panel, as_dict, env_file, users, xray_healthy

CRT = "/certs/certs/{}.crt"      # paths as seen inside the 3x-ui container
KEY = "/certs/private/{}.key"

# Sniffing must be ON or `protocol: ["bittorrent"]` in the routing rules can
# never match - Xray classifies the protocol by reading the first payload, and
# with sniffing off there is nothing to match against. 3x-ui ships that rule in
# its default template, so leaving sniffing off produces a config that looks
# like it blocks torrents and does not.
#
# routeOnly keeps it to that. The sniffed name is used for ROUTING decisions
# only and never replaces the address the client asked for, so this cannot
# introduce the usual destOverride failure where a connection is re-pointed at
# whatever the server's own resolver returns for the sniffed name.
SNIFF = json.dumps({"enabled": True, "destOverride": ["http", "tls", "quic"],
                    "metadataOnly": False, "routeOnly": True})
ALLOC = json.dumps({"strategy": "always", "refresh": 5, "concurrency": 3})


# uTLS profiles Xray knows. "random"/"randomized" pick per connection.
FINGERPRINTS = {"chrome", "firefox", "safari", "ios", "android", "edge",
                "360", "qq", "random", "randomized"}


def _want_hysteria_version(inbound, st, ss, env):
    """settings.version must be the integer 2.

    3x-ui's web UI does not model this field and drops it when an inbound is
    opened and saved. On v3.7 that is survivable - the panel re-inserts it when
    it generates the Xray config (HealHysteriaVersion) - but the stored row is
    still wrong, and older panels had no such healing and simply would not
    start. Cheap to keep correct at the source.
    """
    return {"settings": {"version": 2}} if st.get("version") != 2 else None


def _want_vless_encryption(inbound, st, ss, env):
    """settings.encryption must be "none".

    3x-ui only echoes this into generated client configs; it strips the field
    again when building the Xray config, so it cannot upset the running core.
    Leaving it unset is not neutral: the plain share link then omits
    `encryption` entirely and each client picks its own default, while the JSON
    subscription emits `"encryption": ""` - not a legal VLESS outbound value,
    so a client on that format loses the Reality lane while every other lane
    keeps working.
    """
    return {"settings": {"encryption": "none"}} if st.get("encryption") != "none" else None


def _want_fingerprint(inbound, st, ss, env):
    """realitySettings.settings.fingerprint - which browser clients imitate.

    Purely a client-side choice; the server only publishes a suggestion, which
    3x-ui copies into the share link as `fp=`. It matters far more than it
    looks: current Chrome sends a post-quantum key share (X25519MLKEM768)
    roughly 1.2 KB on its own, pushing the ClientHello past 1700 bytes and so
    over one segment. A middlebox that forwards the first segment and drops the
    second leaves the handshake permanently half-delivered - the server ACKs
    what arrived and waits for a continuation that never comes, so the
    connection hangs with no RST and no error anywhere, while every other lane
    on the same host and port keeps working. safari and ios have no
    post-quantum key share and fit in a single segment.
    """
    fp = (env.get("REALITY_FINGERPRINT") or "").strip()
    if not fp:
        return None
    if fp not in FINGERPRINTS:
        raise SystemExit(f"unknown REALITY_FINGERPRINT {fp!r}; pick one of "
                         f"{', '.join(sorted(FINGERPRINTS))}")
    r = ss.get("realitySettings")
    if not r or (r.get("settings") or {}).get("fingerprint") == fp:
        return None
    return {"streamSettings": {"realitySettings":
                               {"settings": {"fingerprint": fp}}}}


def _want_sniffing(inbound, st, ss, env):
    """Sniffing must be on, or the bittorrent routing rule can never fire."""
    cur = as_dict(inbound.get("sniffing"))
    want = json.loads(SNIFF)
    if cur.get("enabled") and cur.get("routeOnly") == want["routeOnly"]:
        return None
    return {"sniffing": want}


# name -> (protocols it applies to, function returning the desired delta)
RECONCILERS = {
    "hysteria-version": (("hysteria",), _want_hysteria_version),
    "encryption": (("vless",), _want_vless_encryption),
    "fingerprint": (("vless",), _want_fingerprint),
    "sniffing": (("vless", "trojan", "hysteria"), _want_sniffing),
}


def _merge(dst, src):
    for k, v in src.items():
        if isinstance(v, dict) and isinstance(dst.get(k), dict):
            _merge(dst[k], v)
        else:
            dst[k] = v
    return dst


def reconcile_inbounds(p, env, only=None):
    """Bring stored inbounds in line with what this repo expects.

    Everything goes through the panel API rather than x-ui.db. The schema is
    3x-ui's private business and has changed shape between releases, and a
    write that bypasses the panel also bypasses its validation - which is what
    catches, for example, an out-of-range AmneziaWG parameter before it becomes
    an inbound that cannot start.

    All checks for one inbound are collected and applied as a SINGLE update,
    because the panel restarts Xray after each one.
    """
    names = list(RECONCILERS) if only is None else list(only)
    changed = []
    for inbound in p.inbounds():
        proto = inbound.get("protocol")
        st = as_dict(inbound.get("settings"))
        ss = as_dict(inbound.get("streamSettings"))
        delta, applied = {}, []
        for name in names:
            protos, fn = RECONCILERS[name]
            if proto not in protos:
                continue
            d = fn(inbound, st, ss, env)
            if d:
                _merge(delta, d)
                applied.append(name)
        if not delta:
            continue
        fields = {}
        if "settings" in delta:
            fields["settings"] = json.dumps(_merge(dict(st), delta["settings"]))
        if "streamSettings" in delta:
            fields["streamSettings"] = json.dumps(
                _merge(dict(ss), delta["streamSettings"]))
        if "sniffing" in delta:
            fields["sniffing"] = json.dumps(delta["sniffing"])
        r = p.update_inbound(inbound, **fields)
        ok = bool(r.get("success"))
        changed.append((inbound.get("remark"), applied, ok, r.get("msg", "")))
    return changed


def report(changed):
    for remark, applied, ok, msg in changed:
        print(f"  {remark:22} {', '.join(applied):40} "
              f"{'ok' if ok else 'FAILED: ' + str(msg)[:60]}")
    if not changed:
        print("  nothing to change")
    return sum(1 for c in changed if not c[2])


# CLI aliases for a subset of the reconcilers, kept because they are what the
# docs and the runbook tell you to type.
FLAGS = {
    "--repair-hysteria": ["hysteria-version"],
    "--sniffing": ["sniffing"],
    "--repair-links": ["encryption"],
    "--fingerprint": ["fingerprint"],
    "--reconcile": None,          # None = everything
}


def main():
    flag = next((a for a in sys.argv if a in FLAGS), None)
    if flag:
        e = env_file()
        if flag == "--fingerprint":
            i = sys.argv.index(flag)
            if i + 1 < len(sys.argv) and not sys.argv[i + 1].startswith("-"):
                e["REALITY_FINGERPRINT"] = sys.argv[i + 1]
        p = Panel(e).login()
        sys.exit(1 if report(reconcile_inbounds(p, e, FLAGS[flag])) else 0)

    e = env_file()
    dom = e["PUBLIC_DOMAIN"]
    cert = e.get("CERT_BASENAME", e["BASE_DOMAIN"])
    people = users(e)

    p = Panel(e).login()
    existing = {i["remark"]: i for i in p.inbounds()}
    print("existing lanes:", ", ".join(sorted(existing)) or "(none)")

    # reuse subIds already in use, so re-running never orphans a subscription
    sub_ids = {}
    for i in existing.values():
        s = as_dict(i["settings"])
        for c in (s.get("clients") or []) + (s.get("users") or []):
            who = (c.get("email") or "").split("-", 1)[-1]
            if who and c.get("subId"):
                sub_ids.setdefault(who, c["subId"])
    for who in people:
        sub_ids.setdefault(who, secrets.token_hex(8))

    creds = []

    # ── VLESS + Reality + Vision ────────────────────────────────────────────
    if "vless-reality" not in existing:
        clients = []
        for who in people:
            cid = str(uuid.uuid4())
            clients.append({"id": cid, "email": f"r-{who}",
                            "flow": "xtls-rprx-vision", "enable": True,
                            "limitIp": 0, "totalGB": 0, "expiryTime": 0,
                            "subId": sub_ids[who], "tgId": 0, "reset": 0})
            creds.append(("reality-uuid", who, cid))
        short_ids = [secrets.token_hex(4), secrets.token_hex(8)]
        r = p.add_inbound({
            "remark": "vless-reality", "enable": True, "listen": "",
            "port": 443, "protocol": "vless", "expiryTime": 0,
            "up": 0, "down": 0, "total": 0,
            # decryption is what Xray reads; encryption is echoed into the
            # client configs. See _want_vless_encryption().
            "settings": json.dumps({"clients": clients, "decryption": "none",
                                    "encryption": "none", "fallbacks": []}),
            "streamSettings": json.dumps({
                "network": "tcp", "security": "reality",
                "realitySettings": {
                    "show": False,
                    # Point at the LOCAL reverse proxy, which holds a real
                    # certificate for serverNames and serves a real site.
                    # Pointing at a third party you do not control means the
                    # fallback hands out THEIR certificate for YOUR SNI, which
                    # identifies the server in a single connection.
                    "target": "traefik:8443",
                    "xver": 2,          # PROXY v2 -> real client IPs in logs
                    "serverNames": [dom],
                    "privateKey": os.environ["REALITY_PRIV"],
                    "shortIds": short_ids,
                    "settings": {"publicKey": os.environ["REALITY_PUB"],
                                 # see _want_fingerprint()
                                 "fingerprint": e.get("REALITY_FINGERPRINT")
                                 or "chrome",
                                 "serverName": "", "spiderX": "/"}},
                "tcpSettings": {"acceptProxyProtocol": False,
                                "header": {"type": "none"}},
                # Without this, 3x-ui builds the link address from the HTTP
                # Host of the subscription request - i.e. PANEL_DOMAIN - and
                # leaks the admin hostname into every client config.
                "externalProxy": [{"forceTls": "same", "dest": dom,
                                   "port": 443, "remark": ""}]}),
            "sniffing": SNIFF, "allocate": ALLOC})
        print("vless-reality:", r.get("success"), r.get("msg", ""))
        creds.append(("reality-shortIds", "-", ",".join(short_ids)))

    # ── Trojan over httpupgrade, behind the reverse proxy ───────────────────
    if "trojan-httpupgrade" not in existing:
        clients = []
        for who in people:
            pw = secrets.token_urlsafe(24)
            clients.append({"password": pw, "email": f"t-{who}",
                            "enable": True, "limitIp": 0, "totalGB": 0,
                            "expiryTime": 0, "subId": sub_ids[who],
                            "tgId": 0, "reset": 0})
            creds.append(("trojan-password", who, pw))
        r = p.add_inbound({
            "remark": "trojan-httpupgrade", "enable": True, "listen": "",
            "port": 12812, "protocol": "trojan", "expiryTime": 0,
            "up": 0, "down": 0, "total": 0,
            "settings": json.dumps({"clients": clients, "fallbacks": []}),
            "streamSettings": json.dumps({
                "network": "httpupgrade", "security": "none",  # proxy does TLS
                "httpupgradeSettings": {"acceptProxyProtocol": False,
                                        "path": e["TROJAN_PATH"], "host": dom,
                                        "headers": {}},
                "externalProxy": [{"forceTls": "tls", "dest": dom, "port": 443,
                                   "remark": ""}]}),
            "sniffing": SNIFF, "allocate": ALLOC})
        print("trojan-httpupgrade:", r.get("success"), r.get("msg", ""))

    # ── Hysteria2, native in Xray 26.x ──────────────────────────────────────
    if "hysteria2" not in existing:
        us = []
        for who in people:
            pw = secrets.token_urlsafe(18)
            us.append({"auth": pw, "email": f"h-{who}", "subId": sub_ids[who],
                       "enable": True, "limitIp": 0, "totalGB": 0,
                       "expiryTime": 0, "tgId": 0, "reset": 0})
            creds.append(("hysteria2-auth", who, pw))
        r = p.add_inbound({
            "remark": "hysteria2", "enable": True, "listen": "",
            "port": 443, "protocol": "hysteria", "expiryTime": 0,
            "up": 0, "down": 0, "total": 0,
            # settings.version MUST be int 2, and 3x-ui strips it on save -
            # see _want_hysteria_version() above.
            "settings": json.dumps({"version": 2, "users": us}),
            "streamSettings": json.dumps({
                # network=hysteria is required, or Xray says
                # "proxy/hysteria: not hysteria transport" and will not start
                "network": "hysteria", "security": "tls",
                "tlsSettings": {
                    "serverName": dom, "alpn": ["h3"],
                    "certificates": [{"certificateFile": CRT.format(cert),
                                      "keyFile": KEY.format(cert)}]},
                "hysteriaSettings": {"version": 2,
                                     "ignoreClientBandwidth": True},
                "externalProxy": [{"forceTls": "tls", "dest": dom, "port": 443,
                                   "remark": ""}]}),
            "sniffing": SNIFF, "allocate": ALLOC})
        print("hysteria2:", r.get("success"), r.get("msg", ""))

    changed = reconcile_inbounds(p, e)
    if changed:
        print("reconciled:")
        report(changed)

    if not xray_healthy():
        print("WARNING: Xray is not healthy - docker logs 3x-ui", file=sys.stderr)

    if not creds:
        print("nothing to do - all lanes already exist")
        return

    out = os.path.join(ROOT, "CLIENT-CREDENTIALS.txt")
    with open(out, "a" if os.path.exists(out) else "w") as f:
        f.write(f"\n=== {len(creds)} secrets ===\nPUBLIC_DOMAIN={dom}\n")
        for kind, who, val in creds:
            f.write(f"  {kind:22} {who:12} {val}\n")
    os.chmod(out, 0o600)
    print(f"secrets appended to {out} (mode 600)")


if __name__ == "__main__":
    main()
