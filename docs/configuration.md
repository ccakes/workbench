# Configuration Reference

workbench uses a YAML configuration file, by default `bench.yml` in the current or parent directory.

## Config discovery

1. Explicit `--config <path>` flag
2. `bench.yml` or `bench.yaml` in the current directory
3. Walk parent directories until one is found

## Environment variable interpolation

`${VAR}` and `$VAR` references are expanded **only** in the following fields:

- `global.env.*` values
- `global.env_file` path
- `services.<name>.env.*` values
- `services.<name>.env_file` path

Other string fields (`command`, `readiness.pattern`, `readiness.url`,
`container.image`, etc.) are left as-is, since `$` is common in shell
commands and regex patterns and expanding it would surprise users far more
than it helps.

For inline `env.*` values, variable lookup is resolved separately from final
runtime env loading. Higher-priority sources win. For service inline `env`
values, lookup uses this priority order:

1. Parent shell environment
2. Other values in that service's inline `env`
3. That service's `env_file`
4. `global.env`
5. `global.env_file`

Put another way, the service-level fallback chain is:
shell env → service `env` → service `env_file` → `global.env_file`, with
`global.env` also participating just before `global.env_file` when configured.

For `global.env` values, lookup uses the parent shell environment first, then
other `global.env` values, then `global.env_file`.

This means a value defined in `global.env_file` is visible to `${VAR}`
references in any service's inline env — you do not need to source the .env
file into your shell first — but a value exported in your shell overrides the
same key from config or env files.

For `env_file` _path_ fields, only the parent shell environment is consulted
(the file's own contents can't resolve its own path).

```yaml
services:
  api:
    env:
      DB_PASS: ${DATABASE_PASSWORD} # shell wins, then service/global env files
      TOKEN_PREFIX: "tok-${ENVIRONMENT}" # can refer to another inline env key
    command: "./bin/api --pw $DB_PASS" # NOT expanded; runs through the shell
```

