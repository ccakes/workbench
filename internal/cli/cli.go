package cli

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/ccakes/workbench/internal/api"
	"github.com/ccakes/workbench/internal/collector"
	"github.com/ccakes/workbench/internal/config"
	"github.com/ccakes/workbench/internal/events"
	"github.com/ccakes/workbench/internal/runner"
	"github.com/ccakes/workbench/internal/spanbuf"
	"github.com/ccakes/workbench/internal/supervisor"
	"github.com/ccakes/workbench/internal/tui"
	"github.com/ccakes/workbench/internal/watcher"
)

var Version = "dev"

func Run() int {
	if len(os.Args) < 2 {
		return runUp(os.Args[1:])
	}

	switch os.Args[1] {
	case "up":
		return runUp(os.Args[2:])
	case "start":
		return runStart(os.Args[2:])
	case "stop":
		return runStop(os.Args[2:])
	case "restart":
		return runRestart(os.Args[2:])
	case "status":
		return runStatus(os.Args[2:])
	case "logs":
		return runLogs(os.Args[2:])
	case "validate":
		return runValidate(os.Args[2:])
	case "version":
		fmt.Printf("bench %s\n", Version)
		return 0
	case "help", "-h", "--help":
		printUsage()
		return 0
	default:
		fmt.Fprintf(os.Stderr, "unknown command: %s\n\n", os.Args[1])
		printUsage()
		return 1
	}
}

func printUsage() {
	fmt.Fprintf(os.Stderr, `workbench - YAML-native TUI for running and supervising local development services

Usage:
  bench [command] [flags]

Commands:
  up          Start services and open TUI (default)
  start       Start specific services
  stop        Stop specific services
  restart     Restart specific services
  status      Show service status
  logs        Show service logs
  validate    Validate configuration
  version     Show version

Global Flags:
  --config <path>    Path to config file (default: bench.yml)
  --socket <path>    Control socket path (default: auto from config)
  --verbose          Verbose output

Run 'bench <command> --help' for more information on a command.
`)
}

// resolveConfigPath returns the absolute path to the config file.
func resolveConfigPath(configPath string) (string, error) {
	path := configPath
	if path == "" {
		var err error
		path, err = config.FindConfig()
		if err != nil {
			return "", err
		}
	}
	return filepath.Abs(path)
}

func loadConfig(configPath string) (*config.Config, error) {
	path, err := resolveConfigPath(configPath)
	if err != nil {
		return nil, err
	}
	return config.Load(path)
}

// connectToRunning attempts to connect to a running bench instance.
// If socketOverride or BENCH_SOCKET is set, config resolution is skipped.
func connectToRunning(configPath, socketOverride string) (*api.Client, error) {
	var sockPath string

	// Direct socket override doesn't need config
	if socketOverride != "" {
		sockPath = socketOverride
	} else if envSock := os.Getenv("BENCH_SOCKET"); envSock != "" {
		sockPath = envSock
	} else {
		// Need config to derive socket path
		resolved, err := resolveConfigPath(configPath)
		if err != nil {
			return nil, err
		}
		sockPath, err = api.SocketPath(resolved)
		if err != nil {
			return nil, err
		}
	}

	client := api.NewClient(sockPath)
	if err := client.Ping(); err != nil {
		return nil, fmt.Errorf("no running bench instance found: %w", err)
	}
	return client, nil
}

