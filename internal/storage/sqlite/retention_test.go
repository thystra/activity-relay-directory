package sqlite

import (
	"bytes"
	"context"
	"database/sql"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/thystra/activity-relay-directory/internal/storage"
)

func TestPurgeCandidatesUseInactiveTransitionAndExcludeRegisteredSuspended(t *testing.T) {
	database := openMigratedTestDatabase(t)
	repository := newTestRelayRepository(t, database)
	insertRetentionRelay(t, database, "https://a.example/actor", lifecycleUnregistered, administrativeActive, 100, 90)
	insertRetentionRelay(t, database, "https://b.example/actor", lifecyclePruned, administrativeActive, 100, 80)
	insertRetentionRelay(t, database, "https://c.example/actor", lifecycleUnregistered, administrativeSuspended, 50, 40)
	insertRetentionRelay(t, database, "https://d.example/actor", lifecycleRegistered, administrativeActive, 0, 10)
	insertRetentionRelay(t, database, "https://e.example/actor", lifecyclePruned, administrativeActive, 101, 80)

	page, err := repository.PurgeCandidates(context.Background(), storage.PurgeCandidateQuery{
		Limit: 2, CutoffAt: time.Unix(100, 0),
	})
	if err != nil {
		t.Fatalf("PurgeCandidates() error = %v", err)
	}
	if len(page.Candidates) != 2 || page.Candidates[0].RelayActor != "https://a.example/actor" ||
		page.Candidates[1].RelayActor != "https://b.example/actor" ||
		page.Candidates[0].InactiveUnix != 100 || page.Candidates[1].InactiveUnix != 100 ||
		page.Next != (storage.PurgeCandidateCursor{}) {
		t.Fatalf("candidate page = %#v", page)
	}
}

func TestPurgeCandidatesUseCompositeAndEventVersionIndexes(t *testing.T) {
	database := openMigratedTestDatabase(t)
	rows, err := database.Query(`EXPLAIN QUERY PLAN
		SELECT relay_actor,
		       lifecycle_state,
		       CASE lifecycle_state
		           WHEN 'unregistered' THEN unregistered_at_unix
		           WHEN 'pruned' THEN pruned_at_unix
		           ELSE NULL
		       END AS inactive_at_unix,
		       updated_at_unix,
		       COALESCE((SELECT MAX(event_id) FROM relay_events INDEXED BY relay_events_retention_version_idx WHERE relay_events.relay_actor=relays.relay_actor),0),
		       COALESCE((SELECT MAX(moderation_event_id) FROM moderation_events INDEXED BY moderation_events_retention_version_idx WHERE moderation_events.relay_actor=relays.relay_actor),0)
		FROM relays INDEXED BY relays_retention_candidates_idx
		WHERE administrative_state = 'active'
		  AND lifecycle_state IN ('unregistered','pruned')
		  AND CASE lifecycle_state
		          WHEN 'unregistered' THEN unregistered_at_unix
		          WHEN 'pruned' THEN pruned_at_unix
		          ELSE NULL
		      END <= 100
		  AND (CASE lifecycle_state
		           WHEN 'unregistered' THEN unregistered_at_unix
		           WHEN 'pruned' THEN pruned_at_unix
		           ELSE NULL
		       END, relay_actor) > (0, '')
		ORDER BY inactive_at_unix, relay_actor
		LIMIT 101`)
	if err != nil {
		t.Fatalf("EXPLAIN QUERY PLAN error = %v", err)
	}
	defer rows.Close()
	var details []string
	for rows.Next() {
		var id, parent, unused int
		var detail string
		if err := rows.Scan(&id, &parent, &unused, &detail); err != nil {
			t.Fatal(err)
		}
		details = append(details, detail)
	}
	joined := strings.Join(details, "\n")
	for _, index := range []string{
		"relays_retention_candidates_idx",
		"relay_events_retention_version_idx",
		"moderation_events_retention_version_idx",
	} {
		if !strings.Contains(joined, index) {
			t.Fatalf("query plan does not use %s: %s", index, joined)
		}
	}
}

