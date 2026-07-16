package server

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"os"
	"regexp"
	"sync"

	"github.com/chromedp/cdproto/fetch"
	cdpruntime "github.com/chromedp/cdproto/runtime"
	"github.com/chromedp/chromedp"

	"headless-launcher-go/internal/hack"
)

// RoomPage manages a single Chrome tab running a WebLiero room.
type RoomPage struct {
	ID     string
	ctx    context.Context
	cancel context.CancelFunc
	bridge *exposeBridge

	mu       sync.Mutex
	logFuncs []func(string)
	code     string // webliero room code, parsed from the onRoomLink console line
}

// roomLinkRe extracts the room code from the onRoomLink URL the client logs,
// e.g. https://www.webliero.com/?v=20&c=8COdPeKGahk
var roomLinkRe = regexp.MustCompile(`[?&]c=([A-Za-z0-9_-]+)`)

// Code returns the room's webliero join code, or "" if it hasn't registered yet.
func (rp *RoomPage) Code() string {
	rp.mu.Lock()
	defer rp.mu.Unlock()
	return rp.code
}

// NewRoomPage creates a new Chrome tab and sets up console/error listeners + exposed functions.
func NewRoomPage(browserCtx context.Context, id string) (*RoomPage, error) {
	tabCtx, tabCancel := chromedp.NewContext(browserCtx)

	rp := &RoomPage{
		ID:     id,
		ctx:    tabCtx,
		cancel: tabCancel,
		bridge: newExposeBridge(),
	}

	// Register exposed functions before navigation
	rp.bridge.Register("__getPaletteFromPng", stubExposedFunc)
	rp.bridge.Register("__convertPngToArray", stubExposedFunc)
	// __getRandomName was ported to a browser-side JS function
	// (builder-room/__randomname.js) — the launcher no longer provides it.
	rp.bridge.Register("__commitLevel", stubExposedFunc)
	rp.bridge.Register("__getInterestingPaths", func(args []json.RawMessage) (any, error) {
		return hack.GetPaths(), nil
	})

	// Force tab creation so we can set up listeners
	if err := chromedp.Run(tabCtx); err != nil {
		tabCancel()
		return nil, fmt.Errorf("init tab: %w", err)
	}

	// Set up console listener
	chromedp.ListenTarget(tabCtx, func(ev interface{}) {
		switch e := ev.(type) {
		case *cdpruntime.EventConsoleAPICalled:
			rp.handleConsole(e)
		case *cdpruntime.EventExceptionThrown:
			if e.ExceptionDetails != nil && e.ExceptionDetails.Exception != nil {
				desc := e.ExceptionDetails.Exception.Description
				if desc == "" {
					desc = fmt.Sprintf("Exception at %d:%d", e.ExceptionDetails.LineNumber, e.ExceptionDetails.ColumnNumber)
				}
				rp.log(desc)
			}
		case *cdpruntime.EventBindingCalled:
			rp.bridge.HandleBindingCalled(tabCtx, e)
		}
	})

	// Install bindings
	if err := rp.bridge.Install(tabCtx); err != nil {
		tabCancel()
		return nil, fmt.Errorf("install bindings: %w", err)
	}

	return rp, nil
}

// OnLog registers a callback for log messages.
func (rp *RoomPage) OnLog(fn func(string)) {
	rp.mu.Lock()
	defer rp.mu.Unlock()
	rp.logFuncs = append(rp.logFuncs, fn)
}

// OffLog removes a log callback.
func (rp *RoomPage) OffLog(fn func(string)) {
	rp.mu.Lock()
	defer rp.mu.Unlock()
	for i, f := range rp.logFuncs {
		// Compare function pointers
		if fmt.Sprintf("%p", f) == fmt.Sprintf("%p", fn) {
			rp.logFuncs = append(rp.logFuncs[:i], rp.logFuncs[i+1:]...)
			break
		}
	}
}

func (rp *RoomPage) log(msg string) {
	rp.mu.Lock()
	fns := make([]func(string), len(rp.logFuncs))
	copy(fns, rp.logFuncs)
	rp.mu.Unlock()

	for _, fn := range fns {
		fn(msg)
	}
}

func (rp *RoomPage) handleConsole(ev *cdpruntime.EventConsoleAPICalled) {
	parts := make([]string, 0, len(ev.Args))
	for _, arg := range ev.Args {
		val := resolveRemoteObject(arg)
		parts = append(parts, val)
	}
	msg := ""
	for i, p := range parts {
		if i > 0 {
			msg += " "
		}
		msg += p
	}
	if rp.code == "" {
		if m := roomLinkRe.FindStringSubmatch(msg); m != nil {
			rp.mu.Lock()
			rp.code = m[1]
			rp.mu.Unlock()
		}
	}
	rp.log(msg)
}

