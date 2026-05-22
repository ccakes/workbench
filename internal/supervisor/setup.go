package supervisor

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"os/exec"
	"time"
)

const setupDefaultTimeout = 60 * time.Second

// runSetupHook executes the configured setup command for a service after its
// readiness probe has passed. The hook's stdout/stderr are appended to the
// service's log buffer with a `setup` stream tag so the user can see what
// happened. Setup-hook env layers on top of the service's resolved env:
// process env -> global env_file -> global env -> service env_file -> service
// env -> setup env. The hook runs in the service's working directory.
//
// Returns nil on exit 0. Returns a non-nil error on non-zero exit, timeout,
// context cancellation, or if the env or working dir can't be resolved.
func (s *Supervisor) runSetupHook(ctx context.Context, ms *managedService) error {
	cfg := ms.cfg.Setup
	if cfg == nil || len(cfg.Command.Parts) == 0 {
		return errors.New("missing command")
	}

	timeout := cfg.Timeout.Duration
	if timeout <= 0 {
		timeout = setupDefaultTimeout
	}
	cmdCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	env, err := s.buildEnv(ms)
	if err != nil {
		return fmt.Errorf("building env: %w", err)
	}
	for k, v := range cfg.Env {
		env = append(env, k+"="+v)
	}

	parts := cfg.Command.Parts
	cmd := exec.CommandContext(cmdCtx, parts[0], parts[1:]...)
	cmd.Dir = ms.cfg.Dir
	cmd.Env = env

	outR, outW := io.Pipe()
	errR, errW := io.Pipe()
	cmd.Stdout = outW
	cmd.Stderr = errW
	done := make(chan struct{}, 2)
	go func() { streamSetupLines(outR, ms, "setup"); done <- struct{}{} }()
	go func() { streamSetupLines(errR, ms, "setup"); done <- struct{}{} }()

	runErr := cmd.Run()
	// Close pipes so the streaming goroutines drain and exit.
	_ = outW.Close()
	_ = errW.Close()
	<-done
	<-done

	if runErr != nil {
		if cmdCtx.Err() == context.DeadlineExceeded {
			return fmt.Errorf("timed out after %s", timeout)
		}
		return fmt.Errorf("exit: %w", runErr)
	}
	return nil
}

func streamSetupLines(r io.Reader, ms *managedService, stream string) {
	sc := bufio.NewScanner(r)
	for sc.Scan() {
		ms.logs.Add(stream, sc.Text())
	}
	_ = sc.Err() // ignore: pipe closure on cmd exit is normal
}
