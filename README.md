# bench

> `bench` is a YAML-native terminal UI for running, watching, and supervising local development services, with live process state and integrated logs.

## Features

- **YAML configuration** — define services declaratively, no Procfiles or shell scripts
- **Per-service file watching** — automatic restarts on source changes with include/ignore patterns and debounce
- **Rich TUI** — service list, live status, detail pane, and integrated logs in one terminal
- **Process supervision** — restart policies (never, on-failure, always), backoff, max retries
- **Independent service control** — start, stop, restart any service without affecting others
- **Environment management** — inline env vars and .env file loading per service
- **Dependency ordering** — services start in the right order based on `depends_on`
- **CLI mode** — non-interactive commands for scripting and automation

## Install

```bash
go install github.com/ccakes/bench/cmd/bench@latest
```

Or build from source:

```bash
git clone https://github.com/ccakes/bench.git
cd bench
go build -o bench ./cmd/bench
```

## Quick Start

Create a `bench.yml` in your project root:

```yaml
version: 1

services:
  api:
    dir: ./services/api
    command: go run ./cmd/api
    env:
      PORT: "8080"
    restart:
      policy: on-failure
    watch:
      enabled: true
      include:
        - "**/*.go"

  web:
    dir: ./services/web
    command: npm run dev
    restart:
      policy: always
```

Run it:

```bash
bench
```

This starts all services and opens the TUI. Use `bench --no-tui` for headless mode.

## Configuration

See [docs/configuration.md](docs/configuration.md) for the full reference.

### Minimal example

```yaml
version: 1

services:
  myapp:
    dir: .
    command: go run .
```

### Watch-on-change example

```yaml
version: 1

global:
  watch_debounce: 500ms

services:
  api:
    dir: ./api
    command: go run .
    restart:
      policy: on-failure
    watch:
      enabled: true
      include:
        - "**/*.go"
        - "**/*.yaml"
      ignore:
        - "**/testdata/**"
```

When a `.go` or `.yaml` file changes under `./api`, only the `api` service restarts. Other services are unaffected.

## Commands

| Command | Description |
|---------|-------------|
| `bench` or `bench up` | Start services and open TUI |
| `bench start <svc...>` | Start specific services |
| `bench stop <svc...>` | Stop specific services |
| `bench restart <svc...>` | Restart specific services |
| `bench status` | Show service status table |
| `bench logs [svc]` | Stream service logs |
| `bench validate` | Validate config without running |
| `bench version` | Show version |

### Global flags

| Flag | Description |
|------|-------------|
| `--config <path>` | Path to config file (default: auto-discover `bench.yml`) |
| `--no-tui` | Run without TUI (headless mode) |
| `--no-watch` | Disable file watching |
| `--verbose` | Verbose output |

## TUI Keyboard Shortcuts

| Key | Action |
|-----|--------|
| `j` / `k` | Navigate services / scroll logs |
| `Tab` | Switch between service list and log pane |
| `r` | Restart selected service |
| `s` | Stop selected service |
| `S` | Start selected service |
| `w` | Toggle file watch for selected service |
| `f` | Toggle log follow mode |
| `c` | Clear log pane |
| `a` | Toggle all-services log view |
| `g` / `G` | Scroll to top / bottom of logs |
| `/` | Search/filter logs |
| `?` | Show help |
| `q` | Quit |

## Comparison with Procfile tools

| Feature | bench | Overmind | foreman |
|---------|-------|----------|---------|
| Config format | YAML | Procfile | Procfile |
| File watching | Built-in | No | No |
| TUI | Built-in | tmux-based | No |
| Per-service env files | Yes | Yes | Yes |
| Restart policies | Yes | No | No |
| Dependency ordering | Yes | No | No |
| Working directory per service | Yes | No | No |

## Platform Support

- macOS
- Linux

## License

MIT
