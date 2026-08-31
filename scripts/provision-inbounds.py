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
import sqlite3
import sys
import uuid

from panel import DB_PATH, ROOT, Panel, as_dict, env_file, users, xray_healthy

CRT = "/certs/certs/{}.crt"      # paths as seen inside the 3x-ui container
KEY = "/certs/private/{}.key"

SNIFF = json.dumps({"enabled": False, "destOverride": [],
                    "metadataOnly": False, "routeOnly": False})
ALLOC = json.dumps({"strategy": "always", "refresh": 5, "concurrency": 3})


def repair_hysteria_version(db=DB_PATH):
    """Put settings.version=2 back after 3x-ui strips it.

    3x-ui v3.7 does not model `version` for hysteria inbounds, so it drops the
    field on every save - including when you merely open and save the inbound
    in the panel UI. Xray then refuses to start AT ALL, so losing this one
    integer takes every other lane down with it. Re-run after any panel edit
    to a hysteria inbound, then restart 3x-ui.
    """
    con = sqlite3.connect(db)
    fixed = 0
    for iid, st in con.execute(
            "select id, settings from inbounds where protocol='hysteria'"):
        cfg = json.loads(st)
        if cfg.get("version") != 2:
            cfg["version"] = 2
            con.execute("update inbounds set settings=? where id=?",
                        (json.dumps(cfg), iid))
            fixed += 1
    con.commit()
    return fixed


def main():
    if "--repair-hysteria" in sys.argv:
        print(f"repaired {repair_hysteria_version()} hysteria inbound(s); "
              f"restart 3x-ui to apply")
        return

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
            "settings": json.dumps({"clients": clients, "decryption": "none",
                                    "fallbacks": []}),
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
                                 "fingerprint": "chrome", "serverName": "",
                                 "spiderX": "/"}},
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
            # see repair_hysteria_version() above.
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

    n = repair_hysteria_version()
    if n:
        print(f"repaired settings.version on {n} hysteria inbound(s)")

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
