// Package candidatemaintenance retries retained relay candidates with bounded
// backoff and promotes them only after canonical actor validation succeeds.
package candidatemaintenance

import (
	"context"
	"errors"
	"sync"
	"time"

	"github.com/thystra/activity-relay-directory/internal/actorresolver"
	"github.com/thystra/activity-relay-directory/internal/reachability"
	"github.com/thystra/activity-relay-directory/internal/storage"
)

var ErrConfiguration = errors.New("discovery candidate maintenance configuration is invalid")

type Result struct {
	ObservedUnix int64
	Scanned      int
	Promoted     int
	Unreachable  int
	Incompatible int
	Skipped      int
	Truncated    bool
}

type probeResult struct {
	promotion *storage.DiscoveryCandidatePromotionIntent
	failure   storage.DiscoveryCandidateFailure
	err       error
}

func Run(
	ctx context.Context,
	repository storage.DiscoveryCandidateMaintenanceRepository,
	prober reachability.Prober,
	observedAt time.Time,
) (Result, error) {
	result := Result{ObservedUnix: observedAt.UTC().Unix()}
	if ctx == nil || repository == nil || prober == nil || result.ObservedUnix < 0 {
		return result, ErrConfiguration
	}

	var after storage.DiscoveryCandidateCheckCursor
	for result.Scanned < storage.MaximumDiscoveryCandidateAttempts {
		if err := ctx.Err(); err != nil {
			return result, err
		}
		remaining := storage.MaximumDiscoveryCandidateAttempts - result.Scanned
		limit := storage.MaximumDiscoveryCandidatePage
		if remaining < limit {
			limit = remaining
		}
		page, err := repository.DiscoveryCandidateChecks(ctx, storage.DiscoveryCandidateCheckQuery{
			After:      after,
			Limit:      limit,
			ObservedAt: observedAt,
		})
		if err != nil {
			return result, err
		}
		if err := validatePage(page, after, limit, observedAt); err != nil {
			return result, err
		}
		if len(page.Candidates) == 0 {
			return result, nil
		}

		probes := probePage(ctx, prober, page.Candidates)
		for index, candidate := range page.Candidates {
			probe := probes[index]
			if probe.err != nil {
				return result, probe.err
			}
			result.Scanned++
			if probe.promotion != nil {
				intent := *probe.promotion
				intent.OperatorID = candidate.OperatorID
				intent.ReasonCode = candidate.ReasonCode
				intent.SourceKind = candidate.SourceKind
				intent.SourceLabel = candidate.SourceLabel
				outcome, err := repository.PromoteDiscoveryCandidate(ctx, intent, observedAt)
				if err != nil {
					return result, err
				}
				switch outcome {
				case storage.DiscoveryCandidatePromoted:
					result.Promoted++
				case storage.DiscoveryCandidateSkipped:
					result.Skipped++
				default:
					return result, ErrConfiguration
				}
				continue
			}

			state := storage.DiscoveryCandidateUnreachable
			if probe.failure == storage.DiscoveryCandidateActorInvalid {
				state = storage.DiscoveryCandidateIncompatible
				result.Incompatible++
			} else if probe.failure == storage.DiscoveryCandidateActorUnreachable {
				result.Unreachable++
			} else {
				return result, ErrConfiguration
			}
			_, err := repository.RetainDiscoveryCandidate(ctx, storage.DiscoveryCandidateIntent{
				CandidateActorURL: candidate.CandidateActorURL,
				PublicBaseURL:     candidate.PublicBaseURL,
				State:             state,
				Failure:           probe.failure,
				OperatorID:        candidate.OperatorID,
				ReasonCode:        candidate.ReasonCode,
				SourceKind:        candidate.SourceKind,
				SourceLabel:       candidate.SourceLabel,
			}, observedAt)
			if err != nil {
				return result, err
			}
		}

		if page.Next == (storage.DiscoveryCandidateCheckCursor{}) {
			return result, nil
		}
		after = page.Next
		if result.Scanned == storage.MaximumDiscoveryCandidateAttempts {
			result.Truncated = true
			return result, nil
		}
	}
	return result, nil
}

