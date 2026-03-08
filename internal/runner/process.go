package runner

import (
	"bufio"
	"fmt"
	"io"
	"os/exec"
	"sync"
	"syscall"
	"time"

	"github.com/ccakes/bench/internal/config"
	"github.com/ccakes/bench/internal/events"
	"github.com/ccakes/bench/internal/logbuf"
)

// ProcessRunner manages an OS process lifecycle.
type ProcessRunner struct {
	cfg config.ServiceConfig
	cmd *exec.Cmd
	pid int
}

func NewProcessRunner(cfg config.ServiceConfig) *ProcessRunner {
	return &ProcessRunner{cfg: cfg}
}

func (r *ProcessRunner) Start(env []string, logs *logbuf.Buffer, bus *events.Bus, key string) (<-chan int, error) {
	cmd := exec.Command(r.cfg.Command.Parts[0], r.cfg.Command.Parts[1:]...)
	cmd.Dir = r.cfg.Dir
	cmd.Env = env
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}

	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, fmt.Errorf("stdout pipe: %w", err)
	}
	stderr, err := cmd.StderrPipe()
	if err != nil {
		return nil, fmt.Errorf("stderr pipe: %w", err)
	}

	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("starting process: %w", err)
	}

	r.cmd = cmd
	r.pid = cmd.Process.Pid

	var pipeWg sync.WaitGroup
	pipeWg.Add(2)
	go readPipe(logs, bus, key, stdout, "stdout", events.StreamStdout, &pipeWg)
	go readPipe(logs, bus, key, stderr, "stderr", events.StreamStderr, &pipeWg)

	exitCh := make(chan int, 1)
	go func() {
		pipeWg.Wait()
		err := cmd.Wait()
		code := 0
		if err != nil {
			if exitErr, ok := err.(*exec.ExitError); ok {
				code = exitErr.ExitCode()
			} else {
				code = -1
			}
		}
		exitCh <- code
	}()

	return exitCh, nil
}

func (r *ProcessRunner) Stop(exitCh <-chan int, timeout time.Duration) {
	if r.cmd == nil || r.cmd.Process == nil {
		return
	}
	pid := r.cmd.Process.Pid
	_ = syscall.Kill(-pid, syscall.SIGTERM)

	select {
	case <-exitCh:
		return
	case <-time.After(timeout):
		_ = syscall.Kill(-pid, syscall.SIGKILL)
		<-exitCh
	}
}

func (r *ProcessRunner) Info() RunnerInfo {
	return RunnerInfo{
		Type: "process",
		PID:  r.pid,
	}
}

func readPipe(logs *logbuf.Buffer, bus *events.Bus, key string, rd io.ReadCloser, stream string, streamType events.Stream, wg *sync.WaitGroup) {
	defer wg.Done()
	scanner := bufio.NewScanner(rd)
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for scanner.Scan() {
		line := scanner.Text()
		logs.Add(stream, line)
		bus.Publish(events.Event{
			Type:    events.LogLine,
			Service: key,
			Data:    events.LogLineData{Stream: streamType, Line: line},
		})
	}
}
