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

func (p *fakePage) Load(string, []Script) error {
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
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "room.js"), []byte("console.log('hi')"), 0600); err != nil {
		t.Fatal(err)
	}
	return Config{Rooms: []Profile{{ID: "room", Scripts: []string{filepath.Join(dir, "room.js")}}}}
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
	if err := m.Action("room", "start", nil); err != nil {
		t.Fatal(err)
	}
	if got := m.List()[0]; got.State != "running" || got.Link == "" {
		t.Fatal(got)
	}
	if err := m.Action("room", "start", nil); err == nil {
		t.Fatal("duplicate start allowed")
	}
	if err := m.Action("room", "restart", nil); err != nil {
		t.Fatal(err)
	}
	select {
	case <-pages[0].Done():
	default:
		t.Fatal("restart leaked tab")
	}
	pages[1].runErr = errors.New("script failed")
	if err := m.Action("room", "run", []Script{{Source: "bad"}}); err == nil {
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
	if err := m.Action("room", "stop", nil); err != nil {
		t.Fatal(err)
	}
	if m.List()[0].State != "stopped" {
		t.Fatal("not stopped")
	}
}

func TestFailedStartRedactsTokenAndClosesPage(t *testing.T) {
	t.Setenv("TEST_ROOM_TOKEN", "test-secret")
	c := fixture(t)
	c.Rooms[0].TokenEnv = "TEST_ROOM_TOKEN"
	p := &fakePage{done: make(chan struct{}), loadErr: errors.New("failed test-secret")}
	m := NewManager(c, func(log func(string)) (Page, error) { log("test-secret"); return p, nil })
	if err := m.Action("room", "start", nil); err == nil || strings.Contains(err.Error(), "test-secret") {
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
	go func() { done <- m.Action("room", "start", nil) }()
	<-entered
	if err := m.Action("room", "start", nil); err == nil {
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

func TestConfigPathsAndValidation(t *testing.T) {
	c := fixture(t)
	path := filepath.Join(filepath.Dir(c.Rooms[0].Scripts[0]), "config.json")
	for _, input := range []string{
		`{"rooms":[{"id":"room","scripts":["room.js"]}]}`,
		`{"rooms":[{"id":"room","scripts":["room.js"],"typo":true}]}`,
		`{"rooms":[{"id":"../room","scripts":["room.js"]}]}`,
		`{"rooms":[{"id":"room","scripts":["missing.js"]}]}`,
		`{"rooms":[]} {}`,
	} {
		if err := os.WriteFile(path, []byte(input), 0600); err != nil {
			t.Fatal(err)
		}
		loaded, err := LoadConfig(path)
		if input == `{"rooms":[{"id":"room","scripts":["room.js"]}]}` {
			if err != nil || loaded.Rooms[0].Scripts[0] != c.Rooms[0].Scripts[0] {
				t.Fatalf("%v %v", loaded, err)
			}
		} else if err == nil {
			t.Fatalf("accepted %s", input)
		}
	}
}
