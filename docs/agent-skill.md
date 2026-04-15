# Agent Skill

Workbench ships an embedded agent skill that lets AI coding tools manage your local dev environment through the `bench` CLI.

## Viewing and installing the skill

```bash
bench agent-skill
```

This prints the skill content to stdout and detects installed agent tools. If any are found, you're offered the option to save the skill to the correct location for each tool.

Supported tools and their skill paths:

| Tool               | Config detected     | Skill saved to                              |
|--------------------|---------------------|---------------------------------------------|
| Claude Code        | `~/.claude/`        | `~/.claude/skills/workbench/SKILL.md`       |
| Codex              | `~/.codex/`         | `~/.codex/agents/workbench.md`              |
| Gemini Code Assist | `~/.gemini/`        | `~/.gemini/agents/workbench.md`             |
| OpenCode           | `~/.config/opencode/` | `~/.config/opencode/agents/workbench.md`  |

### Print only

To print the skill without the interactive menu (useful for piping):

```bash
bench agent-skill --print
bench agent-skill --print > custom-location.md
```

## What the skill does

Once installed, the agent can invoke `bench` commands (status, logs, start, stop, restart, validate) to diagnose and manage services in your running dev environment. See the [skill source](../internal/cli/skill/SKILL.md) for the full prompt.
