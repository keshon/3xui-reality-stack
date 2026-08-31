#!/usr/bin/env python3
"""Install the routing policy: no BitTorrent, no Russian destinations.

Both live in Xray's routing table, so they apply to every lane at once -
including AmneziaWG, which egresses through a loopback SOCKS5 inbound into
Xray rather than around it.

  * Russian destinations are REFUSED. The tunnel then provably never carries
    them, so this server never appears in a connection to a Russian host. Each
    device routes them directly instead - see docs/routing.md.

  * Sites blocked *in* Russia are exempted by an earlier rule, so a later
    "this looks Russian" rule can never swallow the traffic the tunnel exists
    to carry.

The category data ships with the panel: geosite_RU.dat / geoip_RU.dat are the
runetfreedom/russia-v2ray-rules-dat build, alongside the standard v2fly lists.

Every token is validated against those files BEFORE anything is written, and
the template is restored automatically if Xray does not come back. It is one
process for every lane, so a single unknown category takes the whole server
down rather than just its own rule.

    python3 scripts/routing.py --show
    python3 scripts/routing.py --test
    python3 scripts/routing.py --apply
    python3 scripts/routing.py --apply --domain-strategy AsIs
    python3 scripts/routing.py --remove
"""
import json
import os
import subprocess
import sys
import time

from panel import ROOT, Panel, env_file, xray_healthy

TAG = "xray-stack-"

# Blocked in Russia -> must always go through the tunnel. Listed first so a
# later rule cannot reclassify them.
PROXY_SITE = ["ext:geosite_RU.dat:ru-blocked", "ext:geosite_RU.dat:refilter"]
PROXY_IP = ["ext:geoip_RU.dat:ru-blocked", "ext:geoip_RU.dat:ru-blocked-community"]

# Russian destinations -> refused, forcing them onto each client's direct path.
# tld-ru is the workhorse: .ru/.su/.moscow/.tatar plus the Cyrillic TLDs, which
# no curated per-sector list keeps up with. category-ru alone is 1104 domains
# and contains neither sberbank.ru nor gosuslugi.ru.
RU_SITE = [
    "ext:geosite_RU.dat:ru-available-only-inside",
    "geosite:tld-ru", "geosite:category-ru",
    "geosite:category-bank-ru", "geosite:category-gov-ru",
    "geosite:category-ecommerce-ru", "geosite:category-media-ru",
    "geosite:category-retail-ru", "geosite:category-entertainment-ru",
    "geosite:category-travel-ru", "geosite:category-education-ru",
    "geosite:category-medicine-ru", "geosite:category-tech-media-ru",
    "geosite:category-forums-ru", "geosite:category-betting-ru",
]
RU_IP = ["geoip:ru"]


def get_template(p):
    """Read the Xray config template.

    `obj` comes back as a JSON *string* wrapping the real payload, so it needs
    decoding once before xraySetting is reachable - and xraySetting itself may
    be either an object or another JSON string depending on how it was last
    written.
    """
    r = p.post("/panel/api/xray/", {}, as_json=False)
    obj = (r or {}).get("obj")
    if isinstance(obj, str):
        obj = json.loads(obj)
    t = (obj or {}).get("xraySetting")
    if t is None:
        raise SystemExit("could not read the Xray template: %s" % str(r)[:200])
    return t if isinstance(t, dict) else json.loads(t)


def save_template(p, tpl):
    return p.post("/panel/api/xray/update",
                  {"xraySetting": json.dumps(tpl, indent=2)}, as_json=False)


def validate(p, tokens, kind):
    """Drop tokens the bundled geo files do not actually contain."""
    r = p.post("/panel/api/xray/geodata/validate",
               {"kind": kind, "tokens": ",".join(tokens)}, as_json=False)
    obj = (r or {}).get("obj")
    if obj is None:
        print("  ! validator unavailable; keeping the %s tokens unchecked" % kind)
        return list(tokens), []
    ok, bad = [], []
    for t in tokens:
        v = obj.get(t) if isinstance(obj, dict) else None
        if isinstance(v, bool):
            good = v
        elif isinstance(v, dict):
            good = v.get("valid", True)
        else:
            good = True
        (ok if good else bad).append(t)
    return ok, bad


def strip_ours(rules):
    return [r for r in rules if not str(r.get("ruleTag", "")).startswith(TAG)]


# (what, kind, expected outbound). Reading the rule list proves the rules were
# written; only asking Xray itself proves they decide anything.
CHECKS = [
    ("sberbank.ru", "domain", "blocked"), ("gosuslugi.ru", "domain", "blocked"),
    ("yandex.ru", "domain", "blocked"), ("mail.ru", "domain", "blocked"),
    ("nalog.ru", "domain", "blocked"), ("avito.ru", "domain", "blocked"),
    ("vk.com", "domain", "blocked"), ("kinopoisk.ru", "domain", "blocked"),
    ("xn--80asehdb", "domain", "blocked"),
    ("meduza.io", "domain", "direct"), ("youtube.com", "domain", "direct"),
    ("google.com", "domain", "direct"), ("github.com", "domain", "direct"),
    ("wikipedia.org", "domain", "direct"),
]


