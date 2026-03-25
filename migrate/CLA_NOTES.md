# CLA_NOTES: Migrate Plan Critique Against DESIGN.md

These notes identify gaps, mismatches, and under-specified areas in the migrate plan
documents relative to the architectural constraints in `docs/DESIGN.md`.

---

## 1. Approval flag: not a runtime safety mechanism in current MVP

**Files:** `MIGRATEUX.md` Screen 6, `MIGRATE_TUI_PLAN.md` §Trust-Critical Behaviors

DESIGN.md §8 is explicit: _"not implemented in the current MVP runtime."_

Screen 6 of the TUI shows `approval=required` as a live control alongside enabled
capabilities. An operator who sees this will believe their write/send/exec actions are
gated. They are not. `keeperd` will not pause on these grants today.

`MIGRATE_PLAN.md`'s design notes section correctly identifies this problem and says
outputs should mark intent without implying enforcement. But the TUI wireframes don't
reflect this caveat — they present approval state as if it does something.

**Required fix:** The TUI capability review screen must show approval-required items in a
visually distinct state (e.g., `[intent only — runtime not yet enforced]`) and the
generated agent.kdl comment or policy file must carry this warning inline. Do not let
the operator leave the screen believing they have a safety gate they don't have.

---

## 2. Scope syntax in Screen 6 doesn't match agent.kdl grant syntax

**File:** `MIGRATEUX.md` Screen 6

The proposed capability grants show:

```
Search_Query_Read   scope="web:*"
Browser_Page_Read   scope="https://docs.openclaw.ai/*"
```

DESIGN.md §7 defines the actual grant syntax as:

```kdl
allow "Browser_Page_Read" {
    entity "Link" {
        domain-suffix "docs.openclaw.ai"
    }
}
```

There is no `scope="..."` shorthand in the runtime. The planner must generate valid
entity constraint blocks, not a made-up scope string. The TUI can display a simplified
summary for operators, but the engine must produce correct KDL.

This is also a `vivgen`/load-time concern: `keeperd` validates at load that entity types
referenced in `allow` blocks match the entities declared in `capabilities/*.kdl`
(DESIGN.md §9). A generated `entity "Link" { domain-suffix "..." }` block is
semantically valid; `scope="web:*"` would fail validation.

---

## 3. Capability names not validated against the capability registry

**File:** `MIGRATEUX.md` Screen 6, `MIGRATE_TUI_PLAN.md` §Planner tests

Screen 6 proposes these capability names:
- `Search_Query_Read`
- `Execution_Command_Run`
- `Memory_Record_Read` / `Memory_Record_Write`
- `Messaging_Message_Send`

These follow the Namespace_Noun_Verb grammar (DESIGN.md §9), but they are invented names.
The planner can only propose capabilities that actually exist in `capabilities/capabilities.kdl`.
Generating an `allow` block for `Memory_Record_Read` when that capability doesn't exist
in the registry produces an agent.kdl that keeperd will reject at load time.

The planner must query (or be compiled against) the capability registry, not invent names
from plugin category labels. The `ScopeHint` → capability mapping step needs a defined
mapping table from plugin category to real registered capability names.

---

## 4. Standing orders → "keeperd schedules" mapping is ahead of MVP

**File:** `MIGRATEUX.md` Screen 3 / Screen 4, `GEM.md` mapping table

The Findings Overview and Review Buckets screens describe:
> "3 standing orders can become disabled keeperd schedules"

DESIGN.md defines `schedule {}` blocks only as time-window constraints within individual
`allow` capability grants (e.g., "only send email Mon–Fri 9–5"). There is no concept of
keeperd managing autonomous scheduled task execution in the MVP.

Mapping "standing orders" to `keeperd schedules` implies a scheduled-task primitive that
doesn't exist. The planner should instead:
- preserve standing order content as prompt assets (portable),
- note in the report that autonomous scheduling is not an MVP keeperd feature,
- not generate `.kdl` schedule files that imply a scheduler will execute them.

---

## 5. Bridge stubs have no defined connection to the MUS protocol

**Files:** `MIGRATE_PLAN.md` §Bridge stub schema, `GEM.md` §B, `MIGRATEUX.md` Screen 7

The plan describes generating `bridge/wechat.kdl` with fields like `keeper.sock path`,
`allowed target agent IDs`, and `disabled-by-default` state. But DESIGN.md §3 defines
exactly one external connection model: a process connects to keeper.sock using MUS binary
framing with a `SwarmHeader` (`FromID`, `ToID`, `MsgType`, etc.).

A bridge connecting to keeperd isn't a new config concept — it is an external process
that must:
1. Connect to keeper.sock,
2. Identify itself with a `FromID` that keeperd recognises as a registered agent or ctl client,
3. Exchange MUS-framed messages.

The `bridge.kdl` stub schema needs to be grounded in this. Minimally it needs to capture:
- the MUS identity the bridge will use (`FromID`),
- whether it connects as an agent pipe or as a ctl-level client,
- the keeper.sock path.

