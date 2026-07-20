package cli

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"

	"headless-launcher-go/internal/ipc"
	"headless-launcher-go/internal/server"
)

// Execute runs the root command.
func Execute() error {
	return rootCmd.Execute()
}

var rootCmd = &cobra.Command{
	Use:     "wlhl",
	Short:   "WebLiero Headless Launcher",
	Version: "0.2.0",
}

func init() {
	rootCmd.AddCommand(serverCmd)
	rootCmd.AddCommand(launchCmd)
	rootCmd.AddCommand(runCmd)
	rootCmd.AddCommand(lsCmd)
	rootCmd.AddCommand(stopCmd)
	rootCmd.AddCommand(followCmd)
	rootCmd.AddCommand(statsCmd)
}

// --- server ---

var serverShow bool
var serverChromePath string
var serverLogFile string
var serverLogMaxMB int
var serverLogBackups int
var serverHTTPAddr string
var serverHTTPToken string
var serverDataDir string
var serverProfilesDir string
var serverMaxRooms int
var serverLinkURL string
var serverLinkToken string
var serverLinkName string

var serverCmd = &cobra.Command{
	Use:   "server",
	Short: "Starts the headless chromium browser and waits for cli commands.",
	RunE: func(cmd *cobra.Command, args []string) error {
		token := serverHTTPToken
		if token == "" {
			token = os.Getenv("WLHL_API_TOKEN")
		}
		linkURL := serverLinkURL
		if linkURL == "" {
			linkURL = os.Getenv("WLHL_LINK_URL")
		}
		linkToken := serverLinkToken
		if linkToken == "" {
			linkToken = os.Getenv("WLHL_LINK_TOKEN")
		}
		return server.StartServer(serverShow, serverChromePath, server.LogConfig{
			Path:    serverLogFile,
			MaxMB:   serverLogMaxMB,
			Backups: serverLogBackups,
		}, server.APIConfig{
			Addr:        serverHTTPAddr,
			Token:       token,
			DataDir:     serverDataDir,
			ProfilesDir: serverProfilesDir,
			MaxRooms:    serverMaxRooms,
			LinkURL:     linkURL,
			LinkToken:   linkToken,
			LinkName:    serverLinkName,
		})
	},
}

func init() {
	serverCmd.Flags().BoolVar(&serverShow, "show", false, "Show the browser window (non headless mode)")
	serverCmd.Flags().StringVar(&serverChromePath, "chrome-path", "", "A path to a chromium executable to use")
	serverCmd.Flags().StringVar(&serverLogFile, "log-file", "", "Also write logs to this file, with built-in size rotation (portable: no systemd/NSSM needed)")
	serverCmd.Flags().IntVar(&serverLogMaxMB, "log-max-size", 50, "Rotate the log file when it reaches this many MB")
	serverCmd.Flags().IntVar(&serverLogBackups, "log-max-backups", 5, "How many rotated log files to keep")
	serverCmd.Flags().StringVar(&serverHTTPAddr, "http", "", "Enable the HTTP API on this address (e.g. 127.0.0.1:8091); expose it through a tunnel, not directly")
	serverCmd.Flags().StringVar(&serverHTTPToken, "http-token", "", "Bearer token for the HTTP API (or env WLHL_API_TOKEN)")
	serverCmd.Flags().StringVar(&serverDataDir, "data-dir", "wlhl-data", "Root directory for the local chat store")
	serverCmd.Flags().StringVar(&serverProfilesDir, "profiles-dir", "", "Directory of room profiles (subdir of *.js per profile) for API room creation; empty disables creation")
	serverCmd.Flags().IntVar(&serverMaxRooms, "max-rooms", 4, "Cap on concurrently running rooms (webliero.com allows 4 per IP)")
	serverCmd.Flags().StringVar(&serverLinkURL, "link", "", "Outbound host link to ext-proxy (e.g. wss://ext-proxy.fly.dev/hostlink) — replaces the tunnel (or env WLHL_LINK_URL)")
	serverCmd.Flags().StringVar(&serverLinkToken, "link-token", "", "Host token for the link, minted in ext-proxy /admin (or env WLHL_LINK_TOKEN)")
	serverCmd.Flags().StringVar(&serverLinkName, "link-name", "", "Display name for this host in /admin (default: hostname)")
}

// --- launch ---

var launchToken string
var launchID string
var launchScript string

