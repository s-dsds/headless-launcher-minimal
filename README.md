# WebLiero Headless Launcher — Minimal

Host WebLiero rooms in headless Chrome, with a private local web panel. Add rooms,
choose scripts, and start them without editing configuration files. Windows and
Linux are supported; no Node.js runtime is required.

## Start the launcher

Install Google Chrome or Chromium, then run the executable:

```sh
# Linux
./wlhl server
```

```powershell
# Windows (PowerShell)
.\wlhl.exe server
```

Open the exact URL printed by the server (default `http://127.0.0.1:8787`) and
paste the **admin token** printed below it. This token protects administration;
it is separate from WebLiero's room tokens.

From source, use Go 1.25 or newer:

```sh
go build -o wlhl ./cmd/wlhl
```

On Windows, use `go build -o wlhl.exe ./cmd/wlhl`.

## Add a room in the panel

1. Click **Add room** and enter its name, maximum players, visibility, and optional
   room password. Rooms are unlisted by default.
2. Use **Choose files** or **Choose folder**. A folder imports its `.js` files,
   including subfolders, sorted by relative path. Hidden directories and
   `node_modules` are skipped. Non-JavaScript files are ignored.
3. Use the **↑ / ↓** controls to set execution order, or remove unwanted files.
   Names such as `10-init.js`, `20-rules.js` help establish the initial order.
