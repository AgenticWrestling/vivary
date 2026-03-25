# OpenClaw Migration UX

## Goal

Design a final migration TUI that feels fast, legible, and trustworthy.

The UX should reduce friction by:

- auto-discovering most of the install without asking for hand-holding,
- batching operator decisions into a small number of high-value review steps,
- showing safe defaults clearly,
- preserving provenance and reversibility at every stage.

It should build trust by:

- never hiding unsupported items,
- never silently enabling risky capabilities,
- always showing where a migrated artifact came from,
- making "what will happen" clearer than "what could happen."

## Design Principles

### 1. Discover first, commit later

The first screens are read-only. Operators should see that VIVARY understands the OpenClaw install before it asks them to approve anything.

### 2. Review by risk, not by file tree

Users should not have to inspect 200 files one by one. The TUI should group work into:

- safe automatic imports,
- review-required mappings,
- bridge-required items,
- unsupported items.

### 3. Always show provenance

Every importable item should expose:

- source path
- source type
- detected owner/plugin/channel
- destination target in VIVARY

### 4. Safe defaults must be visible

If VIVARY chooses a conservative default, show it explicitly.

Examples:

- imported automations start disabled
- write/send/exec capabilities start approval-gated
- channel connectors default to deferred bridge stubs
- secrets default to unresolved vault references

### 5. One-screen confidence

Each major screen should answer one operator question cleanly:

- what did you find?
- what can migrate automatically?
- what needs my decision?
- what will be written?
- what was skipped, and why?

## Overall Flow

```text
1. Source Select
2. Discovery Scan
3. Findings Overview
4. Review Buckets
5. Credentials Mapping
6. Capability and Approval Review
7. Bridge / Unsupported Review
8. Plan Preview
9. Apply / Dry Run
10. Results and Next Steps
```

The operator should be able to exit after step 3 with a useful report, or continue to build and apply a migration plan.

## Global TUI Layout

All major screens use the same shell so the operator never loses orientation.

```text
+----------------------------------------------------------------------------------+
| VIVARY MIGRATE                                                     OpenClaw -> VIVARY |
| Source: ~/.openclaw                                  Mode: Plan     Dry Run: ON |
| Step 4/10: Review Buckets                                                  ? help |
+---------------------------+------------------------------------------------------+
| Steps                     | Main Panel                                           |
|                           |                                                      |
| 1  Source                 |                                                      |
| 2  Discover               |                                                      |
| 3  Findings               |                                                      |
| 4  Review Buckets         |                                                      |
| 5  Credentials            |                                                      |
| 6  Capabilities           |                                                      |
| 7  Bridges & Unsupported  |                                                      |
| 8  Plan Preview           |                                                      |
| 9  Apply                  |                                                      |
| 10 Results                |                                                      |
|                           |                                                      |
+---------------------------+------------------------------------------------------+
| Status: 94 items discovered, 31 auto-importable, 12 need review, 18 deferred     |
| Keys: Tab move  Enter open  Space toggle  a approve-safe  d defer  p provenance   |
+----------------------------------------------------------------------------------+
```

Why this reduces friction:

- the operator always knows where they are,
- the footer advertises useful bulk actions,
- the status bar keeps the migration summary in view.

Why this builds trust:

- nothing is hidden behind mode changes,
- the source path, dry-run state, and step count are always visible.

## Screen 1: Source Select

Purpose: make it obvious what install will be scanned and what VIVARY will write.

```text
+----------------------------------------------------------------------------------+
| Source Select                                                                    |
+----------------------------------------------------------------------------------+
| OpenClaw source                                                                   |
|   [ ~/.openclaw                                             ] [Browse] [Rescan]  |
|                                                                                  |
| Output directory                                                                  |
|   [ ./migrate                                              ]                     |
|                                                                                  |
| Planned outputs                                                                   |
|   - findings.kdl                                                                  |
|   - report.md                                                                     |
|   - plan.kdl                                                                      |
|   - imports/ (only after Apply)                                                   |
|                                                                                  |
| Source health                                                                      |
|   [ok] openclaw.json found                                                        |
|   [ok] credentials/ present                                                       |
|   [ok] skills/ present                                                            |
|   [..] plugins/ not yet scanned                                                   |
|                                                                                  |
|                         [ Start Discovery ]      [ Cancel ]                       |
+----------------------------------------------------------------------------------+
```

