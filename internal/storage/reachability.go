package storage

import (
	"context"
	"errors"
	"time"
)

const (
	// ReachabilityMaintenanceInterval is the fixed cadence for the background
	// maintenance worker when the feature is explicitly enabled.
	ReachabilityMaintenanceInterval = time.Hour

	// ReachabilityFreshness is the maximum age of an actor check that is treated
	// as current for maintenance selection and soft-pruning protection.
	ReachabilityFreshness = 6 * time.Hour

	// ReachabilityUnreachableRetry is the low-frequency cadence for a known relay
	// that has remained offline for at least seven days. Such relays continue to
	// be checked indefinitely so a recovery can move them out of unavailable or
	// graveyard presentation without requiring operator intervention.
	ReachabilityUnreachableRetry = 7 * 24 * time.Hour

	// MaximumReachabilityCandidatePage bounds each private candidate read.
	MaximumReachabilityCandidatePage = 24

	// MaximumReachabilityAttemptsPerRun bounds remote actor probes in one worker
	// pass. It is deliberately not runtime configurable.
	MaximumReachabilityAttemptsPerRun = 96

	// MaximumConcurrentReachabilityProbes bounds concurrent remote probes.
	MaximumConcurrentReachabilityProbes = 8
)

var (
	ErrReachabilityReadInput  = errors.New("reachability read input is invalid")
	ErrReachabilityWriteInput = errors.New("reachability write input is invalid")
)

// ReachabilityCandidateCursor is a stable keyset position ordered by never
// checked first, then oldest actor check, then canonical actor URL. RelayActor
// distinguishes a never-checked cursor from the zero starting position.
type ReachabilityCandidateCursor struct {
	HasLastChecked  bool
	LastCheckedUnix int64
	RelayActor      string
}

func (cursor ReachabilityCandidateCursor) Valid() bool {
	if cursor == (ReachabilityCandidateCursor{}) {
		return true
	}
	if cursor.RelayActor == "" {
		return false
	}
	if !cursor.HasLastChecked {
		return cursor.LastCheckedUnix == 0
	}
	return cursor.LastCheckedUnix >= 0
}

// ReachabilityCandidateQuery requests one bounded page against one captured
// server observation time. Normally an actor is due when it has never been
// checked or its last check is older than ReachabilityFreshness. A relay that
// is currently unreachable and has not been seen online for at least seven
// days is retried at ReachabilityUnreachableRetry instead.
type ReachabilityCandidateQuery struct {
	After      ReachabilityCandidateCursor
	Limit      int
	ObservedAt time.Time
}

type ReachabilityCandidate struct {
	RelayActor           string
	ActorLastCheckedUnix *int64
}

type ReachabilityCandidatePage struct {
	Candidates []ReachabilityCandidate
	Next       ReachabilityCandidateCursor
}

// ReachabilityObservationIntent is the complete result of one worker probe.
// A reachable actor with a declared inbox must include the bounded inbox
// diagnostic. An unreachable actor preserves prior inbox evidence.
type ReachabilityObservationIntent struct {
	RelayActor string
	ActorState ReachabilityState
	InboxURL   string
	InboxState InboxProbeState
}

type ReachabilityWriteOutcome string

const (
	ReachabilityWriteApplied ReachabilityWriteOutcome = "applied"
	ReachabilityWriteSkipped ReachabilityWriteOutcome = "skipped"
)

func (outcome ReachabilityWriteOutcome) Valid() bool {
	return outcome == ReachabilityWriteApplied || outcome == ReachabilityWriteSkipped
}

// ReachabilityRepository provides the worker's bounded candidate selection and
// transactionally revalidated observation write. The write must skip stale
// results or identities that are no longer active/eligible.
type ReachabilityRepository interface {
	ReachabilityCandidates(context.Context, ReachabilityCandidateQuery) (ReachabilityCandidatePage, error)
	RecordReachabilityObservation(context.Context, ReachabilityObservationIntent, time.Time) (ReachabilityWriteOutcome, error)
}