Without this, generated bridge stubs are not actionable — they describe intent but give
no implementation surface for whoever builds the bridge process.

---

## 6. `PlannedImport.Enabled` has no agent.kdl equivalent

**File:** `MIGRATE_TUI_PLAN.md` §Planner Model

```go
type PlannedImport struct {
    Enabled     bool
    ...
}
```

The agent.kdl grant syntax has no `enabled: false` flag. A capability is either in the
`allow` list or it isn't. An "imported but disabled" capability in VIVARY means one of:
- The `allow` block is commented out (not machine-readable policy),
- The item is placed in a separate staging file not loaded by keeperd,
- The `allow` block is omitted entirely from agent.kdl, with the intent documented elsewhere.

The plan needs to decide which mechanism "disabled by default" means. The current
`PlannedImport.Enabled` field papers over this. Recommendation: disabled imports should
land in a `migrate/staged/` directory with a note that they require manual promotion into
agent.kdl, rather than generating a live agent.kdl with invisible no-op grants.

---

## 7. Output paths don't map to VIVARY's defined workspace layout

**File:** `MIGRATEUX.md` Screen 8 / Screen 9

The plan preview writes to:
```
prompts/imported/
macros/imported/
schedules/imported/
policies/imported/
bridge/
```

None of these paths are defined in DESIGN.md's workspace model. VIVARY's operator-facing
config files are `keeper.kdl` and `agent.kdl` (and the Btrfs subvolume structure described
in §1). There is no canonical `prompts/`, `macros/`, or `policies/` directory.

Two options:
1. The planner writes into a staging directory (`migrate/imports/`) and the apply step
   requires the operator to promote items into the real agent config.
2. The project formally defines a prompt/macro/schedule directory convention as part of
   the viv workspace structure.

Either is fine, but the current docs imply these paths exist and keeperd will pick them
up, which it won't.

---

## 8. Live apply requires keeperd reload — no such mechanism is defined

**File:** `MIGRATE_TUI_PLAN.md` Phase 4

Phase 4 applies migration outputs, presumably writing or modifying agent.kdl files.
DESIGN.md §3 lists ctl message types. There is no `MsgType_CtlReload` or equivalent for
keeperd to pick up config changes at runtime.

If migration apply modifies agent.kdl while keeperd is running, the in-memory policy
state does not update. The operator must restart keeperd. Phase 4 "live apply" should
explicitly call this out — or the plan needs to define a reload mechanism before Phase 4
is viable.

---

## 9. `vivgen` pipeline missing from capability stub recommendation

**File:** `GEM.md` §C

GEM.md recommends a "Capability Stub generator" that creates a template Go binary and
`capability.kdl` for unsupported/extension items. This is correct directionally, but
DESIGN.md §9 is clear: capabilities are installed as self-documenting CLI binaries
generated by `vivgen` from `capabilities/*.kdl`. The generated binary isn't a freestanding
Go template — it is the output of the vivgen pipeline.

The stub generator should produce:
1. A skeleton `capabilities/my_new_cap.kdl` with the required fields (category, reads/writes
   entity declarations, field metadata),
2. A note that running `vivgen` produces the Go type and schema string.

Generating a raw Go file that bypasses vivgen would diverge from the capability registry
and fail the load-time entity validation check.

---

## 10. Minor: `viv` ctl socket identity assumed, not stated

**File:** `MIGRATE_PLAN.md` §CLI

The plan states "no keeperd dependency" for v0, which is correct. But Phase 3/4 operations
(plan preview referencing live agent state, live apply) implicitly depend on keeper.sock.

DESIGN.md §3: `viv` identifies itself as `FromID: "ctl"`, a reserved identity granting
operator-level access. Migration commands that need to inspect or modify live agent state
must connect as ctl clients and use the defined ctl message types (`MsgType_CtlStatus`,
`MsgType_CtlAgentCreate`, etc.). The plan should note which phases cross this boundary
so the "no keeperd dependency" claim stays accurate for the phases where it holds.

---

## Summary

| Issue | Severity | Phase affected |
|---|---|---|
| Approval presented as live enforcement | High | Phase 2–4 |
| Scope syntax doesn't generate valid agent.kdl | High | Phase 2–3 |
| Capability names not validated against registry | High | Phase 2–3 |
| Standing orders → scheduler primitive doesn't exist | Medium | Phase 2–3 |
| Bridge stubs not grounded in MUS protocol | Medium | Phase 3–4 |
| PlannedImport.Enabled has no agent.kdl equivalent | Medium | Phase 3–4 |
| Output paths not defined in workspace model | Medium | Phase 3–4 |
| Live apply needs keeperd reload mechanism | Medium | Phase 4 |
| Capability stub bypasses vivgen pipeline | Low | Future |
| ctl dependency boundary not stated | Low | Phase 3–4 |
