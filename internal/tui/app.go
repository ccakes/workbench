package tui

import (
	"fmt"
	"strings"
	"time"

	"github.com/charmbracelet/x/ansi"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/ccakes/workbench/internal/events"
	"github.com/ccakes/workbench/internal/logbuf"
	"github.com/ccakes/workbench/internal/service"
	"github.com/ccakes/workbench/internal/spanbuf"
	"github.com/ccakes/workbench/internal/supervisor"
)

const (
	paneList = iota
	paneLogs
)

const (
	viewModeServices = iota
	viewModeTraces
)

type Model struct {
	sup      *supervisor.Supervisor
	store    *spanbuf.Store
	eventCh  chan events.Event
	services []string

	selected   int
	activePane int
	width      int
	height     int

	logFollow  bool
	logOffset  int
	allLogs    bool
	showHelp   bool

	searchMode  bool
	searchQuery string

	// Trace view state
	viewMode       int
	traceSelected  int
	tracePane      int // 0=span list, 1=span detail
	traceSpans     []spanbuf.Span
	traceFilter    string
	traceFilterMode bool
	traceSortMode  int // 0=time, 1=duration, 2=service
	waterfallMode  bool
	waterfallSpans []spanbuf.Span
	serviceMapMode bool
}

type eventMsg events.Event
type tickMsg time.Time

func NewModel(sup *supervisor.Supervisor, store *spanbuf.Store) Model {
	ch := sup.Bus().Subscribe(256)
	return Model{
		sup:       sup,
		store:     store,
		eventCh:   ch,
		services:  sup.ServiceKeys(),
		logFollow: true,
	}
}

func (m Model) Init() tea.Cmd {
	return tea.Batch(
		waitForEvent(m.eventCh),
		tickCmd(),
	)
}

func waitForEvent(ch <-chan events.Event) tea.Cmd {
	return func() tea.Msg {
		evt, ok := <-ch
		if !ok {
			return nil
		}
		return eventMsg(evt)
	}
}

func tickCmd() tea.Cmd {
	return tea.Tick(time.Second, func(t time.Time) tea.Msg {
		return tickMsg(t)
	})
}

func (m Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width = msg.Width
		m.height = msg.Height
		return m, nil

	case tea.KeyMsg:
		if m.searchMode || m.traceFilterMode {
			return m.handleSearchKey(msg)
		}
		if m.showHelp {
			m.showHelp = false
			return m, nil
		}
		if m.viewMode == viewModeTraces {
			return m.handleTraceKey(msg)
		}
		return m.handleKey(msg)

	case eventMsg:
		// Any event triggers a re-render automatically
		if m.logFollow {
			m.logOffset = 0
		}
		return m, waitForEvent(m.eventCh)

	case tickMsg:
		return m, tickCmd()
	}

	return m, nil
}

func (m Model) handleKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "q", "ctrl+c":
		return m, tea.Quit

	case "j", "down":
		if m.activePane == paneList {
			if m.selected < len(m.services)-1 {
				m.selected++
				m.logOffset = 0
				m.logFollow = true
			}
		} else {
			m.logOffset++
		}

	case "k", "up":
		if m.activePane == paneList {
			if m.selected > 0 {
				m.selected--
				m.logOffset = 0
				m.logFollow = true
			}
		} else {
			if m.logOffset > 0 {
				m.logOffset--
				m.logFollow = false
			}
		}

	case "tab":
		m.activePane = (m.activePane + 1) % 2

	case "r":
		if key := m.selectedKey(); key != "" {
			_ = m.sup.RestartService(key, "manual restart")
		}

	case "s":
		if key := m.selectedKey(); key != "" {
			go func() { _ = m.sup.StopService(key) }()
		}

	case "S":
		if key := m.selectedKey(); key != "" {
			_ = m.sup.StartService(key)
		}

	case "w":
		if key := m.selectedKey(); key != "" {
			m.sup.ToggleWatch(key)
		}

	case "f":
		m.logFollow = !m.logFollow
		if m.logFollow {
			m.logOffset = 0
		}

	case "c":
		if key := m.selectedKey(); key != "" {
			if logs := m.sup.ServiceLogs(key); logs != nil {
				logs.Clear()
			}
		}

	case "a":
		m.allLogs = !m.allLogs

	case "/":
		m.searchMode = true
		m.searchQuery = ""

	case "G":
		m.logFollow = true
		m.logOffset = 0

	case "g":
		m.logFollow = false
		// scroll to top — set offset to max
		if key := m.selectedKey(); key != "" {
			if logs := m.sup.ServiceLogs(key); logs != nil {
				m.logOffset = logs.Len()
			}
		}

	case "t":
		if m.store != nil {
			m.viewMode = viewModeTraces
			m.traceSelected = 0
			m.tracePane = 0
			m.refreshTraceSpans()
		}

	case "?":
		m.showHelp = true
	}

	return m, nil
}

