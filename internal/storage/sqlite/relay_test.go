package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"sync"
	"testing"
	"time"

	v1 "github.com/thystra/activity-relay-directory/internal/protocol/v1"
	"github.com/thystra/activity-relay-directory/internal/storage"
)

const (
	testRelayActor = "https://relay.example/actor"
	testPublicBase = "https://relay.example"
)

func TestRelayRepositoryLifecycleTransitions(t *testing.T) {
	database := openMigratedTestDatabase(t)
	repository := newTestRelayRepository(t, database)
	ctx := context.Background()

	assertOutcome(t, transitionResultOf(repository.Register(
		ctx,
		storage.RegisterIntent{
			RelayActor:    testRelayActor,
			PublicBaseURL: testPublicBase,
		},
		time.Unix(100, 0),
	)), v1.OutcomeCreated)
	assertOutcome(t, transitionResultOf(repository.Heartbeat(
		ctx,
		storage.IdentityIntent{RelayActor: testRelayActor},
		time.Unix(110, 0),
	)), v1.OutcomeRecorded)
	assertOutcome(t, transitionResultOf(repository.Register(
		ctx,
		storage.RegisterIntent{
			RelayActor:    testRelayActor,
			PublicBaseURL: testPublicBase,
		},
		time.Unix(120, 0),
	)), v1.OutcomeUnchanged)

	relay := readTestRelay(t, database, testRelayActor)
	if relay.lifecycleState != lifecycleRegistered || relay.updatedAtUnix != 110 ||
		relay.lastSeenAtUnix != 120 || !relay.lastHeartbeat.Valid || relay.lastHeartbeat.Int64 != 110 ||
		relay.unregisteredAt.Valid {
		t.Fatalf("registered relay state = %#v", relay)
	}

	assertOutcome(t, transitionResultOf(repository.Unregister(
		ctx,
		storage.IdentityIntent{RelayActor: testRelayActor},
		time.Unix(130, 0),
	)), v1.OutcomeRemoved)
	assertOutcome(t, transitionResultOf(repository.Unregister(
		ctx,
		storage.IdentityIntent{RelayActor: testRelayActor},
		time.Unix(140, 0),
	)), v1.OutcomeAbsent)

	relay = readTestRelay(t, database, testRelayActor)
	if relay.lifecycleState != lifecycleUnregistered || relay.updatedAtUnix != 130 ||
		relay.lastSeenAtUnix != 120 || !relay.lastHeartbeat.Valid || relay.lastHeartbeat.Int64 != 110 ||
		!relay.unregisteredAt.Valid || relay.unregisteredAt.Int64 != 130 {
		t.Fatalf("unregistered relay state = %#v", relay)
	}

	assertOutcome(t, transitionResultOf(repository.Register(
		ctx,
		storage.RegisterIntent{
			RelayActor:    testRelayActor,
			PublicBaseURL: testPublicBase,
		},
		time.Unix(150, 0),
	)), v1.OutcomeUpdated)
	relay = readTestRelay(t, database, testRelayActor)
	if relay.lifecycleState != lifecycleRegistered || relay.firstRegisteredAt != 100 ||
		relay.updatedAtUnix != 150 || relay.lastSeenAtUnix != 150 ||
		relay.lastHeartbeat.Valid ||
		relay.unregisteredAt.Valid {
		t.Fatalf("restored relay state = %#v", relay)
	}

	wantEvents := []string{
		eventRegisterCreated,
		eventHeartbeatRecorded,
		eventRegisterUnchanged,
		eventUnregisterRemoved,
		eventUnregisterAbsent,
		eventRegisterUpdated,
	}
	if got := readTestEventKinds(t, database, testRelayActor); !equalStrings(got, wantEvents) {
		t.Fatalf("event kinds = %#v, want %#v", got, wantEvents)
	}
}

