# Upstream and scope

This fork retains the Git history of:

- https://github.com/s-dsds/headless-launcher-go
- Starting commit: `06082aeb59b180f9bd2751742865db0ebdb2d0d6`

The original Node launcher was inspected for script compatibility:

- Local remote: `git@gitlab.com:sylvodsds/headless-launcher.git`
- Starting commit: `599593c40173b642ec7927465ab602b3656ec450`
- Its README references https://gitlab.com/webliero/webliero-headless-launcher
- Its package metadata credits Mario Carbajal and declares MIT.

The Go upstream checkout contains no standalone license file. This fork does not
invent a license grant for that upstream code. Existing authorship and history
remain intact. Runtime dependencies retain their respective licenses.

## Minimal fork

The current implementation replaces the upstream IPC, external integrations,
stores, custom-client interception, and native bindings with a small Go runtime,
configuration file, authenticated loopback HTTP API, embedded web panel, and CLI.
It retains the browser model: shared Chrome process, independent room tabs,
official `https://www.webliero.com/headless`, `WLInit`, `window.WLTOKEN`, console
logging, and ordered JavaScript evaluation.

This is one consolidated Go fork serving the minimal use case of both launchers;
it does not maintain a second Node runtime implementation.

## Migration

| Previous workflow | Minimal fork |
| --- | --- |
| `server`, then `launch --id ID --token TOKEN` | Add a profile with `tokenEnv`, set that environment variable, run `server`, then `start ID` |
| `launch script1.js script2.js` | Put the ordered files in the profile's `scripts` array |
| `run ID FILE...` | Same workflow; the client reads and uploads these files |
| `ls`, `stop ID` | Same commands, authenticated over loopback HTTP |
| `follow ID` | `logs ID --follow` |
| `--script`, `HEADLESS_SCRIPT`, patched client hooks | Removed; scripts must use the vanilla headless API |
| Host-link registration, ext-proxy, game/chat stores | Removed |

No production room scripts or credentials were copied from the workspace.
