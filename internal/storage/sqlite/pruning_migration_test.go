package sqlite

import (
	"context"
	"database/sql"
	"testing"
)

func TestSoftPruningMigrationAddsStateTimestampEventAndIndexes(t *testing.T) {
	database := openTestDatabase(t)
	applyMigrationsThrough(t, database, 4)

	suspendedAt := int64(105)
	insertRelay(
		t,
		database,
		testRelayActor,
		lifecycleRegistered,
		administrativeSuspended,
		100,
		110,
		int64Pointer(110),
		nil,
		&suspendedAt,
		true,
	)
	moderationResult, err := database.Exec(
		`INSERT INTO moderation_events
		    (relay_actor, action, moderator_id, reason_code, recorded_at_unix)
		 VALUES (?, ?, 'operator', 'security_review', ?)`,
		testRelayActor,
		moderationSuspendApplied,
		suspendedAt,
	)
	if err != nil {
		t.Fatalf("insert pre-migration moderation event: %v", err)
	}
	oldModerationID, err := moderationResult.LastInsertId()
	if err != nil {
		t.Fatalf("LastInsertId(moderation) error = %v", err)
	}

	result, err := database.Exec(
		`INSERT INTO relay_events
		    (relay_actor, event_kind, recorded_at_unix)
		 VALUES (?, ?, ?)`,
		testRelayActor,
		eventHeartbeatRecorded,
		110,
	)
	if err != nil {
		t.Fatalf("insert pre-migration relay event: %v", err)
	}
	oldEventID, err := result.LastInsertId()
	if err != nil {
		t.Fatalf("LastInsertId() error = %v", err)
	}

	if err := Migrate(context.Background(), database); err != nil {
		t.Fatalf("Migrate(version 5) error = %v", err)
	}
	version, err := SchemaVersion(context.Background(), database)
	if err != nil || version != 5 {
		t.Fatalf("SchemaVersion() = (%d, %v), want (5, nil)", version, err)
	}

	var columnCount, pruningIndexCount, healthIndexCount int
	if err := database.QueryRow(
		`SELECT COUNT(*)
		 FROM pragma_table_info('relays')
		 WHERE name = 'pruned_at_unix'
		   AND type = 'INTEGER'
		   AND "notnull" = 0`,
	).Scan(&columnCount); err != nil {
		t.Fatalf("inspect pruned_at_unix: %v", err)
	}
	if err := database.QueryRow(
		`SELECT COUNT(*) FROM sqlite_schema
		 WHERE type = 'index' AND name = 'relays_pruning_candidates_idx'`,
	).Scan(&pruningIndexCount); err != nil {
		t.Fatalf("inspect pruning index: %v", err)
	}
	if err := database.QueryRow(
		`SELECT COUNT(*) FROM sqlite_schema
		 WHERE type = 'index' AND name = 'relays_health_projection_idx'`,
	).Scan(&healthIndexCount); err != nil {
		t.Fatalf("inspect health index: %v", err)
	}
	if columnCount != 1 || pruningIndexCount != 1 || healthIndexCount != 1 {
		t.Fatalf(
			"migration objects = column:%d pruning_index:%d health_index:%d",
			columnCount,
			pruningIndexCount,
			healthIndexCount,
		)
	}

	var lifecycle, administrative string
	var prunedAt, migratedSuspendedAt sql.NullInt64
	if err := database.QueryRow(
		`SELECT lifecycle_state, administrative_state, pruned_at_unix, suspended_at_unix
		 FROM relays WHERE relay_actor = ?`,
		testRelayActor,
	).Scan(&lifecycle, &administrative, &prunedAt, &migratedSuspendedAt); err != nil {
		t.Fatalf("read migrated relay: %v", err)
	}
	if lifecycle != lifecycleRegistered || administrative != administrativeSuspended ||
		prunedAt.Valid || !migratedSuspendedAt.Valid || migratedSuspendedAt.Int64 != suspendedAt {
		t.Fatalf(
			"migrated relay = lifecycle:%q administrative:%q pruned:%v suspended:%v",
			lifecycle,
			administrative,
			prunedAt,
			migratedSuspendedAt,
		)
	}
	var retainedModerationID int64
	if err := database.QueryRow(
		`SELECT moderation_event_id FROM moderation_events
		 WHERE relay_actor = ? AND action = ?`,
		testRelayActor,
		moderationSuspendApplied,
	).Scan(&retainedModerationID); err != nil {
		t.Fatalf("read retained moderation event: %v", err)
	}
	if retainedModerationID != oldModerationID {
		t.Fatalf("retained moderation event id = %d, want %d", retainedModerationID, oldModerationID)
	}

	result, err = database.Exec(
		`INSERT INTO relay_events
		    (relay_actor, event_kind, recorded_at_unix)
		 VALUES (?, 'relay_pruned', ?)`,
		testRelayActor,
		120,
	)
	if err != nil {
		t.Fatalf("insert relay_pruned event: %v", err)
	}
	newEventID, err := result.LastInsertId()
	if err != nil {
		t.Fatalf("LastInsertId(new) error = %v", err)
	}
	if newEventID <= oldEventID {
		t.Fatalf("new event id = %d, old = %d", newEventID, oldEventID)
	}
	if _, err := database.Exec(
		`UPDATE relay_events SET recorded_at_unix = 121 WHERE event_id = ?`,
		newEventID,
	); err == nil {
		t.Fatal("migrated relay event update was accepted")
	}
}

