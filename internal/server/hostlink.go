package server

// Host link: an OUTBOUND persistent WebSocket to ext-proxy that replaces the
// inbound tunnel (spec: _specs/host-link.md). wlhl dials wss://…/hostlink with
// its host token, sends a hello, and then answers JSON request frames by
// dispatching them into the SAME mux the HTTP API serves — the link is just a
// second transport for the existing API, zero endpoint duplication. Reconnects
// forever with jittered backoff; ping/pong keepalive. The tunnel (if any)
// keeps working — both transports can coexist during migration.

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"math/rand"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"time"

	"github.com/coder/websocket"
)

// linkFrame is the wire format, shared with ext-proxy's hostlink.go.
type linkFrame struct {
	Type   string          `json:"type"`             // hello | req | resp | event
	ID     string          `json:"id,omitempty"`     // correlation id (req/resp)
	Method string          `json:"method,omitempty"` // req
	Path   string          `json:"path,omitempty"`   // req: full path+query, e.g. /api/rooms/x/logs?tail=5
	Body   json.RawMessage `json:"body,omitempty"`   // req body / resp body
	Status int             `json:"status,omitempty"` // resp
	Event  string          `json:"event,omitempty"`  // event name
	Data   json.RawMessage `json:"data,omitempty"`   // event payload
	// hello fields
	Proto   int        `json:"proto,omitempty"`
	Name    string     `json:"name,omitempty"`
	Version string     `json:"version,omitempty"`
	Rooms   []RoomInfo `json:"rooms,omitempty"`
	MaxRoom int        `json:"maxRooms,omitempty"`
}

const (
	linkProto        = 1
	linkPingEvery    = 30 * time.Second
	linkMaxFrame     = 600 << 10 // a hair above the 512KB response cap
	linkReqParallel  = 8         // concurrent in-flight requests served per link
	linkRespBodyCap  = 512 << 10 // matches ext-proxy hostRespCap
	linkBackoffStart = 1 * time.Second
	linkBackoffMax   = 60 * time.Second
)

// startLink runs the reconnect loop in a goroutine. Never returns an error to
// the caller — a broken link is a retriable condition, not a startup failure.
func (s *Server) startLink(cfg APIConfig) {
	name := cfg.LinkName
	if name == "" {
		if h, err := os.Hostname(); err == nil {
			name = h
		} else {
			name = "wlhl"
		}
	}
	go func() {
		backoff := linkBackoffStart
		for {
			err := s.runLinkOnce(cfg.LinkURL, cfg.LinkToken, name, cfg.MaxRooms)
			// jittered backoff: 0.5x..1.5x
			d := backoff/2 + time.Duration(rand.Int63n(int64(backoff)))
			log.Printf("[link] disconnected (%v) — redialing in %s", err, d.Round(time.Second))
			time.Sleep(d)
			backoff *= 2
			if backoff > linkBackoffMax {
				backoff = linkBackoffMax
			}
		}
	}()
}

