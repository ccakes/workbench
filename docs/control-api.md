# Control API

`bench up` exposes a Unix domain socket that enables external tools and CLI subcommands to interact with the running instance.

## Socket Path

The socket path is derived deterministically from the config file's absolute path:

```
SHA256(abs_config_path)[:8] → /tmp/bench-<hash>.sock
```

This means `bench status` auto-discovers the running instance when using the same config file.

Override the socket path with:
- `--socket <path>` flag on any subcommand
- `BENCH_SOCKET` environment variable

## Protocol

Request-per-connection model. The client connects, sends one JSON request line, receives one JSON response line, then the connection closes.

**Request format:**
```json
{"method": "status", "params": {"service": "web"}}
```

**Success response:**
```json
{"ok": true, "data": [...]}
```

**Error response:**
```json
{"ok": false, "error": "unknown service \"foo\""}
```

## Methods

### ping

Health check. Returns the bench version.

```json
{"method": "ping"}
→ {"ok": true, "data": {"version": "0.1.0"}}
```

### status

Returns service snapshots. Without params, returns all services. With `service` param, returns a single service.

```json
{"method": "status"}
{"method": "status", "params": {"service": "web"}}
```

Response fields per service: `key`, `display_name`, `status`, `type`, `pid`, `container_id`, `image`, `uptime`, `exit_code`, `restart_count`, `last_restart`, `last_error`, `watch_enabled`, `ports`.

### start

Start a stopped service.

```json
{"method": "start", "params": {"service": "web"}}
```

### stop

Stop a running service.

```json
{"method": "stop", "params": {"service": "web"}}
```

### restart

Restart a service. Optional `reason` field for logging.

```json
{"method": "restart", "params": {"service": "web", "reason": "config change"}}
```

### logs

Fetch buffered log lines for a service. Default 100 lines. Each line includes a monotonic `seq` number. Use `after_seq` to fetch only lines newer than the given sequence — this supports polling without duplicates, even when multiple lines share the same timestamp.

```json
{"method": "logs", "params": {"service": "web", "last": 50}}
{"method": "logs", "params": {"service": "web", "last": 500, "after_seq": 42}}
```

Response lines include `timestamp`, `stream`, `text`, and `seq`.

### toggle-watch

Toggle file watching for a service.

```json
{"method": "toggle-watch", "params": {"service": "web"}}
→ {"ok": true, "data": {"watch_enabled": false}}
```

### traces

Get trace group summaries (requires tracing enabled). Default limit 50.

```json
{"method": "traces", "params": {"limit": 20}}
```

### spans

Get spans by trace ID or service name (requires tracing enabled).

```json
{"method": "spans", "params": {"trace_id": "abc123..."}}
{"method": "spans", "params": {"service": "web"}}
```

### service-map

Get the service interaction graph (requires tracing enabled).

```json
{"method": "service-map"}
```

## CLI Integration

The following subcommands connect to a running `bench up` instance via the socket:

| Command | Behavior |
|---------|----------|
| `bench status` | Shows live PID, uptime, restart counts. Falls back to config-only if no running instance. |
| `bench start <svc>` | Starts a service in the running instance. |
| `bench stop <svc>` | Stops a service in the running instance. |
| `bench restart <svc>` | Restarts a service in the running instance. |
| `bench logs <svc>` | Fetches buffered logs. Use `--follow` to poll for new lines. |

## Stale Socket Handling

On startup, if a socket file already exists:
- Try connecting to it
- If connection refused → stale socket, remove it and proceed
- If connection succeeds → another instance is running, exit with error
