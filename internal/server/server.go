package server

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net"
	"os"
	"sort"
	"sync"
	"time"

	"github.com/chromedp/cdproto/performance"
	"github.com/chromedp/chromedp"
	"github.com/joho/godotenv"

	"headless-launcher-go/internal/chatstore"
	"headless-launcher-go/internal/ipc"
	"headless-launcher-go/internal/logfile"
)

// Server manages Chrome and the IPC socket.
type Server struct {
	browserCtx     context.Context
	browserCancel  context.CancelFunc
	pages          sync.Map // map[string]*RoomPage
	launchMu       sync.Mutex // serializes the dup-check + cap + slot reservation in LaunchRoom
	headlessScript string

	listener net.Listener

	// HTTP API state (httpapi.go); zero-valued when the API is disabled.
	chatStore   *chatstore.Store
	profilesDir string
	maxRooms    int
	startedAt   time.Time
}

// RoomInfo is the API/ls view of a running room.
type RoomInfo struct {
	ID   string `json:"id"`
	Code string `json:"code,omitempty"`
	Link string `json:"link,omitempty"`
}

// RoomsInfo lists running rooms, sorted by id.
func (s *Server) RoomsInfo() []RoomInfo {
	var out []RoomInfo
	s.pages.Range(func(key, val any) bool {
		ri := RoomInfo{ID: key.(string)}
		if rp, ok := val.(*RoomPage); ok {
			if code := rp.Code(); code != "" {
				ri.Code = code
				ri.Link = "https://www.webliero.com/?v=20&c=" + code
			}
		}
		out = append(out, ri)
		return true
	})
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}

// GetRoom returns a running room page.
func (s *Server) GetRoom(id string) (*RoomPage, bool) {
	val, ok := s.pages.Load(id)
	if !ok {
		return nil, false
	}
	return val.(*RoomPage), true
}

// roomCount returns how many rooms are currently running.
func (s *Server) roomCount() int {
	n := 0
	s.pages.Range(func(any, any) bool { n++; return true })
	return n
}

// LaunchRoom is the shared launch core (IPC handleLaunch + HTTP API). attach,
// when non-nil, runs right after the page exists — before scripts — so callers
// can subscribe to logs early (the IPC client uses it for follow mode).
func (s *Server) LaunchRoom(id, token, headlessScript string, scripts []string, attach func(*RoomPage)) (*RoomPage, error) {
	// Reserve the slot under a lock so two concurrent launches (e.g. an HTTP
	// retry racing an IPC launch) can't both pass the dup-check / cap and both
	// spin up a Chrome tab, orphaning one. NewRoomPage is the slow part and runs
	// after we've claimed the id, so this serializes only the cheap reservation.
	s.launchMu.Lock()
	if _, loaded := s.pages.Load(id); loaded {
		s.launchMu.Unlock()
		return nil, fmt.Errorf("%q is already running, stop it first", id)
	}
	if s.maxRooms > 0 && s.roomCount() >= s.maxRooms {
		s.launchMu.Unlock()
		return nil, fmt.Errorf("room cap reached (%d): webliero.com allows at most 4 rooms per IP", s.maxRooms)
	}
	rp, err := NewRoomPage(s.browserCtx, id)
	if err != nil {
		s.launchMu.Unlock()
		return nil, fmt.Errorf("create room: %w", err)
	}
	s.pages.Store(id, rp)
	s.launchMu.Unlock()

	// Server-side logging + local chat capture
	rp.OnLog(func(m string) {
		log.Printf(`"%s": %s`, id, m)
	})
	if s.chatStore != nil {
		rp.SetChatSink(func(payload string) {
			if err := s.chatStore.Append(id, payload); err != nil {
				log.Printf(`"%s": chatstore: %v`, id, err)
			}
		})
	}
	if attach != nil {
		attach(rp)
	}

	if headlessScript == "" {
		headlessScript = s.headlessScript
	}
	fail := func(err error) (*RoomPage, error) {
		s.pages.Delete(id)
		rp.Close()
		return nil, err
	}
	if err := rp.LoadHeadless(headlessScript); err != nil {
		return fail(fmt.Errorf("load headless: %w", err))
	}
	if err := rp.SetToken(token); err != nil {
		return fail(fmt.Errorf("set token: %w", err))
	}
	for _, script := range scripts {
		if err := rp.RunScriptPath(script); err != nil {
			return fail(fmt.Errorf("run script %s: %w", script, err))
		}
	}
	return rp, nil
}

