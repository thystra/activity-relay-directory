// Package storage defines persistence contracts shared by directory backends.
package storage

import (
	"context"
	"errors"
	"time"

	v1 "github.com/thystra/activity-relay-directory/internal/protocol/v1"
)

var (
	// ErrRepositoryConfiguration identifies a missing or unusable repository
	// dependency.
	ErrRepositoryConfiguration = errors.New("relay repository configuration is invalid")

	// ErrTransitionInput identifies a noncanonical or oversized relay identity.
	ErrTransitionInput = errors.New("relay transition input is invalid")

	// ErrTransitionTime identifies a negative or actor-relative regressing
	// server acceptance time.
	ErrTransitionTime = errors.New("relay transition time is invalid")

	// ErrRelayAbsent means a heartbeat cannot be recorded because the relay has
	// no active registration.
	ErrRelayAbsent = errors.New("relay is not registered")

	// ErrRelaySuspended means moderation blocks register or heartbeat without
	// altering the suspension record.
	ErrRelaySuspended = errors.New("relay is administratively suspended")

	// ErrStorageFailure identifies a database failure. Callers must never expose
	// wrapped backend details to public clients.
	ErrStorageFailure = errors.New("relay storage operation failed")
)

// RegisterIntent is the canonical state input produced after authentication,
// actor binding, and network-target validation have succeeded.
type RegisterIntent struct {
	RelayActor    string
	PublicBaseURL string
}

// IdentityIntent identifies one canonical relay for heartbeat or unregister.
type IdentityIntent struct {
	RelayActor string
}

// RelayRepository applies lifecycle transitions at a server-owned acceptance
// time. Implementations must atomically commit the state and its audit event.
type RelayRepository interface {
	Register(
		context.Context,
		RegisterIntent,
		time.Time,
	) (v1.Outcome, error)
	Heartbeat(
		context.Context,
		IdentityIntent,
		time.Time,
	) (v1.Outcome, error)
	Unregister(
		context.Context,
		IdentityIntent,
		time.Time,
	) (v1.Outcome, error)
}
