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

	// ErrRelayAbsent means the requested retained relay state does not exist, or
	// a heartbeat target has no active registration.
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

// ModerationIntent records one bounded operator decision for an existing
// canonical relay. ModeratorID and ReasonCode are private audit metadata and
// must never be returned by public directory APIs.
type ModerationIntent struct {
	RelayActor  string
	ModeratorID string
	ReasonCode  string
}

// ModerationOutcome classifies an idempotent administrative transition. It is
// internal storage vocabulary, not a public version 1 protocol response.
type ModerationOutcome string

const (
	ModerationSuspended        ModerationOutcome = "suspended"
	ModerationAlreadySuspended ModerationOutcome = "already_suspended"
	ModerationRestored         ModerationOutcome = "restored"
	ModerationAlreadyActive    ModerationOutcome = "already_active"
)

// Valid reports whether an outcome belongs to the closed moderation contract.
func (outcome ModerationOutcome) Valid() bool {
	switch outcome {
	case ModerationSuspended,
		ModerationAlreadySuspended,
		ModerationRestored,
		ModerationAlreadyActive:
		return true
	default:
		return false
	}
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

// ModerationRepository applies operator-owned suspension and restoration to an
// existing retained relay at a server acceptance time. State and private audit
// metadata commit atomically.
type ModerationRepository interface {
	Suspend(
		context.Context,
		ModerationIntent,
		time.Time,
	) (ModerationOutcome, error)
	Restore(
		context.Context,
		ModerationIntent,
		time.Time,
	) (ModerationOutcome, error)
}