// resolveRemoteObject extracts a string representation from a runtime.RemoteObject.
func resolveRemoteObject(obj *cdpruntime.RemoteObject) string {
	if obj.Type == cdpruntime.TypeString {
		var s string
		if err := json.Unmarshal(obj.Value, &s); err == nil {
			return s
		}
	}
	if obj.Type == cdpruntime.TypeNumber || obj.Type == cdpruntime.TypeBoolean {
		return string(obj.Value)
	}
	if obj.Type == cdpruntime.TypeUndefined {
		return "undefined"
	}
	if obj.Description != "" {
		return obj.Description
	}
	if obj.Value != nil {
		return string(obj.Value)
	}
	return fmt.Sprintf("[%s]", obj.Type)
}

// LoadHeadless navigates to webliero.com/headless, optionally intercepting the script.
func (rp *RoomPage) LoadHeadless(scriptPath string, hacked bool) error {
	rp.log("Loading headless...")

	if hacked {
		scriptContents, err := os.ReadFile(scriptPath)
		if err != nil {
			return fmt.Errorf("read hacked script: %w", err)
		}
		rp.log(fmt.Sprintf("hacked script length %d", len(scriptContents)))
		// CDP Fetch.fulfillRequest requires the body base64-encoded; passing
		// raw JS gives -32602 Invalid parameters (regardless of size).
		scriptB64 := base64.StdEncoding.EncodeToString(scriptContents)

		// Enable fetch domain interception
		if err := chromedp.Run(rp.ctx, fetch.Enable().WithPatterns([]*fetch.RequestPattern{
			{URLPattern: "*headless-min.js*"},
		})); err != nil {
			return fmt.Errorf("enable fetch: %w", err)
		}

		// Listen for intercepted requests
		chromedp.ListenTarget(rp.ctx, func(ev interface{}) {
			if e, ok := ev.(*fetch.EventRequestPaused); ok {
				go func() {
					if err := chromedp.Run(rp.ctx, fetch.FulfillRequest(e.RequestID, 200).
						WithResponseHeaders([]*fetch.HeaderEntry{
							{Name: "Content-Type", Value: "application/javascript"},
						}).
						WithBody(scriptB64)); err != nil {
						fmt.Printf("fulfill request: %v\n", err)
					}
				}()
			}
		})
	}

	// Navigate to headless page
	if err := chromedp.Run(rp.ctx, chromedp.Navigate("https://www.webliero.com/headless")); err != nil {
		return fmt.Errorf("navigate: %w", err)
	}

	// Wait for window.WLInit to appear. We can't rely on Promise-returning
	// Evaluate here — chromedp doesn't await Promises by default and EvalAsValue
	// only flips ReturnByValue. Poll is the reliable wait primitive.
	if err := chromedp.Run(rp.ctx, chromedp.Poll(`typeof window.WLInit === 'function'`, nil)); err != nil {
		return fmt.Errorf("wait for WLInit: %w", err)
	}

	rp.log("Loaded headless")

	// Inject exposed function wrappers (after page load)
	if err := rp.bridge.InjectWrappers(rp.ctx); err != nil {
		return fmt.Errorf("inject wrappers: %w", err)
	}

	// Check for EVIL flag
	var evil interface{}
	if err := chromedp.Run(rp.ctx, chromedp.Evaluate(`window.EVIL`, &evil)); err == nil && evil != nil {
		rp.log("hacked")
	}

	return nil
}

// SetToken sets the room token.
func (rp *RoomPage) SetToken(token string) error {
	js := fmt.Sprintf(`window.WLTOKEN = %q`, token)
	return chromedp.Run(rp.ctx, chromedp.Evaluate(js, nil))
}

// RunScriptPath reads a JS file and evaluates it in the page context.
func (rp *RoomPage) RunScriptPath(scriptPath string) error {
	rp.log(fmt.Sprintf("Loading script %s", scriptPath))
	contents, err := os.ReadFile(scriptPath)
	if err != nil {
		return fmt.Errorf("read script %s: %w", scriptPath, err)
	}
	return chromedp.Run(rp.ctx, chromedp.Evaluate(string(contents), nil))
}

// RunScriptPaths evaluates multiple scripts sequentially on the same page.
func (rp *RoomPage) RunScriptPaths(paths []string) error {
	for _, p := range paths {
		if err := rp.RunScriptPath(p); err != nil {
			return err
		}
	}
	return nil
}

// Close navigates to about:blank and closes the tab.
func (rp *RoomPage) Close() error {
	if err := chromedp.Run(rp.ctx, chromedp.Navigate("about:blank")); err != nil {
		// Ignore navigate errors during close
		_ = err
	}
	rp.cancel()
	return nil
}

// Context returns the chromedp context for this room's tab.
func (rp *RoomPage) Context() context.Context {
	return rp.ctx
}

func stubExposedFunc(args []json.RawMessage) (any, error) {
	return nil, nil
}
