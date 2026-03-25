# OpenClaw Migration Plan

## Goal

Ship a useful v0 before any live migration path exists.

The first deliverable is:

- inspect an OpenClaw install,
- classify what it contains,
- write findings to KDL under `./migrate/`,
- give operators a concrete inventory of what is portable, review-required, bridge-required, or unsupported.

## v0 Scope

Command:

```text
viv migrate openclaw inspect --source ~/.openclaw --out ./migrate/findings.kdl
```

v0 does not apply changes to VIVARY. It only discovers and reports.

## Implementation Shape

### CLI

- add `viv migrate openclaw inspect`
- keep it local and file-based; no keeperd dependency
- default output path: `migrate/findings.kdl`

### Go package

Create a dedicated discovery package in `internal/migrate`.

Responsibilities:

- resolve and validate the OpenClaw source directory
- inspect `openclaw.json` when present
- walk the install for high-value artifact types
- classify plugin/category portability
- detect credential references without exposing raw secrets
- render a stable KDL findings document

### v0 discovery targets

- `openclaw.json`
- channel config blocks
- plugin entries
- provider credential references
- `SKILL.md` files
- extension code under `extensions/`
- credential files under `credentials/`
- standing-order and slash-command file heuristics

## KDL Output

Write one machine-readable file:

- `migrate/findings.kdl`

The file should contain:

- source metadata
- config parse status
- summary counts
- portability-tier counts
- discovered artifacts
- channels
- plugins with nearest VIVARY category
- credential references with redacted locators

## Portability Rules In v0

- skills -> `portable`
- slash command assets -> `portable`
- config -> `portable_with_review`
- memory/search/vault plugins -> `portable_with_review`
- channel plugins -> `bridge_required`
- credential files and secret refs -> `portable_with_review` or `bridge_required` depending on type
- raw extension code -> `unsupported`

## Why KDL First

- it matches the rest of the repo's operator-facing config style
- it is easy to diff and inspect
- it keeps the migration report legible for humans and tools

## Test Plan

- unit test config parsing and credential classification
- unit test artifact discovery from a synthetic OpenClaw tree
- unit test KDL rendering
- command test for `viv migrate openclaw inspect`

## Follow-On Work After v0

1. add a markdown summary next to the KDL findings
2. add richer config parsing for automation and provider models
3. add scope/entity inference from discovered plugin and config hints
4. define a bridge stub schema for channel/connectivity migrations
5. add plan generation from KDL inventory
6. add apply-time import for skills, commands, and standing orders

## Design Notes From Critique

Some follow-on requirements became clearer after reviewing the migration design against the broader VIVARY architecture.

### Scope and entity inference

Discovery should eventually do more than classify broad plugin categories such as `Memory`, `Search`, or `Filesystem`.

It should also collect **scope hints** that can help the later planner generate useful VIVARY policy suggestions, for example:

- URL patterns from browser/search-related config
- path prefixes from vault or filesystem-oriented plugins
- likely provider/data-source identifiers from memory/search backends

The goal is not to auto-grant access. The goal is to make `plan.kdl` more concrete and less dependent on hand-written scope reconstruction.

### Bridge stub schema

The design already treats channels as `bridge_required`, but the stub output itself should become more explicit in the next phase.

A bridge stub will likely need to capture at least:

- source bridge kind
- bridge identity
- `keeper.sock` path or other ctl endpoint
- allowed target agent IDs
- disabled-by-default state
- provenance back to the imported OpenClaw config/plugin

v0 does not need to implement this schema, but it should preserve enough discovery metadata to support it cleanly.

### Approval intent versus current runtime support

The migration design uses approval-required defaults as a safety posture for write/send/exec actions.

Because approval behavior is not yet fully implemented across the runtime, migration outputs should be careful to distinguish between:

- intended future approval policy, and
- what the current runtime can actually enforce today.

So the planner should prefer one of these conservative outcomes:

- mark the action disabled until review/runtime support exists, or
- emit explicit policy intent metadata while keeping the imported item non-active by default.

This preserves operator trust without pretending unsupported enforcement already exists.
