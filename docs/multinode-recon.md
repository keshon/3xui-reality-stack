# Этап 0. Разведка multi-node (только чтение)

Дата: 2026-10-15. Изменений кода нет; единственный новый файл — этот отчёт.
Прочитано целиком: `README.md`, `docs/gotchas.md`, `docs/reality.md`,
`docs/diagnosing.md`, `bootstrap.sh`, `install.sh`, `traefik/docker-compose.yml`,
`traefik/docker-compose.dns01.yml`, `traefik/dynamic/tls.yml.tmpl`,
`decoy/docker-compose.yml`, `decoy/nginx.conf`, `xui/docker-compose.yml`,
`.env.example`, `users.txt.example`, `scripts/panel.py`,
`scripts/provision-inbounds.py`, `scripts/consolidate-clients.py`,
`scripts/configure-subscription.py`, `scripts/client-links.py`,
`scripts/add-mtproto.py`, `scripts/add-amneziawg.py`, `scripts/resolve-users.py`,
`scripts/routing.py`.

Апстрим: `github.com/MHSanaei/3x-ui`, ветка `main`, релизы через API GitHub.
Запингованная версия в `xui/docker-compose.yml:9` — `ghcr.io/mhsanaei/3x-ui:v3.7.0`.
Актуальный stable на дату отчёта — `v3.10.2` (опубликован 2026-10-14,
подтверждено `GET api.github.com/repos/MHSanaei/3x-ui/releases/latest`).

## Часть 1. Где код предполагает одну панель

Общая корневая причина во всех пунктах: `scripts/panel.py` строит ровно одно
подключение — `docker inspect 3x-ui` → `http://<ip>:2053<PANEL_PATH>`
(`panel.py:108-159`), логин — один `PANEL_USER/PANEL_PASS` (`panel.py:195-202`).
Всё остальное — следствие: ни одна функция не принимает параметра «какая панель».

