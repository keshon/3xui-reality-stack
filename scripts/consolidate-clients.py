#!/usr/bin/env python3
"""Collapse per-protocol client clones into one account per person.

3x-ui v3.7 has a unified client model: ONE client row carries every protocol's
credential, and `client_inbounds` maps it to as many lanes as you like:

    uuid                              -> vless
    password                          -> trojan / shadowsocks
    auth                              -> hysteria
    privateKey/publicKey/allowedIPs   -> wireguard
    secret/adTag                      -> mtproto

So `r-alice` + `t-alice` + `h-alice` should be a single `alice` attached to
every inbound. Per-lane `flow` lives in `client_inbounds.flow_override`, so
Vision stays on for Reality and off everywhere else automatically.

This also fixes the subscription: with clients attached this way, one URL emits
links for every lane. Per-protocol clones give one link per URL.

Run on the server:
    PANEL_USER=... PANEL_PASS=... python3 scripts/consolidate-clients.py [--apply]

Without --apply it only prints what it would do.
"""
import secrets
import sys
import uuid as uuidlib

from panel import Panel, env_file, normalize_client, valid_uuid

APPLY = "--apply" in sys.argv
DEFAULT_LANES = ("vless-reality,trojan-httpupgrade,hysteria2,mtproto,"
                 "amneziawg")


def stored(p, email):
    """(uuid, password, auth, subId) as the panel reports them, or None.

    Read back through the API rather than out of x-ui.db: this is the
    post-write verification step, and it should observe the same view of the
    data that every other consumer does.
    """
    c = (p.client(email) or {}).get("client")
    if not c:
        return None
    return (c.get("uuid"), c.get("password"), c.get("auth"), c.get("subId"))


def main():
    e = env_file()
    p = Panel(e).login()

    lanes = {i["remark"]: i["id"] for i in p.inbounds()}
    # Plain `wireguard` is deliberately absent: its handshake is fixed-size
    # with recognisable constants. amneziawg is the obfuscated one, and is in.
    want = [k.strip() for k in e.get("LANES", DEFAULT_LANES).split(",") if k.strip()]
    target = [lanes[k] for k in want if k in lanes]
    print("target lanes:", ", ".join(k for k in want if k in lanes))

    emails = sorted(p.emails())
    people = sorted({x.split("-", 1)[-1] for x in emails})

    for who in people:
        clones = [x for x in emails if x == who or x.split("-", 1)[-1] == who]
        primary = who if who in emails else next(
            (x for x in clones if x.startswith("r-")), clones[0])
        dupes = [x for x in clones if x != primary]

        cur = p.client(primary)
        if not cur:
            print(f"  {who:12} SKIP (cannot read {primary})")
            continue
        c = normalize_client(cur["client"])

        # gather credentials from the clones BEFORE touching anything
        for d in dupes:
            row = stored(p, d)
            if row and row[1] and not c.get("password"):
                c["password"] = row[1]

        if not valid_uuid(c["id"]):
            print(f"  {who:12} uuid invalid ({cur['client'].get('uuid')!r}) - regenerating")
            c["id"] = str(uuidlib.uuid4())
        c["uuid"] = c["id"]
        c["email"] = who
        c["password"] = c.get("password") or secrets.token_hex(16)
        c["auth"] = c.get("auth") or secrets.token_hex(16)

        if not APPLY:
            print(f"  {who:12} would merge {clones} -> '{who}' on {target}")
            continue

        r1 = p.update_client(primary, c)
        if not r1.get("success"):
            print(f"  {who:12} UPDATE FAILED: {r1.get('msg')}")
            continue
        r2 = p.attach(who, target)

        # verify BEFORE deleting anything - a half-done merge that then loses
        # the duplicate's password is unrecoverable
        row = stored(p, who)
        if not (row and valid_uuid(row[0]) and row[1] and row[2]):
            print(f"  {who:12} VERIFY FAILED - leaving duplicates in place")
            continue
        for d in dupes:
            p.post(f"/panel/api/clients/del/{d}")
        print(f"  {who:12} ok  uuid={row[0][:8]} sub={row[3]} "
              f"attach={r2.get('success')} removed={dupes or '-'}")

    if not APPLY:
        print("\ndry run - re-run with --apply")


if __name__ == "__main__":
    main()
