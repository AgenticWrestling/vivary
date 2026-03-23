# VIVARY Implementation Plan

## MVP: v0.1 Runtime Core

**Goal:** Prove the core VIVARY runtime with one local agent in MVP scope: isolated execution, narrow capability access, operator visibility, and strong audit/debug tooling. Multi-agent orchestration is intentionally deferred to the next phase.

**Deliverables:**

1. `keeperd` Go daemon with KDL config parsing; exposes a MUS-over-Unix-socket control plane.
2. BubbleTea TUI/CLI (`viv`) as a separate binary that connects to `keeper.sock`.
3. `distrobuild` Nix Flake script producing a reproducible NixOS LXD image.
4. Single-agent workspace provisioning: nspawn environment from a Btrfs template subvolume.
5. Ward binary (deployed inside nspawn) with Claude Code subprocess integration.
6. Narrow initial capability set:
   - `Browser_Page_Read` end-to-end: agent → MUS pipe → `keeperd` ACL check → whitelisting proxy → Chrome CDP → response.
   - `Filesystem_File_Write` scoped to an output path inside the agent subvolume, to prove local write policy enforcement.
7. SQLite-backed audit trail plus a Unix-style `vivlog` CLI for decoding and inspecting MUS records.

**MVP non-goals:**

- multi-agent routing, multicast, or broadcast
- topology governance (`can-invoke`, `max-depth`, `max-spawns`)
- REST gateway families beyond the minimal runtime demonstration
- self-evolution, snapshot governance, or rollback automation

---

## Phase 1: Foundation

### 1.1 `distrobuild` (Nix Flake)

- Define the Nix Flake for the NixOS base system, pinning: Go toolchain, `systemd-nspawn`, btrfs-progs, SQLite, and `lxd`.
- Produce a minimal LXD image with `security.nesting=true` and Cgroup v2 delegation configured.
- Validate the image boots identically on Linux (native LXD), macOS (OrbStack/Lima), and Windows 11 (WSL2).
- Configure `/etc/subuid` and `/etc/subgid` with sufficient range for `max-agents` × 65,536 UID entries.
- Keep Chrome out of the guest image. Browser execution is a host-side concern; the guest only needs the runtime pieces required for `keeperd`, `ward`, provisioning, and audit/debug tooling.

### 1.2 Keeper Daemon (`keeperd`)

- Go project structure: `cmd/keeperd`, `cmd/viv` (TUI/CLI), `cmd/vivlog`, `cmd/ward`, `internal/switchboard`, `internal/capabilities`, `internal/vault`, `internal/chromproxy`, `internal/tui`, `internal/ctl`.
- `keeperd` starts as a daemon, creates a Unix domain socket at a well-known path (`<workspace_root>/keeper.sock`).
- MUS-over-socket control protocol: `viv` and any `viv` subcommands connect to this socket and exchange the same MUS frame format used on agent pipes (`SwarmHeader` + payload). The TUI's `FromID` is `"ctl"` — a reserved identity the ACL layer treats as operator-level.
- KDL config parsing for `orchestrator.kdl` (socket path, log paths, Chrome settings, vault path) and per-agent `agent.kdl`.
- **Replace hand-rolled KDL parser** with `github.com/sblinch/kdl-go`. The current scanner handles only single-line nodes and basic blocks; the library gives full spec compliance (multi-line strings, type annotations, slashdash comments) and removes a class of edge-case bugs (e.g., `//` inside quoted strings). `OrchestratorConfig`, `AgentConfig`, and `ProviderConfig` structs should be aligned strictly with the schemas described in `DESIGN.md` as part of this migration. Add structured validation (required fields, naming conventions, uniqueness of agent IDs) at parse time rather than at first use.
- Move configuration loading and validation into a dedicated `internal/config` package so `cmd/keeperd` remains startup/wiring code rather than parser code.
- Keep `keeperd` runtime state explicit and small: agent status, last prompt seq, last completion, last failure, active ctl subscribers, and current runtime handles. Build ctl responses directly from this state.

### 1.3 BubbleTea TUI (`viv`)

