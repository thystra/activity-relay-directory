package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/thystra/activity-relay-directory/internal/storage"
)

func TestDirectoryProjectionEligibilityKeepsParticipationPathsIndependent(t *testing.T) {
	database := openMigratedTestDatabase(t)
	repository := newTestRelayRepository(t, database)
	observed := time.Unix(10_000_000, 0).UTC()
	fresh := observed.Add(-storage.ReachabilityFreshness).Unix()
	staleReachability := fresh - 1
	pruneBoundary := observed.Add(-storage.DeadBefore).Unix()

	insertPublicListingRelay(t, database, "https://a-fresh.example/actor", lifecycleRegistered, administrativeActive, observed.Unix()-10)

	insertPublicListingRelay(t, database, "https://b-old-reachable.example/actor", lifecycleRegistered, administrativeActive, pruneBoundary)
	insertDirectoryObservation(t, database, "https://b-old-reachable.example/actor", storage.ReachabilityReachable, fresh, &fresh, true)

	insertPublicListingRelay(t, database, "https://c-old-unreachable.example/actor", lifecycleRegistered, administrativeActive, pruneBoundary)
	lastSuccess := fresh
	insertDirectoryObservation(t, database, "https://c-old-unreachable.example/actor", storage.ReachabilityUnreachable, observed.Unix()-1, &lastSuccess, false)

	insertReachabilityDiscovery(t, database, "https://d-discovered.example/actor", discoveryActive, 100)
	insertDirectoryObservation(t, database, "https://d-discovered.example/actor", storage.ReachabilityReachable, fresh, &fresh, true)

	insertReachabilityDiscovery(t, database, "https://e-stale-discovery.example/actor", discoveryActive, 100)
	insertDirectoryObservation(t, database, "https://e-stale-discovery.example/actor", storage.ReachabilityReachable, staleReachability, &staleReachability, false)

	insertReachabilityDiscovery(t, database, "https://f-unreachable-discovery.example/actor", discoveryActive, 100)
	insertDirectoryObservation(t, database, "https://f-unreachable-discovery.example/actor", storage.ReachabilityUnreachable, observed.Unix()-1, &lastSuccess, false)

	insertPublicListingRelay(t, database, "https://g-unregistered-discovered.example/actor", lifecycleUnregistered, administrativeActive, observed.Unix()-100)
	insertReachabilityDiscovery(t, database, "https://g-unregistered-discovered.example/actor", discoveryActive, 100)
	insertDirectoryObservation(t, database, "https://g-unregistered-discovered.example/actor", storage.ReachabilityReachable, observed.Unix()-2, int64PointerSQLite(observed.Unix()-2), true)

	insertPublicListingRelay(t, database, "https://h-suspended.example/actor", lifecycleUnregistered, administrativeSuspended, observed.Unix()-100)
	insertReachabilityDiscovery(t, database, "https://h-suspended.example/actor", discoveryActive, 100)
	insertDirectoryObservation(t, database, "https://h-suspended.example/actor", storage.ReachabilityReachable, observed.Unix()-2, int64PointerSQLite(observed.Unix()-2), false)

	insertReachabilityDiscovery(t, database, "https://i-removed.example/actor", discoveryRemoved, 100)
	insertDirectoryObservation(t, database, "https://i-removed.example/actor", storage.ReachabilityReachable, observed.Unix()-2, int64PointerSQLite(observed.Unix()-2), false)

	insertPublicListingRelay(t, database, "https://j-both.example/actor", lifecycleRegistered, administrativeActive, observed.Unix()-100)
	insertReachabilityDiscovery(t, database, "https://j-both.example/actor", discoveryActive, 100)
	insertDirectoryObservation(t, database, "https://j-both.example/actor", storage.ReachabilityReachable, observed.Unix()-2, int64PointerSQLite(observed.Unix()-2), true)

	before := totalChanges(t, database)
	page, err := repository.ListDirectoryRelays(context.Background(), storage.DirectoryProjectionQuery{
		Limit: storage.MaximumDirectoryProjectionPage, ObservedAt: observed,
	})
	if err != nil {
		t.Fatalf("ListDirectoryRelays() error = %v", err)
	}
	if after := totalChanges(t, database); after != before {
		t.Fatalf("directory projection mutated database: before=%d after=%d", before, after)
	}

	got := make(map[string]storage.DirectoryProjectionRelay)
	for _, relay := range page.Relays {
		if _, exists := got[relay.RelayActor]; exists {
			t.Fatalf("duplicate actor in projection: %s", relay.RelayActor)
		}
		got[relay.RelayActor] = relay
	}
	wantActors := []string{
		"https://a-fresh.example/actor",
		"https://b-old-reachable.example/actor",
		"https://d-discovered.example/actor",
		"https://g-unregistered-discovered.example/actor",
		"https://j-both.example/actor",
	}
	if len(got) != len(wantActors) {
		t.Fatalf("projected actors = %#v, want %#v", mapKeys(got), wantActors)
	}
	for _, actor := range wantActors {
		if _, exists := got[actor]; !exists {
			t.Fatalf("missing projected actor %s; got %#v", actor, mapKeys(got))
		}
	}

	old := got["https://b-old-reachable.example/actor"]
	if old.HeartbeatState != storage.HeartbeatPrune || old.ActorState != storage.ReachabilityReachable || !old.Registered || old.Discovered {
		t.Fatalf("old reachable registered relay = %#v", old)
	}
	discovered := got["https://d-discovered.example/actor"]
	if discovered.HeartbeatState != storage.HeartbeatNotObserved || discovered.LastSeenUnix != nil ||
		discovered.Registered || !discovered.Discovered || discovered.RFC9421VerifiedUnix == nil {
		t.Fatalf("discovered-only relay = %#v", discovered)
	}
	unregistered := got["https://g-unregistered-discovered.example/actor"]
	if unregistered.Registered || !unregistered.Discovered || unregistered.LastSeenUnix == nil ||
		unregistered.HeartbeatState != storage.HeartbeatHealthy {
		t.Fatalf("unregistered active discovery = %#v", unregistered)
	}
	both := got["https://j-both.example/actor"]
	if !both.Registered || !both.Discovered {
		t.Fatalf("combined participation flags = %#v", both)
	}
}

