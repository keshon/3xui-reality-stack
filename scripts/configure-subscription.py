#!/usr/bin/env python3
"""Point 3x-ui's subscription service at the public HTTPS URL.

Out of the box `subURI` is empty, so 3x-ui builds subscription links from
`subPort` + `subPath` and hands out something like

    http://panel.example.com:2096/sub/<subId>

which is unusable: port 2096 is only on the container network, the scheme has
no TLS, and `/sub/` is not the path the reverse proxy forwards. This sets:

  subPath / subJsonPath   what the panel SERVES  (must equal what Traefik forwards)
  subURI  / subJsonURI    what the panel PRINTS  (the public https URL)

Run on the server:  PANEL_USER=... PANEL_PASS=... python3 scripts/configure-subscription.py
"""
import sys

from panel import Panel, env_file


def main():
    e = env_file()
    sub = e["SUB_PATH"].strip("/")
    jsn = e["JSON_PATH"].strip("/")
    # PUBLIC_DOMAIN, not PANEL_DOMAIN: the AmneziaWG client config takes its
    # Endpoint from the host the subscription came from, and nothing overrides
    # it. Serving subs from the admin hostname leaks it into every config.
    domain = e["PUBLIC_DOMAIN"]

    p = Panel(e).login()
    cur = p.settings()
    if not cur:
        sys.exit("could not read panel settings")

    new = dict(cur)
    new.update({
        "subPath": f"/{sub}/",
        "subJsonPath": f"/{jsn}/",
        "subURI": f"https://{domain}/{sub}/",
        "subJsonURI": f"https://{domain}/{jsn}/",
        "subEnable": True,
        "subJsonEnable": True,
    })
    r = p.update_settings(new)
    if not r.get("success"):
        sys.exit(f"subscription settings not applied: {r.get('msg')}")
    print(f"subscription base: https://{domain}/{sub}/<subId>")


if __name__ == "__main__":
    main()
