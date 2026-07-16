package server

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net"
	"os"
	"sync"
	"time"

	"github.com/chromedp/cdproto/fetch"
	"github.com/chromedp/cdproto/network"
	"github.com/chromedp/cdproto/performance"
	"github.com/chromedp/chromedp"
	"github.com/joho/godotenv"

	"headless-launcher-go/internal/hack"
	"headless-launcher-go/internal/ipc"
)

// Server manages Chrome and the IPC socket.
type Server struct {
	browserCtx     context.Context
	browserCancel  context.CancelFunc
	pages          sync.Map // map[string]*RoomPage
	headlessScript string

	listener net.Listener
}

// StartServer launches Chrome and starts the IPC server.
func StartServer(show bool, chromePath string) error {
	_ = godotenv.Load()

	chromeExecPath := os.Getenv("CHROME_EXECPATH")
	if chromePath != "" {
		chromeExecPath = chromePath
	}
	headlessScript := os.Getenv("HEADLESS_SCRIPT")
	if headlessScript == "" {
		headlessScript = "headless-min.js"
	}

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
	case "fetch-script":
		s.handleFetchScript(conn)
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

	headlessScript := s.headlessScript
	if msg.HeadlessScript != "" {
		headlessScript = msg.HeadlessScript
	}

	if err := rp.LoadHeadless(headlessScript, msg.Hacked); err != nil {
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

	// Enable performance domain
	if err := performance.Enable().Do(rp.ctx); err != nil {
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
			metrics, err := performance.GetMetrics().Do(rp.ctx)
			if err != nil {
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

func (s *Server) handleFetchScript(conn *ipc.Conn) {
	s.sendMessage(conn, "Fetching headless script...")

	rp, err := NewRoomPage(s.browserCtx, "__fetch__")
	if err != nil {
		s.sendMessage(conn, fmt.Sprintf("create fetch page: %v", err))
		return
	}
	defer rp.Close()

	// Intercept at Response stage to capture the script body
	var scriptBody []byte
	captured := make(chan struct{}, 1)

	if err := fetch.Enable().WithPatterns([]*fetch.RequestPattern{
		{
			URLPattern:   "*headless-min.js*",
			ResourceType: network.ResourceTypeScript,
			RequestStage: fetch.RequestStageResponse,
		},
	}).Do(rp.ctx); err != nil {
		s.sendMessage(conn, fmt.Sprintf("enable fetch: %v", err))
		return
	}

	chromedp.ListenTarget(rp.ctx, func(ev interface{}) {
		if e, ok := ev.(*fetch.EventRequestPaused); ok {
			go func() {
				s.sendMessage(conn, e.Request.URL)
				body, err := fetch.GetResponseBody(e.RequestID).Do(rp.ctx)
				if err != nil {
					log.Printf("fetch-script: get body: %v", err)
					fetch.ContinueResponse(e.RequestID).Do(rp.ctx)
					return
				}
				scriptBody = body
				fetch.ContinueResponse(e.RequestID).Do(rp.ctx)
				select {
				case captured <- struct{}{}:
				default:
				}
			}()
		}
	})

	// Navigate to trigger the script load
	if err := chromedp.Navigate("https://www.webliero.com/headless").Do(rp.ctx); err != nil {
		s.sendMessage(conn, fmt.Sprintf("navigate: %v", err))
		return
	}

	// Wait for the script to be captured
	select {
	case <-captured:
	case <-time.After(30 * time.Second):
		s.sendMessage(conn, "timeout waiting for script")
		return
	}

	if len(scriptBody) == 0 {
		s.sendMessage(conn, "failed to capture script body")
		return
	}

	// Beautify by loading js-beautify in the page and running it
	beautified := string(scriptBody)

	// Load js-beautify from CDN into the page, then beautify
	beautyJS := fmt.Sprintf(`
		(async function() {
			await new Promise((resolve, reject) => {
				var s = document.createElement('script');
				s.src = 'https://cdnjs.cloudflare.com/ajax/libs/js-beautify/1.14.7/beautify.min.js';
				s.onload = resolve;
				s.onerror = reject;
				document.head.appendChild(s);
			});
			return js_beautify(%s, {
				indent_size: 4,
				indent_char: " ",
				max_preserve_newlines: 5,
				preserve_newlines: true,
				keep_array_indentation: false,
				break_chained_methods: false,
				brace_style: "collapse",
				space_before_conditional: true,
				unescape_strings: false,
				jslint_happy: false,
				end_with_newline: false,
				wrap_line_length: 0,
				comma_first: false,
				e4x: false,
				indent_empty_lines: false
			});
		})()
	`, jsonStringLiteral(beautified))

	var prettyBody string
	if err := chromedp.Evaluate(beautyJS, &prettyBody, chromedp.EvalAsValue).Do(rp.ctx); err == nil && prettyBody != "" {
		beautified = prettyBody
	} else if err != nil {
		log.Printf("beautify failed (continuing with raw): %v", err)
	}

	// Write original backup
	ts := time.Now().Unix()
	originalName := fmt.Sprintf("headless-min-original-%d.js", ts)
	if err := os.WriteFile(originalName, []byte(beautified), 0644); err != nil {
		s.sendMessage(conn, fmt.Sprintf("write original: %v", err))
		return
	}
	s.sendMessage(conn, "original file written")

	// Hack the script
	s.sendMessage(conn, "try hacking script")
	result, err := hack.HackScript(beautified)
	if err != nil {
		s.sendMessage(conn, "error hacking script")
		s.sendMessage(conn, err.Error())
		return
	}

	matchesJSON, _ := json.Marshal(result.Matches)
	pathsJSON, _ := json.Marshal(result.Paths)
	s.sendMessage(conn, "matches "+string(matchesJSON))
	s.sendMessage(conn, "paths "+string(pathsJSON))

	if err := os.WriteFile("headless-min.js", []byte(result.Script), 0644); err != nil {
		s.sendMessage(conn, fmt.Sprintf("write hacked: %v", err))
		return
	}

	s.sendMessage(conn, "fetched")
}

func jsonStringLiteral(s string) string {
	b, _ := json.Marshal(s)
	return string(b)
}