func TestTranche23AcceptanceParticipationMatrix(t *testing.T) {
	database := openMigratedTestDatabase(t)
	repository := newTestRelayRepository(t, database)
	ctx := context.Background()
	observed := time.Unix(50_000_000, 0).UTC()
	fresh := observed.Add(-time.Second)

	healthyActor := "https://a-healthy-registered.example/actor"
	healthyBase := "https://a-healthy-registered.example"

	deadActor := "https://b-dead-reachable.example/actor"
	deadBase := "https://b-dead-reachable.example"

	discoveredActor := "https://c-discovered-only.example/actor"
	discoveredBase := "https://c-discovered-only.example"

	suspendedRegistered := "https://d-suspended-registered.example/actor"
	suspendedDiscovered := "https://e-suspended-discovered.example/actor"

	// Case 2: real lifecycle registration + heartbeat, followed by an
	// independent successful actor observation.
	if _, err := repository.Register(
		ctx,
		storage.RegisterIntent{
			RelayActor:    healthyActor,
			PublicBaseURL: healthyBase,
		},
		observed.Add(-20*time.Second),
	); err != nil {
		t.Fatalf("healthy Register() error = %v", err)
	}
	if _, err := repository.Heartbeat(
		ctx,
		storage.IdentityIntent{RelayActor: healthyActor},
		observed.Add(-10*time.Second),
	); err != nil {
		t.Fatalf("healthy Heartbeat() error = %v", err)
	}
	if err := repository.RecordActorObservation(
		ctx,
		storage.ActorObservationIntent{
			RelayActor: healthyActor,
			State:      storage.ReachabilityReachable,
		},
		fresh,
	); err != nil {
		t.Fatalf("healthy RecordActorObservation() error = %v", err)
	}

	// Case 3: heartbeat evidence is independently dead while the actor is
	// currently reachable.
	deadSeen := observed.Add(-storage.StaleBefore)
	if _, err := repository.Register(
		ctx,
		storage.RegisterIntent{
			RelayActor:    deadActor,
			PublicBaseURL: deadBase,
		},
		deadSeen.Add(-time.Second),
	); err != nil {
		t.Fatalf("dead Register() error = %v", err)
	}
	if _, err := repository.Heartbeat(
		ctx,
		storage.IdentityIntent{RelayActor: deadActor},
		deadSeen,
	); err != nil {
		t.Fatalf("dead Heartbeat() error = %v", err)
	}
	if err := repository.RecordActorObservation(
		ctx,
		storage.ActorObservationIntent{
			RelayActor: deadActor,
			State:      storage.ReachabilityReachable,
		},
		fresh,
	); err != nil {
		t.Fatalf("dead RecordActorObservation() error = %v", err)
	}

	// Case 4: operator discovery plus successful reachability creates no
	// lifecycle row and therefore no heartbeat timestamp.
	if _, err := repository.AddDiscovery(
		ctx,
		storage.DiscoveryAddIntent{
			RelayActor:    discoveredActor,
			PublicBaseURL: discoveredBase,
			OperatorID:    "tranche23",
			ReasonCode:    "public_relay",
			SourceKind:    storage.DiscoverySourceManual,
			SourceLabel:   "acceptance",
		},
		observed.Add(-100*time.Second),
	); err != nil {
		t.Fatalf("discovered AddDiscovery() error = %v", err)
	}
	if err := repository.RecordActorObservation(
		ctx,
		storage.ActorObservationIntent{
			RelayActor: discoveredActor,
			State:      storage.ReachabilityReachable,
		},
		fresh,
	); err != nil {
		t.Fatalf("discovered RecordActorObservation() error = %v", err)
	}

	// Case 10a: suspension hides registration eligibility.
	insertPublicListingRelay(
		t,
		database,
		suspendedRegistered,
		lifecycleRegistered,
		administrativeSuspended,
		observed.Unix()-10,
	)
	insertDirectoryObservation(
		t,
		database,
		suspendedRegistered,
		storage.ReachabilityReachable,
		fresh.Unix(),
		int64PointerSQLite(fresh.Unix()),
		true,
	)

	// Case 10b: suspension also hides discovery eligibility.
	insertPublicListingRelay(
		t,
		database,
		suspendedDiscovered,
		lifecycleUnregistered,
		administrativeSuspended,
		observed.Unix()-100,
	)
	insertReachabilityDiscovery(
		t,
		database,
		suspendedDiscovered,
		discoveryActive,
		100,
	)
	insertDirectoryObservation(
		t,
		database,
		suspendedDiscovered,
		storage.ReachabilityReachable,
		fresh.Unix(),
		int64PointerSQLite(fresh.Unix()),
		false,
	)

	page, err := repository.ListDirectoryRelays(
		ctx,
		storage.DirectoryProjectionQuery{
			Limit:      storage.MaximumDirectoryProjectionPage,
			ObservedAt: observed,
		},
	)
	if err != nil {
		t.Fatalf("ListDirectoryRelays() error = %v", err)
	}

	got := make(map[string]storage.DirectoryProjectionRelay)
	for _, relay := range page.Relays {
		got[relay.RelayActor] = relay
	}

	if len(got) != 3 {
		t.Fatalf("acceptance projection actors = %#v", mapKeys(got))
	}

	healthy, ok := got[healthyActor]
	if !ok ||
		healthy.HeartbeatState != storage.HeartbeatHealthy ||
		healthy.ActorState != storage.ReachabilityReachable ||
		healthy.LastSeenUnix == nil ||
		!healthy.Registered ||
		healthy.Discovered {
		t.Fatalf(
			"healthy registered acceptance relay = %#v, present=%t",
			healthy,
			ok,
		)
	}

	dead, ok := got[deadActor]
	if !ok ||
		dead.HeartbeatState != storage.HeartbeatDead ||
		dead.ActorState != storage.ReachabilityReachable ||
		dead.LastSeenUnix == nil ||
		!dead.Registered ||
		dead.Discovered {
		t.Fatalf(
			"dead/reachable registered acceptance relay = %#v, present=%t",
			dead,
			ok,
		)
	}

	discovered, ok := got[discoveredActor]
	if !ok ||
		discovered.HeartbeatState != storage.HeartbeatNotObserved ||
		discovered.ActorState != storage.ReachabilityReachable ||
		discovered.LastSeenUnix != nil ||
		discovered.Registered ||
		!discovered.Discovered {
		t.Fatalf(
			"discovered-only acceptance relay = %#v, present=%t",
			discovered,
			ok,
		)
	}

	if _, ok := got[suspendedRegistered]; ok {
		t.Fatal("suspended registered relay remained public")
	}
	if _, ok := got[suspendedDiscovered]; ok {
		t.Fatal("suspended discovered relay remained public")
	}
}

