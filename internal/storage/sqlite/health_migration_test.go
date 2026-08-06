package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"testing"
)

func TestHealthMigrationCreatesColumnAndProjectionIndex(t *testing.T) {
	database := openMigratedTestDatabase(t)

	var columnCount int
	if err := database.QueryRow(
		`SELECT COUNT(*)
		 FROM pragma_table_info('relays')
		 WHERE name = 'last_seen_at_unix'
		   AND type = 'INTEGER'
		   AND "notnull" = 1`,
	).Scan(&columnCount); err != nil {
		t.Fatalf("inspect last_seen_at_unix: %v", err)
	}
	if columnCount != 1 {
		t.Fatalf("last_seen_at_unix column count = %d, want 1", columnCount)
	}

	var indexCount int
	if err := database.QueryRow(
		`SELECT COUNT(*)
		 FROM sqlite_schema
		 WHERE type = 'index'
		   AND name = 'relays_health_projection_idx'`,
	).Scan(&indexCount); err != nil {
		t.Fatalf("inspect health projection index: %v", err)
	}
	if indexCount != 1 {
		t.Fatalf("health projection index count = %d, want 1", indexCount)
	}

	if _, err := database.Exec(
		`INSERT INTO relays (
		    relay_actor,
		    public_base_url,
		    lifecycle_state,
		    administrative_state,
		    first_registered_at_unix,
		    updated_at_unix,
		    last_seen_at_unix
		) VALUES (?, ?, ?, ?, ?, ?, ?)`,
		"https://invalid.example/actor",
		"https://invalid.example/",
		lifecycleRegistered,
		administrativeActive,
		100,
		100,
		99,
	); err == nil {
		t.Fatal("last_seen_at_unix before first registration was accepted")
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
		    last_heartbeat_at_unix
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		"https://heartbeat-ahead.example/actor",
		"https://heartbeat-ahead.example/",
		lifecycleRegistered,
		administrativeActive,
		100,
		110,
		100,
		110,
	); err == nil {
		t.Fatal("last_heartbeat_at_unix after last_seen_at_unix was accepted")
	}
}

func TestHealthMigrationBackfillsDeterministicLastSeen(t *testing.T) {
	database := openTestDatabase(t)
	applyMigrationsThrough(t, database, 3)

	rows := []struct {
		actor            string
		lifecycle        string
		updatedAt        int64
		lastHeartbeat    *int64
		unregisteredAt   *int64
		events           []relayEventFixture
		wantLastSeenUnix int64
	}{
		{
			actor:     "https://created.example/actor",
			lifecycle: lifecycleRegistered,
			updatedAt: 100,
			events: []relayEventFixture{
				{kind: eventRegisterCreated, recordedUnix: 100},
			},
			wantLastSeenUnix: 100,
		},
		{
			actor:         "https://heartbeat.example/actor",
			lifecycle:     lifecycleRegistered,
			updatedAt:     110,
			lastHeartbeat: int64Pointer(110),
			events: []relayEventFixture{
				{kind: eventRegisterCreated, recordedUnix: 100},
				{kind: eventHeartbeatRecorded, recordedUnix: 110},
			},
			wantLastSeenUnix: 110,
		},
		{
			actor:         "https://unchanged.example/actor",
			lifecycle:     lifecycleRegistered,
			updatedAt:     110,
			lastHeartbeat: int64Pointer(110),
			events: []relayEventFixture{
				{kind: eventRegisterCreated, recordedUnix: 100},
				{kind: eventHeartbeatRecorded, recordedUnix: 110},
				{kind: eventRegisterUnchanged, recordedUnix: 120},
			},
			wantLastSeenUnix: 120,
		},
		{
			actor:          "https://unregistered.example/actor",
			lifecycle:      lifecycleUnregistered,
			updatedAt:      130,
			lastHeartbeat:  int64Pointer(110),
			unregisteredAt: int64Pointer(130),
			events: []relayEventFixture{
				{kind: eventRegisterCreated, recordedUnix: 100},
				{kind: eventHeartbeatRecorded, recordedUnix: 110},
				{kind: eventUnregisterRemoved, recordedUnix: 130},
			},
			wantLastSeenUnix: 110,
		},
		{
			actor:            "https://eventless.example/actor",
			lifecycle:        lifecycleRegistered,
			updatedAt:        100,
			wantLastSeenUnix: 100,
		},
	}

	for _, row := range rows {
		insertRelay(
			t,
			database,
			row.actor,
			row.lifecycle,
			administrativeActive,
			100,
			row.updatedAt,
			row.lastHeartbeat,
			row.unregisteredAt,
			nil,
			true,
		)
		for _, event := range row.events {
			if _, err := database.Exec(
				`INSERT INTO relay_events
				    (relay_actor, event_kind, recorded_at_unix)
				 VALUES (?, ?, ?)`,
				row.actor,
				event.kind,
				event.recordedUnix,
			); err != nil {
				t.Fatalf("insert relay event for %s: %v", row.actor, err)
			}
		}
	}

	if err := Migrate(context.Background(), database); err != nil {
		t.Fatalf("Migrate(version 3) error = %v", err)
	}

	for _, row := range rows {
		var lastSeen int64
		if err := database.QueryRow(
			`SELECT last_seen_at_unix FROM relays WHERE relay_actor = ?`,
			row.actor,
		).Scan(&lastSeen); err != nil {
			t.Fatalf("read last_seen_at_unix for %s: %v", row.actor, err)
		}
		if lastSeen != row.wantLastSeenUnix {
			t.Fatalf(
				"last_seen_at_unix for %s = %d, want %d",
				row.actor,
				lastSeen,
				row.wantLastSeenUnix,
			)
		}
	}
}