func runUp(args []string) int {
	fs := flag.NewFlagSet("up", flag.ExitOnError)
	configPath := fs.String("config", "", "path to config file")
	socketPath := fs.String("socket", "", "control socket path (default: auto)")
	noTUI := fs.Bool("no-tui", false, "disable TUI, run in foreground")
	noWatch := fs.Bool("no-watch", false, "disable file watching")
	verbose := fs.Bool("verbose", false, "verbose output")
	_ = fs.Parse(args)

	cfg, err := loadConfig(*configPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		return 1
	}

	if err := cfg.Validate(); err != nil {
		fmt.Fprintf(os.Stderr, "config validation failed:\n%v\n", err)
		return 1
	}

	// Check Docker availability if any container services exist
	for _, svc := range cfg.Services {
		if svc.IsContainer() {
			if err := runner.CheckDocker(); err != nil {
				fmt.Fprintf(os.Stderr, "error: %v\n", err)
				return 1
			}
			break
		}
	}

	// Claim the control socket before starting services.
	// This ensures only one bench instance owns a given config.
	var apiSrv *api.Server
	var store *spanbuf.Store
	resolved, resolveErr := resolveConfigPath(*configPath)
	if resolveErr == nil {
		sockPath, sockErr := api.SocketPathFromEnvOrConfig(*socketPath, resolved)
		if sockErr == nil {
			// Create server with nil supervisor/store for now — they're set after creation
			apiSrv = api.New(nil, nil, sockPath, Version)
			if err := apiSrv.Start(); err != nil {
				fmt.Fprintf(os.Stderr, "error: %v\n", err)
				return 1
			}
		}
	}

	bus := events.NewBus()
	sup := supervisor.New(cfg, bus)

	// Wire the supervisor into the already-listening API server
	if apiSrv != nil {
		apiSrv.SetSupervisor(sup)
	}

	if err := sup.Start(); err != nil {
		fmt.Fprintf(os.Stderr, "error starting services: %v\n", err)
		if apiSrv != nil {
			apiSrv.Shutdown()
		}
		return 1
	}

	// Start watcher
	var watchMgr *watcher.Manager
	if !*noWatch {
		watchMgr = watcher.NewManager(cfg, sup, bus)
		if err := watchMgr.Start(); err != nil {
			fmt.Fprintf(os.Stderr, "warning: file watcher failed to start: %v\n", err)
		}
	}

	// Start tracing collector if enabled
	var col *collector.Collector
	if cfg.Global.Tracing.Enabled {
		store = spanbuf.NewStore(int64(cfg.Global.Tracing.BufferSize))
		col = collector.New(store, bus, cfg.Global.Tracing.Port)
		if err := col.Start(); err != nil {
			fmt.Fprintf(os.Stderr, "warning: tracing collector failed to start: %v\n", err)
			col = nil
		}
		if apiSrv != nil {
			apiSrv.SetStore(store)
		}
	}

	if *noTUI {
		code := runHeadless(sup, bus, *verbose)
		if apiSrv != nil {
			apiSrv.Shutdown()
		}
		if col != nil {
			_ = col.Shutdown()
		}
		return code
	}

	m := tui.NewModel(sup, store)
	p := tea.NewProgram(m, tea.WithAltScreen())

	if _, err := p.Run(); err != nil {
		fmt.Fprintf(os.Stderr, "TUI error: %v\n", err)
	}

	if apiSrv != nil {
		apiSrv.Shutdown()
	}
	if col != nil {
		_ = col.Shutdown()
	}
	if watchMgr != nil {
		watchMgr.Stop()
	}
	sup.Shutdown()
	return 0
}

func runHeadless(sup *supervisor.Supervisor, bus *events.Bus, verbose bool) int {
	ch := bus.Subscribe(64)
	defer bus.Unsubscribe(ch)

	sigCh := make(chan os.Signal, 1)
	signalNotify(sigCh)

	for {
		select {
		case evt := <-ch:
			switch evt.Type {
			case events.ServiceStateChanged:
				if data, ok := evt.Data.(events.StateChangeData); ok {
					fmt.Printf("[%s] %s: %s -> %s",
						evt.Timestamp.Format("15:04:05"),
						evt.Service,
						data.OldStatus,
						data.NewStatus)
					if data.Reason != "" {
						fmt.Printf(" (%s)", data.Reason)
					}
					fmt.Println()
				}
			case events.LogLine:
				if data, ok := evt.Data.(events.LogLineData); ok {
					fmt.Printf("[%s] %s|%s: %s\n",
						evt.Timestamp.Format("15:04:05"),
						evt.Service,
						data.Stream,
						data.Line)
				}
			case events.FileChanged:
				if verbose {
					if data, ok := evt.Data.(events.FileChangeData); ok {
						fmt.Printf("[%s] %s: file changed: %s\n",
							evt.Timestamp.Format("15:04:05"),
							evt.Service,
							data.Path)
					}
				}
			}
		case <-sigCh:
			fmt.Println("\nshutting down...")
			sup.Shutdown()
			return 0
		}
	}
}

func runStart(args []string) int {
	fs := flag.NewFlagSet("start", flag.ExitOnError)
	configPath := fs.String("config", "", "path to config file")
	socketOverride := fs.String("socket", "", "control socket path")
	_ = fs.Parse(args)

	services := fs.Args()
	if len(services) == 0 {
		fmt.Fprintf(os.Stderr, "usage: bench start <service...>\n")
		return 1
	}

	client, err := connectToRunning(*configPath, *socketOverride)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		return 1
	}

	for _, svc := range services {
		if _, err := client.Call("start", map[string]string{"service": svc}); err != nil {
			fmt.Fprintf(os.Stderr, "error starting %s: %v\n", svc, err)
			return 1
		}
		fmt.Printf("started %s\n", svc)
	}
	return 0
}

