package runner

import (
	"reflect"
	"testing"
	"time"

	"github.com/ccakes/workbench/internal/config"
)

func sampleSpec() RunSpec {
	return RunSpec{
		Name:    "bench-db",
		Labels:  []string{"managed-by=bench"},
		Env:     []string{"FOO=bar"},
		Ports:   []string{"5432:5432"},
		Volumes: []string{"/data:/var/lib/pg"},
		Network: "backend",
		Image:   "postgres:16",
		Command: []string{"postgres", "-c", "log_statement=all"},
	}
}

func TestDockerBackend_RunArgs(t *testing.T) {
	got := dockerBackend{}.RunArgs(sampleSpec())
	want := []string{
		"run", "-d", "--name", "bench-db",
		"--label", "managed-by=bench",
		"--add-host", "host.docker.internal:host-gateway",
		"-e", "FOO=bar",
		"-p", "5432:5432",
		"-v", "/data:/var/lib/pg",
		"--network", "backend",
		"postgres:16",
		"postgres", "-c", "log_statement=all",
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("RunArgs mismatch\n got: %v\nwant: %v", got, want)
	}
}

func TestAppleBackend_RunArgs_NoHostAlias(t *testing.T) {
	b := newAppleBackend(config.GlobalConfig{})
	got := b.RunArgs(sampleSpec())
	// Apple omits --add-host; everything else matches Docker's ordering.
	want := []string{
		"run", "-d", "--name", "bench-db",
		"--label", "managed-by=bench",
		"-e", "FOO=bar",
		"-p", "5432:5432",
		"-v", "/data:/var/lib/pg",
		"--network", "backend",
		"postgres:16",
		"postgres", "-c", "log_statement=all",
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("RunArgs mismatch\n got: %v\nwant: %v", got, want)
	}
}

func TestBackends_LogsStopKillRemoveArgs(t *testing.T) {
	tests := []struct {
		name    string
		backend ContainerBackend
		logs    []string
		stop    []string
		kill    []string
		rmForce []string
		rmSoft  []string
	}{
		{
			name:    "docker",
			backend: dockerBackend{},
			logs:    []string{"logs", "--follow", "cid"},
			stop:    []string{"stop", "-t", "10", "cid"},
			kill:    []string{"kill", "cid"},
			rmForce: []string{"rm", "-f", "-v", "bench-db"},
			rmSoft:  []string{"rm", "-v", "cid"},
		},
		{
			name:    "apple",
			backend: newAppleBackend(config.GlobalConfig{}),
			logs:    []string{"logs", "-f", "cid"},
			stop:    []string{"stop", "-t", "10", "cid"},
			kill:    []string{"kill", "cid"},
			rmForce: []string{"delete", "-f", "bench-db"},
			rmSoft:  []string{"delete", "cid"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.backend.LogsArgs("cid"); !reflect.DeepEqual(got, tt.logs) {
				t.Errorf("LogsArgs = %v, want %v", got, tt.logs)
			}
			if got := tt.backend.StopArgs("cid", 10*time.Second); !reflect.DeepEqual(got, tt.stop) {
				t.Errorf("StopArgs = %v, want %v", got, tt.stop)
			}
			if got := tt.backend.KillArgs("cid"); !reflect.DeepEqual(got, tt.kill) {
				t.Errorf("KillArgs = %v, want %v", got, tt.kill)
			}
			if got := tt.backend.RemoveArgs("bench-db", true); !reflect.DeepEqual(got, tt.rmForce) {
				t.Errorf("RemoveArgs(force) = %v, want %v", got, tt.rmForce)
			}
			if got := tt.backend.RemoveArgs("cid", false); !reflect.DeepEqual(got, tt.rmSoft) {
				t.Errorf("RemoveArgs(soft) = %v, want %v", got, tt.rmSoft)
			}
		})
	}
}

func TestBackends_OTELHost(t *testing.T) {
	if got := (dockerBackend{}).OTELHost(); got != "host.docker.internal" {
		t.Errorf("docker OTELHost = %q", got)
	}
	if got := newAppleBackend(config.GlobalConfig{}).OTELHost(); got != defaultAppleGatewayIP {
		t.Errorf("apple OTELHost = %q, want default %q", got, defaultAppleGatewayIP)
	}
	custom := newAppleBackend(config.GlobalConfig{Apple: config.AppleConfig{GatewayIP: "10.9.8.7"}})
	if got := custom.OTELHost(); got != "10.9.8.7" {
		t.Errorf("apple OTELHost = %q, want 10.9.8.7", got)
	}
}

func TestResolveBackend_Explicit(t *testing.T) {
	if got := ResolveBackend(config.GlobalConfig{}).Name(); got != "docker" {
		t.Errorf("default => %q", got)
	}
	if got := ResolveBackend(config.GlobalConfig{ContainerBackend: config.BackendDocker}).Name(); got != "docker" {
		t.Errorf("docker => %q", got)
	}
	if got := ResolveBackend(config.GlobalConfig{ContainerBackend: config.BackendApple}).Name(); got != "apple" {
		t.Errorf("apple => %q", got)
	}
}

