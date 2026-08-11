package sqlite

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"

	"github.com/thystra/activity-relay-directory/internal/storage"
)

var _ storage.RetentionRepository = (*RelayRepository)(nil)

const relayEventsDeleteTriggerSQL = `CREATE TRIGGER relay_events_no_delete
BEFORE DELETE ON relay_events
BEGIN
    SELECT RAISE(ABORT, 'relay events are append-only');
END`

// PurgeCandidates returns one indexed bounded page of administratively active
// unregistered or pruned rows ordered by their authoritative inactive transition.
func (repository *RelayRepository) PurgeCandidates(
	ctx context.Context,
	query storage.PurgeCandidateQuery,
) (storage.PurgeCandidatePage, error) {
	if repository == nil || repository.database == nil || ctx == nil {
		return storage.PurgeCandidatePage{}, storage.ErrRepositoryConfiguration
	}
	if !query.After.Valid() || query.Limit <= 0 ||
		query.Limit > storage.MaximumPurgeCandidatePage ||
		(query.After != (storage.PurgeCandidateCursor{}) &&
			!validHealthProjectionActor(query.After.RelayActor)) {
		return storage.PurgeCandidatePage{}, storage.ErrRetentionReadInput
	}
	cutoffUnix := query.CutoffAt.UTC().Unix()
	if cutoffUnix < 0 ||
		(query.After != (storage.PurgeCandidateCursor{}) && query.After.InactiveUnix > cutoffUnix) {
		return storage.PurgeCandidatePage{}, storage.ErrRetentionReadInput
	}

	rows, err := repository.database.QueryContext(
		ctx,
		`SELECT relay_actor,
                lifecycle_state,
                CASE lifecycle_state
                    WHEN 'unregistered' THEN unregistered_at_unix
                    WHEN 'pruned' THEN pruned_at_unix
                    ELSE NULL
                END AS inactive_at_unix,
                updated_at_unix,
                COALESCE((
                    SELECT MAX(event_id)
                    FROM relay_events INDEXED BY relay_events_retention_version_idx
                    WHERE relay_events.relay_actor = relays.relay_actor
                ), 0) AS latest_relay_event_id,
                COALESCE((
                    SELECT MAX(moderation_event_id)
                    FROM moderation_events INDEXED BY moderation_events_retention_version_idx
                    WHERE moderation_events.relay_actor = relays.relay_actor
                ), 0) AS latest_moderation_event_id
         FROM relays INDEXED BY relays_retention_candidates_idx
         WHERE administrative_state = ?
           AND lifecycle_state IN ('unregistered', 'pruned')
           AND CASE lifecycle_state
                   WHEN 'unregistered' THEN unregistered_at_unix
                   WHEN 'pruned' THEN pruned_at_unix
                   ELSE NULL
               END <= ?
           AND (
               CASE lifecycle_state
                   WHEN 'unregistered' THEN unregistered_at_unix
                   WHEN 'pruned' THEN pruned_at_unix
                   ELSE NULL
               END,
               relay_actor
           ) > (?, ?)
         ORDER BY inactive_at_unix, relay_actor
         LIMIT ?`,
		administrativeActive,
		cutoffUnix,
		query.After.InactiveUnix,
		query.After.RelayActor,
		query.Limit+1,
	)
	if err != nil {
		return storage.PurgeCandidatePage{}, storageFailure("read retention candidates", err)
	}
	defer rows.Close()

	page := storage.PurgeCandidatePage{
		Candidates: make([]storage.PurgeCandidate, 0, query.Limit),
	}
	for rows.Next() {
		var candidate storage.PurgeCandidate
		var lifecycle string
		if err := rows.Scan(
			&candidate.RelayActor,
			&lifecycle,
			&candidate.InactiveUnix,
			&candidate.UpdatedUnix,
			&candidate.LatestRelayEventID,
			&candidate.LatestModerationEventID,
		); err != nil {
			return storage.PurgeCandidatePage{}, storageFailure("decode retention candidate", err)
		}
		candidate.LifecycleState = storage.RelayLifecycleState(lifecycle)
		if !candidate.Valid() || !validHealthProjectionActor(candidate.RelayActor) ||
			candidate.InactiveUnix > cutoffUnix {
			return storage.PurgeCandidatePage{}, storageFailure(
				"validate retention candidate",
				errors.New("invalid retained retention state"),
			)
		}
		if len(page.Candidates) == query.Limit {
			last := page.Candidates[len(page.Candidates)-1]
			page.Next = storage.PurgeCandidateCursor{
				InactiveUnix: last.InactiveUnix,
				RelayActor:   last.RelayActor,
			}
			break
		}
		page.Candidates = append(page.Candidates, candidate)
	}
	if err := rows.Err(); err != nil {
		return storage.PurgeCandidatePage{}, storageFailure("iterate retention candidates", err)
	}
	return page, nil
}

