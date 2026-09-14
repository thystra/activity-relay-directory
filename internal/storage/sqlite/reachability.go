package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"time"

	v1 "github.com/thystra/activity-relay-directory/internal/protocol/v1"
	"github.com/thystra/activity-relay-directory/internal/storage"
)

var _ storage.ReachabilityRepository = (*RelayRepository)(nil)

const reachabilityEligibleCTE = `WITH eligible(relay_actor) AS (
	SELECT relay_actor
	FROM relays
	WHERE lifecycle_state = ? AND administrative_state = ?
	UNION
	SELECT discovery.relay_actor
	FROM relay_discoveries AS discovery
	WHERE discovery.discovery_state = ?
	  AND NOT EXISTS (
		SELECT 1
		FROM relays AS retained
		WHERE retained.relay_actor = discovery.relay_actor
		  AND retained.administrative_state = ?
	  )
)`

// ReachabilityCandidates returns due actors in a fair stable order: never
// checked first, then oldest actor check, then actor URL. Registration and
// active operator discovery are independent eligibility paths, while a retained
// administrative suspension suppresses both.
func (repository *RelayRepository) ReachabilityCandidates(
	ctx context.Context,
	query storage.ReachabilityCandidateQuery,
) (storage.ReachabilityCandidatePage, error) {
	if repository == nil || repository.database == nil || ctx == nil {
		return storage.ReachabilityCandidatePage{}, storage.ErrRepositoryConfiguration
	}
	if !query.After.Valid() || query.Limit <= 0 ||
		query.Limit > storage.MaximumReachabilityCandidatePage ||
		(query.After != (storage.ReachabilityCandidateCursor{}) &&
			!validHealthProjectionActor(query.After.RelayActor)) {
		return storage.ReachabilityCandidatePage{}, storage.ErrReachabilityReadInput
	}
	observedUnix := query.ObservedAt.UTC().Unix()
	if observedUnix < 0 {
		return storage.ReachabilityCandidatePage{}, storage.ErrReachabilityReadInput
	}
	cutoffUnix := observedUnix - int64(storage.ReachabilityFreshness/time.Second)
	if query.After.HasLastChecked && query.After.LastCheckedUnix >= cutoffUnix {
		return storage.ReachabilityCandidatePage{}, storage.ErrReachabilityReadInput
	}

	arguments := []any{
		lifecycleRegistered,
		administrativeActive,
		discoveryActive,
		administrativeSuspended,
		cutoffUnix,
	}
	whereAfter := ""
	switch {
	case query.After == (storage.ReachabilityCandidateCursor{}):
	case !query.After.HasLastChecked:
		whereAfter = `
	  AND (
		(observation.actor_last_checked_at_unix IS NULL AND eligible.relay_actor > ?)
		OR observation.actor_last_checked_at_unix IS NOT NULL
	  )`
		arguments = append(arguments, query.After.RelayActor)
	default:
		whereAfter = `
	  AND observation.actor_last_checked_at_unix IS NOT NULL
	  AND (
		observation.actor_last_checked_at_unix > ?
		OR (observation.actor_last_checked_at_unix = ? AND eligible.relay_actor > ?)
	  )`
		arguments = append(
			arguments,
			query.After.LastCheckedUnix,
			query.After.LastCheckedUnix,
			query.After.RelayActor,
		)
	}
	arguments = append(arguments, query.Limit+1)

	rows, err := repository.database.QueryContext(
		ctx,
		reachabilityEligibleCTE+`
	SELECT eligible.relay_actor,
	       observation.actor_last_checked_at_unix
	FROM eligible
	LEFT JOIN relay_observations AS observation
	  ON observation.relay_actor = eligible.relay_actor
	WHERE (
		observation.actor_last_checked_at_unix IS NULL
		OR observation.actor_last_checked_at_unix < ?
	)`+whereAfter+`
	ORDER BY (observation.actor_last_checked_at_unix IS NOT NULL) ASC,
	         observation.actor_last_checked_at_unix ASC,
	         eligible.relay_actor ASC
	LIMIT ?`,
		arguments...,
	)
	if err != nil {
		return storage.ReachabilityCandidatePage{}, storageFailure("read reachability candidates", err)
	}
	defer rows.Close()

	page := storage.ReachabilityCandidatePage{
		Candidates: make([]storage.ReachabilityCandidate, 0, query.Limit),
	}
	for rows.Next() {
		var (
			candidate storage.ReachabilityCandidate
			checked   sql.NullInt64
		)
		if err := rows.Scan(&candidate.RelayActor, &checked); err != nil {
			return storage.ReachabilityCandidatePage{}, storageFailure("decode reachability candidate", err)
		}
		canonical, err := v1.NormalizeRelayActorURL(candidate.RelayActor)
		if err != nil || canonical != candidate.RelayActor {
			return storage.ReachabilityCandidatePage{}, storageFailure(
				"validate reachability candidate",
				errors.New("invalid retained actor identity"),
			)
		}
		if checked.Valid {
			if checked.Int64 < 0 || checked.Int64 >= cutoffUnix {
				return storage.ReachabilityCandidatePage{}, storageFailure(
					"validate reachability candidate",
					errors.New("invalid retained reachability time"),
				)
			}
			value := checked.Int64
			candidate.ActorLastCheckedUnix = &value
		}
		if len(page.Candidates) == query.Limit {
			page.Next = reachabilityCursor(page.Candidates[len(page.Candidates)-1])
			break
		}
		page.Candidates = append(page.Candidates, candidate)
	}
	if err := rows.Err(); err != nil {
		return storage.ReachabilityCandidatePage{}, storageFailure("iterate reachability candidates", err)
	}
	return page, nil
}