func TestRelayRepositorySuspensionBlocksRegisterAndHeartbeatButNotUnregister(t *testing.T) {
	database := openMigratedTestDatabase(t)
	repository := newTestRelayRepository(t, database)
	ctx := context.Background()

	assertOutcome(t, transitionResultOf(repository.Register(
		ctx,
		storage.RegisterIntent{
			RelayActor:    testRelayActor,
			PublicBaseURL: testPublicBase,
		},
		time.Unix(100, 0),
	)), v1.OutcomeCreated)
	if _, err := database.Exec(
		`UPDATE relays
		 SET administrative_state = ?, suspended_at_unix = ?, updated_at_unix = ?
		 WHERE relay_actor = ?`,
		administrativeSuspended,
		110,
		110,
		testRelayActor,
	); err != nil {
		t.Fatalf("suspend relay: %v", err)
	}

	_, err := repository.Register(
		ctx,
		storage.RegisterIntent{
			RelayActor:    testRelayActor,
			PublicBaseURL: testPublicBase,
		},
		time.Unix(120, 0),
	)
	if !errors.Is(err, storage.ErrRelaySuspended) {
		t.Fatalf("Register() error = %v, want ErrRelaySuspended", err)
	}
	_, err = repository.Heartbeat(
		ctx,
		storage.IdentityIntent{RelayActor: testRelayActor},
		time.Unix(120, 0),
	)
	if !errors.Is(err, storage.ErrRelaySuspended) {
		t.Fatalf("Heartbeat() error = %v, want ErrRelaySuspended", err)
	}
	assertOutcome(t, transitionResultOf(repository.Unregister(
		ctx,
		storage.IdentityIntent{RelayActor: testRelayActor},
		time.Unix(120, 0),
	)), v1.OutcomeRemoved)

	relay := readTestRelay(t, database, testRelayActor)
	if relay.administrativeState != administrativeSuspended ||
		!relay.suspendedAt.Valid || relay.suspendedAt.Int64 != 110 ||
		relay.lifecycleState != lifecycleUnregistered {
		t.Fatalf("suspended unregistered relay = %#v", relay)
	}
	wantEvents := []string{eventRegisterCreated, eventUnregisterRemoved}
	if got := readTestEventKinds(t, database, testRelayActor); !equalStrings(got, wantEvents) {
		t.Fatalf("event kinds = %#v, want %#v", got, wantEvents)
	}
}

func TestRelayRepositoryHeartbeatRequiresActiveRegistration(t *testing.T) {
	database := openMigratedTestDatabase(t)
	repository := newTestRelayRepository(t, database)
	ctx := context.Background()

	_, err := repository.Heartbeat(
		ctx,
		storage.IdentityIntent{RelayActor: testRelayActor},
		time.Unix(100, 0),
	)
	if !errors.Is(err, storage.ErrRelayAbsent) {
		t.Fatalf("Heartbeat(absent) error = %v, want ErrRelayAbsent", err)
	}
	assertOutcome(t, transitionResultOf(repository.Register(
		ctx,
		storage.RegisterIntent{
			RelayActor:    testRelayActor,
			PublicBaseURL: testPublicBase,
		},
		time.Unix(100, 0),
	)), v1.OutcomeCreated)
	assertOutcome(t, transitionResultOf(repository.Unregister(
		ctx,
		storage.IdentityIntent{RelayActor: testRelayActor},
		time.Unix(110, 0),
	)), v1.OutcomeRemoved)
	_, err = repository.Heartbeat(
		ctx,
		storage.IdentityIntent{RelayActor: testRelayActor},
		time.Unix(120, 0),
	)
	if !errors.Is(err, storage.ErrRelayAbsent) {
		t.Fatalf("Heartbeat(unregistered) error = %v, want ErrRelayAbsent", err)
	}
}

func TestRelayRepositoryUnregisterAbsentIsAuditedAndIdempotent(t *testing.T) {
	database := openMigratedTestDatabase(t)
	repository := newTestRelayRepository(t, database)
	ctx := context.Background()
	intent := storage.IdentityIntent{RelayActor: testRelayActor}

	assertOutcome(t, transitionResultOf(
		repository.Unregister(ctx, intent, time.Unix(100, 0)),
	), v1.OutcomeAbsent)
	assertOutcome(t, transitionResultOf(
		repository.Unregister(ctx, intent, time.Unix(110, 0)),
	), v1.OutcomeAbsent)

	var relayCount int
	if err := database.QueryRow(
		`SELECT COUNT(*) FROM relays WHERE relay_actor = ?`,
		testRelayActor,
	).Scan(&relayCount); err != nil {
		t.Fatalf("count relays: %v", err)
	}
	if relayCount != 0 {
		t.Fatalf("relay count = %d, want 0", relayCount)
	}
	wantEvents := []string{eventUnregisterAbsent, eventUnregisterAbsent}
	if got := readTestEventKinds(t, database, testRelayActor); !equalStrings(got, wantEvents) {
		t.Fatalf("event kinds = %#v, want %#v", got, wantEvents)
	}
}

