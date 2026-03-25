# VIVARY MUS Protocol

This document describes the MUS-framed protocol currently implemented between:

- `vivary` and `keeperd` over `keeper.sock`
- `keeperd` and `ward` over stdio pipes

It also notes where the current implementation matches `docs/DESIGN.md` and where it diverges.

## Bottom Line

The implementation matches the design in the broad structure:

- one MUS-framed control protocol is used for both ctl and agent traffic
- `vivary` speaks to `keeperd` as `FromID: "ctl"`
- `ward` speaks to `keeperd` over MUS on stdio
- `keeperd` stamps agent identity on inbound Ward frames
- capability requests, responses, and telemetry all flow over the same framed transport

The main gaps are:

- the implemented `SwarmHeader` is smaller than the design header
- ctl event and approval message families in `docs/DESIGN.md` are not implemented yet
- capability args/data still carry JSON in a few places even though the frame/header and most top-level payload structs are now MUS
- ctl subscription pushes raw `CompletionEvent` and `FailureEvent` frames, not a separate `MsgType_CtlEvent`

## Implemented Wire Format

The on-wire frame is implemented in `internal/switchboard/codec.go`.

### Header

The current header is:

```go
type SwarmHeader struct {
    Version    uint8
    Type       MsgType
    FromID     string
    ToID       string
    SeqNo      uint64
    PayloadLen uint32
}
```

Encoding rules:

- `Version` and `Type` are fixed 1-byte fields
- `FromID` and `ToID` are `varuint length + UTF-8 bytes`
- `SeqNo` is unsigned LEB128
- `PayloadLen` is unsigned LEB128 on the wire, stored as `uint32` after decode
- the payload is read as exactly `PayloadLen` bytes after the header

Important limits:

- `MaxIDLen = 256`
- `MaxPayloadBytes = 4 MiB`

### What This Means Practically

The frame is not an outer `length + blob` envelope. It is a self-delimiting MUS-style header followed by a fixed-length payload. So the design's phrase "length-prefixed MUS frame" is close in spirit, but not exact in implementation.

## Implemented Message Types

From `internal/switchboard/msgtype.go`:

### Ctl and control-plane

- `MsgType_CtlSubscribe = 0x01`
- `MsgType_CtlStatus = 0x02`
- `MsgType_CtlAgentCreate = 0x03`
- `MsgType_CtlAgentDestroy = 0x04`
- `MsgType_CtlAgentList = 0x05`
- `MsgType_CtlPrompt = 0x06`
- `MsgType_CtlApproval = 0x07`

### Capability path

- `MsgType_CapabilityRequest = 0x10`
- `MsgType_CapabilityResponse = 0x11`

### Telemetry

- `MsgType_CompletionEvent = 0x20`
- `MsgType_FailureEvent = 0x21`

### Liveness

- `MsgType_Ping = 0xFE`
- `MsgType_Pong = 0xFF`

## Payload Encoding by Path

The MUS header is binary, and the current implementation now uses MUS for the top-level payload structs on the control plane, capability path, and telemetry path. JSON still appears mainly inside capability args/results where the concrete capability implementations still consume or return JSON blobs.

### `vivary` <-> `keeperd`

Ctl payloads are binary MUS structs from `internal/ctl/protocol.go`.

Implemented request payloads:

- `CtlSubscribe`: empty payload
- `CtlStatus`: empty payload
- `CtlAgentCreate`: MUS `AgentCreatePayload`
- `CtlAgentDestroy`: MUS `AgentDestroyPayload`
- `CtlAgentList`: empty payload
- `CtlPrompt`: MUS `PromptPayload`
- `Ping`: empty payload

Implemented response payloads:

- `CtlSubscribe`: 1-byte MUS ack payload (`1 = ok`)
- `CtlStatus`: MUS `StatusPayload`
- `CtlAgentCreate`: 1-byte MUS ack payload (`1 = ok`, `0 = error`) with optional error text on failure paths where implemented
- `CtlAgentDestroy`: same ack/error convention
- `CtlAgentList`: MUS `AgentListPayload`
- `CtlPrompt`: 1-byte MUS ack payload (`1 = ok`, `0 = error`) with optional error text on failure paths where implemented
- `Pong`: empty payload

