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

// PurgeCandidates returns one bounded keyset page containing both eligible
// lifecycle rows and removed discovery rows. Ordering is stable across the two
// sources by inactive time, canonical actor, and candidate kind.
func (repository *RelayRepository) PurgeCandidates(
	ctx context.Context,
	query storage.PurgeCandidateQuery,
) (storage.PurgeCandidatePage, error) {
	if repository == nil || repository.database == nil || ctx == nil {
		return storage.PurgeCandidatePage{}, storage.ErrRepositoryConfiguration
	}
	if !query.After.Valid() || query.Limit <= 0 || query.Limit > storage.MaximumPurgeCandidatePage ||
		(query.After != (storage.PurgeCandidateCursor{}) && !validHealthProjectionActor(query.After.RelayActor)) {
		return storage.PurgeCandidatePage{}, storage.ErrRetentionReadInput
	}
	cutoffUnix := query.CutoffAt.UTC().Unix()
	if cutoffUnix < 0 ||
		(query.After != (storage.PurgeCandidateCursor{}) && query.After.InactiveUnix > cutoffUnix) {
		return storage.PurgeCandidatePage{}, storage.ErrRetentionReadInput
	}

	perSourceLimit := query.Limit + 1
	rows, err := repository.database.QueryContext(ctx, `WITH
	lifecycle_candidates AS (
		SELECT
			'lifecycle' AS candidate_kind,
			relay_actor,
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
			), 0) AS latest_moderation_event_id,
			0 AS latest_discovery_event_id,
			COALESCE((
				SELECT revision
				FROM relay_observations
				WHERE relay_observations.relay_actor = relays.relay_actor
			), 0) AS observation_revision
		FROM relays INDEXED BY relays_retention_candidates_idx
		WHERE administrative_state = 'active'
		  AND lifecycle_state IN ('unregistered', 'pruned')
		  AND CASE lifecycle_state
				WHEN 'unregistered' THEN unregistered_at_unix
				WHEN 'pruned' THEN pruned_at_unix
				ELSE NULL
			  END <= ?
		  AND (CASE lifecycle_state
				WHEN 'unregistered' THEN unregistered_at_unix
				WHEN 'pruned' THEN pruned_at_unix
				ELSE NULL
			  END, relay_actor, 'lifecycle') > (?, ?, ?)
		ORDER BY inactive_at_unix, relay_actor
		LIMIT ?
	),
	discovery_candidates AS (
		SELECT
			'discovery' AS candidate_kind,
			relay_actor,
			'' AS lifecycle_state,
			removed_at_unix AS inactive_at_unix,
			updated_at_unix,
			0 AS latest_relay_event_id,
			0 AS latest_moderation_event_id,
			COALESCE((
				SELECT MAX(discovery_event_id)
				FROM discovery_events INDEXED BY discovery_events_retention_version_idx
				WHERE discovery_events.relay_actor = relay_discoveries.relay_actor
			), 0) AS latest_discovery_event_id,
			COALESCE((
				SELECT revision
				FROM relay_observations
				WHERE relay_observations.relay_actor = relay_discoveries.relay_actor
			), 0) AS observation_revision
		FROM relay_discoveries INDEXED BY relay_discoveries_retention_candidates_idx
		WHERE discovery_state = 'removed'
		  AND removed_at_unix <= ?
		  AND (removed_at_unix, relay_actor, 'discovery') > (?, ?, ?)
		ORDER BY removed_at_unix, relay_actor
		LIMIT ?
	),
	candidates AS (
		SELECT * FROM lifecycle_candidates
		UNION ALL
		SELECT * FROM discovery_candidates
	)
	SELECT candidate_kind, relay_actor, lifecycle_state, inactive_at_unix,
	       updated_at_unix, latest_relay_event_id, latest_moderation_event_id,
	       latest_discovery_event_id, observation_revision
	FROM candidates
	ORDER BY inactive_at_unix, relay_actor, candidate_kind
	LIMIT ?`,
		cutoffUnix,
		query.After.InactiveUnix,
		query.After.RelayActor,
		string(query.After.Kind),
		perSourceLimit,
		cutoffUnix,
		query.After.InactiveUnix,
		query.After.RelayActor,
		string(query.After.Kind),
		perSourceLimit,
		perSourceLimit,
	)
	if err != nil {
		return storage.PurgeCandidatePage{}, storageFailure("read retention candidates", err)
	}
	defer rows.Close()

	page := storage.PurgeCandidatePage{Candidates: make([]storage.PurgeCandidate, 0, query.Limit)}
	for rows.Next() {
		var candidate storage.PurgeCandidate
		var kind, lifecycle string
		if err := rows.Scan(
			&kind,
			&candidate.RelayActor,
			&lifecycle,
			&candidate.InactiveUnix,
			&candidate.UpdatedUnix,
			&candidate.LatestRelayEventID,
			&candidate.LatestModerationEventID,
			&candidate.LatestDiscoveryEventID,
			&candidate.ObservationRevision,
		); err != nil {
			return storage.PurgeCandidatePage{}, storageFailure("decode retention candidate", err)
		}
		candidate.Kind = storage.PurgeCandidateKind(kind)
		candidate.LifecycleState = storage.RelayLifecycleState(lifecycle)
		if !candidate.Valid() || !validHealthProjectionActor(candidate.RelayActor) || candidate.InactiveUnix > cutoffUnix {
			return storage.PurgeCandidatePage{}, storageFailure(
				"validate retention candidate", errors.New("invalid retained retention state"),
			)
		}
		if len(page.Candidates) == query.Limit {
			last := page.Candidates[len(page.Candidates)-1]
			page.Next = storage.PurgeCandidateCursor{
				InactiveUnix: last.InactiveUnix,
				RelayActor:   last.RelayActor,
				Kind:         last.Kind,
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

// PurgeBatch revalidates one bounded mixed candidate page under an immediate
// write transaction. Lifecycle history is deleted only under the existing
// temporary trigger scope. Discovery audit is deliberately retained. An
// observation row is removed only after no lifecycle or discovery row remains.
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
		if !candidate.Valid() || !validHealthProjectionActor(candidate.RelayActor) || candidate.InactiveUnix > cutoffUnix {
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
	var runPolicyVersion int
	if err := transaction.QueryRowContext(ctx,
		`SELECT outcome, cutoff_at_unix, policy_version FROM retention_runs WHERE retention_run_id = ?`, runID,
	).Scan(&runOutcome, &runCutoffUnix, &runPolicyVersion); err != nil {
		return storage.PurgeBatchResult{}, storageFailure("read retention run", err)
	}
	if runOutcome != "running" || runCutoffUnix != cutoffUnix || runPolicyVersion != storage.RetentionPolicyVersion {
		return storage.PurgeBatchResult{}, storage.ErrRetentionWriteInput
	}

	needsLifecycleDelete := false
	for _, candidate := range candidates {
		if candidate.Kind == storage.PurgeCandidateLifecycle {
			needsLifecycleDelete = true
			break
		}
	}
	if needsLifecycleDelete {
		if _, err := transaction.ExecContext(ctx, `DROP TRIGGER relay_events_no_delete`); err != nil {
			return storage.PurgeBatchResult{}, storageFailure("open retention delete scope", err)
		}
	}

	result := storage.PurgeBatchResult{Attempted: len(candidates)}
	for _, candidate := range candidates {
		current, err := readRetentionState(ctx, transaction, candidate)
		if err != nil {
			return storage.PurgeBatchResult{}, err
		}
		if current == nil || !retentionStateMatches(candidate, current, cutoffUnix) {
			result.Skipped++
			continue
		}

		switch candidate.Kind {
		case storage.PurgeCandidateLifecycle:
			deletedEvents, err := transaction.ExecContext(ctx,
				`DELETE FROM relay_events WHERE relay_actor = ?`, candidate.RelayActor)
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
			deletedRelay, err := transaction.ExecContext(ctx, `DELETE FROM relays
				WHERE relay_actor = ? AND administrative_state = ? AND lifecycle_state = ?`,
				candidate.RelayActor, administrativeActive, string(candidate.LifecycleState))
			if err != nil {
				return storage.PurgeBatchResult{}, storageFailure("delete inactive relay", err)
			}
			count, err := deletedRelay.RowsAffected()
			if err != nil || count != 1 {
				if err == nil {
					err = fmt.Errorf("deleted relay count = %d", count)
				}
				return storage.PurgeBatchResult{}, storageFailure("count deleted inactive relay", err)
			}
			result.PurgedRelays++
			result.PurgedLifecycleEvents += int(eventCount)

		case storage.PurgeCandidateDiscovery:
			deleted, err := transaction.ExecContext(ctx, `DELETE FROM relay_discoveries
				WHERE relay_actor = ? AND discovery_state = 'removed'`, candidate.RelayActor)
			if err != nil {
				return storage.PurgeBatchResult{}, storageFailure("delete removed discovery", err)
			}
			count, err := deleted.RowsAffected()
			if err != nil || count != 1 {
				if err == nil {
					err = fmt.Errorf("deleted discovery count = %d", count)
				}
				return storage.PurgeBatchResult{}, storageFailure("count deleted discovery", err)
			}
			result.PurgedDiscoveries++
		default:
			return storage.PurgeBatchResult{}, storage.ErrRetentionWriteInput
		}

		deletedObservation, err := transaction.ExecContext(ctx, `DELETE FROM relay_observations
			WHERE relay_actor = ?
			  AND NOT EXISTS (SELECT 1 FROM relays WHERE relays.relay_actor = relay_observations.relay_actor)
			  AND NOT EXISTS (SELECT 1 FROM relay_discoveries WHERE relay_discoveries.relay_actor = relay_observations.relay_actor)`,
			candidate.RelayActor)
		if err != nil {
			return storage.PurgeBatchResult{}, storageFailure("delete unowned relay observation", err)
		}
		observationCount, err := deletedObservation.RowsAffected()
		if err != nil || observationCount < 0 || observationCount > 1 {
			if err == nil {
				err = fmt.Errorf("deleted observation count = %d", observationCount)
			}
			return storage.PurgeBatchResult{}, storageFailure("count deleted relay observation", err)
		}
		result.PurgedObservations += int(observationCount)
	}

	if needsLifecycleDelete {
		if _, err := transaction.ExecContext(ctx, relayEventsDeleteTriggerSQL); err != nil {
			return storage.PurgeBatchResult{}, storageFailure("close retention delete scope", err)
		}
	}
	checkpoint, err := transaction.ExecContext(ctx, `UPDATE retention_runs
		SET candidates_scanned = candidates_scanned + ?,
		    purged_relays = purged_relays + ?,
		    purged_discoveries = purged_discoveries + ?,
		    purged_observations = purged_observations + ?,
		    purged_lifecycle_events = purged_lifecycle_events + ?,
		    skipped = skipped + ?,
		    batches = batches + 1
		WHERE retention_run_id = ? AND outcome = 'running'`,
		result.Attempted,
		result.PurgedRelays,
		result.PurgedDiscoveries,
		result.PurgedObservations,
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
	kind                    storage.PurgeCandidateKind
	lifecycle               string
	administrative          string
	inactiveUnix            int64
	updatedUnix             int64
	latestRelayEventID      int64
	latestModerationEventID int64
	latestDiscoveryEventID  int64
	observationRevision     int64
}

func readRetentionState(
	ctx context.Context,
	transaction *sql.Tx,
	candidate storage.PurgeCandidate,
) (*retentionState, error) {
	state := retentionState{kind: candidate.Kind, observationRevision: 0}
	switch candidate.Kind {
	case storage.PurgeCandidateLifecycle:
		var inactive sql.NullInt64
		err := transaction.QueryRowContext(ctx, `SELECT
			lifecycle_state,
			administrative_state,
			CASE lifecycle_state
				WHEN 'unregistered' THEN unregistered_at_unix
				WHEN 'pruned' THEN pruned_at_unix
				ELSE NULL
			END,
			updated_at_unix,
			COALESCE((SELECT MAX(event_id) FROM relay_events INDEXED BY relay_events_retention_version_idx
				WHERE relay_events.relay_actor = relays.relay_actor), 0),
			COALESCE((SELECT MAX(moderation_event_id) FROM moderation_events INDEXED BY moderation_events_retention_version_idx
				WHERE moderation_events.relay_actor = relays.relay_actor), 0),
			COALESCE((SELECT revision FROM relay_observations
				WHERE relay_observations.relay_actor = relays.relay_actor), 0)
			FROM relays WHERE relay_actor = ?`, candidate.RelayActor).Scan(
			&state.lifecycle,
			&state.administrative,
			&inactive,
			&state.updatedUnix,
			&state.latestRelayEventID,
			&state.latestModerationEventID,
			&state.observationRevision,
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

	case storage.PurgeCandidateDiscovery:
		var removed sql.NullInt64
		err := transaction.QueryRowContext(ctx, `SELECT
			discovery_state,
			removed_at_unix,
			updated_at_unix,
			COALESCE((SELECT MAX(discovery_event_id) FROM discovery_events INDEXED BY discovery_events_retention_version_idx
				WHERE discovery_events.relay_actor = relay_discoveries.relay_actor), 0),
			COALESCE((SELECT revision FROM relay_observations
				WHERE relay_observations.relay_actor = relay_discoveries.relay_actor), 0)
			FROM relay_discoveries WHERE relay_actor = ?`, candidate.RelayActor).Scan(
			&state.lifecycle,
			&removed,
			&state.updatedUnix,
			&state.latestDiscoveryEventID,
			&state.observationRevision,
		)
		if errors.Is(err, sql.ErrNoRows) {
			return nil, nil
		}
		if err != nil {
			return nil, storageFailure("read discovery for retention", err)
		}
		if !removed.Valid {
			state.inactiveUnix = -1
		} else {
			state.inactiveUnix = removed.Int64
		}
		return &state, nil
	default:
		return nil, storage.ErrRetentionWriteInput
	}
}

func retentionStateMatches(candidate storage.PurgeCandidate, current *retentionState, cutoffUnix int64) bool {
	if current == nil || current.kind != candidate.Kind ||
		current.inactiveUnix != candidate.InactiveUnix || current.updatedUnix != candidate.UpdatedUnix ||
		current.observationRevision != candidate.ObservationRevision || current.inactiveUnix > cutoffUnix {
		return false
	}
	switch candidate.Kind {
	case storage.PurgeCandidateLifecycle:
		return current.administrative == administrativeActive &&
			current.lifecycle == string(candidate.LifecycleState) &&
			current.latestRelayEventID == candidate.LatestRelayEventID &&
			current.latestModerationEventID == candidate.LatestModerationEventID
	case storage.PurgeCandidateDiscovery:
		return current.lifecycle == discoveryRemoved &&
			current.latestDiscoveryEventID == candidate.LatestDiscoveryEventID
	default:
		return false
	}
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
	primaryPurged := finish.PurgedRelays + finish.PurgedDiscoveries
	if finish.RunID <= 0 ||
		finish.CandidatesScanned < 0 || finish.CandidatesScanned > storage.MaximumPurgeAttemptsPerRun ||
		finish.PurgedRelays < 0 || finish.PurgedDiscoveries < 0 ||
		primaryPurged > finish.CandidatesScanned ||
		finish.PurgedObservations < 0 || finish.PurgedObservations > primaryPurged ||
		finish.PurgedLifecycleEvents < 0 || finish.Skipped < 0 ||
		primaryPurged+finish.Skipped > finish.CandidatesScanned ||
		(finish.Outcome == storage.RetentionCompleted &&
			primaryPurged+finish.Skipped != finish.CandidatesScanned) ||
		finish.Batches < 0 || finish.Batches > storage.MaximumPurgeAttemptsPerRun ||
		(finish.CandidatesScanned == 0 && finish.Batches != 0) ||
		(finish.CandidatesScanned > 0 && (finish.Batches == 0 || finish.Batches > finish.CandidatesScanned)) ||
		!finish.Outcome.Valid() || finish.FinishedUnix < 0 {
		return storage.ErrRetentionWriteInput
	}

	var checkpoint storage.RetentionRunFinish
	var checkpointTruncated int
	var currentOutcome string
	var currentPolicyVersion int
	var finished sql.NullInt64
	if err := repository.database.QueryRowContext(
		ctx,
		`SELECT retention_run_id,
                policy_version,
                candidates_scanned,
                purged_relays,
                purged_discoveries,
                purged_observations,
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
		&currentPolicyVersion,
		&checkpoint.CandidatesScanned,
		&checkpoint.PurgedRelays,
		&checkpoint.PurgedDiscoveries,
		&checkpoint.PurgedObservations,
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
	if currentOutcome == "running" && currentPolicyVersion != storage.RetentionPolicyVersion {
		return storage.ErrRetentionWriteInput
	}
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
		finish.PurgedDiscoveries != checkpoint.PurgedDiscoveries ||
		finish.PurgedObservations != checkpoint.PurgedObservations ||
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
             purged_discoveries = ?,
             purged_observations = ?,
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
		finish.PurgedDiscoveries,
		finish.PurgedObservations,
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