func TestRetentionRepositoryRejectsOversizedCandidateReadsAndBatches(t *testing.T) {
	database := openMigratedTestDatabase(t)
	repository := newTestRelayRepository(t, database)
	if _, err := repository.PurgeCandidates(context.Background(), storage.PurgeCandidateQuery{
		Limit: storage.MaximumPurgeCandidatePage + 1, CutoffAt: time.Unix(100, 0),
	}); !errors.Is(err, storage.ErrRetentionReadInput) {
		t.Fatalf("PurgeCandidates(oversized) error = %v", err)
	}
	candidates := make([]storage.PurgeCandidate, storage.MaximumPurgeCandidatePage+1)
	for index := range candidates {
		candidates[index] = storage.PurgeCandidate{
			RelayActor:     "https://relay.example/actor",
			LifecycleState: storage.LifecycleUnregistered,
			InactiveUnix:   1,
			UpdatedUnix:    1,
		}
	}
	if _, err := repository.PurgeBatch(context.Background(), 1, candidates, time.Unix(100, 0)); !errors.Is(err, storage.ErrRetentionWriteInput) {
		t.Fatalf("PurgeBatch(oversized) error = %v", err)
	}
}

func TestPurgeBatchDeletesLifecycleHistoryPreservesModerationAndRestoresGuard(t *testing.T) {
	database := openMigratedTestDatabase(t)
	repository := newTestRelayRepository(t, database)
	actor := testRelayActor
	insertRetentionRelay(t, database, actor, lifecycleUnregistered, administrativeActive, 100, 90)
	if _, err := database.Exec(
		`INSERT INTO relay_events (relay_actor,event_kind,recorded_at_unix) VALUES (?, 'unregister_removed', 100)`, actor,
	); err != nil {
		t.Fatal(err)
	}
	if _, err := database.Exec(
		`INSERT INTO moderation_events (relay_actor,action,moderator_id,reason_code,recorded_at_unix)
		 VALUES (?, 'restore_applied', 'operator', 'security', 95)`, actor,
	); err != nil {
		t.Fatal(err)
	}

	page, err := repository.PurgeCandidates(context.Background(), storage.PurgeCandidateQuery{
		Limit: 1, CutoffAt: time.Unix(100, 0),
	})
	if err != nil || len(page.Candidates) != 1 {
		t.Fatalf("PurgeCandidates() = %#v, %v", page, err)
	}
	runID := beginTestRetentionRun(t, repository, 100)
	result, err := repository.PurgeBatch(context.Background(), runID, page.Candidates, time.Unix(100, 0))
	if err != nil {
		t.Fatalf("PurgeBatch() error = %v", err)
	}
	if result.Attempted != 1 || result.PurgedRelays != 1 || result.PurgedLifecycleEvents != 1 || result.Skipped != 0 {
		t.Fatalf("PurgeBatch() = %#v", result)
	}
	var relays, events, moderation int
	_ = database.QueryRow(`SELECT COUNT(*) FROM relays WHERE relay_actor=?`, actor).Scan(&relays)
	_ = database.QueryRow(`SELECT COUNT(*) FROM relay_events WHERE relay_actor=?`, actor).Scan(&events)
	_ = database.QueryRow(`SELECT COUNT(*) FROM moderation_events WHERE relay_actor=?`, actor).Scan(&moderation)
	if relays != 0 || events != 0 || moderation != 1 {
		t.Fatalf("post-purge counts relays=%d events=%d moderation=%d", relays, events, moderation)
	}

	if _, err := database.Exec(
		`INSERT INTO relay_events (relay_actor,event_kind,recorded_at_unix) VALUES ('https://guard.example/actor','unregister_absent',1)`,
	); err != nil {
		t.Fatal(err)
	}
	if _, err := database.Exec(`DELETE FROM relay_events WHERE relay_actor='https://guard.example/actor'`); err == nil {
		t.Fatal("relay event delete guard was not restored after purge commit")
	}
}

