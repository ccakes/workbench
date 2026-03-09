# Claude Skill

Workbench ships a Claude Code skill that lets Claude manage your local dev environment through the `bench` CLI. The skill is embedded in the binary and can be installed with a single command.

## Installing the skill

```bash
bench install-skill
```

This writes the skill file to `~/.claude/skills/workbench/SKILL.md`.

### Custom path

To install under a different `.claude` directory:

```bash
bench install-skill --claude-path /path/to/.claude
```

## What the skill does

Once installed, Claude Code can automatically invoke `bench` commands (status, logs, start, stop, restart, validate) to diagnose and manage services in your running dev environment. See the [skill source](../claude/SKILL.md) for the full prompt.
