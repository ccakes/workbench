package runner

import (
	"encoding/json"
	"fmt"
	"os/exec"
	"runtime"
	"strconv"
	"strings"
	"time"

	"github.com/ccakes/workbench/internal/config"
)

const (
	defaultAppleGatewayIP = "192.168.64.1"
	// appleInspectFailureLimit is how many consecutive `inspect` failures the
	// exit poll tolerates (transient daemon hiccups) before assuming the
	// container is gone and reporting an unknown exit.
	appleInspectFailureLimit = 3
	// appleMinMacOSMajor is the minimum macOS major version the Apple backend
	// supports (custom networks and DNS domains require macOS 26).
	appleMinMacOSMajor = 26
)

// appleBackend runs containers via Apple's `container` CLI on Apple silicon.
type appleBackend struct {
	binary    string
	gatewayIP string
}

func newAppleBackend(g config.GlobalConfig) appleBackend {
	gw := g.Apple.GatewayIP
	if gw == "" {
		gw = defaultAppleGatewayIP
	}
	return appleBackend{binary: "container", gatewayIP: gw}
}

func (appleBackend) Name() string     { return "apple" }
func (b appleBackend) Binary() string { return b.binary }

// Available checks that the host can run Apple containers: Apple silicon,
// macOS 26+, and a running `container` system service.
func (b appleBackend) Available() error {
	if runtime.GOOS != "darwin" || runtime.GOARCH != "arm64" {
		return fmt.Errorf("apple container backend requires an Apple silicon Mac")
	}
	if v, err := macOSMajorVersion(); err == nil && v < appleMinMacOSMajor {
		return fmt.Errorf("apple container backend requires macOS %d or later (found macOS %d)", appleMinMacOSMajor, v)
	}
	out, err := exec.Command(b.binary, "system", "status").CombinedOutput()
	if err != nil {
		return fmt.Errorf("apple container is not available: %s (try `container system start`)", strings.TrimSpace(string(out)))
	}
	return nil
}

func (b appleBackend) RunArgs(spec RunSpec) []string {
	// `container` has no `--add-host`; containers reach the host via the vmnet
	// gateway IP (see OTELHost), so no host alias is injected here.
	return buildRunArgs(spec, nil)
}

func (appleBackend) LogsArgs(id string) []string { return []string{"logs", "-f", id} }

func (appleBackend) StopArgs(id string, timeout time.Duration) []string {
	return []string{"stop", "-t", strconv.Itoa(int(timeout.Seconds())), id}
}

func (appleBackend) KillArgs(id string) []string { return []string{"kill", id} }

func (appleBackend) RemoveArgs(target string, force bool) []string {
	// `container` uses `delete` (aliased `rm`) and has no `-v`; anonymous
	// volumes are not auto-removed — a documented difference from Docker.
	args := []string{"delete"}
	if force {
		args = append(args, "-f")
	}
	return append(args, target)
}

// WaitExit polls `container inspect` until the container leaves the running
// state, then reports its exit code. `container` has no `wait` subcommand.
//
// Exit-code fidelity is best-effort: `container inspect` does not reliably
// expose the Linux process exit code, so a cleanly stopped container with no
// code reported is treated as exit 0. Repeated inspect failures (the container
// is gone) yield -1.
func (b appleBackend) WaitExit(id string) int {
	failures := 0
	for {
		time.Sleep(containerPollInterval)
		status, code, err := b.inspect(id)
		if err != nil {
			failures++
			if failures >= appleInspectFailureLimit {
				return -1
			}
			continue
		}
		failures = 0
		if status != "" && !strings.EqualFold(status, "running") && !strings.EqualFold(status, "starting") {
			return code
		}
	}
}

func (b appleBackend) OTELHost() string { return b.gatewayIP }

// inspect runs `container inspect <id>` and extracts the status and best-effort
// exit code. `container inspect` emits JSON by default and, unlike Docker, has
// no `--format` flag (passing one errors), so the raw output is parsed here.
func (b appleBackend) inspect(id string) (status string, code int, err error) {
	out, err := exec.Command(b.binary, "inspect", id).Output()
	if err != nil {
		return "", -1, err
	}
	return parseAppleInspect(out)
}

// parseAppleInspect extracts the container status and a best-effort exit code
// from `container inspect` output. The payload is an array of one object whose
// "status" field is itself an object holding the run state as {"state":
// "running"|"stopped"|...}; older/alternate shapes expose a bare "status"
// string, which is also handled. The exit code, when present, is found by a
// case-insensitive search for an "exitCode"/"exit_code" field at any nesting
// level. Returns code 0 when no exit code is exposed.
func parseAppleInspect(data []byte) (status string, code int, err error) {
	var arr []map[string]any
	if err := json.Unmarshal(data, &arr); err != nil || len(arr) == 0 {
		// Some versions may emit a bare object rather than a single-element array.
		var obj map[string]any
		if err2 := json.Unmarshal(data, &obj); err2 != nil {
			return "", -1, fmt.Errorf("parsing container inspect output: %w", err)
		}
		arr = []map[string]any{obj}
	}
	obj := arr[0]
	switch s := obj["status"].(type) {
	case string:
		status = s
	case map[string]any:
		if st, ok := s["state"].(string); ok {
			status = st
		}
	}
	if c, ok := searchExitCode(obj); ok {
		code = c
	}
	return status, code, nil
}

// searchExitCode walks a decoded JSON value looking for an exit-code field,
// tolerating naming/nesting differences across `container` versions.
func searchExitCode(v any) (int, bool) {
	switch t := v.(type) {
	case map[string]any:
		for k, val := range t {
			key := strings.ToLower(strings.ReplaceAll(k, "_", ""))
			if key == "exitcode" || key == "exitstatus" {
				if f, ok := val.(float64); ok {
					return int(f), true
				}
			}
		}
		for _, val := range t {
			if c, ok := searchExitCode(val); ok {
				return c, true
			}
		}
	case []any:
		for _, val := range t {
			if c, ok := searchExitCode(val); ok {
				return c, true
			}
		}
	}
	return 0, false
}

// macOSMajorVersion returns the major component of `sw_vers -productVersion`.
func macOSMajorVersion() (int, error) {
	out, err := exec.Command("sw_vers", "-productVersion").Output()
	if err != nil {
		return 0, err
	}
	v := strings.TrimSpace(string(out))
	if i := strings.IndexByte(v, '.'); i >= 0 {
		v = v[:i]
	}
	return strconv.Atoi(v)
}
