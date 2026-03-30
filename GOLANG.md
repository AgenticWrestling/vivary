# GOLANG

Practical Go naming guidance for this repo, distilled from Alex Edwards' article on Go naming conventions and adapted for day-to-day use here.

## Core idea

Prefer names that are boring, predictable, and easy to scan.

- Optimize for clarity over cleverness.
- Use the name that a Go programmer would guess before reading the implementation.
- Keep names short when the scope is small, and more descriptive when the scope is broad.

## Identifiers

- Use `camelCase` for unexported names.
- Use `PascalCase` for exported names.
- Do not use `snake_case`, `SCREAMING_SNAKE_CASE`, or mixed separator styles.
- Stick to ASCII unless there is a very strong reason not to.
- Keep initialisms consistent: `API`, `URL`, `HTTP`, `ID`, `JSON`, `ACL`, `CLI`.
  - Good: `agentID`, `APIURL`, `ctlSocketPath`
  - Bad: `agentId`, `ApiUrl`, `CtlSocketPath`

## Packages

- Package names should be lowercase and usually one word.
- Prefer short noun-like names that match the package's responsibility.
- Avoid separators in package names.
- Avoid vague package names like `util`, `common`, `helpers`, or `misc`.
- Avoid names that stutter at call sites.
  - Prefer `audit.Record`, not `audit.AuditRecord`
  - Prefer `config.ParseAgentKDL`, not `config.ParseConfigAgentKDL`

## Types

- Name types for what they are, not for how they are implemented.
- Avoid redundant suffixes like `Struct`, `Interface`, `Object`, or `Data` unless they add real meaning.
- Avoid type names that merely repeat the package name.

## Functions and methods

- Name functions for the behavior they provide.
- Prefer straightforward verbs like `Parse`, `Load`, `Write`, `Validate`, `Decode`, `Dispatch`, `Start`.
- Use `New` for constructors when it is the natural default constructor.
- Do not prefix getters with `Get`.
  - Prefer `Status()`, not `GetStatus()`
- Prefix setters with `Set` when a setter is actually needed.

## Variables

- Short names are fine for tight local scope.
  - `i`, `n`, `r`, `w`, `cfg`, `err`, `ctx`
- Use more descriptive names when scope widens or multiple similar values are in play.
- Avoid one-letter names when they make control flow or data ownership unclear.

## Receivers

- Use short receiver names, usually 1-3 characters.
- Keep the receiver name consistent across all methods on the same type.
- Do not use `this`, `self`, or `me`.
  - Good: `func (d *daemon) run(...)`
  - Good: `func (w *ward) executeTool(...)`

## Interfaces

- Small interfaces should usually describe capability, often with an `-er` shape.
  - `Reader`, `Writer`, `Dispatcher`, `Validator`
- Do not name interfaces `ThingInterface`.
- Define interfaces where they are consumed, not automatically where they are implemented.

## Files

- File names should be lowercase and summarize contents.
- Prefer one word when practical: `router.go`, `codec.go`, `audit.go`.
- If multiple words are needed, keep usage consistent within the repo.
- Reserve special suffixes for real Go meaning, such as `_test.go`.

## Errors

- Error values should read naturally in logs and wrapped messages.
- Use lowercase error strings without trailing punctuation.
- Prefer context-rich messages at boundaries.
  - Good: `return fmt.Errorf("parse agent config: %w", err)`

## Repo-specific preferences

- Preserve existing domain terms: `keeperd`, `ward`, `viv`, `vivlog`, `chromed`, `capwrap`, `MUS`, `KDL`.
- Keep control-plane and runtime names explicit rather than cute.
- Favor names that make policy boundaries obvious.
  - Good: `dispatchCapabilityRequest`
  - Good: `SchemaErrorRetries`
  - Good: `CapabilityResponsePayload`
- Avoid generic names in security-sensitive code; the name should make the boundary obvious.

## Quick checklist

Before introducing a new name, ask:

1. Would an experienced Go developer guess this name?
2. Does it avoid package stutter?
3. Is the casing idiomatic, including initialisms?
4. Is it as short as possible without becoming vague?
5. Does it make the boundary or responsibility clearer?

## Rule of thumb

If two names are both accurate, pick the one that feels more ordinary.
