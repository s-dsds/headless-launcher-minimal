package admin

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"headless-launcher-minimal/internal/launcher"
)

func TestPrivateAPI(t *testing.T) {
	m := launcher.NewManager(launcher.Config{Rooms: []launcher.Profile{{ID: "test"}}}, nil)
	h := Handler(m, strings.Repeat("x", 32), "127.0.0.1:8787")
	for _, tc := range []struct {
		name, host, origin, auth, method, path string
		want                                   int
	}{
		{"anonymous", "127.0.0.1:8787", "", "", "GET", "/api/rooms", 401},
		{"bad token", "127.0.0.1:8787", "", "Bearer wrong", "GET", "/api/rooms", 401},
		{"rebinding", "evil.example:8787", "", "Bearer " + strings.Repeat("x", 32), "GET", "/api/rooms", 403},
		{"cross origin", "127.0.0.1:8787", "https://evil.example", "Bearer " + strings.Repeat("x", 32), "POST", "/api/rooms/test/stop", 403},
		{"authenticated", "127.0.0.1:8787", "http://127.0.0.1:8787", "Bearer " + strings.Repeat("x", 32), "GET", "/api/rooms", 200},
		{"read cannot mutate", "127.0.0.1:8787", "", "Bearer " + strings.Repeat("x", 32), "GET", "/api/rooms/test/stop", 405},
		{"stop", "127.0.0.1:8787", "", "Bearer " + strings.Repeat("x", 32), "POST", "/api/rooms/test/stop", 200},
		{"panel", "127.0.0.1:8787", "", "", "GET", "/", 200},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := httptest.NewRequest(tc.method, "http://"+tc.host+tc.path, nil)
			r.Header.Set("Origin", tc.origin)
			r.Header.Set("Authorization", tc.auth)
			w := httptest.NewRecorder()
			h.ServeHTTP(w, r)
			if w.Code != tc.want {
				t.Fatalf("got %d: %s", w.Code, w.Body.String())
			}
			if w.Header().Get("Content-Security-Policy") == "" {
				t.Fatal("missing CSP")
			}
			if strings.Contains(w.Body.String(), strings.Repeat("x", 32)) {
				t.Fatal("token disclosed")
			}
		})
	}
}

func TestLoopbackOnly(t *testing.T) {
	for _, address := range []string{"0.0.0.0:0", "[::]:0", "localhost:0", "192.0.2.1:0"} {
		if l, err := Listen(address); err == nil {
			l.Close()
			t.Fatalf("accepted %s", address)
		}
	}
	l, err := Listen("127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	l.Close()
}

func TestRunBodyValidation(t *testing.T) {
	m := launcher.NewManager(launcher.Config{Rooms: []launcher.Profile{{ID: "test"}}}, nil)
	h := Handler(m, "secret", "127.0.0.1:8787")
	for _, body := range []string{`[]`, `[{"source":"1","unknown":true}]`, `[{"source":"1"}] {}`, `null`} {
		r := httptest.NewRequest(http.MethodPost, "http://127.0.0.1:8787/api/rooms/test/run", strings.NewReader(body))
		r.Header.Set("Authorization", "Bearer secret")
		r.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		if w.Code != 400 {
			t.Fatalf("%s: %d", body, w.Code)
		}
	}
}
