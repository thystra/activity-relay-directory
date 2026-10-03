package sqlite

import (
	"context"
	"testing"
	"time"

	"github.com/thystra/activity-relay-directory/internal/storage"
)

func TestDiscoveryCandidateChecksApplyStagedBackoffThenWeekly(t *testing.T) {
	database := openMigratedTestDatabase(t)
	repository := newTestRelayRepository(t, database)
	ctx := context.Background()
	actor := "https://dead.example/actor"
	base := "https://dead.example"
	intent := storage.DiscoveryCandidateIntent{
		CandidateActorURL: actor,
		PublicBaseURL:     base,
		State:             storage.DiscoveryCandidateUnreachable,
		Failure:           storage.DiscoveryCandidateActorUnreachable,
		OperatorID:        "operator",
		ReasonCode:        "public_list",
		SourceKind:        storage.DiscoverySourceFile,
		SourceLabel:       "curated_list",
	}

	checkedAt := time.Unix(100, 0).UTC()
	if _, err := repository.RetainDiscoveryCandidate(ctx, intent, checkedAt); err != nil {
		t.Fatal(err)
	}

	for failureCount, delay := range []time.Duration{
		storage.DiscoveryCandidateRetryOne,
		storage.DiscoveryCandidateRetryTwo,
		storage.DiscoveryCandidateRetryThree,
		storage.DiscoveryCandidateRetryFour,
		storage.DiscoveryCandidateRetryLong,
		storage.DiscoveryCandidateRetryLong,
	} {
		failures := int64(failureCount + 1)
		before := checkedAt.Add(delay - time.Second)
		page, err := repository.DiscoveryCandidateChecks(ctx, storage.DiscoveryCandidateCheckQuery{
			Limit: 1, ObservedAt: before,
		})
		if err != nil {
			t.Fatalf("checks before failure %d due time: %v", failures, err)
		}
		if len(page.Candidates) != 0 {
			t.Fatalf("failure %d candidate became due early: %#v", failures, page)
		}

		dueAt := checkedAt.Add(delay)
		page, err = repository.DiscoveryCandidateChecks(ctx, storage.DiscoveryCandidateCheckQuery{
			Limit: 1, ObservedAt: dueAt,
		})
		if err != nil {
			t.Fatalf("checks at failure %d due time: %v", failures, err)
		}
		if len(page.Candidates) != 1 || page.Candidates[0].CandidateActorURL != actor ||
			page.Candidates[0].FailureCount != failures || page.Candidates[0].NextCheckUnix != dueAt.Unix() {
			t.Fatalf("failure %d page = %#v", failures, page)
		}

		if failureCount == 5 {
			break
		}
		checkedAt = dueAt
		if _, err := repository.RetainDiscoveryCandidate(ctx, intent, checkedAt); err != nil {
			t.Fatalf("record failure %d: %v", failures+1, err)
		}
	}
}

func TestPromoteDiscoveryCandidateCreatesVerifiedDiscoveryAndResolvesCandidate(t *testing.T) {
	database := openMigratedTestDatabase(t)
	repository := newTestRelayRepository(t, database)
	ctx := context.Background()
	actor := "https://returning.example/actor"
	base := "https://returning.example"

	if _, err := repository.RetainDiscoveryCandidate(ctx, storage.DiscoveryCandidateIntent{
		CandidateActorURL: actor,
		PublicBaseURL:     base,
		State:             storage.DiscoveryCandidateUnreachable,
		Failure:           storage.DiscoveryCandidateActorUnreachable,
		OperatorID:        "operator",
		ReasonCode:        "public_list",
		SourceKind:        storage.DiscoverySourceFile,
		SourceLabel:       "curated_list",
	}, time.Unix(100, 0).UTC()); err != nil {
		t.Fatal(err)
	}

	outcome, err := repository.PromoteDiscoveryCandidate(ctx, storage.DiscoveryCandidatePromotionIntent{
		CandidateActorURL: actor,
		PublicBaseURL:     base,
		InboxURL:          "https://returning.example/inbox",
		InboxState:        storage.InboxMethodRejected,
		OperatorID:        "operator",
		ReasonCode:        "public_list",
		SourceKind:        storage.DiscoverySourceFile,
		SourceLabel:       "curated_list",
	}, time.Unix(200, 0).UTC())
	if err != nil || outcome != storage.DiscoveryCandidatePromoted {
		t.Fatalf("PromoteDiscoveryCandidate() = (%q, %v)", outcome, err)
	}

	discovery, found, err := repository.GetDiscovery(ctx, storage.IdentityIntent{RelayActor: actor})
	if err != nil || !found || discovery.State != storage.DiscoveryActive ||
		discovery.PublicBaseURL != base || discovery.FirstDiscoveredUnix != 200 {
		t.Fatalf("promoted discovery = %#v, found=%t err=%v", discovery, found, err)
	}
	observation, found, err := repository.GetObservation(ctx, storage.IdentityIntent{RelayActor: actor})
	if err != nil || !found || observation.ActorState != storage.ReachabilityReachable ||
		observation.ActorLastCheckedUnix == nil || *observation.ActorLastCheckedUnix != 200 ||
		observation.ActorLastSuccessUnix == nil || *observation.ActorLastSuccessUnix != 200 ||
		observation.InboxURL != "https://returning.example/inbox" ||
		observation.InboxProbeState != storage.InboxMethodRejected {
		t.Fatalf("promoted observation = %#v, found=%t err=%v", observation, found, err)
	}
	candidate, found, err := repository.GetDiscoveryCandidate(ctx, actor)
	if err != nil || !found || candidate.State != storage.DiscoveryCandidateResolved ||
		candidate.LastFailure != "" || candidate.LastSuccessUnix == nil || *candidate.LastSuccessUnix != 200 {
		t.Fatalf("resolved candidate = %#v, found=%t err=%v", candidate, found, err)
	}

	var resolvedEvents int
	if err := database.QueryRow(`SELECT COUNT(*) FROM discovery_candidate_events
		WHERE candidate_actor_url = ? AND action = 'candidate_resolved'`, actor).Scan(&resolvedEvents); err != nil {
		t.Fatal(err)
	}
	if resolvedEvents != 1 {
		t.Fatalf("candidate_resolved events = %d, want 1", resolvedEvents)
	}

	page, err := repository.DiscoveryCandidateChecks(ctx, storage.DiscoveryCandidateCheckQuery{
		Limit: 1, ObservedAt: time.Unix(1_000_000, 0).UTC(),
	})
	if err != nil || len(page.Candidates) != 0 {
		t.Fatalf("resolved candidate remained due: %#v, %v", page, err)
	}
}