func reachabilityCursor(candidate storage.ReachabilityCandidate) storage.ReachabilityCandidateCursor {
	cursor := storage.ReachabilityCandidateCursor{RelayActor: candidate.RelayActor}
	if candidate.ActorLastCheckedUnix != nil {
		cursor.HasLastChecked = true
		cursor.LastCheckedUnix = *candidate.ActorLastCheckedUnix
	}
	return cursor
}

// RecordReachabilityObservation applies one worker result only while the actor
// is still eligible. A newer/equal actor check wins over a stale concurrent
// worker result. The actor and inbox observation are committed together.
func (repository *RelayRepository) RecordReachabilityObservation(
	ctx context.Context,
	intent storage.ReachabilityObservationIntent,
	observedAt time.Time,
) (storage.ReachabilityWriteOutcome, error) {
	if err := validateReachabilityObservationIntent(intent); err != nil {
		return "", err
	}
	observedUnix, err := observationUnix(observedAt)
	if err != nil {
		return "", err
	}
	transaction, lease, err := repository.begin(ctx)
	if err != nil {
		return "", err
	}
	defer lease.Release()
	defer func() { _ = transaction.Rollback() }()

	eligible, err := reachabilityEligible(ctx, transaction, intent.RelayActor)
	if err != nil {
		return "", storageFailure("revalidate reachability eligibility", err)
	}
	if !eligible {
		return storage.ReachabilityWriteSkipped, nil
	}
	current, err := selectObservation(ctx, transaction, intent.RelayActor)
	if err != nil {
		return "", storageFailure("read reachability observation", err)
	}
	if current != nil && current.actorLastChecked.Valid &&
		current.actorLastChecked.Int64 >= observedUnix {
		return storage.ReachabilityWriteSkipped, nil
	}
	if current == nil {
		if err := insertEmptyObservation(ctx, transaction, intent.RelayActor, observedUnix); err != nil {
			return "", err
		}
		current, err = selectObservation(ctx, transaction, intent.RelayActor)
		if err != nil || current == nil {
			if err == nil {
				err = errors.New("observation row missing after insert")
			}
			return "", storageFailure("read inserted reachability observation", err)
		}
	}
	updatedUnix := maxInt64(current.updatedUnix, observedUnix)

	switch intent.ActorState {
	case storage.ReachabilityReachable:
		var (
			inboxURL       any
			inboxDeclared  any
			inboxCheckedAt any
		)
		if intent.InboxURL != "" {
			inboxURL = intent.InboxURL
			inboxDeclared = observedUnix
			inboxCheckedAt = observedUnix
		}
		if _, err := transaction.ExecContext(ctx, `UPDATE relay_observations SET
			actor_state = ?,
			actor_last_checked_at_unix = ?,
			actor_last_success_at_unix = ?,
			inbox_url = ?,
			inbox_declared_at_unix = ?,
			inbox_probe_state = ?,
			inbox_last_checked_at_unix = ?,
			updated_at_unix = ?,
			revision = revision + 1
			WHERE relay_actor = ?`,
			string(intent.ActorState),
			observedUnix,
			observedUnix,
			inboxURL,
			inboxDeclared,
			string(intent.InboxState),
			inboxCheckedAt,
			updatedUnix,
			intent.RelayActor,
		); err != nil {
			return "", storageFailure("write reachable observation", err)
		}
	case storage.ReachabilityUnreachable:
		if _, err := transaction.ExecContext(ctx, `UPDATE relay_observations SET
			actor_state = ?,
			actor_last_checked_at_unix = ?,
			updated_at_unix = ?,
			revision = revision + 1
			WHERE relay_actor = ?`,
			string(intent.ActorState), observedUnix, updatedUnix, intent.RelayActor,
		); err != nil {
			return "", storageFailure("write unreachable observation", err)
		}
	default:
		return "", storage.ErrReachabilityWriteInput
	}

	if err := transaction.Commit(); err != nil {
		return "", storageFailure("commit reachability observation", err)
	}
	return storage.ReachabilityWriteApplied, nil
}

