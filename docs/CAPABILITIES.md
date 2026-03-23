# VIVARY Capability System

This document describes the design, structure, and evolution of the VIVARY capability model — the mechanism by which agents request access to external resources and actions, and by which operators control what those agents are permitted to do, on what resources, and under what conditions.

The `capabilities/` directory contains four KDL files that together define this model:

| File | Purpose |
|---|---|
| `categories.kdl` | Namespace registry — 15 functional categories, each mapped to a gateway binary |
| `capabilities.kdl` | Capability catalogue — 50 named actions with field schemas and entity declarations |
| `components.kdl` | ECS component schemas — reusable property sets that compose into entity types |
| `entities.kdl` | ECS entity types — named resource types with valid policy scope-constraint keys |

---

## Design Principles

**1. Generic over vendor-specific**

Capability names are deliberately vendor-neutral. An agent uses `Calendar_Event_Create`, not `Gsuite_Calendar_Insert`. `keeperd`'s gateway layer resolves the generic verb to the correct provider API based on the agent's assigned `credential_id`. This allows providers to be swapped without changing agent code or prompts.

**2. Namespace_Noun_Verb grammar**

Every capability name follows a strict three-part structure:

```text
Namespace_Noun_Verb
    │       │     └── Action: Create | Delete | Read | List | Update | Stream | Exec | Invoke
    │       └──────── Resource type within the namespace
    └──────────────── Category (matches a category in categories.kdl)
```

Examples: `Browser_Page_Read`, `Email_Message_Send`, `VersionControl_Branch_Create`.

Vague verb stems (`Get`, `Do`, `Handle`) and vendor-specific terms (`Gsuite`, `Github`, `Slack`) are rejected by the `vivgen` linter at generation time.

**3. Zero-exposure credentials**

Agents never hold API keys, OAuth tokens, or passwords. They reference a `credential_id` string. `keeperd` resolves this to the actual secret from its AES-256-GCM vault only at the gateway invocation layer, after all ACL checks have passed.

**4. KDL-first compile-time generation**

The definitive source of capability structure is the `capabilities/*.kdl` files. `vivgen` reads those KDL definitions at compile time and generates the Go types, static schema strings, and registry data compiled into the binaries. This eliminates runtime reflection and keeps the schema the LLM receives in sync with what `keeperd` and `ward` parse.

**5. Deny by default**

An agent may only invoke capabilities explicitly listed in its `agent.kdl` `allow` blocks. Any capability not listed is silently denied and logged. There is no wildcard grant.

---

## Categories (`categories.kdl`)

Categories define the top-level namespaces and map each to a gateway binary responsible for implementation. Each category carries a risk level (or separate read/write risk levels) that informs default policy decisions.

| Category | Gateway | Read risk | Write risk |
|---|---|---|---|
| `Browser` | chrome-sidecar | medium | medium |
| `Search` | search-gateway | low | — |
| `Filesystem` | fs-gateway | low | low |
| `Execution` | code-gateway | — | high |
| `Email` | mail-gateway | low | critical |
| `Calendar` | calendar-gateway | low | high |
| `Spreadsheet` | sheet-gateway | low | high |
| `Document` | doc-gateway | low | high |
| `Messaging` | msg-gateway | low | critical |
| `Memory` | memory-gateway | low | low |
| `VersionControl` | vcs-gateway | low | high |
| `Database` | data-gateway | medium | critical |
| `System` | sys-gateway | high | high |
| `Media` | media-gateway | low | — |
| `Swarm` | switchboard | low | medium |

**Risk levels:** `low` → `medium` → `high` → `critical`. Critical actions (sending email, sending chat messages) require explicit `allow` grants and are candidates for `requires-approval true`.

---

## Capabilities (`capabilities.kdl`)

Each capability entry defines:

- **`category`** — which namespace and gateway handles it
- **`description`** — plain-language description used for operator review and documentation generation
- **`risk`** — enforcement hint for default policy generation
- **`reads entity=`** / **`writes entity=`** — which ECS entity types this capability operates on; used to validate scope constraints in `agent.kdl` at load time
- **`field` blocks** — typed input fields with descriptions, examples, enums, defaults, and min/max constraints; these are the source for `Explain()` schema generation
- **`returns`** — the type and description of the response

### Example entry

```kdl
capability "Email_Message_Send" {
    category    "Email"
    description "Send an existing draft message"
    risk        "critical"
    reads  entity="EmailDraft"
    writes entity="EmailMessage"

    field "draft_id" type="string" required=true {
        description "Draft ID from Email_Message_Create"
    }

    returns "object" description="Object with fields: message_id, sent_at"
}
```

### Go SDK interface

Every capability implements `SwarmCapability`:

```go
type SwarmCapability interface {
    Explain() string            // static JSON schema string; generated by vivgen
    MarshalMUS() ([]byte, error)
    UnmarshalMUS([]byte) error
}
```

### vivgen

`vivgen` reads the KDL capability catalogue and emits generated Go code containing capability structs, static `Explain()` schema strings, and registry/lookup data. It also runs the capability linter:

- Name matches `Namespace_Noun_Verb` pattern
- All required fields have KDL metadata for description and example/enum/default as appropriate
- JSON field names are `snake_case`
- No vendor-specific terms in the namespace or noun segments
- Capability/category/entity references are consistent across the KDL files

```go
// file: email_message_send.go
// Code generated by vivgen from capabilities.kdl

type Email_Message_Send struct {
    DraftID string `json:"draft_id"`
}
```

---

## ECS Resource Model

### Why ECS

