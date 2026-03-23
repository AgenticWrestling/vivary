# VIVARY Codebase Notes

These notes compare the current codebase to `docs/DESIGN.md` and `docs/PLAN.md` and focus on what is actually implemented now.

## Overall Read

The repository has a real MVP-shaped runtime core in place:

- `keeperd`, `viv`, `vivlog`, `ward`, and `vivgen` all exist
- the ctl socket, MUS framing, router, audit DB, and basic CLI/TUI loop are implemented
- the two main MVP capabilities exist in code: `Browser_Page_Read` and `Filesystem_File_Write`
- tests cover the codec/router, audit DB, TUI decoding, ctl socket basics, `vivgen`, Ward loop detection, browser plumbing, and filesystem guards

The biggest gaps are not missing binaries; they are incomplete enforcement and incomplete end-to-end behavior in a few key places:

- approval flow is still a stub
- `keeperd` runtime status is thinner than the docs describe
- provisioning is abstracted better now, but policy/config are still much simpler than the docs
- browser whitelist enforcement is strong in the capability layer but not yet real in the proxy layer
- audit payload policy is still stubbed on the `keeperd` side
- Ward still has a partially-complete prompt/tool execution path

## Current Status Against `docs/PLAN.md`

### MVP deliverables

#### Implemented

- `keeperd` exists and exposes a MUS-framed Unix socket control plane via `keeper.sock`; see `cmd/keeperd/main.go` and `internal/switchboard/codec.go`
- `viv` exists as a separate CLI/TUI binary and speaks the ctl protocol; see `cmd/viv/main.go` and `cmd/viv/tui.go`
- `vivlog` exists and reads the SQLite audit DB; see `cmd/vivlog/main.go`
- `ward` exists as a separate binary and emits capability/completion/failure frames; see `cmd/ward/main.go`
- the Nix flake and runtime/base image packaging exist; see `flake.nix`, `nix/modules/distro-base.nix`, and `nix/modules/distro-runtime.nix`
- `Filesystem_File_Write` is implemented with traversal, absolute-path, and symlink-escape protection; see `internal/capabilities/filesystem.go` and `internal/capabilities/capability_test.go`
- SQLite-backed audit logging is implemented; see `internal/audit/audit.go`

#### Partially implemented

- single-agent provisioning exists and now uses a `ContainerRuntime` abstraction plus cleanup stack, but the full documented nspawn/bind-mount/network lifecycle is still simplified; see `cmd/keeperd/provisioning.go` and `internal/runtime/runtime.go`
- `Browser_Page_Read` exists end-to-end in code shape, including Chrome launch and CDP access, but the enforcement story is not yet as tight as the plan claims; see `internal/capabilities/browser.go`, `internal/chromproxy/proxy.go`, and `cmd/keeperd/chrome.go`
- `vivgen` exists and generates schemas/types/registry from `capabilities/*.kdl`, but `keeperd` still manually registers only the two built-in capability implementations; see `cmd/vivgen/main.go` and `cmd/keeperd/main.go`
- config loading has been moved into `internal/config` and now uses `kdl-go`, but it still only models a reduced config surface and minimal validation; see `internal/config/config.go`

#### Not implemented yet

- approval handling is still explicitly stubbed in `keeperd`; see `cmd/keeperd/main.go:328`
- vault flows are not present
- robust end-to-end exit-test coverage from the plan is not present

### Phase 1: Foundation

#### 1.1 `distrobuild`

- Implemented in flake form rather than as a standalone script: the flake builds base and runtime LXD image artifacts; see `flake.nix`
- The runtime image includes `keeperd`, `viv`, `vivlog`, `ward`, and `cap-cli`
- Chrome is kept out of the guest image, matching the docs
- Cross-platform validation and the broader operational story described in the plan are not evident in the codebase itself

#### 1.2 `keeperd`

- Implemented: daemon startup, ctl socket listener, audit DB, router, dispatcher, Chrome sidecar launch, and agent map; see `cmd/keeperd/main.go`
- Implemented: config loading moved behind `internal/config`
- Implemented: `kdl-go` is now in use for orchestrator and agent config parsing
- Partial: provider parsing still walks the parsed KDL tree manually instead of using a fully-typed validation layer; see `internal/config/config.go:82`
- Partial: runtime state is still small, but smaller than planned; `agentState` only stores ID, subvolume path, config, and pipe, not last prompt/event/cost state

#### 1.3 `viv`

