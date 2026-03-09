package cli

import (
	"embed"
	"flag"
	"fmt"
	"os"
	"path/filepath"
)

//go:embed skill/SKILL.md
var skillFS embed.FS

func runInstallSkill(args []string) int {
	fs := flag.NewFlagSet("install-skill", flag.ExitOnError)
	claudePath := fs.String("claude-path", "", "path to .claude directory (default: ~/.claude)")
	_ = fs.Parse(args)

	base := *claudePath
	if base == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			fmt.Fprintf(os.Stderr, "error: cannot determine home directory: %v\n", err)
			return 1
		}
		base = filepath.Join(home, ".claude")
	}

	dest := filepath.Join(base, "skills", "workbench", "SKILL.md")
	if err := os.MkdirAll(filepath.Dir(dest), 0o755); err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		return 1
	}

	data, err := skillFS.ReadFile("skill/SKILL.md")
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		return 1
	}

	if err := os.WriteFile(dest, data, 0o644); err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		return 1
	}

	fmt.Printf("installed skill to %s\n", dest)
	return 0
}
