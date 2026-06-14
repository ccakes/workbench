//go:build !windows

package cli

import (
	"os"
	"syscall"
)

// spawnDaemon starts a detached `bench up --daemon` worker in its own session
// (setsid) so it survives the launcher exiting and is not tied to the
// controlling terminal. Its stdout/stderr go to logPath for diagnostics.
func spawnDaemon(self string, args []string, logPath string) error {
	logFile, err := os.OpenFile(logPath, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
	if err != nil {
		return err
	}
	defer func() { _ = logFile.Close() }()

	cmd := execCommand(self, args...)
	cmd.Env = append(os.Environ(), "BENCH_DAEMON=1")
	cmd.Stdin = nil
	cmd.Stdout = logFile
	cmd.Stderr = logFile
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	return cmd.Start()
}
