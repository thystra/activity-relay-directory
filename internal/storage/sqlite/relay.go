package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	v1 "github.com/thystra/activity-relay-directory/internal/protocol/v1"
	"github.com/thystra/activity-relay-directory/internal/storage"
)

const (
	maximumRelayActorBytes     = 4096
	maximumPublicBaseBytes     = 2048
	maximumReasonCodeBytes     = storage.MaximumModerationReasonCodeBytes
	lifecycleRegistered        = "registered"
	lifecycleUnregistered      = "unregistered"
	administrativeActive       = "active"
	administrativeSuspended    = "suspended"
	eventRegisterCreated       = "register_created"
	eventRegisterUpdated       = "register_updated"
	eventRegisterUnchanged     = "register_unchanged"
	eventHeartbeatRecorded     = "heartbeat_recorded"
	eventUnregisterRemoved     = "unregister_removed"
	eventUnregisterAbsent      = "unregister_absent"
	moderationSuspendApplied   = "suspend_applied"
	moderationSuspendUnchanged = "suspend_unchanged"
	moderationRestoreApplied   = "restore_applied"
	moderationRestoreUnchanged = "restore_unchanged"
	enrollmentOpened           = "enrollment_opened"
	enrollmentOpenUnchanged    = "enrollment_open_unchanged"
	enrollmentClosed           = "enrollment_closed"
	enrollmentClosedUnchanged  = "enrollment_closed_unchanged"
)

// RelayRepository applies authenticated relay lifecycle intents to SQLite.
// It does not authenticate requests, resolve actors, or expose HTTP handlers.
type RelayRepository struct {
	database *sql.DB
}

var _ storage.RelayRepository = (*RelayRepository)(nil)
var _ storage.ModerationRepository = (*RelayRepository)(nil)
var _ storage.EnrollmentRepository = (*RelayRepository)(nil)

// NewRelayRepository binds relay state transitions to a migrated database.
func NewRelayRepository(database *sql.DB) (*RelayRepository, error) {
	if database == nil {
		return nil, storage.ErrRepositoryConfiguration
	}
	return &RelayRepository{database: database}, nil
}

// Register creates, restores, or confirms a relay registration. It never
// clears administrative suspension and restores no pre-unregister heartbeat.
func (repository *RelayRepository) Register(
	ctx context.Context,
	intent storage.RegisterIntent,
	acceptedAt time.Time,
) (v1.Outcome, error) {
	if err := validateRegisterIntent(intent); err != nil {
		return "", err
	}
	acceptedUnix, err := transitionUnix(acceptedAt)
	if err != nil {
		return "", err
	}

	transaction, err := repository.begin(ctx)
	if err != nil {
		return "", err
	}
	defer func() { _ = transaction.Rollback() }()

	relay, err := selectRelay(ctx, transaction, intent.RelayActor)
	if err != nil {
		return "", storageFailure("read relay", err)
	}
	if relay != nil && relay.administrativeState == administrativeSuspended {
		return "", storage.ErrRelaySuspended
	}
	if relay == nil {
		enrollmentOpen, err := selectEnrollmentOpen(ctx, transaction)
		if err != nil {
			return "", storageFailure("read enrollment policy", err)
		}
		if !enrollmentOpen {
			return "", storage.ErrEnrollmentClosed
		}
	}
	if err := requireMonotonicTime(
		ctx,
		transaction,
		intent.RelayActor,
		acceptedUnix,
		relay,
	); err != nil {
		return "", err
	}

	outcome := v1.OutcomeCreated
	eventKind := eventRegisterCreated
	switch {
	case relay == nil:
		_, err = transaction.ExecContext(
			ctx,
			`INSERT INTO relays (
			    relay_actor,
			    public_base_url,
			    lifecycle_state,
			    administrative_state,
			    first_registered_at_unix,
			    updated_at_unix
			) VALUES (?, ?, ?, ?, ?, ?)`,
			intent.RelayActor,
			intent.PublicBaseURL,
			lifecycleRegistered,
			administrativeActive,
			acceptedUnix,
			acceptedUnix,
		)
	case relay.lifecycleState == lifecycleRegistered &&
		relay.publicBaseURL == intent.PublicBaseURL:
		outcome = v1.OutcomeUnchanged
		eventKind = eventRegisterUnchanged
	case relay.lifecycleState == lifecycleUnregistered:
		outcome = v1.OutcomeUpdated
		eventKind = eventRegisterUpdated
		_, err = transaction.ExecContext(
			ctx,
			`UPDATE relays
			 SET public_base_url = ?,
			     lifecycle_state = ?,
			     updated_at_unix = ?,
			     last_heartbeat_at_unix = NULL,
			     unregistered_at_unix = NULL
			 WHERE relay_actor = ?`,
			intent.PublicBaseURL,
			lifecycleRegistered,
			acceptedUnix,
			intent.RelayActor,
		)
	default:
		return "", storageFailure("classify registration", errors.New("invalid relay state"))
	}
	if err != nil {
		return "", storageFailure("write relay", err)
	}
	if err := insertRelayEvent(
		ctx,
		transaction,
		intent.RelayActor,
		eventKind,
		acceptedUnix,
	); err != nil {
		return "", err
	}
	if err := transaction.Commit(); err != nil {
		return "", storageFailure("commit register", err)
	}
	return outcome, nil
}

