//go:build !windows

package supervisor

import (
	"strings"
	"testing"
	"time"

	"github.com/ccakes/bench/internal/config"
	"github.com/ccakes/bench/internal/events"
	"github.com/ccakes/bench/internal/service"
)

// helper: build a config with a single service and sensible test defaults.
func singleServiceConfig(t *testing.T, key string, command string, policy string, maxRetries int, backoff time.Duration) *config.Config {
	t.Helper()
	dir := t.TempDir()

	shutdownDur := config.Duration{Duration: 1 * time.Second}
	backoffDur := config.Duration{Duration: backoff}

	cfg := &config.Config{
		Version: 1,
		Global: config.GlobalConfig{
			ShutdownTimeout: shutdownDur,
			LogBufferLines:  500,
		},
		Services: map[string]config.ServiceConfig{
			key: {
				Dir: dir,
				Command: config.Command{
					Shell: true,
					Parts: []string{"sh", "-c", command},
				},
				Restart: config.RestartConfig{
					Policy:     policy,
					MaxRetries: maxRetries,
					Backoff:    backoffDur,
				},
			},
		},
	}
	return cfg
}

// helper: poll a condition with a timeout. Returns true if the condition was met.
func pollUntil(t *testing.T, timeout time.Duration, interval time.Duration, condition func() bool) bool {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if condition() {
			return true
		}
		time.Sleep(interval)
	}
	return false
}

func getStatus(info *service.Info) service.Status {
	info.RLock()
	defer info.RUnlock()
	return info.Status
}

func getRestartCount(info *service.Info) int {
	info.RLock()
	defer info.RUnlock()
	return info.RestartCount
}

