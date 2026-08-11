package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"time"

	"github.com/thystra/activity-relay-directory/internal/storage"
)

// Suspend applies or confirms administrative suspension for an existing relay.
// It leaves lifecycle state and registration metadata unchanged.
func (repository *RelayRepository) Suspend(
	ctx context.Context,
	intent storage.ModerationIntent,
	acceptedAt time.Time,
) (storage.ModerationOutcome, error) {
	return repository.moderate(
		ctx,
		intent,
		acceptedAt,
		administrativeSuspended,
	)
}

// Restore clears suspension for an existing relay without registering it or
// changing lifecycle state and registration metadata.
func (repository *RelayRepository) Restore(
	ctx context.Context,
	intent storage.ModerationIntent,
	acceptedAt time.Time,
) (storage.ModerationOutcome, error) {
	return repository.moderate(
		ctx,
		intent,
		acceptedAt,
		administrativeActive,
	)
}

func (repository *RelayRepository) moderate(
	ctx context.Context,
	intent storage.ModerationIntent,
	acceptedAt time.Time,
	targetState string,
) (storage.ModerationOutcome, error) {
	if err := validateModerationIntent(intent); err != nil {
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

	relay, err := selectRelay(ctx, transaction, intent.RelayActor)
	if err != nil {
		return "", storageFailure("read relay for moderation", err)
	}
	if relay == nil {
		return "", storage.ErrRelayAbsent
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

	outcome, action, changed, err := classifyModeration(
		relay.administrativeState,
		targetState,
	)
	if err != nil {
		return "", err
	}
	if changed {
		var suspendedAt any
		if targetState == administrativeSuspended {
			suspendedAt = acceptedUnix
		}
		if _, err := transaction.ExecContext(
			ctx,
			`UPDATE relays
			 SET administrative_state = ?,
			     suspended_at_unix = ?,
			     updated_at_unix = ?
			 WHERE relay_actor = ?`,
			targetState,
			suspendedAt,
			acceptedUnix,
			intent.RelayActor,
		); err != nil {
			return "", storageFailure("write relay moderation", err)
		}
	}
	if err := insertModerationEvent(
		ctx,
		transaction,
		intent,
		action,
		acceptedUnix,
	); err != nil {
		return "", err
	}
	if err := transaction.Commit(); err != nil {
		return "", storageFailure("commit relay moderation", err)
	}
	return outcome, nil
}

func classifyModeration(
	currentState string,
	targetState string,
) (storage.ModerationOutcome, string, bool, error) {
	switch {
	case currentState == administrativeActive && targetState == administrativeSuspended:
		return storage.ModerationSuspended, moderationSuspendApplied, true, nil
	case currentState == administrativeSuspended && targetState == administrativeSuspended:
		return storage.ModerationAlreadySuspended, moderationSuspendUnchanged, false, nil
	case currentState == administrativeSuspended && targetState == administrativeActive:
		return storage.ModerationRestored, moderationRestoreApplied, true, nil
	case currentState == administrativeActive && targetState == administrativeActive:
		return storage.ModerationAlreadyActive, moderationRestoreUnchanged, false, nil
	default:
		return "", "", false, storageFailure(
			"classify relay moderation",
			errors.New("invalid administrative state"),
		)
	}
}

func insertModerationEvent(
	ctx context.Context,
	transaction *sql.Tx,
	intent storage.ModerationIntent,
	action string,
	recordedUnix int64,
) error {
	if _, err := transaction.ExecContext(
		ctx,
		`INSERT INTO moderation_events
		    (relay_actor, action, moderator_id, reason_code, recorded_at_unix)
		 VALUES (?, ?, ?, ?, ?)`,
		intent.RelayActor,
		action,
		intent.ModeratorID,
		intent.ReasonCode,
		recordedUnix,
	); err != nil {
		return storageFailure("write moderation event", err)
	}
	return nil
}
