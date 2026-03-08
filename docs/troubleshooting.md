# Troubleshooting

## Common issues

### "no bench.yml found in current or parent directories"

workbench searches for `bench.yml` or `bench.yaml` starting from the current directory and walking up to the filesystem root. Either:
- Create a `bench.yml` in your project
- Use `--config <path>` to specify the location explicitly

### Service shows "failed" immediately

The service command could not be started. Check:
- Does the working directory (`dir`) exist?
- Is the command executable available in PATH?
- Are file permissions correct?

The error detail is shown in the TUI detail pane or in CLI output.

### Service restarts in a loop

If a service keeps crashing and restarting:
- Check the logs in the TUI for error output
- The restart count and last error are shown in the detail pane
- Set `max_retries` to limit restart attempts
- Use `policy: never` to disable auto-restart while debugging

### File changes don't trigger restart

Check:
- `watch.enabled` is set to `true` for the service
- The changed file matches an `include` pattern (if include patterns are set)
- The changed file doesn't match an `ignore` pattern
- The watch indicator shows "on" in the TUI (toggle with `w`)

### Watch restarts are too frequent

Increase the debounce window:
```yaml
watch:
  debounce: 1s
```

Or set it globally:
```yaml
global:
  watch_debounce: 1s
```

### Environment variables not loading

Env vars are loaded in order: system env → global env_file → service env_file → service env (inline). Later values override earlier ones.

Check:
- The `env_file` path is correct (relative to the config file location)
- The file uses `KEY=VALUE` format
- Quoted values use matching quotes: `KEY="value"` or `KEY='value'`

### Service won't stop (hangs on stopping)

workbench sends SIGTERM to the process group, then waits `shutdown_timeout` before escalating to SIGKILL. If stopping seems slow:
- Reduce `shutdown_timeout` (per-service or global)
- Ensure the service handles SIGTERM properly
- Check if the service spawns child processes that don't forward signals

### TUI looks broken or garbled

- Ensure your terminal supports 256 colors
- Try resizing the terminal window
- If using tmux/screen, ensure it passes through escape sequences
- Minimum recommended terminal size: 80x24

### Dependency cycle detected

workbench validates the `depends_on` graph at startup. If you see a cycle error, check your dependency chain. For example:

```
dependency cycle detected: a -> b -> c -> a
```

Remove one of the circular dependencies.

## Getting debug output

Run with `--verbose` and `--no-tui` for detailed event logging:

```bash
bench up --no-tui --verbose
```

This prints state changes, log lines, and file change events to stdout.

## Validate without running

```bash
bench validate
```

This parses and validates the config, checks directory existence, verifies env files, and detects dependency cycles — without starting any processes.
