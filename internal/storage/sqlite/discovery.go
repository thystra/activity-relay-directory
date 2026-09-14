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
	discoveryActive    = "active"
	discoveryRemoved   = "removed"
	discoveryAdded     = "discovery_added"
	discoveryUpdated   = "discovery_updated"
	discoveryUnchanged = "discovery_unchanged"
	discoveryRemovedOK = "discovery_removed"
	discoveryAbsent    = "discovery_absent"
)

var _ storage.DiscoveryRepository = (*RelayRepository)(nil)
var _ storage.ObservationRepository = (*RelayRepository)(nil)

type discoveryRecord struct {
	publicBaseURL       string
	state               string
	firstDiscoveredUnix int64
	updatedUnix         int64
	removedUnix         sql.NullInt64
}

func (repository *RelayRepository) AddDiscovery(
	ctx context.Context,
	intent storage.DiscoveryAddIntent,
	acceptedAt time.Time,
) (storage.DiscoveryOutcome, error) {
	if err := validateDiscoveryAddIntent(intent); err != nil {
		return "", err
	}
	acceptedUnix, err := transitionUnix(acceptedAt)
	if err != nil {
		return "", err
	}
	transaction, lease, err := repository.begin(ctx)
	if err != nil {
		return "", err
	}
	defer lease.Release()
	defer func() { _ = transaction.Rollback() }()

	current, err := selectDiscovery(ctx, transaction, intent.RelayActor)
	if err != nil {
		return "", storageFailure("read discovery", err)
	}
	if err := requireDiscoveryTime(ctx, transaction, intent.RelayActor, acceptedUnix, current); err != nil {
		return "", err
	}

	outcome := storage.DiscoveryAdded
	action := discoveryAdded
	switch {
	case current == nil:
		_, err = transaction.ExecContext(ctx, `INSERT INTO relay_discoveries (
			relay_actor, public_base_url, discovery_state,
			first_discovered_at_unix, updated_at_unix
		) VALUES (?, ?, ?, ?, ?)`,
			intent.RelayActor, intent.PublicBaseURL, discoveryActive, acceptedUnix, acceptedUnix,
		)
	case current.state == discoveryActive && current.publicBaseURL == intent.PublicBaseURL:
		outcome = storage.DiscoveryUnchanged
		action = discoveryUnchanged
	case current.state == discoveryActive || current.state == discoveryRemoved:
		outcome = storage.DiscoveryUpdated
		action = discoveryUpdated
		_, err = transaction.ExecContext(ctx, `UPDATE relay_discoveries
			SET public_base_url = ?, discovery_state = ?, updated_at_unix = ?, removed_at_unix = NULL
			WHERE relay_actor = ?`,
			intent.PublicBaseURL, discoveryActive, acceptedUnix, intent.RelayActor,
		)
	default:
		return "", storageFailure("classify discovery add", errors.New("invalid discovery state"))
	}
	if err != nil {
		return "", storageFailure("write discovery", err)
	}
	if err := insertDiscoveryEvent(ctx, transaction, intent.RelayActor, action,
		intent.OperatorID, intent.ReasonCode, intent.SourceKind, intent.SourceLabel, acceptedUnix); err != nil {
		return "", err
	}
	if err := transaction.Commit(); err != nil {
		return "", storageFailure("commit discovery add", err)
	}
	return outcome, nil
}

func (repository *RelayRepository) RemoveDiscovery(
	ctx context.Context,
	intent storage.DiscoveryRemoveIntent,
	acceptedAt time.Time,
) (storage.DiscoveryOutcome, error) {
	if err := validateDiscoveryRemoveIntent(intent); err != nil {
		return "", err
	}
	acceptedUnix, err := transitionUnix(acceptedAt)
	if err != nil {
		return "", err
	}
	transaction, lease, err := repository.begin(ctx)
	if err != nil {
		return "", err
	}
	defer lease.Release()
	defer func() { _ = transaction.Rollback() }()

	current, err := selectDiscovery(ctx, transaction, intent.RelayActor)
	if err != nil {
		return "", storageFailure("read discovery", err)
	}
	if err := requireDiscoveryTime(ctx, transaction, intent.RelayActor, acceptedUnix, current); err != nil {
		return "", err
	}

	outcome := storage.DiscoveryAbsent
	action := discoveryAbsent
	if current != nil && current.state == discoveryActive {
		outcome = storage.DiscoveryRemovedOK
		action = discoveryRemovedOK
		if _, err := transaction.ExecContext(ctx, `UPDATE relay_discoveries
			SET discovery_state = ?, updated_at_unix = ?, removed_at_unix = ?
			WHERE relay_actor = ?`,
			discoveryRemoved, acceptedUnix, acceptedUnix, intent.RelayActor,
		); err != nil {
			return "", storageFailure("write discovery removal", err)
		}
	}
	if err := insertDiscoveryEvent(ctx, transaction, intent.RelayActor, action,
		intent.OperatorID, intent.ReasonCode, intent.SourceKind, intent.SourceLabel, acceptedUnix); err != nil {
		return "", err
	}
	if err := transaction.Commit(); err != nil {
		return "", storageFailure("commit discovery removal", err)
	}
	return outcome, nil
}