var launchCmd = &cobra.Command{
	Use:   "launch [scripts...]",
	Short: "Starts a new room",
	RunE: func(cmd *cobra.Command, args []string) error {
		scripts := resolveScriptPaths(args)
		msg := ipc.LaunchMsg{
			ScriptPath:     ipc.StringOrStrings(scripts),
			Token:          launchToken,
			ID:             launchID,
			HeadlessScript: launchScript,
		}
		return connect("launch", msg, defaultHandler)
	},
}

func init() {
	launchCmd.Flags().StringVar(&launchToken, "token", "", "The headless token to use with the room")
	launchCmd.Flags().StringVar(&launchID, "id", "default", "The id to give the room")
	launchCmd.Flags().StringVar(&launchScript, "script", "", "Serve this headless-min.js instead of webliero's (e.g. one hacked by headless-modifier); omit for the vanilla client")
}

// --- run ---

var runCmd = &cobra.Command{
	Use:   "run <id> <scripts...>",
	Short: "Runs script files in an already launched room",
	Args:  cobra.MinimumNArgs(2),
	RunE: func(cmd *cobra.Command, args []string) error {
		id := args[0]
		scripts := resolveScriptPaths(args[1:])
		msg := ipc.RunScriptMsg{
			ScriptPaths: scripts,
			ID:          id,
		}
		return connect("run-script", msg, defaultHandler)
	},
}

// --- ls ---

var lsCmd = &cobra.Command{
	Use:   "ls",
	Short: "Shows the ids of the currently running rooms",
	RunE: func(cmd *cobra.Command, args []string) error {
		return connect("ls", nil, defaultHandler)
	},
}

// --- stop ---

var stopCmd = &cobra.Command{
	Use:   "stop <id>",
	Short: "Stops a room",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		msg := ipc.StopMsg{ID: args[0]}
		return connect("stop", msg, defaultHandler)
	},
}

// --- follow ---

var followCmd = &cobra.Command{
	Use:   "follow <id>",
	Short: "Follows a room, allowing you to see its logs and events in real time",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		msg := ipc.FollowMsg{ID: args[0]}
		return connect("follow", msg, defaultHandler)
	},
}

// --- stats ---

var statsCmd = &cobra.Command{
	Use:   "stats <id>",
	Short: "Shows browser stats from a room",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		msg := ipc.MetricsMsg{ID: args[0]}
		// Clear screen for animation
		fmt.Print(strings.Repeat("\n", 25))

		var lastMetrics *ipc.MetricsResultMsg
		return connect("metrics", msg, func(env ipc.Envelope) bool {
			switch env.Type {
			case "message":
				var m string
				json.Unmarshal(env.Data, &m)
				fmt.Println(m)
			case "metrics":
				var m ipc.MetricsResultMsg
				if err := json.Unmarshal(env.Data, &m); err != nil {
					return true
				}

				var scriptPct, taskPct float64
				if lastMetrics != nil {
					interval := m.Timestamp - lastMetrics.Timestamp
					if interval > 0 {
						scriptPct = 100 * (m.ScriptDuration - lastMetrics.ScriptDuration) / interval
						taskPct = 100 * (m.TaskDuration - lastMetrics.TaskDuration) / interval
					}
				}
				lastMetrics = &m

				// Move cursor to top and clear
				fmt.Print("\033[H\033[J")
				fmt.Printf("Stats for '%s':\n", m.ID)
				fmt.Printf("Heap: %s / %s\n", prettyBytes(m.JSHeapUsedSize), prettyBytes(m.JSHeapTotalSize))
				fmt.Printf("Scripts %%: %.2f\n", scriptPct)
				fmt.Printf("Task %%: %.2f\n", taskPct)
			}
			return true
		})
	},
}

func resolveScriptPaths(paths []string) []string {
	resolved := make([]string, len(paths))
	for i, p := range paths {
		abs, err := filepath.Abs(p)
		if err != nil {
			resolved[i] = p
		} else {
			resolved[i] = abs
		}
	}
	return resolved
}

func prettyBytes(b float64) string {
	units := []string{"B", "kB", "MB", "GB", "TB"}
	i := 0
	for b >= 1000 && i < len(units)-1 {
		b /= 1000
		i++
	}
	if i == 0 {
		return fmt.Sprintf("%.0f %s", b, units[i])
	}
	return fmt.Sprintf("%.2f %s", b, units[i])
}
