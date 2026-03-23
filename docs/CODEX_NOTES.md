# VIVARY Codebase Notes

These notes compare the current repository state to `docs/PLAN.md` and `docs/DESIGN.md`, with an emphasis on making the system robust, understandable, and not over-engineered for the MVP.

## Overall Read

The repo already has a credible runtime core:

- MUS framing and routing exist
- `keeperd`, `ward`, `viv`, and `vivlog` all exist
- the TUI/CLI loop is present
- audit logging is real and tested
- the Nix/LXD base and runtime image work has started

The strongest parts are the small switchboard/audit core and the clear `keeperd` vs `ward` split.

The weakest parts are provisioning correctness, configuration parsing, schema/policy consistency, and the number of places where the docs describe a fuller design than the code actually implements.

The main recommendation is to finish the single-agent runtime cleanly before adding more abstraction. The code should read like a straightforward Go service with a few well-defined subsystems, not like a speculative platform.

## Current Status Against `docs/PLAN.md`

### Clearly implemented

- `keeperd` exists and speaks MUS-framed traffic over `keeper.sock`
- `viv` exists as CLI and BubbleTea TUI
- `ward` exists and can receive prompts, spawn a Claude subprocess, emit capability requests, and send completion/failure events
- `vivlog` exists and can inspect the SQLite audit DB
- MUS codec, router, SeqNo enforcement, identity stamping, and byte-rate limiting exist with decent test coverage
- `Filesystem_File_Write` is implemented
- browser/CDP plumbing exists in some form
- Nix flake, base image, runtime image, and distro helper scripts exist

### Partially implemented

- config loading exists, but it is still a hand-rolled KDL subset parser rather than the planned strict library-backed parser
- agent provisioning exists, but it is not yet safely abstracted or robustly recoverable
- TUI live updates exist, but agent runtime state shown in the UI is still fairly thin
- Ward has the shape of the planned adapter, but its subprocess/backend abstraction is still incomplete
- `vivgen` exists, but schema synchronization with Ward and keeperd is not fully finished
- browser capability exists in code, but the default end-to-end story still looks incomplete

### Missing or still stub-like

- approval flow is mostly unimplemented
- typed ECS scope enforcement is not implemented; scope is still mostly a string path/prefix model
- ctl traffic is not yet handled by the same router path as Ward traffic
- audit payload policy is not fully implemented
- retention/pruning and other operator maintenance flows are not implemented
- end-to-end testing coverage is well behind the plan

## Current Status Against `docs/DESIGN.md`

### Where the code matches the design well

- `keeperd` is the semantic authority and `ward` is the syntax/protocol adapter
- the MVP is effectively single-agent first
- MUS framing is the shared transport idea across ctl and Ward traffic
- `keeperd` stamps agent identity on inbound Ward frames
- completion and failure events are first-class runtime concepts
- browser access is mediated by `keeperd`, not by the agent directly
- the audit/debug story is CLI-first, not TUI-only

### Where the code is meaningfully simpler than the design

- config and policy are much flatter than the design's full `agent.kdl` / ECS / schedule / rate / approval model
- the wire header is smaller than the design header; `ParentID` and `Depth` do not exist yet
- ctl messages are simpler and the approval message family is not there yet
- payloads are mostly `MUS header + JSON payload`, not a fuller typed MUS payload ecosystem
- the ctl socket uses the same codec as Ward traffic, but not the same router implementation

### Where the code diverges in risky ways

- provisioning/spawn logic appears more fragile than the design suggests
- browser isolation and whitelist enforcement do not yet look as tight as the design claims
- hard-coded Ward schema constants and hard-coded Claude invocation are still present despite the documented `vivgen` and `AgentCLI` direction
- the design assumes clearer per-agent runtime state and approval workflows than the implementation currently provides

## Main Suggestions

## 1. Finish the MVP with fewer moving parts

The codebase should optimize for one polished vertical slice:

- one local `keeperd`
- one local `ward`
- one real prompt flow
- two capabilities: browser read and scoped file write
- one solid audit/debug path

Anything beyond that should be treated as future work, even if the docs already sketch it.

That means being willing to simplify or postpone:

- approval plumbing
- ECS scope generalization
- multi-agent header fields
- provider-generalized Ward behavior
- richer vault/gateway family design

## 2. Replace the config parser early

`cmd/keeperd/config.go` is currently too much hand-rolled parser for a system that wants to be trustworthy.

Why this matters:

- configuration is part of the security boundary
- a partial parser is harder to reason about than a real library + validation layer
- the current parser shape pulls policy bugs toward runtime instead of parse time

Suggested direction:

- introduce a dedicated `internal/config` package
- use a real KDL library
- define explicit structs for orchestrator, agent, and provider config
- validate required fields, uniqueness, enum values, and naming constraints at load time
- keep runtime code working with already-validated config objects only

This is one of the highest-leverage simplifications in the repo.

## 3. Extract provisioning behind a small runtime interface

`cmd/keeperd/provisioning.go` currently mixes:

- Btrfs operations
- file writes
- nspawn spawning
- nftables setup
- fallback behavior for non-Linux
- agent state registration

That is too much policy and too much OS detail in one place.

Suggested direction:

- create `internal/runtime` or `internal/provisioning`
- define a small `ContainerRuntime` interface
- keep Linux implementation concrete and boring
- add a fake/test implementation for non-Linux and unit tests
- make `keeperd` orchestration code call into a small runtime API rather than shelling directly everywhere

