# VIVARY Implementation Plan

## MVP: v0.1 Runtime Core

**Goal:** Prove the core VIVARY runtime with one governed local agent: isolated execution, narrow capability access, operator visibility, and strong audit/debug tooling. Multi-agent orchestration is intentionally deferred to the next phase.

**Deliverables:**

1. `keeperd` Go daemon with KDL config parsing; exposes a MUS-over-Unix-socket control plane.
2. BubbleTea TUI/CLI (`vivary`) as a separate binary that connects to `keeper.sock`.
3. `distrobuild` Nix Flake script producing a reproducible NixOS LXD image.
4. Single-agent workspace provisioning: nspawn environment from a Btrfs template subvolume.
5. Ward binary (deployed inside nspawn) with Claude Code subprocess integration.
6. Narrow initial capability set:
   - `Browser_Page_Read` end-to-end: agent → MUS pipe → `keeperd` ACL check → whitelisting proxy → Chrome CDP → response.
   - `Filesystem_File_Write` scoped to an output path inside the agent subvolume, to prove local write policy enforcement.
7. SQLite-backed audit trail plus a Unix-style `vivary-log` CLI for decoding and inspecting MUS records.

**MVP non-goals:**

- multi-agent routing, multicast, or broadcast
- topology governance (`can-invoke`, `max-depth`, `max-spawns`)
- REST gateway families beyond the minimal runtime demonstration
- self-evolution, snapshot governance, or rollback automation

---

## Phase 1: Foundation

### 1.1 `distrobuild` (Nix Flake)

- Define the Nix Flake for the NixOS base system, pinning: Go toolchain, `systemd-nspawn`, btrfs-progs, SQLite, Chromium (headless), and `lxd`.
- Produce a minimal LXD image with `security.nesting=true` and Cgroup v2 delegation configured.
- Validate the image boots identically on Linux (native LXD), macOS (OrbStack/Lima), and Windows 11 (WSL2).
- Configure `/etc/subuid` and `/etc/subgid` with sufficient range for `max-agents` × 65,536 UID entries.

### 1.2 Keeper Daemon (`keeperd`)

- Go project structure: `cmd/keeperd`, `cmd/vivary` (TUI), `cmd/ward`, `internal/switchboard`, `internal/capabilities`, `internal/vault`, `internal/chromproxy`, `internal/tui`, `internal/ctl`.
- `keeperd` starts as a daemon, creates a Unix domain socket at a well-known path (`<workspace_root>/keeper.sock`).
- MUS-over-socket control protocol: `vivary` and any `vivary` subcommands connect to this socket and exchange the same MUS frame format used on agent pipes (`SwarmHeader` + payload). The TUI's `FromID` is `"ctl"` — a reserved identity the ACL layer treats as operator-level.
- KDL config parsing for `orchestrator.kdl` (socket path, log paths, Chrome settings, vault path) and per-agent `agent.kdl`.

### 1.3 BubbleTea TUI (`vivary`)

- Separate binary connecting to `keeper.sock` via MUS frames.
- Single-agent detail view first: status, current prompt seq, last token count, last cost, last event, and approval/debug shortcuts.
- Live-updates via `MsgType_CtlSubscribe` sent on connect; `keeperd` pushes Completion Events and Failure Events to all `ctl` subscribers.
- The initial CLI surface should be at least as important as the TUI: status, prompt dispatch, agent lifecycle, and log inspection must all work without the full screen UI.

### 1.4 Agent Workspace Provisioning

- Command: `vivary agent create <id> --template <path>` (sends `MsgType_CtlAgentCreate` to `keeperd`).
- `keeperd` creates a Btrfs subvolume, copies the template, bind-mounts the Ward binary read-only, writes the agent's `agent.kdl`, configures per-agent cgroup v2 limits, and spawns the nspawn container with the Ward binary as the init process.
- Per-agent veth pair created at spawn time; nftables rules applied on the host-side veth to whitelist only the configured LLM API endpoint (see `DESIGN.md` §6c).

---

## Phase 2: Single-Agent Control Plane

### 2.1 MUS Codec

- Define `SwarmHeader` and all v0.1 `MsgType` constants in Go using `mus-go`.
- Fuzz `UnmarshalMUS` with random byte sequences — malformed frames must never panic `keeperd`.
- Benchmark: establish a baseline framing/decoding target and capture operator-visible latency for prompt dispatch and capability round-trips.

