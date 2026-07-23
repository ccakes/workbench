//go:build !windows

package supervisor

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ccakes/workbench/internal/config"
	"github.com/ccakes/workbench/internal/events"
)

// effectiveEnv resolves a KEY=VALUE slice to a map using last-write-wins, which
// mirrors how os/exec and docker dedupe duplicate keys in an env list.
func effectiveEnv(env []string) map[string]string {
	out := make(map[string]string, len(env))
	for _, e := range env {
		if k, v, ok := strings.Cut(e, "="); ok {
			out[k] = v
		}
	}
	return out
}

// buildEnvForService constructs a Supervisor from cfg and runs buildEnv for the
// named service.
func buildEnvForService(t *testing.T, cfg *config.Config, key string) map[string]string {
	t.Helper()
	sup := New(cfg, events.NewBus())
	ms, ok := sup.services[key]
	if !ok {
		t.Fatalf("service %q not found", key)
	}
	env, err := sup.buildEnv(ms)
	if err != nil {
		t.Fatalf("buildEnv: %v", err)
	}
	return effectiveEnv(env)
}

func writeEnvFile(t *testing.T, contents string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), ".env")
	if err := os.WriteFile(p, []byte(contents), 0o600); err != nil {
		t.Fatalf("write env file: %v", err)
	}
	return p
}

// containerService returns a minimal container ServiceConfig. Using a container
// keeps the OS environment out of buildEnv so env layering is deterministic.
func containerService() config.ServiceConfig {
	return config.ServiceConfig{
		Container: &config.ContainerConfig{Image: "scratch"},
	}
}

func tracingCfg(svc config.ServiceConfig) *config.Config {
	return &config.Config{
		Version: 1,
		Global: config.GlobalConfig{
			// Pin the Docker backend so the injected OTEL host is deterministic
			// regardless of the test machine (auto-detect could otherwise pick
			// the Apple backend on Apple silicon).
			ContainerBackend: config.BackendDocker,
			Tracing:          config.TracingConfig{Enabled: true, Port: 4318},
		},
		Services: map[string]config.ServiceConfig{"svc": svc},
	}
}

// TestOTEL_InjectsDefaultsWhenUnset verifies the collector endpoint/protocol
// defaults are injected when tracing is on and nothing else sets them.
func TestOTEL_InjectsDefaultsWhenUnset(t *testing.T) {
	// A container service reaches the host collector via host.docker.internal,
	// not its own loopback.
	env := buildEnvForService(t, tracingCfg(containerService()), "svc")
	if got, want := env["OTEL_EXPORTER_OTLP_ENDPOINT"], "http://host.docker.internal:4318"; got != want {
		t.Errorf("endpoint = %q, want %q", got, want)
	}
	if got, want := env["OTEL_EXPORTER_OTLP_PROTOCOL"], "http/protobuf"; got != want {
		t.Errorf("protocol = %q, want %q", got, want)
	}
}

// TestOTEL_InjectsAppleGatewayIP verifies that on the Apple backend the
// collector endpoint uses the configured vmnet gateway IP rather than
// host.docker.internal (which Apple containers can't resolve).
func TestOTEL_InjectsAppleGatewayIP(t *testing.T) {
	cfg := tracingCfg(containerService())
	cfg.Global.ContainerBackend = config.BackendApple
	cfg.Global.Apple.GatewayIP = "10.1.2.3"
	env := buildEnvForService(t, cfg, "svc")
	if got, want := env["OTEL_EXPORTER_OTLP_ENDPOINT"], "http://10.1.2.3:4318"; got != want {
		t.Errorf("endpoint = %q, want %q", got, want)
	}
}

// TestOTEL_NotInjectedWhenTracingDisabled verifies no OTEL vars appear when
// tracing is off.
func TestOTEL_NotInjectedWhenTracingDisabled(t *testing.T) {
	cfg := tracingCfg(containerService())
	cfg.Global.Tracing.Enabled = false
	env := buildEnvForService(t, cfg, "svc")
	if v, ok := env["OTEL_EXPORTER_OTLP_ENDPOINT"]; ok {
		t.Errorf("endpoint unexpectedly set to %q with tracing disabled", v)
	}
}

// TestOTEL_GlobalEnvFileWins is the core regression: a value defined in
// global.env_file must take precedence over the injected default. The injection
// is the lowest-precedence source, "before even global.env_file".
func TestOTEL_GlobalEnvFileWins(t *testing.T) {
	cfg := tracingCfg(containerService())
	cfg.Global.EnvFile = writeEnvFile(t, "OTEL_EXPORTER_OTLP_ENDPOINT=http://collector:9999\n")

	env := buildEnvForService(t, cfg, "svc")
	if got, want := env["OTEL_EXPORTER_OTLP_ENDPOINT"], "http://collector:9999"; got != want {
		t.Errorf("endpoint = %q, want %q (global env_file must win over injected default)", got, want)
	}
	// Protocol was not set anywhere, so the default still applies.
	if got, want := env["OTEL_EXPORTER_OTLP_PROTOCOL"], "http/protobuf"; got != want {
		t.Errorf("protocol = %q, want %q", got, want)
	}
}

// TestOTEL_ServiceEnvFileWins verifies a service env_file value wins over the
// injected default.
func TestOTEL_ServiceEnvFileWins(t *testing.T) {
	svc := containerService()
	svc.EnvFile = writeEnvFile(t, "OTEL_EXPORTER_OTLP_PROTOCOL=grpc\n")
	env := buildEnvForService(t, tracingCfg(svc), "svc")
	if got, want := env["OTEL_EXPORTER_OTLP_PROTOCOL"], "grpc"; got != want {
		t.Errorf("protocol = %q, want %q (service env_file must win)", got, want)
	}
}

// TestOTEL_InlineEnvWins verifies inline env (global and service) wins over the
// injected default.
func TestOTEL_InlineEnvWins(t *testing.T) {
	t.Run("global inline", func(t *testing.T) {
		svc := containerService()
		cfg := tracingCfg(svc)
		cfg.Global.Env = map[string]string{"OTEL_EXPORTER_OTLP_ENDPOINT": "http://global:1111"}
		env := buildEnvForService(t, cfg, "svc")
		if got, want := env["OTEL_EXPORTER_OTLP_ENDPOINT"], "http://global:1111"; got != want {
			t.Errorf("endpoint = %q, want %q", got, want)
		}
	})
	t.Run("service inline", func(t *testing.T) {
		svc := containerService()
		svc.Env = map[string]string{"OTEL_EXPORTER_OTLP_ENDPOINT": "http://svc:2222"}
		env := buildEnvForService(t, tracingCfg(svc), "svc")
		if got, want := env["OTEL_EXPORTER_OTLP_ENDPOINT"], "http://svc:2222"; got != want {
			t.Errorf("endpoint = %q, want %q", got, want)
		}
	})
}

// TestOTEL_OSEnvWins verifies an OTEL var inherited from the OS environment
// (process services only) wins over the injected default.
func TestOTEL_OSEnvWins(t *testing.T) {
	t.Setenv("OTEL_EXPORTER_OTLP_ENDPOINT", "http://os-env:8888")
	svc := config.ServiceConfig{
		Dir:     t.TempDir(),
		Command: &config.Command{Shell: true, Parts: []string{"sh", "-c", "true"}},
	}
	env := buildEnvForService(t, tracingCfg(svc), "svc")
	if got, want := env["OTEL_EXPORTER_OTLP_ENDPOINT"], "http://os-env:8888"; got != want {
		t.Errorf("endpoint = %q, want %q (OS env must win over injected default)", got, want)
	}
}
