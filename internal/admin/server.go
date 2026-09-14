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

// Listen honors the chosen network interface; the CLI defaults to loopback.
func Listen(address string) (net.Listener, error) { return net.Listen("tcp", address) }

func Handler(m *launcher.Manager, token, host string) http.Handler {
	bindIP, _, _ := net.SplitHostPort(host)
	localOnly := net.ParseIP(bindIP).IsLoopback()
	files, _ := fs.Sub(assets, "web")
	static := http.FileServer(http.FS(files))
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("Referrer-Policy", "no-referrer")
		w.Header().Set("Content-Security-Policy", "default-src 'self'; script-src 'self'; style-src 'self'; connect-src 'self'; frame-ancestors 'none'; base-uri 'none'; form-action 'self'")
		// Local mode pins the listener host. Network mode accepts this server's
		// IP or DNS name, but browser requests must still be same-origin.
		if localOnly && r.Host != host {
			http.Error(w, "invalid host", http.StatusForbidden)
			return
		}
		scheme := "http"
		if r.TLS != nil {
			scheme = "https"
		}
		if origin := r.Header.Get("Origin"); origin != "" && origin != scheme+"://"+r.Host {
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
			switch r.Method {
			case "GET":
				json.NewEncoder(w).Encode(m.List())
			case "POST":
				var profile launcher.Profile
				if !decode(w, r, &profile) {
					return
				}
				id, err := m.Add(profile)
				if err != nil {
					http.Error(w, err.Error(), 400)
					return
				}
				json.NewEncoder(w).Encode(map[string]string{"id": id})
			default:
				http.Error(w, "method not allowed", 405)
			}
			return
		}
		if !strings.HasPrefix(r.URL.Path, "/api/rooms/") {
			http.NotFound(w, r)
			return
		}
		parts := strings.Split(strings.TrimPrefix(r.URL.Path, "/api/rooms/"), "/")
		if len(parts) > 2 || !m.Has(parts[0]) {
			http.NotFound(w, r)
			return
		}
		id := parts[0]
		if len(parts) == 1 {
			var err error
			switch r.Method {
			case "GET":
				profile, e := m.Profile(id)
				err = e
				if err == nil {
					json.NewEncoder(w).Encode(profile)
					return
				}
			case "PUT":
				var profile launcher.Profile
				if !decode(w, r, &profile) {
					return
				}
				err = m.Update(id, profile)
			case "DELETE":
				err = m.Delete(id)
			default:
				http.Error(w, "method not allowed", 405)
				return
			}
			if err != nil {
				http.Error(w, err.Error(), 409)
				return
			}
			json.NewEncoder(w).Encode(map[string]bool{"ok": true})
			return
		}
		if r.Method != "POST" {
			http.Error(w, "method not allowed", 405)
			return
		}
		var scripts []launcher.Script
		var input struct {
			Token string `json:"token"`
		}
		action := parts[1]
		switch action {
		case "run":
			if !decode(w, r, &scripts) {
				return
			}
			if len(scripts) == 0 {
				http.Error(w, "provide at least one script", 400)
				return
			}
			if err := launcher.ValidateScripts(scripts); err != nil {
				http.Error(w, err.Error(), 400)
				return
			}
		case "start", "restart":
			if !decode(w, r, &input) {
				return
			}
		case "stop":
		default:
			http.NotFound(w, r)
			return
		}
		if err := m.Action(id, action, input.Token, scripts); err != nil {
			http.Error(w, err.Error(), 409)
			return
		}
		json.NewEncoder(w).Encode(map[string]bool{"ok": true})
	})
}

func decode(w http.ResponseWriter, r *http.Request, value any) bool {
	if r.Header.Get("Content-Type") != "application/json" {
		http.Error(w, "expected application/json", 415)
		return false
	}
	r.Body = http.MaxBytesReader(w, r.Body, 32<<20)
	d := json.NewDecoder(r.Body)
	d.DisallowUnknownFields()
	if err := d.Decode(value); err != nil {
		http.Error(w, "invalid JSON body", 400)
		return false
	}
	if d.Decode(new(any)) != io.EOF {
		http.Error(w, "unexpected trailing data", 400)
		return false
	}
	return true
}