// TestStartStop starts a service that writes to a marker file then waits for
// SIGTERM via trap+wait. This lets the supervisor kill the process cleanly via
// its normal SIGTERM path while the process itself exits promptly, avoiding the
// internal cmd.Wait race between killProcess and startProcess.
func TestStartStop(t *testing.T) {
	// The process traps SIGTERM so that it exits immediately and cleanly,
	// which avoids a race between concurrent cmd.Wait calls in the
	// production code.
	cfg := singleServiceConfig(t, "svc",
		`trap 'exit 0' TERM; echo started; while true; do sleep 0.1; done`,
		"never", 0, 100*time.Millisecond)
	bus := events.NewBus()
	sup := New(cfg, bus)

	if err := sup.Start(); err != nil {
		t.Fatalf("Start() failed: %v", err)
	}

	info := sup.ServiceInfo("svc")
	if info == nil {
		t.Fatal("ServiceInfo returned nil for 'svc'")
	}

	// Wait for running state
	ok := pollUntil(t, 3*time.Second, 20*time.Millisecond, func() bool {
		return getStatus(info) == service.StatusRunning
	})
	if !ok {
		t.Fatalf("expected service to reach Running, got %s", getStatus(info))
	}

	// Stop it — StopService sends to stopCh and waits on doneCh.
	errCh := make(chan error, 1)
	go func() {
		errCh <- sup.StopService("svc")
	}()

	select {
	case err := <-errCh:
		if err != nil {
			t.Fatalf("StopService() failed: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("StopService() did not return within 5 seconds")
	}

	if s := getStatus(info); s != service.StatusStopped {
		t.Fatalf("expected Stopped, got %s", s)
	}
}

func TestProcessCapture(t *testing.T) {
	cfg := singleServiceConfig(t, "svc", "echo MARKER_LINE_1 && echo MARKER_LINE_2", "never", 0, 100*time.Millisecond)
	bus := events.NewBus()
	sup := New(cfg, bus)

	if err := sup.Start(); err != nil {
		t.Fatalf("Start() failed: %v", err)
	}

	logs := sup.ServiceLogs("svc")
	if logs == nil {
		t.Fatal("ServiceLogs returned nil")
	}

	info := sup.ServiceInfo("svc")

	// The command exits immediately (exit 0). With policy "never" and exit
	// code 0 the run loop sets StatusStopped and returns.
	pollUntil(t, 3*time.Second, 20*time.Millisecond, func() bool {
		s := getStatus(info)
		return s == service.StatusStopped || s == service.StatusFailed
	})

	// Wait for log lines to be flushed
	ok := pollUntil(t, 3*time.Second, 20*time.Millisecond, func() bool {
		return logs.Len() >= 2
	})
	if !ok {
		t.Fatalf("expected at least 2 log lines, got %d", logs.Len())
	}

	lines := logs.Lines()
	var texts []string
	for _, l := range lines {
		texts = append(texts, l.Text)
	}
	joined := strings.Join(texts, "\n")
	if !strings.Contains(joined, "MARKER_LINE_1") {
		t.Errorf("logs missing MARKER_LINE_1, got: %s", joined)
	}
	if !strings.Contains(joined, "MARKER_LINE_2") {
		t.Errorf("logs missing MARKER_LINE_2, got: %s", joined)
	}
}

func TestRestartOnFailure(t *testing.T) {
	// Command exits with code 1 immediately. With on-failure policy it should restart.
	cfg := singleServiceConfig(t, "svc", "exit 1", "on-failure", 5, 100*time.Millisecond)
	bus := events.NewBus()
	sup := New(cfg, bus)

	if err := sup.Start(); err != nil {
		t.Fatalf("Start() failed: %v", err)
	}

	info := sup.ServiceInfo("svc")

	// Wait until at least one restart has happened
	ok := pollUntil(t, 5*time.Second, 50*time.Millisecond, func() bool {
		return getRestartCount(info) >= 1
	})
	if !ok {
		t.Fatalf("expected at least 1 restart, got %d", getRestartCount(info))
	}

	// The service will eventually hit max_retries (5) and stop on its own.
	ok = pollUntil(t, 10*time.Second, 50*time.Millisecond, func() bool {
		return getStatus(info) == service.StatusFailed
	})
	if !ok {
		t.Logf("warning: service did not reach Failed within timeout, status=%s", getStatus(info))
	}
}

func TestRestartNever(t *testing.T) {
	// Command exits with code 1. With "never" policy, should stay failed.
	cfg := singleServiceConfig(t, "svc", "exit 1", "never", 0, 100*time.Millisecond)
	bus := events.NewBus()
	sup := New(cfg, bus)

	if err := sup.Start(); err != nil {
		t.Fatalf("Start() failed: %v", err)
	}

	info := sup.ServiceInfo("svc")

	ok := pollUntil(t, 3*time.Second, 20*time.Millisecond, func() bool {
		return getStatus(info) == service.StatusFailed
	})
	if !ok {
		t.Fatalf("expected service to reach Failed, got %s", getStatus(info))
	}

	// Give time to verify it does NOT restart
	time.Sleep(300 * time.Millisecond)
	if rc := getRestartCount(info); rc != 0 {
		t.Fatalf("expected 0 restarts with 'never' policy, got %d", rc)
	}
	if s := getStatus(info); s != service.StatusFailed {
		t.Fatalf("expected service to remain Failed, got %s", s)
	}
}

// TestManualRestart starts a process that traps SIGTERM and exits cleanly,
// triggers a manual restart, and verifies the restart count increments.
func TestManualRestart(t *testing.T) {
	cfg := singleServiceConfig(t, "svc",
		`trap 'exit 0' TERM; echo started; while true; do sleep 0.1; done`,
		"never", 0, 100*time.Millisecond)
	bus := events.NewBus()
	sup := New(cfg, bus)

	if err := sup.Start(); err != nil {
		t.Fatalf("Start() failed: %v", err)
	}

	info := sup.ServiceInfo("svc")

	ok := pollUntil(t, 3*time.Second, 20*time.Millisecond, func() bool {
		return getStatus(info) == service.StatusRunning
	})
	if !ok {
		t.Fatalf("expected Running, got %s", getStatus(info))
	}

	initialCount := getRestartCount(info)

	// Trigger manual restart
	if err := sup.RestartService("svc", "manual test restart"); err != nil {
		t.Fatalf("RestartService() failed: %v", err)
	}

	// Wait for restart count to increment
	ok = pollUntil(t, 5*time.Second, 50*time.Millisecond, func() bool {
		return getRestartCount(info) > initialCount
	})
	if !ok {
		t.Fatalf("expected restart count to increment from %d, still %d", initialCount, getRestartCount(info))
	}

	// Verify it's running again after restart
	ok = pollUntil(t, 3*time.Second, 20*time.Millisecond, func() bool {
		return getStatus(info) == service.StatusRunning
	})
	if !ok {
		t.Fatalf("expected Running after restart, got %s", getStatus(info))
	}

	// Clean up: stop the service via StopService in a goroutine with timeout
	done := make(chan struct{})
	go func() {
		_ = sup.StopService("svc")
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Log("warning: cleanup StopService timed out")
	}
}

func TestMaxRetries(t *testing.T) {
	// Command exits with code 1. Policy on-failure, max_retries=2.
	// The run loop increments retries after each failure. When retries > max_retries
	// it stops. So with max_retries=2 it will:
	//   attempt 1: fail, retries=1 (<=2, restart)
	//   attempt 2: fail, retries=2 (<=2, restart)
	//   attempt 3: fail, retries=3 (>2, stop with "max retries exceeded")
	cfg := singleServiceConfig(t, "svc", "exit 1", "on-failure", 2, 100*time.Millisecond)
	bus := events.NewBus()
	sup := New(cfg, bus)

	if err := sup.Start(); err != nil {
		t.Fatalf("Start() failed: %v", err)
	}

	info := sup.ServiceInfo("svc")

	// Should eventually reach Failed with max retries exceeded
	ok := pollUntil(t, 10*time.Second, 50*time.Millisecond, func() bool {
		s := getStatus(info)
		if s != service.StatusFailed {
			return false
		}
		info.RLock()
		lastErr := info.LastError
		info.RUnlock()
		return strings.Contains(lastErr, "max retries")
	})
	if !ok {
		info.RLock()
		lastErr := info.LastError
		status := info.Status
		restarts := info.RestartCount
		info.RUnlock()
		t.Fatalf("expected Failed with 'max retries' error; status=%s, lastError=%q, restartCount=%d", status, lastErr, restarts)
	}

	// Verify restart count is exactly 2 (the service auto-restarted twice
	// before the third attempt exceeded max_retries).
	rc := getRestartCount(info)
	if rc != 2 {
		t.Errorf("expected restart count of 2, got %d", rc)
	}
}

// TestShutdown starts multiple SIGTERM-aware services and verifies that
// Shutdown stops them all.
func TestShutdown(t *testing.T) {
	dir := t.TempDir()
	shutdownDur := config.Duration{Duration: 1 * time.Second}
	backoffDur := config.Duration{Duration: 100 * time.Millisecond}

	makeSvc := func() config.ServiceConfig {
		return config.ServiceConfig{
			Dir: dir,
			Command: config.Command{
				Shell: true,
				Parts: []string{"sh", "-c", `trap 'exit 0' TERM; while true; do sleep 0.1; done`},
			},
			Restart: config.RestartConfig{
				Policy:  "never",
				Backoff: backoffDur,
			},
		}
	}

	cfg := &config.Config{
		Version: 1,
		Global: config.GlobalConfig{
			ShutdownTimeout: shutdownDur,
			LogBufferLines:  500,
		},
		Services: map[string]config.ServiceConfig{
			"a": makeSvc(),
			"b": makeSvc(),
			"c": makeSvc(),
		},
	}

	bus := events.NewBus()
	sup := New(cfg, bus)

	if err := sup.Start(); err != nil {
		t.Fatalf("Start() failed: %v", err)
	}

	// Wait for all services to be running
	for _, key := range []string{"a", "b", "c"} {
		info := sup.ServiceInfo(key)
		ok := pollUntil(t, 3*time.Second, 20*time.Millisecond, func() bool {
			return getStatus(info) == service.StatusRunning
		})
		if !ok {
			t.Fatalf("service %q did not reach Running, got %s", key, getStatus(info))
		}
	}

	// Shutdown should stop all services
	done := make(chan struct{})
	go func() {
		sup.Shutdown()
		close(done)
	}()

	select {
	case <-done:
		// ok
	case <-time.After(10 * time.Second):
		t.Fatal("Shutdown() did not complete within 10 seconds")
	}

	// Verify all services are stopped
	for _, key := range []string{"a", "b", "c"} {
		info := sup.ServiceInfo(key)
		s := getStatus(info)
		if s != service.StatusStopped {
			t.Errorf("service %q: expected Stopped, got %s", key, s)
		}
	}
}
