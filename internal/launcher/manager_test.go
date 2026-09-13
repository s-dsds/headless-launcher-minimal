package launcher

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

type fakePage struct {
	done    chan struct{}
	once    sync.Once
	loadErr error
	runErr  error
	load    func()
}

func (p *fakePage) Load(string, Profile) error {
	if p.load != nil {
		p.load()
	}
	return p.loadErr
}
func (p *fakePage) Run([]Script) error    { return p.runErr }
func (p *fakePage) Close()                { p.once.Do(func() { close(p.done) }) }
func (p *fakePage) Done() <-chan struct{} { return p.done }

func fixture(t *testing.T) Config {
	t.Helper()
	return Config{Rooms: []Profile{{ID: "room", Name: "Test room", Settings: Settings{MaxPlayers: 12}, Scripts: []Script{{Name: "room.js", Source: "console.log('hi')"}}}}}
}

func TestLifecycle(t *testing.T) {
	var pages []*fakePage
	m := NewManager(fixture(t), func(log func(string)) (Page, error) {
		p := &fakePage{done: make(chan struct{})}
		pages = append(pages, p)
		log("https://www.webliero.com/?v=20&c=abc")
		return p, nil
	})
	defer m.Close()
	if err := m.Action("room", "start", "test-secret", nil); err != nil {
		t.Fatal(err)
	}
	if got := m.List()[0]; got.State != "running" || got.Link == "" {
		t.Fatal(got)
	}
	if err := m.Action("room", "start", "test-secret", nil); err == nil {
		t.Fatal("duplicate start allowed")
	}
	if err := m.Action("room", "restart", "test-secret", nil); err != nil {
		t.Fatal(err)
	}
	select {
	case <-pages[0].Done():
	default:
		t.Fatal("restart leaked tab")
	}
	pages[1].runErr = errors.New("script failed")
	if err := m.Action("room", "run", "test-secret", []Script{{Name: "bad.js", Source: "bad"}}); err == nil {
		t.Fatal("missing script failure")
	}
	if got := m.List()[0]; got.State != "error" || got.Link != "" {
		t.Fatal(got)
	}
	select {
	case <-pages[1].Done():
	default:
		t.Fatal("failed script leaked tab")
	}
	if err := m.Action("room", "stop", "test-secret", nil); err != nil {
		t.Fatal(err)
	}
	if m.List()[0].State != "stopped" {
		t.Fatal("not stopped")
	}
}

func TestFailedStartRedactsTokenAndClosesPage(t *testing.T) {
	c := fixture(t)
	p := &fakePage{done: make(chan struct{}), loadErr: errors.New("failed test-secret")}
	m := NewManager(c, func(log func(string)) (Page, error) { log("test-secret"); return p, nil })
	if err := m.Action("room", "start", "test-secret", nil); err == nil || strings.Contains(err.Error(), "test-secret") {
		t.Fatalf("bad error: %v", err)
	}
	select {
	case <-p.Done():
	default:
		t.Fatal("failed start leaked page")
	}
	for _, l := range m.List()[0].Logs {
		if strings.Contains(l.Message, "test-secret") {
			t.Fatal("token leaked")
		}
	}
}

func TestConcurrentMutationRejectedAndLogsBounded(t *testing.T) {
	entered, release := make(chan struct{}), make(chan struct{})
	p := &fakePage{done: make(chan struct{}), load: func() { close(entered); <-release }}
	m := NewManager(fixture(t), func(func(string)) (Page, error) { return p, nil })
	done := make(chan error, 1)
	go func() { done <- m.Action("room", "start", "test-secret", nil) }()
	<-entered
	if err := m.Action("room", "start", "test-secret", nil); err == nil {
		t.Fatal("overlapping mutation allowed")
	}
	if m.List()[0].State != "starting" {
		t.Fatal("missing starting state")
	}
	close(release)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 500; i++ {
		m.rooms["room"].log(strings.Repeat("x", 5000))
	}
	s := m.List()[0]
	if len(s.Logs) != 300 || len(s.Logs[0].Message) > 4100 {
		t.Fatal("unbounded logs")
	}
	p.Close()
	deadline := time.Now().Add(time.Second)
	for m.List()[0].State != "error" && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if m.List()[0].State != "error" {
		t.Fatal("tab crash not detected")
	}
	m.Close()
}