### 2.2 Pipe Router

- One Ward stdio pipe in MVP, implemented in a way that can later generalize to multiple agents.
- Identity enforcement: overwrite `FromID` on all inbound frames with the pipe's registered agent ID.
- `SeqNo` monotonicity check: non-increasing sequence from a sender drops the frame and emits a security log event.
- Byte-rate limiter per pipe (before MUS decoding): frames exceeding the budget are dropped and logged as `pipe_flood` events.
- Single-recipient request/response routing between ctl, keeper, and one Ward in MVP.
- Ctl socket handled by the same router: MUS frames from `keeper.sock` use the same routing path, with `"ctl"` as a reserved sender identity.

### 2.3 Ward Binary

- `cmd/ward`: standalone Go binary, bind-mounted read-only into nspawn.
- `AgentCLI` interface for spawning the LLM subprocess; initial implementation wraps `claude --headless`.
- Capability CLI binaries installed alongside the Ward; each supports `--help` to emit its JSON schema.
- Prompt dispatch loop: receive MUS prompt → spawn LLM subprocess → intercept CLI tool invocations → parse backend-specific call syntax → validate structural/schema correctness → translate valid calls to MUS capability requests → return MUS results as CLI stdout → collect final answer → emit Completion Event.
- Keep Ward narrow: it is the syntax/protocol adapter at the LLM boundary, not the semantic policy authority.
- Schema validation on every tool call against the registered `SwarmCapability.Explain()` schema.
- `keeperd` remains responsible for semantic enforcement after a request is well-formed: ACLs, scope, rate limits, approvals, credential use, and dispatch.
- Loop detection: `(capability_name, full_serialised_args)` hash ring with configurable repeat threshold; on trigger, terminate subprocess and emit `loop_detected` failure event.

### 2.4 Debug Tooling

- `vivary-log` CLI reads the SQLite WAL and decodes MUS records into a Unix-style inspection surface.
- First commands should include:
  - `vivary log tail`
  - `vivary log show --agent <id>`
  - `vivary log grep --msg-type <type>`
  - `vivary log decode --seq <n>`
- Output priorities: readable headers, stable field names, raw payload access when allowed, and machine-friendly output modes for shell pipelines.
- `vivary-log` must be good enough to debug protocol and ACL issues before richer UI tooling exists.

---

## Phase 3: Minimal Capability Surface

### 3.1 `vivary-gen` Tool

- `go generate`-driven binary: reads Go structs with `jsonschema` tags, emits a `_schema.go` companion file with the static `Explain()` string.
- Linter rules enforced at compile time: Namespace_Noun_Verb naming, description + example/enum on all exported fields, snake_case JSON keys, no vendor-specific terms.
- Backward compatibility check: CI fails if a field is removed or renamed without a versioned capability name.
- Included in `distrobuild` so generated files are part of the reproducible build.

### 3.2 Chrome Sidecar & Whitelisting Proxy

- `keeperd` launches headless Chromium on the host with `--remote-debugging-port=9222` and a per-agent `--user-data-dir` on agent spawn.
- Whitelisting proxy: before forwarding any CDP verb to port 9222, check the target URL against the agent's `browser.whitelist`. Deny with a `capability_denied` log event if not listed.
- Implement `Browser_Page_Read`: navigate to URL, wait for `networkidle`, extract readable text via the accessibility tree (not raw HTML), return as MUS response.

### 3.3 Filesystem Output Capability

- Implement `Filesystem_File_Write` with scope constraints limited to a configured writable prefix such as `output/`.
- Deny path traversal, absolute paths, symlink escapes, and writes outside the agent subvolume.
- Require stable audit records for write attempts, including denied requests.

### 3.4 Logging Infrastructure

- SQLite WAL table: `timestamp TEXT | msg_type TEXT | from_id TEXT | to_id TEXT | payload BLOB`.
- Completion Event writer: emitted by the Ward on subprocess exit (fields: `agent_id`, `prompt_seq`, `model`, `input_tokens`, `output_tokens`, `cost_usd`, `context_window_used_pct`, `tool_calls_made`, `outcome`).
- Failure Event writer: emitted by the Ward for each failure mode (`schema_mismatch`, `loop_detected`, `capability_denied`, `subprocess_crash`, `timeout`, `pipe_flood`).
- `vivary-log` CLI: decodes MUS payloads from the WAL, pretty-prints records; filters by agent, time range, event type, and sequence number.
- Honour per-capability `audit-payload false` default for high-sensitivity categories (Email, Messaging, Document, Database).

