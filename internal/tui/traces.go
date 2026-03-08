package tui

import (
	"fmt"
	"sort"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"

	"github.com/ccakes/bench/internal/spanbuf"
)

func (m Model) handleTraceKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	if m.waterfallMode {
		return m.handleWaterfallKey(msg)
	}
	if m.serviceMapMode {
		switch msg.String() {
		case "m", "esc":
			m.serviceMapMode = false
		case "q", "ctrl+c":
			return m, tea.Quit
		}
		return m, nil
	}

	switch msg.String() {
	case "q", "ctrl+c":
		return m, tea.Quit

	case "t", "esc":
		m.viewMode = viewModeServices
		m.waterfallMode = false
		m.serviceMapMode = false

	case "j", "down":
		if m.tracePane == 0 && m.traceSelected < len(m.filteredSpans())-1 {
			m.traceSelected++
		}

	case "k", "up":
		if m.tracePane == 0 && m.traceSelected > 0 {
			m.traceSelected--
		}

	case "tab":
		m.tracePane = (m.tracePane + 1) % 2

	case "enter":
		spans := m.filteredSpans()
		if m.traceSelected >= 0 && m.traceSelected < len(spans) {
			s := spans[m.traceSelected]
			m.waterfallSpans = m.store.SpansByTrace(s.TraceID)
			m.waterfallMode = true
		}

	case "1":
		m.traceSortMode = 0
		m.sortTraceSpans()
	case "2":
		m.traceSortMode = 1
		m.sortTraceSpans()
	case "3":
		m.traceSortMode = 2
		m.sortTraceSpans()

	case "/":
		m.traceFilterMode = true
		m.traceFilter = ""

	case "m":
		m.serviceMapMode = true

	case "?":
		m.showHelp = true
	}

	return m, nil
}

func (m *Model) sortTraceSpans() {
	spans := m.traceSpans
	switch m.traceSortMode {
	case 0: // time
		sort.Slice(spans, func(i, j int) bool {
			return spans[i].StartTime.After(spans[j].StartTime)
		})
	case 1: // duration
		sort.Slice(spans, func(i, j int) bool {
			return spans[i].Duration > spans[j].Duration
		})
	case 2: // service
		sort.Slice(spans, func(i, j int) bool {
			if spans[i].ServiceName == spans[j].ServiceName {
				return spans[i].StartTime.After(spans[j].StartTime)
			}
			return spans[i].ServiceName < spans[j].ServiceName
		})
	}
}

func (m Model) filteredSpans() []spanbuf.Span {
	if m.traceFilter == "" {
		return m.traceSpans
	}
	var result []spanbuf.Span
	for _, s := range m.traceSpans {
		if strings.Contains(s.Name, m.traceFilter) ||
			strings.Contains(s.ServiceName, m.traceFilter) {
			result = append(result, s)
		}
	}
	return result
}

func (m Model) viewTraces() string {
	if m.width == 0 || m.height == 0 {
		return "initializing..."
	}

	if m.serviceMapMode {
		return m.viewServiceMap()
	}

	if m.waterfallMode {
		return m.viewWaterfall()
	}

	// Split: left 40% span list, right 60% span detail
	leftWidth := m.width * 40 / 100
	if leftWidth < 30 {
		leftWidth = 30
	}
	rightWidth := m.width - leftWidth

	statusBarHeight := 1
	mainHeight := m.height - statusBarHeight - 1

	// Left pane: span list
	leftContent := m.viewSpanList(leftWidth-4, mainHeight-2)
	leftBorder := styleBorder
	if m.tracePane == 0 {
		leftBorder = styleBorderActive
	}
	leftPane := leftBorder.
		Width(leftWidth - 2).MaxWidth(leftWidth).
		Height(mainHeight - 2).MaxHeight(mainHeight).
		Render(leftContent)

	// Right pane: span detail
	rightContent := m.viewSpanDetail(rightWidth-4, mainHeight-2)
	rightBorder := styleBorder
	if m.tracePane == 1 {
		rightBorder = styleBorderActive
	}
	rightPane := rightBorder.
		Width(rightWidth - 2).MaxWidth(rightWidth).
		Height(mainHeight - 2).MaxHeight(mainHeight).
		Render(rightContent)

	main := lipgloss.JoinHorizontal(lipgloss.Top, leftPane, rightPane)

	statusBar := m.viewTraceStatusBar()

	output := lipgloss.JoinVertical(lipgloss.Left, main, statusBar)

	lines := strings.Split(output, "\n")
	if len(lines) > m.height-1 {
		lines = lines[:m.height-1]
	}
	return strings.Join(lines, "\n")
}

