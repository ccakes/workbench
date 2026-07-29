package runner

import (
	"fmt"
	"os/exec"
	"strconv"
	"strings"
	"time"
)

// dockerBackend runs containers via the `docker` CLI. It reproduces
// workbench's original Docker behavior exactly.
type dockerBackend struct{}

func (dockerBackend) Name() string   { return "docker" }
func (dockerBackend) Binary() string { return "docker" }

// Available verifies that Docker is available and running.
func (dockerBackend) Available() error {
	out, err := exec.Command("docker", "info").CombinedOutput()
	if err != nil {
		return fmt.Errorf("docker is not available: %s", strings.TrimSpace(string(out)))
	}
	return nil
}

func (dockerBackend) RunArgs(spec RunSpec) []string {
	// host-gateway maps host.docker.internal to the host so containers can
	// reach host-side services (e.g. the OTLP trace collector). Some runtimes
	// provide this alias automatically; adding it explicitly makes it portable
	// to plain Docker on Linux.
	return buildRunArgs(spec, []string{"--add-host", "host.docker.internal:host-gateway"})
}

func (dockerBackend) LogsArgs(id string) []string { return []string{"logs", "--follow", id} }

func (dockerBackend) StopArgs(id string, timeout time.Duration) []string {
	return []string{"stop", "-t", strconv.Itoa(int(timeout.Seconds())), id}
}

func (dockerBackend) KillArgs(id string) []string { return []string{"kill", id} }

func (dockerBackend) RemoveArgs(target string, force bool) []string {
	// -v also removes the container's anonymous volumes (e.g. images with a
	// VOLUME directive like Postgres/Cassandra) to avoid leaking disk space.
	args := []string{"rm"}
	if force {
		args = append(args, "-f")
	}
	return append(args, "-v", target)
}

func (dockerBackend) ExecArgs(id string, cmd []string) []string {
	return append([]string{"exec", id}, cmd...)
}

func (dockerBackend) WaitExit(id string) int {
	// `docker wait` blocks until the container exits and prints the exit code.
	out, err := exec.Command("docker", "wait", id).Output()
	if err != nil {
		return -1
	}
	code := 0
	_, _ = fmt.Sscanf(strings.TrimSpace(string(out)), "%d", &code)
	return code
}

func (dockerBackend) OTELHost() string { return "host.docker.internal" }
