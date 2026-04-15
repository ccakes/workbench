package cli

import (
	"bufio"
	"embed"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

//go:embed skill/SKILL.md
var skillFS embed.FS

type agentTarget struct {
	name    string
	dir     string // config dir relative to home
	relPath string // skill file relative to dir
}

var agentTargets = []agentTarget{
	{"Claude Code", ".claude", "skills/workbench/SKILL.md"},
	{"Codex", ".codex", "agents/workbench.md"},
	{"Gemini Code Assist", ".gemini", "agents/workbench.md"},
	{"OpenCode", ".config/opencode", "agents/workbench.md"},
}

func runAgentSkill(args []string) int {
	fs := flag.NewFlagSet("agent-skill", flag.ExitOnError)
	printOnly := fs.Bool("print", false, "print skill content and exit")
	_ = fs.Parse(args)

	data, err := skillFS.ReadFile("skill/SKILL.md")
	if err != nil {
		fmt.Fprintf(os.Stderr, "error reading embedded skill: %v\n", err)
		return 1
	}

	fmt.Print(string(data))

	if *printOnly {
		return 0
	}

	home, err := os.UserHomeDir()
	if err != nil {
		fmt.Fprintf(os.Stderr, "\nerror: cannot determine home directory: %v\n", err)
		return 1
	}

	type match struct {
		name    string
		dest    string
		display string
	}

	var found []match
	for _, t := range agentTargets {
		base := filepath.Join(home, t.dir)
		info, err := os.Stat(base)
		if err != nil || !info.IsDir() {
			continue
		}
		found = append(found, match{
			name:    t.name,
			dest:    filepath.Join(base, t.relPath),
			display: "~/" + filepath.Join(t.dir, t.relPath),
		})
	}

	if len(found) == 0 {
		fmt.Fprintf(os.Stderr, "\nno supported agent tool configurations detected\n")
		return 0
	}

	fmt.Fprintf(os.Stderr, "\ndetected agent tools:\n\n")
	for i, m := range found {
		fmt.Fprintf(os.Stderr, "  [%d] %-20s  %s\n", i+1, m.name, m.display)
	}
	fmt.Fprintln(os.Stderr)
	if len(found) > 1 {
		fmt.Fprintf(os.Stderr, "  [a] save to all\n")
	}
	fmt.Fprintf(os.Stderr, "  [q] exit\n")
	fmt.Fprintf(os.Stderr, "\nchoice: ")

	reader := bufio.NewReader(os.Stdin)
	line, _ := reader.ReadString('\n')
	choice := strings.TrimSpace(line)

	switch choice {
	case "q", "":
		return 0
	case "a":
		if len(found) < 2 {
			fmt.Fprintf(os.Stderr, "invalid choice\n")
			return 1
		}
		for _, m := range found {
			if err := saveSkill(m.dest, data); err != nil {
				fmt.Fprintf(os.Stderr, "  error saving to %s: %v\n", m.name, err)
			} else {
				fmt.Fprintf(os.Stderr, "  saved to %s\n", m.display)
			}
		}
	default:
		var idx int
		if _, err := fmt.Sscanf(choice, "%d", &idx); err != nil || idx < 1 || idx > len(found) {
			fmt.Fprintf(os.Stderr, "invalid choice\n")
			return 1
		}
		m := found[idx-1]
		if err := saveSkill(m.dest, data); err != nil {
			fmt.Fprintf(os.Stderr, "error: %v\n", err)
			return 1
		}
		fmt.Fprintf(os.Stderr, "saved to %s\n", m.display)
	}

	return 0
}

func saveSkill(dest string, data []byte) error {
	if err := os.MkdirAll(filepath.Dir(dest), 0o755); err != nil {
		return err
	}
	return os.WriteFile(dest, data, 0o644)
}
