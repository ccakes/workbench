package tui

import (
	"testing"
	"time"

	"github.com/ccakes/workbench/internal/api"
)

func TestSnapFromStatusUsesStartedAt(t *testing.T) {
	started := time.Now().Add(-300 * time.Millisecond)
	snap := snapFromStatus(api.ServiceStatus{Key: "web", Status: "ready", StartedAt: started})
	if !snap.StartTime.Equal(started) {
		t.Errorf("got StartTime %v, want %v", snap.StartTime, started)
	}
}

func TestSnapFromStatusFallsBackToUptime(t *testing.T) {
	snap := snapFromStatus(api.ServiceStatus{Key: "web", Status: "ready", Uptime: "1m30s"})
	if snap.StartTime.IsZero() {
		t.Fatal("StartTime not set from uptime")
	}
	if got := snap.Uptime(); got < 90*time.Second || got > 91*time.Second {
		t.Errorf("got uptime %v, want ~1m30s", got)
	}
}