// PurgeBatch revalidates one bounded candidate page under an immediate write
// transaction. The relay-events delete trigger is dropped only inside this
// transaction and is recreated before commit; rollback restores the trigger.
// Private moderation events are deliberately retained.
func (repository *RelayRepository) PurgeBatch(
	ctx context.Context,
	runID int64,
	candidates []storage.PurgeCandidate,
	cutoffAt time.Time,
) (storage.PurgeBatchResult, error) {
	if repository == nil || repository.database == nil || ctx == nil {
		return storage.PurgeBatchResult{}, storage.ErrRepositoryConfiguration
	}
	cutoffUnix := cutoffAt.UTC().Unix()
	if runID <= 0 || cutoffUnix < 0 || len(candidates) == 0 || len(candidates) > storage.MaximumPurgeCandidatePage {
		return storage.PurgeBatchResult{}, storage.ErrRetentionWriteInput
	}
	for _, candidate := range candidates {
		if !candidate.Valid() || !validHealthProjectionActor(candidate.RelayActor) ||
			candidate.InactiveUnix > cutoffUnix {
			return storage.PurgeBatchResult{}, storage.ErrRetentionWriteInput
		}
	}

	transaction, lease, err := repository.begin(ctx)
	if err != nil {
		return storage.PurgeBatchResult{}, err
	}
	defer lease.Release()
	defer func() { _ = transaction.Rollback() }()

	var runOutcome string
	var runCutoffUnix int64
	if err := transaction.QueryRowContext(
		ctx,
		`SELECT outcome, cutoff_at_unix FROM retention_runs WHERE retention_run_id = ?`,
		runID,
	).Scan(&runOutcome, &runCutoffUnix); err != nil {
		return storage.PurgeBatchResult{}, storageFailure("read retention run", err)
	}
	if runOutcome != "running" || runCutoffUnix != cutoffUnix {
		return storage.PurgeBatchResult{}, storage.ErrRetentionWriteInput
	}

	if _, err := transaction.ExecContext(ctx, `DROP TRIGGER relay_events_no_delete`); err != nil {
		return storage.PurgeBatchResult{}, storageFailure("open retention delete scope", err)
	}

	result := storage.PurgeBatchResult{Attempted: len(candidates)}
	for _, candidate := range candidates {
		current, err := readRetentionState(ctx, transaction, candidate.RelayActor)
		if err != nil {
			return storage.PurgeBatchResult{}, err
		}
		if current == nil || current.administrative != administrativeActive ||
			current.lifecycle != string(candidate.LifecycleState) ||
			current.inactiveUnix != candidate.InactiveUnix ||
			current.updatedUnix != candidate.UpdatedUnix ||
			current.latestRelayEventID != candidate.LatestRelayEventID ||
			current.latestModerationEventID != candidate.LatestModerationEventID ||
			current.inactiveUnix > cutoffUnix {
			result.Skipped++
			continue
		}

		deletedEvents, err := transaction.ExecContext(
			ctx,
			`DELETE FROM relay_events WHERE relay_actor = ?`,
			candidate.RelayActor,
		)
		if err != nil {
			return storage.PurgeBatchResult{}, storageFailure("delete retained lifecycle events", err)
		}
		eventCount, err := deletedEvents.RowsAffected()
		if err != nil || eventCount < 0 {
			if err == nil {
				err = errors.New("negative deleted lifecycle event count")
			}
			return storage.PurgeBatchResult{}, storageFailure("count deleted lifecycle events", err)
		}

		deletedRelay, err := transaction.ExecContext(
			ctx,
			`DELETE FROM relays
             WHERE relay_actor = ?
               AND administrative_state = ?
               AND lifecycle_state = ?`,
			candidate.RelayActor,
			administrativeActive,
			string(candidate.LifecycleState),
		)
		if err != nil {
			return storage.PurgeBatchResult{}, storageFailure("delete inactive relay", err)
		}
		relayCount, err := deletedRelay.RowsAffected()
		if err != nil || relayCount != 1 {
			if err == nil {
				err = fmt.Errorf("deleted relay count = %d", relayCount)
			}
			return storage.PurgeBatchResult{}, storageFailure("count deleted inactive relay", err)
		}
		result.PurgedRelays++
		result.PurgedLifecycleEvents += int(eventCount)
	}

	if _, err := transaction.ExecContext(ctx, relayEventsDeleteTriggerSQL); err != nil {
		return storage.PurgeBatchResult{}, storageFailure("close retention delete scope", err)
	}
	checkpoint, err := transaction.ExecContext(
		ctx,
		`UPDATE retention_runs
		 SET candidates_scanned = candidates_scanned + ?,
		     purged_relays = purged_relays + ?,
		     purged_lifecycle_events = purged_lifecycle_events + ?,
		     skipped = skipped + ?,
		     batches = batches + 1
		 WHERE retention_run_id = ?
		   AND outcome = 'running'`,
		result.Attempted,
		result.PurgedRelays,
		result.PurgedLifecycleEvents,
		result.Skipped,
		runID,
	)
	if err != nil {
		return storage.PurgeBatchResult{}, storageFailure("checkpoint retention batch", err)
	}
	checkpointCount, err := checkpoint.RowsAffected()
	if err != nil || checkpointCount != 1 {
		if err == nil {
			err = fmt.Errorf("retention checkpoint count = %d", checkpointCount)
		}
		return storage.PurgeBatchResult{}, storageFailure("checkpoint retention batch", err)
	}
	if err := transaction.Commit(); err != nil {
		return storage.PurgeBatchResult{}, storageFailure("commit retention batch", err)
	}
	return result, nil
}

