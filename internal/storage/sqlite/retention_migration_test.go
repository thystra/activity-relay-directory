package sqlite

import (
	"context"
	"testing"
	"time"

	v1 "github.com/thystra/activity-relay-directory/internal/protocol/v1"
	"github.com/thystra/activity-relay-directory/internal/storage"
)

func TestInactiveRetentionMigrationPreservesRowsAndAddsGuardedObjects(t *testing.T) {
	database := openTestDatabase(t)
	applyMigrationsThrough(t, database, 5)
	repository := newTestRelayRepository(t, database)
	assertOutcome(t, transitionResultOf(repository.Register(
		context.Background(),
		storage.RegisterIntent{RelayActor: testRelayActor, PublicBaseURL: testPublicBase},
		time.Unix(100, 0),
	)), v1.OutcomeCreated)
	assertOutcome(t, transitionResultOf(repository.Unregister(
		context.Background(),
		storage.IdentityIntent{RelayActor: testRelayActor},
		time.Unix(200, 0),
	)), v1.OutcomeRemoved)

	var relayBefore, eventsBefore int
	if err := database.QueryRow(`SELECT COUNT(*) FROM relays`).Scan(&relayBefore); err != nil {
		t.Fatal(err)
	}
	if err := database.QueryRow(`SELECT COUNT(*) FROM relay_events`).Scan(&eventsBefore); err != nil {
		t.Fatal(err)
	}

	if err := Migrate(context.Background(), database); err != nil {
		t.Fatalf("Migrate() error = %v", err)
	}

	var relayAfter, eventsAfter, identityBytes, indexCount, relayVersionIndexCount, moderationVersionIndexCount, runTableCount int
	if err := database.QueryRow(`SELECT COUNT(*) FROM relays`).Scan(&relayAfter); err != nil {
		t.Fatal(err)
	}
	if err := database.QueryRow(`SELECT COUNT(*) FROM relay_events`).Scan(&eventsAfter); err != nil {
		t.Fatal(err)
	}
	if err := database.QueryRow(`SELECT length(database_identity) FROM retention_metadata WHERE singleton = 1`).Scan(&identityBytes); err != nil {
		t.Fatal(err)
	}
	if err := database.QueryRow(`SELECT COUNT(*) FROM sqlite_schema WHERE type='index' AND name='relays_retention_candidates_idx'`).Scan(&indexCount); err != nil {
		t.Fatal(err)
	}
	if err := database.QueryRow(`SELECT COUNT(*) FROM sqlite_schema WHERE type='index' AND name='relay_events_retention_version_idx'`).Scan(&relayVersionIndexCount); err != nil {
		t.Fatal(err)
	}
	if err := database.QueryRow(`SELECT COUNT(*) FROM sqlite_schema WHERE type='index' AND name='moderation_events_retention_version_idx'`).Scan(&moderationVersionIndexCount); err != nil {
		t.Fatal(err)
	}
	if err := database.QueryRow(`SELECT COUNT(*) FROM sqlite_schema WHERE type='table' AND name='retention_runs'`).Scan(&runTableCount); err != nil {
		t.Fatal(err)
	}
	if relayAfter != relayBefore || eventsAfter != eventsBefore || identityBytes != 16 ||
		indexCount != 1 || relayVersionIndexCount != 1 || moderationVersionIndexCount != 1 || runTableCount != 1 {
		t.Fatalf("retention migration state = relays:%d/%d events:%d/%d identity:%d indexes:%d/%d/%d runs:%d",
			relayBefore, relayAfter, eventsBefore, eventsAfter, identityBytes,
			indexCount, relayVersionIndexCount, moderationVersionIndexCount, runTableCount)
	}

	if _, err := database.Exec(`DELETE FROM relay_events WHERE relay_actor = ?`, testRelayActor); err == nil {
		t.Fatal("relay event append-only delete unexpectedly succeeded after retention migration")
	}
	if _, err := database.Exec(`UPDATE retention_metadata SET policy_version=1 WHERE singleton=1`); err == nil {
		t.Fatal("retention metadata update unexpectedly succeeded")
	}
	if _, err := database.Exec(`DELETE FROM retention_metadata WHERE singleton=1`); err == nil {
		t.Fatal("retention metadata delete unexpectedly succeeded")
	}
}

