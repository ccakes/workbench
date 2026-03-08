package tui

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/lipgloss"

	"github.com/ccakes/workbench/internal/config"
	"github.com/ccakes/workbench/internal/events"
	"github.com/ccakes/workbench/internal/supervisor"
)

func TestDebugPaneHeights(t *testing.T) {
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
		},
	}

	bus := events.NewBus()
	sup := supervisor.New(cfg, bus)
	m := NewModel(sup, nil)
	m.width = 130
	m.height = 35

	leftWidth := m.width * 30 / 100
	if leftWidth < 20 {
		leftWidth = 20
	}
	if leftWidth > 40 {
		leftWidth = 40
	}
	rightWidth := m.width - leftWidth
	mainHeight := m.height - 1 - 1

	for i, key := range m.services {
		m.selected = i

		// Render left pane
		leftContent := m.viewServiceList(leftWidth-4, mainHeight-2)
		leftBorder := styleBorder
		leftPane := leftBorder.
			Width(leftWidth - 2).MaxWidth(leftWidth).
			Height(mainHeight - 2).MaxHeight(mainHeight).
			Render(leftContent)

		detailHeight := 10
		logHeight := mainHeight - detailHeight

		detailContent := m.viewDetail(rightWidth-4, detailHeight-2)
		detailPane := styleBorder.
			Width(rightWidth - 2).MaxWidth(rightWidth).
			Height(detailHeight - 2).MaxHeight(detailHeight).
			Render(detailContent)

		logContent := m.viewLogs(rightWidth-4, logHeight-2)
		logPane := styleBorder.
			Width(rightWidth - 2).MaxWidth(rightWidth).
			Height(logHeight - 2).MaxHeight(logHeight).
			Render(logContent)

		rightPane := lipgloss.JoinVertical(lipgloss.Left, detailPane, logPane)

		leftLines := strings.Count(leftPane, "\n") + 1
		rightLines := strings.Count(rightPane, "\n") + 1
		detailLines := strings.Count(detailPane, "\n") + 1
		logLines := strings.Count(logPane, "\n") + 1

		leftLineWidths := make(map[int]int)
		for j, line := range strings.Split(leftPane, "\n") {
			w := lipgloss.Width(line)
			leftLineWidths[w]++
			_ = j
		}
		rightLineWidths := make(map[int]int)
		for j, line := range strings.Split(rightPane, "\n") {
			w := lipgloss.Width(line)
			rightLineWidths[w]++
			_ = j
		}

		fmt.Printf("\n=== Selected: %s ===\n", key)
		fmt.Printf("  mainHeight=%d, leftWidth=%d, rightWidth=%d\n", mainHeight, leftWidth, rightWidth)
		fmt.Printf("  leftPane lines: %d, rightPane lines: %d\n", leftLines, rightLines)
		fmt.Printf("  detailPane lines: %d, logPane lines: %d\n", detailLines, logLines)
		fmt.Printf("  leftPane widths: %v\n", leftLineWidths)
		fmt.Printf("  rightPane widths: %v\n", rightLineWidths)

		if leftLines != rightLines {
			t.Errorf("  MISMATCH: left=%d right=%d", leftLines, rightLines)
		}
	}
}
