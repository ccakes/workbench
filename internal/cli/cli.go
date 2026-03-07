package cli

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"strings"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/ccakes/bench/internal/config"
	"github.com/ccakes/bench/internal/events"
	"github.com/ccakes/bench/internal/supervisor"
	"github.com/ccakes/bench/internal/tui"
	"github.com/ccakes/bench/internal/watcher"
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
	fmt.Fprintf(os.Stderr, `bench - YAML-native TUI for running and supervising local development services

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
  --verbose          Verbose output

Run 'bench <command> --help' for more information on a command.
`)
}

func loadConfig(configPath string) (*config.Config, error) {
	path := configPath
	if path == "" {
		var err error
		path, err = config.FindConfig()
		if err != nil {
			return nil, err
		}
	}
	return config.Load(path)
}

func runUp(args []string) int {
	fs := flag.NewFlagSet("up", flag.ExitOnError)
	configPath := fs.String("config", "", "path to config file")
	noTUI := fs.Bool("no-tui", false, "disable TUI, run in foreground")
	noWatch := fs.Bool("no-watch", false, "disable file watching")
	verbose := fs.Bool("verbose", false, "verbose output")
	fs.Parse(args)

	cfg, err := loadConfig(*configPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		return 1
	}

	if err := cfg.Validate(); err != nil {
		fmt.Fprintf(os.Stderr, "config validation failed:\n%v\n", err)
		return 1
	}

	bus := events.NewBus()
	sup := supervisor.New(cfg, bus)

	if err := sup.Start(); err != nil {
		fmt.Fprintf(os.Stderr, "error starting services: %v\n", err)
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

	if *noTUI {
		return runHeadless(sup, bus, *verbose)
	}

	m := tui.NewModel(sup)
	p := tea.NewProgram(m, tea.WithAltScreen())

	if _, err := p.Run(); err != nil {
		fmt.Fprintf(os.Stderr, "TUI error: %v\n", err)
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
	fs.Parse(args)

	cfg, err := loadConfig(*configPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		return 1
	}
	if err := cfg.Validate(); err != nil {
		fmt.Fprintf(os.Stderr, "config validation failed:\n%v\n", err)
		return 1
	}

	services := fs.Args()
	if len(services) == 0 {
		fmt.Fprintf(os.Stderr, "usage: bench start <service...>\n")
		return 1
	}

	bus := events.NewBus()
	sup := supervisor.New(cfg, bus)

	for _, svc := range services {
		if err := sup.StartService(svc); err != nil {
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
	fs.Parse(args)

	cfg, err := loadConfig(*configPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		return 1
	}

	services := fs.Args()
	if len(services) == 0 {
		fmt.Fprintf(os.Stderr, "usage: bench stop <service...>\n")
		return 1
	}

	bus := events.NewBus()
	sup := supervisor.New(cfg, bus)

	for _, svc := range services {
		if err := sup.StopService(svc); err != nil {
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
	fs.Parse(args)

	cfg, err := loadConfig(*configPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		return 1
	}

	services := fs.Args()
	if len(services) == 0 {
		fmt.Fprintf(os.Stderr, "usage: bench restart <service...>\n")
		return 1
	}

	bus := events.NewBus()
	sup := supervisor.New(cfg, bus)

	for _, svc := range services {
		if err := sup.RestartService(svc, "manual restart"); err != nil {
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
	jsonOut := fs.Bool("json", false, "JSON output")
	fs.Parse(args)

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
	fmt.Printf("%-20s %-12s %-10s %s\n", "SERVICE", "STATUS", "RESTARTS", "COMMAND")
	fmt.Printf("%-20s %-12s %-10s %s\n",
		strings.Repeat("-", 20),
		strings.Repeat("-", 12),
		strings.Repeat("-", 10),
		strings.Repeat("-", 30))

	for _, key := range order {
		svc := cfg.Services[key]
		status := "configured"
		if !svc.GetAutoStart() {
			status = "disabled"
		}
		fmt.Printf("%-20s %-12s %-10s %s\n",
			key,
			status,
			"-",
			svc.Command.String())
	}
	return 0
}

func statusJSON(cfg *config.Config) int {
	type svcStatus struct {
		Key       string `json:"key"`
		Command   string `json:"command"`
		Dir       string `json:"dir"`
		AutoStart bool   `json:"auto_start"`
	}
	var services []svcStatus
	order, _ := cfg.StartOrder()
	for _, key := range order {
		svc := cfg.Services[key]
		services = append(services, svcStatus{
			Key:       key,
			Command:   svc.Command.String(),
			Dir:       svc.Dir,
			AutoStart: svc.GetAutoStart(),
		})
	}
	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	enc.Encode(services)
	return 0
}

func runLogs(args []string) int {
	fs := flag.NewFlagSet("logs", flag.ExitOnError)
	configPath := fs.String("config", "", "path to config file")
	follow := fs.Bool("follow", false, "follow log output")
	fs.Bool("f", false, "follow log output (shorthand)")
	fs.Parse(args)

	cfg, err := loadConfig(*configPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		return 1
	}
	if err := cfg.Validate(); err != nil {
		fmt.Fprintf(os.Stderr, "config validation failed:\n%v\n", err)
		return 1
	}

	services := fs.Args()

	bus := events.NewBus()
	sup := supervisor.New(cfg, bus)

	if err := sup.Start(); err != nil {
		fmt.Fprintf(os.Stderr, "error starting services: %v\n", err)
		return 1
	}

	ch := bus.Subscribe(256)
	sigCh := make(chan os.Signal, 1)
	signalNotify(sigCh)

	filterService := ""
	if len(services) > 0 {
		filterService = services[0]
	}

	for {
		select {
		case evt := <-ch:
			if evt.Type != events.LogLine {
				continue
			}
			if filterService != "" && evt.Service != filterService {
				continue
			}
			if data, ok := evt.Data.(events.LogLineData); ok {
				fmt.Printf("[%s] %s|%s: %s\n",
					evt.Timestamp.Format("15:04:05"),
					evt.Service,
					data.Stream,
					data.Line)
			}
			if !*follow {
				// In non-follow mode, drain available events then exit
				// For now, follow mode is the default behavior for logs
			}
		case <-sigCh:
			sup.Shutdown()
			return 0
		}
	}
}

func runValidate(args []string) int {
	fs := flag.NewFlagSet("validate", flag.ExitOnError)
	configPath := fs.String("config", "", "path to config file")
	fs.Parse(args)

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
		watch := "off"
		if svc.Watch.IsEnabled() {
			watch = "on"
		}
		fmt.Printf("  %-20s dir=%-30s watch=%s restart=%s\n",
			key, svc.Dir, watch, svc.Restart.Policy)
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

