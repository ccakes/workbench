package config

import (
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

	// Service defaults
	svc := cfg.Services["web"]
	if svc.Restart.Policy != "never" {
		t.Errorf("restart.policy = %q, want %q", svc.Restart.Policy, "never")
	}
	if svc.Restart.Backoff.Duration != 1*time.Second {
		t.Errorf("restart.backoff = %v, want 1s", svc.Restart.Backoff.Duration)
	}
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
	defer os.Chdir(orig)

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
	defer os.Chdir(orig)

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
	defer os.Chdir(orig)

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
	defer os.Chdir(orig)

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
	defer os.Chdir(orig)

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
