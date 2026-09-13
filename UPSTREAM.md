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
automatically saved room definitions, authenticated loopback HTTP API, embedded web panel, and CLI.
It retains the browser model: shared Chrome process, independent room tabs,
official `https://www.webliero.com/headless`, `WLInit`, `window.WLTOKEN`, console
logging, and ordered JavaScript evaluation.

This is one consolidated Go fork serving the minimal use case of both launchers;
it does not maintain a second Node runtime implementation.

## Migration

Create rooms in the panel with a name, settings, and files or an entire script
folder. Enable “My scripts call WLInit” for existing initializer scripts.
Supply a WebLiero token at each start/restart; no room-token environment variables
or autostart configuration are used. Definitions and uploaded script contents are
saved automatically. See README.md for the current workflow.

`ls`, `stop`, `run`, and `logs --follow` remain available in the CLI.
Client interception, host-link registration, ext-proxy, and game/chat stores
remain removed. No production room scripts or credentials were copied from the
workspace.
