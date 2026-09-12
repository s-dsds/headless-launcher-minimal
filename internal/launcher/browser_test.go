package launcher

import (
	"context"
	"github.com/chromedp/cdproto/cdp"
	"github.com/chromedp/cdproto/target"
	"github.com/chromedp/chromedp"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestBrowserSmoke(t *testing.T) {
	if os.Getenv("WLHL_BROWSER_TEST") != "1" {
		t.Skip("set WLHL_BROWSER_TEST=1 to test with Chrome")
	}
	site := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		w.Write([]byte(`<html><script>window.WLInit = function() {};</script></html>`))
	}))
	defer site.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	b, err := NewBrowser(ctx, os.Getenv("CHROME_EXECPATH"), false)
	if err != nil {
		t.Fatal(err)
	}
	defer b.Close()
	b.URL = site.URL
	var mu sync.Mutex
	var logs []string
	p, err := b.NewPage(func(s string) { mu.Lock(); logs = append(logs, s); mu.Unlock() })
	if err != nil {
		t.Fatal(err)
	}
	defer p.Close()
	if err := p.Load("fixture-token", []Script{
		{Name: "first.js", Source: `new Promise(resolve => setTimeout(() => {window.order = ['first']; resolve();}, 50))`},
		{Name: "second.js", Source: `if (window.order.join() !== 'first' || window.WLTOKEN !== 'fixture-token') throw new Error('ordering/token failed'); window.order.push('second'); console.log('ORDER_OK');`},
	}); err != nil {
		t.Fatal(err)
	}
	if err := p.Run([]Script{{Name: "verify.js", Source: `if (window.order.join() !== 'first,second') throw new Error('state lost');`}}); err != nil {
		t.Fatal(err)
	}
	other, err := b.NewPage(func(string) {})
	if err != nil {
		t.Fatal(err)
	}
	defer other.Close()
	if err := other.Load("", []Script{{Source: `if (window.order !== undefined) throw new Error('tabs share globals');`}}); err != nil {
		t.Fatal(err)
	}
	if err := p.Run([]Script{{Name: "failure.js", Source: `Promise.reject(new Error('expected failure'))`}}); err == nil {
		t.Fatal("Promise rejection not reported")
	}
	mu.Lock()
	found := strings.Contains(strings.Join(logs, "\n"), "ORDER_OK")
	mu.Unlock()
	if !found {
		t.Fatal("console not captured")
	}
	pageContext := chromedp.FromContext(p.(*browserPage).ctx)
	if err := target.CloseTarget(pageContext.Target.TargetID).Do(cdp.WithExecutor(ctx, chromedp.FromContext(b.ctx).Browser)); err != nil {
		t.Fatal(err)
	}
	select {
	case <-p.Done():
	case <-time.After(5 * time.Second):
		t.Fatal("tab did not close")
	}
}

// Opt-in only: never requires real tokens for CI or ordinary tests.
func TestLiveWebLiero(t *testing.T) {
	token := os.Getenv("WLHL_LIVE_TEST_TOKEN")
	if token == "" {
		t.Skip("set WLHL_LIVE_TEST_TOKEN for an unlisted live room test")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	b, err := NewBrowser(ctx, os.Getenv("CHROME_EXECPATH"), false)
	if err != nil {
		t.Fatal(err)
	}
	defer b.Close()
	link := make(chan string, 1)
	captcha := make(chan struct{}, 1)
	p, err := b.NewPage(func(s string) {
		if roomLink.MatchString(s) {
			select {
			case link <- "received":
			default:
			}
		}
		if strings.Contains(s, "TEST_CAPTCHA") {
			select {
			case captcha <- struct{}{}:
			default:
			}
		}
	})
	if err != nil {
		t.Fatal(err)
	}
	defer p.Close()
	if err := p.Load(token, []Script{{Name: "live-smoke.js", Source: `var room = WLInit({token:window.WLTOKEN,roomName:'Launcher verification',maxPlayers:2,public:false}); room.onRoomLink = link => console.log(link); room.onCaptcha = () => console.log('TEST_CAPTCHA');`}}); err != nil {
		t.Fatal("headless load failed:", strings.ReplaceAll(err.Error(), token, "[REDACTED]"))
	}
	select {
	case <-link:
		t.Log("Unlisted WebLiero room registered; closing it now")
	case <-captcha:
		t.Fatal("WebLiero rejected the supplied token")
	case <-ctx.Done():
		t.Fatal("no room link received before timeout")
	}
}
