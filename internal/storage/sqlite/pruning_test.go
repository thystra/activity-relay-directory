package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"

	v1 "github.com/thystra/activity-relay-directory/internal/protocol/v1"
	"github.com/thystra/activity-relay-directory/internal/pruning"
	"github.com/thystra/activity-relay-directory/internal/storage"
)

func TestPruneCandidatesUseExactBoundaryKeysetAndIgnoreSuspension(t *testing.T) {
	database := openMigratedTestDatabase(t)
	repository := newTestRelayRepository(t, database)
	observed := time.Unix(3_000_000, 0)
	cutoff := observed.Unix() - int64(storage.DeadBefore/time.Second)

	insertPruningRelay(t, database, "https://older.example/actor", lifecycleRegistered, administrativeActive, cutoff-1, nil)
	insertPruningRelay(t, database, "https://exact-a.example/actor", lifecycleRegistered, administrativeActive, cutoff, nil)
	suspendedAt := int64(500)
	insertPruningRelay(t, database, "https://exact-b.example/actor", lifecycleRegistered, administrativeSuspended, cutoff, &suspendedAt)
	insertPruningRelay(t, database, "https://fresh.example/actor", lifecycleRegistered, administrativeActive, cutoff+1, nil)
	unregisteredAt := cutoff + 2
	insertPruningRelayWithLifecycleTime(t, database, "https://unregistered.example/actor", lifecycleUnregistered, administrativeActive, cutoff-10, &unregisteredAt, nil)
	prunedAt := cutoff + 3
	insertPruningRelayWithPrunedTime(t, database, "https://pruned.example/actor", administrativeActive, cutoff-10, &prunedAt, nil)

	first, err := repository.PruneCandidates(context.Background(), storage.PruneCandidateQuery{
		Limit:      2,
		ObservedAt: observed,
	})
	if err != nil {
		t.Fatalf("PruneCandidates(first) error = %v", err)
	}
	if len(first.Candidates) != 2 ||
		first.Candidates[0].RelayActor != "https://older.example/actor" ||
		first.Candidates[1].RelayActor != "https://exact-a.example/actor" ||
		first.Next != (storage.PruneCandidateCursor{
			LastSeenUnix: cutoff,
			RelayActor:   "https://exact-a.example/actor",
		}) {
		t.Fatalf("first page = %#v", first)
	}

	second, err := repository.PruneCandidates(context.Background(), storage.PruneCandidateQuery{
		After:      first.Next,
		Limit:      2,
		ObservedAt: observed,
	})
	if err != nil {
		t.Fatalf("PruneCandidates(second) error = %v", err)
	}
	if len(second.Candidates) != 1 ||
		second.Candidates[0].RelayActor != "https://exact-b.example/actor" ||
		second.Candidates[0].AdministrativeState != storage.AdministrativeSuspended ||
		second.Next != (storage.PruneCandidateCursor{}) {
		t.Fatalf("second page = %#v", second)
	}
}

func TestSoftPruningRunIsRestartSafeAndIdempotent(t *testing.T) {
	path := filepath.Join(t.TempDir(), "directory.sqlite")
	database, err := Open(context.Background(), path)
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	if err := Migrate(context.Background(), database); err != nil {
		t.Fatalf("Migrate() error = %v", err)
	}
	repository := newTestRelayRepository(t, database)
	ctx := context.Background()
	observed := time.Unix(100+int64(storage.DeadBefore/time.Second), 0)

	assertOutcome(t, transitionResultOf(repository.Register(
		ctx,
		storage.RegisterIntent{RelayActor: testRelayActor, PublicBaseURL: testPublicBase},
		time.Unix(100, 0),
	)), v1.OutcomeCreated)

	first, err := pruning.Run(ctx, repository, observed)
	if err != nil || first.Scanned != 1 || first.Pruned != 1 || first.Skipped != 0 || first.Truncated {
		t.Fatalf("first Run() = (%#v, %v)", first, err)
	}
	if err := database.Close(); err != nil {
		t.Fatalf("Close() error = %v", err)
	}

	database, err = Open(context.Background(), path)
	if err != nil {
		t.Fatalf("Open(after restart) error = %v", err)
	}
	t.Cleanup(func() { _ = database.Close() })
	repository = newTestRelayRepository(t, database)
	second, err := pruning.Run(ctx, repository, observed.Add(time.Hour))
	if err != nil || second.Scanned != 0 || second.Pruned != 0 || second.Skipped != 0 || second.Truncated {
		t.Fatalf("second Run() = (%#v, %v)", second, err)
	}
	if got := readTestEventKinds(t, database, testRelayActor); !equalStrings(
		got,
		[]string{eventRegisterCreated, eventRelayPruned},
	) {
		t.Fatalf("events after restart-safe runs = %#v", got)
	}
}

