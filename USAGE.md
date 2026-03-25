# VIVARY Usage Guide

Current state: MVP runtime core (v0.1-dev). All commands below work today.
Multi-agent routing remains deferred; the CLI, TUI, and Nix/LXD packaging path are wired up.

---

## Prerequisites

- Go 1.23+
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

# Launch the default runtime image into the default container name
task distro:launch

# Or launch explicitly
./scripts/distro-lxd.sh launch vivary-runtime vivary
```

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

`distro:restart:keeperd` starts `keeperd` in the container with `--workspace /var/lib/vivary/workspace` by default and writes logs to `/var/log/vivary/keeperd.log`. Override with `KEEPERD_WORKSPACE=/some/path` if your container uses a different workspace.

`distro:connect` uses `lxc exec` from the host to launch `viv tui` inside the running container against `$KEEPERD_WORKSPACE/keeper.sock` (default `/var/lib/vivary/workspace/keeper.sock`).

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
chrome-debug-addr "127.0.0.1:9222"
chrome-binary  "chromium"       // path or name; empty = disable sidecar
chrome-user-data-dir "./chrome-data"
max-pipe-bytes-per-sec 1048576
log-level      "info"   // debug | info | warn | error
providers-file "./providers.kdl"
```

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
