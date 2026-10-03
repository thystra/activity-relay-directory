package sqlite

import (
	"context"
	"database/sql"
	"testing"
	"time"

	"github.com/thystra/activity-relay-directory/internal/storage"
)

func TestReachabilityCandidatesAreFairDeduplicatedAndSuspensionAware(t *testing.T) {
	database := openMigratedTestDatabase(t)
	repository := newTestRelayRepository(t, database)
	ctx := context.Background()
	observed := time.Unix(1_000_000, 0)
	cutoff := observed.Add(-storage.ReachabilityFreshness)

	insertPruningRelay(t, database, "https://never-a.example/actor", lifecycleRegistered, administrativeActive, 100, nil)
	insertPruningRelay(t, database, "https://never-b.example/actor", lifecycleRegistered, administrativeActive, 100, nil)
	insertPruningRelay(t, database, "https://oldest.example/actor", lifecycleRegistered, administrativeActive, 100, nil)
	insertPruningRelay(t, database, "https://older.example/actor", lifecycleRegistered, administrativeActive, 100, nil)
	insertPruningRelay(t, database, "https://fresh.example/actor", lifecycleRegistered, administrativeActive, 100, nil)
	insertPruningRelay(t, database, "https://dual.example/actor", lifecycleRegistered, administrativeActive, 100, nil)
	insertPruningRelay(t, database, "https://suspended.example/actor", lifecycleRegistered, administrativeSuspended, 100, int64Pointer(200))

	insertReachabilityDiscovery(t, database, "https://discovered.example/actor", discoveryActive, 100)
	insertReachabilityDiscovery(t, database, "https://dual.example/actor", discoveryActive, 100)
	insertReachabilityDiscovery(t, database, "https://suspended.example/actor", discoveryActive, 100)
	insertReachabilityDiscovery(t, database, "https://removed.example/actor", discoveryRemoved, 100)

	for actor, checked := range map[string]time.Time{
		"https://oldest.example/actor": cutoff.Add(-2 * time.Hour),
		"https://older.example/actor":  cutoff.Add(-time.Hour),
		"https://fresh.example/actor":  cutoff,
	} {
		if err := repository.RecordActorObservation(ctx, storage.ActorObservationIntent{
			RelayActor: actor,
			State:      storage.ReachabilityReachable,
		}, checked); err != nil {
			t.Fatalf("RecordActorObservation(%s) error = %v", actor, err)
		}
	}

	var got []string
	var after storage.ReachabilityCandidateCursor
	for {
		page, err := repository.ReachabilityCandidates(ctx, storage.ReachabilityCandidateQuery{
			After:      after,
			Limit:      2,
			ObservedAt: observed,
		})
		if err != nil {
			t.Fatalf("ReachabilityCandidates() error = %v", err)
		}
		for _, candidate := range page.Candidates {
			got = append(got, candidate.RelayActor)
		}
		if page.Next == (storage.ReachabilityCandidateCursor{}) {
			break
		}
		after = page.Next
	}
	want := []string{
		"https://discovered.example/actor",
		"https://dual.example/actor",
		"https://never-a.example/actor",
		"https://never-b.example/actor",
		"https://oldest.example/actor",
		"https://older.example/actor",
	}
	if !equalStrings(got, want) {
		t.Fatalf("candidate order = %#v, want %#v", got, want)
	}
}

