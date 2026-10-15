#!/usr/bin/env python3
"""Reconcile panel clients with users.txt across the master and all nodes.

users.txt is the single source of truth. For every name it ensures one
unified client attached to every lane: the local LANES plus the
{node}-reality / {node}-trojan-httpupgrade / {node}-hysteria2 lanes of each
node in nodes.txt. The panel fans client writes out to the nodes itself
(master API only - this script never talks to a node directly).

  python3 scripts/reconcile-clients.py            dry run (default, changes nothing)
  python3 scripts/reconcile-clients.py --apply    create missing clients, attach missing lanes
  python3 scripts/reconcile-clients.py --prune    additionally revoke clients gone from users.txt
                                                  (dry-run lists them without --apply too)

Two consecutive --apply runs print a zero diff. Removing a line from
users.txt only STOPS re-creation; access is revoked solely by --prune
(--apply), exactly like the single-stack rule. Legacy per-lane clones
(r-alice, t-alice...) are consolidate-clients.py's job - merge them first;
this script leaves clones of known users alone and only prunes emails whose
base name is gone entirely.

Optional per-account limits (0 = no limit, the default):
  USER_LIMIT_GB=10    total traffic in GiB, 0 for unlimited
  USER_EXPIRY_DAYS=90 account lifetime in days from creation, 0 for never

Limits are written on creation; on existing accounts they are enforced only
when non-zero and different, so a second run is always a no-op and nothing
is silently wiped.

Run on the master: PANEL_USER=... PANEL_PASS=... python3 scripts/reconcile-clients.py

Sources: ClientController.create binds {client, inboundIds}, fills protocol
credentials and mints subId when empty (client.go, client_crud.go); update
preserves omitted credentials; attach adds missing per-inbound clients.
"""
import sys
import time

from panel import Panel, env_file, normalize_client, users

APPLY = "--apply" in sys.argv
PRUNE = "--prune" in sys.argv
GB = 1024 ** 3
DAY_MS = 86400 * 1000


def limits(env):
    """(totalGB bytes, expiryTime ms) for a client created NOW.

    Zero in means zero out: 0 is unlimited/never, matching the panel default.
    """
    try:
        gb = float(env.get("USER_LIMIT_GB") or 0)
    except ValueError:
        raise SystemExit("USER_LIMIT_GB must be a number")
    try:
        days = float(env.get("USER_EXPIRY_DAYS") or 0)
    except ValueError:
        raise SystemExit("USER_EXPIRY_DAYS must be a number")
    if gb < 0 or days < 0:
        raise SystemExit("USER_LIMIT_GB/USER_EXPIRY_DAYS must be >= 0")
    total = int(gb * GB)
    expiry = int(time.time() * 1000 + days * DAY_MS) if days else 0
    return total, expiry


def target_lanes(p, env, node_names):
    """Inbound ids every account must sit on: existing LANES + node lanes."""
    lanes = {i["remark"]: i["id"] for i in p.inbounds()}
    want = [k.strip() for k in
            env.get("LANES", "vless-reality,trojan-httpupgrade,hysteria2,"
                             "mtproto,amneziawg").split(",") if k.strip()]
    for node in node_names:
        want += [f"{node}-reality", f"{node}-trojan-httpupgrade",
                 f"{node}-hysteria2"]
    ids, missing = [], []
    for remark in want:
        if remark in lanes:
            ids.append(lanes[remark])
        elif remark not in missing:
            missing.append(remark)
    return ids, missing


def main():
    e = env_file()
    if (e.get("ROLE") or "standalone") == "node":
        raise SystemExit("reconcile-clients runs on the master - users.txt "
                         "lives there, and the panel fans writes out itself")
    people = users(e)
    try:
        from panel import read_nodes
        node_names = [n["name"] for n in read_nodes(env=e)]
    except SystemExit:
        node_names = []

    want_total, want_expiry = limits(e)
    p = Panel(e).login()
    target, missing = target_lanes(p, e, node_names)
    print("target lanes: %d inbound(s)%s" % (
        len(target), f" (missing: {', '.join(missing)})" if missing else ""))
    if node_names:
        print("nodes: " + ", ".join(node_names))
    if not target:
        raise SystemExit("no target lanes exist - provision them first")

    plan_add, plan_attach, plan_limits, plan_prune = [], [], [], []
    ok, failed = 0, 0

    for who in people:
        cur = p.client(who)
        if not cur or not cur.get("client"):
            plan_add.append(who)
            if not APPLY:
                continue
            r = p.add_client({"email": who, "enable": True,
                              "totalGB": want_total, "expiryTime": want_expiry},
                             target)
            if r.get("success"):
                print(f"  {who:12} created on {len(target)} lane(s)")
                ok += 1
            else:
                print(f"  {who:12} CREATE FAILED: {r.get('msg')}")
                failed += 1
            continue
        have = set(cur.get("inboundIds") or [])
        lacking = [i for i in target if i not in have]
        c = cur["client"]
        limited = ((want_total and c.get("totalGB") != want_total) or
                   (want_expiry and c.get("expiryTime") != want_expiry))
        if not lacking and not limited:
            ok += 1
            continue
        if lacking:
            plan_attach.append((who, lacking))
        if limited:
            plan_limits.append(who)
        if not APPLY:
            continue
        if lacking:
            r = p.attach(who, target)
            if not r.get("success"):
                print(f"  {who:12} ATTACH FAILED: {r.get('msg')}")
                failed += 1
                continue
        if limited:
            c = normalize_client(c)
            c["totalGB"] = want_total or c.get("totalGB", 0)
            c["expiryTime"] = want_expiry or c.get("expiryTime", 0)
            r = p.update_client(who, c)
            if not r.get("success"):
                print(f"  {who:12} LIMITS FAILED: {r.get('msg')}")
                failed += 1
                continue
        print(f"  {who:12} ok"
              f"{' attached' if lacking else ''}"
              f"{' limits' if limited else ''}")
        ok += 1

    if PRUNE or not APPLY:
        try:
            known = set(people)
            for row in p.clients():
                email = row.get("email") or ""
                base = email.split("-", 1)[-1]
                if email not in known and base not in known:
                    plan_prune.append(email)
        except Exception as ex:  # noqa: BLE001 - prune listing is best-effort
            print(f"  !! could not list clients for prune: {ex}")

    if not APPLY:
        for who in plan_add:
            print(f"  {who:12} would create on {len(target)} lane(s)")
        for who, lacking in plan_attach:
            print(f"  {who:12} would attach to {len(lacking)} lane(s)")
        for who in plan_limits:
            print(f"  {who:12} would enforce limits")
        for email in plan_prune:
            print(f"  {email:12} would revoke (gone from users.txt)"
                  + ("" if PRUNE else " [needs --prune --apply]"))
        print(f"\ndry run: {len(plan_add)} to create, "
              f"{len(plan_attach)} to attach, {len(plan_limits)} limits, "
              f"{len(plan_prune)} to revoke - re-run with --apply"
              + (" --prune" if plan_prune and not PRUNE else ""))
        return

    if PRUNE:
        for email in plan_prune:
            r = p.post(f"/panel/api/clients/del/{email}")
            if r.get("success"):
                print(f"  {email:12} revoked")
                ok += 1
            else:
                print(f"  {email:12} REVOKE FAILED: {r.get('msg')}")
                failed += 1

    print(f"\ndone: {ok} ok, {failed} failed")
    sys.exit(1 if failed else 0)


if __name__ == "__main__":
    main()