type retentionState struct {
	lifecycle               string
	administrative          string
	inactiveUnix            int64
	updatedUnix             int64
	latestRelayEventID      int64
	latestModerationEventID int64
}

func readRetentionState(
	ctx context.Context,
	transaction *sql.Tx,
	actor string,
) (*retentionState, error) {
	var state retentionState
	var inactive sql.NullInt64
	err := transaction.QueryRowContext(
		ctx,
		`SELECT lifecycle_state,
                administrative_state,
                CASE lifecycle_state
                    WHEN 'unregistered' THEN unregistered_at_unix
                    WHEN 'pruned' THEN pruned_at_unix
                    ELSE NULL
                END,
                updated_at_unix,
                COALESCE((
                    SELECT MAX(event_id)
                    FROM relay_events INDEXED BY relay_events_retention_version_idx
                    WHERE relay_events.relay_actor = relays.relay_actor
                ), 0),
                COALESCE((
                    SELECT MAX(moderation_event_id)
                    FROM moderation_events INDEXED BY moderation_events_retention_version_idx
                    WHERE moderation_events.relay_actor = relays.relay_actor
                ), 0)
         FROM relays
         WHERE relay_actor = ?`,
		actor,
	).Scan(
		&state.lifecycle,
		&state.administrative,
		&inactive,
		&state.updatedUnix,
		&state.latestRelayEventID,
		&state.latestModerationEventID,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, storageFailure("read relay for retention", err)
	}
	if !inactive.Valid {
		state.inactiveUnix = -1
	} else {
		state.inactiveUnix = inactive.Int64
	}
	return &state, nil
}

