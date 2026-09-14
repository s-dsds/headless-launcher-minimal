package main

import (
	"bufio"
	"bytes"
	"context"
	"crypto/rand"
	"crypto/tls"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"headless-launcher-minimal/internal/admin"
	"headless-launcher-minimal/internal/launcher"
)

var version = "dev"

func main() {
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, "wlhl:", err)
		os.Exit(1)
	}
}

func run(args []string) error {
	if len(args) == 0 || args[0] == "help" || args[0] == "--help" {
		fmt.Println(`WebLiero minimal launcher

  wlhl server [--data rooms.json] [--listen 127.0.0.1:8787]
              [--chrome-path PATH] [--show] [--tls-cert FILE --tls-key FILE]
  wlhl ls
  wlhl start ROOM   (prompts for a fresh room token)
  wlhl stop ROOM
  wlhl restart ROOM (prompts for a fresh room token)
  wlhl run ROOM FILE_OR_DIRECTORY [...]
  wlhl logs ROOM [--follow]
  wlhl version

Client commands use WLHL_URL (default http://127.0.0.1:8787) and
WLHL_ADMIN_TOKEN. Server generates an admin token if none is set.
Add and edit rooms in the web panel. Saved rooms are restored stopped.`)
		return nil
	}
	if args[0] == "version" {
		fmt.Println(version)
		return nil
	}
	if args[0] == "server" {
		return serve(args[1:])
	}
	return client(args)
}

func serve(args []string) error {
	f := flag.NewFlagSet("server", flag.ContinueOnError)
	configPath := f.String("data", "rooms.json", "saved room storage (managed by the panel)")
	listen := f.String("listen", "127.0.0.1:8787", "admin bind address; use 0.0.0.0:8787 to accept outside connections")
	chrome := f.String("chrome-path", os.Getenv("CHROME_EXECPATH"), "Chrome/Chromium executable")
	show := f.Bool("show", false, "show browser for troubleshooting")
	certFile := f.String("tls-cert", "", "PEM certificate file for direct HTTPS")
	keyFile := f.String("tls-key", "", "PEM private key file for direct HTTPS")
	if err := f.Parse(args); err != nil {
		return err
	}
	if f.NArg() != 0 {
		return errors.New("unexpected server arguments")
	}
	tlsConfig, err := loadTLSConfig(*certFile, *keyFile)
	if err != nil {
		return err
	}
	c, err := launcher.LoadConfig(*configPath)
	if err != nil {
		return err
	}
	listener, err := admin.Listen(*listen)
	if err != nil {
		return err
	}
	defer listener.Close()
	token := os.Getenv("WLHL_ADMIN_TOKEN")
	if token == "" {
		b := make([]byte, 32)
		if _, err := rand.Read(b); err != nil {
			return err
		}
		token = hex.EncodeToString(b)
	}
	if len(token) < 32 {
		return errors.New("WLHL_ADMIN_TOKEN must contain at least 32 characters")
	}
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()
	browser, err := launcher.NewBrowser(ctx, *chrome, *show)
	if err != nil {
		return err
	}
	m := launcher.NewManager(c, browser.NewPage)
	m.SetStorage(*configPath)
	defer func() { browser.Close(); m.Close() }()
	server := &http.Server{Handler: admin.Handler(m, token, listener.Addr().String()), ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 15 * time.Second, WriteTimeout: 100 * time.Second, IdleTimeout: 60 * time.Second, MaxHeaderBytes: 16 << 10, TLSConfig: tlsConfig}
	done := make(chan error, 1)
	go func() {
		if tlsConfig != nil {
			done <- server.ServeTLS(listener, "", "")
		} else {
			done <- server.Serve(listener)
		}
	}()
	panelURL := "http://" + listener.Addr().String()
	if tlsConfig != nil {
		panelURL = "https://" + listener.Addr().String()
	}
	fmt.Printf("Admin panel: %s\nAdmin token: %s\n", panelURL, token)
	if addr, ok := listener.Addr().(*net.TCPAddr); ok && addr.IP.IsUnspecified() {
		fmt.Printf("From another device, replace %s with this server’s IP or hostname.\n", addr.IP)
	}
	var serveErr error
	select {
	case <-ctx.Done():
	case <-browser.Done():
		serveErr = errors.New("browser exited; restart the launcher")
	case err := <-done:
		if !errors.Is(err, http.ErrServerClosed) {
			serveErr = err
		}
	}
	// Cancel CDP operations before draining HTTP requests.
	cancel()
	shutdown, stop := context.WithTimeout(context.Background(), 5*time.Second)
	defer stop()
	if err := server.Shutdown(shutdown); err != nil {
		server.Close()
	}
	return serveErr
}

