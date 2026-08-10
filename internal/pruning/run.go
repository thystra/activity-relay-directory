// Package pruning coordinates bounded reversible soft-pruning runs without
// owning a database connection, scheduler, or public HTTP route.
package pruning

import (
	"context"
	"errors"
	"time"

	v1 "github.com/thystra/activity-relay-directory/internal/protocol/v1"
	"github.com/thystra/activity-relay-directory/internal/storage"
)

var ErrConfiguration = errors.New("soft-pruning runner configuration is invalid")

// Result summarizes one bounded run. Truncated means another eligible page was
// known to exist when the fixed process-wide transition budget was exhausted.
type Result struct {
	ObservedUnix int64
	Scanned      int
	Pruned       int
	Skipped      int
	Truncated    bool
}

// Run scans keyset pages and performs at most MaximumSoftPruneAttemptsPerRun
// transactionally revalidated candidate attempts against one captured observation
// time. It never hard-deletes durable records.
func Run(
	ctx context.Context,
	repository storage.PruningRepository,
	observedAt time.Time,
) (Result, error) {
	if ctx == nil || repository == nil {
		return Result{}, ErrConfiguration
	}
	observedUnix := observedAt.UTC().Unix()
	if observedUnix < 0 {
		return Result{}, ErrConfiguration
	}
	if err := ctx.Err(); err != nil {
		return Result{}, err
	}

	result := Result{ObservedUnix: observedUnix}
	cutoffUnix := observedUnix - int64(storage.DeadBefore/time.Second)
	cursor := storage.PruneCandidateCursor{}

	for result.Scanned < storage.MaximumSoftPruneAttemptsPerRun {
		remaining := storage.MaximumSoftPruneAttemptsPerRun - result.Scanned
		limit := storage.MaximumPruneCandidatePage
		if remaining < limit {
			limit = remaining
		}

		page, err := repository.PruneCandidates(ctx, storage.PruneCandidateQuery{
			After:      cursor,
			Limit:      limit,
			ObservedAt: observedAt,
		})
		if err != nil {
			return result, err
		}
		if len(page.Candidates) == 0 {
			if page.Next != (storage.PruneCandidateCursor{}) {
				return result, ErrConfiguration
			}
			return result, nil
		}
		if len(page.Candidates) > limit {
			return result, ErrConfiguration
		}

		position := cursor
		for _, candidate := range page.Candidates {
			candidatePosition := storage.PruneCandidateCursor{
				LastSeenUnix: candidate.LastSeenUnix,
				RelayActor:   candidate.RelayActor,
			}
			canonicalActor, actorErr := v1.NormalizeRelayActorURL(candidate.RelayActor)
			if !candidatePosition.Valid() || actorErr != nil || canonicalActor != candidate.RelayActor ||
				!candidate.AdministrativeState.Valid() || candidate.LastSeenUnix > cutoffUnix ||
				!cursorAfter(candidatePosition, position) {
				return result, ErrConfiguration
			}
			position = candidatePosition
			if err := ctx.Err(); err != nil {
				return result, err
			}
			outcome, err := repository.SoftPrune(
				ctx,
				storage.IdentityIntent{RelayActor: candidate.RelayActor},
				observedAt,
			)
			if err != nil {
				return result, err
			}
			if !outcome.Valid() {
				return result, ErrConfiguration
			}
			result.Scanned++
			switch outcome {
			case storage.PruneApplied:
				result.Pruned++
			case storage.PruneAlreadyPruned, storage.PruneNotEligible:
				result.Skipped++
			}
		}

		if page.Next == (storage.PruneCandidateCursor{}) {
			return result, nil
		}
		if !page.Next.Valid() || page.Next != position {
			return result, ErrConfiguration
		}
		cursor = page.Next
		if result.Scanned == storage.MaximumSoftPruneAttemptsPerRun {
			result.Truncated = true
			return result, nil
		}
	}
	return result, nil
}

func cursorAfter(candidate, previous storage.PruneCandidateCursor) bool {
	return candidate.LastSeenUnix > previous.LastSeenUnix ||
		(candidate.LastSeenUnix == previous.LastSeenUnix &&
			candidate.RelayActor > previous.RelayActor)
}
