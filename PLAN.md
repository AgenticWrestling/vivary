# WRESTLE Implementation Plan

## MVP: v0.1

**Goal:** A single agent running in a local nspawn container, controllable from the TUI, capable of browsing whitelisted web URLs via the Orchestrator-managed Chrome proxy.

**Deliverables:**
1. Go Orchestrator daemon with KDL config parsing; exposes a MUS-over-Unix-socket control plane.
2. BubbleTea TUI (`wrestle`) as a separate binary that connects to the Orchestrator daemon socket.
3. `distrobuild` Nix Flake script producing a reproducible NixOS LXD image.
4. Agent workspace provisioning: nspawn environment from a Btrfs template subvolume.
5. Gateway binary (deployed inside nspawn) with Claude Code subprocess integration.
6. `Chrome_Tab_GetWebContent` capability end-to-end: agent → MUS pipe → Orchestrator ACL check → whitelisting proxy → Chrome CDP → response.

---

## Phase 1: Foundation

### 1.1 `distrobuild` (Nix Flake)
- Define the Nix Flake for the NixOS base system, pinning: Go toolchain, `systemd-nspawn`, btrfs-progs, SQLite, Chromium (headless), and `lxd`.
- Produce a minimal LXD image with `security.nesting=true` and Cgroup v2 delegation configured.
- Validate the image boots identically on Linux (native LXD), macOS (OrbStack/Lima), and Windows 11 (WSL2).

### 1.2 Orchestrator Daemon
- Go project structure: `cmd/orchestratord`, `cmd/wrestle` (TUI), `cmd/gateway`, `internal/switchboard`, `internal/capabilities`, `internal/vault`, `internal/chromproxy`, `internal/tui`, `internal/ctl`.
- Orchestrator starts as a daemon, creates a Unix domain socket at a well-known path (e.g., `<workspace_root>/orchestrator.sock`).
- MUS-over-socket control protocol: TUI and any `wrestle` subcommands connect to this socket and exchange the same MUS frame format used on agent pipes (`SwarmHeader` + payload). The TUI's `FromID` is `"ctl"` — a reserved identity the ACL layer treats as operator-level.
- KDL config parsing for `orchestrator.kdl` (socket path, log paths, Chrome settings, vault path) and per-agent `agent.kdl`.

### 1.3 BubbleTea TUI (`wrestle`)
- Separate binary connecting to `orchestrator.sock` via MUS frames.
- Matrix view: one row per agent — agent ID, status, current prompt seq, last token count, last cost, last event.
- Live-updates via a subscription message sent to the Orchestrator on connect (`MsgType_CtlSubscribe`); Orchestrator pushes Completion Events and Failure Events to all `ctl` subscribers.

### 1.4 Agent Workspace Provisioning
- Command: `wrestle agent create <id> --template <path>` (sends a `MsgType_CtlAgentCreate` MUS message to the daemon).
- Daemon creates a Btrfs subvolume, copies the template, bind-mounts the Gateway binary read-only, writes the agent's `agent.kdl`, and spawns the nspawn container with the Gateway binary as the init process.
- Enforces cgroup v2 resource limits from `agent.kdl`.

---

## Phase 2: Stdio Switchboard

### 2.1 MUS Codec
- Define `SwarmHeader` and all v0.1 `MsgType` constants in Go using `mus-go`.
- Fuzz `UnmarshalMUS` with random byte sequences — malformed frames must never panic the Orchestrator.
- Benchmark: establish a baseline routing throughput target (frames/sec at p99 latency).

### 2.2 Pipe Router
- Goroutine per agent stdio pipe, feeding a central routing channel.
- Identity enforcement: overwrite `FromID` on all inbound frames with the pipe's registered agent ID.
- `SeqNo` monotonicity check: non-increasing sequence from a sender drops the frame and emits a security log event.
- Unicast, multicast (`group:` prefix), and broadcast (`*`) routing.
- Ctl socket handled by the same router: MUS frames from the Unix socket use the same routing path, with `"ctl"` as a reserved sender.

### 2.3 Gateway Binary
- `cmd/gateway`: standalone Go binary, bind-mounted read-only into nspawn.
- `AgentCLI` interface for spawning the LLM subprocess; initial implementation wraps `claude --headless`.
- Prompt dispatch loop: receive MUS prompt → spawn subprocess → pipe tool call JSON to Orchestrator → return MUS results as tool responses → collect final answer → emit Completion Event.
- Schema validation on every tool call JSON against the registered `SwarmCapability.Explain()` schema.
- Loop detection: (tool_name, args) hash ring with configurable repeat threshold; on trigger, terminate subprocess and emit `loop_detected` failure event.

---

## Phase 3: Chrome Capability

### 3.1 `wrestle-gen` Tool
- `go generate`-driven binary: reads Go structs with `jsonschema` tags, emits a `_schema.go` companion file with the static `Explain()` string.
- Linter rules enforced at compile time: Namespace_Noun_Verb naming, description + example/enum on all exported fields, snake_case JSON keys, no vendor-specific terms.
- Included in `distrobuild` so generated files are part of the reproducible build.

