# VIVARY Implementation Critique

_Claude Code analysis — 2026-03-23. Compares actual implementation against DESIGN.md and PLAN.md._

---

## 1. Critical Blockers — MVP Path Cannot Close Without These

### 1.1 Ward Tool-Result Protocol Is Incomplete (BLOCKER)

`cmd/ward/main.go:runLLMSubprocess()` contains an explicit TODO:

```go
// TODO(Phase 2): inject toolResp back into claude subprocess via the MCP tool-result protocol
```

This means the prompt → tool call → capability response → tool result → completion path is broken. The Ward can receive a `CapabilityResponse` from `keeperd` but **cannot inject the result back into the Claude subprocess**. The LLM never sees tool results, so it can never reach a completion. This is PLAN.md MVP Checklist item #1 and the single most important gap in the entire codebase.

**Fix:** Implement the bidirectional tool-result injection before any other Ward work. The subprocess must receive tool results, not just emit tool calls.

### 1.2 `shouldAuditPayload` Is a Stub

The audit layer does not call `Capability.AuditPayload()`. High-sensitivity categories (`Email`, `Messaging`, `Document`, `Database`, `Credential`) are not defaulting to `audit-payload false`. Every frame payload is being stored unconditionally.

PLAN.md §3.4 marks this as a remaining MVP deliverable. Until it is wired, PII from sensitive capability calls lands in the SQLite WAL with no policy control.

### 1.3 Browser Proxy Has an Allow-All Fallback

PLAN.md §3.2b explicitly flags this: _"Remove the current effective allow-all fallback in the proxy's local scope check path."_ The `chromproxy` package has a `WhitelistPolicy` struct but the proxy-layer enforcement appears to fall through to permissive behavior when local scope checks don't match. The capability layer (ACL/scope) is the only real enforcement point today. Defence-in-depth requires the proxy layer to also enforce before any CDP traffic is sent.

---

## 2. Functional Completeness Against DESIGN.md

### 2.1 Ward Execution Path (DESIGN.md §2)

| Design requirement | Status |
|---|---|
| Per-prompt subprocess model | Implemented |
| Spawn `claude --headless` | Implemented |
| Tool invocation interception | Implemented |
| Schema validation before forwarding | Implemented (validated against generated schemas) |
| MUS encoding of CapabilityRequest | Implemented |
| CapabilityResponse decoded | Implemented |
| Tool result injected into LLM subprocess | **NOT IMPLEMENTED** (see 1.1) |
| CompletionEvent emitted on subprocess exit | Implemented |
| FailureEvent for known failure modes | Implemented |
| `AgentCLI` interface (thin adapter) | Present — but check if it has grown beyond justified scope |

The execution path is a ring with one gap. Every stage works except the return leg from keeperd back into the LLM subprocess.

### 2.2 keeperd Runtime State (DESIGN.md §3, PLAN.md §1.2)

`viv status` and the TUI pull from keeperd, but keeperd does not track:
- last prompt `SeqNo`
- last completion event
- last failure event
- last event timestamp
- input/output token counts
- cost (USD)
- `context_window_used_pct`

PLAN.md §1.2 lists this as remaining work: _"Make `keeperd` runtime state explicit and authoritative."_ Currently status snapshots return sparse agent records. Until this is resolved, the CLI and TUI output are unreliable for operators debugging a run in progress.

### 2.3 Filesystem Capabilities (DESIGN.md §6b / PLAN.md §3.3)

`Filesystem_File_Write` is implemented with path safety (no absolute paths, no `..` traversal, symlink escape check). **Good.** However:

- `Filesystem_File_Read` does not exist. The design anticipates a writable output scope with readable content for the LLM — but there is no read capability registered.
- Audit assertion tests for allowed/denied writes do not exist (PLAN.md §3.3 explicitly calls for these).

### 2.4 Browser Capabilities (DESIGN.md §6a / PLAN.md §3.2)

- `Browser_Page_Read` is the intended MVP capability. It is registered and the CDP proxy wires up. **Good.**
- `Browser_Page_Screenshot` and `Browser_Page_GetTitle` and `Browser_Form_Submit` are defined in the KDL but should not exist in the registered capability set for MVP. PLAN.md §3.2c: _"Keep `Browser_Page_Read` as the only browser capability in MVP."_ Having unimplemented capabilities registered creates false impressions to operators and wastes schema validation surface.
- `args.Timeout` is accepted by `Browser_Page_Read` but silently ignored — hardcoded 30s is used instead. Silent behavioral divergence from declared schema.
- Per-agent browser profile isolation is **not implemented** (DESIGN.md §6a already acknowledges this — make sure DESIGN.md stays accurate).

