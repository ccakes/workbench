package cli

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
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

// execCommand is a thin alias around exec.Command so tests can stub it out
// if needed without touching production code.
var execCommand = exec.Command

var validContainerPrefix = regexp.MustCompile(`^[a-zA-Z0-9_-]+$`)

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
	case "wait":
		return runWait(os.Args[2:])
	case "clean":
		return runClean(os.Args[2:])
	case "reload":
		return runReload(os.Args[2:])
	case "validate":
		return runValidate(os.Args[2:])
	case "import-compose":
		return runImportCompose(os.Args[2:])
	case "agent-skill":
		return runAgentSkill(os.Args[2:])
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
  up                 Start services and open TUI (default)
  start              Start specific services
  stop               Stop specific services
  restart            Restart specific services
  status             Show service status
  logs               Show service logs
  wait               Block until services reach ready
  clean              Remove stale socket and prefix-matched containers
  reload             Re-read config and restart services whose config changed
  validate           Validate configuration
  import-compose     Convert docker-compose.yml to bench.yml
  agent-skill        Print embedded agent skill with save options
  version            Show version

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
	var (
		sockPath string
		resolved string
		source   string // describes where sockPath came from, for diagnostics
	)

	switch {
	case socketOverride != "":
		sockPath = socketOverride
		source = "--socket"
	case os.Getenv("BENCH_SOCKET") != "":
		sockPath = os.Getenv("BENCH_SOCKET")
		source = "BENCH_SOCKET"
	default:
		r, err := resolveConfigPath(configPath)
		if err != nil {
			return nil, err
		}
		resolved = r
		sp, err := api.SocketPath(resolved)
		if err != nil {
			return nil, err
		}
		sockPath = sp
		source = "config"
	}

	client := api.NewClient(sockPath)
	if err := client.Ping(); err != nil {
		return nil, noRunningBenchError(sockPath, resolved, source, err)
	}
	return client, nil
}

// noRunningBenchError formats a connect failure with enough context to act on:
// the resolved config path (if any), the expected socket path, and a hint
// showing the exact command that would start a bench for this config.
func noRunningBenchError(sockPath, configPath, source string, cause error) error {
	if source == "config" && configPath != "" {
		return fmt.Errorf(
			"no bench up is running for config %s (expected socket %s).\n"+
				"  start it with: bench up --config %s\n"+
				"  underlying error: %v",
			configPath, sockPath, configPath, cause,
		)
	}
	return fmt.Errorf(
		"no bench up is running on socket %s (from %s).\n"+
			"  start one with: bench up\n"+
			"  underlying error: %v",
		sockPath, source, cause,
	)
}