func (m Model) viewSpanList(width, height int) string {
	var b strings.Builder

	spans := m.filteredSpans()

	// Header
	title := styleTitle.Render("Spans")
	count := styleLabel.Render(fmt.Sprintf(" (%d)", len(spans)))
	sortLabel := ""
	switch m.traceSortMode {
	case 0:
		sortLabel = " [time]"
	case 1:
		sortLabel = " [duration]"
	case 2:
		sortLabel = " [service]"
	}
	filter := ""
	if m.traceFilterMode {
		filter = styleStatusPending.Render(fmt.Sprintf(" /%s", m.traceFilter))
	} else if m.traceFilter != "" {
		filter = styleLabel.Render(fmt.Sprintf(" [filter: %s]", m.traceFilter))
	}
	b.WriteString(title + count + styleLabel.Render(sortLabel) + filter + "\n")

	if len(spans) == 0 {
		b.WriteString(styleLabel.Render("  (no spans)"))
		return b.String()
	}

	// Calculate column widths
	svcWidth := 12
	durWidth := 8
	statusWidth := 3
	nameWidth := max(1, width-svcWidth-durWidth-statusWidth-6)

	visibleLines := height - 1
	startIdx := 0
	if m.traceSelected >= visibleLines {
		startIdx = m.traceSelected - visibleLines + 1
	}

	for i := startIdx; i < len(spans) && i < startIdx+visibleLines; i++ {
		s := spans[i]

		svc := truncate(s.ServiceName, svcWidth)
		svc = svc + strings.Repeat(" ", max(0, svcWidth-ansi.StringWidth(svc)))

		name := truncate(s.Name, nameWidth)
		name = name + strings.Repeat(" ", max(0, nameWidth-ansi.StringWidth(name)))

		dur := formatSpanDuration(s.Duration)
		dur = strings.Repeat(" ", max(0, durWidth-len(dur))) + dur

		var statusIcon string
		switch s.Status {
		case spanbuf.StatusOK:
			statusIcon = styleSpanOK.Render("✓")
		case spanbuf.StatusError:
			statusIcon = styleSpanError.Render("✗")
		default:
			statusIcon = styleLabel.Render("·")
		}

		line := " " + styleValue.Render(svc) + " " + name + " " + dur + " " + statusIcon

		if i == m.traceSelected {
			line = styleSelected.Render(line)
		}

		b.WriteString(line)
		if i < startIdx+visibleLines-1 && i < len(spans)-1 {
			b.WriteString("\n")
		}
	}

	return b.String()
}

