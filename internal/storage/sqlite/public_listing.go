package sqlite

import (
	"context"
	"errors"
	"time"

	v1 "github.com/thystra/activity-relay-directory/internal/protocol/v1"
	"github.com/thystra/activity-relay-directory/internal/storage"
)

var _ storage.PublicListingRepository = (*RelayRepository)(nil)

// ListPublicRelays returns one indexed, bounded page that is safe for public
// presentation. Ineligible lifecycle/moderation rows and the fixed 30-day
// public cutoff are enforced in SQL before any row reaches the HTTP layer.
func (repository *RelayRepository) ListPublicRelays(
	ctx context.Context,
	query storage.HealthProjectionQuery,
) (storage.HealthProjectionPage, error) {
	if repository == nil || repository.database == nil || ctx == nil {
		return storage.HealthProjectionPage{}, storage.ErrRepositoryConfiguration
	}
	if !query.After.Valid() ||
		query.Limit <= 0 || query.Limit > storage.MaximumPublicListingPage ||
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
	cutoffUnix := observedUnix - int64(storage.DeadBefore/time.Second)

	rows, err := repository.database.QueryContext(
		ctx,
		`SELECT relay_actor,
		        public_base_url,
		        last_seen_at_unix
		 FROM relays INDEXED BY relays_health_projection_idx
		 WHERE lifecycle_state = ?
		   AND administrative_state = ?
		   AND last_seen_at_unix > ?
		   AND (last_seen_at_unix, relay_actor) > (?, ?)
		 ORDER BY last_seen_at_unix, relay_actor
		 LIMIT ?`,
		lifecycleRegistered,
		administrativeActive,
		cutoffUnix,
		query.After.LastSeenUnix,
		query.After.RelayActor,
		query.Limit+1,
	)
	if err != nil {
		return storage.HealthProjectionPage{}, storageFailure("read public relay listing", err)
	}
	defer rows.Close()

	page := storage.HealthProjectionPage{
		Relays: make([]storage.HealthProjectionRelay, 0, query.Limit),
	}
	for rows.Next() {
		var relay storage.HealthProjectionRelay
		if err := rows.Scan(&relay.RelayActor, &relay.PublicBaseURL, &relay.LastSeenUnix); err != nil {
			return storage.HealthProjectionPage{}, storageFailure("decode public relay listing", err)
		}

		identity, err := v1.NormalizeRelayIdentity(relay.RelayActor, relay.PublicBaseURL)
		if err != nil || identity.RelayActor != relay.RelayActor ||
			identity.PublicBaseURL != relay.PublicBaseURL {
			return storage.HealthProjectionPage{}, storageFailure(
				"validate public relay listing",
				errors.New("invalid retained relay identity"),
			)
		}

		relay.HealthState, err = storage.ClassifyHealth(relay.LastSeenUnix, observedUnix)
		if err != nil {
			return storage.HealthProjectionPage{}, err
		}
		if !relay.PublicEligible() {
			return storage.HealthProjectionPage{}, storage.ErrHealthTime
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
		return storage.HealthProjectionPage{}, storageFailure("iterate public relay listing", err)
	}
	return page, nil
}
