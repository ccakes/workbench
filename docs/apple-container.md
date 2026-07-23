# Apple Container backend

On Apple silicon, workbench can run container services on
[Apple's `container`](https://github.com/apple/container) tool instead of
Docker. Apple `container` runs each Linux container in its own lightweight
virtual machine and integrates directly with Virtualization.framework, avoiding
the Docker Desktop dependency.

The `bench.yaml` service schema is unchanged — the same `container:` block runs
on either backend. Only the global `container_backend` setting differs.

## Requirements

- Apple silicon Mac (arm64).
- macOS 26 (Tahoe) or later.
- The [`container`](https://github.com/apple/container) CLI installed and its
  system service running:
  ```bash
  container system start
  ```

## Selecting the backend

```yaml
global:
  container_backend: auto   # docker | apple | auto (default)
  apple:
    gateway_ip: 192.168.64.1
```

- **`auto`** (default) — use Apple `container` when running on Apple silicon with
  the `container` binary installed; otherwise use Docker.
- **`docker`** — always use Docker.
- **`apple`** — always use Apple `container`. Startup fails with a clear message
  if the host doesn't meet the requirements above.

The active backend is shown per container service in the TUI detail pane and in
`bench status` (the `TYPE` column reads `container/apple` or `container/docker`,
and `--json` includes a `backend` field).

## Host connectivity (tracing)

Docker exposes `host.docker.internal` so a container can reach services on the
host (workbench uses this for the OTLP trace collector). Apple `container` has
no such alias. Instead, containers reach the host at the vmnet **gateway IP**,
which defaults to `192.168.64.1`.

Workbench injects that gateway IP as the OTLP endpoint host for Apple-backend
container services automatically. If you've changed the `container` default
subnet (in `~/.config/container/config.toml`), set `apple.gateway_ip` to the
matching gateway address.

## Differences from Docker

- **Isolation** — one lightweight VM per container, rather than shared-kernel
  namespaces. Startup is slightly slower but isolation is stronger.
- **Exit codes** — `container` has no `wait` subcommand and does not reliably
  expose a container's process exit code via `inspect`. Workbench detects that a
  container has stopped by polling its status; the reported exit code is
  best-effort. This mainly affects `restart.policy: on-failure`, which may not
  distinguish a clean exit from a crash as precisely as it does on Docker.
- **Anonymous volumes** — Docker's `-v` removal flag drops anonymous volumes on
  cleanup. Apple `container` has no equivalent; anonymous volumes are not
  auto-removed.
- **Host networking / `--add-host`** — not used by workbench on this backend;
  host access goes through the gateway IP instead.

## Images without an arm64 variant

Apple `container` runs images for the host architecture (arm64), using Rosetta
to run amd64 images where possible. An image with **no usable variant** for the
host — e.g. an amd64-only image whose binaries Rosetta can't run — fails at
start with `does not support required platforms` or `exec format error`.

Workbench treats this as a **terminal failure**: the service goes straight to
`failed` with a clear message (`image does not support this platform: …`) and is
*not* retried, even under `restart.policy: always`. Retrying can never succeed,
so looping would only bury the real reason in noise. Rebuild or source a
multi-arch (arm64) image to fix it.

## Out of scope

- Building images (`container build`) — workbench only runs pre-built images.
- `container system dns` domains — workbench uses the gateway IP so it never
  needs `sudo` or to disable iCloud Private Relay.
- Starting the `container` system service — workbench reports if it's not
  running but does not run `container system start` for you.
