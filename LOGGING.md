# Running & logging `wlhl server`

`wlhl server` is a long-lived daemon. It writes all room output — every room
`console.log`, plus **uncaught JS exceptions** from the room scripts — to its
own **stdout/stderr**. It does **not** write or rotate a log file itself, so how
you run it decides where the logs go and whether they survive.

> Do **not** leave it attached to a throwaway path (e.g. a temp/scratch dir).
> If that path is cleaned up you lose all history and `wlhl follow` only shows
> *new* lines — which is exactly how you end up "unable to see past logs".

`wlhl follow <room>` streams live logs; it is **not** a history tool. Use the
options below to keep durable, rotated history.

---

## Recommended (cross-platform): built-in rotated log file

The portable answer that works identically on Linux, Windows and macOS is
`wlhl`'s own rotating file — no OS supervisor or rotation tooling required:

```bash
wlhl server --chrome-path /tmp/chrome-wrap.sh \
  --log-file /var/log/wlhl/wlhl.log \
  --log-max-size 50 \        # rotate at 50 MB (default)
  --log-max-backups 5        # keep wlhl.log.1 … .5 (default)
```

Output is **teed**: it still goes to stderr (so systemd/NSSM/interactive runs
see it) *and* to the rotated file. Rotation is rename-based
(`wlhl.log` → `wlhl.log.1` → `.2` …) and Windows-safe.

The per-platform options below are still useful for auto-restart/supervision,
but with `--log-file` none of them is needed just for durable logs.

---

## Linux — systemd (best) 

Auto-restart, `journalctl` history with `--since`, built-in rotation.

```ini
# ~/.config/systemd/user/wlhl.service
[Unit]
Description=WebLiero headless launcher
After=network-online.target

[Service]
WorkingDirectory=/home/qmdev/liero/dock/headless-launcher-go
ExecStart=/home/qmdev/liero/dock/headless-launcher-go/wlhl server --chrome-path /tmp/chrome-wrap.sh
Restart=on-failure
RestartSec=5

[Install]
WantedBy=default.target
```
```bash
loginctl enable-linger "$USER"        # run without an active login session
systemctl --user enable --now wlhl
journalctl --user -u wlhl -f          # live
journalctl --user -u wlhl --since -2h # history (the thing follow can't do)
```
Tune retention in `journald.conf` (`SystemMaxUse=`, `MaxRetentionSec=`).

## Windows — service via NSSM (fallback)

Windows has no systemd/screen. [NSSM](https://nssm.cc) runs any exe as a
service **and** captures stdout/stderr to a file with rotation:

```bat
nssm install wlhl "C:\path\wlhl.exe" server --chrome-path "C:\path\chrome-wrap.bat"
nssm set wlhl AppDirectory "C:\path"
nssm set wlhl AppStdout "C:\logs\wlhl.log"
nssm set wlhl AppStderr "C:\logs\wlhl.log"
nssm set wlhl AppRotateFiles 1
nssm set wlhl AppRotateBytes 52428800   REM rotate at 50 MB
nssm start wlhl
```
(No NSSM? `sc.exe create` works for the service, but you'd redirect output to a
file yourself and rotate it separately — which is why the built-in `--log-file`
above is the cleaner long-term answer.)

## macOS — launchd

A `~/Library/LaunchAgents/*.plist` with `StandardOutPath`/`StandardErrorPath`
pointing at a log file (rotate with `newsyslog`).

## Any platform — screen/tmux (simple, portable)

Closest to the old TypeScript setup. Portable but **no rotation** on its own:

```bash
screen -dmS wlhl bash -c 'cd .../headless-launcher-go && \
  exec ./wlhl server --chrome-path /tmp/chrome-wrap.sh >> /var/log/wlhl/wlhl.log 2>&1'
```
Pair it with `logrotate` (Linux/mac) so the file doesn't grow unbounded:
```
# /etc/logrotate.d/wlhl
/var/log/wlhl/wlhl.log { daily rotate 14 compress missingok copytruncate }
```
Use `copytruncate` (the process holds the fd open and never reopens the file).

---

## Notes

- **Browser decay:** one Chrome running for days degrades (fps/ping collapse,
  GPU CPU creep). Recycle the server periodically (a `systemctl --user restart`
  timer, or a scheduled restart) rather than running it for weeks.
- **`--chrome-path`** must point at your Chrome wrapper on each OS.