func TestHealthMigrationRollsBackOnSchemaConflict(t *testing.T) {
	database := openTestDatabase(t)
	applyMigrationsThrough(t, database, 3)

	insertRelay(
		t,
		database,
		testRelayActor,
		lifecycleRegistered,
		administrativeActive,
		100,
		100,
		nil,
		nil,
		nil,
		true,
	)
	if _, err := database.Exec(
		`CREATE TABLE health_index_conflict (value INTEGER);
		 CREATE INDEX relays_health_projection_idx
		     ON health_index_conflict (value);`,
	); err != nil {
		t.Fatalf("create late migration conflict: %v", err)
	}

	if err := Migrate(context.Background(), database); err == nil {
		t.Fatal("Migrate() error = nil, want health migration failure")
	}

	version, err := SchemaVersion(context.Background(), database)
	if err != nil {
		t.Fatalf("SchemaVersion() error = %v", err)
	}
	if version != 3 {
		t.Fatalf("schema version after rollback = %d, want 3", version)
	}

	var relayCount, columnCount, listingIndexCount, replacementTableCount int
	if err := database.QueryRow(
		`SELECT COUNT(*) FROM relays WHERE relay_actor = ?`,
		testRelayActor,
	).Scan(&relayCount); err != nil {
		t.Fatalf("count retained relay: %v", err)
	}
	if err := database.QueryRow(
		`SELECT COUNT(*)
		 FROM pragma_table_info('relays')
		 WHERE name = 'last_seen_at_unix'`,
	).Scan(&columnCount); err != nil {
		t.Fatalf("inspect rolled-back relay schema: %v", err)
	}
	if err := database.QueryRow(
		`SELECT COUNT(*)
		 FROM sqlite_schema
		 WHERE type = 'index'
		   AND name = 'relays_listing_state_idx'`,
	).Scan(&listingIndexCount); err != nil {
		t.Fatalf("inspect restored listing index: %v", err)
	}
	if err := database.QueryRow(
		`SELECT COUNT(*)
		 FROM sqlite_schema
		 WHERE type = 'table'
		   AND name = 'relays_health_v4'`,
	).Scan(&replacementTableCount); err != nil {
		t.Fatalf("inspect rolled-back replacement table: %v", err)
	}
	if relayCount != 1 || columnCount != 0 ||
		listingIndexCount != 1 || replacementTableCount != 0 {
		t.Fatalf(
			"rollback state = relay_count:%d last_seen_columns:%d listing_indexes:%d replacement_tables:%d",
			relayCount,
			columnCount,
			listingIndexCount,
			replacementTableCount,
		)
	}
}

type relayEventFixture struct {
	kind         string
	recordedUnix int64
}

func applyMigrationsThrough(t *testing.T, database *sql.DB, through int) {
	t.Helper()
	migrations, err := loadMigrations()
	if err != nil {
		t.Fatalf("loadMigrations() error = %v", err)
	}
	if through <= 0 || through > len(migrations) {
		t.Fatalf("invalid migration boundary %d", through)
	}
	if _, err := database.Exec(migrationTableSQL); err != nil {
		t.Fatalf("create migration table: %v", err)
	}
	for _, migration := range migrations[:through] {
		if _, err := database.Exec(migration.sql); err != nil {
			t.Fatalf("apply migration %d: %v", migration.version, err)
		}
		if _, err := database.Exec(
			`INSERT INTO schema_migrations
			    (version, name, sha256, applied_at_unix)
			 VALUES (?, ?, ?, 0)`,
			migration.version,
			migration.name,
			migration.sha256,
		); err != nil {
			t.Fatalf("record migration %d: %v", migration.version, err)
		}
	}
}

func TestHealthMigrationVersionTwoUpgradeIncludesClosedEnrollmentAndLastSeen(t *testing.T) {
	database := openTestDatabase(t)
	applyMigrationsThrough(t, database, 2)

	insertRelay(
		t,
		database,
		testRelayActor,
		lifecycleRegistered,
		administrativeActive,
		100,
		110,
		int64Pointer(110),
		nil,
		nil,
		true,
	)

	if err := Migrate(context.Background(), database); err != nil {
		t.Fatalf("Migrate(version 2) error = %v", err)
	}

	var enrollmentOpen int
	var lastSeen int64
	if err := database.QueryRow(
		`SELECT enrollment_open FROM directory_policy WHERE singleton = 1`,
	).Scan(&enrollmentOpen); err != nil {
		t.Fatalf("read upgraded enrollment policy: %v", err)
	}
	if err := database.QueryRow(
		`SELECT last_seen_at_unix FROM relays WHERE relay_actor = ?`,
		testRelayActor,
	).Scan(&lastSeen); err != nil {
		t.Fatalf("read upgraded last seen: %v", err)
	}
	if enrollmentOpen != 0 || lastSeen != 110 {
		t.Fatalf(
			"version 2 upgrade = enrollment:%d last_seen:%d",
			enrollmentOpen,
			lastSeen,
		)
	}
}

func TestHealthMigrationRejectsNilContextWithoutChangingVersion(t *testing.T) {
	database := openTestDatabase(t)
	applyMigrationsThrough(t, database, 3)

	if err := Migrate(nil, database); !errors.Is(err, ErrMigrationConfiguration) {
		t.Fatalf("Migrate(nil) error = %v, want ErrMigrationConfiguration", err)
	}
	version, err := SchemaVersion(context.Background(), database)
	if err != nil {
		t.Fatalf("SchemaVersion() error = %v", err)
	}
	if version != 3 {
		t.Fatalf("schema version = %d, want 3", version)
	}
}
