# Architecture

## Overview

bench is structured as a layered system where the TUI is a view over runtime state, not the owner of it.

```
┌──────────────────────────────────┐
│            TUI / CLI             │  View/Controller
├──────────────────────────────────┤
│           Event Bus              │  Internal pub/sub
├──────────┬───────────┬───────────┤
│Supervisor│  Watcher  │Log Buffer │  Runtime Engine
├──────────┴───────────┴───────────┤
│         Config Loader            │  Configuration
└──────────────────────────────────┘
```

## Components

### Config (`internal/config/`)

Parses YAML, applies defaults, validates schema and semantic rules (cycle detection, path existence). Produces a `Config` struct consumed by all other components.

### Service State (`internal/service/`)

Defines the `Status` enum and `Info` struct. Info holds runtime metadata (PID, uptime, exit code, restart count) with mutex-protected access. `Snapshot` provides lock-free copies for the TUI.

### Event Bus (`internal/events/`)

Simple typed pub/sub. Publishers call `Publish(Event)`, subscribers receive on buffered channels. Slow subscribers have events dropped rather than blocking the system.

Events:
- `ServiceStateChanged` — status transitions
- `LogLine` — stdout/stderr output
- `FileChanged` — watched file modifications

### Log Buffer (`internal/logbuf/`)

Thread-safe ring buffer storing log lines per service. Fixed capacity (configurable via `log_buffer_lines`). Supports `Lines()`, `Last(n)`, and `Clear()`.

### Supervisor (`internal/supervisor/`)

The core runtime. Manages a map of service key to `managedService`. Each service gets its own goroutine (`runLoop`) that:

1. Starts the process
2. Captures stdout/stderr via pipe readers
3. Waits for: process exit, stop signal, restart signal, or context cancellation
4. Applies restart policy (never/on-failure/always) with backoff and max retries
5. Sends SIGTERM to process group on stop, escalates to SIGKILL after timeout

External operations communicate via buffered channels (stopCh, restartCh), not shared mutable state.

### Watcher (`internal/watcher/`)

Per-service file watcher using fsnotify. For each watched service:
- Recursively adds directories under watch paths
- Filters events through include/ignore glob patterns (doublestar)
- Debounces rapid changes into a single restart
- Calls `supervisor.RestartService()` when triggered

### TUI (`internal/tui/`)

Built on bubbletea (Elm-architecture). The model subscribes to the event bus and re-renders on events and a 1-second tick (for uptime display).

Layout:
- Left pane: service list with status indicators
- Right upper: selected service detail (PID, command, restarts, etc.)
- Right lower: log view with follow/search/filter

### CLI (`internal/cli/`)

Subcommand dispatch using stdlib `flag`. Each command creates its own FlagSet. The `up` command wires together config, supervisor, watcher, and TUI.

## Design Principles

- **Config is separate from runtime state** — ServiceConfig vs service.Info
- **Services are state machines** — well-defined status transitions
- **TUI subscribes to state, doesn't own it** — supervisor is the source of truth
- **Process isolation** — each service runs in its own process group (Setpgid)
- **Restart logic is centralized** — in the supervisor runLoop, not scattered
- **Event-driven** — components communicate through the bus, not direct calls
