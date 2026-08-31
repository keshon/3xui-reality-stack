#!/usr/bin/env python3
"""Extract certificates from Traefik's acme.json onto disk, for services that
need cert files rather than Traefik's in-memory store (here: Hysteria2).

Replaces ldez/traefik-certs-dumper, which is unmaintained and panics on
Traefik 3.7's acme.json with `unsupported key type: ''` - it requires an
Account.KeyType field that Traefik no longer writes.

Writes, only when the content actually changes:
    /certs/certs/<main-domain>.crt     0644
    /certs/private/<main-domain>.key   0600

A wildcard cert has main="example.com" with sans=["*.example.com"], so it
lands as example.com.crt - which is what CERT_BASENAME should be set to.
"""
import base64
import json
import os
import sys
import time

SRC = os.environ.get("ACME_JSON", "/acme/acme.json")
DST = os.environ.get("CERT_DIR", "/certs")
INTERVAL = int(os.environ.get("INTERVAL", "60"))


def write_if_changed(path: str, data: bytes, mode: int) -> bool:
    try:
        if open(path, "rb").read() == data:
            return False
    except FileNotFoundError:
        pass
    tmp = path + ".tmp"
    with open(tmp, "wb") as f:
        f.write(data)
    os.chmod(tmp, mode)
    os.replace(tmp, path)  # atomic: readers never see a half-written cert
    return True


def dump() -> int:
    try:
        with open(SRC) as f:
            data = json.load(f)
    except (FileNotFoundError, json.JSONDecodeError, ValueError):
        return 0  # empty or being rewritten by Traefik; try again next tick
    if not data:
        return 0

    os.makedirs(f"{DST}/certs", exist_ok=True)
    os.makedirs(f"{DST}/private", exist_ok=True)
    os.chmod(f"{DST}/private", 0o700)

    changed = 0
    for resolver, v in data.items():
        for c in (v or {}).get("Certificates") or []:
            name = (c.get("domain") or {}).get("main")
            if not name:
                continue
            name = name.replace("*.", "", 1)
            try:
                crt = base64.b64decode(c["certificate"])
                key = base64.b64decode(c["key"])
            except (KeyError, ValueError):
                continue
            if not crt or not key:
                continue
            if write_if_changed(f"{DST}/certs/{name}.crt", crt, 0o644):
                changed += 1
                print(f"wrote certs/{name}.crt", flush=True)
            if write_if_changed(f"{DST}/private/{name}.key", key, 0o600):
                changed += 1
                print(f"wrote private/{name}.key", flush=True)
    return changed


if __name__ == "__main__":
    print(f"watching {SRC} -> {DST} every {INTERVAL}s", flush=True)
    while True:
        try:
            dump()
        except Exception as e:  # never die; a renewal must not be missed
            print(f"error: {e}", file=sys.stderr, flush=True)
        time.sleep(INTERVAL)
