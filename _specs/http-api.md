# wlhl HTTP API — local logs/chat + room lifecycle for the admin panel

## Why

Three needs, one mechanism (all requested 2026-07-18):

1. **Chat logs out of RTDB.** Chat is bulk, append-only data; RTDB reads are
   metered and awkward to page. Store chat locally on the wlhl host, serve it
   to ext-proxy on demand.
2. **wlhl logs on the admin panel.** Room logs can grow huge (error loops), so
   they can never live in RTDB; they already live on the wlhl host
   (`--log-file`). Serve bounded tails on demand.
3. **Room creation from the panel.** A grant with create permission can start,
   stop and create rooms — capped by webliero.com's hard limit of 4 rooms per
   IP, and further limitable by the super admin.

wlhl already has all the primitives (launch/stop/ls over IPC, per-room log
fan-out, a rotating file writer). The API is a thin authenticated HTTP layer
over them plus two small local stores.

## Topology

ext-proxy (cloud, fly.dev) must reach wlhl (home server, NAT). Reuse the
chat-backend pattern that already works in production: the operator exposes
wlhl's HTTP listener through a tunnel (cloudflared) and ext-proxy calls it
with a bearer token. wlhl binds `127.0.0.1` by default — the tunnel is the
only way in, and the token still guards it.

```
panel (browser) → ext-proxy /padmin/<room>/api/… → [tunnel] → wlhl :8091 /api/…
```

## wlhl side

New flags on `server`:

| flag | default | meaning |
|---|---|---|
| `--http <addr>` | off | enable the API, e.g. `127.0.0.1:8091` |
| `--http-token <tok>` | env `WLHL_API_TOKEN` | bearer token (required if `--http`) |
| `--data-dir <dir>` | `./wlhl-data` | chat store root |
| `--profiles-dir <dir>` | off | room profiles for creation (see below) |
| `--max-rooms <n>` | 4 | hard cap on concurrently running rooms (webliero per-IP limit) |

Env `WLHL_SOCKET` overrides the IPC socket path (also lets a second test
instance run beside a live one).

### Endpoints (all `Authorization: Bearer <token>`, JSON unless noted)

- `GET /api/health` → `{ok, rooms, uptime}`
- `GET /api/rooms` → `[{id, code, link, registered}]`
- `POST /api/rooms` `{id, token, profile, conf}` → launch. `profile` names a
  subdirectory of `--profiles-dir`; its `*.js` run in **alphabetical order**
  (the fork's `_`/`z_` prefix convention). CONFIG = the profile's optional
  `_conf.defaults.json` (host-local settings — e.g. the firebase web-SDK
  block — live on the host, next to the scripts that need them) overlaid with
  the API's `conf` (caller wins per top-level key), materialized as
  `const CONFIG = {...};` injected **first**, replacing any `_conf.js`. No
  arbitrary code crosses the API — callers pick a named profile and
  parameters only.
- `DELETE /api/rooms/{id}` → stop.
- `GET /api/rooms/{id}/logs?tail=N` (text/plain) → last N lines from the
  room's in-memory ring buffer (2000 lines kept; N capped at 1000). Bounded
  by construction — an error loop can never make this response huge.
- `GET /api/rooms/{id}/chat/dates` → `["YYYYMMDD", …]`
- `GET /api/rooms/{id}/chat?date=YYYYMMDD&limit=200&before=<ts>` → newest-first
  page of `{ts, name, auth, msg}` from the local store.

### Chat store

The fork emits one structured console line per chat message:

```
@@CHAT@@ {"ts":1784…,"name":"daro","auth":"…","msg":"gg"}
```

wlhl's console listener detects the prefix and appends the JSON to
`<data-dir>/chat/<roomId>/<YYYYMMDD>.jsonl`. The raw line stays in the normal
log too (logs remain the ground truth; the store is the queryable index).
The fork keeps dual-writing chat to RTDB until the panel reads from the host
API everywhere; then the RTDB write can be dropped.

### Room ring buffer

`RoomPage` keeps the last 2000 log lines (timestamped) regardless of how the
room was launched (IPC or HTTP). `logs?tail=` reads from it; history beyond
the ring lives in `--log-file`.

## ext-proxy side

- Firestore `hosts` collection `{id, name, url, token, maxRooms, enabled}` —
  owner CRUD on `/admin` (same shape as the chat Backend section). `token` is
  never sent to browsers.
- `Room` gains `hostId` — set automatically at panel creation, settable by the
  owner for existing rooms.
- `RoomAdmin` gains `canCreate bool`, `createLimit int` (0 = no extra limit
  under the host cap). Set from the grant modal.
- Panel endpoints:
  - `GET /padmin/<room>/api/hostlogs?tail=N` — super grants only; proxies to
    the room's host with the host token; tail ≤ 500, upstream timeout 10s,
    response body capped (512 KB) with `io.LimitReader`.
  - `POST /padmin/api/createroom` `{hostId, roomId, name, maxPlayers, public,
    token}` — requires `canCreate`; counts the host's live rooms via
    `GET /api/rooms` and refuses at `min(host.maxRooms, 4)` (and the grant's
    `createLimit` for rooms it created); on success registers the Room doc
    (`createdByGrant`), appends the room to the grant's room set, returns the
    panel URL. The webliero **headless token** is pasted by the creating admin
    (tokens are minted per-human on webliero.com; wlhl never stores them).
  - `DELETE /padmin/<room>/api/hostroom` — stop the room; super grants or the
    creating grant.

## Phasing

1. ✅ **wlhl API + chat store + ring buffer** (e9d2822) — tested with a second
   server instance (`WLHL_SOCKET` override) and dummy rooms.
2. ✅ **Fork chat emit** (webliero-simple-panel 1405c2e; RTDB dual-write kept).
3. ✅ **ext-proxy: hosts CRUD + hostlogs proxy + panel Server-log tab**
   (ext-proxy aa95ac4).
4. ✅ **ext-proxy: createroom/stop + grant perms + picker Create-room form**
   (same commit). End-to-end verified against a live test host.
5. Later: panel chat tab reads from the host API when the room has a host;
   then drop the fork's RTDB chat write. Also later: room "restart with new
   token" flow (stop keeps settings; relaunch just needs a fresh token).

## Deploy notes

- wlhl: rebuild + restart with `--http 127.0.0.1:8091 --http-token …
  --data-dir … --profiles-dir <dir-with-default/> [--log-file …]`; put a
  tunnel (cloudflared) in front; `default` profile = the room script set for
  panel-created rooms (e.g. a webliero-simple-panel checkout — `_conf.js` is
  replaced by the generated CONFIG; add `_conf.defaults.json` with the
  firebase web-SDK block + any host-local defaults).
- ext-proxy: register the host under /admin → "Room hosts". (No firebase env
  needed — the host profile's `_conf.defaults.json` carries the firebase
  web-SDK block.)
