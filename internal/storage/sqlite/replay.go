package sqlite

import (
	"context"
	"database/sql"
	"fmt"
	"time"

	v1 "github.com/thystra/activity-relay-directory/internal/protocol/v1"
	"github.com/thystra/activity-relay-directory/internal/storage"
)

const (
	defaultRFC9421ReplayCleanupBatch = 256
	maximumRFC9421ReplayCleanupBatch = 4096
)

// RFC9421ReplayStore durably reserves opaque key-ID/nonce digests in SQLite.
// It stores no raw signature identifier.
type RFC9421ReplayStore struct {
	database       *sql.DB
	writeAdmission storage.WriteAdmission
	now            func() time.Time
	cleanupBatch   int
}

var _ v1.RFC9421ReplayStore = (*RFC9421ReplayStore)(nil)

// NewRFC9421ReplayStore creates a durable replay store with bounded cleanup.
func NewRFC9421ReplayStore(
	database *sql.DB,
	writeAdmission storage.WriteAdmission,
) (*RFC9421ReplayStore, error) {
	return newRFC9421ReplayStore(
		database,
		writeAdmission,
		time.Now,
		defaultRFC9421ReplayCleanupBatch,
	)
}

func newRFC9421ReplayStore(
	database *sql.DB,
	writeAdmission storage.WriteAdmission,
	now func() time.Time,
	cleanupBatch int,
) (*RFC9421ReplayStore, error) {
	if database == nil || writeAdmission == nil || now == nil || cleanupBatch <= 0 ||
		cleanupBatch > maximumRFC9421ReplayCleanupBatch {
		return nil, v1.ErrRFC9421ReplayStore
	}
	return &RFC9421ReplayStore{
		database:       database,
		writeAdmission: writeAdmission,
		now:            now,
		cleanupBatch:   cleanupBatch,
	}, nil
}

// ReserveRFC9421Replay atomically reserves key until expiresAt. An unexpired
// primary-key conflict returns false without changing that reservation.
func (store *RFC9421ReplayStore) ReserveRFC9421Replay(
	ctx context.Context,
	key v1.RFC9421ReplayKey,
	expiresAt time.Time,
) (bool, error) {
	now, expiresUnix, err := store.validate(ctx, expiresAt)
	if err != nil {
		return false, err
	}

	lease, err := store.writeAdmission.AcquireWrite(ctx)
	if err != nil {
		return false, fmt.Errorf("%w: %w", v1.ErrRFC9421ReplayStore, err)
	}
	defer lease.Release()
	transaction, err := store.database.BeginTx(ctx, nil)
	if err != nil {
		return false, replayStoreFailure("begin reservation", err)
	}
	defer func() { _ = transaction.Rollback() }()

	if _, err := transaction.ExecContext(
		ctx,
		`DELETE FROM replay_reservations
		 WHERE replay_key = ? AND expires_at_unix <= ?`,
		key[:],
		now.Unix(),
	); err != nil {
		return false, replayStoreFailure("remove expired replay key", err)
	}
	if _, err := cleanupExpiredRFC9421Replay(
		ctx,
		transaction,
		now.Unix(),
		store.cleanupBatch,
	); err != nil {
		return false, err
	}

	result, err := transaction.ExecContext(
		ctx,
		`INSERT INTO replay_reservations
		    (replay_key, reserved_at_unix, expires_at_unix)
		 VALUES (?, ?, ?)
		 ON CONFLICT(replay_key) DO NOTHING`,
		key[:],
		now.Unix(),
		expiresUnix,
	)
	if err != nil {
		return false, replayStoreFailure("write replay reservation", err)
	}
	rowsAffected, err := result.RowsAffected()
	if err != nil {
		return false, replayStoreFailure("classify replay reservation", err)
	}
	if rowsAffected != 0 && rowsAffected != 1 {
		return false, replayStoreFailure(
			"classify replay reservation",
			fmt.Errorf("unexpected rows affected: %d", rowsAffected),
		)
	}
	if err := transaction.Commit(); err != nil {
		return false, replayStoreFailure("commit replay reservation", err)
	}
	return rowsAffected == 1, nil
}

