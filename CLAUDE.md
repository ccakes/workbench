# workbench

TUI process orchestrator for local development. Manages multiple services (processes and containers) with a split-pane terminal UI. The binary is called `bench`.

## Quick reference

```
go build ./cmd/bench        # build
go test ./...               # test
bench -f bench.yaml         # run (looks for bench.yaml by default)
bench -f bench.yaml -headless  # run without TUI
```

## Project structure

```
cmd/bench/              Entry point (binary name is `bench`)
internal/
  cli/                  Subcommand dispatch, headless mode, signal handling
  collector/            OTLP HTTP trace collector (protobuf decoding)
  config/               YAML parsing, validation, defaults, env file loading
  events/               Event types and pub/sub Bus (buffered channels)
  logbuf/               Thread-safe ring buffer for log lines
  runner/               Process and container execution (runner.go, process.go, docker.go, container.go)
  service/              Status enum, Info struct with Snapshot pattern
  spanbuf/              Size-based ring buffer for spans, service interaction graph
  supervisor/           Process lifecycle, restart policies, signal handling
  tui/                  Bubbletea model with left/right pane layout, trace browser
  watcher/              fsnotify + debounce + glob pattern matching
```

## Key conventions

- **stdlib `flag`** for CLI — no cobra.
- **Bubbletea + lipgloss** for TUI rendering.
- **Snapshot pattern**: `service.Info` uses `RWMutex` with `Snapshot()` for lock-free TUI reads.
- **Per-service goroutine**: Supervisor uses `runLoop` with channel-based stop/restart. Only the `startProcess` goroutine calls `cmd.Wait()` to avoid double-wait deadlocks.
- **Process groups**: `syscall.Setpgid` for clean signal handling; `stopProcess` sends SIGTERM to the group, escalates to SIGKILL after timeout.

## TUI gotchas

- **Always use visual width, not byte length, for string measurement.** Process output often contains ANSI escape codes (colored logs from Gradle, Spring Boot, etc.). Use `ansi.StringWidth()` and `ansi.Truncate()` from `charmbracelet/x/ansi` — never `len()` — when calculating display widths or truncating strings for the TUI. Using byte length causes lines to overflow their panes and break the layout.
- The `truncate()` helper in `tui/app.go` handles this correctly — use it for all width-limited text.

## Documentation

When adding new features, make sure to write a simple markdown document for them under `docs/`. Also when making changes, always check whether any existing documentation needs to be updated.
