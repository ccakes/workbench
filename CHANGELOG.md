# Changelog

All notable changes to this project are documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [Unreleased]

### Fixed

- Starting a session no longer fails with `spawning session daemon: open
  /tmp/bench-<uid>/...log: no such file or directory` when the per-user socket
  directory is missing (e.g. after the OS reaps `/tmp`). The launcher now
  ensures the directory exists before writing the daemon log, instead of
  relying on the not-yet-started daemon to create it.

## [0.6.9] - 2026-06-15

### Fixed

- Profile gating now applies even when no `--profile` is given. Previously
  `bench up` with no profile launched every profile-tagged service (contrary to
  the documented "profile-less services only" default); now those services
  register as `disabled` and stay visible in the TUI and `bench status`, ready to
  start on demand with `bench start <service>`.

### Added

- `bench down` stops all services in a running session and shuts the session
  down, releasing the control socket. Useful for tearing everything down to start
  fresh — including sessions started in the background by an AI agent.
- Detachable sessions. `bench up --daemon` starts the session in the background
  (detached, no UI) and returns to the shell. Running `bench` with no subcommand
  now **attaches** an interactive TUI to the running session over the control
  socket — so you can "take over" a session an agent started headless, and detach
  again without stopping it. Only one TUI may be attached at a time.
- The TUI quit dialog now offers a third option, **background** (`b`): disconnect
  the UI but leave the session running. Available when attached to a detached
  session; quit otherwise stops all services (`s`).

### Changed

- Bare `bench` (no subcommand) now attaches to a running session instead of
  aliasing `bench up`. Use `bench up` for the classic foreground TUI, or
  `bench up --daemon` to start a background session.

## [0.6.8] - 2026-05-31

### Added

- Quit confirmation prompt in the TUI: pressing `q` or `Ctrl+C` now opens a
  "Quit workbench?" dialog so an accidental keypress no longer tears down all
  running services. Confirm with `y`/`Enter`, cancel with any other key.

### Fixed

- Container services now receive a reachable OTLP endpoint. The injected
  `OTEL_EXPORTER_OTLP_ENDPOINT` previously always used `localhost`, which inside
  a container is its own loopback — so container spans were silently dropped
  while host-process spans worked. Container services now get
  `http://host.docker.internal:<port>`, and each container run is given a
  `host.docker.internal:host-gateway` alias so this resolves on every Docker
  runtime.
- The trace collector now honours OTLP/HTTP request headers. It decompresses
  bodies sent with `Content-Encoding: gzip` (the default for several SDKs,
  including the Perl OTLP exporter) and decodes JSON payloads
  (`Content-Type: application/json`) in addition to protobuf. Previously a
  gzipped or JSON export was rejected with HTTP 400 and the spans were lost.

## [0.6.7] - 2026-05-30

### Added

- `bench socket` command prints the control socket path of a running instance,
  searching the common temp roots so it works even when the caller's `$TMPDIR`
  differs from the one that started `bench up`.

### Fixed

- Container services no longer leak anonymous Docker volumes. `docker rm` is now
  invoked with `-v`, so volumes created by images with a `VOLUME` directive
  (Postgres, Cassandra, etc.) are removed alongside the container on every stop
  and restart instead of accumulating as dangling volumes that consume disk.
- Control commands (`status`, `logs`, `start`, etc.) now auto-discover the
  running instance's socket across temp directories. Previously, when a command
  ran with a different `$TMPDIR` than `bench up` (common for agents that set
  their own `$TMPDIR`), it silently failed to connect and `bench status` showed
  `configured` for every service.

## [0.6.6] 2026-05-29

### Added

- Horizontal scrolling in the log pane (`h`/`l` or arrow keys) so log lines wider
  than the pane can be read instead of being truncated.
- Page scrolling in the log pane with `Ctrl+D`/`Ctrl+U` (and `PageDown`/`PageUp`).

### Fixed

- Log pane vertical scrolling direction was reversed: `j`/`k` (and the arrow keys)
  now scroll down toward the newest output and up toward the oldest, matching their
  labels. Scrolling also clamps correctly at both ends, and reaching the bottom
  re-enables follow mode.
- Injected OTEL tracing defaults (`OTEL_EXPORTER_OTLP_ENDPOINT`/`OTEL_EXPORTER_OTLP_PROTOCOL`)
  no longer override values set in `global.env_file`, a service `env_file`, or the OS
  environment. The presence check used incorrect string lengths and never matched, so
  the defaults clobbered those layers; they are now correctly treated as the
  lowest-precedence source.

