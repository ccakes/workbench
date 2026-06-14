package tui

import (
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/charmbracelet/x/ansi"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/ccakes/workbench/internal/events"
	"github.com/ccakes/workbench/internal/logbuf"
	"github.com/ccakes/workbench/internal/service"
	"github.com/ccakes/workbench/internal/spanbuf"
)

func sortStrings(s []string) { sort.Strings(s) }

const (
	paneList = iota
	paneLogs
)

const (
	viewModeServices = iota
	viewModeTraces
)

// logScrollStepX is how many columns h/l move the log pane horizontally.
const logScrollStepX = 8

type Model struct {
	session  Session
	eventCh  <-chan events.Event
	services []string

	selected   int
	activePane int
	width      int
	height     int

	logFollow  bool
	logOffset  int // vertical scroll offset (lines from bottom), 0 = newest
	logOffsetX int // horizontal scroll offset (columns), 0 = leftmost
	allLogs    bool
	showHelp   bool

	searchMode  bool
	searchQuery string

	confirmQuit bool

	// Trace view state
	viewMode        int
	traceSelected   int
	tracePane       int // 0=span list, 1=span detail
	traceSpans      []spanbuf.Span
	traceFilter     string
	traceFilterMode bool
	traceSortMode   int // 0=time, 1=duration, 2=service
	waterfallMode   bool
	waterfallSpans  []spanbuf.Span
	serviceMapMode  bool
}

type eventMsg events.Event
type tickMsg time.Time

