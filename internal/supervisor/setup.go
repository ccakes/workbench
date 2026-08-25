package supervisor

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"os/exec"
	"time"

	"github.com/ccakes/workbench/internal/config"
	"github.com/ccakes/workbench/internal/runner"
)

const setupDefaultTimeout = 60 * time.Second

// runSetupHook executes the configured setup command for a service after its
// readiness probe has passed. The hook's stdout/stderr are appended to the
// service's log buffer with a `setup` stream tag so the user can see what
// happened. Host-exec env layers on top of the service's resolved env:
// process env -> global env_file -> global env -> service env_file -> service
// env -> setup env. Container exec inherits the running container's env.
//
// Returns nil on exit 0. Returns a non-nil error on non-zero exit, timeout,
// context cancellation, or if the env or working dir can't be resolved.
func (s *Supervisor) runSetupHook(ctx context.Context, ms *managedService, execer runner.ContainerExecer) error {
	cfg := ms.cfg.Setup
	if cfg == nil {
		return fmt.Errorf("missing config")
	}
	bin, args, err := resolveExec(*cfg, execer)
	if err != nil {
		return err
	}

	timeout := cfg.Timeout.Duration
	if timeout <= 0 {
		timeout = setupDefaultTimeout
	}
	cmdCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	cmd := exec.CommandContext(cmdCtx, bin, args...)
	cmd.Dir = ms.cfg.Dir
	if cfg.Kind == config.ExecKind {
		env, err := s.buildEnv(ms)
		if err != nil {
			return fmt.Errorf("building env: %w", err)
		}
		for k, v := range cfg.Env {
			env = append(env, k+"="+v)
		}
		cmd.Env = env
	}

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
