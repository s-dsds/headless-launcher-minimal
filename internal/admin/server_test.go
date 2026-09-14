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

func TestSelectableBind(t *testing.T) {
	for _, address := range []string{"127.0.0.1:0", "0.0.0.0:0"} {
		listener, err := Listen(address)
		if err != nil {
			t.Fatal(err)
		}
		listener.Close()
	}
	if listener, err := Listen("not-an-address"); err == nil {
		listener.Close()
		t.Fatal("accepted malformed address")
	}
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

func TestOutsideAccess(t *testing.T) {
	m := launcher.NewManager(launcher.Config{}, nil)
	h := Handler(m, "secret", "0.0.0.0:8787")
	for _, tc := range []struct {
		name, host, origin, auth string
		want                     int
	}{
		{"IP address", "192.0.2.10:8787", "http://192.0.2.10:8787", "Bearer secret", 200},
		{"DNS name", "rooms.example.com:8787", "http://rooms.example.com:8787", "Bearer secret", 200},
		{"anonymous", "rooms.example.com:8787", "http://rooms.example.com:8787", "", 401},
		{"wrong token", "rooms.example.com:8787", "http://rooms.example.com:8787", "Bearer wrong", 401},
		{"cross origin", "rooms.example.com:8787", "https://evil.example", "Bearer secret", 403},
		{"remote CLI", "192.0.2.10:8787", "", "Bearer secret", 200},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := httptest.NewRequest("GET", "http://"+tc.host+"/api/rooms", nil)
			r.Header.Set("Origin", tc.origin)
			r.Header.Set("Authorization", tc.auth)
			w := httptest.NewRecorder()
			h.ServeHTTP(w, r)
			if w.Code != tc.want {
				t.Fatalf("%d: %s", w.Code, w.Body.String())
			}
		})
	}
}

func TestDirectHTTPS(t *testing.T) {
	m := launcher.NewManager(launcher.Config{}, nil)
	server := httptest.NewTLSServer(Handler(m, "secret", "0.0.0.0:8787"))
	defer server.Close()
	for _, origin := range []string{server.URL, strings.Replace(server.URL, "https:", "http:", 1)} {
		req, err := http.NewRequest("GET", server.URL+"/api/rooms", nil)
		if err != nil {
			t.Fatal(err)
		}
		req.Header.Set("Authorization", "Bearer secret")
		req.Header.Set("Origin", origin)
		response, err := server.Client().Do(req)
		if err != nil {
			t.Fatal(err)
		}
		response.Body.Close()
		want := 200
		if origin != server.URL {
			want = 403
		}
		if response.StatusCode != want {
			t.Fatalf("got %d", response.StatusCode)
		}
	}
}