func TestDirectoryProjectionPaginatesByCanonicalActor(t *testing.T) {
	database := openMigratedTestDatabase(t)
	repository := newTestRelayRepository(t, database)
	observed := time.Unix(20_000_000, 0).UTC()
	actors := []string{
		"https://a.example/actor",
		"https://b.example/actor",
		"https://c.example/actor",
		"https://d.example/actor",
		"https://e.example/actor",
	}
	for index, actor := range actors {
		insertReachabilityDiscovery(t, database, actor, discoveryActive, int64(index+1))
		checked := observed.Unix() - int64(index+1)
		insertDirectoryObservation(t, database, actor, storage.ReachabilityReachable, checked, &checked, false)
	}

	var got []string
	cursor := storage.DirectoryProjectionCursor{}
	for {
		page, err := repository.ListDirectoryRelays(context.Background(), storage.DirectoryProjectionQuery{
			After: cursor, Limit: 2, ObservedAt: observed,
		})
		if err != nil {
			t.Fatalf("ListDirectoryRelays(%#v) error = %v", cursor, err)
		}
		for _, relay := range page.Relays {
			got = append(got, relay.RelayActor)
		}
		if page.Next == (storage.DirectoryProjectionCursor{}) {
			break
		}
		if page.Next == cursor {
			t.Fatalf("cursor did not advance: %#v", cursor)
		}
		cursor = page.Next
	}
	if !equalStrings(got, actors) {
		t.Fatalf("paginated actors = %#v, want %#v", got, actors)
	}
}

