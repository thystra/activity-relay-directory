package sqlite

import (
	"context"
	"errors"
	"time"

	"github.com/thystra/activity-relay-directory/internal/storage"
)

var _ storage.DirectorySummaryRepository = (*RelayRepository)(nil)

// ReadDirectorySummary returns aggregate counts for the human directory without
// exposing unresolved candidate identities or private discovery provenance.
func (repository *RelayRepository) ReadDirectorySummary(
	ctx context.Context,
	observedAt time.Time,
) (storage.DirectorySummary, error) {
	if repository == nil || repository.database == nil || ctx == nil {
		return storage.DirectorySummary{}, storage.ErrRepositoryConfiguration
	}
	observedUnix := observedAt.UTC().Unix()
	if observedUnix < 0 {
		return storage.DirectorySummary{}, storage.ErrDirectoryProjectionInput
	}
	freshCutoff := observedUnix - int64(storage.ReachabilityFreshness/time.Second)

	var known, online, pending int64
	err := repository.database.QueryRowContext(ctx, `WITH public_relays(relay_actor) AS (
		SELECT relay_actor
		FROM relays
		WHERE lifecycle_state IN (?, ?)
		  AND administrative_state = ?
		UNION
		SELECT discovery.relay_actor
		FROM relay_discoveries AS discovery
		WHERE discovery.discovery_state = ?
		  AND NOT EXISTS (
		      SELECT 1
		      FROM relays AS suspended
		      WHERE suspended.relay_actor = discovery.relay_actor
		        AND suspended.administrative_state = ?
		  )
	), public_summary AS (
		SELECT
			COUNT(*) AS known_relays,
			COALESCE(SUM(CASE
				WHEN observation.actor_state = ?
				 AND observation.actor_last_checked_at_unix IS NOT NULL
				 AND observation.actor_last_success_at_unix IS NOT NULL
				 AND observation.actor_last_checked_at_unix = observation.actor_last_success_at_unix
				 AND observation.actor_last_success_at_unix BETWEEN ? AND ?
				THEN 1 ELSE 0 END), 0) AS online_relays
		FROM public_relays AS public
		LEFT JOIN relay_observations AS observation
		  ON observation.relay_actor = public.relay_actor
	), candidate_summary AS (
		SELECT COUNT(*) AS pending_verification
		FROM relay_discovery_candidates AS candidate
		WHERE candidate.candidate_state IN (?, ?)
		  AND NOT EXISTS (
		      SELECT 1
		      FROM public_relays AS public
		      WHERE public.relay_actor = candidate.candidate_actor_url
		  )
	)
	SELECT public_summary.known_relays,
	       public_summary.online_relays,
	       candidate_summary.pending_verification
	FROM public_summary, candidate_summary`,
		string(storage.LifecycleRegistered),
		string(storage.LifecyclePruned),
		string(storage.AdministrativeActive),
		string(storage.DiscoveryActive),
		string(storage.AdministrativeSuspended),
		string(storage.ReachabilityReachable),
		freshCutoff,
		observedUnix,
		string(storage.DiscoveryCandidateUnreachable),
		string(storage.DiscoveryCandidateIncompatible),
	).Scan(&known, &online, &pending)
	if err != nil {
		return storage.DirectorySummary{}, storageFailure("read directory summary", err)
	}

	maximumInt := int64(int(^uint(0) >> 1))
	if known < 0 || online < 0 || pending < 0 || online > known ||
		known > maximumInt || online > maximumInt || pending > maximumInt {
		return storage.DirectorySummary{}, storageFailure(
			"validate directory summary",
			errors.New("invalid aggregate directory counts"),
		)
	}

	summary := storage.DirectorySummary{
		KnownRelays:         int(known),
		OnlineRelays:        int(online),
		OfflineRelays:       int(known - online),
		PendingVerification: int(pending),
	}
	if !summary.Valid() {
		return storage.DirectorySummary{}, storageFailure(
			"validate directory summary",
			errors.New("invalid directory summary"),
		)
	}
	return summary, nil
}
