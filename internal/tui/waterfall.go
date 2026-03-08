package tui

import (
	"fmt"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"

	"github.com/ccakes/workbench/internal/spanbuf"
)

func (m Model) handleWaterfallKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "q", "ctrl+c":
		return m, tea.Quit
	case "esc":
		m.waterfallMode = false
	}
	return m, nil
}

func (m Model) viewWaterfall() string {
	if len(m.waterfallSpans) == 0 {
		return styleLabel.Render("no spans in trace")
	}

	spans := m.waterfallSpans

	// Find trace bounds
	traceStart := spans[0].StartTime
	traceEnd := spans[0].EndTime
	for _, s := range spans[1:] {
		if s.StartTime.Before(traceStart) {
			traceStart = s.StartTime
		}
		if s.EndTime.After(traceEnd) {
			traceEnd = s.EndTime
		}
	}
	traceDuration := traceEnd.Sub(traceStart)
	if traceDuration == 0 {
		traceDuration = time.Millisecond
	}

	statusBarHeight := 1
	mainHeight := m.height - statusBarHeight - 1

	var b strings.Builder

	// Header
	traceID := spanbuf.TraceIDHex(spans[0].TraceID)
	header := fmt.Sprintf("Trace %s  (%d spans, %s)",
		truncate(traceID, 16),
		len(spans),
		formatSpanDuration(traceDuration))
	b.WriteString(styleTitle.Render(header) + "\n\n")

	// Calculate column widths
	svcWidth := 10
	nameWidth := 20
	barWidth := max(10, m.width-svcWidth-nameWidth-12)

	usedLines := 2
	for i, s := range spans {
		if usedLines >= mainHeight-2 {
			remaining := len(spans) - i
			b.WriteString(styleLabel.Render(fmt.Sprintf("  ... %d more spans", remaining)))
			break
		}

		svc := truncate(s.ServiceName, svcWidth)
		svc = svc + strings.Repeat(" ", max(0, svcWidth-ansi.StringWidth(svc)))

		name := truncate(s.Name, nameWidth)
		name = name + strings.Repeat(" ", max(0, nameWidth-ansi.StringWidth(name)))

		// Calculate bar position
		relStart := s.StartTime.Sub(traceStart)
		relEnd := s.EndTime.Sub(traceStart)

		startPos := int(float64(relStart) / float64(traceDuration) * float64(barWidth))
		endPos := int(float64(relEnd) / float64(traceDuration) * float64(barWidth))
		if endPos <= startPos {
			endPos = startPos + 1
		}
		if endPos > barWidth {
			endPos = barWidth
		}

		// Build bar
		bar := strings.Repeat(" ", startPos)
		barLen := endPos - startPos

		var barStyle func(string) string
		switch s.Status {
		case spanbuf.StatusError:
			barStyle = func(s string) string { return styleSpanError.Render(s) }
		case spanbuf.StatusOK:
			barStyle = func(s string) string { return styleSpanOK.Render(s) }
		default:
			barStyle = func(s string) string { return styleWaterfallBar.Render(s) }
		}

		bar += barStyle(strings.Repeat("█", barLen))
		bar += strings.Repeat(" ", max(0, barWidth-endPos))

		dur := formatSpanDuration(s.Duration)

		line := " " + styleValue.Render(svc) + " " + name + " |" + bar + "| " + styleLabel.Render(dur)
		b.WriteString(line + "\n")
		usedLines++
	}

	// Status bar
	b.WriteString("\n")
	statusBar := styleHelpKey.Render("esc") + styleHelp.Render(":back") + "  " +
		styleHelpKey.Render("q") + styleHelp.Render(":quit")

	output := lipgloss.JoinVertical(lipgloss.Left, b.String(), truncate(statusBar, m.width))

	lines := strings.Split(output, "\n")
	if len(lines) > m.height-1 {
		lines = lines[:m.height-1]
	}
	return strings.Join(lines, "\n")
}

func (m Model) viewServiceMap() string {
	if m.store == nil {
		return styleLabel.Render("tracing not enabled")
	}

	snap := m.store.ServiceMap()

	statusBarHeight := 1
	_ = statusBarHeight

	var b strings.Builder
	b.WriteString(styleTitle.Render("Service Map") + "\n\n")

	if len(snap.Edges) == 0 {
		b.WriteString(styleLabel.Render("  (no service interactions recorded)"))
	} else {
		for _, edge := range snap.Edges {
			avgDur := formatSpanDuration(edge.AvgDuration)
			errStr := ""
			if edge.ErrorCount > 0 {
				errStr = styleSpanError.Render(fmt.Sprintf(" (%d errors)", edge.ErrorCount))
			}
			line := fmt.Sprintf("  %s ──%s──> %s  [%d calls]%s",
				styleServiceMapNode.Render(edge.From),
				styleLabel.Render(avgDur),
				styleServiceMapNode.Render(edge.To),
				edge.CallCount,
				errStr)
			b.WriteString(line + "\n")
		}
	}

	b.WriteString("\n")
	statusBar := " " + styleHelpKey.Render("m/esc") + styleHelp.Render(":back") + "  " +
		styleHelpKey.Render("q") + styleHelp.Render(":quit")

	output := lipgloss.JoinVertical(lipgloss.Left, b.String(), truncate(statusBar, m.width))

	lines := strings.Split(output, "\n")
	if len(lines) > m.height-1 {
		lines = lines[:m.height-1]
	}
	return strings.Join(lines, "\n")
}