| Место | Предположение (файл:строки) |
|---|---|
| `scripts/panel.py` | `container_ip()` по имени `3x-ui`, `xray()`/`mtg()` — `docker exec 3x-ui`, порт `2053`, один `.env` (`36-105`), `xray_healthy()` читает логи одного контейнера (`257-267`) |
| `scripts/provision-inbounds.py` | Один `Panel(e).login()` (`208,216`); один `PUBLIC_DOMAIN` (`212`); `externalProxy.dest = PUBLIC_DOMAIN:443` во всех трёх лейнах (`276,302,333`); `target: traefik:8443`, `serverNames: [PUBLIC_DOMAIN]` (`261-263`); порты `443/12812/443` захардкожены (`246,294,319`); `subId` переиспользуются только с этой панели (`222-227`) |
| `scripts/consolidate-clients.py` | Один `Panel`, `lanes={remark:id}` с одной панели (`51-53`); `attach(who, target)` несёт inbound-id первой панели (`97`); verify-then-delete страхует только её (`99-106`); люди выводятся `split("-",1)[-1]` из одной плоской рассылки (`61`) |
| `scripts/configure-subscription.py` | Пишет `subURI/subJsonURI=https://PUBLIC_DOMAIN/...` на одну панель (`29-45`); вторая панель останется с дефолтом `http://...:2096/sub/<subId>` |
| `scripts/client-links.py` | `fetch_subscription` идёт на `http://<container_ip>:2096<SUB_PATH>` с `Host: PUBLIC_DOMAIN` (`58-65`); `--urls` печатает один `PUBLIC_DOMAIN` (`81-85`); клиенты читаются с одной панели (`79`) |
| `scripts/add-mtproto.py` | Один `PUBLIC_DOMAIN` как FakeTLS-фронт (`44`), один порт (`45`), `mtg` в одном контейнере (`50,83`), ссылка `tg://proxy?server=PUBLIC_DOMAIN` (`91`) |
| `scripts/add-amneziawg.py` | Один `AWG_PORT`/подсеть/MTU (`55,84,101`); `default_iface()` читает маршрут одного хоста (`47-50`); проверка слушателя — `docker exec 3x-ui` (`129-132`); та же подсеть `10.8.1.0/24` на второй ноде даст коллизию |
| `scripts/routing.py` | Один шаблон Xray (`get_template/save_template`, `70-90`), один `docker restart 3x-ui` (`236`), бэкап в один файл (`303`), проверка подписки через `:2096` + `Host: PUBLIC_DOMAIN` (`248-250`); правило `domain:BASE_DOMAIN` (`336-339`) — одна зона |
| `scripts/resolve-users.py` | Панели не касается, но читает один `.env`/`users.txt` — при расхождении файлов по хостам ростеры разъедутся |
| `install.sh` | Один `.env` (`load_env/set_env`, `25-49`); DNS-проверка `PUBLIC+PANEL_DOMAIN → MYIP` одного хоста (`81-87`); `docker exec 3x-ui setting -webBasePath` (`149-151`); все шаги provision — без цикла по нодам (`156-179`); verify — один домен, один `docker logs 3x-ui` (`184-197`) |
| `bootstrap.sh` | Одна сеть `proxy` с одним `PROXY_SUBNET` (`18-35`); один `CERT_BASENAME` (`38`); `compose up` только на текущем хосте (`67-70`) |
| `xui/docker-compose.yml` | `container_name: 3x-ui` (`10`) блокирует второй инстанс на хосте; привязки `443:443/tcp+udp`, `8443`, `${AWG_PORT}` (`17-31`); лейблы Traefik на единственные `PANEL_DOMAIN/PUBLIC_DOMAIN`, `SUB/JSON/TROJAN_PATH` (`44-68`) |
| `traefik/*.yml` | `:443/tcp` сознательно не публикуется — им владеет Xray (`14-15`); один `PROXY_SUBNET` для `proxyProtocol.trustedIPs` (должен совпадать с `xver=2` своей ноды); DNS-01-вариант: одна зона `*.BASE_DOMAIN`, один `acme.json`, один `CF_DNS_API_TOKEN` — две ноды на одной зоне будут гоняться за DNS-01 независимо |
| `decoy/` | Роутеры `Host(PUBLIC_DOMAIN)` (приоритет 1) + catch-all `/` (приоритет 0) — один публичный хост на Traefik; `nginx.conf` нейтрален (слушает `:80`, доменов не знает) |
| `.env.example` | Схема полностью сингулярная: `BASE/PUBLIC/PANEL_DOMAIN`, `PANEL/SUB/JSON/TROJAN_PATH`, `USERS/LANES`, порты, `CERT_BASENAME`, `PROXY_SUBNET`. Места под список нод нет; правка под вторую ноду перезапишет значения первой |

Что сломается первым при появлении второй панели (порядок): `Panel()` всегда
набрёт первую `3x-ui` — скрипты молча проигнорируют вторую ноду (`provision`
скажет «nothing to do»); `consolidate --apply` понесёт inbound-id первой панели
на вторую; вторая панель останется с дефолтным `subURI :2096`.

## Часть 2. Multi-node в 3x-ui v3 — API с источниками

Всё ниже — ветка `main` апстрима; пути даны относительно корня репо
`MHSanaei/3x-ui`, полный префикс — `{basePath}/panel/api/...`
(`internal/web/controller/api.go`: группа `/panel/api`, подгруппа `/nodes` —
«Nodes API — multi-panel management»). Проверено прямым чтением:

- `GET .../operations/multi-node.mdx` (док) и
- `api.github.com/repos/MHSanaei/3x-ui/contents/frontend/src/lib/xray/inbound-clone.ts?ref=main`
  (base64-тело декодировано, 70 строк),
- `GET api.github.com/repos/MHSanaei/3x-ui/releases/latest` → `v3.10.2`.

Релизная рамка (по телам релизов через Releases API): multi-node приехал в
`v3.0.0`; гейты протоколов на нодах — `internal/web/service/inbound_protocol.go`
(`checkNodeCanHostProtocol`): MTProto с `v3.5.0`, AmneziaWG с `v3.7.0`
(ровно наш пин — на грани), TUIC с `v3.8.0`, MASQUE с `v3.9.1`.
Синхронизация и heartbeat hardening (`v3.7.0→v3.10.2`): adopt совпадающих
inbound'ов вместо пересоздания, fan-out правок клиентов на все ноды сразу,
`v3.10.2` — мерж трафика пишет только изменившиеся строки.

### Ноды (`/panel/api/nodes/...`, `internal/web/controller/node.go`, 16 операций)