- Separate binary connecting to `keeper.sock` via MUS frames.
- Single-agent detail view first: status, current prompt seq, last token count, last cost, last event, and approval/debug shortcuts.
- Live-updates via `MsgType_CtlSubscribe` sent on connect; `keeperd` pushes Completion Events and Failure Events to all `ctl` subscribers.
- The initial CLI surface should be at least as important as the TUI: status, prompt dispatch, agent lifecycle, and log inspection must all work without the full screen UI.
- `keeperd` must maintain enough real runtime state that the TUI is showing authoritative values rather than sparse placeholders. Status, last event, and cost data should come from keeper-owned state, not incidental side effects.

### 1.4 Agent Workspace Provisioning

- Command: `viv agent create <id> --template <path>` (sends `MsgType_CtlAgentCreate` to `keeperd`).
- `keeperd` creates a Btrfs subvolume, copies the template, bind-mounts the Ward binary read-only, writes the agent's `agent.kdl`, configures per-agent cgroup v2 limits, and spawns the nspawn container with the Ward binary as the init process.
- Per-agent veth pair created at spawn time; nftables rules applied on the host-side veth to whitelist only the configured LLM API endpoint (see `DESIGN.md` §6c).
- **`ContainerRuntime` interface**: extract all nspawn/btrfs/machinectl/nft `exec.Command` calls into a `ContainerRuntime` interface (`Provision`, `Destroy`, `ApplyNetworkRules`, `RemoveNetworkRules`). Provide a real Linux implementation and a stub/fake for non-Linux test runs. This makes provisioning logic unit-testable without requiring real kernel resources and eases future support for alternative runtimes (e.g., OCI containers).
- **Provisioning error recovery**: if any provisioning step fails (subvolume creation, bind-mount, nftables, nspawn spawn) the partially-created state (subvolume, veth pair, nftables table) must be torn down reliably before returning the error. Use a deferred cleanup stack pattern.
- Treat the direct Ward subprocess path on non-Linux as a development/testing fallback only, not as the target runtime architecture.
- Fix Linux provisioning correctness before broadening features: nspawn argument construction, cleanup ordering, and runtime registration should be verified on real Linux before more orchestration work lands.

---

## Phase 2: Runtime Control Plane

### 2.1 MUS Codec

- Define `SwarmHeader` and all v0.1 `MsgType` constants in Go using `mus-go`.
- For MVP, standardize on `MUS header + payload bytes`, with JSON payloads where that keeps the code simpler. Make this explicit in docs and code comments rather than implying a fuller typed MUS payload system already exists.
- Fuzz `UnmarshalMUS` with random byte sequences — malformed frames must never panic `keeperd`.
- Benchmark: establish a baseline framing/decoding target and capture operator-visible latency for prompt dispatch and capability round-trips.
- **SeqNo edge cases**: handle SeqNo wrapping (sender restarts from 0) more gracefully — a configurable grace window or explicit reconnect handshake rather than silently dropping all frames until reboot.
- **Router performance**: benchmark the `io.MultiReader(peek, pipe)` approach in `readLoop` against a `bufio.Reader`-based alternative. If allocations per frame are measurably higher, switch to the buffered approach.

### 2.2 Pipe Router

- One Ward stdio pipe in MVP, implemented in a way that can later generalize to multiple agents.
- Identity enforcement: overwrite `FromID` on all inbound frames with the pipe's registered agent ID.
- `SeqNo` monotonicity check: non-increasing sequence from a sender drops the frame and emits a security log event.
- Byte-rate limiter per pipe (before MUS decoding): frames exceeding the budget are dropped and logged as `pipe_flood` events.
- Single-recipient request/response routing between ctl, keeper, and one Ward in MVP.
- Keep a shared frame codec and shared message model across ctl and Ward traffic.
- Decide explicitly whether ctl should share the same router implementation as Ward traffic or only the same codec/message model. Choose the simpler implementation for MVP; architectural symmetry is secondary to clarity.
- If ctl remains on a separate handler path for MVP, extract a small shared frame-handling layer so protocol behavior does not drift.

### 2.3 Ward Binary