Trust choices:

- show outputs before discovery starts,
- validate the path immediately,
- do not hide where files will land.

## Screen 2: Discovery Scan

Purpose: reassure the operator that the tool is doing real work and not freezing.

```text
+----------------------------------------------------------------------------------+
| Discovery Scan                                                                   |
+----------------------------------------------------------------------------------+
| Scanning OpenClaw install...                                                     |
|                                                                                  |
| [###########-----------------------------]  27%                                  |
|                                                                                  |
| Current phase                                                                     |
|   -> parsing openclaw.json                                                        |
|      scanning skills roots                                                        |
|      scanning plugin entries                                                      |
|      scanning credential stores                                                   |
|      classifying portability                                                      |
|                                                                                  |
| Live findings                                                                     |
|   portable                12                                                     |
|   portable_with_review     8                                                     |
|   bridge_required         11                                                     |
|   unsupported              3                                                     |
|                                                                                  |
| Recent discoveries                                                                 |
|   + skill      skills/research/SKILL.md                                           |
|   + plugin     plugins.entries.memory-lancedb-pro                                 |
|   + credential channels.telegram.botToken                                         |
|   + file       credentials/whatsapp/work/creds.json                               |
|                                                                                  |
|                            [ Stop After Scan ]                                    |
+----------------------------------------------------------------------------------+
```

Trust choices:

- show phases and live counters,
- show recent findings,
- let the operator stop after discovery and keep the report.

## Screen 3: Findings Overview

Purpose: summarize the install without overwhelming the operator.

```text
+----------------------------------------------------------------------------------+
| Findings Overview                                                                |
+----------------------------------------------------------------------------------+
| Install summary                                                                   |
|   Source                ~/.openclaw                                               |
|   Last modified         2026-03-24 11:52 UTC                                      |
|   Config parsed         yes                                                       |
|   Total findings        94                                                        |
|                                                                                  |
| Portability summary                                                                |
|   [portable]               31   skills, slash commands, prompt assets             |
|   [review]                 27   memory/search plugins, schedules, secrets         |
|   [bridge]                 18   channels, QR sessions, browser-side bridges       |
|   [unsupported]            18   raw extension code, native runtime plugins        |
|                                                                                  |
| High-value opportunities                                                           |
|   1. 14 skill assets can import automatically                                     |
|   2. 6 slash commands can become VIVARY macros                                    |
|   3. 3 standing orders can become disabled keeperd schedules                      |
|   4. 2 memory plugins likely map to Memory_* capabilities                         |
|                                                                                  |
| Highest-risk items                                                                 |
|   1. 4 channel connectors require bridge review                                   |
|   2. 7 credentials need vault mapping or re-auth                                  |
|   3. 5 extensions contain executable plugin code                                  |
|                                                                                  |
|          [ Review Findings ]   [ Export Report ]   [ Exit With Discovery Only ]   |
+----------------------------------------------------------------------------------+
```

Friction reduction:

- puts the important answer first,
- points to the likely wins,
- lets the operator stop here and still get value.

## Screen 4: Review Buckets

Purpose: triage by portability tier instead of forcing a file-by-file walkthrough.

```text
+----------------------------------------------------------------------------------+
| Review Buckets                                                                   |
+----------------------------------------------------------------------------------+
| Buckets                                                                           |
|   > Portable (31)                                                                 |
|     Review Required (27)                                                          |
|     Bridge Required (18)                                                          |
|     Unsupported (18)                                                              |
|                                                                                  |
| Portable items                                                                     |
|   [x] skill           skills/research/SKILL.md         -> prompts/imported/...    |
|   [x] skill           skills/ops/SKILL.md              -> prompts/imported/...    |
|   [x] slash-command   commands/daily.md                -> macros/daily.kdl        |
|   [x] standing-order  ops/standing-orders.md           -> schedules/ops.kdl       |
|   [x] prompt-pack     plugins/bundle/prompts.md        -> prompts/imported/...    |
|                                                                                  |
| Side panel                                                                         |
|   Selected item                                                                    |
|   Type:        skill                                                               |
|   Source:      skills/research/SKILL.md                                            |
|   Destination: prompts/imported/research.kdl                                       |
|   Why safe:    prompt asset only; no executable plugin code detected               |
|   Provenance:  bundled=false  plugin-owned=false  user-invocable=true              |
|                                                                                  |
| Actions                                                                            |
|   [Approve All Safe]  [View Diff Preview]  [Open Provenance]                       |
+----------------------------------------------------------------------------------+
```

