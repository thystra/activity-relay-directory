// Package reachability performs bounded background actor/inbox maintenance.
// It has no public request entry point and never mutates lifecycle heartbeat
// state.
package reachability

import (
	"context"
	"errors"
	"sync"
	"time"

	"github.com/thystra/activity-relay-directory/internal/actorresolver"
	v1 "github.com/thystra/activity-relay-directory/internal/protocol/v1"
	"github.com/thystra/activity-relay-directory/internal/storage"
)

var ErrConfiguration = errors.New("reachability maintenance configuration is invalid")

type Prober interface {
	ProbeActor(context.Context, string) (actorresolver.ActorProbeResult, error)
	ProbeInbox(context.Context, string) (actorresolver.InboxProbeResult, error)
}

type Result struct {
	ObservedUnix        int64
	Scanned             int
	Reachable           int
	Unreachable         int
	Skipped             int
	InboxResponsive     int
	InboxMethodRejected int
	InboxUnreachable    int
	Truncated           bool
}

type probeResult struct {
	intent storage.ReachabilityObservationIntent
	err    error
}

// Run processes at most the fixed attempt budget. Remote work is concurrent;
// durable writes are deliberately serialized in candidate order.
func Run(
	ctx context.Context,
	repository storage.ReachabilityRepository,
	prober Prober,
	observedAt time.Time,
) (Result, error) {
	result := Result{ObservedUnix: observedAt.UTC().Unix()}
	if ctx == nil || repository == nil || prober == nil || result.ObservedUnix < 0 {
		return result, ErrConfiguration
	}

	var after storage.ReachabilityCandidateCursor
	for result.Scanned < storage.MaximumReachabilityAttemptsPerRun {
		if err := ctx.Err(); err != nil {
			return result, err
		}
		remaining := storage.MaximumReachabilityAttemptsPerRun - result.Scanned
		limit := storage.MaximumReachabilityCandidatePage
		if remaining < limit {
			limit = remaining
		}
		page, err := repository.ReachabilityCandidates(ctx, storage.ReachabilityCandidateQuery{
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
		for index := range page.Candidates {
			if probes[index].err != nil {
				return result, probes[index].err
			}
			result.Scanned++
			outcome, err := repository.RecordReachabilityObservation(
				ctx,
				probes[index].intent,
				observedAt,
			)
			if err != nil {
				return result, err
			}
			switch outcome {
			case storage.ReachabilityWriteSkipped:
				result.Skipped++
			case storage.ReachabilityWriteApplied:
				if probes[index].intent.ActorState == storage.ReachabilityReachable {
					result.Reachable++
					switch probes[index].intent.InboxState {
					case storage.InboxResponsive:
						result.InboxResponsive++
					case storage.InboxMethodRejected:
						result.InboxMethodRejected++
					case storage.InboxUnreachable:
						result.InboxUnreachable++
					}
				} else {
					result.Unreachable++
				}
			default:
				return result, ErrConfiguration
			}
		}

		if page.Next == (storage.ReachabilityCandidateCursor{}) {
			return result, nil
		}
		after = page.Next
		if result.Scanned == storage.MaximumReachabilityAttemptsPerRun {
			result.Truncated = true
			return result, nil
		}
	}
	return result, nil
}

func probePage(
	ctx context.Context,
	prober Prober,
	candidates []storage.ReachabilityCandidate,
) []probeResult {
	results := make([]probeResult, len(candidates))
	semaphore := make(chan struct{}, storage.MaximumConcurrentReachabilityProbes)
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
			results[index] = probeOne(ctx, prober, candidate.RelayActor)
		}()
	}
	group.Wait()
	return results
}

func probeOne(ctx context.Context, prober Prober, actor string) probeResult {
	actorResult, err := prober.ProbeActor(ctx, actor)
	if err != nil {
		if ctx.Err() != nil {
			return probeResult{err: ctx.Err()}
		}
		return probeResult{intent: storage.ReachabilityObservationIntent{
			RelayActor: actor,
			ActorState: storage.ReachabilityUnreachable,
			InboxState: storage.InboxNotChecked,
		}}
	}
	if actorResult.ActorID != actor {
		return probeResult{err: ErrConfiguration}
	}
	intent := storage.ReachabilityObservationIntent{
		RelayActor: actor,
		ActorState: storage.ReachabilityReachable,
		InboxURL:   actorResult.InboxURL,
		InboxState: storage.InboxNotChecked,
	}
	if actorResult.InboxURL == "" {
		return probeResult{intent: intent}
	}
	inboxResult, err := prober.ProbeInbox(ctx, actorResult.InboxURL)
	if err != nil {
		if ctx.Err() != nil {
			return probeResult{err: ctx.Err()}
		}
		intent.InboxState = storage.InboxUnreachable
		return probeResult{intent: intent}
	}
	switch inboxResult {
	case actorresolver.InboxProbeResponsive:
		intent.InboxState = storage.InboxResponsive
	case actorresolver.InboxProbeMethodRejected:
		intent.InboxState = storage.InboxMethodRejected
	case actorresolver.InboxProbeUnreachable:
		intent.InboxState = storage.InboxUnreachable
	default:
		return probeResult{err: ErrConfiguration}
	}
	return probeResult{intent: intent}
}

func validatePage(
	page storage.ReachabilityCandidatePage,
	after storage.ReachabilityCandidateCursor,
	limit int,
	observedAt time.Time,
) error {
	if len(page.Candidates) > limit || len(page.Candidates) > storage.MaximumReachabilityCandidatePage {
		return ErrConfiguration
	}
	if len(page.Candidates) == 0 {
		if page.Next != (storage.ReachabilityCandidateCursor{}) {
			return ErrConfiguration
		}
		return nil
	}
	cutoff := observedAt.UTC().Unix() - int64(storage.ReachabilityFreshness/time.Second)
	previous := after
	for _, candidate := range page.Candidates {
		canonical, err := v1.NormalizeRelayActorURL(candidate.RelayActor)
		if err != nil || canonical != candidate.RelayActor {
			return ErrConfiguration
		}
		cursor := candidateCursor(candidate)
		if !cursorAfter(cursor, previous) {
			return ErrConfiguration
		}
		if candidate.ActorLastCheckedUnix != nil && *candidate.ActorLastCheckedUnix >= cutoff {
			return ErrConfiguration
		}
		previous = cursor
	}
	if page.Next != (storage.ReachabilityCandidateCursor{}) && page.Next != previous {
		return ErrConfiguration
	}
	return nil
}

func candidateCursor(candidate storage.ReachabilityCandidate) storage.ReachabilityCandidateCursor {
	cursor := storage.ReachabilityCandidateCursor{RelayActor: candidate.RelayActor}
	if candidate.ActorLastCheckedUnix != nil {
		cursor.HasLastChecked = true
		cursor.LastCheckedUnix = *candidate.ActorLastCheckedUnix
	}
	return cursor
}

func cursorAfter(candidate, previous storage.ReachabilityCandidateCursor) bool {
	if previous == (storage.ReachabilityCandidateCursor{}) {
		return true
	}
	if !previous.HasLastChecked {
		if !candidate.HasLastChecked {
			return candidate.RelayActor > previous.RelayActor
		}
		return true
	}
	if !candidate.HasLastChecked {
		return false
	}
	return candidate.LastCheckedUnix > previous.LastCheckedUnix ||
		(candidate.LastCheckedUnix == previous.LastCheckedUnix && candidate.RelayActor > previous.RelayActor)
}
