# VIVARY Implementation Plan

This plan tracks the MVP runtime core first and distinguishes between:

- implemented foundations already in the repo
- remaining MVP consolidation work needed to make the runtime trustworthy
- later multi-agent and gateway work that should stay explicitly deferred

## MVP: v0.1 Runtime Core

**Goal:** Prove the core VIVARY runtime with one local agent in MVP scope: isolated execution, narrow capability access, operator visibility, and strong audit/debug tooling. Multi-agent orchestration remains intentionally deferred.

**Implemented foundations:**

1. `keeperd` exists and exposes a MUS-framed Unix-socket control plane.
2. `viv` exists as a separate CLI/TUI binary.
3. `vivlog` exists and reads the SQLite audit database.
4. `ward` exists and exchanges MUS frames with `keeperd`.
5. MUS framing, routing, SeqNo enforcement, identity stamping, and byte-rate limiting exist.
6. `vivgen` exists and generates capability schemas/types from `capabilities/*.kdl`.
7. `Filesystem_File_Write` exists with strong path and symlink protections.
8. Browser/CDP plumbing exists in substantial form.
9. Nix flake packaging for base/runtime images exists.

**Remaining MVP deliverables:**

1. Keep Ward's now-wired schema validation and malformed-call failure reporting covered by prompt-run tests so the prompt -> tool -> capability response -> completion path stays fully coherent.
2. Close the remaining browser mediation gaps: document/profile isolation clarity and full prompt-run allow/deny coverage against the shipped proxy path.
3. Finish `keeperd` runtime state and CLI/TUI status parity so operator views are fully truthful and consistent across both surfaces.
4. Finish audit payload sensitivity policy so stored payload behavior matches capability categories beyond the currently wired per-capability hook.
5. Make provisioning behavior safe and predictable on Linux.
6. Expand test coverage from unit-level pieces to real MVP end-to-end assertions.

**MVP non-goals:**

- multi-agent routing, multicast, or broadcast
- topology governance (`can-invoke`, `max-depth`, `max-spawns`)
- REST gateway families beyond the minimal runtime demonstration
- self-evolution, snapshot governance, or rollback automation
- broad provider/plugin abstractions beyond what is needed for one clear runtime path

---

## Phase 1: Foundation

### 1.1 `distrobuild` (Nix Flake)

**Already done:**

- Flake exists and builds base/runtime images.
- Runtime image includes `keeperd`, `viv`, `vivlog`, `ward`, and `cap-cli`.
- Chrome stays outside the guest image.

**Remaining work:**

- Validate the image boots identically on Linux (native LXD), macOS (OrbStack/Lima), and Windows 11 (WSL2).
- Document the operational path that replaces the original standalone `distrobuild` script wording.
- Configure and verify `/etc/subuid` and `/etc/subgid` with sufficient range for later multi-agent work.
- Add a smoke test for image contents so runtime artifacts and expected binary paths do not drift.

### 1.2 Keeper Daemon (`keeperd`)

**Already done:**

- Go daemon startup, ctl socket listener, audit DB wiring, router wiring, capability dispatcher, and Chrome sidecar launch exist.
- Config loading has moved into `internal/config`.
- `kdl-go` is in use for orchestrator and agent config parsing.

**Remaining work:**

- Keep `cmd/keeperd` as wiring code only; continue moving runtime-specific logic into internal packages where it improves clarity.
- Tighten config validation at parse time: required fields, naming conventions, uniqueness, enum validation, and clearer operator-facing errors.
- Replace the still-manual provider tree walk with a stricter typed validation path.
- Extend the current keeper-owned runtime state from prompt seq / last event / outcome / token / cost summaries to include distinct last-completion and last-failure detail where that materially improves operator debugging.
- Keep ctl responses sourced from keeper-owned runtime state, not reconstructed from best-effort client-side event history.
- Keep semantic enforcement in `keeperd`; do not let policy leak into Ward or browser helpers.

### 1.3 BubbleTea TUI (`viv`)

**Already done:**

- Separate binary exists.
- CLI commands for status, prompt dispatch, ping, and agent lifecycle exist.
- TUI subscribe/status flow and event rendering exist.
- CLI status now renders keeper-owned prompt/event/outcome/token/cost/tool-call fields and has snapshot coverage.

**Remaining work:**

- Make the TUI detail/status views match the CLI on field meanings, field coverage, and fallback behavior.
- Keep the CLI surface as important as the TUI: every MVP operator action should stay available without fullscreen UI.
- Add matching TUI/status rendering coverage so keeper-owned runtime fields cannot drift between surfaces.