func TestRelayRepositoryRejectsInvalidInputAndRegressingTime(t *testing.T) {
	database := openMigratedTestDatabase(t)
	repository := newTestRelayRepository(t, database)
	ctx := context.Background()

	for _, intent := range []storage.RegisterIntent{
		{},
		{RelayActor: "HTTPS://relay.example/actor", PublicBaseURL: testPublicBase},
		{RelayActor: testRelayActor, PublicBaseURL: "https://other.example"},
	} {
		_, err := repository.Register(ctx, intent, time.Unix(100, 0))
		if !errors.Is(err, storage.ErrTransitionInput) {
			t.Fatalf("Register(%#v) error = %v, want ErrTransitionInput", intent, err)
		}
	}
	_, err := repository.Unregister(
		ctx,
		storage.IdentityIntent{RelayActor: "https://relay.example/a/../actor"},
		time.Unix(100, 0),
	)
	if !errors.Is(err, storage.ErrTransitionInput) {
		t.Fatalf("Unregister(noncanonical) error = %v, want ErrTransitionInput", err)
	}
	_, err = repository.Unregister(
		ctx,
		storage.IdentityIntent{RelayActor: testRelayActor},
		time.Unix(-1, 0),
	)
	if !errors.Is(err, storage.ErrTransitionTime) {
		t.Fatalf("Unregister(pre-epoch) error = %v, want ErrTransitionTime", err)
	}

	assertOutcome(t, transitionResultOf(repository.Unregister(
		ctx,
		storage.IdentityIntent{RelayActor: testRelayActor},
		time.Unix(200, 0),
	)), v1.OutcomeAbsent)
	_, err = repository.Register(
		ctx,
		storage.RegisterIntent{
			RelayActor:    testRelayActor,
			PublicBaseURL: testPublicBase,
		},
		time.Unix(199, 0),
	)
	if !errors.Is(err, storage.ErrTransitionTime) {
		t.Fatalf("Register(regressing) error = %v, want ErrTransitionTime", err)
	}
	assertOutcome(t, transitionResultOf(repository.Register(
		ctx,
		storage.RegisterIntent{
			RelayActor:    testRelayActor,
			PublicBaseURL: testPublicBase,
		},
		time.Unix(200, 0),
	)), v1.OutcomeCreated)
}

func TestRelayRepositoryRollsBackStateWhenAuditInsertFails(t *testing.T) {
	database := openMigratedTestDatabase(t)
	repository := newTestRelayRepository(t, database)
	if _, err := database.Exec(`
		CREATE TRIGGER relay_events_reject_test
		BEFORE INSERT ON relay_events
		BEGIN
		    SELECT RAISE(ABORT, 'test audit failure');
		END;
	`); err != nil {
		t.Fatalf("create rejecting trigger: %v", err)
	}

	_, err := repository.Register(
		context.Background(),
		storage.RegisterIntent{
			RelayActor:    testRelayActor,
			PublicBaseURL: testPublicBase,
		},
		time.Unix(100, 0),
	)
	if !errors.Is(err, storage.ErrStorageFailure) {
		t.Fatalf("Register() error = %v, want ErrStorageFailure", err)
	}

	for _, table := range []string{"relays", "relay_events"} {
		var count int
		if err := database.QueryRow(`SELECT COUNT(*) FROM ` + table).Scan(&count); err != nil {
			t.Fatalf("count %s: %v", table, err)
		}
		if count != 0 {
			t.Fatalf("%s count = %d, want 0 after rollback", table, count)
		}
	}
}

