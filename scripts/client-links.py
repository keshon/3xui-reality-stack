#!/usr/bin/env python3
"""Print what each person actually needs, one block per account.

The subscription URL already carries every lane, and for most people that URL
is the only thing they need. This exists for the two cases it does not cover:

  * AmneziaWG. It IS in the subscription, as the `vpn://` entry - but only the
    Amnezia clients understand that scheme. Happ, v2rayN, Hiddify and the rest
    ignore it, so it looks like the lane is missing. The official AmneziaWG app
    wants an ordinary WireGuard-style .conf, which is what `vpn://` decodes to.

  * Telegram. `tg://proxy?...` needs no VPN client at all, so it is worth
    handing out on its own.

Run on the server:

    python3 scripts/client-links.py                 # everyone, to stdout
    python3 scripts/client-links.py keshon icehead  # just these
    python3 scripts/client-links.py --write         # also CLIENT-LINKS.txt
    python3 scripts/client-links.py --awg-dir awg   # per-user .conf files
"""
import base64
import os
import sqlite3
import sys
import urllib.request

from panel import DB_PATH, ROOT, container_ip, env_file

SCHEME_NAMES = {
    "vless://": "VLESS + Reality + Vision",
    "trojan://": "Trojan over httpupgrade",
    "hysteria2://": "Hysteria2",
    "hy2://": "Hysteria2",
    "tg://": "Telegram MTProto",
    "vpn://": "AmneziaWG",
}


def b64decode_loose(s):
    """Decode base64 that may be url-safe and may have lost its padding."""
    s = s.strip().replace("-", "+").replace("_", "/")
    return base64.b64decode(s + "=" * (-len(s) % 4))


def fetch_subscription(env, sub_id):
    """Read a subscription from the panel directly.

    Over the container network on :2096 rather than the public https URL: this
    runs on the same host, and a public fetch would depend on DNS hairpinning
    and on the very tunnel it is meant to be reporting on.

    The Host header must still be PUBLIC_DOMAIN. 3x-ui builds the AmneziaWG
    `Endpoint` and the MTProto `server` from the host the subscription was
    requested with - not from any configured value - so fetching by container
    IP hands out configs pointing at 172.x.
    """
    ip = container_ip()
    if not ip:
        raise SystemExit("3x-ui container is not running")
    url = f"http://{ip}:2096{env['SUB_PATH'].rstrip('/')}/{sub_id}"
    req = urllib.request.Request(url, headers={"Host": env["PUBLIC_DOMAIN"]})
    with urllib.request.urlopen(req, timeout=20) as r:
        body = b64decode_loose(r.read().decode())
    return body.decode("utf-8", "replace").splitlines()


def main():
    args = [a for a in sys.argv[1:] if not a.startswith("--")]
    write = "--write" in sys.argv
    awg_dir = None
    for i, a in enumerate(sys.argv):
        if a == "--awg-dir" and i + 1 < len(sys.argv):
            awg_dir = sys.argv[i + 1]
            args = [x for x in args if x != awg_dir]

    env = env_file()
    con = sqlite3.connect(DB_PATH)
    rows = list(con.execute("select email, sub_id from clients order by email"))
    if args:
        rows = [r for r in rows if r[0] in args]
    if not rows:
        raise SystemExit("no matching accounts")

    out = []
    for email, sub_id in rows:
        out.append(f"\n===== {email} =====")
        out.append(f"subscription: https://{env['PUBLIC_DOMAIN']}"
                   f"{env['SUB_PATH']}/{sub_id}")
        out.append("  every lane below is already inside that URL; a client"
                   " that supports it needs nothing else.")
        try:
            lines = fetch_subscription(env, sub_id)
        except Exception as ex:
            out.append(f"  !! could not read the subscription: {ex}")
            continue

        for line in lines:
            line = line.strip()
            if not line:
                continue
            scheme = next((s for s in SCHEME_NAMES if line.startswith(s)), None)
            label = SCHEME_NAMES.get(scheme, "unknown")
            if scheme == "vpn://":
                conf = b64decode_loose(line[len("vpn://"):]).decode(
                    "utf-8", "replace")
                out.append(f"\n  -- {label} (import as a .conf) --")
                out.extend("     " + l for l in conf.splitlines())
                if awg_dir:
                    os.makedirs(awg_dir, exist_ok=True)
                    path = os.path.join(awg_dir, f"{email}.conf")
                    with open(path, "w", newline="\n") as f:
                        f.write(conf if conf.endswith("\n") else conf + "\n")
                    os.chmod(path, 0o600)
                    out.append(f"     -> written to {path}")
            else:
                out.append(f"\n  -- {label} --")
                out.append(f"     {line}")
        out.append("")

    text = "\n".join(out)
    print(text)

    if write:
        path = os.path.join(ROOT, "CLIENT-LINKS.txt")
        header = (f"# {env['PUBLIC_DOMAIN']} - client links\n"
                  f"#\n"
                  f"# The subscription URL carries every lane and self-updates."
                  f" The per-lane\n"
                  f"# entries below are the same thing, unpacked, for clients"
                  f" that only take\n"
                  f"# one protocol - notably the AmneziaWG config, which"
                  f" general-purpose\n"
                  f"# proxy clients skip because they do not know the vpn://"
                  f" scheme.\n")
        with open(path, "w", newline="\n") as f:
            f.write(header + text + "\n")
        os.chmod(path, 0o600)
        print(f"\nwritten to {path} (mode 600)")


if __name__ == "__main__":
    main()
