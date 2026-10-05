package sqlite

import (
	"context"
	"errors"
	"time"

	"github.com/thystra/activity-relay-directory/internal/storage"
)

var _ storage.TelemetryRepository = (*RelayRepository)(nil)

func (repository *RelayRepository) ReplaceRelayTelemetry(
	ctx context.Context,
	intent storage.TelemetryIntent,
	reportedAt time.Time,
) error {
	if repository == nil || repository.database == nil || ctx == nil {
		return storage.ErrRepositoryConfiguration
	}
	if err := storage.ValidateTelemetryIntent(intent, reportedAt); err != nil {
		return err
	}
	result, err := repository.database.ExecContext(ctx, `
INSERT INTO relay_telemetry (relay_actor, receiving_instance_count, reported_at_unix)
VALUES (?, ?, ?)
ON CONFLICT(relay_actor) DO UPDATE SET
    receiving_instance_count = excluded.receiving_instance_count,
    reported_at_unix = excluded.reported_at_unix
WHERE excluded.reported_at_unix >= relay_telemetry.reported_at_unix`,
		intent.RelayActor, intent.ReceivingInstanceCount, reportedAt.UTC().Unix())
	if err != nil {
		return storageFailure("replace relay telemetry", err)
	}
	rows, err := result.RowsAffected()
	if err != nil {
		return storageFailure("read relay telemetry write result", err)
	}
	if rows < 0 || rows > 1 {
		return storageFailure("validate relay telemetry write result", errors.New("unexpected affected row count"))
	}
	return nil
}
