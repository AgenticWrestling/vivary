# VIVARY System Architecture

This Mermaid diagram mirrors the architecture diagram currently shown on the website.

```mermaid
flowchart LR
    subgraph HOST[Host OS]
        subgraph LXD[NixOS LXD Layer]
            VIV[viv
            TUI / CLI over keeper.sock]
            KEEPER[keeperd
            policy, routing, audit
            identity and dispatch]

            subgraph AGENT[nspawn Agent]
                WARD[ward
                syntax + protocol adapter]
                LLM[LLM subprocess]
            end
        end

        CHROME[Chrome
        host-side browser
        isolated profile per agent
        keeperd speaks CDP]

        AUDIT[MUS Audit Log
        SQLite WAL
        decoded by vivlog
        frames, events, denials]
    end

    VIV -->|MUS over Unix socket| KEEPER
    KEEPER -->|MUS over stdio| WARD
    WARD -->|spawn / stdio| LLM
    KEEPER -->|CDP proxy| CHROME
    KEEPER -->|MUS frames and event records| AUDIT
    VIV -.->|prompt dispatch| KEEPER
```

## Notes

- Chrome and audit inspection remain outside the container boundary.
- The NixOS LXD layer is the reproducible host environment for `keeperd`, `viv`, and agent provisioning.
- Each agent runs inside `systemd-nspawn` with `ward` as its boundary adapter.
- The agent has no raw credentials and no unrestricted network egress.

## Engineering View

This version is less of a mirror of the landing page and more of a source-of-truth systems view aligned with the current design docs.

```mermaid
flowchart TD
    OP[Operator]

    CLOUD[LLM API Endpoints
    cloud providers]

    subgraph HOST[Host OS]
        DISPATCH[Dispatch Agent
        ward + Agent CLI]
        CHROME[Chrome
        browser process]
        AUDIT[SQLite WAL
        MUS audit log]

        subgraph LXD[NixOS LXD Layer]
            VIV[viv
            TUI / CLI]
            KEEPER[keeperd
            policy authority
            routing + audit]

            subgraph DISO[systemd-nspawn Agent: Designer]
                DWARD[ward]
                DCLI[Agent CLI]
                DFS[Agent filesystem]
            end

            subgraph AISO[systemd-nspawn Agent: Architect]
                AWARD[ward]
                ACLI[Agent CLI]
                AFS[Agent filesystem]
            end

            subgraph QISO[systemd-nspawn Agent: QA]
                QWARD[ward]
                QCLI[Agent CLI]
                QFS[Agent filesystem]
            end

            subgraph MISO[systemd-nspawn Agent: Marketing]
                MWARD[ward]
                MCLI[Agent CLI]
                MFS[Agent filesystem]
            end
        end
    end

    OP --> VIV
    VIV -->|MUS frames / Unix socket| KEEPER

    KEEPER -->|MUS over stdio| DISPATCH
    DISPATCH -->|spawn / stdio| DCLIHOST[Agent CLI]
    DISPATCH -->|firewalled veth| CLOUD

    KEEPER -->|MUS over stdio| DWARD
    DWARD -->|spawn / stdio| DCLI
    DWARD --> DFS
    DCLI -->|firewalled veth| CLOUD

    KEEPER -->|MUS over stdio| AWARD
    AWARD -->|spawn / stdio| ACLI
    AWARD --> AFS
    ACLI -->|firewalled veth| CLOUD

    KEEPER -->|MUS over stdio| QWARD
    QWARD -->|spawn / stdio| QCLI
    QWARD --> QFS
    QCLI -->|firewalled veth| CLOUD

    KEEPER -->|MUS over stdio| MWARD
    MWARD -->|spawn / stdio| MCLI
    MWARD --> MFS
    MCLI -->|firewalled veth| CLOUD

    KEEPER -->|CDP proxy| CHROME
    KEEPER -->|audit records| AUDIT
    DISPATCH -->|completion / failure events| KEEPER
    DISPATCH -->|capability requests| KEEPER
    DWARD -->|completion / failure events| KEEPER
    DWARD -->|capability requests| KEEPER
    AWARD -->|completion / failure events| KEEPER
    AWARD -->|capability requests| KEEPER
    QWARD -->|completion / failure events| KEEPER
    QWARD -->|capability requests| KEEPER
    MWARD -->|completion / failure events| KEEPER
    MWARD -->|capability requests| KEEPER
```

## Engineering Notes

- `keeperd` is the semantic and policy authority.
- `ward` is the per-agent syntax/protocol adapter and subprocess manager.
- `viv` talks to `keeperd` over a Unix socket using the same MUS frame model as Ward traffic.
- Browser access is host-side and mediated by `keeperd`; the agent never talks to Chrome directly.
- Most agents run inside `systemd-nspawn`, but a host-resident dispatch agent is also possible.
- Each agent connects to its own LLM API endpoint; the `firewalled veth` edge is the enforcement point for isolated agents.
