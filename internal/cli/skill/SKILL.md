---
name: workbench
description: >
  Manage the local dev environment using the bench CLI. Use when the user asks
  about service status, logs, starting/stopping/restarting services, or
  diagnosing issues with their running dev environment. Also use when the user
  says "bench", "workbench", "services", or asks about processes/containers.
bench-version: {{BENCH_VERSION}}
user-invocable: false
allowed-tools: Bash(bench *)
---

# workbench — Dev Environment Management

`bench` is a TUI process orchestrator that runs the user's local dev stack
from a `bench.yml` config. A `bench up` instance must already be running for
control commands to work — they talk to it over a Unix socket.

## First: check the skill is current

This skill was generated for `bench` version **{{BENCH_VERSION}}**. Before
acting on anything below, run:

```bash
bench version
```

If the version printed differs from `{{BENCH_VERSION}}` above, the skill is
stale — features, flags, or status fields may have moved. **Tell the user:**

> Your installed `bench` is version X but my workbench skill is for version Y.
> Run `bench agent-skill` and re-save it to get the up-to-date guidance.

Then proceed with caution: prefer `bench <command> --help` over anything in
this skill, since `--help` is generated from the running binary.

## Common commands

```bash
bench status -json              # parseable snapshot of every service
bench status --why              # human table with REASON column always shown
bench logs <service> -last 200  # recent logs for a service
bench logs                      # multi-service tail (all services, [svc] prefix)
bench logs --grep ERROR --since 5m   # filter by regex + time window
bench start|stop|restart <service> [more services...]
bench wait [<service>...] --timeout 5m  # block until ready (0=ok 1=fail 2=timeout)
bench up [<service>...] [--profile <name>...]  # start subset or profile set
bench reload                    # apply config changes in-place
bench clean [--dry-run] [--force]  # remove stale socket + matching containers
bench validate                  # check bench.yml without starting anything
```

All subcommands accept `--config <path>` and `--socket <path>` (or
`BENCH_SOCKET` env var). Positional service names can appear before or after
flags.

## Finding a running instance (agents read this)

Control commands talk to `bench up` over a Unix socket whose directory comes
from `$TMPDIR`. Agents (including this one) usually run with a **different
`$TMPDIR`** than the user's login shell that started `bench up`, so the naive
socket path won't match where the server actually put it.

You normally don't need to do anything: `status`, `logs`, `start`, etc.
**auto-discover** the socket by searching the common temp roots (`$TMPDIR`,
`/tmp`, `/var/folders/*/*/T`), so they just work across this mismatch.

If a command still reports *"no bench up is running"* but the user says one is,
resolve the socket explicitly and pass it along:

```bash
bench socket                         # prints the live socket path, or exits 1
bench --socket "$(bench socket)" status -json
# or export it once for the session:
export BENCH_SOCKET="$(bench socket)"
```

`bench socket` is the fallback. If even that misses (an unusual `$TMPDIR`),
locate it directly — the filename is stable per config:

```bash
find /tmp /var/folders -name 'bench-*.sock' 2>/dev/null
```

A "configured" status for **every** service is the tell-tale sign of a missed
connection: that string only appears in the config-only fallback, never from a
live server.

## Status JSON: useful fields

`key`, `status`, `type`, `pid`, `uptime`, `restart_count`, `exit_code`,
`last_error`, `last_log_line`, `last_log_stream`, `watch_enabled`, `ports`.

`last_log_line` is the most recent buffered log line for the service —
useful for one-call health checks without a follow-up `bench logs` round
trip.

## Discovering more

Anything not covered here, find via `--help`:

```bash
bench --help
bench <command> --help
```

`--help` is the authoritative source — this skill is a fast-path for the
common stuff, not an exhaustive reference.

## Notes worth knowing

- Services are either processes (`command:`) or containers (`container:`);
  containers need Docker running.
- Status flow: `pending → starting → running → [setup →] ready`. The optional
  `setup` step runs a host `exec` or service `container_exec` bootstrap command
  after the readiness probe passes; dependents wait for `ready`.
- Readiness probe kinds: `tcp`, `http`, `log_pattern`, `exec`, `container_exec`,
  `grpc`. Probe stdout/stderr appears in the service log buffer tagged with
  stream `probe`. `exec` runs on the host; `container_exec` runs inside the
  service's own container and is the portable way to probe a container (it
  supplies the container name and the backend CLI, so it needs no `docker exec`
  prefix and works on both container backends).
- Log buffers are ring buffers — old lines rotate out.
- Unknown YAML fields are rejected; a typo like `expect_status` under
  `readiness:` fails validation rather than silently being ignored.
- File watchers may auto-restart services on code changes (`watch_enabled`
  field in status); when set, manual `bench restart` is rarely needed.
- `${VAR}` is expanded only inside `env:` and `env_file:` values — not in
  commands or readiness fields. Service inline env lookup prefers shell env,
  then same-service `env`, service `env_file`, `global.env`, and
  `global.env_file`.
- `profiles: [name]` per service + `bench up --profile <name>` to filter
  what gets started. Services without profiles are always-on.
