# Host Link — replacing the tunnel with an outbound persistent connection

Status: design proposal (2026-07-19), not yet built. Answers: "having a tunnel is
cumbersome — wouldn't it be easier to switch to another protocol like gRPC and
keep a bidirectional connection between ext-proxy & wlhl, with some kind of
auto-registering system?"

## Verdict up front

**Yes to the reverse connection, no to gRPC — use a WebSocket.** The tunnel
exists only because ext-proxy needs to reach a machine with no inbound port. If
wlhl instead dials OUT to ext-proxy and keeps that connection open, ext-proxy
can send its requests back down the same pipe and the tunnel (ngrok/cloudflared,
the URL-rotation dance, the "host unreachable = tunnel died" failure mode, the
manual /admin URL edits) disappears entirely. This is the standard agent pattern
(GitHub Actions runners, Teleport, fly's own agent).

Why WebSocket and not gRPC, grounded in the actual deployment:

1. **Fly config.** ext-proxy's fly.toml has one service, `handlers ["http"]` /
   `["tls","http"]` — Fly terminates TLS and speaks HTTP/1.1 to the backend;
   the backend is plain `net/http` with no h2c. gRPC needs end-to-end HTTP/2:
   either a dedicated raw-TLS port or Fly h2 config plus an h2c-capable server.
   All churn, zero benefit at this scale.
2. **Proven path.** ext-proxy already terminates WebSockets in prod through
   Fly's edge (`websocket.Accept` on the game proxy, nhooyr v1.8.7). The known
   Fly caveat (chat.go:199-211: the edge drops Connection/Upgrade for **browser
   HTTP/2 Extended CONNECT** clients) does not apply here — a Go client dials
   WS over HTTP/1.1 Upgrade, the path that survives untouched. If we ever see
   the header issue anyway, the chat.go workaround (reconstruct headers from
   Sec-WebSocket-Key) is already written in this codebase.
3. **Toolchain weight.** gRPC means protobuf codegen in two repos for a surface
   of ~8 RPCs that are already JSON in/JSON out. wlhl currently has FOUR direct
   deps (chromedp, cdproto, godotenv, cobra); keeping it lean matters on the
   "runs on random Windows machines" end. One WS client lib (coder/websocket,
   the renamed successor of the nhooyr lib ext-proxy already uses) is the whole
   cost.
4. **No streaming need.** The RPC inventory (below) is request/response; the
   biggest payload is a 512 KB log tail, the slowest call is room-create
   (~10-15 s). Bidirectional streaming buys nothing; correlation-id
   multiplexing over one socket covers it. (If we later want live log
   *streaming* to the panel, WS does that too.)

gRPC would be the right call if this grew into many strongly-typed services or
polyglot clients. It's two Go programs and eight JSON endpoints.

## Current state (from the code, 2026-07-19)

- All traffic is ext-proxy → wlhl through `callHost` (hostapi.go:31-60), bearer
  token, 15 s timeout, 512 KB cap. Endpoints: health, rooms list/create/stop,
  logs tail, chat page/dates/search (spec: _specs/http-api.md).
- `hosts/{id}` Firestore doc: `{Name, URL, Token, MaxRooms<=4, Enabled}`. No
  lastSeen, no heartbeat, no self-registration — the owner hand-creates the doc
  and pastes the tunnel URL; quick-tunnel rotation = manual URL edit (HOSTING.md
  troubleshooting: "quick-tunnel URL rotated: update the host URL").
- Panel chat tab polls a host RPC every 5 s while visible; logs tab is
  super-only on-demand.

## Design

### Connection

wlhl gains a link client (`internal/hostlink`):

    wlhl server --http 127.0.0.1:8091 \
                --link wss://ext-proxy.fly.dev/hostlink --link-token <host token>

- Dials `wss://…/hostlink` with `Authorization: Bearer <token>` (same token the
  host doc already holds). Reconnect forever with jittered backoff (1s → 60s
  cap). WS ping/pong keepalive every 30 s; a missed pong closes and re-dials.
- On connect, sends `hello`:

      {type:"hello", proto:1, name, version, maxRooms, rooms:[RoomInfo], startedAt}

- ext-proxy authenticates the token against the hosts collection (iterate the
  tiny hosts list with a constant-time compare — tokens are stored plaintext
  today, so no hash index; hashing-at-rest would be a separate migration),
  marks the host **online**, stores the hello info + lastSeen in
  memory, and mirrors `{online, lastSeen, version}` onto the host doc
  (debounced, e.g. 60 s) so /admin shows it.

### RPC over the link

One JSON frame protocol, correlation-id multiplexed (the proxy keeps a
pending map[id]chan; every in-flight call selects on the link's close signal so
a dying socket fails calls fast instead of burning the 15s timeout):

    proxy → host: {type:"req",  id:"r42", method:"GET", path:"/api/rooms/qmpanel/logs?tail=200"}
                  {type:"req",  id:"r43", method:"POST", path:"/api/rooms", body:{…}}
    host → proxy: {type:"resp", id:"r42", status:200, body:<json|string>}
    host → proxy: {type:"event", event:"rooms", rooms:[…]}        // push, unsolicited

The **path-based framing is deliberate**: wlhl dispatches an incoming `req` into
its OWN existing HTTP mux — the UN-WRAPPED mux, not the requireBearer handler:
the link is the pre-authenticated channel, and this also lets a link-only host
run with no --http listener/token at all. Dispatch is an in-process round-trip
(`httptest.NewRecorder` + `mux.ServeHTTP`). Zero endpoint logic is duplicated —
the HTTP API stays the single source of truth, the link is just a second
transport for it. Same bearer check can even be skipped (the link itself is the
authenticated channel).

ext-proxy side: `callHost` grows a transport switch —

    if link := linkFor(host.ID); link != nil { return link.roundTrip(ctx, method, path, body) }
    if host.URL != "" { …existing tunnel HTTP call… }
    return error("host offline")

Same 15 s timeout and 512 KB cap enforced on the link path. **Both transports
coexist** — a host with only a URL keeps working, a host with a link needs no
URL. Migration is per-host and reversible; HOSTING.md's tunnel section becomes
the fallback appendix.

### Auto-registration

Token-based attach (not open self-registration — minting stays owner-controlled,
so a leaked binary can't enroll itself):

1. Owner creates the host in /admin as today — but the URL field becomes
   optional; creating mints the token.
2. Operator pastes the token into wlhl's config (`--link-token` / .env).
3. wlhl connects; hello fills in name/version/maxRooms/live rooms; /admin shows
   the host online with lastSeen. No URL, no tunnel, no DNS, survives IP
   changes and reboots (wlhl just re-dials).

The host-side "registration" the user asked for is exactly the hello frame: the
doc's operational fields (name, version, rooms, capacity) come from the host
itself, live, instead of being typed in.

### Bonus wins (free once the link exists)

- **Real health**: online/offline + lastSeen in /admin and in the create-room
  picker, replacing the manual "test" button as the primary signal (button
  stays, now also usable for link hosts).
- **Push events**: wlhl can push `rooms` on any room start/stop/code-arrival —
  the create-room flow stops polling 20×500 ms for the join code; the panel's
  5 s chat poll could later become push too. (Phase 2 — request/response parity
  first.)
- **One less prod dependency**: no ngrok account/limits, no cloudflared config,
  no "which tunnel URL is live" state.

### Sizing / limits

Fly's service has 200 hard connection limit; one long-lived connection per host
(≤ a handful) is noise. Frames are JSON — the 512 KB log tail is the worst case
and already capped. Big payloads stay fine over WS (no proxy buffering issues:
the connection is backend-terminated).

### Phasing

1. **Phase 1**: `/hostlink` endpoint + `internal/hostlink` client + callHost
   transport switch + online/lastSeen in /admin. Tunnel path untouched.
   E2E-testable locally (run ext-proxy dev + wlhl on one machine, no tunnel).
2. **Phase 2**: push events (rooms/codes), create-room without the poll loop,
   panel "host online" badges.
3. **Phase 3 (optional)**: retire tunnel docs to an appendix; consider log
   *streaming* over the link.

## Open questions for the owner

- Token in wlhl config: `.env` alongside the existing secrets, or a flag? (.env
  suggested — same place the room tokens live.)
- Should a link-host with a stale connection fall back to its URL if one is
  configured, or hard-fail? (Suggested: fall back if URL set, with a log line.)
- /hostlink on the same fly app vs a subdomain: same app, same port — no infra
  change needed.