// BeginRetentionRun creates the private aggregate run record before any
// destructive candidate scan. It contains no relay identities.
func (repository *RelayRepository) BeginRetentionRun(
	ctx context.Context,
	start storage.RetentionRunStart,
) (int64, error) {
	if repository == nil || repository.database == nil || ctx == nil {
		return 0, storage.ErrRepositoryConfiguration
	}
	if start.PolicyVersion != storage.RetentionPolicyVersion ||
		start.RetentionDays <= 0 || start.RetentionDays > storage.MaximumInactiveRetentionDays ||
		start.ObservedUnix < 0 || start.CutoffUnix < 0 || start.CutoffUnix > start.ObservedUnix ||
		!validRetentionDigest(start.BackupSHA256) || start.StartedUnix < start.ObservedUnix {
		return 0, storage.ErrRetentionWriteInput
	}
	retentionSeconds := int64(start.RetentionDays) * int64(24*time.Hour/time.Second)
	if retentionSeconds > start.ObservedUnix || start.ObservedUnix-retentionSeconds != start.CutoffUnix {
		return 0, storage.ErrRetentionWriteInput
	}
	lease, err := repository.acquireWrite(ctx)
	if err != nil {
		return 0, err
	}
	defer lease.Release()
	result, err := repository.database.ExecContext(
		ctx,
		`INSERT INTO retention_runs (
             policy_version,
             retention_days,
             observed_at_unix,
             cutoff_at_unix,
             backup_sha256,
             started_at_unix
         ) VALUES (?, ?, ?, ?, ?, ?)`,
		start.PolicyVersion,
		start.RetentionDays,
		start.ObservedUnix,
		start.CutoffUnix,
		start.BackupSHA256,
		start.StartedUnix,
	)
	if err != nil {
		return 0, storageFailure("start retention run audit", err)
	}
	runID, err := result.LastInsertId()
	if err != nil || runID <= 0 {
		if err == nil {
			err = errors.New("invalid retention run identifier")
		}
		return 0, storageFailure("read retention run identifier", err)
	}
	return runID, nil
}