func (repository *RelayRepository) GetDiscovery(
	ctx context.Context,
	intent storage.IdentityIntent,
) (storage.DiscoveryRecord, bool, error) {
	if repository == nil || repository.database == nil || ctx == nil {
		return storage.DiscoveryRecord{}, false, storage.ErrRepositoryConfiguration
	}
	if err := validateIdentityIntent(intent); err != nil {
		return storage.DiscoveryRecord{}, false, storage.ErrDiscoveryReadInput
	}
	var record storage.DiscoveryRecord
	var state string
	var removed sql.NullInt64
	err := repository.database.QueryRowContext(ctx, `SELECT
		relay_actor, public_base_url, discovery_state,
		first_discovered_at_unix, updated_at_unix, removed_at_unix
		FROM relay_discoveries WHERE relay_actor = ?`, intent.RelayActor).Scan(
		&record.RelayActor, &record.PublicBaseURL, &state,
		&record.FirstDiscoveredUnix, &record.UpdatedUnix, &removed,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return storage.DiscoveryRecord{}, false, nil
	}
	if err != nil {
		return storage.DiscoveryRecord{}, false, storageFailure("read discovery state", err)
	}
	record.State = storage.DiscoveryState(state)
	if removed.Valid {
		value := removed.Int64
		record.RemovedUnix = &value
	}
	if !record.State.Valid() || record.RelayActor != intent.RelayActor {
		return storage.DiscoveryRecord{}, false, storageFailure("validate discovery state", errors.New("invalid discovery row"))
	}
	return record, true, nil
}

