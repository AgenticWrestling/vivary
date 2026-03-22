// Package switchboard implements the MUS-framed message router at the heart of
// the VIVARY control plane.  In the MVP there is exactly one agent pipe plus
// the ctl socket; the router is written to generalise to multiple pipes in a
// later phase without structural changes.
package switchboard

// MsgType identifies the payload schema carried in a SwarmFrame.
// Values 0x00–0x0F are reserved for the core control-plane protocol.
// Values 0x10–0x1F are agent ↔ keeper capability request/response pairs.
// Values 0x20–0x2F are audit/telemetry events.
type MsgType uint8

const (
	// ---- Control-plane (ctl ↔ keeperd) ----

	// MsgType_CtlSubscribe requests a stream of push events from keeperd.
	// Payload: CtlSubscribePayload.
	MsgType_CtlSubscribe MsgType = 0x01

	// MsgType_CtlStatus requests current daemon/agent status.
	// Payload: empty (request) / CtlStatusPayload (response).
	MsgType_CtlStatus MsgType = 0x02

	// MsgType_CtlAgentCreate provisions a new agent workspace.
	// Payload: CtlAgentCreatePayload.
	MsgType_CtlAgentCreate MsgType = 0x03

	// MsgType_CtlAgentDestroy tears down an agent workspace.
	// Payload: CtlAgentDestroyPayload.
	MsgType_CtlAgentDestroy MsgType = 0x04

	// MsgType_CtlAgentList requests the list of known agents.
	// Payload: empty (request) / CtlAgentListPayload (response).
	MsgType_CtlAgentList MsgType = 0x05

	// MsgType_CtlPrompt dispatches a prompt to an agent.
	// Payload: CtlPromptPayload.
	MsgType_CtlPrompt MsgType = 0x06

	// MsgType_CtlApproval grants or denies a pending capability approval gate.
	// Payload: CtlApprovalPayload.
	MsgType_CtlApproval MsgType = 0x07

	// ---- Agent ↔ Keeper (capability protocol) ----

	// MsgType_CapabilityRequest is sent by a Ward requesting a capability action.
	// Payload: CapabilityRequestPayload.
	MsgType_CapabilityRequest MsgType = 0x10

	// MsgType_CapabilityResponse is sent by keeperd in reply to a request.
	// Payload: CapabilityResponsePayload.
	MsgType_CapabilityResponse MsgType = 0x11

	// ---- Telemetry / audit events (Ward → Keeper) ----

	// MsgType_CompletionEvent is emitted by the Ward on clean subprocess exit.
	// Payload: CompletionEventPayload.
	MsgType_CompletionEvent MsgType = 0x20

	// MsgType_FailureEvent is emitted by the Ward for any failure mode.
	// Payload: FailureEventPayload.
	MsgType_FailureEvent MsgType = 0x21

	// MsgType_Ping / MsgType_Pong for liveness checks.
	MsgType_Ping MsgType = 0xFE
	MsgType_Pong MsgType = 0xFF
)

// String returns the canonical name of a MsgType, useful for logging.
func (m MsgType) String() string {
	switch m {
	case MsgType_CtlSubscribe:
		return "CtlSubscribe"
	case MsgType_CtlStatus:
		return "CtlStatus"
	case MsgType_CtlAgentCreate:
		return "CtlAgentCreate"
	case MsgType_CtlAgentDestroy:
		return "CtlAgentDestroy"
	case MsgType_CtlAgentList:
		return "CtlAgentList"
	case MsgType_CtlPrompt:
		return "CtlPrompt"
	case MsgType_CtlApproval:
		return "CtlApproval"
	case MsgType_CapabilityRequest:
		return "CapabilityRequest"
	case MsgType_CapabilityResponse:
		return "CapabilityResponse"
	case MsgType_CompletionEvent:
		return "CompletionEvent"
	case MsgType_FailureEvent:
		return "FailureEvent"
	case MsgType_Ping:
		return "Ping"
	case MsgType_Pong:
		return "Pong"
	default:
		return "Unknown"
	}
}
