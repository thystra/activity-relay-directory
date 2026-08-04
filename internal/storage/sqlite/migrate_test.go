package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"path/filepath"
	"sync"
	"testing"
)

func TestMigrateCreatesSchemaAndIsIdempotent(t *testing.T) {
	database := openTestDatabase(t)
	ctx := context.Background()

	version, err := SchemaVersion(ctx, database)
	if err != nil {
		t.Fatalf("SchemaVersion() before migration error = %v", err)
	}
	if version != 0 {
		t.Fatalf("SchemaVersion() before migration = %d, want 0", version)
	}

	if err := Migrate(ctx, database); err != nil {
		t.Fatalf("Migrate() error = %v", err)
	}
	if err := Migrate(ctx, database); err != nil {
		t.Fatalf("second Migrate() error = %v", err)
	}

	version, err = SchemaVersion(ctx, database)
	if err != nil {
		t.Fatalf("SchemaVersion() error = %v", err)
	}
	if version != CurrentSchemaVersion {
		t.Fatalf("SchemaVersion() = %d, want %d", version, CurrentSchemaVersion)
	}

	migrations, err := loadMigrations()
	if err != nil {
		t.Fatalf("loadMigrations() error = %v", err)
	}
	var count int
	var name, digest string
	if err := database.QueryRow(
		`SELECT COUNT(*), MIN(name), MIN(sha256) FROM schema_migrations`,
	).Scan(&count, &name, &digest); err != nil {
		t.Fatalf("read migration row: %v", err)
	}
	if count != 1 || name != migrations[0].name || digest != migrations[0].sha256 {
		t.Fatalf("migration row = (%d, %q, %q), want embedded migration", count, name, digest)
	}

	for _, table := range []string{
		"schema_migrations",
		"relays",
		"replay_reservations",
		"relay_events",
	} {
		assertTableExists(t, database, table)
	}
}

func TestMigrateSerializesConcurrentCallers(t *testing.T) {
	database := openTestDatabase(t)
	ctx := context.Background()

	const callers = 8
	errorsByCaller := make(chan error, callers)
	var wait sync.WaitGroup
	for range callers {
		wait.Add(1)
		go func() {
			defer wait.Done()
			errorsByCaller <- Migrate(ctx, database)
		}()
	}
	wait.Wait()
	close(errorsByCaller)

	for err := range errorsByCaller {
		if err != nil {
			t.Fatalf("concurrent Migrate() error = %v", err)
		}
	}
	var count int
	if err := database.QueryRow(`SELECT COUNT(*) FROM schema_migrations`).Scan(&count); err != nil {
		t.Fatalf("count migrations: %v", err)
	}
	if count != CurrentSchemaVersion {
		t.Fatalf("migration count = %d, want %d", count, CurrentSchemaVersion)
	}
}

func TestMigrateRejectsDriftAndFutureSchema(t *testing.T) {
	t.Run("drift", func(t *testing.T) {
		database := openMigratedTestDatabase(t)
		if _, err := database.Exec(
			`UPDATE schema_migrations SET sha256 = ? WHERE version = 1`,
			fmt.Sprintf("%064d", 0),
		); err != nil {
			t.Fatalf("alter migration history: %v", err)
		}
		if err := Migrate(context.Background(), database); !errors.Is(err, ErrMigrationDrift) {
			t.Fatalf("Migrate() error = %v, want ErrMigrationDrift", err)
		}
	})

	t.Run("future", func(t *testing.T) {
		database := openMigratedTestDatabase(t)
		if _, err := database.Exec(
			`INSERT INTO schema_migrations
			    (version, name, sha256, applied_at_unix)
			 VALUES (?, ?, ?, ?)`,
			CurrentSchemaVersion+1,
			"future",
			fmt.Sprintf("%064d", 1),
			0,
		); err != nil {
			t.Fatalf("insert future migration: %v", err)
		}
		if err := Migrate(context.Background(), database); !errors.Is(err, ErrMigrationTooNew) {
			t.Fatalf("Migrate() error = %v, want ErrMigrationTooNew", err)
		}
	})
}

func TestMigrateRollsBackFailedMigration(t *testing.T) {
	database := openTestDatabase(t)
	if _, err := database.Exec(`CREATE TABLE relays (conflict INTEGER)`); err != nil {
		t.Fatalf("create conflicting table: %v", err)
	}

	if err := Migrate(context.Background(), database); err == nil {
		t.Fatal("Migrate() error = nil, want failure")
	}

	var count int
	if err := database.QueryRow(
		`SELECT COUNT(*) FROM sqlite_schema
		 WHERE type = 'table' AND name = 'schema_migrations'`,
	).Scan(&count); err != nil {
		t.Fatalf("inspect migration table: %v", err)
	}
	if count != 0 {
		t.Fatal("failed migration left schema_migrations behind")
	}
}

func TestRelaySchemaEnforcesLifecycleAndAdministrativeState(t *testing.T) {
	database := openMigratedTestDatabase(t)

	insertRelay(t, database, "https://relay.example/actor", "registered", "active", 100, 100, nil, nil, nil, true)
	insertRelay(t, database, "https://relay.example/unknown", "unknown", "active", 100, 100, nil, nil, nil, false)
	insertRelay(t, database, "https://relay.example/missing-unregister", "unregistered", "active", 100, 100, nil, nil, nil, false)
	insertRelay(t, database, "https://relay.example/active-suspended", "registered", "active", 100, 120, nil, nil, int64Pointer(110), false)
	insertRelay(t, database, "https://relay.example/suspended", "registered", "suspended", 100, 120, nil, nil, int64Pointer(110), true)
	insertRelay(t, database, "https://relay.example/bad-time", "registered", "active", 100, 99, nil, nil, nil, false)
	insertRelay(t, database, "https://relay.example/unregistered", "unregistered", "active", 100, 130, int64Pointer(110), int64Pointer(130), nil, true)
}