### 1.4 Agent Workspace Provisioning

**Already done:**

- `ContainerRuntime` interface exists.
- Linux and stub runtimes exist.
- Provisioning uses a cleanup stack for create-time rollback.

**Remaining work:**

- Keep Linux provisioning correctness as a top MVP priority: nspawn argument construction, cleanup ordering, runtime registration, and shutdown behavior should be verified on real Linux.
- Treat the direct Ward subprocess path on non-Linux as a development/testing fallback only, not as the target runtime story.
- Move agent ACL creation away from hard-coded default grants and toward config-driven installation from validated agent policy.
- Make nftables setup and teardown reliable; decide which failures are fatal versus degraded but acceptable.
- Add tests around partial-failure cleanup for subvolume creation, process spawn, and network rule application.
- Make agent destroy symmetric with create, including teardown of network rules, process lifetime, router registration, and ACL removal.

---

## Phase 2: Runtime Control Plane

### 2.1 MUS Codec

**Already done:**

- Shared MUS framing and codec exist.
- `pkg/mus` provides public binary encoding/decoding helpers.
- All core communications (viv ↔ keeperd, keeperd ↔ Ward) are fully binary MUS encoded.
- Capability argument structs and control plane payloads have binary MUS marshallers.
- Round-trip and malformed-frame tests exist.

**Remaining work:**

- Make the binary MUS choice explicit in docs and code comments everywhere the protocol is introduced.
- Fuzz `UnmarshalMUS` with random byte sequences so malformed frames never panic `keeperd`.
- Benchmark framing/decoding and capture operator-visible latency for prompt dispatch and capability round-trips.
- Handle SeqNo restart/wrap behavior more gracefully than simple rewind denial.
- Benchmark the `io.MultiReader(peek, pipe)` read path against a buffered alternative and switch only if it measurably improves allocations or clarity.

### 2.2 Pipe Router

**Already done:**

- Identity stamping, SeqNo enforcement, and byte-rate limiting exist.
- Router tests cover the core security invariants.
- ctl and Ward traffic share the same frame codec and binary message model.

**Remaining work:**

- Decide explicitly whether ctl should share the same router implementation as Ward traffic or only the same codec/message model; choose the simpler MVP implementation.
- If ctl stays on a separate handler path, keep a small shared frame-handling layer so behavior cannot drift.
- Add tests for payload-too-large/operator-visible error handling where the codec rejects frames.

### 2.3 Ward Binary

**Already done:**

- Standalone Ward binary exists.
- Per-prompt subprocess execution exists.
- Loop detection exists.
- Tool socket server for capability CLIs exists.
- Completion and failure events exist.
- Generated schemas and binary MUS marshallers are available inside Ward.
- Schema validation and malformed/schema-invalid failure signaling are implemented and covered at the tool and prompt-abort levels.

**Remaining work:**

- Keep the current Claude CLI -> Ward tool socket -> keeperd capability path as the single MVP execution path and avoid reintroducing overlapping mechanisms.
- Keep the Ward validation/failure path stable and extend it to broader prompt-run coverage.
- Introduce one normalized internal event shape inside Ward so backend-specific parsing stays behind a very small adapter boundary.
- Keep schema synchronization through `vivgen` as the only source of truth; do not reintroduce hard-coded schema copies.
- If a future `AgentCLI` interface is added, keep it thin and justified by actual need, not speculative provider generalization.

### 2.4 Debug Tooling

**Already done:**

- `vivlog tail`, `show`, `grep`, and `decode` exist.
- `vivlog decode` supports full binary MUS decoding for all message types.
- SQLite audit queries and decode helpers exist.

**Remaining work:**

- Add filtering by time range and event type where it materially improves operator debugging.
- Add tests covering stable machine-readable output modes.
- Make sure `vivlog` stays sufficient for protocol bring-up, ACL debugging, and prompt-run inspection before richer UI work lands.

### 2.5 Migration System (OpenClaw -> VIVARY)

**Already done:**

- `internal/migrate/openclaw.go` implements v0 discovery (artifacts, channels, plugins, redacted credentials).
- `viv migrate openclaw inspect` CLI command exists.
- Redaction and portability classification rules are implemented.

**Remaining work:**

- **Phase 1: Discovery TUI.** Build a read-only Bubble Tea explorer for `findings.kdl` inside `viv`.
- **Phase 2: Plan Builder.** Implement decision capture (approve/defer/resolve) and `plan.kdl` generation.
- **Phase 3: Apply & Verification.** Implement dry-run and live import (prompts, macros, schedules).
- **Capability Stub Generator:** Add a command/TUI helper to generate Go/KDL boilerplate for `unsupported` plugins.
- **Entity Inference:** Automate the mapping of plugin-level configuration to VIVARY `entity` scope constraints (path-prefix, domain-suffix).