4. Paste a [WebLiero headless token](https://www.webliero.com/headlesstoken) and
   click **Save & start**. Use **Save for later** to save without a token.

The panel stores a copy of the selected script contents. Re-selecting a file
with the same relative name replaces its saved copy in the existing order.
Local file changes are not watched: stop the room, choose **Edit**, re-select the
changed files/folder, then save. Clear the list first if you want to replace all
scripts, including removing files that no longer exist in the source folder.

Use **Stop**, **Edit**, **Start**, **Restart**, and **Delete** on each room card.
Editing or deleting a running room requires stopping it first. A failed start
keeps the saved definition so you can correct it or try another token.

## Room creation and existing scripts

By default, the launcher calls the standard `WLInit` API before loading your
scripts. They can access the room through `window.WLROOM`:

```js
const room = window.WLROOM;
room.onPlayerJoin = player => console.log('Joined:', player.name);
```

For an existing script collection that already calls `WLInit`, enable **My
scripts call WLInit**. The picker checks this automatically when it recognizes
such a call; review the checkbox before saving. In this mode the launcher waits
for your script to create the room. The selected name, player limit, visibility,
password, and fresh token override those options in your script's `WLInit` call;
other script-supplied options are retained. `window.WLTOKEN` is also supplied for
compatibility. Only one room can be created per tab.

In both modes, scripts run sequentially in the saved order and returned Promises
are awaited. The launcher sets default room-link and token-rejection logging.
If your scripts replace `room.onRoomLink` or `room.onCaptcha`, keep their logging
if you want those events in the panel. The included `examples/room.js` is an
existing-style initialization script; enable **My scripts call WLInit** for it.

“Running” means scripts loaded, not proof that the room registered successfully.
A join link appears after it is logged; check **Recent logs** for token rejection
or script errors. Custom-client hooks and patched scripts are unsupported.

## Tokens and saved rooms

WebLiero room tokens are short-lived and supplied **per launch**. **Start** and
**Restart** ask for a token every time. The launcher never puts these tokens in
its saved room definitions, pre-fills them, or automatically reuses them. When
testing, you may explicitly paste the same token again if WebLiero accepts it.
A missing token on Restart leaves the existing room running. The same token
cannot be used by two active rooms at once.

Names, settings, and script contents are saved automatically to `rooms.json` in
the launcher's working directory. No hand-written JSON is needed. For another
location, use `wlhl server --data /path/to/rooms.json`. The parent directory is
created as needed. Back up this file to back up your rooms. Room passwords and
script contents are included; WebLiero tokens and admin tokens are not.

On server restart, saved rooms reappear **stopped**. There is no `autostart` or
room-token environment variable. Old manual configurations using `tokenEnv`,
`autostart`, or script path arrays are not the new storage format: add those
rooms once through the panel instead. Do not share a storage file between
multiple launcher processes.

## CLI and browser options

The web panel is the primary administration interface. The CLI also supports:

```sh
export WLHL_ADMIN_TOKEN='admin-token-printed-by-server'
./wlhl ls
./wlhl start ROOM_ID      # prompts for a WebLiero token on stdin
./wlhl logs ROOM_ID --follow
./wlhl run ROOM_ID ./extra.js ./scripts-directory
./wlhl restart ROOM_ID    # prompts for a token again
./wlhl stop ROOM_ID
```

PowerShell uses `$env:WLHL_ADMIN_TOKEN = 'admin-token-printed-by-server'` and
`.\wlhl.exe`. `ls` includes each room's generated ID. CLI token input is read from
stdin (terminal input is echoed), not an environment variable or command argument.
`run` imports files/directories and executes them in the existing room; it does
not change the saved definition. Do not run initialization scripts again in an
existing room; use Restart instead.

`--chrome-path PATH` or `CHROME_EXECPATH` selects Chrome/Chromium. `--show` opens
a visible browser for troubleshooting. Chrome needs its ordinary system
libraries on Linux; no desktop session is needed. Run as a regular OS user.
`Ctrl+C` closes the server and its browser tabs.

Use `--listen 127.0.0.1:8790` to change the admin port and set
`WLHL_URL=http://127.0.0.1:8790` for CLI commands. You may set `WLHL_ADMIN_TOKEN`
to a random secret of at least 32 characters before starting the server to keep a
stable **admin** token. Otherwise a fresh token is generated on each server start.

## Privacy and limits

- Administration binds only to a numeric loopback address (`127.0.0.1` or `::1`).
  Use the printed URL, not `localhost`. Every API read/write requires the admin
  token, with Host and Origin checks against rebinding and cross-origin requests.
- The panel has no external assets, analytics, or browser credential storage.
  Refreshing the page requires reconnecting. Room tokens are sent only for the
  selected start/restart and retained in memory only while that launch is active.
- The official WebLiero headless page is used, with its ordinary `WLInit` API.
  There is no client patching, ext-proxy, host registration, native script bridge,
  chat/game database, or tunnel integration. Scripts are trusted browser code.
- Up to 64 saved rooms, 100 scripts and 4 MiB of source per room, and 64 MiB of
  encoded storage. Logs retain 300 lines per room in memory, with bounded line
  lengths and known launch tokens redacted.
- Failed startup closes its tab. A failing `run` also closes the tab because code
  may have partially applied. Unexpected tab/browser exits are reported. There is
  no automatic restart loop; start again with a fresh token.

## Tests and releases

```sh
go vet ./...
go test -race ./...
WLHL_BROWSER_TEST=1 go test -v ./internal/... -run 'TestBrowserSmoke|TestPanelBrowser'
```

On PowerShell set `$env:WLHL_BROWSER_TEST = '1'` before the last command.
Tests cover persistence and failed saves, fresh-token lifecycle, directory import,
script order and room settings, real Chrome file/folder pickers, panel editing,
authentication, and tab cleanup. Browser fixtures use no live room tokens.
`TestLiveWebLiero` is opt-in via `WLHL_LIVE_TEST_TOKEN` and creates one unlisted
room before closing it.

[CI](.github/workflows/ci.yml) runs tests and browser integration on native Windows
and Linux runners and uploads binaries. [Release](.github/workflows/release.yml)
repeats verification, then packages Linux amd64/arm64 and Windows amd64 archives
with examples, docs, and SHA-256 checksums. Linux arm64 is cross-compiled.
Workflows run after pushing; native Windows checks cannot run on a Linux host.

This consolidated Go fork retains the original Go launcher's history; see
[UPSTREAM.md](UPSTREAM.md). To publish the local `minimal` branch to an empty repo:

```sh
git remote add origin git@github.com:OWNER/REPOSITORY.git
git push -u origin minimal:main
git tag v0.1.0
git push origin v0.1.0
```
