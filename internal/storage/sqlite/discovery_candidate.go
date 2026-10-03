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

var (
	_ storage.DiscoveryCandidateRepository            = (*RelayRepository)(nil)
	_ storage.DiscoveryCandidateMaintenanceRepository = (*RelayRepository)(nil)
)

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
	var failure any
	if intent.Failure != "" {
		failure = string(intent.Failure)
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
		failure,
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

func (repository *RelayRepository) DiscoveryCandidateChecks(
	ctx context.Context,
	query storage.DiscoveryCandidateCheckQuery,
) (storage.DiscoveryCandidateCheckPage, error) {
	if repository == nil || repository.database == nil || ctx == nil ||
		!query.After.Valid() || query.Limit <= 0 || query.Limit > storage.MaximumDiscoveryCandidatePage {
		return storage.DiscoveryCandidateCheckPage{}, storage.ErrDiscoveryCandidateMaintenanceRead
	}
	observedUnix := query.ObservedAt.UTC().Unix()
	if observedUnix < 0 {
		return storage.DiscoveryCandidateCheckPage{}, storage.ErrDiscoveryCandidateMaintenanceRead
	}
	if query.After != (storage.DiscoveryCandidateCheckCursor{}) && query.After.NextCheckUnix > observedUnix {
		return storage.DiscoveryCandidateCheckPage{}, storage.ErrDiscoveryCandidateMaintenanceRead
	}

	rows, err := repository.database.QueryContext(ctx, `WITH due AS (
		SELECT candidate.candidate_actor_url,
		       candidate.public_base_url,
		       candidate.candidate_state,
		       candidate.last_failure_code,
		       candidate.first_seen_at_unix,
		       candidate.last_checked_at_unix,
		       candidate.last_success_at_unix,
		       candidate.failure_count,
		       candidate.last_checked_at_unix + CASE
		         WHEN candidate.failure_count = 1 THEN ?
		         WHEN candidate.failure_count = 2 THEN ?
		         WHEN candidate.failure_count = 3 THEN ?
		         WHEN candidate.failure_count = 4 THEN ?
		         ELSE ?
		       END AS next_check_at_unix,
		       event.operator_id,
		       event.reason_code,
		       event.source_kind,
		       event.source_label
		FROM relay_discovery_candidates AS candidate
		JOIN discovery_candidate_events AS event
		  ON event.discovery_candidate_event_id = (
		      SELECT MIN(first_event.discovery_candidate_event_id)
		      FROM discovery_candidate_events AS first_event
		      WHERE first_event.candidate_actor_url = candidate.candidate_actor_url
		  )
		WHERE candidate.candidate_state != ?
		  AND candidate.failure_count >= 1
	)
	SELECT candidate_actor_url,
	       public_base_url,
	       candidate_state,
	       last_failure_code,
	       first_seen_at_unix,
	       last_checked_at_unix,
	       last_success_at_unix,
	       failure_count,
	       next_check_at_unix,
	       operator_id,
	       reason_code,
	       source_kind,
	       source_label
	FROM due
	WHERE next_check_at_unix <= ?
	  AND (
	    ? = '' OR next_check_at_unix > ? OR
	    (next_check_at_unix = ? AND candidate_actor_url > ?)
	  )
	ORDER BY next_check_at_unix, candidate_actor_url
	LIMIT ?`,
		int64(storage.DiscoveryCandidateRetryOne/time.Second),
		int64(storage.DiscoveryCandidateRetryTwo/time.Second),
		int64(storage.DiscoveryCandidateRetryThree/time.Second),
		int64(storage.DiscoveryCandidateRetryFour/time.Second),
		int64(storage.DiscoveryCandidateRetryLong/time.Second),
		string(storage.DiscoveryCandidateResolved),
		observedUnix,
		query.After.CandidateURL,
		query.After.NextCheckUnix,
		query.After.NextCheckUnix,
		query.After.CandidateURL,
		query.Limit+1,
	)
	if err != nil {
		return storage.DiscoveryCandidateCheckPage{}, storageFailure("read discovery candidate checks", err)
	}
	defer rows.Close()

	page := storage.DiscoveryCandidateCheckPage{
		Candidates: make([]storage.DiscoveryCandidateCheck, 0, query.Limit),
	}
	for rows.Next() {
		var (
			candidate storage.DiscoveryCandidateCheck
			state     string
			failure   string
			success   sql.NullInt64
			source    string
			label     sql.NullString
		)
		if err := rows.Scan(
			&candidate.CandidateActorURL,
			&candidate.PublicBaseURL,
			&state,
			&failure,
			&candidate.FirstSeenUnix,
			&candidate.LastCheckedUnix,
			&success,
			&candidate.FailureCount,
			&candidate.NextCheckUnix,
			&candidate.OperatorID,
			&candidate.ReasonCode,
			&source,
			&label,
		); err != nil {
			return storage.DiscoveryCandidateCheckPage{}, storageFailure("decode discovery candidate check", err)
		}
		candidate.State = storage.DiscoveryCandidateState(state)
		candidate.LastFailure = storage.DiscoveryCandidateFailure(failure)
		candidate.SourceKind = storage.DiscoverySourceKind(source)
		if success.Valid {
			value := success.Int64
			candidate.LastSuccessUnix = &value
		}
		if label.Valid {
			candidate.SourceLabel = label.String
		}
		if !validDiscoveryCandidateCheck(candidate, observedUnix) {
			return storage.DiscoveryCandidateCheckPage{}, storageFailure(
				"validate discovery candidate check",
				errors.New("invalid discovery candidate check row"),
			)
		}
		if len(page.Candidates) == query.Limit {
			last := page.Candidates[len(page.Candidates)-1]
			page.Next = storage.DiscoveryCandidateCheckCursor{
				NextCheckUnix: last.NextCheckUnix,
				CandidateURL:  last.CandidateActorURL,
			}
			break
		}
		page.Candidates = append(page.Candidates, candidate)
	}
	if err := rows.Err(); err != nil {
		return storage.DiscoveryCandidateCheckPage{}, storageFailure("iterate discovery candidate checks", err)
	}
	return page, nil
}

func validDiscoveryCandidateCheck(candidate storage.DiscoveryCandidateCheck, observedUnix int64) bool {
	identity, err := v1.NormalizeRelayIdentity(candidate.CandidateActorURL, candidate.PublicBaseURL)
	if err != nil || identity.RelayActor != candidate.CandidateActorURL ||
		identity.PublicBaseURL != candidate.PublicBaseURL ||
		(candidate.State != storage.DiscoveryCandidateUnreachable &&
			candidate.State != storage.DiscoveryCandidateIncompatible) ||
		!candidate.LastFailure.Valid() || candidate.FailureCount < 1 ||
		candidate.FirstSeenUnix < 0 || candidate.LastCheckedUnix < candidate.FirstSeenUnix ||
		candidate.NextCheckUnix < candidate.LastCheckedUnix || candidate.NextCheckUnix > observedUnix ||
		!storage.ValidOperatorID(candidate.OperatorID) ||
		!storage.ValidModerationReasonCode(candidate.ReasonCode) ||
		!candidate.SourceKind.Valid() || !storage.ValidDiscoverySourceLabel(candidate.SourceLabel) {
		return false
	}
	delay := storage.DiscoveryCandidateRetryDelay(candidate.FailureCount)
	return delay > 0 && candidate.NextCheckUnix == candidate.LastCheckedUnix+int64(delay/time.Second)
}

func (repository *RelayRepository) PromoteDiscoveryCandidate(
	ctx context.Context,
	intent storage.DiscoveryCandidatePromotionIntent,
	observedAt time.Time,
) (storage.DiscoveryCandidatePromotionOutcome, error) {
	if err := validateDiscoveryCandidatePromotionIntent(intent); err != nil {
		return "", err
	}
	observedUnix, err := observationUnix(observedAt)
	if err != nil {
		return "", storage.ErrDiscoveryCandidateMaintenanceWrite
	}
	transaction, lease, err := repository.begin(ctx)
	if err != nil {
		return "", err
	}
	defer lease.Release()
	defer func() { _ = transaction.Rollback() }()

	candidate, err := selectDiscoveryCandidate(ctx, transaction, intent.CandidateActorURL)
	if err != nil {
		return "", storageFailure("read discovery candidate promotion", err)
	}
	if candidate == nil || candidate.state == string(storage.DiscoveryCandidateResolved) ||
		candidate.lastCheckedUnix >= observedUnix {
		return storage.DiscoveryCandidateSkipped, nil
	}

	current, err := selectDiscovery(ctx, transaction, intent.CandidateActorURL)
	if err != nil {
		return "", storageFailure("read promoted discovery", err)
	}
	if err := requireDiscoveryTime(ctx, transaction, intent.CandidateActorURL, observedUnix, current); err != nil {
		if errors.Is(err, storage.ErrTransitionTime) {
			return storage.DiscoveryCandidateSkipped, nil
		}
		return "", err
	}

	discoveryAction := discoveryAdded
	switch {
	case current == nil:
		_, err = transaction.ExecContext(ctx, `INSERT INTO relay_discoveries (
			relay_actor, public_base_url, discovery_state,
			first_discovered_at_unix, updated_at_unix
		) VALUES (?, ?, ?, ?, ?)`,
			intent.CandidateActorURL,
			intent.PublicBaseURL,
			discoveryActive,
			observedUnix,
			observedUnix,
		)
	case current.state == discoveryActive && current.publicBaseURL == intent.PublicBaseURL:
		discoveryAction = discoveryUnchanged
	case current.state == discoveryActive || current.state == discoveryRemoved:
		discoveryAction = discoveryUpdated
		_, err = transaction.ExecContext(ctx, `UPDATE relay_discoveries
			SET public_base_url = ?, discovery_state = ?, updated_at_unix = ?, removed_at_unix = NULL
			WHERE relay_actor = ?`,
			intent.PublicBaseURL,
			discoveryActive,
			observedUnix,
			intent.CandidateActorURL,
		)
	default:
		return "", storageFailure("classify promoted discovery", errors.New("invalid discovery state"))
	}
	if err != nil {
		return "", storageFailure("write promoted discovery", err)
	}
	if err := insertDiscoveryEvent(
		ctx,
		transaction,
		intent.CandidateActorURL,
		discoveryAction,
		intent.OperatorID,
		intent.ReasonCode,
		intent.SourceKind,
		intent.SourceLabel,
		observedUnix,
	); err != nil {
		return "", err
	}

	currentObservation, err := selectObservation(ctx, transaction, intent.CandidateActorURL)
	if err != nil {
		return "", storageFailure("read promoted observation", err)
	}
	if currentObservation == nil {
		if err := insertEmptyObservation(ctx, transaction, intent.CandidateActorURL, observedUnix); err != nil {
			return "", err
		}
		currentObservation, err = selectObservation(ctx, transaction, intent.CandidateActorURL)
		if err != nil || currentObservation == nil {
			if err == nil {
				err = errors.New("observation row missing after promotion insert")
			}
			return "", storageFailure("read promoted observation", err)
		}
	}
	if currentObservation.actorLastChecked.Valid && currentObservation.actorLastChecked.Int64 >= observedUnix {
		return storage.DiscoveryCandidateSkipped, nil
	}

	var inboxURL, inboxDeclared, inboxChecked any
	if intent.InboxURL != "" {
		inboxURL = intent.InboxURL
		inboxDeclared = observedUnix
		inboxChecked = observedUnix
	}
	if _, err := transaction.ExecContext(ctx, `UPDATE relay_observations SET
		actor_state = ?,
		actor_last_checked_at_unix = ?,
		actor_last_success_at_unix = ?,
		inbox_url = ?,
		inbox_declared_at_unix = ?,
		inbox_probe_state = ?,
		inbox_last_checked_at_unix = ?,
		updated_at_unix = ?,
		revision = revision + 1
		WHERE relay_actor = ?`,
		string(storage.ReachabilityReachable),
		observedUnix,
		observedUnix,
		inboxURL,
		inboxDeclared,
		string(intent.InboxState),
		inboxChecked,
		observedUnix,
		intent.CandidateActorURL,
	); err != nil {
		return "", storageFailure("write promoted observation", err)
	}

	if _, err := transaction.ExecContext(ctx, `UPDATE relay_discovery_candidates SET
		candidate_state = ?,
		last_failure_code = NULL,
		last_checked_at_unix = ?,
		last_success_at_unix = ?,
		updated_at_unix = ?
		WHERE candidate_actor_url = ?`,
		string(storage.DiscoveryCandidateResolved),
		observedUnix,
		observedUnix,
		observedUnix,
		intent.CandidateActorURL,
	); err != nil {
		return "", storageFailure("resolve discovery candidate", err)
	}
	resolvedEvent := storage.DiscoveryCandidateIntent{
		CandidateActorURL: intent.CandidateActorURL,
		PublicBaseURL:     intent.PublicBaseURL,
		State:             storage.DiscoveryCandidateResolved,
		OperatorID:        intent.OperatorID,
		ReasonCode:        intent.ReasonCode,
		SourceKind:        intent.SourceKind,
		SourceLabel:       intent.SourceLabel,
	}
	if err := insertDiscoveryCandidateEvent(ctx, transaction, resolvedEvent, "candidate_resolved", observedUnix); err != nil {
		return "", err
	}
	if err := transaction.Commit(); err != nil {
		return "", storageFailure("commit discovery candidate promotion", err)
	}
	return storage.DiscoveryCandidatePromoted, nil
}

func validateDiscoveryCandidatePromotionIntent(intent storage.DiscoveryCandidatePromotionIntent) error {
	identity, err := v1.NormalizeRelayIdentity(intent.CandidateActorURL, intent.PublicBaseURL)
	if err != nil || identity.RelayActor != intent.CandidateActorURL || identity.PublicBaseURL != intent.PublicBaseURL ||
		!storage.ValidOperatorID(intent.OperatorID) || !storage.ValidModerationReasonCode(intent.ReasonCode) ||
		!intent.SourceKind.Valid() || !storage.ValidDiscoverySourceLabel(intent.SourceLabel) {
		return storage.ErrDiscoveryCandidateMaintenanceWrite
	}
	if intent.InboxURL == "" {
		if intent.InboxState != storage.InboxNotChecked {
			return storage.ErrDiscoveryCandidateMaintenanceWrite
		}
		return nil
	}
	canonicalInbox, err := v1.NormalizeRelayActorURL(intent.InboxURL)
	if err != nil || canonicalInbox != intent.InboxURL ||
		(intent.InboxState != storage.InboxResponsive &&
			intent.InboxState != storage.InboxMethodRejected &&
			intent.InboxState != storage.InboxUnreachable) {
		return storage.ErrDiscoveryCandidateMaintenanceWrite
	}
	return nil
}
