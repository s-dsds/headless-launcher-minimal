package launcher

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/chromedp/cdproto/runtime"
	"github.com/chromedp/cdproto/target"
	"github.com/chromedp/chromedp"
)

const HeadlessURL = "https://www.webliero.com/headless"

type Page interface {
	Load(string, Profile) error
	Run([]Script) error
	Close()
	Done() <-chan struct{}
}

type Browser struct {
	ctx    context.Context
	cancel context.CancelFunc
	URL    string
}

func NewBrowser(parent context.Context, path string, show bool) (*Browser, error) {
	opts := append([]chromedp.ExecAllocatorOption{}, chromedp.DefaultExecAllocatorOptions[:]...)
	opts = append(opts, chromedp.Flag("headless", !show), chromedp.Flag("autoplay-policy", "no-user-gesture-required"))
	if path != "" {
		opts = append(opts, chromedp.ExecPath(path))
	}
	alloc, allocCancel := chromedp.NewExecAllocator(parent, opts...)
	ctx, cancel := chromedp.NewContext(alloc)
	// Keep the browser context alive after startup; a timeout only cancels on failure.
	timer := time.AfterFunc(45*time.Second, cancel)
	err := chromedp.Run(ctx)
	timer.Stop()
	if err != nil {
		cancel()
		allocCancel()
		return nil, fmt.Errorf("start Chrome (install Chrome/Chromium or set --chrome-path): %w", err)
	}
	return &Browser{ctx: ctx, cancel: func() { cancel(); allocCancel() }, URL: HeadlessURL}, nil
}

func (b *Browser) Close()                { b.cancel() }
func (b *Browser) Done() <-chan struct{} { return b.ctx.Done() }

func (b *Browser) NewPage(log func(string)) (Page, error) {
	ctx, cancel := chromedp.NewContext(b.ctx)
	p := &browserPage{ctx: ctx, cancel: cancel, url: b.URL, log: log}
	chromedp.ListenTarget(ctx, func(event any) {
		switch e := event.(type) {
		case *runtime.EventConsoleAPICalled:
			parts := make([]string, 0, len(e.Args))
			for _, arg := range e.Args {
				var str string
				if arg.Type == runtime.TypeString && json.Unmarshal(arg.Value, &str) == nil {
					parts = append(parts, str)
				} else if len(arg.Value) > 0 {
					parts = append(parts, string(arg.Value))
				} else {
					parts = append(parts, arg.Description)
				}
			}
			log(strings.Join(parts, " "))
		case *runtime.EventExceptionThrown:
			if e.ExceptionDetails != nil {
				message := e.ExceptionDetails.Text
				if e.ExceptionDetails.Exception != nil {
					message += ": " + e.ExceptionDetails.Exception.Description
				}
				log("JavaScript error: " + message)
			}
		}
	})
	timer := time.AfterFunc(45*time.Second, cancel)
	err := chromedp.Run(ctx)
	timer.Stop()
	if err != nil {
		cancel()
		return nil, err
	}
	// A closed/crashed target does not automatically cancel a chromedp tab context.
	tab := chromedp.FromContext(ctx).Target
	targetID, sessionID := tab.TargetID, tab.SessionID
	chromedp.ListenBrowser(ctx, func(event any) {
		switch e := event.(type) {
		case *target.EventTargetDestroyed:
			if e.TargetID == targetID {
				go cancel()
			}
		case *target.EventTargetCrashed:
			if e.TargetID == targetID {
				go cancel()
			}
		case *target.EventDetachedFromTarget:
			if e.SessionID == sessionID {
				go cancel()
			}
		}
	})
	return p, nil
}

type browserPage struct {
	ctx    context.Context
	cancel context.CancelFunc
	url    string
	log    func(string)
}

func (p *browserPage) Done() <-chan struct{} { return p.ctx.Done() }
func (p *browserPage) Close()                { p.cancel() }

func (p *browserPage) Load(token string, profile Profile) error {
	ctx, cancel := context.WithTimeout(p.ctx, 45*time.Second)
	defer cancel()
	encoded, _ := json.Marshal(token)
	if err := chromedp.Run(ctx,
		chromedp.Navigate(p.url),
		chromedp.Poll(`typeof window.WLInit === 'function'`, nil, chromedp.WithPollingTimeout(30*time.Second)),
		chromedp.Evaluate(`window.WLTOKEN = `+string(encoded), nil),
	); err != nil {
		return fmt.Errorf("load headless page: %w", err)
	}
	settings, _ := json.Marshal(map[string]any{"roomName": profile.Name, "maxPlayers": profile.Settings.MaxPlayers, "public": profile.Settings.Public, "password": profile.Settings.Password})
	bootstrap := `(function(settings, scriptCreatesRoom) {
  const original = window.WLInit;
  let created = false;
  window.WLInit = function(options) {
   if (created) throw new Error('Room already created. Enable "My scripts call WLInit" for existing room scripts.');
   const selected = Object.assign({}, options || {}, settings, {token:window.WLTOKEN});
   if (!selected.password) selected.password = null;
   const room = original.call(window, selected);
   created = true;
   window.WLROOM = room;
   room.onRoomLink = link => console.log(link);
   room.onCaptcha = () => console.error('Token rejected. Stop the room and start with a fresh token.');
   return room;
  };
  if (!scriptCreatesRoom) window.WLInit({});
 })(` + string(settings) + `,` + fmt.Sprint(profile.ScriptCreatesRoom) + `);`
	if err := chromedp.Run(ctx, chromedp.Evaluate(bootstrap, nil)); err != nil {
		return fmt.Errorf("initialize room: %w", err)
	}
	return p.run(ctx, profile.Scripts)
}

func (p *browserPage) Run(scripts []Script) error {
	ctx, cancel := context.WithTimeout(p.ctx, 30*time.Second)
	defer cancel()
	return p.run(ctx, scripts)
}

func (p *browserPage) run(ctx context.Context, scripts []Script) error {
	for _, script := range scripts {
		p.log("Running " + script.Name)
		// Await a returned Promise so dependent scripts run in the configured order.
		if err := chromedp.Run(ctx, chromedp.Evaluate(script.Source, nil, func(p *runtime.EvaluateParams) *runtime.EvaluateParams { return p.WithAwaitPromise(true) })); err != nil {
			return fmt.Errorf("script %s: %w", script.Name, err)
		}
	}
	return nil
}
