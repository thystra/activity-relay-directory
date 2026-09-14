package storage

import (
	"context"
	"errors"
	"time"
)

const (
	// MaximumInactiveRetentionDays is the largest accepted hard-retention
	// policy. One hundred years is intentionally far above the documented
	// examples while still bounding cutoff arithmetic and configuration input.
	MaximumInactiveRetentionDays = 36500

	// MaximumPurgeCandidatePage bounds one indexed retention candidate read.
	MaximumPurgeCandidatePage = 100

	// MaximumPurgeAttemptsPerRun bounds one manual destructive maintenance run.
	MaximumPurgeAttemptsPerRun = 1000

	// RetentionPolicyVersion identifies the current hard-retention policy/audit
	// contract. Version 2 adds removed discovery state and orphan-observation
	// cleanup while preserving version-1 run records as historical evidence.
	RetentionPolicyVersion = 2

	PurgeCandidateLifecycle PurgeCandidateKind = "lifecycle"
	PurgeCandidateDiscovery PurgeCandidateKind = "discovery"
)

var (
	// ErrRetentionReadInput identifies an invalid retention cutoff, keyset
	// cursor, page size, or policy value.
	ErrRetentionReadInput = errors.New("inactive-retention read input is invalid")

	// ErrRetentionWriteInput identifies a malformed or oversized destructive
	// retention request.
	ErrRetentionWriteInput = errors.New("inactive-retention write input is invalid")
)

type PurgeCandidateKind string

func (kind PurgeCandidateKind) Valid() bool {
	return kind == PurgeCandidateLifecycle || kind == PurgeCandidateDiscovery
}

// PurgeCandidateCursor is a private indexed keyset position ordered by the
// authoritative inactive-transition timestamp, canonical relay actor, and
// candidate kind. The zero value starts from the first candidate.
type PurgeCandidateCursor struct {
	InactiveUnix int64
	RelayActor   string
	Kind         PurgeCandidateKind
}

// Valid reports whether the cursor is zero or a complete nonnegative position.
func (cursor PurgeCandidateCursor) Valid() bool {
	if cursor == (PurgeCandidateCursor{}) {
		return true
	}
	return cursor.InactiveUnix >= 0 && cursor.RelayActor != "" && cursor.Kind.Valid()
}

// PurgeCandidateQuery requests one bounded page at a fixed cutoff. Version 2
// considers administratively active inactive lifecycle rows and removed
// discovery rows.
type PurgeCandidateQuery struct {
	After    PurgeCandidateCursor
	Limit    int
	CutoffAt time.Time
}

// PurgeCandidate is private destructive-maintenance input. RelayActor is never
// emitted by the dry-run or retention-run audit adapters. ObservationRevision
// is 0 when no observation row existed at candidate-read time; the monotonic
// observation revision prevents same-second fresh evidence from being lost to
// a stale destructive candidate.
type PurgeCandidate struct {
	Kind                    PurgeCandidateKind
	RelayActor              string
	LifecycleState          RelayLifecycleState
	InactiveUnix            int64
	UpdatedUnix             int64
	LatestRelayEventID      int64
	LatestModerationEventID int64
	LatestDiscoveryEventID  int64
	ObservationRevision     int64
}

// Valid reports whether the candidate represents a complete snapshot of one
// inactive lifecycle/discovery row and the private decision versions needed to
// reject stale destructive work.
func (candidate PurgeCandidate) Valid() bool {
	if !candidate.Kind.Valid() || candidate.RelayActor == "" ||
		candidate.InactiveUnix < 0 || candidate.UpdatedUnix < candidate.InactiveUnix ||
		candidate.ObservationRevision < 0 {
		return false
	}
	switch candidate.Kind {
	case PurgeCandidateLifecycle:
		return candidate.LatestRelayEventID >= 0 && candidate.LatestModerationEventID >= 0 &&
			(candidate.LifecycleState == LifecycleUnregistered || candidate.LifecycleState == LifecyclePruned)
	case PurgeCandidateDiscovery:
		return candidate.LifecycleState == "" && candidate.LatestDiscoveryEventID >= 0
	default:
		return false
	}
}

// PurgeCandidatePage is one bounded page. Next is zero when no later eligible
// row was observed.
type PurgeCandidatePage struct {
	Candidates []PurgeCandidate
	Next       PurgeCandidateCursor
}

// PurgeBatchResult summarizes one committed destructive batch. A skipped row
// was revalidated transactionally and found no longer eligible. Observation
// deletions are side effects after the final lifecycle/discovery owner is gone
// and therefore do not count as primary candidates.
type PurgeBatchResult struct {
	Attempted             int
	PurgedRelays          int
	PurgedDiscoveries     int
	PurgedObservations    int
	PurgedLifecycleEvents int
	Skipped               int
}

// RetentionOutcome is the closed private final run-audit outcome vocabulary.
type RetentionOutcome string

const (
	RetentionCompleted RetentionOutcome = "completed"
	RetentionCanceled  RetentionOutcome = "canceled"
	RetentionFailed    RetentionOutcome = "failed"
)

// Valid reports whether an outcome belongs to the final private audit contract.
func (outcome RetentionOutcome) Valid() bool {
	switch outcome {
	case RetentionCompleted, RetentionCanceled, RetentionFailed:
		return true
	default:
		return false
	}
}

// RetentionRunStart creates the private aggregate run record before any
// destructive candidate read. The database assigns the run identifier.
type RetentionRunStart struct {
	PolicyVersion int
	RetentionDays int
	ObservedUnix  int64
	CutoffUnix    int64
	BackupSHA256  string
	StartedUnix   int64
}

// RetentionRunFinish finalizes one run after its transactionally checkpointed
// batches. Failed or canceled runs may have scanned candidates that were never
// committed as processed.
type RetentionRunFinish struct {
	RunID                 int64
	CandidatesScanned     int
	PurgedRelays          int
	PurgedDiscoveries     int
	PurgedObservations    int
	PurgedLifecycleEvents int
	Skipped               int
	Batches               int
	Truncated             bool
	Outcome               RetentionOutcome
	FinishedUnix          int64
}

// RetentionRepository exposes bounded reads plus a private run record whose
// aggregate progress is checkpointed atomically with each destructive batch.
type RetentionRepository interface {
	PurgeCandidates(context.Context, PurgeCandidateQuery) (PurgeCandidatePage, error)
	BeginRetentionRun(context.Context, RetentionRunStart) (int64, error)
	PurgeBatch(context.Context, int64, []PurgeCandidate, time.Time) (PurgeBatchResult, error)
	FinishRetentionRun(context.Context, RetentionRunFinish) error
}
