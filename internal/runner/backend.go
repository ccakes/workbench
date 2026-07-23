package runner

import (
	"os/exec"
	"runtime"
	"time"

	"github.com/ccakes/workbench/internal/config"
)

// ContainerBackend abstracts the container runtime that workbench shells out
// to. The Docker backend preserves workbench's original behavior exactly; the
// Apple backend adapts to the `container` CLI's differences (no `wait`
// subcommand, no host-gateway alias, macOS-26-only).
//
// Both `container` and `docker` share almost the same run/logs/stop/kill
// argument syntax, so the backend mostly builds argument slices that the
// ContainerRunner executes via Binary(). The exceptions — availability checks
// and waiting for exit — differ enough that the backend owns them directly.
type ContainerBackend interface {
	// Name is the short identifier shown in the TUI and `bench status`.
	Name() string
	// Binary is the executable workbench invokes (e.g. "docker", "container").
	Binary() string
	// Available reports whether the backend can run containers now, with a
	// user-facing error explaining what to fix when it cannot.
	Available() error
	// RunArgs builds the args for a detached `run`.
	RunArgs(spec RunSpec) []string
	// LogsArgs builds the args to follow a container's logs.
	LogsArgs(id string) []string
	// StopArgs builds the args to gracefully stop a container.
	StopArgs(id string, timeout time.Duration) []string
	// KillArgs builds the args to force-kill a container.
	KillArgs(id string) []string
	// RemoveArgs builds the args to remove a container by name or id.
	RemoveArgs(target string, force bool) []string
	// WaitExit blocks until the container terminates and returns its exit code.
	// The strategy differs per backend (Docker `wait` vs polling `inspect`).
	WaitExit(id string) int
	// OTELHost is the hostname a container uses to reach a service on the macOS
	// host (e.g. the OTLP trace collector).
	OTELHost() string
}

// RunSpec captures everything needed to build a container `run` invocation.
// It is backend-agnostic; each backend renders it into its own argument list.
type RunSpec struct {
	Name    string
	Labels  []string
	Env     []string
	Ports   []string
	Volumes []string
	Network string
	Image   string
	Command []string
}

// containerPollInterval is how often the Apple backend polls `inspect` to
// detect that a container has stopped.
const containerPollInterval = 250 * time.Millisecond

// ResolveBackend selects the container backend from global config.
// config.BackendDocker and config.BackendApple are explicit; config.BackendAuto
// (the default) prefers Apple's `container` on Apple silicon when the binary is
// installed, otherwise Docker. Selection is pure and side-effect-free — the
// environment/daemon health check happens later in Available().
func ResolveBackend(g config.GlobalConfig) ContainerBackend {
	switch g.ContainerBackend {
	case config.BackendDocker:
		return dockerBackend{}
	case config.BackendApple:
		return newAppleBackend(g)
	default: // BackendAuto or unset
		if isAppleSilicon() && appleContainerInstalled() {
			return newAppleBackend(g)
		}
		return dockerBackend{}
	}
}

// buildRunArgs assembles a detached-run argument list shared by both backends.
// hostAlias is inserted after the labels: Docker uses it for the host-gateway
// alias; Apple passes nil. Argument order matches workbench's original Docker
// runner so the Docker path is unchanged.
func buildRunArgs(spec RunSpec, hostAlias []string) []string {
	args := []string{"run", "-d", "--name", spec.Name}
	for _, l := range spec.Labels {
		args = append(args, "--label", l)
	}
	args = append(args, hostAlias...)
	for _, e := range spec.Env {
		args = append(args, "-e", e)
	}
	for _, p := range spec.Ports {
		args = append(args, "-p", p)
	}
	for _, v := range spec.Volumes {
		args = append(args, "-v", v)
	}
	if spec.Network != "" {
		args = append(args, "--network", spec.Network)
	}
	args = append(args, spec.Image)
	args = append(args, spec.Command...)
	return args
}

func isAppleSilicon() bool {
	return runtime.GOOS == "darwin" && runtime.GOARCH == "arm64"
}

func appleContainerInstalled() bool {
	_, err := exec.LookPath("container")
	return err == nil
}