- `cmd/ward`: standalone Go binary, bind-mounted read-only into nspawn.
- `AgentCLI` interface for spawning the LLM subprocess; initial implementation wraps `claude --headless`. The interface must be thin enough that a Gemini CLI or OpenAI-compatible runner can be wired in without touching core Ward logic. Each implementation parses its own JSON stream format and emits normalised `tool_use` / `message_stop` events.
- Capability CLI binaries installed alongside the Ward; each supports `--help` to emit its JSON schema.
- Prompt dispatch loop: receive MUS prompt → spawn LLM subprocess → intercept CLI tool invocations → parse backend-specific call syntax → validate structural/schema correctness → translate valid calls to MUS capability requests → return MUS results as CLI stdout → collect final answer → emit Completion Event.
- Keep Ward narrow: it is the syntax/protocol adapter at the LLM boundary, not the semantic policy authority.
- Pick one real LLM integration path and make it crisp. Remove overlapping or partially-implemented tool-call paths so the prompt → tool call → MUS request flow is obvious to a Go reader.
- Introduce one normalized internal event shape inside Ward (`tool_use`, `message_stop`, etc.) so backend-specific parsing stays behind a tiny adapter boundary.
- **Schema synchronisation**: Ward must load capability schemas from the same source of truth as `keeperd` — either by calling `cap-cli --help` at startup or by embedding schemas generated by `vivary-gen`. The hard-coded schema constants in `ward/main.go` are an MVP shortcut and must be removed once `vivary-gen` is part of the build pipeline (§3.1).
- Schema validation on every tool call against the registered `SwarmCapability.Explain()` schema.
- `keeperd` remains responsible for semantic enforcement after a request is well-formed: ACLs, scope, rate limits, approvals, credential use, and dispatch.
- Loop detection: `(capability_name, full_serialised_args)` hash ring with configurable repeat threshold; on trigger, terminate subprocess and emit `loop_detected` failure event.

### 2.4 Debug Tooling

- `vivlog` CLI reads the SQLite WAL and decodes MUS records into a Unix-style inspection surface.
- First commands should include:
  - `vivlog tail`
  - `vivlog show --agent <id>`
  - `vivlog grep --msg-type <type>`
  - `vivlog decode --seq <n>`
- Output priorities: readable headers, stable field names, raw payload access when allowed, and machine-friendly output modes for shell pipelines.
- `vivlog` must be good enough to debug protocol and ACL issues before richer UI tooling exists.

---

## Phase 3: Minimal Capability Surface

### 3.1 `vivary-gen` Tool

- `go generate`-driven binary: reads Go structs with `jsonschema` tags, emits a `_schema.go` companion file with the static `Explain()` string.
- Linter rules enforced at compile time: Namespace_Noun_Verb naming, description + example/enum on all exported fields, snake_case JSON keys, no vendor-specific terms.
- Backward compatibility check: CI fails if a field is removed or renamed without a versioned capability name.
- Included in `distrobuild` so generated files are part of the reproducible build.

### 3.2 Chrome Sidecar & Whitelisting Proxy

#### 3.2a Browser Launch and Profile Isolation

- `keeperd` launches Chromium on the host with `--remote-debugging-port=9222` and a per-agent `--user-data-dir` on agent spawn.
- Profile isolation must either work exactly as described in the docs or be reduced to a simpler documented model. Do not leave this ambiguous.

#### 3.2b Whitelisting Proxy

- Before forwarding any CDP verb to port 9222, check the target URL against the agent's `browser.whitelist`. Deny with a `capability_denied` log event if not listed.
- Whitelist enforcement must happen in a testable layer and must not depend only on higher-level capability checks.

#### 3.2c `Browser_Page_Read`

- Implement `Browser_Page_Read`: navigate to URL, wait for `networkidle`, extract readable text via the accessibility tree (not raw HTML), return as MUS response.
- Keep the browser surface very small in MVP. Get `Browser_Page_Read` correct before adding any broader browser capability family.

### 3.3 Filesystem Output Capability

- Implement `Filesystem_File_Write` with scope constraints limited to a configured writable prefix such as `output/`.
- Deny path traversal, absolute paths, symlink escapes, and writes outside the agent subvolume.
- Require stable audit records for write attempts, including denied requests.

### 3.4 Logging Infrastructure

