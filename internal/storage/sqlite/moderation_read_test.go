package sqlite

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	v1 "github.com/thystra/activity-relay-directory/internal/protocol/v1"
	"github.com/thystra/activity-relay-directory/internal/storage"
)

func TestModerationReadRepositoryReportsRetainedStateAcrossLifecycle(t *testing.T) {
	database := openMigratedTestDatabase(t)
	repository := newTestRelayRepository(t, database)
	ctx := context.Background()

	assertOutcome(t, transitionResultOf(repository.Register(
		ctx,
		storage.RegisterIntent{RelayActor: testRelayActor, PublicBaseURL: testPublicBase},
		time.Unix(100, 0),
	)), v1.OutcomeCreated)
	assertOutcome(t, transitionResultOf(repository.Heartbeat(
		ctx,
		storage.IdentityIntent{RelayActor: testRelayActor},
		time.Unix(105, 0),
	)), v1.OutcomeRecorded)
	assertModerationOutcome(t, moderationResultOf(repository.Suspend(
		ctx,
		testModerationIntent(),
		time.Unix(110, 0),
	)), storage.ModerationSuspended)

	state, err := repository.ModerationState(ctx, testRelayActor)
	if err != nil {
		t.Fatalf("ModerationState() error = %v", err)
	}
	if state.RelayActor != testRelayActor || state.PublicBaseURL != testPublicBase ||
		state.LifecycleState != storage.LifecycleRegistered ||
		state.AdministrativeState != storage.AdministrativeSuspended ||
		state.FirstRegisteredUnix != 100 || state.UpdatedUnix != 110 ||
		state.LastHeartbeatUnix == nil || *state.LastHeartbeatUnix != 105 ||
		state.UnregisteredUnix != nil || state.SuspendedUnix == nil ||
		*state.SuspendedUnix != 110 {
		t.Fatalf("suspended state = %#v", state)
	}

	assertOutcome(t, transitionResultOf(repository.Unregister(
		ctx,
		storage.IdentityIntent{RelayActor: testRelayActor},
		time.Unix(120, 0),
	)), v1.OutcomeRemoved)
	assertModerationOutcome(t, moderationResultOf(repository.Restore(
		ctx,
		testModerationIntent(),
		time.Unix(130, 0),
	)), storage.ModerationRestored)

	state, err = repository.ModerationState(ctx, testRelayActor)
	if err != nil {
		t.Fatalf("ModerationState(unregistered) error = %v", err)
	}
	if state.LifecycleState != storage.LifecycleUnregistered ||
		state.AdministrativeState != storage.AdministrativeActive ||
		state.UpdatedUnix != 130 || state.UnregisteredUnix == nil ||
		*state.UnregisteredUnix != 120 || state.SuspendedUnix != nil {
		t.Fatalf("restored unregistered state = %#v", state)
	}
}