func (m *Model) refreshTraceSpans() {
	if m.store == nil {
		return
	}
	m.traceSpans = m.store.Spans()
}

func (m Model) handleSearchKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	if m.traceFilterMode {
		switch msg.String() {
		case "enter", "esc":
			m.traceFilterMode = false
			m.refreshTraceSpans()
		case "backspace":
			if len(m.traceFilter) > 0 {
				m.traceFilter = m.traceFilter[:len(m.traceFilter)-1]
			}
		default:
			if len(msg.String()) == 1 {
				m.traceFilter += msg.String()
			}
		}
		return m, nil
	}

	switch msg.String() {
	case "enter", "esc":
		m.searchMode = false
	case "backspace":
		if len(m.searchQuery) > 0 {
			m.searchQuery = m.searchQuery[:len(m.searchQuery)-1]
		}
	default:
		if len(msg.String()) == 1 {
			m.searchQuery += msg.String()
		}
	}
	return m, nil
}

func (m Model) selectedKey() string {
	if m.selected >= 0 && m.selected < len(m.services) {
		return m.services[m.selected]
	}
	return ""
}

func (m Model) View() string {
	if m.width == 0 || m.height == 0 {
		return "initializing..."
	}

	if m.showHelp {
		return m.viewHelp()
	}

	if m.viewMode == viewModeTraces {
		return m.viewTraces()
	}

	// Layout: left pane (service list) | right pane (detail + logs)
	leftWidth := m.width * 30 / 100
	if leftWidth < 20 {
		leftWidth = 20
	}
	if leftWidth > 40 {
		leftWidth = 40
	}
	rightWidth := m.width - leftWidth

	statusBarHeight := 1
	mainHeight := m.height - statusBarHeight - 1 // -1 to prevent terminal scroll causing duplicate lines

	// Left pane: service list
	leftContent := m.viewServiceList(leftWidth-4, mainHeight-2)
	leftBorder := styleBorder
	if m.activePane == paneList {
		leftBorder = styleBorderActive
	}
	leftPane := leftBorder.
		Width(leftWidth - 2).MaxWidth(leftWidth).
		Height(mainHeight - 2).MaxHeight(mainHeight).
		Render(leftContent)

	// Right pane: detail + logs
	detailHeight := 10
	if mainHeight < 20 {
		detailHeight = 6
	}
	logHeight := mainHeight - detailHeight

	detailContent := m.viewDetail(rightWidth-4, detailHeight-2)
	detailPane := styleBorder.
		Width(rightWidth - 2).MaxWidth(rightWidth).
		Height(detailHeight - 2).MaxHeight(detailHeight).
		Render(detailContent)

	logContent := m.viewLogs(rightWidth-4, logHeight-2)
	logBorder := styleBorder
	if m.activePane == paneLogs {
		logBorder = styleBorderActive
	}
	logPane := logBorder.
		Width(rightWidth - 2).MaxWidth(rightWidth).
		Height(logHeight - 2).MaxHeight(logHeight).
		Render(logContent)

	rightPane := lipgloss.JoinVertical(lipgloss.Left, detailPane, logPane)

	main := lipgloss.JoinHorizontal(lipgloss.Top, leftPane, rightPane)

	statusBar := m.viewStatusBar()

	output := lipgloss.JoinVertical(lipgloss.Left, main, statusBar)

	// Clamp output to terminal height-1 to prevent terminal scroll
	lines := strings.Split(output, "\n")
	if len(lines) > m.height-1 {
		lines = lines[:m.height-1]
	}
	return strings.Join(lines, "\n")
}

