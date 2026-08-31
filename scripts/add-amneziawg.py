#!/usr/bin/env python3
"""Add an AmneziaWG lane - obfuscated WireGuard, run by the panel.

Xray has no amneziawg implementation, but 3x-ui embeds
`github.com/amnezia-vpn/amneziawg-go` and runs instances in-process, so this
inbound never reaches the Xray config and cannot crash the core.

It is the WireGuard worth having: plain WG has a fixed-size handshake with
recognisable constants, which is exactly what AmneziaWG's junk packets, magic
headers and randomised timings exist to break up.

The obfuscation parameters and the server keypair are NOT generated here. Send
an inbound with no `server` block and the panel builds the whole AmneziaWG 3.1
set itself (`normalizeAmneziaWGSettings` -> `defaultAmneziaWGServer`), randomised
per deployment and already satisfying its own validator. Generating them here
instead means restating constraints that belong to the panel - S3 <= 64, S4 <=
32, S1+56 != S2, every S >= 12 once a header protection key is set, max rekey
below min reject - and getting a rejected inbound the moment any of them moves.

Things that are not obvious and cost time to find:

  * The settings shape is NESTED - {"server": {...}, "clients": []} - not the
    flat {secretKey, peers} that Xray's own wireguard inbound uses.
  * The instance will NOT start unless `externalInterface` is set AND at least
    one client is attached. With either missing it stores fine, reports success,
    and silently does nothing - no listener, no log line. The panel cannot guess
    the interface, so that one field is still ours to fill in.
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
import json
import sqlite3
import subprocess
import sys
import time

from panel import DB_PATH, Panel, as_dict, env_file, users, xray_healthy


def default_iface():
    out = subprocess.run(["ip", "route", "show", "default"],
                         capture_output=True, text=True).stdout.split()
    return out[out.index("dev") + 1] if "dev" in out else "eth0"


def main():
    e = env_file()
    port = int(e.get("AWG_PORT", "51820"))
    p = Panel(e).login()

    if not p.inbound("amneziawg"):
        r = p.add_inbound({
            "remark": "amneziawg", "enable": True, "listen": "", "port": port,
            "protocol": "amneziawg", "expiryTime": 0,
            "up": 0, "down": 0, "total": 0,
            # No "server" key: that is the signal to generate one.
            "settings": json.dumps({"clients": []}),
            "streamSettings": json.dumps({}),
            "sniffing": json.dumps({"enabled": False}),
            "allocate": json.dumps({"strategy": "always", "refresh": 5,
                                    "concurrency": 3})})
        print("amneziawg inbound:", r.get("success"), r.get("msg", ""))
        if not r.get("success"):
            sys.exit(1)
    else:
        print("amneziawg inbound already exists")

    # ── the few fields the panel cannot know ────────────────────────────────
    inb = p.inbound("amneziawg")
    st = as_dict(inb["settings"])
    server = st.get("server") or {}
    if not server.get("privateKey"):
        sys.exit("amneziawg: the panel did not generate a server block")

    want = {
        "externalInterface": default_iface(),   # required, or nothing listens
        "subnetIp": e.get("AWG_SUBNET", "10.8.1.0"),
        "primaryDns": "1.1.1.1",
        "secondaryDns": "1.0.0.1",
        # Sets the server's netstack MTU AND emits the "MTU =" line into every
        # generated client config - 3x-ui writes that line only when this is
        # non-zero, so leaving it unset means clients fall back to WireGuard's
        # 1420. That is too big here. Plain WG budgets 60 bytes of overhead
        # (IP 20 + UDP 8 + header 16 + Poly1305 16); AmneziaWG 3.1 then adds up
        # to ~64 more of transport junk (S4), content padding and random
        # trailers. On a 1500-byte path that leaves ~1376, and access networks
        # are rarely 1500 - a phone on this deployment advertised MSS 1394,
        # i.e. a 1434-byte path, which leaves ~1310.
        #
        # Too-large packets do not fail cleanly. Small requests fit and work,
        # so the tunnel looks alive; anything that fills a packet stalls and
        # retries. That is the "video takes 12 seconds to start and then
        # rebuffers" shape.
        "mtu": int(e.get("AWG_MTU") or 1280),
    }
    if any(server.get(k) != v for k, v in want.items()):
        server.update(want)
        st["server"] = server
        r = p.update_inbound(inb, settings=json.dumps(st))
        print(f"  interface={want['externalInterface']} "
              f"subnet={want['subnetIp']}/24:", r.get("success"),
              r.get("msg", ""))
        if not r.get("success"):
            sys.exit(1)
        inb = p.inbound("amneziawg")

    # ── peers ───────────────────────────────────────────────────────────────
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