Trust choices:

- the operator can bulk-approve low-risk imports,
- every item still has a provenance pane,
- the screen explains why something is considered safe.

## Screen 5: Credentials Mapping

Purpose: minimize credential pain while making unresolved secrets impossible to miss.

```text
+----------------------------------------------------------------------------------+
| Credentials Mapping                                                              |
+----------------------------------------------------------------------------------+
| Detected credential references: 12                                                |
| Resolved: 5   Needs action: 7                                                     |
|                                                                                  |
| Owner                         Kind                    Source            Action     |
| > channel:telegram/default    bot_token               env:TELEGRAM...   Map       |
|   channel:slack/default       app_token               inline(redacted)  Replace   |
|   channel:slack/default       signing_secret          inline(redacted)  Replace   |
|   provider:anthropic/default  setup_token             auth-store        Re-auth   |
|   plugin:memory-lancedb-pro   api_key                 inline(redacted)  Map       |
|   channel:whatsapp/work       device_pairing_session  creds.json        Re-link   |
|                                                                                  |
| Detail                                                                          |
|   Owner:          channel:whatsapp/work                                          |
|   Kind:           device_pairing_session                                          |
|   Storage:        credential_file                                                 |
|   Migration:      bridge_required / re-auth recommended                           |
|   Why:            linked-device sessions are not safely portable as vault items    |
|                                                                                  |
| Resolution                                                                       |
|   ( ) Map to existing vault credential                                            |
|   ( ) Create placeholder and block activation                                     |
|   (x) Require re-auth after migration                                             |
|                                                                                  |
|                    [ Apply Safe Mappings ]    [ Continue ]                        |
+----------------------------------------------------------------------------------+
```

Friction reduction:

- uses a small set of action verbs: `Map`, `Replace`, `Re-auth`, `Re-link`,
- preselects the recommended safe resolution.

Trust choices:

- raw secret values never appear,
- unresolved items stay visible in the header,
- session-style credentials are explicitly not treated like normal tokens.

## Screen 6: Capability and Approval Review

Purpose: convert OpenClaw behavior into governed VIVARY policy without drowning the operator in ACL syntax.

```text
+----------------------------------------------------------------------------------+
| Capability and Approval Review                                                   |
+----------------------------------------------------------------------------------+
| Proposed VIVARY policy                                                            |
|                                                                                  |
| Agent template: imported-openclaw-main                                            |
|                                                                                  |
| Capability grants                                                                  |
|   [x] Search_Query_Read                 scope="web:*"                             |
|   [x] Browser_Page_Read                scope="https://docs.openclaw.ai/*"        |
|   [ ] Execution_Command_Run            approval=required                          |
|   [x] Filesystem_File_Write            scope="workspace:/imports/*"              |
|   [ ] Messaging_Message_Send           deferred with bridge                        |
|   [x] Memory_Record_Read               scope="agent:self"                        |
|   [x] Memory_Record_Write              approval=required                          |
|                                                                                  |
| Approval defaults                                                                   |
|   write actions       -> approval required                                        |
|   send actions        -> approval required                                        |
|   exec/system actions -> approval required                                        |
|   read/search         -> auto-allow within scope                                  |
|                                                                                  |
| Why this proposal                                                                   |
|   Detected plugin/tool usage suggests search, memory, and workspace writes are     |
|   needed. Messaging remains deferred because no bridge is active in this plan.     |
|                                                                                  |
|       [ Keep Recommended Defaults ]   [ Advanced Scope Editor ]                   |
+----------------------------------------------------------------------------------+
```

Trust choices:

- show policy in operator language first,
- offer advanced editing without requiring it,
- explain why a capability was proposed.

## Screen 7: Bridges and Unsupported

Purpose: turn potential disappointment into explicit, inspectable decisions.

