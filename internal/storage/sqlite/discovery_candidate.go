package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"time"

	v1 "github.com/thystra/activity-relay-directory/internal/protocol/v1"
	"github.com/thystra/activity-relay-directory/internal/storage"
)

const (
	discoveryCandidateAdded   = "candidate_added"
	discoveryCandidateUpdated = "candidate_updated"
)

var _ storage.DiscoveryCandidateRepository = (*RelayRepository)(nil)

type discoveryCandidateRecord struct {
	publicBaseURL   string
	state           string
	lastFailure     sql.NullString
	firstSeenUnix   int64
	lastCheckedUnix int64
	lastSuccessUnix sql.NullInt64
	failureCount    int64
	updatedUnix     int64
}

func (repository *RelayRepository) RetainDiscoveryCandidate(
	ctx context.Context,
	intent storage.DiscoveryCandidateIntent,
	acceptedAt time.Time,
) (storage.DiscoveryCandidateOutcome, error) {
	if err := validateDiscoveryCandidateIntent(intent); err != nil {
		return "", err
	}
	acceptedUnix, err := transitionUnix(acceptedAt)
	if err != nil {
		return "", storage.ErrDiscoveryCandidateTime
	}
	transaction, lease, err := repository.begin(ctx)
	if err != nil {
		return "", err
	}
	defer lease.Release()
	defer func() { _ = transaction.Rollback() }()

	current, err := selectDiscoveryCandidate(ctx, transaction, intent.CandidateActorURL)
	if err != nil {
		return "", storageFailure("read discovery candidate", err)
	}
	if current != nil && acceptedUnix < current.lastCheckedUnix {
		return "", storage.ErrDiscoveryCandidateTime
	}

	outcome := storage.DiscoveryCandidateAdded
	action := discoveryCandidateAdded
	if current == nil {
		if _, err := transaction.ExecContext(ctx, `INSERT INTO relay_discovery_candidates (
			candidate_actor_url, public_base_url, candidate_state, last_failure_code,
			first_seen_at_unix, last_checked_at_unix, failure_count, updated_at_unix
		) VALUES (?, ?, ?, ?, ?, ?, 1, ?)`,
			intent.CandidateActorURL,
			intent.PublicBaseURL,
			string(intent.State),
			string(intent.Failure),
			acceptedUnix,
			acceptedUnix,
			acceptedUnix,
		); err != nil {
			return "", storageFailure("write discovery candidate", err)
		}
	} else {
		outcome = storage.DiscoveryCandidateUpdated
		action = discoveryCandidateUpdated
		if _, err := transaction.ExecContext(ctx, `UPDATE relay_discovery_candidates SET
			public_base_url = ?,
			candidate_state = ?,
			last_failure_code = ?,
			last_checked_at_unix = ?,
			failure_count = failure_count + 1,
			updated_at_unix = ?
			WHERE candidate_actor_url = ?`,
			intent.PublicBaseURL,
			string(intent.State),
			string(intent.Failure),
			acceptedUnix,
			acceptedUnix,
			intent.CandidateActorURL,
		); err != nil {
			return "", storageFailure("update discovery candidate", err)
		}
	}

	if err := insertDiscoveryCandidateEvent(
		ctx,
		transaction,
		intent,
		action,
		acceptedUnix,
	); err != nil {
		return "", err
	}
	if err := transaction.Commit(); err != nil {
		return "", storageFailure("commit discovery candidate", err)
	}
	return outcome, nil
}