## [0.6.4] - 2026-05-28

### Fixed

- Global flags (`--config`, `--socket`) now work when placed before the
  subcommand — e.g. `bench --config FILE logs --follow`. Previously only the
  trailing form (`bench logs --config FILE`) was accepted; the leading form
  failed with "unknown command: --config".
- `${VAR}` interpolation in inline `env:` values now sees keys defined in
  `global.env_file` (and per-service `env_file`). Previously the
  substitution source was only the parent shell, so a reference like
  `DATABASE_URL: "postgres://${MEGADB_USER}@..."` expanded to an empty
  user even when `MEGADB_USER` was set in the env_file — and the empty
  inline value then clobbered the env_file value at service start, since
  inline env wins last in `supervisor.buildEnv`. Interpolation now falls
  back from shell env to same-scope inline env, service env_file, global
  inline env, and global env_file.

## [0.6.0] - 2026-05-23

### Added

- Config composition via the top-level `extends:` directive. A config file can
  declare `extends: path/to/parent.yml` to inherit every service and global
  setting from a parent file. Chains are allowed; cycles are rejected. Service
  name conflicts between child and parent fail loading. See
  `docs/configuration.md#composition-with-extends`.
- `bench status` table now carries a `REASON` column. By default it shows the
  first line of `last_error` only for services in a problem state
  (`failed`/`backoff`/`restarting`); pass `--why` to surface it for every row.
- `last_log_line` and `last_log_stream` fields on `bench status -json`,
  populated from the most recent buffered log line. Useful for monitoring
  scripts that previously had to shell out to `bench logs`.
- `readiness.kind: exec` — runs an arbitrary command and treats exit 0 as
  ready. Covers cases TCP can't: cassandra auth coming up after the port
  opens, postgres finishing recovery, etc. Probe stdout/stderr is appended to
  the service's log buffer tagged with stream `probe`.
- `readiness.settle` — delay between probe-success and the Ready transition,
  for services where the readiness signal arrives just before the service is
  really usable.
- `readiness.interval` — per-service inter-attempt sleep (defaults to 500ms;
  applies to tcp/http/exec).
- `readiness.max_attempts` — cap on retries before the supervisor gives up on
  the probe, stops the service, and marks it failed (default 0 = unlimited,
  matching previous behaviour).
- `setup:` hook per service: a command that runs once the readiness probe
  passes and before dependents are unblocked. Useful for in-band bootstrap
  steps (creating dev users, seeding flag environments) that previously had
  to be wrapped into another service's command. Status flow becomes
  `Running → Setup → Ready`; non-zero exit or timeout fails the service.
- `bench up <service> [<service2>...]` — start only the named services and
  their transitive depends_on closure. Services outside the closure stay
  disabled even if their config says `auto_start: true`.
- `readiness.kind: grpc` — issues a standard gRPC health-check call
  (`grpc.health.v1.Health/Check`). Optional `service` field picks a named
  gRPC service; empty probes overall server health.
- `profiles: [...]` per service + `bench up --profile <name>` (repeatable).
  Matches docker-compose semantics: services with no profiles are always
  launched; profile-tagged services launch only when one of their profiles
  is active. Transitive deps of started services are pulled in regardless of
  profile.
- `group: <name>` field on services (display-only). The TUI groups the
  service list by `group` so long stacks scan more easily. No runtime effect.
- `bench wait [services...] [--timeout 5m]` — block until target services
  hit `ready`. No args waits for every auto-started service. Exit codes:
  0 = all ready, 1 = at least one failed/stopped, 2 = timeout. Useful as a
  single CI gate for "is the stack up?".
- `bench clean [--force] [--dry-run]` — remove a stale socket and any
  bench-managed containers whose name starts with the config's
  `container_prefix`.
  Refuses to run if a bench is currently alive on the socket unless
  `--force` is passed.
- `bench logs` (no service) — multi-service tail with `[service]` prefixes,
  fan-in across all service log buffers, sorted chronologically.
- `bench logs --grep <regex>` — client-side filter on the streamed lines.
- `bench logs --since <duration>` — server-side time window (e.g. `5m`).
- Environment variable interpolation: `${VAR}` and `$VAR` are expanded in
  `env`/`env_file` fields only. Commands, regex patterns, URLs are left
  alone so existing `$` literals don't break.