func TestModerationAuditUsesBoundedIndexedKeysetPages(t *testing.T) {
	database := openMigratedTestDatabase(t)
	repository := newTestRelayRepository(t, database)
	ctx := context.Background()
	assertOutcome(t, transitionResultOf(repository.Register(
		ctx,
		storage.RegisterIntent{RelayActor: testRelayActor, PublicBaseURL: testPublicBase},
		time.Unix(100, 0),
	)), v1.OutcomeCreated)

	intent := testModerationIntent()
	operations := []func() (storage.ModerationOutcome, error){
		func() (storage.ModerationOutcome, error) {
			return repository.Suspend(ctx, intent, time.Unix(110, 0))
		},
		func() (storage.ModerationOutcome, error) {
			return repository.Suspend(ctx, intent, time.Unix(110, 0))
		},
		func() (storage.ModerationOutcome, error) {
			return repository.Restore(ctx, intent, time.Unix(120, 0))
		},
		func() (storage.ModerationOutcome, error) {
			return repository.Restore(ctx, intent, time.Unix(120, 0))
		},
	}
	for _, operation := range operations {
		if outcome, err := operation(); err != nil || !outcome.Valid() {
			t.Fatalf("moderation operation = (%q, %v)", outcome, err)
		}
	}

	first, err := repository.ModerationAudit(ctx, storage.ModerationAuditQuery{
		RelayActor: testRelayActor,
		Limit:      2,
	})
	if err != nil {
		t.Fatalf("ModerationAudit(first) error = %v", err)
	}
	if len(first.Events) != 2 || first.Next == (storage.ModerationAuditCursor{}) {
		t.Fatalf("first page = %#v", first)
	}
	if first.Events[0].Action != storage.ModerationActionSuspendApplied ||
		first.Events[1].Action != storage.ModerationActionSuspendUnchanged ||
		first.Events[0].RecordedUnix != 110 || first.Events[1].RecordedUnix != 110 ||
		first.Next.RecordedUnix != 110 || first.Next.EventID != first.Events[1].EventID {
		t.Fatalf("first page ordering = %#v", first)
	}

	second, err := repository.ModerationAudit(ctx, storage.ModerationAuditQuery{
		RelayActor: testRelayActor,
		After:      first.Next,
		Limit:      2,
	})
	if err != nil {
		t.Fatalf("ModerationAudit(second) error = %v", err)
	}
	if len(second.Events) != 2 || second.Next != (storage.ModerationAuditCursor{}) ||
		second.Events[0].Action != storage.ModerationActionRestoreApplied ||
		second.Events[1].Action != storage.ModerationActionRestoreUnchanged {
		t.Fatalf("second page = %#v", second)
	}
	for _, page := range []storage.ModerationAuditPage{first, second} {
		for _, event := range page.Events {
			if event.RelayActor != testRelayActor || event.ModeratorID != testModeratorID ||
				event.ReasonCode != testReasonCode || !event.Action.Valid() {
				t.Fatalf("audit event = %#v", event)
			}
		}
	}
}