func TestDirectoryProjectionPaginatesBackwardByCanonicalActor(t *testing.T) {
	database := openMigratedTestDatabase(t)
	repository := newTestRelayRepository(t, database)
	observed := time.Unix(20_000_000, 0).UTC()
	actors := []string{
		"https://a.example/actor",
		"https://b.example/actor",
		"https://c.example/actor",
		"https://d.example/actor",
		"https://e.example/actor",
	}
	for index, actor := range actors {
		insertReachabilityDiscovery(t, database, actor, discoveryActive, int64(index+1))
		checked := observed.Unix() - int64(index+1)
		insertDirectoryObservation(t, database, actor, storage.ReachabilityReachable, checked, &checked, false)
	}

	first, err := repository.ListDirectoryRelays(context.Background(), storage.DirectoryProjectionQuery{
		Limit: 2, ObservedAt: observed,
	})
	if err != nil {
		t.Fatalf("first page error = %v", err)
	}
	second, err := repository.ListDirectoryRelays(context.Background(), storage.DirectoryProjectionQuery{
		After: first.Next, Limit: 2, ObservedAt: observed,
	})
	if err != nil {
		t.Fatalf("second page error = %v", err)
	}
	third, err := repository.ListDirectoryRelays(context.Background(), storage.DirectoryProjectionQuery{
		After: second.Next, Limit: 2, ObservedAt: observed,
	})
	if err != nil {
		t.Fatalf("third page error = %v", err)
	}
	if third.Previous == (storage.DirectoryProjectionCursor{}) {
		t.Fatalf("third page missing previous cursor: %#v", third)
	}

	backSecond, err := repository.ListDirectoryRelays(context.Background(), storage.DirectoryProjectionQuery{
		Before: third.Previous, Limit: 2, ObservedAt: observed,
	})
	if err != nil {
		t.Fatalf("back to second page error = %v", err)
	}
	if got := relayActors(backSecond.Relays); !equalStrings(got, actors[2:4]) {
		t.Fatalf("back second actors = %#v, want %#v", got, actors[2:4])
	}
	if backSecond.Previous == (storage.DirectoryProjectionCursor{}) ||
		backSecond.Next != second.Next {
		t.Fatalf("back second cursors = previous %#v next %#v; want previous and next %#v", backSecond.Previous, backSecond.Next, second.Next)
	}

	backFirst, err := repository.ListDirectoryRelays(context.Background(), storage.DirectoryProjectionQuery{
		Before: backSecond.Previous, Limit: 2, ObservedAt: observed,
	})
	if err != nil {
		t.Fatalf("back to first page error = %v", err)
	}
	if got := relayActors(backFirst.Relays); !equalStrings(got, actors[:2]) {
		t.Fatalf("back first actors = %#v, want %#v", got, actors[:2])
	}
	if backFirst.Previous != (storage.DirectoryProjectionCursor{}) || backFirst.Next != first.Next {
		t.Fatalf("back first cursors = previous %#v next %#v; want zero previous and next %#v", backFirst.Previous, backFirst.Next, first.Next)
	}
}