func (m Model) viewServiceList(width, height int) string {
	var b strings.Builder
	b.WriteString(styleTitle.Render("Services"))
	b.WriteString("\n")

	for i, key := range m.services {
		if i >= height-1 {
			break
		}
		info := m.sup.ServiceInfo(key)
		if info == nil {
			continue
		}
		snap := info.Snapshot()

		indicator := statusIndicator(snap.Status.String())
		name := snap.Name()
		displayName := name
		status := snap.Status.String()
		styledStatus := statusStyle(status).Render(status)

		uptime := ""
		if snap.Status.IsRunning() {
			uptime = " " + formatDuration(snap.Uptime())
		}

		// Calculate name column width to fill the line.
		// indicator (2) + spaces (3) + name + uptime + status must fit in width.
		suffixLen := len(uptime) + 1 + len(status)
		nameWidth := max(1, width-suffixLen-5) // 5 = " " + indicator + " " + " " before uptime/status + margin
		truncatedName := truncate(displayName, nameWidth)
		// Pad name with spaces (plain text, so len() == visual width)
		padded := truncatedName + strings.Repeat(" ", max(0, nameWidth-len(truncatedName)))

		line := " " + indicator + " " + padded
		if uptime != "" {
			line += styleLabel.Render(uptime)
		}
		line += " " + styledStatus

		if i == m.selected {
			// Pad to full pane width so background highlight spans the row
			lineVisual := ansi.StringWidth(line)
			if lineVisual < width {
				line += strings.Repeat(" ", width-lineVisual)
			}
			line = styleSelected.Render(line)
		}

		b.WriteString(line)
		if i < len(m.services)-1 {
			b.WriteString("\n")
		}
	}
	return b.String()
}

func (m Model) viewDetail(width, height int) string {
	key := m.selectedKey()
	if key == "" {
		return styleLabel.Render("no service selected")
	}

	info := m.sup.ServiceInfo(key)
	if info == nil {
		return ""
	}
	snap := info.Snapshot()
	svcCfg := m.sup.ServiceConfig(key)

	var rows []string
	title := styleTitle.Render(snap.Name()) + " " + statusStyle(snap.Status.String()).Render(snap.Status.String())
	rows = append(rows, title)

	// Label prefix "  %-10s" = 12 visual chars; value gets the rest
	maxVal := max(1, width-12)
	row := func(label, value string) {
		rows = append(rows, styleLabel.Render(fmt.Sprintf("  %-10s", label))+styleValue.Render(truncate(value, maxVal)))
	}

	if svcCfg != nil && snap.ServiceType == "container" {
		if snap.ContainerID != "" {
			row("Container", snap.ContainerID)
		}
		if snap.Image != "" {
			row("Image", snap.Image)
		}
		if len(snap.Ports) > 0 {
			row("Ports", strings.Join(snap.Ports, ", "))
		}
	} else {
		if snap.PID > 0 {
			row("PID", fmt.Sprintf("%d", snap.PID))
		}
		if svcCfg != nil {
			row("Dir", svcCfg.Dir)
			if svcCfg.Command != nil {
				row("Command", svcCfg.Command.String())
			}
		}
	}
	if svcCfg != nil {
		if svcCfg.EnvFile != "" {
			row("Env File", svcCfg.EnvFile)
		}
		row("Restart", svcCfg.Restart.Policy)
	}
	if snap.Status.IsRunning() {
		row("Uptime", formatDuration(snap.Uptime()))
	}
	row("Restarts", fmt.Sprintf("%d", snap.RestartCount))

	watchStr := "off"
	if snap.WatchEnabled {
		watchStr = "on"
	}
	row("Watch", watchStr)

	if snap.ExitCode != 0 {
		row("Exit Code", fmt.Sprintf("%d", snap.ExitCode))
	}
	if snap.LastRestart != "" {
		row("Last", snap.LastRestart)
	}
	if snap.LastError != "" && snap.Status == service.StatusFailed {
		row("Error", snap.LastError)
	}

	if len(rows) > height {
		rows = rows[:height]
	}
	return strings.Join(rows, "\n")
}

