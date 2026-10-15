# Runbook: replace a burned node

A node is expendable: when its IP burns (blocking, CAPTCHA-everywhere,
throttling that follows the address, not the config), you spin up a fresh
VPS and drop the old one. One command per step, in this order. Timings are
for 2-3 nodes and 40-60 users; nothing here restarts the surviving nodes.

Notation: `<name>` is the nodes.txt label (de, nl, fr), `<old>` the burned
host, `<new>` the replacement.

## 0. What you need at hand

- The repo on the master, `nodes.py`/`reconcile-clients.py`/`verify.sh`
  working (`nodes.py list` shows the other nodes online).
- A fresh VPS (Debian/Ubuntu, root, 1 GB RAM is enough), provider firewall
  open for 80/tcp, 443/tcp, 443/udp, plus your MTPROTO/AWG ports.
- DNS control for the zone.

## 1. DNS

Point the node's hostnames at the NEW ip. Two options:

- **Keep the names** (clients notice nothing): change the A records of the
  node's `PUBLIC_DOMAIN` and `PANEL_DOMAIN` to the new IP, wait for
  propagation (`getent ahostsv4 <name>` from the master).
- **Rotate the names** (the old ones may be burned too): create A records
  for fresh names, use those in step 2. Old subscriptions keep working
  until step 5 re-points them - clients re-fetch the same URL and get the
  new addresses.

## 2. Install the new node

On the NEW vps, as root:

```bash
git clone https://github.com/keshon/3xui-reality-stack.git /opt/3xui-reality-stack
cd /opt/3xui-reality-stack
cp .env.example .env
$EDITOR .env   # ROLE=node, same BASE_DOMAIN zone as the fleet,
               # PUBLIC_DOMAIN/PANEL_DOMAIN of this node,
               # same TROJAN_PATH and CERT_BASENAME as the master
sudo ./install.sh
```

`install.sh` with `ROLE=node` skips users, lanes and subscriptions, but
issues the wildcard cert, brings up Traefik/decoy/Xray/panel and mints the
node-sync API token into `.env` as `NODE_API_TOKEN` (mode 600).

Copy that token to the master (clipboard, password manager - never chat):

```bash
# on the new node:
grep NODE_API_TOKEN .env
```

## 3. Register the node on the master

On the MASTER:

```bash
$EDITOR nodes.txt   # replace the <old> line: new panel-url (with the new
                    # PANEL_PATH from the new node's .env), new token,
                    # new or kept public-domain
chmod 600 nodes.txt
python3 scripts/nodes.py add <name>    # idempotent; conflicts fail loud
python3 scripts/nodes.py test <name>   # must say online
```

If you kept the node NAME, `add` reports a CONFLICT against the still
registered old row: `nodes.py remove <name> --yes` FIRST (this only
unregisters the panel row; the old host keeps serving until step 5), then
`add` again. Never reuse one NAME for two live nodes - remarks
(`<name>-reality`...) would collide.

## 4. Lanes and users

Still on the master:

```bash
python3 scripts/provision-inbounds.py   # creates <name>-* lanes with FRESH
                                        # Reality keys (never a copy)
python3 scripts/reconcile-clients.py --apply   # attaches everyone
./scripts/verify.sh                     # keys differ, no PANEL_DOMAIN in
                                        # subscriptions, fallback certs
```

`verify.sh` failing anywhere stops the runbook here - fix it before
touching the old node. Hand the subscription to one user first (README
rule): one report beats sixty.

## 5. Retire the old node

Only when the new one is verified:

```bash
python3 scripts/nodes.py remove <old-name> --yes   # same name: already done in step 3
# delete the <old>-reality / <old>-trojan-httpupgrade / <old>-hysteria2
# inbounds in the master panel (Panel -> Inbounds), or leave them disabled
# for a day so stragglers fail over instead of hard-failing
```

Then: power off the old VPS, delete it at the provider, remove stale DNS
records. Rotate nothing else - the surviving nodes keep their keys, and
per-user credentials never lived on the node (unified clients belong to
the master and fan out).

## 6. Rollback

- New node broken, old still alive: `nodes.py remove <name> --yes` for the
  new row, re-add the old line to nodes.txt (`add` + `test`), destroy the
  new VPS. Clients never saw an unverified lane if step 4 was not run.
- New lanes created but bad: delete the `<name>-*` inbounds in the panel;
  `provision-inbounds.py` re-creates them from scratch on the next run.
- Every step is re-runnable: `install.sh`, `nodes.py add`, `provision`,
  `reconcile --apply` are all idempotent; only `remove --yes` and
  `--prune` destroy, and both say so beforehand.

## 7. After

- `./scripts/healthcheck.sh` from cron (non-zero on any problem, optional
  Telegram alert) and `./scripts/backup.sh` on the master (panel DB, .env,
  users.txt, nodes.txt, certs; rotation included).
- If the MASTER burned instead of a node: that is a different procedure
  (panel DB restore from backup onto a fresh standalone install, then
  re-register nodes) - deliberately not covered here until tested once.