func TestRecordReachabilityObservationRevalidatesEligibilityAndTime(t *testing.T) {
	database := openMigratedTestDatabase(t)
	repository := newTestRelayRepository(t, database)
	ctx := context.Background()
	actor := "https://relay.example/actor"
	insertPruningRelay(t, database, actor, lifecycleRegistered, administrativeActive, 100, nil)
	observed := time.Unix(1000, 0)

	outcome, err := repository.RecordReachabilityObservation(ctx, storage.ReachabilityObservationIntent{
		RelayActor: actor,
		ActorState: storage.ReachabilityReachable,
		InboxURL:   "https://relay.example/inbox",
		InboxState: storage.InboxMethodRejected,
	}, observed)
	if err != nil || outcome != storage.ReachabilityWriteApplied {
		t.Fatalf("RecordReachabilityObservation() = (%q, %v)", outcome, err)
	}
	observation, ok, err := repository.GetObservation(ctx, storage.IdentityIntent{RelayActor: actor})
	if err != nil || !ok || observation.ActorState != storage.ReachabilityReachable ||
		observation.ActorLastCheckedUnix == nil || *observation.ActorLastCheckedUnix != observed.Unix() ||
		observation.ActorLastSuccessUnix == nil || *observation.ActorLastSuccessUnix != observed.Unix() ||
		observation.InboxURL != "https://relay.example/inbox" ||
		observation.InboxProbeState != storage.InboxMethodRejected ||
		observation.InboxLastCheckedUnix == nil || *observation.InboxLastCheckedUnix != observed.Unix() {
		t.Fatalf("observation = (%#v, %t, %v)", observation, ok, err)
	}

	outcome, err = repository.RecordReachabilityObservation(ctx, storage.ReachabilityObservationIntent{
		RelayActor: actor,
		ActorState: storage.ReachabilityUnreachable,
		InboxState: storage.InboxNotChecked,
	}, observed)
	if err != nil || outcome != storage.ReachabilityWriteSkipped {
		t.Fatalf("same-time stale write = (%q, %v)", outcome, err)
	}

	if _, err := database.Exec(`UPDATE relays SET administrative_state = ?, suspended_at_unix = ?, updated_at_unix = ? WHERE relay_actor = ?`,
		administrativeSuspended, 1100, 1100, actor); err != nil {
		t.Fatalf("suspend relay: %v", err)
	}
	outcome, err = repository.RecordReachabilityObservation(ctx, storage.ReachabilityObservationIntent{
		RelayActor: actor,
		ActorState: storage.ReachabilityUnreachable,
		InboxState: storage.InboxNotChecked,
	}, observed.Add(time.Hour))
	if err != nil || outcome != storage.ReachabilityWriteSkipped {
		t.Fatalf("suspended write = (%q, %v)", outcome, err)
	}
}

func TestRecordReachabilityObservationAllowsIndependentDiscoveryPath(t *testing.T) {
	database := openMigratedTestDatabase(t)
	repository := newTestRelayRepository(t, database)
	ctx := context.Background()
	actor := "https://discovery-only.example/actor"
	insertReachabilityDiscovery(t, database, actor, discoveryActive, 100)

	outcome, err := repository.RecordReachabilityObservation(ctx, storage.ReachabilityObservationIntent{
		RelayActor: actor,
		ActorState: storage.ReachabilityReachable,
		InboxState: storage.InboxNotChecked,
	}, time.Unix(200, 0))
	if err != nil || outcome != storage.ReachabilityWriteApplied {
		t.Fatalf("active discovery write = (%q, %v)", outcome, err)
	}
	if _, err := database.Exec(`UPDATE relay_discoveries SET discovery_state = ?, removed_at_unix = ?, updated_at_unix = ? WHERE relay_actor = ?`,
		discoveryRemoved, 300, 300, actor); err != nil {
		t.Fatalf("remove discovery: %v", err)
	}
	outcome, err = repository.RecordReachabilityObservation(ctx, storage.ReachabilityObservationIntent{
		RelayActor: actor,
		ActorState: storage.ReachabilityUnreachable,
		InboxState: storage.InboxNotChecked,
	}, time.Unix(400, 0))
	if err != nil || outcome != storage.ReachabilityWriteSkipped {
		t.Fatalf("removed discovery write = (%q, %v)", outcome, err)
	}
}

func TestReachabilityInputValidation(t *testing.T) {
	database := openMigratedTestDatabase(t)
	repository := newTestRelayRepository(t, database)
	ctx := context.Background()
	if _, err := repository.ReachabilityCandidates(ctx, storage.ReachabilityCandidateQuery{
		Limit: storage.MaximumReachabilityCandidatePage + 1, ObservedAt: time.Now(),
	}); err != storage.ErrReachabilityReadInput {
		t.Fatalf("oversized candidate query error = %v", err)
	}
	if _, err := repository.RecordReachabilityObservation(ctx, storage.ReachabilityObservationIntent{
		RelayActor: "https://relay.example/actor",
		ActorState: storage.ReachabilityReachable,
		InboxURL:   "https://relay.example/inbox",
		InboxState: storage.InboxNotChecked,
	}, time.Now()); err != storage.ErrReachabilityWriteInput {
		t.Fatalf("incomplete inbox observation error = %v", err)
	}
}

