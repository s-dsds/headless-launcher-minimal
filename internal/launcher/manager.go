package launcher

import (
	"fmt"
	"os"
	"regexp"
	"strings"
	"sync"
	"time"
)

type LogLine struct {
	At      time.Time `json:"at"`
	Message string    `json:"message"`
}

type Snapshot struct {
	ID    string    `json:"id"`
	State string    `json:"state"`
	Link  string    `json:"link"`
	Error string    `json:"error"`
	Logs  []LogLine `json:"logs"`
}

type room struct {
	op      sync.Mutex
	mu      sync.Mutex
	profile Profile
	page    Page
	state   string
	link    string
	err     string
	token   string
	logs    []LogLine
}

type Manager struct {
	rooms   map[string]*room // Immutable after construction.
	order   []string
	newPage func(func(string)) (Page, error)
}

func NewManager(c Config, newPage func(func(string)) (Page, error)) *Manager {
	m := &Manager{rooms: map[string]*room{}, newPage: newPage}
	for _, p := range c.Rooms {
		m.rooms[p.ID] = &room{profile: p, state: "stopped"}
		m.order = append(m.order, p.ID)
	}
	return m
}

var roomLink = regexp.MustCompile(`https://www\.webliero\.com/\?[^\s"<>]*\bc=[A-Za-z0-9_-]+`)

func (r *room) log(message string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	message = r.redact(message)
	if len(message) > 4096 {
		message = message[:4096] + "…"
	}
	if link := roomLink.FindString(message); link != "" {
		r.link = link
	}
	if len(r.logs) == 300 {
		copy(r.logs, r.logs[1:])
		r.logs = r.logs[:299]
	}
	r.logs = append(r.logs, LogLine{At: time.Now().UTC(), Message: message})
}

func (r *room) redact(s string) string {
	if r.token != "" {
		return strings.ReplaceAll(s, r.token, "[REDACTED]")
	}
	return s
}

func (m *Manager) List() []Snapshot {
	out := make([]Snapshot, 0, len(m.order))
	for _, id := range m.order {
		r := m.rooms[id]
		r.mu.Lock()
		out = append(out, Snapshot{ID: id, State: r.state, Link: r.link, Error: r.err, Logs: append([]LogLine{}, r.logs...)})
		r.mu.Unlock()
	}
	return out
}

func (m *Manager) Has(id string) bool { return m.rooms[id] != nil }

func (m *Manager) Action(id, action string, scripts []Script) error {
	r := m.rooms[id]
	if r == nil {
		return fmt.Errorf("unknown room %q", id)
	}
	// Reject overlapping mutations; the UI and shutdown can still read state.
	if !r.op.TryLock() {
		return fmt.Errorf("room %s is busy", id)
	}
	defer r.op.Unlock()
	switch action {
	case "stop":
		r.stop()
		return nil
	case "restart":
		r.stop()
		return m.start(r)
	case "start":
		return m.start(r)
	case "run":
		r.mu.Lock()
		page := r.page
		state := r.state
		r.mu.Unlock()
		if page == nil || state != "running" {
			return fmt.Errorf("room is not running")
		}
		if len(scripts) == 0 {
			return fmt.Errorf("provide at least one script")
		}
		if err := page.Run(scripts); err != nil {
			r.stop() // A timed-out/partially applied script leaves unknown state.
			return r.fail(err)
		}
		return nil
	default:
		return fmt.Errorf("unknown action")
	}
}

func (r *room) fail(err error) error {
	r.mu.Lock()
	r.state = "error"
	r.link = ""
	r.err = r.redact(err.Error())
	message := r.err
	r.mu.Unlock()
	r.log(message)
	return fmt.Errorf("%s", message)
}

func (m *Manager) start(r *room) error {
	r.mu.Lock()
	if r.page != nil {
		r.mu.Unlock()
		return fmt.Errorf("room already started; use restart")
	}
	r.state = "starting"
	r.err = ""
	r.link = ""
	r.token = os.Getenv(r.profile.TokenEnv)
	token := r.token
	r.mu.Unlock()
	if r.profile.TokenEnv != "" && token == "" {
		return r.fail(fmt.Errorf("set environment variable %s before starting the server", r.profile.TokenEnv))
	}
	scripts, err := ReadScripts(r.profile.Scripts)
	if err != nil {
		return r.fail(err)
	}
	p, err := m.newPage(r.log)
	if err != nil {
		return r.fail(err)
	}
	if err = p.Load(token, scripts); err != nil {
		p.Close()
		return r.fail(err)
	}
	r.mu.Lock()
	r.page = p
	r.state = "running"
	r.mu.Unlock()
	r.log("Scripts loaded; waiting for the room script to report its join link")
	go func() {
		<-p.Done()
		r.mu.Lock()
		if r.page == p {
			r.page = nil
			r.state = "error"
			r.link = ""
			r.err = "Browser tab closed unexpectedly; restart the room"
		}
		r.mu.Unlock()
	}()
	return nil
}

func (r *room) stop() {
	r.mu.Lock()
	p := r.page
	r.page = nil
	r.state = "stopped"
	r.link = ""
	r.err = ""
	r.mu.Unlock()
	if p != nil {
		p.Close()
	}
	r.log("Stopped")
}

// Close is called after cancelling the browser, which unblocks in-flight work.
func (m *Manager) Close() {
	for _, id := range m.order {
		r := m.rooms[id]
		r.op.Lock()
		r.stop()
		r.op.Unlock()
	}
}
