# AGENTS.md

This file is guidance for code agents working in the VIVARY repository.

## Project Priorities

- Preserve the core product focus: VIVARY is a governed runtime for isolated, auditable AI agents.
- Favor the current MVP direction: single-agent runtime core first, multi-agent orchestration later.
- Keep the security model legible. Do not blur the boundary between `ward` and `keeperd`.
- Prefer designs that improve operator trust, debuggability, and policy clarity over designs that add cleverness.

## Architectural Intent

- `keeperd` is the semantic and policy authority.
- `ward` is the per-agent syntax/protocol adapter and subprocess manager.
- Keep `ward` narrow: it should parse backend-specific tool-call output, validate structural/schema correctness, translate to MUS, and report local execution facts.
- Keep semantic enforcement in `keeperd`: ACLs, scope constraints, schedule/rate limits, approvals, credential resolution, audit policy, and dispatch.
- Do not casually move policy logic into `ward`.
- Do not expand multi-agent topology features into MVP-scoped work unless explicitly asked.

## Implementation Guidelines

- Prefer small, composable packages with clear boundaries.
- Optimize for correctness and inspectability before optimization for throughput.
- Preserve the MUS-based transport model unless there is a strong documented reason to change it.
- Avoid introducing dynamic plugin systems or runtime capability registration.
- Keep capability naming and schema discipline strict: `Namespace_Noun_Verb`, stable JSON schema, explicit examples/descriptions.
- Prefer explicit failure modes and structured errors over silent fallback behavior.
- When adding logs or events, make sure they are useful to both humans and CLI tooling.
- Build CLI-first debug surfaces; the TUI should not be the only way to understand runtime behavior.

## Testing Expectations

Testing is a first-class requirement in this repo. New work is not complete without tests.

- Add or update unit tests for each meaningful code change.
- Add or update integration tests when behavior crosses process, container, routing, capability, or persistence boundaries.
- Prefer writing the test that proves the intended behavior before or alongside implementation.
- Test both success paths and denial/failure paths.
- Test security boundaries explicitly, not just happy paths.
- Test operator-observable behavior: logs, events, CLI output, and error messages.
- Keep tests deterministic where possible; avoid timing-sensitive flakiness.

## Unit Testing Requirements

- MUS codec changes must include round-trip and malformed-input coverage.
- Routing/control-plane changes must include identity stamping, sequence enforcement, and back-pressure coverage.
- Capability changes must include schema validation and ACL/scope enforcement coverage.
- `ward` changes must include malformed tool-call parsing, schema-invalid requests, subprocess lifecycle behavior, and local failure reporting.
- `keeperd` changes must include semantic denial cases, approval behavior where relevant, and audit/event emission.
- CLI/debug tooling changes must include output and filtering behavior tests where practical.

## Integration Testing Requirements

- Any change affecting `keeperd` ↔ `ward` behavior should be covered by an integration test.
- Any new capability should have an end-to-end test proving allow and deny behavior.
- Any change to filesystem or network isolation should include an integration test that attempts the prohibited action.
- Any change to audit logging should include a test that verifies records can be inspected via the intended tooling.
- Any change to prompt execution flow should include a test covering prompt arrival, tool usage, completion event emission, and failure handling.

## KDL Expectations

- Always default to KDLv2 syntax, not KDLv1.
- Avoid using extra quote marks where not needed. (e.g. identifiers without special chars)

## Documentation Expectations

- Update docs when behavior, architecture, scope, or terminology changes.
- Keep MVP-vs-later-phase distinctions explicit.
- Use VIVARY consistently; do not introduce legacy project names.
- If a tradeoff is unresolved, document it plainly instead of implying it is settled.

## Good Changes

- Make policy denials easier to understand.
- Make `vivlog` more useful.
- Reduce trusted-surface complexity.
- Tighten capability schemas and validation.
- Improve test coverage around boundaries and failure modes.
- Simplify the MVP while preserving the core security/governance story.

## Changes To Avoid

- Expanding scope with speculative features before the runtime core is solid.
- Moving semantic policy decisions out of `keeperd`.
- Adding hidden coupling between TUI behavior and core runtime behavior.
- Introducing ad hoc capability names, weak schemas, or undocumented message types.
- Shipping code without unit and integration coverage proportionate to the change.

