package runner

import (
	"time"

	"github.com/ccakes/workbench/internal/events"
	"github.com/ccakes/workbench/internal/logbuf"
)

// Runner abstracts process vs container lifecycle management.
type Runner interface {
	// Start launches the service. Returns a channel that receives the exit code
	// when the service terminates. env is the full environment slice.
	Start(env []string, logs *logbuf.Buffer, bus *events.Bus, key string) (<-chan int, error)

	// Stop gracefully stops the service, waiting on exitCh for confirmation.
	// If the service doesn't exit within timeout, it is forcefully killed.
	Stop(exitCh <-chan int, timeout time.Duration)

	// Info returns runtime information about the runner.
	Info() RunnerInfo
}

// ContainerExecer is implemented by runners that can run a command inside the
// container they manage. The container_exec readiness probe uses it so a probe
// can target a service's own container without bench.yml naming either the
// container or the backend's CLI. ProcessRunner does not implement it, so a
// failed type assertion is how the probe detects a non-container service.
type ContainerExecer interface {
	// ExecCommand returns the binary and arguments that run cmd inside the
	// managed container.
	ExecCommand(cmd []string) (bin string, args []string)
}

// RunnerInfo holds runtime details that differ between process and container runners.
type RunnerInfo struct {
	Type        string // "process" or "container"
	PID         int
	ContainerID string
	Image       string
	Ports       []string
}
