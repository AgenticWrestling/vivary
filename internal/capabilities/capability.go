// Package capabilities defines the Capability interface and the registry that
// keeperd uses to dispatch incoming CapabilityRequest frames.
//
// Each capability is a named, versioned unit of external access.  Names follow
// the Namespace_Noun_Verb convention (e.g. Browser_Page_Read).
//
// Capabilities are registered at startup; keeperd dispatches requests to the
// registered implementation after passing ACL, scope, and approval checks.
package capabilities

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"
)

// Request carries a decoded capability invocation.
type Request struct {
	// Name is the fully-qualified capability name, e.g. "Browser_Page_Read".
	Name string

	// AgentID is the requesting agent (already identity-stamped by the router).
	AgentID string

	// SeqNo is the frame SeqNo of the originating CapabilityRequest frame.
	SeqNo uint64

	// Args is the raw JSON arguments object from the agent's tool invocation.
	Args json.RawMessage
}

// Response is the result returned to the Ward as a CapabilityResponse payload.
type Response struct {
	// OK indicates whether the capability succeeded.
	OK bool `json:"ok"`

	// Data is the capability-specific result (text, JSON, etc).
	Data json.RawMessage `json:"data,omitempty"`

	// ErrorCode is set on failure.
	ErrorCode string `json:"error_code,omitempty"`

	// ErrorDetail is a human-readable failure description.
	ErrorDetail string `json:"error_detail,omitempty"`
}

// DeniedResponse returns a Response for a capability_denied outcome.
func DeniedResponse(detail string) Response {
	return Response{OK: false, ErrorCode: "capability_denied", ErrorDetail: detail}
}

// Capability is the interface every registered capability must implement.
type Capability interface {
	// Name returns the fully-qualified capability name.
	Name() string

	// Explain returns the static JSON Schema string for this capability's Args.
	// Used by the Ward to validate tool call arguments before forwarding.
	Explain() string

	// AuditPayload returns false for high-sensitivity capability categories
	// (Email, Messaging, Document, Database) where payloads must not be stored.
	AuditPayload() bool

	// Execute performs the capability action.  keeperd calls this only after
	// ACL, scope, and approval checks pass.
	Execute(ctx context.Context, req Request) (Response, error)
}

// ---- Registry --------------------------------------------------------------

// Registry maps capability names to implementations.
type Registry struct {
	mu   sync.RWMutex
	caps map[string]Capability
}

// NewRegistry creates an empty Registry.
func NewRegistry() *Registry {
	return &Registry{caps: make(map[string]Capability)}
}

// Register adds c to the registry.  Panics on duplicate name.
func (reg *Registry) Register(c Capability) {
	reg.mu.Lock()
	defer reg.mu.Unlock()
	if _, exists := reg.caps[c.Name()]; exists {
		panic("capabilities: duplicate registration: " + c.Name())
	}
	reg.caps[c.Name()] = c
}

// Lookup returns the Capability for name, or (nil, false) if not found.
func (reg *Registry) Lookup(name string) (Capability, bool) {
	reg.mu.RLock()
	c, ok := reg.caps[name]
	reg.mu.RUnlock()
	return c, ok
}

// Names returns the sorted list of registered capability names.
func (reg *Registry) Names() []string {
	reg.mu.RLock()
	defer reg.mu.RUnlock()
	out := make([]string, 0, len(reg.caps))
	for k := range reg.caps {
		out = append(out, k)
	}
	return out
}

// ConstraintSet maps scope-constraint keys (e.g. "path-prefix", "domain-suffix")
// to one or more allowed values.
type ConstraintSet map[string][]string

// ScopeConstraint defines the resource-level policy for a capability grant.
// It maps an entity name to a set of constraints on its component fields.
type ScopeConstraint struct {
	// Entity is the ECS resource type, e.g. "File", "Link", "EmailMessage".
	Entity string `json:"entity"`

	// Constraints are the typed key-value pairs from agent.kdl.
	Constraints ConstraintSet `json:"constraints"`
}

// ---- ACL -------------------------------------------------------------------

