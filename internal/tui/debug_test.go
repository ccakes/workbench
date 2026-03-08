package tui

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/lipgloss"

	"github.com/ccakes/bench/internal/config"
	"github.com/ccakes/bench/internal/events"
	"github.com/ccakes/bench/internal/supervisor"
)

func TestDebugRender(t *testing.T) {
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
				Command: &config.Command{Shell: true, Parts: []string{"sh", "-c", "echo hi"}},
				Restart: config.RestartConfig{Policy: "never"},
			},
			"api-gateway": {
				Dir:     dir,
				Command: &config.Command{Shell: true, Parts: []string{"sh", "-c", "echo hi"}},
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
				Command: &config.Command{Shell: true, Parts: []string{"sh", "-c", "echo hi"}},
				Restart: config.RestartConfig{Policy: "on-failure"},
			},
		},
	}

	bus := events.NewBus()
	sup := supervisor.New(cfg, bus)
	m := NewModel(sup, nil)
	m.width = 130
	m.height = 35

	// Test each selection
	for i, key := range m.services {
		m.selected = i
		output := m.View()
		lines := strings.Split(output, "\n")
		
		fmt.Printf("\n=== Selected: %s (index %d) ===\n", key, i)
		fmt.Printf("Total lines: %d\n", len(lines))
		
		maxW := 0
		for j, line := range lines {
			vw := lipgloss.Width(line)
			if vw > maxW {
				maxW = vw
			}
			if vw != m.width && j < len(lines)-1 { // skip status bar
				fmt.Printf("  LINE %2d: vw=%3d (expected %d) | %q\n", j+1, vw, m.width, stripAnsi(line))
			}
		}
		fmt.Printf("Max visual width: %d (terminal: %d)\n", maxW, m.width)
	}
}

func stripAnsi(s string) string {
	// Simple ANSI stripper for debug output
	var b strings.Builder
	i := 0
	for i < len(s) {
		if s[i] == '\x1b' && i+1 < len(s) && s[i+1] == '[' {
			// Skip to 'm'
			j := i + 2
			for j < len(s) && s[j] != 'm' {
				j++
			}
			i = j + 1
			continue
		}
		b.WriteByte(s[i])
		i++
	}
	return b.String()
}
