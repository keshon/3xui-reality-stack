# Multi-node

One master panel manages 2-3 expendable traffic nodes (DE, NL, FR). Users
are described once in `users.txt`; each gets one subscription URL from the
master carrying every lane of every node. A burned node is replaced in
minutes - see [replace-node.md](replace-node.md) for the runbook.

```
                        users.txt (master only)
                             |
                    +------------------+
                    | MASTER (Germany?)|  3x-ui, ROLE=master
                    | PUBLIC_DOMAIN    |  serves subscriptions,
                    | PANEL_DOMAIN     |  owns users, pushes lanes+clients
                    +--------+---------+
                             | panel API (node-sync Bearer tokens)
              +--------------+--------------+
              |              |              |
     +--------+------+ +-----+--------+ +---+------------+
     | NODE de       | | NODE nl      | | NODE fr        |
     | ROLE=node     | | ROLE=node    | | ROLE=node      |
     | :443 Xray    | | :443 Xray    | | :443 Xray      |
     | fallback->   | | fallback->  | | fallback->    |
     | traefik:8443 | | traefik:8443 | | traefik:8443   |
     +-------------------------------+------------------+
     each: own PUBLIC_DOMAIN + wildcard cert + own Reality keys
```

The data path never changes: Xray owns :443 on every host, Traefik sits
behind it via fallback + PROXY v2. Nothing is ever put in front of Xray.

## Roles

| ROLE | users.txt | lanes | subscriptions | panel |
|---|---|---|---|---|
| `standalone` (default) | local | local | local | password + UI, as before |
| `master` | owns it | local + pushes to nodes | serves all | password + UI, plus nodes register here |
| `node` | none | from master (stage 3) | none | node-sync Bearer token only (`NODE_API_TOKEN`); password stays a local bootstrap secret |

## Fleet conventions (all .env files, master and nodes)

- One DNS zone (`BASE_DOMAIN`/`CERT_BASENAME`): every `PUBLIC_DOMAIN` is a
  host in it, one wildcard cert covers all. The hysteria cert path is
  rendered from `CERT_BASENAME` - a node in another zone gets a wrong path
  and its Xray will not start.
- Same `TROJAN_PATH` everywhere: node Traefik routers render from the
  node's own `.env` and must match the `{node}-trojan-httpupgrade` inbound.
- Same 3x-ui version everywhere, >= v3.7.0 (scoped `node-sync` tokens and
  AmneziaWG-on-node both gate on it; sync hardening after it is worth
  having - latest tested pin is in `xui/docker-compose.yml`).
- `users.txt` / `nodes.txt` exist only on the master, both mode 600.

## Deploying three nodes

On the master (fresh or converted standalone: set `ROLE=master`, re-run
`./install.sh` - standalone behaviour is unchanged):

```bash
cp nodes.txt.example nodes.txt
$EDITOR nodes.txt   # de/nl/fr lines (panel-url carries each PANEL_PATH)
chmod 600 nodes.txt
```

Per node VPS (root, Debian/Ubuntu, firewall: 80/tcp, 443/tcp+udp, plus
MTPROTO/AWG ports; A records for its two domains, grey cloud):

```bash
git clone <repo> /opt/3xui-reality-stack && cd $_
cp .env.example .env
$EDITOR .env        # ROLE=node, this node's PUBLIC_DOMAIN/PANEL_DOMAIN
sudo ./install.sh   # cert, Traefik, decoy, Xray, panel, NODE_API_TOKEN
grep NODE_API_TOKEN .env   # -> paste into master's nodes.txt
```

Back on the master, per node (or all at once - every step is idempotent):

```bash
python3 scripts/nodes.py add <name> && python3 scripts/nodes.py test <name>
# ... repeat for all three, then once:
python3 scripts/provision-inbounds.py    # <name>-* lanes, fresh keys each
python3 scripts/reconcile-clients.py --apply
./scripts/verify.sh                      # must be ALL GREEN before handing out URLs
./scripts/healthcheck.sh                 # exit 0
```

Schedule on the master: `healthcheck.sh` (cron, alerts on non-zero, optional
Telegram via `TELEGRAM_BOT_TOKEN`/`TELEGRAM_CHAT_ID`) and `backup.sh`
(panel DB, `.env`, `users.txt`, `nodes.txt`, certs; `BACKUP_KEEP` rotation).

Adding a user afterwards: append to `users.txt`, `reconcile-clients.py
--apply`. Removing: delete the line (stops re-creation), then
`reconcile-clients.py --prune --apply` to actually revoke.

## Rollback

- Per stage: `git log --oneline` shows one commit per stage
  (0 recon, 1 ROLE, 2 nodes, 3 lanes, 4 reconcile, 5 ops, 6 this doc) -
  `git revert` the stage commit. Code has no migrations.
- Data created along the way is panel state, not code: unregister nodes
  (`nodes.py remove <name> --yes`), delete `<name>-*` inbounds in the panel,
  revoke clients (`reconcile-clients.py --prune --apply` after restoring
  `users.txt`). A node host itself is disposable: power it off.
- Back to single server: set `ROLE=standalone`, delete `nodes.txt`,
  re-run `./install.sh`. Node lanes already synced stay until deleted in
  the panel - they do not break local lanes.

## Known limitations (will not surprise you if read first)

- MTProto and AmneziaWG lanes are NOT provisioned on nodes - only
  vless-reality, trojan-httpupgrade, hysteria2. The master's own mtproto /
  amneziawg lanes are still in every subscription, so those protocols keep
  working via the master host.
- Subscriptions come only from the master. Master down = subscriptions stop
  updating, but established tunnels on the nodes keep working (node Xray
  runs autonomously).
- All users go to all nodes: no per-node assignment, no per-node limits.
  `USER_LIMIT_GB`/`USER_EXPIRY_DAYS` apply fleet-wide (0 = unlimited).
- Node API tokens are write-only upstream: the master never reads them
  back, so `nodes.txt` (mode 600, never committed) is the only copy of the
  secret. Lose it = mint a new one on the node (`mint-node-token.py
  --rotate`) and update the line.
- `inbound_sync_mode` is always `all`; selective sync by tag is a panel
  feature this stack does not use.
- UDP healthcheck cannot prove a QUIC listener (QUIC answers nothing
  without a handshake): refused = fail, timeout = pass. The TCP, cert and
  master-status steps carry the verdict; see `healthcheck.sh` header.
- The master reaches node panels over HTTPS through each node's own
  fallback chain (Xray -> Traefik). If a node's Xray is down, its panel is
  unreachable too - that is what the master status check reports.
- Version floor is v3.7.0, not a suggestion: older panels have no scoped
  tokens (`mint-node-token.py` fails with a version hint) and refuse
  AmneziaWG-on-node.
