package api

import (
	"encoding/json"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ccakes/workbench/internal/config"
	"github.com/ccakes/workbench/internal/events"
	"github.com/ccakes/workbench/internal/service"
	"github.com/ccakes/workbench/internal/spanbuf"
	"github.com/ccakes/workbench/internal/supervisor"
)

func testConfig(t *testing.T) *config.Config {
	t.Helper()
	dir := t.TempDir()
	// Write a trivial script that sleeps
	script := filepath.Join(dir, "run.sh")
	if err := os.WriteFile(script, []byte("#!/bin/sh\nexec sleep 60\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	return &config.Config{
		Version: 1,
		Global: config.GlobalConfig{
			LogBufferLines:  100,
			ShutdownTimeout: config.Duration{Duration: 5 * time.Second},
		},
		Services: map[string]config.ServiceConfig{
			"web": {
				Dir:     dir,
				Command: &config.Command{Shell: true, Parts: []string{"sh", "-c", "exec sleep 60"}},
				Restart: config.RestartConfig{Policy: "never", Backoff: config.Duration{Duration: time.Second}},
			},
			"api": {
				Dir:     dir,
				Command: &config.Command{Shell: true, Parts: []string{"sh", "-c", "exec sleep 60"}},
				Restart: config.RestartConfig{Policy: "never", Backoff: config.Duration{Duration: time.Second}},
			},
		},
	}
}

func setupServer(t *testing.T) (*Server, *Client, *supervisor.Supervisor) {
	t.Helper()
	cfg := testConfig(t)
	bus := events.NewBus()
	sup := supervisor.New(cfg, bus)

	sockPath := filepath.Join(t.TempDir(), "bench.sock")
	srv := New(sup, nil, sockPath, "test")
	if err := srv.Start(); err != nil {
		t.Fatalf("server start: %v", err)
	}
	t.Cleanup(srv.Shutdown)

	client := NewClient(sockPath)
	return srv, client, sup
}

func TestPing(t *testing.T) {
	_, client, _ := setupServer(t)

	data, err := client.Call("ping", nil)
	if err != nil {
		t.Fatalf("ping: %v", err)
	}
	var result map[string]string
	if err := json.Unmarshal(data, &result); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if result["version"] != "test" {
		t.Errorf("got version %q, want %q", result["version"], "test")
	}
}

func TestStatus(t *testing.T) {
	_, client, _ := setupServer(t)

	data, err := client.Call("status", nil)
	if err != nil {
		t.Fatalf("status: %v", err)
	}
	var result []ServiceStatus
	if err := json.Unmarshal(data, &result); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(result) != 2 {
		t.Fatalf("got %d services, want 2", len(result))
	}
	// Verify both services appear
	keys := map[string]bool{}
	for _, s := range result {
		keys[s.Key] = true
	}
	if !keys["web"] || !keys["api"] {
		t.Errorf("expected web and api services, got %v", keys)
	}
}

func TestStatusSingleService(t *testing.T) {
	_, client, _ := setupServer(t)

	data, err := client.Call("status", map[string]string{"service": "web"})
	if err != nil {
		t.Fatalf("status: %v", err)
	}
	var result ServiceStatus
	if err := json.Unmarshal(data, &result); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if result.Key != "web" {
		t.Errorf("got key %q, want %q", result.Key, "web")
	}
}

func TestStatusUnknownService(t *testing.T) {
	_, client, _ := setupServer(t)

	_, err := client.Call("status", map[string]string{"service": "unknown"})
	if err == nil {
		t.Fatal("expected error for unknown service")
	}
}

func TestUnknownMethod(t *testing.T) {
	_, client, _ := setupServer(t)

	_, err := client.Call("nonexistent", nil)
	if err == nil {
		t.Fatal("expected error for unknown method")
	}
}

func TestStartStop(t *testing.T) {
	_, client, sup := setupServer(t)

	// Start the service
	if err := sup.Start(); err != nil {
		t.Fatalf("sup.Start: %v", err)
	}
	t.Cleanup(sup.Shutdown)

	// Wait for running
	pollUntil(t, 5*time.Second, 50*time.Millisecond, func() bool {
		info := sup.ServiceInfo("web")
		info.RLock()
		defer info.RUnlock()
		return info.Status.IsRunning()
	})

	// Stop via API
	_, err := client.Call("stop", map[string]string{"service": "web"})
	if err != nil {
		t.Fatalf("stop: %v", err)
	}

	// Verify stopped
	pollUntil(t, 5*time.Second, 50*time.Millisecond, func() bool {
		info := sup.ServiceInfo("web")
		info.RLock()
		defer info.RUnlock()
		return info.Status == service.StatusStopped
	})

	// Start via API
	_, err = client.Call("start", map[string]string{"service": "web"})
	if err != nil {
		t.Fatalf("start: %v", err)
	}

	// Verify running again
	pollUntil(t, 5*time.Second, 50*time.Millisecond, func() bool {
		info := sup.ServiceInfo("web")
		info.RLock()
		defer info.RUnlock()
		return info.Status.IsRunning()
	})
}

func TestLogs(t *testing.T) {
	_, client, sup := setupServer(t)

	// Add some log lines directly
	buf := sup.ServiceLogs("web")
	buf.Add("stdout", "line 1")
	buf.Add("stderr", "line 2")

	data, err := client.Call("logs", map[string]any{"service": "web", "last": 10})
	if err != nil {
		t.Fatalf("logs: %v", err)
	}
	var lines []LogLine
	if err := json.Unmarshal(data, &lines); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(lines) != 2 {
		t.Fatalf("got %d lines, want 2", len(lines))
	}
	if lines[0].Text != "line 1" || lines[1].Text != "line 2" {
		t.Errorf("unexpected log content: %+v", lines)
	}
}

func TestTracesDisabled(t *testing.T) {
	_, client, _ := setupServer(t)

	_, err := client.Call("traces", nil)
	if err == nil {
		t.Fatal("expected error when tracing disabled")
	}
}

func TestTracesEnabled(t *testing.T) {
	cfg := testConfig(t)
	bus := events.NewBus()
	sup := supervisor.New(cfg, bus)
	store := spanbuf.NewStore(1024 * 1024)

	sockPath := filepath.Join(t.TempDir(), "bench.sock")
	srv := New(sup, store, sockPath, "test")
	if err := srv.Start(); err != nil {
		t.Fatalf("server start: %v", err)
	}
	t.Cleanup(srv.Shutdown)

	// Add a span
	store.Add(spanbuf.Span{
		TraceID:     [16]byte{1},
		SpanID:      [8]byte{1},
		Name:        "test-span",
		ServiceName: "test-svc",
		StartTime:   time.Now(),
		EndTime:     time.Now().Add(time.Millisecond),
		Duration:    time.Millisecond,
	})

	client := NewClient(sockPath)

	data, err := client.Call("traces", nil)
	if err != nil {
		t.Fatalf("traces: %v", err)
	}
	var traces []traceSummary
	if err := json.Unmarshal(data, &traces); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(traces) != 1 {
		t.Fatalf("got %d traces, want 1", len(traces))
	}
	if traces[0].SpanCount != 1 {
		t.Errorf("got span_count %d, want 1", traces[0].SpanCount)
	}
}

func TestStaleSocketCleanup(t *testing.T) {
	sockPath := filepath.Join(t.TempDir(), "bench.sock")

	// Create a real Unix socket listener, then close it to leave a stale socket file.
	// On some platforms (macOS), closing the listener removes the file, so we
	// recreate it via syscall if needed.
	ln, err := net.Listen("unix", sockPath)
	if err != nil {
		t.Fatal(err)
	}
	_ = ln.Close()

	// If the OS cleaned up the socket file on close, recreate a stale socket
	// using a second listener that we close.
	if _, err := os.Stat(sockPath); os.IsNotExist(err) {
		ln2, err := net.Listen("unix", sockPath)
		if err != nil {
			t.Fatal(err)
		}
		// Keep the fd but stop accepting — simulates a stale socket
		_ = ln2.Close()
		// On macOS this still removes it. Fall back to testing that the server
		// starts cleanly when there's no socket at all.
		if _, err := os.Stat(sockPath); os.IsNotExist(err) {
			t.Log("OS removes socket on close; testing clean start instead")
		}
	}

	cfg := testConfig(t)
	bus := events.NewBus()
	sup := supervisor.New(cfg, bus)

	srv := New(sup, nil, sockPath, "test")
	if err := srv.Start(); err != nil {
		t.Fatalf("should have started cleanly: %v", err)
	}
	srv.Shutdown()

	// Socket should be cleaned up after shutdown
	if _, err := os.Stat(sockPath); !os.IsNotExist(err) {
		t.Error("socket file should be removed after shutdown")
	}
}

func TestNonSocketFileNotRemoved(t *testing.T) {
	sockPath := filepath.Join(t.TempDir(), "bench.sock")

	// Create a regular file at the socket path
	if err := os.WriteFile(sockPath, []byte("not a socket"), 0o600); err != nil {
		t.Fatal(err)
	}

	cfg := testConfig(t)
	bus := events.NewBus()
	sup := supervisor.New(cfg, bus)

	srv := New(sup, nil, sockPath, "test")
	err := srv.Start()
	if err == nil {
		srv.Shutdown()
		t.Fatal("expected error when socket path is a regular file")
	}

	// The regular file should NOT have been removed
	if _, statErr := os.Stat(sockPath); os.IsNotExist(statErr) {
		t.Error("regular file should not be removed")
	}
}

func TestSocketPath(t *testing.T) {
	p1, err := SocketPath("/home/user/bench.yml")
	if err != nil {
		t.Fatal(err)
	}
	p2, err := SocketPath("/home/user/bench.yml")
	if err != nil {
		t.Fatal(err)
	}
	if p1 != p2 {
		t.Errorf("same config should produce same socket path: %q != %q", p1, p2)
	}

	p3, err := SocketPath("/other/bench.yml")
	if err != nil {
		t.Fatal(err)
	}
	if p1 == p3 {
		t.Error("different config should produce different socket path")
	}
}

func TestServiceMap(t *testing.T) {
	cfg := testConfig(t)
	bus := events.NewBus()
	sup := supervisor.New(cfg, bus)
	store := spanbuf.NewStore(1024 * 1024)

	sockPath := filepath.Join(t.TempDir(), "bench.sock")
	srv := New(sup, store, sockPath, "test")
	if err := srv.Start(); err != nil {
		t.Fatalf("server start: %v", err)
	}
	t.Cleanup(srv.Shutdown)

	client := NewClient(sockPath)

	data, err := client.Call("service-map", nil)
	if err != nil {
		t.Fatalf("service-map: %v", err)
	}
	var edges []serviceMapEdge
	if err := json.Unmarshal(data, &edges); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	// Empty store should have no edges
	if len(edges) != 0 {
		t.Errorf("expected 0 edges, got %d", len(edges))
	}
}

func TestRestart(t *testing.T) {
	_, client, sup := setupServer(t)

	if err := sup.Start(); err != nil {
		t.Fatalf("sup.Start: %v", err)
	}
	t.Cleanup(sup.Shutdown)

	pollUntil(t, 5*time.Second, 50*time.Millisecond, func() bool {
		info := sup.ServiceInfo("web")
		info.RLock()
		defer info.RUnlock()
		return info.Status.IsRunning()
	})

	_, err := client.Call("restart", map[string]string{"service": "web", "reason": "test restart"})
	if err != nil {
		t.Fatalf("restart: %v", err)
	}

	// Should eventually be running again
	pollUntil(t, 5*time.Second, 50*time.Millisecond, func() bool {
		info := sup.ServiceInfo("web")
		info.RLock()
		defer info.RUnlock()
		return info.Status.IsRunning() && info.RestartCount >= 1
	})
}

func TestToggleWatch(t *testing.T) {
	_, client, _ := setupServer(t)

	data, err := client.Call("toggle-watch", map[string]string{"service": "web"})
	if err != nil {
		t.Fatalf("toggle-watch: %v", err)
	}
	var result map[string]bool
	if err := json.Unmarshal(data, &result); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	// Should have toggled from default (false for this test config)
	if _, ok := result["watch_enabled"]; !ok {
		t.Error("expected watch_enabled in response")
	}
}

func TestSpansByTrace(t *testing.T) {
	cfg := testConfig(t)
	bus := events.NewBus()
	sup := supervisor.New(cfg, bus)
	store := spanbuf.NewStore(1024 * 1024)

	sockPath := filepath.Join(t.TempDir(), "bench.sock")
	srv := New(sup, store, sockPath, "test")
	if err := srv.Start(); err != nil {
		t.Fatalf("server start: %v", err)
	}
	t.Cleanup(srv.Shutdown)

	traceID := [16]byte{0xab, 0xcd, 0xef, 0x01, 0x23, 0x45, 0x67, 0x89, 0xab, 0xcd, 0xef, 0x01, 0x23, 0x45, 0x67, 0x89}
	store.Add(spanbuf.Span{
		TraceID:     traceID,
		SpanID:      [8]byte{1},
		Name:        "root-span",
		ServiceName: "web",
		StartTime:   time.Now(),
		EndTime:     time.Now().Add(time.Millisecond),
		Duration:    time.Millisecond,
	})

	client := NewClient(sockPath)

	data, err := client.Call("spans", map[string]string{"trace_id": "abcdef0123456789abcdef0123456789"})
	if err != nil {
		t.Fatalf("spans: %v", err)
	}
	var spans []spanInfo
	if err := json.Unmarshal(data, &spans); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(spans) != 1 {
		t.Fatalf("got %d spans, want 1", len(spans))
	}
	if spans[0].Name != "root-span" {
		t.Errorf("got name %q, want %q", spans[0].Name, "root-span")
	}
}

func TestSpansByService(t *testing.T) {
	cfg := testConfig(t)
	bus := events.NewBus()
	sup := supervisor.New(cfg, bus)
	store := spanbuf.NewStore(1024 * 1024)

	sockPath := filepath.Join(t.TempDir(), "bench.sock")
	srv := New(sup, store, sockPath, "test")
	if err := srv.Start(); err != nil {
		t.Fatalf("server start: %v", err)
	}
	t.Cleanup(srv.Shutdown)

	store.Add(spanbuf.Span{
		TraceID:     [16]byte{2},
		SpanID:      [8]byte{1},
		Name:        "svc-span",
		ServiceName: "my-svc",
		StartTime:   time.Now(),
		EndTime:     time.Now().Add(time.Millisecond),
		Duration:    time.Millisecond,
	})

	client := NewClient(sockPath)

	data, err := client.Call("spans", map[string]string{"service": "my-svc"})
	if err != nil {
		t.Fatalf("spans: %v", err)
	}
	var spans []spanInfo
	if err := json.Unmarshal(data, &spans); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(spans) != 1 {
		t.Fatalf("got %d spans, want 1", len(spans))
	}
}

func TestLogsCursor(t *testing.T) {
	_, client, sup := setupServer(t)

	buf := sup.ServiceLogs("web")
	buf.Add("stdout", "old line")
	buf.Add("stdout", "new line")

	// Fetch all
	data, err := client.Call("logs", map[string]any{"service": "web", "last": 10})
	if err != nil {
		t.Fatalf("logs: %v", err)
	}
	var allLines []LogLine
	if err := json.Unmarshal(data, &allLines); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(allLines) != 2 {
		t.Fatalf("got %d lines, want 2", len(allLines))
	}
	if allLines[0].Seq == 0 || allLines[1].Seq == 0 {
		t.Fatal("expected non-zero seq values")
	}

	// Use the first line's seq as cursor — should only return the second
	data, err = client.Call("logs", map[string]any{"service": "web", "last": 10, "after_seq": allLines[0].Seq})
	if err != nil {
		t.Fatalf("logs with cursor: %v", err)
	}
	var newLines []LogLine
	if err := json.Unmarshal(data, &newLines); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(newLines) != 1 {
		t.Fatalf("got %d lines after cursor, want 1", len(newLines))
	}
	if newLines[0].Text != "new line" {
		t.Errorf("got %q, want %q", newLines[0].Text, "new line")
	}
}

func TestLogsCursorSameTimestamp(t *testing.T) {
	_, client, sup := setupServer(t)

	// Add lines that will have the same timestamp (same goroutine, no sleep)
	buf := sup.ServiceLogs("web")
	buf.Add("stdout", "line-a")
	buf.Add("stdout", "line-b")
	buf.Add("stdout", "line-c")

	data, err := client.Call("logs", map[string]any{"service": "web", "last": 10})
	if err != nil {
		t.Fatalf("logs: %v", err)
	}
	var all []LogLine
	if err := json.Unmarshal(data, &all); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(all) != 3 {
		t.Fatalf("got %d lines, want 3", len(all))
	}

	// Cursor after first line — should get exactly 2 remaining
	data, err = client.Call("logs", map[string]any{"service": "web", "last": 10, "after_seq": all[0].Seq})
	if err != nil {
		t.Fatalf("logs: %v", err)
	}
	var rest []LogLine
	if err := json.Unmarshal(data, &rest); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(rest) != 2 {
		t.Fatalf("got %d lines after cursor, want 2 (seq cursor should not drop same-timestamp lines)", len(rest))
	}
	if rest[0].Text != "line-b" || rest[1].Text != "line-c" {
		t.Errorf("unexpected lines: %+v", rest)
	}
}

func TestSocketPathOverridePrecedence(t *testing.T) {
	// Direct override takes highest priority
	path, err := SocketPathFromEnvOrConfig("/custom/path.sock", "/some/config.yml")
	if err != nil {
		t.Fatal(err)
	}
	if path != "/custom/path.sock" {
		t.Errorf("got %q, want /custom/path.sock", path)
	}

	// Config-derived path when no override
	p1, err := SocketPathFromEnvOrConfig("", "/some/config.yml")
	if err != nil {
		t.Fatal(err)
	}
	p2, err := SocketPath("/some/config.yml")
	if err != nil {
		t.Fatal(err)
	}
	if p1 != p2 {
		t.Errorf("expected same path from SocketPathFromEnvOrConfig and SocketPath: %q != %q", p1, p2)
	}
}

func TestCleanStaleSocketConnectionRefused(t *testing.T) {
	sockPath := filepath.Join("/tmp", fmt.Sprintf("bench-test-%d.sock", os.Getpid()))
	defer func() { _ = os.Remove(sockPath) }()

	// Non-existent socket should be fine
	err := cleanStaleSocket(sockPath)
	if err != nil {
		t.Fatalf("non-existent socket should be fine: %v", err)
	}

	// Create a listener, close it, test if file remains
	ln, err := net.Listen("unix", sockPath)
	if err != nil {
		t.Fatal(err)
	}
	_ = ln.Close()

	// If the file still exists (Linux behavior), cleanStaleSocket should remove it
	if _, statErr := os.Stat(sockPath); statErr == nil {
		err = cleanStaleSocket(sockPath)
		if err != nil {
			t.Fatalf("stale socket should be cleaned: %v", err)
		}
		if _, statErr := os.Stat(sockPath); !os.IsNotExist(statErr) {
			t.Error("stale socket should have been removed")
		}
	}
}

func TestAnotherInstanceRunning(t *testing.T) {
	sockPath := filepath.Join(t.TempDir(), "bench.sock")

	// Start a real listener to simulate a running instance
	ln, err := net.Listen("unix", sockPath)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = ln.Close() }()

	cfg := testConfig(t)
	bus := events.NewBus()
	sup := supervisor.New(cfg, bus)

	srv := New(sup, nil, sockPath, "test")
	err = srv.Start()
	if err == nil {
		srv.Shutdown()
		t.Fatal("expected error when another instance is running")
	}
	if !strings.Contains(err.Error(), "already running") {
		t.Errorf("expected 'already running' error, got: %v", err)
	}
}