// CleanupExpiredRFC9421Replay deletes at most maximum expired reservations.
// It is suitable for later bounded maintenance scheduling; reservations also
// perform one default-size cleanup batch.
func (store *RFC9421ReplayStore) CleanupExpiredRFC9421Replay(
	ctx context.Context,
	maximum int,
) (int64, error) {
	if store == nil || store.database == nil || store.writeAdmission == nil || store.now == nil ||
		ctx == nil || maximum <= 0 ||
		maximum > maximumRFC9421ReplayCleanupBatch {
		return 0, v1.ErrRFC9421ReplayStore
	}
	select {
	case <-ctx.Done():
		return 0, replayStoreFailure("cancel replay cleanup", ctx.Err())
	default:
	}
	now := time.Unix(store.now().UTC().Unix(), 0)
	if now.Unix() < 0 {
		return 0, v1.ErrRFC9421ReplayStore
	}

	lease, err := store.writeAdmission.AcquireWrite(ctx)
	if err != nil {
		return 0, fmt.Errorf("%w: %w", v1.ErrRFC9421ReplayStore, err)
	}
	defer lease.Release()
	transaction, err := store.database.BeginTx(ctx, nil)
	if err != nil {
		return 0, replayStoreFailure("begin replay cleanup", err)
	}
	defer func() { _ = transaction.Rollback() }()

	deleted, err := cleanupExpiredRFC9421Replay(
		ctx,
		transaction,
		now.Unix(),
		maximum,
	)
	if err != nil {
		return 0, err
	}
	if err := transaction.Commit(); err != nil {
		return 0, replayStoreFailure("commit replay cleanup", err)
	}
	return deleted, nil
}

func (store *RFC9421ReplayStore) validate(
	ctx context.Context,
	expiresAt time.Time,
) (time.Time, int64, error) {
	if store == nil || store.database == nil || store.writeAdmission == nil || store.now == nil ||
		store.cleanupBatch <= 0 ||
		store.cleanupBatch > maximumRFC9421ReplayCleanupBatch || ctx == nil {
		return time.Time{}, 0, v1.ErrRFC9421ReplayStore
	}
	select {
	case <-ctx.Done():
		return time.Time{}, 0, replayStoreFailure("cancel replay reservation", ctx.Err())
	default:
	}

	now := time.Unix(store.now().UTC().Unix(), 0)
	expiresAt = time.Unix(expiresAt.UTC().Unix(), 0)
	if now.Unix() < 0 || !expiresAt.After(now) ||
		expiresAt.After(now.Add(v1.RFC9421ReplayTTL)) {
		return time.Time{}, 0, v1.ErrRFC9421ReplayStore
	}
	return now, expiresAt.Unix(), nil
}

func cleanupExpiredRFC9421Replay(
	ctx context.Context,
	transaction *sql.Tx,
	nowUnix int64,
	maximum int,
) (int64, error) {
	result, err := transaction.ExecContext(
		ctx,
		`DELETE FROM replay_reservations
		 WHERE replay_key IN (
		     SELECT replay_key
		     FROM replay_reservations
		     WHERE expires_at_unix <= ?
		     ORDER BY expires_at_unix, replay_key
		     LIMIT ?
		 )`,
		nowUnix,
		maximum,
	)
	if err != nil {
		return 0, replayStoreFailure("delete expired replay reservations", err)
	}
	deleted, err := result.RowsAffected()
	if err != nil {
		return 0, replayStoreFailure("count expired replay reservations", err)
	}
	if deleted < 0 || deleted > int64(maximum) {
		return 0, replayStoreFailure(
			"count expired replay reservations",
			fmt.Errorf("unexpected rows affected: %d", deleted),
		)
	}
	return deleted, nil
}

func replayStoreFailure(action string, err error) error {
	return fmt.Errorf("%w: %s: %w", v1.ErrRFC9421ReplayStore, action, err)
}
