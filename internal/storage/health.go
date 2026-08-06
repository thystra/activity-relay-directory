package storage

import (
	"context"
	"errors"
	"time"

	v1 "github.com/thystra/activity-relay-directory/internal/protocol/v1"
)

const (
	// MaximumHealthProjectionPage bounds every private health read. Later
	// maintenance and public adapters may choose a lower limit.
	MaximumHealthProjectionPage = 100

	// Version 1 health boundaries are policy constants rather than runtime
	// configuration. Changing them requires a protocol and fixture review.
	HealthyThrough = 36 * time.Hour
	StaleBefore    = 7 * 24 * time.Hour
	DeadBefore     = 30 * 24 * time.Hour
)

var (
	// ErrHealthReadInput identifies an invalid cursor, page size, or captured
	// server observation time.
	ErrHealthReadInput = errors.New("health projection input is invalid")

	// ErrHealthTime identifies a future last-seen value or otherwise invalid
	// server-owned time relationship. Callers must fail closed.
	ErrHealthTime = errors.New("health projection time is invalid")
)

// ClassifyHealth applies the fixed version 1 boundaries to one server-owned
// last-seen timestamp and one captured server observation time.
func ClassifyHealth(lastSeenUnix, observedUnix int64) (v1.HealthState, error) {
	if lastSeenUnix < 0 || observedUnix < 0 || lastSeenUnix > observedUnix {
		return "", ErrHealthTime
	}

	ageSeconds := observedUnix - lastSeenUnix
	switch {
	case ageSeconds <= int64(HealthyThrough/time.Second):
		return v1.HealthHealthy, nil
	case ageSeconds < int64(StaleBefore/time.Second):
		return v1.HealthStale, nil
	case ageSeconds < int64(DeadBefore/time.Second):
		return v1.HealthDead, nil
	default:
		return v1.HealthPrune, nil
	}
}

// HealthProjectionCursor is an indexed keyset position ordered by last-seen
// time and canonical relay actor. The zero value starts from the first row.
// A nonzero cursor is valid only when both fields are complete.
type HealthProjectionCursor struct {
	LastSeenUnix int64
	RelayActor   string
}

// Valid reports whether the cursor is the zero starting position or a complete
// nonnegative position. Backend implementations still validate canonical actor
// syntax before issuing a query.
func (cursor HealthProjectionCursor) Valid() bool {
	if cursor == (HealthProjectionCursor{}) {
		return true
	}
	return cursor.LastSeenUnix >= 0 && cursor.RelayActor != ""
}

// HealthProjectionQuery requests one bounded page. ObservedAt is captured once
// by the caller and classifies every relay in the page against the same server
// time.
type HealthProjectionQuery struct {
	After      HealthProjectionCursor
	Limit      int
	ObservedAt time.Time
}

// HealthProjectionRelay is one active registered relay plus its deterministic
// version 1 health state. It contains no moderation or audit metadata.
type HealthProjectionRelay struct {
	RelayActor    string
	PublicBaseURL string
	HealthState   v1.HealthState
	LastSeenUnix  int64
}

// HealthProjectionPage is one bounded keyset page. Next is zero when no later
// row was observed during this read.
type HealthProjectionPage struct {
	Relays []HealthProjectionRelay
	Next   HealthProjectionCursor
}

// HealthProjectionRepository reads active registered relay health without
// mutating durable state. Suspended and unregistered rows never enter the
// projection.
type HealthProjectionRepository interface {
	ProjectHealth(
		context.Context,
		HealthProjectionQuery,
	) (HealthProjectionPage, error)
}
