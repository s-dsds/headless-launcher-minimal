package server

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"
	"sync/atomic"

	"github.com/chromedp/cdproto/runtime"
	"github.com/chromedp/chromedp"
)

// ExposedFunc is a Go function callable from browser JS.
// It receives JSON-encoded args and returns a JSON-encodable result.
type ExposedFunc func(args []json.RawMessage) (any, error)

// exposeBridge manages runtime.AddBinding + promise resolution.
type exposeBridge struct {
	mu    sync.Mutex
	funcs map[string]ExposedFunc
	seq   atomic.Int64
}

func newExposeBridge() *exposeBridge {
	return &exposeBridge{
		funcs: make(map[string]ExposedFunc),
	}
}

// Register adds a function to be exposed. Must be called before navigation.
func (b *exposeBridge) Register(name string, fn ExposedFunc) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.funcs[name] = fn
}

// Install calls runtime.AddBinding for all registered functions and
// sets up the EventBindingCalled listener.
func (b *exposeBridge) Install(ctx context.Context) error {
	b.mu.Lock()
	names := make([]string, 0, len(b.funcs))
	for name := range b.funcs {
		names = append(names, name)
	}
	b.mu.Unlock()

	actions := make([]chromedp.Action, 0, len(names))
	for _, name := range names {
		actions = append(actions, runtime.AddBinding(name))
	}
	if err := chromedp.Run(ctx, actions...); err != nil {
		return fmt.Errorf("addBindings: %w", err)
	}
	return nil
}

// InjectWrappers evaluates JS that wraps each binding in a Promise-returning function.
// Call after page load / after navigating.
func (b *exposeBridge) InjectWrappers(ctx context.Context) error {
	b.mu.Lock()
	names := make([]string, 0, len(b.funcs))
	for name := range b.funcs {
		names = append(names, name)
	}
	b.mu.Unlock()

	// Install the resolver infrastructure
	const infra = `
		window.__cdpPending = window.__cdpPending || {};
		window.__cdpResolve = function(id, result, error) {
			var p = window.__cdpPending[id];
			if (p) {
				delete window.__cdpPending[id];
				if (error) { p.reject(new Error(error)); }
				else { p.resolve(result); }
			}
		};
	`
	if err := chromedp.Run(ctx, chromedp.Evaluate(infra, nil)); err != nil {
		return fmt.Errorf("inject infra: %w", err)
	}

	for _, name := range names {
		// Wrap each binding: calling name(...args) returns a Promise
		wrapper := fmt.Sprintf(`
			(function() {
				var origBinding = window['%[1]s'];
				window['%[1]s'] = function() {
					var args = Array.prototype.slice.call(arguments);
					var id = '__cdp_' + Date.now() + '_' + Math.random();
					return new Promise(function(resolve, reject) {
						window.__cdpPending[id] = {resolve: resolve, reject: reject};
						origBinding(JSON.stringify({id: id, args: args}));
					});
				};
			})();
		`, name)
		if err := chromedp.Run(ctx, chromedp.Evaluate(wrapper, nil)); err != nil {
			return fmt.Errorf("inject wrapper %s: %w", name, err)
		}
	}

	return nil
}

// HandleBindingCalled processes a runtime.EventBindingCalled event.
func (b *exposeBridge) HandleBindingCalled(ctx context.Context, ev *runtime.EventBindingCalled) {
	b.mu.Lock()
	fn, ok := b.funcs[ev.Name]
	b.mu.Unlock()
	if !ok {
		return
	}

	// Parse the payload: {"id":"...","args":[...]}
	var payload struct {
		ID   string            `json:"id"`
		Args []json.RawMessage `json:"args"`
	}
	if err := json.Unmarshal([]byte(ev.Payload), &payload); err != nil {
		fmt.Printf("expose: unmarshal payload for %s: %v\n", ev.Name, err)
		return
	}

	// Run the Go function
	go func() {
		result, err := fn(payload.Args)
		var resolveJS string
		if err != nil {
			errJSON, _ := json.Marshal(err.Error())
			resolveJS = fmt.Sprintf(`window.__cdpResolve(%q, null, %s)`, payload.ID, errJSON)
		} else {
			resultJSON, _ := json.Marshal(result)
			resolveJS = fmt.Sprintf(`window.__cdpResolve(%q, %s, null)`, payload.ID, resultJSON)
		}
		if err2 := chromedp.Run(ctx, chromedp.Evaluate(resolveJS, nil)); err2 != nil {
			fmt.Printf("expose: resolve %s/%s: %v\n", ev.Name, payload.ID, err2)
		}
	}()
}
