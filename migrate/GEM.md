# OpenClaw to VIVARY Migration Critique

## Executive Summary
The migration design (`MIGRATE_PLAN.md`, `MIGRATEUX.md`, `MIGRATE_TUI_PLAN.md`) is architecturally sound and aligns well with VIVARY’s "Research -> Strategy -> Execution" workflow. By prioritizing **Discovery** as a read-only, non-destructive phase, the design builds trust before any filesystem mutations occur.

The mapping of OpenClaw components into VIVARY's **ECS (Entity-Component-System)** and **Isolated Runtime** models is conservative and security-conscious, correctly identifying where "Logic Gaps" exist (e.g., raw JS/TS extensions).

---

## 1. Architectural Alignment with DESIGN.md

### Agent Isolation & Identity
*   **VIVARY Design:** Each agent runs in a `systemd-nspawn` container with unique UIDs and a dedicated Btrfs subvolume.
*   **Migration Mapping:** The design correctly maps OpenClaw agents to VIVARY agent templates. It treats existing plugin code as "unsupported," preventing the injection of unverified or non-isolated logic into the new runtime core.
*   **Critique:** This is the correct "security-first" posture. It forces the operator to explicitly define VIVARY capabilities rather than "smuggling" legacy code into the sandbox.

### Capability Model (ECS)
*   **VIVARY Design:** Strict Namespace_Noun_Verb grammar (e.g., `Browser_Page_Read`) using an ECS approach (Entities/Components).
*   **Migration Mapping:** `MIGRATEUX.md` proposes grouping plugins into VIVARY categories (Memory, Search, Filesystem).
*   **Critique:** While categorical grouping is a good start, the "Logic Gap" between coarse-grained OpenClaw plugins and fine-grained VIVARY capabilities is the biggest friction point. The migration engine should eventually assist in identifying specific **Entities** (e.g., `File`, `Link`) and **Components** (e.g., `path-prefix`) for the `agent.kdl` policy.

### The Ward (Protocol Adapter)
*   **VIVARY Design:** Ward is a syntax/protocol adapter that turns LLM tool-calls into MUS capability requests.
*   **Migration Mapping:** OpenClaw `SKILL.md` files and slash commands are mapped to portable prompt assets and macros.
*   **Critique:** This is a perfect fit. Since VIVARY's Ward uses a per-prompt subprocess model, these assets can be injected as context or CLI args, preserving the core behavior of OpenClaw skills without modification.

---

## 2. Component Mapping Matrix

| OpenClaw Component | VIVARY Target | Portability Tier | Architectural Note |
| :--- | :--- | :--- | :--- |
| `SKILL.md` | Prompts / Fragments | `portable` | Injected into Ward's LLM subprocess. |
| Slash Commands | Prompt Macros | `portable` | Maps to `viv` or Ward-side expansion. |
| Standing Orders | `keeperd` Schedules | `portable_with_review` | Managed by `keeperd`'s policy engine. |
| Memory/Search Plugins | VIVARY Capabilities | `portable_with_review` | Requires mapping to `Memory_*` / `Search_*`. |
| Channel Plugins | External Bridges | `bridge_required` | Keeps ingress logic outside the core runtime. |
| Credentials | VIVARY Vault | `portable_with_review` | Identified/redacted during discovery; moved to AES-256 vault. |
| JS/TS Extensions | Unsupported | `unsupported` | Requires re-implementation as Capabilities (Go CLI tools). |

---

## 3. Trust and Security Signals

### Credential Redaction
The `internal/migrate/openclaw.go` implementation correctly identifies and redacts secrets during discovery. This is a critical trust signal: the operator knows VIVARY *understands* the secret's location without needing to *read* the secret's value into the discovery report.

### Bridge Isolation
The "Bridge Required" classification for Slack, Telegram, and WhatsApp aligns with VIVARY's goal of a single-agent runtime core. By treating these as "bridges" that speak MUS over Unix Sockets, VIVARY avoids bloating `keeperd` with third-party messaging SDKs.

### Dry-Run Priority
The "Discovery Review TUI" being read-only is a strong design choice. It allows users to validate VIVARY's understanding of their legacy setup before any risk of data loss.

---

## 4. Gaps and Recommendations

### A. Entity & Scope Inference
The current discovery engine (`openclaw.go`) identifies plugin categories but not the *scope* of their access.
*   **Recommendation:** Enhance discovery to look for "scope hints" (e.g., specific URLs in browser config, directory paths in vault config) and propose them as `entity` blocks in the generated `plan.kdl`.

### B. Bridge Stub Definition
`MIGRATEUX.md` mentions "generating bridge stubs."
*   **Recommendation:** Explicitly define the `bridge.kdl` schema in `docs/DESIGN.md`. A bridge needs to know its `FromID`, the `keeper.sock` path, and its allowed target agents.

### C. The "Logic Gap" UX
Moving from a JS plugin to a VIVARY Go Capability is a manual task.
*   **Recommendation:** The TUI should provide a "Capability Stub" generator for `unsupported` items that creates a template Go binary and `capability.kdl` file, reducing the boilerplate for manual porting.

### D. Approval Flow Clarification
`DESIGN.md` states approvals are "not implemented in the current MVP," yet the migration design relies on them as a default safety net.
*   **Recommendation:** Ensure the migration engine flags these as `requires-approval true` in `agent.kdl`, even if the runtime initially ignores or hard-fails on them. This preserves the security intent for when the feature lands.
