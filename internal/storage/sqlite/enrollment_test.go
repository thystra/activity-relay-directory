package sqlite

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	v1 "github.com/thystra/activity-relay-directory/internal/protocol/v1"
	"github.com/thystra/activity-relay-directory/internal/storage"
)

func TestEnrollmentDefaultsClosedAndRejectsNeverSeenRelay(t *testing.T) {
	database := openMigratedTestDatabase(t)
	repository, err := NewRelayRepository(database)
	if err != nil {
		t.Fatalf("NewRelayRepository() error = %v", err)
	}
	open, err := repository.EnrollmentOpen(context.Background())
	if err != nil || open {
		t.Fatalf("EnrollmentOpen() = (%v, %v), want closed", open, err)
	}
	outcome, err := repository.Register(
		context.Background(),
		storage.RegisterIntent{
			RelayActor:    testRelayActor,
			PublicBaseURL: testPublicBase,
		},
		time.Unix(100, 0),
	)
	if outcome != "" || !errors.Is(err, storage.ErrEnrollmentClosed) {
		t.Fatalf("Register() = (%q, %v), want ErrEnrollmentClosed", outcome, err)
	}
	for _, table := range []string{"relays", "relay_events"} {
		var count int
		if err := database.QueryRow(`SELECT COUNT(*) FROM ` + table).Scan(&count); err != nil {
			t.Fatalf("count %s: %v", table, err)
		}
		if count != 0 {
			t.Fatalf("%s count = %d, want 0", table, count)
		}
	}
}

func TestClosedEnrollmentPreservesAcceptedRelayLifecycle(t *testing.T) {
	database := openMigratedTestDatabase(t)
	repository, err := NewRelayRepository(database)
	if err != nil {
		t.Fatalf("NewRelayRepository() error = %v", err)
	}
	ctx := context.Background()
	assertEnrollmentOutcome(t, enrollmentResultOf(repository.SetEnrollment(
		ctx, true, storage.EnrollmentIntent{OperatorID: "operator"}, time.Unix(90, 0),
	)), storage.EnrollmentOpened)
	assertOutcome(t, transitionResultOf(repository.Register(
		ctx,
		storage.RegisterIntent{RelayActor: testRelayActor, PublicBaseURL: testPublicBase},
		time.Unix(100, 0),
	)), v1.OutcomeCreated)
	assertEnrollmentOutcome(t, enrollmentResultOf(repository.SetEnrollment(
		ctx, false, storage.EnrollmentIntent{OperatorID: "operator"}, time.Unix(110, 0),
	)), storage.EnrollmentClosed)
	assertOutcome(t, transitionResultOf(repository.Register(
		ctx,
		storage.RegisterIntent{RelayActor: testRelayActor, PublicBaseURL: testPublicBase},
		time.Unix(120, 0),
	)), v1.OutcomeUnchanged)
	assertOutcome(t, transitionResultOf(repository.Heartbeat(
		ctx,
		storage.IdentityIntent{RelayActor: testRelayActor},
		time.Unix(130, 0),
	)), v1.OutcomeRecorded)
	assertOutcome(t, transitionResultOf(repository.Unregister(
		ctx,
		storage.IdentityIntent{RelayActor: testRelayActor},
		time.Unix(140, 0),
	)), v1.OutcomeRemoved)
	assertOutcome(t, transitionResultOf(repository.Register(
		ctx,
		storage.RegisterIntent{RelayActor: testRelayActor, PublicBaseURL: testPublicBase},
		time.Unix(150, 0),
	)), v1.OutcomeUpdated)

	_, err = repository.Register(
		ctx,
		storage.RegisterIntent{
			RelayActor:    "https://new-relay.example/actor",
			PublicBaseURL: "https://new-relay.example",
		},
		time.Unix(160, 0),
	)
	if !errors.Is(err, storage.ErrEnrollmentClosed) {
		t.Fatalf("new relay Register() error = %v, want ErrEnrollmentClosed", err)
	}
}