// Heartbeat records liveness only for an active registered relay.
func (repository *RelayRepository) Heartbeat(
	ctx context.Context,
	intent storage.IdentityIntent,
	acceptedAt time.Time,
) (v1.Outcome, error) {
	if err := validateIdentityIntent(intent); err != nil {
		return "", err
	}
	acceptedUnix, err := transitionUnix(acceptedAt)
	if err != nil {
		return "", err
	}

	transaction, err := repository.begin(ctx)
	if err != nil {
		return "", err
	}
	defer func() { _ = transaction.Rollback() }()

	relay, err := selectRelay(ctx, transaction, intent.RelayActor)
	if err != nil {
		return "", storageFailure("read relay", err)
	}
	if relay == nil || relay.lifecycleState != lifecycleRegistered {
		return "", storage.ErrRelayAbsent
	}
	if relay.administrativeState == administrativeSuspended {
		return "", storage.ErrRelaySuspended
	}
	if err := requireMonotonicTime(
		ctx,
		transaction,
		intent.RelayActor,
		acceptedUnix,
		relay,
	); err != nil {
		return "", err
	}

	if _, err := transaction.ExecContext(
		ctx,
		`UPDATE relays
		 SET updated_at_unix = ?, last_heartbeat_at_unix = ?
		 WHERE relay_actor = ?`,
		acceptedUnix,
		acceptedUnix,
		intent.RelayActor,
	); err != nil {
		return "", storageFailure("write heartbeat", err)
	}
	if err := insertRelayEvent(
		ctx,
		transaction,
		intent.RelayActor,
		eventHeartbeatRecorded,
		acceptedUnix,
	); err != nil {
		return "", err
	}
	if err := transaction.Commit(); err != nil {
		return "", storageFailure("commit heartbeat", err)
	}
	return v1.OutcomeRecorded, nil
}

// Unregister transitions an active listing once and returns absent thereafter.
// Suspension and its timestamp are deliberately preserved.
func (repository *RelayRepository) Unregister(
	ctx context.Context,
	intent storage.IdentityIntent,
	acceptedAt time.Time,
) (v1.Outcome, error) {
	if err := validateIdentityIntent(intent); err != nil {
		return "", err
	}
	acceptedUnix, err := transitionUnix(acceptedAt)
	if err != nil {
		return "", err
	}

	transaction, err := repository.begin(ctx)
	if err != nil {
		return "", err
	}
	defer func() { _ = transaction.Rollback() }()

	relay, err := selectRelay(ctx, transaction, intent.RelayActor)
	if err != nil {
		return "", storageFailure("read relay", err)
	}
	if err := requireMonotonicTime(
		ctx,
		transaction,
		intent.RelayActor,
		acceptedUnix,
		relay,
	); err != nil {
		return "", err
	}

	outcome := v1.OutcomeAbsent
	eventKind := eventUnregisterAbsent
	if relay != nil && relay.lifecycleState == lifecycleRegistered {
		outcome = v1.OutcomeRemoved
		eventKind = eventUnregisterRemoved
		if _, err := transaction.ExecContext(
			ctx,
			`UPDATE relays
			 SET lifecycle_state = ?,
			     updated_at_unix = ?,
			     unregistered_at_unix = ?
			 WHERE relay_actor = ?`,
			lifecycleUnregistered,
			acceptedUnix,
			acceptedUnix,
			intent.RelayActor,
		); err != nil {
			return "", storageFailure("write unregister", err)
		}
	}
	if err := insertRelayEvent(
		ctx,
		transaction,
		intent.RelayActor,
		eventKind,
		acceptedUnix,
	); err != nil {
		return "", err
	}
	if err := transaction.Commit(); err != nil {
		return "", storageFailure("commit unregister", err)
	}
	return outcome, nil
}

type relayRecord struct {
	publicBaseURL       string
	lifecycleState      string
	administrativeState string
	updatedAtUnix       int64
}