If a referenced variable is unset in every layer, it expands to the empty
string (matching Go's `os.ExpandEnv`).

## Strict field validation

Unknown YAML fields are rejected at parse time. A typo such as `expect_status: 200` under a `readiness:` block produces:

```
error: parsing config bench.yml: parsing config: yaml: unmarshal errors:
  line 9: field expect_status not found in type config.ReadinessConfig
```

Run `bench validate` to surface these errors without starting any services.

## Schema

### Root

| Field      | Type    | Required | Description                                                                                |
| ---------- | ------- | -------- | ------------------------------------------------------------------------------------------ |
| `version`  | integer | yes      | Config version, must be `1`                                                                |
| `extends`  | path    | no       | Path to a parent config file to inherit from. See [Composition](#composition-with-extends) |
| `global`   | object  | no       | Global settings                                                                            |
| `services` | map     | yes      | Service definitions (key = service ID)                                                     |

### Global

| Field              | Type     | Default | Description                                                   |
| ------------------ | -------- | ------- | ------------------------------------------------------------- |
| `shutdown_timeout` | duration | `10s`   | Time to wait for graceful stop before SIGKILL                 |
| `log_buffer_lines` | integer  | `5000`  | Max log lines kept per service                                |
| `watch_debounce`   | duration | `300ms` | Default debounce for file watchers                            |
| `env`              | map      |         | Global environment variables applied to all services          |
| `env_file`         | path     |         | Global .env file loaded for all services                      |
| `container_prefix` | string   | dirname | Prefix for Docker container names (e.g. `{prefix}-{service}`) |
| `tracing`          | object   |         | Tracing configuration                                         |

#### Tracing

| Field         | Type      | Default | Description                          |
| ------------- | --------- | ------- | ------------------------------------ |
| `enabled`     | bool      | `false` | Enable embedded OTLP trace collector |
| `port`        | integer   | `4318`  | HTTP port for the OTLP collector     |
| `buffer_size` | byte size | `500MB` | Max memory for stored spans          |

When enabled, workbench starts an OTLP HTTP collector on the configured port. Services that export traces to `http://localhost:<port>/v1/traces` will have their spans captured and viewable in the TUI trace browser (press `t`).

The collector follows OTLP/HTTP content negotiation: it accepts protobuf
(`application/x-protobuf`) and JSON (`application/json`) payloads, and
decompresses bodies sent with `Content-Encoding: gzip`. Exporters may use their
default protocol and compression — no special configuration is required beyond
the endpoint.

Byte sizes use human-readable format: `100MB`, `1GB`, etc.

### Service

| Field              | Type               | Default        | Description                                                                                               |
| ------------------ | ------------------ | -------------- | --------------------------------------------------------------------------------------------------------- |
| `name`             | string             | key            | Display name shown in TUI                                                                                 |
| `dir`              | path               | **required**\* | Working directory for the process                                                                         |
| `command`          | string or string[] | **required**\* | Command to execute                                                                                        |
| `container`        | object             |                | Container configuration (see below)                                                                       |
| `env`              | map                |                | Inline environment variables                                                                              |
| `env_file`         | path               |                | Path to .env file                                                                                         |
| `auto_start`       | bool               | `true`         | Start automatically with `bench up`                                                                       |
| `depends_on`       | string[]           |                | Services that must reach Running before this one starts (see [Dependency ordering](#dependency-ordering)) |
| `restart`          | object             |                | Restart policy configuration                                                                              |
| `watch`            | object             |                | File watch configuration                                                                                  |
| `readiness`        | object             |                | Readiness detection                                                                                       |
| `labels`           | map                |                | Arbitrary key-value labels                                                                                |
| `stop_signal`      | string             | `SIGTERM`      | Signal sent on stop                                                                                       |
| `shutdown_timeout` | duration           | global         | Override global shutdown timeout                                                                          |

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

| Field     | Type               | Default      | Description                                                                              |
| --------- | ------------------ | ------------ | ---------------------------------------------------------------------------------------- |
| `image`   | string             | **required** | Docker image to run                                                                      |
| `ports`   | string[]           |              | Port mappings (`host:container` or `host_ip:host:container`)                             |
| `volumes` | string[]           |              | Volume mounts (`host:container`). Relative host paths resolve from config file directory |
| `network` | string             |              | Docker network to connect to                                                             |
| `command` | string or string[] |              | Override container entrypoint/command                                                    |

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

| Field         | Type     | Default   | Description                        |
| ------------- | -------- | --------- | ---------------------------------- |
| `policy`      | string   | `never`   | `never`, `on-failure`, or `always` |
| `max_retries` | integer  | unlimited | Max consecutive restart attempts   |
| `backoff`     | duration | `1s`      | Delay between restarts             |

**Policies:**

- `never` — process exits and stays stopped
- `on-failure` — restart only on non-zero exit code
- `always` — restart regardless of exit code

### Watch

| Field      | Type     | Default | Description                                    |
| ---------- | -------- | ------- | ---------------------------------------------- |
| `enabled`  | bool     | `false` | Enable file watching                           |
| `paths`    | string[] | `["."]` | Directories to watch (relative to service dir) |
| `include`  | glob[]   |         | Only trigger on matching files                 |
| `ignore`   | glob[]   |         | Skip matching files                            |
| `debounce` | duration | global  | Debounce window for changes                    |
| `restart`  | bool     | `true`  | Restart service on matching changes            |

Glob patterns use doublestar syntax: `**/*.go`, `src/**/*.ts`, etc.

Common noisy directories (`.git`, `node_modules`, `__pycache__`) are always excluded from watching.

### Readiness

| Field           | Type           | Description                                                                       |
| --------------- | -------------- | --------------------------------------------------------------------------------- |
| `kind`          | string         | `none`, `log_pattern`, `tcp`, `http`, `exec`, or `grpc`                           |
| `pattern`       | string         | Go regular expression matched against log lines (for `log_pattern`)               |
| `address`       | string         | TCP address to dial, `host:port` (for `tcp` and `grpc`)                           |
| `url`           | string         | HTTP URL to GET; any 2xx response means ready (for `http`)                        |
| `command`       | string or list | Shell command or argv to run (for `exec`); exit 0 = ready                         |
| `service`       | string         | gRPC service name (for `grpc`); empty = overall server health                     |
| `timeout`       | duration       | Per-attempt probe timeout (default `2s`)                                          |
| `initial_delay` | duration       | Delay before the first probe attempt                                              |
| `interval`      | duration       | Sleep between failed attempts (default `500ms`); applies to `tcp`, `http`, `exec` |
| `max_attempts`  | integer        | Cap on probe attempts before giving up (default `0` = unlimited)                  |
| `settle`        | duration       | Delay between probe-success and the Ready transition                              |

Every service transitions `Starting → Running → Ready`. Services without a
probe are promoted to **Ready** immediately once the process is up — so
**Ready** is the uniform "good to go" steady state. Services with a probe
configured stay in **Running** until the probe succeeds, then transition to
**Ready**. By default probes retry indefinitely on failure; set
`max_attempts` to cap retries and have the supervisor cancel the probe
when exhausted (dependents then cascade to Failed). If a probe never
succeeds and `max_attempts` is unset, the service will sit in Running with
dependents parked in Pending.

- **`log_pattern`** scans each new stdout/stderr line against the regex,
  starting from lines emitted after the probe begins (so a stale match from a
  previous run cannot false-trigger on restart).
- **`tcp`** dials `address` with a `timeout` deadline per attempt. First
  successful connect wins.
- **`http`** issues `GET url` using an `http.Client` with `timeout`. Any 2xx
  response marks the service Ready.
- **`exec`** runs `command` with a `timeout` deadline per attempt. Exit 0 = ready.
  stdout/stderr from the probe is appended to the service's log buffer tagged
  with stream `probe`, so you can see what the probe is observing.
- **`grpc`** issues a `grpc.health.v1.Health/Check` call against `address`. Ready
  when the server responds with status `SERVING`. Set `service` to probe a
  specific gRPC service registered for health reporting; leave it empty to
  probe overall server health. Useful for gRPC services that take time to
  finish initialising even after the listening port opens.

`settle` covers the case where TCP/HTTP comes up before the service is really
ready (think postgres opening the listening socket before recovery completes).
If set, the supervisor sleeps for `settle` after the probe passes before
marking the service Ready. Dependents do not unblock until the settle delay
elapses.

### Profiles

A service can declare `profiles: [name1, name2]`. By default, services with no
profile are always launched. Services with one or more profiles are only
launched when `bench up --profile <name>` activates at least one of them.

```yaml
services:
  postgres: {} # always-on
  flagman:
    profiles: [core]
  portal:
    profiles: [frontend]
```

```bash
bench up                    # postgres only (no profiles active)
bench up --profile core     # postgres + flagman
bench up --profile core --profile frontend  # postgres + flagman + portal
```

Profile-tagged services that no active profile selects are **not removed** — they
register as `disabled` and stay visible in the TUI and `bench status`. Start one
on demand with `bench start <service>` (or the `s` key in the TUI) without
restarting the session.

When `bench up <service>` is invoked with positional arguments, the explicit
service list wins — profile flags are ignored, and the named services + their
transitive deps come up regardless of profile.

### Group

`group: <name>` is a display-only tag used by the TUI to organise the service
list. It has no runtime behaviour. Services without a group are listed
unstructured.

### Setup hook

A service can declare a `setup:` block that runs once the readiness probe
passes (and after any `settle` delay), before the service transitions to
**Ready** and before any dependents are unblocked. Use this for per-service
bootstrap that's logically part of bringing this service up — creating a dev
environment in a flag service, seeding a default DB user, applying migrations.

| Field     | Type           | Description                                            |
| --------- | -------------- | ------------------------------------------------------ |
| `command` | string or list | Shell command or argv to run; exit 0 = setup succeeded |
| `timeout` | duration       | Cap on setup runtime (default `60s`)                   |
| `env`     | map            | Extra env applied on top of the service's env          |

```yaml
services:
  flagman:
    command: ./bin/flagman serve
    readiness:
      kind: http
      url: http://localhost:4242/health
    setup:
      command: ./bin/flagman create-env development
      timeout: 30s
```

The status flow is `Running → Setup → Ready`. On non-zero exit or timeout the
supervisor stops the service and marks it **Failed** with the setup error in
`last_error`, so dependents cascade just as they would for any other failure.
Setup stdout/stderr is appended to the service's log buffer tagged with
stream `setup` so you can see exactly what happened.

## Composition with `extends`

A config file can declare a single parent file via `extends:`. Every service and global setting from the parent is inherited; the child adds its own services and overrides on top.

```yaml
# bench/core.yml — shared infrastructure
version: 1
services:
  postgres:
    container:
      image: postgres:16
      ports: ["5432:5432"]
  elasticsearch:
    container:
      image: elasticsearch:8
      ports: ["9200:9200"]
```

```yaml
# forge.yml — depends on core
version: 1
extends: bench/core.yml
services:
  forge-api:
    dir: ./api
    command: "go run ./cmd/api"
  forge-web:
    dir: ./web
    command: "npm run dev"
```

`bench --config forge.yml` runs all four services together.

### Rules

- **Single parent.** `extends:` is a single path, not a list. Chains are allowed (`a.yml` extends `b.yml` extends `c.yml`); cycles are rejected at load time.
- **Path resolution is per-file.** `extends:` and any relative paths in a config (`dir`, `env_file`, container volumes) are resolved against the directory of _that_ file. A parent in `bench/core.yml` with `dir: ./svc` resolves to `bench/svc`, regardless of where the child config lives.
- **Service name conflicts are an error.** If both child and parent define a service with the same name, loading fails. To customise a parent service, change the parent or rename one of them.
- **Global `env`: per-key merge.** Parent env vars are inherited; the child can add keys or override individual ones.
- **Global scalars (`log_buffer_lines`, `shutdown_timeout`, `container_prefix`, …): child wins when set.** If the child omits a field, the parent's value is used; otherwise the default applies.
- **`tracing.enabled` is enable-only.** A child can turn tracing on but cannot turn off tracing the parent enabled. `tracing.port` and `tracing.buffer_size` follow normal child-wins rules.
- **`container_prefix` defaults to the entry-point file's directory name** (the file passed to `--config`), not the parent's.
- **Defaults are applied once, on the merged result.** Defaults never mask values inherited from a parent.

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
  does _not_ wait (otherwise it would deadlock). Manually starting the
  dependent via `bench start` will still wait for any still-Pending deps to
  become Running.
- **Dependency dies after the dependent is already Running** — the dependent
  keeps running. Restarts of the dependent do re-check dependencies.

Cycles are detected at config load time and produce a validation error.

## Environment variable loading

Environment variables are loaded in this order (later overrides earlier):

1. Injected tracing defaults (`OTEL_EXPORTER_OTLP_ENDPOINT`, `OTEL_EXPORTER_OTLP_PROTOCOL`)
2. System environment
3. Global `env_file`
4. Global `env` (inline)
5. Service `env_file`
6. Service `env` (inline)

The OTEL tracing defaults are the lowest-precedence source: when
`global.tracing.enabled` is true, workbench injects
variables but they can be overwritten by any other
layer — so a value from your shell, `global.env_file`, a service `env_file`, or
inline `env` always wins.

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
