package server

// HTTP API for the room-admin panel (see _specs/http-api.md): bounded log
// tails, the local chat store, and room lifecycle (list/create/stop). ext-proxy
// reaches this through an operator-managed tunnel; every request carries the
// bearer token. Bind 127.0.0.1 unless you know why you need otherwise.

import (
	"crypto/subtle"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
)

var apiRoomIDRe = regexp.MustCompile(`^[a-z0-9_-]{1,64}$`)
var profileNameRe = regexp.MustCompile(`^[A-Za-z0-9_-]{1,64}$`)

func (s *Server) startHTTP(cfg APIConfig) error {
	mux := http.NewServeMux()

	mux.HandleFunc("GET /api/health", func(w http.ResponseWriter, r *http.Request) {
		writeAPI(w, map[string]any{
			"ok":     true,
			"rooms":  s.roomCount(),
			"uptime": int(time.Since(s.startedAt).Seconds()),
		})
	})

	mux.HandleFunc("GET /api/rooms", func(w http.ResponseWriter, r *http.Request) {
		rooms := s.RoomsInfo()
		if rooms == nil {
			rooms = []RoomInfo{}
		}
		writeAPI(w, rooms)
	})

	mux.HandleFunc("POST /api/rooms", s.apiCreateRoom)

	mux.HandleFunc("DELETE /api/rooms/{id}", func(w http.ResponseWriter, r *http.Request) {
		id := r.PathValue("id")
		if err := s.StopRoom(id); err != nil {
			apiError(w, http.StatusNotFound, err.Error())
			return
		}
		log.Printf("[api] room %q stopped", id)
		writeAPI(w, map[string]any{"ok": true})
	})

	mux.HandleFunc("GET /api/rooms/{id}/logs", func(w http.ResponseWriter, r *http.Request) {
		rp, ok := s.GetRoom(r.PathValue("id"))
		if !ok {
			apiError(w, http.StatusNotFound, "room not running")
			return
		}
		// tail is double-bounded: the ring keeps 2000 lines, we serve ≤1000 —
		// an error loop can never turn this into a huge response.
		tail := clampInt(r.URL.Query().Get("tail"), 200, 1, 1000)
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		for _, l := range rp.TailLogs(tail) {
			fmt.Fprintf(w, "%s %s\n", l.At.Format("2006-01-02 15:04:05"), l.Msg)
		}
	})

	mux.HandleFunc("GET /api/rooms/{id}/chat/dates", func(w http.ResponseWriter, r *http.Request) {
		if s.chatStore == nil {
			apiError(w, http.StatusServiceUnavailable, "chat store disabled")
			return
		}
		days, err := s.chatStore.Dates(r.PathValue("id"))
		if err != nil {
			apiError(w, http.StatusBadRequest, err.Error())
			return
		}
		writeAPI(w, days)
	})

	mux.HandleFunc("GET /api/rooms/{id}/chat", func(w http.ResponseWriter, r *http.Request) {
		if s.chatStore == nil {
			apiError(w, http.StatusServiceUnavailable, "chat store disabled")
			return
		}
		q := r.URL.Query()
		date := q.Get("date")
		if date == "" {
			date = time.Now().Format("20060102")
		}
		limit := clampInt(q.Get("limit"), 200, 1, 500)
		before, _ := strconv.ParseInt(q.Get("before"), 10, 64)
		msgs, err := s.chatStore.Query(r.PathValue("id"), date, limit, before)
		if err != nil {
			apiError(w, http.StatusBadRequest, err.Error())
			return
		}
		if msgs == nil {
			msgs = []json.RawMessage{}
		}
		writeAPI(w, map[string]any{"date": date, "messages": msgs})
	})

	handler := requireBearer(cfg.Token, mux)
	httpSrv := &http.Server{
		Addr:              cfg.Addr,
		Handler:           handler,
		ReadHeaderTimeout: 10 * time.Second,
	}
	go func() {
		log.Println("HTTP API listening on", cfg.Addr)
		if err := httpSrv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Println("http api:", err)
		}
	}()
	return nil
}