func TestDirectoryProjectionBoundsSparseInactiveCandidatesAndAdvancesCursor(t *testing.T) {
	database := openMigratedTestDatabase(t)
	repository := newTestRelayRepository(t, database)
	observed := time.Unix(20_000_000, 0).UTC()

	for index := 0; index < storage.MaximumDirectoryProjectionScan+5; index++ {
		actor := fmt.Sprintf("https://a%03d.example/actor", index)
		insertReachabilityDiscovery(t, database, actor, discoveryRemoved, int64(index+1))
	}
	eligibleActor := "https://z.example/actor"
	insertReachabilityDiscovery(t, database, eligibleActor, discoveryActive, 1_000)
	checked := observed.Unix() - 1
	insertDirectoryObservation(t, database, eligibleActor, storage.ReachabilityReachable, checked, &checked, false)

	first, err := repository.ListDirectoryRelays(context.Background(), storage.DirectoryProjectionQuery{
		Limit: 1, ObservedAt: observed,
	})
	if err != nil {
		t.Fatalf("first ListDirectoryRelays() error = %v", err)
	}
	if len(first.Relays) != 0 || first.Next == (storage.DirectoryProjectionCursor{}) {
		t.Fatalf("first page = %#v, want empty bounded page with continuation", first)
	}

	second, err := repository.ListDirectoryRelays(context.Background(), storage.DirectoryProjectionQuery{
		After: first.Next, Limit: 1, ObservedAt: observed,
	})
	if err != nil {
		t.Fatalf("second ListDirectoryRelays() error = %v", err)
	}
	if len(second.Relays) != 1 || second.Relays[0].RelayActor != eligibleActor ||
		second.Next != (storage.DirectoryProjectionCursor{}) ||
		second.Previous == (storage.DirectoryProjectionCursor{}) {
		t.Fatalf("second page = %#v, want final eligible relay with previous cursor", second)
	}

	back, err := repository.ListDirectoryRelays(context.Background(), storage.DirectoryProjectionQuery{
		Before: second.Previous, Limit: 1, ObservedAt: observed,
	})
	if err != nil {
		t.Fatalf("reverse sparse ListDirectoryRelays() error = %v", err)
	}
	if len(back.Relays) != 0 || back.Previous != (storage.DirectoryProjectionCursor{}) || back.Next != first.Next {
		t.Fatalf("reverse sparse page = %#v, want original empty bounded page", back)
	}
}

