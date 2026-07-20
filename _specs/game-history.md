# Game History (SQLite on wlhl) + Live Queue + Multi-Region

Status: design (2026-07-20). Companion to `_specs/host-link.md` (the transport
this rides on). Implements: "local store for history — better than JSONL for
size/querying/analysis/replication/backup; multi-region sharding with common
aggregation and merged analysis; live queue on the stats panel, imperceptibly
fast, browser notification when it's your turn."

## Decisions

- **Engine: SQLite via `modernc.org/sqlite`** (pure Go, no CGO — wlhl must keep
  building for random Windows machines). WAL mode. One DB per room:
  `<data-dir>/games/<room>.db` (parallel to `chat/<room>/`).
- **RTDB keeps the bounded aggregates** (z_stats increments, unchanged). SQLite
  keeps the unbounded per-game rows — disk is where append-forever belongs.
  Nothing ever bulk-reads RTDB; nothing unbounded is ever written to it.
- **Rooms emit, wlhl stores**: the room script prints one console line per
  game; wlhl captures it exactly like `@@CHAT@@`. Rooms without a wlhl host
  simply have no history (aggregates still work).

## 1. Emission (fork, z_stats.js)

At the end of `statsOnGameEnd` (all deltas already computed), emit ONE line:

    @@GAME@@ {"ts":<ms>,"map":"<name>","n":<fullCount>,"durationMs":<ms>,
              "players":[{"auth","name","team","score","kills","deaths",
                          "rank","elo","eloDelta","partial"}...],
              "winner":"<auth>"|null, "partial":<bool>}

- Emitted for EVERY game (not just duels) — history is general; 1v1 analysis
  filters `n==2`.
- **Field availability (verified against z_stats.js):** `team` must be CAPTURED
  at statsAddParticipant (participants don't carry it today; a mid-game leaver
  is gone from getPlayerList at game end). `eloDelta` = `newElo - elo` (no such
  field exists yet — compute at emission). `rank`/`elo`/`eloDelta` are null for
  midSession/partial players and N<2 games. `winner` = the single full player
  with rank===1 (statsAssignRanks averages ties → a top tie yields rank 1.5 →
  winner null; same rule for every N, not just duels). `durationMs` is 0 when
  the game spanned a mid-game script reload (statsGameStartTs reset).
- `partial` mirrors the existing partial-game semantics (N<2 or aborted).
- Marker line is fire-and-forget inside try/catch: emission can never affect
  the game loop. No RTDB write.

## 2. Storage (wlhl, internal/gamestore)

Schema (v1, created on first open; `PRAGMA user_version` for migrations):

    CREATE TABLE games (
      id INTEGER PRIMARY KEY,           -- rowid
      ts INTEGER NOT NULL,              -- epoch ms
      map TEXT NOT NULL DEFAULT '',
      n INTEGER NOT NULL,               -- full-game participant count
      duration_ms INTEGER NOT NULL DEFAULT 0,
      winner_auth TEXT,                 -- null = tie/partial
      partial INTEGER NOT NULL DEFAULT 0,
      raw TEXT NOT NULL                 -- the original JSON (schema escape hatch)
    );
    CREATE INDEX games_ts ON games(ts);
    CREATE INDEX games_map ON games(map);
    CREATE TABLE game_players (
      game_id INTEGER NOT NULL REFERENCES games(id),
      auth TEXT NOT NULL, name TEXT NOT NULL,
      team INTEGER, score INTEGER, kills INTEGER, deaths INTEGER,
      rank INTEGER, elo INTEGER, elo_delta INTEGER
    );
    CREATE INDEX gp_auth_ts ON game_players(auth, game_id);
    CREATE INDEX gp_game ON game_players(game_id);

Store API: `Append(room, json)` (parse, insert both tables, single tx),
`Query(room, {beforeID|beforeTs, limit, auth?, map?, n?})` newest-first,
`PlayerSummary(room, auth, {sinceTs?})`, `Aggregate(room, spec)` (see §4),
`Dates`, `ExportJSONL(room, w)` (portability/backup escape hatch).
Single APPLICATION writer (the capture goroutine); DB access is still
mutex/tx-guarded like chatstore, and the backup CLI opens a second connection
relying on WAL read-consistency. --data-dir must be LOCAL disk — WAL is unsafe
on SMB/network shares (worth a doc note for Windows operators). The combined
Query filters (auth+map/n/beforeTs) join `games` per candidate row rather than
using a composite index — fine at these volumes. Malformed marker JSON → log + drop, never crash (chatstore precedent).

Backups: `Backup(room, path)` via `VACUUM INTO` (modernc.org/sqlite does NOT
expose the C online-backup API; VACUUM INTO yields a consistent copy of a live
WAL DB); wired to a CLI (`wlhl games backup <room> <dest>`). Continuous off-site replication stays an
ops choice (Litestream sidecar works unmodified on a WAL SQLite file) — not
built into wlhl.

## 3. API (wlhl HTTP, same bearer + caps as chat)

    GET /api/rooms/{id}/games?limit&beforeId&auth&map&n     → {games:[...], nextBefore}
        limit default 50, MAX 150 — a full page must stay under ext-proxy's
        512KB hostRespCap (500 games x ~1.1KB would overflow it)
    GET /api/rooms/{id}/games/player/{auth}?sinceTs         → summary + recent
    GET /api/rooms/{id}/games/aggregate?spec=...            → partial aggregates (§4)
    GET /api/rooms/{id}/games/dates                         → day list (UI paging aid)