func TestRelayRepositoryRollsBackHeartbeatAndUnregisterWhenAuditInsertFails(t *testing.T) {
	for _, operation := range []string{"heartbeat", "unregister"} {
		t.Run(operation, func(t *testing.T) {
			database := openMigratedTestDatabase(t)
			repository := newTestRelayRepository(t, database)
			ctx := context.Background()
			assertOutcome(t, transitionResultOf(repository.Register(
				ctx,
				storage.RegisterIntent{
					RelayActor:    testRelayActor,
					PublicBaseURL: testPublicBase,
				},
				time.Unix(100, 0),
			)), v1.OutcomeCreated)
			if _, err := database.Exec(`
				CREATE TRIGGER relay_events_reject_test
				BEFORE INSERT ON relay_events
				BEGIN
				    SELECT RAISE(ABORT, 'test audit failure');
				END;
			`); err != nil {
				t.Fatalf("create rejecting trigger: %v", err)
			}

			var err error
			switch operation {
			case "heartbeat":
				_, err = repository.Heartbeat(
					ctx,
					storage.IdentityIntent{RelayActor: testRelayActor},
					time.Unix(110, 0),
				)
			case "unregister":
				_, err = repository.Unregister(
					ctx,
					storage.IdentityIntent{RelayActor: testRelayActor},
					time.Unix(110, 0),
				)
			}
			if !errors.Is(err, storage.ErrStorageFailure) {
				t.Fatalf("%s error = %v, want ErrStorageFailure", operation, err)
			}

			relay := readTestRelay(t, database, testRelayActor)
			if relay.lifecycleState != lifecycleRegistered ||
				relay.updatedAtUnix != 100 || relay.lastSeenAtUnix != 100 ||
				relay.lastHeartbeat.Valid ||
				relay.unregisteredAt.Valid {
				t.Fatalf("relay changed after %s rollback: %#v", operation, relay)
			}
			if got := readTestEventKinds(t, database, testRelayActor); !equalStrings(
				got,
				[]string{eventRegisterCreated},
			) {
				t.Fatalf("events after %s rollback = %#v", operation, got)
			}
		})
	}
}

func TestRelayRepositorySerializesConcurrentRegistrationAndHeartbeat(t *testing.T) {
	database := openMigratedTestDatabase(t)
	repository := newTestRelayRepository(t, database)
	ctx := context.Background()
	registerIntent := storage.RegisterIntent{
		RelayActor:    testRelayActor,
		PublicBaseURL: testPublicBase,
	}

	const callers = 16
	registerResults := make(chan transitionResult, callers)
	var wait sync.WaitGroup
	for range callers {
		wait.Add(1)
		go func() {
			defer wait.Done()
			outcome, err := repository.Register(ctx, registerIntent, time.Unix(100, 0))
			registerResults <- transitionResult{outcome: outcome, err: err}
		}()
	}
	wait.Wait()
	close(registerResults)

	created := 0
	unchanged := 0
	for result := range registerResults {
		if result.err != nil {
			t.Fatalf("concurrent Register() error = %v", result.err)
		}
		switch result.outcome {
		case v1.OutcomeCreated:
			created++
		case v1.OutcomeUnchanged:
			unchanged++
		default:
			t.Fatalf("concurrent Register() outcome = %q", result.outcome)
		}
	}
	if created != 1 || unchanged != callers-1 {
		t.Fatalf("register outcomes: created=%d unchanged=%d", created, unchanged)
	}

	heartbeatResults := make(chan transitionResult, callers)
	for range callers {
		wait.Add(1)
		go func() {
			defer wait.Done()
			outcome, err := repository.Heartbeat(
				ctx,
				storage.IdentityIntent{RelayActor: testRelayActor},
				time.Unix(110, 0),
			)
			heartbeatResults <- transitionResult{outcome: outcome, err: err}
		}()
	}
	wait.Wait()
	close(heartbeatResults)
	for result := range heartbeatResults {
		if result.err != nil || result.outcome != v1.OutcomeRecorded {
			t.Fatalf(
				"concurrent Heartbeat() = (%q, %v), want recorded",
				result.outcome,
				result.err,
			)
		}
	}

	unregisterResults := make(chan transitionResult, callers)
	for range callers {
		wait.Add(1)
		go func() {
			defer wait.Done()
			outcome, err := repository.Unregister(
				ctx,
				storage.IdentityIntent{RelayActor: testRelayActor},
				time.Unix(120, 0),
			)
			unregisterResults <- transitionResult{outcome: outcome, err: err}
		}()
	}
	wait.Wait()
	close(unregisterResults)
	removed := 0
	absent := 0
	for result := range unregisterResults {
		if result.err != nil {
			t.Fatalf("concurrent Unregister() error = %v", result.err)
		}
		switch result.outcome {
		case v1.OutcomeRemoved:
			removed++
		case v1.OutcomeAbsent:
			absent++
		default:
			t.Fatalf("concurrent Unregister() outcome = %q", result.outcome)
		}
	}
	if removed != 1 || absent != callers-1 {
		t.Fatalf("unregister outcomes: removed=%d absent=%d", removed, absent)
	}

	var relayCount, eventCount int
	if err := database.QueryRow(`SELECT COUNT(*) FROM relays`).Scan(&relayCount); err != nil {
		t.Fatalf("count relays: %v", err)
	}
	if err := database.QueryRow(`SELECT COUNT(*) FROM relay_events`).Scan(&eventCount); err != nil {
		t.Fatalf("count events: %v", err)
	}
	if relayCount != 1 || eventCount != callers*3 {
		t.Fatalf("counts = relays:%d events:%d", relayCount, eventCount)
	}
}