```text
+----------------------------------------------------------------------------------+
| Bridges and Unsupported                                                          |
+----------------------------------------------------------------------------------+
| Bridge-required items                                                              |
|   [x] channel plugin   wechat                -> generate bridge stub              |
|   [x] channel plugin   dingtalk              -> generate bridge stub              |
|   [ ] channel plugin   whatsapp              -> defer entirely                    |
|   [x] browser bridge   camofox-browser       -> preserve metadata only            |
|                                                                                  |
| Unsupported items                                                                  |
|   [x] extension code   extensions/custom.ts  -> preserve source path in report    |
|   [x] native plugin    plugins/foo/index.js  -> preserve source path in report    |
|   [x] mobile node      ios pairing state     -> preserve source path in report    |
|                                                                                  |
| Selected item detail                                                               |
|   Item:        plugins.entries.wechat                                              |
|   Decision:    generate bridge stub, disabled                                      |
|   Why:         popular ingress path, but outside VIVARY runtime core               |
|   Output:      bridge/wechat.kdl                                                   |
|   Activation:  not enabled; operator must install bridge later                     |
|                                                                                  |
|                  [ Accept Deferred Plan ]   [ Review Unsupported Report ]          |
+----------------------------------------------------------------------------------+
```

Trust choices:

- unsupported items do not disappear,
- bridge generation is explicit and disabled by default,
- the operator sees exactly what "defer" means.

## Screen 8: Plan Preview

Purpose: provide a final human-checkable summary before writing anything.

```text
+----------------------------------------------------------------------------------+
| Plan Preview                                                                     |
+----------------------------------------------------------------------------------+
| This migration will write:                                                        |
|                                                                                  |
|   prompts/imported/                  14 files                                     |
|   macros/imported/                    6 files                                     |
|   schedules/imported/                 3 files (disabled)                          |
|   policies/imported/                  2 files                                     |
|   bridge/                             2 stub configs                              |
|   originals/                         11 preserved references                      |
|   report.md                           1 report                                    |
|   findings.kdl                        1 inventory                                 |
|   plan.kdl                            1 plan                                      |
|                                                                                  |
| This migration will not enable:                                                   |
|   - channel bridges                                                               |
|   - send/exec approvals                                                           |
|   - unresolved credentials                                                        |
|                                                                                  |
| Blocking items: 3                                                                 |
|   - anthropic setup-token requires re-auth                                        |
|   - whatsapp device session requires re-link                                      |
|   - one native plugin preserved as unsupported                                    |
|                                                                                  |
|             [ Dry Run Diff ]   [ Write Plan Only ]   [ Apply Migration ]          |
+----------------------------------------------------------------------------------+
```

Friction reduction:

- the operator can choose `Write Plan Only`,
- the write set is summarized by destination folder, not dumped as raw paths.

## Screen 9: Apply / Dry Run

Purpose: make writes feel predictable and reversible.

```text
+----------------------------------------------------------------------------------+
| Apply Migration                                                                  |
+----------------------------------------------------------------------------------+
| Mode: Dry Run                                                                     |
|                                                                                  |
| Planned operations                                                                 |
|   CREATE  prompts/imported/research.kdl                                           |
|   CREATE  macros/imported/daily.kdl                                               |
|   CREATE  schedules/imported/ops.kdl                                              |
|   CREATE  policies/imported/main-agent.kdl                                        |
|   CREATE  bridge/wechat.kdl                                                       |
|   WRITE   report.md                                                               |
|   WRITE   findings.kdl                                                            |
|   WRITE   plan.kdl                                                                |
|                                                                                  |
| Safety checks                                                                      |
|   [ok] no existing VIVARY files will be overwritten without backup                |
|   [ok] unresolved credentials remain placeholders                                 |
|   [ok] no imported action is auto-enabled                                         |
|   [ok] unsupported items are report-only                                          |
|                                                                                  |
|                        [ Run Dry Run ]   [ Switch To Apply ]                      |
+----------------------------------------------------------------------------------+
```

Trust choices:

- dry run is the default,
- safety checks are explicit,
- the operator sees file-level effects before apply.

## Screen 10: Results and Next Steps

Purpose: close the loop and give the operator a clear, low-stress next move.

