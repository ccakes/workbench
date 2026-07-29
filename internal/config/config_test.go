package config

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// ---------------------------------------------------------------------------
// Parse
// ---------------------------------------------------------------------------

func TestParse_ValidYAML(t *testing.T) {
	dir := t.TempDir()
	yaml := []byte(`
version: 1
services:
  api:
    dir: .
    command: "go run ./cmd/api"
    env:
      PORT: "8080"
    depends_on: []
`)
	cfg, err := Parse(yaml, dir)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cfg.Version != 1 {
		t.Errorf("version = %d, want 1", cfg.Version)
	}
	svc, ok := cfg.Services["api"]
	if !ok {
		t.Fatal("expected service 'api' to exist")
	}
	if svc.Dir != dir {
		t.Errorf("dir = %q, want %q", svc.Dir, dir)
	}
	if svc.Env["PORT"] != "8080" {
		t.Errorf("env PORT = %q, want %q", svc.Env["PORT"], "8080")
	}
}

func TestParse_Defaults(t *testing.T) {
	yaml := []byte(`
version: 1
services:
  web:
    dir: /tmp
    command: "echo hi"
`)
	cfg, err := Parse(yaml, "/tmp")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Global defaults
	if cfg.Global.LogBufferLines != 5000 {
		t.Errorf("log_buffer_lines = %d, want 5000", cfg.Global.LogBufferLines)
	}
	if cfg.Global.ShutdownTimeout.Duration != 10*time.Second {
		t.Errorf("shutdown_timeout = %v, want 10s", cfg.Global.ShutdownTimeout.Duration)
	}
	if cfg.Global.WatchDebounce.Duration != 300*time.Millisecond {
		t.Errorf("watch_debounce = %v, want 300ms", cfg.Global.WatchDebounce.Duration)
	}

	// Container backend defaults
	if cfg.Global.ContainerBackend != BackendAuto {
		t.Errorf("container_backend = %q, want %q", cfg.Global.ContainerBackend, BackendAuto)
	}
	if cfg.Global.Apple.GatewayIP != "192.168.64.1" {
		t.Errorf("apple.gateway_ip = %q, want 192.168.64.1", cfg.Global.Apple.GatewayIP)
	}

	// Service defaults
	svc := cfg.Services["web"]
	if svc.Restart.Policy != "never" {
		t.Errorf("restart.policy = %q, want %q", svc.Restart.Policy, "never")
	}
	if svc.Restart.Backoff.Duration != 1*time.Second {
		t.Errorf("restart.backoff = %v, want 1s", svc.Restart.Backoff.Duration)
	}
}

func TestParse_ContainerBackendOverride(t *testing.T) {
	yaml := []byte(`
version: 1
global:
  container_backend: apple
  apple:
    gateway_ip: 10.0.0.1
services:
  db:
    container:
      image: postgres:16
`)
	cfg, err := Parse(yaml, "/tmp")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cfg.Global.ContainerBackend != BackendApple {
		t.Errorf("container_backend = %q, want %q", cfg.Global.ContainerBackend, BackendApple)
	}
	if cfg.Global.Apple.GatewayIP != "10.0.0.1" {
		t.Errorf("apple.gateway_ip = %q, want 10.0.0.1", cfg.Global.Apple.GatewayIP)
	}
}

func TestValidate_InvalidContainerBackend(t *testing.T) {
	cfg := &Config{
		Version: 1,
		Global:  GlobalConfig{ContainerBackend: "podman"},
		Services: map[string]ServiceConfig{
			"db": {Container: &ContainerConfig{Image: "postgres:16"}},
		},
	}
	err := cfg.Validate()
	if err == nil {
		t.Fatal("expected validation error for invalid container_backend")
	}
	assertContains(t, err.Error(), "invalid container_backend")
}

func TestValidate_InvalidAppleGatewayIP(t *testing.T) {
	cfg := &Config{
		Version: 1,
		Global:  GlobalConfig{Apple: AppleConfig{GatewayIP: "not-an-ip"}},
		Services: map[string]ServiceConfig{
			"db": {Container: &ContainerConfig{Image: "postgres:16"}},
		},
	}
	err := cfg.Validate()
	if err == nil {
		t.Fatal("expected validation error for invalid apple.gateway_ip")
	}
	assertContains(t, err.Error(), "apple.gateway_ip")
}

func TestParse_CommandAsString(t *testing.T) {
	yaml := []byte(`
version: 1
services:
  app:
    dir: /tmp
    command: "go run main.go"
`)
	cfg, err := Parse(yaml, "/tmp")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	cmd := cfg.Services["app"].Command
	if !cmd.Shell {
		t.Error("expected Shell to be true for string command")
	}
	if len(cmd.Parts) != 3 {
		t.Fatalf("expected 3 parts, got %d", len(cmd.Parts))
	}
	if cmd.Parts[0] != "sh" || cmd.Parts[1] != "-c" || cmd.Parts[2] != "go run main.go" {
		t.Errorf("parts = %v, want [sh -c 'go run main.go']", cmd.Parts)
	}
	if cmd.String() != "go run main.go" {
		t.Errorf("String() = %q, want %q", cmd.String(), "go run main.go")
	}
}

func TestParse_CommandAsArray(t *testing.T) {
	yaml := []byte(`
version: 1
services:
  app:
    dir: /tmp
    command: ["go", "run", "main.go"]
`)
	cfg, err := Parse(yaml, "/tmp")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	cmd := cfg.Services["app"].Command
	if cmd.Shell {
		t.Error("expected Shell to be false for array command")
	}
	if len(cmd.Parts) != 3 {
		t.Fatalf("expected 3 parts, got %d", len(cmd.Parts))
	}
	if cmd.Parts[0] != "go" || cmd.Parts[1] != "run" || cmd.Parts[2] != "main.go" {
		t.Errorf("parts = %v, want [go run main.go]", cmd.Parts)
	}
}

func TestParse_CommandEmptyArray(t *testing.T) {
	yaml := []byte(`
version: 1
services:
  app:
    dir: /tmp
    command: []
`)
	_, err := Parse(yaml, "/tmp")
	if err == nil {
		t.Fatal("expected error for empty command array")
	}
}

func TestParse_DurationFields(t *testing.T) {
	yaml := []byte(`
version: 1
global:
  shutdown_timeout: "30s"
  watch_debounce: "500ms"
services:
  app:
    dir: /tmp
    command: "echo hi"
    restart:
      backoff: "2s"
`)
	cfg, err := Parse(yaml, "/tmp")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cfg.Global.ShutdownTimeout.Duration != 30*time.Second {
		t.Errorf("shutdown_timeout = %v, want 30s", cfg.Global.ShutdownTimeout.Duration)
	}
	if cfg.Global.WatchDebounce.Duration != 500*time.Millisecond {
		t.Errorf("watch_debounce = %v, want 500ms", cfg.Global.WatchDebounce.Duration)
	}
	if cfg.Services["app"].Restart.Backoff.Duration != 2*time.Second {
		t.Errorf("backoff = %v, want 2s", cfg.Services["app"].Restart.Backoff.Duration)
	}
}

func TestParse_InvalidDuration(t *testing.T) {
	yaml := []byte(`
version: 1
global:
  shutdown_timeout: "not-a-duration"
services:
  app:
    dir: /tmp
    command: "echo hi"
`)
	_, err := Parse(yaml, "/tmp")
	if err == nil {
		t.Fatal("expected error for invalid duration")
	}
}

func TestParse_RelativePaths(t *testing.T) {
	baseDir := t.TempDir()
	yaml := []byte(`
version: 1
global:
  env_file: ".env"
services:
  app:
    dir: "src"
    command: "echo hi"
    env_file: "app.env"
`)
	cfg, err := Parse(yaml, baseDir)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	svc := cfg.Services["app"]
	wantDir := filepath.Join(baseDir, "src")
	if svc.Dir != wantDir {
		t.Errorf("dir = %q, want %q", svc.Dir, wantDir)
	}
	wantEnv := filepath.Join(baseDir, "app.env")
	if svc.EnvFile != wantEnv {
		t.Errorf("env_file = %q, want %q", svc.EnvFile, wantEnv)
	}
	wantGlobalEnv := filepath.Join(baseDir, ".env")
	if cfg.Global.EnvFile != wantGlobalEnv {
		t.Errorf("global env_file = %q, want %q", cfg.Global.EnvFile, wantGlobalEnv)
	}
}

func TestParse_AbsolutePathsUnchanged(t *testing.T) {
	yaml := []byte(`
version: 1
services:
  app:
    dir: "/absolute/path"
    command: "echo hi"
    env_file: "/absolute/env"
`)
	cfg, err := Parse(yaml, "/some/base")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	svc := cfg.Services["app"]
	if svc.Dir != "/absolute/path" {
		t.Errorf("dir = %q, want %q", svc.Dir, "/absolute/path")
	}
	if svc.EnvFile != "/absolute/env" {
		t.Errorf("env_file = %q, want %q", svc.EnvFile, "/absolute/env")
	}
}

func TestParse_InvalidYAML(t *testing.T) {
	_, err := Parse([]byte(`{{{invalid`), "/tmp")
	if err == nil {
		t.Fatal("expected error for invalid YAML")
	}
}

// TestParse_RejectsUnknownFields covers the strict-mode behaviour added to
// catch silent typos like `expect_status: 200` under a readiness block.
func TestParse_RejectsUnknownFields(t *testing.T) {
	cases := []struct {
		name    string
		yaml    string
		wantSub string
	}{
		{
			name: "unknown readiness field",
			yaml: `
version: 1
services:
  api:
    dir: /tmp
    command: "echo hi"
    readiness:
      kind: http
      url: "http://localhost:8080/health"
      expect_status: 200
`,
			wantSub: "expect_status",
		},
		{
			name: "unknown top-level service field",
			yaml: `
version: 1
services:
  api:
    dir: /tmp
    command: "echo hi"
    autostart: true
`,
			wantSub: "autostart",
		},
		{
			name: "unknown global field",
			yaml: `
version: 1
global:
  log_buffer_size: 1000
services:
  api:
    dir: /tmp
    command: "echo hi"
`,
			wantSub: "log_buffer_size",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := Parse([]byte(tc.yaml), "/tmp")
			if err == nil {
				t.Fatalf("expected parse error mentioning %q, got nil", tc.wantSub)
			}
			if !strings.Contains(err.Error(), tc.wantSub) {
				t.Fatalf("expected error to mention %q, got: %v", tc.wantSub, err)
			}
		})
	}
}

