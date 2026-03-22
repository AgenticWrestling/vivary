# VIVARY Usage Guide

Current state: MVP runtime core (v0.1-dev).  All commands below work today.
Multi-agent routing, the BubbleTea TUI, and the Nix distrobuild are not yet wired up.

---

## Prerequisites

- Go 1.23+
- [`task`](https://taskfile.dev) (optional but recommended)
- Linux with `systemd-nspawn` + `btrfs-progs` for full container isolation
  (macOS / WSL2: Ward runs as a plain subprocess — isolation is not enforced)

---

## Build

```sh
# All binaries into ./bin
task build

# Or individually
task build:keeperd
task build:ward
task build:vivary
task build:vivary-log

# Or with go directly
go build -o bin/keeperd    ./cmd/keeperd
go build -o bin/ward       ./cmd/ward
go build -o bin/vivary     ./cmd/vivary
go build -o bin/vivary-log ./cmd/vivary-log
go build -o bin/cap-cli    ./cmd/cap-cli
go build -o bin/vivary-gen ./cmd/vivary-gen
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

## vivary CLI

All subcommands connect to `keeper.sock`.  Use `--socket` to override the path.

```sh
# Liveness check
bin/vivary --socket ./keeper.sock ping

# Daemon and agent status
bin/vivary status

# Agent management
bin/vivary agent list
bin/vivary agent create --id my-agent --template /path/to/template
bin/vivary agent create --id my-agent --template /path/to/template --provider anthropic
bin/vivary agent destroy --id my-agent

# Dispatch a prompt to a running agent
bin/vivary prompt --agent my-agent --seq 1 "Summarise the README"
```

`task dev:vivary CLI_ARGS="status"` runs the CLI against `./keeper.sock` without building first.

---

## vivary-log

Inspect the SQLite audit trail written by keeperd.

```sh
# Most recent 20 frames (default)
bin/vivary-log tail

# Last 50 frames
bin/vivary-log tail --n 50

# All frames for a specific agent
bin/vivary-log show --agent my-agent

# Filter by message type
bin/vivary-log grep --msg-type CompletionEvent
bin/vivary-log grep --msg-type FailureEvent
bin/vivary-log grep --msg-type capability_denied

# Decode a specific frame by sequence number
bin/vivary-log decode --seq 42

# Machine-readable JSON output (any subcommand)
bin/vivary-log tail --json
bin/vivary-log show --agent my-agent --json

# Point at a non-default DB
bin/vivary-log --db /var/lib/vivary/workspace/audit.db tail
```

`task dev:vivary-log CLI_ARGS="tail"` runs against `./audit.db`.

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

## cap-cli (capability CLI binary)

`cap-cli` is the generic capability CLI deployed inside each nspawn container.
It is symlinked per capability name (e.g. `Browser_Page_Read` → `cap-cli`).

```sh
# Print the JSON Schema for a capability (useful for debugging)
Browser_Page_Read --help
Filesystem_File_Write --help

# Invoke a capability (normally called by the LLM subprocess, not by hand)
WARD_TOOL_SOCK=/run/ward-tool.sock Browser_Page_Read --url https://example.com
WARD_TOOL_SOCK=/run/ward-tool.sock Filesystem_File_Write --path out.txt --content "hello"
```

---

## vivary-gen (schema code generator)

Generates a static `Explain()` method for a capability Args struct.

```sh
bin/vivary-gen \
  -in  internal/capabilities/browser.go \
  -out internal/capabilities/browser_schema.go \
  -cap Browser_Page_Read

# With backward-compat check against a saved previous schema
bin/vivary-gen \
  -in  internal/capabilities/browser.go \
  -out internal/capabilities/browser_schema.go \
  -cap Browser_Page_Read \
  -prev-schema .schema-snapshots/Browser_Page_Read.json
```

Linting rules enforced at generation time:
- Capability name must be `Namespace_Noun_Verb` (two underscores, each segment
  starts with an uppercase letter, no vendor-specific terms).
- Every exported field must have a `description` struct tag.
- All JSON field names must be `snake_case`.
- Removed or renamed fields fail the backward-compat check.

---

## Typical dev workflow

```sh
# Terminal 1 — run the daemon
task dev:keeperd

# Terminal 2 — interact
task dev:vivary CLI_ARGS="ping"
task dev:vivary CLI_ARGS="status"
task dev:vivary CLI_ARGS="agent create --id test-1 --template /tmp/agent-template"
task dev:vivary CLI_ARGS="agent list"

# Terminal 3 — watch the audit log
task dev:vivary-log CLI_ARGS="tail --n 5"
```

---

## What is not yet wired up

| Feature | Phase | Notes |
|---|---|---|
| End-to-end prompt run (keeperd → Ward → Claude) | MVP | Prompt forwarding wired; Ward tool-socket and LLM subprocess implemented; full run needs a live agent + Claude CLI |
| Headless Chrome sidecar | 3.2 | keeperd launches Chrome automatically at startup; non-fatal if `chromium` not in PATH |
| BubbleTea TUI | 1.3 | CLI is fully functional; TUI is scaffolded |
| Multi-agent routing | Phase 5 | Intentionally deferred until MVP exit tests pass |

### What is now working

| Feature | Notes |
|---|---|
| Nix distrobuild | `nix build .#distrobuild` and `.#distrobuild-runtime` produce LXD-importable tarballs |
| nftables veth egress | Applied at nspawn spawn time; restricted to provider IPs when `--provider` is set |
| providers.kdl | Registry of LLM provider API endpoints; controls nftables IP allowlist |
| Prompt forwarding | keeperd → Ward via `MsgType_CtlPrompt` MUS frame |