// StopRoom stops a running room.
func (s *Server) StopRoom(id string) error {
	val, ok := s.pages.LoadAndDelete(id)
	if !ok {
		return fmt.Errorf("%q doesn't exist", id)
	}
	val.(*RoomPage).Close()
	return nil
}

// LogConfig configures the server's built-in rotating log file. Path "" means
// stdout/stderr only (the pre-existing behaviour).
type LogConfig struct {
	Path    string
	MaxMB   int
	Backups int
}

// APIConfig configures the optional HTTP API (httpapi.go). Addr "" = disabled.
type APIConfig struct {
	Addr        string // listen address, e.g. 127.0.0.1:8091
	Token       string // bearer token; required when Addr is set
	DataDir     string // chat store root
	ProfilesDir string // room profiles for API-driven creation ("" = creation disabled)
	MaxRooms    int    // concurrent room cap (webliero.com allows 4 per IP)
}

// StartServer launches Chrome and starts the IPC server.
func StartServer(show bool, chromePath string, logCfg LogConfig, apiCfg APIConfig) error {
	_ = godotenv.Load()

	// Built-in rotating log file: the portable answer for durable logs on
	// platforms without systemd/journald (Windows, mac). Tee to stderr as well
	// so supervisors (systemd/NSSM) and interactive runs still see output.
	if logCfg.Path != "" {
		lw, err := logfile.New(logCfg.Path, logCfg.MaxMB, logCfg.Backups)
		if err != nil {
			return fmt.Errorf("log file: %w", err)
		}
		defer lw.Close()
		log.SetOutput(io.MultiWriter(os.Stderr, lw))
		log.Println("logging to", logCfg.Path,
			fmt.Sprintf("(rotate at %dMB, keep %d)", logCfg.MaxMB, logCfg.Backups))
	}

	chromeExecPath := os.Getenv("CHROME_EXECPATH")
	if chromePath != "" {
		chromeExecPath = chromePath
	}
	// Optional server-wide default script to serve when a launch doesn't pass
	// --script. Empty = vanilla webliero client (the default; hacking is opt-in
	// via --script <file-from-headless-modifier>).
	headlessScript := os.Getenv("HEADLESS_SCRIPT")

	log.Println("Starting chromium...", chromeExecPath)

	opts := append(chromedp.DefaultExecAllocatorOptions[:],
		chromedp.Flag("disable-background-networking", true),
		chromedp.Flag("disable-background-timer-throttling", true),
		chromedp.Flag("disable-client-side-phishing-detection", true),
		chromedp.Flag("disable-extensions", true),
		chromedp.Flag("disable-sync", true),
		chromedp.Flag("disable-translate", true),
		chromedp.Flag("allow-running-insecure-content", true),
		chromedp.Flag("disable-web-security", true),
		chromedp.Flag("no-sandbox", true),
		chromedp.Flag("no-first-run", true),
		chromedp.Flag("disable-features", "WebRtcHideLocalIpsWithMdns"),
	)
	if chromeExecPath != "" {
		opts = append(opts, chromedp.ExecPath(chromeExecPath))
	}
	if show {
		opts = append(opts, chromedp.Flag("headless", false))
	}

	allocCtx, allocCancel := chromedp.NewExecAllocator(context.Background(), opts...)
	browserCtx, browserCancel := chromedp.NewContext(allocCtx)

	// Force Chrome to start
	if err := chromedp.Run(browserCtx); err != nil {
		allocCancel()
		browserCancel()
		return fmt.Errorf("start chrome: %w", err)
	}
	log.Println("Chromium up")

	srv := &Server{
		browserCtx:     browserCtx,
		browserCancel:  browserCancel,
		headlessScript: headlessScript,
		profilesDir:    apiCfg.ProfilesDir,
		maxRooms:       apiCfg.MaxRooms,
		startedAt:      time.Now(),
	}

	// Optional HTTP API: local chat store + bounded log tails + room lifecycle
	// for the admin panel (reached through an operator-managed tunnel).
	if apiCfg.Addr != "" {
		if apiCfg.Token == "" {
			allocCancel()
			browserCancel()
			return fmt.Errorf("--http requires --http-token (or WLHL_API_TOKEN)")
		}
		if apiCfg.DataDir != "" {
			srv.chatStore = chatstore.New(apiCfg.DataDir)
		}
		if err := srv.startHTTP(apiCfg); err != nil {
			allocCancel()
			browserCancel()
			return err
		}
	}

	socketPath := ipc.SocketPath()
	// Remove stale socket
	os.Remove(socketPath)

	ln, err := net.Listen("unix", socketPath)
	if err != nil {
		allocCancel()
		browserCancel()
		return fmt.Errorf("listen %s: %w", socketPath, err)
	}
	srv.listener = ln
	log.Println("IPC server listening on", socketPath)

	// Accept loop
	for {
		conn, err := ln.Accept()
		if err != nil {
			log.Println("accept error:", err)
			break
		}
		go srv.handleConnection(ipc.NewConn(conn))
	}

	allocCancel()
	browserCancel()
	return nil
}