func (repository *RelayRepository) begin(ctx context.Context) (*sql.Tx, error) {
	if repository == nil || repository.database == nil || ctx == nil {
		return nil, storage.ErrRepositoryConfiguration
	}
	transaction, err := repository.database.BeginTx(ctx, nil)
	if err != nil {
		return nil, storageFailure("begin transition", err)
	}
	return transaction, nil
}

func selectRelay(
	ctx context.Context,
	transaction *sql.Tx,
	relayActor string,
) (*relayRecord, error) {
	var relay relayRecord
	err := transaction.QueryRowContext(
		ctx,
		`SELECT public_base_url,
		        lifecycle_state,
		        administrative_state,
		        updated_at_unix
		 FROM relays
		 WHERE relay_actor = ?`,
		relayActor,
	).Scan(
		&relay.publicBaseURL,
		&relay.lifecycleState,
		&relay.administrativeState,
		&relay.updatedAtUnix,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &relay, nil
}

func requireMonotonicTime(
	ctx context.Context,
	transaction *sql.Tx,
	relayActor string,
	acceptedUnix int64,
	relay *relayRecord,
) error {
	latest := int64(-1)
	if relay != nil {
		latest = relay.updatedAtUnix
	}
	var latestEvent sql.NullInt64
	if err := transaction.QueryRowContext(
		ctx,
		`SELECT MAX(recorded_at_unix)
		 FROM relay_events
		 WHERE relay_actor = ?`,
		relayActor,
	).Scan(&latestEvent); err != nil {
		return storageFailure("read relay event time", err)
	}
	if latestEvent.Valid && latestEvent.Int64 > latest {
		latest = latestEvent.Int64
	}
	var latestModeration sql.NullInt64
	if err := transaction.QueryRowContext(
		ctx,
		`SELECT MAX(recorded_at_unix)
		 FROM moderation_events
		 WHERE relay_actor = ?`,
		relayActor,
	).Scan(&latestModeration); err != nil {
		return storageFailure("read moderation event time", err)
	}
	if latestModeration.Valid && latestModeration.Int64 > latest {
		latest = latestModeration.Int64
	}
	if acceptedUnix < latest {
		return storage.ErrTransitionTime
	}
	return nil
}

func insertRelayEvent(
	ctx context.Context,
	transaction *sql.Tx,
	relayActor string,
	eventKind string,
	recordedUnix int64,
) error {
	if _, err := transaction.ExecContext(
		ctx,
		`INSERT INTO relay_events
		    (relay_actor, event_kind, recorded_at_unix)
		 VALUES (?, ?, ?)`,
		relayActor,
		eventKind,
		recordedUnix,
	); err != nil {
		return storageFailure("write relay event", err)
	}
	return nil
}

func validateRegisterIntent(intent storage.RegisterIntent) error {
	if len(intent.RelayActor) == 0 ||
		len(intent.RelayActor) > maximumRelayActorBytes ||
		len(intent.PublicBaseURL) == 0 ||
		len(intent.PublicBaseURL) > maximumPublicBaseBytes {
		return storage.ErrTransitionInput
	}
	identity, err := v1.NormalizeRelayIdentity(
		intent.RelayActor,
		intent.PublicBaseURL,
	)
	if err != nil || identity.RelayActor != intent.RelayActor ||
		identity.PublicBaseURL != intent.PublicBaseURL {
		return storage.ErrTransitionInput
	}
	return nil
}

func validateIdentityIntent(intent storage.IdentityIntent) error {
	if len(intent.RelayActor) == 0 || len(intent.RelayActor) > maximumRelayActorBytes {
		return storage.ErrTransitionInput
	}
	actor, err := v1.NormalizeRelayActorURL(intent.RelayActor)
	if err != nil || actor != intent.RelayActor {
		return storage.ErrTransitionInput
	}
	return nil
}

func validateModerationIntent(intent storage.ModerationIntent) error {
	if err := validateIdentityIntent(storage.IdentityIntent{
		RelayActor: intent.RelayActor,
	}); err != nil {
		return err
	}
	if !storage.ValidOperatorID(intent.ModeratorID) ||
		!storage.ValidModerationReasonCode(intent.ReasonCode) {
		return storage.ErrTransitionInput
	}
	return nil
}

func transitionUnix(acceptedAt time.Time) (int64, error) {
	acceptedUnix := acceptedAt.UTC().Unix()
	if acceptedUnix < 0 {
		return 0, storage.ErrTransitionTime
	}
	return acceptedUnix, nil
}

func storageFailure(action string, err error) error {
	return fmt.Errorf("%w: %s: %w", storage.ErrStorageFailure, action, err)
}
