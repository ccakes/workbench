package cli

import (
	"bytes"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/ccakes/workbench/internal/config"
)

func TestReorderFlags(t *testing.T) {
	tests := []struct {
		name string
		args []string
		want []string
	}{
		{
			name: "flags already first",
			args: []string{"-last", "200", "megalith"},
			want: []string{"-last", "200", "megalith"},
		},
		{
			name: "positional before flag",
			args: []string{"megalith", "-last", "200"},
			want: []string{"-last", "200", "megalith"},
		},
		{
			name: "positional between flags",
			args: []string{"-f", "megalith", "-last", "200"},
			want: []string{"-f", "megalith", "-last", "200"},
		},
		{
			name: "equals form",
			args: []string{"megalith", "-last=200"},
			want: []string{"-last=200", "megalith"},
		},
		{
			name: "no flags",
			args: []string{"megalith"},
			want: []string{"megalith"},
		},
		{
			name: "multiple positional after flags",
			args: []string{"-config", "bench.yaml", "svc1", "svc2"},
			want: []string{"-config", "bench.yaml", "svc1", "svc2"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := reorderFlags(tt.args)
			if !reflect.DeepEqual(got, tt.want) {
				t.Errorf("reorderFlags(%v) = %v, want %v", tt.args, got, tt.want)
			}
		})
	}
}

func TestRunValidateLoadsExtendedConfig(t *testing.T) {
	tmp := t.TempDir()
	if err := os.Mkdir(filepath.Join(tmp, "core"), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(tmp, "app"), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(tmp, "core.yml"), []byte(`
version: 1
services:
  base:
    dir: core
    command: "echo base"
`), 0644); err != nil {
		t.Fatal(err)
	}
	child := filepath.Join(tmp, "bench.yml")
	if err := os.WriteFile(child, []byte(`
version: 1
extends: core.yml
services:
  app:
    dir: app
    command: "echo app"
`), 0644); err != nil {
		t.Fatal(err)
	}

	var code int
	stdout, stderr := captureStdoutStderr(t, func() {
		code = runValidate([]string{"-config", child})
	})
	if code != 0 {
		t.Fatalf("runValidate returned %d, stderr: %s", code, stderr)
	}
	if !strings.Contains(stdout, "2 services defined") {
		t.Fatalf("validate output did not include merged service count; stdout: %s", stdout)
	}
	if !strings.Contains(stdout, "base") || !strings.Contains(stdout, "app") {
		t.Fatalf("validate output did not include inherited and child services; stdout: %s", stdout)
	}
}

func TestApplyServiceSubset(t *testing.T) {
	cfg := &config.Config{
		Services: map[string]config.ServiceConfig{
			"web":    {DependsOn: []string{"db"}},
			"db":     {},
			"worker": {},
		},
	}
	if err := applyServiceSubset(cfg, []string{"web"}); err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	web := cfg.Services["web"]
	db := cfg.Services["db"]
	worker := cfg.Services["worker"]
	if !web.GetAutoStart() {
		t.Error("web should be auto_start after subset")
	}
	if !db.GetAutoStart() {
		t.Error("db should be auto_start (transitive dep of web)")
	}
	if worker.GetAutoStart() {
		t.Error("worker should be auto_start=false (outside subset)")
	}
}

func TestApplyProfileFilter(t *testing.T) {
	cfg := &config.Config{
		Services: map[string]config.ServiceConfig{
			"always":   {}, // no profiles -> always on
			"core-a":   {Profiles: []string{"core"}, DependsOn: []string{"db"}},
			"core-b":   {Profiles: []string{"core"}},
			"frontend": {Profiles: []string{"frontend"}},
			"db":       {}, // pulled in transitively by core-a
		},
	}
	applyProfileFilter(cfg, []string{"core"})
	for _, key := range []string{"always", "core-a", "core-b", "db"} {
		svc := cfg.Services[key]
		if !svc.GetAutoStart() {
			t.Errorf("expected %s to be auto_start under profile=core", key)
		}
	}
	frontend := cfg.Services["frontend"]
	if frontend.GetAutoStart() {
		t.Error("frontend should be disabled when profile=core is active")
	}
}

func captureStdoutStderr(t *testing.T, fn func()) (string, string) {
	t.Helper()

	origStdout := os.Stdout
	origStderr := os.Stderr
	outR, outW, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	errR, errW, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}

	os.Stdout = outW
	os.Stderr = errW
	defer func() {
		os.Stdout = origStdout
		os.Stderr = origStderr
	}()

	outCh := make(chan string, 1)
	errCh := make(chan string, 1)
	go func() {
		var buf bytes.Buffer
		_, _ = io.Copy(&buf, outR)
		outCh <- buf.String()
	}()
	go func() {
		var buf bytes.Buffer
		_, _ = io.Copy(&buf, errR)
		errCh <- buf.String()
	}()

	fn()

	_ = outW.Close()
	_ = errW.Close()
	stdout := <-outCh
	stderr := <-errCh
	_ = outR.Close()
	_ = errR.Close()
	return stdout, stderr
}
