#!/usr/bin/env python3
"""Add an MTProto (FakeTLS) lane and give every account a secret.

MTProto is worth having as its own lane: Telegram clients speak it natively, so
there is no tunnel, no mux, no ALPN negotiation and no VPN client involved -
just a tg://proxy link.

Two things make it safe and consistent:

  * It is run by the bundled `mtg` binary, NOT by Xray. Verified by the absence
    of "mtproto" in the generated Xray config. So unlike an `amneziawg` inbound
    - which Xray cannot run and which therefore crashes the whole core - this
    one cannot take the other lanes down.

  * The FakeTLS fronting domain is set to PUBLIC_DOMAIN. 3x-ui defaults it to
    www.cloudflare.com, which would make clients present SNI=www.cloudflare.com
    while connecting to your IP - exactly the SNI/IP mismatch that makes a
    server stand out. Fronting your own domain keeps DNS, SNI and IP agreeing.

Run on the server:  PANEL_USER=... PANEL_PASS=... python3 scripts/add-mtproto.py
"""
import binascii
import json
import sys

from panel import Panel, env_file, mtg, normalize_client, users, xray_healthy


def fronts(secret, domain):
    """True if this FakeTLS secret already encodes `domain`.

    Layout: "ee" + 32 hex chars of key + the domain, hex-encoded.
    """
    if not secret or not secret.startswith("ee") or len(secret) <= 34:
        return False
    try:
        return binascii.unhexlify(secret[34:]).decode("utf-8") == domain
    except (binascii.Error, UnicodeDecodeError, ValueError):
        return False


def main():
    e = env_file()
    dom = e["PUBLIC_DOMAIN"]
    port = int(e.get("MTPROTO_PORT", "8443"))

    p = Panel(e).login()

    if not p.inbound("mtproto"):
        secret = mtg("generate-secret", "--hex", dom)
        if not secret.startswith("ee"):
            sys.exit(f"mtg did not return a FakeTLS secret: {secret[:60]!r}")
        r = p.add_inbound({
            "remark": "mtproto", "enable": True, "listen": "", "port": port,
            "protocol": "mtproto", "expiryTime": 0, "up": 0, "down": 0,
            "total": 0,
            "settings": json.dumps({"secret": secret, "users": []}),
            "streamSettings": json.dumps({}),
            "sniffing": json.dumps({"enabled": False}),
            "allocate": json.dumps({"strategy": "always", "refresh": 5,
                                    "concurrency": 3})})
        print("mtproto inbound:", r.get("success"), r.get("msg", ""))
        if not r.get("success"):
            sys.exit(1)
    else:
        print("mtproto inbound already exists")

    inb = p.inbound("mtproto")
    emails = p.emails() or users(e)

    for who in emails:
        cur = p.client(who)
        if not cur:
            continue
        p.attach(who, [inb["id"]])
        c = normalize_client(cur["client"])
        if fronts(c.get("secret"), dom):
            print(f"  {who:12} secret already fronts {dom}")
            continue
        # Only re-issue when the secret is missing or fronts the wrong domain.
        # Regenerating unconditionally would rotate everyone's link on every
        # run, which would quietly break clients each time install.sh re-runs.
        c["secret"] = mtg("generate-secret", "--hex", dom)
        r = p.update_client(who, c)
        print(f"  {who:12} secret re-issued "
              f"{'ok' if r.get('success') else r.get('msg')}")

    if not xray_healthy():
        print("WARNING: Xray is not healthy - check: docker logs 3x-ui",
              file=sys.stderr)
    print(f"\nlinks:  tg://proxy?server={dom}&port={port}&secret=<secret>")


if __name__ == "__main__":
    main()
