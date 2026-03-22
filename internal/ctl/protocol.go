// Package ctl defines the payload structures exchanged between the vivary
// CLI/TUI and keeperd over the ctl socket (MUS frames with FromID="ctl").
//
// All payload structs are JSON-encoded in the MUS frame PayloadLen bytes.
// JSON is used for ctl payloads (not raw MUS) because the ctl path is low-
// frequency operator traffic and human-readability aids debugging.
package ctl

import "encoding/json"

// Reserved identity for the operator control socket.
const CtlIdentity = "ctl"

// ---- Request payloads (ctl → keeperd) --------------------------------------

// SubscribePayload requests that keeperd push all future CompletionEvents and
// FailureEvents to this ctl connection.
type SubscribePayload struct{}

// AgentCreatePayload requests provisioning of a new agent workspace.
type AgentCreatePayload struct {
	// ID is the unique agent identifier.  Must match [a-z0-9][a-z0-9\-]{0,62}.
	ID string `json:"id"`

	// Template is the host path to the Btrfs subvolume template directory.
	Template string `json:"template"`

	// CPUShares is the relative CPU weight (default 1024).
	CPUShares uint32 `json:"cpu_shares,omitempty"`

	// MemoryMaxBytes is the cgroup memory.max limit (0 = unlimited).
	MemoryMaxBytes uint64 `json:"memory_max_bytes,omitempty"`
}

// AgentDestroyPayload requests teardown of an agent workspace.
type AgentDestroyPayload struct {
	ID string `json:"id"`
}

// PromptPayload dispatches a prompt to a specific agent.
type PromptPayload struct {
	// AgentID is the target agent.
	AgentID string `json:"agent_id"`

	// Seq is the operator-assigned prompt sequence number (must be monotonic).
	Seq uint64 `json:"seq"`

	// Text is the raw prompt text sent to the Ward for forwarding to the LLM.
	Text string `json:"text"`
}

// ApprovalPayload grants or denies a pending capability request that was
// held for operator approval.
type ApprovalPayload struct {
	// RequestID is the capability request's SeqNo.
	RequestID uint64 `json:"request_id"`

	// Granted is true to allow, false to deny.
	Granted bool `json:"granted"`

	// Reason is an optional operator-provided note stored in the audit log.
	Reason string `json:"reason,omitempty"`
}

// ---- Response/push payloads (keeperd → ctl) --------------------------------

// StatusPayload is the response to a CtlStatus request.
type StatusPayload struct {
	DaemonVersion string        `json:"daemon_version"`
	Agents        []AgentStatus `json:"agents"`
	UptimeSeconds int64         `json:"uptime_seconds"`
}

// AgentStatus summarises the runtime state of a single agent.
type AgentStatus struct {
	ID           string `json:"id"`
	State        string `json:"state"` // "running", "idle", "provisioning", "error"
	LastPromptSeq uint64 `json:"last_prompt_seq"`
	LastEventAt  string `json:"last_event_at"` // RFC3339
}

// AgentListPayload is the response to a CtlAgentList request.
type AgentListPayload struct {
	Agents []AgentStatus `json:"agents"`
}

// ---- Helper ----------------------------------------------------------------

// MarshalJSON marshals v to a JSON byte slice; panics on error (only structs
// in this package are valid inputs so marshalling should never fail).
func MarshalJSON(v any) []byte {
	b, err := json.Marshal(v)
	if err != nil {
		panic("ctl: json marshal: " + err.Error())
	}
	return b
}
