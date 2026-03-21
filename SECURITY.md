# VIVARY Security Model

This document describes the threat model, isolation boundaries, and known security tradeoffs in VIVARY. It is intended for operators evaluating the system and contributors working on security-sensitive components.

---

## Isolation Boundaries

VIVARY employs three nested containment layers:

```
┌─────────────────────────────────────────────────────┐
│  Bare Host OS                                       │
│  ┌───────────────────────────────────────────────┐  │
│  │  LXD/LXC Container (NixOS)                   │  │
│  │  keeperd · vivary · Chrome                   │  │
│  │  ┌─────────────────┐  ┌─────────────────┐    │  │
│  │  │ nspawn Agent A  │  │ nspawn Agent B  │    │  │
│  │  │  Ward · LLM     │  │  Ward · LLM     │    │  │
│  │  └─────────────────┘  └─────────────────┘    │  │
│  └───────────────────────────────────────────────┘  │
└─────────────────────────────────────────────────────┘
```

**Layer 1 — nspawn with User Namespacing (`-U`):** The agent's primary security boundary. Isolates process tree, IPC namespace, hostname, and UID/GID space. An agent escaping this boundary reaches the NixOS LXD container — not the bare host.

**Layer 2 — LXD/LXC container:** A transport and distribution layer, not a security boundary. Provides cross-platform portability and a reproducible NixOS base image. `security.nesting=true` is required for nspawn to run inside it, which weakens this layer (see below).

**Layer 3 — nftables per-agent veth firewall:** Network-layer isolation. Each agent container has a dedicated virtual ethernet pair; nftables rules on the host-side veth drop all outbound traffic except to the single configured LLM API endpoint.

---

## Threat Model

### What VIVARY protects against

- **Credential theft:** API keys and OAuth tokens never enter the nspawn jail. Agents reference a `credential_id`; `keeperd` resolves to the actual secret only at gateway invocation time, after all ACL checks pass.
- **Agent impersonation:** `keeperd` overwrites `FromID` on all inbound MUS frames from the pipe's registered agent identity. An agent cannot forge messages that appear to come from another agent or from the operator (`"ctl"`).
- **Capability escalation:** An agent may only invoke capabilities in its explicit `allow` list. There is no wildcard grant. `keeperd` enforces this before dispatching to any gateway binary.
- **Cross-agent data leakage:** Each agent has a separate Btrfs subvolume. The nspawn `-U` flag gives each container a disjoint UID range on the host, preventing one agent from reading another's files via UID overlap.
- **Unbounded fan-out / recursive spawning:** The `can-invoke`, `max-concurrent`, `max-depth`, and `max-agents` topology constraints in `orchestrator.kdl` are enforced at routing time. An agent cannot spawn or message agents outside its declared topology.
- **Network exfiltration:** The per-agent nftables ruleset drops all outbound traffic except to the configured LLM API endpoint. An agent cannot open sockets to arbitrary hosts, even if the LLM subprocess attempts to do so.

### What VIVARY does NOT protect against

- **Kernel exploits from within nspawn:** If an agent exploits a Linux kernel vulnerability that allows namespace escape, it reaches the NixOS LXD container. The LXD boundary with `security.nesting=true` is weakened (see Nesting section below) — a kernel exploit inside nspawn may reach the bare host.
- **Compromised LLM provider:** The LLM subprocess communicates with the external LLM API. VIVARY cannot inspect or modify this traffic (it is TLS). A compromised or manipulated LLM response could instruct the Ward to invoke permitted capabilities in harmful ways within their policy constraints.
- **Operator mistakes in `agent.kdl`:** VIVARY enforces what is declared. An operator granting `Email_Message_Send` without domain constraints enables the agent to email any recipient.

---

## Nesting Attack Surface

`security.nesting=true` on the LXD container grants the NixOS guest access to additional kernel namespace APIs needed to run `systemd-nspawn`. This meaningfully expands the kernel attack surface compared to a flat nspawn deployment.