func NewModel(session Session) Model {
	return Model{
		session:   session,
		eventCh:   session.Events(),
		services:  session.Keys(),
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
		if m.confirmQuit {
			switch msg.String() {
			case "y", "Y", "enter":
				return m, tea.Quit
			default:
				m.confirmQuit = false
			}
			return m, nil
		}
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
		m.confirmQuit = true
		return m, nil

	case "j", "down":
		if m.activePane == paneList {
			if m.selected < len(m.displayOrder())-1 {
				m.selected++
				m.logOffset = 0
				m.logOffsetX = 0
				m.logFollow = true
			}
		} else {
			// Scroll logs down, toward the newest lines.
			if m.logOffset > 0 {
				m.logOffset--
			}
			if m.logOffset == 0 {
				m.logFollow = true // back at the live tail
			}
		}

	case "k", "up":
		if m.activePane == paneList {
			if m.selected > 0 {
				m.selected--
				m.logOffset = 0
				m.logOffsetX = 0
				m.logFollow = true
			}
		} else {
			// Scroll logs up, toward the oldest lines.
			maxOffset, _ := m.logScrollBounds()
			if m.logOffset < maxOffset {
				m.logOffset++
				m.logFollow = false
			}
		}

	case "h", "left":
		if m.activePane == paneLogs {
			m.logOffsetX -= logScrollStepX
			if m.logOffsetX < 0 {
				m.logOffsetX = 0
			}
		}

	case "l", "right":
		if m.activePane == paneLogs {
			_, maxX := m.logScrollBounds()
			m.logOffsetX += logScrollStepX
			if m.logOffsetX > maxX {
				m.logOffsetX = maxX
			}
		}

	case "ctrl+d", "pgdown":
		if m.activePane == paneLogs {
			m.logOffset -= m.logPageStep()
			if m.logOffset < 0 {
				m.logOffset = 0
			}
			if m.logOffset == 0 {
				m.logFollow = true
			}
		}

	case "ctrl+u", "pgup":
		if m.activePane == paneLogs {
			maxOffset, _ := m.logScrollBounds()
			m.logOffset += m.logPageStep()
			if m.logOffset > maxOffset {
				m.logOffset = maxOffset
			}
			m.logFollow = false
		}

	case "tab":
		m.activePane = (m.activePane + 1) % 2

	case "r":
		if key := m.selectedKey(); key != "" {
			_ = m.session.RestartService(key, "manual restart")
		}

	case "s":
		if key := m.selectedKey(); key != "" {
			session := m.session
			go func() { _ = session.StopService(key) }()
		}

	case "S":
		if key := m.selectedKey(); key != "" {
			_ = m.session.StartService(key)
		}

	case "w":
		if key := m.selectedKey(); key != "" {
			m.session.ToggleWatch(key)
		}

	case "f":
		m.logFollow = !m.logFollow
		if m.logFollow {
			m.logOffset = 0
		}

	case "c":
		if key := m.selectedKey(); key != "" {
			m.session.ClearLogs(key)
		}

	case "a":
		m.allLogs = !m.allLogs

	case "/":
		m.searchMode = true
		m.searchQuery = ""
		m.logOffsetX = 0

	case "G":
		m.logFollow = true
		m.logOffset = 0

	case "g":
		// scroll to top (oldest line)
		m.logFollow = false
		maxOffset, _ := m.logScrollBounds()
		m.logOffset = maxOffset

	case "t":
		if m.session.HasTracing() {
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
	if !m.session.HasTracing() {
		return
	}
	m.traceSpans = m.session.Spans()
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
	order := m.displayOrder()
	if m.selected >= 0 && m.selected < len(order) {
		return order[m.selected]
	}
	return ""
}

// displayOrder returns service keys in the order they appear in the rendered
// service list. When at least one service has a `group:` set, groups are
// listed alphabetically with ungrouped services collected under "Other" at
// the bottom; otherwise the supervisor's start order is preserved.
func (m Model) displayOrder() []string {
	useGroups := false
	for _, key := range m.services {
		if cfg := m.session.Config(key); cfg != nil && cfg.Group != "" {
			useGroups = true
			break
		}
	}
	if !useGroups {
		return m.services
	}

	grouped := make(map[string][]string)
	var groupOrder []string
	var ungrouped []string
	for _, key := range m.services {
		var groupName string
		if cfg := m.session.Config(key); cfg != nil {
			groupName = cfg.Group
		}
		if groupName == "" {
			ungrouped = append(ungrouped, key)
			continue
		}
		if _, seen := grouped[groupName]; !seen {
			groupOrder = append(groupOrder, groupName)
		}
		grouped[groupName] = append(grouped[groupName], key)
	}
	sortStrings(groupOrder)

	out := make([]string, 0, len(m.services))
	for _, g := range groupOrder {
		out = append(out, grouped[g]...)
	}
	out = append(out, ungrouped...)
	return out
}

func (m Model) View() string {
	if m.width == 0 || m.height == 0 {
		return "initializing..."
	}

	if m.confirmQuit {
		return m.viewConfirmQuit()
	}

	if m.showHelp {
		return m.viewHelp()
	}

	if m.viewMode == viewModeTraces {
		return m.viewTraces()
	}

	// Layout: left pane (service list) | right pane (detail + logs)
	leftWidth := min(max(m.width*30/100, 20), 40)
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

	order := m.displayOrder()

	// Group headers are only rendered when at least one service has a
	// `group:` field set — otherwise the list looks like it did before.
	useGroups := false
	for _, key := range order {
		if cfg := m.session.Config(key); cfg != nil && cfg.Group != "" {
			useGroups = true
			break
		}
	}

	rendered := 0
	emitGroupHeader := func(name string) bool {
		if rendered >= height-1 {
			return false
		}
		b.WriteString(styleLabel.Render(name))
		b.WriteString("\n")
		rendered++
		return true
	}
	emitService := func(i int, key string) bool {
		if rendered >= height-1 {
			return false
		}
		snap, ok := m.session.Info(key)
		if !ok {
			return true
		}
		indicator := statusIndicator(snap.Status.String())
		name := snap.Name()
		status := snap.Status.String()
		styledStatus := statusStyle(status).Render(status)

		uptime := ""
		if snap.Status.IsRunning() {
			uptime = " " + formatDuration(snap.Uptime())
		}

		suffixLen := len(uptime) + 1 + len(status)
		nameWidth := max(1, width-suffixLen-5)
		truncatedName := truncate(name, nameWidth)
		padded := truncatedName + strings.Repeat(" ", max(0, nameWidth-len(truncatedName)))

		line := " " + indicator + " " + padded
		if uptime != "" {
			line += styleLabel.Render(uptime)
		}
		line += " " + styledStatus

		if i == m.selected {
			// Strip nested ANSI codes first — their embedded resets would
			// otherwise terminate the selection background partway through
			// the line, leaving only the leading space highlighted.
			line = ansi.Strip(line)
			lineVisual := ansi.StringWidth(line)
			if lineVisual < width {
				line += strings.Repeat(" ", width-lineVisual)
			}
			line = styleSelected.Render(line)
		}
		b.WriteString(line)
		b.WriteString("\n")
		rendered++
		return true
	}

	if !useGroups {
		for i, key := range order {
			if !emitService(i, key) {
				break
			}
		}
		return strings.TrimRight(b.String(), "\n")
	}

	var lastGroup string
	ungroupedHeaderEmitted := false
	for i, key := range order {
		var groupName string
		if cfg := m.session.Config(key); cfg != nil {
			groupName = cfg.Group
		}
		if groupName == "" {
			if !ungroupedHeaderEmitted {
				if !emitGroupHeader("Other") {
					return strings.TrimRight(b.String(), "\n")
				}
				ungroupedHeaderEmitted = true
			}
		} else if groupName != lastGroup {
			if !emitGroupHeader(groupName) {
				return strings.TrimRight(b.String(), "\n")
			}
			lastGroup = groupName
		}
		if !emitService(i, key) {
			return strings.TrimRight(b.String(), "\n")
		}
	}
	return strings.TrimRight(b.String(), "\n")
}

func (m Model) viewDetail(width, height int) string {
	key := m.selectedKey()
	if key == "" {
		return styleLabel.Render("no service selected")
	}

	snap, ok := m.session.Info(key)
	if !ok {
		return ""
	}
	svcCfg := m.session.Config(key)

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

// logViewport mirrors the layout math in View to report the log pane's content
// width and number of visible log lines. handleKey uses it to clamp scrolling
// without rendering. Keep in sync with View's layout calculations.
func (m Model) logViewport() (width, visibleLines int) {
	leftWidth := min(max(m.width*30/100, 20), 40)
	rightWidth := m.width - leftWidth

	mainHeight := m.height - 1 - 1 // status bar + terminal-scroll guard
	detailHeight := 10
	if mainHeight < 20 {
		detailHeight = 6
	}
	logHeight := mainHeight - detailHeight

	// viewLogs receives (rightWidth-4, logHeight-2); it then reserves 1 line
	// for the header, so visible log lines = (logHeight-2) - 1.
	width = max(rightWidth-4, 1)
	visibleLines = max((logHeight-2)-1, 1)
	return width, visibleLines
}

// logPageStep is the number of lines ctrl+d/ctrl+u (PageDown/PageUp) move,
// leaving one line of overlap for context.
func (m Model) logPageStep() int {
	_, visibleLines := m.logViewport()
	step := max(visibleLines-1, 1)
	return step
}

// logScrollBounds returns the maximum vertical offset (oldest line at the top)
// and the maximum horizontal offset (so the longest visible line's right edge
// reaches the pane edge) for the current view. Used to clamp scrolling.
func (m Model) logScrollBounds() (maxOffset, maxX int) {
	width, visibleLines := m.logViewport()
	lines := m.currentLogLines()
	total := len(lines)

	maxOffset = max(total-visibleLines, 0)

	// Horizontal bound is based on the lines currently in view. Recompute the
	// visible window the same way viewLogs does (offset clamped to maxOffset).
	offset := max(min(m.logOffset, maxOffset), 0)
	end := total - offset
	start := max(end-visibleLines, 0)
	longest := 0
	for i := start; i < end && i < total; i++ {
		if w := ansi.StringWidth(lines[i].Text); w > longest {
			longest = w
		}
	}
	textWidth := max(width-11, 1)
	maxX = max(longest-textWidth, 0)
	return maxOffset, maxX
}

// currentLogLines returns the log lines for the active view (single service or
// merged "all" view), already sorted and search-filtered. Shared by viewLogs
// (rendering) and handleKey (scroll clamping) so both agree on the line set.
func (m Model) currentLogLines() []logbuf.Line {
	key := m.selectedKey()
	if key == "" {
		return nil
	}

	var lines []logbuf.Line
	if m.allLogs {
		// Merge logs from all services
		for _, k := range m.services {
			logs := m.session.Logs(k)
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
		logs := m.session.Logs(key)
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

	return lines
}

func (m Model) viewLogs(width, height int) string {
	if m.selectedKey() == "" {
		return ""
	}

	lines := m.currentLogLines()
	total := len(lines)
	if total == 0 {
		label := styleLabel.Render("Logs")
		if m.allLogs {
			label = styleLabel.Render("Logs (all services)")
		}
		return label + "\n" + styleLabel.Render("  (no output)")
	}

	// Calculate visible range
	visibleLines := max(
		// reserve 1 for header
		height-1, 1)

	end := min(max(total-m.logOffset, 0), total)
	start := max(end-visibleLines, 0)

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
	b.WriteString(label)
	b.WriteString(follow)
	b.WriteString(search)
	b.WriteString("\n")

	// Width available for the log text after the "15:04:05 " timestamp prefix.
	textWidth := max(width-11, 1)

	for i := start; i < end; i++ {
		l := lines[i]
		ts := l.Timestamp.Format("15:04:05")
		// Horizontal scroll: slice the line to the visible column window,
		// preserving ANSI styling. ansi.Cut bounds the visual width so the
		// rendered line never overflows the pane.
		text := ansi.Cut(l.Text, m.logOffsetX, m.logOffsetX+textWidth)

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
		{"j/k", "scroll"},
		{"h/l", "pan"},
		{"tab", "switch pane"},
		{"r", "restart"},
		{"s", "stop"},
		{"S", "start"},
		{"w", "watch"},
		{"f", "follow"},
		{"/", "search"},
	}
	if m.session.HasTracing() {
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

func (m Model) viewConfirmQuit() string {
	prompt := lipgloss.JoinVertical(lipgloss.Center,
		styleTitle.Render("Quit workbench?"),
		"",
		styleHelp.Render("This will stop all managed services."),
		"",
		styleHelpKey.Render("y")+styleHelp.Render(" confirm   ")+
			styleHelpKey.Render("n")+styleHelp.Render(" cancel"),
	)

	dialog := styleBorderActive.Padding(1, 3).Render(prompt)

	return lipgloss.Place(m.width, m.height-1,
		lipgloss.Center, lipgloss.Center, dialog)
}

func (m Model) viewHelp() string {
	var b strings.Builder
	b.WriteString(styleTitle.Render("Keyboard Shortcuts"))
	b.WriteString("\n\n")

	bindings := []struct{ key, desc string }{
		{"j / ↓", "Move selection down / scroll logs down"},
		{"k / ↑", "Move selection up / scroll logs up"},
		{"h / ←", "Scroll logs left"},
		{"l / →", "Scroll logs right"},
		{"ctrl+d / pgdn", "Scroll logs down one page"},
		{"ctrl+u / pgup", "Scroll logs up one page"},
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
	if m.session.HasTracing() {
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
	if d < 10*time.Minute {
		return fmt.Sprintf("%dm%ds", int(d.Minutes()), int(d.Seconds())%60)
	}
	if d < time.Hour {
		return fmt.Sprintf("%dm", int(d.Minutes()))
	}
	if d < 3*time.Hour {
		return fmt.Sprintf("%dh%dm", int(d.Hours()), int(d.Minutes())%60)
	}
	return fmt.Sprintf("%dh", int(d.Hours()))
}
