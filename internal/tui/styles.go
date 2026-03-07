package tui

import "github.com/charmbracelet/lipgloss"

var (
	colorGreen   = lipgloss.Color("2")
	colorRed     = lipgloss.Color("1")
	colorYellow  = lipgloss.Color("3")
	colorBlue    = lipgloss.Color("4")
	colorMagenta = lipgloss.Color("5")
	colorCyan    = lipgloss.Color("6")
	colorGray    = lipgloss.Color("8")
	colorWhite   = lipgloss.Color("15")

	styleBorder = lipgloss.NewStyle().
			Border(lipgloss.RoundedBorder()).
			BorderForeground(colorGray)

	styleBorderActive = lipgloss.NewStyle().
				Border(lipgloss.RoundedBorder()).
				BorderForeground(colorCyan)

	styleTitle = lipgloss.NewStyle().
			Bold(true).
			Foreground(colorWhite)

	styleSelected = lipgloss.NewStyle().
			Bold(true).
			Foreground(colorCyan)

	styleStatusRunning = lipgloss.NewStyle().Foreground(colorGreen)
	styleStatusStopped = lipgloss.NewStyle().Foreground(colorGray)
	styleStatusFailed  = lipgloss.NewStyle().Foreground(colorRed)
	styleStatusPending = lipgloss.NewStyle().Foreground(colorYellow)
	styleStatusBackoff = lipgloss.NewStyle().Foreground(colorMagenta)

	styleLabel = lipgloss.NewStyle().
			Foreground(colorGray)

	styleValue = lipgloss.NewStyle().
			Foreground(colorWhite)

	styleStdout = lipgloss.NewStyle().Foreground(colorWhite)
	styleStderr = lipgloss.NewStyle().Foreground(colorRed)

	styleStatusBar = lipgloss.NewStyle().
			Foreground(colorGray)

	styleHelp = lipgloss.NewStyle().
			Foreground(colorGray)

	styleHelpKey = lipgloss.NewStyle().
			Foreground(colorCyan).
			Bold(true)
)

func statusStyle(status string) lipgloss.Style {
	switch status {
	case "running", "ready":
		return styleStatusRunning
	case "stopped", "disabled":
		return styleStatusStopped
	case "failed":
		return styleStatusFailed
	case "pending", "starting", "restarting", "stopping":
		return styleStatusPending
	case "backoff":
		return styleStatusBackoff
	default:
		return styleStatusStopped
	}
}

func statusIndicator(status string) string {
	switch status {
	case "running", "ready":
		return styleStatusRunning.Render("●")
	case "starting", "restarting":
		return styleStatusPending.Render("◐")
	case "stopping":
		return styleStatusPending.Render("◑")
	case "failed":
		return styleStatusFailed.Render("●")
	case "backoff":
		return styleStatusBackoff.Render("◌")
	case "pending":
		return styleStatusPending.Render("○")
	case "stopped":
		return styleStatusStopped.Render("○")
	case "disabled":
		return styleStatusStopped.Render("⊘")
	default:
		return "○"
	}
}