- SQLite WAL table: `timestamp TEXT | msg_type TEXT | from_id TEXT | to_id TEXT | payload BLOB`.
- Completion Event writer: emitted by the Ward on subprocess exit (fields: `agent_id`, `prompt_seq`, `model`, `input_tokens`, `output_tokens`, `cost_usd`, `context_window_used_pct`, `tool_calls_made`, `outcome`).
- Failure Event writer: emitted by the Ward for each failure mode (`schema_mismatch`, `loop_detected`, `capability_denied`, `subprocess_crash`, `timeout`, `pipe_flood`).
- `vivlog` CLI: decodes MUS payloads from the WAL, pretty-prints records; filters by agent, time range, event type, and sequence number.
- Honour per-capability `audit-payload false` default for high-sensitivity categories (Email, Messaging, Document, Database).
- **`shouldAuditPayload` implementation**: replace the current stub (always `true`) with real per-capability logic driven by a `AuditPayload() bool` method on the `Capability` interface. High-sensitivity categories (Email, Messaging, Document, Database, Credential) default to `false`; operator config can override per capability.
- **Audit DB maintenance**: treat retention/pruning as post-MVP hardening unless real usage forces it earlier. The MVP requirement is correct payload policy and reliable inspection, not a full audit lifecycle subsystem.

### 3.5 ECS Scope Model

- Migrate the raw `Scope` string passed through `context` to the Entity-Component-System resource model described in `CAPABILITIES.md`. Scope constraints should be expressed as typed key-value pairs (`to-domain`, `path-prefix`, `max-chars`, etc.) rather than free-form strings.
- The `ACLEntry.Scope` field becomes a structured `ScopeConstraint` that each capability validates against its own schema. This eliminates the ad-hoc parsing currently done inside `urlMatchesScope`, `checkNoSymlinkEscape`, etc.
- Backward-compatible migration: keep the raw-string path working behind a compat shim until all built-in capabilities are migrated.

### 3.6 MVP Consolidation

- Remove hard-coded Ward schema constants and finish schema synchronization through `vivary-gen` or an equivalent single source of truth.
- Make browser mediation and whitelist enforcement obviously correct and integration-tested before adding more capability families.
- Make provisioning safe and recoverable: cleanup-on-failure, correct runtime registration, and predictable Linux behavior.
- Make ctl/event semantics explicit and stable so the TUI, CLI, and audit log all agree on message behavior.
- Eliminate duplicate or speculative code paths in Ward and keeperd. The MVP should read as one clear runtime path, not several half-finished alternatives.
- Ensure the TUI and `keeperd` runtime state model reflect real prompt, event, and cost data.

---

## Phase 4: MVP Exit Testing

Before adding multi-agent orchestration, VIVARY must pass an explicit runtime-core test gate. The goal is to prove that the MVP runtime is understandable, inspectable, and operationally trustworthy.

### 4.1 Unit Tests

| Suite | Coverage |
|---|---|
| `TestMUSCodec` | Round-trip marshal/unmarshal for all MVP MsgTypes; fuzz `UnmarshalMUS`; benchmark allocations per frame. |
| `TestCtlSocket` | Subscribe, status, prompt dispatch, approval messages, and agent lifecycle commands round-trip correctly. |
| `TestRuntimeRouter` | Identity stamping, `SeqNo` enforcement, and byte-rate limiting on the Ward pipe. |
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
| TUI/CLI connection | `viv` connects to `keeper.sock`, receives live Completion/Failure events, and can inspect state without polling. |
| Prompt run | A prompt reaches the Ward, spawns the LLM subprocess, converts well-formed tool calls into MUS requests, executes allowed tools, and returns a final answer plus completion event. |
| Filesystem isolation | Agent attempts write to `/etc` and read of keeper host files — both fail. |
| Network isolation | Agent attempts TCP connection to an arbitrary external host — nftables drops it. |
| Browser whitelist allow | Agent requests `Browser_Page_Read` for a whitelisted URL; content returned. |
| Browser whitelist deny | Agent requests `Browser_Page_Read` for an unlisted URL; `capability_denied` logged, empty or denied response returned. |
| Scoped file write allow | Agent writes to allowed `output/` path; content appears in its subvolume and audit trail records the action. |
| Scoped file write deny | Agent attempts traversal or disallowed path write; request denied and recorded. |
| Pipe flood resilience | Agent floods stdout with garbage; frames are dropped, event logged, and `keeperd` remains responsive. |
| Audit/debug workflow | Operator can use `vivlog` commands to locate a specific prompt run, inspect associated events, and decode the relevant MUS record. |