func TestEnrollmentDecisionsAreIdempotentAuditedAndTransactional(t *testing.T) {
	database := openMigratedTestDatabase(t)
	repository, err := NewRelayRepository(database)
	if err != nil {
		t.Fatalf("NewRelayRepository() error = %v", err)
	}
	ctx := context.Background()
	assertEnrollmentOutcome(t, enrollmentResultOf(repository.SetEnrollment(
		ctx, false, storage.EnrollmentIntent{OperatorID: "operator"}, time.Unix(100, 0),
	)), storage.EnrollmentAlreadyClosed)
	assertEnrollmentOutcome(t, enrollmentResultOf(repository.SetEnrollment(
		ctx, true, storage.EnrollmentIntent{OperatorID: "operator"}, time.Unix(110, 0),
	)), storage.EnrollmentOpened)
	assertEnrollmentOutcome(t, enrollmentResultOf(repository.SetEnrollment(
		ctx, true, storage.EnrollmentIntent{OperatorID: "operator"}, time.Unix(120, 0),
	)), storage.EnrollmentAlreadyOpen)
	assertEnrollmentOutcome(t, enrollmentResultOf(repository.SetEnrollment(
		ctx, false, storage.EnrollmentIntent{OperatorID: "operator"}, time.Unix(130, 0),
	)), storage.EnrollmentClosed)
	if _, err := repository.SetEnrollment(
		ctx,
		true,
		storage.EnrollmentIntent{OperatorID: "operator"},
		time.Unix(129, 0),
	); !errors.Is(err, storage.ErrTransitionTime) {
		t.Fatalf("regressing SetEnrollment() error = %v", err)
	}

	rows, err := database.Query(
		`SELECT action, operator_id, recorded_at_unix
		 FROM enrollment_events ORDER BY enrollment_event_id`,
	)
	if err != nil {
		t.Fatalf("query enrollment events: %v", err)
	}
	defer rows.Close()
	wantActions := []string{
		enrollmentClosedUnchanged,
		enrollmentOpened,
		enrollmentOpenUnchanged,
		enrollmentClosed,
	}
	var actions []string
	for rows.Next() {
		var action, operatorID string
		var recordedAt int64
		if err := rows.Scan(&action, &operatorID, &recordedAt); err != nil {
			t.Fatalf("scan enrollment event: %v", err)
		}
		if operatorID != "operator" || recordedAt < 100 || recordedAt > 130 {
			t.Fatalf("enrollment event = (%q, %q, %d)", action, operatorID, recordedAt)
		}
		actions = append(actions, action)
	}
	if !equalStrings(actions, wantActions) {
		t.Fatalf("actions = %#v, want %#v", actions, wantActions)
	}
	if _, err := database.Exec(`
		CREATE TRIGGER enrollment_events_reject_test
		BEFORE INSERT ON enrollment_events
		BEGIN
		    SELECT RAISE(ABORT, 'test audit failure');
		END;
	`); err != nil {
		t.Fatalf("create rejecting trigger: %v", err)
	}
	_, err = repository.SetEnrollment(
		ctx, true, storage.EnrollmentIntent{OperatorID: "operator"}, time.Unix(140, 0),
	)
	if !errors.Is(err, storage.ErrStorageFailure) {
		t.Fatalf("SetEnrollment() error = %v, want ErrStorageFailure", err)
	}
	open, err := repository.EnrollmentOpen(ctx)
	if err != nil || open {
		t.Fatalf("EnrollmentOpen() after rollback = (%v, %v), want closed", open, err)
	}
}

func TestEnrollmentAndRegistrationSerialize(t *testing.T) {
	database := openMigratedTestDatabase(t)
	repository, err := NewRelayRepository(database)
	if err != nil {
		t.Fatalf("NewRelayRepository() error = %v", err)
	}
	ctx := context.Background()
	const callers = 16
	var wait sync.WaitGroup
	wait.Add(callers)
	results := make(chan error, callers)
	for index := range callers {
		go func(index int) {
			defer wait.Done()
			if index%2 == 0 {
				_, err := repository.SetEnrollment(
					ctx, true, storage.EnrollmentIntent{OperatorID: "operator"}, time.Unix(100, 0),
				)
				results <- err
				return
			}
			_, err := repository.Register(
				ctx,
				storage.RegisterIntent{RelayActor: testRelayActor, PublicBaseURL: testPublicBase},
				time.Unix(100, 0),
			)
			if errors.Is(err, storage.ErrEnrollmentClosed) {
				err = nil
			}
			results <- err
		}(index)
	}
	wait.Wait()
	close(results)
	for err := range results {
		if err != nil {
			t.Fatalf("concurrent operation error = %v", err)
		}
	}
}

type enrollmentResult struct {
	outcome storage.EnrollmentOutcome
	err     error
}

func enrollmentResultOf(
	outcome storage.EnrollmentOutcome,
	err error,
) enrollmentResult {
	return enrollmentResult{outcome: outcome, err: err}
}

func assertEnrollmentOutcome(
	t *testing.T,
	result enrollmentResult,
	want storage.EnrollmentOutcome,
) {
	t.Helper()
	if result.err != nil || result.outcome != want {
		t.Fatalf(
			"enrollment transition = (%q, %v), want %q",
			result.outcome,
			result.err,
			want,
		)
	}
}
