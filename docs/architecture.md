# Architecture

## Overview

workbench is structured as a layered system where the TUI is a view over runtime state, not the owner of it.

```
┌──────────────────────────────────────────┐
│            TUI / CLI / Control API       │  View/Controller
├──────────────────────────────────────────┤
│              Event Bus                   │  Internal pub/sub
├──────────┬───────────┬──────┬────────────┤
│Supervisor│  Watcher  │ Logs │ Span Store │  Runtime Engine
├──────────┴───────────┴──────┴────────────┤
│            Config Loader                 │  Configuration
└──────────────────────────────────────────┘
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

Built on bubbletea (Elm-architecture). The model subscribes to an event channel and re-renders on events and a 1-second tick (for uptime display).

The model talks to a `Session` rather than to the supervisor directly. Two implementations back it:

- `localSession` — a passthrough over an in-process supervisor + span store, used by foreground `bench up`.
- `remoteSession` — an `api.Client`-backed client used when attaching to a detached daemon over the control socket. It opens the streaming `subscribe` endpoint, mirrors the daemon's state into local caches (snapshots, per-service log buffers, spans) kept fresh by the stream, and serves the model's render-time reads from those caches so they never block on the socket.

Because both go through `Session`, the model code is identical in both modes.

Layout:
- Left pane: service list with status indicators
- Right upper: selected service detail (PID, command, restarts, etc.)
- Right lower: log view with follow/search/filter

### Control API (`internal/api/`)

Unix domain socket server started by the session host. Exposes a JSON request-per-connection protocol for querying and controlling the running instance. CLI subcommands (`bench status`, `bench start`, `bench stop`, `bench down`, etc.) connect to this socket instead of creating standalone supervisors.

There is also a long-lived `subscribe` stream: a client holds the connection open and the server pushes bus events (state changes, log lines, span batches) as newline-delimited JSON. This drives the attached TUI. Only **one** interactive client may hold the subscribe stream at a time (a second is rejected); ordinary request/response control commands remain multi-client.

Socket path is derived deterministically from the config file path (`SHA256(abs_path)[:8]` → `/tmp/bench-<hash>.sock`), so the client auto-discovers the running instance. See [Control API docs](control-api.md) for the full protocol.

### CLI (`internal/cli/`)

Subcommand dispatch using stdlib `flag`. Each command creates its own FlagSet.

Session model (see also [Control API docs](control-api.md)):

- `bench up` — foreground: wires together config, supervisor, watcher, collector, API server, and an in-process TUI (`localSession`). Unchanged classic behavior.
- `bench up --daemon` — starts the session in the background: spawns a detached worker (the same `up` code path, re-entered with the `BENCH_DAEMON` env sentinel and `setsid`, stdout/stderr to a log file beside the socket), waits for its socket, and returns to the shell. The worker owns the supervisor and blocks until `down` or a signal.
- `bench` (no subcommand) — attaches an interactive TUI to the running session over the socket (`remoteSession`), auto-spawning a `--daemon` session first if none is live. Quitting can leave the session running ("background"); see [keyboard shortcuts](keyboard-shortcuts.md).
- `bench down` — stops all services and ends the session.

Other subcommands connect to the running instance via the control socket.

## Design Principles

- **Config is separate from runtime state** — ServiceConfig vs service.Info
- **Services are state machines** — well-defined status transitions
- **TUI subscribes to state, doesn't own it** — supervisor is the source of truth
- **Process isolation** — each service runs in its own process group (Setpgid)
- **Restart logic is centralized** — in the supervisor runLoop, not scattered
- **Event-driven** — components communicate through the bus, not direct calls