---

### Phase 3: Minimal Capability Surface

### 3.1 `vivgen` Tool

**Already done:**

- `vivgen` loads `capabilities/*.kdl` and generates Go types, schemas, and a registry.
- Backward-compat tests exist.

**Remaining work:**

- Continue tightening generator/linter rules: Namespace_Noun_Verb naming, descriptions/examples/enums, snake_case JSON keys, and cross-file consistency.
- Generate or validate the central capability registry used by `keeperd`, not just Ward.
- Keep generated output stability covered by contract/golden tests.
- Ensure generated artifacts are part of the reproducible runtime build path.

#### `TestVivgen` coverage

- **Already present in part:** KDL load success, some failure cases, schema generation, type generation, registry generation, and backward-compat checks.
- **Still needed:** fuller duplicate/unknown-reference coverage, stronger metadata lint coverage, and representative golden output stability checks if current tests do not fully pin output shape.

### 3.2 Chrome Sidecar & Whitelisting Proxy

#### 3.2a Browser Launch and Profile Isolation

- `keeperd` already launches Chromium on the host side in a non-fatal way.
- Make profile isolation match the docs exactly or simplify the docs to the implementation actually shipped.
- Do not leave agent browser context/profile isolation ambiguous.

#### 3.2b Whitelisting Proxy

- Keep capability-layer whitelist checks and the now-wired proxy-layer whitelist enforcement aligned as defence-in-depth.
- Browser allow/deny coverage now exists at the proxy layer, capability->proxy handoff layer, and keeperd response/audit layer; extend it to broader prompt-run assertions so the shipped path stays obviously correct.
- Emit clear `capability_denied`/security records for blocked browser targets.

#### 3.2c `Browser_Page_Read`

- Keep `Browser_Page_Read` as the only browser capability in MVP.
- Ensure navigation, wait strategy, and readable text extraction are deterministic enough for testing.
- Keep explicit allow/deny integration coverage in place before expanding browser surface area; remaining gaps are full prompt-run and live-Chrome assertions.
- Make failure behavior legible when Chrome is unavailable, the target is denied, or extraction fails.

### 3.3 Filesystem Output Capability

- `Filesystem_File_Write` is already implemented with good path safety.
- Keep scope constraints limited to a configured writable prefix such as `output/`.
- Add explicit operator-observable audit assertions for allowed and denied writes.
- Keep deny behavior for traversal, absolute paths, and symlink escapes covered by tests.

### 3.4 Logging Infrastructure

**Already done:**

- SQLite frames table exists.
- Completion and failure event payloads exist.
- `vivlog` exists.
- Audit helpers can now query security-event records directly in tests/tooling.

**Remaining work:**

- Keep the current keeper-side `Capability.AuditPayload()` hook as the source of truth for whether request payloads are stored.
- Default high-sensitivity capability categories (`Email`, `Messaging`, `Document`, `Database`, `Credential`) to `audit-payload false` as those capability families land.
- Keep payload policy small and explicit for MVP; retention/pruning remains post-MVP unless usage forces it sooner.
- Add tests proving payload omission/storage behavior for both allow and deny paths.
- Ensure all operator-visible event kinds have stable names and payload shapes.

### 3.5 ECS Scope Model

- The codebase now has an initial compatibility bridge: legacy `Scope string` plus structured `ScopeConstraint`.
- For MVP, finish a small explicit typed-scope model for the built-in capabilities rather than forcing the full ECS design everywhere at once.
- Migrate browser scope to typed domain/domain-suffix constraints.
- Migrate filesystem scope to typed path-prefix constraints.
- Keep the raw-string path only as a compatibility shim until built-ins no longer depend on it.
- Defer the full schedule/rate/approval-rich ECS policy realization until the MVP core is stable unless one of those policies becomes necessary for the current runtime slice.

### 3.6 MVP Consolidation

This is now the most important section of the plan.

- Keep browser mediation obviously correct by preserving the proxy whitelist checks already in place and extending the remaining prompt-run/live-Chrome integration coverage before adding more capability families.
- Make provisioning safe and recoverable: cleanup-on-failure, correct runtime registration, predictable Linux behavior.
- Make ctl/event semantics explicit and stable so the TUI, CLI, and audit log all agree on message behavior.
- Finish keeper-owned status details and CLI/TUI parity on top of the existing prompt/event/cost state.
- Keep Ward malformed-call validation/failure handling on the single clear runtime path and extend coverage to the broader end-to-end prompt flow.
- Keep the runtime single-agent and boring until these pieces are solid.