| Метод | Путь | Назначение; ключевые поля |
|---|---|---|
| GET | `/list` | Все ноды: `NodeView[]` — `status online/offline/unknown`, `latencyMs`, `cpuPct/memPct`, `xrayVersion/panelVersion`, `uptimeSecs`, `lastHeartbeat/lastError`, `xrayState/xrayError`, счётчики |
| GET | `/get/:id` | Одна нода (`NodeView`) |
| GET | `/webCert/:id` | TLS cert/key paths ноды (для «Set Cert from Panel») |
| POST | `/add` | Регистрация; тело `NodeMutationRequest`; без `outboundTag` — `Probe` с таймаутом ~6с; ответ `NodeView` |
| POST | `/update/:id` | Замена connection details; `apiToken` write-only (пусто = keep, флаг = clear) |
| POST | `/del/:id` | Удаление; inbound'ы за ней не мигрируются |
| POST | `/setEnable/:id` | Пауза/возобновление синка (`{enable}`) |
| POST | `/test` | Проба несохранённых данных (без записи) |
| POST | `/certFingerprint` | SHA-256 (base64) leaf-серта для режима `pin` |
| POST | `/inbounds` | Список remote inbound'ов для selective import (по несохранённым данным) |
| POST | `/probe/:id` | Обновить закэшированное здоровье |
| POST | `/updatePanel` | Self-updater нод (`{ids[], dev}`) |
| GET | `/history/:id/:metric/:bucket` | История метрик ноды |
| POST | `/mtls/ca` | CA мастера (PEM, публичная) |
| POST | `/mtls/trustCA` | CA, которой доверяет панель в роли ноды (вступает после рестарта) |
| POST | `/mtls/reloadClient` | Перепроверить mTLS-креды мастера |

Регистрация ноды (`docs/.../operations/multi-node.mdx` + `internal/database/model/model.go`
`type Node`, `internal/web/service/node.go`): `name` (unique), `scheme http|https`,
`address` (required), `port`, `basePath`, `api_token` (required, хранится
шифрованно `nodetoken.Encrypt`, в ответах только `hasApiToken`; не нужен в `mtls`),
`tls_verify_mode verify|skip|pin|mtls`, `inbound_sync_mode all|selected`,
`inbound_tags`, `outbound_tag` (egress bridge), heartbeat-поля пишет мастер.
Heartbeat: `@every 5s` (`internal/web/web.go`, `job/node_heartbeat_job.go`,
32-way fan-out, события `node.up/node.down` в Telegram/Discord, WebSocket
`BroadcastNodes`). Трафик: `job/node_traffic_sync_job.go`, тоже 5с.

### Inbound'ы на нодах — отдельного `/clone` НЕТ

Клон = повторный `POST /panel/api/inbounds/add` с `nodeId`
(`frontend/.../CloneInboundModal.tsx`: цикл по целям, только online+enabled;
`frontend/src/lib/xray/inbound-clone.ts` `buildClonePayload`, подтверждено
дословно): `settings` — копия исходника с `clients=[]`, **`streamSettings` и
`sniffing` — дословная копия**, свежий случайный порт 10000–60000,
`remark+" (clone)"`, `enable:false`, `listen:''`, без `tag` (бекенд назначит с
per-node префиксом), `shareAddrStrategy/shareAddr` наследуются.
В `internal/web/service/inbound_node.go` по поиску `reality|privateKey|shortId` —
совпадений нет: серверной регенерации Reality-ключей не найдено.

### Адрес ноды в ссылках подписки (`internal/sub/service.go`)

Одна подписка (`subId`) на мастере отдаёт все ноды: `getInboundsBySubId` —
один SQL без фильтра по `node_id`, локальные + нодовые inbound'ы мержатся.
`resolveInboundAddress`: кандидаты по `shareAddrStrategy`
(`node|listen|custom`, дефолт `node`) — адрес ноды из таблицы `nodes`
(`loadNodes`), затем `listen`, затем публичный хост/хост запроса; сверху всё
перекрывает legacy `externalProxy`. **Managed hosts** — per-inbound оверрайды
(`type Host` в `model.go`: `address/port/security/sni/hostHeader/path/alpn/
fingerprint/...`, `nodeGuids[]` — скоуп на ноды, `excludeFromSubTypes[]`);
рендерятся поверх всего в `getSubs` через `hostEndpoints`. Док:
`multi-node.mdx` §Managed hosts. БД: SQLite по умолчанию, PostgreSQL только
рекомендован для scale/multi-node (`internal/database/db.go` — таблицы нод в
обоих диалектах; README «Database Options»).

