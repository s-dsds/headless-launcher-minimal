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

func (p *panelPage) Load(string, []launcher.Script) error { return nil }
func (p *panelPage) Run([]launcher.Script) error          { return nil }
func (p *panelPage) Close()                               { p.once.Do(func() { close(p.done) }) }
func (p *panelPage) Done() <-chan struct{}                { return p.done }

func TestPanelBrowser(t *testing.T) {
	if os.Getenv("WLHL_BROWSER_TEST") != "1" {
		t.Skip("set WLHL_BROWSER_TEST=1")
	}
	path := filepath.Join(t.TempDir(), "room.js")
	if err := os.WriteFile(path, []byte("// fixture"), 0600); err != nil {
		t.Fatal(err)
	}
	m := launcher.NewManager(launcher.Config{Rooms: []launcher.Profile{{ID: "arena", Scripts: []string{path}}, {ID: "practice", Scripts: []string{path}}}}, func(log func(string)) (launcher.Page, error) {
		log("https://www.webliero.com/?v=20&c=fixture")
		log(`<img src=x onerror="window.INJECTED=true">`)
		return &panelPage{done: make(chan struct{})}, nil
	})
	defer m.Close()
	server := httptest.NewUnstartedServer(nil)
	server.Config.Handler = Handler(m, strings.Repeat("x", 32), server.Listener.Addr().String())
	server.Start()
	defer server.Close()
	parent, cancel := context.WithTimeout(context.Background(), 45*time.Second)
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
		chromedp.Navigate(server.URL),
		chromedp.WaitVisible("#login"),
		chromedp.SetValue("#token", strings.Repeat("x", 32)),
		chromedp.Click("#login button"),
		chromedp.WaitVisible("#dashboard"),
		chromedp.Click("article:first-child .actions button:first-child"),
		chromedp.Poll(`document.querySelector('article .badge').textContent === 'running'`, nil),
		chromedp.Poll(`document.querySelector('article pre').textContent.includes('<img')`, nil),
	); err != nil {
		t.Fatal(err)
	}
	var injected bool
	if err := chromedp.Run(ctx, chromedp.Evaluate(`!!window.INJECTED || !!document.querySelector('article img')`, &injected)); err != nil {
		t.Fatal(err)
	}
	if injected {
		t.Fatal("log HTML executed")
	}
	if screenshot := os.Getenv("WLHL_PANEL_SCREENSHOT"); screenshot != "" {
		var png []byte
		if err := chromedp.Run(ctx, chromedp.EmulateViewport(1200, 850), chromedp.Click("article:first-child summary"), chromedp.FullScreenshot(&png, 90)); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(screenshot, png, 0600); err != nil {
			t.Fatal(err)
		}
	}
	if err := chromedp.Run(ctx,
		chromedp.Click("article:first-child .actions button:nth-child(2)"),
		chromedp.Poll(`!document.querySelector('article .actions button:nth-child(3)').disabled`, nil),
		chromedp.Click("article:first-child .actions button:nth-child(3)"),
		chromedp.Poll(`document.querySelector('article .badge').textContent === 'stopped'`, nil),
		chromedp.Click("#logout"), chromedp.WaitVisible("#login"),
	); err != nil {
		t.Fatal(err)
	}
	if m.List()[0].State != "stopped" {
		t.Fatal("stop did not reach API")
	}
}