func TestSoftPruningMigrationEnforcesStateRelationships(t *testing.T) {
	database := openMigratedTestDatabase(t)

	validSuspendedAt := int64(120)
	validPrunedAt := int64(200)
	insertPruningRelayWithPrunedTime(
		t,
		database,
		"https://valid-pruned.example/actor",
		administrativeSuspended,
		100,
		&validPrunedAt,
		&validSuspendedAt,
	)

	for name, statement := range map[string]string{
		"pruned without timestamp": `INSERT INTO relays (
			relay_actor, public_base_url, lifecycle_state, administrative_state,
			first_registered_at_unix, updated_at_unix, last_seen_at_unix
		) VALUES ('https://missing-pruned.example/actor', 'https://missing-pruned.example/',
			'pruned', 'active', 100, 200, 100)`,
		"registered with pruned timestamp": `INSERT INTO relays (
			relay_actor, public_base_url, lifecycle_state, administrative_state,
			first_registered_at_unix, updated_at_unix, last_seen_at_unix, pruned_at_unix
		) VALUES ('https://registered-pruned.example/actor', 'https://registered-pruned.example/',
			'registered', 'active', 100, 200, 100, 200)`,
		"pruned before last seen": `INSERT INTO relays (
			relay_actor, public_base_url, lifecycle_state, administrative_state,
			first_registered_at_unix, updated_at_unix, last_seen_at_unix, pruned_at_unix
		) VALUES ('https://early-pruned.example/actor', 'https://early-pruned.example/',
			'pruned', 'active', 100, 200, 190, 180)`,
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := database.Exec(statement); err == nil {
				t.Fatal("invalid pruning state was accepted")
			}
		})
	}
}

func TestSoftPruningMigrationRollsBackOnLateIndexConflict(t *testing.T) {
	database := openTestDatabase(t)
	applyMigrationsThrough(t, database, 4)
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
		`CREATE TABLE pruning_index_conflict (value INTEGER);
		 CREATE INDEX relays_pruning_candidates_idx
		     ON pruning_index_conflict (value);`,
	); err != nil {
		t.Fatalf("create late migration conflict: %v", err)
	}

	if err := Migrate(context.Background(), database); err == nil {
		t.Fatal("Migrate() error = nil, want soft-pruning migration failure")
	}
	version, err := SchemaVersion(context.Background(), database)
	if err != nil || version != 4 {
		t.Fatalf("schema version after rollback = (%d, %v), want (4, nil)", version, err)
	}

	var relayCount, prunedColumnCount, healthIndexCount, replacementRelayCount, replacementEventCount int
	queries := []struct {
		query string
		out   *int
	}{
		{`SELECT COUNT(*) FROM relays WHERE relay_actor = ?`, &relayCount},
		{`SELECT COUNT(*) FROM pragma_table_info('relays') WHERE name = 'pruned_at_unix'`, &prunedColumnCount},
		{`SELECT COUNT(*) FROM sqlite_schema WHERE type = 'index' AND name = 'relays_health_projection_idx'`, &healthIndexCount},
		{`SELECT COUNT(*) FROM sqlite_schema WHERE type = 'table' AND name = 'relays_pruning_v5'`, &replacementRelayCount},
		{`SELECT COUNT(*) FROM sqlite_schema WHERE type = 'table' AND name = 'relay_events_pruning_v5'`, &replacementEventCount},
	}
	for index, query := range queries {
		var err error
		if index == 0 {
			err = database.QueryRow(query.query, testRelayActor).Scan(query.out)
		} else {
			err = database.QueryRow(query.query).Scan(query.out)
		}
		if err != nil {
			t.Fatalf("rollback inspection %d: %v", index, err)
		}
	}
	if relayCount != 1 || prunedColumnCount != 0 || healthIndexCount != 1 ||
		replacementRelayCount != 0 || replacementEventCount != 0 {
		t.Fatalf(
			"rollback state = relay:%d pruned_column:%d health_index:%d relay_replacement:%d event_replacement:%d",
			relayCount,
			prunedColumnCount,
			healthIndexCount,
			replacementRelayCount,
			replacementEventCount,
		)
	}
}