### 4.3 Exit Criteria

- Single-agent runtime is stable across supported host environments.
- Policy denials are understandable from CLI/TUI output and audit logs.
- Operators can debug capability failures and prompt runs using `vivlog` without bespoke internal tooling.
- The MVP demonstrates clear value as a capability-restricted, isolated, auditable runtime even with no swarm features enabled.

---

## Phase 5: Multi-Agent Orchestration

Only after the MVP exit tests pass do we add swarm concerns.

### 5.1 Swarm Routing and Topology

- Extend the pipe router from MVP request/response routing to unicast, multicast (`group:` prefix), and broadcast (`*`) routing.
- Add topology governance: `can-invoke`, `max-concurrent`, `max-depth`, and `max-agents`.
- Add `ParentID`/`Depth` enforcement for subagent invocation chains.

### 5.2 Multi-Agent Operator UX

- Expand the TUI from the MVP detail view to matrix/fleet views.
- Add operator workflows for subagent lifecycle, group visibility, and invocation tracing.
- Extend `vivlog` with correlation views across parent/child agent runs.

### 5.3 Credential Vault

- AES-256-GCM encrypted store in `keeperd`.
- Agents use a `credential_id` in capability requests; `keeperd` resolves to the actual secret only at gateway invocation time, after ACL checks pass.
- Secret passed to REST gateway binaries via environment variable — never via MUS or agent filesystem.
- `viv vault add|rotate|list` commands (sent as MUS ctl messages to `keeperd`).
- **Encryption at rest (SQLCipher)**: once the vault is introduced, evaluate replacing the plain SQLite audit DB with SQLCipher (AES-256) so that audit records and vault data share the same encrypted file. Key derived from operator passphrase or hardware token; `keeperd` prompts on startup if the key is not in the environment.

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

## Future Investigations (not yet scheduled)

These items are worth tracking but are not committed to any phase yet.

### Dynamic UID Range Allocation

The MVP uses `systemd-nspawn -U` for automatic UID allocation. `DESIGN.md` describes a deliberate 65,536-entry-per-agent allocation scheme managed by `keeperd` against `/etc/subuid`. Implement this when multi-agent is introduced (Phase 5) so that agent UID ranges are predictable, non-overlapping, and recoverable across daemon restarts.

### MCP Tool Protocol

Investigate whether Ward can act as a Model Context Protocol (MCP) host, exposing capability CLIs as MCP tools rather than Unix socket connections. This would simplify tool discovery for LLM subprocesses that speak MCP natively and reduce Ward's bespoke JSON parsing surface. Note: this is a significant architectural change — Ward's current design as a narrow syntax/protocol adapter is deliberate. Any MCP integration must not weaken the ACL enforcement boundary or route capability requests around `keeperd`. Evaluate after Phase 4 exit tests pass.

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
| TUI connection | `viv` TUI connects to `keeper.sock`, receives live Completion Events as an agent runs. |
| Filesystem isolation | Agent attempts write to `/etc` and read of keeper host files — both fail. |
| Network isolation | Agent attempts TCP connection to an arbitrary external host — nftables drops it. |
| Identity integrity | Agent embeds a foreign `FromID` in its header; `keeperd` overwrites with the pipe's registered ID. |
| Chrome whitelist allow | Agent requests `Browser_Page_Read` for a whitelisted URL; content returned. |
| Chrome whitelist deny | Agent requests `Browser_Page_Read` for an unlisted URL; `capability_denied` logged, empty response returned. |
| Completion event | After a prompt run, SQLite WAL contains a Completion Event with correct token counts and cost. |
| Loop detection | Repeating tool call sequence triggers `loop_detected` event; subprocess terminated cleanly. |
| Debug tooling | `vivlog` locates and decodes the relevant WAL entries for a given prompt run. |

### Deferred to Multi-Agent Phase

| Test | Assertion |
|---|---|
| Process isolation | Agent A cannot see Agent B's processes via `ps` or access its IPC namespace. |
| UID isolation | Agent A cannot read Agent B's subvolume files even with direct UID guessing. |
| Swarm unicast | Agent A sends MUS message to Agent B; Agent C does not receive it; payload integrity verified. |