func TestReplayReservationSchemaEnforcesOpaqueDigestAndAtomicUniqueness(t *testing.T) {
	database := openMigratedTestDatabase(t)
	key := make([]byte, 32)
	badWindowKey := make([]byte, 32)
	badWindowKey[len(badWindowKey)-1] = 1

	if _, err := database.Exec(
		`INSERT INTO replay_reservations
		    (replay_key, reserved_at_unix, expires_at_unix)
		 VALUES (?, ?, ?)`,
		key,
		100,
		200,
	); err != nil {
		t.Fatalf("insert replay reservation: %v", err)
	}
	if _, err := database.Exec(
		`INSERT INTO replay_reservations
		    (replay_key, reserved_at_unix, expires_at_unix)
		 VALUES (?, ?, ?)`,
		key,
		101,
		201,
	); err == nil {
		t.Fatal("duplicate replay key was accepted")
	}
	if _, err := database.Exec(
		`INSERT INTO replay_reservations
		    (replay_key, reserved_at_unix, expires_at_unix)
		 VALUES (?, ?, ?)`,
		make([]byte, 31),
		100,
		200,
	); err == nil {
		t.Fatal("31-byte replay key was accepted")
	}
	if _, err := database.Exec(
		`INSERT INTO replay_reservations
		    (replay_key, reserved_at_unix, expires_at_unix)
		 VALUES (?, ?, ?)`,
		badWindowKey,
		200,
		200,
	); err == nil {
		t.Fatal("nonpositive replay reservation window was accepted")
	}
}

func TestRelayEventsAreAppendOnlyAndIndependentOfCurrentRelayState(t *testing.T) {
	database := openMigratedTestDatabase(t)

	result, err := database.Exec(
		`INSERT INTO relay_events (relay_actor, event_kind, recorded_at_unix)
		 VALUES (?, ?, ?)`,
		"https://absent.example/actor",
		"unregister_absent",
		100,
	)
	if err != nil {
		t.Fatalf("insert absent-relay event: %v", err)
	}
	eventID, err := result.LastInsertId()
	if err != nil {
		t.Fatalf("LastInsertId() error = %v", err)
	}

	if _, err := database.Exec(
		`UPDATE relay_events SET recorded_at_unix = 101 WHERE event_id = ?`,
		eventID,
	); err == nil {
		t.Fatal("relay event update was accepted")
	}
	if _, err := database.Exec(
		`DELETE FROM relay_events WHERE event_id = ?`,
		eventID,
	); err == nil {
		t.Fatal("relay event deletion was accepted")
	}

	var count int
	if err := database.QueryRow(`SELECT COUNT(*) FROM relay_events`).Scan(&count); err != nil {
		t.Fatalf("count relay events: %v", err)
	}
	if count != 1 {
		t.Fatalf("relay event count = %d, want 1", count)
	}
}

func openTestDatabase(t *testing.T) *sql.DB {
	t.Helper()
	path := filepath.Join(t.TempDir(), "directory.sqlite")
	database, err := Open(context.Background(), path)
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	t.Cleanup(func() { _ = database.Close() })
	return database
}

func openMigratedTestDatabase(t *testing.T) *sql.DB {
	t.Helper()
	database := openTestDatabase(t)
	if err := Migrate(context.Background(), database); err != nil {
		t.Fatalf("Migrate() error = %v", err)
	}
	return database
}

func assertPragmaInteger(t *testing.T, database *sql.DB, name string, want int) {
	t.Helper()
	var got int
	if err := database.QueryRow(`PRAGMA ` + name).Scan(&got); err != nil {
		t.Fatalf("read PRAGMA %s: %v", name, err)
	}
	if got != want {
		t.Fatalf("PRAGMA %s = %d, want %d", name, got, want)
	}
}

func assertTableExists(t *testing.T, database *sql.DB, table string) {
	t.Helper()
	var count int
	if err := database.QueryRow(
		`SELECT COUNT(*) FROM sqlite_schema WHERE type = 'table' AND name = ?`,
		table,
	).Scan(&count); err != nil {
		t.Fatalf("inspect table %q: %v", table, err)
	}
	if count != 1 {
		t.Fatalf("table %q count = %d, want 1", table, count)
	}
}

func insertRelay(
	t *testing.T,
	database *sql.DB,
	actor, lifecycle, administrative string,
	firstRegisteredAt, updatedAt int64,
	lastHeartbeatAt, unregisteredAt, suspendedAt *int64,
	wantSuccess bool,
) {
	t.Helper()
	_, err := database.Exec(
		`INSERT INTO relays (
		    relay_actor,
		    public_base_url,
		    lifecycle_state,
		    administrative_state,
		    first_registered_at_unix,
		    updated_at_unix,
		    last_heartbeat_at_unix,
		    unregistered_at_unix,
		    suspended_at_unix
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		actor,
		"https://relay.example/",
		lifecycle,
		administrative,
		firstRegisteredAt,
		updatedAt,
		lastHeartbeatAt,
		unregisteredAt,
		suspendedAt,
	)
	if wantSuccess && err != nil {
		t.Fatalf("insert relay %q error = %v", actor, err)
	}
	if !wantSuccess && err == nil {
		t.Fatalf("insert relay %q succeeded, want constraint failure", actor)
	}
}

func int64Pointer(value int64) *int64 {
	return &value
}