func TestPurgeBatchRollsBackLifecycleDeletionAndRestoresGuardOnFailure(t *testing.T) {
	database := openMigratedTestDatabase(t)
	repository := newTestRelayRepository(t, database)
	actor := testRelayActor
	insertRetentionRelay(t, database, actor, lifecycleUnregistered, administrativeActive, 100, 90)
	if _, err := database.Exec(
		`INSERT INTO relay_events (relay_actor,event_kind,recorded_at_unix) VALUES (?, 'unregister_removed', 100)`, actor,
	); err != nil {
		t.Fatal(err)
	}
	page, err := repository.PurgeCandidates(context.Background(), storage.PurgeCandidateQuery{
		Limit: 1, CutoffAt: time.Unix(100, 0),
	})
	if err != nil || len(page.Candidates) != 1 {
		t.Fatalf("PurgeCandidates() = %#v, %v", page, err)
	}
	if _, err := database.Exec(`CREATE TRIGGER retention_test_block_relay_delete
		BEFORE DELETE ON relays
		BEGIN
			SELECT RAISE(ABORT, 'test relay delete failure');
		END`); err != nil {
		t.Fatal(err)
	}
	runID := beginTestRetentionRun(t, repository, 100)
	if _, err := repository.PurgeBatch(context.Background(), runID, page.Candidates, time.Unix(100, 0)); err == nil {
		t.Fatal("PurgeBatch() error = nil, want forced relay delete failure")
	}
	var relays, events int
	_ = database.QueryRow(`SELECT COUNT(*) FROM relays WHERE relay_actor=?`, actor).Scan(&relays)
	_ = database.QueryRow(`SELECT COUNT(*) FROM relay_events WHERE relay_actor=?`, actor).Scan(&events)
	if relays != 1 || events != 1 {
		t.Fatalf("rolled-back state relays=%d events=%d", relays, events)
	}
	if _, err := database.Exec(`DELETE FROM relay_events WHERE relay_actor=?`, actor); err == nil {
		t.Fatal("relay event append-only trigger was not restored by transaction rollback")
	}
}

func TestPurgeBatchRevalidatesEveryConcurrentLifecycleOrModerationDecision(t *testing.T) {
	for _, action := range []string{"register", "unregister_absent", "suspend", "restore_unchanged", "suspend_restore"} {
		t.Run(action, func(t *testing.T) {
			database := openMigratedTestDatabase(t)
			repository := newTestRelayRepository(t, database)
			actor := testRelayActor
			insertRetentionRelay(t, database, actor, lifecycleUnregistered, administrativeActive, 100, 90)
			page, err := repository.PurgeCandidates(context.Background(), storage.PurgeCandidateQuery{
				Limit: 1, CutoffAt: time.Unix(100, 0),
			})
			if err != nil || len(page.Candidates) != 1 {
				t.Fatalf("candidate capture = %#v, %v", page, err)
			}
			moderation := storage.ModerationIntent{
				RelayActor: actor, ModeratorID: "operator", ReasonCode: "security",
			}
			switch action {
			case "register":
				if _, err := repository.Register(context.Background(), storage.RegisterIntent{
					RelayActor: actor, PublicBaseURL: testPublicBase,
				}, time.Unix(110, 0)); err != nil {
					t.Fatal(err)
				}
			case "unregister_absent":
				if _, err := repository.Unregister(context.Background(), storage.IdentityIntent{RelayActor: actor}, time.Unix(110, 0)); err != nil {
					t.Fatal(err)
				}
			case "suspend":
				if _, err := repository.Suspend(context.Background(), moderation, time.Unix(110, 0)); err != nil {
					t.Fatal(err)
				}
			case "restore_unchanged":
				if _, err := repository.Restore(context.Background(), moderation, time.Unix(110, 0)); err != nil {
					t.Fatal(err)
				}
			case "suspend_restore":
				if _, err := repository.Suspend(context.Background(), moderation, time.Unix(110, 0)); err != nil {
					t.Fatal(err)
				}
				if _, err := repository.Restore(context.Background(), moderation, time.Unix(120, 0)); err != nil {
					t.Fatal(err)
				}
			}
			runID := beginTestRetentionRun(t, repository, 100)
			result, err := repository.PurgeBatch(context.Background(), runID, page.Candidates, time.Unix(100, 0))
			if err != nil || result.PurgedRelays != 0 || result.Skipped != 1 {
				t.Fatalf("PurgeBatch(after %s) = %#v, %v", action, result, err)
			}
			var count int
			if err := database.QueryRow(`SELECT COUNT(*) FROM relays WHERE relay_actor=?`, actor).Scan(&count); err != nil || count != 1 {
				t.Fatalf("relay count after %s = %d, %v", action, count, err)
			}
		})
	}
}

