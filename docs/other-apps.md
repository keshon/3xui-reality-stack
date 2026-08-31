# Putting your own webapps behind this stack

The reverse proxy still does everything it normally does. It just no longer
owns `:443` — Xray does, and anything that is not a valid Reality handshake is
handed to Traefik on an internal `:8443`. Your apps do not need to know that.

What changes is only the label vocabulary.

## The two entrypoints

| name | port | notes |
|---|---|---|
| `web` | `:80` | published; redirects everything to `https://…:443` |
| `websecure` | `:8443` | **internal**, never published; TLS terminated here |

`websecure` is where every app router goes. It is not reachable from outside
the host — Xray dials it — so nothing is exposed by not publishing it.

## The minimum an app needs

```yaml
services:
  myapp:
    networks: [proxy]                       # the external 'proxy' network
    labels:
      - traefik.enable=true
      - traefik.http.routers.myapp.entrypoints=websecure
      - traefik.http.routers.myapp.rule=Host(`myapp.example.com`)
      - traefik.http.services.myapp.loadbalancer.server.port=8080

networks:
  proxy:
    external: true
```

Then add an A record for `myapp.example.com` pointing at this server. Nothing
else: the wildcard certificate already covers it.

## Three things to leave out

**No `tls=true`, no `tls.certresolver`.** TLS is configured on the `websecure`
entrypoint, together with the `*.BASE_DOMAIN` wildcard. A router-level
`certresolver` *overrides* that block, and Traefik then quietly issues a
separate per-hostname certificate — which publishes that hostname in the public
Certificate Transparency logs and undoes the reason the wildcard exists.

**No HTTP→HTTPS router.** The redirect is configured on the `web` entrypoint
itself, so every host gets it. A per-app `-http` router and a `redirectscheme`
middleware are redundant.

**No `letsencrypt` resolver name.** This stack's resolver is called `le`, and
you should not be naming it in an app's labels anyway (see above).

## Migrating an app from a stock Traefik setup

The usual stock compose file uses entrypoints named `http`/`https` and a
resolver named `letsencrypt`. To convert:

```
entrypoints=https              ->  entrypoints=websecure
tls=true                       ->  delete
tls.certresolver=letsencrypt   ->  delete
the whole <app>-http router    ->  delete
the <app>-to-https middleware  ->  delete
```

Everything else — service port, headers middlewares, auth middlewares —
carries over unchanged.

## Cloudflare-proxied hostnames

An orange-cloud hostname works: Cloudflare connects to the origin on `:443`,
Xray sees a connection that is not Reality, and it lands on Traefik like any
other. Two conditions:

* the zone's SSL mode must be **Full** or **Full (strict)**. *Flexible* makes
  Cloudflare connect to `:80` instead, where the redirect to `https` sends it
  straight back — a redirect loop.
* it must be an ordinary HTTP app. Cloudflare terminates TLS and does not
  forward UDP, so `PUBLIC_DOMAIN`, `PANEL_DOMAIN` and `TOOLS_DOMAIN` must stay
  **DNS-only**, grey cloud. Proxying those breaks every tunnel lane.

## What not to put here

Anything that wants its own TCP or UDP port on the host. Ports `443/tcp`,
`443/udp`, `80/tcp`, `MTPROTO_PORT` and `AWG_PORT` belong to this stack; publish
an app on any other free port directly from its own compose file, as usual.