### 2.5 Container Runtime / Provisioning (DESIGN.md §1, PLAN.md §1.4)

`LinuxRuntime` and `StubRuntime` implement `ContainerRuntime`. The nftables methods (`ApplyNetworkRules`, `RemoveNetworkRules`) are present as thin wrappers around `nft`/`machinectl` commands, but:

- **Destroy is not symmetric with create** — PLAN.md §1.4: _"Make agent destroy symmetric with create, including teardown of network rules, process lifetime, router registration, and ACL removal."_ Need to audit whether all resources are fully cleaned up on destroy.
- **Partial-failure cleanup** — the provisioning cleanup stack exists but coverage of subvolume + process + network rule partial failures is not tested.
- **ACL creation is still hard-coded default grants**, not config-driven from validated agent policy (PLAN.md §1.4).

### 2.6 Config Validation (PLAN.md §1.2)

Current validation at parse time is minimal — it checks agent ID uniqueness. Missing:

- Required field presence checks
- Naming convention enforcement (agent IDs, capability names)
- Enum validation for provider names, capability categories
- Cross-file consistency (capability names in `agent.kdl` grants exist in the registry)
- Operator-facing error messages that describe the problem clearly

Tightening this is PLAN.md §1.2 remaining work.

### 2.7 Control Plane Socket (DESIGN.md §3)

The ctl message family is implemented. However:

- **Approval messages exist (`CtlApprovalRequired`, `CtlApprovalGrant`, `CtlApprovalDeny`) but return "not yet implemented (Phase 2)"** — this is acceptable per PLAN.md §3.7, but the return value should be a clear `ErrNotImplemented`/`capability_denied` with a documented reason, not silent failure.
- **Vault messages (`CtlVaultAdd`)** — vault is not implemented at all. Phase 5, acceptable, but again make sure any call to it returns a clear explicit error.

### 2.8 Capabilities Beyond MVP Scope in KDL

A recent commit added Commerce, SmartHome, and Messaging capabilities to `capabilities.kdl`. These have no gateway binaries, no MUS marshallers, and no registered dispatchers. They exist only as KDL stubs. Per PLAN.md MVP non-goals: _"broad provider/plugin abstractions beyond what is needed for one clear runtime path."_ These should be moved to a `capabilities/future/` directory or clearly marked as non-registered stubs to avoid confusion.

---

## 3. Refactoring and Consolidation Opportunities

### 3.1 Ctl vs Ward Router Path — Decide and Simplify

PLAN.md §2.2: _"Decide explicitly whether ctl should share the same router implementation as Ward traffic or only the same codec/message model."_ Currently the separation is unclear — the same `switchboard` codec is used, but the handler dispatch for ctl connections appears to be a separate path in `keeperd/main.go`. This should be made explicit:

- If ctl is a separate handler path: add a small shared `FrameHandler` interface so behavior cannot drift independently.
- If ctl is the same router: wire it in and remove the separate handler code.

### 3.2 Scope Model — Complete the Migration, Remove Raw-String Path

PLAN.md §3.5 notes: _"The codebase now has an initial compatibility bridge: legacy `Scope string` plus structured `ScopeConstraint`."_ Both exist simultaneously. Until built-in capabilities (`Browser_Page_Read`, `Filesystem_File_Write`) no longer depend on the raw-string path, the constraint model cannot be tested cleanly. Completing the migration:

1. Migrate `Browser_Page_Read` scope to typed `domain`/`domain-suffix` constraints.
2. Migrate `Filesystem_File_Write` scope to typed `path-prefix` constraints.
3. Remove the raw-string compatibility shim.

This will also make `agent.kdl` grant validation more meaningful.

### 3.3 Provider Tree Walk — Replace With Typed Validation

PLAN.md §1.2: _"Replace the still-manual provider tree walk with a stricter typed validation path."_ The `config.go` loader does a manual KDL tree walk for providers. This should use the same typed validation pattern as capability loading to eliminate discrepancies between what is loaded and what is validated.

### 3.4 Hard-Coded Default ACL Grants — Move to Config

PLAN.md §1.4: _"Move agent ACL creation away from hard-coded default grants and toward config-driven installation from validated agent policy."_ Currently provisioning hard-codes which capabilities are granted to a new agent. This means the `agent.kdl` grant blocks are not the real policy authority at provision time, which breaks the audit trail and makes `vivlog` output misleading.

### 3.5 Browser Capabilities Registered But Not MVP

