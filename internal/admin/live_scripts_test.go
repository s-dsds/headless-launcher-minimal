package admin

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/chromedp/chromedp"
	"headless-launcher-minimal/internal/launcher"
)

// Exercises the complete panel -> API -> live Chrome tab -> console log path.
// Screenshots are optional and use only these demo rooms, never production tokens.
func TestPanelBrowserLiveScripts(t *testing.T) {
	if os.Getenv("WLHL_BROWSER_TEST") != "1" {
		t.Skip("set WLHL_BROWSER_TEST=1")
	}
	parent, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	site := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		w.Write([]byte(`<html><script>window.WLInit = options => ({options});</script></html>`))
	}))
	defer site.Close()
	browser, err := launcher.NewBrowser(parent, os.Getenv("CHROME_EXECPATH"), false)
	if err != nil {
		t.Fatal(err)
	}
	defer browser.Close()
	browser.URL = site.URL
	config := launcher.Config{Rooms: []launcher.Profile{
		{ID: "arena", Name: "Friday night arena", Settings: launcher.Settings{MaxPlayers: 12}, Scripts: []launcher.Script{{Name: "10-room-rules.js", Source: `window.round = 1; console.log('Room ready. Waiting for players.'); console.log('https://www.webliero.com/?c=demo-room');`}}},
		{ID: "practice", Name: "Practice room", Settings: launcher.Settings{MaxPlayers: 6}, Scripts: []launcher.Script{}},
	}}
	m := launcher.NewManager(config, browser.NewPage)
	defer m.Close()
	if err := m.Action("arena", "start", "demo-token-not-a-live-credential", nil); err != nil {
		t.Fatal(err)
	}
	server := httptest.NewUnstartedServer(nil)
	server.Config.Handler = Handler(m, strings.Repeat("x", 32), server.Listener.Addr().String())
	server.Start()
	defer server.Close()
	dir := t.TempDir()
	folder := filepath.Join(dir, "live-scripts")
	if err := os.Mkdir(folder, 0700); err != nil {
		t.Fatal(err)
	}
	for name, source := range map[string]string{
		"10-update-rules.js": `new Promise(resolve => setTimeout(() => { window.WLROOM.scoreLimit = 15; console.log('Score limit updated to 15.'); resolve(); }, 50))`,
		"20-announce.js":     `if (window.WLROOM.scoreLimit !== 15 || window.round !== 1) throw new Error('Script order or existing room state lost'); console.log('Announcement: new rules apply next round.');`,
	} {
		if err := os.WriteFile(filepath.Join(folder, name), []byte(source), 0600); err != nil {
			t.Fatal(err)
		}
	}
	extra := filepath.Join(dir, "30-status.js")
	if err := os.WriteFile(extra, []byte(`console.log('Live update complete. Room is still running.');`), 0600); err != nil {
		t.Fatal(err)
	}
	options := append([]chromedp.ExecAllocatorOption{}, chromedp.DefaultExecAllocatorOptions[:]...)
	if path := os.Getenv("CHROME_EXECPATH"); path != "" {
		options = append(options, chromedp.ExecPath(path))
	}
	allocator, stop := chromedp.NewExecAllocator(parent, options...)
	defer stop()
	ctx, closeUI := chromedp.NewContext(allocator)
	defer closeUI()
	run := func(actions ...chromedp.Action) {
		t.Helper()
		if err := chromedp.Run(ctx, actions...); err != nil {
			t.Fatal(err)
		}
	}
	capture := func(name string) {
		t.Helper()
		root := os.Getenv("WLHL_DOC_SCREENSHOTS")
		if root == "" {
			return
		}
		if err := os.MkdirAll(root, 0755); err != nil {
			t.Fatal(err)
		}
		var png []byte
		run(chromedp.EmulateViewport(1440, 1200), chromedp.FullScreenshot(&png, 90))
		if err := os.WriteFile(filepath.Join(root, name), png, 0644); err != nil {
			t.Fatal(err)
		}
	}
	run(chromedp.Navigate(server.URL), chromedp.WaitVisible("#login"), chromedp.SetValue("#token", strings.Repeat("x", 32)), chromedp.Click("#login button"), chromedp.WaitVisible("#dashboard"),
		chromedp.Poll(`document.querySelector('[data-id=practice] button[data-action=run]').disabled`, nil),
		chromedp.Click("[data-id=arena] button[data-action=run]"), chromedp.WaitVisible("#run-dialog"),
		chromedp.Click("#run-form button[type=submit]"), chromedp.Poll(`document.querySelector('#run-error').textContent.includes('Choose at least one')`, nil),
		chromedp.SetUploadFiles("#run-script-folder", []string{folder}), chromedp.Poll(`document.querySelectorAll('#run-script-list li').length === 2 && !document.querySelector('#run-script-files').disabled`, nil),
		chromedp.SetUploadFiles("#run-script-files", []string{extra}), chromedp.Poll(`document.querySelectorAll('#run-script-list li').length === 3 && !document.querySelector('#run-script-files').disabled`, nil),
		chromedp.Click("#run-script-list li:first-child button:nth-child(2)"), chromedp.Click("#run-script-list li:nth-child(2) button:first-child"),
	)
	capture("run-scripts.png")
	run(chromedp.Click("#run-form button[type=submit]"), chromedp.Poll(`!document.querySelector('#run-dialog').open`, nil),
		chromedp.Poll(`document.querySelector('[data-id=arena] pre').textContent.includes('Live update complete')`, nil),
		chromedp.Poll(`document.querySelector('#notice').textContent.includes('Executed 3 scripts')`, nil),
	)
	if m.List()[0].State != "running" {
		t.Fatal("live execution stopped the room")
	}
	profile, err := m.Profile("arena")
	if err != nil {
		t.Fatal(err)
	}
	if len(profile.Scripts) != 1 || profile.Scripts[0].Name != "10-room-rules.js" {
		t.Fatal("live scripts changed saved startup scripts")
	}
	capture("dashboard.png")
	run(chromedp.Click("[data-id=arena] button[data-action=stop]"), chromedp.Poll(`document.querySelector('[data-id=arena] .badge').textContent === 'stopped'`, nil),
		chromedp.Click("[data-id=arena] button[data-action=edit]"), chromedp.WaitVisible("#editor"),
	)
	capture("room-editor.png")
	run(chromedp.Click("#editor-close"))
	// Verify that the real server rejects attempts to run scripts in a stopped room.
	if err := m.Action("arena", "run", "", []launcher.Script{{Name: "no.js", Source: "1"}}); err == nil {
		t.Fatal("ran scripts in a stopped room")
	}
}