func (m Model) viewSpanDetail(width, height int) string {
	spans := m.filteredSpans()
	if m.traceSelected < 0 || m.traceSelected >= len(spans) {
		return styleLabel.Render("no span selected")
	}

	s := spans[m.traceSelected]

	var rows []string
	title := styleTitle.Render(s.Name)
	rows = append(rows, title)

	maxVal := max(1, width-14)
	row := func(label, value string) {
		rows = append(rows, styleLabel.Render(fmt.Sprintf("  %-12s", label))+styleValue.Render(truncate(value, maxVal)))
	}

	row("Trace ID", spanbuf.TraceIDHex(s.TraceID))
	row("Span ID", spanbuf.SpanIDHex(s.SpanID))
	if s.ParentSpanID != [8]byte{} {
		row("Parent ID", spanbuf.SpanIDHex(s.ParentSpanID))
	}
	row("Service", s.ServiceName)
	row("Kind", s.Kind.String())
	row("Duration", formatSpanDuration(s.Duration))

	var statusStr string
	switch s.Status {
	case spanbuf.StatusOK:
		statusStr = styleSpanOK.Render("ok")
	case spanbuf.StatusError:
		statusStr = styleSpanError.Render("error")
		if s.StatusMsg != "" {
			statusStr += " " + styleSpanError.Render(truncate(s.StatusMsg, maxVal-8))
		}
	default:
		statusStr = styleLabel.Render("unset")
	}
	rows = append(rows, styleLabel.Render(fmt.Sprintf("  %-12s", "Status"))+statusStr)

	row("Start", s.StartTime.Format("15:04:05.000"))
	row("End", s.EndTime.Format("15:04:05.000"))

	// Attributes
	if len(s.Attributes) > 0 {
		rows = append(rows, "")
		rows = append(rows, styleTitle.Render("  Attributes"))
		for _, attr := range s.Attributes {
			val := truncate(attr.Value, maxVal-len(attr.Key)-4)
			rows = append(rows, styleLabel.Render("    "+attr.Key+"=")+styleValue.Render(val))
		}
	}

	// Events
	if len(s.Events) > 0 {
		rows = append(rows, "")
		rows = append(rows, styleTitle.Render("  Events"))
		for _, evt := range s.Events {
			ts := evt.Timestamp.Format("15:04:05.000")
			rows = append(rows, styleLabel.Render("    "+ts+" ")+styleValue.Render(truncate(evt.Name, maxVal-16)))
		}
	}

	if len(rows) > height {
		rows = rows[:height]
	}
	return strings.Join(rows, "\n")
}

func (m Model) viewTraceStatusBar() string {
	if m.traceFilterMode {
		return styleStatusBar.Render(fmt.Sprintf(" Filter: %s█", m.traceFilter))
	}

	keys := []struct{ key, desc string }{
		{"j/k", "navigate"},
		{"tab", "switch pane"},
		{"enter", "waterfall"},
		{"1/2/3", "sort"},
		{"/", "filter"},
		{"m", "service map"},
		{"t/esc", "services"},
		{"q", "quit"},
	}

	var parts []string
	for _, k := range keys {
		parts = append(parts, styleHelpKey.Render(k.key)+styleHelp.Render(":"+k.desc))
	}

	bar := " " + strings.Join(parts, "  ")

	// Show buffer stats
	if m.store != nil {
		stats := fmt.Sprintf(" | spans:%d buf:%s",
			m.store.Len(),
			formatBytes(m.store.BytesUsed()))
		bar += styleLabel.Render(stats)
	}

	return truncate(bar, m.width)
}

func formatSpanDuration(d interface{ Nanoseconds() int64 }) string {
	ns := d.Nanoseconds()
	switch {
	case ns < 1000:
		return fmt.Sprintf("%dns", ns)
	case ns < 1_000_000:
		return fmt.Sprintf("%.1fµs", float64(ns)/1000)
	case ns < 1_000_000_000:
		return fmt.Sprintf("%.1fms", float64(ns)/1_000_000)
	default:
		return fmt.Sprintf("%.2fs", float64(ns)/1_000_000_000)
	}
}

func formatBytes(b int64) string {
	switch {
	case b >= 1024*1024*1024:
		return fmt.Sprintf("%.1fGB", float64(b)/(1024*1024*1024))
	case b >= 1024*1024:
		return fmt.Sprintf("%.1fMB", float64(b)/(1024*1024))
	case b >= 1024:
		return fmt.Sprintf("%.1fKB", float64(b)/1024)
	default:
		return fmt.Sprintf("%dB", b)
	}
}