### 3.7 Approval and Policy Gating

Approval is documented in the design but not yet implemented.

- Decide whether approval is required for the MVP exit slice.
- If yes, implement one narrow approval path only: ctl subscriber notification, grant/deny response, timeout handling, and audit emission.
- If no, document approval as explicitly deferred and remove any implication that it is already part of the MVP runtime claim.
- Do not partially implement multiple approval targets before the ctl path is real.

---

## Phase 4: MVP Exit Testing

Before adding multi-agent orchestration, VIVARY must pass an explicit runtime-core test gate. The goal is to prove that the MVP runtime is understandable, inspectable, and operationally trustworthy.

### 4.1 Unit Tests

| Suite | Coverage |
|---|---|
| `TestMUSCodec` | Round-trip marshal/unmarshal for all MVP MsgTypes; fuzz `UnmarshalMUS`; benchmark allocations per frame. |
| `TestCtlSocket` | Subscribe, status, prompt dispatch, approval messages if enabled, and agent lifecycle commands round-trip correctly. |
| `TestRuntimeRouter` | Identity stamping, `SeqNo` enforcement, byte-rate limiting, and oversize/malformed frame handling. |
| `TestCapabilityACL` | Authorized verbs permitted; unauthorized dropped with security log event; `Browser_Page_Read` and `Filesystem_File_Write` scope checks enforced. |
| `TestVivgen` | Generated schema/registry/type output remains correct and lint rules reject bad definitions. |
| `TestWardLoop` | Loop detection triggers at threshold; malformed or schema-invalid tool call emits correct failure event; subprocess crash emits correct failure event. |
| `TestChromeProxy` | Whitelisted domain forwarded; unlisted domain denied in the proxy layer; readable text extraction returns normalized content. |
| `TestFilesystemWrite` | Allowed writes succeed; path traversal, symlink escape, and absolute path writes are denied and logged. |
| `TestVivaryLog` | WAL decoding, filtering, payload omission, and raw record inspection behave consistently across output modes. |
| `TestKeeperRuntimeState` | Status snapshots reflect prompt seq, last event, and cost/token state accurately after runtime activity. |

### 4.2 End-to-End Tests

| Test | Assertion |
|---|---|
| Environment parity | `keeperd` boots and passes health check on Linux, macOS (OrbStack/Lima), and Windows (WSL2). |
| TUI/CLI connection | `viv` connects to `keeper.sock`, receives live Completion/Failure events, and can inspect state without polling. |
| Prompt run | A prompt reaches the Ward, spawns the LLM subprocess, converts well-formed tool calls into capability requests, executes allowed tools, returns tool results correctly, and emits a completion event. |
| Filesystem isolation | Agent attempts write to `/etc` and read of keeper host files - both fail. |
| Network isolation | Agent attempts TCP connection to an arbitrary external host - nftables drops it. |
| Browser whitelist allow | Agent requests `Browser_Page_Read` for a whitelisted URL; content returned. |
| Browser whitelist deny | Agent requests `Browser_Page_Read` for an unlisted URL; denial logged and response refused. |
| Scoped file write allow | Agent writes to allowed `output/` path; content appears in its subvolume and audit trail records the action. |
| Scoped file write deny | Agent attempts traversal or disallowed path write; request denied and recorded. |
| Pipe flood resilience | Agent floods stdout with garbage; frames are dropped, event logged, and `keeperd` remains responsive. |
| Audit/debug workflow | Operator can use `vivlog` commands to locate a specific prompt run, inspect associated events, and decode the relevant frame. |
| Provisioning cleanup | A failed agent create leaves no stale router pipe, ACL entry, subvolume, or nftables table behind. |

### 4.3 Exit Criteria

- Single-agent runtime is stable across supported host environments.
- Policy denials are understandable from CLI/TUI output and audit logs.
- Operators can debug capability failures and prompt runs using `vivlog` without bespoke tooling.
- Browser and filesystem capability boundaries are enforced in the layers the docs claim they are enforced in.
- The MVP demonstrates clear value as a capability-restricted, isolated, auditable runtime even with no swarm features enabled.

---

## Phase 5: Multi-Agent Orchestration

Only after the MVP exit tests pass do we add swarm concerns.

### 5.1 Swarm Routing and Topology