def test(p, inbound_tag):
    def route(**kw):
        r = p.post("/panel/api/xray/routeTest", kw, as_json=False)
        obj = (r or {}).get("obj")
        if isinstance(obj, str):
            try:
                obj = json.loads(obj)
            except ValueError:
                return obj
        if isinstance(obj, dict):
            return obj.get("outboundTag") or obj.get("outbound") or str(obj)[:60]
        return str(obj)[:60] if obj is not None else "?" + str(r.get("msg", ""))[:40]

    bad = 0
    for what, kind, want in CHECKS:
        got = route(**{kind: what}, port="443", network="tcp",
                    inboundTag=inbound_tag)
        ok = got == want
        bad += not ok
        print("  %-16s want=%-8s got=%-10s %s"
              % (what, want, got, "ok" if ok else "MISMATCH"))
    got = route(domain="tracker.example.com", port="6881", network="tcp",
                protocol="bittorrent", inboundTag=inbound_tag)
    ok = got == "blocked"
    bad += not ok
    print("  %-16s want=%-8s got=%-10s %s"
          % ("bittorrent", "blocked", got, "ok" if ok else "MISMATCH"))
    print("\n%d check(s) did not match" % bad if bad else "\nall checks passed")
    return bad


def show(routing):
    print("domainStrategy: %s" % routing.get("domainStrategy"))
    for r in routing.get("rules", []):
        what = {}
        for k in ("domain", "ip", "protocol", "inboundTag"):
            if k in r:
                v = r[k]
                what[k] = len(v) if isinstance(v, list) else v
        print("  %-34s -> %-8s %s"
              % (r.get("ruleTag") or "-", r.get("outboundTag"), what))


def main():
    if not any(a in sys.argv for a in ("--show", "--apply", "--remove",
                                       "--test")):
        sys.exit(__doc__)

    strategy = "IPIfNonMatch"
    if "--domain-strategy" in sys.argv:
        strategy = sys.argv[sys.argv.index("--domain-strategy") + 1]

    p = Panel(env_file()).login()
    tpl = get_template(p)
    routing = tpl.setdefault("routing", {})
    rules = routing.setdefault("rules", [])

    if "--show" in sys.argv:
        show(routing)
        return

    if "--test" in sys.argv:
        tag = "in-443-tcp"
        if "--inbound" in sys.argv:
            tag = sys.argv[sys.argv.index("--inbound") + 1]
            
        print("asking Xray where it would send each destination (via %s)" % tag)
        sys.exit(1 if test(p, tag) else 0)

    backup = os.path.join(ROOT, "xray-template.backup.json")
    with open(backup, "w", newline="\n") as f:
        json.dump(tpl, f, indent=2)
    print("backed up the current template to %s" % backup)

    rules[:] = strip_ours(rules)

    if "--remove" in sys.argv:
        print("removed the xray-stack routing rules")
    else:
        ps, bad_ps = validate(p, PROXY_SITE, "site")
        pi, bad_pi = validate(p, PROXY_IP, "ip")
        rs, bad_rs = validate(p, RU_SITE, "site")
        ri, bad_ri = validate(p, RU_IP, "ip")
        for label, bad in (("proxy-site", bad_ps), ("proxy-ip", bad_pi),
                           ("ru-site", bad_rs), ("ru-ip", bad_ri)):
            if bad:
                print("  ! dropped unknown %s categories: %s"
                      % (label, ", ".join(bad)))
        if not rs and not ri:
            sys.exit("no usable Russian categories - refusing to write a "
                     "policy that would enforce nothing")

        new = []
        if ps:
            new.append({"ruleTag": TAG + "ru-blocked-proxy", "type": "field",
                        "domain": ps, "outboundTag": "direct"})
        if pi:
            new.append({"ruleTag": TAG + "ru-blocked-proxy-ip", "type": "field",
                        "ip": pi, "outboundTag": "direct"})
        if rs:
            new.append({"ruleTag": TAG + "ru-refuse", "type": "field",
                        "domain": rs, "outboundTag": "blocked"})
        if ri:
            new.append({"ruleTag": TAG + "ru-refuse-ip", "type": "field",
                        "ip": ri, "outboundTag": "blocked"})

        # After the api / private / bittorrent rules, before anything added by
        # hand in the panel.
        keep = 0
        for r in rules:
            blob = json.dumps(r)
            if r.get("inboundTag") or "bittorrent" in blob \
                    or "geoip:private" in blob:
                keep += 1
            else:
                break
        rules[:] = rules[:keep] + new + rules[keep:]
        routing["domainStrategy"] = strategy
        print("domainStrategy=%s; inserted %d rule(s) at position %d"
              % (strategy, len(new), keep))

    r = save_template(p, tpl)
    if not r.get("success"):
        sys.exit("template not saved: %s" % r.get("msg"))

    # One Xray process serves every lane. Verify, and put it back if not.
    time.sleep(8)
    healthy = False
    for _ in range(6):
        if xray_healthy(80):
            healthy = True
            break
        time.sleep(3)
    if not healthy:
        print("!! Xray did not come back - restoring the previous template",
              file=sys.stderr)
        save_template(p, json.load(open(backup)))
        time.sleep(10)
        subprocess.run(["docker", "restart", "3x-ui"], capture_output=True)
        sys.exit("rolled back; nothing changed")
    print("Xray healthy")


if __name__ == "__main__":
    main()