**Threat:** A sufficiently advanced kernel namespace or syscall exploit executed from within an agent nspawn jail could potentially escape to the bare host rather than stopping at the LXD boundary.

**Current mitigations:**
- nspawn `-U` (User Namespacing) maps all UIDs inside the agent container to an unprivileged range on the host — even within the LXD container, the agent processes are unprivileged.
- The `distrobuild` NixOS image is minimal (no unnecessary packages, no unnecessary daemons).
- Per-agent cgroup v2 limits constrain the CPU and memory available for exploit execution.

**Planned mitigations (post-MVP):**
- A hardened LXD profile with an explicit AppArmor policy limiting which kernel subsystems the nspawn container can access.
- A seccomp filter attached to the LXD container, blocking syscall families not required by `keeperd` or nspawn.

**Risk acceptance:** For the MVP, `security.nesting=true` without a hardened profile is accepted. Operators running VIVARY on shared infrastructure or with untrusted agent workloads should be aware of this tradeoff and wait for the hardened profile.

---

## UID Namespace Allocation

Each nspawn container uses a non-overlapping 65,536-entry UID range, allocated from `keeperd`'s host-level `/etc/subuid` and `/etc/subgid` entries.

`keeperd` maintains an in-memory allocation table of assigned ranges. On agent provision, it selects the next unallocated range, writes the nspawn `--private-users=<start>:65536` argument, and records the assignment in the SQLite WAL. On agent teardown, the range is returned to the free pool.

This ensures:
- Two concurrent agent containers cannot share UID numbers on the host namespace.
- A file owned by UID 1000 inside Agent A maps to a different host UID than UID 1000 inside Agent B.
- Cross-agent file access via Btrfs subvolume mounting is blocked at the kernel UID check level, in addition to the filesystem path isolation.

---

## Pipe Flooding

An agent (or a compromised Ward binary) could attempt to saturate `keeperd`'s routing loop by emitting high-volume garbage bytes on its stdout pipe.

**Mitigations:**
- Before MUS decoding, each agent's pipe reader applies a configurable byte-rate limit (default: configurable in `orchestrator.kdl`). Bytes exceeding the rate budget are discarded and a `pipe_flood` security event is logged.
- The nspawn cgroup CPU limit constrains the rate at which an agent can produce output, independently of the application-layer rate limit.
- A `pipe_flood` event triggers an operator notification via `vivary`. Repeated flooding events can be configured to automatically halt the offending agent.

---

## PII and Audit Log Sensitivity

Every MUS frame passing through the Switchboard is recorded in the SQLite WAL audit trail. This creates a risk that sensitive data processed by agents (email bodies, document contents, database query results) is persisted in a central log.

**Default policy:** Capabilities in high-sensitivity categories (`Email`, `Messaging`, `Document`, `Database`) default to `audit-payload false`. `keeperd` logs the frame header (timestamp, agent ID, capability name, entity type, outcome) but not the payload blob.

**Operator override:** Full payload logging can be enabled per-capability or per-agent in `agent.kdl`:
```kdl
allow "Email_Message_Read" {
    audit-payload true   // override default; logs full payload blob
    ...
}
```

**Planned:** SQLCipher encryption at rest for the SQLite WAL. Until implemented, the WAL file should be protected by host-OS filesystem permissions (readable only by the `keeperd` process user).

---

## Credential Vault

`keeperd` stores all credentials (API keys, OAuth tokens, service account JSON) in an AES-256-GCM encrypted file on disk. The encryption key is derived from a passphrase provided at daemon start (or from a host keyring integration).

- Credentials are decrypted in-process only at the moment of gateway invocation, after all ACL checks have passed.
- The decrypted value is passed to the REST gateway binary via an environment variable on its process — it never touches the agent's nspawn filesystem or the MUS message stream.
- `vivary vault add|rotate` sends credentials over the local Unix socket (not the network). The ctl socket is accessible only to processes on the same host with filesystem access to the socket path.

**Not implemented in MVP:** Key rotation automation, audit log of vault access events, hardware security module (HSM) integration.