func TestParse_AutoStart(t *testing.T) {
	yaml := []byte(`
version: 1
services:
  svc_default:
    dir: /tmp
    command: "echo hi"
  svc_true:
    dir: /tmp
    command: "echo hi"
    auto_start: true
  svc_false:
    dir: /tmp
    command: "echo hi"
    auto_start: false
`)
	cfg, err := Parse(yaml, "/tmp")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	svcDefault := cfg.Services["svc_default"]
	if !svcDefault.GetAutoStart() {
		t.Error("svc_default: GetAutoStart() = false, want true (default)")
	}
	svcTrue := cfg.Services["svc_true"]
	if !svcTrue.GetAutoStart() {
		t.Error("svc_true: GetAutoStart() = false, want true")
	}
	svcFalse := cfg.Services["svc_false"]
	if svcFalse.GetAutoStart() {
		t.Error("svc_false: GetAutoStart() = true, want false")
	}
}

func TestParse_WatchDefaults(t *testing.T) {
	trueVal := true
	yaml := []byte(`
version: 1
services:
  watched:
    dir: /tmp
    command: "echo hi"
    watch:
      enabled: true
  unwatched:
    dir: /tmp
    command: "echo hi"
`)
	cfg, err := Parse(yaml, "/tmp")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	watched := cfg.Services["watched"]
	if !watched.Watch.IsEnabled() {
		t.Error("watched.Watch.IsEnabled() = false, want true")
	}
	_ = trueVal
	// When enabled and no paths specified, default to ["."]
	if len(watched.Watch.Paths) != 1 || watched.Watch.Paths[0] != "." {
		t.Errorf("watched.Watch.Paths = %v, want [\".\"]", watched.Watch.Paths)
	}
	if !watched.Watch.ShouldRestart() {
		t.Error("watched.Watch.ShouldRestart() = false, want true (default)")
	}

	unwatched := cfg.Services["unwatched"]
	if unwatched.Watch.IsEnabled() {
		t.Error("unwatched.Watch.IsEnabled() = true, want false (default)")
	}
}

func TestParse_GetShutdownTimeout(t *testing.T) {
	yaml := []byte(`
version: 1
global:
  shutdown_timeout: "20s"
services:
  with_override:
    dir: /tmp
    command: "echo hi"
    shutdown_timeout: "5s"
  without_override:
    dir: /tmp
    command: "echo hi"
`)
	cfg, err := Parse(yaml, "/tmp")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	withOverride := cfg.Services["with_override"]
	if got := withOverride.GetShutdownTimeout(cfg.Global.ShutdownTimeout); got != 5*time.Second {
		t.Errorf("with_override shutdown timeout = %v, want 5s", got)
	}

	withoutOverride := cfg.Services["without_override"]
	if got := withoutOverride.GetShutdownTimeout(cfg.Global.ShutdownTimeout); got != 20*time.Second {
		t.Errorf("without_override shutdown timeout = %v, want 20s", got)
	}

	// Test fallback to default 10s when global is zero
	zeroGlobal := Duration{}
	svc := ServiceConfig{}
	if got := svc.GetShutdownTimeout(zeroGlobal); got != 10*time.Second {
		t.Errorf("default shutdown timeout = %v, want 10s", got)
	}
}

func TestParse_GetWatchDebounce(t *testing.T) {
	yaml := []byte(`
version: 1
global:
  watch_debounce: "1s"
services:
  with_override:
    dir: /tmp
    command: "echo hi"
    watch:
      enabled: true
      debounce: "200ms"
  without_override:
    dir: /tmp
    command: "echo hi"
    watch:
      enabled: true
`)
	cfg, err := Parse(yaml, "/tmp")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	withOverride := cfg.Services["with_override"]
	if got := withOverride.Watch.GetDebounce(cfg.Global.WatchDebounce); got != 200*time.Millisecond {
		t.Errorf("with_override debounce = %v, want 200ms", got)
	}

	withoutOverride := cfg.Services["without_override"]
	if got := withoutOverride.Watch.GetDebounce(cfg.Global.WatchDebounce); got != 1*time.Second {
		t.Errorf("without_override debounce = %v, want 1s", got)
	}

	// Test fallback to 300ms when global is zero
	zeroGlobal := Duration{}
	w := WatchConfig{}
	if got := w.GetDebounce(zeroGlobal); got != 300*time.Millisecond {
		t.Errorf("default debounce = %v, want 300ms", got)
	}
}

