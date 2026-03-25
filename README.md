# VIVARY

**V**irtualized **I**solated **V**erifiable **A**gent **R**untime **Y**ard

*Coordinate AI agents in ways you can actually explain.*

VIVARY is a governed runtime for isolated, auditable AI agents on a shared host. The initial product focus is a single agent running inside a hardened `systemd-nspawn` enclosure with no credentials, no raw network access beyond its LLM API endpoint, and no visibility beyond its own filesystem and a tightly controlled [MUS](https://github.com/mus-format/mus-go) formatted stdio pipe to the central `keeperd` daemon. Multi-agent swarm orchestration is a later phase built on top of this runtime core.

## Core Features

- **Triple-Boundary Isolation:** Each agent's only environmental access is (1) its own Btrfs subvolume filesystem, (2) a MUS stdio pipe to `keeperd`, and (3) a firewalled veth interface permitting outbound connections to its configured LLM API endpoint _only_. No other network sockets. No credentials in the container.

- **O(1) Persistence:** Btrfs subvolumes per agent, with atomic snapshot-based rollback for operator-directed reconfiguration and recovery; more autonomous evolution flows are deferred.

- **Vivary Keeper: Orchestrator & Policy Core:** A stdio-native control plane in `keeperd` that owns routing, semantic policy enforcement, credential resolution, approvals, and audit logging. The MVP focuses on one agent and one ctl connection; multi-agent routing is introduced in a later phase.

- **Vivary Ward:** Each agent's nspawn container runs a purpose-built Go binary — the **Ward** — that manages the LLM subprocess lifecycle and acts as a syntax/protocol adapter between an LLM's tool-call format and VIVARY's MUS capability schema. It rejects malformed calls locally, forwards well-formed requests to `keeperd`, and returns results. It is the agent's sole control plane.

- **Capability-as-CLI:** Capabilities appear to the LLM as self-documenting bash-invokable CLI tools. Running any capability with `--help` returns its full JSON schema. The Ward handles the syntax translation layer from those invocations into MUS frames, while `keeperd` remains the semantic authority on whether a request is actually allowed.

- **Headless External Access:** Safe, auditable access to the outside world:
  - **Chrome CDP Firewall:** A shared headless Chrome instance runs on the host OS. `keeperd` proxies whitelisted, carefully designed resource+capability (noun/verb) commands — agents _never_ connect to the debug port directly.

  - **REST Gateway Binaries:** Heavyweight APIs (Google Workspace, etc.) are a planned post-MVP extension. The runtime is designed to front them through thin host-side binaries invoked by `keeperd`.

- **Credential-Reference Model:** Agents reference a `credential_id` only. Full encrypted vault workflows remain a later phase rather than a completed MVP feature.

- **Identity Integrity:** Agent identity (`FromID`) is stamped by `keeperd` based on which pipe a message arrived on. Agents cannot spoof each other's identities.

- **Structured Logging:** Three log streams — per-prompt completion events (tokens, cost, context %), agent failure events (schema mismatch, loop detection, crashes), and a binary MUS audit trail in a SQLite WAL.

- **Unix-Style Debug Tooling:** `vivlog` decodes and inspects MUS audit records from the WAL, supports grep-friendly filters, and gives operators a simple CLI for understanding what the runtime actually did.

- **Linux-First Runtime Packaging:** Packaged as a NixOS LXD/LXC container via a `distrobuild` Nix Flake path. Linux is the current full runtime/isolation story; macOS and WSL2 are still primarily development and validation environments.

## Binaries

| Binary | Role |
|---|---|
| `keeperd` | Central daemon — message router, policy enforcer, and audit authority |
| `ward` | Per-agent binary inside each nspawn container — LLM lifecycle manager |
| `viv` | Operator TUI and CLI — connects to `keeperd` via MUS-over-Unix-socket |
| `vivlog` | Audit log reader — decodes and inspects the SQLite MUS audit trail |

## Documentation

- [docs/DESIGN.md](docs/DESIGN.md) — Full system architecture
- [docs/CAPABILITIES.md](docs/CAPABILITIES.md) — Capability model, ECS resource system, and policy grant design
- [docs/SECURITY.md](docs/SECURITY.md) — Threat model, isolation boundaries, and operational security notes
- [docs/PLAN.md](docs/PLAN.md) — Phased implementation plan and testing strategy

## Getting Started

See `USAGE.md` for the current build, test, local runtime, and LXD workflow instructions.

## Target: v0.1 MVP

- Single-agent governed runtime: `keeperd`, `ward`, and `viv` ctl/TUI for one local agent.
- `distrobuild` Nix Flake script for the base NixOS LXD container (the "vivary").
- nspawn agent workspace provisioning from a template subvolume.
- Initial capability set kept intentionally narrow: `Browser_Page_Read` plus scoped filesystem output.
- Structured audit/debug tooling, including `vivlog`, before expanding into multi-agent orchestration.