func runUp(args []string) int {
	fs := flag.NewFlagSet("up", flag.ExitOnError)
	configPath := fs.String("config", "", "path to config file")
	socketPath := fs.String("socket", "", "control socket path (default: auto)")
	noTUI := fs.Bool("no-tui", false, "disable TUI, run in foreground")
	noWatch := fs.Bool("no-watch", false, "disable file watching")
	verbose := fs.Bool("verbose", false, "verbose output")
	var profiles stringSlice
	fs.Var(&profiles, "profile", "activate the named profile (repeatable)")
	_ = fs.Parse(reorderFlags(args))

	cfg, err := loadConfig(*configPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		return 1
	}

	if err := cfg.Validate(); err != nil {
		fmt.Fprintf(os.Stderr, "config validation failed:\n%v\n", err)
		return 1
	}

	if roots := fs.Args(); len(roots) > 0 {
		if err := applyServiceSubset(cfg, roots); err != nil {
			fmt.Fprintf(os.Stderr, "error: %v\n", err)
			return 1
		}
	} else if len(profiles) > 0 {
		applyProfileFilter(cfg, profiles)
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

// stringSlice is a flag.Value that accumulates repeated --flag values into
// a slice. Used for --profile, which is repeatable like in docker-compose.
type stringSlice []string

func (s *stringSlice) String() string { return strings.Join(*s, ",") }
func (s *stringSlice) Set(v string) error {
	*s = append(*s, v)
	return nil
}

// applyProfileFilter overrides auto_start so only services matching the
// active profile set will launch. A service is started if (a) it has no
// `profiles:` set (always-on), or (b) at least one of its profiles is in the
// active set. Transitive depends_on of started services are pulled in too,
// so a profile-tagged service's dependencies still come up even if they
// themselves carry a different profile or no profile.
func applyProfileFilter(cfg *config.Config, active []string) {
	activeSet := make(map[string]bool, len(active))
	for _, p := range active {
		activeSet[p] = true
	}

	// First pass: identify roots — services that should be started.
	var roots []string
	for key, svc := range cfg.Services {
		if len(svc.Profiles) == 0 {
			roots = append(roots, key)
			continue
		}
		for _, p := range svc.Profiles {
			if activeSet[p] {
				roots = append(roots, key)
				break
			}
		}
	}

	// Pull transitive deps into the closure so dependencies are honoured.
	keep, err := cfg.TransitiveDeps(roots)
	if err != nil {
		// Shouldn't happen — roots come from cfg.Services. If somehow it
		// does, fall back to leaving cfg untouched rather than crashing.
		return
	}
	off := false
	for key, svc := range cfg.Services {
		if keep[key] {
			if svc.AutoStart == nil {
				on := true
				svc.AutoStart = &on
				cfg.Services[key] = svc
			}
			continue
		}
		svc.AutoStart = &off
		cfg.Services[key] = svc
	}
}

// applyServiceSubset disables auto_start on every service that is not in the
// transitive depends_on closure of roots. Roots themselves and their
// dependencies keep their existing auto_start setting, so a service the user
// explicitly named on the CLI will start even if its config says
// auto_start: false. Returns an error if any root is unknown.
func applyServiceSubset(cfg *config.Config, roots []string) error {
	keep, err := cfg.TransitiveDeps(roots)
	if err != nil {
		return err
	}
	off := false
	for key, svc := range cfg.Services {
		if keep[key] {
			// Honour an explicit "auto_start: true" override for roots and
			// their deps; default to enabled if unset.
			if svc.AutoStart == nil {
				on := true
				svc.AutoStart = &on
				cfg.Services[key] = svc
			}
			continue
		}
		svc.AutoStart = &off
		cfg.Services[key] = svc
	}
	return nil
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
	_ = fs.Parse(reorderFlags(args))

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
	_ = fs.Parse(reorderFlags(args))

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
	_ = fs.Parse(reorderFlags(args))

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
	why := fs.Bool("why", false, "show last_error column for all services")
	_ = fs.Parse(reorderFlags(args))

	// Try connecting to a running instance for live status
	client, connErr := connectToRunning(*configPath, *socketOverride)
	if connErr == nil {
		return statusFromRunning(client, *jsonOut, *why, fs.Args())
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
// When showWhy is true, the REASON column (last_error) is shown for every row;
// otherwise it appears only for services whose status indicates a problem.
func statusFromRunning(client *api.Client, jsonOut, showWhy bool, serviceFilter []string) int {
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

	fmt.Printf("%-20s %-10s %-12s %-8s %-10s %-12s %s\n", "SERVICE", "TYPE", "STATUS", "PID", "RESTARTS", "UPTIME", "REASON")
	fmt.Printf("%-20s %-10s %-12s %-8s %-10s %-12s %s\n",
		strings.Repeat("-", 20),
		strings.Repeat("-", 10),
		strings.Repeat("-", 12),
		strings.Repeat("-", 8),
		strings.Repeat("-", 10),
		strings.Repeat("-", 12),
		strings.Repeat("-", 30))

	for _, svc := range services {
		pid := "-"
		if svc.PID > 0 {
			pid = fmt.Sprintf("%d", svc.PID)
		}
		uptime := "-"
		if svc.Uptime != "" {
			uptime = svc.Uptime
		}
		reason := statusReason(svc, showWhy)
		fmt.Printf("%-20s %-10s %-12s %-8s %-10d %-12s %s\n",
			svc.Key,
			svc.Type,
			svc.Status,
			pid,
			svc.RestartCount,
			uptime,
			reason)
	}
	return 0
}

// statusReason picks a short, single-line reason to show in the REASON column.
// With showWhy=true, last_error is always shown if non-empty. Otherwise only
// statuses that already imply a problem (failed/backoff/restarting) surface it,
// so a healthy stack stays uncluttered.
func statusReason(s api.ServiceStatus, showWhy bool) string {
	if s.LastError == "" {
		return ""
	}
	if !showWhy {
		switch s.Status {
		case "failed", "backoff", "restarting":
			// fall through
		default:
			return ""
		}
	}
	line := s.LastError
	if i := strings.IndexByte(line, '\n'); i >= 0 {
		line = line[:i]
	}
	const maxReason = 60
	if len(line) > maxReason {
		line = line[:maxReason-1] + "…"
	}
	return line
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
	grep := fs.String("grep", "", "filter lines by regex (client-side)")
	since := fs.Duration("since", 0, "only show lines newer than this duration ago (e.g. 5m)")
	_ = fs.Parse(reorderFlags(args))

	services := fs.Args()
	multi := len(services) != 1

	var grepRe *regexp.Regexp
	if *grep != "" {
		re, err := regexp.Compile(*grep)
		if err != nil {
			fmt.Fprintf(os.Stderr, "error: invalid --grep regex: %v\n", err)
			return 1
		}
		grepRe = re
	}

	client, err := connectToRunning(*configPath, *socketOverride)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		return 1
	}

	var cursor uint64     // single-service follow cursor
	lastTs := time.Time{} // multi-service follow cursor
	doFollow := *follow || *followShort

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
			if grepRe != nil && !grepRe.MatchString(l.Text) {
				continue
			}
			ts := l.Timestamp
			parsed, perr := time.Parse(time.RFC3339Nano, l.Timestamp)
			if perr == nil {
				ts = parsed.Format("15:04:05")
				if parsed.After(lastTs) {
					lastTs = parsed
				}
			}
			tag := l.Service
			if tag == "" && !multi {
				tag = services[0]
			}
			fmt.Printf("[%s] %s|%s: %s\n", ts, tag, l.Stream, l.Text)
			if l.Seq > cursor {
				cursor = l.Seq
			}
		}
		return nil
	}

	// Build initial request params.
	initial := map[string]any{"last": *last}
	switch {
	case multi:
		if len(services) > 0 {
			initial["services"] = services
		}
	default:
		initial["service"] = services[0]
	}
	if *since > 0 {
		initial["since_ms"] = since.Milliseconds()
	}
	if err := fetchAndPrint(initial); err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		return 1
	}

	if !doFollow {
		return 0
	}

	// Poll for new logs. Single-service follow uses the seq cursor; multi
	// uses a timestamp window so we don't need per-service cursors over the
	// wire. Some duplication is possible on the seam — `--grep` and the
	// terminal usually mask it.
	sigCh := make(chan os.Signal, 1)
	signalNotify(sigCh)
	for {
		select {
		case <-sigCh:
			return 0
		case <-time.After(time.Second):
			params := map[string]any{"last": 500}
			if multi {
				if len(services) > 0 {
					params["services"] = services
				}
				if !lastTs.IsZero() {
					params["since_ms"] = time.Since(lastTs).Milliseconds() + 1
				}
			} else {
				params["service"] = services[0]
				if cursor > 0 {
					params["after_seq"] = cursor
				}
			}
			if err := fetchAndPrint(params); err != nil {
				fmt.Fprintf(os.Stderr, "error: %v\n", err)
				return 1
			}
		}
	}
}