func TestVerifyRetentionBackupAndRestorePrePurgeDatabase(t *testing.T) {
	dir := t.TempDir()
	livePath := filepath.Join(dir, "live.sqlite")
	backupPath := filepath.Join(dir, "backup.sqlite")
	ctx := context.Background()

	live, err := Open(ctx, livePath)
	if err != nil {
		t.Fatal(err)
	}
	if err := Migrate(ctx, live); err != nil {
		t.Fatal(err)
	}
	repository := newTestRelayRepository(t, live)
	insertRetentionRelay(t, live, testRelayActor, lifecycleUnregistered, administrativeActive, 100, 90)
	if _, err := live.Exec(`INSERT INTO relay_events (relay_actor,event_kind,recorded_at_unix) VALUES (?, 'unregister_removed',100)`, testRelayActor); err != nil {
		t.Fatal(err)
	}
	if _, err := live.Exec(`PRAGMA wal_checkpoint(TRUNCATE)`); err != nil {
		t.Fatal(err)
	}
	if err := live.Close(); err != nil {
		t.Fatal(err)
	}
	copyTestFile(t, livePath, backupPath)

	live, err = Open(ctx, livePath)
	if err != nil {
		t.Fatal(err)
	}
	digest, err := VerifyRetentionBackup(ctx, live, livePath, backupPath)
	if err != nil || len(digest) != 64 {
		t.Fatalf("VerifyRetentionBackup() = (%q, %v)", digest, err)
	}
	for _, suffix := range []string{"-wal", "-shm", "-journal"} {
		if _, err := os.Lstat(backupPath + suffix); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("backup verification created sidecar %s: %v", suffix, err)
		}
	}
	repository, _ = NewRelayRepository(live)
	page, err := repository.PurgeCandidates(ctx, storage.PurgeCandidateQuery{
		Limit: 1, CutoffAt: time.Unix(100, 0),
	})
	if err != nil || len(page.Candidates) != 1 {
		t.Fatalf("PurgeCandidates() = %#v, %v", page, err)
	}
	runID := beginTestRetentionRun(t, repository, 100)
	if _, err := repository.PurgeBatch(ctx, runID, page.Candidates, time.Unix(100, 0)); err != nil {
		t.Fatal(err)
	}
	if err := live.Close(); err != nil {
		t.Fatal(err)
	}

	_ = os.Remove(livePath + "-wal")
	_ = os.Remove(livePath + "-shm")
	copyTestFile(t, backupPath, livePath)
	backupBytes, err := os.ReadFile(backupPath)
	if err != nil {
		t.Fatal(err)
	}
	restoredBytes, err := os.ReadFile(livePath)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(backupBytes, restoredBytes) {
		t.Fatal("restored database bytes differ from verified pre-retention backup")
	}
	restored, err := OpenReadOnly(ctx, livePath)
	if err != nil {
		t.Fatal(err)
	}
	defer restored.Close()
	if err := CheckReady(ctx, restored); err != nil {
		t.Fatal(err)
	}
	var relays, events int
	_ = restored.QueryRow(`SELECT COUNT(*) FROM relays WHERE relay_actor=?`, testRelayActor).Scan(&relays)
	_ = restored.QueryRow(`SELECT COUNT(*) FROM relay_events WHERE relay_actor=?`, testRelayActor).Scan(&events)
	if relays != 1 || events != 1 {
		t.Fatalf("restored pre-retention state relays=%d events=%d", relays, events)
	}
}

