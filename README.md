# WRESTLE

**W**orkspace **R**emote **E**xecution **S**warm **T**raceable **L**inux **E**ngine

WRESTLE is an agent orchestration engine for running swarms of isolated, auditable AI agents on a shared host. It provides high-performance, secure, and fully traceable environments where agents collaborate, invoke capabilities, and evolve iteratively — without ever holding credentials or touching raw network sockets.

## Core Features

- **High-Grade Isolation:** Each agent runs inside a `systemd-nspawn` container with User Namespacing (`-U`), isolating process tree, IPC, and hostname.
- **O(1) Persistence:** Btrfs subvolumes per agent, with atomic snapshot-based rollback before any self-directed configuration changes.
- **Stdio Switchboard:** A purely stdio-native message router in the Orchestrator. MUS-encoded binary frames are multiplexed across all active agent pipes, with unicast, multicast, and broadcast routing. No network message broker required.
- **Gateway Architecture:** Each agent's nspawn container runs a purpose-built Go **Gateway binary** that manages the LLM subprocess lifecycle (Claude Code or alternatives), translates JSON tool calls to MUS capability requests, and returns results — acting as the agent's sole control plane.
- **Headless Cognitive Gateway:** Safe, auditable access to external systems:
  - **Chrome CDP Firewall:** A shared headless Chrome instance runs on the host OS. The Orchestrator proxies whitelisted, MUS-wrapped CDP verbs to Chrome's debug port — agents never connect to the port directly.
  - **RESTful API Simplification:** Heavyweight APIs (Google Workspace, etc.) are wrapped by thin Go gateway binaries. The Orchestrator translates MUS verbs into authenticated API calls, stripping metadata bloat before returning results.
- **Zero-Exposure Credential Management:** The Orchestrator holds all secrets in an AES-256-GCM encrypted vault. Agents reference a `credential_id`; the secret value never enters the nspawn jail.
- **Identity Integrity:** Agent identity (`FromID`) is stamped by the Orchestrator based on which pipe a message arrived on. Agents cannot spoof each other's identities.
- **Structured Logging:** Three log streams — per-prompt completion events (tokens, cost, context %), agent failure events (schema mismatch, loop detection, crashes), and a binary MUS audit trail in a SQLite WAL.
- **Cross-Platform Parity:** Packaged as a NixOS LXD/LXC container via a `distrobuild` Nix Flake script. Identical execution environments on Linux (native LXD), Windows 11 (WSL2), and macOS (OrbStack/Lima).

## Getting Started

*(Deployment instructions pending `distrobuild` implementation — see PLAN.md Phase 1.)*

## Target: v0.1 MVP

- Go Orchestrator with KDL configuration and BubbleTea matrix TUI.
- `distrobuild` Nix Flake script for the base NixOS LXD container.
- nspawn agent workspace provisioning from a template subvolume.
- Initial Gateway binary with Claude Code subprocess integration.
- Initial capability set, including `Chrome_Tab_GetWebContent` with Orchestrator-managed domain whitelisting.