func (m Model) viewLogs(width, height int) string {
	key := m.selectedKey()
	if key == "" {
		return ""
	}

	var lines []logbuf.Line
	if m.allLogs {
		// Merge logs from all services
		for _, k := range m.services {
			logs := m.sup.ServiceLogs(k)
			if logs != nil {
				lines = append(lines, logs.Lines()...)
			}
		}
		// Sort by timestamp (simple insertion sort since logs are mostly ordered)
		for i := 1; i < len(lines); i++ {
			for j := i; j > 0 && lines[j].Timestamp.Before(lines[j-1].Timestamp); j-- {
				lines[j], lines[j-1] = lines[j-1], lines[j]
			}
		}
	} else {
		logs := m.sup.ServiceLogs(key)
		if logs != nil {
			lines = logs.Lines()
		}
	}

	// Apply search filter
	if m.searchQuery != "" {
		var filtered []logbuf.Line
		for _, l := range lines {
			if strings.Contains(l.Text, m.searchQuery) {
				filtered = append(filtered, l)
			}
		}
		lines = filtered
	}

	total := len(lines)
	if total == 0 {
		label := styleLabel.Render("Logs")
		if m.allLogs {
			label = styleLabel.Render("Logs (all services)")
		}
		return label + "\n" + styleLabel.Render("  (no output)")
	}

	// Calculate visible range
	visibleLines := height - 1 // reserve 1 for header
	if visibleLines < 1 {
		visibleLines = 1
	}

	end := total - m.logOffset
	if end < 0 {
		end = 0
	}
	if end > total {
		end = total
	}
	start := end - visibleLines
	if start < 0 {
		start = 0
	}

	var b strings.Builder
	// Header
	label := styleLabel.Render("Logs")
	if m.allLogs {
		label = styleLabel.Render("Logs (all)")
	}
	follow := ""
	if m.logFollow {
		follow = styleStatusRunning.Render(" [follow]")
	}
	search := ""
	if m.searchMode {
		search = styleStatusPending.Render(fmt.Sprintf(" /%s", m.searchQuery))
	} else if m.searchQuery != "" {
		search = styleLabel.Render(fmt.Sprintf(" [filter: %s]", m.searchQuery))
	}
	b.WriteString(label + follow + search + "\n")

	for i := start; i < end; i++ {
		l := lines[i]
		ts := l.Timestamp.Format("15:04:05")
		text := truncate(l.Text, width-11)

		var line string
		if l.Stream == "stderr" {
			line = styleLabel.Render(ts) + " " + styleStderr.Render(text)
		} else {
			line = styleLabel.Render(ts) + " " + styleStdout.Render(text)
		}

		b.WriteString(line)
		if i < end-1 {
			b.WriteString("\n")
		}
	}

	return b.String()
}

func (m Model) viewStatusBar() string {
	if m.searchMode {
		return styleStatusBar.Render(fmt.Sprintf(" Search: %s█", m.searchQuery))
	}

	keys := []struct{ key, desc string }{
		{"j/k", "navigate"},
		{"tab", "switch pane"},
		{"r", "restart"},
		{"s", "stop"},
		{"S", "start"},
		{"w", "watch"},
		{"f", "follow"},
		{"/", "search"},
	}
	if m.store != nil {
		keys = append(keys, struct{ key, desc string }{"t", "traces"})
	}
	keys = append(keys,
		struct{ key, desc string }{"?", "help"},
		struct{ key, desc string }{"q", "quit"},
	)

	var parts []string
	for _, k := range keys {
		parts = append(parts, styleHelpKey.Render(k.key)+styleHelp.Render(":"+k.desc))
	}

	bar := " " + strings.Join(parts, "  ")
	return truncate(bar, m.width)
}

func (m Model) viewHelp() string {
	var b strings.Builder
	b.WriteString(styleTitle.Render("Keyboard Shortcuts"))
	b.WriteString("\n\n")

	bindings := []struct{ key, desc string }{
		{"j / ↓", "Move selection down / scroll logs down"},
		{"k / ↑", "Move selection up / scroll logs up"},
		{"tab", "Switch between service list and log pane"},
		{"r", "Restart selected service"},
		{"s", "Stop selected service"},
		{"S", "Start selected service"},
		{"w", "Toggle file watch for selected service"},
		{"f", "Toggle log follow mode"},
		{"c", "Clear log pane for selected service"},
		{"a", "Toggle all-services log mode"},
		{"g", "Scroll to top of logs"},
		{"G", "Scroll to bottom of logs (follow)"},
		{"/", "Search/filter logs"},
	}
	if m.store != nil {
		bindings = append(bindings, struct{ key, desc string }{"t", "Open trace browser"})
	}
	bindings = append(bindings,
		struct{ key, desc string }{"?", "Toggle this help"},
		struct{ key, desc string }{"q", "Quit"},
	)

	for _, b2 := range bindings {
		fmt.Fprintf(&b, "  %s  %s\n",
			styleHelpKey.Render(fmt.Sprintf("%-8s", b2.key)),
			b2.desc)
	}

	b.WriteString("\n")
	b.WriteString(styleLabel.Render("  Press any key to close"))
	return b.String()
}

func truncate(s string, maxLen int) string {
	if maxLen <= 0 {
		return ""
	}
	if ansi.StringWidth(s) <= maxLen {
		return s
	}
	if maxLen <= 3 {
		return ansi.Truncate(s, maxLen, "")
	}
	return ansi.Truncate(s, maxLen-3, "") + "..."
}

func formatDuration(d time.Duration) string {
	if d < time.Minute {
		return fmt.Sprintf("%ds", int(d.Seconds()))
	}
	if d < time.Hour {
		return fmt.Sprintf("%dm%ds", int(d.Minutes()), int(d.Seconds())%60)
	}
	return fmt.Sprintf("%dh%dm", int(d.Hours()), int(d.Minutes())%60)
}
