package admin

import (
	"context"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/chromedp/chromedp"
	"headless-launcher-minimal/internal/launcher"
)

type panelPage struct {
	done chan struct{}
	once sync.Once
}

func (p *panelPage) Load(string, launcher.Profile) error { return nil }
func (p *panelPage) Run([]launcher.Script) error         { return nil }
func (p *panelPage) Close()                              { p.once.Do(func() { close(p.done) }) }
func (p *panelPage) Done() <-chan struct{}               { return p.done }

func TestPanelBrowser(t *testing.T) {
	if os.Getenv("WLHL_BROWSER_TEST") != "1" {
		t.Skip("set WLHL_BROWSER_TEST=1")
	}
	dir := t.TempDir()
	folder := filepath.Join(dir, "scripts")
	if err := os.MkdirAll(filepath.Join(folder, "nested"), 0700); err != nil {
		t.Fatal(err)
	}
	for name, source := range map[string]string{"20-rules.js": "// second", "10-init.js": "// first", "nested/30.js": "// third", "ignore.txt": "ignore"} {
		if err := os.WriteFile(filepath.Join(folder, name), []byte(source), 0600); err != nil {
			t.Fatal(err)
		}
	}
	extra := filepath.Join(dir, "40-extra.js")
	if err := os.WriteFile(extra, []byte("// extra"), 0600); err != nil {
		t.Fatal(err)
	}
	m := launcher.NewManager(launcher.Config{}, func(log func(string)) (launcher.Page, error) {
		log("https://www.webliero.com/?v=20&c=fixture")
		log(`<img src=x onerror="window.INJECTED=true">`)
		return &panelPage{done: make(chan struct{})}, nil
	})
	defer m.Close()
	m.SetStorage(filepath.Join(dir, "rooms.json"))
	server := httptest.NewUnstartedServer(nil)
	server.Config.Handler = Handler(m, strings.Repeat("x", 32), server.Listener.Addr().String())
	server.Start()
	defer server.Close()
	parent, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	options := append([]chromedp.ExecAllocatorOption{}, chromedp.DefaultExecAllocatorOptions[:]...)
	if path := os.Getenv("CHROME_EXECPATH"); path != "" {
		options = append(options, chromedp.ExecPath(path))
	}
	allocator, stop := chromedp.NewExecAllocator(parent, options...)
	defer stop()
	ctx, closeBrowser := chromedp.NewContext(allocator)
	defer closeBrowser()
	if err := chromedp.Run(ctx,
		chromedp.Navigate(server.URL), chromedp.WaitVisible("#login"), chromedp.SetValue("#token", strings.Repeat("x", 32)), chromedp.Click("#login button"), chromedp.WaitVisible("#dashboard"),
		chromedp.Click("#add-room"), chromedp.SetValue("#room-name", "Arena from panel"), chromedp.SetValue("#max-players", "8"),
		chromedp.SetUploadFiles("#script-folder", []string{folder}),
		chromedp.Poll(`document.querySelectorAll('#script-list li').length === 3 && !document.querySelector('#script-files').disabled`, nil),
		chromedp.SetUploadFiles("#script-files", []string{extra}),
		chromedp.Poll(`document.querySelectorAll('#script-list li').length === 4 && !document.querySelector('#script-files').disabled`, nil),
		chromedp.Click("#script-list li:first-child button:nth-child(2)"),
		chromedp.SetValue("#create-token", "initial-test-token"), chromedp.Click("#room-form button[value=start]"),
		chromedp.Poll(`document.querySelector('article .badge')?.textContent === 'running'`, nil),
	); err != nil {
		t.Fatal(err)
	}
	id := m.List()[0].ID
	profile, err := m.Profile(id)
	if err != nil {
		t.Fatal(err)
	}
	if profile.Name != "Arena from panel" || profile.Settings.MaxPlayers != 8 || len(profile.Scripts) != 4 || profile.Scripts[0].Name != "20-rules.js" || profile.Scripts[1].Name != "10-init.js" {
		t.Fatalf("picker/order/settings not saved: %+v", profile)
	}
	var leaked bool
	if err := chromedp.Run(ctx, chromedp.Evaluate(`!!window.INJECTED || !!document.querySelector('article img') || !!document.querySelector('#create-token').value`, &leaked)); err != nil {
		t.Fatal(err)
	}
	if leaked {
		t.Fatal("log HTML executed or token retained in form")
	}
	if screenshot := os.Getenv("WLHL_PANEL_SCREENSHOT"); screenshot != "" {
		var png []byte
		if err := chromedp.Run(ctx, chromedp.EmulateViewport(1200, 900), chromedp.Click("article details:last-child summary"), chromedp.FullScreenshot(&png, 90)); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(screenshot, png, 0600); err != nil {
			t.Fatal(err)
		}
	}
	if err := chromedp.Run(ctx,
		chromedp.Click("article button[data-action=restart]"), chromedp.WaitVisible("#start-dialog"),
		chromedp.Poll(`document.querySelector('#start-token').value === ''`, nil), chromedp.SetValue("#start-token", "new-test-token"), chromedp.Click("#start-form button[type=submit]"),
		chromedp.Poll(`!document.querySelector('article button[data-action=stop]').disabled`, nil),
		chromedp.Click("article button[data-action=stop]"), chromedp.Poll(`document.querySelector('article .badge').textContent === 'stopped'`, nil),
		chromedp.Click("article button[data-action=edit]"), chromedp.WaitVisible("#editor"),
		chromedp.Poll(`document.querySelectorAll('#script-list li').length === 4`, nil),
		chromedp.SetValue("#room-name", "Edited arena"), chromedp.Click("#room-form button[value=save]"),
		chromedp.Poll(`document.querySelector('article h2').textContent === 'Edited arena'`, nil),
		chromedp.Click("#logout"), chromedp.WaitVisible("#login"),
	); err != nil {
		t.Fatal(err)
	}
	saved, err := launcher.LoadConfig(filepath.Join(dir, "rooms.json"))
	if err != nil {
		t.Fatal(err)
	}
	if saved.Rooms[0].Name != "Edited arena" || len(saved.Rooms[0].Scripts) != 4 || m.List()[0].State != "stopped" {
		t.Fatal("edit/persistence/stop failed")
	}
	data, err := os.ReadFile(filepath.Join(dir, "rooms.json"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), "test-token") {
		t.Fatal("launch token persisted")
	}
}