// reorderFlags moves flag-like args (starting with "-") and their values
// before positional args so that Go's flag package parses them correctly.
// This allows "bench logs megalith -last 200" to work like "bench logs -last 200 megalith".
func reorderFlags(args []string) []string {
	var flags, positional []string
	for i := 0; i < len(args); i++ {
		if strings.HasPrefix(args[i], "-") {
			flags = append(flags, args[i])
			// If the flag uses "-flag value" form (not "-flag=value"),
			// consume the next arg as the value.
			if !strings.Contains(args[i], "=") && i+1 < len(args) && !strings.HasPrefix(args[i+1], "-") {
				i++
				flags = append(flags, args[i])
			}
		} else {
			positional = append(positional, args[i])
		}
	}
	return append(flags, positional...)
}

func runReload(args []string) int {
	fs := flag.NewFlagSet("reload", flag.ExitOnError)
	configPath := fs.String("config", "", "path to config file")
	socketOverride := fs.String("socket", "", "control socket path")
	_ = fs.Parse(reorderFlags(args))

	resolved, err := resolveConfigPath(*configPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		return 1
	}

	client, err := connectToRunning(*configPath, *socketOverride)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		return 1
	}

	data, err := client.Call("reload", map[string]string{"config_path": resolved})
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		return 1
	}

	var report supervisor.ReloadReport
	if err := json.Unmarshal(data, &report); err != nil {
		fmt.Fprintf(os.Stderr, "error: parsing reload response: %v\n", err)
		return 1
	}

	fmt.Println(report.Summary())
	for _, key := range report.Restarted {
		fmt.Printf("  restarted: %s\n", key)
	}
	for _, key := range report.Added {
		fmt.Printf("  added (not auto-started — needs full `bench up`): %s\n", key)
	}
	for _, key := range report.Removed {
		fmt.Printf("  removed (still running — needs full `bench down`): %s\n", key)
	}
	for key, errMsg := range report.Errors {
		fmt.Fprintf(os.Stderr, "  error %s: %s\n", key, errMsg)
	}
	if len(report.Errors) > 0 {
		return 1
	}
	return 0
}

