package api

import (
	"os"
	"path/filepath"
	"testing"
)

// EnsureSocketDir must create the private per-user socket directory when it is
// missing, because the launcher writes the daemon log there before the daemon
// itself binds its socket. macOS reaps /tmp entries, so a directory from a
// prior session may be gone by the next launch.
func TestEnsureSocketDirCreatesMissingDir(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("TMPDIR", tmp)

	dir, err := privateSocketDir()
	if err != nil {
		t.Fatalf("privateSocketDir: %v", err)
	}
	if _, err := os.Stat(dir); !os.IsNotExist(err) {
		t.Fatalf("expected %s to be absent before the call, stat err = %v", dir, err)
	}

	sockPath := filepath.Join(dir, "bench-abc.sock")
	if err := EnsureSocketDir(sockPath); err != nil {
		t.Fatalf("EnsureSocketDir: %v", err)
	}

	info, err := os.Stat(dir)
	if err != nil {
		t.Fatalf("stat after create: %v", err)
	}
	if !info.IsDir() {
		t.Fatalf("%s is not a directory", dir)
	}
	if perm := info.Mode().Perm(); perm != 0o700 {
		t.Fatalf("directory perms = %o, want 700", perm)
	}

	// Idempotent: a second call on an existing dir must succeed.
	if err := EnsureSocketDir(sockPath); err != nil {
		t.Fatalf("second EnsureSocketDir: %v", err)
	}
}

// A socket path outside the managed private directory (e.g. a custom --socket
// or BENCH_SOCKET) is left untouched — EnsureSocketDir must not create the
// private dir in that case.
func TestEnsureSocketDirNoOpOutsidePrivateDir(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("TMPDIR", tmp)

	dir, err := privateSocketDir()
	if err != nil {
		t.Fatalf("privateSocketDir: %v", err)
	}

	elsewhere := filepath.Join(t.TempDir(), "custom.sock")
	if err := EnsureSocketDir(elsewhere); err != nil {
		t.Fatalf("EnsureSocketDir: %v", err)
	}
	if _, err := os.Stat(dir); !os.IsNotExist(err) {
		t.Fatalf("private dir %s should not have been created, stat err = %v", dir, err)
	}
}

// A symlink where the private directory is expected is rejected rather than
// followed, to avoid binding the socket through an attacker-controlled link.
func TestEnsureSocketDirRejectsSymlink(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("TMPDIR", tmp)

	dir, err := privateSocketDir()
	if err != nil {
		t.Fatalf("privateSocketDir: %v", err)
	}
	target := filepath.Join(t.TempDir(), "target")
	if err := os.Mkdir(target, 0o700); err != nil {
		t.Fatalf("mkdir target: %v", err)
	}
	if err := os.Symlink(target, dir); err != nil {
		t.Fatalf("symlink: %v", err)
	}

	if err := EnsureSocketDir(filepath.Join(dir, "bench-abc.sock")); err == nil {
		t.Fatalf("expected error for symlinked socket directory, got nil")
	}
}
