package runner

import (
	"fmt"
	"os/exec"
	"strings"
	"sync"
	"time"

	"github.com/ccakes/bench/internal/config"
	"github.com/ccakes/bench/internal/events"
	"github.com/ccakes/bench/internal/logbuf"
)

// ContainerRunner manages a Docker container lifecycle.
type ContainerRunner struct {
	cfg         config.ServiceConfig
	containerID string
	name        string
	logCmd      *exec.Cmd
}

func NewContainerRunner(cfg config.ServiceConfig, serviceKey string) *ContainerRunner {
	return &ContainerRunner{
		cfg:  cfg,
		name: "bench-" + serviceKey,
	}
}

func (r *ContainerRunner) Start(env []string, logs *logbuf.Buffer, bus *events.Bus, key string) (<-chan int, error) {
	cc := r.cfg.Container

	// Clean up any stale container with same name
	cleanup := exec.Command("docker", "rm", "-f", r.name)
	_ = cleanup.Run() // ignore errors — container may not exist

	// Build docker run args
	args := []string{"run", "-d", "--name", r.name, "--label", "managed-by=bench"}

	// Environment variables from env slice (already merged by supervisor)
	for _, e := range env {
		// Only pass non-system env vars — filter to config-specified keys
		args = append(args, "-e", e)
	}

	for _, p := range cc.Ports {
		args = append(args, "-p", p)
	}
	for _, v := range cc.Volumes {
		args = append(args, "-v", v)
	}
	if cc.Network != "" {
		args = append(args, "--network", cc.Network)
	}

	args = append(args, cc.Image)

	if len(cc.Command.Parts) > 0 {
		args = append(args, cc.Command.Parts...)
	}

	// Run container
	cmd := exec.Command("docker", args...)
	out, err := cmd.Output()
	if err != nil {
		if exitErr, ok := err.(*exec.ExitError); ok {
			return nil, fmt.Errorf("docker run failed: %s", strings.TrimSpace(string(exitErr.Stderr)))
		}
		return nil, fmt.Errorf("docker run: %w", err)
	}
	r.containerID = strings.TrimSpace(string(out))
	// containerID stores the full ID; Info() returns the short form

	// Stream logs
	r.logCmd = exec.Command("docker", "logs", "--follow", r.containerID)
	stdout, err := r.logCmd.StdoutPipe()
	if err != nil {
		return nil, fmt.Errorf("docker logs stdout pipe: %w", err)
	}
	stderr, err := r.logCmd.StderrPipe()
	if err != nil {
		return nil, fmt.Errorf("docker logs stderr pipe: %w", err)
	}
	if err := r.logCmd.Start(); err != nil {
		return nil, fmt.Errorf("docker logs: %w", err)
	}

	var pipeWg sync.WaitGroup
	pipeWg.Add(2)
	go readPipe(logs, bus, key, stdout, "stdout", events.StreamStdout, &pipeWg)
	go readPipe(logs, bus, key, stderr, "stderr", events.StreamStderr, &pipeWg)

	// Wait for container exit
	exitCh := make(chan int, 1)
	go func() {
		// docker wait returns the exit code
		waitCmd := exec.Command("docker", "wait", r.containerID)
		out, err := waitCmd.Output()
		// Once container exits, log streaming will end naturally
		pipeWg.Wait()

		code := 0
		if err != nil {
			code = -1
		} else {
			trimmed := strings.TrimSpace(string(out))
			_, _ = fmt.Sscanf(trimmed, "%d", &code)
		}

		// Clean up log follower
		if r.logCmd.Process != nil {
			_ = r.logCmd.Process.Kill()
			_ = r.logCmd.Wait()
		}

		exitCh <- code
	}()

	return exitCh, nil
}

func (r *ContainerRunner) Stop(exitCh <-chan int, timeout time.Duration) {
	if r.containerID == "" {
		return
	}

	timeoutSecs := fmt.Sprintf("%d", int(timeout.Seconds()))
	stopCmd := exec.Command("docker", "stop", "-t", timeoutSecs, r.containerID)
	_ = stopCmd.Run()

	// Wait for exit with a grace period beyond the docker stop timeout
	select {
	case <-exitCh:
	case <-time.After(timeout + 5*time.Second):
		killCmd := exec.Command("docker", "kill", r.containerID)
		_ = killCmd.Run()
		<-exitCh
	}

	// Remove container
	rmCmd := exec.Command("docker", "rm", r.containerID)
	_ = rmCmd.Run()
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