// apiCreateRoom launches a room from a named profile. No code crosses the API:
// callers choose a profile (a directory of scripts configured server-side) and
// a CONFIG object — arbitrary script injection stays impossible.
func (s *Server) apiCreateRoom(w http.ResponseWriter, r *http.Request) {
	if s.profilesDir == "" {
		apiError(w, http.StatusServiceUnavailable, "room creation disabled (no --profiles-dir)")
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, 64*1024)
	var body struct {
		ID      string         `json:"id"`
		Token   string         `json:"token"`
		Profile string         `json:"profile"`
		Conf    map[string]any `json:"conf"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		apiError(w, http.StatusBadRequest, "invalid JSON")
		return
	}
	if !apiRoomIDRe.MatchString(body.ID) {
		apiError(w, http.StatusBadRequest, "invalid room id (want "+apiRoomIDRe.String()+")")
		return
	}
	if body.Token == "" {
		apiError(w, http.StatusBadRequest, "headless token required")
		return
	}
	if !profileNameRe.MatchString(body.Profile) {
		apiError(w, http.StatusBadRequest, "invalid profile name")
		return
	}

	scripts, err := s.profileScripts(body.Profile, body.Conf, body.ID)
	if err != nil {
		apiError(w, http.StatusBadRequest, err.Error())
		return
	}

	if _, err := s.LaunchRoom(body.ID, body.Token, "", scripts, nil); err != nil {
		apiError(w, http.StatusConflict, err.Error())
		return
	}
	log.Printf("[api] room %q launched (profile %q)", body.ID, body.Profile)

	// The room registers with webliero asynchronously; poll briefly so the
	// caller usually gets the join code in this same response.
	var info RoomInfo
	for i := 0; i < 20; i++ {
		time.Sleep(500 * time.Millisecond)
		if rp, ok := s.GetRoom(body.ID); ok {
			if code := rp.Code(); code != "" {
				info = RoomInfo{ID: body.ID, Code: code, Link: "https://www.webliero.com/?v=20&c=" + code}
				break
			}
		} else {
			break // stopped meanwhile
		}
	}
	if info.ID == "" {
		info = RoomInfo{ID: body.ID} // still registering — poll GET /api/rooms
	}
	writeAPI(w, info)
}

// profileScripts resolves a profile directory into an ordered script list.
// *.js run in alphabetical order (the fork's `_`/`z_` prefix convention).
//
// CONFIG assembly: an optional `_conf.defaults.json` in the profile provides
// the base (host-local settings like the firebase web-SDK block — they live
// HERE, next to the scripts that need them, so callers such as ext-proxy never
// have to know them). The API's conf object is overlaid on top (caller wins,
// per top-level key). The merged object is materialized as
// `const CONFIG = {...}` injected first, replacing any _conf.js.
func (s *Server) profileScripts(profile string, conf map[string]any, roomID string) ([]string, error) {
	dir := filepath.Join(s.profilesDir, profile)
	ents, err := os.ReadDir(dir)
	if err != nil {
		return nil, fmt.Errorf("unknown profile %q", profile)
	}

	merged := map[string]any{}
	if b, err := os.ReadFile(filepath.Join(dir, "_conf.defaults.json")); err == nil {
		if err := json.Unmarshal(b, &merged); err != nil {
			return nil, fmt.Errorf("profile %q _conf.defaults.json: %w", profile, err)
		}
	}
	for k, v := range conf {
		merged[k] = v
	}
	genConf := len(merged) > 0

	var files []string
	for _, e := range ents {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".js") {
			continue
		}
		if genConf && e.Name() == "_conf.js" {
			continue // replaced by the generated conf below
		}
		files = append(files, filepath.Join(dir, e.Name()))
	}
	if len(files) == 0 {
		return nil, fmt.Errorf("profile %q has no scripts", profile)
	}
	sort.Strings(files)

	if genConf {
		cj, err := json.MarshalIndent(merged, "", "  ")
		if err != nil {
			return nil, fmt.Errorf("conf: %w", err)
		}
		f, err := os.CreateTemp("", "wlhl-conf-"+roomID+"-*.js")
		if err != nil {
			return nil, err
		}
		if _, err := fmt.Fprintf(f, "const CONFIG = %s;\n", cj); err != nil {
			f.Close()
			return nil, err
		}
		f.Close()
		files = append([]string{f.Name()}, files...)
	}
	return files, nil
}

// --- plumbing ---

func requireBearer(token string, next http.Handler) http.Handler {
	want := []byte("Bearer " + token)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got := []byte(r.Header.Get("Authorization"))
		if len(got) != len(want) || subtle.ConstantTimeCompare(got, want) != 1 {
			apiError(w, http.StatusUnauthorized, "unauthorized")
			return
		}
		next.ServeHTTP(w, r)
	})
}

func writeAPI(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(v)
}

func apiError(w http.ResponseWriter, status int, msg string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(map[string]string{"error": msg})
}

func clampInt(s string, def, min, max int) int {
	n, err := strconv.Atoi(s)
	if err != nil || n < min {
		if s == "" {
			return def
		}
		return min
	}
	if n > max {
		return max
	}
	return n
}