---

## Phase 4: MVP Exit Testing

Before adding multi-agent orchestration, VIVARY must pass an explicit runtime-core test gate. The goal is to prove that the single-agent governed runtime is understandable, inspectable, and operationally trustworthy.

### 4.1 Unit Tests

| Suite | Coverage |
|---|---|
| `TestMUSCodec` | Round-trip marshal/unmarshal for all MVP MsgTypes; fuzz `UnmarshalMUS`; benchmark allocations per frame. |
| `TestCtlSocket` | Subscribe, status, prompt dispatch, approval messages, and agent lifecycle commands round-trip correctly. |
| `TestSingleAgentRouter` | Identity stamping, `SeqNo` enforcement, and byte-rate limiting on the Ward pipe. |
| `TestCapabilityACL` | Authorized verbs permitted; unauthorized dropped with security log event; `Browser_Page_Read` and `Filesystem_File_Write` scope checks enforced. |
| `TestVivaryGen` | Generated `Explain()` validates as JSON Schema; linter rejects missing descriptions or naming violations; backward-compat check fails on removed field. |
| `TestWardLoop` | Loop detection triggers at threshold; malformed or schema-invalid tool call emits correct failure event; subprocess crash emits correct failure event. |
| `TestChromeProxy` | Whitelisted domain forwarded; unlisted domain denied and logged; readable text extraction returns normalized content. |
| `TestFilesystemWrite` | Allowed writes succeed under configured prefix; path traversal, symlink escape, and absolute path writes are denied and logged. |
| `TestVivaryLog` | WAL decoding, filtering, and raw record inspection behave consistently across output modes. |

### 4.2 End-to-End Tests

| Test | Assertion |
|---|---|
| Environment parity | `keeperd` boots and passes health check on Linux, macOS (OrbStack), Windows (WSL2). |
| TUI/CLI connection | `vivary` connects to `keeper.sock`, receives live Completion/Failure events, and can inspect state without polling. |
| Prompt run | A prompt reaches the Ward, spawns the LLM subprocess, converts well-formed tool calls into MUS requests, executes allowed tools, and returns a final answer plus completion event. |
| Filesystem isolation | Agent attempts write to `/etc` and read of keeper host files — both fail. |
| Network isolation | Agent attempts TCP connection to an arbitrary external host — nftables drops it. |
| Browser whitelist allow | Agent requests `Browser_Page_Read` for a whitelisted URL; content returned. |
| Browser whitelist deny | Agent requests `Browser_Page_Read` for an unlisted URL; `capability_denied` logged, empty or denied response returned. |
| Scoped file write allow | Agent writes to allowed `output/` path; content appears in its subvolume and audit trail records the action. |
| Scoped file write deny | Agent attempts traversal or disallowed path write; request denied and recorded. |
| Pipe flood resilience | Agent floods stdout with garbage; frames are dropped, event logged, and `keeperd` remains responsive. |
| Audit/debug workflow | Operator can use `vivary log` commands to locate a specific prompt run, inspect associated events, and decode the relevant MUS record. |

### 4.3 Exit Criteria

- Single-agent runtime is stable across supported host environments.
- Policy denials are understandable from CLI/TUI output and audit logs.
- Operators can debug capability failures and prompt runs using `vivary-log` without bespoke internal tooling.
- The MVP demonstrates clear value as a governed runtime even with no swarm features enabled.

---

## Phase 5: Multi-Agent Orchestration

Only after the MVP exit tests pass do we add swarm concerns.

### 5.1 Swarm Routing and Topology

- Extend the pipe router from single-agent request/response to unicast, multicast (`group:` prefix), and broadcast (`*`) routing.
- Add topology governance: `can-invoke`, `max-concurrent`, `max-depth`, and `max-agents`.
- Add `ParentID`/`Depth` enforcement for subagent invocation chains.

### 5.2 Multi-Agent Operator UX