Node→master allowlist (что нода принимает от мастера, `api.go`
`nodeSyncScopeAllow`): `GET /server/status, /inbounds/list, .../hosts/list`;
`POST /inbounds/add, /inbounds/del/:id, /inbounds/update/:id`,
`/clients/add, /del/:email, /:email/detach, /update/:email`,
`/inbounds/pushClientTraffics`, `/server/restartXrayService` и др.

## Часть 3. Ответы на открытые вопросы (раздел 5 ТЗ)

1. **Что видят клиенты при недоступном мастере? Подписку отдаёт только мастер?**
   По коду: подписки строит и отдаёт панель, где лежит `subId` клиента, —
   `internal/sub/service.go` (`getSubs`, тот же fan-out для JSON/Clash).
   Нодовые inbound'ы вливаются в ответ мастера через синкнутые строки, а не
   проксируются живьём. Вывод: при недоступном мастере подписка не обновится
   (неоткуда её взять — на нодах те же `subId` только если клиентов туда
   синкнули, но это уже архитектура «подписка с ноды», в ТЗ не входящая).
   Сами туннели при этом живут: Xray ноды работает автономно.
   Статус: ответ по коду частичный; поведение «нода отдаёт подписку за мастера»
   не исследовалось — см. «Не удалось» п.1.
2. **Копирует ли клонирование inbound'а Reality-ключи и shortIds?**
   Да, дословно. Первоисточник: `frontend/src/lib/xray/inbound-clone.ts`
   (`buildClonePayload`): `settings` = копия с `clients=[]`,
   `streamSettings`/`sniffing` — прямая копия строкой; серверной регенерации
   в `inbound_node.go`/`inbound.go:AddInbound` не найдено (поиск по
   `reality|privateKey|shortId` пуст). Следствие для этапа 3: после
   клонирования ключи и `shortIds` ротировать вручную/скриптом; UI-кнопка
   «clone» для нашей задачи непригодна без пост-обработки.
3. **Может ли мастер ходить на панель ноды через `PANEL_DOMAIN` за Traefik,
   и как ограничить доступ по IP (allowlist, PROXY v2)?**
   Мастер ходит на `{scheme}://{address}:{port}{basePath}panel/api/...`
   (`remote.go`, `FetchCertFingerprint` диал `scheme://addr:port+basePath`).
   Ничто не запрещает указать `PANEL_DOMAIN` ноды как `address` — это обычный
   HTTPS к панели через её Traefik. Но: (а) в этом стеке `PANEL_DOMAIN`
   висит за тем же Xray-fallback → PROXY v2 → Traefik, т.е. доступность API
   ноды зависит от живости её Xray — хрупкая связка для управляющего канала;
   (б) готового IP-allowlist middleware в стеке нет (в `traefik/`,
   `xui/docker-compose.yml` allowlist отсутствует; `forwardedHeaders.trustedIPs`
   — это доверие заголовкам, не фильтр клиентов). Реальные IP за fallback'ом
   есть (PROXY v2 → `proxyProtocol.trustedIPs=${PROXY_SUBNET}`), так что
   Traefik `ipAllowList` на роутере панели технически применим — но в текущем
   коде его нет, это работа этапа 1/2, не констатация. Статус: архитектурно
   возможно, готовой реализации нет.
4. **Какая версия 3x-ui v3.x? Пинить в compose.**
   Запинено `v3.7.0` (`xui/docker-compose.yml:9`); latest stable — `v3.10.2`
   (2026-10-14, подтверждено Releases API). Multi-node есть уже в `v3.7.0`
   (фича с `v3.0.0`), гейты протоколов (`inbound_protocol.go`): MTProto-на-ноде
   требует ≥`v3.5.0` (ок), AmneziaWG-на-ноде — ≥`v3.7.0` (ровно на грани пина),
   дальше только hardening синка (`v3.8.x–v3.10.x`). Рекомендация для этапов:
   поднять пин минимум до `v3.9.1+`, лучше `v3.10.2`, иначе чинить баги синка,
   уже исправленные апстримом. Отдельное решение, фиксируется на этапе 1.