Proxied by ext-proxy (`hostapi` today, host-link frames once live) with the
same 15s/512KB bounds. Panel gets a **Matches** tab (NOT "History" — that tab already exists for the
change-log/undo feature) (room-wide + per-
player filter, "load older" paging); the public stats page gets recent-matches + ELO-over-time — note this is NEW
plumbing for statspage.go, which today is RTDB-only: it gains a host-proxied,
CACHED (60s) games call via callHost, degrading to no-matches when the host is
offline.

## 4. Multi-region

Model: a "room group" = N regional rooms (e.g. arena-eu on host A, arena-na on
host B). Each game happens on exactly ONE host → shards never overlap.

- **Common aggregation (RTDB)**: regional siblings may share one stats root via
  a `stats_room_id` CONFIG override in z_stats (write path; default = room_id,
  fully backward compatible). ServerValue.increment merges atomically from both
  writers; ELO/streak absolutes are per-auth and one player can't play two
  regions at once, so no cross-writer races. One leaderboard. **ext-proxy must
  learn the same mapping** — the public stats page + panel stats tab read
  `<base>/<room.ID>/stats` today (statspage.go/statsread.go), so a room doc
  gains an optional `statsRoomId` field the readers resolve; without it the
  override would silently blank `/stats/<room>`.
- **History analysis (merged)**: ext-proxy fans out the §3 endpoints to every
  host in the group IN PARALLEL, then:
  - history pages: k-way merge with a STABLE total order (ts DESC, then
    region id, then game id — cross-region ts ties must not drop/dup rows at
    page boundaries); cursor = opaque per-region vector carrying each region's
    beforeId of the last EMITTED (not last fetched) row.
  - aggregates: PUSHDOWN — each region returns partial {sum,count,max,min,
    byKey{...}} shapes which merge losslessly; avg computed after merge.
    (No medians/percentiles in v1 — they don't merge; if ever needed, fetch
    rows.)
  - a region being down degrades to partial results + a "region offline"
    flag, never an error.
- Room-group membership lives on the ext-proxy `rooms` doc (`groupId` field —
  NET-NEW: Room struct field + store setter + /admin form; small but real
  scope), owner-managed in /admin. v1 ships the fan-out for `groupId`-less rooms too
  (group of one) so the code path is single.

## 5. Live queue (arena rooms → stats page, push end-to-end)

    arena_plugin: ONE emitQueue() choke point (called from PlayerQueue
        add/shift/remove + the seat-swap sites; the playing set snapshots from
        getPlayerList teams at emission) →
        console "@@QUEUE@@ {playing:[{name}...], queue:[{name}...], updatedAt}"
        (names only — no auth hashes on a public page)
    wlhl: captures the marker (like @@CHAT@@); keeps latest per room in memory;
        pushes {type:"event", event:"queue", room, state} over the host-link
    ext-proxy: holds latest queue state per room in memory (no persistence);
        GET /stats/<room> page includes the queue widget;
        GET /api/queue/<room>/stream = SSE (text/event-stream) replaying the
        current state then live updates. SSE is plain net/http + Flusher —
        the Fly edge caveat is browser-h2 WEBSOCKETS, SSE is unaffected — BUT
        Fly's proxy idle timeout (~60s) drops silent streams: send a
        ":keep-alive" comment every ~25s. Each viewer holds one connection
        against the fly.toml hard_limit=200 pool (soft 150): fine for tens of
        concurrent viewers; if a page ever draws hundreds, raise the limit or
        coalesce to polling.
    browser (stats page): renders playing + queue live; player clicks their
        name once ("notify me") → localStorage; Notification API (permission
        prompt inside that click = valid user gesture) fires when they reach
        queue head / enter play. Falls back to a title-bar flash + sound when
        notifications are denied OR unsupported (iOS Safari tabs have no
        Notification API outside installed PWAs).

The fallback endpoint `/api/rooms/{id}/queue` on wlhl is NEW (add to
_specs/http-api.md). Latency budget: console capture is immediate (CDP event), link push <100ms,
SSE fan-out <50ms — comfortably imperceptible. Fallback when a room has no
link (tunnel-only host or no host): ext-proxy polls the host's
`/api/rooms/{id}/queue` (wlhl serves its in-memory state) every 3s and feeds
the same SSE — degraded but functional; no RTDB involvement either way.

## Build order

1. **host-link phase 1** (spec `host-link.md`): /hostlink endpoint + wlhl link
   client + callHost transport switch, INCLUDING the event frame type on both
   sides (the live queue in step 3 rides events — folding them into phase 1
   removes the unstated phase-2 dependency). Prerequisite for push + fan-out.
2. **gamestore**: wlhl internal/gamestore + @@GAME@@ capture + /games API;
   z_stats emission; ext-proxy proxy endpoints + panel Match-history tab.
3. **live queue**: @@QUEUE@@ in arena_plugin + wlhl capture/serve + link event
   + ext-proxy SSE + stats-page widget + notifications.
4. **multi-region**: groupId + fan-out merge + stats_room_id override.
   (Ships last; nothing earlier depends on it.)

## Risks / notes

- modernc.org/sqlite is a large (pure-Go) dependency — accepted: it's the only
  CGO-free path to SQL on Windows, and wlhl is a server binary, not a library.
- History durability = host disk. Aggregates (RTDB) survive host loss; match
  logs are backup-able (§2) but not replicated by default. Stated trade-off.
- The queue widget shows player NAMES on a public page — same exposure as the
  in-game player list; no auth ids leave the room script.
- @@GAME@@ emission adds one ~1-2KB console line per game — noise-free for the
  ring buffer (2000 lines ≈ days of games).
