//go:build windows

package cli

import "fmt"

// spawnDaemon is unsupported on platforms without setsid; bench's process-group
// signal handling is unix-only anyway.
func spawnDaemon(self string, args []string, logPath string) error {
	return fmt.Errorf("background sessions are not supported on this platform")
}