func reachabilityEligible(ctx context.Context, transaction *sql.Tx, actor string) (bool, error) {
	var eligible int
	err := transaction.QueryRowContext(ctx, `SELECT CASE WHEN
		EXISTS (
			SELECT 1 FROM relays
			WHERE relay_actor = ?
			  AND lifecycle_state = ?
			  AND administrative_state = ?
		)
		OR (
			EXISTS (
				SELECT 1 FROM relay_discoveries
				WHERE relay_actor = ? AND discovery_state = ?
			)
			AND NOT EXISTS (
				SELECT 1 FROM relays
				WHERE relay_actor = ? AND administrative_state = ?
			)
		)
		THEN 1 ELSE 0 END`,
		actor, lifecycleRegistered, administrativeActive,
		actor, discoveryActive,
		actor, administrativeSuspended,
	).Scan(&eligible)
	return eligible == 1, err
}

func validateReachabilityObservationIntent(intent storage.ReachabilityObservationIntent) error {
	canonical, err := v1.NormalizeRelayActorURL(intent.RelayActor)
	if err != nil || canonical != intent.RelayActor {
		return storage.ErrReachabilityWriteInput
	}
	switch intent.ActorState {
	case storage.ReachabilityReachable:
		if intent.InboxURL == "" {
			if intent.InboxState != storage.InboxNotChecked {
				return storage.ErrReachabilityWriteInput
			}
			return nil
		}
		canonicalInbox, err := v1.NormalizeRelayActorURL(intent.InboxURL)
		if err != nil || canonicalInbox != intent.InboxURL ||
			(intent.InboxState != storage.InboxResponsive &&
				intent.InboxState != storage.InboxMethodRejected &&
				intent.InboxState != storage.InboxUnreachable) {
			return storage.ErrReachabilityWriteInput
		}
		return nil
	case storage.ReachabilityUnreachable:
		if intent.InboxURL != "" || intent.InboxState != storage.InboxNotChecked {
			return storage.ErrReachabilityWriteInput
		}
		return nil
	default:
		return storage.ErrReachabilityWriteInput
	}
}
