package runner

import (
	"os"
	"os/exec"
	"syscall"
	"testing"
	"time"
)

func TestDescendants_self(t *testing.T) {
	// Our own process should have no descendants (test binary is single-process).
	desc := descendants(os.Getpid())
	if len(desc) != 0 {
		t.Errorf("expected no descendants for test process, got %v", desc)
	}
}

func TestDescendants_childTree(t *testing.T) {
	// Use a compound command so sh cannot exec — it must fork children and wait.
	cmd := exec.Command("sh", "-c", "sleep 60 & sleep 60 & wait")
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if err := cmd.Start(); err != nil {
		t.Fatalf("start: %v", err)
	}
	defer func() {
		_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
		_ = cmd.Wait()
	}()

	// Give sh a moment to fork the sleep children.
	time.Sleep(200 * time.Millisecond)

	desc := descendants(cmd.Process.Pid)
	if len(desc) < 2 {
		t.Fatalf("expected at least 2 descendants (two sleeps), got %d: %v", len(desc), desc)
	}
}

func TestDescendants_invalidPID(t *testing.T) {
	// A PID that almost certainly doesn't exist should return no descendants.
	desc := descendants(9999999)
	if len(desc) != 0 {
		t.Errorf("expected no descendants for bogus PID, got %v", desc)
	}
}

func TestAnyAlive(t *testing.T) {
	// Our own PID is alive.
	if !anyAlive([]int{os.Getpid()}) {
		t.Error("expected own PID to be alive")
	}
	// Bogus PID should not be alive.
	if anyAlive([]int{9999999}) {
		t.Error("expected bogus PID to be dead")
	}
	// Empty list.
	if anyAlive(nil) {
		t.Error("expected empty list to return false")
	}
}
