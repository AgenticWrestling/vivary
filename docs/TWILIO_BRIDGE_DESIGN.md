# VIVARY Twilio SMS Bridge Design

This document is a roadmap design example for a post-MVP bridge, not a description of a currently implemented subsystem.

## Overview

The Twilio SMS Bridge provides bidirectional SMS communication for VIVARY agents. It handles incoming SMS messages (ingress) by triggering agent prompts and exposes SMS sending/listing capabilities (egress) for agents to use as tools.

## Architecture

- **Language:** Go
- **External Dependencies:** Twilio Go SDK, `github.com/gin-gonic/gin` (for webhook server)
- **VIVARY Connection:** Unix Domain Socket (`keeper.sock`) using MUS protocol

## Workflow

### 1. Ingress (SMS -> VIVARY)

1. **Twilio Webhook:** Twilio sends an HTTP POST request to the Bridge's configured webhook URL.
2. **Payload Parsing:** The Bridge validates the Twilio signature and parses the `From`, `To`, and `Body`.
3. **Identity Mapping:** The Bridge looks up which agent should receive the message (e.g., based on the `To` number or a default mapping).
4. **MUS Prompt:** The Bridge would send a prompt/control message to `keeperd`:
   - `FromID`: `bridge:twilio-sms-01`
   - `ToID`: `assistant-01`
   - `Payload`: The SMS body and metadata.

### 2. Capability (VIVARY -> SMS)

The Bridge exposes the `Messaging_SMS_Send` capability. When an agent calls this tool:

1. **Capability Request:** `keeperd` routes the `MsgType_CapabilityRequest` to the Twilio Bridge.
2. **Twilio API Call:** The Bridge uses its configured `AccountSID` and `AuthToken` to call the Twilio REST API.
3. **Response:** The Bridge returns the Twilio SID and status as a `MsgType_CapabilityResponse`.

## Proposed Capability Definition (`capabilities/messaging_sms.kdl`)

```kdl
capability Messaging_SMS_Send {
    category Messaging
    writes entity=SMSMessage
    description "Send an SMS message to a specific phone number."

    argument to type=string description="Recipient phone number (E.164)"
    argument body type=string description="The message content"

    returns sid type=string description="Twilio message SID"
    returns status type=string description="Initial status (e.g., 'queued')"
}

capability Messaging_SMS_List {
    category Messaging
    reads entity=SMSMessage
    description List recent SMS messages received or sent.

    argument limit type=int default=20

    returns messages type=array {
        item type=object {
            field from type=string
            field body type=string
            field timestamp type=string
        }
    }
}
```

## Bridge Implementation Details

### Credentials

The Bridge requires `TWILIO_ACCOUNT_SID` and `TWILIO_AUTH_TOKEN`. In a future production VIVARY setup, these could be stored in the VIVARY credential/vault layer or passed to the Bridge process securely.

### Persistence

The Bridge should ideally maintain a small local cache (e.g., SQLite) of message history to support `Messaging_SMS_List` without constant API calls to Twilio, or it can proxy the list request directly.

### Health Checks

The Bridge should periodically send `MsgType_Ping` to `keeperd` and reconnect if the socket is closed.

## Deployment

The Twilio Bridge runs as a background service on the host OS.

```bash
# Example start command
./twilio-bridge --socket ./keeper.sock --config ./twilio.kdl
```

## Security

- **Webhook Validation:** Must use Twilio's request validator to prevent spoofing.
- **ACL Enforcement:** `keeperd` ensures the Bridge only prompts agents it is permitted to address.
- **Capability Isolation:** Only agents with `allow "Messaging_SMS_Send"` in their `agent.kdl` can trigger the Bridge's Twilio API calls.
