package sqlite

import (
	"bytes"
	"context"
	"database/sql"
	"strings"
	"testing"
)

func TestDiscoveryReachabilityMigrationUpgradesVersionSevenPreservingRetentionIdentityAndAudit(t *testing.T) {
	database := openTestDatabase(t)
	applyMigrationsThrough(t, database, 7)

	insertRelay(
		t, database, testRelayActor, lifecycleRegistered, administrativeActive,
		100, 110, int64Pointer(110), nil, nil, true,
	)
	if _, err := database.Exec(`INSERT INTO retention_runs (
		policy_version,retention_days,observed_at_unix,cutoff_at_unix,
		candidates_scanned,purged_relays,purged_lifecycle_events,skipped,batches,
		outcome,backup_sha256,started_at_unix,finished_at_unix
	) VALUES (1,1,86500,100,1,1,2,0,1,'completed',?,86500,86501)`, strings.Repeat("a", 64)); err != nil {
		t.Fatalf("insert version-1 retention audit: %v", err)
	}
	var identityBefore []byte
	if err := database.QueryRow(`SELECT database_identity FROM retention_metadata WHERE singleton=1`).Scan(&identityBefore); err != nil {
		t.Fatal(err)
	}

	if err := Migrate(context.Background(), database); err != nil {
		t.Fatalf("Migrate(version 7) error = %v", err)
	}
	version, err := SchemaVersion(context.Background(), database)
	if err != nil || version != 8 {
		t.Fatalf("SchemaVersion() = (%d, %v), want (8, nil)", version, err)
	}
	var identityAfter []byte
	var policy int
	if err := database.QueryRow(`SELECT database_identity,policy_version FROM retention_metadata WHERE singleton=1`).Scan(&identityAfter, &policy); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(identityBefore, identityAfter) || policy != 2 {
		t.Fatalf("retention metadata identity_preserved=%t policy=%d", bytes.Equal(identityBefore, identityAfter), policy)
	}

	var historicalPolicy, discoveries, observations int
	if err := database.QueryRow(`SELECT policy_version,purged_discoveries,purged_observations FROM retention_runs WHERE retention_run_id=1`).Scan(
		&historicalPolicy, &discoveries, &observations,
	); err != nil {
		t.Fatal(err)
	}
	if historicalPolicy != 1 || discoveries != 0 || observations != 0 {
		t.Fatalf("historical retention audit = policy:%d discoveries:%d observations:%d", historicalPolicy, discoveries, observations)
	}

	var actorState string
	var actorChecked, actorSuccess sql.NullInt64
	var rfcVerified, updated, revision int64
	if err := database.QueryRow(`SELECT actor_state,actor_last_checked_at_unix,actor_last_success_at_unix,
		rfc9421_verified_at_unix,updated_at_unix,revision FROM relay_observations WHERE relay_actor=?`, testRelayActor).Scan(
		&actorState, &actorChecked, &actorSuccess, &rfcVerified, &updated, &revision,
	); err != nil {
		t.Fatal(err)
	}
	if actorState != "unknown" || actorChecked.Valid || actorSuccess.Valid || rfcVerified != 110 || updated != 110 || revision != 1 {
		t.Fatalf("backfilled observation = state:%s checked:%v success:%v rfc:%d updated:%d revision:%d", actorState, actorChecked, actorSuccess, rfcVerified, updated, revision)
	}
	for _, table := range []string{"relay_discoveries", "discovery_events", "relay_observations"} {
		assertTableExists(t, database, table)
	}
	var quick string
	if err := database.QueryRow(`PRAGMA quick_check`).Scan(&quick); err != nil || quick != "ok" {
		t.Fatalf("quick_check = %q, %v", quick, err)
	}
}

func TestDiscoveryReachabilityMigrationRollsBackCompletelyOnLateConflict(t *testing.T) {
	database := openTestDatabase(t)
	applyMigrationsThrough(t, database, 7)
	var identityBefore []byte
	if err := database.QueryRow(`SELECT database_identity FROM retention_metadata WHERE singleton=1`).Scan(&identityBefore); err != nil {
		t.Fatal(err)
	}
	if _, err := database.Exec(`CREATE INDEX relay_observations_updated_idx ON relays(updated_at_unix)`); err != nil {
		t.Fatal(err)
	}
	if err := Migrate(context.Background(), database); err == nil {
		t.Fatal("Migrate() error = nil, want forced migration-8 conflict")
	}
	version, err := SchemaVersion(context.Background(), database)
	if err != nil || version != 7 {
		t.Fatalf("SchemaVersion() after rollback = (%d, %v), want (7, nil)", version, err)
	}
	for _, table := range []string{"relay_discoveries", "discovery_events", "relay_observations"} {
		var count int
		if err := database.QueryRow(`SELECT COUNT(*) FROM sqlite_schema WHERE type='table' AND name=?`, table).Scan(&count); err != nil {
			t.Fatal(err)
		}
		if count != 0 {
			t.Fatalf("rolled-back table %s count = %d", table, count)
		}
	}
	var identityAfter []byte
	var policy int
	if err := database.QueryRow(`SELECT database_identity,policy_version FROM retention_metadata WHERE singleton=1`).Scan(&identityAfter, &policy); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(identityBefore, identityAfter) || policy != 1 {
		t.Fatalf("rollback retention metadata identity_preserved=%t policy=%d", bytes.Equal(identityBefore, identityAfter), policy)
	}
}