func TestSoftPruneIsReversibleIdempotentAndPreservesSuspension(t *testing.T) {
	database := openMigratedTestDatabase(t)
	repository := newTestRelayRepository(t, database)
	ctx := context.Background()
	observed := time.Unix(100+int64(storage.DeadBefore/time.Second), 0)

	assertOutcome(t, transitionResultOf(repository.Register(
		ctx,
		storage.RegisterIntent{RelayActor: testRelayActor, PublicBaseURL: testPublicBase},
		time.Unix(100, 0),
	)), v1.OutcomeCreated)
	moderation, err := repository.Suspend(
		ctx,
		storage.ModerationIntent{
			RelayActor:  testRelayActor,
			ModeratorID: "operator",
			ReasonCode:  "security_review",
		},
		time.Unix(200, 0),
	)
	if err != nil || moderation != storage.ModerationSuspended {
		t.Fatalf("Suspend() = (%q, %v)", moderation, err)
	}

	outcome, err := repository.SoftPrune(
		ctx,
		storage.IdentityIntent{RelayActor: testRelayActor},
		observed,
	)
	if err != nil || outcome != storage.PruneApplied {
		t.Fatalf("SoftPrune() = (%q, %v)", outcome, err)
	}
	outcome, err = repository.SoftPrune(
		ctx,
		storage.IdentityIntent{RelayActor: testRelayActor},
		observed.Add(time.Second),
	)
	if err != nil || outcome != storage.PruneAlreadyPruned {
		t.Fatalf("SoftPrune(repeat) = (%q, %v)", outcome, err)
	}

	relay := readTestRelay(t, database, testRelayActor)
	if relay.lifecycleState != lifecyclePruned ||
		relay.administrativeState != administrativeSuspended ||
		!relay.prunedAt.Valid || relay.prunedAt.Int64 != observed.Unix() ||
		!relay.suspendedAt.Valid || relay.suspendedAt.Int64 != 200 ||
		relay.unregisteredAt.Valid {
		t.Fatalf("pruned suspended relay = %#v", relay)
	}
	if got := readTestEventKinds(t, database, testRelayActor); !equalStrings(
		got,
		[]string{eventRegisterCreated, eventRelayPruned},
	) {
		t.Fatalf("events after repeated prune = %#v", got)
	}
	operatorState, err := repository.ModerationState(ctx, testRelayActor)
	if err != nil || operatorState.LifecycleState != storage.LifecyclePruned ||
		operatorState.AdministrativeState != storage.AdministrativeSuspended ||
		operatorState.PrunedUnix == nil || *operatorState.PrunedUnix != observed.Unix() {
		t.Fatalf("ModerationState(pruned) = (%#v, %v)", operatorState, err)
	}

	_, err = repository.Register(
		ctx,
		storage.RegisterIntent{RelayActor: testRelayActor, PublicBaseURL: testPublicBase},
		observed.Add(2*time.Second),
	)
	if !errors.Is(err, storage.ErrRelaySuspended) {
		t.Fatalf("Register(suspended pruned) error = %v", err)
	}
	moderation, err = repository.Restore(
		ctx,
		storage.ModerationIntent{
			RelayActor:  testRelayActor,
			ModeratorID: "operator",
			ReasonCode:  "review_complete",
		},
		observed.Add(3*time.Second),
	)
	if err != nil || moderation != storage.ModerationRestored {
		t.Fatalf("Restore() = (%q, %v)", moderation, err)
	}
	assertOutcome(t, transitionResultOf(repository.Register(
		ctx,
		storage.RegisterIntent{RelayActor: testRelayActor, PublicBaseURL: testPublicBase},
		observed.Add(4*time.Second),
	)), v1.OutcomeUpdated)

	relay = readTestRelay(t, database, testRelayActor)
	if relay.lifecycleState != lifecycleRegistered || relay.firstRegisteredAt != 100 ||
		relay.prunedAt.Valid || relay.unregisteredAt.Valid || relay.lastHeartbeat.Valid ||
		relay.lastSeenAtUnix != observed.Add(4*time.Second).Unix() {
		t.Fatalf("re-registered pruned relay = %#v", relay)
	}
	if got := readTestEventKinds(t, database, testRelayActor); !equalStrings(
		got,
		[]string{eventRegisterCreated, eventRelayPruned, eventRegisterUpdated},
	) {
		t.Fatalf("events after re-registration = %#v", got)
	}
	var moderationEvents int
	if err := database.QueryRow(
		`SELECT COUNT(*) FROM moderation_events WHERE relay_actor = ?`,
		testRelayActor,
	).Scan(&moderationEvents); err != nil || moderationEvents != 2 {
		t.Fatalf("moderation history = (%d, %v)", moderationEvents, err)
	}
}

