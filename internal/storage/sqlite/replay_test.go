package sqlite

import (
	"bytes"
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	v1 "github.com/thystra/activity-relay-directory/internal/protocol/v1"
)

func TestSQLiteRFC9421ReplayStoreReservesUntilExpiry(t *testing.T) {
	database := openMigratedTestDatabase(t)
	now := time.Date(2026, 8, 5, 0, 0, 0, 0, time.UTC)
	store := newTestRFC9421ReplayStore(t, database, &now, 4)
	key := v1.DeriveRFC9421ReplayKey("https://relay.example/actor#key", "nonce")
	expiresAt := now.Add(v1.RFC9421ReplayTTL)

	reserved, err := store.ReserveRFC9421Replay(context.Background(), key, expiresAt)
	if err != nil || !reserved {
		t.Fatalf("first reserve = %t, %v", reserved, err)
	}
	reserved, err = store.ReserveRFC9421Replay(context.Background(), key, expiresAt)
	if err != nil || reserved {
		t.Fatalf("duplicate reserve = %t, %v", reserved, err)
	}

	var storedKey []byte
	var reservedUnix, expiresUnix int64
	if err := database.QueryRow(
		`SELECT replay_key, reserved_at_unix, expires_at_unix
		 FROM replay_reservations
		 WHERE replay_key = ?`,
		key[:],
	).Scan(&storedKey, &reservedUnix, &expiresUnix); err != nil {
		t.Fatalf("read reservation: %v", err)
	}
	if !bytes.Equal(storedKey, key[:]) || reservedUnix != now.Unix() ||
		expiresUnix != expiresAt.Unix() {
		t.Fatalf(
			"stored reservation = (%x, %d, %d)",
			storedKey,
			reservedUnix,
			expiresUnix,
		)
	}

	now = expiresAt
	reserved, err = store.ReserveRFC9421Replay(
		context.Background(),
		key,
		now.Add(v1.RFC9421ReplayTTL),
	)
	if err != nil || !reserved {
		t.Fatalf("post-expiry reserve = %t, %v", reserved, err)
	}
}

func TestSQLiteRFC9421ReplayStorePersistsAcrossRestart(t *testing.T) {
	path := filepath.Join(t.TempDir(), "directory.sqlite")
	now := time.Date(2026, 8, 5, 0, 0, 0, 0, time.UTC)
	key := v1.DeriveRFC9421ReplayKey("key", "restart-nonce")
	expiresAt := now.Add(v1.RFC9421ReplayTTL)

	database, err := Open(context.Background(), path)
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	if err := Migrate(context.Background(), database); err != nil {
		_ = database.Close()
		t.Fatalf("Migrate() error = %v", err)
	}
	store := newTestRFC9421ReplayStore(t, database, &now, 4)
	if reserved, err := store.ReserveRFC9421Replay(
		context.Background(),
		key,
		expiresAt,
	); err != nil || !reserved {
		_ = database.Close()
		t.Fatalf("first-process reserve = %t, %v", reserved, err)
	}
	if err := database.Close(); err != nil {
		t.Fatalf("first Close() error = %v", err)
	}

	database, err = Open(context.Background(), path)
	if err != nil {
		t.Fatalf("second Open() error = %v", err)
	}
	t.Cleanup(func() { _ = database.Close() })
	if err := Migrate(context.Background(), database); err != nil {
		t.Fatalf("second Migrate() error = %v", err)
	}
	store = newTestRFC9421ReplayStore(t, database, &now, 4)
	if reserved, err := store.ReserveRFC9421Replay(
		context.Background(),
		key,
		expiresAt,
	); err != nil || reserved {
		t.Fatalf("second-process duplicate = %t, %v", reserved, err)
	}
}

func TestSQLiteRFC9421ReplayStoreIsAtomicAcrossConnections(t *testing.T) {
	path := filepath.Join(t.TempDir(), "directory.sqlite")
	now := time.Date(2026, 8, 5, 0, 0, 0, 0, time.UTC)
	database, err := Open(context.Background(), path)
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	if err := Migrate(context.Background(), database); err != nil {
		_ = database.Close()
		t.Fatalf("Migrate() error = %v", err)
	}
	databases := []*sql.DB{database}
	for range 3 {
		connection, err := Open(context.Background(), path)
		if err != nil {
			t.Fatalf("additional Open() error = %v", err)
		}
		databases = append(databases, connection)
	}
	t.Cleanup(func() {
		for _, connection := range databases {
			_ = connection.Close()
		}
	})

	stores := make([]*RFC9421ReplayStore, 0, len(databases))
	for _, connection := range databases {
		stores = append(stores, newTestRFC9421ReplayStore(t, connection, &now, 8))
	}
	key := v1.DeriveRFC9421ReplayKey("key", "shared-nonce")

	const attempts = 64
	var successes atomic.Int32
	var duplicates atomic.Int32
	var failures atomic.Int32
	var wait sync.WaitGroup
	wait.Add(attempts)
	for attempt := range attempts {
		go func(store *RFC9421ReplayStore) {
			defer wait.Done()
			reserved, err := store.ReserveRFC9421Replay(
				context.Background(),
				key,
				now.Add(v1.RFC9421ReplayTTL),
			)
			switch {
			case err != nil:
				failures.Add(1)
			case reserved:
				successes.Add(1)
			default:
				duplicates.Add(1)
			}
		}(stores[attempt%len(stores)])
	}
	wait.Wait()
	if successes.Load() != 1 || duplicates.Load() != attempts-1 ||
		failures.Load() != 0 {
		t.Fatalf(
			"reservations: successes=%d duplicates=%d failures=%d",
			successes.Load(),
			duplicates.Load(),
			failures.Load(),
		)
	}
}

