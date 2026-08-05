package sqlite

import (
	"context"
	"database/sql"
	"errors"

	v1 "github.com/thystra/activity-relay-directory/internal/protocol/v1"
	"github.com/thystra/activity-relay-directory/internal/storage"
)

var _ storage.ModerationReadRepository = (*RelayRepository)(nil)

// ModerationState returns one retained relay's current private operator state.
// It performs no writes and exposes no moderation audit tokens.
func (repository *RelayRepository) ModerationState(
	ctx context.Context,
	relayActor string,
) (storage.ModerationState, error) {
	if repository == nil || repository.database == nil || ctx == nil {
		return storage.ModerationState{}, storage.ErrRepositoryConfiguration
	}
	if !validAdministrativeReadActor(relayActor) {
		return storage.ModerationState{}, storage.ErrAdministrativeReadInput
	}

	var (
		state          storage.ModerationState
		lifecycle      string
		administrative string
		lastHeartbeat  sql.NullInt64
		unregistered   sql.NullInt64
		suspended      sql.NullInt64
	)
	err := repository.database.QueryRowContext(
		ctx,
		`SELECT relay_actor,
		        public_base_url,
		        lifecycle_state,
		        administrative_state,
		        first_registered_at_unix,
		        updated_at_unix,
		        last_heartbeat_at_unix,
		        unregistered_at_unix,
		        suspended_at_unix
		 FROM relays
		 WHERE relay_actor = ?`,
		relayActor,
	).Scan(
		&state.RelayActor,
		&state.PublicBaseURL,
		&lifecycle,
		&administrative,
		&state.FirstRegisteredUnix,
		&state.UpdatedUnix,
		&lastHeartbeat,
		&unregistered,
		&suspended,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return storage.ModerationState{}, storage.ErrRelayAbsent
	}
	if err != nil {
		return storage.ModerationState{}, storageFailure("read moderation state", err)
	}

	state.LifecycleState = storage.RelayLifecycleState(lifecycle)
	state.AdministrativeState = storage.RelayAdministrativeState(administrative)
	if !state.LifecycleState.Valid() || !state.AdministrativeState.Valid() {
		return storage.ModerationState{}, storageFailure(
			"validate moderation state",
			errors.New("invalid retained state"),
		)
	}
	state.LastHeartbeatUnix = nullableInt64(lastHeartbeat)
	state.UnregisteredUnix = nullableInt64(unregistered)
	state.SuspendedUnix = nullableInt64(suspended)
	return state, nil
}

// ModerationAudit returns one bounded private keyset page ordered by the
// existing actor/time/event index. It performs no writes.
func (repository *RelayRepository) ModerationAudit(
	ctx context.Context,
	query storage.ModerationAuditQuery,
) (storage.ModerationAuditPage, error) {
	if repository == nil || repository.database == nil || ctx == nil {
		return storage.ModerationAuditPage{}, storage.ErrRepositoryConfiguration
	}
	if !validAdministrativeReadActor(query.RelayActor) ||
		!query.After.Valid() ||
		query.Limit <= 0 || query.Limit > storage.MaximumModerationAuditPage {
		return storage.ModerationAuditPage{}, storage.ErrAdministrativeReadInput
	}

	var retained int
	if err := repository.database.QueryRowContext(
		ctx,
		`SELECT 1 FROM relays WHERE relay_actor = ?`,
		query.RelayActor,
	).Scan(&retained); errors.Is(err, sql.ErrNoRows) {
		return storage.ModerationAuditPage{}, storage.ErrRelayAbsent
	} else if err != nil {
		return storage.ModerationAuditPage{}, storageFailure("read audit relay", err)
	}

	rows, err := repository.database.QueryContext(
		ctx,
		`SELECT moderation_event_id,
		        relay_actor,
		        action,
		        moderator_id,
		        reason_code,
		        recorded_at_unix
		 FROM moderation_events
		 WHERE relay_actor = ?
		   AND (recorded_at_unix, moderation_event_id) > (?, ?)
		 ORDER BY recorded_at_unix, moderation_event_id
		 LIMIT ?`,
		query.RelayActor,
		query.After.RecordedUnix,
		query.After.EventID,
		query.Limit+1,
	)
	if err != nil {
		return storage.ModerationAuditPage{}, storageFailure("read moderation audit", err)
	}
	defer rows.Close()

	page := storage.ModerationAuditPage{
		Events: make([]storage.ModerationAuditEvent, 0, query.Limit),
	}
	for rows.Next() {
		var (
			event  storage.ModerationAuditEvent
			action string
		)
		if err := rows.Scan(
			&event.EventID,
			&event.RelayActor,
			&action,
			&event.ModeratorID,
			&event.ReasonCode,
			&event.RecordedUnix,
		); err != nil {
			return storage.ModerationAuditPage{}, storageFailure("decode moderation audit", err)
		}
		event.Action = storage.ModerationAction(action)
		if event.EventID <= 0 || event.RecordedUnix < 0 || !event.Action.Valid() {
			return storage.ModerationAuditPage{}, storageFailure(
				"validate moderation audit",
				errors.New("invalid audit event"),
			)
		}
		if len(page.Events) == query.Limit {
			last := page.Events[len(page.Events)-1]
			page.Next = storage.ModerationAuditCursor{
				RecordedUnix: last.RecordedUnix,
				EventID:      last.EventID,
			}
			break
		}
		page.Events = append(page.Events, event)
	}
	if err := rows.Err(); err != nil {
		return storage.ModerationAuditPage{}, storageFailure("iterate moderation audit", err)
	}
	return page, nil
}

func validAdministrativeReadActor(relayActor string) bool {
	if len(relayActor) == 0 || len(relayActor) > maximumRelayActorBytes {
		return false
	}
	canonical, err := v1.NormalizeRelayActorURL(relayActor)
	return err == nil && canonical == relayActor
}

func nullableInt64(value sql.NullInt64) *int64 {
	if !value.Valid {
		return nil
	}
	result := value.Int64
	return &result
}