// runLinkOnce dials, says hello, and serves frames until the connection dies.
func (s *Server) runLinkOnce(url, token, name string, maxRooms int) error {
	dialCtx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	conn, _, err := websocket.Dial(dialCtx, url, &websocket.DialOptions{
		HTTPHeader: http.Header{"Authorization": {"Bearer " + token}},
	})
	cancel()
	if err != nil {
		return fmt.Errorf("dial: %w", err)
	}
	conn.SetReadLimit(linkMaxFrame)
	ctx, stop := context.WithCancel(context.Background())
	defer stop()
	defer conn.Close(websocket.StatusNormalClosure, "bye")

	writeMu := make(chan struct{}, 1) // serialize writes (ch-as-mutex, ctx-aware)
	writeMu <- struct{}{}
	writeFrame := func(f *linkFrame) error {
		b, err := json.Marshal(f)
		if err != nil {
			return err
		}
		select {
		case <-writeMu:
		case <-ctx.Done():
			return ctx.Err()
		}
		defer func() { writeMu <- struct{}{} }()
		wctx, wcancel := context.WithTimeout(ctx, 10*time.Second)
		defer wcancel()
		return conn.Write(wctx, websocket.MessageText, b)
	}

	// hello — the auto-registration payload
	if err := writeFrame(&linkFrame{
		Type: "hello", Proto: linkProto, Name: name,
		Version: linkVersion(), Rooms: s.RoomsInfo(), MaxRoom: maxRooms,
	}); err != nil {
		return fmt.Errorf("hello: %w", err)
	}
	log.Printf("[link] connected to %s as %q", url, name)
	s.setLinkSender(writeFrame)
	defer s.setLinkSender(nil)

	// keepalive
	go func() {
		t := time.NewTicker(linkPingEvery)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-t.C:
				pctx, pcancel := context.WithTimeout(ctx, 10*time.Second)
				err := conn.Ping(pctx)
				pcancel()
				if err != nil {
					stop() // missed pong: kill the read loop → redial
					return
				}
			}
		}
	}()

	sem := make(chan struct{}, linkReqParallel)
	for {
		typ, data, err := conn.Read(ctx)
		if err != nil {
			return err
		}
		if typ != websocket.MessageText {
			continue
		}
		var f linkFrame
		if err := json.Unmarshal(data, &f); err != nil {
			log.Printf("[link] bad frame: %v", err)
			continue
		}
		if f.Type != "req" {
			continue // future: server→host control frames
		}
		select {
		case sem <- struct{}{}:
		case <-ctx.Done():
			return ctx.Err()
		}
		go func(f linkFrame) {
			defer func() { <-sem }()
			resp := s.serveLinkRequest(&f)
			if err := writeFrame(resp); err != nil {
				log.Printf("[link] write resp %s: %v", f.ID, err)
			}
		}(f)
	}
}

// serveLinkRequest dispatches a req frame into the API mux in-process. The
// link itself is the authenticated channel, so the bearer check is skipped —
// which also means the link works even when no --http listener is configured.
func (s *Server) serveLinkRequest(f *linkFrame) *linkFrame {
	resp := &linkFrame{Type: "resp", ID: f.ID}
	if s.apiMux == nil {
		resp.Status = http.StatusServiceUnavailable
		resp.Body, _ = json.Marshal(map[string]string{"error": "api not initialized"})
		return resp
	}
	if !strings.HasPrefix(f.Path, "/api/") {
		resp.Status = http.StatusBadRequest
		resp.Body, _ = json.Marshal(map[string]string{"error": "path must start with /api/"})
		return resp
	}
	var body io.Reader
	if len(f.Body) > 0 {
		body = strings.NewReader(string(f.Body))
	}
	req := httptest.NewRequest(f.Method, f.Path, body)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	rec := httptest.NewRecorder()
	s.apiMux.ServeHTTP(rec, req)
	resp.Status = rec.Code
	out := rec.Body.Bytes()
	if len(out) > linkRespBodyCap {
		out = out[:linkRespBodyCap]
	}
	// resp bodies travel as raw JSON when possible, else as a JSON string
	if json.Valid(out) {
		resp.Body = json.RawMessage(out)
	} else {
		resp.Body, _ = json.Marshal(string(out))
	}
	return resp
}

// PushLinkEvent sends an unsolicited event frame (queue updates, room list
// changes). No-op when the link is down — events are ephemeral by design.
func (s *Server) PushLinkEvent(event string, data interface{}) {
	s.linkMu.Lock()
	send := s.linkSend
	s.linkMu.Unlock()
	if send == nil {
		return
	}
	b, err := json.Marshal(data)
	if err != nil {
		return
	}
	if err := send(&linkFrame{Type: "event", Event: event, Data: b}); err != nil {
		log.Printf("[link] push %s: %v", event, err)
	}
}

func (s *Server) setLinkSender(fn func(*linkFrame) error) {
	s.linkMu.Lock()
	s.linkSend = fn
	s.linkMu.Unlock()
}

// linkVersion reports the build version (set via -ldflags in release builds).
var wlhlVersion = "dev"

func linkVersion() string { return wlhlVersion }