func TestSQLiteRFC9421ReplayStoreCleansExpiredRowsInBoundedBatches(t *testing.T) {
	database := openMigratedTestDatabase(t)
	now := time.Date(2026, 8, 5, 0, 0, 0, 0, time.UTC)
	store := newTestRFC9421ReplayStore(t, database, &now, 3)

	for index := range 10 {
		insertTestReplayReservation(
			t,
			database,
			v1.DeriveRFC9421ReplayKey("expired", string(rune('a'+index))),
			now.Add(-2*time.Minute),
			now.Add(-time.Minute),
		)
	}
	futureKey := v1.DeriveRFC9421ReplayKey("future", "retained")
	insertTestReplayReservation(
		t,
		database,
		futureKey,
		now,
		now.Add(time.Minute),
	)

	newKey := v1.DeriveRFC9421ReplayKey("new", "reservation")
	if reserved, err := store.ReserveRFC9421Replay(
		context.Background(),
		newKey,
		now.Add(v1.RFC9421ReplayTTL),
	); err != nil || !reserved {
		t.Fatalf("reserve with cleanup = %t, %v", reserved, err)
	}
	if got := countExpiredTestReservations(t, database, now); got != 7 {
		t.Fatalf("expired rows after reserve = %d, want 7", got)
	}

	deleted, err := store.CleanupExpiredRFC9421Replay(context.Background(), 2)
	if err != nil || deleted != 2 {
		t.Fatalf("bounded cleanup = %d, %v", deleted, err)
	}
	if got := countExpiredTestReservations(t, database, now); got != 5 {
		t.Fatalf("expired rows after explicit cleanup = %d, want 5", got)
	}
	if !testReplayReservationExists(t, database, futureKey) ||
		!testReplayReservationExists(t, database, newKey) {
		t.Fatal("cleanup removed an unexpired reservation")
	}
}

func TestSQLiteRFC9421ReplayStoreReplacesItsExpiredKeyBeyondCleanupBatch(t *testing.T) {
	database := openMigratedTestDatabase(t)
	now := time.Date(2026, 8, 5, 0, 0, 0, 0, time.UTC)
	store := newTestRFC9421ReplayStore(t, database, &now, 2)
	for index := range 4 {
		insertTestReplayReservation(
			t,
			database,
			v1.DeriveRFC9421ReplayKey("older", string(rune('a'+index))),
			now.Add(-3*time.Minute),
			now.Add(-2*time.Minute),
		)
	}
	target := v1.DeriveRFC9421ReplayKey("target", "expired")
	insertTestReplayReservation(t, database, target, now.Add(-time.Minute), now.Add(-time.Second))

	reserved, err := store.ReserveRFC9421Replay(
		context.Background(),
		target,
		now.Add(time.Minute),
	)
	if err != nil || !reserved {
		t.Fatalf("expired-target reserve = %t, %v", reserved, err)
	}
	if got := countExpiredTestReservations(t, database, now); got != 2 {
		t.Fatalf("expired rows after targeted replacement = %d, want 2", got)
	}
}

func TestSQLiteRFC9421ReplayStoreRollsBackCleanupWhenReservationFails(t *testing.T) {
	database := openMigratedTestDatabase(t)
	now := time.Date(2026, 8, 5, 0, 0, 0, 0, time.UTC)
	store := newTestRFC9421ReplayStore(t, database, &now, 3)
	for index := range 3 {
		insertTestReplayReservation(
			t,
			database,
			v1.DeriveRFC9421ReplayKey("expired", string(rune('a'+index))),
			now.Add(-2*time.Minute),
			now.Add(-time.Minute),
		)
	}
	if _, err := database.Exec(`
		CREATE TRIGGER replay_reservations_reject_test
		BEFORE INSERT ON replay_reservations
		BEGIN
		    SELECT RAISE(ABORT, 'sensitive test failure');
		END;
	`); err != nil {
		t.Fatalf("create rejecting trigger: %v", err)
	}

	key := v1.DeriveRFC9421ReplayKey("new", "rejected")
	reserved, err := store.ReserveRFC9421Replay(
		context.Background(),
		key,
		now.Add(time.Minute),
	)
	if reserved || !errors.Is(err, v1.ErrRFC9421ReplayStore) {
		t.Fatalf("failed reserve = %t, %v", reserved, err)
	}
	if got := countExpiredTestReservations(t, database, now); got != 3 {
		t.Fatalf("expired rows after rollback = %d, want 3", got)
	}
	if testReplayReservationExists(t, database, key) {
		t.Fatal("failed reservation was persisted")
	}
}