- `bench reload` — re-read the config and restart services whose service
  config changed (command, container, env, readiness, setup). Unchanged
  services are not disturbed. Added/removed services are reported but not
  acted on; a full `bench up` is still required to pick up structural
  changes. Reports per-service outcome on stdout.

### Changed

- Control sockets derived from config paths now live under a per-user private
  temp directory (`0700`) instead of directly under `/tmp`, preventing other
  local users from opening the default control socket.
- The embedded agent skill (`bench agent-skill`) is now stamped with the
  binary's version at print/save time. The skill instructs the agent to
  compare the stamped version against the live `bench version` and prompt
  the user to refresh if stale. Skill body was also slimmed down — it now
  explains the tool's nature and points at `bench <cmd> --help` rather than
  enumerating every flag.
- **Breaking:** unknown YAML fields in `bench.yml` are now rejected at parse
  time. A typo like `expect_status: 200` under a `readiness:` block fails
  validation instead of being silently ignored. Run `bench validate` against
  any existing configs before upgrading.
- `bench clean` only removes containers labelled `managed-by=bench` and still
  matching the config's container-name prefix.
- `bench reload` refreshes changed service metadata such as display name,
  watch state, container image, and ports in addition to restarting changed
  service configs.
- `bench wait` can now wait for the configured timeout instead of being capped
  by the generic 10-second control-client deadline.
- `env_file` path interpolation now happens before relative path resolution, so
  paths such as `$HOME/.env` resolve to the intended absolute file.
- `bench start`, `stop`, `restart`, and `status` now accept positional service
  names before or after flags (e.g. `bench restart api --config X` and
  `bench restart --config X api` both work). Previously only flag-first
  ordering parsed `--config` correctly.
- The "no running bench instance" error now reports the resolved config path,
  the expected socket path, and the exact command to start a bench for that
  config — instead of leaking only the raw `dial unix …` failure.

## [0.5.0] - 2026-04-19

### Added

- Runtime readiness probes. Services with `readiness.kind` of `log_pattern`,
  `tcp`, or `http` now transition to **ready** once the probe succeeds.
  Services without a probe are promoted to **ready** as soon as the process is
  up, so **ready** is the uniform "good to go" state across all services.

### Fixed

- `depends_on` now actually blocks dependent services from starting until
  their dependencies reach **ready**. Previously it only influenced
  topological sort order, so every service transitioned to running almost
  simultaneously. If a dependency fails, dependents now cascade to failed
  instead of running without their prerequisites.

## [0.4.0] - 2026-04-19

### Added

- `bench import-compose` subcommand for converting Docker Compose files into
  `bench.yml`.
- Embedded Claude Skill, exposed via the `bench agent-skill` subcommand which
  prints the skill and offers to install it into detected agent tools (Claude
  Code, Codex, Gemini Code Assist, OpenCode).
- Global `env` map merged into per-service environments (per-service entries
  win on conflict).

### Fixed

- `bench logs --last` is now respected when placed after the service name.
- Service stop now reliably terminates escaped descendant processes (e.g. Java
  processes spawned by Gradle daemons that detach into their own session).

### Changed

- Service-list columns reordered so status stays right-aligned.

### Removed

- The previous `install-skill` subcommand. Use `agent-skill` instead.

## [0.3.0] - 2026-03-09

### Added

- Unix socket control API. `bench` CLI subcommands (`start`, `stop`, `restart`,
  `status`, `logs`) now talk to a running `bench up` instance over a Unix
  domain socket instead of running standalone supervisors. The socket path is
  derived from the config file path so clients auto-discover the server.

## [0.2.1] - 2026-03-08

### Changed

- `bench trace` is now listed in `--help`; tracing documentation expanded.

## [0.2.0] - 2026-03-08

### Added

- Embedded OTLP trace collector with a TUI trace browser (list, detail,
  waterfall, and service-map views). Opt-in via `global.tracing` in config.

### Changed

- **Breaking:** project renamed to **workbench**; Go module path moved from
  `github.com/ccakes/bench` to `github.com/ccakes/workbench`. The binary is
  still `bench`.

## [0.1.0] - 2026-03-08

Initial release.

### Added

- TUI process orchestrator with split-pane layout, per-service supervision,
  restart policies, log ring buffer, file-watching with debounce/glob matching,
  headless mode, and YAML configuration.
- Container-based services alongside processes.
