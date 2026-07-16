# wlhl — WebLiero Headless Launcher

Hosts WebLiero headless rooms in a shared Chrome (one tab per room), driven over
a local socket. Go rewrite of the original Node/puppeteer launcher.

```sh
wlhl server                                   # start the browser + control socket (once)
wlhl launch --id myroom --token <TOKEN>        # launch a room (vanilla client)
wlhl run myroom room/*.js                       # load room scripts into it
wlhl ls                                          # list rooms + join codes
wlhl follow myroom                               # stream a room's logs
wlhl stop myroom
```

## Hacked client

The launcher itself does **not** patch the client — it's hack-free and carries
no webliero internals. To host a room that needs the extended client (weapon
bans, `onPlayerHit`/`onPlayerSpawn`, position tracing, `__ReadPNG`), produce a
hacked `headless-min.js` with **[headless-modifier](https://github.com/s-dsds/headless-modifier)**
and serve it with `--script`:

```sh
headless-modifier -o hacked-min.js             # fetch + beautify + hack
wlhl launch --id myroom --token <TOKEN> --script hacked-min.js
```

Without `--script`, the room loads webliero's own vanilla client. A server-wide
default can be set via the `HEADLESS_SCRIPT` env var.

## Commands

`server` · `launch` · `run` · `ls` · `follow` · `stop` · `stats`

Chrome path: `--chrome-path` or the `CHROME_EXECPATH` env var.