func TestServerStartBeforeSupervisor(t *testing.T) {
	sockPath := filepath.Join("/tmp", fmt.Sprintf("bench-test-sup-%d.sock", os.Getpid()))
	defer func() { _ = os.Remove(sockPath) }()

	// Start server with nil supervisor (simulates runUp ordering)
	srv := New(nil, nil, sockPath, "test")
	if err := srv.Start(); err != nil {
		t.Fatalf("server start: %v", err)
	}
	t.Cleanup(srv.Shutdown)

	client := NewClient(sockPath)

	// Ping should work even without supervisor
	data, err := client.Call("ping", nil)
	if err != nil {
		t.Fatalf("ping: %v", err)
	}
	var result map[string]string
	if err := json.Unmarshal(data, &result); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if result["version"] != "test" {
		t.Errorf("got version %q, want %q", result["version"], "test")
	}

	// Status should fail gracefully before supervisor is wired
	_, err = client.Call("status", nil)
	if err == nil {
		t.Fatal("expected error before supervisor is set")
	}

	// Wire supervisor
	cfg := testConfig(t)
	bus := events.NewBus()
	sup := supervisor.New(cfg, bus)
	srv.SetSupervisor(sup)

	// Now status should work
	data, err = client.Call("status", nil)
	if err != nil {
		t.Fatalf("status after SetSupervisor: %v", err)
	}
	var statuses []ServiceStatus
	if err := json.Unmarshal(data, &statuses); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(statuses) != 2 {
		t.Errorf("got %d services, want 2", len(statuses))
	}
}

func pollUntil(t *testing.T, timeout, interval time.Duration, cond func() bool) {
	t.Helper()
	deadline := time.After(timeout)
	for {
		if cond() {
			return
		}
		select {
		case <-deadline:
			t.Fatal("timed out waiting for condition")
		case <-time.After(interval):
		}
	}
}