// FinishRetentionRun makes one running audit record immutable. Counts may
// include candidates scanned by a failed/canceled final batch that did not
// commit; committed batch counts were already checkpointed atomically.
func (repository *RelayRepository) FinishRetentionRun(
	ctx context.Context,
	finish storage.RetentionRunFinish,
) error {
	if repository == nil || repository.database == nil || ctx == nil {
		return storage.ErrRepositoryConfiguration
	}
	if finish.RunID <= 0 ||
		finish.CandidatesScanned < 0 || finish.CandidatesScanned > storage.MaximumPurgeAttemptsPerRun ||
		finish.PurgedRelays < 0 || finish.PurgedRelays > finish.CandidatesScanned ||
		finish.PurgedLifecycleEvents < 0 || finish.Skipped < 0 ||
		finish.PurgedRelays+finish.Skipped > finish.CandidatesScanned ||
		(finish.Outcome == storage.RetentionCompleted &&
			finish.PurgedRelays+finish.Skipped != finish.CandidatesScanned) ||
		finish.Batches < 0 || finish.Batches > storage.MaximumPurgeAttemptsPerRun ||
		(finish.CandidatesScanned == 0 && finish.Batches != 0) ||
		(finish.CandidatesScanned > 0 && (finish.Batches == 0 || finish.Batches > finish.CandidatesScanned)) ||
		!finish.Outcome.Valid() || finish.FinishedUnix < 0 {
		return storage.ErrRetentionWriteInput
	}

	var checkpoint storage.RetentionRunFinish
	var checkpointTruncated int
	var currentOutcome string
	var finished sql.NullInt64
	if err := repository.database.QueryRowContext(
		ctx,
		`SELECT retention_run_id,
                candidates_scanned,
                purged_relays,
                purged_lifecycle_events,
                skipped,
                batches,
                truncated,
                outcome,
                finished_at_unix
         FROM retention_runs
         WHERE retention_run_id = ?`,
		finish.RunID,
	).Scan(
		&checkpoint.RunID,
		&checkpoint.CandidatesScanned,
		&checkpoint.PurgedRelays,
		&checkpoint.PurgedLifecycleEvents,
		&checkpoint.Skipped,
		&checkpoint.Batches,
		&checkpointTruncated,
		&currentOutcome,
		&finished,
	); err != nil {
		return storageFailure("read retention run audit for finalization", err)
	}
	checkpoint.Truncated = checkpointTruncated == 1
	if currentOutcome != "running" {
		if !finished.Valid {
			return storage.ErrRetentionWriteInput
		}
		checkpoint.Outcome = storage.RetentionOutcome(currentOutcome)
		checkpoint.FinishedUnix = finished.Int64
		if checkpoint != finish {
			return storage.ErrRetentionWriteInput
		}
		return nil
	}

	// Destructive counts and committed batch count are checkpointed only inside
	// PurgeBatch transactions. Finalization may account for candidates scanned
	// by a failed/canceled batch, but it may never invent committed effects.
	if finish.PurgedRelays != checkpoint.PurgedRelays ||
		finish.PurgedLifecycleEvents != checkpoint.PurgedLifecycleEvents ||
		finish.Skipped != checkpoint.Skipped ||
		finish.Batches < checkpoint.Batches ||
		finish.CandidatesScanned < checkpoint.CandidatesScanned ||
		(finish.Outcome == storage.RetentionCompleted &&
			(finish.CandidatesScanned != checkpoint.CandidatesScanned ||
				finish.Batches != checkpoint.Batches)) {
		return storage.ErrRetentionWriteInput
	}

	lease, err := repository.acquireWrite(ctx)
	if err != nil {
		return err
	}
	defer lease.Release()

	truncated := 0
	if finish.Truncated {
		truncated = 1
	}
	result, err := repository.database.ExecContext(
		ctx,
		`UPDATE retention_runs
         SET candidates_scanned = ?,
             purged_relays = ?,
             purged_lifecycle_events = ?,
             skipped = ?,
             batches = ?,
             truncated = ?,
             outcome = ?,
             finished_at_unix = ?
         WHERE retention_run_id = ?
           AND outcome = 'running'`,
		finish.CandidatesScanned,
		finish.PurgedRelays,
		finish.PurgedLifecycleEvents,
		finish.Skipped,
		finish.Batches,
		truncated,
		string(finish.Outcome),
		finish.FinishedUnix,
		finish.RunID,
	)
	if err != nil {
		return storageFailure("finish retention run audit", err)
	}
	count, err := result.RowsAffected()
	if err != nil {
		return storageFailure("inspect retention run finalization", err)
	}
	if count != 1 {
		return storage.ErrRetentionWriteInput
	}
	return nil
}

// RetentionDatabaseIdentity returns the persistent random database identity
// added by the retention migration.
func RetentionDatabaseIdentity(ctx context.Context, database *sql.DB) ([]byte, error) {
	if ctx == nil || database == nil {
		return nil, storage.ErrRepositoryConfiguration
	}
	var identity []byte
	var version int
	if err := database.QueryRowContext(
		ctx,
		`SELECT database_identity, policy_version
         FROM retention_metadata
         WHERE singleton = 1`,
	).Scan(&identity, &version); err != nil {
		return nil, storageFailure("read retention database identity", err)
	}
	if len(identity) != 16 || version != storage.RetentionPolicyVersion {
		return nil, storageFailure("validate retention database identity", errors.New("invalid retention metadata"))
	}
	return append([]byte(nil), identity...), nil
}

