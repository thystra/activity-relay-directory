package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	v1 "github.com/thystra/activity-relay-directory/internal/protocol/v1"
	"github.com/thystra/activity-relay-directory/internal/storage"
)

const (
	testModeratorID = "operator@example.org"
	testReasonCode  = "security_review"
)

func testModerationIntent() storage.ModerationIntent {
	return storage.ModerationIntent{
		RelayActor:  testRelayActor,
		ModeratorID: testModeratorID,
		ReasonCode:  testReasonCode,
	}
}

func TestModerationRepositorySuspendsRestoresAndAuditsIdempotently(t *testing.T) {
	database := openMigratedTestDatabase(t)
	repository := newTestRelayRepository(t, database)
	ctx := context.Background()
	registerIntent := storage.RegisterIntent{
		RelayActor:    testRelayActor,
		PublicBaseURL: testPublicBase,
	}
	moderationIntent := testModerationIntent()
	assertOutcome(t, transitionResultOf(
		repository.Register(ctx, registerIntent, time.Unix(100, 0)),
	), v1.OutcomeCreated)
	assertOutcome(t, transitionResultOf(repository.Heartbeat(
		ctx,
		storage.IdentityIntent{RelayActor: testRelayActor},
		time.Unix(105, 0),
	)), v1.OutcomeRecorded)

	assertModerationOutcome(t, moderationResultOf(
		repository.Suspend(ctx, moderationIntent, time.Unix(110, 0)),
	), storage.ModerationSuspended)
	relay := readTestRelay(t, database, testRelayActor)
	if relay.lifecycleState != lifecycleRegistered ||
		relay.administrativeState != administrativeSuspended ||
		relay.updatedAtUnix != 110 || !relay.suspendedAt.Valid ||
		relay.suspendedAt.Int64 != 110 || relay.publicBaseURL != testPublicBase ||
		!relay.lastHeartbeat.Valid || relay.lastHeartbeat.Int64 != 105 {
		t.Fatalf("suspended relay = %#v", relay)
	}
	if _, err := repository.Register(ctx, registerIntent, time.Unix(120, 0)); !errors.Is(err, storage.ErrRelaySuspended) {
		t.Fatalf("Register(suspended) error = %v, want ErrRelaySuspended", err)
	}
	if _, err := repository.Heartbeat(
		ctx,
		storage.IdentityIntent{RelayActor: testRelayActor},
		time.Unix(120, 0),
	); !errors.Is(err, storage.ErrRelaySuspended) {
		t.Fatalf("Heartbeat(suspended) error = %v, want ErrRelaySuspended", err)
	}

	assertModerationOutcome(t, moderationResultOf(
		repository.Suspend(ctx, moderationIntent, time.Unix(120, 0)),
	), storage.ModerationAlreadySuspended)
	relay = readTestRelay(t, database, testRelayActor)
	if relay.updatedAtUnix != 110 || relay.suspendedAt.Int64 != 110 {
		t.Fatalf("unchanged suspension moved state time: %#v", relay)
	}
	assertOutcome(t, transitionResultOf(repository.Unregister(
		ctx,
		storage.IdentityIntent{RelayActor: testRelayActor},
		time.Unix(130, 0),
	)), v1.OutcomeRemoved)

	assertModerationOutcome(t, moderationResultOf(
		repository.Restore(ctx, moderationIntent, time.Unix(140, 0)),
	), storage.ModerationRestored)
	relay = readTestRelay(t, database, testRelayActor)
	if relay.lifecycleState != lifecycleUnregistered ||
		relay.administrativeState != administrativeActive ||
		relay.updatedAtUnix != 140 || relay.suspendedAt.Valid ||
		!relay.unregisteredAt.Valid || relay.unregisteredAt.Int64 != 130 ||
		!relay.lastHeartbeat.Valid || relay.lastHeartbeat.Int64 != 105 {
		t.Fatalf("restored relay = %#v", relay)
	}
	assertModerationOutcome(t, moderationResultOf(
		repository.Restore(ctx, moderationIntent, time.Unix(150, 0)),
	), storage.ModerationAlreadyActive)
	relay = readTestRelay(t, database, testRelayActor)
	if relay.updatedAtUnix != 140 || relay.suspendedAt.Valid {
		t.Fatalf("unchanged restoration moved state: %#v", relay)
	}
	assertOutcome(t, transitionResultOf(
		repository.Register(ctx, registerIntent, time.Unix(160, 0)),
	), v1.OutcomeUpdated)

	wantModeration := []testModerationEvent{
		{action: moderationSuspendApplied, moderatorID: testModeratorID, reasonCode: testReasonCode, recordedAt: 110},
		{action: moderationSuspendUnchanged, moderatorID: testModeratorID, reasonCode: testReasonCode, recordedAt: 120},
		{action: moderationRestoreApplied, moderatorID: testModeratorID, reasonCode: testReasonCode, recordedAt: 140},
		{action: moderationRestoreUnchanged, moderatorID: testModeratorID, reasonCode: testReasonCode, recordedAt: 150},
	}
	if got := readTestModerationEvents(t, database, testRelayActor); !equalModerationEvents(got, wantModeration) {
		t.Fatalf("moderation events = %#v, want %#v", got, wantModeration)
	}
	wantLifecycle := []string{
		eventRegisterCreated,
		eventHeartbeatRecorded,
		eventUnregisterRemoved,
		eventRegisterUpdated,
	}
	if got := readTestEventKinds(t, database, testRelayActor); !equalStrings(got, wantLifecycle) {
		t.Fatalf("lifecycle events = %#v, want %#v", got, wantLifecycle)
	}
}