func runClean(args []string) int {
	fs := flag.NewFlagSet("clean", flag.ExitOnError)
	configPath := fs.String("config", "", "path to config file")
	force := fs.Bool("force", false, "clean even if a bench is currently running")
	dryRun := fs.Bool("dry-run", false, "show what would be removed without doing it")
	_ = fs.Parse(reorderFlags(args))

	cfg, err := loadConfig(*configPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		return 1
	}
	resolved, _ := resolveConfigPath(*configPath)
	sockPath, err := api.SocketPath(resolved)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		return 1
	}

	// Refuse if a bench is currently running on this socket unless --force.
	// We test by pinging — a successful ping means an owner is alive.
	probe := api.NewClient(sockPath)
	if err := probe.Ping(); err == nil && !*force {
		fmt.Fprintf(os.Stderr, "a bench is running on %s — stop it first or pass --force\n", sockPath)
		return 1
	}

	prefix := cfg.Global.ContainerPrefix
	if !validContainerPrefix.MatchString(prefix) {
		fmt.Fprintf(os.Stderr, "error: invalid container_prefix %q\n", prefix)
		return 1
	}
	containers, err := listContainersWithPrefix(prefix)
	if err != nil {
		fmt.Fprintf(os.Stderr, "warning: could not enumerate containers: %v\n", err)
	}

	if *dryRun {
		fmt.Printf("would remove socket: %s\n", sockPath)
		for _, c := range containers {
			fmt.Printf("would remove container: %s\n", c)
		}
		return 0
	}

	if _, err := os.Stat(sockPath); err == nil {
		if err := os.Remove(sockPath); err != nil {
			fmt.Fprintf(os.Stderr, "warning: removing socket: %v\n", err)
		} else {
			fmt.Printf("removed socket: %s\n", sockPath)
		}
	}

	for _, c := range containers {
		if err := dockerRemove(c); err != nil {
			fmt.Fprintf(os.Stderr, "warning: removing container %s: %v\n", c, err)
			continue
		}
		fmt.Printf("removed container: %s\n", c)
	}
	return 0
}

// listContainersWithPrefix returns container names whose name starts with
// the given prefix. Uses docker CLI directly so we don't have to vendor the
// Docker SDK.
func listContainersWithPrefix(prefix string) ([]string, error) {
	if prefix == "" {
		return nil, nil
	}
	cmd := execCommand("docker", "ps", "-a", "--filter", "label=managed-by=bench", "--format", "{{.Names}}")
	out, err := cmd.Output()
	if err != nil {
		return nil, err
	}
	return containerNamesWithPrefix(prefix, string(out)), nil
}

func containerNamesWithPrefix(prefix, out string) []string {
	var names []string
	want := prefix + "-"
	for _, line := range strings.Split(out, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		if !strings.HasPrefix(line, want) {
			continue
		}
		names = append(names, line)
	}
	return names
}

func dockerRemove(name string) error {
	// stop then remove; ignore stop failure (container may already be stopped)
	_ = execCommand("docker", "stop", name).Run()
	return execCommand("docker", "rm", "-f", name).Run()
}

func runWait(args []string) int {
	fs := flag.NewFlagSet("wait", flag.ExitOnError)
	configPath := fs.String("config", "", "path to config file")
	socketOverride := fs.String("socket", "", "control socket path")
	timeout := fs.Duration("timeout", 5*time.Minute, "max time to wait")
	_ = fs.Parse(reorderFlags(args))

	client, err := connectToRunning(*configPath, *socketOverride)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		return 1
	}

	params := map[string]any{
		"services":   fs.Args(),
		"timeout_ms": timeout.Milliseconds(),
	}
	callTimeout := time.Duration(0)
	if *timeout > 0 {
		callTimeout = *timeout + 5*time.Second
	}
	data, err := client.CallWithTimeout("wait", params, callTimeout)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		return 1
	}

	var result api.WaitResult
	if err := json.Unmarshal(data, &result); err != nil {
		fmt.Fprintf(os.Stderr, "error: parsing wait response: %v\n", err)
		return 1
	}

	switch result.Outcome {
	case "ready":
		for svc, st := range result.States {
			fmt.Printf("%-20s %s\n", svc, st)
		}
		return 0
	case "failed":
		for svc, st := range result.States {
			fmt.Fprintf(os.Stderr, "%-20s %s\n", svc, st)
		}
		fmt.Fprintln(os.Stderr, "one or more services failed to reach ready")
		return 1
	case "timeout":
		for svc, st := range result.States {
			fmt.Fprintf(os.Stderr, "%-20s %s\n", svc, st)
		}
		fmt.Fprintf(os.Stderr, "timeout after %s\n", time.Duration(result.WaitedMs)*time.Millisecond)
		return 2
	}
	fmt.Fprintf(os.Stderr, "unexpected outcome %q\n", result.Outcome)
	return 1
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
