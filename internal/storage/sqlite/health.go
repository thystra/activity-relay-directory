package sqlite

import (
	"context"
	"errors"

	v1 "github.com/thystra/activity-relay-directory/internal/protocol/v1"
	"github.com/thystra/activity-relay-directory/internal/storage"
)

var _ storage.HealthProjectionRepository = (*RelayRepository)(nil)

// ProjectHealth returns one indexed, bounded page of active registered relays.
// It performs no writes and classifies every returned row against the single
// caller-captured observation time.
func (repository *RelayRepository) ProjectHealth(
	ctx context.Context,
	query storage.HealthProjectionQuery,
) (storage.HealthProjectionPage, error) {
	if repository == nil || repository.database == nil || ctx == nil {
		return storage.HealthProjectionPage{}, storage.ErrRepositoryConfiguration
	}
	if !query.After.Valid() ||
		query.Limit <= 0 || query.Limit > storage.MaximumHealthProjectionPage ||
		(query.After != (storage.HealthProjectionCursor{}) &&
			!validHealthProjectionActor(query.After.RelayActor)) {
		return storage.HealthProjectionPage{}, storage.ErrHealthReadInput
	}

	observedUnix := query.ObservedAt.UTC().Unix()
	if observedUnix < 0 ||
		(query.After != (storage.HealthProjectionCursor{}) &&
			query.After.LastSeenUnix > observedUnix) {
		return storage.HealthProjectionPage{}, storage.ErrHealthReadInput
	}

	rows, err := repository.database.QueryContext(
		ctx,
		`SELECT relay_actor,
		        public_base_url,
		        last_seen_at_unix
		 FROM relays INDEXED BY relays_health_projection_idx
		 WHERE lifecycle_state = ?
		   AND administrative_state = ?
		   AND (last_seen_at_unix, relay_actor) > (?, ?)
		 ORDER BY last_seen_at_unix, relay_actor
		 LIMIT ?`,
		lifecycleRegistered,
		administrativeActive,
		query.After.LastSeenUnix,
		query.After.RelayActor,
		query.Limit+1,
	)
	if err != nil {
		return storage.HealthProjectionPage{}, storageFailure("read health projection", err)
	}
	defer rows.Close()

	page := storage.HealthProjectionPage{
		Relays: make([]storage.HealthProjectionRelay, 0, query.Limit),
	}
	for rows.Next() {
		var relay storage.HealthProjectionRelay
		if err := rows.Scan(
			&relay.RelayActor,
			&relay.PublicBaseURL,
			&relay.LastSeenUnix,
		); err != nil {
			return storage.HealthProjectionPage{}, storageFailure("decode health projection", err)
		}

		identity, err := v1.NormalizeRelayIdentity(relay.RelayActor, relay.PublicBaseURL)
		if err != nil || identity.RelayActor != relay.RelayActor ||
			identity.PublicBaseURL != relay.PublicBaseURL {
			return storage.HealthProjectionPage{}, storageFailure(
				"validate health projection",
				errors.New("invalid retained relay identity"),
			)
		}

		relay.HealthState, err = storage.ClassifyHealth(relay.LastSeenUnix, observedUnix)
		if err != nil {
			return storage.HealthProjectionPage{}, err
		}

		if len(page.Relays) == query.Limit {
			last := page.Relays[len(page.Relays)-1]
			page.Next = storage.HealthProjectionCursor{
				LastSeenUnix: last.LastSeenUnix,
				RelayActor:   last.RelayActor,
			}
			break
		}
		page.Relays = append(page.Relays, relay)
	}
	if err := rows.Err(); err != nil {
		return storage.HealthProjectionPage{}, storageFailure("iterate health projection", err)
	}
	return page, nil
}

func validHealthProjectionActor(relayActor string) bool {
	if len(relayActor) == 0 || len(relayActor) > maximumRelayActorBytes {
		return false
	}
	canonical, err := v1.NormalizeRelayActorURL(relayActor)
	return err == nil && canonical == relayActor
}