func TestVerifyRetentionBackupRejectsSymlinkAndNonOwnerOnlyFile(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	livePath := filepath.Join(dir, "live.sqlite")
	backupPath := filepath.Join(dir, "backup.sqlite")
	symlinkPath := filepath.Join(dir, "backup-link.sqlite")

	live, err := Open(ctx, livePath)
	if err != nil {
		t.Fatal(err)
	}
	if err := Migrate(ctx, live); err != nil {
		t.Fatal(err)
	}
	if _, err := live.Exec(`PRAGMA wal_checkpoint(TRUNCATE)`); err != nil {
		t.Fatal(err)
	}
	if err := live.Close(); err != nil {
		t.Fatal(err)
	}
	copyTestFile(t, livePath, backupPath)
	if err := os.Symlink(backupPath, symlinkPath); err != nil {
		t.Fatal(err)
	}

	live, err = Open(ctx, livePath)
	if err != nil {
		t.Fatal(err)
	}
	defer live.Close()
	if _, err := VerifyRetentionBackup(ctx, live, livePath, symlinkPath); !errors.Is(err, storage.ErrRetentionWriteInput) {
		t.Fatalf("VerifyRetentionBackup(symlink) error = %v", err)
	}
	if err := os.Chmod(backupPath, 0o640); err != nil {
		t.Fatal(err)
	}
	if _, err := VerifyRetentionBackup(ctx, live, livePath, backupPath); !errors.Is(err, storage.ErrRetentionWriteInput) {
		t.Fatalf("VerifyRetentionBackup(non-owner-only) error = %v", err)
	}
	if err := os.Chmod(backupPath, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(backupPath+"-wal", nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := VerifyRetentionBackup(ctx, live, livePath, backupPath); !errors.Is(err, storage.ErrRetentionWriteInput) {
		t.Fatalf("VerifyRetentionBackup(sidecar) error = %v", err)
	}
}

func TestVerifyRetentionBackupRejectsDifferentDatabaseIdentity(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	livePath := filepath.Join(dir, "live.sqlite")
	otherPath := filepath.Join(dir, "other.sqlite")
	live, err := Open(ctx, livePath)
	if err != nil {
		t.Fatal(err)
	}
	defer live.Close()
	if err := Migrate(ctx, live); err != nil {
		t.Fatal(err)
	}
	other, err := Open(ctx, otherPath)
	if err != nil {
		t.Fatal(err)
	}
	if err := Migrate(ctx, other); err != nil {
		t.Fatal(err)
	}
	if _, err := other.Exec(`PRAGMA wal_checkpoint(TRUNCATE)`); err != nil {
		t.Fatal(err)
	}
	if err := other.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := VerifyRetentionBackup(ctx, live, livePath, otherPath); !errors.Is(err, storage.ErrRetentionWriteInput) {
		t.Fatalf("VerifyRetentionBackup(different identity) error = %v", err)
	}
}

func insertRetentionRelay(
	t *testing.T,
	database *sql.DB,
	actor, lifecycle, administrative string,
	inactiveUnix, lastSeenUnix int64,
) {
	t.Helper()
	var unregistered, pruned, suspended any
	switch lifecycle {
	case lifecycleUnregistered:
		unregistered = inactiveUnix
	case lifecyclePruned:
		pruned = inactiveUnix
	}
	updated := inactiveUnix
	if lifecycle == lifecycleRegistered {
		updated = lastSeenUnix
	}
	if administrative == administrativeSuspended {
		suspended = updated
	}
	_, err := database.Exec(
		`INSERT INTO relays (
			relay_actor, public_base_url, lifecycle_state, administrative_state,
			first_registered_at_unix, updated_at_unix, last_seen_at_unix,
			last_heartbeat_at_unix, unregistered_at_unix, pruned_at_unix, suspended_at_unix
		) VALUES (?, ?, ?, ?, 1, ?, ?, NULL, ?, ?, ?)`,
		actor,
		"https://"+strings.Split(strings.TrimPrefix(actor, "https://"), "/")[0],
		lifecycle,
		administrative,
		updated,
		lastSeenUnix,
		unregistered,
		pruned,
		suspended,
	)
	if err != nil {
		t.Fatalf("insert retention relay %s: %v", actor, err)
	}
}

func copyTestFile(t *testing.T, source, destination string) {
	t.Helper()
	in, err := os.Open(source)
	if err != nil {
		t.Fatal(err)
	}
	defer in.Close()
	out, err := os.OpenFile(destination, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := io.Copy(out, in); err != nil {
		_ = out.Close()
		t.Fatal(err)
	}
	if err := out.Sync(); err != nil {
		_ = out.Close()
		t.Fatal(err)
	}
	if err := out.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestRetentionRunCheckpointSurvivesCrashAndFinalizationIsImmutable(t *testing.T) {
	database := openMigratedTestDatabase(t)
	repository := newTestRelayRepository(t, database)
	actor := testRelayActor
	insertRetentionRelay(t, database, actor, lifecycleUnregistered, administrativeActive, 100, 90)
	if _, err := database.Exec(
		`INSERT INTO relay_events (relay_actor,event_kind,recorded_at_unix) VALUES (?, 'unregister_removed',100)`, actor,
	); err != nil {
		t.Fatal(err)
	}
	page, err := repository.PurgeCandidates(context.Background(), storage.PurgeCandidateQuery{
		Limit: 1, CutoffAt: time.Unix(100, 0),
	})
	if err != nil || len(page.Candidates) != 1 {
		t.Fatalf("PurgeCandidates() = %#v, %v", page, err)
	}
	runID := beginTestRetentionRun(t, repository, 100)
	result, err := repository.PurgeBatch(context.Background(), runID, page.Candidates, time.Unix(100, 0))
	if err != nil || result.PurgedRelays != 1 || result.PurgedLifecycleEvents != 1 {
		t.Fatalf("PurgeBatch() = %#v, %v", result, err)
	}

	// Simulate a process crash/restart here: no FinishRetentionRun call has
	// happened, and the original database handle is closed before inspection.
	var sequence int
	var name, databasePath string
	if err := database.QueryRow(`PRAGMA database_list`).Scan(&sequence, &name, &databasePath); err != nil {
		t.Fatal(err)
	}
	if name != "main" || databasePath == "" {
		t.Fatalf("database_list = sequence:%d name:%q path:%q", sequence, name, databasePath)
	}
	if err := database.Close(); err != nil {
		t.Fatal(err)
	}
	database, err = Open(context.Background(), databasePath)
	if err != nil {
		t.Fatalf("Open(after simulated restart) error = %v", err)
	}
	defer database.Close()
	repository = newTestRelayRepository(t, database)

	// A committed deletion must already have durable aggregate checkpoint
	// evidence in the running private audit row.
	var outcome string
	var scanned, purged, lifecycleEvents, skipped, batches int
	var finished sql.NullInt64
	if err := database.QueryRow(
		`SELECT outcome,candidates_scanned,purged_relays,purged_lifecycle_events,skipped,batches,finished_at_unix
		 FROM retention_runs WHERE retention_run_id=?`, runID,
	).Scan(&outcome, &scanned, &purged, &lifecycleEvents, &skipped, &batches, &finished); err != nil {
		t.Fatal(err)
	}
	if outcome != "running" || scanned != 1 || purged != 1 || lifecycleEvents != 1 ||
		skipped != 0 || batches != 1 || finished.Valid {
		t.Fatalf("running checkpoint = outcome:%s scanned:%d purged:%d events:%d skipped:%d batches:%d finished:%v",
			outcome, scanned, purged, lifecycleEvents, skipped, batches, finished)
	}

	finish := storage.RetentionRunFinish{
		RunID: runID, CandidatesScanned: 1, PurgedRelays: 1, PurgedLifecycleEvents: 1,
		Batches: 1, Outcome: storage.RetentionCompleted, FinishedUnix: 86500,
	}
	if err := repository.FinishRetentionRun(context.Background(), finish); err != nil {
		t.Fatalf("FinishRetentionRun() error = %v", err)
	}
	if err := repository.FinishRetentionRun(context.Background(), finish); err != nil {
		t.Fatalf("FinishRetentionRun(exact repeat) error = %v", err)
	}
	conflicting := finish
	conflicting.Outcome = storage.RetentionFailed
	if err := repository.FinishRetentionRun(context.Background(), conflicting); !errors.Is(err, storage.ErrRetentionWriteInput) {
		t.Fatalf("FinishRetentionRun(conflicting) error = %v", err)
	}
	if _, err := database.Exec(`UPDATE retention_runs SET purged_relays=0 WHERE retention_run_id=?`, runID); err == nil {
		t.Fatal("finalized retention run accepted an update")
	}
	if _, err := database.Exec(`DELETE FROM retention_runs WHERE retention_run_id=?`, runID); err == nil {
		t.Fatal("retention run accepted deletion")
	}
}

func TestRetentionRunBindsPolicyCutoffAndRejectsInventedFinalEffects(t *testing.T) {
	database := openMigratedTestDatabase(t)
	repository := newTestRelayRepository(t, database)
	ctx := context.Background()

	bad := storage.RetentionRunStart{
		PolicyVersion: storage.RetentionPolicyVersion,
		RetentionDays: 1,
		ObservedUnix:  86500,
		CutoffUnix:    101,
		BackupSHA256:  strings.Repeat("a", 64),
		StartedUnix:   86500,
	}
	if _, err := repository.BeginRetentionRun(ctx, bad); !errors.Is(err, storage.ErrRetentionWriteInput) {
		t.Fatalf("BeginRetentionRun(inconsistent cutoff) error = %v", err)
	}

	runID := beginTestRetentionRun(t, repository, 100)
	insertRetentionRelay(t, database, testRelayActor, lifecycleUnregistered, administrativeActive, 100, 90)
	page, err := repository.PurgeCandidates(ctx, storage.PurgeCandidateQuery{Limit: 1, CutoffAt: time.Unix(100, 0)})
	if err != nil || len(page.Candidates) != 1 {
		t.Fatalf("PurgeCandidates() = %#v, %v", page, err)
	}
	if _, err := repository.PurgeBatch(ctx, runID, page.Candidates, time.Unix(101, 0)); !errors.Is(err, storage.ErrRetentionWriteInput) {
		t.Fatalf("PurgeBatch(mismatched run cutoff) error = %v", err)
	}
	invented := storage.RetentionRunFinish{
		RunID: runID, CandidatesScanned: 1, PurgedRelays: 1,
		Batches: 1, Outcome: storage.RetentionCompleted, FinishedUnix: 86500,
	}
	if err := repository.FinishRetentionRun(ctx, invented); !errors.Is(err, storage.ErrRetentionWriteInput) {
		t.Fatalf("FinishRetentionRun(invented effects) error = %v", err)
	}
}

func beginTestRetentionRun(t *testing.T, repository *RelayRepository, cutoffUnix int64) int64 {
	t.Helper()
	const day = int64(24 * time.Hour / time.Second)
	observedUnix := cutoffUnix + day
	runID, err := repository.BeginRetentionRun(context.Background(), storage.RetentionRunStart{
		PolicyVersion: storage.RetentionPolicyVersion,
		RetentionDays: 1,
		ObservedUnix:  observedUnix,
		CutoffUnix:    cutoffUnix,
		BackupSHA256:  strings.Repeat("a", 64),
		StartedUnix:   observedUnix,
	})
	if err != nil {
		t.Fatalf("BeginRetentionRun() error = %v", err)
	}
	return runID
}