func TestIsUnsupportedPlatformError(t *testing.T) {
	tests := []struct {
		name string
		out  string
		want bool
	}{
		{
			// Real Apple `container` 1.1 output for an amd64-only image.
			name: "apple no compatible variant",
			out:  "Error: image sha256:1178cdd375f7 does not support required platforms",
			want: true,
		},
		{
			// Real Apple `container` 1.1 output when a variant exists but its
			// binaries can't exec on the host.
			name: "apple exec format error",
			out:  `failed to exec [echo hi] Error Domain=NSPOSIXErrorDomain Code=8 "Exec format error"`,
			want: true,
		},
		{
			name: "docker no matching manifest",
			out:  "no matching manifest for linux/arm64/v8 in the manifest list entries",
			want: true,
		},
		{
			name: "docker platform mismatch",
			out:  "image with reference X was found but does not match the specified platform",
			want: true,
		},
		{
			name: "unrelated failure",
			out:  "Error: connection refused",
			want: false,
		},
		{
			name: "empty",
			out:  "",
			want: false,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := isUnsupportedPlatformError(tt.out); got != tt.want {
				t.Errorf("isUnsupportedPlatformError(%q) = %v, want %v", tt.out, got, tt.want)
			}
		})
	}
}

func TestParseAppleInspect(t *testing.T) {
	tests := []struct {
		name       string
		data       string
		wantStatus string
		wantCode   int
		wantErr    bool
	}{
		{
			name:       "running array",
			data:       `[{"status":"running","configuration":{"id":"db"}}]`,
			wantStatus: "running",
			wantCode:   0,
		},
		{
			name:       "stopped with exitCode",
			data:       `[{"status":"stopped","exitCode":137}]`,
			wantStatus: "stopped",
			wantCode:   137,
		},
		{
			name:       "stopped nested exit_code",
			data:       `[{"status":"stopped","process":{"exit_code":2}}]`,
			wantStatus: "stopped",
			wantCode:   2,
		},
		{
			name:       "stopped no code defaults zero",
			data:       `[{"status":"stopped"}]`,
			wantStatus: "stopped",
			wantCode:   0,
		},
		{
			name:       "bare object",
			data:       `{"status":"stopped","exitCode":1}`,
			wantStatus: "stopped",
			wantCode:   1,
		},
		{
			// Real `container inspect` (v1.1.0) shape: status is an object
			// whose "state" holds the run state, not a bare string.
			name:       "nested status object running",
			data:       `[{"id":"n-redis","configuration":{"id":"n-redis"},"status":{"state":"running","startedDate":"2026-07-23T00:47:05Z","networks":[{"ipv4Address":"192.168.64.36/24"}]}}]`,
			wantStatus: "running",
			wantCode:   0,
		},
		{
			name:       "nested status object stopped",
			data:       `[{"id":"n-redis","status":{"state":"stopped","startedDate":"2026-07-23T00:47:05Z"}}]`,
			wantStatus: "stopped",
			wantCode:   0,
		},
		{
			name:    "malformed",
			data:    `not json`,
			wantErr: true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			status, code, err := parseAppleInspect([]byte(tt.data))
			if (err != nil) != tt.wantErr {
				t.Fatalf("err = %v, wantErr %v", err, tt.wantErr)
			}
			if tt.wantErr {
				return
			}
			if status != tt.wantStatus {
				t.Errorf("status = %q, want %q", status, tt.wantStatus)
			}
			if code != tt.wantCode {
				t.Errorf("code = %d, want %d", code, tt.wantCode)
			}
		})
	}
}

func TestBackends_ExecArgs(t *testing.T) {
	cmd := []string{"pg_isready", "-U", "bench"}
	tests := []struct {
		name    string
		backend ContainerBackend
		want    []string
	}{
		{"docker", dockerBackend{}, []string{"exec", "bench-db", "pg_isready", "-U", "bench"}},
		{"apple", newAppleBackend(config.GlobalConfig{}), []string{"exec", "bench-db", "pg_isready", "-U", "bench"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.backend.ExecArgs("bench-db", cmd); !reflect.DeepEqual(got, tt.want) {
				t.Errorf("ExecArgs = %v, want %v", got, tt.want)
			}
		})
	}
	// The caller's slice must not be aliased or mutated by arg building.
	if !reflect.DeepEqual(cmd, []string{"pg_isready", "-U", "bench"}) {
		t.Errorf("ExecArgs mutated the caller's command slice: %v", cmd)
	}
}

func TestContainerRunner_ExecCommand(t *testing.T) {
	tests := []struct {
		name    string
		backend ContainerBackend
		wantBin string
	}{
		{"docker", dockerBackend{}, "docker"},
		{"apple", newAppleBackend(config.GlobalConfig{}), "container"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := NewContainerRunner(config.ServiceConfig{}, "postgres", "astrobot", tt.backend)
			bin, args := r.ExecCommand([]string{"pg_isready"})
			if bin != tt.wantBin {
				t.Errorf("bin = %q, want %q", bin, tt.wantBin)
			}
			// Targets the prefix-derived name, and works before Start() has
			// assigned a container id.
			want := []string{"exec", "astrobot-postgres", "pg_isready"}
			if !reflect.DeepEqual(args, want) {
				t.Errorf("args = %v, want %v", args, want)
			}
		})
	}
}

// ContainerRunner must satisfy ContainerExecer for the container_exec probe to
// find it via type assertion; ProcessRunner must not.
func TestContainerExecerImplementations(t *testing.T) {
	var _ ContainerExecer = (*ContainerRunner)(nil)
	if _, ok := any(NewProcessRunner(config.ServiceConfig{})).(ContainerExecer); ok {
		t.Error("ProcessRunner must not implement ContainerExecer")
	}
}