func TestRelayRepositoryConfigurationAndCanceledContext(t *testing.T) {
	if repository, err := NewRelayRepository(nil, storage.AllowWrites); repository != nil ||
		!errors.Is(err, storage.ErrRepositoryConfiguration) {
		t.Fatalf("NewRelayRepository(nil, storage.AllowWrites) = (%v, %v)", repository, err)
	}
	var nilRepository *RelayRepository
	_, err := nilRepository.Unregister(
		context.Background(),
		storage.IdentityIntent{RelayActor: testRelayActor},
		time.Unix(100, 0),
	)
	if !errors.Is(err, storage.ErrRepositoryConfiguration) {
		t.Fatalf("nil repository Unregister() error = %v", err)
	}

	database := openMigratedTestDatabase(t)
	repository := newTestRelayRepository(t, database)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err = repository.Unregister(
		ctx,
		storage.IdentityIntent{RelayActor: testRelayActor},
		time.Unix(100, 0),
	)
	if !errors.Is(err, storage.ErrStorageFailure) || !errors.Is(err, context.Canceled) {
		t.Fatalf("Unregister(canceled) error = %v", err)
	}
}

type transitionResult struct {
	outcome v1.Outcome
	err     error
}

type testRelayRecord struct {
	publicBaseURL       string
	lifecycleState      string
	administrativeState string
	firstRegisteredAt   int64
	updatedAtUnix       int64
	lastSeenAtUnix      int64
	lastHeartbeat       sql.NullInt64
	unregisteredAt      sql.NullInt64
	prunedAt            sql.NullInt64
	suspendedAt         sql.NullInt64
}

func newTestRelayRepository(t *testing.T, database *sql.DB) *RelayRepository {
	t.Helper()
	if _, err := database.Exec(
		`UPDATE directory_policy SET enrollment_open = 1 WHERE singleton = 1`,
	); err != nil {
		t.Fatalf("open test enrollment: %v", err)
	}
	repository, err := NewRelayRepository(database, storage.AllowWrites)
	if err != nil {
		t.Fatalf("NewRelayRepository() error = %v", err)
	}
	return repository
}

func transitionResultOf(outcome v1.Outcome, err error) transitionResult {
	return transitionResult{outcome: outcome, err: err}
}

func assertOutcome(t *testing.T, result transitionResult, want v1.Outcome) {
	t.Helper()
	if result.err != nil || result.outcome != want {
		t.Fatalf(
			"transition = (%q, %v), want %q",
			result.outcome,
			result.err,
			want,
		)
	}
}

func readTestRelay(t *testing.T, database *sql.DB, relayActor string) testRelayRecord {
	t.Helper()
	var relay testRelayRecord
	if err := database.QueryRow(
		`SELECT public_base_url,
		        lifecycle_state,
		        administrative_state,
		        first_registered_at_unix,
		        updated_at_unix,
		        last_seen_at_unix,
		        last_heartbeat_at_unix,
		        unregistered_at_unix,
		        pruned_at_unix,
		        suspended_at_unix
		 FROM relays
		 WHERE relay_actor = ?`,
		relayActor,
	).Scan(
		&relay.publicBaseURL,
		&relay.lifecycleState,
		&relay.administrativeState,
		&relay.firstRegisteredAt,
		&relay.updatedAtUnix,
		&relay.lastSeenAtUnix,
		&relay.lastHeartbeat,
		&relay.unregisteredAt,
		&relay.prunedAt,
		&relay.suspendedAt,
	); err != nil {
		t.Fatalf("read relay: %v", err)
	}
	return relay
}

func readTestEventKinds(t *testing.T, database *sql.DB, relayActor string) []string {
	t.Helper()
	rows, err := database.Query(
		`SELECT event_kind
		 FROM relay_events
		 WHERE relay_actor = ?
		 ORDER BY event_id`,
		relayActor,
	)
	if err != nil {
		t.Fatalf("query relay events: %v", err)
	}
	defer rows.Close()

	var kinds []string
	for rows.Next() {
		var kind string
		if err := rows.Scan(&kind); err != nil {
			t.Fatalf("scan relay event: %v", err)
		}
		kinds = append(kinds, kind)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("iterate relay events: %v", err)
	}
	return kinds
}

