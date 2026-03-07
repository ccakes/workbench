package service

import (
	"testing"
	"time"
)

func TestStatusString(t *testing.T) {
	expected := map[Status]string{
		StatusPending:    "pending",
		StatusStarting:   "starting",
		StatusRunning:    "running",
		StatusReady:      "ready",
		StatusStopping:   "stopping",
		StatusStopped:    "stopped",
		StatusFailed:     "failed",
		StatusRestarting: "restarting",
		StatusBackoff:    "backoff",
		StatusDisabled:   "disabled",
	}

	for status, want := range expected {
		got := status.String()
		if got != want {
			t.Errorf("Status(%d).String() = %q, want %q", int(status), got, want)
		}
	}

	// Unknown status should include the numeric value
	unknown := Status(999)
	s := unknown.String()
	if s != "unknown(999)" {
		t.Errorf("unknown status: got %q, want %q", s, "unknown(999)")
	}
}

func TestIsRunning(t *testing.T) {
	running := []Status{StatusRunning, StatusReady, StatusStarting}
	notRunning := []Status{StatusPending, StatusStopping, StatusStopped, StatusFailed, StatusRestarting, StatusBackoff, StatusDisabled}

	for _, s := range running {
		if !s.IsRunning() {
			t.Errorf("%s.IsRunning() = false, want true", s)
		}
	}
	for _, s := range notRunning {
		if s.IsRunning() {
			t.Errorf("%s.IsRunning() = true, want false", s)
		}
	}
}

func TestSnapshot(t *testing.T) {
	info := NewInfo("db", "Database")
	info.Lock()
	info.Status = StatusRunning
	info.PID = 1234
	info.StartTime = time.Now().Add(-5 * time.Minute)
	info.ExitCode = 0
	info.RestartCount = 3
	info.LastRestart = "manual"
	info.LastError = "exit 1"
	info.WatchEnabled = true
	info.Unlock()

	snap := info.Snapshot()

	if snap.Key != "db" {
		t.Errorf("Key: got %q, want %q", snap.Key, "db")
	}
	if snap.DisplayName != "Database" {
		t.Errorf("DisplayName: got %q, want %q", snap.DisplayName, "Database")
	}
	if snap.Status != StatusRunning {
		t.Errorf("Status: got %v, want %v", snap.Status, StatusRunning)
	}
	if snap.PID != 1234 {
		t.Errorf("PID: got %d, want %d", snap.PID, 1234)
	}
	if snap.ExitCode != 0 {
		t.Errorf("ExitCode: got %d, want %d", snap.ExitCode, 0)
	}
	if snap.RestartCount != 3 {
		t.Errorf("RestartCount: got %d, want %d", snap.RestartCount, 3)
	}
	if snap.LastRestart != "manual" {
		t.Errorf("LastRestart: got %q, want %q", snap.LastRestart, "manual")
	}
	if snap.LastError != "exit 1" {
		t.Errorf("LastError: got %q, want %q", snap.LastError, "exit 1")
	}
	if !snap.WatchEnabled {
		t.Error("WatchEnabled: got false, want true")
	}

	// Name() should prefer DisplayName
	if snap.Name() != "Database" {
		t.Errorf("Name(): got %q, want %q", snap.Name(), "Database")
	}

	// Snapshot without DisplayName should fall back to Key
	info2 := NewInfo("redis", "")
	snap2 := info2.Snapshot()
	if snap2.Name() != "redis" {
		t.Errorf("Name() fallback: got %q, want %q", snap2.Name(), "redis")
	}
}

func TestUptime(t *testing.T) {
	t.Run("running service", func(t *testing.T) {
		info := NewInfo("web", "Web")
		info.Lock()
		info.Status = StatusRunning
		info.StartTime = time.Now().Add(-10 * time.Second)
		info.Unlock()

		uptime := info.Uptime()
		// Should be roughly 10s (allow 9-11s for test timing)
		if uptime < 9*time.Second || uptime > 11*time.Second {
			t.Errorf("running uptime: got %v, want ~10s", uptime)
		}
	})

	t.Run("stopped service with stop time", func(t *testing.T) {
		info := NewInfo("bg", "Background")
		start := time.Now().Add(-60 * time.Second)
		stop := time.Now().Add(-30 * time.Second)
		info.Lock()
		info.Status = StatusStopped
		info.StartTime = start
		info.StopTime = stop
		info.Unlock()

		uptime := info.Uptime()
		// Should be 30s (the duration between start and stop)
		if uptime != 30*time.Second {
			t.Errorf("stopped uptime: got %v, want 30s", uptime)
		}
	})

	t.Run("stopped service without times", func(t *testing.T) {
		info := NewInfo("idle", "Idle")
		info.Lock()
		info.Status = StatusStopped
		info.Unlock()

		uptime := info.Uptime()
		if uptime != 0 {
			t.Errorf("no-start uptime: got %v, want 0", uptime)
		}
	})

	t.Run("stopped service with start but no stop", func(t *testing.T) {
		info := NewInfo("odd", "Odd")
		info.Lock()
		info.Status = StatusFailed
		info.StartTime = time.Now().Add(-20 * time.Second)
		// StopTime is zero
		info.Unlock()

		uptime := info.Uptime()
		if uptime != 0 {
			t.Errorf("no-stop uptime: got %v, want 0", uptime)
		}
	})

	t.Run("snapshot uptime for running", func(t *testing.T) {
		info := NewInfo("snap", "Snap")
		info.Lock()
		info.Status = StatusReady
		info.StartTime = time.Now().Add(-5 * time.Second)
		info.Unlock()

		snap := info.Snapshot()
		uptime := snap.Uptime()
		if uptime < 4*time.Second || uptime > 6*time.Second {
			t.Errorf("snapshot uptime: got %v, want ~5s", uptime)
		}
	})

	t.Run("snapshot uptime for stopped", func(t *testing.T) {
		info := NewInfo("done", "Done")
		start := time.Now().Add(-120 * time.Second)
		stop := start.Add(45 * time.Second)
		info.Lock()
		info.Status = StatusStopped
		info.StartTime = start
		info.StopTime = stop
		info.Unlock()

		snap := info.Snapshot()
		if snap.Uptime() != 45*time.Second {
			t.Errorf("snapshot stopped uptime: got %v, want 45s", snap.Uptime())
		}
	})
}