- Expand the TUI from single-agent detail to matrix/fleet views.
- Add operator workflows for subagent lifecycle, group visibility, and invocation tracing.
- Extend `vivary-log` with correlation views across parent/child agent runs.

### 5.3 Credential Vault

- AES-256-GCM encrypted store in `keeperd`.
- Agents use a `credential_id` in capability requests; `keeperd` resolves to the actual secret only at gateway invocation time, after ACL checks pass.
- Secret passed to REST gateway binaries via environment variable — never via MUS or agent filesystem.
- `vivary vault add|rotate|list` commands (sent as MUS ctl messages to `keeperd`).

### 5.4 REST API Gateway Binaries

- Separate Go binary per API family (e.g., `cmd/gateway-gsuite`), invoked by `keeperd`.
- Initial set: `Spreadsheet_Range_Read`, `Spreadsheet_Cell_Update`, `Email_Message_List`, `Email_Message_Read`, `Calendar_Event_List`, `Calendar_Event_Create`.
- Response normalization: strip formatting metadata before returning to agent.
- Vendor-neutral capability names resolve to the appropriate provider based on the assigned `credential_id`.

---

## Later Phase: Runtime Evolution and Snapshot Governance

- Agent-driven self-evolution is explicitly out of MVP and out of the first multi-agent phase.
- If introduced later, it should come only after the runtime core and swarm governance layers are already operationally proven.
- Snapshot-based reconfiguration should begin with operator-directed changes before any agent-directed proposal mechanism is added.

### Full Telemetry Hook

- All Switchboard frames (including ctl messages) appended to the SQLite WAL at the router level.

---

## Testing Plan

### Unit Tests

| Suite | Coverage |
|---|---|
| `TestMUSCodec` | Round-trip marshal/unmarshal for all MsgTypes; fuzz `UnmarshalMUS`; benchmark allocations per frame. |
| `TestSwitchboard` | For multi-agent phase: unicast, multicast, broadcast correctness; SeqNo enforcement; FromID overwrite; pipe-flood rate limiter; concurrent routing under race detector. |
| `TestCtlSocket` | TUI subscribe message results in Completion/Failure events pushed to the ctl connection; operator commands (agent create, vault add) round-trip correctly. |
| `TestCapabilityACL` | Authorized verbs permitted; unauthorized dropped with security log event; unknown agent ID rejected; `ctl` identity passes operator-level verbs. |
| `TestVivaryGen` | Generated `Explain()` validates as JSON Schema; linter rejects structs missing descriptions or violating naming convention; backward-compat check fails on removed field. |
| `TestWardLoop` | Loop detection triggers at threshold; schema mismatch emits correct failure event; subprocess crash emits correct failure event. |
| `TestChromeProxy` | Whitelisted domain forwarded; unlisted domain denied and logged; CDP response correctly normalised. |

### End-to-End Tests

| Test | Assertion |
|---|---|
| Environment parity | `keeperd` boots and passes health check on Linux, macOS (OrbStack), Windows (WSL2). |
| TUI connection | `vivary` TUI connects to `keeper.sock`, receives live Completion Events as an agent runs. |
| Filesystem isolation | Agent attempts write to `/etc` and read of keeper host files — both fail. |
| Network isolation | Agent attempts TCP connection to an arbitrary external host — nftables drops it. |
| Identity integrity | Agent embeds a foreign `FromID` in its header; `keeperd` overwrites with the pipe's registered ID. |
| Chrome whitelist allow | Agent requests `Browser_Page_Read` for a whitelisted URL; content returned. |
| Chrome whitelist deny | Agent requests `Browser_Page_Read` for an unlisted URL; `capability_denied` logged, empty response returned. |
| Completion event | After a prompt run, SQLite WAL contains a Completion Event with correct token counts and cost. |
| Loop detection | Repeating tool call sequence triggers `loop_detected` event; subprocess terminated cleanly. |
| Debug tooling | `vivary-log` locates and decodes the relevant WAL entries for a given prompt run. |

### Deferred to Multi-Agent Phase

| Test | Assertion |
|---|---|
| Process isolation | Agent A cannot see Agent B's processes via `ps` or access its IPC namespace. |
| UID isolation | Agent A cannot read Agent B's subvolume files even with direct UID guessing. |
| Swarm unicast | Agent A sends MUS message to Agent B; Agent C does not receive it; payload integrity verified. |
