# Hosting: connecting a wlhl server to the admin panel

How to expose a `wlhl server` to ext-proxy so the panel gets server logs,
host-side chat (with search), match history, room create/stop and live queue
push. Architecture + API: `_specs/http-api.md`.

## 0. The host link (RECOMMENDED — no tunnel at all)

Since 2026-07-20 wlhl can dial ext-proxy directly and keep one authenticated
WebSocket open; ext-proxy sends its requests back down that pipe (spec:
`_specs/host-link.md`). No tunnel daemon, no URL to register or re-edit, no
DNS — survives IP changes and reboots by re-dialing (1s→60s backoff).

```
panel (browser) → ext-proxy (fly.dev) ⇐[host link, dialed by wlhl]⇒ wlhl
```

1. In ext-proxy `/admin` → Hosts → create the host. Leave the **URL empty**;
   set a strong token. (The token IS the host's identity.)
2. Run wlhl with the link (no `--http` listener needed at all):

```bash
wlhl server \
  --link wss://ext-proxy.fly.dev/hostlink \
  --link-token <the host token> \
  --link-name myserver \
  --data-dir /var/lib/wlhl \
  --profiles-dir /etc/wlhl/profiles
# or env: WLHL_LINK_URL / WLHL_LINK_TOKEN
```

3. `/admin` shows the host **online** with lastSeen/version the moment it
   connects. Done — everything below (tunnels) is the LEGACY/fallback path,
   still fully supported; a host may have both (the link is preferred, the
   tunnel URL is used when the link is down).

---

Legacy topology (tunnel):

```
panel (browser) → ext-proxy (fly.dev) → [tunnel] → wlhl --http 127.0.0.1:8091
```

ext-proxy runs in the cloud; wlhl runs on your server behind NAT — the cloud
can't connect inward. A tunnel daemon on your server dials OUT, holds the
connection open, and hands you a public https URL that forwards to the local
port. No port-forwarding, no public IP, survives IP changes / VPN switches.

## 1. Run wlhl with the API enabled

```bash
wlhl server \
  --chrome-path /path/to/chrome-wrap.sh \
  --http 127.0.0.1:8091 \
  --http-token "$(openssl rand -hex 24)" \      # or env WLHL_API_TOKEN
  --data-dir /var/lib/wlhl \                    # chat store
  --profiles-dir /path/to/wlhl-profiles \       # room profiles (creation)
  --max-rooms 4 \                               # webliero.com's per-IP limit
  --log-file /var/log/wlhl/wlhl.log             # rotated logs (LOGGING.md)
```

Keep the bind on `127.0.0.1` — the tunnel is the only way in, and the bearer
token still guards every request (constant-time compare; tokens never reach
browsers on the ext-proxy side either).

**Profiles**: `--profiles-dir` contains one subdirectory per profile; panel
room creation uses `default/`. Make it a webliero-simple-panel checkout plus a
`_conf.defaults.json` carrying host-local settings (the firebase web-SDK
block). Scripts run in **alphabetical order** — keep the `_`/`z_` filename
prefixes; the generated CONFIG replaces `_conf.js`.

```json
// wlhl-profiles/default/_conf.defaults.json
{
  "firebase": {
    "apiKey": "…",
    "databaseURL": "https://liero-1t.firebaseio.com",
    "projectId": "liero-1t",
    "storageBucket": "liero-1t.appspot.com"
  },
  "discord_invite": "https://discord.gg/…"
}
```

## 2. Put a tunnel in front

Any https reverse tunnel works — wlhl doesn't care what fronts it. Two known-
good options:

### ngrok

```bash
ngrok http 8091
```

Free tier includes **one static domain** — claim it in the ngrok dashboard
(e.g. `your-name.ngrok-free.app`) so the URL survives restarts:

```bash
ngrok http --url=your-name.ngrok-free.app 8091
```

Notes:
- The free-tier browser interstitial does NOT affect this: it only triggers
  for browser-looking User-Agents, and all tunnel traffic here is ext-proxy's
  server-side Go client. (Fallback: send `ngrok-skip-browser-warning`.)
- Free bandwidth caps are far above this API's traffic (small JSON, bounded
  log tails). Map uploads don't go through the tunnel at all (browser →
  ext-proxy → Firestore).
- Run it as a service next to wlhl (`ngrok service install` on Linux/Windows,
  or a systemd unit wrapping the command above).

### cloudflared

Quick tunnel — zero setup, but the URL is random and **changes on every
restart** (you then re-edit the host URL in /admin):

```bash
cloudflared tunnel --url http://127.0.0.1:8091
# prints e.g. https://tidy-owl-example.trycloudflare.com
```

Named tunnel — stable hostname; needs a (free) Cloudflare account and a
domain on it. One-time setup:

```bash
cloudflared tunnel login
cloudflared tunnel create wlhl
cloudflared tunnel route dns wlhl wlhl.yourdomain.com
```

