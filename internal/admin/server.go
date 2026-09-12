package admin

import (
	"crypto/subtle"
	"embed"
	"encoding/json"
	"io"
	"io/fs"
	"net"
	"net/http"
	"strings"

	"headless-launcher-minimal/internal/launcher"
)

//go:embed web/*
var assets embed.FS

// Listen only accepts numeric loopback addresses; a separate port alone is not privacy.
func Listen(address string) (net.Listener, error) {
	host, _, err := net.SplitHostPort(address)
	if err != nil {
		return nil, err
	}
	ip := net.ParseIP(host)
	if ip == nil || !ip.IsLoopback() {
		return nil, &net.AddrError{Err: "admin address must be a numeric loopback address (127.0.0.1 or ::1)", Addr: address}
	}
	return net.Listen("tcp", address)
}

func Handler(m *launcher.Manager, token, host string) http.Handler {
	files, _ := fs.Sub(assets, "web")
	static := http.FileServer(http.FS(files))
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("Referrer-Policy", "no-referrer")
		w.Header().Set("Content-Security-Policy", "default-src 'self'; script-src 'self'; style-src 'self'; connect-src 'self'; frame-ancestors 'none'; base-uri 'none'; form-action 'self'")
		// Pin Host to the actual listener address to reject DNS rebinding.
		if r.Host != host {
			http.Error(w, "invalid host", http.StatusForbidden)
			return
		}
		if origin := r.Header.Get("Origin"); origin != "" && origin != "http://"+host {
			http.Error(w, "invalid origin", http.StatusForbidden)
			return
		}
		if !strings.HasPrefix(r.URL.Path, "/api/") {
			if r.Method != "GET" && r.Method != "HEAD" {
				http.Error(w, "method not allowed", 405)
				return
			}
			static.ServeHTTP(w, r)
			return
		}
		if token == "" || subtle.ConstantTimeCompare([]byte(r.Header.Get("Authorization")), []byte("Bearer "+token)) != 1 {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Path == "/api/rooms" {
			if r.Method != "GET" {
				http.Error(w, "method not allowed", 405)
				return
			}
			json.NewEncoder(w).Encode(m.List())
			return
		}
		parts := strings.Split(strings.TrimPrefix(r.URL.Path, "/api/rooms/"), "/")
		if !strings.HasPrefix(r.URL.Path, "/api/rooms/") || len(parts) != 2 || !m.Has(parts[0]) {
			http.NotFound(w, r)
			return
		}
		if r.Method != "POST" {
			http.Error(w, "method not allowed", 405)
			return
		}
		action := parts[1]
		if action != "start" && action != "stop" && action != "restart" && action != "run" {
			http.NotFound(w, r)
			return
		}
		var scripts []launcher.Script
		if action == "run" {
			if r.Header.Get("Content-Type") != "application/json" {
				http.Error(w, "expected application/json", 415)
				return
			}
			r.Body = http.MaxBytesReader(w, r.Body, 8<<20) // JSON escaping adds overhead.
			d := json.NewDecoder(r.Body)
			d.DisallowUnknownFields()
			if err := d.Decode(&scripts); err != nil {
				http.Error(w, "invalid scripts", 400)
				return
			}
			if d.Decode(new(any)) != io.EOF {
				http.Error(w, "invalid trailing data", 400)
				return
			}
			total := 0
			for _, s := range scripts {
				total += len(s.Source)
			}
			if len(scripts) == 0 || len(scripts) > 100 || total > launcher.MaxScriptBytes {
				http.Error(w, "provide 1–100 scripts, at most 4 MiB total", 400)
				return
			}
		}
		if err := m.Action(parts[0], action, scripts); err != nil {
			http.Error(w, err.Error(), http.StatusConflict)
			return
		}
		json.NewEncoder(w).Encode(map[string]bool{"ok": true})
	})
}