func TestDirectoryProjectionFailsClosedWhenRetainedOriginsDisagree(t *testing.T) {
	database := openMigratedTestDatabase(t)
	repository := newTestRelayRepository(t, database)
	observed := time.Unix(20_000_000, 0).UTC()
	actor := "https://relay.example/actor"

	insertPublicListingRelay(t, database, actor, lifecycleRegistered, administrativeActive, observed.Unix()-10)
	insertReachabilityDiscovery(t, database, actor, discoveryActive, observed.Unix()-10)
	if _, err := database.Exec(`UPDATE relay_discoveries SET public_base_url = ? WHERE relay_actor = ?`,
		"https://other.example", actor); err != nil {
		t.Fatalf("corrupt discovery origin: %v", err)
	}
	checked := observed.Unix() - 1
	insertDirectoryObservation(t, database, actor, storage.ReachabilityReachable, checked, &checked, false)

	if _, err := repository.ListDirectoryRelays(context.Background(), storage.DirectoryProjectionQuery{
		Limit: 10, ObservedAt: observed,
	}); err == nil {
		t.Fatal("ListDirectoryRelays() accepted conflicting retained origins")
	}
}

func TestDirectoryProjectionRejectsInvalidInputAndFutureEvidence(t *testing.T) {
	database := openMigratedTestDatabase(t)
	repository := newTestRelayRepository(t, database)
	observed := time.Unix(1_000, 0).UTC()

	for _, query := range []storage.DirectoryProjectionQuery{
		{},
		{Limit: storage.MaximumDirectoryProjectionPage + 1, ObservedAt: observed},
		{Limit: 1, ObservedAt: time.Unix(-1, 0)},
		{Limit: 1, ObservedAt: observed, After: storage.DirectoryProjectionCursor{RelayActor: "HTTPS://relay.example/actor"}},
		{Limit: 1, ObservedAt: observed, Before: storage.DirectoryProjectionCursor{RelayActor: "HTTPS://relay.example/actor"}},
		{Limit: 1, ObservedAt: observed, After: storage.DirectoryProjectionCursor{RelayActor: "https://a.example/actor"}, Before: storage.DirectoryProjectionCursor{RelayActor: "https://b.example/actor"}},
	} {
		_, err := repository.ListDirectoryRelays(context.Background(), query)
		if !errors.Is(err, storage.ErrDirectoryProjectionInput) {
			t.Fatalf("ListDirectoryRelays(%#v) error = %v, want ErrDirectoryProjectionInput", query, err)
		}
	}

	actor := "https://future.example/actor"
	insertReachabilityDiscovery(t, database, actor, discoveryActive, 100)
	future := observed.Unix() + 1
	insertDirectoryObservation(t, database, actor, storage.ReachabilityReachable, future, &future, false)
	_, err := repository.ListDirectoryRelays(context.Background(), storage.DirectoryProjectionQuery{Limit: 10, ObservedAt: observed})
	if !errors.Is(err, storage.ErrDirectoryProjectionData) {
		t.Fatalf("future evidence error = %v, want ErrDirectoryProjectionData", err)
	}
}