func loadTLSConfig(certFile, keyFile string) (*tls.Config, error) {
	if certFile == "" && keyFile == "" {
		return nil, nil
	}
	if certFile == "" || keyFile == "" {
		return nil, errors.New("provide both --tls-cert and --tls-key")
	}
	certificate, err := tls.LoadX509KeyPair(certFile, keyFile)
	if err != nil {
		return nil, fmt.Errorf("load TLS certificate/key: %w", err)
	}
	return &tls.Config{MinVersion: tls.VersionTLS12, Certificates: []tls.Certificate{certificate}}, nil
}

func endpoint() (string, error) {
	address := os.Getenv("WLHL_URL")
	if address == "" {
		address = "http://127.0.0.1:8787"
	}
	u, err := url.Parse(address)
	if err != nil {
		return "", err
	}
	if (u.Scheme != "https" && u.Scheme != "http") || u.Hostname() == "" || u.User != nil || (u.Path != "" && u.Path != "/") || u.RawQuery != "" || u.Fragment != "" {
		return "", errors.New("WLHL_URL must be an HTTP or HTTPS server URL without a path")
	}
	return strings.TrimRight(address, "/"), nil
}

func request(ctx context.Context, method, path string, body []byte) ([]byte, error) {
	base, err := endpoint()
	if err != nil {
		return nil, err
	}
	token := os.Getenv("WLHL_ADMIN_TOKEN")
	if token == "" {
		return nil, errors.New("set WLHL_ADMIN_TOKEN to the admin token printed by the server")
	}
	req, err := http.NewRequestWithContext(ctx, method, base+"/api/"+path, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	// Connect directly to the explicitly selected server, without environment proxies.
	httpClient := &http.Client{Timeout: 100 * time.Second, Transport: &http.Transport{Proxy: nil}, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	defer httpClient.CloseIdleConnections()
	resp, err := httpClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	b, err := io.ReadAll(io.LimitReader(resp.Body, 100<<20))
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("%s: %s", resp.Status, strings.TrimSpace(string(b)))
	}
	return b, nil
}

func client(args []string) error {
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()
	command := args[0]
	if command == "ls" && len(args) == 1 {
		b, err := request(ctx, "GET", "rooms", nil)
		if err != nil {
			return err
		}
		fmt.Println(string(b))
		return nil
	}
	if command == "logs" && (len(args) == 2 || (len(args) == 3 && args[2] == "--follow")) {
		var last time.Time
		for {
			b, err := request(ctx, "GET", "rooms", nil)
			if err != nil {
				return err
			}
			var rooms []launcher.Snapshot
			if err := json.Unmarshal(b, &rooms); err != nil {
				return err
			}
			found := false
			for _, r := range rooms {
				if r.ID == args[1] {
					found = true
					for _, l := range r.Logs {
						if l.At.After(last) {
							fmt.Printf("%s %s\n", l.At.Format(time.RFC3339), l.Message)
							last = l.At
						}
					}
				}
			}
			if !found {
				return fmt.Errorf("unknown room %q", args[1])
			}
			if len(args) == 2 {
				return nil
			}
			select {
			case <-ctx.Done():
				return nil
			case <-time.After(time.Second):
			}
		}
	}
	if (command == "start" || command == "stop" || command == "restart") && len(args) == 2 {
		var body []byte
		if command != "stop" {
			fmt.Fprint(os.Stderr, "Paste WebLiero room token: ")
			input, err := bufio.NewReader(os.Stdin).ReadString('\n')
			if err != nil && err != io.EOF {
				return err
			}
			body, _ = json.Marshal(map[string]string{"token": strings.TrimSpace(input)})
		}
		_, err := request(ctx, "POST", "rooms/"+url.PathEscape(args[1])+"/"+command, body)
		if err == nil {
			fmt.Println("OK")
		}
		return err
	}
	if command == "run" && len(args) >= 3 {
		scripts, err := launcher.ReadScripts(args[2:])
		if err != nil {
			return err
		}
		body, err := json.Marshal(scripts)
		if err != nil {
			return err
		}
		_, err = request(ctx, "POST", "rooms/"+url.PathEscape(args[1])+"/run", body)
		if err == nil {
			fmt.Println("OK")
		}
		return err
	}
	return errors.New("invalid command or arguments; use wlhl help")
}
