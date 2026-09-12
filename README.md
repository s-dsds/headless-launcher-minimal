# WebLiero Headless Launcher — Minimal

A standalone Go launcher for ordinary WebLiero room scripts, with a private web
panel and CLI. One Chrome/Chromium process hosts one tab per configured room.
Runs on Windows and Linux; no Node.js runtime is required.

This is a local fork of `s-dsds/headless-launcher-go`, preserving its Git history
and the original Node launcher's `WLInit` / `WLTOKEN` script model. See
[UPSTREAM.md](UPSTREAM.md) for provenance and migration notes.

The minimal version loads WebLiero's official headless page unchanged. It has no
client patching, custom headless-client injection, ext-proxy integration, host
registration, tunnels, chat/game databases, or exposed native helper functions.

## Quick start

Install **Google Chrome or Chromium**. Linux needs the browser's normal system
libraries; a desktop session is unnecessary. Run the launcher as a regular user.
The browser is a runtime dependency and is not bundled in releases.

Download and extract the archive for your system after publishing a release, or
build from this repository with **Go 1.25 or newer**:

```sh
go build -o wlhl ./cmd/wlhl
```

On Windows, build with `go build -o wlhl.exe ./cmd/wlhl`.

Get a headless token from <https://www.webliero.com/headlesstoken>.

**Linux (bash):**

```sh
export WEBLIERO_TOKEN='your-webliero-token'
./wlhl server --config examples/config.json
```

**Windows (PowerShell):**

```powershell
$env:WEBLIERO_TOKEN = 'your-webliero-token'
.\wlhl.exe server --config examples/config.json
```

Open the exact panel URL printed by the server (default
`http://127.0.0.1:8787`). Paste its **admin token**, then click **Start**.
The sample room is **unlisted** (`public: false`). A join link appears when the
room script logs `onRoomLink`; expand **Recent logs** for errors or captcha/token
rejection. “Running” means the scripts loaded, not proof of successful room
registration. Stop, edit the script, and restart to apply changes from scratch.

`Ctrl+C` stops the server and all its browser tabs. To choose a browser:

```sh
./wlhl server --config examples/config.json --chrome-path /usr/bin/chromium
```

```powershell
.\wlhl.exe server --config examples/config.json --chrome-path 'C:\Program Files\Google\Chrome\Application\chrome.exe'
```

`CHROME_EXECPATH` is also supported. `--show` opens a visible browser for debugging.

## Configure multiple rooms and ordered scripts

Create a `config.json` beside your scripts:

```json
{
  "rooms": [
    {
      "id": "arena",
      "scripts": ["scripts/init.js", "scripts/admins.js", "scripts/rules.js"],
      "tokenEnv": "ARENA_TOKEN",
      "autostart": true
    },
    {
      "id": "practice",
      "scripts": ["scripts/practice.js"],
      "tokenEnv": "PRACTICE_TOKEN",
      "autostart": false
    }
  ]
}
```

Set those environment variables **before** starting the server. Tokens are read
from the server environment, not the CLI client's environment. Changing a token
requires restarting the server with the new environment. No `.env` file is loaded.

Script paths are relative to the configuration file, including on Windows.
Forward slashes work on both platforms. Paths are explicit: no glob expansion or
folder sorting. Scripts execute sequentially in the same tab, with returned
Promises awaited. Different rooms have separate JavaScript globals. The launcher
sets `window.WLTOKEN`; the first script calls `WLInit`. The sample in
[examples/room.js](examples/room.js) shows the standard API.

Profiles are read at server startup; edit the configuration and restart the
server to add/remove rooms. **Restart** re-reads the configured script files and
creates a fresh tab. The panel controls existing profiles; script editing uses
your text editor. Up to 64 profiles and 4 MiB of scripts per operation are allowed.

## CLI administration

In another terminal, set `WLHL_ADMIN_TOKEN` to the token printed by the server:

```sh
export WLHL_ADMIN_TOKEN='admin-token-from-server'
./wlhl ls
./wlhl start myroom
./wlhl logs myroom --follow
./wlhl run myroom ./extra-rules.js ./another-script.js
./wlhl restart myroom
./wlhl stop myroom
```

PowerShell uses `$env:WLHL_ADMIN_TOKEN = 'admin-token-from-server'` and
`.\wlhl.exe` for the same commands. `run` reads files on the CLI side and executes
them in the existing tab; it does not save them to the room profile. Do not re-run
a script that calls `WLInit` on an existing room; use **restart** instead.

For a different port, start with `--listen 127.0.0.1:8790` and set
`WLHL_URL=http://127.0.0.1:8790` in the CLI terminal. To keep a stable admin token
across server restarts, set `WLHL_ADMIN_TOKEN` before starting the server to a
random secret of at least 32 characters. Otherwise a fresh 256-bit token is
generated each time.

## Privacy and operation

- The panel/API **only bind to a numeric loopback address** (`127.0.0.1` or `::1`).
  Non-loopback addresses are rejected. Use the printed URL, not `localhost`.
- Every API read and write requires the admin bearer token. Host and Origin
  checks reject DNS rebinding and cross-origin browser requests. The static login
  page is public on loopback but contains no credentials or room data.
- The panel holds its token in memory; refreshing requires reconnecting. No CDN,
  analytics, external assets, or browser storage is used. The CLI ignores proxy
  environment variables and does not follow redirects.
- The admin token controls script execution; give it only to trusted local users.
  Room scripts are trusted code and can use the browser's ordinary network APIs.
  “Private administration” does not change whether the game room is public.
- Recent logs are in memory: 300 lines per room, capped at roughly 4 KiB per line.
  Known room tokens are redacted. Logs disappear when the server exits.
- Failed startup closes its tab. A failing `run` closes the affected tab to avoid
  continuing partially applied code. Room actions have bounded timeouts; competing
  mutations are rejected as busy. Browser/tab failure is reported, with manual
  restart. There is no automatic restart loop or OS service installer.

## Tests and GitHub Actions

```sh
go vet ./...
go test -race ./...
WLHL_BROWSER_TEST=1 go test -v ./internal/launcher -run TestBrowserSmoke
```

For PowerShell: `$env:WLHL_BROWSER_TEST = '1'` before the browser test command.
The browser test uses a local fixture to verify real tab creation, script order,
Promise rejection, console capture, isolation, and cleanup. Set
`WLHL_LIVE_TEST_TOKEN` only when intentionally running `TestLiveWebLiero` against
the live service; it opens one unlisted room and closes it. CI uses no live tokens.

[CI](.github/workflows/ci.yml) tests and builds on both native Windows and Linux
runners, including Chrome integration, and uploads binaries as artifacts.
[Release](.github/workflows/release.yml) repeats the checks on both platforms,
then packages Linux amd64/arm64 and Windows amd64 archives, examples, docs, and
SHA-256 checksums. Linux arm64 is cross-compiled; runtime integration is exercised
on amd64. Actions use the official
[checkout](https://github.com/actions/checkout),
[setup-go](https://github.com/actions/setup-go), and
[upload-artifact](https://github.com/actions/upload-artifact) actions.

This local repository has an `upstream` remote and a `minimal` branch. After
creating an empty GitHub repository, publish it with:

```sh
git remote add origin git@github.com:OWNER/REPOSITORY.git
git push -u origin minimal:main
git tag v0.1.0
git push origin v0.1.0
```

The tag triggers release publishing. Workflows only run once pushed to GitHub.