```yaml
# ~/.cloudflared/config.yml
tunnel: wlhl
credentials-file: /home/you/.cloudflared/<tunnel-id>.json
ingress:
  - hostname: wlhl.yourdomain.com
    service: http://127.0.0.1:8091
  - service: http_status:404
```

```bash
cloudflared tunnel run wlhl          # or: cloudflared service install
```

### Anything else

`tailscale funnel`, `ssh -R` through a VPS, or a plain reverse proxy if the
box has a public IP — all fine. Requirements: https in front, forwards to
`127.0.0.1:8091`, stable-ish URL.

## 3. Register the host in ext-proxy

`/admin` → **Room hosts (wlhl)** → **+ Add host**:

| field | value |
|---|---|
| Host id | short name, e.g. `home1` |
| API URL | the tunnel URL, e.g. `https://your-name.ngrok-free.app` |
| API token | the `--http-token` value (write-only; never shown again) |
| Max rooms | ≤ 4 (webliero.com hard limit per IP) |

Press **test** — it must report the `/api/health` probe through the tunnel.

Then:
- **Existing rooms** (launched by hand): link them with the
  "Link an existing room to a host" control in the same section — that turns
  on their Server-log tab (super grants), host-side chat + search, and Stop.
- **New rooms**: grants with the create permission (grant modal → "May create
  & stop rooms on hosts") get a **Create a room** form on their `/padmin/`
  picker page. The webliero headless token is pasted per-creation, used once
  for registration, never stored.

## Arena rooms (ranked 1v1 ladder)

An arena room is just a profile: a webliero-simple-panel checkout whose
`_conf.defaults.json` enables the arena plugin and — **required** — sets
`gameMode` to `lms` (the ladder's rank/ELO logic is built on LMS lives;
`_init.js` defaults to `dm`, which leaves ELO/h2h/streaks inert):

```json
// wlhl-profiles/arena/_conf.defaults.json
{
  "firebase": { "apiKey": "…", "databaseURL": "https://liero-1t.firebaseio.com",
                "projectId": "liero-1t", "storageBucket": "liero-1t.appspot.com" },
  "baseRoomName": "arena",
  "gameMode": "lms",
  "plugins": {
    "announcer": { "enabled": true },
    "newjohn":   { "enabled": true },
    "arena":     { "enabled": true, "maxGames": 3 }
  }
}
```

Notes:
- `baseRoomName` moves the room's RTDB subtree off the default `simple/`
  (fork ≥ f5b47ae). If set, also set the same value in the room's `rtdbBase`
  field in ext-proxy `/admin` so the panel reads the same subtree.
- Plugins are strict opt-in: a profile without a `plugins` block runs a plain
  room even though the plugin files are present.
- `maxGames` (win-streak cap before the winner rotates out, 0 = unlimited) is
  tunable live from the panel's Plugins tab; the JSON is only the boot value.
- Give the profile dir a `README.txt` — its first line becomes the profile's
  description in the panel's room-creation picker (GET /api/profiles).
- Match history / live queue need `--data-dir` (SQLite per room) and reach the
  panel over the link automatically.
- Optional RTDB nodes for flavor/permissions (`motd` welcome-line list,
  `eastereggs` per-auth join announcements, `vips` camera spectators exempt
  from the AFK purge, `admins/<auth>.hidden`): see the fork's
  `_specs/social-nodes.md`.

**Updating room scripts: RESTART the room, never hot-reload.** Hot-reload
leaves `onPlayerActivity`/`onPlayerKicked` unchained, which silently corrupts
the AFK/leaver stats (the room logs a loud warning when it detects this).
Stop + recreate through the panel is the supported path.

## Upgrading wlhl on a live server

- Restarting `wlhl server` kills every room it hosts — schedule accordingly.
- The IPC socket moved to `/tmp/app.wlserver-go` (so the legacy TS launcher can
  coexist). Upgrade the server binary and the CLI **together**; anything
  pinned to the old path needs `WLHL_SOCKET`.
- wlhl now **exits(1) when chromium dies** instead of limping on. Run it under
  a supervisor, e.g. systemd with `Restart=always` (`RestartSec=5`).

## Troubleshooting

- `host unreachable` on test → tunnel down, or quick-tunnel URL rotated:
  update the host URL (edit button).
- `HTTP 401` → token mismatch between `--http-token` and the stored host
  token (re-enter it via edit; blank keeps the current one).
- Room create fails with `room cap reached` → count LIVE rooms on the host
  (`wlhl ls`); webliero.com allows 4 per IP, rooms launched outside the panel
  count too.
- Chat tab empty on a hosted room → the room script must emit `@@CHAT@@`
  lines (webliero-simple-panel ≥ commit 1405c2e) and the server needs
  `--data-dir`.
