// Package retention coordinates bounded hard-retention maintenance without
// owning a database connection, scheduler, or public HTTP route.
package retention

import (
	"context"
	"errors"
	"time"

	v1 "github.com/thystra/activity-relay-directory/internal/protocol/v1"
	"github.com/thystra/activity-relay-directory/internal/storage"
)

var ErrConfiguration = errors.New("inactive-retention runner configuration is invalid")

// Summary is the identity-free result of a bounded candidate scan.
type Summary struct {
	ObservedUnix       int64
	CutoffUnix         int64
	CandidateCount     int
	OldestInactiveUnix *int64
	NewestInactiveUnix *int64
	Batches            int
	Truncated          bool
}

// Result summarizes one bounded destructive run. Each completed batch is a
// durable progress checkpoint; restart safely rescans only rows that still
// exist and remain eligible.
type Result struct {
	Summary
	PurgedRelays          int
	PurgedLifecycleEvents int
	Skipped               int
}

// Summarize performs a bounded read-only scan. A zero retention policy is a
// disabled policy and returns an empty summary without querying candidates.
func Summarize(
	ctx context.Context,
	repository interface {
		PurgeCandidates(context.Context, storage.PurgeCandidateQuery) (storage.PurgeCandidatePage, error)
	},
	retentionDays int,
	observedAt time.Time,
) (Summary, error) {
	if ctx == nil || repository == nil || !validRetentionDays(retentionDays) {
		return Summary{}, ErrConfiguration
	}
	observedUnix, cutoffUnix, err := cutoff(retentionDays, observedAt)
	if err != nil {
		return Summary{}, err
	}
	summary := Summary{ObservedUnix: observedUnix, CutoffUnix: cutoffUnix}
	if retentionDays == 0 {
		return summary, nil
	}
	if err := ctx.Err(); err != nil {
		return summary, err
	}

	cursor := storage.PurgeCandidateCursor{}
	for summary.CandidateCount < storage.MaximumPurgeAttemptsPerRun {
		remaining := storage.MaximumPurgeAttemptsPerRun - summary.CandidateCount
		limit := storage.MaximumPurgeCandidatePage
		if remaining < limit {
			limit = remaining
		}
		page, err := repository.PurgeCandidates(ctx, storage.PurgeCandidateQuery{
			After: cursor, Limit: limit, CutoffAt: time.Unix(cutoffUnix, 0).UTC(),
		})
		if err != nil {
			return summary, err
		}
		if err := validatePage(page, cursor, limit, cutoffUnix); err != nil {
			return summary, err
		}
		if len(page.Candidates) == 0 {
			return summary, nil
		}
		summary.Batches++
		for _, candidate := range page.Candidates {
			value := candidate.InactiveUnix
			if summary.OldestInactiveUnix == nil {
				summary.OldestInactiveUnix = &value
			}
			newest := value
			summary.NewestInactiveUnix = &newest
			summary.CandidateCount++
		}
		if page.Next == (storage.PurgeCandidateCursor{}) {
			return summary, nil
		}
		cursor = page.Next
		if summary.CandidateCount == storage.MaximumPurgeAttemptsPerRun {
			summary.Truncated = true
			return summary, nil
		}
	}
	return summary, nil
}

