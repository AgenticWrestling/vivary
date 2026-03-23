# VIVIARY Refactoring and Improvement Notes

These notes are based on an investigation of the current VIVIARY codebase compared to the architectural vision in `DESIGN.md`, `SECURITY.md`, and `CAPABILITIES.md`.

## 1. Configuration & Parsing

### Current State
- `keeperd` uses a hand-rolled minimal KDL parser in `config.go`.
- It only supports simple single-line nodes and basic blocks.
- `AgentConfig` and `OrchestratorConfig` are partially implemented and somewhat fragmented.

### Recommendations
- **Adopt a Robust KDL Library**: Replace the hand-rolled parser with a feature-complete library (e.g., `github.com/samber/kdl-go` or `github.com/knadh/koanf`).
- **Unified Config Schema**: Align the Go structs strictly with the schemas described in `DESIGN.md`.
- **Validation**: Add structured validation for configuration files (e.g., ensuring agent IDs are unique and follow naming conventions).

## 2. Agent Provisioning (`provisioning.go`)

### Current State
- Heavily relies on `exec.Command` for `systemd-nspawn`, `btrfs`, `machinectl`, and `nft`.
- Provisioning sequence is linear and has limited error recovery.
- Network isolation (nftables) is implemented but may be fragile due to interface naming (`ve-<machine>`).

### Recommendations
- **Abstraction Layer**: Create a `ContainerRuntime` interface to abstract the details of `nspawn` and `machinectl`.
- **Robustness**: Implement better error handling and cleanup logic. If a provisioning step fails, the partially created state (subvolumes, interfaces) should be reliably torn down.
- **Dynamic UID Allocation**: The `DESIGN.md` mentions a 65,536-entry UID range allocation system. Current implementation uses `-U` but doesn't seem to manage the specific ranges dynamically as planned.

## 3. Ward & Tool Execution

### Current State
- `ward` spawns `claude` and scans `stdout` for JSON lines. This is tightly coupled to the Claude Code CLI output format.
- Tool interception uses a local Unix socket (`toolserver.go`) which capability CLIs connect to.
- Hard-coded schemas for MVP capabilities in `ward/main.go`.

### Recommendations
- **Generalize LLM Subprocess**: Abstract the LLM interface (`AgentCLI`) to support different providers (Claude, Gemini, etc.) without modifying the core `ward` logic.
- **Model Context Protocol (MCP)**: Investigate if VIVIARY can act as an MCP host or proxy to simplify tool discovery and execution.
- **Schema Synchronization**: Use `vivary-gen` to generate the schemas used by `ward` instead of hard-coding them.

## 4. Capability System (`internal/capabilities`)

### Current State
- `Capability` interface is simple but effective.
- `Dispatcher` handles ACL checks and execution.
- `Scope` is currently a raw string passed through `context`.

### Recommendations
- **ECS Resource Model**: Transition the raw `Scope` string to the Entity-Component-System model described in `CAPABILITIES.md`. Use the resource types and scope-constraint keys (e.g., `to-domain`, `path-prefix`).
- **Audit Policy**: Implement `shouldAuditPayload` based on capability categories and risk levels.
- **Approval Gate**: Implement the `MsgType_CtlApproval` flow (Phase 2) to allow operators to approve/deny sensitive actions.

## 5. Switchboard & MUS Protocol

### Current State
- `Router` handles basic identity stamping and rate limiting.
- `SeqNo` monotonicity check is implemented.
- `peek [2]byte` logic in `readLoop` is used for early rate-limiting but adds complexity.

### Recommendations
- **Router Robustness**: Handle `SeqNo` wrapping or resets more gracefully.
- **Multi-Agent Routing**: Prepare the router for Phase 5 (unicast, multicast, broadcast) by expanding the `ToID` handling.
- **Performance**: Benchmark the `io.MultiReader` approach vs a buffered reader for rate limiting.

## 6. Audit & Logging

### Current State
- SQLite WAL implementation is solid.
- `SecurityEvent` and `Frame` records are stored.

### Recommendations
- **PII Protection**: Ensure sensitive fields (like email bodies) are reliably excluded from the log by default.
- **Log Rotation/Pruning**: Add a mechanism to manage the growth of the audit database.
- **Encryption at Rest**: Plan for SQLCipher integration as mentioned in `SECURITY.md`.

## 7. Developer Ergonomics & Tooling

### Current State
- `Taskfile.yml` exists but could be expanded.
- `jj` is used for version control.

### Recommendations
- **Automated Tests**: Expand unit tests for `provisioning.go` (mocking the shell commands) and `ward` loop detection.
- **`vivary-gen` Integration**: Ensure `vivary-gen` is integrated into the build pipeline to keep schemas in sync.
- **Nix Integration**: Further tighten the Nix-based reproducible environment to include all required host-side tools (`nft`, `btrfs-progs`, etc.).