func TestCommandString(t *testing.T) {
	tests := []struct {
		name string
		cmd  Command
		want string
	}{
		{
			name: "shell command",
			cmd:  Command{Shell: true, Parts: []string{"sh", "-c", "echo hello"}},
			want: "echo hello",
		},
		{
			name: "single part",
			cmd:  Command{Shell: false, Parts: []string{"mybin"}},
			want: "mybin",
		},
		{
			name: "multi part",
			cmd:  Command{Shell: false, Parts: []string{"go", "run", "."}},
			want: "[go run .]",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.cmd.String(); got != tt.want {
				t.Errorf("String() = %q, want %q", got, tt.want)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// Validate
// ---------------------------------------------------------------------------

func TestValidate_MissingServices(t *testing.T) {
	cfg := &Config{Version: 1}
	err := cfg.Validate()
	if err == nil {
		t.Fatal("expected validation error for no services")
	}
	assertContains(t, err.Error(), "no services defined")
}

func TestValidate_UnsupportedVersion(t *testing.T) {
	dir := t.TempDir()
	cfg := &Config{
		Version: 99,
		Services: map[string]ServiceConfig{
			"app": {
				Dir:     dir,
				Command: &Command{Parts: []string{"echo"}},
				Restart: RestartConfig{Policy: "never"},
			},
		},
	}
	err := cfg.Validate()
	if err == nil {
		t.Fatal("expected validation error for unsupported version")
	}
	assertContains(t, err.Error(), "unsupported config version: 99")
}

func TestValidate_MissingDir(t *testing.T) {
	cfg := &Config{
		Version: 1,
		Services: map[string]ServiceConfig{
			"app": {
				Command: &Command{Parts: []string{"echo"}},
				Restart: RestartConfig{Policy: "never"},
			},
		},
	}
	err := cfg.Validate()
	if err == nil {
		t.Fatal("expected validation error for missing dir")
	}
	assertContains(t, err.Error(), "dir is required")
}

func TestValidate_DirNotExist(t *testing.T) {
	cfg := &Config{
		Version: 1,
		Services: map[string]ServiceConfig{
			"app": {
				Dir:     "/nonexistent/path/that/does/not/exist",
				Command: &Command{Parts: []string{"echo"}},
				Restart: RestartConfig{Policy: "never"},
			},
		},
	}
	err := cfg.Validate()
	if err == nil {
		t.Fatal("expected validation error for nonexistent dir")
	}
	assertContains(t, err.Error(), "does not exist")
}

func TestValidate_DirIsFile(t *testing.T) {
	tmp := t.TempDir()
	f := filepath.Join(tmp, "notadir")
	if err := os.WriteFile(f, []byte("x"), 0644); err != nil {
		t.Fatal(err)
	}
	cfg := &Config{
		Version: 1,
		Services: map[string]ServiceConfig{
			"app": {
				Dir:     f,
				Command: &Command{Parts: []string{"echo"}},
				Restart: RestartConfig{Policy: "never"},
			},
		},
	}
	err := cfg.Validate()
	if err == nil {
		t.Fatal("expected validation error when dir is a file")
	}
	assertContains(t, err.Error(), "is not a directory")
}

func TestValidate_MissingCommand(t *testing.T) {
	dir := t.TempDir()
	cfg := &Config{
		Version: 1,
		Services: map[string]ServiceConfig{
			"app": {
				Dir:     dir,
				Restart: RestartConfig{Policy: "never"},
			},
		},
	}
	err := cfg.Validate()
	if err == nil {
		t.Fatal("expected validation error for missing command")
	}
	assertContains(t, err.Error(), "must have either command or container")
}

func TestValidate_InvalidRestartPolicy(t *testing.T) {
	dir := t.TempDir()
	cfg := &Config{
		Version: 1,
		Services: map[string]ServiceConfig{
			"app": {
				Dir:     dir,
				Command: &Command{Parts: []string{"echo"}},
				Restart: RestartConfig{Policy: "invalid-policy"},
			},
		},
	}
	err := cfg.Validate()
	if err == nil {
		t.Fatal("expected validation error for invalid restart policy")
	}
	assertContains(t, err.Error(), "invalid restart policy")
}

func TestValidate_ValidRestartPolicies(t *testing.T) {
	dir := t.TempDir()
	for _, policy := range []string{"never", "on-failure", "always"} {
		t.Run(policy, func(t *testing.T) {
			cfg := &Config{
				Version: 1,
				Services: map[string]ServiceConfig{
					"app": {
						Dir:     dir,
						Command: &Command{Parts: []string{"echo"}},
						Restart: RestartConfig{Policy: policy},
					},
				},
			}
			err := cfg.Validate()
			if err != nil {
				t.Errorf("unexpected validation error for policy %q: %v", policy, err)
			}
		})
	}
}

func TestValidate_UnknownDependsOn(t *testing.T) {
	dir := t.TempDir()
	cfg := &Config{
		Version: 1,
		Services: map[string]ServiceConfig{
			"app": {
				Dir:       dir,
				Command:   &Command{Parts: []string{"echo"}},
				Restart:   RestartConfig{Policy: "never"},
				DependsOn: []string{"nonexistent"},
			},
		},
	}
	err := cfg.Validate()
	if err == nil {
		t.Fatal("expected validation error for unknown depends_on")
	}
	assertContains(t, err.Error(), "references unknown service")
}

func TestValidate_CycleDetection(t *testing.T) {
	dir := t.TempDir()
	cfg := &Config{
		Version: 1,
		Services: map[string]ServiceConfig{
			"a": {
				Dir:       dir,
				Command:   &Command{Parts: []string{"echo"}},
				Restart:   RestartConfig{Policy: "never"},
				DependsOn: []string{"b"},
			},
			"b": {
				Dir:       dir,
				Command:   &Command{Parts: []string{"echo"}},
				Restart:   RestartConfig{Policy: "never"},
				DependsOn: []string{"a"},
			},
		},
	}
	err := cfg.Validate()
	if err == nil {
		t.Fatal("expected validation error for dependency cycle")
	}
	assertContains(t, err.Error(), "dependency cycle detected")
}

func TestValidate_SelfCycle(t *testing.T) {
	dir := t.TempDir()
	cfg := &Config{
		Version: 1,
		Services: map[string]ServiceConfig{
			"a": {
				Dir:       dir,
				Command:   &Command{Parts: []string{"echo"}},
				Restart:   RestartConfig{Policy: "never"},
				DependsOn: []string{"a"},
			},
		},
	}
	err := cfg.Validate()
	if err == nil {
		t.Fatal("expected validation error for self-referencing dependency")
	}
	assertContains(t, err.Error(), "dependency cycle detected")
}

func TestValidate_ThreeNodeCycle(t *testing.T) {
	dir := t.TempDir()
	cfg := &Config{
		Version: 1,
		Services: map[string]ServiceConfig{
			"a": {
				Dir:       dir,
				Command:   &Command{Parts: []string{"echo"}},
				Restart:   RestartConfig{Policy: "never"},
				DependsOn: []string{"b"},
			},
			"b": {
				Dir:       dir,
				Command:   &Command{Parts: []string{"echo"}},
				Restart:   RestartConfig{Policy: "never"},
				DependsOn: []string{"c"},
			},
			"c": {
				Dir:       dir,
				Command:   &Command{Parts: []string{"echo"}},
				Restart:   RestartConfig{Policy: "never"},
				DependsOn: []string{"a"},
			},
		},
	}
	err := cfg.Validate()
	if err == nil {
		t.Fatal("expected validation error for 3-node cycle")
	}
	assertContains(t, err.Error(), "dependency cycle detected")
}

func TestValidate_EnvFileMissing(t *testing.T) {
	dir := t.TempDir()
	cfg := &Config{
		Version: 1,
		Services: map[string]ServiceConfig{
			"app": {
				Dir:     dir,
				Command: &Command{Parts: []string{"echo"}},
				Restart: RestartConfig{Policy: "never"},
				EnvFile: filepath.Join(dir, "nonexistent.env"),
			},
		},
	}
	err := cfg.Validate()
	if err == nil {
		t.Fatal("expected validation error for missing env_file")
	}
	assertContains(t, err.Error(), "env_file")
	assertContains(t, err.Error(), "could not be read")
}

func TestValidate_EnvFileExists(t *testing.T) {
	dir := t.TempDir()
	envPath := filepath.Join(dir, "test.env")
	if err := os.WriteFile(envPath, []byte("KEY=val\n"), 0644); err != nil {
		t.Fatal(err)
	}
	cfg := &Config{
		Version: 1,
		Services: map[string]ServiceConfig{
			"app": {
				Dir:     dir,
				Command: &Command{Parts: []string{"echo"}},
				Restart: RestartConfig{Policy: "never"},
				EnvFile: envPath,
			},
		},
	}
	if err := cfg.Validate(); err != nil {
		t.Fatalf("unexpected validation error: %v", err)
	}
}

func TestValidate_GlobalEnvFileMissing(t *testing.T) {
	dir := t.TempDir()
	cfg := &Config{
		Version: 1,
		Global: GlobalConfig{
			EnvFile: filepath.Join(dir, "missing.env"),
		},
		Services: map[string]ServiceConfig{
			"app": {
				Dir:     dir,
				Command: &Command{Parts: []string{"echo"}},
				Restart: RestartConfig{Policy: "never"},
			},
		},
	}
	err := cfg.Validate()
	if err == nil {
		t.Fatal("expected validation error for missing global env_file")
	}
	assertContains(t, err.Error(), "global env_file")
}

func TestValidate_ReadinessKinds(t *testing.T) {
	dir := t.TempDir()
	tests := []struct {
		name      string
		readiness ReadinessConfig
		wantErr   string
	}{
		{name: "none", readiness: ReadinessConfig{Kind: "none"}, wantErr: ""},
		{name: "empty", readiness: ReadinessConfig{Kind: ""}, wantErr: ""},
		{name: "log_pattern valid", readiness: ReadinessConfig{Kind: "log_pattern", Pattern: "ready"}, wantErr: ""},
		{name: "log_pattern missing pattern", readiness: ReadinessConfig{Kind: "log_pattern"}, wantErr: "requires a pattern"},
		{name: "tcp valid", readiness: ReadinessConfig{Kind: "tcp", Address: ":8080"}, wantErr: ""},
		{name: "tcp missing address", readiness: ReadinessConfig{Kind: "tcp"}, wantErr: "requires an address"},
		{name: "http valid", readiness: ReadinessConfig{Kind: "http", URL: "http://localhost"}, wantErr: ""},
		{name: "http missing url", readiness: ReadinessConfig{Kind: "http"}, wantErr: "requires a url"},
		{name: "invalid kind", readiness: ReadinessConfig{Kind: "bogus"}, wantErr: "invalid readiness kind"},
		{name: "exec valid", readiness: ReadinessConfig{Kind: "exec", Command: &Command{Parts: []string{"echo", "ok"}}}, wantErr: ""},
		{name: "exec missing command", readiness: ReadinessConfig{Kind: "exec"}, wantErr: "requires a command"},
		{name: "grpc valid", readiness: ReadinessConfig{Kind: "grpc", Address: "localhost:50051"}, wantErr: ""},
		{name: "grpc missing address", readiness: ReadinessConfig{Kind: "grpc"}, wantErr: "requires an address"},
		{name: "negative max_attempts", readiness: ReadinessConfig{Kind: "none", MaxAttempts: -1}, wantErr: "max_attempts must be >= 0"},
		{name: "negative interval", readiness: ReadinessConfig{Kind: "none", Interval: Duration{Duration: -time.Second}}, wantErr: "interval must be >= 0"},
		{name: "negative settle", readiness: ReadinessConfig{Kind: "none", Settle: Duration{Duration: -time.Second}}, wantErr: "settle must be >= 0"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := &Config{
				Version: 1,
				Services: map[string]ServiceConfig{
					"app": {
						Dir:       dir,
						Command:   &Command{Parts: []string{"echo"}},
						Restart:   RestartConfig{Policy: "never"},
						Readiness: tt.readiness,
					},
				},
			}
			err := cfg.Validate()
			if tt.wantErr == "" {
				if err != nil {
					t.Errorf("unexpected error: %v", err)
				}
			} else {
				if err == nil {
					t.Fatal("expected validation error")
				}
				assertContains(t, err.Error(), tt.wantErr)
			}
		})
	}
}

func TestValidate_ValidationErrorType(t *testing.T) {
	cfg := &Config{Version: 99}
	err := cfg.Validate()
	if err == nil {
		t.Fatal("expected error")
	}
	ve, ok := err.(*ValidationError)
	if !ok {
		t.Fatalf("expected *ValidationError, got %T", err)
	}
	if len(ve.Errors) == 0 {
		t.Error("expected at least one error in ValidationError.Errors")
	}
}

func TestValidate_MultipleErrors(t *testing.T) {
	cfg := &Config{
		Version: 99,
		Services: map[string]ServiceConfig{
			"app": {
				// missing dir, missing command, invalid policy
				Restart: RestartConfig{Policy: "bogus"},
			},
		},
	}
	err := cfg.Validate()
	if err == nil {
		t.Fatal("expected validation error")
	}
	ve := err.(*ValidationError)
	// Should have at least: version, dir required, command required, invalid policy
	if len(ve.Errors) < 3 {
		t.Errorf("expected at least 3 errors, got %d: %v", len(ve.Errors), ve.Errors)
	}
}

func TestValidate_FullyValid(t *testing.T) {
	dir := t.TempDir()
	cfg := &Config{
		Version: 1,
		Services: map[string]ServiceConfig{
			"db": {
				Dir:     dir,
				Command: &Command{Parts: []string{"postgres"}},
				Restart: RestartConfig{Policy: "always"},
			},
			"api": {
				Dir:       dir,
				Command:   &Command{Shell: true, Parts: []string{"sh", "-c", "go run ."}},
				Restart:   RestartConfig{Policy: "on-failure"},
				DependsOn: []string{"db"},
			},
		},
	}
	if err := cfg.Validate(); err != nil {
		t.Fatalf("expected no validation errors, got: %v", err)
	}
}

// ---------------------------------------------------------------------------
// StartOrder
// ---------------------------------------------------------------------------

func TestStartOrder_NoDeps(t *testing.T) {
	dir := t.TempDir()
	cfg := &Config{
		Version: 1,
		Services: map[string]ServiceConfig{
			"alpha": {Dir: dir, Command: &Command{Parts: []string{"a"}}, Restart: RestartConfig{Policy: "never"}},
			"beta":  {Dir: dir, Command: &Command{Parts: []string{"b"}}, Restart: RestartConfig{Policy: "never"}},
			"gamma": {Dir: dir, Command: &Command{Parts: []string{"c"}}, Restart: RestartConfig{Policy: "never"}},
		},
	}
	order, err := cfg.StartOrder()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(order) != 3 {
		t.Fatalf("expected 3 items, got %d", len(order))
	}
	// With no deps, should be sorted alphabetically
	expected := []string{"alpha", "beta", "gamma"}
	for i, want := range expected {
		if order[i] != want {
			t.Errorf("order[%d] = %q, want %q", i, order[i], want)
		}
	}
}

func TestStartOrder_LinearDeps(t *testing.T) {
	dir := t.TempDir()
	cfg := &Config{
		Version: 1,
		Services: map[string]ServiceConfig{
			"app": {Dir: dir, Command: &Command{Parts: []string{"a"}}, Restart: RestartConfig{Policy: "never"}, DependsOn: []string{"api"}},
			"api": {Dir: dir, Command: &Command{Parts: []string{"b"}}, Restart: RestartConfig{Policy: "never"}, DependsOn: []string{"db"}},
			"db":  {Dir: dir, Command: &Command{Parts: []string{"c"}}, Restart: RestartConfig{Policy: "never"}},
		},
	}
	order, err := cfg.StartOrder()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(order) != 3 {
		t.Fatalf("expected 3 items, got %d", len(order))
	}
	// db must come before api, api before app
	idxOf := func(name string) int {
		for i, n := range order {
			if n == name {
				return i
			}
		}
		return -1
	}
	if idxOf("db") >= idxOf("api") {
		t.Errorf("db (idx %d) should come before api (idx %d)", idxOf("db"), idxOf("api"))
	}
	if idxOf("api") >= idxOf("app") {
		t.Errorf("api (idx %d) should come before app (idx %d)", idxOf("api"), idxOf("app"))
	}
}

func TestStartOrder_DiamondDeps(t *testing.T) {
	dir := t.TempDir()
	// Diamond: top depends on left and right, both depend on bottom
	//       top
	//      /   \
	//   left   right
	//      \   /
	//      bottom
	cfg := &Config{
		Version: 1,
		Services: map[string]ServiceConfig{
			"top":    {Dir: dir, Command: &Command{Parts: []string{"t"}}, Restart: RestartConfig{Policy: "never"}, DependsOn: []string{"left", "right"}},
			"left":   {Dir: dir, Command: &Command{Parts: []string{"l"}}, Restart: RestartConfig{Policy: "never"}, DependsOn: []string{"bottom"}},
			"right":  {Dir: dir, Command: &Command{Parts: []string{"r"}}, Restart: RestartConfig{Policy: "never"}, DependsOn: []string{"bottom"}},
			"bottom": {Dir: dir, Command: &Command{Parts: []string{"b"}}, Restart: RestartConfig{Policy: "never"}},
		},
	}
	order, err := cfg.StartOrder()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(order) != 4 {
		t.Fatalf("expected 4 items, got %d: %v", len(order), order)
	}
	idxOf := func(name string) int {
		for i, n := range order {
			if n == name {
				return i
			}
		}
		return -1
	}
	// bottom must be first (before left and right)
	if idxOf("bottom") >= idxOf("left") {
		t.Errorf("bottom should come before left: %v", order)
	}
	if idxOf("bottom") >= idxOf("right") {
		t.Errorf("bottom should come before right: %v", order)
	}
	// left and right must come before top
	if idxOf("left") >= idxOf("top") {
		t.Errorf("left should come before top: %v", order)
	}
	if idxOf("right") >= idxOf("top") {
		t.Errorf("right should come before top: %v", order)
	}
}

func TestStartOrder_SingleService(t *testing.T) {
	dir := t.TempDir()
	cfg := &Config{
		Version: 1,
		Services: map[string]ServiceConfig{
			"only": {Dir: dir, Command: &Command{Parts: []string{"x"}}, Restart: RestartConfig{Policy: "never"}},
		},
	}
	order, err := cfg.StartOrder()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(order) != 1 || order[0] != "only" {
		t.Errorf("order = %v, want [only]", order)
	}
}

func TestStartOrder_CycleReturnsError(t *testing.T) {
	dir := t.TempDir()
	cfg := &Config{
		Version: 1,
		Services: map[string]ServiceConfig{
			"a": {Dir: dir, Command: &Command{Parts: []string{"x"}}, Restart: RestartConfig{Policy: "never"}, DependsOn: []string{"b"}},
			"b": {Dir: dir, Command: &Command{Parts: []string{"x"}}, Restart: RestartConfig{Policy: "never"}, DependsOn: []string{"a"}},
		},
	}
	_, err := cfg.StartOrder()
	if err == nil {
		t.Fatal("expected error for cycle in StartOrder")
	}
	assertContains(t, err.Error(), "dependency cycle detected")
}

// ---------------------------------------------------------------------------
// LoadEnvFile
// ---------------------------------------------------------------------------

func TestLoadEnvFile_BasicKeyValue(t *testing.T) {
	tmp := t.TempDir()
	path := filepath.Join(tmp, ".env")
	content := "FOO=bar\nBAZ=qux\n"
	if err := os.WriteFile(path, []byte(content), 0644); err != nil {
		t.Fatal(err)
	}
	env, err := LoadEnvFile(path)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(env) != 2 {
		t.Fatalf("expected 2 entries, got %d: %v", len(env), env)
	}
	if env[0] != "FOO=bar" {
		t.Errorf("env[0] = %q, want %q", env[0], "FOO=bar")
	}
	if env[1] != "BAZ=qux" {
		t.Errorf("env[1] = %q, want %q", env[1], "BAZ=qux")
	}
}

func TestLoadEnvFile_QuotedValues(t *testing.T) {
	tmp := t.TempDir()
	path := filepath.Join(tmp, ".env")
	content := `DOUBLE="hello world"
SINGLE='hello world'
UNQUOTED=hello world
`
	if err := os.WriteFile(path, []byte(content), 0644); err != nil {
		t.Fatal(err)
	}
	env, err := LoadEnvFile(path)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(env) != 3 {
		t.Fatalf("expected 3 entries, got %d: %v", len(env), env)
	}
	if env[0] != "DOUBLE=hello world" {
		t.Errorf("env[0] = %q, want %q", env[0], "DOUBLE=hello world")
	}
	if env[1] != "SINGLE=hello world" {
		t.Errorf("env[1] = %q, want %q", env[1], "SINGLE=hello world")
	}
	if env[2] != "UNQUOTED=hello world" {
		t.Errorf("env[2] = %q, want %q", env[2], "UNQUOTED=hello world")
	}
}

func TestLoadEnvFile_Comments(t *testing.T) {
	tmp := t.TempDir()
	path := filepath.Join(tmp, ".env")
	content := "# this is a comment\nKEY=val\n# another comment\n"
	if err := os.WriteFile(path, []byte(content), 0644); err != nil {
		t.Fatal(err)
	}
	env, err := LoadEnvFile(path)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(env) != 1 {
		t.Fatalf("expected 1 entry, got %d: %v", len(env), env)
	}
	if env[0] != "KEY=val" {
		t.Errorf("env[0] = %q, want %q", env[0], "KEY=val")
	}
}

func TestLoadEnvFile_ExportPrefix(t *testing.T) {
	tmp := t.TempDir()
	path := filepath.Join(tmp, ".env")
	content := "export FOO=bar\nexport BAZ=qux\n"
	if err := os.WriteFile(path, []byte(content), 0644); err != nil {
		t.Fatal(err)
	}
	env, err := LoadEnvFile(path)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(env) != 2 {
		t.Fatalf("expected 2 entries, got %d: %v", len(env), env)
	}
	if env[0] != "FOO=bar" {
		t.Errorf("env[0] = %q, want %q", env[0], "FOO=bar")
	}
	if env[1] != "BAZ=qux" {
		t.Errorf("env[1] = %q, want %q", env[1], "BAZ=qux")
	}
}

func TestLoadEnvFile_EmptyLines(t *testing.T) {
	tmp := t.TempDir()
	path := filepath.Join(tmp, ".env")
	content := "\n\nFOO=bar\n\n\nBAZ=qux\n\n"
	if err := os.WriteFile(path, []byte(content), 0644); err != nil {
		t.Fatal(err)
	}
	env, err := LoadEnvFile(path)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(env) != 2 {
		t.Fatalf("expected 2 entries, got %d: %v", len(env), env)
	}
	if env[0] != "FOO=bar" {
		t.Errorf("env[0] = %q, want %q", env[0], "FOO=bar")
	}
	if env[1] != "BAZ=qux" {
		t.Errorf("env[1] = %q, want %q", env[1], "BAZ=qux")
	}
}

func TestLoadEnvFile_EmptyFile(t *testing.T) {
	tmp := t.TempDir()
	path := filepath.Join(tmp, ".env")
	if err := os.WriteFile(path, []byte(""), 0644); err != nil {
		t.Fatal(err)
	}
	env, err := LoadEnvFile(path)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(env) != 0 {
		t.Errorf("expected 0 entries, got %d: %v", len(env), env)
	}
}

func TestLoadEnvFile_EmptyValue(t *testing.T) {
	tmp := t.TempDir()
	path := filepath.Join(tmp, ".env")
	content := "EMPTY=\n"
	if err := os.WriteFile(path, []byte(content), 0644); err != nil {
		t.Fatal(err)
	}
	env, err := LoadEnvFile(path)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(env) != 1 {
		t.Fatalf("expected 1 entry, got %d: %v", len(env), env)
	}
	if env[0] != "EMPTY=" {
		t.Errorf("env[0] = %q, want %q", env[0], "EMPTY=")
	}
}

func TestLoadEnvFile_WindowsLineEndings(t *testing.T) {
	tmp := t.TempDir()
	path := filepath.Join(tmp, ".env")
	content := "FOO=bar\r\nBAZ=qux\r\n"
	if err := os.WriteFile(path, []byte(content), 0644); err != nil {
		t.Fatal(err)
	}
	env, err := LoadEnvFile(path)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(env) != 2 {
		t.Fatalf("expected 2 entries, got %d: %v", len(env), env)
	}
	if env[0] != "FOO=bar" {
		t.Errorf("env[0] = %q, want %q", env[0], "FOO=bar")
	}
	if env[1] != "BAZ=qux" {
		t.Errorf("env[1] = %q, want %q", env[1], "BAZ=qux")
	}
}

func TestLoadEnvFile_ValueWithEquals(t *testing.T) {
	tmp := t.TempDir()
	path := filepath.Join(tmp, ".env")
	content := "URL=postgres://user:pass@host/db?opt=val\n"
	if err := os.WriteFile(path, []byte(content), 0644); err != nil {
		t.Fatal(err)
	}
	env, err := LoadEnvFile(path)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(env) != 1 {
		t.Fatalf("expected 1 entry, got %d: %v", len(env), env)
	}
	// The first = splits key/value; subsequent = signs are part of the value
	if env[0] != "URL=postgres://user:pass@host/db?opt=val" {
		t.Errorf("env[0] = %q, want %q", env[0], "URL=postgres://user:pass@host/db?opt=val")
	}
}

func TestLoadEnvFile_WhitespaceAroundLines(t *testing.T) {
	tmp := t.TempDir()
	path := filepath.Join(tmp, ".env")
	content := "  FOO=bar  \n\tBAZ=qux\t\n"
	if err := os.WriteFile(path, []byte(content), 0644); err != nil {
		t.Fatal(err)
	}
	env, err := LoadEnvFile(path)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(env) != 2 {
		t.Fatalf("expected 2 entries, got %d: %v", len(env), env)
	}
	if env[0] != "FOO=bar" {
		t.Errorf("env[0] = %q, want %q", env[0], "FOO=bar")
	}
	if env[1] != "BAZ=qux" {
		t.Errorf("env[1] = %q, want %q", env[1], "BAZ=qux")
	}
}

func TestLoadEnvFile_FileNotFound(t *testing.T) {
	_, err := LoadEnvFile("/nonexistent/path/.env")
	if err == nil {
		t.Fatal("expected error for missing file")
	}
}

func TestLoadEnvFile_MixedContent(t *testing.T) {
	tmp := t.TempDir()
	path := filepath.Join(tmp, ".env")
	content := `# Database config
export DB_HOST="localhost"
DB_PORT=5432

# App settings
APP_NAME='my-app'
export DEBUG=true
`
	if err := os.WriteFile(path, []byte(content), 0644); err != nil {
		t.Fatal(err)
	}
	env, err := LoadEnvFile(path)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	expected := []string{
		"DB_HOST=localhost",
		"DB_PORT=5432",
		"APP_NAME=my-app",
		"DEBUG=true",
	}
	if len(env) != len(expected) {
		t.Fatalf("expected %d entries, got %d: %v", len(expected), len(env), env)
	}
	for i, want := range expected {
		if env[i] != want {
			t.Errorf("env[%d] = %q, want %q", i, env[i], want)
		}
	}
}

func TestLoadEnvFile_NoTrailingNewline(t *testing.T) {
	tmp := t.TempDir()
	path := filepath.Join(tmp, ".env")
	content := "KEY=value"
	if err := os.WriteFile(path, []byte(content), 0644); err != nil {
		t.Fatal(err)
	}
	env, err := LoadEnvFile(path)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(env) != 1 {
		t.Fatalf("expected 1 entry, got %d: %v", len(env), env)
	}
	if env[0] != "KEY=value" {
		t.Errorf("env[0] = %q, want %q", env[0], "KEY=value")
	}
}

// ---------------------------------------------------------------------------
// FindConfig
// ---------------------------------------------------------------------------

func TestFindConfig_NoConfigExists(t *testing.T) {
	tmp := t.TempDir()
	// Save and restore working directory
	orig, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = os.Chdir(orig) }()

	if err := os.Chdir(tmp); err != nil {
		t.Fatal(err)
	}
	_, err = FindConfig()
	if err == nil {
		t.Fatal("expected error when no config file exists")
	}
	assertContains(t, err.Error(), "no bench.yml found")
}

func TestFindConfig_FindsInCurrentDir(t *testing.T) {
	tmp := resolveSymlinks(t, t.TempDir())
	configPath := filepath.Join(tmp, "bench.yml")
	if err := os.WriteFile(configPath, []byte("version: 1\n"), 0644); err != nil {
		t.Fatal(err)
	}

	orig, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = os.Chdir(orig) }()

	if err := os.Chdir(tmp); err != nil {
		t.Fatal(err)
	}
	found, err := FindConfig()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if found != configPath {
		t.Errorf("found = %q, want %q", found, configPath)
	}
}

func TestFindConfig_FindsYamlExtension(t *testing.T) {
	tmp := resolveSymlinks(t, t.TempDir())
	configPath := filepath.Join(tmp, "bench.yaml")
	if err := os.WriteFile(configPath, []byte("version: 1\n"), 0644); err != nil {
		t.Fatal(err)
	}

	orig, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = os.Chdir(orig) }()

	if err := os.Chdir(tmp); err != nil {
		t.Fatal(err)
	}
	found, err := FindConfig()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if found != configPath {
		t.Errorf("found = %q, want %q", found, configPath)
	}
}