func (s *Server) handleConnection(conn *ipc.Conn) {
	defer conn.Close()

	env, err := conn.Receive()
	if err != nil {
		log.Println("receive error:", err)
		return
	}

	switch env.Type {
	case "launch":
		s.handleLaunch(conn, env.Data)
	case "run-script":
		s.handleRunScript(conn, env.Data)
	case "ls":
		s.handleLs(conn)
	case "stop":
		s.handleStop(conn, env.Data)
	case "follow":
		s.handleFollow(conn, env.Data)
	case "metrics":
		s.handleMetrics(conn, env.Data)
	default:
		log.Println("unknown message type:", env.Type)
	}
}

func (s *Server) sendMessage(conn *ipc.Conn, msg string) {
	if err := conn.Send("message", msg); err != nil {
		log.Println("send error:", err)
	}
}

func (s *Server) followPage(conn *ipc.Conn, rp *RoomPage) func() {
	logFn := func(msg string) {
		s.sendMessage(conn, msg)
	}
	rp.OnLog(logFn)

	closeCh := make(chan struct{}, 1)
	go func() {
		// Detect socket close by reading (will get EOF)
		buf := make([]byte, 1)
		for {
			_, err := conn.NetConn().Read(buf)
			if err != nil {
				select {
				case closeCh <- struct{}{}:
				default:
				}
				return
			}
		}
	}()

	stop := func() {
		rp.OffLog(logFn)
	}

	return stop
}

func (s *Server) handleLaunch(conn *ipc.Conn, data json.RawMessage) {
	var msg ipc.LaunchMsg
	if err := json.Unmarshal(data, &msg); err != nil {
		s.sendMessage(conn, fmt.Sprintf("invalid launch message: %v", err))
		return
	}

	s.sendMessage(conn, fmt.Sprintf(`Launching room id="%s" token="%s"`, msg.ID, msg.Token))

	// The headless script (msg.HeadlessScript / --script) serves a local
	// (e.g. hacked) client; empty = webliero's vanilla one. A failed launch now
	// tears the page down instead of leaving a half-initialized zombie room.
	_, err := s.LaunchRoom(msg.ID, msg.Token, msg.HeadlessScript, msg.ScriptPath, func(rp *RoomPage) {
		s.followPage(conn, rp) // stream logs to this client from the first line
	})
	if err != nil {
		s.sendMessage(conn, err.Error())
		return
	}
	// Keep connection open (follow mode) - client disconnects when done
}

func (s *Server) handleRunScript(conn *ipc.Conn, data json.RawMessage) {
	var msg ipc.RunScriptMsg
	if err := json.Unmarshal(data, &msg); err != nil {
		s.sendMessage(conn, fmt.Sprintf("invalid run-script message: %v", err))
		return
	}

	val, ok := s.pages.Load(msg.ID)
	if !ok {
		s.sendMessage(conn, fmt.Sprintf(`"%s" doesn't exist`, msg.ID))
		return
	}
	rp := val.(*RoomPage)

	stop := s.followPage(conn, rp)
	defer func() {
		// Wait 50ms for console events before disconnecting
		time.Sleep(50 * time.Millisecond)
		stop()
	}()

	for _, script := range msg.ScriptPaths {
		if err := rp.RunScriptPath(script); err != nil {
			log.Println("run-script error:", err)
			break
		}
	}
}