```text
+----------------------------------------------------------------------------------+
| Migration Results                                                                |
+----------------------------------------------------------------------------------+
| Result: Plan written successfully                                                 |
|                                                                                  |
| Outputs                                                                            |
|   findings.kdl         ./migrate/findings.kdl                                     |
|   report.md            ./migrate/report.md                                        |
|   plan.kdl             ./migrate/plan.kdl                                         |
|   imports/             ./migrate/imports/                                         |
|                                                                                  |
| Imported now                                                                        |
|   - 14 prompt assets                                                               |
|   - 6 macros                                                                       |
|   - 3 disabled schedules                                                           |
|                                                                                  |
| Still blocked                                                                       |
|   - 2 credentials need re-auth                                                     |
|   - 2 channel bridges remain disabled                                              |
|   - 1 native plugin remains unsupported                                            |
|                                                                                  |
| Recommended next steps                                                              |
|   1. Resolve credential placeholders                                               |
|   2. Review generated policy scopes                                                |
|   3. Enable imported schedules after dry-run validation                            |
|   4. Install channel bridges only if needed                                        |
|                                                                                  |
|                [ Open Output Folder ]   [ View Report ]   [ Finish ]              |
+----------------------------------------------------------------------------------+
```

Why this builds trust:

- it distinguishes completed work from blocked work,
- it ends with concrete next steps instead of a vague success message.

## Detail Views

The main screens above stay concise. Deep inspection happens in dedicated panels.

### Provenance Drawer

```text
+----------------------------------------------------------------------------------+
| Provenance                                                                       |
+----------------------------------------------------------------------------------+
| Source path        ~/.openclaw/skills/research/SKILL.md                           |
| Source type        skill asset                                                    |
| Detected by        skills adapter                                                 |
| Confidence         high                                                           |
| Portability        portable                                                       |
| Destination        prompts/imported/research.kdl                                  |
| Warnings           references env var OPENAI_API_KEY                              |
|                                                                                  |
| Original excerpt                                                                   |
|   ---                                                                             |
|   name: research                                                                  |
|   description: Deep research workflow                                             |
|   ---                                                                             |
|   Use search, summarize findings, cite sources...                                 |
+----------------------------------------------------------------------------------+
```

### Diff Preview Drawer

```text
+----------------------------------------------------------------------------------+
| Diff Preview                                                                     |
+----------------------------------------------------------------------------------+
| Source: skills/research/SKILL.md                                                  |
| Dest:   prompts/imported/research.kdl                                             |
|                                                                                  |
| - user-invocable: true                                                            |
| + macro exposure: true                                                            |
|                                                                                  |
| - OpenClaw-only plugin directive removed                                          |
| + provenance metadata added                                                       |
|                                                                                  |
| + prompt-body "Use search, summarize findings, cite sources..."                  |
+----------------------------------------------------------------------------------+
```

## Bulk Actions That Reduce Friction Safely

The final UX should support a few carefully chosen bulk actions:

- `Approve All Safe`
- `Map Known Env Vars`
- `Defer All Bridges`
- `Preserve All Unsupported As Report-Only`
- `Require Approval For All Writes`

These reduce repetitive clicks while remaining conservative.

Bulk actions that should **not** exist:

- `Enable Everything`
- `Auto-import Secrets`
- `Trust All Plugins`
- `Port Channels Into Core`

## Trust Signals

The TUI should deliberately include trust-building cues throughout:

- dry-run badge at the top of every planning/apply screen
- count of unresolved items in the footer/header
- explicit disabled-by-default labels on schedules, bridges, and risky capabilities
- provenance hotkey on every review screen
- visible distinction between `portable_with_review` and `unsupported`
- a final report that includes skipped items, not just imported items

## Friction Traps To Avoid

- asking the operator to classify every plugin manually
- forcing credential resolution before discovery results are visible
- mixing low-risk prompt imports with high-risk channel decisions on the same screen
- presenting raw KDL or JSON as the only review surface
- hiding defaults or auto-selected actions
- implying that bridge-required items are already functional after migration

## Final Recommendation

The final migration UX should feel like a careful import wizard for operators, not a consumer-style one-click transfer. The fastest path should be:

- discover automatically,
- bulk-approve obviously safe prompt/macro imports,
- resolve only the decisions that materially affect trust,
- dry-run by default,
- write a complete, inspectable plan before live activation.

That combination keeps the workflow low-friction for common cases while preserving the operator confidence VIVARY depends on.
