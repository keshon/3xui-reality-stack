#!/usr/bin/env python3
"""Add an AmneziaWG lane - obfuscated WireGuard, run by the panel.

Xray has no amneziawg implementation, but 3x-ui embeds
`github.com/amnezia-vpn/amneziawg-go` and runs instances in-process, so this
inbound never reaches the Xray config and cannot crash the core.

It is the WireGuard worth having: plain WG has a fixed-size handshake with
recognisable constants, which is exactly what AmneziaWG's junk packets, magic
headers and randomised timings exist to break up. This uses the v1.5 parameter
set (s3/s4, i1-i5, header protection, randomised rekey timings).

Things that are not obvious and cost time to find:

  * The settings shape is NESTED - {"server": {...}, "clients": []} - not the
    flat {secretKey, peers} that Xray's own wireguard inbound uses.
  * The instance will NOT start unless `externalInterface` is set AND at least
    one client is attached. With either missing it stores fine, reports success,
    and silently does nothing - no listener, no log line.
  * It needs NO NET_ADMIN and NO /dev/net/tun. The log calls it an "embedded
    interface": it is an in-process userspace netstack, so only the published
    UDP port is required.
  * Attaching an account via the client API assigns it a keypair and a /32
    automatically and repopulates settings.clients. Do not hand-write peers.
  * The Endpoint in generated client configs is taken from the host the
    SUBSCRIPTION was fetched from, and externalProxy does not override it -
    which is why subscriptions are served from PUBLIC_DOMAIN.

Run on the server:  PANEL_USER=... PANEL_PASS=... python3 scripts/add-amneziawg.py
"""
import base64
import json
import secrets
import sqlite3
import subprocess
import sys
import time

from panel import DB_PATH, Panel, as_dict, env_file, users, xray_healthy


def wg_keypair():
    """A Curve25519 keypair in WireGuard's base64 format.

    `wg genkey | wg pubkey` is the usual way, but wireguard-tools is not
    installed on a minimal Ubuntu and nothing else here wants it: no kernel
    module and no interface is involved, this is only a key. openssl is already
    a hard dependency and produces byte-identical keys - the raw 32 bytes are
    the tail of the DER in both the PKCS#8 private and the SPKI public form.

    Raises rather than returning empty strings: the panel accepts an empty
    privateKey, reports success, and then silently runs nothing.
    """
    def openssl(args, stdin=None):
        r = subprocess.run(["openssl", *args], input=stdin, capture_output=True)
        if r.returncode != 0:
            raise SystemExit(f"openssl {args[0]} failed: "
                             f"{r.stderr.decode(errors='replace').strip()[:200]}")
        return r.stdout

    priv_der = openssl(["genpkey", "-algorithm", "X25519", "-outform", "DER"])
    pub_der = openssl(["pkey", "-inform", "DER", "-pubout", "-outform", "DER"],
                      stdin=priv_der)
    if len(priv_der) < 32 or len(pub_der) < 32:
        raise SystemExit("openssl returned a truncated X25519 key")
    return (base64.b64encode(priv_der[-32:]).decode(),
            base64.b64encode(pub_der[-32:]).decode())


def default_iface():
    out = subprocess.run(["ip", "route", "show", "default"],
                         capture_output=True, text=True).stdout.split()
    return out[out.index("dev") + 1] if "dev" in out else "eth0"


def obfuscation():
    """AmneziaWG v1.5 parameters. Randomised per deployment: shared constants
    across many servers would themselves become the signature."""
    r = secrets.SystemRandom()
    def rng(lo, hi, spread):
        a = r.randint(lo, hi)
        return f"{a}-{a + r.randint(1, spread)}"
    h = sorted(r.sample(range(5, 2_147_483_000), 8))
    return {
        "jc": r.randint(3, 8),
        "jmin": r.randint(50, 100), "jmax": r.randint(150, 250),
        "s1": r.randint(15, 150), "s2": r.randint(15, 150),
        "s3": r.randint(15, 150), "s4": r.randint(15, 150),
        "h1": f"{h[0]}-{h[1]}", "h2": f"{h[2]}-{h[3]}",
        "h3": f"{h[4]}-{h[5]}", "h4": f"{h[6]}-{h[7]}",
        "i1": f"<r {r.randint(40, 120)}>", "i2": "", "i3": "", "i4": "", "i5": "",
        "headerProtectionKey": subprocess.run(
            ["openssl", "rand", "-base64", "32"],
            capture_output=True, text=True).stdout.strip(),
        "contentPaddingAddition": rng(10, 40, 30),
        "rekeyAfterTime": rng(100, 140, 40),
        "rekeyTimeout": rng(3, 5, 3),
        "rejectAfterTime": rng(180, 260, 60),
        "keepaliveTimeout": rng(10, 18, 6),
        "maxHandshakeAttempts": rng(18, 45, 25),
        "randomTrailers": True,
        "disableCookies": True,
    }


def main():
    e = env_file()
    port = int(e.get("AWG_PORT", "51820"))
    p = Panel(e).login()

    if not p.inbound("amneziawg"):
        priv, pub = wg_keypair()
        server = {
            "privateKey": priv, "publicKey": pub,
            "subnetIp": e.get("AWG_SUBNET", "10.8.1.0"), "subnetCidr": 24,
            "primaryDns": "1.1.1.1", "secondaryDns": "1.0.0.1",
            # REQUIRED, or the instance never starts
            "externalInterface": default_iface(),
            "ipv6Enabled": False, "ipv6Subnet": "",
            "ipv6ExternalInterface": "",
        }
        server.update(obfuscation())
        r = p.add_inbound({
            "remark": "amneziawg", "enable": True, "listen": "", "port": port,
            "protocol": "amneziawg", "expiryTime": 0,
            "up": 0, "down": 0, "total": 0,
            "settings": json.dumps({"server": server, "clients": []}),
            "streamSettings": json.dumps({}),
            "sniffing": json.dumps({"enabled": False}),
            "allocate": json.dumps({"strategy": "always", "refresh": 5,
                                    "concurrency": 3})})
        print("amneziawg inbound:", r.get("success"), r.get("msg", ""))
        if not r.get("success"):
            sys.exit(1)
    else:
        print("amneziawg inbound already exists")

    inb = p.inbound("amneziawg")
    con = sqlite3.connect(DB_PATH)
    emails = [r[0] for r in con.execute("select email from clients")] or users(e)
    for who in emails:
        p.attach(who, [inb["id"]])
    time.sleep(6)

    n = len(as_dict(p.inbound("amneziawg")["settings"]).get("clients") or [])
    listening = subprocess.run(
        ["docker", "exec", "3x-ui", "sh", "-c",
         f"netstat -lnu 2>/dev/null | grep -c {port}"],
        capture_output=True, text=True).stdout.strip()
    print(f"peers: {n}   udp/{port} listeners: {listening}")
    if listening == "0":
        print("WARNING: no listener. The instance needs externalInterface set "
              "AND at least one peer.", file=sys.stderr)
    if not xray_healthy():
        print("WARNING: Xray unhealthy - docker logs 3x-ui", file=sys.stderr)


if __name__ == "__main__":
    main()
