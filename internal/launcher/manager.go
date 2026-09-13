package launcher

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
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
	ID                string    `json:"id"`
	Name              string    `json:"name"`
	Settings          Settings  `json:"settings"`
	ScriptCreatesRoom bool      `json:"scriptCreatesRoom"`
	ScriptNames       []string  `json:"scriptNames"`
	State             string    `json:"state"`
	Link              string    `json:"link"`
	Error             string    `json:"error"`
	Logs              []LogLine `json:"logs"`
}
type room struct {
	op                      sync.Mutex
	mu                      sync.Mutex
	profile                 Profile
	page                    Page
	state, link, err, token string
	generation              uint64
	logs                    []LogLine
}
type tokenClaim struct{ roomID string }

type Manager struct {
	tokens  map[string]*tokenClaim
	mu      sync.RWMutex
	rooms   map[string]*room
	order   []string
	closed  bool
	newPage func(func(string)) (Page, error)
	persist func(Config) error
}

func NewManager(c Config, newPage func(func(string)) (Page, error)) *Manager {
	m := &Manager{rooms: map[string]*room{}, tokens: map[string]*tokenClaim{}, newPage: newPage}
	for _, p := range c.Rooms {
		m.rooms[p.ID] = &room{profile: cloneProfile(p), state: "stopped"}
		m.order = append(m.order, p.ID)
	}
	return m
}

// SetStorage is configured once, before exposing the manager to requests.
func (m *Manager) SetStorage(path string) {
	m.persist = func(c Config) error { return SaveConfig(path, c) }
}
func (m *Manager) get(id string) *room { m.mu.RLock(); defer m.mu.RUnlock(); return m.rooms[id] }
func (m *Manager) Has(id string) bool  { return m.get(id) != nil }