func runStop(args []string) int {
	fs := flag.NewFlagSet("stop", flag.ExitOnError)
	configPath := fs.String("config", "", "path to config file")
	socketOverride := fs.String("socket", "", "control socket path")
	_ = fs.Parse(args)

	services := fs.Args()
	if len(services) == 0 {
		fmt.Fprintf(os.Stderr, "usage: bench stop <service...>\n")
		return 1
	}

	client, err := connectToRunning(*configPath, *socketOverride)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		return 1
	}

	for _, svc := range services {
		if _, err := client.Call("stop", map[string]string{"service": svc}); err != nil {
			fmt.Fprintf(os.Stderr, "error stopping %s: %v\n", svc, err)
			return 1
		}
		fmt.Printf("stopped %s\n", svc)
	}
	return 0
}

func runRestart(args []string) int {
	fs := flag.NewFlagSet("restart", flag.ExitOnError)
	configPath := fs.String("config", "", "path to config file")
	socketOverride := fs.String("socket", "", "control socket path")
	_ = fs.Parse(args)

	services := fs.Args()
	if len(services) == 0 {
		fmt.Fprintf(os.Stderr, "usage: bench restart <service...>\n")
		return 1
	}

	client, err := connectToRunning(*configPath, *socketOverride)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		return 1
	}

	for _, svc := range services {
		params := map[string]string{"service": svc, "reason": "manual restart"}
		if _, err := client.Call("restart", params); err != nil {
			fmt.Fprintf(os.Stderr, "error restarting %s: %v\n", svc, err)
			return 1
		}
		fmt.Printf("restarted %s\n", svc)
	}
	return 0
}

func runStatus(args []string) int {
	fs := flag.NewFlagSet("status", flag.ExitOnError)
	configPath := fs.String("config", "", "path to config file")
	socketOverride := fs.String("socket", "", "control socket path")
	jsonOut := fs.Bool("json", false, "JSON output")
	_ = fs.Parse(args)

	// Try connecting to a running instance for live status
	client, connErr := connectToRunning(*configPath, *socketOverride)
	if connErr == nil {
		return statusFromRunning(client, *jsonOut, fs.Args())
	}

	// Fall back to config-only output
	cfg, err := loadConfig(*configPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		return 1
	}

	if *jsonOut {
		return statusJSON(cfg)
	}

	// Table output
	order, _ := cfg.StartOrder()
	fmt.Printf("%-20s %-10s %-12s %-10s %s\n", "SERVICE", "TYPE", "STATUS", "RESTARTS", "COMMAND/IMAGE")
	fmt.Printf("%-20s %-10s %-12s %-10s %s\n",
		strings.Repeat("-", 20),
		strings.Repeat("-", 10),
		strings.Repeat("-", 12),
		strings.Repeat("-", 10),
		strings.Repeat("-", 30))

	for _, key := range order {
		svc := cfg.Services[key]
		status := "configured"
		if !svc.GetAutoStart() {
			status = "disabled"
		}
		svcType := "process"
		cmdStr := ""
		if svc.IsContainer() {
			svcType = "container"
			cmdStr = svc.Container.Image
		} else if svc.Command != nil {
			cmdStr = svc.Command.String()
		}
		fmt.Printf("%-20s %-10s %-12s %-10s %s\n",
			key,
			svcType,
			status,
			"-",
			cmdStr)
	}
	return 0
}

// statusFromRunning queries live status from a running bench instance.
func statusFromRunning(client *api.Client, jsonOut bool, serviceFilter []string) int {
	var params map[string]string
	if len(serviceFilter) > 0 {
		params = map[string]string{"service": serviceFilter[0]}
	}

	data, err := client.Call("status", params)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		return 1
	}

	if jsonOut {
		var raw json.RawMessage
		if err := json.Unmarshal(data, &raw); err == nil {
			enc := json.NewEncoder(os.Stdout)
			enc.SetIndent("", "  ")
			_ = enc.Encode(raw)
		}
		return 0
	}

	// Parse into a list (single service response wraps into a list)
	var services []api.ServiceStatus
	if err := json.Unmarshal(data, &services); err != nil {
		// Try single object
		var single api.ServiceStatus
		if err := json.Unmarshal(data, &single); err == nil {
			services = []api.ServiceStatus{single}
		} else {
			fmt.Fprintf(os.Stderr, "error: failed to parse status response\n")
			return 1
		}
	}

	fmt.Printf("%-20s %-10s %-12s %-8s %-10s %s\n", "SERVICE", "TYPE", "STATUS", "PID", "RESTARTS", "UPTIME")
	fmt.Printf("%-20s %-10s %-12s %-8s %-10s %s\n",
		strings.Repeat("-", 20),
		strings.Repeat("-", 10),
		strings.Repeat("-", 12),
		strings.Repeat("-", 8),
		strings.Repeat("-", 10),
		strings.Repeat("-", 12))

	for _, svc := range services {
		pid := "-"
		if svc.PID > 0 {
			pid = fmt.Sprintf("%d", svc.PID)
		}
		uptime := "-"
		if svc.Uptime != "" {
			uptime = svc.Uptime
		}
		fmt.Printf("%-20s %-10s %-12s %-8s %-10d %s\n",
			svc.Key,
			svc.Type,
			svc.Status,
			pid,
			svc.RestartCount,
			uptime)
	}
	return 0
}