Capabilities define *what actions exist*. A separate concern is *what resources a granted action may operate on*. A naive approach — per-capability scope fields — doesn't compose or reuse well across 50+ capabilities. The Entity-Component-System model solves this by defining resource schemas once and referencing them by name.

### Components (`components.kdl`)

Components are reusable property schemas. They are never used alone — they compose into entity types.

| Component | Key fields | Purpose |
|---|---|---|
| `Addressable` | url, domain, scheme, path | Any URL-identified resource |
| `Located` | path, name, extension, parent | Filesystem-positioned resource |
| `Identified` | id, provider, external_url | Provider-assigned opaque ID |
| `Owned` | credential_id, owner_email | Credential/user association |
| `Shared` | members, is_public, access_level | Multi-party access |
| `Timestamped` | created_at, modified_at | Creation/modification times (ISO8601 UTC) |
| `Scheduled` | start, end, timezone, all_day | Time-windowed resources |
| `Sized` | size_bytes, mime_type | Data size and type |
| `Addressed` | from, to, cc, subject | Sender/recipient addressing |

### Entities (`entities.kdl`)

Entity types compose components and declare which `scope-constraint` keys are valid for policy grants. `keeperd` validates at agent load time that every `entity` block in an `allow` grant uses only declared scope-constraint keys.

Selected examples:

```kdl
entity "EmailMessage" {
    components "Identified" "Owned" "Addressed" "Timestamped"
    scope-constraints {
        "from-domain"      field="Addressed.from"    description="..."
        "to-domain"        field="Addressed.to"      description="..."
        "to-address"       field="Addressed.to"      description="..."
        "subject-contains" field="Addressed.subject" description="..."
    }
}

entity "Link" {
    components "Addressable"
    scope-constraints {
        "domain"        field="Addressable.domain" description="..."
        "domain-suffix" field="Addressable.domain" description="..."
        "scheme"        field="Addressable.scheme" description="..."
        "path-prefix"   field="Addressable.path"   description="..."
    }
}
```

The full entity registry covers: `File`, `Folder`, `Link`, `EmailMessage`, `EmailDraft`, `Contact`, `CalendarEvent`, `Spreadsheet`, `SpreadsheetRange`, `Document`, `Message`, `Channel`, `Repository`, `Commit`, `Branch`, `DatabaseTable`, `Process`, `Memory`, `AgentMessage`, `Agent`, `ImageFile`, `AudioFile`.

---

## Policy Grants (`agent.kdl`)

The `allow` block in `agent.kdl` is where operators express not just *which* capabilities an agent may use, but *on what resources*, *when*, and *at what rate*. `keeperd` enforces all conditions before dispatching to a gateway binary.

### Grant anatomy

```kdl
allow "Email_Message_Send" {
    // Entity scope — filter by component field values
    entity "EmailMessage" {
        to-domain "example.com" "partner.org"
    }

    // Temporal constraint — UTC with named timezone for display
    schedule {
        days "mon" "tue" "wed" "thu" "fri"
        hours "09:00"-"17:00"
        timezone "Europe/London"
    }

    // Rate limiting
    rate { max 20  per="day" }

    // Human approval gate
    requires-approval false
}
```

### Enforcement order

For every inbound capability request `keeperd` checks, in order:

1. Capability is in the agent's `allow` list → else `capability_denied`
2. Current time (UTC) is within the `schedule` window → else `capability_denied`
3. Rate counter for this agent+capability is within the `rate` limit → else `capability_denied`
4. Entity scope constraints match the request's target resource → else `capability_denied`
5. If `requires-approval true` → suspend and emit `MsgType_CtlApprovalRequired` to notification targets
6. Dispatch to gateway binary

### Temporal constraints

All time comparisons are performed in UTC. The `timezone` field is metadata for human-readable display in the TUI and approval notifications — it does not affect enforcement. This avoids DST ambiguity and ensures consistent behaviour across the swarm regardless of host timezone.

### Approval flow

When `requires-approval true`, `keeperd`:

1. Suspends the capability request
2. Emits `MsgType_CtlApprovalRequired` to all registered `ApprovalTarget` implementations
3. Waits up to `orchestrator.kdl approval.timeout` (default `5m`) for a grant or deny
4. On timeout, applies `approval.on-timeout` policy (`deny` or `approve`)

Initial approval target: `vivary` via the ctl Unix socket. Future target: mobile push via a configurable signed webhook — same `ApprovalTarget` interface, no `keeperd` changes required.

---

## Capability Evolution

The capability catalogue is versioned with the codebase. Adding or changing a capability requires:

1. Update `capabilities/capabilities.kdl` with the new or changed capability entry
2. If the capability operates on a new resource type, update `capabilities/entities.kdl` (and any new component schemas in `capabilities/components.kdl`)
3. Run `vivgen` to regenerate the Go types, schema strings, and registry data
4. Update the runtime implementation that dispatches or enforces the capability as needed
5. The `vivgen` linter enforces naming and documentation standards — CI fails if any capability violates them

New categories require a matching gateway binary implementation before the capability can be dispatched.

---

## What Is Not In Scope

- **Capability discovery by agents:** Agents receive the `Explain()` schemas for their granted capabilities injected into their system prompt at session start. They do not query a runtime registry.
- **Dynamic capability registration:** Capabilities are compile-time artefacts. There is no plugin or hot-load mechanism — this is intentional to prevent supply-chain attacks via dynamically loaded capability code.
- **Cross-agent capability delegation:** An agent cannot grant a subagent more capabilities than it holds itself. `keeperd` enforces this at topology validation time.
