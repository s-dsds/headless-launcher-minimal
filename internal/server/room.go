package server

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"os"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/chromedp/cdproto/fetch"
	cdpruntime "github.com/chromedp/cdproto/runtime"
	"github.com/chromedp/chromedp"
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

	// Bounded ring of recent log lines so the HTTP API can serve tails without
	// touching the (possibly huge) log file. Newest at (recentPos-1)%cap.
	recent    []LogLine
	recentPos int

	gameSink  func(payload string) // "@@GAME@@ {...}" → gamestore
	queueSink func(payload string) // "@@QUEUE@@ {...}" → live queue state
	// chatSink receives the JSON payload of structured "@@CHAT@@ {...}" console
	// lines (set by the server when a chat store is configured).
	chatSink func(payload string)
}

// LogLine is one captured console/exception line.
type LogLine struct {
	At  time.Time `json:"at"`
	Msg string    `json:"msg"`
}

// recentCap bounds per-room retained log lines (~2000 lines ≈ a few hundred KB
// worst-case). History beyond this lives in the server's --log-file.
const recentCap = 2000

// chatMarker prefixes structured chat lines emitted by the room script.
const chatMarker = "@@CHAT@@ "

// gameMarker prefixes structured game-end records (spec: game-history.md §1).
const gameMarker = "@@GAME@@ "

// queueMarker prefixes live queue-state snapshots (spec: game-history.md §5).
const queueMarker = "@@QUEUE@@ "

// SetChatSink registers the receiver for structured chat payloads.
func (rp *RoomPage) SetChatSink(fn func(payload string)) {
	rp.mu.Lock()
	defer rp.mu.Unlock()
	rp.chatSink = fn
}

// SetGameSink registers the receiver for structured game-end payloads.
func (rp *RoomPage) SetGameSink(fn func(payload string)) {
	rp.mu.Lock()
	defer rp.mu.Unlock()
	rp.gameSink = fn
}

// SetQueueSink registers the receiver for live queue-state payloads.
func (rp *RoomPage) SetQueueSink(fn func(payload string)) {
	rp.mu.Lock()
	defer rp.mu.Unlock()
	rp.queueSink = fn
}

// TailLogs returns up to n recent log lines, oldest first.
func (rp *RoomPage) TailLogs(n int) []LogLine {
	rp.mu.Lock()
	defer rp.mu.Unlock()
	size := len(rp.recent)
	if n > size {
		n = size
	}
	out := make([]LogLine, 0, n)
	// ring is either not yet wrapped (recentPos==len) or wrapped (fixed cap)
	start := rp.recentPos - n
	for i := start; i < rp.recentPos; i++ {
		out = append(out, rp.recent[((i%size)+size)%size])
	}
	return out
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

	// The launcher no longer exposes any Go-backed room functions: the old
	// __getPaletteFromPng/__convertPngToArray/__commitLevel were dead no-op
	// stubs, __getInterestingPaths was a hack debug hook, and __getRandomName
	// moved to a browser-side script (builder-room/__randomname.js). Anything
	// a room needs from the client (weapons, callbacks, __ReadPNG) now comes
	// baked into the hacked script produced by headless-modifier.

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
	line := LogLine{At: time.Now(), Msg: msg}
	if len(rp.recent) < recentCap {
		rp.recent = append(rp.recent, line)
	} else {
		rp.recent[rp.recentPos%recentCap] = line
	}
	rp.recentPos++
	fns := make([]func(string), len(rp.logFuncs))
	copy(fns, rp.logFuncs)
	sink := rp.chatSink
	gsink := rp.gameSink
	qsink := rp.queueSink
	rp.mu.Unlock()

	// Structured lines → their stores (the raw line still goes to the normal
	// log below: logs stay the ground truth, the stores are the index).
	if sink != nil && strings.HasPrefix(msg, chatMarker) {
		sink(strings.TrimPrefix(msg, chatMarker))
	}
	if gsink != nil && strings.HasPrefix(msg, gameMarker) {
		gsink(strings.TrimPrefix(msg, gameMarker))
	}
	if qsink != nil && strings.HasPrefix(msg, queueMarker) {
		qsink(strings.TrimPrefix(msg, queueMarker))
	}

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

// LoadHeadless navigates to webliero.com/headless. When scriptPath is set, the
// headless-min.js request is intercepted and served from that file (e.g. a
// script hacked by headless-modifier); otherwise webliero's own vanilla client
// loads unmodified.
func (rp *RoomPage) LoadHeadless(scriptPath string) error {
	rp.log("Loading headless...")

	if scriptPath == "" {
		// Loud on purpose: before the hack-split, hacked was the default, so a
		// pre-split launch config that never passed --script now silently gets
		// a vanilla client — room scripts relying on injected hooks (__ReadPNG,
		// @@GAME@@/@@QUEUE@@ history emission) break at runtime with no launch
		// error. Vanilla stays a valid choice; it just must be visible.
		rp.log("WARNING: no --script / HEADLESS_SCRIPT — serving webliero's VANILLA client (no interception hooks; scripts needing a modified client will fail)")
	}

	if scriptPath != "" {
		scriptContents, err := os.ReadFile(scriptPath)
		if err != nil {
			return fmt.Errorf("read script %s: %w", scriptPath, err)
		}
		rp.log(fmt.Sprintf("serving script %s (%d bytes)", scriptPath, len(scriptContents)))
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
