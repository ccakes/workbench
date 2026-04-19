# Configuration Reference

workbench uses a YAML configuration file, by default `bench.yml` in the current or parent directory.

## Config discovery

1. Explicit `--config <path>` flag
2. `bench.yml` or `bench.yaml` in the current directory
3. Walk parent directories until one is found

## Schema

### Root

| Field | Type | Required | Description |
|-------|------|----------|-------------|
| `version` | integer | yes | Config version, must be `1` |
| `global` | object | no | Global settings |
| `services` | map | yes | Service definitions (key = service ID) |

### Global

| Field | Type | Default | Description |
|-------|------|---------|-------------|
| `shutdown_timeout` | duration | `10s` | Time to wait for graceful stop before SIGKILL |
| `log_buffer_lines` | integer | `5000` | Max log lines kept per service |
| `watch_debounce` | duration | `300ms` | Default debounce for file watchers |
| `env` | map | | Global environment variables applied to all services |
| `env_file` | path | | Global .env file loaded for all services |
| `container_prefix` | string | dirname | Prefix for Docker container names (e.g. `{prefix}-{service}`) |
| `tracing` | object | | Tracing configuration |

#### Tracing

| Field | Type | Default | Description |
|-------|------|---------|-------------|
| `enabled` | bool | `false` | Enable embedded OTLP trace collector |
| `port` | integer | `4318` | HTTP port for the OTLP collector |
| `buffer_size` | byte size | `500MB` | Max memory for stored spans |

When enabled, workbench starts an OTLP HTTP collector on the configured port. Services that export traces to `http://localhost:<port>/v1/traces` will have their spans captured and viewable in the TUI trace browser (press `t`).

Byte sizes use human-readable format: `100MB`, `1GB`, etc.

### Service