func TestSoftPruneRevalidatesAfterHeartbeatAndRegister(t *testing.T) {
	for _, operation := range []string{"heartbeat", "register"} {
		t.Run(operation, func(t *testing.T) {
			database := openMigratedTestDatabase(t)
			repository := newTestRelayRepository(t, database)
			ctx := context.Background()
			observed := time.Unix(100+int64(storage.DeadBefore/time.Second), 0)

			assertOutcome(t, transitionResultOf(repository.Register(
				ctx,
				storage.RegisterIntent{RelayActor: testRelayActor, PublicBaseURL: testPublicBase},
				time.Unix(100, 0),
			)), v1.OutcomeCreated)
			page, err := repository.PruneCandidates(ctx, storage.PruneCandidateQuery{
				Limit:      1,
				ObservedAt: observed,
			})
			if err != nil || len(page.Candidates) != 1 {
				t.Fatalf("candidate page = (%#v, %v)", page, err)
			}

			switch operation {
			case "heartbeat":
				assertOutcome(t, transitionResultOf(repository.Heartbeat(
					ctx,
					storage.IdentityIntent{RelayActor: testRelayActor},
					observed.Add(time.Second),
				)), v1.OutcomeRecorded)
			case "register":
				assertOutcome(t, transitionResultOf(repository.Register(
					ctx,
					storage.RegisterIntent{RelayActor: testRelayActor, PublicBaseURL: testPublicBase},
					observed.Add(time.Second),
				)), v1.OutcomeUnchanged)
			}

			outcome, err := repository.SoftPrune(
				ctx,
				storage.IdentityIntent{RelayActor: testRelayActor},
				observed,
			)
			if err != nil || outcome != storage.PruneNotEligible {
				t.Fatalf("SoftPrune(after %s) = (%q, %v)", operation, outcome, err)
			}
			if relay := readTestRelay(t, database, testRelayActor); relay.lifecycleState != lifecycleRegistered || relay.prunedAt.Valid {
				t.Fatalf("relay after %s race = %#v", operation, relay)
			}
		})
	}
}

