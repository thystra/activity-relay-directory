package sqlite

import (
	"context"
	"testing"
)

func TestMigrateVersionNineAddsProfilesWithoutChangingRetainedRelay(t *testing.T) {
	database := openTestDatabase(t)
	migrations, err := loadMigrations()
	if err != nil {
		t.Fatalf("loadMigrations() error = %v", err)
	}
	// Later schema additions must not invalidate the historical v9 upgrade test.
	if len(migrations) < 12 || migrations[9].version != 10 || migrations[9].name != "relay_profiles" ||
		migrations[10].version != 11 || migrations[10].name != "relay_telemetry" ||
		migrations[11].version != 12 || migrations[11].name != "participating_relay_telemetry" {
		t.Fatalf("profile migration sequence does not contain the expected versions 10-12 (count: %d)", len(migrations))
	}
	if _, err := database.Exec(migrationTableSQL); err != nil {
		t.Fatalf("create migration table: %v", err)
	}
	for _, migration := range migrations[:9] {
		if _, err := database.Exec(migration.sql); err != nil {
			t.Fatalf("apply version %d schema: %v", migration.version, err)
		}
		if _, err := database.Exec(
			`INSERT INTO schema_migrations
			    (version, name, sha256, applied_at_unix)
			 VALUES (?, ?, ?, 0)`,
			migration.version,
			migration.name,
			migration.sha256,
		); err != nil {
			t.Fatalf("record version %d migration: %v", migration.version, err)
		}
	}

	heartbeat := int64(105)
	insertRelay(
		t,
		database,
		testRelayActor,
		lifecycleRegistered,
		administrativeActive,
		100,
		105,
		&heartbeat,
		nil,
		nil,
		true,
	)
	if err := Migrate(context.Background(), database); err != nil {
		t.Fatalf("Migrate(version 9) error = %v", err)
	}
	if err := CheckReady(context.Background(), database); err != nil {
		t.Fatalf("CheckReady(upgraded) error = %v", err)
	}

	relay := readTestRelay(t, database, testRelayActor)
	if relay.lifecycleState != lifecycleRegistered || relay.administrativeState != administrativeActive ||
		relay.updatedAtUnix != 105 || relay.lastSeenAtUnix != 105 ||
		!relay.lastHeartbeat.Valid || relay.lastHeartbeat.Int64 != 105 {
		t.Fatalf("profile-to-current migration changed relay state: %#v", relay)
	}
	for _, table := range []string{"relay_profile_values", "relay_profile_events", "relay_telemetry"} {
		assertTableExists(t, database, table)
		var count int
		if err := database.QueryRow(`SELECT COUNT(*) FROM ` + table).Scan(&count); err != nil {
			t.Fatalf("count %s: %v", table, err)
		}
		if count != 0 {
			t.Fatalf("%s count = %d, want 0", table, count)
		}
	}
}

func TestProfileSchemaRejectsCurrentAssertionWithoutRetainedIdentity(t *testing.T) {
	database := openMigratedTestDatabase(t)
	if _, err := database.Exec(
		`INSERT INTO relay_profile_values (
		    relay_actor, source_kind, field_name, value_json,
		    accepted_at_unix, revision
		) VALUES (?, 'relay', 'relay_type', '"general"', 100, 1)`,
		testRelayActor,
	); err == nil {
		t.Fatal("profile assertion without retained identity was accepted")
	}
}

func TestProfileSchemaRejectsMalformedStoredValuesAndProvenance(t *testing.T) {
	database := openMigratedTestDatabase(t)
	insertRelay(t, database, testRelayActor, lifecycleRegistered, administrativeActive, 100, 100, nil, nil, nil, true)

	for name, statement := range map[string]string{
		"malformed json": `INSERT INTO relay_profile_values (
			relay_actor, source_kind, field_name, value_json, accepted_at_unix, revision
		) VALUES ('https://relay.example/actor', 'relay', 'notes', 'not-json', 100, 1)`,
		"array scalar": `INSERT INTO relay_profile_values (
			relay_actor, source_kind, field_name, value_json, accepted_at_unix, revision
		) VALUES ('https://relay.example/actor', 'relay', 'notes', '["wrong"]', 100, 1)`,
		"scalar list": `INSERT INTO relay_profile_values (
			relay_actor, source_kind, field_name, value_json, accepted_at_unix, revision
		) VALUES ('https://relay.example/actor', 'relay', 'topics', '"wrong"', 100, 1)`,
		"non https provenance": `INSERT INTO relay_profile_values (
			relay_actor, source_kind, field_name, value_json, source_label, source_url,
			accepted_at_unix, revision
		) VALUES ('https://relay.example/actor', 'csv', 'notes', '"note"', 'community',
			'http://example.org/list.csv', 100, 1)`,
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := database.Exec(statement); err == nil {
				t.Fatal("invalid stored profile value was accepted")
			}
		})
	}
}