func (s *Server) handleLs(conn *ipc.Conn) {
	log.Println("ls")
	s.pages.Range(func(key, val any) bool {
		id := key.(string)
		line := id
		if rp, ok := val.(*RoomPage); ok {
			if code := rp.Code(); code != "" {
				line = fmt.Sprintf("%s\t%s\thttps://www.webliero.com/?v=20&c=%s", id, code, code)
			} else {
				line = fmt.Sprintf("%s\t(not registered)", id)
			}
		}
		s.sendMessage(conn, line)
		return true
	})
}

func (s *Server) handleStop(conn *ipc.Conn, data json.RawMessage) {
	var msg ipc.StopMsg
	if err := json.Unmarshal(data, &msg); err != nil {
		s.sendMessage(conn, fmt.Sprintf("invalid stop message: %v", err))
		return
	}

	if err := s.StopRoom(msg.ID); err != nil {
		s.sendMessage(conn, err.Error())
		return
	}
	s.sendMessage(conn, fmt.Sprintf(`"%s" closed`, msg.ID))
}

func (s *Server) handleFollow(conn *ipc.Conn, data json.RawMessage) {
	var msg ipc.FollowMsg
	if err := json.Unmarshal(data, &msg); err != nil {
		s.sendMessage(conn, fmt.Sprintf("invalid follow message: %v", err))
		return
	}

	val, ok := s.pages.Load(msg.ID)
	if !ok {
		s.sendMessage(conn, fmt.Sprintf(`"%s" doesn't exist`, msg.ID))
		return
	}
	rp := val.(*RoomPage)

	s.sendMessage(conn, fmt.Sprintf(`Following room id="%s"`, msg.ID))
	s.followPage(conn, rp)

	// Block until client disconnects
	buf := make([]byte, 1)
	for {
		if _, err := conn.NetConn().Read(buf); err != nil {
			return
		}
	}
}

func (s *Server) handleMetrics(conn *ipc.Conn, data json.RawMessage) {
	var msg ipc.MetricsMsg
	if err := json.Unmarshal(data, &msg); err != nil {
		s.sendMessage(conn, fmt.Sprintf("invalid metrics message: %v", err))
		return
	}

	val, ok := s.pages.Load(msg.ID)
	if !ok {
		s.sendMessage(conn, fmt.Sprintf(`"%s" doesn't exist`, msg.ID))
		return
	}
	rp := val.(*RoomPage)

	// Enable performance domain. Must go through chromedp.Run so the CDP
	// executor is present in the context — calling Do(rp.ctx) directly fails
	// with "invalid context" (the executor is only injected by Run).
	if err := chromedp.Run(rp.ctx, performance.Enable()); err != nil {
		s.sendMessage(conn, fmt.Sprintf("enable performance: %v", err))
		return
	}

	done := make(chan struct{})
	go func() {
		buf := make([]byte, 1)
		for {
			if _, err := conn.NetConn().Read(buf); err != nil {
				close(done)
				return
			}
		}
	}()

	ticker := time.NewTicker(1 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-done:
			return
		case <-ticker.C:
			var metrics []*performance.Metric
			if err := chromedp.Run(rp.ctx, chromedp.ActionFunc(func(ctx context.Context) error {
				var e error
				metrics, e = performance.GetMetrics().Do(ctx)
				return e
			})); err != nil {
				return
			}

			result := ipc.MetricsResultMsg{ID: msg.ID}
			for _, m := range metrics {
				switch m.Name {
				case "Timestamp":
					result.Timestamp = m.Value
				case "ScriptDuration":
					result.ScriptDuration = m.Value
				case "TaskDuration":
					result.TaskDuration = m.Value
				case "JSHeapUsedSize":
					result.JSHeapUsedSize = m.Value
				case "JSHeapTotalSize":
					result.JSHeapTotalSize = m.Value
				}
			}

			if err := conn.Send("metrics", result); err != nil {
				return
			}
		}
	}
}