| Field | Type | Default | Description |
|-------|------|---------|-------------|
| `name` | string | key | Display name shown in TUI |
| `dir` | path | **required**\* | Working directory for the process |
| `command` | string or string[] | **required**\* | Command to execute |
| `container` | object | | Container configuration (see below) |
| `env` | map | | Inline environment variables |
| `env_file` | path | | Path to .env file |
| `auto_start` | bool | `true` | Start automatically with `bench up` |
| `depends_on` | string[] | | Services that must reach Running before this one starts (see [Dependency ordering](#dependency-ordering)) |
| `restart` | object | | Restart policy configuration |
| `watch` | object | | File watch configuration |
| `readiness` | object | | Readiness detection |
| `labels` | map | | Arbitrary key-value labels |
| `stop_signal` | string | `SIGTERM` | Signal sent on stop |
| `shutdown_timeout` | duration | global | Override global shutdown timeout |

\* `dir` and `command` are required for process-based services. For container-based services, use the `container` field instead.

A service is either **process-based** (has `command`) or **container-based** (has `container`). The two are mutually exclusive.

#### Command formats

String form (runs via `sh -c`):
```yaml
command: go run ./cmd/api
```

Array form (exec directly):
```yaml
command:
  - npm
  - run
  - dev
```

### Container

| Field | Type | Default | Description |
|-------|------|---------|-------------|
| `image` | string | **required** | Docker image to run |
| `ports` | string[] | | Port mappings (`host:container` or `host_ip:host:container`) |
| `volumes` | string[] | | Volume mounts (`host:container`). Relative host paths resolve from config file directory |
| `network` | string | | Docker network to connect to |
| `command` | string or string[] | | Override container entrypoint/command |

Container services are managed via Docker. workbench handles the full lifecycle: pulling, starting, log streaming, and cleanup. Environment variables from `env` and `env_file` are passed to the container via `-e` flags. Containers are named `{container_prefix}-{service_key}` — see the `container_prefix` global setting.

```yaml
services:
  postgres:
    container:
      image: postgres:16-alpine
      ports:
        - 127.0.0.1:5432:5432
      volumes:
        - ./pgdata:/var/lib/postgresql/data
    env:
      POSTGRES_USER: bench
      POSTGRES_PASSWORD: bench
      POSTGRES_DB: app
    restart:
      policy: always
    readiness:
      kind: tcp
      address: ":5432"
```

### Restart

| Field | Type | Default | Description |
|-------|------|---------|-------------|
| `policy` | string | `never` | `never`, `on-failure`, or `always` |
| `max_retries` | integer | unlimited | Max consecutive restart attempts |
| `backoff` | duration | `1s` | Delay between restarts |

**Policies:**
- `never` — process exits and stays stopped
- `on-failure` — restart only on non-zero exit code
- `always` — restart regardless of exit code

### Watch

| Field | Type | Default | Description |
|-------|------|---------|-------------|
| `enabled` | bool | `false` | Enable file watching |
| `paths` | string[] | `["."]` | Directories to watch (relative to service dir) |
| `include` | glob[] | | Only trigger on matching files |
| `ignore` | glob[] | | Skip matching files |
| `debounce` | duration | global | Debounce window for changes |
| `restart` | bool | `true` | Restart service on matching changes |

Glob patterns use doublestar syntax: `**/*.go`, `src/**/*.ts`, etc.

Common noisy directories (`.git`, `node_modules`, `__pycache__`) are always excluded from watching.

### Readiness

| Field | Type | Description |
|-------|------|-------------|
| `kind` | string | `none`, `log_pattern`, `tcp`, or `http` |
| `pattern` | string | Go regular expression matched against log lines (for `log_pattern`) |
| `address` | string | TCP address to dial, `host:port` (for `tcp`) |
| `url` | string | HTTP URL to GET; any 2xx response means ready (for `http`) |
| `timeout` | duration | Per-attempt probe timeout (default `2s`) |
| `initial_delay` | duration | Delay before the first probe attempt |

Every service transitions `Starting → Running → Ready`. Services without a
probe are promoted to **Ready** immediately once the process is up — so
**Ready** is the uniform "good to go" steady state. Services with a probe
configured stay in **Running** until the probe succeeds, then transition to
**Ready**. Probes retry indefinitely on failure; they never mark the service
as Failed. If a probe never succeeds, investigate the configuration — the
service will sit in Running with dependents parked in Pending.

- **`log_pattern`** scans each new stdout/stderr line against the regex,
  starting from lines emitted after the probe begins (so a stale match from a
  previous run cannot false-trigger on restart).
- **`tcp`** dials `address` with a `timeout` deadline per attempt. First
  successful connect wins.
- **`http`** issues `GET url` using an `http.Client` with `timeout`. Any 2xx
  response marks the service Ready.

## Dependency ordering

`depends_on` controls service startup order. A service with
`depends_on: [X, Y]` will not launch its process until both `X` and `Y` have
reached **Ready**. Since unprobed services reach Ready the instant their
process starts, this degrades to "wait for process up" for simple cases while
still giving probe-configured deps their full readiness semantics.

While a service is blocked on dependencies it shows **pending** with a
`waiting for: …` reason. Once every dependency is Ready, the service
proceeds to Starting.

Edge cases:

- **Dependency fails or stops before becoming Running** — the dependent is
  marked Failed and does not start. The failure reason references the dep.
- **Dependency has `auto_start: false`** — treated as opt-out; the dependent
  does *not* wait (otherwise it would deadlock). Manually starting the
  dependent via `bench start` will still wait for any still-Pending deps to
  become Running.
- **Dependency dies after the dependent is already Running** — the dependent
  keeps running. Restarts of the dependent do re-check dependencies.

Cycles are detected at config load time and produce a validation error.

## Environment variable loading

Environment variables are loaded in this order (later overrides earlier):

1. System environment
2. Global `env_file`
3. Global `env` (inline)
4. Service `env_file`
5. Service `env` (inline)

### .env file format

```env
# Comments start with #
KEY=value
QUOTED="value with spaces"
SINGLE='single quoted'
export EXPORTED=also works
```

## Duration format

Durations use Go duration syntax: `100ms`, `1s`, `2s500ms`, `1m`, `5m30s`, `1h`.

## Full example

```yaml
version: 1

global:
  shutdown_timeout: 10s
  log_buffer_lines: 5000
  watch_debounce: 300ms
  env:
    LOG_LEVEL: info
  env_file: .env
  container_prefix: myproject
  tracing:
    enabled: true
    port: 4318
    buffer_size: 500MB

services:
  api:
    name: API
    dir: ./services/api
    command: go run ./cmd/api
    env:
      PORT: "8080"
      LOG_LEVEL: debug
    env_file: .env.local
    auto_start: true
    restart:
      policy: on-failure
      max_retries: 10
      backoff: 2s
    watch:
      enabled: true
      paths:
        - .
      include:
        - "**/*.go"
        - "**/*.yaml"
      ignore:
        - "**/tmp/**"
      debounce: 500ms
    readiness:
      kind: log_pattern
      pattern: "server started"

  web:
    dir: ./services/web
    command:
      - npm
      - run
      - dev
    env_file: .env
    restart:
      policy: always
      backoff: 1s
    watch:
      enabled: true
      include:
        - "src/**"
        - "vite.config.*"
      ignore:
        - "dist/**"

  worker:
    dir: ./services/worker
    command: go run ./cmd/worker
    depends_on:
      - api
    auto_start: true
    restart:
      policy: on-failure
    watch:
      enabled: false

  # Container-based services
  postgres:
    container:
      image: postgres:16-alpine
      ports:
        - 127.0.0.1:5432:5432
      volumes:
        - ./pgdata:/var/lib/postgresql/data
    env:
      POSTGRES_USER: bench
      POSTGRES_PASSWORD: bench
      POSTGRES_DB: app
    restart:
      policy: always
    readiness:
      kind: tcp
      address: ":5432"

  redis:
    container:
      image: redis:7-alpine
      ports:
        - 127.0.0.1:6379:6379
    restart:
      policy: always
```