func selectDiscovery(ctx context.Context, transaction *sql.Tx, actor string) (*discoveryRecord, error) {
	var record discoveryRecord
	err := transaction.QueryRowContext(ctx, `SELECT
		public_base_url, discovery_state, first_discovered_at_unix, updated_at_unix, removed_at_unix
		FROM relay_discoveries WHERE relay_actor = ?`, actor).Scan(
		&record.publicBaseURL, &record.state, &record.firstDiscoveredUnix, &record.updatedUnix, &record.removedUnix,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &record, nil
}

func requireDiscoveryTime(
	ctx context.Context,
	transaction *sql.Tx,
	actor string,
	acceptedUnix int64,
	current *discoveryRecord,
) error {
	latest := int64(-1)
	if current != nil {
		latest = current.updatedUnix
	}
	var latestEvent sql.NullInt64
	if err := transaction.QueryRowContext(ctx,
		`SELECT MAX(recorded_at_unix) FROM discovery_events WHERE relay_actor = ?`, actor,
	).Scan(&latestEvent); err != nil {
		return storageFailure("read discovery event time", err)
	}
	if latestEvent.Valid && latestEvent.Int64 > latest {
		latest = latestEvent.Int64
	}
	if acceptedUnix < latest {
		return storage.ErrTransitionTime
	}
	return nil
}

func insertDiscoveryEvent(
	ctx context.Context,
	transaction *sql.Tx,
	actor, action, operatorID, reasonCode string,
	sourceKind storage.DiscoverySourceKind,
	sourceLabel string,
	recordedUnix int64,
) error {
	var label any
	if sourceLabel != "" {
		label = sourceLabel
	}
	if _, err := transaction.ExecContext(ctx, `INSERT INTO discovery_events (
		relay_actor, action, operator_id, reason_code, source_kind, source_label, recorded_at_unix
	) VALUES (?, ?, ?, ?, ?, ?, ?)`,
		actor, action, operatorID, reasonCode, string(sourceKind), label, recordedUnix,
	); err != nil {
		return storageFailure("write discovery event", err)
	}
	return nil
}

func validateDiscoveryAddIntent(intent storage.DiscoveryAddIntent) error {
	identity, err := v1.NormalizeRelayIdentity(intent.RelayActor, intent.PublicBaseURL)
	if err != nil || identity.RelayActor != intent.RelayActor || identity.PublicBaseURL != intent.PublicBaseURL ||
		!storage.ValidOperatorID(intent.OperatorID) || !storage.ValidModerationReasonCode(intent.ReasonCode) ||
		!intent.SourceKind.Valid() || !storage.ValidDiscoverySourceLabel(intent.SourceLabel) {
		return storage.ErrTransitionInput
	}
	return nil
}

func validateDiscoveryRemoveIntent(intent storage.DiscoveryRemoveIntent) error {
	if err := validateIdentityIntent(storage.IdentityIntent{RelayActor: intent.RelayActor}); err != nil ||
		!storage.ValidOperatorID(intent.OperatorID) || !storage.ValidModerationReasonCode(intent.ReasonCode) ||
		!intent.SourceKind.Valid() || !storage.ValidDiscoverySourceLabel(intent.SourceLabel) {
		return storage.ErrTransitionInput
	}
	return nil
}

func (repository *RelayRepository) RecordActorObservation(
	ctx context.Context,
	intent storage.ActorObservationIntent,
	observedAt time.Time,
) error {
	if err := validateActorObservationIntent(intent); err != nil {
		return err
	}
	observedUnix, err := observationUnix(observedAt)
	if err != nil {
		return err
	}
	transaction, lease, err := repository.begin(ctx)
	if err != nil {
		return err
	}
	defer lease.Release()
	defer func() { _ = transaction.Rollback() }()
	if ok, err := retainedObservationIdentity(ctx, transaction, intent.RelayActor); err != nil {
		return storageFailure("read observation identity", err)
	} else if !ok {
		return storage.ErrObservationAbsent
	}
	current, err := selectObservation(ctx, transaction, intent.RelayActor)
	if err != nil {
		return storageFailure("read actor observation", err)
	}
	if current != nil && current.actorLastChecked.Valid && observedUnix < current.actorLastChecked.Int64 {
		return storage.ErrObservationTime
	}
	if current == nil {
		if err := insertEmptyObservation(ctx, transaction, intent.RelayActor, observedUnix); err != nil {
			return err
		}
		current, err = selectObservation(ctx, transaction, intent.RelayActor)
		if err != nil || current == nil {
			if err == nil {
				err = errors.New("observation row missing after insert")
			}
			return storageFailure("read inserted actor observation", err)
		}
	}
	updatedUnix := maxInt64(current.updatedUnix, observedUnix)
	if intent.State == storage.ReachabilityReachable {
		var inbox any
		var declared any
		if intent.InboxURL != "" {
			inbox = intent.InboxURL
			declared = observedUnix
		}
		_, err = transaction.ExecContext(ctx, `UPDATE relay_observations SET
			actor_state = ?, actor_last_checked_at_unix = ?, actor_last_success_at_unix = ?,
			inbox_url = ?, inbox_declared_at_unix = ?,
			inbox_probe_state = 'not_checked', inbox_last_checked_at_unix = NULL,
			updated_at_unix = ?, revision = revision + 1
			WHERE relay_actor = ?`,
			string(intent.State), observedUnix, observedUnix, inbox, declared, updatedUnix, intent.RelayActor,
		)
	} else {
		_, err = transaction.ExecContext(ctx, `UPDATE relay_observations SET
			actor_state = ?, actor_last_checked_at_unix = ?, updated_at_unix = ?, revision = revision + 1
			WHERE relay_actor = ?`, string(intent.State), observedUnix, updatedUnix, intent.RelayActor)
	}
	if err != nil {
		return storageFailure("write actor observation", err)
	}
	if err := transaction.Commit(); err != nil {
		return storageFailure("commit actor observation", err)
	}
	return nil
}

func (repository *RelayRepository) RecordInboxObservation(
	ctx context.Context,
	intent storage.InboxObservationIntent,
	observedAt time.Time,
) error {
	if err := validateInboxObservationIntent(intent); err != nil {
		return err
	}
	observedUnix, err := observationUnix(observedAt)
	if err != nil {
		return err
	}
	transaction, lease, err := repository.begin(ctx)
	if err != nil {
		return err
	}
	defer lease.Release()
	defer func() { _ = transaction.Rollback() }()
	current, err := selectObservation(ctx, transaction, intent.RelayActor)
	if err != nil {
		return storageFailure("read inbox observation", err)
	}
	if current == nil || !current.inboxURL.Valid || current.inboxURL.String != intent.InboxURL {
		return storage.ErrObservationConflict
	}
	if (current.inboxDeclared.Valid && observedUnix < current.inboxDeclared.Int64) ||
		(current.inboxLastChecked.Valid && observedUnix < current.inboxLastChecked.Int64) {
		return storage.ErrObservationTime
	}
	updatedUnix := maxInt64(current.updatedUnix, observedUnix)
	if _, err := transaction.ExecContext(ctx, `UPDATE relay_observations SET
		inbox_probe_state = ?, inbox_last_checked_at_unix = ?, updated_at_unix = ?, revision = revision + 1
		WHERE relay_actor = ?`, string(intent.State), observedUnix, updatedUnix, intent.RelayActor); err != nil {
		return storageFailure("write inbox observation", err)
	}
	if err := transaction.Commit(); err != nil {
		return storageFailure("commit inbox observation", err)
	}
	return nil
}

func (repository *RelayRepository) GetObservation(
	ctx context.Context,
	intent storage.IdentityIntent,
) (storage.RelayObservation, bool, error) {
	if repository == nil || repository.database == nil || ctx == nil {
		return storage.RelayObservation{}, false, storage.ErrRepositoryConfiguration
	}
	if err := validateIdentityIntent(intent); err != nil {
		return storage.RelayObservation{}, false, storage.ErrObservationInput
	}
	row, err := selectObservationDB(ctx, repository.database, intent.RelayActor)
	if errors.Is(err, sql.ErrNoRows) {
		return storage.RelayObservation{}, false, nil
	}
	if err != nil {
		return storage.RelayObservation{}, false, storageFailure("read relay observation", err)
	}
	observation, err := decodeObservation(intent.RelayActor, row)
	if err != nil {
		return storage.RelayObservation{}, false, storageFailure("validate relay observation", err)
	}
	return observation, true, nil
}

type observationRecord struct {
	actorState       string
	actorLastChecked sql.NullInt64
	actorLastSuccess sql.NullInt64
	inboxURL         sql.NullString
	inboxDeclared    sql.NullInt64
	inboxProbeState  string
	inboxLastChecked sql.NullInt64
	rfc9421Verified  sql.NullInt64
	updatedUnix      int64
	revision         int64
}

func selectObservation(ctx context.Context, transaction *sql.Tx, actor string) (*observationRecord, error) {
	var row observationRecord
	err := transaction.QueryRowContext(ctx, observationSelectSQL, actor).Scan(
		&row.actorState, &row.actorLastChecked, &row.actorLastSuccess,
		&row.inboxURL, &row.inboxDeclared, &row.inboxProbeState,
		&row.inboxLastChecked, &row.rfc9421Verified, &row.updatedUnix, &row.revision,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &row, nil
}

const observationSelectSQL = `SELECT
	actor_state, actor_last_checked_at_unix, actor_last_success_at_unix,
	inbox_url, inbox_declared_at_unix, inbox_probe_state,
	inbox_last_checked_at_unix, rfc9421_verified_at_unix, updated_at_unix, revision
	FROM relay_observations WHERE relay_actor = ?`

type queryRower interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}

func selectObservationDB(ctx context.Context, database queryRower, actor string) (*observationRecord, error) {
	var row observationRecord
	err := database.QueryRowContext(ctx, observationSelectSQL, actor).Scan(
		&row.actorState, &row.actorLastChecked, &row.actorLastSuccess,
		&row.inboxURL, &row.inboxDeclared, &row.inboxProbeState,
		&row.inboxLastChecked, &row.rfc9421Verified, &row.updatedUnix, &row.revision,
	)
	if err != nil {
		return nil, err
	}
	return &row, nil
}

func decodeObservation(actor string, row *observationRecord) (storage.RelayObservation, error) {
	if row == nil {
		return storage.RelayObservation{}, errors.New("missing observation row")
	}
	result := storage.RelayObservation{
		RelayActor:      actor,
		ActorState:      storage.ReachabilityState(row.actorState),
		InboxProbeState: storage.InboxProbeState(row.inboxProbeState),
		UpdatedUnix:     row.updatedUnix,
	}
	if row.actorLastChecked.Valid {
		value := row.actorLastChecked.Int64
		result.ActorLastCheckedUnix = &value
	}
	if row.actorLastSuccess.Valid {
		value := row.actorLastSuccess.Int64
		result.ActorLastSuccessUnix = &value
	}
	if row.inboxURL.Valid {
		result.InboxURL = row.inboxURL.String
	}
	if row.inboxDeclared.Valid {
		value := row.inboxDeclared.Int64
		result.InboxDeclaredUnix = &value
	}
	if row.inboxLastChecked.Valid {
		value := row.inboxLastChecked.Int64
		result.InboxLastCheckedUnix = &value
	}
	if row.rfc9421Verified.Valid {
		value := row.rfc9421Verified.Int64
		result.RFC9421VerifiedUnix = &value
	}
	if !result.ActorState.Valid() || !result.InboxProbeState.Valid() || result.UpdatedUnix < 0 || row.revision < 1 {
		return storage.RelayObservation{}, errors.New("invalid observation row")
	}
	return result, nil
}

func retainedObservationIdentity(ctx context.Context, transaction *sql.Tx, actor string) (bool, error) {
	var count int
	if err := transaction.QueryRowContext(ctx, `SELECT
		(SELECT COUNT(*) FROM relays WHERE relay_actor = ?) +
		(SELECT COUNT(*) FROM relay_discoveries WHERE relay_actor = ?)`, actor, actor).Scan(&count); err != nil {
		return false, err
	}
	return count > 0, nil
}

func insertEmptyObservation(ctx context.Context, transaction *sql.Tx, actor string, updatedUnix int64) error {
	if _, err := transaction.ExecContext(ctx, `INSERT INTO relay_observations (
		relay_actor, actor_state, inbox_probe_state, updated_at_unix
	) VALUES (?, 'unknown', 'not_checked', ?) ON CONFLICT(relay_actor) DO NOTHING`, actor, updatedUnix); err != nil {
		return storageFailure("create relay observation", err)
	}
	return nil
}

func recordRFC9421VerifiedTx(ctx context.Context, transaction *sql.Tx, actor string, verifiedUnix int64) error {
	current, err := selectObservation(ctx, transaction, actor)
	if err != nil {
		return storageFailure("read RFC 9421 observation", err)
	}
	if current == nil {
		if err := insertEmptyObservation(ctx, transaction, actor, verifiedUnix); err != nil {
			return err
		}
		current, err = selectObservation(ctx, transaction, actor)
		if err != nil || current == nil {
			if err == nil {
				err = errors.New("observation row missing after RFC 9421 insert")
			}
			return storageFailure("read inserted RFC 9421 observation", err)
		}
	}
	if current.rfc9421Verified.Valid && verifiedUnix < current.rfc9421Verified.Int64 {
		return storage.ErrObservationTime
	}
	updatedUnix := maxInt64(current.updatedUnix, verifiedUnix)
	if _, err := transaction.ExecContext(ctx, `UPDATE relay_observations
		SET rfc9421_verified_at_unix = ?, updated_at_unix = ?, revision = revision + 1
		WHERE relay_actor = ?`, verifiedUnix, updatedUnix, actor); err != nil {
		return storageFailure("write RFC 9421 observation", err)
	}
	return nil
}

func validateActorObservationIntent(intent storage.ActorObservationIntent) error {
	if err := validateIdentityIntent(storage.IdentityIntent{RelayActor: intent.RelayActor}); err != nil ||
		(intent.State != storage.ReachabilityReachable && intent.State != storage.ReachabilityUnreachable) {
		return storage.ErrObservationInput
	}
	if intent.State == storage.ReachabilityUnreachable && intent.InboxURL != "" {
		return storage.ErrObservationInput
	}
	if intent.InboxURL != "" {
		canonical, err := v1.NormalizeRelayActorURL(intent.InboxURL)
		if err != nil || canonical != intent.InboxURL || len(intent.InboxURL) > maximumRelayActorBytes {
			return storage.ErrObservationInput
		}
	}
	return nil
}

func validateInboxObservationIntent(intent storage.InboxObservationIntent) error {
	if err := validateIdentityIntent(storage.IdentityIntent{RelayActor: intent.RelayActor}); err != nil ||
		intent.State == storage.InboxNotChecked || !intent.State.Valid() || intent.InboxURL == "" {
		return storage.ErrObservationInput
	}
	canonical, err := v1.NormalizeRelayActorURL(intent.InboxURL)
	if err != nil || canonical != intent.InboxURL || len(intent.InboxURL) > maximumRelayActorBytes {
		return storage.ErrObservationInput
	}
	return nil
}

func observationUnix(observedAt time.Time) (int64, error) {
	value := observedAt.UTC().Unix()
	if value < 0 {
		return 0, storage.ErrObservationTime
	}
	return value, nil
}

func maxInt64(left, right int64) int64 {
	if left > right {
		return left
	}
	return right
}
