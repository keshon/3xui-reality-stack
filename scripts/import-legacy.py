#!/usr/bin/env python3
"""Import a reviewed v2 inbound inventory after rehearsing the panel upgrade.

The private JSON snapshot contains ``inbounds`` from the old database. This
script changes accounts through the upgraded panel API, preserves subscription
IDs and working Trojan passwords, and retires XHTTP only when explicitly asked.
It does not upgrade containers or change the public listener.
"""
import argparse
import json
import secrets
import uuid
from pathlib import Path

from panel import Panel, as_dict, env_file, normalize_client, parse_users


def accounts(snapshot):
    groups = {}
    owners = {}
    for inbound in snapshot["inbounds"]:
        stream = as_dict(inbound.get("stream_settings"))
        if inbound["protocol"] == "vless" and stream.get("security") == "reality":
            lane = "reality"
        elif inbound["protocol"] == "trojan" and stream.get("network") == "httpupgrade":
            lane = "trojan"
        elif inbound["protocol"] == "vless" and stream.get("network") == "xhttp":
            lane = "xhttp"
        else:
            raise ValueError("Unsupported legacy inbound; review it before importing")
        for client in as_dict(inbound["settings"]).get("clients", []):
            email = client["email"]
            name = email.split("-", 1)[-1]
            if parse_users(name) != [name]:
                raise ValueError("Legacy account name needs an explicit mapping")
            sub = client.get("subId")
            if not sub or (sub in owners and owners[sub] != name):
                raise ValueError("Missing or ambiguous subscription identity")
            owners[sub] = name
            group = groups.setdefault(name, {})
            if lane in group:
                raise ValueError("Duplicate transport for a legacy account")
            group[lane] = client
    result = []
    for name, group in sorted(groups.items()):
        if not {"reality", "trojan"} <= group.keys():
            raise ValueError("Every account needs its original Reality and Trojan credentials")
        for field, default in [("subId", None), ("enable", True),
                               ("totalGB", 0), ("expiryTime", 0), ("limitIp", 0)]:
            if len({c.get(field, default) for c in group.values()}) != 1:
                raise ValueError("Conflicting legacy limits or identity; review before importing")
        reality, trojan = group["reality"], group["trojan"]
        uuid.UUID(reality["id"])
        if not trojan.get("password"):
            raise ValueError("Missing original Trojan password")
        result.append({"name": name, "uuid": reality["id"],
                       "password": trojan["password"], "subId": reality["subId"],
                       "source": reality["email"],
                       "retire": [c["email"] for c in group.values()
                                  if c["email"] != reality["email"]]})
    if not result:
        raise ValueError("No accounts in legacy inventory")
    return result


def migrate(panel, plan, target):
    inventory = {c["email"]: c for c in panel.clients()}
    for account in plan:
        name = account["name"]
        source = name if name in inventory else account["source"]
        current = panel.client(source)
        if not current or current["client"].get("uuid") != account["uuid"]:
            raise ValueError("Existing Reality credential differs from the reviewed snapshot")
        client = normalize_client(current["client"])
        client.update(email=name, password=account["password"], subId=account["subId"],
                      auth=client.get("auth") or secrets.token_urlsafe(24))
        if not panel.update_client(source, client).get("success"):
            raise RuntimeError("Account update failed; retain the legacy deployment")
        if not panel.attach(name, target).get("success"):
            raise RuntimeError("Account attachment failed; duplicates were retained")
        saved = panel.client(name)
        row = saved["client"]
        if (row.get("uuid"), row.get("password"), row.get("subId")) != (
                account["uuid"], account["password"], account["subId"]):
            raise RuntimeError("Credential verification failed; duplicates were retained")
        if not row.get("auth") or not set(target) <= set(saved.get("inboundIds", [])):
            raise RuntimeError("Attachment verification failed; duplicates were retained")
        for email in account["retire"]:
            if email in inventory:
                if not panel.post(f"/panel/api/clients/del/{email}").get("success"):
                    raise RuntimeError("Could not retire a verified legacy duplicate")
        print("account migrated; credentials and subscription ID verified")


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("snapshot", type=Path)
    parser.add_argument("--apply", action="store_true")
    parser.add_argument("--retire-xhttp", action="store_true")
    args = parser.parse_args()
    plan = accounts(json.loads(args.snapshot.read_text()))
    print(f"Reviewed {len(plan)} accounts; original subscription IDs and Trojan passwords retained")
    if not args.apply:
        return
    if not args.retire_xhttp:
        parser.error("This import replaces legacy XHTTP; explicitly pass --retire-xhttp")
    env = env_file()
    panel = Panel(env).login()
    lanes = {i["remark"]: i["id"] for i in panel.inbounds()}
    wanted = env["LANES"].split(",")
    if not set(wanted) <= lanes.keys():
        raise ValueError("Provision all destination lanes before importing")
    migrate(panel, plan, [lanes[k] for k in wanted])
    for inbound in panel.inbounds():
        if as_dict(inbound.get("streamSettings")).get("network") == "xhttp":
            if not panel.post(f"/panel/api/inbounds/del/{inbound['id']}").get("success"):
                raise RuntimeError("Could not retire legacy XHTTP inbound")
    print("Legacy import complete")


if __name__ == "__main__":
    main()
