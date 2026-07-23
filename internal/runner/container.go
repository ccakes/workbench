package runner

import (
	"fmt"
	"os/exec"
	"strings"
	"sync"
	"time"

	"github.com/ccakes/workbench/internal/config"
	"github.com/ccakes/workbench/internal/events"
	"github.com/ccakes/workbench/internal/logbuf"
)

// logDrainGrace is how long the exit goroutine waits for the log follower to
// drain naturally after the container stops before force-killing it. Docker's
// `logs --follow` exits on stop; some `container logs -f` cases may not.
const logDrainGrace = 2 * time.Second

// ContainerRunner manages a container lifecycle via a ContainerBackend
// (Docker or Apple's `container`).
type ContainerRunner struct {
	cfg         config.ServiceConfig
	backend     ContainerBackend
	containerID string
	name        string
	logCmd      *exec.Cmd
}

func NewContainerRunner(cfg config.ServiceConfig, serviceKey, prefix string, backend ContainerBackend) *ContainerRunner {
	return &ContainerRunner{
		cfg:     cfg,
		backend: backend,
		name:    prefix + "-" + serviceKey,
	}
}

func (r *ContainerRunner) Start(env []string, logs *logbuf.Buffer, bus *events.Bus, key string) (<-chan int, error) {
	cc := r.cfg.Container
	bin := r.backend.Binary()

	// Clean up any stale container with the same name (force removal).
	_ = exec.Command(bin, r.backend.RemoveArgs(r.name, true)...).Run() // ignore errors — container may not exist

	spec := RunSpec{
		Name:    r.name,
		Labels:  []string{"managed-by=bench"},
		Env:     env, // already merged by supervisor
		Ports:   cc.Ports,
		Volumes: cc.Volumes,
		Network: cc.Network,
		Image:   cc.Image,
		Command: cc.Command.Parts,
	}

	// Run container
	out, err := exec.Command(bin, r.backend.RunArgs(spec)...).Output()
	if err != nil {
		if exitErr, ok := err.(*exec.ExitError); ok {
			return nil, fmt.Errorf("%s run failed: %s", bin, strings.TrimSpace(string(exitErr.Stderr)))
		}
		return nil, fmt.Errorf("%s run: %w", bin, err)
	}
	r.containerID = strings.TrimSpace(string(out))
	// containerID stores the full ID; Info() returns the short form

	// Stream logs
	r.logCmd = exec.Command(bin, r.backend.LogsArgs(r.containerID)...)
	stdout, err := r.logCmd.StdoutPipe()
	if err != nil {
		return nil, fmt.Errorf("%s logs stdout pipe: %w", bin, err)
	}
	stderr, err := r.logCmd.StderrPipe()
	if err != nil {
		return nil, fmt.Errorf("%s logs stderr pipe: %w", bin, err)
	}
	if err := r.logCmd.Start(); err != nil {
		return nil, fmt.Errorf("%s logs: %w", bin, err)
	}

	var pipeWg sync.WaitGroup
	pipeWg.Add(2)
	go readPipe(logs, bus, key, stdout, "stdout", events.StreamStdout, &pipeWg)
	go readPipe(logs, bus, key, stderr, "stderr", events.StreamStderr, &pipeWg)

	logsDone := make(chan struct{})
	go func() {
		pipeWg.Wait()
		close(logsDone)
	}()

	// Wait for container exit
	exitCh := make(chan int, 1)
	go func() {
		code := r.backend.WaitExit(r.containerID)

		// The container has exited. Give the log follower a moment to drain
		// naturally (Docker's `logs --follow` exits on stop); if it doesn't,
		// force it down so the pipes close and readPipe returns.
		select {
		case <-logsDone:
		case <-time.After(logDrainGrace):
		}
		if r.logCmd.Process != nil {
			_ = r.logCmd.Process.Kill()
			_ = r.logCmd.Wait()
		}
		<-logsDone

		exitCh <- code
	}()

	return exitCh, nil
}

func (r *ContainerRunner) Stop(exitCh <-chan int, timeout time.Duration) {
	if r.containerID == "" {
		return
	}
	bin := r.backend.Binary()

	_ = exec.Command(bin, r.backend.StopArgs(r.containerID, timeout)...).Run()

	// Wait for exit with a grace period beyond the stop timeout
	select {
	case <-exitCh:
	case <-time.After(timeout + 5*time.Second):
		_ = exec.Command(bin, r.backend.KillArgs(r.containerID)...).Run()
		<-exitCh
	}

	// Remove the container so repeated starts/restarts don't leave it behind.
	// On Docker this also drops anonymous volumes (-v); the Apple backend has
	// no equivalent flag.
	_ = exec.Command(bin, r.backend.RemoveArgs(r.containerID, false)...).Run()
}

func (r *ContainerRunner) Info() RunnerInfo {
	shortID := r.containerID
	if len(shortID) > 12 {
		shortID = shortID[:12]
	}
	return RunnerInfo{
		Type:        "container",
		ContainerID: shortID,
		Image:       r.cfg.Container.Image,
		Ports:       r.cfg.Container.Ports,
	}
}
