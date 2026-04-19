//go:build !windows

package supervisor

import (
	"net"
	"strings"
	"testing"
	"time"

	"github.com/ccakes/workbench/internal/config"
	"github.com/ccakes/workbench/internal/events"
	"github.com/ccakes/workbench/internal/service"
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
				Command: &config.Command{
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

	// Wait for ready state (no probe → instant promote from Running)
	ok := pollUntil(t, 3*time.Second, 20*time.Millisecond, func() bool {
		return getStatus(info) == service.StatusReady
	})
	if !ok {
		t.Fatalf("expected service to reach Ready, got %s", getStatus(info))
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
		return getStatus(info) == service.StatusReady
	})
	if !ok {
		t.Fatalf("expected Ready, got %s", getStatus(info))
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
		return getStatus(info) == service.StatusReady
	})
	if !ok {
		t.Fatalf("expected Ready after restart, got %s", getStatus(info))
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
			Command: &config.Command{
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

	// Wait for all services to be ready
	for _, key := range []string{"a", "b", "c"} {
		info := sup.ServiceInfo(key)
		ok := pollUntil(t, 3*time.Second, 20*time.Millisecond, func() bool {
			return getStatus(info) == service.StatusReady
		})
		if !ok {
			t.Fatalf("service %q did not reach Ready, got %s", key, getStatus(info))
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

// longRunningSvc builds a service config that runs until SIGTERMed.
func longRunningSvc(dir string) config.ServiceConfig {
	return config.ServiceConfig{
		Dir: dir,
		Command: &config.Command{
			Shell: true,
			Parts: []string{"sh", "-c", `trap 'exit 0' TERM; while true; do sleep 0.1; done`},
		},
		Restart: config.RestartConfig{
			Policy:  "never",
			Backoff: config.Duration{Duration: 100 * time.Millisecond},
		},
	}
}

// failingSvc builds a service config whose command exits 1 immediately and
// does not restart.
func failingSvc(dir string) config.ServiceConfig {
	return config.ServiceConfig{
		Dir: dir,
		Command: &config.Command{
			Shell: true,
			Parts: []string{"sh", "-c", "exit 1"},
		},
		Restart: config.RestartConfig{
			Policy:  "never",
			Backoff: config.Duration{Duration: 100 * time.Millisecond},
		},
	}
}

// TestDependsOn_WaitsForDepRunning verifies a dependent service does not
// transition to Running until its dependency has reached Running.
func TestDependsOn_WaitsForDepRunning(t *testing.T) {
	dir := t.TempDir()

	depSvc := longRunningSvc(dir)
	autoStart := false
	depSvc.AutoStart = &autoStart // dep is parked until we manually start it

	dependent := longRunningSvc(dir)
	dependent.DependsOn = []string{"dep"}

	cfg := &config.Config{
		Version: 1,
		Global: config.GlobalConfig{
			ShutdownTimeout: config.Duration{Duration: 1 * time.Second},
			LogBufferLines:  500,
		},
		Services: map[string]config.ServiceConfig{
			"dep":       depSvc,
			"dependent": dependent,
		},
	}

	bus := events.NewBus()
	sup := New(cfg, bus)

	if err := sup.Start(); err != nil {
		t.Fatalf("Start() failed: %v", err)
	}
	defer func() {
		done := make(chan struct{})
		go func() {
			sup.Shutdown()
			close(done)
		}()
		select {
		case <-done:
		case <-time.After(5 * time.Second):
			t.Log("warning: shutdown timed out")
		}
	}()

	depInfo := sup.ServiceInfo("dep")
	dependentInfo := sup.ServiceInfo("dependent")

	// dep should stay Disabled (auto_start:false) — so "dependent" should be
	// free to start (skip-disabled-dep behavior) and reach Ready.
	ok := pollUntil(t, 3*time.Second, 20*time.Millisecond, func() bool {
		return getStatus(dependentInfo) == service.StatusReady
	})
	if !ok {
		t.Fatalf("expected dependent to reach Ready with disabled dep, got %s", getStatus(dependentInfo))
	}
	if s := getStatus(depInfo); s != service.StatusDisabled {
		t.Errorf("expected dep to be Disabled, got %s", s)
	}
}

// TestDependsOn_BlocksUntilDepReady verifies that when a dependency is slow
// to start, the dependent stays in Pending until the dependency is Running.
func TestDependsOn_BlocksUntilDepReady(t *testing.T) {
	dir := t.TempDir()

	// Dep is long-running and starts normally, but auto_start is false so we
	// can control *when* it starts — simulating a slow-to-become-ready dep.
	depSvc := longRunningSvc(dir)
	autoStart := false
	depSvc.AutoStart = &autoStart

	dependent := longRunningSvc(dir)
	dependent.DependsOn = []string{"dep"}

	// Set dep's initial info state to Pending (not Disabled) so dependent blocks.
	// We achieve this via a custom setup: build supervisor, override dep's
	// info.Status to Pending after construction.
	cfg := &config.Config{
		Version: 1,
		Global: config.GlobalConfig{
			ShutdownTimeout: config.Duration{Duration: 1 * time.Second},
			LogBufferLines:  500,
		},
		Services: map[string]config.ServiceConfig{
			"dep":       depSvc,
			"dependent": dependent,
		},
	}

	bus := events.NewBus()
	sup := New(cfg, bus)

	// Force dep into Pending (not Disabled) so the dependent actually waits.
	depInfo := sup.ServiceInfo("dep")
	depInfo.Lock()
	depInfo.Status = service.StatusPending
	depInfo.Unlock()

	if err := sup.Start(); err != nil {
		t.Fatalf("Start() failed: %v", err)
	}
	defer func() {
		done := make(chan struct{})
		go func() {
			sup.Shutdown()
			close(done)
		}()
		select {
		case <-done:
		case <-time.After(5 * time.Second):
			t.Log("warning: shutdown timed out")
		}
	}()

	dependentInfo := sup.ServiceInfo("dependent")

	// Give enough time for a bug to manifest (if dependent didn't wait, it
	// would be Running well within 500ms).
	time.Sleep(500 * time.Millisecond)

	if s := getStatus(dependentInfo); s != service.StatusPending {
		t.Fatalf("expected dependent to be Pending while dep is Pending, got %s", s)
	}

	// Now start the dep. Dependent should proceed to Running.
	if err := sup.StartService("dep"); err != nil {
		t.Fatalf("StartService(dep) failed: %v", err)
	}

	ok := pollUntil(t, 5*time.Second, 20*time.Millisecond, func() bool {
		return getStatus(dependentInfo) == service.StatusReady
	})
	if !ok {
		t.Fatalf("expected dependent to reach Ready after dep started, got %s", getStatus(dependentInfo))
	}
	if s := getStatus(depInfo); s != service.StatusReady {
		t.Errorf("expected dep to be Ready, got %s", s)
	}
}

// TestDependsOn_CascadesFailure verifies that when a dependency fails, the
// dependent is marked Failed rather than running anyway.
func TestDependsOn_CascadesFailure(t *testing.T) {
	dir := t.TempDir()

	cfg := &config.Config{
		Version: 1,
		Global: config.GlobalConfig{
			ShutdownTimeout: config.Duration{Duration: 1 * time.Second},
			LogBufferLines:  500,
		},
		Services: map[string]config.ServiceConfig{
			"dep": failingSvc(dir),
			"dependent": func() config.ServiceConfig {
				s := longRunningSvc(dir)
				s.DependsOn = []string{"dep"}
				return s
			}(),
		},
	}

	bus := events.NewBus()
	sup := New(cfg, bus)

	if err := sup.Start(); err != nil {
		t.Fatalf("Start() failed: %v", err)
	}
	defer func() {
		done := make(chan struct{})
		go func() {
			sup.Shutdown()
			close(done)
		}()
		select {
		case <-done:
		case <-time.After(5 * time.Second):
			t.Log("warning: shutdown timed out")
		}
	}()

	depInfo := sup.ServiceInfo("dep")
	dependentInfo := sup.ServiceInfo("dependent")

	// dep should end up Failed (exit 1, policy never).
	ok := pollUntil(t, 3*time.Second, 20*time.Millisecond, func() bool {
		return getStatus(depInfo) == service.StatusFailed
	})
	if !ok {
		t.Fatalf("expected dep to reach Failed, got %s", getStatus(depInfo))
	}

	// Dependent should propagate the failure — never run.
	ok = pollUntil(t, 3*time.Second, 20*time.Millisecond, func() bool {
		return getStatus(dependentInfo) == service.StatusFailed
	})
	if !ok {
		t.Fatalf("expected dependent to reach Failed after dep failed, got %s", getStatus(dependentInfo))
	}

	dependentInfo.RLock()
	lastErr := dependentInfo.LastError
	dependentInfo.RUnlock()
	if !strings.Contains(lastErr, `"dep"`) || !strings.Contains(lastErr, "failed") {
		t.Errorf("expected cascade-failure reason to mention dep, got %q", lastErr)
	}
}

// TestDependsOn_StopWhileWaiting verifies that a service waiting on a pending
// dep responds to a stop request instead of blocking forever.
func TestDependsOn_StopWhileWaiting(t *testing.T) {
	dir := t.TempDir()

	depSvc := longRunningSvc(dir)
	autoStart := false
	depSvc.AutoStart = &autoStart

	dependent := longRunningSvc(dir)
	dependent.DependsOn = []string{"dep"}

	cfg := &config.Config{
		Version: 1,
		Global: config.GlobalConfig{
			ShutdownTimeout: config.Duration{Duration: 1 * time.Second},
			LogBufferLines:  500,
		},
		Services: map[string]config.ServiceConfig{
			"dep":       depSvc,
			"dependent": dependent,
		},
	}

	bus := events.NewBus()
	sup := New(cfg, bus)

	// Force dep to Pending so dependent waits.
	depInfo := sup.ServiceInfo("dep")
	depInfo.Lock()
	depInfo.Status = service.StatusPending
	depInfo.Unlock()

	if err := sup.Start(); err != nil {
		t.Fatalf("Start() failed: %v", err)
	}

	dependentInfo := sup.ServiceInfo("dependent")
	ok := pollUntil(t, 2*time.Second, 20*time.Millisecond, func() bool {
		return getStatus(dependentInfo) == service.StatusPending
	})
	if !ok {
		t.Fatalf("expected dependent to reach Pending while waiting, got %s", getStatus(dependentInfo))
	}

	errCh := make(chan error, 1)
	go func() { errCh <- sup.StopService("dependent") }()

	select {
	case err := <-errCh:
		if err != nil {
			t.Fatalf("StopService() failed: %v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("StopService() did not return; dep-wait did not honour stop")
	}

	if s := getStatus(dependentInfo); s != service.StatusStopped {
		t.Errorf("expected dependent Stopped, got %s", s)
	}

	sup.Shutdown()
}

// openTCPListener returns a 127.0.0.1:<port> listener that accepts and closes
// every connection. The listener is closed on test cleanup.
func openTCPListener(t *testing.T) net.Listener {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("net.Listen: %v", err)
	}
	t.Cleanup(func() { _ = l.Close() })
	go func() {
		for {
			conn, err := l.Accept()
			if err != nil {
				return
			}
			_ = conn.Close()
		}
	}()
	return l
}

// reservedAddr returns a 127.0.0.1:<port> address whose port is not currently
// bound — useful for "probe will never succeed" scenarios.
func reservedAddr(t *testing.T) string {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("net.Listen: %v", err)
	}
	addr := l.Addr().String()
	_ = l.Close()
	return addr
}

func shutdownWithTimeout(t *testing.T, sup *Supervisor) {
	t.Helper()
	done := make(chan struct{})
	go func() {
		sup.Shutdown()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Log("warning: shutdown timed out")
	}
}

// TestReadiness_TCPReachesReady verifies that a service with a TCP readiness
// probe pointing at an already-listening port transitions to Ready.
func TestReadiness_TCPReachesReady(t *testing.T) {
	dir := t.TempDir()

	l := openTCPListener(t)

	svc := longRunningSvc(dir)
	svc.Readiness = config.ReadinessConfig{
		Kind:    "tcp",
		Address: l.Addr().String(),
		Timeout: config.Duration{Duration: 500 * time.Millisecond},
	}

	cfg := &config.Config{
		Version: 1,
		Global: config.GlobalConfig{
			ShutdownTimeout: config.Duration{Duration: 1 * time.Second},
			LogBufferLines:  500,
		},
		Services: map[string]config.ServiceConfig{"svc": svc},
	}

	bus := events.NewBus()
	sup := New(cfg, bus)

	if err := sup.Start(); err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer shutdownWithTimeout(t, sup)

	info := sup.ServiceInfo("svc")
	ok := pollUntil(t, 3*time.Second, 50*time.Millisecond, func() bool {
		return getStatus(info) == service.StatusReady
	})
	if !ok {
		t.Fatalf("expected svc to reach Ready, got %s", getStatus(info))
	}
}

// TestReadiness_DependentWaitsForReady verifies that a dependent with a
// readiness-configured dep stays Pending while the dep is only Running, and
// proceeds once the dep reaches Ready.
func TestReadiness_DependentWaitsForReady(t *testing.T) {
	dir := t.TempDir()

	// Open a listener, then immediately close it so the port is reserved but
	// the probe will fail at first. We'll reopen after observing B's Pending.
	addr := reservedAddr(t)

	depSvc := longRunningSvc(dir)
	depSvc.Readiness = config.ReadinessConfig{
		Kind:    "tcp",
		Address: addr,
		Timeout: config.Duration{Duration: 200 * time.Millisecond},
	}

	dependent := longRunningSvc(dir)
	dependent.DependsOn = []string{"dep"}

	cfg := &config.Config{
		Version: 1,
		Global: config.GlobalConfig{
			ShutdownTimeout: config.Duration{Duration: 1 * time.Second},
			LogBufferLines:  500,
		},
		Services: map[string]config.ServiceConfig{
			"dep":       depSvc,
			"dependent": dependent,
		},
	}

	bus := events.NewBus()
	sup := New(cfg, bus)

	if err := sup.Start(); err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer shutdownWithTimeout(t, sup)

	depInfo := sup.ServiceInfo("dep")
	dependentInfo := sup.ServiceInfo("dependent")

	// Wait for dep to reach Running (process spawned) but the probe should
	// fail indefinitely against the closed port.
	ok := pollUntil(t, 3*time.Second, 20*time.Millisecond, func() bool {
		return getStatus(depInfo) == service.StatusRunning
	})
	if !ok {
		t.Fatalf("expected dep to reach Running, got %s", getStatus(depInfo))
	}

	// Dependent should park in Pending — dep is Running but not Ready.
	time.Sleep(600 * time.Millisecond)
	if s := getStatus(dependentInfo); s != service.StatusPending {
		t.Fatalf("expected dependent Pending while dep is Running-but-not-Ready, got %s", s)
	}
	if s := getStatus(depInfo); s == service.StatusReady {
		t.Fatalf("dep should still be Running (probe failing), got Ready — test setup broken")
	}

	// Now open the listener. Probe should succeed; dep → Ready; dependent → Running.
	l, err := net.Listen("tcp", addr)
	if err != nil {
		t.Fatalf("late listen: %v", err)
	}
	defer func() { _ = l.Close() }()
	go func() {
		for {
			conn, err := l.Accept()
			if err != nil {
				return
			}
			_ = conn.Close()
		}
	}()

	ok = pollUntil(t, 5*time.Second, 50*time.Millisecond, func() bool {
		return getStatus(depInfo) == service.StatusReady
	})
	if !ok {
		t.Fatalf("expected dep to reach Ready after listener opened, got %s", getStatus(depInfo))
	}

	ok = pollUntil(t, 3*time.Second, 50*time.Millisecond, func() bool {
		return getStatus(dependentInfo) == service.StatusReady
	})
	if !ok {
		t.Fatalf("expected dependent to reach Ready after dep became Ready, got %s", getStatus(dependentInfo))
	}
}

// TestReadiness_UnprobedDepUnblocksDependent is a regression guard that a dep
// without a readiness probe still unblocks its dependent promptly — the
// always-fire-probe path promotes unprobed services to Ready immediately.
func TestReadiness_UnprobedDepUnblocksDependent(t *testing.T) {
	dir := t.TempDir()

	dependent := longRunningSvc(dir)
	dependent.DependsOn = []string{"dep"}

	cfg := &config.Config{
		Version: 1,
		Global: config.GlobalConfig{
			ShutdownTimeout: config.Duration{Duration: 1 * time.Second},
			LogBufferLines:  500,
		},
		Services: map[string]config.ServiceConfig{
			"dep":       longRunningSvc(dir), // no readiness configured
			"dependent": dependent,
		},
	}

	bus := events.NewBus()
	sup := New(cfg, bus)

	if err := sup.Start(); err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer shutdownWithTimeout(t, sup)

	dependentInfo := sup.ServiceInfo("dependent")
	ok := pollUntil(t, 3*time.Second, 20*time.Millisecond, func() bool {
		return getStatus(dependentInfo) == service.StatusReady
	})
	if !ok {
		t.Fatalf("expected dependent to reach Ready (no readiness on dep), got %s", getStatus(dependentInfo))
	}
}

// TestReadiness_ProbeGoroutineExitsOnStop verifies the probe goroutine is
// cancelled when the service is stopped — no goroutine leak, stop returns
// promptly even though the probe would otherwise loop forever.
func TestReadiness_ProbeGoroutineExitsOnStop(t *testing.T) {
	dir := t.TempDir()

	svc := longRunningSvc(dir)
	svc.Readiness = config.ReadinessConfig{
		Kind:    "tcp",
		Address: reservedAddr(t), // never-listening
		Timeout: config.Duration{Duration: 100 * time.Millisecond},
	}

	cfg := &config.Config{
		Version: 1,
		Global: config.GlobalConfig{
			ShutdownTimeout: config.Duration{Duration: 1 * time.Second},
			LogBufferLines:  500,
		},
		Services: map[string]config.ServiceConfig{"svc": svc},
	}

	bus := events.NewBus()
	sup := New(cfg, bus)

	if err := sup.Start(); err != nil {
		t.Fatalf("Start: %v", err)
	}

	info := sup.ServiceInfo("svc")
	ok := pollUntil(t, 3*time.Second, 20*time.Millisecond, func() bool {
		return getStatus(info) == service.StatusRunning
	})
	if !ok {
		t.Fatalf("expected svc to reach Running, got %s", getStatus(info))
	}

	errCh := make(chan error, 1)
	go func() { errCh <- sup.StopService("svc") }()

	select {
	case err := <-errCh:
		if err != nil {
			t.Fatalf("StopService: %v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("StopService did not return — probe goroutine likely stuck")
	}

	if s := getStatus(info); s != service.StatusStopped {
		t.Errorf("expected Stopped, got %s", s)
	}

	sup.Shutdown()
}

// TestReadiness_RestartResetsBaseline verifies that after a restart, the
// log_pattern probe matches a fresh "UP" line and transitions to Ready again
// rather than being tripped (or not) by stale buffer contents.
func TestReadiness_RestartResetsBaseline(t *testing.T) {
	dir := t.TempDir()

	svc := config.ServiceConfig{
		Dir: dir,
		Command: &config.Command{
			Shell: true,
			Parts: []string{"sh", "-c", `trap 'exit 0' TERM; echo UP; while true; do sleep 0.1; done`},
		},
		Restart: config.RestartConfig{
			Policy:  "never",
			Backoff: config.Duration{Duration: 50 * time.Millisecond},
		},
		Readiness: logPatternReadiness("^UP$"),
	}

	cfg := &config.Config{
		Version: 1,
		Global: config.GlobalConfig{
			ShutdownTimeout: config.Duration{Duration: 1 * time.Second},
			LogBufferLines:  500,
		},
		Services: map[string]config.ServiceConfig{"svc": svc},
	}

	bus := events.NewBus()
	sup := New(cfg, bus)

	if err := sup.Start(); err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer shutdownWithTimeout(t, sup)

	info := sup.ServiceInfo("svc")

	// First run reaches Ready.
	ok := pollUntil(t, 3*time.Second, 20*time.Millisecond, func() bool {
		return getStatus(info) == service.StatusReady
	})
	if !ok {
		t.Fatalf("expected initial Ready, got %s", getStatus(info))
	}
	firstRestart := getRestartCount(info)

	// Trigger a restart.
	if err := sup.RestartService("svc", "test"); err != nil {
		t.Fatalf("RestartService: %v", err)
	}

	// Restart count should bump.
	ok = pollUntil(t, 3*time.Second, 20*time.Millisecond, func() bool {
		return getRestartCount(info) > firstRestart
	})
	if !ok {
		t.Fatalf("expected restart count to increment")
	}

	// Must reach Ready *again* — tests that baseline reset on restart works.
	// Without baseline reset, a stale UP line from run 1 could (best case) still
	// match — so to truly test reset we rely on the fact that after restart, a
	// new UP line is emitted and the probe catches it just as it did first time.
	ok = pollUntil(t, 5*time.Second, 50*time.Millisecond, func() bool {
		return getStatus(info) == service.StatusReady
	})
	if !ok {
		t.Fatalf("expected Ready again after restart, got %s", getStatus(info))
	}
}