- Implemented: separate binary, status/list/prompt/ping/agent lifecycle CLI surface; see `cmd/viv/main.go`
- Implemented: TUI subscribe/status flow and local display of completion/failure data; see `cmd/viv/tui.go`
- Partial: the TUI can show last completion/failure once events arrive, but `keeperd` status snapshots do not yet carry authoritative last prompt/event/cost fields

#### 1.4 Agent workspace provisioning

- Implemented: `ContainerRuntime` abstraction with Linux and stub runtimes; see `internal/runtime/runtime.go`
- Implemented: cleanup stack for create failures; see `cmd/keeperd/provisioning.go:57`
- Implemented: non-Linux stub runtime fallback
- Partial: ACL installation during create is still hard-coded to the two MVP capabilities and default scopes rather than driven by `agent.kdl`; see `cmd/keeperd/provisioning.go:103`
- Partial: nftables rules are applied, but failure is only logged as non-fatal; see `cmd/keeperd/provisioning.go:188`
- Partial: UID/subuid allocation, explicit host-level range tracking, and full documented bind-mount/provisioning rigor are not implemented

### Phase 2: Runtime control plane

#### 2.1 MUS codec

- Implemented: shared MUS-like framing, varint/string encoding, frame read/write helpers, payload limits, and codec tests; see `internal/switchboard/codec.go` and `internal/switchboard/codec_test.go`
- Implemented: the code is explicit that payloads are usually JSON carried inside the frame
- Not implemented: the richer header from `DESIGN.md` (`ParentID`, `Depth`) does not exist
- Not evident: fuzzing/benchmarking from the plan is not present in the current test suite

#### 2.2 Pipe router

- Implemented: one-router model for Ward pipes with identity stamping, SeqNo monotonicity, and byte-rate limiting; see `internal/switchboard/router.go`
- Implemented: router tests cover identity overwrite, `pipe_flood`, and seq rewind; see `internal/switchboard/router_test.go`
- Partial: ctl traffic uses the same frame codec and message model, but not the same router path; ctl is handled separately in `cmd/keeperd/main.go`

#### 2.3 Ward binary

- Implemented: separate Ward binary, per-prompt subprocess model, completion/failure events, loop detection, and capability request/response plumbing; see `cmd/ward/main.go`
- Implemented: generated capability schemas are loaded via `capabilities.GeneratedRegistry()`
- Implemented: a Unix-socket tool server exists for capability CLI binaries; see `cmd/ward/toolserver.go`
- Partial: the backend path is still hard-coded to Claude in `runLLMSubprocess`; the planned `AgentCLI` abstraction is not present
- Partial: Ward parses Claude `stream-json` events directly and still contains an explicit TODO about injecting tool results back into the subprocess; see `cmd/ward/main.go:333`
- Partial: Ward schema use is real for schema lookup/help, but the overall prompt -> tool -> tool-result loop is not yet as crisp as the design describes

#### 2.4 Debug tooling

- Implemented: `vivlog tail`, `show`, `grep`, and `decode`; see `cmd/vivlog/main.go`
- Implemented: audit DB query helpers and decode helpers; see `internal/audit/audit.go`

### Phase 3: Minimal capability surface

#### 3.1 `vivgen`

- Implemented: KDL load, schema generation, Go type generation, registry generation, and backward-compat tests; see `cmd/vivgen/main.go`, `cmd/vivgen/kdl_test.go`, `cmd/vivgen/typegen_contract_test.go`, and `cmd/vivgen/backward_compat_test.go`
- Partial: `vivgen` is clearly real now, but not all of the planned linting/consistency rules are visible yet
- Partial: generated artifacts are used by Ward, but `keeperd` capability registration is still manual

#### 3.2 Chrome sidecar and whitelist proxy

- Implemented: Chrome sidecar launch exists and is non-fatal if unavailable; see `cmd/keeperd/chrome.go`
- Implemented: CDP navigation, network-idle wait, and accessibility-tree/innerText extraction exist; see `internal/chromproxy/proxy.go`
- Partial: the code comments claim per-agent isolation, but the startup path launches Chrome with one default user-data-dir and the proxy reuses one target per agent in-process; the full per-agent profile story described in the docs is not actually implemented
- Important gap: proxy-layer whitelist enforcement is effectively stubbed because `scope(agentID)` returns `""` and `urlInScope(..., "")` allows all URLs; the real deny check currently happens in the capability layer, not the proxy layer; see `internal/chromproxy/proxy.go:57` and `internal/chromproxy/proxy.go:366`