func TestPersistenceAndDynamicRooms(t *testing.T) {
	path := filepath.Join(t.TempDir(), "rooms.json")
	c, err := LoadConfig(path)
	if err != nil || len(c.Rooms) != 0 {
		t.Fatalf("first launch: %v", err)
	}
	m := NewManager(c, func(func(string)) (Page, error) { return &fakePage{done: make(chan struct{})}, nil })
	m.SetStorage(path)
	p := fixture(t).Rooms[0]
	id, err := m.Add(p)
	if err != nil {
		t.Fatal(err)
	}
	if err = m.Action(id, "start", "one-use-token", nil); err != nil {
		t.Fatal(err)
	}
	if err = m.Update(id, p); err == nil {
		t.Fatal("edited running room")
	}
	if err = m.Action(id, "restart", "", nil); err == nil || m.List()[0].State != "running" {
		t.Fatal("missing token stopped running room")
	}
	if err = m.Delete(id); err == nil {
		t.Fatal("deleted running room")
	}
	if err = m.Action(id, "stop", "", nil); err != nil {
		t.Fatal(err)
	}
	if m.get(id).token != "" {
		t.Fatal("stopped room retained token")
	}
	p.Name = "Updated"
	p.Scripts = append(p.Scripts, Script{Name: "second.js", Source: "console.log('second')"})
	if err = m.Update(id, p); err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(b), "one-use-token") || strings.Contains(string(b), "tokenEnv") {
		t.Fatal("token persisted")
	}
	loaded, err := LoadConfig(path)
	if err != nil {
		t.Fatal(err)
	}
	restored := NewManager(loaded, nil)
	if got := restored.List()[0]; got.State != "stopped" || got.Name != "Updated" || len(got.ScriptNames) != 2 {
		t.Fatal(got)
	}
	if err = restored.Action(id, "start", "", nil); err == nil {
		t.Fatal("restored without fresh token")
	}
	if err = m.Delete(id); err != nil {
		t.Fatal(err)
	}
	loaded, err = LoadConfig(path)
	if err != nil || len(loaded.Rooms) != 0 {
		t.Fatalf("delete not persisted: %v", err)
	}
}

func TestPersistenceFailureDoesNotChangeRooms(t *testing.T) {
	m := NewManager(fixture(t), nil)
	m.persist = func(Config) error { return errors.New("disk full") }
	p := fixture(t).Rooms[0]
	p.Name = "changed"
	if err := m.Update(p.ID, p); err == nil {
		t.Fatal("failed update accepted")
	}
	if m.List()[0].Name == "changed" {
		t.Fatal("failed update changed definition")
	}
	if err := m.Delete(p.ID); err == nil || !m.Has(p.ID) {
		t.Fatal("failed delete changed definitions")
	}
	p.ID = "new"
	if _, err := m.Add(p); err == nil || m.Has(p.ID) {
		t.Fatal("failed add changed definitions")
	}
}

func TestDirectoryImport(t *testing.T) {
	dir := t.TempDir()
	for name, source := range map[string]string{"20-rules.js": "second", "10-init.js": "first", "nested/30.js": "third", "note.txt": "ignore", "node_modules/dep.js": "ignore"} {
		path := filepath.Join(dir, name)
		if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(source), 0600); err != nil {
			t.Fatal(err)
		}
	}
	scripts, err := ReadScripts([]string{dir})
	if err != nil {
		t.Fatal(err)
	}
	if len(scripts) != 3 || scripts[0].Name != "10-init.js" || scripts[1].Source != "second" || scripts[2].Name != "nested/30.js" {
		t.Fatal(scripts)
	}
}

func TestConcurrentAddAndList(t *testing.T) {
	m := NewManager(Config{}, nil)
	var wg sync.WaitGroup
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			p := Profile{Name: "New", Settings: Settings{MaxPlayers: 12}}
			if _, err := m.Add(p); err != nil {
				t.Error(err)
			}
			m.List()
		}()
	}
	wg.Wait()
	if len(m.List()) != 20 {
		t.Fatal("lost room")
	}
}

func TestTokenCannotBeSharedBetweenActiveRooms(t *testing.T) {
	c := fixture(t)
	second := c.Rooms[0]
	second.ID = "second"
	c.Rooms = append(c.Rooms, second)
	m := NewManager(c, func(func(string)) (Page, error) { return &fakePage{done: make(chan struct{})}, nil })
	defer m.Close()
	if err := m.Action("room", "start", "token-a", nil); err != nil {
		t.Fatal(err)
	}
	if err := m.Action("second", "start", "token-b", nil); err != nil {
		t.Fatal(err)
	}
	if err := m.Action("second", "restart", "token-a", nil); err == nil {
		t.Fatal("shared token accepted")
	}
	if m.List()[1].State != "running" {
		t.Fatal("rejected restart stopped original room")
	}
	if err := m.Action("room", "restart", "token-a", nil); err != nil {
		t.Fatal("explicit retry for testing rejected:", err)
	}
}
