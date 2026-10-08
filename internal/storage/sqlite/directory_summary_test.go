package sqlite

import (
	"context"
	"testing"
	"time"

	"github.com/thystra/activity-relay-directory/internal/storage"
)

func TestDirectorySummaryCountsPublicAndPendingRelays(t *testing.T) {
	database := openMigratedTestDatabase(t)
	repository := newTestRelayRepository(t, database)
	observed := time.Unix(20_000_000, 0).UTC()
	fresh := observed.Add(-time.Second).Unix()
	stale := observed.Add(-storage.ReachabilityFreshness - time.Second).Unix()

	insertPublicListingRelay(t, database, "https://a-online.example/actor", lifecycleRegistered, administrativeActive, observed.Unix()-100)
	insertDirectoryObservation(t, database, "https://a-online.example/actor", storage.ReachabilityReachable, fresh, &fresh, true)

	insertReachabilityDiscovery(t, database, "https://b-offline.example/actor", discoveryActive, observed.Unix()-100)
	insertDirectoryObservation(t, database, "https://b-offline.example/actor", storage.ReachabilityReachable, stale, &stale, false)

	insertPublicListingRelay(t, database, "https://c-both.example/actor", lifecycleRegistered, administrativeActive, observed.Unix()-100)
	insertReachabilityDiscovery(t, database, "https://c-both.example/actor", discoveryActive, observed.Unix()-100)
	insertDirectoryObservation(t, database, "https://c-both.example/actor", storage.ReachabilityReachable, fresh, &fresh, true)

	// Authenticated liveness counts as online even if the latest actor GET failed.
	heartbeatOnly := "https://heartbeat-only.example/actor"
	insertPublicListingRelay(t, database, heartbeatOnly, lifecycleRegistered, administrativeActive, observed.Unix()-100)
	if _, err := database.Exec(`UPDATE relays SET last_heartbeat_at_unix=? WHERE relay_actor=?`, observed.Unix()-100, heartbeatOnly); err != nil {
		t.Fatal(err)
	}
	insertDirectoryObservation(t, database, heartbeatOnly, storage.ReachabilityUnreachable, fresh, nil, false)

	insertPublicListingRelay(t, database, "https://d-suspended.example/actor", lifecycleUnregistered, administrativeSuspended, observed.Unix()-100)
	insertReachabilityDiscovery(t, database, "https://d-suspended.example/actor", discoveryActive, observed.Unix()-100)
	insertDirectoryObservation(t, database, "https://d-suspended.example/actor", storage.ReachabilityReachable, fresh, &fresh, false)

	insertReachabilityDiscovery(t, database, "https://e-removed.example/actor", discoveryRemoved, observed.Unix()-100)
	insertDirectoryObservation(t, database, "https://e-removed.example/actor", storage.ReachabilityReachable, fresh, &fresh, false)

	for actor, state := range map[string]storage.DiscoveryCandidateState{
		"https://candidate-unreachable.example/actor":  storage.DiscoveryCandidateUnreachable,
		"https://candidate-incompatible.example/actor": storage.DiscoveryCandidateIncompatible,
		// A stale unresolved-candidate row for an actor that is already verified
		// publicly must not inflate the pending-verification count.
		"https://a-online.example/actor": storage.DiscoveryCandidateUnreachable,
	} {
		failure := storage.DiscoveryCandidateActorUnreachable
		if state == storage.DiscoveryCandidateIncompatible {
			failure = storage.DiscoveryCandidateActorInvalid
		}
		if _, err := repository.RetainDiscoveryCandidate(context.Background(), storage.DiscoveryCandidateIntent{
			CandidateActorURL: actor,
			PublicBaseURL:     actor[:len(actor)-len("/actor")],
			State:             state,
			Failure:           failure,
			OperatorID:        "summary-test",
			ReasonCode:        "public_list",
			SourceKind:        storage.DiscoverySourceFile,
			SourceLabel:       "summary-test",
		}, observed.Add(-time.Minute)); err != nil {
			t.Fatalf("RetainDiscoveryCandidate(%s) error = %v", actor, err)
		}
	}

	if _, err := database.Exec(`INSERT INTO relay_discovery_candidates (
		candidate_actor_url, public_base_url, candidate_state, last_failure_code,
		first_seen_at_unix, last_checked_at_unix, last_success_at_unix,
		failure_count, updated_at_unix
	) VALUES (?, ?, 'resolved', NULL, ?, ?, ?, 0, ?)`,
		"https://candidate-resolved.example/actor",
		"https://candidate-resolved.example",
		observed.Unix()-200,
		observed.Unix()-100,
		observed.Unix()-100,
		observed.Unix()-100,
	); err != nil {
		t.Fatalf("insert resolved candidate error = %v", err)
	}

	before := totalChanges(t, database)
	summary, err := repository.ReadDirectorySummary(context.Background(), observed)
	if err != nil {
		t.Fatalf("ReadDirectorySummary() error = %v", err)
	}
	if after := totalChanges(t, database); after != before {
		t.Fatalf("directory summary mutated database: before=%d after=%d", before, after)
	}
	want := storage.DirectorySummary{
		KnownRelays:         4,
		OnlineRelays:        3,
		OfflineRelays:       1,
		PendingVerification: 2,
	}
	if summary != want {
		t.Fatalf("ReadDirectorySummary() = %#v, want %#v", summary, want)
	}

	page, err := repository.ListDirectoryRelays(context.Background(), storage.DirectoryProjectionQuery{
		Limit:      storage.MaximumDirectoryProjectionPage,
		ObservedAt: observed,
	})
	if err != nil {
		t.Fatalf("ListDirectoryRelays() error = %v", err)
	}
	if page.Next != (storage.DirectoryProjectionCursor{}) {
		t.Fatalf("ListDirectoryRelays() unexpectedly paginated summary fixture: %#v", page.Next)
	}
	online, offline := 0, 0
	for _, relay := range page.Relays {
		switch relay.Tier {
		case storage.DirectoryTierHeartbeatOnline, storage.DirectoryTierOnline:
			online++
		case storage.DirectoryTierUnavailable, storage.DirectoryTierGraveyard:
			offline++
		default:
			t.Fatalf("unexpected tier %d for %s", relay.Tier, relay.RelayActor)
		}
	}
	if len(page.Relays) != summary.KnownRelays || online != summary.OnlineRelays || offline != summary.OfflineRelays {
		t.Fatalf("summary/projection mismatch: relays=%d online=%d offline=%d summary=%#v", len(page.Relays), online, offline, summary)
	}
}

func TestDirectorySummaryRejectsInvalidConfigurationAndTime(t *testing.T) {
	var nilRepository *RelayRepository
	if _, err := nilRepository.ReadDirectorySummary(context.Background(), time.Unix(100, 0)); err != storage.ErrRepositoryConfiguration {
		t.Fatalf("nil repository error = %v, want ErrRepositoryConfiguration", err)
	}

	database := openMigratedTestDatabase(t)
	repository := newTestRelayRepository(t, database)
	if _, err := repository.ReadDirectorySummary(context.Background(), time.Unix(-1, 0)); err != storage.ErrDirectoryProjectionInput {
		t.Fatalf("negative time error = %v, want ErrDirectoryProjectionInput", err)
	}
}