func TestModerationRepositoryRejectsAbsentInvalidAndRegressingIntents(t *testing.T) {
	database := openMigratedTestDatabase(t)
	repository := newTestRelayRepository(t, database)
	ctx := context.Background()
	intent := testModerationIntent()

	for _, operation := range []func() (storage.ModerationOutcome, error){
		func() (storage.ModerationOutcome, error) {
			return repository.Suspend(ctx, intent, time.Unix(100, 0))
		},
		func() (storage.ModerationOutcome, error) {
			return repository.Restore(ctx, intent, time.Unix(100, 0))
		},
	} {
		outcome, err := operation()
		if outcome != "" || !errors.Is(err, storage.ErrRelayAbsent) {
			t.Fatalf("absent moderation = (%q, %v), want ErrRelayAbsent", outcome, err)
		}
	}
	if events := readTestModerationEvents(t, database, testRelayActor); len(events) != 0 {
		t.Fatalf("absent moderation created events: %#v", events)
	}
	assertOutcome(t, transitionResultOf(repository.Register(
		ctx,
		storage.RegisterIntent{RelayActor: testRelayActor, PublicBaseURL: testPublicBase},
		time.Unix(100, 0),
	)), v1.OutcomeCreated)

	invalid := []storage.ModerationIntent{
		{},
		{RelayActor: "HTTPS://relay.example/actor", ModeratorID: "operator", ReasonCode: "security"},
		{RelayActor: testRelayActor, ModeratorID: "", ReasonCode: "security"},
		{RelayActor: testRelayActor, ModeratorID: "-operator", ReasonCode: "security"},
		{RelayActor: testRelayActor, ModeratorID: "operator name", ReasonCode: "security"},
		{RelayActor: testRelayActor, ModeratorID: "operator/role", ReasonCode: "security"},
		{RelayActor: testRelayActor, ModeratorID: "op\x00erator", ReasonCode: "security"},
		{RelayActor: testRelayActor, ModeratorID: "opérator", ReasonCode: "security"},
		{RelayActor: testRelayActor, ModeratorID: strings.Repeat("x", storage.MaximumOperatorIDBytes+1), ReasonCode: "security"},
		{RelayActor: testRelayActor, ModeratorID: "operator", ReasonCode: ""},
		{RelayActor: testRelayActor, ModeratorID: "operator", ReasonCode: "Security"},
		{RelayActor: testRelayActor, ModeratorID: "operator", ReasonCode: "security note"},
		{RelayActor: testRelayActor, ModeratorID: "operator", ReasonCode: strings.Repeat("x", maximumReasonCodeBytes+1)},
	}
	for _, invalidIntent := range invalid {
		outcome, err := repository.Suspend(ctx, invalidIntent, time.Unix(110, 0))
		if outcome != "" || !errors.Is(err, storage.ErrTransitionInput) {
			t.Fatalf("Suspend(invalid) = (%q, %v), want ErrTransitionInput", outcome, err)
		}
		disclosedModerator := invalidIntent.ModeratorID != "" &&
			strings.Contains(err.Error(), invalidIntent.ModeratorID)
		disclosedReason := invalidIntent.ReasonCode != "" &&
			strings.Contains(err.Error(), invalidIntent.ReasonCode)
		if err != nil && (disclosedModerator || disclosedReason) {
			t.Fatalf("invalid moderation error disclosed metadata: %v", err)
		}
	}
	assertModerationOutcome(t, moderationResultOf(repository.Suspend(
		ctx,
		storage.ModerationIntent{
			RelayActor:  testRelayActor,
			ModeratorID: strings.Repeat("x", storage.MaximumOperatorIDBytes),
			ReasonCode:  strings.Repeat("x", maximumReasonCodeBytes),
		},
		time.Unix(110, 0),
	)), storage.ModerationSuspended)
	if _, err := repository.Suspend(ctx, intent, time.Unix(-1, 0)); !errors.Is(err, storage.ErrTransitionTime) {
		t.Fatalf("Suspend(pre-epoch) error = %v, want ErrTransitionTime", err)
	}
	if _, err := repository.Suspend(ctx, intent, time.Unix(99, 0)); !errors.Is(err, storage.ErrTransitionTime) {
		t.Fatalf("Suspend(regressing) error = %v, want ErrTransitionTime", err)
	}
}