### 3.2 Chrome Sidecar & Whitelisting Proxy
- Orchestrator launches headless Chromium on the host with `--remote-debugging-port=9222` and a per-agent `--user-data-dir` on agent spawn.
- Whitelisting proxy: before forwarding any CDP verb to port 9222, check the target URL against the agent's `browser.whitelist`. Deny with a `capability_denied` log event if not listed.
- Implement `Chrome_Tab_GetWebContent`: navigate to URL, wait for `networkidle`, extract readable text via the accessibility tree (not raw HTML), return as MUS response.

### 3.3 Logging Infrastructure
- SQLite WAL table: `timestamp TEXT | msg_type TEXT | from_id TEXT | to_id TEXT | payload BLOB`.
- Completion Event writer: emitted by Gateway on subprocess exit (fields: agent_id, prompt_seq, model, input_tokens, output_tokens, cost_usd, context_window_used_pct, tool_calls_made, outcome).
- Failure Event writer: emitted by Gateway for each failure mode (`schema_mismatch`, `loop_detected`, `capability_denied`, `subprocess_crash`, `timeout`).
- `wrestle-log` CLI: decodes MUS payloads from the WAL, pretty-prints records; filters by agent, time range, event type.

---

## Phase 4: Credential Vault & REST Gateways

### 4.1 Credential Vault
- AES-256-GCM encrypted store in the Orchestrator daemon.
- Agents use a `credential_id` in capability requests; the Orchestrator resolves to the actual secret only at the gateway invocation layer.
- `wrestle vault add|rotate|list` commands (sent as MUS ctl messages to the daemon).

### 4.2 REST API Gateway Binaries
- Separate Go binary per API family (e.g., `cmd/gateway-gsuite`), invoked by the Orchestrator process.
- Initial set: `Gsuite_Sheet_ReadRange`, `Gsuite_Sheet_UpdateCell`, `Gsuite_Mail_ListUnread`, `Gsuite_Mail_GetBody`, `Calendar_Event_List`, `Calendar_Event_Create`.
- Response normalization: strip formatting metadata before returning to agent.
- Vendor-neutral aliases resolve to the appropriate provider based on the assigned `credential_id`.

---

## Phase 5: Self-Improvement Loop

### 5.1 Evolution Proposal
- `MsgType_SelfEvolution` MUS message: agent proposes a diff to its `agent.kdl`.
- Governance rules in `orchestrator.kdl`: e.g., agents may not grant themselves capabilities they don't currently hold, may not raise memory limits beyond a host-wide cap.

### 5.2 Snapshot & Reinstantiation
- On accepted proposal: Btrfs snapshot (`snap-<id>-pre-<seqno>`), apply new `agent.kdl`, terminate nspawn, restart.
- On rollback trigger: restore from pre-evolution snapshot, restart, emit telemetry event.

### 5.3 Full Telemetry Hook
- All Switchboard frames (including ctl messages) appended to the SQLite WAL at the router level.

---

## Testing Plan

### Unit Tests

| Suite | Coverage |
|---|---|
| `TestMUSCodec` | Round-trip marshal/unmarshal for all MsgTypes; fuzz `UnmarshalMUS`; benchmark allocations per frame. |
| `TestSwitchboard` | Unicast, multicast, broadcast correctness; SeqNo enforcement; FromID overwrite; concurrent routing under race detector. |
| `TestCtlSocket` | TUI subscribe message results in Completion/Failure events pushed to the ctl connection; operator commands (agent create, vault add) round-trip correctly. |
| `TestCapabilityACL` | Authorized verbs permitted; unauthorized dropped with security log event; unknown agent ID rejected; `ctl` identity passes operator-level verbs. |
| `TestWrestleGen` | Generated `Explain()` validates as JSON Schema; linter rejects structs missing descriptions or violating naming convention. |
| `TestGatewayLoop` | Loop detection triggers at threshold; schema mismatch emits correct failure event; subprocess crash emits correct failure event. |
| `TestChromeProxy` | Whitelisted domain forwarded; unlisted domain denied and logged; CDP response correctly normalized. |

### End-to-End Tests

| Test | Assertion |
|---|---|
| Environment parity | Orchestrator boots and passes health check on Linux, macOS (OrbStack), Windows (WSL2). |
| TUI connection | `wrestle` TUI connects to `orchestrator.sock`, receives live Completion Events as an agent runs. |
| Filesystem isolation | Agent attempts write to `/etc` and read of Orchestrator host files — both fail. |
| Process isolation | Agent A cannot see Agent B's processes via `ps` or access its IPC namespace. |
| Swarm unicast | Agent A sends MUS message to Agent B; Agent C does not receive it; payload integrity verified. |
| Identity integrity | Agent embeds a foreign `FromID` in its header; Orchestrator overwrites with the pipe's registered ID. |
| Chrome whitelist allow | Agent requests `Chrome_Tab_GetWebContent` for a whitelisted URL; content returned. |
| Chrome whitelist deny | Agent requests `Chrome_Tab_GetWebContent` for an unlisted URL; `capability_denied` logged, empty response returned. |
| Completion event | After a prompt run, SQLite WAL contains a Completion Event with correct token counts and cost. |
| Loop detection | Repeating tool call sequence triggers `loop_detected` event; subprocess terminated cleanly. |
| Evolution + rollback | Valid evolution proposal accepted; snapshot taken; agent restarted. Crash on first post-evolution run triggers rollback to snapshot within 500ms. |