func TestRetentionRunAuditAllowsOnlyMonotonicRunningCheckpointsThenBecomesImmutable(t *testing.T) {
	database := openMigratedTestDatabase(t)
	_, err := database.Exec(`INSERT INTO retention_runs (
		policy_version, retention_days, observed_at_unix, cutoff_at_unix,
		backup_sha256, started_at_unix
	) VALUES (1,365,40000000,8464000,?,40000000)`,
		"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
	)
	if err != nil {
		t.Fatalf("insert running retention audit: %v", err)
	}
	if _, err := database.Exec(`UPDATE retention_runs
		SET candidates_scanned=1,purged_relays=1,purged_lifecycle_events=2,batches=1
		WHERE retention_run_id=1`); err != nil {
		t.Fatalf("monotonic running checkpoint rejected: %v", err)
	}
	if _, err := database.Exec(`UPDATE retention_runs SET candidates_scanned=0 WHERE retention_run_id=1`); err == nil {
		t.Fatal("retention audit accepted regressing checkpoint")
	}
	if _, err := database.Exec(`UPDATE retention_runs SET retention_days=1 WHERE retention_run_id=1`); err == nil {
		t.Fatal("retention audit accepted policy mutation")
	}
	if _, err := database.Exec(`UPDATE retention_runs
		SET outcome='completed',finished_at_unix=40000000
		WHERE retention_run_id=1`); err != nil {
		t.Fatalf("retention audit finalization rejected: %v", err)
	}
	if _, err := database.Exec(`UPDATE retention_runs SET purged_lifecycle_events=3 WHERE retention_run_id=1`); err == nil {
		t.Fatal("final retention audit accepted an update")
	}
	if _, err := database.Exec(`DELETE FROM retention_runs WHERE retention_run_id=1`); err == nil {
		t.Fatal("retention audit delete unexpectedly succeeded")
	}
}

func TestInactiveRetentionMigrationRollsBackOnLateIndexConflict(t *testing.T) {
	database := openTestDatabase(t)
	applyMigrationsThrough(t, database, 5)
	if _, err := database.Exec(`CREATE INDEX relays_retention_candidates_idx ON relays (relay_actor)`); err != nil {
		t.Fatal(err)
	}
	if _, err := database.Exec(`INSERT INTO relays (
		relay_actor, public_base_url, lifecycle_state, administrative_state,
		first_registered_at_unix, updated_at_unix, last_seen_at_unix,
		unregistered_at_unix
	) VALUES ('https://rollback.example/actor','https://rollback.example','unregistered','active',1,10,5,10)`); err != nil {
		t.Fatal(err)
	}

	if err := Migrate(context.Background(), database); err == nil {
		t.Fatal("Migrate() error = nil, want retention-index conflict")
	}
	version, err := SchemaVersion(context.Background(), database)
	if err != nil || version != 5 {
		t.Fatalf("SchemaVersion() = (%d, %v), want (5, nil)", version, err)
	}
	for _, table := range []string{"retention_metadata", "retention_runs"} {
		var count int
		if err := database.QueryRow(`SELECT COUNT(*) FROM sqlite_schema WHERE type='table' AND name=?`, table).Scan(&count); err != nil {
			t.Fatal(err)
		}
		if count != 0 {
			t.Fatalf("%s survived failed migration", table)
		}
	}
	for _, index := range []string{"relay_events_retention_version_idx", "moderation_events_retention_version_idx"} {
		var count int
		if err := database.QueryRow(`SELECT COUNT(*) FROM sqlite_schema WHERE type='index' AND name=?`, index).Scan(&count); err != nil {
			t.Fatal(err)
		}
		if count != 0 {
			t.Fatalf("%s survived failed migration", index)
		}
	}
	var relayCount int
	if err := database.QueryRow(`SELECT COUNT(*) FROM relays WHERE relay_actor='https://rollback.example/actor'`).Scan(&relayCount); err != nil || relayCount != 1 {
		t.Fatalf("pre-migration relay count = %d, %v", relayCount, err)
	}
	var versionSixCount int
	if err := database.QueryRow(`SELECT COUNT(*) FROM schema_migrations WHERE version=6`).Scan(&versionSixCount); err != nil || versionSixCount != 0 {
		t.Fatalf("version 6 migration record count = %d, %v", versionSixCount, err)
	}
}