func TestModerationEventTimeConstrainsLaterLifecycleTransitions(t *testing.T) {
	database := openMigratedTestDatabase(t)
	repository := newTestRelayRepository(t, database)
	ctx := context.Background()
	assertOutcome(t, transitionResultOf(repository.Register(
		ctx,
		storage.RegisterIntent{RelayActor: testRelayActor, PublicBaseURL: testPublicBase},
		time.Unix(100, 0),
	)), v1.OutcomeCreated)
	assertModerationOutcome(t, moderationResultOf(repository.Restore(
		ctx,
		testModerationIntent(),
		time.Unix(200, 0),
	)), storage.ModerationAlreadyActive)

	if _, err := repository.Heartbeat(
		ctx,
		storage.IdentityIntent{RelayActor: testRelayActor},
		time.Unix(199, 0),
	); !errors.Is(err, storage.ErrTransitionTime) {
		t.Fatalf("Heartbeat(before moderation event) error = %v, want ErrTransitionTime", err)
	}
	assertOutcome(t, transitionResultOf(repository.Heartbeat(
		ctx,
		storage.IdentityIntent{RelayActor: testRelayActor},
		time.Unix(200, 0),
	)), v1.OutcomeRecorded)
}

func TestModerationRepositoryRollsBackStateWhenAuditInsertFails(t *testing.T) {
	database := openMigratedTestDatabase(t)
	repository := newTestRelayRepository(t, database)
	ctx := context.Background()
	assertOutcome(t, transitionResultOf(repository.Register(
		ctx,
		storage.RegisterIntent{RelayActor: testRelayActor, PublicBaseURL: testPublicBase},
		time.Unix(100, 0),
	)), v1.OutcomeCreated)
	if _, err := database.Exec(`
		CREATE TRIGGER moderation_events_reject_test
		BEFORE INSERT ON moderation_events
		BEGIN
		    SELECT RAISE(ABORT, 'private moderation failure');
		END;
	`); err != nil {
		t.Fatalf("create moderation rejection trigger: %v", err)
	}

	outcome, err := repository.Suspend(
		ctx,
		testModerationIntent(),
		time.Unix(110, 0),
	)
	if outcome != "" || !errors.Is(err, storage.ErrStorageFailure) {
		t.Fatalf("Suspend() = (%q, %v), want ErrStorageFailure", outcome, err)
	}
	relay := readTestRelay(t, database, testRelayActor)
	if relay.administrativeState != administrativeActive ||
		relay.updatedAtUnix != 100 || relay.suspendedAt.Valid {
		t.Fatalf("relay changed after moderation rollback: %#v", relay)
	}
	if events := readTestModerationEvents(t, database, testRelayActor); len(events) != 0 {
		t.Fatalf("moderation rollback left events: %#v", events)
	}
}