func (repository *RelayRepository) GetDiscoveryCandidate(
	ctx context.Context,
	candidateActorURL string,
) (storage.DiscoveryCandidateRecord, bool, error) {
	if repository == nil || repository.database == nil || ctx == nil {
		return storage.DiscoveryCandidateRecord{}, false, storage.ErrRepositoryConfiguration
	}
	if !validDiscoveryCandidateActor(candidateActorURL) {
		return storage.DiscoveryCandidateRecord{}, false, storage.ErrDiscoveryCandidateInput
	}

	var record storage.DiscoveryCandidateRecord
	var state string
	var failure sql.NullString
	var success sql.NullInt64
	err := repository.database.QueryRowContext(ctx, `SELECT
		candidate_actor_url,
		public_base_url,
		candidate_state,
		last_failure_code,
		first_seen_at_unix,
		last_checked_at_unix,
		last_success_at_unix,
		failure_count,
		updated_at_unix
		FROM relay_discovery_candidates
		WHERE candidate_actor_url = ?`,
		candidateActorURL,
	).Scan(
		&record.CandidateActorURL,
		&record.PublicBaseURL,
		&state,
		&failure,
		&record.FirstSeenUnix,
		&record.LastCheckedUnix,
		&success,
		&record.FailureCount,
		&record.UpdatedUnix,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return storage.DiscoveryCandidateRecord{}, false, nil
	}
	if err != nil {
		return storage.DiscoveryCandidateRecord{}, false, storageFailure("read discovery candidate state", err)
	}

	record.State = storage.DiscoveryCandidateState(state)
	if failure.Valid {
		record.LastFailure = storage.DiscoveryCandidateFailure(failure.String)
	}
	if success.Valid {
		value := success.Int64
		record.LastSuccessUnix = &value
	}
	if record.CandidateActorURL != candidateActorURL ||
		!record.State.Valid() ||
		(record.LastFailure != "" && !record.LastFailure.Valid()) ||
		record.FirstSeenUnix < 0 ||
		record.LastCheckedUnix < record.FirstSeenUnix ||
		record.FailureCount < 0 ||
		record.UpdatedUnix < record.LastCheckedUnix {
		return storage.DiscoveryCandidateRecord{}, false, storageFailure(
			"validate discovery candidate state",
			errors.New("invalid discovery candidate row"),
		)
	}
	return record, true, nil
}

func selectDiscoveryCandidate(
	ctx context.Context,
	transaction *sql.Tx,
	candidateActorURL string,
) (*discoveryCandidateRecord, error) {
	var record discoveryCandidateRecord
	err := transaction.QueryRowContext(ctx, `SELECT
		public_base_url,
		candidate_state,
		last_failure_code,
		first_seen_at_unix,
		last_checked_at_unix,
		last_success_at_unix,
		failure_count,
		updated_at_unix
		FROM relay_discovery_candidates
		WHERE candidate_actor_url = ?`,
		candidateActorURL,
	).Scan(
		&record.publicBaseURL,
		&record.state,
		&record.lastFailure,
		&record.firstSeenUnix,
		&record.lastCheckedUnix,
		&record.lastSuccessUnix,
		&record.failureCount,
		&record.updatedUnix,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &record, nil
}

func insertDiscoveryCandidateEvent(
	ctx context.Context,
	transaction *sql.Tx,
	intent storage.DiscoveryCandidateIntent,
	action string,
	recordedUnix int64,
) error {
	var label any
	if intent.SourceLabel != "" {
		label = intent.SourceLabel
	}
	if _, err := transaction.ExecContext(ctx, `INSERT INTO discovery_candidate_events (
		candidate_actor_url,
		action,
		candidate_state,
		failure_code,
		operator_id,
		reason_code,
		source_kind,
		source_label,
		recorded_at_unix
	) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		intent.CandidateActorURL,
		action,
		string(intent.State),
		string(intent.Failure),
		intent.OperatorID,
		intent.ReasonCode,
		string(intent.SourceKind),
		label,
		recordedUnix,
	); err != nil {
		return storageFailure("write discovery candidate event", err)
	}
	return nil
}

func validateDiscoveryCandidateIntent(intent storage.DiscoveryCandidateIntent) error {
	identity, err := v1.NormalizeRelayIdentity(
		intent.CandidateActorURL,
		intent.PublicBaseURL,
	)
	if err != nil ||
		identity.RelayActor != intent.CandidateActorURL ||
		identity.PublicBaseURL != intent.PublicBaseURL ||
		!intent.State.Valid() ||
		intent.State == storage.DiscoveryCandidateResolved ||
		!intent.Failure.Valid() ||
		!storage.ValidOperatorID(intent.OperatorID) ||
		!storage.ValidModerationReasonCode(intent.ReasonCode) ||
		!intent.SourceKind.Valid() ||
		!storage.ValidDiscoverySourceLabel(intent.SourceLabel) {
		return storage.ErrDiscoveryCandidateInput
	}
	switch intent.State {
	case storage.DiscoveryCandidateUnreachable:
		if intent.Failure != storage.DiscoveryCandidateActorUnreachable {
			return storage.ErrDiscoveryCandidateInput
		}
	case storage.DiscoveryCandidateIncompatible:
		if intent.Failure != storage.DiscoveryCandidateActorInvalid {
			return storage.ErrDiscoveryCandidateInput
		}
	default:
		return storage.ErrDiscoveryCandidateInput
	}
	return nil
}

func validDiscoveryCandidateActor(candidateActorURL string) bool {
	canonical, err := v1.NormalizeRelayActorURL(candidateActorURL)
	return err == nil && canonical == candidateActorURL
}