func probePage(
	ctx context.Context,
	prober reachability.Prober,
	candidates []storage.DiscoveryCandidateCheck,
) []probeResult {
	results := make([]probeResult, len(candidates))
	semaphore := make(chan struct{}, storage.MaximumConcurrentCandidateProbes)
	var group sync.WaitGroup
	for index, candidate := range candidates {
		index, candidate := index, candidate
		group.Add(1)
		go func() {
			defer group.Done()
			select {
			case semaphore <- struct{}{}:
			case <-ctx.Done():
				results[index].err = ctx.Err()
				return
			}
			defer func() { <-semaphore }()
			results[index] = probeOne(ctx, prober, candidate)
		}()
	}
	group.Wait()
	return results
}

func probeOne(
	ctx context.Context,
	prober reachability.Prober,
	candidate storage.DiscoveryCandidateCheck,
) probeResult {
	actor, err := prober.ProbeActor(ctx, candidate.CandidateActorURL)
	if err != nil {
		if ctx.Err() != nil {
			return probeResult{err: ctx.Err()}
		}
		failure := storage.DiscoveryCandidateActorUnreachable
		if errors.Is(err, actorresolver.ErrActorDocument) || errors.Is(err, actorresolver.ErrPublicKey) {
			failure = storage.DiscoveryCandidateActorInvalid
		}
		return probeResult{failure: failure}
	}
	if actor.ActorID != candidate.CandidateActorURL {
		return probeResult{failure: storage.DiscoveryCandidateActorInvalid}
	}
	intent := storage.DiscoveryCandidatePromotionIntent{
		CandidateActorURL: candidate.CandidateActorURL,
		PublicBaseURL:     candidate.PublicBaseURL,
		InboxURL:          actor.InboxURL,
		InboxState:        storage.InboxNotChecked,
	}
	if actor.InboxURL == "" {
		return probeResult{promotion: &intent}
	}
	inbox, err := prober.ProbeInbox(ctx, actor.InboxURL)
	if err != nil {
		if ctx.Err() != nil {
			return probeResult{err: ctx.Err()}
		}
		intent.InboxState = storage.InboxUnreachable
		return probeResult{promotion: &intent}
	}
	switch inbox {
	case actorresolver.InboxProbeResponsive:
		intent.InboxState = storage.InboxResponsive
	case actorresolver.InboxProbeMethodRejected:
		intent.InboxState = storage.InboxMethodRejected
	case actorresolver.InboxProbeUnreachable:
		intent.InboxState = storage.InboxUnreachable
	default:
		return probeResult{err: ErrConfiguration}
	}
	return probeResult{promotion: &intent}
}

func validatePage(
	page storage.DiscoveryCandidateCheckPage,
	after storage.DiscoveryCandidateCheckCursor,
	limit int,
	observedAt time.Time,
) error {
	if len(page.Candidates) > limit || len(page.Candidates) > storage.MaximumDiscoveryCandidatePage {
		return ErrConfiguration
	}
	if len(page.Candidates) == 0 {
		if page.Next != (storage.DiscoveryCandidateCheckCursor{}) {
			return ErrConfiguration
		}
		return nil
	}
	previous := after
	for _, candidate := range page.Candidates {
		cursor := storage.DiscoveryCandidateCheckCursor{
			NextCheckUnix: candidate.NextCheckUnix,
			CandidateURL:  candidate.CandidateActorURL,
		}
		if candidate.NextCheckUnix > observedAt.UTC().Unix() || !candidateCursorAfter(cursor, previous) {
			return ErrConfiguration
		}
		previous = cursor
	}
	if page.Next != (storage.DiscoveryCandidateCheckCursor{}) && page.Next != previous {
		return ErrConfiguration
	}
	return nil
}

func candidateCursorAfter(candidate, previous storage.DiscoveryCandidateCheckCursor) bool {
	if previous == (storage.DiscoveryCandidateCheckCursor{}) {
		return true
	}
	return candidate.NextCheckUnix > previous.NextCheckUnix ||
		(candidate.NextCheckUnix == previous.NextCheckUnix && candidate.CandidateURL > previous.CandidateURL)
}
