# Putting your own webapps behind this stack

The reverse proxy still does everything it normally does. It just no longer
owns `:443` — Xray does, and anything that is not a valid Reality handshake is
handed to Traefik on an internal `:8443`. Your apps do not need to know that.

What changes is only the label vocabulary.

## The two entrypoints

| name | port | notes |
|---|---|---|
| `web` | `:80` | published; redirects everything to `https://…:443` |
| `websecure` | `:8443` | TLS terminated here; mapped separately by the management binding |

`websecure` is where app routers go. Public HTTPS reaches it through Xray's
fallback. The management port also maps to this entrypoint; on nodes that
binding defaults to all interfaces. Restrict management access appropriately.
See the [listener map](architecture.md#listeners).

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
  forward UDP, so `PUBLIC_DOMAIN` and `PANEL_DOMAIN` must stay **DNS-only**,
  grey cloud. Keep traffic endpoints direct; an HTTP CDN proxy cannot forward
  Reality authentication or the UDP transports.

## What not to put here

Anything that wants its own TCP or UDP port on the host. Ports `443/tcp`,
`443/udp`, `80/tcp`, `MTPROTO_PORT` and `AWG_PORT` belong to this stack; publish
an app on any other free port directly from its own compose file, as usual.

For shared deployments that need extra Traefik listeners or dashboard routing,
put deployment-specific Compose settings in `.stack/traefik.compose.yml`.
Bootstrap and source upgrades include this file after the selected Traefik
Compose file; state backups preserve it. Keep app containers in their own
Compose projects. A `command` override replaces the whole Traefik command
list, so review it when upgrading Traefik settings.
