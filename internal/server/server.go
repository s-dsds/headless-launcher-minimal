package server

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net"
	"os"
	"sync"
	"time"

	"github.com/chromedp/cdproto/performance"
	"github.com/chromedp/chromedp"
	"github.com/joho/godotenv"

	"headless-launcher-go/internal/ipc"
	"headless-launcher-go/internal/logfile"
)

// Server manages Chrome and the IPC socket.
type Server struct {
	browserCtx     context.Context
	browserCancel  context.CancelFunc
	pages          sync.Map // map[string]*RoomPage
	headlessScript string

	listener net.Listener
}

// LogConfig configures the server's built-in rotating log file. Path "" means
// stdout/stderr only (the pre-existing behaviour).
type LogConfig struct {
	Path    string
	MaxMB   int
	Backups int
}

// StartServer launches Chrome and starts the IPC server.
func StartServer(show bool, chromePath string, logCfg LogConfig) error {
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

	scripts := []string(msg.ScriptPath)
	id := msg.ID

	if _, loaded := s.pages.Load(id); loaded {
		s.sendMessage(conn, fmt.Sprintf(`"%s" is already running, you must stop it first.`, id))
		return
	}

	s.sendMessage(conn, fmt.Sprintf(`Launching room id="%s" token="%s"`, id, msg.Token))

	rp, err := NewRoomPage(s.browserCtx, id)
	if err != nil {
		s.sendMessage(conn, fmt.Sprintf("create room: %v", err))
		return
	}
	s.pages.Store(id, rp)

	// Server-side logging
	rp.OnLog(func(m string) {
		log.Printf(`"%s": %s`, id, m)
	})

	// Follow page logs to this client
	s.followPage(conn, rp)

	// Serve a local script (e.g. one hacked by headless-modifier) only when
	// asked via --script or the HEADLESS_SCRIPT env default; otherwise the room
	// runs webliero's vanilla client.
	headlessScript := msg.HeadlessScript
	if headlessScript == "" {
		headlessScript = s.headlessScript
	}

	if err := rp.LoadHeadless(headlessScript); err != nil {
		s.sendMessage(conn, fmt.Sprintf("load headless: %v", err))
		return
	}

	if err := rp.SetToken(msg.Token); err != nil {
		s.sendMessage(conn, fmt.Sprintf("set token: %v", err))
		return
	}

	for _, script := range scripts {
		if err := rp.RunScriptPath(script); err != nil {
			s.sendMessage(conn, fmt.Sprintf("run script: %v", err))
			return
		}
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

	val, ok := s.pages.LoadAndDelete(msg.ID)
	if !ok {
		s.sendMessage(conn, fmt.Sprintf(`"%s" doesn't exist`, msg.ID))
		return
	}
	rp := val.(*RoomPage)
	rp.Close()
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