Pushed event payloads to subscribed ctl clients:

- `CompletionEvent`: MUS `audit.CompletionEvent` / `ctl.CompletionEventPayload`
- `FailureEvent`: MUS `audit.FailureEvent` / `ctl.FailureEventPayload`

### `keeperd` <-> `ward`

Prompt path:

- `keeperd -> ward`: `MsgType_CtlPrompt` carrying the same MUS `ctl.PromptPayload`

Capability path:

- `ward -> keeperd`: `MsgType_CapabilityRequest` carrying MUS `capabilities.CapabilityRequestPayload`
  - `Capability`: MUS string
  - `Args`: length-prefixed bytes; today these bytes are still typically JSON for the concrete capability args object

- `keeperd -> ward`: `MsgType_CapabilityResponse` carrying MUS `capabilities.CapabilityResponsePayload`
  - `OK`: 1 byte
  - `Data`: length-prefixed bytes; today these bytes are still typically JSON for capability-specific result data
  - `ErrorCode`: MUS string
  - `ErrorDetail`: MUS string

Telemetry path:

- `ward -> keeperd`: `MsgType_CompletionEvent` with MUS `audit.CompletionEvent`
- `ward -> keeperd`: `MsgType_FailureEvent` with MUS `audit.FailureEvent`

Liveness path:

- `Ping` / `Pong` with empty payloads

## Implemented Flows

### 1. TUI / CLI startup

Current `vivary` behavior:

1. connect to `keeper.sock`
2. send `MsgType_CtlSubscribe`
3. send `MsgType_CtlStatus`
4. wait for subscribe ack, status response, and then live event pushes

Important detail: `keeperd` does not automatically send status after subscribe. The TUI explicitly requests status in a second frame.

### 2. Prompt dispatch

Current flow:

1. `vivary` sends `MsgType_CtlPrompt` with `ctl.PromptPayload`
2. `keeperd` validates agent existence and active pipe
3. `keeperd` forwards the same payload to Ward as `MsgType_CtlPrompt`
4. Ward spawns the LLM subprocess and handles tool calls

### 3. Capability round-trip

Current flow:

1. LLM emits a tool-use event
2. Ward parses it and validates local structure
3. Ward sends `MsgType_CapabilityRequest`
4. `keeperd` dispatches through the capability dispatcher and ACL layer
5. `keeperd` replies with `MsgType_CapabilityResponse`
6. Ward matches the response by echoed `SeqNo`

Correlation rule:

- capability responses reuse the request `SeqNo`

### 4. Live event push to TUI

Current flow:

1. Ward emits `CompletionEvent` or `FailureEvent`
2. `keeperd` audits it and fans it out to subscribed ctl clients
3. ctl subscribers receive frames of type `MsgType_CompletionEvent` or `MsgType_FailureEvent`
4. `vivary` decodes those directly and updates the UI

## Enforcement Actually Implemented

### On Ward pipes

Implemented in `internal/switchboard/router.go`:

- byte-rate limiting before full MUS decode
- identity overwrite to the registered pipe agent ID
- monotonic `SeqNo` enforcement per pipe
- frame drop on seq rewind
- `pipe_flood` security event emission

### On ctl socket

Implemented in `cmd/keeperd/main.go`:

- `FromID` must equal `"ctl"`
- ctl frames are audited
- ctl connections can subscribe for pushes

Notably, ctl traffic does not currently go through the same router machinery as Ward pipes. It uses the same frame codec, but a separate socket handler path.

## Match Against `docs/DESIGN.md`

### Matches

- `vivary` and `keeperd` communicate over a Unix socket using the same MUS frame format as agent pipes
- `vivary` uses `FromID: "ctl"`
- `keeperd` is the central routing and policy point
- Ward uses stdio MUS as its only IPC channel to `keeperd`
- Ward emits completion and failure telemetry
- capability requests and responses are correlated by `SeqNo`
- `keeperd` overwrites Ward-originated `FromID`
- byte-rate limiting and `SeqNo` monotonicity exist on Ward pipes

### Divergences

#### 1. Header shape is smaller than designed

`docs/DESIGN.md` describes:

- `ParentID`
- `Depth`
- a `MsgType` field name in the schema block

The implementation only has:

