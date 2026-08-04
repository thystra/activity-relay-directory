// Package v1 defines the version 1 directory protocol vocabulary and JSON
// message shapes. It does not implement transport, authentication, or storage.
package v1

// Version is the protocol version carried by every version 1 message.
const Version = 1

// Operation identifies a signed directory lifecycle operation.
type Operation string

const (
	OperationRegister   Operation = "register"
	OperationHeartbeat  Operation = "heartbeat"
	OperationUnregister Operation = "unregister"
)

// Valid reports whether the operation is part of the version 1 vocabulary.
func (operation Operation) Valid() bool {
	switch operation {
	case OperationRegister, OperationHeartbeat, OperationUnregister:
		return true
	default:
		return false
	}
}

// HealthState classifies a relay by the recency of its last valid heartbeat.
type HealthState string

const (
	HealthHealthy HealthState = "healthy"
	HealthStale   HealthState = "stale"
	HealthDead    HealthState = "dead"
	HealthPrune   HealthState = "prune"
)

// Valid reports whether the health state is part of the version 1 vocabulary.
func (state HealthState) Valid() bool {
	switch state {
	case HealthHealthy, HealthStale, HealthDead, HealthPrune:
		return true
	default:
		return false
	}
}

// AdministrativeState records whether moderation permits an entry to be
// listed. Suspension overrides any automatic health classification.
type AdministrativeState string

const (
	AdministrativeActive    AdministrativeState = "active"
	AdministrativeSuspended AdministrativeState = "suspended"
)

// Valid reports whether the administrative state is part of version 1.
func (state AdministrativeState) Valid() bool {
	switch state {
	case AdministrativeActive, AdministrativeSuspended:
		return true
	default:
		return false
	}
}

// Outcome is the state-based result of an idempotent lifecycle operation.
type Outcome string

const (
	OutcomeCreated   Outcome = "created"
	OutcomeUpdated   Outcome = "updated"
	OutcomeUnchanged Outcome = "unchanged"
	OutcomeRecorded  Outcome = "recorded"
	OutcomeRemoved   Outcome = "removed"
	OutcomeAbsent    Outcome = "absent"
)

// ValidFor reports whether an outcome is valid for an operation.
func (outcome Outcome) ValidFor(operation Operation) bool {
	switch operation {
	case OperationRegister:
		return outcome == OutcomeCreated ||
			outcome == OutcomeUpdated ||
			outcome == OutcomeUnchanged
	case OperationHeartbeat:
		return outcome == OutcomeRecorded
	case OperationUnregister:
		return outcome == OutcomeRemoved || outcome == OutcomeAbsent
	default:
		return false
	}
}

// ErrorCode is a stable machine-readable protocol error.
type ErrorCode string

const (
	ErrorInvalidRequest             ErrorCode = "invalid_request"
	ErrorUnsupportedProtocolVersion ErrorCode = "unsupported_protocol_version"
	ErrorAuthenticationFailed       ErrorCode = "authentication_failed"
	ErrorReplayDetected             ErrorCode = "replay_detected"
	ErrorRegistrationUnavailable    ErrorCode = "registration_unavailable"
	ErrorRelaySuspended             ErrorCode = "relay_suspended"
	ErrorRateLimited                ErrorCode = "rate_limited"
	ErrorInternal                   ErrorCode = "internal_error"
)

// Valid reports whether the error code is part of version 1.
func (code ErrorCode) Valid() bool {
	switch code {
	case ErrorInvalidRequest,
		ErrorUnsupportedProtocolVersion,
		ErrorAuthenticationFailed,
		ErrorReplayDetected,
		ErrorRegistrationUnavailable,
		ErrorRelaySuspended,
		ErrorRateLimited,
		ErrorInternal:
		return true
	default:
		return false
	}
}

// RegisterRequest is the JSON body for a register operation. Production code
// must decode it through DecodeRegisterRequest so all version 1 constraints
// are enforced.
type RegisterRequest struct {
	ProtocolVersion int       `json:"protocol_version"`
	Operation       Operation `json:"operation"`
	RelayActor      string    `json:"relay_actor"`
	PublicBaseURL   string    `json:"public_base_url"`
}

// IdentityRequest is the JSON body shared by heartbeat and unregister. Use the
// operation-specific decoder so the signed target and operation stay distinct.
type IdentityRequest struct {
	ProtocolVersion int       `json:"protocol_version"`
	Operation       Operation `json:"operation"`
	RelayActor      string    `json:"relay_actor"`
}

// OperationResponse reports the state-based outcome of a lifecycle operation.
type OperationResponse struct {
	ProtocolVersion int       `json:"protocol_version"`
	Operation       Operation `json:"operation"`
	Outcome         Outcome   `json:"outcome"`
	RelayActor      string    `json:"relay_actor"`
}

// ErrorResponse is the common JSON error envelope.
type ErrorResponse struct {
	ProtocolVersion int           `json:"protocol_version"`
	Error           ErrorDocument `json:"error"`
}

// ErrorDocument contains a stable code and a bounded human-readable message.
// Clients must branch on Code rather than Message.
type ErrorDocument struct {
	Code    ErrorCode `json:"code"`
	Message string    `json:"message"`
}