func TestFindConfig_FindsInParentDir(t *testing.T) {
	tmp := resolveSymlinks(t, t.TempDir())
	configPath := filepath.Join(tmp, "bench.yml")
	if err := os.WriteFile(configPath, []byte("version: 1\n"), 0644); err != nil {
		t.Fatal(err)
	}
	child := filepath.Join(tmp, "subdir")
	if err := os.Mkdir(child, 0755); err != nil {
		t.Fatal(err)
	}

	orig, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = os.Chdir(orig) }()

	if err := os.Chdir(child); err != nil {
		t.Fatal(err)
	}
	found, err := FindConfig()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if found != configPath {
		t.Errorf("found = %q, want %q", found, configPath)
	}
}

func TestFindConfig_PrefersYmlOverYaml(t *testing.T) {
	tmp := resolveSymlinks(t, t.TempDir())
	ymlPath := filepath.Join(tmp, "bench.yml")
	yamlPath := filepath.Join(tmp, "bench.yaml")
	if err := os.WriteFile(ymlPath, []byte("version: 1\n"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(yamlPath, []byte("version: 1\n"), 0644); err != nil {
		t.Fatal(err)
	}

	orig, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = os.Chdir(orig) }()

	if err := os.Chdir(tmp); err != nil {
		t.Fatal(err)
	}
	found, err := FindConfig()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// bench.yml is tried first in the names slice
	if found != ymlPath {
		t.Errorf("found = %q, want %q (bench.yml preferred over bench.yaml)", found, ymlPath)
	}
}

// ---------------------------------------------------------------------------
// Load (integration)
// ---------------------------------------------------------------------------

func TestLoad_FullRoundtrip(t *testing.T) {
	tmp := t.TempDir()
	svcDir := filepath.Join(tmp, "myapp")
	if err := os.Mkdir(svcDir, 0755); err != nil {
		t.Fatal(err)
	}
	configPath := filepath.Join(tmp, "bench.yml")
	content := `version: 1
global:
  shutdown_timeout: "15s"
  log_buffer_lines: 1000
services:
  myapp:
    dir: myapp
    command: "go run ."
    restart:
      policy: on-failure
      max_retries: 3
`
	if err := os.WriteFile(configPath, []byte(content), 0644); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load(configPath)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cfg.Global.ShutdownTimeout.Duration != 15*time.Second {
		t.Errorf("shutdown_timeout = %v, want 15s", cfg.Global.ShutdownTimeout.Duration)
	}
	if cfg.Global.LogBufferLines != 1000 {
		t.Errorf("log_buffer_lines = %d, want 1000", cfg.Global.LogBufferLines)
	}
	svc := cfg.Services["myapp"]
	if svc.Dir != svcDir {
		t.Errorf("dir = %q, want %q", svc.Dir, svcDir)
	}
	if svc.Restart.Policy != "on-failure" {
		t.Errorf("restart.policy = %q, want %q", svc.Restart.Policy, "on-failure")
	}
	if svc.Restart.MaxRetries != 3 {
		t.Errorf("restart.max_retries = %d, want 3", svc.Restart.MaxRetries)
	}
}

func TestLoad_FileNotFound(t *testing.T) {
	_, err := Load("/nonexistent/bench.yml")
	if err == nil {
		t.Fatal("expected error for missing file")
	}
}

// ---------------------------------------------------------------------------
// helpers
// ---------------------------------------------------------------------------

func assertContains(t *testing.T, got, substr string) {
	t.Helper()
	if !strings.Contains(got, substr) {
		t.Errorf("expected error to contain %q, got %q", substr, got)
	}
}

// resolveSymlinks resolves symlinks in a path so that comparisons work on
// macOS where /var is a symlink to /private/var.
func resolveSymlinks(t *testing.T, path string) string {
	t.Helper()
	resolved, err := filepath.EvalSymlinks(path)
	if err != nil {
		t.Fatalf("EvalSymlinks(%q): %v", path, err)
	}
	return resolved
}

// ---------------------------------------------------------------------------
// Tracing config
// ---------------------------------------------------------------------------

func TestParse_TracingConfig(t *testing.T) {
	yaml := []byte(`
version: 1
global:
  tracing:
    enabled: true
    port: 9999
    buffer_size: "1GB"
services:
  app:
    dir: /tmp
    command: "echo hi"
`)
	cfg, err := Parse(yaml, "/tmp")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !cfg.Global.Tracing.Enabled {
		t.Error("tracing.enabled = false, want true")
	}
	if cfg.Global.Tracing.Port != 9999 {
		t.Errorf("tracing.port = %d, want 9999", cfg.Global.Tracing.Port)
	}
	wantSize := ByteSize(1024 * 1024 * 1024) // 1GB
	if cfg.Global.Tracing.BufferSize != wantSize {
		t.Errorf("tracing.buffer_size = %d, want %d", cfg.Global.Tracing.BufferSize, wantSize)
	}
}

func TestParse_TracingDefaults(t *testing.T) {
	yaml := []byte(`
version: 1
services:
  app:
    dir: /tmp
    command: "echo hi"
`)
	cfg, err := Parse(yaml, "/tmp")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cfg.Global.Tracing.Enabled {
		t.Error("tracing.enabled should default to false")
	}
	if cfg.Global.Tracing.Port != 4318 {
		t.Errorf("tracing.port = %d, want 4318 (default)", cfg.Global.Tracing.Port)
	}
	wantSize := ByteSize(500 * 1024 * 1024) // 500MB
	if cfg.Global.Tracing.BufferSize != wantSize {
		t.Errorf("tracing.buffer_size = %d, want %d (500MB default)", cfg.Global.Tracing.BufferSize, wantSize)
	}
}

// ---------------------------------------------------------------------------
// ByteSize parsing
// ---------------------------------------------------------------------------

func TestByteSize_Parsing(t *testing.T) {
	tests := []struct {
		input string
		want  int64
	}{
		{"500MB", 500 * 1024 * 1024},
		{"1GB", 1024 * 1024 * 1024},
		{"1.5GB", int64(1.5 * 1024 * 1024 * 1024)},
		{"1024KB", 1024 * 1024},
		{"100B", 100},
		{"2TB", 2 * 1024 * 1024 * 1024 * 1024},
		{"512mb", 512 * 1024 * 1024},
		{"10M", 10 * 1024 * 1024},
		{"1K", 1024},
	}
	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			got, err := parseByteSize(tt.input)
			if err != nil {
				t.Fatalf("parseByteSize(%q) error: %v", tt.input, err)
			}
			if got != tt.want {
				t.Errorf("parseByteSize(%q) = %d, want %d", tt.input, got, tt.want)
			}
		})
	}
}