- `Version`
- `Type`
- `FromID`
- `ToID`
- `SeqNo`
- `PayloadLen`

So multi-agent topology metadata is not on the wire yet.

#### 2. No `MsgType_CtlEvent`

The design describes a dedicated `MsgType_CtlEvent` wrapper for pushed ctl events.

The implementation instead pushes raw:

- `MsgType_CompletionEvent`
- `MsgType_FailureEvent`

directly to subscribed ctl clients.

#### 3. Approval protocol is not implemented yet

The design describes:

- `MsgType_CtlApprovalRequired`
- `MsgType_CtlApprovalGrant`
- `MsgType_CtlApprovalDeny`

The implementation has only one reserved enum value:

- `MsgType_CtlApproval`

and it is not wired through `keeperd` or the TUI.

#### 4. ctl command set differs from the design table

Design names include:

- `MsgType_CtlAgentStop`
- `MsgType_CtlVaultAdd`

Implementation currently has:

- `MsgType_CtlAgentDestroy`
- no vault messages yet

#### 5. ctl startup flow differs slightly

The design says subscribe, then receive initial status followed by pushes.

The implementation requires:

- subscribe
- explicit status request

That is close, but not the same handshake.

#### 6. Capability args/results still bridge through JSON blobs in places

The design sometimes reads as if typed MUS payload structs exist end-to-end.

The implementation today is:

- MUS for framing/header
- MUS for ctl payload structs
- MUS for capability request/response envelopes
- MUS for completion/failure payloads
- JSON still inside some capability `Args` / `Data` byte blobs while concrete capabilities continue to use JSON objects internally

This is fully workable, but the remaining JSON-at-the-edges should be described explicitly.

#### 7. ctl traffic is codec-compatible with agent traffic, but not routed by the same router path

The design says the ctl socket is handled by the same router path.

In implementation:

- Ward pipes use `internal/switchboard.Router`
- ctl uses its own `handleCtlConn` loop

The frame codec is shared, but the routing path is not yet unified.

## Recommended Design Doc Updates

If `docs/DESIGN.md` is meant to describe current reality rather than target architecture, these are the main updates to make:

- say explicitly that current MUS usage is `binary header + JSON payload`
- say explicitly that current MUS usage is `binary header + MUS payload structs`, with JSON still embedded in some capability args/result byte blobs
- replace `MsgType_CtlEvent` with direct pushed `CompletionEvent` and `FailureEvent`, or mark `CtlEvent` as planned
- mark approval message families as planned, not implemented
- note that `ctl` uses the same frame format but not yet the same router implementation
- note that `ParentID` and `Depth` are future multi-agent header fields, not current wire fields

## Current Protocol Reference

### Header field meanings

- `Version`: protocol version, currently `0`
- `Type`: message discriminator
- `FromID`: sender identity
- `ToID`: logical recipient identity
- `SeqNo`: per-sender sequence number
- `PayloadLen`: payload byte length

### Identity conventions

- `ctl` is reserved for `vivary`
- `keeper` is used by `keeperd`
- agent IDs are used by Ward pipes

### SeqNo conventions

- `vivary`, `keeperd`, and `ward` each maintain local monotonic counters for sent frames
- Ward capability responses are matched by echoing the original request `SeqNo`
- Ward pipe inbound frames are rejected on non-increasing `SeqNo`

### Audit behavior

- ctl frames are written to the audit DB by the ctl handler
- Ward frames are written by the switchboard/dispatch path
- completion and failure events are always stored

### Remaining JSON bridges

- `CapabilityRequestPayload.Args` is MUS length-prefixed bytes, but those bytes are still decoded as JSON by `keeperd` before capability dispatch
- `CapabilityResponsePayload.Data` is MUS length-prefixed bytes, but many capabilities still return JSON result blobs inside it
- capability implementations such as `Browser_Page_Read` and `Filesystem_File_Write` still consume JSON args structs internally

## Conclusion

The current implementation is directionally aligned with `docs/DESIGN.md`, especially around the single framed control plane, identity handling, capability routing, and telemetry flow.

But it is still a narrower MVP protocol than the design document describes. The biggest differences are the reduced header, the remaining JSON bridges inside capability args/result byte blobs, direct event pushes instead of a ctl event wrapper, and the absence of the approval and unified ctl-router layers.