#### 3.3 Filesystem output capability

- Implemented well for MVP: scoped writes, deny traversal, deny absolute paths, deny symlink escapes, append mode, and tests; see `internal/capabilities/filesystem.go` and `internal/capabilities/capability_test.go`
- Partial: audit records exist for frame flow and security events, but there is no separate capability-specific write-attempt audit record beyond the normal frame/security logging

#### 3.4 Logging infrastructure

- Implemented: SQLite frames table and security events table; see `internal/audit/audit.go`
- Implemented: completion and failure event payload types and round-trip helpers
- Implemented: `vivlog` inspection surface
- Not implemented: `shouldAuditPayload` in `keeperd` still always returns `true`, so the documented per-capability payload suppression is not active; see `cmd/keeperd/dispatch.go:157`
- Not implemented: high-sensitivity category defaults are not enforced even though `Capability` already exposes `AuditPayload()`

#### 3.5 ECS scope model

- Partially implemented: `ACLEntry` now has both legacy `Scope string` and structured `Constraints []ScopeConstraint`; see `internal/capabilities/capability.go`
- Partially implemented: browser and filesystem capability code can read ECS-style constraints as a compatibility path
- Not implemented: config parsing, policy loading, schedules, rates, approvals, and most of the full ECS resource model described in the docs are not yet wired through

#### 3.6 MVP consolidation

- Partially achieved: config loading and provisioning abstraction are noticeably further along than older notes would suggest
- Still incomplete: browser mediation correctness, audit payload policy, authoritative runtime state, and Ward simplification remain open MVP consolidation items

## Current Status Against `docs/DESIGN.md`

### Strong matches

- `keeperd` is the semantic authority and `ward` is kept as the boundary adapter; see `cmd/keeperd/main.go` and `cmd/ward/main.go`
- the codebase is still clearly MVP-first and single-agent-first
- ctl and Ward share the same frame format and message model
- `keeperd` stamps identity on inbound Ward frames and enforces SeqNo monotonicity
- completion and failure events are first-class runtime concepts
- CLI-first debug tooling is real

### Implemented in a simpler MVP form

- the wire header is smaller than the design header: `Version`, `Type`, `FromID`, `ToID`, `SeqNo`, `PayloadLen`; no `ParentID` or `Depth`
- payloads are `MUS header + JSON payload`, not a fully typed MUS payload system
- config and policy are much flatter than the design's full ECS/schedule/rate/approval model
- ctl message coverage is smaller than the design table: subscribe/status/agent create/destroy/list/prompt/ping exist; vault and approval push flows do not

### Important divergences

- the approval flow described in the design is not implemented; the ctl approval message exists but returns a stub error
- `keeperd` status snapshots do not yet reflect the richer runtime state the design describes; `LastPromptSeq` and `LastEventAt` exist in payload structs but are not populated from daemon state
- browser whitelist enforcement is not yet duplicated correctly at the proxy layer, despite the design calling for a whitelisting proxy as an enforcement boundary
- the Chrome isolation story in code is looser than the design text; one shared Chrome process is launched with one default user-data-dir
- the design describes capability CLIs as the normal LLM-facing path, but Ward still also contains a direct Claude `tool_use` stream path with an unfinished tool-result TODO
- the design's approval, rate, schedule, vault, and audit-payload policy layers are still mostly future-facing relative to the code

## Testing Read

The repo has meaningful unit coverage for the pieces that already exist:

- router/codec tests cover core protocol invariants
- capability tests cover filesystem security checks and browser scope matching
- `vivgen` has real parser/generator/backward-compat tests
- ctl socket tests cover ping, status, subscribe, list, and identity rejection
- TUI tests cover status/completion/failure rendering and prompt dispatch
- audit tests cover write/query/decode basics

What is still notably behind the docs:

- there is little evidence of full end-to-end runtime tests for prompt execution through a real LLM subprocess
- browser allow/deny integration coverage is still weaker than the docs call for
- environment parity and isolation exit tests are not present in the current tree

## Bottom Line

The codebase now implements most of the MVP skeleton described in `docs/PLAN.md`: the binaries exist, the transport exists, the audit path exists, the filesystem capability is solid, and the browser path exists in substantial form.

The remaining gap is mostly about tightening behavior, not inventing new architecture:

- make Ward's prompt/tool path fully complete
- make proxy-layer browser enforcement real
- make audit payload policy real
- make `keeperd` runtime state/status authoritative
- finish approval/policy features only if they are required for the MVP slice being exercised now