func TestByteSize_Invalid(t *testing.T) {
	invalid := []string{
		"",
		"XYZ",
		"500QQ",
	}
	for _, input := range invalid {
		t.Run(input, func(t *testing.T) {
			_, err := parseByteSize(input)
			if err == nil {
				t.Errorf("parseByteSize(%q) expected error, got nil", input)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// Tracing validation
// ---------------------------------------------------------------------------

func TestValidate_TracingInvalidPort(t *testing.T) {
	dir := t.TempDir()
	cfg := &Config{
		Version: 1,
		Global: GlobalConfig{
			Tracing: TracingConfig{
				Enabled:    true,
				Port:       0,
				BufferSize: ByteSize(500 * 1024 * 1024),
			},
		},
		Services: map[string]ServiceConfig{
			"app": {
				Dir:     dir,
				Command: &Command{Parts: []string{"echo"}},
				Restart: RestartConfig{Policy: "never"},
			},
		},
	}
	err := cfg.Validate()
	if err == nil {
		t.Fatal("expected validation error for invalid tracing port")
	}
	assertContains(t, err.Error(), "tracing port must be between")
}

func TestValidate_TracingPortTooHigh(t *testing.T) {
	dir := t.TempDir()
	cfg := &Config{
		Version: 1,
		Global: GlobalConfig{
			Tracing: TracingConfig{
				Enabled:    true,
				Port:       70000,
				BufferSize: ByteSize(500 * 1024 * 1024),
			},
		},
		Services: map[string]ServiceConfig{
			"app": {
				Dir:     dir,
				Command: &Command{Parts: []string{"echo"}},
				Restart: RestartConfig{Policy: "never"},
			},
		},
	}
	err := cfg.Validate()
	if err == nil {
		t.Fatal("expected validation error for port > 65535")
	}
	assertContains(t, err.Error(), "tracing port must be between")
}

func TestValidate_TracingInvalidBufferSize(t *testing.T) {
	dir := t.TempDir()
	cfg := &Config{
		Version: 1,
		Global: GlobalConfig{
			Tracing: TracingConfig{
				Enabled:    true,
				Port:       4318,
				BufferSize: ByteSize(0),
			},
		},
		Services: map[string]ServiceConfig{
			"app": {
				Dir:     dir,
				Command: &Command{Parts: []string{"echo"}},
				Restart: RestartConfig{Policy: "never"},
			},
		},
	}
	err := cfg.Validate()
	if err == nil {
		t.Fatal("expected validation error for zero buffer_size")
	}
	assertContains(t, err.Error(), "tracing buffer_size must be greater than 0")
}

func TestValidate_TracingDisabledSkipsValidation(t *testing.T) {
	dir := t.TempDir()
	cfg := &Config{
		Version: 1,
		Global: GlobalConfig{
			Tracing: TracingConfig{
				Enabled:    false,
				Port:       0,
				BufferSize: ByteSize(0),
			},
		},
		Services: map[string]ServiceConfig{
			"app": {
				Dir:     dir,
				Command: &Command{Parts: []string{"echo"}},
				Restart: RestartConfig{Policy: "never"},
			},
		},
	}
	if err := cfg.Validate(); err != nil {
		t.Fatalf("expected no error when tracing is disabled, got: %v", err)
	}
}

// ---------------------------------------------------------------------------
// extends:
// ---------------------------------------------------------------------------

// writeYAML writes a config file inside dir and returns its absolute path.
func writeYAML(t *testing.T, dir, name, body string) string {
	t.Helper()
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, []byte(body), 0644); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
	return path
}

func TestLoad_Extends_BasicInheritance(t *testing.T) {
	tmp := t.TempDir()
	if err := os.Mkdir(filepath.Join(tmp, "api"), 0755); err != nil {
		t.Fatal(err)
	}
	writeYAML(t, tmp, "core.yml", `
version: 1
services:
  postgres:
    container:
      image: postgres:16
      ports: ["5432:5432"]
`)
	child := writeYAML(t, tmp, "forge.yml", `
version: 1
extends: core.yml
services:
  api:
    dir: api
    command: "echo hi"
`)
	cfg, err := Load(child)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if _, ok := cfg.Services["postgres"]; !ok {
		t.Errorf("expected inherited service 'postgres'")
	}
	if _, ok := cfg.Services["api"]; !ok {
		t.Errorf("expected own service 'api'")
	}
	if cfg.Extends != "" {
		t.Errorf("Extends should be cleared on merged config, got %q", cfg.Extends)
	}
}

func TestLoad_Extends_RelativePathResolution(t *testing.T) {
	tmp := t.TempDir()
	parentDir := filepath.Join(tmp, "bench")
	if err := os.Mkdir(parentDir, 0755); err != nil {
		t.Fatal(err)
	}
	parentSvcDir := filepath.Join(parentDir, "svc")
	if err := os.Mkdir(parentSvcDir, 0755); err != nil {
		t.Fatal(err)
	}
	writeYAML(t, parentDir, "core.yml", `
version: 1
services:
  worker:
    dir: ./svc
    command: "echo hi"
`)
	child := writeYAML(t, tmp, "forge.yml", `
version: 1
extends: bench/core.yml
services:
  api:
    dir: .
    command: "echo hi"
`)
	cfg, err := Load(child)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	wantWorker := resolveSymlinks(t, parentSvcDir)
	gotWorker := resolveSymlinks(t, cfg.Services["worker"].Dir)
	if gotWorker != wantWorker {
		t.Errorf("worker dir = %q, want %q (resolved from parent's dir)", gotWorker, wantWorker)
	}
	wantAPI := resolveSymlinks(t, tmp)
	gotAPI := resolveSymlinks(t, cfg.Services["api"].Dir)
	if gotAPI != wantAPI {
		t.Errorf("api dir = %q, want %q (resolved from child's dir)", gotAPI, wantAPI)
	}
}

func TestLoad_Extends_RelativeEnvFilesAndVolumesPerFile(t *testing.T) {
	tmp := t.TempDir()
	parentDir := filepath.Join(tmp, "parent")
	childDir := filepath.Join(tmp, "child")
	if err := os.Mkdir(parentDir, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(childDir, 0755); err != nil {
		t.Fatal(err)
	}

	writeYAML(t, parentDir, "core.yml", `
version: 1
global:
  env_file: parent.env
services:
  postgres:
    container:
      image: postgres:16
      volumes:
        - ./parent-data:/var/lib/postgresql/data
    env_file: parent.svc.env
`)
	child := writeYAML(t, childDir, "forge.yml", `
version: 1
extends: ../parent/core.yml
services:
  redis:
    container:
      image: redis:7
      volumes:
        - ./child-data:/data
    env_file: child.svc.env
`)

	cfg, err := Load(child)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}

	if want := filepath.Join(parentDir, "parent.env"); cfg.Global.EnvFile != want {
		t.Errorf("global env_file = %q, want %q", cfg.Global.EnvFile, want)
	}
	parentSvc := cfg.Services["postgres"]
	if want := filepath.Join(parentDir, "parent.svc.env"); parentSvc.EnvFile != want {
		t.Errorf("parent service env_file = %q, want %q", parentSvc.EnvFile, want)
	}
	if want := filepath.Join(parentDir, "parent-data") + ":/var/lib/postgresql/data"; parentSvc.Container.Volumes[0] != want {
		t.Errorf("parent volume = %q, want %q", parentSvc.Container.Volumes[0], want)
	}
	childSvc := cfg.Services["redis"]
	if want := filepath.Join(childDir, "child.svc.env"); childSvc.EnvFile != want {
		t.Errorf("child service env_file = %q, want %q", childSvc.EnvFile, want)
	}
	if want := filepath.Join(childDir, "child-data") + ":/data"; childSvc.Container.Volumes[0] != want {
		t.Errorf("child volume = %q, want %q", childSvc.Container.Volumes[0], want)
	}
}

func TestLoad_Extends_AbsolutePath(t *testing.T) {
	tmp := t.TempDir()
	parentDir := t.TempDir()
	parentPath := writeYAML(t, parentDir, "core.yml", `
version: 1
services:
  postgres:
    container:
      image: postgres:16
      ports: ["5432:5432"]
`)
	child := writeYAML(t, tmp, "forge.yml", fmt.Sprintf(`
version: 1
extends: %s
services:
  api:
    dir: /tmp
    command: "echo"
`, parentPath))
	cfg, err := Load(child)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if _, ok := cfg.Services["postgres"]; !ok {
		t.Error("expected service 'postgres' inherited via absolute extends path")
	}
}

func TestLoad_Extends_ChainedThreeLevels(t *testing.T) {
	tmp := t.TempDir()
	writeYAML(t, tmp, "a.yml", `
version: 1
services:
  a:
    dir: /tmp
    command: "echo a"
`)
	writeYAML(t, tmp, "b.yml", `
version: 1
extends: a.yml
services:
  b:
    dir: /tmp
    command: "echo b"
`)
	cPath := writeYAML(t, tmp, "c.yml", `
version: 1
extends: b.yml
services:
  c:
    dir: /tmp
    command: "echo c"
`)
	cfg, err := Load(cPath)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	for _, name := range []string{"a", "b", "c"} {
		if _, ok := cfg.Services[name]; !ok {
			t.Errorf("expected service %q in merged config", name)
		}
	}
}

func TestLoad_Extends_Cycle(t *testing.T) {
	tmp := t.TempDir()
	writeYAML(t, tmp, "a.yml", `
version: 1
extends: b.yml
services:
  a:
    dir: /tmp
    command: "echo"
`)
	writeYAML(t, tmp, "b.yml", `
version: 1
extends: a.yml
services:
  b:
    dir: /tmp
    command: "echo"
`)
	_, err := Load(filepath.Join(tmp, "a.yml"))
	if err == nil {
		t.Fatal("expected cycle error")
	}
	assertContains(t, err.Error(), "cycle")
}

func TestLoad_Extends_SelfCycle(t *testing.T) {
	tmp := t.TempDir()
	path := writeYAML(t, tmp, "self.yml", `
version: 1
extends: self.yml
services:
  s:
    dir: /tmp
    command: "echo"
`)
	_, err := Load(path)
	if err == nil {
		t.Fatal("expected cycle error")
	}
	assertContains(t, err.Error(), "cycle")
}

func TestLoad_Extends_MissingParent(t *testing.T) {
	tmp := t.TempDir()
	child := writeYAML(t, tmp, "forge.yml", `
version: 1
extends: nope.yml
services:
  api:
    dir: /tmp
    command: "echo"
`)
	_, err := Load(child)
	if err == nil {
		t.Fatal("expected error for missing parent")
	}
	assertContains(t, err.Error(), "nope.yml")
}

func TestLoad_Extends_ParseErrorIncludesFilePath(t *testing.T) {
	tmp := t.TempDir()
	parent := writeYAML(t, tmp, "bad-parent.yml", `
version: 1
global:
  shutdown_timeout: not-a-duration
services:
  parent:
    dir: /tmp
    command: "echo"
`)
	child := writeYAML(t, tmp, "forge.yml", `
version: 1
extends: bad-parent.yml
services:
  child:
    dir: /tmp
    command: "echo"
`)

	_, err := Load(child)
	if err == nil {
		t.Fatal("expected parse error from parent")
	}
	assertContains(t, err.Error(), "parsing config")
	assertContains(t, err.Error(), parent)
}

func TestLoad_Extends_ServiceConflict(t *testing.T) {
	tmp := t.TempDir()
	writeYAML(t, tmp, "core.yml", `
version: 1
services:
  postgres:
    container:
      image: postgres:16
      ports: ["5432:5432"]
`)
	child := writeYAML(t, tmp, "forge.yml", `
version: 1
extends: core.yml
services:
  postgres:
    container:
      image: postgres:17
      ports: ["5432:5432"]
`)
	_, err := Load(child)
	if err == nil {
		t.Fatal("expected service-conflict error")
	}
	assertContains(t, err.Error(), "postgres")
	assertContains(t, err.Error(), "conflict")
}

func TestLoad_Extends_GlobalEnvMerge(t *testing.T) {
	tmp := t.TempDir()
	writeYAML(t, tmp, "core.yml", `
version: 1
global:
  env:
    FOO: "1"
    BAR: "2"
services:
  a:
    dir: /tmp
    command: "echo"
`)
	child := writeYAML(t, tmp, "forge.yml", `
version: 1
extends: core.yml
global:
  env:
    BAR: "20"
    BAZ: "3"
services:
  b:
    dir: /tmp
    command: "echo"
`)
	cfg, err := Load(child)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	want := map[string]string{"FOO": "1", "BAR": "20", "BAZ": "3"}
	for k, v := range want {
		if cfg.Global.Env[k] != v {
			t.Errorf("global env[%q] = %q, want %q", k, cfg.Global.Env[k], v)
		}
	}
	if len(cfg.Global.Env) != len(want) {
		t.Errorf("global env size = %d, want %d (got %v)", len(cfg.Global.Env), len(want), cfg.Global.Env)
	}
}

func TestLoad_Extends_GlobalScalarOverride(t *testing.T) {
	tmp := t.TempDir()
	writeYAML(t, tmp, "core.yml", `
version: 1
global:
  log_buffer_lines: 1000
services:
  a:
    dir: /tmp
    command: "echo"
`)
	child := writeYAML(t, tmp, "forge.yml", `
version: 1
extends: core.yml
global:
  log_buffer_lines: 9999
services:
  b:
    dir: /tmp
    command: "echo"
`)
	cfg, err := Load(child)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.Global.LogBufferLines != 9999 {
		t.Errorf("log_buffer_lines = %d, want 9999 (child override)", cfg.Global.LogBufferLines)
	}
}

func TestLoad_Extends_GlobalScalarInherit(t *testing.T) {
	tmp := t.TempDir()
	writeYAML(t, tmp, "core.yml", `
version: 1
global:
  log_buffer_lines: 9999
services:
  a:
    dir: /tmp
    command: "echo"
`)
	child := writeYAML(t, tmp, "forge.yml", `
version: 1
extends: core.yml
services:
  b:
    dir: /tmp
    command: "echo"
`)
	cfg, err := Load(child)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.Global.LogBufferLines != 9999 {
		t.Errorf("log_buffer_lines = %d, want 9999 (inherited)", cfg.Global.LogBufferLines)
	}
}

func TestLoad_Extends_DefaultsAppliedOnce(t *testing.T) {
	tmp := t.TempDir()
	writeYAML(t, tmp, "core.yml", `
version: 1
services:
  a:
    dir: /tmp
    command: "echo"
`)
	child := writeYAML(t, tmp, "forge.yml", `
version: 1
extends: core.yml
services:
  b:
    dir: /tmp
    command: "echo"
`)
	cfg, err := Load(child)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.Global.LogBufferLines != 5000 {
		t.Errorf("log_buffer_lines = %d, want 5000 (default)", cfg.Global.LogBufferLines)
	}
	if cfg.Global.ShutdownTimeout.Duration != 10*time.Second {
		t.Errorf("shutdown_timeout = %v, want 10s (default)", cfg.Global.ShutdownTimeout.Duration)
	}
}

func TestLoad_Extends_ContainerPrefixFromEntrypoint(t *testing.T) {
	tmp := t.TempDir()
	parentDir := filepath.Join(tmp, "core_dir")
	childDir := filepath.Join(tmp, "forge_dir")
	if err := os.Mkdir(parentDir, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(childDir, 0755); err != nil {
		t.Fatal(err)
	}
	writeYAML(t, parentDir, "core.yml", `
version: 1
services:
  a:
    dir: /tmp
    command: "echo"
`)
	child := writeYAML(t, childDir, "forge.yml", `
version: 1
extends: ../core_dir/core.yml
services:
  b:
    dir: /tmp
    command: "echo"
`)
	cfg, err := Load(child)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.Global.ContainerPrefix != "forge_dir" {
		t.Errorf("container_prefix = %q, want %q (entry-point dir name)", cfg.Global.ContainerPrefix, "forge_dir")
	}
}

func TestParse_RejectsExtends(t *testing.T) {
	yaml := []byte(`
version: 1
extends: other.yml
services:
  a:
    dir: /tmp
    command: "echo"
`)
	_, err := Parse(yaml, "/tmp")
	if err == nil {
		t.Fatal("expected error from Parse when extends is set")
	}
	assertContains(t, err.Error(), "extends")
}

func TestExpandEnvInConfig(t *testing.T) {
	t.Setenv("BENCH_TEST_DB_PASS", "s3cret")
	t.Setenv("BENCH_TEST_TOKEN", "abc")
	t.Setenv("BENCH_TEST_HOME", t.TempDir())
	tmp := t.TempDir()

	dir := tmp + "/svc"
	if err := os.Mkdir(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	cfgPath := tmp + "/bench.yml"
	yaml := `
version: 1
global:
  env_file: "$BENCH_TEST_HOME/global.env"
  env:
    GLOBAL_VAR: "g-${BENCH_TEST_TOKEN}"
services:
  api:
    dir: svc
    command: "echo $LITERAL"
    env_file: "$BENCH_TEST_HOME/service.env"
    env:
      DB_PASS: "${BENCH_TEST_DB_PASS}"
      LITERAL: "no-expansion-here-$$"
`
	if err := os.WriteFile(cfgPath, []byte(yaml), 0o644); err != nil {
		t.Fatal(err)
	}

	cfg, err := Load(cfgPath)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}

	if got := cfg.Services["api"].Env["DB_PASS"]; got != "s3cret" {
		t.Errorf("DB_PASS = %q, want s3cret", got)
	}
	if got := cfg.Global.Env["GLOBAL_VAR"]; got != "g-abc" {
		t.Errorf("GLOBAL_VAR = %q, want g-abc", got)
	}
	if got, want := cfg.Global.EnvFile, filepath.Join(os.Getenv("BENCH_TEST_HOME"), "global.env"); got != want {
		t.Errorf("global env_file = %q, want %q", got, want)
	}
	if got, want := cfg.Services["api"].EnvFile, filepath.Join(os.Getenv("BENCH_TEST_HOME"), "service.env"); got != want {
		t.Errorf("service env_file = %q, want %q", got, want)
	}
	// Command was NOT expanded — the literal $LITERAL stays.
	if got := cfg.Services["api"].Command.Parts[2]; got != "echo $LITERAL" {
		t.Errorf("command should not be expanded; got %q", got)
	}
}

// TestExpandEnvInConfig_FromEnvFile covers the regression where inline
// `${VAR}` references resolved to empty whenever VAR was defined only in a
// global env_file (and not in the parent shell). At runtime
// supervisor.buildEnv layers inline env on top of env_file, so the empty
// interpolation would silently clobber the env_file value.
func TestExpandEnvInConfig_FromEnvFile(t *testing.T) {
	tmp := t.TempDir()
	envPath := filepath.Join(tmp, ".env")
	if err := os.WriteFile(envPath, []byte("MEGADB_USER=megadb_admin\nMEGADB_PASS=s3cret\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	cfgPath := filepath.Join(tmp, "bench.yml")
	yaml := `
version: 1
global:
  env_file: .env
services:
  api:
    command: "echo hi"
    env:
      DATABASE_URL: "postgres://${MEGADB_USER}:${MEGADB_PASS}@localhost/db"
`
	if err := os.WriteFile(cfgPath, []byte(yaml), 0o644); err != nil {
		t.Fatal(err)
	}
	// Explicitly clear the vars from the parent env to prove the env_file is
	// the only source.
	t.Setenv("MEGADB_USER", "")
	t.Setenv("MEGADB_PASS", "")
	if err := os.Unsetenv("MEGADB_USER"); err != nil {
		t.Fatal(err)
	}
	if err := os.Unsetenv("MEGADB_PASS"); err != nil {
		t.Fatal(err)
	}

	cfg, err := Load(cfgPath)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	got := cfg.Services["api"].Env["DATABASE_URL"]
	want := "postgres://megadb_admin:s3cret@localhost/db"
	if got != want {
		t.Errorf("DATABASE_URL = %q, want %q", got, want)
	}
}

// TestExpandEnvInConfig_ServiceEnvFile covers per-service env_file values
// feeding into the substitution source for that service's inline env.
func TestExpandEnvInConfig_ServiceEnvFile(t *testing.T) {
	tmp := t.TempDir()
	svcEnv := filepath.Join(tmp, "svc.env")
	if err := os.WriteFile(svcEnv, []byte("TOKEN=svc-token\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	cfgPath := filepath.Join(tmp, "bench.yml")
	yaml := `
version: 1
services:
  api:
    command: "echo hi"
    env_file: svc.env
    env:
      AUTHORIZATION: "Bearer ${TOKEN}"
`
	if err := os.WriteFile(cfgPath, []byte(yaml), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("TOKEN", "")
	if err := os.Unsetenv("TOKEN"); err != nil {
		t.Fatal(err)
	}

	cfg, err := Load(cfgPath)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if got := cfg.Services["api"].Env["AUTHORIZATION"]; got != "Bearer svc-token" {
		t.Errorf("AUTHORIZATION = %q, want %q", got, "Bearer svc-token")
	}
}

// TestExpandEnvInConfig_Precedence pins the interpolation lookup order:
// shell env -> service env -> service env_file -> global env -> global
// env_file. Runtime env loading has its own final-value precedence; this test
// covers what `${VAR}` references see while config is loaded.
func TestExpandEnvInConfig_Precedence(t *testing.T) {
	tmp := t.TempDir()
	globalEnv := filepath.Join(tmp, ".env")
	if err := os.WriteFile(globalEnv, []byte(strings.Join([]string{
		"BENCH_PRECEDENCE_SHELL=from-global-file",
		"BENCH_PRECEDENCE_SERVICE=from-global-file",
		"BENCH_PRECEDENCE_SERVICE_FILE=from-global-file",
		"BENCH_PRECEDENCE_GLOBAL=from-global-file",
		"BENCH_PRECEDENCE_GLOBAL_FILE=from-global-file",
	}, "\n")+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	serviceEnv := filepath.Join(tmp, "svc.env")
	if err := os.WriteFile(serviceEnv, []byte(strings.Join([]string{
		"BENCH_PRECEDENCE_SERVICE=from-service-file",
		"BENCH_PRECEDENCE_SERVICE_FILE=from-service-file",
	}, "\n")+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	cfgPath := filepath.Join(tmp, "bench.yml")
	yaml := `
version: 1
global:
  env_file: .env
  env:
    BENCH_PRECEDENCE_GLOBAL: from-global-env
services:
  api:
    command: "echo hi"
    env_file: svc.env
    env:
      BENCH_PRECEDENCE_SERVICE: from-service-env
      OUT_SHELL: "${BENCH_PRECEDENCE_SHELL}"
      OUT_SERVICE: "${BENCH_PRECEDENCE_SERVICE}"
      OUT_SERVICE_FILE: "${BENCH_PRECEDENCE_SERVICE_FILE}"
      OUT_GLOBAL: "${BENCH_PRECEDENCE_GLOBAL}"
      OUT_GLOBAL_FILE: "${BENCH_PRECEDENCE_GLOBAL_FILE}"
`
	if err := os.WriteFile(cfgPath, []byte(yaml), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("BENCH_PRECEDENCE_SHELL", "from-shell")
	for _, key := range []string{
		"BENCH_PRECEDENCE_SERVICE",
		"BENCH_PRECEDENCE_SERVICE_FILE",
		"BENCH_PRECEDENCE_GLOBAL",
		"BENCH_PRECEDENCE_GLOBAL_FILE",
	} {
		t.Setenv(key, "")
		if err := os.Unsetenv(key); err != nil {
			t.Fatal(err)
		}
	}

	cfg, err := Load(cfgPath)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	env := cfg.Services["api"].Env
	assertEnv := func(key, want string) {
		t.Helper()
		if got := env[key]; got != want {
			t.Errorf("%s = %q, want %q", key, got, want)
		}
	}
	assertEnv("OUT_SHELL", "from-shell")
	assertEnv("OUT_SERVICE", "from-service-env")
	assertEnv("OUT_SERVICE_FILE", "from-service-file")
	assertEnv("OUT_GLOBAL", "from-global-env")
	assertEnv("OUT_GLOBAL_FILE", "from-global-file")
}

// TestExpandEnvInConfig_ShellOverridesEnvFile pins the precedence order:
// a value present in the parent shell environment wins over the same key
// defined in an env_file. Without this, sourcing a fresh value into the
// shell wouldn't take effect until the env_file was edited.
func TestExpandEnvInConfig_ShellOverridesEnvFile(t *testing.T) {
	tmp := t.TempDir()
	envPath := filepath.Join(tmp, ".env")
	if err := os.WriteFile(envPath, []byte("PRIORITY=from-file\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	cfgPath := filepath.Join(tmp, "bench.yml")
	yaml := `
version: 1
global:
  env_file: .env
services:
  api:
    command: "echo hi"
    env:
      PRIORITY_OUT: "${PRIORITY}"
`
	if err := os.WriteFile(cfgPath, []byte(yaml), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PRIORITY", "from-shell")

	cfg, err := Load(cfgPath)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if got := cfg.Services["api"].Env["PRIORITY_OUT"]; got != "from-shell" {
		t.Errorf("PRIORITY_OUT = %q, want %q", got, "from-shell")
	}
}

func TestTransitiveDeps(t *testing.T) {
	cfg := &Config{
		Services: map[string]ServiceConfig{
			"a": {DependsOn: []string{"b", "c"}},
			"b": {DependsOn: []string{"d"}},
			"c": {},
			"d": {},
			"e": {}, // unrelated
		},
	}

	got, err := cfg.TransitiveDeps([]string{"a"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	want := map[string]bool{"a": true, "b": true, "c": true, "d": true}
	if len(got) != len(want) {
		t.Fatalf("expected %d keys, got %d (%v)", len(want), len(got), got)
	}
	for k := range want {
		if !got[k] {
			t.Errorf("expected %q in closure, missing", k)
		}
	}
	if got["e"] {
		t.Error("unrelated service e should not be in closure of {a}")
	}

	// Unknown root surfaces an error rather than silently being included.
	if _, err := cfg.TransitiveDeps([]string{"nope"}); err == nil {
		t.Error("expected error for unknown root")
	}
}

func TestValidate_ContainerExecReadiness(t *testing.T) {
	containerSvc := func(r ReadinessConfig) ServiceConfig {
		return ServiceConfig{
			Container: &ContainerConfig{Image: "postgres:16"},
			Restart:   RestartConfig{Policy: "never"},
			Readiness: r,
		}
	}

	t.Run("valid on a container service", func(t *testing.T) {
		cfg := &Config{Version: 1, Services: map[string]ServiceConfig{
			"db": containerSvc(ReadinessConfig{
				Kind:    "container_exec",
				Command: &Command{Parts: []string{"pg_isready", "-U", "bench"}},
			}),
		}}
		if err := cfg.Validate(); err != nil {
			t.Fatalf("expected container_exec to validate, got %v", err)
		}
	})

	t.Run("requires a command", func(t *testing.T) {
		cfg := &Config{Version: 1, Services: map[string]ServiceConfig{
			"db": containerSvc(ReadinessConfig{Kind: "container_exec"}),
		}}
		err := cfg.Validate()
		if err == nil {
			t.Fatal("expected validation error for missing command")
		}
		assertContains(t, err.Error(), "container_exec requires a command")
	})

	t.Run("rejects a process service", func(t *testing.T) {
		cfg := &Config{Version: 1, Services: map[string]ServiceConfig{
			"app": {
				Dir:     ".",
				Command: &Command{Parts: []string{"echo"}},
				Restart: RestartConfig{Policy: "never"},
				Readiness: ReadinessConfig{
					Kind:    "container_exec",
					Command: &Command{Parts: []string{"pg_isready"}},
				},
			},
		}}
		err := cfg.Validate()
		if err == nil {
			t.Fatal("expected validation error for a non-container service")
		}
		assertContains(t, err.Error(), "container_exec requires a container service")
	})
}