// Persist the proposed snapshot before changing the in-memory definitions.
func (m *Manager) saveLocked(id string, replacement *Profile) error {
	c := Config{Rooms: []Profile{}}
	found := false
	for _, key := range m.order {
		if key == id {
			found = true
			if replacement != nil {
				c.Rooms = append(c.Rooms, cloneProfile(*replacement))
			}
			continue
		}
		r := m.rooms[key]
		r.mu.Lock()
		c.Rooms = append(c.Rooms, cloneProfile(r.profile))
		r.mu.Unlock()
	}
	if !found && replacement != nil {
		c.Rooms = append(c.Rooms, cloneProfile(*replacement))
	}
	if m.persist != nil {
		return m.persist(c)
	}
	return nil
}
func (m *Manager) Add(p Profile) (string, error) {
	if p.ID == "" {
		b := make([]byte, 8)
		if _, err := rand.Read(b); err != nil {
			return "", err
		}
		p.ID = hex.EncodeToString(b)
	}
	p.Name = strings.TrimSpace(p.Name)
	if err := ValidateProfile(p); err != nil {
		return "", err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.closed {
		return "", fmt.Errorf("launcher is stopping")
	}
	if m.rooms[p.ID] != nil {
		return "", fmt.Errorf("room id already exists")
	}
	if len(m.rooms) >= 64 {
		return "", fmt.Errorf("at most 64 rooms")
	}
	if err := m.saveLocked(p.ID, &p); err != nil {
		return "", fmt.Errorf("save room: %w", err)
	}
	m.rooms[p.ID] = &room{profile: cloneProfile(p), state: "stopped"}
	m.order = append(m.order, p.ID)
	return p.ID, nil
}
func (m *Manager) Profile(id string) (Profile, error) {
	r := m.get(id)
	if r == nil {
		return Profile{}, fmt.Errorf("unknown room")
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	return cloneProfile(r.profile), nil
}
func (m *Manager) Update(id string, p Profile) error {
	p.ID = id
	p.Name = strings.TrimSpace(p.Name)
	if err := ValidateProfile(p); err != nil {
		return err
	}
	r := m.get(id)
	if r == nil {
		return fmt.Errorf("unknown room")
	}
	if !r.op.TryLock() {
		return fmt.Errorf("room is busy")
	}
	defer r.op.Unlock()
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.closed || m.rooms[id] != r {
		return fmt.Errorf("room is no longer available")
	}
	r.mu.Lock()
	active := r.page != nil
	r.mu.Unlock()
	if active {
		return fmt.Errorf("stop the room before editing")
	}
	if err := m.saveLocked(id, &p); err != nil {
		return fmt.Errorf("save room: %w", err)
	}
	r.mu.Lock()
	r.profile = cloneProfile(p)
	r.mu.Unlock()
	return nil
}
func (m *Manager) Delete(id string) error {
	r := m.get(id)
	if r == nil {
		return fmt.Errorf("unknown room")
	}
	if !r.op.TryLock() {
		return fmt.Errorf("room is busy")
	}
	defer r.op.Unlock()
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.closed || m.rooms[id] != r {
		return fmt.Errorf("room is no longer available")
	}
	r.mu.Lock()
	active := r.page != nil
	r.mu.Unlock()
	if active {
		return fmt.Errorf("stop the room before deleting")
	}
	if err := m.saveLocked(id, nil); err != nil {
		return fmt.Errorf("delete room: %w", err)
	}
	delete(m.rooms, id)
	for i, key := range m.order {
		if key == id {
			m.order = append(m.order[:i], m.order[i+1:]...)
			break
		}
	}
	return nil
}

var roomLink = regexp.MustCompile(`https://www\.webliero\.com/\?[^\s"<>]*\bc=[A-Za-z0-9_-]+`)

func (r *room) redact(s string) string {
	if r.token != "" {
		return strings.ReplaceAll(s, r.token, "[REDACTED]")
	}
	return s
}
func (r *room) log(message string) { r.mu.Lock(); defer r.mu.Unlock(); r.logLocked(message) }
func (r *room) logLocked(message string) {
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
func (m *Manager) List() []Snapshot {
	m.mu.RLock()
	defer m.mu.RUnlock()
	out := make([]Snapshot, 0, len(m.order))
	for _, id := range m.order {
		r := m.rooms[id]
		r.mu.Lock()
		names := []string{}
		for _, s := range r.profile.Scripts {
			names = append(names, s.Name)
		}
		out = append(out, Snapshot{ID: id, Name: r.profile.Name, Settings: r.profile.Settings, ScriptCreatesRoom: r.profile.ScriptCreatesRoom, ScriptNames: names, State: r.state, Link: r.link, Error: r.err, Logs: append([]LogLine{}, r.logs...)})
		r.mu.Unlock()
	}
	return out
}

// A token may only be in flight for one room. An explicit retry after stopping is allowed.
func (m *Manager) claimToken(id, token string) (func(), error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if existing := m.tokens[token]; existing != nil && existing.roomID != id {
		return nil, fmt.Errorf("this token is already in use by another room")
	}
	claim := &tokenClaim{roomID: id}
	m.tokens[token] = claim
	return func() {
		m.mu.Lock()
		defer m.mu.Unlock()
		if m.tokens[token] == claim {
			delete(m.tokens, token)
		}
	}, nil
}

func (m *Manager) Action(id, action, token string, scripts []Script) error {
	r := m.get(id)
	if r == nil {
		return fmt.Errorf("unknown room %q", id)
	}
	if !r.op.TryLock() {
		return fmt.Errorf("room is busy")
	}
	defer r.op.Unlock()
	m.mu.RLock()
	available := !m.closed && m.rooms[id] == r
	m.mu.RUnlock()
	if !available {
		return fmt.Errorf("room is no longer available")
	}
	switch action {
	case "stop":
		r.stop()
		return nil
	case "start", "restart":
		token = strings.TrimSpace(token)
		if token == "" || len(token) > 4096 {
			return fmt.Errorf("paste a fresh WebLiero token to start this room")
		}
		r.mu.Lock()
		active := r.page != nil
		r.mu.Unlock()
		if action == "start" && active {
			return fmt.Errorf("room already started; use restart")
		}
		release, err := m.claimToken(id, token)
		if err != nil {
			return err
		}
		if action == "restart" {
			r.stop()
		}
		return m.start(r, token, release)
	case "run":
		if len(scripts) == 0 {
			return fmt.Errorf("provide at least one script")
		}
		if err := ValidateScripts(scripts); err != nil {
			return err
		}
		r.mu.Lock()
		page := r.page
		state := r.state
		r.mu.Unlock()
		if page == nil || state != "running" {
			return fmt.Errorf("room is not running")
		}
		if err := page.Run(scripts); err != nil {
			safe := r.fail(err)
			r.stop()
			r.fail(safe)
			return safe
		}
		return nil
	default:
		return fmt.Errorf("unknown action")
	}
}
func (r *room) fail(err error) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.state = "error"
	r.link = ""
	r.err = r.redact(err.Error())
	r.logLocked(r.err)
	return fmt.Errorf("%s", r.err)
}
func (m *Manager) start(r *room, token string, release func()) error {
	r.mu.Lock()
	if r.page != nil {
		r.mu.Unlock()
		release()
		return fmt.Errorf("room already started; use restart")
	}
	r.state = "starting"
	r.err = ""
	r.link = ""
	r.token = token
	r.generation++
	generation := r.generation
	profile := cloneProfile(r.profile)
	r.mu.Unlock()
	log := func(message string) {
		r.mu.Lock()
		defer r.mu.Unlock()
		if r.generation == generation {
			r.logLocked(strings.ReplaceAll(message, token, "[REDACTED]"))
		}
	}
	p, err := m.newPage(log)
	if err == nil {
		err = p.Load(token, profile)
	}
	if err != nil {
		if p != nil {
			p.Close()
		}
		safe := r.fail(err)
		r.mu.Lock()
		r.token = ""
		r.generation++
		r.mu.Unlock()
		release()
		return safe
	}
	r.mu.Lock()
	r.page = p
	r.state = "running"
	r.mu.Unlock()
	r.log("Scripts loaded; waiting for the room link")
	go func() {
		<-p.Done()
		release()
		r.mu.Lock()
		defer r.mu.Unlock()
		if r.page == p {
			r.page = nil
			r.token = ""
			r.generation++
			r.state = "error"
			r.link = ""
			r.err = "Browser tab closed; start again with a fresh token"
		}
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
	r.token = ""
	r.generation++
	r.mu.Unlock()
	if p != nil {
		p.Close()
	}
	r.log("Stopped")
}
func (m *Manager) Close() {
	m.mu.Lock()
	m.closed = true
	rooms := make([]*room, 0, len(m.rooms))
	for _, r := range m.rooms {
		rooms = append(rooms, r)
	}
	m.mu.Unlock()
	for _, r := range rooms {
		r.op.Lock()
		r.stop()
		r.op.Unlock()
	}
}
