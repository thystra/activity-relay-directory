// Package v3 defines the version 3 directory lifecycle contract. Version 3
// keeps lifecycle outcomes and errors compatible with version 1 while adding a
// complete descriptive relay profile to register requests.
package v3

import (
	v1 "github.com/thystra/activity-relay-directory/internal/protocol/v1"
	"github.com/thystra/activity-relay-directory/internal/storage"
)

const Version = 3

const RFC9421SignatureTag = "activity-relay-directory-v3"

type Operation = v1.Operation

const (
	OperationRegister   = v1.OperationRegister
	OperationHeartbeat  = v1.OperationHeartbeat
	OperationUnregister = v1.OperationUnregister
)

type Outcome = v1.Outcome

const (
	OutcomeCreated   = v1.OutcomeCreated
	OutcomeUpdated   = v1.OutcomeUpdated
	OutcomeUnchanged = v1.OutcomeUnchanged
	OutcomeRecorded  = v1.OutcomeRecorded
	OutcomeRemoved   = v1.OutcomeRemoved
	OutcomeAbsent    = v1.OutcomeAbsent
)

type ErrorCode = v1.ErrorCode

const (
	ErrorInvalidRequest             = v1.ErrorInvalidRequest
	ErrorUnsupportedProtocolVersion = v1.ErrorUnsupportedProtocolVersion
	ErrorAuthenticationFailed       = v1.ErrorAuthenticationFailed
	ErrorReplayDetected             = v1.ErrorReplayDetected
	ErrorLifecycleUnavailable       = v1.ErrorLifecycleUnavailable
	ErrorEnrollmentClosed           = v1.ErrorEnrollmentClosed
	ErrorRelayNotRegistered         = v1.ErrorRelayNotRegistered
	ErrorRelaySuspended             = v1.ErrorRelaySuspended
	ErrorRateLimited                = v1.ErrorRateLimited
	ErrorInternal                   = v1.ErrorInternal
)

// RegisterRequest is a strictly decoded version 3 registration. Profile is the
// complete normalized relay self-report; omitted fields are not permitted on
// the wire, and empty values explicitly clear that relay-owned assertion.
type RegisterRequest struct {
	ProtocolVersion int
	Operation       Operation
	RelayActor      string
	PublicBaseURL   string
	Profile         storage.RelayProfile
	Telemetry       *storage.ParticipatingTelemetryIntent
}

// HeartbeatRequest may carry bounded self-reported operational telemetry.
type HeartbeatRequest struct {
	ProtocolVersion int
	Operation       Operation
	RelayActor      string
	Telemetry       *storage.ParticipatingTelemetryIntent
}

// IdentityRequest is used by version 3 unregister operations.
type IdentityRequest struct {
	ProtocolVersion int       `json:"protocol_version"`
	Operation       Operation `json:"operation"`
	RelayActor      string    `json:"relay_actor"`
}

// OperationResponse reports the state-based result of a version 3 lifecycle
// operation.
type OperationResponse struct {
	ProtocolVersion int       `json:"protocol_version"`
	Operation       Operation `json:"operation"`
	Outcome         Outcome   `json:"outcome"`
	RelayActor      string    `json:"relay_actor"`
}

// ErrorResponse is the version 3 error envelope.
type ErrorResponse struct {
	ProtocolVersion int           `json:"protocol_version"`
	Error           ErrorDocument `json:"error"`
}

type ErrorDocument struct {
	Code    ErrorCode `json:"code"`
	Message string    `json:"message"`
}