func TestModerationRepositorySerializesConcurrentIdempotentDecisions(t *testing.T) {
	database := openMigratedTestDatabase(t)
	repository := newTestRelayRepository(t, database)
	ctx := context.Background()
	assertOutcome(t, transitionResultOf(repository.Register(
		ctx,
		storage.RegisterIntent{RelayActor: testRelayActor, PublicBaseURL: testPublicBase},
		time.Unix(100, 0),
	)), v1.OutcomeCreated)

	const callers = 16
	run := func(
		operation func() (storage.ModerationOutcome, error),
		changed storage.ModerationOutcome,
		unchanged storage.ModerationOutcome,
	) {
		t.Helper()
		results := make(chan moderationResult, callers)
		var wait sync.WaitGroup
		for range callers {
			wait.Add(1)
			go func() {
				defer wait.Done()
				results <- moderationResultOf(operation())
			}()
		}
		wait.Wait()
		close(results)
		changedCount := 0
		unchangedCount := 0
		for result := range results {
			if result.err != nil {
				t.Fatalf("concurrent moderation error = %v", result.err)
			}
			switch result.outcome {
			case changed:
				changedCount++
			case unchanged:
				unchangedCount++
			default:
				t.Fatalf("concurrent moderation outcome = %q", result.outcome)
			}
		}
		if changedCount != 1 || unchangedCount != callers-1 {
			t.Fatalf("outcomes = changed:%d unchanged:%d", changedCount, unchangedCount)
		}
	}
	intent := testModerationIntent()
	run(
		func() (storage.ModerationOutcome, error) {
			return repository.Suspend(ctx, intent, time.Unix(110, 0))
		},
		storage.ModerationSuspended,
		storage.ModerationAlreadySuspended,
	)
	run(
		func() (storage.ModerationOutcome, error) {
			return repository.Restore(ctx, intent, time.Unix(120, 0))
		},
		storage.ModerationRestored,
		storage.ModerationAlreadyActive,
	)
	relay := readTestRelay(t, database, testRelayActor)
	if relay.administrativeState != administrativeActive ||
		relay.updatedAtUnix != 120 || relay.suspendedAt.Valid {
		t.Fatalf("relay after concurrent moderation = %#v", relay)
	}
	if events := readTestModerationEvents(t, database, testRelayActor); len(events) != callers*2 {
		t.Fatalf("moderation event count = %d, want %d", len(events), callers*2)
	}
}

func TestModerationRepositoryConfigurationAndCanceledContext(t *testing.T) {
	var nilRepository *RelayRepository
	if _, err := nilRepository.Suspend(
		context.Background(),
		testModerationIntent(),
		time.Unix(100, 0),
	); !errors.Is(err, storage.ErrRepositoryConfiguration) {
		t.Fatalf("nil repository Suspend() error = %v", err)
	}
	database := openMigratedTestDatabase(t)
	repository := newTestRelayRepository(t, database)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := repository.Restore(
		ctx,
		testModerationIntent(),
		time.Unix(100, 0),
	); !errors.Is(err, storage.ErrStorageFailure) || !errors.Is(err, context.Canceled) {
		t.Fatalf("Restore(canceled) error = %v", err)
	}
}

func TestModerationOutcomeVocabulary(t *testing.T) {
	for _, outcome := range []storage.ModerationOutcome{
		storage.ModerationSuspended,
		storage.ModerationAlreadySuspended,
		storage.ModerationRestored,
		storage.ModerationAlreadyActive,
	} {
		if !outcome.Valid() {
			t.Fatalf("outcome %q is not valid", outcome)
		}
	}
	if storage.ModerationOutcome("other").Valid() {
		t.Fatal("unknown moderation outcome is valid")
	}
}

type moderationResult struct {
	outcome storage.ModerationOutcome
	err     error
}

type testModerationEvent struct {
	action      string
	moderatorID string
	reasonCode  string
	recordedAt  int64
}

func moderationResultOf(
	outcome storage.ModerationOutcome,
	err error,
) moderationResult {
	return moderationResult{outcome: outcome, err: err}
}

func assertModerationOutcome(
	t *testing.T,
	result moderationResult,
	want storage.ModerationOutcome,
) {
	t.Helper()
	if result.err != nil || result.outcome != want {
		t.Fatalf(
			"moderation transition = (%q, %v), want %q",
			result.outcome,
			result.err,
			want,
		)
	}
}

func readTestModerationEvents(
	t *testing.T,
	database *sql.DB,
	relayActor string,
) []testModerationEvent {
	t.Helper()
	rows, err := database.Query(
		`SELECT action, moderator_id, reason_code, recorded_at_unix
		 FROM moderation_events
		 WHERE relay_actor = ?
		 ORDER BY moderation_event_id`,
		relayActor,
	)
	if err != nil {
		t.Fatalf("query moderation events: %v", err)
	}
	defer rows.Close()

	var events []testModerationEvent
	for rows.Next() {
		var event testModerationEvent
		if err := rows.Scan(
			&event.action,
			&event.moderatorID,
			&event.reasonCode,
			&event.recordedAt,
		); err != nil {
			t.Fatalf("scan moderation event: %v", err)
		}
		events = append(events, event)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("iterate moderation events: %v", err)
	}
	return events
}

func equalModerationEvents(left, right []testModerationEvent) bool {
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
