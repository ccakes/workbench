# Changelog

All notable changes to this project are documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

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