func statusJSON(cfg *config.Config) int {
	type svcStatus struct {
		Key       string `json:"key"`
		Type      string `json:"type"`
		Command   string `json:"command,omitempty"`
		Image     string `json:"image,omitempty"`
		Dir       string `json:"dir,omitempty"`
		AutoStart bool   `json:"auto_start"`
	}
	var services []svcStatus
	order, _ := cfg.StartOrder()
	for _, key := range order {
		svc := cfg.Services[key]
		s := svcStatus{
			Key:       key,
			Dir:       svc.Dir,
			AutoStart: svc.GetAutoStart(),
		}
		if svc.IsContainer() {
			s.Type = "container"
			s.Image = svc.Container.Image
		} else {
			s.Type = "process"
			if svc.Command != nil {
				s.Command = svc.Command.String()
			}
		}
		services = append(services, s)
	}
	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	_ = enc.Encode(services)
	return 0
}

func runLogs(args []string) int {
	fs := flag.NewFlagSet("logs", flag.ExitOnError)
	configPath := fs.String("config", "", "path to config file")
	socketOverride := fs.String("socket", "", "control socket path")
	last := fs.Int("last", 100, "number of log lines to fetch")
	follow := fs.Bool("follow", false, "follow log output (poll)")
	followShort := fs.Bool("f", false, "follow log output (shorthand)")
	_ = fs.Parse(args)

	services := fs.Args()
	if len(services) == 0 {
		fmt.Fprintf(os.Stderr, "usage: bench logs <service>\n")
		return 1
	}
	svcName := services[0]

	client, err := connectToRunning(*configPath, *socketOverride)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		return 1
	}

	// cursor tracks the seq of the last line printed to avoid duplicates
	var cursor uint64

	fetchAndPrint := func(params map[string]any) error {
		data, err := client.Call("logs", params)
		if err != nil {
			return err
		}
		var lines []api.LogLine
		if err := json.Unmarshal(data, &lines); err != nil {
			return err
		}
		for _, l := range lines {
			ts := l.Timestamp
			if t, err := time.Parse(time.RFC3339Nano, l.Timestamp); err == nil {
				ts = t.Format("15:04:05")
			}
			fmt.Printf("[%s] %s|%s: %s\n", ts, svcName, l.Stream, l.Text)
			if l.Seq > cursor {
				cursor = l.Seq
			}
		}
		return nil
	}

	// Initial fetch
	if err := fetchAndPrint(map[string]any{"service": svcName, "last": *last}); err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		return 1
	}

	if !*follow && !*followShort {
		return 0
	}

	// Poll for new logs using sequence cursor to avoid duplicates
	sigCh := make(chan os.Signal, 1)
	signalNotify(sigCh)
	for {
		select {
		case <-sigCh:
			return 0
		case <-time.After(time.Second):
			params := map[string]any{"service": svcName, "last": 500}
			if cursor > 0 {
				params["after_seq"] = cursor
			}
			if err := fetchAndPrint(params); err != nil {
				fmt.Fprintf(os.Stderr, "error: %v\n", err)
				return 1
			}
		}
	}
}

func runValidate(args []string) int {
	fs := flag.NewFlagSet("validate", flag.ExitOnError)
	configPath := fs.String("config", "", "path to config file")
	_ = fs.Parse(args)

	cfg, err := loadConfig(*configPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		return 1
	}

	if err := cfg.Validate(); err != nil {
		fmt.Fprintf(os.Stderr, "validation failed:\n%v\n", err)
		return 1
	}

	order, _ := cfg.StartOrder()
	fmt.Printf("config is valid: %d services defined\n", len(cfg.Services))
	fmt.Printf("start order: %s\n", strings.Join(order, " -> "))
	for _, key := range order {
		svc := cfg.Services[key]
		svcType := "process"
		if svc.IsContainer() {
			svcType = "container"
		}
		watch := "off"
		if svc.Watch.IsEnabled() {
			watch = "on"
		}
		fmt.Printf("  %-20s type=%-10s dir=%-30s watch=%s restart=%s\n",
			key, svcType, svc.Dir, watch, svc.Restart.Policy)
	}
	return 0
}

// signalNotify abstracts os/signal for the CLI.
func signalNotify(ch chan<- os.Signal) {
	// Imported in signal_unix.go
	signalNotifyFunc(ch)
}

// This is set by the platform-specific file.
var signalNotifyFunc = func(ch chan<- os.Signal) {
	// no-op fallback
}

