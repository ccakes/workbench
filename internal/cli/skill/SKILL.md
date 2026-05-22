---
name: workbench
description: >
  Manage the local dev environment using the bench CLI. Use when the user asks
  about service status, logs, starting/stopping/restarting services, or
  diagnosing issues with their running dev environment. Also use when the user
  says "bench", "workbench", "services", or asks about processes/containers.
user-invocable: false
allowed-tools: Bash(bench *)
---

# workbench — Dev Environment Management

You can manage the user's local development environment through the `bench` CLI.
A `bench up` instance must already be running for control commands to work.

## Commands

### Check service status

```bash
bench status -json              # all services, parseable
bench status -json <service>    # single service
bench status --why              # human table with REASON column shown for every service
```

Key fields: `key`, `status`, `type`, `pid`, `uptime`, `exit_code`, `restart_count`,
`last_error`, `last_log_line`, `last_log_stream`, `watch_enabled`, `ports`.

The human-readable table always shows a REASON column. For healthy services it is
empty; for `failed`, `backoff`, or `restarting` services it shows the first line
of `last_error`. Pass `--why` to surface the column for every service.

### Read logs

```bash
bench logs <service>              # last 100 lines
bench logs <service> -last 500    # last 500 lines
```

### Start / stop / restart

```bash
bench start <service> [service2 ...]
bench stop <service> [service2 ...]
bench restart <service> [service2 ...]
```

### Validate config

```bash
bench validate
bench validate -config other.yml
```

## Overrides

All subcommands accept `--config <path>` (default: `bench.yml`) and
`--socket <path>` (or `BENCH_SOCKET` env var) to target a specific instance.
Flags can appear before or after positional service names — e.g. both
`bench restart api --config X` and `bench restart --config X api` work.

## Typical workflows

**Diagnose a failing service:**

1. `bench status -json` — find unhealthy services (non-running status, high restart count, `last_error` set). `last_log_line` carries the most recent buffered log line so you may not need to fetch logs.
2. `bench logs <service> -last 200` — read recent output for errors when `last_log_line` is not enough
3. Fix the code
4. `bench restart <service>`

**Check environment health:**

Run `bench status -json` and verify all services show `"status": "running"`.
Report any with errors or unexpected restart counts.

**Restart after code change:**

If file watching is enabled (`watch_enabled: true`), restarts are automatic.
Otherwise run `bench restart <service>`.

## Important notes

- Service keys match the keys in `bench.yml` (e.g. `api-gateway`, `megalith`, `portal`, `megadb`)
- Services are either processes (`command:`) or containers (`container:`)
- Container services require Docker to be running
- Always use `-json` flag on `bench status` when you need to parse the output
- Log buffers are ring buffers — very old logs may have rotated out
- There is no multi-service log stream; fetch logs per service separately
- Unknown YAML fields are rejected by `bench validate` and `bench up`. A typo like `expect_status` under `readiness:` will fail loudly rather than silently doing nothing