5. **`consolidate-clients.py` и `client-links.py` при нескольких нодах?**
   Оба — одно-панельные (см. часть 1): consolidate несёт inbound-id первой
   панели и сольёт/удалит только её дубликаты; вторая нода останется с
   per-lane клонами (`r-/t-/h-`), т.е. по одной ссылке на подписку вместо всех
   лейнов. `client-links --urls` покажет только первую панель. Оба требуют
   параметризации панелью (этап 4).
6. **MTProto и AmneziaWG на нодах как в одиночном стеке?**
   По бекенду — да, оба node-eligible (`inbound_protocol.go`:
   `nodeEligibleProtocols` включает `MTProto:true, AmneziaWG:true`;
   фронтенд-зеркало `frontend/src/lib/xray/node-protocols.ts`). Условия:
   версия ноды ≥ гейта (`v3.5.0`/`v3.7.0`); неизвестная `panelVersion`
   (нет heartbeat) — отказ. Нюансы из кода: AWG-пиры назначаются через client
   API как и локально; в `sub/service.go:clientsForLinkExport` WG/AWG-ссылки
   всегда берут идентити из settings-своего inbound'а — при одном клиенте на
   нескольких tunnel-inbound'ах поля схлопываются к последнему синку (дизайн,
   не баг). MTProto-FakeTLS per-client секрета на нодах дальше не копалось —
   см. «Не удалось» п.2.

### Не удалось установить (не угадываю — блокеры/проверки для этапов)

1. Отдаёт ли панель ноды подписку самостоятельно так же, как мастер
   (нужно ли/можно ли держать подписки доступными при падении мастера) —
   `sub/service.go` прочитан частично (по отчёту), прямой проверки нет.
   Влияет на этап 5/6 (runbook «мастер недоступен»).
2. Семантика синка MTProto-секретов и AWG-пиров при fan-out правок
   (`v3.8.0` «client edits fan out») на уровне полей — нужен стенд.
   Влияет на этап 4 (reconcile должен писать через API мастера, не нод).
3. Поведение `inbound_sync_mode: selected` по тегам в нашей схеме remark'ов
   (`DE-/NL-/FR-reality`) — теги vs remark'ы; проверить на стенде на этапе 3.
4. Гонка DNS-01 двух нод за `*.BASE_DOMAIN` с раздельными `acme.json` —
   вероятно доброкачественная (один wildcard, идемпотентный выпуск), но замера
   нет. Влияет на этап 1 (роль `node` и ACME).
5. Точный минимальный пин выше `v3.7.0` — решение этапа 1 после прогона
   мастера и ноды на одной версии (кандидат `v3.10.2`).

## Вывод для следующих этапов

- Блокеров «нельзя сделать» не найдено: нужные API существуют ицитированы.
- Этап 1: ввести `ROLE`, параметризовать `Panel` выбором цели; решить пин
  (кандидат `v3.10.2`); для `node` не трогать `users.txt`/подписки, но
  выпускать серт, поднимать Traefik/decoy/Xray/панель.
- Этап 2: `nodes.txt` + `nodes.py` поверх `POST /panel/api/nodes/add|/test`
  (`NodeMutationRequest`: name/scheme/address/port/basePath/apiToken/
  tls_verify_mode/inbound_sync_mode).
- Этап 3: НЕ пользоваться UI-клоном (копирует `privateKey`/`shortIds`
  дословно) — создавать лейны `POST /inbounds/add` с `nodeId` и свежими
  ключами; адрес в ссылках — `shareAddrStrategy=node` (дефолт тянет адрес
  ноды) или managed hosts со скоупом `nodeGuids`; проверить отсутствие
  `PANEL_DOMAIN` в подписке.
- Этап 4: reconcile — писать клиентов через API мастера (fan-out на ноды —
  дело панели `v3.8.0+`), dry-run по умолчанию, `--prune` отдельно.
- Приёмка этапа 0: `git status` — только `docs/multinode-recon.md`.

Откат: `git revert <коммит>` — отчёт ни на что не влияет.