func TestSQLiteRFC9421ReplayStoreRejectsInvalidUse(t *testing.T) {
	database := openMigratedTestDatabase(t)
	now := time.Date(2026, 8, 5, 0, 0, 0, 0, time.UTC)
	store := newTestRFC9421ReplayStore(t, database, &now, 4)
	key := v1.DeriveRFC9421ReplayKey("key", "nonce")

	for _, test := range []struct {
		name      string
		ctx       context.Context
		expiresAt time.Time
	}{
		{name: "nil context", expiresAt: now.Add(time.Minute)},
		{name: "expired", ctx: context.Background(), expiresAt: now},
		{name: "retention too long", ctx: context.Background(), expiresAt: now.Add(v1.RFC9421ReplayTTL + time.Second)},
	} {
		t.Run(test.name, func(t *testing.T) {
			reserved, err := store.ReserveRFC9421Replay(test.ctx, key, test.expiresAt)
			if reserved || !errors.Is(err, v1.ErrRFC9421ReplayStore) {
				t.Fatalf("ReserveRFC9421Replay() = %t, %v", reserved, err)
			}
		})
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if reserved, err := store.ReserveRFC9421Replay(
		ctx,
		key,
		now.Add(time.Minute),
	); reserved || !errors.Is(err, v1.ErrRFC9421ReplayStore) ||
		!errors.Is(err, context.Canceled) {
		t.Fatalf("canceled reserve = %t, %v", reserved, err)
	}
	for _, maximum := range []int{0, -1, maximumRFC9421ReplayCleanupBatch + 1} {
		if deleted, err := store.CleanupExpiredRFC9421Replay(
			context.Background(),
			maximum,
		); deleted != 0 || !errors.Is(err, v1.ErrRFC9421ReplayStore) {
			t.Fatalf("CleanupExpiredRFC9421Replay(%d) = %d, %v", maximum, deleted, err)
		}
	}
	if created, err := NewRFC9421ReplayStore(nil); created != nil ||
		!errors.Is(err, v1.ErrRFC9421ReplayStore) {
		t.Fatalf("NewRFC9421ReplayStore(nil) = (%v, %v)", created, err)
	}
	if created, err := newRFC9421ReplayStore(
		database,
		func() time.Time { return now },
		maximumRFC9421ReplayCleanupBatch+1,
	); created != nil || !errors.Is(err, v1.ErrRFC9421ReplayStore) {
		t.Fatalf("newRFC9421ReplayStore(oversized batch) = (%v, %v)", created, err)
	}

	if err := database.Close(); err != nil {
		t.Fatalf("Close() error = %v", err)
	}
	if reserved, err := store.ReserveRFC9421Replay(
		context.Background(),
		key,
		now.Add(time.Minute),
	); reserved || !errors.Is(err, v1.ErrRFC9421ReplayStore) {
		t.Fatalf("closed-database reserve = %t, %v", reserved, err)
	}
}

func newTestRFC9421ReplayStore(
	t *testing.T,
	database *sql.DB,
	now *time.Time,
	cleanupBatch int,
) *RFC9421ReplayStore {
	t.Helper()
	store, err := newRFC9421ReplayStore(
		database,
		func() time.Time { return *now },
		cleanupBatch,
	)
	if err != nil {
		t.Fatalf("newRFC9421ReplayStore() error = %v", err)
	}
	return store
}

func insertTestReplayReservation(
	t *testing.T,
	database *sql.DB,
	key v1.RFC9421ReplayKey,
	reservedAt time.Time,
	expiresAt time.Time,
) {
	t.Helper()
	if _, err := database.Exec(
		`INSERT INTO replay_reservations
		    (replay_key, reserved_at_unix, expires_at_unix)
		 VALUES (?, ?, ?)`,
		key[:],
		reservedAt.Unix(),
		expiresAt.Unix(),
	); err != nil {
		t.Fatalf("insert replay reservation: %v", err)
	}
}

func countExpiredTestReservations(
	t *testing.T,
	database *sql.DB,
	now time.Time,
) int {
	t.Helper()
	var count int
	if err := database.QueryRow(
		`SELECT COUNT(*)
		 FROM replay_reservations
		 WHERE expires_at_unix <= ?`,
		now.Unix(),
	).Scan(&count); err != nil {
		t.Fatalf("count expired replay reservations: %v", err)
	}
	return count
}

func testReplayReservationExists(
	t *testing.T,
	database *sql.DB,
	key v1.RFC9421ReplayKey,
) bool {
	t.Helper()
	var count int
	if err := database.QueryRow(
		`SELECT COUNT(*) FROM replay_reservations WHERE replay_key = ?`,
		key[:],
	).Scan(&count); err != nil {
		t.Fatalf("inspect replay reservation: %v", err)
	}
	return count == 1
}