`Browser_Page_Screenshot`, `Browser_Page_GetTitle`, and `Browser_Form_Submit` are in the KDL and may be partially registered. They should be explicitly gated or removed from the dispatch registry for MVP. Having registered capabilities with no tested execution path creates a false completeness impression and makes it harder to reason about the security surface.

### 3.6 `capwrap` Symlink Setup Should Be Part of Provisioning

The `capwrap` binary is designed to be symlinked inside nspawn containers by capability name. Verify that `LinuxRuntime` provisions these symlinks as part of `ProvisionWorkspace()`. If this is done manually or only in documentation, a newly provisioned agent will have no capability CLIs and the Ward's tool-socket server will be unreachable.

---

## 4. Architecture Concerns

### 4.1 Responsibility Boundary: Ward Owns Schema Validation, Not keeperd

DESIGN.md §2 is clear: _"Ward owns: syntactic/schema validation"_, _"keeperd owns: ACLs, resource scope checks."_ The current code appears to do schema validation in Ward (good) and ACL enforcement in keeperd (good). Make sure this boundary doesn't drift — in particular, any "is this capability name in the registry?" check should live in keeperd, while "are these arguments structurally valid against the schema?" lives in Ward.

### 4.2 MUS SeqNo Wrap Behavior

PLAN.md §2.1: _"Handle SeqNo restart/wrap behavior more gracefully than simple rewind denial."_ The router currently treats any non-monotonic sequence as a security event. For a long-running agent, uint64 overflow is unlikely but the rewind-on-reconnect case (Ward restarts but keeperd has not) is a realistic operational concern. The fix is small but needs to be deliberate.

### 4.3 `io.MultiReader(peek, pipe)` Read Path

PLAN.md §2.1 flags this: _"Benchmark the `io.MultiReader(peek, pipe)` read path against a buffered alternative."_ This is a correctness risk, not just a performance issue — if the peek buffer interacts with backpressure in an unexpected way during a large payload, frame boundaries may be misread. A buffered read path is simpler and easier to reason about.

### 4.4 Test Coverage Gaps vs Phase 4 Exit Criteria

PLAN.md §4.1 defines 10 unit test suites. Current coverage:

| Suite | Status |
|---|---|
| `TestMUSCodec` | Present (codec_test.go) |
| `TestCtlSocket` | Present (ctl_test.go) — unclear depth |
| `TestRuntimeRouter` | Present (router_test.go) |
| `TestCapabilityACL` | Present (capability_test.go) — unclear scope enforcement coverage |
| `TestVivgen` | Present (gen_test.go, backward_compat_test.go) |
| `TestWardLoop` | Present (loop_test.go) — schema-invalid call coverage unclear |
| `TestChromeProxy` | Present (proxy_test.go) — allow/deny proxy-layer test coverage unclear |
| `TestFilesystemWrite` | Present (capability_test.go?) — traversal/symlink deny coverage unclear |
| `TestVivaryLog` | Not verified as a distinct suite |
| `TestKeeperRuntimeState` | **Does not exist** — keeper runtime state not implemented yet |

PLAN.md §4.2 end-to-end tests: none of the integration tests listed there appear to exist yet. These are required before the MVP is considered done.

---

## 5. Ordered Recommended Work (Based on PLAN.md Checklist)

1. **Fix Ward tool-result injection** — implement MCP tool-result protocol so LLM subprocess receives capability responses. Without this, nothing runs end-to-end.
2. **Remove chromproxy allow-all fallback** — make proxy-layer whitelist enforcement real. Add allow/deny proxy-layer tests.
3. **Add keeperd runtime state tracking** — last SeqNo, last completion, last failure, last event timestamp, token counts, cost. Wire into ctl status responses.
4. **Wire `Capability.AuditPayload()` into keeper storage** — replace stub with real per-capability policy. Add tests.
5. **Verify and harden Linux provisioning** — audit create/destroy symmetry, partial-failure cleanup, capwrap symlink setup.
6. **Migrate scope model** — complete typed domain/path-prefix scope and retire raw-string path.
7. **Remove non-MVP capabilities from registry** — gate or stub out everything except `Browser_Page_Read` and `Filesystem_File_Write` for MVP dispatch.
8. **Write MVP end-to-end tests** — prompt run, browser allow/deny, filesystem allow/deny, provisioning cleanup, audit/debug workflow.
9. **Tighten config validation** — required fields, enum validation, cross-file consistency, clear error messages.
10. **Move Commerce/SmartHome/Messaging KDL to future/** — prevent noise and false completeness signals.