// Run executes bounded hard retention and records one aggregate private audit.
// backupSHA256 must identify the independently verified pre-retention backup.
func Run(
	ctx context.Context,
	repository storage.RetentionRepository,
	retentionDays int,
	observedAt time.Time,
	backupSHA256 string,
) (Result, error) {
	if ctx == nil || repository == nil || retentionDays <= 0 ||
		!validRetentionDays(retentionDays) || !validSHA256(backupSHA256) {
		return Result{}, ErrConfiguration
	}
	observedUnix, cutoffUnix, err := cutoff(retentionDays, observedAt)
	if err != nil {
		return Result{}, err
	}
	if err := ctx.Err(); err != nil {
		return Result{}, err
	}

	runID, err := repository.BeginRetentionRun(ctx, storage.RetentionRunStart{
		PolicyVersion: storage.RetentionPolicyVersion,
		RetentionDays: retentionDays,
		ObservedUnix:  observedUnix,
		CutoffUnix:    cutoffUnix,
		BackupSHA256:  backupSHA256,
		StartedUnix:   observedUnix,
	})
	if err != nil {
		return Result{}, err
	}

	result := Result{Summary: Summary{ObservedUnix: observedUnix, CutoffUnix: cutoffUnix}}
	cursor := storage.PurgeCandidateCursor{}
	runErr := error(nil)

	for result.CandidateCount < storage.MaximumPurgeAttemptsPerRun {
		if err := ctx.Err(); err != nil {
			runErr = err
			break
		}
		remaining := storage.MaximumPurgeAttemptsPerRun - result.CandidateCount
		limit := storage.MaximumPurgeCandidatePage
		if remaining < limit {
			limit = remaining
		}
		page, err := repository.PurgeCandidates(ctx, storage.PurgeCandidateQuery{
			After: cursor, Limit: limit, CutoffAt: time.Unix(cutoffUnix, 0).UTC(),
		})
		if err != nil {
			runErr = err
			break
		}
		if err := validatePage(page, cursor, limit, cutoffUnix); err != nil {
			runErr = err
			break
		}
		if len(page.Candidates) == 0 {
			break
		}

		result.Batches++
		result.CandidateCount += len(page.Candidates)
		for _, candidate := range page.Candidates {
			value := candidate.InactiveUnix
			if result.OldestInactiveUnix == nil {
				result.OldestInactiveUnix = &value
			}
			newest := value
			result.NewestInactiveUnix = &newest
		}

		batch, err := repository.PurgeBatch(
			ctx,
			runID,
			page.Candidates,
			time.Unix(cutoffUnix, 0).UTC(),
		)
		if err != nil {
			runErr = err
			break
		}
		if batch.Attempted != len(page.Candidates) ||
			batch.PurgedRelays < 0 || batch.PurgedLifecycleEvents < 0 ||
			batch.Skipped < 0 || batch.PurgedRelays+batch.Skipped != batch.Attempted {
			runErr = ErrConfiguration
			break
		}
		result.PurgedRelays += batch.PurgedRelays
		result.PurgedLifecycleEvents += batch.PurgedLifecycleEvents
		result.Skipped += batch.Skipped

		if page.Next == (storage.PurgeCandidateCursor{}) {
			break
		}
		cursor = page.Next
		if result.CandidateCount == storage.MaximumPurgeAttemptsPerRun {
			result.Truncated = true
			break
		}
	}

	outcome := storage.RetentionCompleted
	if runErr != nil {
		if errors.Is(runErr, context.Canceled) || errors.Is(runErr, context.DeadlineExceeded) {
			outcome = storage.RetentionCanceled
		} else {
			outcome = storage.RetentionFailed
		}
	}

	finish := storage.RetentionRunFinish{
		RunID:                 runID,
		CandidatesScanned:     result.CandidateCount,
		PurgedRelays:          result.PurgedRelays,
		PurgedLifecycleEvents: result.PurgedLifecycleEvents,
		Skipped:               result.Skipped,
		Batches:               result.Batches,
		Truncated:             result.Truncated,
		Outcome:               outcome,
		FinishedUnix:          observedUnix,
	}
	finishCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	finishErr := repository.FinishRetentionRun(finishCtx, finish)
	if runErr != nil {
		if finishErr != nil {
			return result, errors.Join(runErr, finishErr)
		}
		return result, runErr
	}
	if finishErr != nil {
		return result, finishErr
	}
	return result, nil
}

func validatePage(
	page storage.PurgeCandidatePage,
	previous storage.PurgeCandidateCursor,
	limit int,
	cutoffUnix int64,
) error {
	if len(page.Candidates) > limit {
		return ErrConfiguration
	}
	if len(page.Candidates) == 0 {
		if page.Next != (storage.PurgeCandidateCursor{}) {
			return ErrConfiguration
		}
		return nil
	}
	position := previous
	for _, candidate := range page.Candidates {
		canonical, err := v1.NormalizeRelayActorURL(candidate.RelayActor)
		current := storage.PurgeCandidateCursor{
			InactiveUnix: candidate.InactiveUnix,
			RelayActor:   candidate.RelayActor,
		}
		if err != nil || canonical != candidate.RelayActor || !candidate.Valid() ||
			candidate.InactiveUnix > cutoffUnix || !cursorAfter(current, position) {
			return ErrConfiguration
		}
		position = current
	}
	if page.Next != (storage.PurgeCandidateCursor{}) {
		if !page.Next.Valid() || page.Next != position {
			return ErrConfiguration
		}
	}
	return nil
}

func cursorAfter(candidate, previous storage.PurgeCandidateCursor) bool {
	return candidate.InactiveUnix > previous.InactiveUnix ||
		(candidate.InactiveUnix == previous.InactiveUnix && candidate.RelayActor > previous.RelayActor)
}

func validRetentionDays(days int) bool {
	return days >= 0 && days <= storage.MaximumInactiveRetentionDays
}

func cutoff(days int, observedAt time.Time) (int64, int64, error) {
	if !validRetentionDays(days) {
		return 0, 0, ErrConfiguration
	}
	observedUnix := observedAt.UTC().Unix()
	if observedUnix < 0 {
		return 0, 0, ErrConfiguration
	}
	seconds := int64(days) * int64(24*time.Hour/time.Second)
	if seconds > observedUnix {
		return 0, 0, ErrConfiguration
	}
	return observedUnix, observedUnix - seconds, nil
}

func validSHA256(value string) bool {
	if len(value) != 64 {
		return false
	}
	for _, c := range value {
		if (c < '0' || c > '9') && (c < 'a' || c > 'f') {
			return false
		}
	}
	return true
}