func TestDirectoryProjectionStateValidatorsRejectUnknownValues(t *testing.T) {
	if !validDirectoryLifecycleState(lifecycleRegistered) ||
		!validDirectoryLifecycleState(lifecycleUnregistered) ||
		!validDirectoryLifecycleState(lifecyclePruned) ||
		validDirectoryLifecycleState("unexpected") {
		t.Fatal("lifecycle state validator did not preserve the closed state set")
	}
	if !validDirectoryAdministrativeState(administrativeActive) ||
		!validDirectoryAdministrativeState(administrativeSuspended) ||
		validDirectoryAdministrativeState("unexpected") {
		t.Fatal("administrative state validator did not preserve the closed state set")
	}
	if !validDirectoryDiscoveryState(discoveryActive) ||
		!validDirectoryDiscoveryState(discoveryRemoved) ||
		validDirectoryDiscoveryState("unexpected") {
		t.Fatal("discovery state validator did not preserve the closed state set")
	}
}

func TestDirectoryProjectionQueryPlanUsesActorKeysets(t *testing.T) {
	database := openMigratedTestDatabase(t)
	for _, test := range []struct {
		statement string
		keyset    string
	}{
		{directoryRelayCandidateSQL, "PRIMARY KEY (relay_actor>?)"},
		{directoryDiscoveryCandidateSQL, "PRIMARY KEY (relay_actor>?)"},
		{directoryRelayPreviousCandidateSQL, "PRIMARY KEY (relay_actor<?)"},
		{directoryDiscoveryPreviousCandidateSQL, "PRIMARY KEY (relay_actor<?)"},
	} {
		rows, err := database.Query(`EXPLAIN QUERY PLAN `+test.statement, "https://m.example/actor", storage.MaximumDirectoryProjectionScan+1)
		if err != nil {
			t.Fatalf("EXPLAIN QUERY PLAN error = %v", err)
		}
		var details []string
		for rows.Next() {
			var id, parent, unused int
			var detail string
			if err := rows.Scan(&id, &parent, &unused, &detail); err != nil {
				_ = rows.Close()
				t.Fatalf("scan query plan: %v", err)
			}
			details = append(details, detail)
		}
		if err := rows.Close(); err != nil {
			t.Fatalf("close query-plan rows: %v", err)
		}
		joined := strings.Join(details, "\n")
		if !strings.Contains(joined, test.keyset) || strings.Contains(strings.ToUpper(joined), "TEMP B-TREE") {
			t.Fatalf("candidate query plan is not actor-keyset bounded:\n%s", joined)
		}
	}
}

func insertDirectoryObservation(
	t *testing.T,
	database *sql.DB,
	actor string,
	state storage.ReachabilityState,
	checked int64,
	success *int64,
	withDiagnostics bool,
) {
	t.Helper()
	var inboxURL, inboxDeclared, inboxChecked, verified any
	inboxState := string(storage.InboxNotChecked)
	if withDiagnostics {
		inboxURL = publicBaseForActor(actor) + "/inbox"
		inboxDeclared = checked
		inboxChecked = checked
		inboxState = string(storage.InboxMethodRejected)
		verified = checked
	}
	_, err := database.Exec(`INSERT INTO relay_observations (
		relay_actor, actor_state, actor_last_checked_at_unix,
		actor_last_success_at_unix, inbox_url, inbox_declared_at_unix,
		inbox_probe_state, inbox_last_checked_at_unix,
		rfc9421_verified_at_unix, updated_at_unix, revision
	) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, 1)`,
		actor, string(state), checked, nullableTestInt(success), inboxURL, inboxDeclared,
		inboxState, inboxChecked, verified, checked,
	)
	if err != nil {
		t.Fatalf("insert observation %s: %v", actor, err)
	}
}

func nullableTestInt(value *int64) any {
	if value == nil {
		return nil
	}
	return *value
}

func int64PointerSQLite(value int64) *int64 { return &value }

func relayActors(relays []storage.DirectoryProjectionRelay) []string {
	actors := make([]string, 0, len(relays))
	for _, relay := range relays {
		actors = append(actors, relay.RelayActor)
	}
	return actors
}

func mapKeys(values map[string]storage.DirectoryProjectionRelay) []string {
	result := make([]string, 0, len(values))
	for key := range values {
		result = append(result, key)
	}
	return result
}