func insertReachabilityDiscovery(t *testing.T, database *sql.DB, actor, state string, at int64) {
	t.Helper()
	var removed any
	if state == discoveryRemoved {
		removed = at
	}
	if _, err := database.Exec(`INSERT INTO relay_discoveries (
		relay_actor, public_base_url, discovery_state,
		first_discovered_at_unix, updated_at_unix, removed_at_unix
	) VALUES (?, ?, ?, ?, ?, ?)`, actor, publicBaseForActor(actor), state, at, at, removed); err != nil {
		t.Fatalf("insert discovery %s: %v", actor, err)
	}
}

func TestReachabilityCandidatesSlowLongOfflineRelaysToWeeklyAndRetainPrunedRecovery(t *testing.T) {
	database := openMigratedTestDatabase(t)
	repository := newTestRelayRepository(t, database)
	ctx := context.Background()
	observed := time.Unix(2_000_000, 0).UTC()
	week := int64(storage.ReachabilityUnreachableRetry / time.Second)

	recentActor := "https://recent-offline.example/actor"
	insertPruningRelay(
		t, database, recentActor, lifecycleRegistered, administrativeActive,
		observed.Add(-48*time.Hour).Unix(), nil,
	)
	recentChecked := observed.Add(-storage.ReachabilityFreshness - time.Second).Unix()
	recentSuccess := observed.Add(-48 * time.Hour).Unix()
	insertDirectoryObservation(
		t, database, recentActor, storage.ReachabilityUnreachable,
		recentChecked, &recentSuccess, false,
	)

	weeklyActor := "https://weekly-offline.example/actor"
	weeklySeen := observed.Unix() - week - 1
	insertPruningRelay(
		t, database, weeklyActor, lifecycleRegistered, administrativeActive,
		weeklySeen, nil,
	)
	weeklyChecked := observed.Unix() - week
	insertDirectoryObservation(
		t, database, weeklyActor, storage.ReachabilityUnreachable,
		weeklyChecked, &weeklySeen, false,
	)

	prunedActor := "https://pruned-offline.example/actor"
	prunedSeen := observed.Unix() - week - 100
	prunedAt := observed.Unix() - 10
	insertPruningRelayWithPrunedTime(
		t, database, prunedActor, administrativeActive,
		prunedSeen, &prunedAt, nil,
	)
	prunedChecked := observed.Unix() - week - 1
	insertDirectoryObservation(
		t, database, prunedActor, storage.ReachabilityUnreachable,
		prunedChecked, &prunedSeen, false,
	)

	page, err := repository.ReachabilityCandidates(ctx, storage.ReachabilityCandidateQuery{
		Limit: storage.MaximumReachabilityCandidatePage, ObservedAt: observed,
	})
	if err != nil {
		t.Fatalf("ReachabilityCandidates() error = %v", err)
	}
	got := make(map[string]bool)
	for _, candidate := range page.Candidates {
		got[candidate.RelayActor] = true
	}
	if !got[recentActor] {
		t.Fatalf("recent offline relay was not due: %#v", got)
	}
	if got[weeklyActor] {
		t.Fatalf("weekly relay became due at exact seven-day boundary: %#v", got)
	}
	if !got[prunedActor] {
		t.Fatalf("soft-pruned relay was not retained for recovery checks: %#v", got)
	}

	page, err = repository.ReachabilityCandidates(ctx, storage.ReachabilityCandidateQuery{
		Limit: storage.MaximumReachabilityCandidatePage, ObservedAt: observed.Add(time.Second),
	})
	if err != nil {
		t.Fatalf("ReachabilityCandidates(+1s) error = %v", err)
	}
	got = make(map[string]bool)
	for _, candidate := range page.Candidates {
		got[candidate.RelayActor] = true
	}
	if !got[weeklyActor] {
		t.Fatalf("weekly relay was not due after seven days: %#v", got)
	}

	outcome, err := repository.RecordReachabilityObservation(ctx, storage.ReachabilityObservationIntent{
		RelayActor: prunedActor,
		ActorState: storage.ReachabilityReachable,
		InboxState: storage.InboxNotChecked,
	}, observed.Add(2*time.Second))
	if err != nil || outcome != storage.ReachabilityWriteApplied {
		t.Fatalf("pruned recovery write = (%q, %v)", outcome, err)
	}
}