Also add a deferred cleanup stack so partial failures unwind cleanly.

This makes the code easier for a Go developer to read because it separates business logic from host integration.

## 4. Decide on one Ward tool path and make it crisp

Ward currently feels like it is between two models:

- direct parsing of backend JSON stream events
- separate capability CLI/tool server flow

For the MVP, it should be very obvious how a prompt becomes a tool call and how that becomes a MUS request.

Suggested direction:

- pick the single Claude path you actually want to support now
- keep one normalized internal event shape for Ward
- move all backend-specific parsing behind one tiny interface
- remove duplicate or overlapping mechanisms until they are really needed

Ward should read as:

- receive prompt
- spawn backend
- parse backend tool event
- validate structure
- send MUS capability request
- receive response
- emit completion/failure

No more, no less.

## 5. Centralize capability policy and stop leaking scope through ad hoc strings

Right now the code and docs are between two worlds:

- simple string scope checks
- the fuller ECS scope model in `docs/CAPABILITIES.md`

For the MVP, a middle path is probably best.

Suggested direction:

- avoid jumping straight to the full ECS model everywhere
- replace the raw `Scope string` with a small typed scope structure for the two built-in capabilities only
- keep validation explicit and local
- introduce the full ECS model only once there are enough capabilities to justify it

For example:

- browser scope: domain suffix whitelist
- filesystem scope: path prefix whitelist

This is much easier to understand than free-form strings, but much less heavy than the whole ECS machinery being fully realized at once.

## 6. Unify protocol handling only when it helps readability

The design wants ctl and Ward traffic handled by the same router path. That is elegant, but not worth forcing if it makes the code harder to follow in the MVP.

Suggested direction:

- keep the shared codec and shared message types
- extract a small common frame-handling layer
- unify ctl and Ward routing paths only if the result is actually simpler

The important thing is protocol consistency, not architectural symmetry for its own sake.

## 7. Make runtime state explicit in `keeperd`

The UI/TUI will stay easier to reason about if `keeperd` owns a simple in-memory model of:

- current agent state
- last prompt seq
- last completion event
- last failure event
- current subscribers

Right now some of that exists only indirectly.

Suggested direction:

- introduce a small `agentRuntimeState` struct in `keeperd`
- update it from prompt dispatch and event receipt
- build ctl status responses from that state directly

This makes the TUI more truthful and reduces scattered bookkeeping.

## 8. Tighten browser mediation before expanding capability count

The browser path is one of the main reasons VIVARY is interesting, so it needs to be obviously correct.

Suggested direction:

- make whitelist enforcement real and testable at the proxy layer
- ensure per-agent browser profile behavior matches the docs or simplify the docs
- keep the browser surface very small in MVP: `Browser_Page_Read` only
- add explicit allow/deny integration tests before expanding browser features

If browser isolation is not solid, a lot of the product story weakens.

## 9. Keep audit policy simple but real

`shouldAuditPayload` and payload retention are exactly the kinds of things that become tech debt if left half-implemented.

Suggested direction:

- implement `Capability.AuditPayload()` for the built-in capabilities now
- make the audit DB rules explicit and small
- postpone more advanced retention/encryption work until after MVP unless required for real use

This keeps the audit story honest without dragging the code into a larger storage project.

## 10. Use package boundaries to make the repo legible

A Go developer should be able to scan the repo and quickly understand where things live.

Suggested target shape:

- `cmd/keeperd` — thin startup/wiring only
- `cmd/ward` — thin startup/wiring only
- `cmd/viv` — CLI/TUI only
- `internal/config` — KDL loading + validation
- `internal/switchboard` — MUS frame codec + router
- `internal/runtime` — provisioning/nspawn/nftables/Btrfs integration
- `internal/capabilities` — dispatcher + built-in capabilities
- `internal/chromproxy` — browser mediation only
- `internal/audit` — audit DB + decode helpers
- `internal/ctl` — ctl message payloads and helpers

The repo is already close to this shape, but `cmd/keeperd` still contains too much domain logic.

## Concrete Refactoring Priorities

If the goal is robust but not over-engineered, these feel like the right order:

1. Replace the hand-rolled config parser with strict config loading/validation
2. Fix and isolate provisioning/runtime integration behind a small interface
3. Simplify Ward to one clear backend path and remove hard-coded schema duplication
4. Make browser mediation/whitelist behavior explicitly correct and tested
5. Improve `keeperd` runtime state bookkeeping for truthful ctl/TUI output
6. Implement real audit payload policy for built-in capabilities
7. Only then decide how much of the ECS scope model should land in MVP

## Things To Avoid Right Now

- implementing the full multi-agent wire/header model before MVP is stable
- building a generalized plugin system for capabilities or model backends
- forcing total ctl/router unification if it makes MVP code harder to follow
- fully realizing the whole ECS design before the two built-in capabilities are clean
- adding more gateway families before browser + filesystem are solid

## Bottom Line

The codebase is already on a promising path, but the next step is not more architecture. It is consolidation.

VIVARY will be stronger if the MVP becomes:

- smaller
- more explicit
- easier to test
- easier to inspect
- easier for a Go developer to read in one pass

The core idea is good. The code should now be bent toward finishing that core cleanly rather than catching up to every future-facing detail already present in the design docs.
