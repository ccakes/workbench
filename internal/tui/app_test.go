package tui

import (
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/lipgloss"

	"github.com/ccakes/bench/internal/config"
	"github.com/ccakes/bench/internal/events"
	"github.com/ccakes/bench/internal/supervisor"
)

// TestViewLineWidths verifies that no rendered line exceeds the terminal width.
// This catches overflow bugs that corrupt the TUI layout.
func TestViewLineWidths(t *testing.T) {
	dir := t.TempDir()
	cfg := &config.Config{
		Version: 1,
		Global: config.GlobalConfig{
			ShutdownTimeout: config.Duration{Duration: 1 * time.Second},
			LogBufferLines:  100,
		},
		Services: map[string]config.ServiceConfig{
			"megalith": {
				Dir:     dir,
				Command: &config.Command{Shell: true, Parts: []string{"sh", "-c", "echo hello && sleep 999"}},
				Restart: config.RestartConfig{Policy: "never"},
			},
			"api-gateway": {
				Dir:     dir,
				Command: &config.Command{Shell: true, Parts: []string{"sh", "-c", "echo hello && sleep 999"}},
				Restart: config.RestartConfig{Policy: "never"},
			},
			"megadb": {
				Container: &config.ContainerConfig{
					Image: "073096867023.dkr.ecr.ap-southeast-2.amazonaws.com/containers/postgres:latest",
					Ports: []string{"127.0.0.1:5432:5432"},
				},
				Restart: config.RestartConfig{Policy: "never"},
			},
			"redis": {
				Container: &config.ContainerConfig{
					Image: "redis:7-alpine",
					Ports: []string{"6379:6379"},
				},
				Restart: config.RestartConfig{Policy: "always"},
			},
			"portal": {
				Dir:     dir,
				Command: &config.Command{Shell: true, Parts: []string{"sh", "-c", "echo hello && sleep 999"}},
				Restart: config.RestartConfig{Policy: "on-failure"},
			},
		},
	}

	bus := events.NewBus()
	sup := supervisor.New(cfg, bus)
	m := NewModel(sup)

	// Test at various terminal sizes
	sizes := []struct{ w, h int }{
		{80, 24},
		{120, 40},
		{160, 50},
		{200, 60},
	}

	for _, sz := range sizes {
		m.width = sz.w
		m.height = sz.h

		// Test with each service selected
		for i := range m.services {
			m.selected = i
			output := m.View()
			lines := strings.Split(output, "\n")

			for lineNum, line := range lines {
				vw := lipgloss.Width(line)
				if vw > sz.w {
					t.Errorf("size %dx%d, selected=%d (%s), line %d: visual width %d > terminal width %d\n  line: %q",
						sz.w, sz.h, i, m.services[i], lineNum+1, vw, sz.w, line)
				}
			}
		}
	}
}

// TestServiceListLineWidths checks each line from viewServiceList stays within bounds.
func TestServiceListLineWidths(t *testing.T) {
	dir := t.TempDir()
	cfg := &config.Config{
		Version: 1,
		Global: config.GlobalConfig{
			ShutdownTimeout: config.Duration{Duration: 1 * time.Second},
			LogBufferLines:  100,
		},
		Services: map[string]config.ServiceConfig{
			"short": {
				Dir:     dir,
				Command: &config.Command{Parts: []string{"echo"}},
				Restart: config.RestartConfig{Policy: "never"},
			},
			"container-svc": {
				Container: &config.ContainerConfig{
					Image: "very-long-registry.example.com/org/image:latest",
					Ports: []string{"8080:8080"},
				},
				Restart: config.RestartConfig{Policy: "always"},
			},
			"a-really-long-service-name-here": {
				Dir:     dir,
				Command: &config.Command{Parts: []string{"echo"}},
				Restart: config.RestartConfig{Policy: "never"},
			},
		},
	}

	bus := events.NewBus()
	sup := supervisor.New(cfg, bus)
	m := NewModel(sup)

	for _, contentWidth := range []int{20, 30, 36, 50} {
		content := m.viewServiceList(contentWidth, 20)
		for lineNum, line := range strings.Split(content, "\n") {
			vw := lipgloss.Width(line)
			if vw > contentWidth {
				t.Errorf("contentWidth=%d, line %d: visual width %d > %d\n  line: %q",
					contentWidth, lineNum+1, vw, contentWidth, line)
			}
		}
	}
}

// TestDetailPaneLineWidths checks each line from viewDetail stays within bounds.
func TestDetailPaneLineWidths(t *testing.T) {
	dir := t.TempDir()
	cfg := &config.Config{
		Version: 1,
		Global: config.GlobalConfig{
			ShutdownTimeout: config.Duration{Duration: 1 * time.Second},
			LogBufferLines:  100,
		},
		Services: map[string]config.ServiceConfig{
			"process-svc": {
				Dir:     dir,
				Command: &config.Command{Shell: true, Parts: []string{"sh", "-c", "a very long command string that might overflow the detail pane width easily"}},
				Restart: config.RestartConfig{Policy: "on-failure"},
			},
			"container-svc": {
				Container: &config.ContainerConfig{
					Image:   "073096867023.dkr.ecr.ap-southeast-2.amazonaws.com/containers/postgres:latest",
					Ports:   []string{"127.0.0.1:5432:5432", "127.0.0.1:5433:5433"},
					Volumes: []string{"/very/long/host/path/to/data:/var/lib/postgresql/data"},
					Network: "bench-net",
				},
				Restart: config.RestartConfig{Policy: "always"},
			},
		},
	}

	bus := events.NewBus()
	sup := supervisor.New(cfg, bus)
	m := NewModel(sup)

	for _, contentWidth := range []int{40, 60, 80, 100} {
		for i, key := range m.services {
			m.selected = i
			content := m.viewDetail(contentWidth, 10)
			for lineNum, line := range strings.Split(content, "\n") {
				vw := lipgloss.Width(line)
				if vw > contentWidth {
					t.Errorf("svc=%s, contentWidth=%d, line %d: visual width %d > %d\n  line: %q",
						key, contentWidth, lineNum+1, vw, contentWidth, line)
				}
			}
		}
	}
}
