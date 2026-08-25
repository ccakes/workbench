package supervisor

import (
	"context"
	"reflect"
	"testing"
	"time"

	"github.com/ccakes/workbench/internal/config"
	"github.com/ccakes/workbench/internal/logbuf"
)

func TestSetupContainerExec(t *testing.T) {
	want := []string{"psql", "-c", "select 1"}
	setup := &config.ServiceHookConfig{
		Kind:    config.ContainerExecKind,
		Command: &config.Command{Parts: want},
		Timeout: config.Duration{Duration: time.Second},
	}
	svc := config.ServiceConfig{
		Container: &config.ContainerConfig{Image: "postgres:16"},
		Setup:     setup,
	}
	ms := &managedService{cfg: svc, logs: logbuf.New(10)}
	sup := &Supervisor{cfg: &config.Config{}}
	execer := &stubExecer{bin: "sh", args: []string{"-c", "echo setup-complete"}}

	if err := sup.runSetupHook(context.Background(), ms, execer); err != nil {
		t.Fatalf("runSetupHook: %v", err)
	}
	if !reflect.DeepEqual(execer.got, want) {
		t.Fatalf("ExecCommand got %v, want %v", execer.got, want)
	}
	if !logContains(ms.logs, "setup-complete") {
		t.Fatal("missing setup output")
	}
}
