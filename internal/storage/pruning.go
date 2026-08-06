package storage

import (
	"context"
	"errors"
	"time"
)

const (
	// MaximumPruneCandidatePage bounds each indexed candidate read.
	MaximumPruneCandidatePage = 100

	// MaximumSoftPruneAttemptsPerRun is the fixed process-wide candidate-attempt
	// budget for one automatic maintenance run. It is deliberately not runtime
	// configurable.
	MaximumSoftPruneAttemptsPerRun = 1000

	// MinimumSoftPruningInterval prevents an enabled scheduler from repeatedly
	// scanning durable state at an operator-supplied high frequency.
	MinimumSoftPruningInterval = time.Hour

	// DefaultSoftPruningInterval is the conservative automatic maintenance
	// cadence when the scheduler is explicitly enabled.
	DefaultSoftPruningInterval = 24 * time.Hour
)

var (
	// ErrPruningReadInput identifies an invalid cursor, page limit, or captured
	// server observation time.
	ErrPruningReadInput = errors.New("soft-pruning read input is invalid")
)

// PruneOutcome classifies one idempotent transactional soft-pruning attempt.
type PruneOutcome string

const (
	PruneApplied       PruneOutcome = "pruned"
	PruneAlreadyPruned PruneOutcome = "already_pruned"
	PruneNotEligible   PruneOutcome = "not_eligible"
)

// Valid reports whether the value belongs to the closed soft-pruning outcome
// contract.
func (outcome PruneOutcome) Valid() bool {
	switch outcome {
	case PruneApplied, PruneAlreadyPruned, PruneNotEligible:
		return true
	default:
		return false
	}
}

// PruneCandidateCursor is an indexed keyset position ordered by last-seen time
// and canonical relay actor. The zero value starts from the first candidate.
type PruneCandidateCursor struct {
	LastSeenUnix int64
	RelayActor   string
}

// Valid reports whether the cursor is the zero position or a complete
// nonnegative position. Backends still validate canonical actor syntax.
func (cursor PruneCandidateCursor) Valid() bool {
	if cursor == (PruneCandidateCursor{}) {
		return true
	}
	return cursor.LastSeenUnix >= 0 && cursor.RelayActor != ""
}

// PruneCandidateQuery requests one bounded page at a single captured server
// observation time. Eligibility begins exactly at the version 1 30-day health
// boundary.
type PruneCandidateQuery struct {
	After      PruneCandidateCursor
	Limit      int
	ObservedAt time.Time
}

// PruneCandidate is private local maintenance input. Suspension is included so
// dry-run output can demonstrate that administrative state is independent from
// the reversible lifecycle transition.
type PruneCandidate struct {
	RelayActor          string
	PublicBaseURL       string
	AdministrativeState RelayAdministrativeState
	LastSeenUnix        int64
}

// PruneCandidatePage is one bounded keyset page. Next is zero when the query
// observed no later eligible row.
type PruneCandidatePage struct {
	Candidates []PruneCandidate
	Next       PruneCandidateCursor
}

// PruningRepository exposes only the bounded candidate read and the
// transactionally revalidated reversible transition. It never hard-deletes
// retained state or audit history.
type PruningRepository interface {
	PruneCandidates(context.Context, PruneCandidateQuery) (PruneCandidatePage, error)
	SoftPrune(context.Context, IdentityIntent, time.Time) (PruneOutcome, error)
}
