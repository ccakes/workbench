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

// RunnerInfo holds runtime details that differ between process and container runners.
type RunnerInfo struct {
	Type        string // "process" or "container"
	PID         int
	ContainerID string
	Image       string
	Ports       []string
}