func TestSoftPruneSkipsConcurrentLaterModerationAndRetriesSafely(t *testing.T) {
	database := openMigratedTestDatabase(t)
	repository := newTestRelayRepository(t, database)
	ctx := context.Background()
	observed := time.Unix(100+int64(storage.DeadBefore/time.Second), 0)

	assertOutcome(t, transitionResultOf(repository.Register(
		ctx,
		storage.RegisterIntent{RelayActor: testRelayActor, PublicBaseURL: testPublicBase},
		time.Unix(100, 0),
	)), v1.OutcomeCreated)
	page, err := repository.PruneCandidates(ctx, storage.PruneCandidateQuery{
		Limit:      1,
		ObservedAt: observed,
	})
	if err != nil || len(page.Candidates) != 1 {
		t.Fatalf("candidate page = (%#v, %v)", page, err)
	}
	moderation, err := repository.Suspend(
		ctx,
		storage.ModerationIntent{
			RelayActor:  testRelayActor,
			ModeratorID: "operator",
			ReasonCode:  "concurrent_review",
		},
		observed.Add(time.Second),
	)
	if err != nil || moderation != storage.ModerationSuspended {
		t.Fatalf("Suspend() = (%q, %v)", moderation, err)
	}

	outcome, err := repository.SoftPrune(
		ctx,
		storage.IdentityIntent{RelayActor: testRelayActor},
		observed,
	)
	if err != nil || outcome != storage.PruneNotEligible {
		t.Fatalf("SoftPrune(concurrent moderation) = (%q, %v)", outcome, err)
	}
	relay := readTestRelay(t, database, testRelayActor)
	if relay.lifecycleState != lifecycleRegistered ||
		relay.administrativeState != administrativeSuspended || relay.prunedAt.Valid {
		t.Fatalf("relay after concurrent moderation = %#v", relay)
	}

	outcome, err = repository.SoftPrune(
		ctx,
		storage.IdentityIntent{RelayActor: testRelayActor},
		observed.Add(2*time.Second),
	)
	if err != nil || outcome != storage.PruneApplied {
		t.Fatalf("SoftPrune(retry) = (%q, %v)", outcome, err)
	}
	relay = readTestRelay(t, database, testRelayActor)
	if relay.lifecycleState != lifecyclePruned ||
		relay.administrativeState != administrativeSuspended || !relay.prunedAt.Valid {
		t.Fatalf("relay after retry = %#v", relay)
	}
}

func TestSoftPruneRollsBackWhenAuditInsertFails(t *testing.T) {
	database := openMigratedTestDatabase(t)
	repository := newTestRelayRepository(t, database)
	ctx := context.Background()
	observed := time.Unix(100+int64(storage.DeadBefore/time.Second), 0)

	assertOutcome(t, transitionResultOf(repository.Register(
		ctx,
		storage.RegisterIntent{RelayActor: testRelayActor, PublicBaseURL: testPublicBase},
		time.Unix(100, 0),
	)), v1.OutcomeCreated)
	if _, err := database.Exec(`
		CREATE TRIGGER reject_test_prune_event
		BEFORE INSERT ON relay_events
		WHEN NEW.event_kind = 'relay_pruned'
		BEGIN
		    SELECT RAISE(ABORT, 'test prune event failure');
		END;
	`); err != nil {
		t.Fatalf("create rejecting trigger: %v", err)
	}

	_, err := repository.SoftPrune(
		ctx,
		storage.IdentityIntent{RelayActor: testRelayActor},
		observed,
	)
	if !errors.Is(err, storage.ErrStorageFailure) {
		t.Fatalf("SoftPrune() error = %v, want ErrStorageFailure", err)
	}
	relay := readTestRelay(t, database, testRelayActor)
	if relay.lifecycleState != lifecycleRegistered || relay.prunedAt.Valid ||
		relay.updatedAtUnix != 100 {
		t.Fatalf("relay changed after rollback = %#v", relay)
	}
	if got := readTestEventKinds(t, database, testRelayActor); !equalStrings(
		got,
		[]string{eventRegisterCreated},
	) {
		t.Fatalf("events after rollback = %#v", got)
	}
}

