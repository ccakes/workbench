package tui

import (
	"fmt"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/ccakes/workbench/internal/config"
	"github.com/ccakes/workbench/internal/events"
	"github.com/ccakes/workbench/internal/supervisor"
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
	m := NewModel(sup, nil)

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

// pressKey feeds a single rune key through handleKey and returns the updated model.
func pressKey(t *testing.T, m Model, key string) Model {
	t.Helper()
	updated, _ := m.handleKey(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(key)})
	return updated.(Model)
}

// TestLogScroll exercises four-direction log-pane scrolling: j/k scroll
// down/up the vim way and clamp at both ends, h/l pan horizontally through
// wide lines and clamp, and rendered lines never exceed the pane width.
func TestLogScroll(t *testing.T) {
	dir := t.TempDir()
	cfg := &config.Config{
		Version: 1,
		Global: config.GlobalConfig{
			ShutdownTimeout: config.Duration{Duration: 1 * time.Second},
			LogBufferLines:  500,
		},
		Services: map[string]config.ServiceConfig{
			"svc": {
				Dir:     dir,
				Command: &config.Command{Shell: true, Parts: []string{"sh", "-c", "echo hi"}},
				Restart: config.RestartConfig{Policy: "never"},
			},
		},
	}

	bus := events.NewBus()
	sup := supervisor.New(cfg, bus)
	m := NewModel(sup, nil)

	// Seed many lines, each far wider than the pane.
	logs := sup.ServiceLogs("svc")
	for i := range 100 {
		logs.Add("stdout", fmt.Sprintf("line %03d: %s", i, strings.Repeat("x", 120)))
	}

	m.width = 80
	m.height = 24
	m.selected = 0
	m.activePane = paneLogs
	m.logOffset = 0
	m.logOffsetX = 0
	m.logFollow = true

	maxOffset, maxX := m.logScrollBounds()
	if maxOffset <= 0 {
		t.Fatalf("expected a positive maxOffset with 100 seeded lines, got %d", maxOffset)
	}
	if maxX <= 0 {
		t.Fatalf("expected a positive maxX with wide lines, got %d", maxX)
	}

	// k scrolls up (toward oldest) and disables follow.
	m = pressKey(t, m, "k")
	if m.logOffset != 1 {
		t.Errorf("after one k: logOffset = %d, want 1", m.logOffset)
	}
	if m.logFollow {
		t.Error("after k: logFollow should be false")
	}

	// k clamps at maxOffset no matter how far we push.
	for range 200 {
		m = pressKey(t, m, "k")
	}
	if m.logOffset != maxOffset {
		t.Errorf("k clamp: logOffset = %d, want maxOffset %d", m.logOffset, maxOffset)
	}

	// j scrolls back down to the bottom and re-enables follow.
	for range 200 {
		m = pressKey(t, m, "j")
	}
	if m.logOffset != 0 {
		t.Errorf("j clamp: logOffset = %d, want 0", m.logOffset)
	}
	if !m.logFollow {
		t.Error("after scrolling to bottom: logFollow should be true")
	}

	// l pans right and clamps at maxX.
	m = pressKey(t, m, "l")
	if m.logOffsetX != logScrollStepX {
		t.Errorf("after one l: logOffsetX = %d, want %d", m.logOffsetX, logScrollStepX)
	}
	for range 200 {
		m = pressKey(t, m, "l")
	}
	if m.logOffsetX != maxX {
		t.Errorf("l clamp: logOffsetX = %d, want maxX %d", m.logOffsetX, maxX)
	}

	// Rendered lines must never exceed the terminal width, even fully panned.
	for lineNum, line := range strings.Split(m.View(), "\n") {
		if vw := lipgloss.Width(line); vw > m.width {
			t.Errorf("panned right, line %d: visual width %d > terminal width %d\n  line: %q",
				lineNum+1, vw, m.width, line)
		}
	}

	// h pans back to the left edge.
	for range 200 {
		m = pressKey(t, m, "h")
	}
	if m.logOffsetX != 0 {
		t.Errorf("h clamp: logOffsetX = %d, want 0", m.logOffsetX)
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
	m := NewModel(sup, nil)

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
	m := NewModel(sup, nil)

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
