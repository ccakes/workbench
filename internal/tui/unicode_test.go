package tui

import (
	"fmt"
	"testing"

	"github.com/charmbracelet/lipgloss"
)

func TestUnicodeIndicatorWidths(t *testing.T) {
	chars := map[string]string{
		"BLACK CIRCLE":         "●",
		"CIRCLE LEFT HALF":     "◐",
		"CIRCLE RIGHT HALF":    "◑",
		"DOTTED CIRCLE":        "◌",
		"WHITE CIRCLE":         "○",
		"CIRCLED DIVISION":     "⊘",
		"ROUNDED BORDER TL":    "╭",
		"ROUNDED BORDER TR":    "╮",
		"ROUNDED BORDER BL":    "╰",
		"ROUNDED BORDER BR":    "╯",
		"HORIZONTAL LINE":      "─",
		"VERTICAL LINE":        "│",
	}

	for name, ch := range chars {
		lgW := lipgloss.Width(ch)
		fmt.Printf("  %s (%s U+%04X): lipgloss width=%d, bytes=%d\n",
			ch, name, []rune(ch)[0], lgW, len(ch))
		if lgW != 1 {
			t.Errorf("%s: lipgloss says width=%d, expected 1", name, lgW)
		}
	}

	// Now test a full styled indicator
	styled := styleStatusRunning.Render("●")
	fmt.Printf("\n  Styled ●: lipgloss width=%d, bytes=%d\n", lipgloss.Width(styled), len(styled))
}
