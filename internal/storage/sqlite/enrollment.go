package sqlite

import (
	"context"
	"database/sql"
	"time"

	"github.com/thystra/activity-relay-directory/internal/storage"
)

// EnrollmentOpen returns the current durable policy. A missing or malformed
// singleton fails closed through ErrStorageFailure.
func (repository *RelayRepository) EnrollmentOpen(ctx context.Context) (bool, error) {
	if repository == nil || repository.database == nil || ctx == nil {
		return false, storage.ErrRepositoryConfiguration
	}
	var open int
	if err := repository.database.QueryRowContext(
		ctx,
		`SELECT enrollment_open FROM directory_policy WHERE singleton = 1`,
	).Scan(&open); err != nil {
		return false, storageFailure("read enrollment policy", err)
	}
	if open != 0 && open != 1 {
		return false, storage.ErrStorageFailure
	}
	return open == 1, nil
}

// SetEnrollment applies or confirms one local policy decision and appends a
// private audit event in the same immediate transaction.
func (repository *RelayRepository) SetEnrollment(
	ctx context.Context,
	open bool,
	intent storage.EnrollmentIntent,
	acceptedAt time.Time,
) (storage.EnrollmentOutcome, error) {
	if !storage.ValidOperatorID(intent.OperatorID) {
		return "", storage.ErrTransitionInput
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

	current, err := selectEnrollmentOpen(ctx, transaction)
	if err != nil {
		return "", storageFailure("read enrollment policy", err)
	}
	if err := requireEnrollmentTime(ctx, transaction, acceptedUnix); err != nil {
		return "", err
	}
	outcome, action := classifyEnrollment(current, open)
	if current != open {
		if _, err := transaction.ExecContext(
			ctx,
			`UPDATE directory_policy
			 SET enrollment_open = ?, updated_at_unix = ?
			 WHERE singleton = 1`,
			boolInteger(open),
			acceptedUnix,
		); err != nil {
			return "", storageFailure("write enrollment policy", err)
		}
	}
	if _, err := transaction.ExecContext(
		ctx,
		`INSERT INTO enrollment_events
		    (action, operator_id, recorded_at_unix)
		 VALUES (?, ?, ?)`,
		action,
		intent.OperatorID,
		acceptedUnix,
	); err != nil {
		return "", storageFailure("write enrollment event", err)
	}
	if err := transaction.Commit(); err != nil {
		return "", storageFailure("commit enrollment policy", err)
	}
	return outcome, nil
}

func requireEnrollmentTime(
	ctx context.Context,
	transaction *sql.Tx,
	acceptedUnix int64,
) error {
	var updatedAt int64
	if err := transaction.QueryRowContext(
		ctx,
		`SELECT updated_at_unix FROM directory_policy WHERE singleton = 1`,
	).Scan(&updatedAt); err != nil {
		return storageFailure("read enrollment policy time", err)
	}
	var latestEvent sql.NullInt64
	if err := transaction.QueryRowContext(
		ctx,
		`SELECT MAX(recorded_at_unix) FROM enrollment_events`,
	).Scan(&latestEvent); err != nil {
		return storageFailure("read enrollment event time", err)
	}
	if latestEvent.Valid && latestEvent.Int64 > updatedAt {
		updatedAt = latestEvent.Int64
	}
	if acceptedUnix < updatedAt {
		return storage.ErrTransitionTime
	}
	return nil
}

func selectEnrollmentOpen(ctx context.Context, transaction *sql.Tx) (bool, error) {
	var open int
	if err := transaction.QueryRowContext(
		ctx,
		`SELECT enrollment_open FROM directory_policy WHERE singleton = 1`,
	).Scan(&open); err != nil {
		return false, err
	}
	if open != 0 && open != 1 {
		return false, storage.ErrStorageFailure
	}
	return open == 1, nil
}

func classifyEnrollment(current, target bool) (storage.EnrollmentOutcome, string) {
	switch {
	case !current && target:
		return storage.EnrollmentOpened, enrollmentOpened
	case current && target:
		return storage.EnrollmentAlreadyOpen, enrollmentOpenUnchanged
	case current && !target:
		return storage.EnrollmentClosed, enrollmentClosed
	default:
		return storage.EnrollmentAlreadyClosed, enrollmentClosedUnchanged
	}
}

func boolInteger(value bool) int {
	if value {
		return 1
	}
	return 0
}