func equalStrings(left, right []string) bool {
	if len(left) != len(right) {
		return false
	}
	for index := range left {
		if left[index] != right[index] {
			return false
		}
	}
	return true
}

func TestAllRelayRepositoryMutatorsHonorHardGrowthAdmissionBeforeTransaction(t *testing.T) {
	database := openMigratedTestDatabase(t)
	allowRepository := newTestRelayRepository(t, database)
	ctx := context.Background()
	registeredAt := time.Unix(100, 0)
	if outcome, err := allowRepository.Register(ctx, storage.RegisterIntent{
		RelayActor: testRelayActor, PublicBaseURL: testPublicBase,
	}, registeredAt); err != nil || outcome != v1.OutcomeCreated {
		t.Fatalf("setup Register() = (%q, %v)", outcome, err)
	}
	beforeEvents := readTestEventKinds(t, database, testRelayActor)

	hardRepository, err := NewRelayRepository(database, writeAdmissionError{err: storage.ErrWriteAdmissionHard})
	if err != nil {
		t.Fatalf("NewRelayRepository(hard) error = %v", err)
	}
	assertHard := func(name string, err error) {
		t.Helper()
		if !errors.Is(err, storage.ErrWriteAdmissionHard) {
			t.Fatalf("%s error = %v, want hard-limit rejection", name, err)
		}
	}
	_, err = hardRepository.Register(ctx, storage.RegisterIntent{
		RelayActor: "https://growth-new.example/actor", PublicBaseURL: "https://growth-new.example",
	}, registeredAt.Add(time.Second))
	assertHard("Register", err)
	_, err = hardRepository.Heartbeat(ctx, storage.IdentityIntent{RelayActor: testRelayActor}, registeredAt.Add(time.Second))
	assertHard("Heartbeat", err)
	_, err = hardRepository.Unregister(ctx, storage.IdentityIntent{RelayActor: testRelayActor}, registeredAt.Add(time.Second))
	assertHard("Unregister", err)
	_, err = hardRepository.SetEnrollment(ctx, false, storage.EnrollmentIntent{OperatorID: "growth-test"}, registeredAt.Add(time.Second))
	assertHard("SetEnrollment", err)
	_, err = hardRepository.Suspend(ctx, storage.ModerationIntent{
		RelayActor: testRelayActor, ModeratorID: "growth-test", ReasonCode: "storage_guard",
	}, registeredAt.Add(time.Second))
	assertHard("Suspend", err)
	_, err = hardRepository.Restore(ctx, storage.ModerationIntent{
		RelayActor: testRelayActor, ModeratorID: "growth-test", ReasonCode: "storage_guard",
	}, registeredAt.Add(time.Second))
	assertHard("Restore", err)
	_, err = hardRepository.SoftPrune(
		ctx,
		storage.IdentityIntent{RelayActor: testRelayActor},
		registeredAt.Add(storage.DeadBefore),
	)
	assertHard("SoftPrune", err)

	if got := readTestEventKinds(t, database, testRelayActor); !equalStrings(got, beforeEvents) {
		t.Fatalf("hard-limit attempts changed relay events: before=%#v after=%#v", beforeEvents, got)
	}
	relay := readTestRelay(t, database, testRelayActor)
	if relay.lifecycleState != lifecycleRegistered || relay.administrativeState != administrativeActive || relay.updatedAtUnix != registeredAt.Unix() {
		t.Fatalf("hard-limit attempts changed relay state: %#v", relay)
	}
	open, err := hardRepository.EnrollmentOpen(ctx)
	if err != nil || !open {
		t.Fatalf("EnrollmentOpen() after hard attempts = (%t, %v), want open", open, err)
	}
	var moderationEvents, enrollmentEvents int
	if err := database.QueryRow(`SELECT COUNT(*) FROM moderation_events`).Scan(&moderationEvents); err != nil {
		t.Fatal(err)
	}
	if err := database.QueryRow(`SELECT COUNT(*) FROM enrollment_events`).Scan(&enrollmentEvents); err != nil {
		t.Fatal(err)
	}
	if moderationEvents != 0 || enrollmentEvents != 0 {
		t.Fatalf("hard-limit attempts wrote private events: moderation=%d enrollment=%d", moderationEvents, enrollmentEvents)
	}
}
