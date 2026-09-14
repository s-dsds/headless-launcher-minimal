# Panel screenshots

These PNGs are real Chrome screenshots of the embedded administration panel with
local demo room data. No live WebLiero room or real credentials are used.

Regenerate from the repository root with Chrome installed:

```sh
WLHL_BROWSER_TEST=1 WLHL_DOC_SCREENSHOTS="$PWD/docs/screenshots" \
  go test -v ./internal/admin -run '^TestPanelBrowserLiveScripts$' -count=1
```

PowerShell:

```powershell
$env:WLHL_BROWSER_TEST = '1'
$env:WLHL_DOC_SCREENSHOTS = "$PWD/docs/screenshots"
go test -v ./internal/admin -run '^TestPanelBrowserLiveScripts$' -count=1
```

The test runs uploaded scripts in a real headless tab and verifies retained room
state, execution ordering, console output, and unchanged startup definitions.
