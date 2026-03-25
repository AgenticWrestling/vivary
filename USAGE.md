# VIVARY Usage Guide

Current state: MVP runtime core (v0.1-dev). All commands below work today.
Multi-agent routing remains deferred; the CLI, TUI, and Nix/LXD packaging path are wired up.

---

## Prerequisites

- Go 1.24.2+
- [`task`](https://taskfile.dev) (optional but recommended)

Platform-specific requirements:

- Linux:
  - `systemd-nspawn` + `btrfs-progs` for keeperd-managed agent isolation
  - `lxc` / LXD for the `distro*` image build, import, launch, push, and connect workflows
  - `nix` with flakes enabled for `task distro` and `task distro:runtime`
- macOS / WSL2:
  - CLI and daemon development work, builds, and most tests work
  - Ward runs as a plain subprocess; Linux isolation features are not enforced
  - `systemd-nspawn`, nftables isolation, and the LXD runtime image workflow are Linux-only

---

## Build

```sh
# All binaries into ./bin
task build

# Or individually
task build:keeperd
task build:ward
task build:viv
task build:vivlog
task build:capwrap

# Or with go directly
go build -o bin/keeperd ./cmd/keeperd
go build -o bin/ward    ./cmd/ward
go build -o bin/viv     ./cmd/viv
go build -o bin/vivlog  ./cmd/vivlog
go build -o bin/capwrap ./cmd/capwrap
go build -o bin/vivgen  ./cmd/vivgen
```

---

## Tests

```sh
task test
# or
go test ./...
```

Passing test suites:

| Suite | Package |
|---|---|
| `TestMUSCodecRoundTrip` | `internal/switchboard` |
| `TestReadWriteFrame` | `internal/switchboard` |
| `TestUnmarshalMUS_TruncatedHeader` | `internal/switchboard` |
| `TestUnmarshalMUS_PayloadTooLarge` | `internal/switchboard` |
| `FuzzUnmarshalMUS` (seed corpus) | `internal/switchboard` |
| `TestCapabilityACL_*` | `internal/capabilities` |
| `TestFilesystemWrite_*` | `internal/capabilities` |
| `TestBrowserPageRead_URLScope` | `internal/capabilities` |
| `TestVivaryLog_*` | `internal/audit` |
| `TestCompletionEventRoundTrip` | `internal/audit` |
| `TestWardLoop_*` | `cmd/ward` |

---

## LXD / distrobuild

This section is Linux-only. It assumes:

- `lxc` is installed and connected to a working LXD daemon
- `nix` is installed with flakes enabled
- `/usr/bin/google-chrome-beta` exists on the host for browser-backed agent sessions
- `chromed@.service` is installed on the host (`task distro:chromed:install`)

If the host unit is not installed, `scripts/distro-lxd.sh launch` falls back to
running a user-managed `chromed` process from `/usr/local/bin/chromed` or
`./bin/chromed`, with state under `./.vivary/chromed/<container>/`.

The repo ships two LXD-importable Nix images:

- `vivary-base`: minimal immutable-ish OS image, no VIVARY binaries
- `vivary-runtime`: base image plus VIVARY runtime binaries

Build/import them with:

```sh
# Build tarballs only
task distro
task distro:runtime

# Build and import into LXD
task distro:import
task distro:import:runtime

# Install the host-side chromed service and unit
task distro:chromed:install

# Launch the default runtime image into the default container name
task distro:launch

# Inspect the matching host chromed instance
task distro:chromed:status

# Or launch explicitly
./scripts/distro-lxd.sh launch vivary-runtime vivary
```

`task distro:launch` now does three things together:

- launches the LXD container
- starts the matching host `chromed@<container>.service` systemd unit, or falls
  back to a user-managed `chromed` process when the unit is unavailable
- bind-mounts the host chromed runtime directory into the container at `/run/vivary/chromed-host`

That mount exposes the host-side MUS socket at `/run/vivary/chromed-host/chromed.sock`, which is how `keeperd` asks `chromed` to create or release per-agent Chrome sessions.

Chrome sessions stay headless in the shipped path, so you should not expect a
visible desktop Chrome window or profile switcher to pop up on the host while
browser e2e tests run. Host-side profile state is still created under the
matching `chromed` profile root, one directory per agent ID.

Inside the runtime container:

- `keeperd`, `viv`, `vivlog` are in PATH
- helper binaries live at:
  - `/usr/lib/vivary/ward`
  - `/usr/lib/vivary/capwrap`

Inside each agent rootfs, keeperd bind-mounts/readies the helper binaries at simple LSB-style paths:

- `/usr/bin/ward`
- `/usr/bin/capwrap`
- `/usr/bin/<CapabilityName>` symlinked to `/usr/bin/capwrap`

For developer iteration against an existing runtime container, you can rebuild and push updated binaries into the default container (`vivary`):

```sh
task distro:push

# Or target a different running container
task distro:push CONTAINER=my-vivary-dev

# Push and restart keeperd inside the default container
task distro:push:restart

# Launch the TUI against keeperd inside the default container
task distro:connect

# Or just restart keeperd inside a running container
task distro:restart:keeperd CONTAINER=my-vivary-dev

# Or launch the TUI in a different running container
task distro:connect CONTAINER=my-vivary-dev
```

`distro:push` is a dev convenience and mutates the running container. The cleaner full-image path is still to rebuild/import/launch a fresh `vivary-runtime` image.

`distro:push` also installs override binaries in `/usr/local/bin` inside the
container. `keeperd` prefers those paths for `keeperd`, `ward`, and `capwrap`
when present, so local dev pushes can override the immutable image copies.

`distro:restart:keeperd` starts `keeperd` in the container with `--workspace /var/lib/vivary/workspace` by default and writes logs to `/var/log/vivary/keeperd.log`. Override with `KEEPERD_WORKSPACE=/some/path` if your container uses a different workspace.

`distro:connect` uses `lxc exec` from the host to launch `viv tui` inside the running container against `$KEEPERD_WORKSPACE/keeper.sock` (default `/var/lib/vivary/workspace/keeper.sock`).

`task distro:stop` and `task distro:delete` stop the matching host `chromed@<container>.service` unit as part of the same lifecycle.

To exercise the live browser mediation path against a running LXD container,
use:

```sh
task distro:test:browser-e2e CONTAINER=vivary-static
```

That task provisions one allow-path agent and one deny-path agent, drives both
through the LXD + `chromed` path, and asserts that only the allow-path agent
acquires a live browser session.

---

## Running keeperd

`keeperd` is the policy daemon.  It owns the control socket and the audit log.

```sh
# Start in the current directory (creates keeper.sock and audit.db here)
bin/keeperd --workspace .

# Or point at an explicit workspace
bin/keeperd --workspace /var/lib/vivary/workspace
```

keeperd reads `orchestrator.kdl` from the workspace root if present.
All fields are optional; defaults are shown:

```kdl
// orchestrator.kdl
socket-path    "./keeper.sock"
audit-db       "./audit.db"
vault-path     "./vault.enc"
chromed-socket-path "/run/vivary/chromed-host/chromed.sock"
chrome-proxy-server ""          // optional advertised keeper proxy host/IP; empty = auto-detect container IPv4
max-pipe-bytes-per-sec 1048576
log-level      "info"   // debug | info | warn | error
providers-file "./providers.kdl"
```

For host-browser mode:

- `keeperd` talks to the host-side `chromed` service over the MUS socket at `chromed-socket-path`
- on first browser use for an agent, `keeperd` requests a dedicated Chrome session from `chromed`
- `chromed` creates a per-agent profile directory on the host and starts `/usr/bin/google-chrome-beta` with that profile
- `keeperd` starts a per-agent HTTP proxy inside the container and passes its URL to `chromed` as Chrome's `--proxy-server`
- the proxy binds on the container network address, not `127.0.0.1`, because host Chrome cannot reach container loopback
- the default proxy port range is `8700-8800`; if a port is already taken, `keeperd` uses the next free port in the range
- if `chrome-proxy-server` is empty, `keeperd` advertises the first non-loopback container IPv4 address automatically

This means browser traffic stays mediated by `keeperd` even though Chrome itself runs on the host OS.

keeperd also reads `providers.kdl` (see `providers.kdl` at the project root for
the built-in registry).  Each provider entry maps a name to a canonical API URL:

```kdl
// providers.kdl
provider "anthropic" {
    api-url     "https://api.anthropic.com"
    description "Anthropic Claude API"
}
```

The provider name is referenced when creating an agent (`--provider anthropic`).
At spawn time keeperd resolves the hostname to IPv4 addresses and restricts the
agent's nftables egress to those IPs on port 443.  Falls back to port-443-only
if the provider is omitted or cannot be resolved.

---

## viv CLI

All subcommands connect to `keeper.sock`.  Use `--socket` to override the path.

```sh
# Liveness check
bin/viv --socket ./keeper.sock ping

# Daemon and agent status
bin/viv status
bin/viv tui

# Agent management
bin/viv agent list
bin/viv agent create --id my-agent --template /path/to/template
bin/viv agent create --id my-agent --template /path/to/template --provider anthropic
bin/viv agent destroy --id my-agent

# Dispatch a prompt to a running agent
bin/viv prompt --agent my-agent --seq 1 "Summarise the README"
```

`task dev:viv CLI_ARGS="status"` runs the CLI against `./keeper.sock` without building first.

---

## vivlog

Inspect the SQLite audit trail written by keeperd.

```sh
# Most recent 20 frames (default)
bin/vivlog tail

# Last 50 frames
bin/vivlog tail --n 50

# All frames for a specific agent
bin/vivlog show --agent my-agent

# Filter by message type
bin/vivlog grep --msg-type CompletionEvent
bin/vivlog grep --msg-type FailureEvent
bin/vivlog grep --msg-type capability_denied

# Decode a specific frame by sequence number
bin/vivlog decode --seq 42

# Machine-readable JSON output (any subcommand)
bin/vivlog tail --json
bin/vivlog show --agent my-agent --json

# Point at a non-default DB
bin/vivlog --db /var/lib/vivary/workspace/audit.db tail
```

`task dev:vivlog CLI_ARGS="tail"` runs against `./audit.db`.

---

## Ward (inside nspawn)

Ward is normally spawned by keeperd and should not need to be started manually.
For development without nspawn:

```sh
# Ward reads MUS frames from stdin and writes them to stdout.
# In dev mode keeperd spawns it directly as a subprocess.
bin/ward --agent-id dev-agent --log-level debug
```

The capability CLI socket path defaults to `/run/ward-tool.sock` inside the
container.  Override with `--tool-sock` or the `WARD_TOOL_SOCK` env var.

---

## capwrap (capability wrapper binary)

`capwrap` is the generic capability wrapper deployed inside each nspawn container.
It determines the capability to invoke from its binary name (argv[0]).
For manual testing, you can symlink it:

```sh
ln -s capwrap bin/Browser_Page_Read
ln -s capwrap bin/Filesystem_File_Write

# Print the JSON Schema for a capability
bin/Browser_Page_Read --help
bin/Filesystem_File_Write --help

# Invoke a capability manually
WARD_TOOL_SOCK=/run/ward-tool.sock bin/Browser_Page_Read --url https://example.com
WARD_TOOL_SOCK=/run/ward-tool.sock bin/Filesystem_File_Write --path out.txt --content "hello"
```

---

## vivgen (schema code generator)

Generates Go types and JSON schemas from `capabilities/*.kdl`.

```sh
bin/vivgen --dir capabilities --pkg capabilities --output internal/capabilities/generated.go
```

The generator enforces:

- Capability name must be `Namespace_Noun_Verb` (two underscores, each segment
  starts with an uppercase letter, no vendor-specific terms).
- Field metadata in KDL (description, examples, enums).
- Consistent naming between capabilities, entities, and categories.
- Backward compatibility (removed or renamed fields fail the check).

---

## Typical dev workflow

```sh
# Terminal 1 — run the daemon
task dev:keeperd

# Terminal 2 — interact
task dev:viv CLI_ARGS="ping"
task dev:viv CLI_ARGS="status"
task dev:viv CLI_ARGS="agent create --id test-1 --template /tmp/agent-template"
task dev:viv CLI_ARGS="agent list"

# Terminal 3 — watch the audit log
task dev:vivlog CLI_ARGS="tail --n 5"
```

---

## What is not yet wired up

| Feature | Phase | Notes |
|---|---|---|
| Live Claude-backed prompt run | MVP | keeperd↔Ward prompt-run paths are tested; a real Claude CLI run still needs an actual provisioned agent and provider setup |
| Live Chrome verification | 3.2 | keeperd launches Chrome automatically at startup; remaining gap is verification against a real sidecar process |
| Multi-agent routing | Phase 5 | Intentionally deferred until MVP exit tests pass |

### What is now working

| Feature | Notes |
|---|---|
| BubbleTea TUI | `bin/viv tui` is wired up against the current MUS control-plane/status flow |
| Nix distrobuild | `nix build .#distrobuild` and `.#distrobuild-runtime` produce LXD-importable tarballs |
| Runtime container refresh | `task distro:push` rebuilds and syncs runtime binaries into a running LXD container |
| nftables veth egress | Applied at nspawn spawn time; restricted to provider IPs when `--provider` is set |
| providers.kdl | Registry of LLM provider API endpoints; controls nftables IP allowlist |
| Prompt forwarding | keeperd → Ward via `MsgType_CtlPrompt` MUS frame with prompt/completion/failure coverage at the keeperd↔Ward boundary |
