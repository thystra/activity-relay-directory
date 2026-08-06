package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"testing"
	"time"

	v1 "github.com/thystra/activity-relay-directory/internal/protocol/v1"
	"github.com/thystra/activity-relay-directory/internal/storage"
)

func TestRelayRepositoryRefreshesLastSeenOnAcceptedRegisterAndHeartbeat(t *testing.T) {
	database := openMigratedTestDatabase(t)
	repository := newTestRelayRepository(t, database)
	ctx := context.Background()
	register := storage.RegisterIntent{
		RelayActor:    testRelayActor,
		PublicBaseURL: testPublicBase,
	}

	assertOutcome(t, transitionResultOf(
		repository.Register(ctx, register, time.Unix(100, 0)),
	), v1.OutcomeCreated)
	assertLastSeen(t, database, testRelayActor, 100)

	assertOutcome(t, transitionResultOf(
		repository.Register(ctx, register, time.Unix(110, 0)),
	), v1.OutcomeUnchanged)
	assertLastSeen(t, database, testRelayActor, 110)

	assertOutcome(t, transitionResultOf(repository.Heartbeat(
		ctx,
		storage.IdentityIntent{RelayActor: testRelayActor},
		time.Unix(120, 0),
	)), v1.OutcomeRecorded)
	assertLastSeen(t, database, testRelayActor, 120)

	assertOutcome(t, transitionResultOf(repository.Unregister(
		ctx,
		storage.IdentityIntent{RelayActor: testRelayActor},
		time.Unix(130, 0),
	)), v1.OutcomeRemoved)
	assertLastSeen(t, database, testRelayActor, 120)

	assertOutcome(t, transitionResultOf(
		repository.Register(ctx, register, time.Unix(140, 0)),
	), v1.OutcomeUpdated)
	assertLastSeen(t, database, testRelayActor, 140)
}

func TestRelayRepositoryRejectsAcceptedTimeBeforeStoredLastSeen(t *testing.T) {
	database := openMigratedTestDatabase(t)
	repository := newTestRelayRepository(t, database)
	ctx := context.Background()
	register := storage.RegisterIntent{
		RelayActor:    testRelayActor,
		PublicBaseURL: testPublicBase,
	}

	assertOutcome(t, transitionResultOf(
		repository.Register(ctx, register, time.Unix(100, 0)),
	), v1.OutcomeCreated)
	if _, err := database.Exec(
		`UPDATE relays SET last_seen_at_unix = 130 WHERE relay_actor = ?`,
		testRelayActor,
	); err != nil {
		t.Fatalf("advance stored last seen: %v", err)
	}

	_, err := repository.Heartbeat(
		ctx,
		storage.IdentityIntent{RelayActor: testRelayActor},
		time.Unix(120, 0),
	)
	if !errors.Is(err, storage.ErrTransitionTime) {
		t.Fatalf("Heartbeat(regressing last seen) error = %v, want ErrTransitionTime", err)
	}
	assertLastSeen(t, database, testRelayActor, 130)
}

func assertLastSeen(
	t *testing.T,
	database *sql.DB,
	relayActor string,
	want int64,
) {
	t.Helper()
	var got int64
	if err := database.QueryRow(
		`SELECT last_seen_at_unix FROM relays WHERE relay_actor = ?`,
		relayActor,
	).Scan(&got); err != nil {
		t.Fatalf("read last_seen_at_unix: %v", err)
	}
	if got != want {
		t.Fatalf("last_seen_at_unix = %d, want %d", got, want)
	}
}
