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

    subgraph HOST[Host OS]
        CHROME[Chrome
        browser process]
        AUDIT[SQLite WAL
        MUS audit log]

        subgraph LXD[NixOS LXD Layer]
            VIV[viv
            TUI / CLI]
            SOCK[keeper.sock
            Unix socket]
            KEEPER[keeperd
            policy authority
            routing + audit]

            subgraph NSPAWN[systemd-nspawn Agent]
                WARD[ward
                syntax / protocol adapter]
                LLM[LLM subprocess]
                FS[Agent filesystem
                Btrfs subvolume]
                NET[LLM API egress
                firewalled veth]
            end
        end
    end

    OP --> VIV
    VIV -->|MUS frames| SOCK
    SOCK --> KEEPER
    KEEPER -->|MUS over stdio| WARD
    WARD -->|spawn / stdio| LLM
    WARD --> FS
    LLM --> NET
    KEEPER -->|CDP proxy| CHROME
    KEEPER -->|audit records| AUDIT
    WARD -->|completion / failure events| KEEPER
    WARD -->|capability requests| KEEPER
```

## Engineering Notes

- `keeperd` is the semantic and policy authority.
- `ward` is the per-agent syntax/protocol adapter and subprocess manager.
- `viv` talks to `keeperd` over `keeper.sock` using the same MUS frame model as Ward traffic.
- Browser access is host-side and mediated by `keeperd`; the agent never talks to Chrome directly.
- The agent boundary is the combination of the nspawn container, its filesystem, its MUS pipe, and its restricted LLM API egress.