// ACLEntry is a single row in an agent's capability allow-list.
type ACLEntry struct {
	// CapabilityName is the allowed capability, e.g. "Browser_Page_Read".
	CapabilityName string

	// Scope is the legacy capability-specific scope string (deprecated).
	Scope string

	// Constraints is the ECS-style scope model.  If present, it takes
	// precedence over the Scope string.
	Constraints []ScopeConstraint
}

// ACL holds the full capability policy for one agent.
type ACL struct {
	AgentID string
	Entries []ACLEntry
}

// Allowed returns true if the agent's ACL permits name.  It returns both the
// legacy scope string and the new ECS constraints if available.
func (a *ACL) Allowed(name string) (allowed bool, scope string, constraints []ScopeConstraint) {
	for _, e := range a.Entries {
		if e.CapabilityName == name {
			return true, e.Scope, e.Constraints
		}
	}
	return false, "", nil
}

// ---- Dispatcher ------------------------------------------------------------

// Dispatcher wraps a Registry and ACL map to provide keeperd's top-level
// capability dispatch logic.
type Dispatcher struct {
	reg  *Registry
	mu   sync.RWMutex
	acls map[string]*ACL // keyed by AgentID
}

// NewDispatcher creates a Dispatcher backed by reg.
func NewDispatcher(reg *Registry) *Dispatcher {
	return &Dispatcher{reg: reg, acls: make(map[string]*ACL)}
}

// SetACL installs or replaces the ACL for an agent.
func (d *Dispatcher) SetACL(acl *ACL) {
	d.mu.Lock()
	d.acls[acl.AgentID] = acl
	d.mu.Unlock()
}

// RemoveACL removes an agent's ACL (call on agent destroy).
func (d *Dispatcher) RemoveACL(agentID string) {
	d.mu.Lock()
	delete(d.acls, agentID)
	d.mu.Unlock()
}

// Dispatch resolves the request against the ACL and executes the capability.
// Returns a capability_denied Response (not an error) for policy failures so
// the caller can record the event and return the denial to the agent.
func (d *Dispatcher) Dispatch(ctx context.Context, req Request) (Response, error) {
	// --- ACL check ---
	d.mu.RLock()
	acl := d.acls[req.AgentID]
	d.mu.RUnlock()

	if acl == nil {
		return DeniedResponse(fmt.Sprintf("no ACL registered for agent %q", req.AgentID)), nil
	}
	allowed, scope, constraints := acl.Allowed(req.Name)
	if !allowed {
		return DeniedResponse(fmt.Sprintf("capability %q not in agent ACL", req.Name)), nil
	}

	// --- Capability lookup ---
	cap, ok := d.reg.Lookup(req.Name)
	if !ok {
		return DeniedResponse(fmt.Sprintf("capability %q not registered", req.Name)), nil
	}

	// --- Inject scope into context so the capability impl can enforce it ---
	ctx = contextWithScope(ctx, scope)
	ctx = contextWithConstraints(ctx, constraints)

	return cap.Execute(ctx, req)
}

// ---- Context keys ----------------------------------------------------------

type contextKey int

const (
	scopeKey       contextKey = 1
	constraintsKey contextKey = 2
)

func contextWithScope(ctx context.Context, scope string) context.Context {
	return context.WithValue(ctx, scopeKey, scope)
}

func contextWithConstraints(ctx context.Context, c []ScopeConstraint) context.Context {
	return context.WithValue(ctx, constraintsKey, c)
}

// ScopeFromContext returns the operator-configured legacy scope for the
// executing capability.  Capability implementations should use this to
// enforce path or URL prefix constraints for backward compatibility.
func ScopeFromContext(ctx context.Context) string {
	v, _ := ctx.Value(scopeKey).(string)
	return v
}

// ConstraintsFromContext returns the ECS-style scope constraints for the
// executing capability.
func ConstraintsFromContext(ctx context.Context) []ScopeConstraint {
	v, _ := ctx.Value(constraintsKey).([]ScopeConstraint)
	return v
}