func TestModerationReadRepositoryRejectsAbsentInvalidCanceledAndOversizedReads(t *testing.T) {
	database := openMigratedTestDatabase(t)
	repository := newTestRelayRepository(t, database)

	if _, err := repository.ModerationState(nil, testRelayActor); !errors.Is(err, storage.ErrRepositoryConfiguration) {
		t.Fatalf("ModerationState(nil context) error = %v", err)
	}
	if _, err := repository.ModerationAudit(nil, storage.ModerationAuditQuery{
		RelayActor: testRelayActor,
		Limit:      1,
	}); !errors.Is(err, storage.ErrRepositoryConfiguration) {
		t.Fatalf("ModerationAudit(nil context) error = %v", err)
	}

	if _, err := repository.ModerationState(context.Background(), testRelayActor); !errors.Is(err, storage.ErrRelayAbsent) {
		t.Fatalf("ModerationState(absent) error = %v", err)
	}
	if _, err := repository.ModerationAudit(context.Background(), storage.ModerationAuditQuery{
		RelayActor: testRelayActor,
		Limit:      1,
	}); !errors.Is(err, storage.ErrRelayAbsent) {
		t.Fatalf("ModerationAudit(absent) error = %v", err)
	}

	invalidQueries := []storage.ModerationAuditQuery{
		{},
		{RelayActor: "HTTPS://relay.example/actor", Limit: 1},
		{RelayActor: testRelayActor, Limit: 0},
		{RelayActor: testRelayActor, Limit: storage.MaximumModerationAuditPage + 1},
		{RelayActor: testRelayActor, After: storage.ModerationAuditCursor{RecordedUnix: 1}, Limit: 1},
		{RelayActor: testRelayActor, After: storage.ModerationAuditCursor{RecordedUnix: -1, EventID: 1}, Limit: 1},
	}
	for _, query := range invalidQueries {
		if _, err := repository.ModerationAudit(context.Background(), query); !errors.Is(err, storage.ErrAdministrativeReadInput) {
			t.Fatalf("ModerationAudit(%#v) error = %v", query, err)
		}
	}
	if _, err := repository.ModerationState(context.Background(), "HTTPS://relay.example/actor"); !errors.Is(err, storage.ErrAdministrativeReadInput) {
		t.Fatalf("ModerationState(invalid) error = %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := repository.ModerationState(ctx, testRelayActor); !errors.Is(err, storage.ErrStorageFailure) || !errors.Is(err, context.Canceled) {
		t.Fatalf("ModerationState(canceled) error = %v", err)
	}
}

func TestModerationReadsRemainBoundedDuringConcurrentDecisions(t *testing.T) {
	database := openMigratedTestDatabase(t)
	repository := newTestRelayRepository(t, database)
	ctx := context.Background()
	assertOutcome(t, transitionResultOf(repository.Register(
		ctx,
		storage.RegisterIntent{RelayActor: testRelayActor, PublicBaseURL: testPublicBase},
		time.Unix(100, 0),
	)), v1.OutcomeCreated)

	const decisions = 12
	var wait sync.WaitGroup
	for index := 0; index < decisions; index++ {
		wait.Add(1)
		go func(index int) {
			defer wait.Done()
			acceptedAt := time.Unix(int64(110+index), 0)
			if index%2 == 0 {
				_, _ = repository.Suspend(ctx, testModerationIntent(), acceptedAt)
			} else {
				_, _ = repository.Restore(ctx, testModerationIntent(), acceptedAt)
			}
		}(index)
	}

	for index := 0; index < decisions; index++ {
		state, err := repository.ModerationState(ctx, testRelayActor)
		if err != nil {
			t.Fatalf("concurrent ModerationState() error = %v", err)
		}
		if !state.AdministrativeState.Valid() {
			t.Fatalf("concurrent state = %#v", state)
		}
		page, err := repository.ModerationAudit(ctx, storage.ModerationAuditQuery{
			RelayActor: testRelayActor,
			Limit:      3,
		})
		if err != nil {
			t.Fatalf("concurrent ModerationAudit() error = %v", err)
		}
		if len(page.Events) > 3 {
			t.Fatalf("concurrent audit page length = %d", len(page.Events))
		}
	}
	wait.Wait()
}

func TestModerationReadVocabulary(t *testing.T) {
	for _, state := range []storage.RelayLifecycleState{
		storage.LifecycleRegistered,
		storage.LifecycleUnregistered,
		storage.LifecyclePruned,
	} {
		if !state.Valid() {
			t.Fatalf("lifecycle state %q is invalid", state)
		}
	}
	for _, state := range []storage.RelayAdministrativeState{
		storage.AdministrativeActive,
		storage.AdministrativeSuspended,
	} {
		if !state.Valid() {
			t.Fatalf("administrative state %q is invalid", state)
		}
	}
	for _, action := range []storage.ModerationAction{
		storage.ModerationActionSuspendApplied,
		storage.ModerationActionSuspendUnchanged,
		storage.ModerationActionRestoreApplied,
		storage.ModerationActionRestoreUnchanged,
	} {
		if !action.Valid() {
			t.Fatalf("moderation action %q is invalid", action)
		}
	}
}

func TestModerationAuditQueryUsesActorTimeIndex(t *testing.T) {
	database := openMigratedTestDatabase(t)
	rows, err := database.Query(
		`EXPLAIN QUERY PLAN
		 SELECT moderation_event_id,
		        relay_actor,
		        action,
		        moderator_id,
		        reason_code,
		        recorded_at_unix
		 FROM moderation_events
		 WHERE relay_actor = ?
		   AND (recorded_at_unix, moderation_event_id) > (?, ?)
		 ORDER BY recorded_at_unix, moderation_event_id
		 LIMIT ?`,
		testRelayActor,
		110,
		2,
		storage.MaximumModerationAuditPage+1,
	)
	if err != nil {
		t.Fatalf("EXPLAIN QUERY PLAN error = %v", err)
	}
	defer rows.Close()
	var details []string
	for rows.Next() {
		var id, parent, unused int
		var detail string
		if err := rows.Scan(&id, &parent, &unused, &detail); err != nil {
			t.Fatalf("scan query plan: %v", err)
		}
		details = append(details, detail)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("iterate query plan: %v", err)
	}
	joined := strings.Join(details, "\n")
	if !strings.Contains(joined, "moderation_events_actor_time_idx") {
		t.Fatalf("query plan does not use actor/time index:\n%s", joined)
	}
	if !strings.Contains(joined, "recorded_at_unix>?") {
		t.Fatalf("query plan does not use the indexed cursor range:\n%s", joined)
	}
}
