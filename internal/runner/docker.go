package runner

import (
	"fmt"
	"os/exec"
	"strings"
)

// CheckDocker verifies that Docker is available and running.
func CheckDocker() error {
	cmd := exec.Command("docker", "info")
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("docker is not available: %s", strings.TrimSpace(string(out)))
	}
	return nil
}