## When In Doubt

- Choose the simpler design.
- Choose the more testable design.
- Choose the design that makes failures easier to inspect.
- Choose the design that preserves the `ward`/`keeperd` boundary.
- Ask whether the change strengthens the single-agent governed runtime before it strengthens later swarm features.
- Don't add new dependencies without a very good reason, and ask for confirmation.

## Version Control

- Use `jj` for tracking each work increment.
- Write clear commit messages to describe what has been achieved.

## context-mode — MANDATORY routing rules

You have context-mode MCP tools available. These rules are NOT optional — they protect your context window from flooding. A single unrouted command can dump 56 KB into context and waste the entire session.

## BLOCKED commands — do NOT attempt these

### curl / wget — BLOCKED

Any shell command containing `curl` or `wget` will be intercepted and blocked by the context-mode plugin. Do NOT retry.
Instead use:

- `context-mode_ctx_fetch_and_index(url, source)` to fetch and index web pages
- `context-mode_ctx_execute(language: "javascript", code: "const r = await fetch(...)")` to run HTTP calls in sandbox

### Inline HTTP — BLOCKED

Any shell command containing `fetch('http`, `requests.get(`, `requests.post(`, `http.get(`, or `http.request(` will be intercepted and blocked. Do NOT retry with shell.
Instead use:

- `context-mode_ctx_execute(language, code)` to run HTTP calls in sandbox — only stdout enters context

### Direct web fetching — BLOCKED

Do NOT use any direct URL fetching tool. Use the sandbox equivalent.
Instead use:

- `context-mode_ctx_fetch_and_index(url, source)` then `context-mode_ctx_search(queries)` to query the indexed content

## REDIRECTED tools — use sandbox equivalents

### Shell (>20 lines output)

Shell is ONLY for: `git`, `mkdir`, `rm`, `mv`, `cd`, `ls`, `npm install`, `pip install`, and other short-output commands.
For everything else, use:

- `context-mode_ctx_batch_execute(commands, queries)` — run multiple commands + search in ONE call
- `context-mode_ctx_execute(language: "shell", code: "...")` — run in sandbox, only stdout enters context

### File reading (for analysis)

If you are reading a file to **edit** it → reading is correct (edit needs content in context).
If you are reading to **analyze, explore, or summarize** → use `context-mode_ctx_execute_file(path, language, code)` instead. Only your printed summary enters context.

### grep / search (large results)

Search results can flood context. Use `context-mode_ctx_execute(language: "shell", code: "grep ...")` to run searches in sandbox. Only your printed summary enters context.

## Tool selection hierarchy

1. **GATHER**: `context-mode_ctx_batch_execute(commands, queries)` — Primary tool. Runs all commands, auto-indexes output, returns search results. ONE call replaces 30+ individual calls.
2. **FOLLOW-UP**: `context-mode_ctx_search(queries: ["q1", "q2", ...])` — Query indexed content. Pass ALL questions as array in ONE call.
3. **PROCESSING**: `context-mode_ctx_execute(language, code)` | `context-mode_ctx_execute_file(path, language, code)` — Sandbox execution. Only stdout enters context.
4. **WEB**: `context-mode_ctx_fetch_and_index(url, source)` then `context-mode_ctx_search(queries)` — Fetch, chunk, index, query. Raw HTML never enters context.
5. **INDEX**: `context-mode_ctx_index(content, source)` — Store content in FTS5 knowledge base for later search.

## Output constraints

- Keep responses under 500 words.
- Write artifacts (code, configs, PRDs) to FILES — never return them as inline text. Return only: file path + 1-line description.
- When indexing content, use descriptive source labels so others can `search(source: "label")` later.

## ctx commands

| Command | Action |
|---------|--------|
| `ctx stats` | Call the `stats` MCP tool and display the full output verbatim |
| `ctx doctor` | Call the `doctor` MCP tool, run the returned shell command, display as checklist |
| `ctx upgrade` | Call the `upgrade` MCP tool, run the returned shell command, display as checklist |