// VerifyRetentionBackup validates one secure standalone SQLite backup before a
// destructive purge. The backup must have the current schema, pass quick_check,
// and carry the same persistent database identity as the live database.
func VerifyRetentionBackup(
	ctx context.Context,
	liveDatabase *sql.DB,
	livePath string,
	backupPath string,
) (string, error) {
	if ctx == nil || liveDatabase == nil || livePath == "" || backupPath == "" ||
		!filepath.IsAbs(livePath) || !filepath.IsAbs(backupPath) ||
		filepath.Clean(livePath) != livePath || filepath.Clean(backupPath) != backupPath ||
		livePath == backupPath {
		return "", storage.ErrRetentionWriteInput
	}
	liveInfo, err := os.Stat(livePath)
	if err != nil {
		return "", storageFailure("inspect live database for backup verification", err)
	}
	backupInfo, err := os.Lstat(backupPath)
	if err != nil {
		return "", storageFailure("inspect retention backup", err)
	}
	if backupInfo.Mode()&os.ModeSymlink != 0 || !backupInfo.Mode().IsRegular() ||
		backupInfo.Mode().Perm()&0o077 != 0 || os.SameFile(liveInfo, backupInfo) {
		return "", storage.ErrRetentionWriteInput
	}
	if err := requireStandaloneRetentionBackup(backupPath); err != nil {
		return "", err
	}

	backupDatabase, err := openImmutableReadOnly(ctx, backupPath)
	if err != nil {
		return "", err
	}
	defer backupDatabase.Close()
	if err := CheckReady(ctx, backupDatabase); err != nil {
		return "", storageFailure("verify retention backup schema", err)
	}
	if err := quickCheck(ctx, backupDatabase); err != nil {
		return "", storageFailure("verify retention backup integrity", err)
	}
	liveIdentity, err := RetentionDatabaseIdentity(ctx, liveDatabase)
	if err != nil {
		return "", err
	}
	backupIdentity, err := RetentionDatabaseIdentity(ctx, backupDatabase)
	if err != nil {
		return "", err
	}
	if !bytes.Equal(liveIdentity, backupIdentity) {
		return "", storage.ErrRetentionWriteInput
	}
	afterVerifyInfo, err := os.Lstat(backupPath)
	if err != nil || !os.SameFile(backupInfo, afterVerifyInfo) ||
		afterVerifyInfo.Mode().Perm()&0o077 != 0 || !afterVerifyInfo.Mode().IsRegular() {
		return "", storage.ErrRetentionWriteInput
	}
	if err := requireStandaloneRetentionBackup(backupPath); err != nil {
		return "", err
	}

	file, err := os.Open(backupPath)
	if err != nil {
		return "", storageFailure("open retention backup for digest", err)
	}
	defer file.Close()
	openedInfo, err := file.Stat()
	if err != nil {
		return "", storageFailure("inspect opened retention backup", err)
	}
	if !os.SameFile(backupInfo, openedInfo) || openedInfo.Mode().Perm()&0o077 != 0 || !openedInfo.Mode().IsRegular() {
		return "", storage.ErrRetentionWriteInput
	}
	digest := sha256.New()
	if _, err := io.Copy(digest, file); err != nil {
		return "", storageFailure("hash retention backup", err)
	}
	return hex.EncodeToString(digest.Sum(nil)), nil
}

func requireStandaloneRetentionBackup(path string) error {
	for _, suffix := range []string{"-wal", "-shm", "-journal"} {
		_, err := os.Lstat(path + suffix)
		switch {
		case err == nil:
			return storage.ErrRetentionWriteInput
		case errors.Is(err, os.ErrNotExist):
			continue
		default:
			return storageFailure("inspect retention backup sidecar", err)
		}
	}
	return nil
}

func quickCheck(ctx context.Context, database *sql.DB) error {
	rows, err := database.QueryContext(ctx, `PRAGMA quick_check`)
	if err != nil {
		return err
	}
	defer rows.Close()
	count := 0
	for rows.Next() {
		var value string
		if err := rows.Scan(&value); err != nil {
			return err
		}
		count++
		if value != "ok" {
			return errors.New("SQLite quick_check failed")
		}
	}
	if err := rows.Err(); err != nil {
		return err
	}
	if count != 1 {
		return errors.New("SQLite quick_check returned an unexpected result")
	}
	return nil
}

func validRetentionDigest(value string) bool {
	if len(value) != sha256.Size*2 {
		return false
	}
	_, err := hex.DecodeString(value)
	return err == nil && value == string(bytes.ToLower([]byte(value)))
}