- Extend the router from MVP request/response routing to unicast, multicast (`group:` prefix), and broadcast (`*`) routing.
- Add topology governance: `can-invoke`, `max-concurrent`, `max-depth`, and `max-agents`.
- Add `ParentID`/`Depth` enforcement for subagent invocation chains.
- Implement deliberate UID/subuid allocation and tracking as described in the design once multi-agent runtime identity isolation matters operationally.

### 5.2 Multi-Agent Operator UX

- Expand the TUI from the MVP detail view to matrix/fleet views.
- Add operator workflows for subagent lifecycle, group visibility, and invocation tracing.
- Extend `vivlog` with correlation views across parent/child agent runs.

### 5.3 Credential Vault

- AES-256-GCM encrypted store in `keeperd`.
- Agents use a `credential_id`; `keeperd` resolves the secret only at gateway invocation time.
- Secret passed to REST gateway binaries via environment variable, never via MUS or agent filesystem.
- `viv vault add|rotate|list` commands sent as ctl messages to `keeperd`.
- Once the vault is real, re-evaluate SQLCipher or equivalent encrypted-at-rest storage for audit/vault data.

### 5.4 REST API Gateway Binaries

- Separate Go binary per API family.
- Initial set: `Spreadsheet_Range_Read`, `Spreadsheet_Cell_Update`, `Email_Message_List`, `Email_Message_Read`, `Calendar_Event_List`, `Calendar_Event_Create`.
- Response normalization strips formatting metadata before returning to the agent.
- Vendor-neutral capability names resolve to the appropriate provider based on assigned credentials.

---

## Later Phase: Runtime Evolution and Snapshot Governance

- Agent-driven self-evolution remains out of MVP and out of the first multi-agent phase.
- If introduced later, it should come only after the runtime core and swarm governance layers are operationally proven.
- Snapshot-based reconfiguration should begin with operator-directed changes before any agent-directed proposal mechanism is added.

### Full Telemetry Hook

- All switchboard frames, including ctl messages, should remain appendable to the audit trail at the router/control-plane boundary.

---

## Future Investigations (not yet scheduled)

These items are worth tracking but are not committed to any phase yet.

### Switchboard Identity Encoding

Investigate switching MUS `SwarmHeader` identities from varlen UTF-8 strings to fixed `uint32` values once the runtime has a clear, stable identity-mapping story.

Constraints for any future investigation:

- keep the operator-facing/audit-visible identity model legible; human-meaningful agent IDs should not disappear from logs and tooling
- define reserved control-plane IDs explicitly (`ctl`, `keeper`) before changing the wire format
- do not introduce ad hoc or non-deterministic string->ID mapping that could make routing or debugging harder
- treat this as a wire-size/throughput optimization, not MVP-critical functionality

### MCP Tool Protocol

Investigate whether Ward can act as a Model Context Protocol (MCP) host, exposing capability CLIs as MCP tools rather than relying on the current bespoke integration path.

Constraints for any future investigation:

- it must not weaken the `keeperd` enforcement boundary
- it must not route capability requests around `keeperd`
- it should only be evaluated after the MVP runtime path is already crisp and tested

---

## Ordered MVP Checklist

If the goal is to finish the MVP cleanly, work should happen in this order:

1. Keep Ward's single clear prompt/tool execution path crisp.
   - preserve the chosen prompt -> tool -> result -> completion flow
   - avoid reintroducing overlapping or half-implemented execution branches
   - extend prompt-run tests around the existing schema/failure events for malformed tool calls
2. Extend browser mediation validation and test coverage.
   - keep whitelist policy enforced in both the capability layer and proxy layer
   - make profile/context isolation match the docs or simplify the docs
   - add the remaining broader allow/deny prompt-run and live-Chrome integration tests around the shipped path
3. Make `keeperd` runtime state/status authoritative.
   - keep tracking prompt seq, last event, outcome, cost, token counts, and tool-call counts in keeper-owned state
   - extend/return the remaining status details consistently to CLI/TUI callers and keep CLI/TUI rendering aligned
4. Implement real audit payload policy.
   - keep `Capability.AuditPayload()` wired into keeper-side storage decisions
   - add coverage for stored vs omitted payload behavior as non-audited capability families land
5. Harden Linux provisioning behavior and cleanup guarantees.
   - verify create/destroy symmetry on real Linux
   - prove partial-failure cleanup for runtime/network/subvolume setup
6. Close the most important MVP end-to-end test gaps.
   - prompt run
   - browser allow/deny
   - scoped filesystem allow/deny
   - provisioning cleanup
   - audit/debug workflow