func TestPruneCandidatesRejectInvalidInputAndUseCompositeIndex(t *testing.T) {
	database := openMigratedTestDatabase(t)
	repository := newTestRelayRepository(t, database)
	observed := time.Unix(3_000_000, 0)
	cutoff := observed.Unix() - int64(storage.DeadBefore/time.Second)

	for _, query := range []storage.PruneCandidateQuery{
		{Limit: 0, ObservedAt: observed},
		{Limit: storage.MaximumPruneCandidatePage + 1, ObservedAt: observed},
		{Limit: 1, ObservedAt: time.Unix(-1, 0)},
		{Limit: 1, ObservedAt: observed, After: storage.PruneCandidateCursor{LastSeenUnix: cutoff + 1, RelayActor: testRelayActor}},
		{Limit: 1, ObservedAt: observed, After: storage.PruneCandidateCursor{LastSeenUnix: cutoff, RelayActor: "HTTPS://relay.example/actor"}},
	} {
		if _, err := repository.PruneCandidates(context.Background(), query); !errors.Is(err, storage.ErrPruningReadInput) {
			t.Fatalf("PruneCandidates(%#v) error = %v", query, err)
		}
	}

	rows, err := database.Query(
		`EXPLAIN QUERY PLAN
		 SELECT relay_actor, public_base_url, administrative_state, last_seen_at_unix
		 FROM relays INDEXED BY relays_pruning_candidates_idx
		 WHERE lifecycle_state = ?
		   AND last_seen_at_unix <= ?
		   AND (last_seen_at_unix, relay_actor) > (?, ?)
		 ORDER BY last_seen_at_unix, relay_actor
		 LIMIT ?`,
		lifecycleRegistered,
		cutoff,
		0,
		"",
		2,
	)
	if err != nil {
		t.Fatalf("EXPLAIN QUERY PLAN error = %v", err)
	}
	defer rows.Close()
	var details []string
	for rows.Next() {
		var id, parent, notUsed int
		var detail string
		if err := rows.Scan(&id, &parent, &notUsed, &detail); err != nil {
			t.Fatalf("scan query plan: %v", err)
		}
		details = append(details, detail)
	}
	joined := strings.ReplaceAll(strings.Join(details, "\n"), " ", "")
	if !strings.Contains(joined, "relays_pruning_candidates_idx") ||
		!strings.Contains(joined, "(last_seen_at_unix,relay_actor)>(?,?)") {
		t.Fatalf("query plan = %q", strings.Join(details, "\n"))
	}
}

func insertPruningRelay(
	t *testing.T,
	database *sql.DB,
	actor, lifecycle, administrative string,
	lastSeen int64,
	suspendedAt *int64,
) {
	t.Helper()
	insertPruningRelayWithLifecycleTime(t, database, actor, lifecycle, administrative, lastSeen, nil, suspendedAt)
}

func insertPruningRelayWithLifecycleTime(
	t *testing.T,
	database *sql.DB,
	actor, lifecycle, administrative string,
	lastSeen int64,
	unregisteredAt, suspendedAt *int64,
) {
	t.Helper()
	var prunedAt *int64
	insertPruningRelayRow(t, database, actor, lifecycle, administrative, lastSeen, unregisteredAt, prunedAt, suspendedAt)
}

func insertPruningRelayWithPrunedTime(
	t *testing.T,
	database *sql.DB,
	actor, administrative string,
	lastSeen int64,
	prunedAt, suspendedAt *int64,
) {
	t.Helper()
	insertPruningRelayRow(t, database, actor, lifecyclePruned, administrative, lastSeen, nil, prunedAt, suspendedAt)
}

func insertPruningRelayRow(
	t *testing.T,
	database *sql.DB,
	actor, lifecycle, administrative string,
	lastSeen int64,
	unregisteredAt, prunedAt, suspendedAt *int64,
) {
	t.Helper()
	updated := lastSeen
	for _, value := range []*int64{unregisteredAt, prunedAt, suspendedAt} {
		if value != nil && *value > updated {
			updated = *value
		}
	}
	if _, err := database.Exec(
		`INSERT INTO relays (
		    relay_actor,
		    public_base_url,
		    lifecycle_state,
		    administrative_state,
		    first_registered_at_unix,
		    updated_at_unix,
		    last_seen_at_unix,
		    unregistered_at_unix,
		    pruned_at_unix,
		    suspended_at_unix
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		actor,
		publicBaseForActor(actor),
		lifecycle,
		administrative,
		int64(100),
		updated,
		lastSeen,
		unregisteredAt,
		prunedAt,
		suspendedAt,
	); err != nil {
		t.Fatalf("insert pruning relay %s: %v", actor, err)
	}
}
