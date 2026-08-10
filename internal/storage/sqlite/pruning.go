package sqlite

import (
	"context"
	"errors"
	"time"

	v1 "github.com/thystra/activity-relay-directory/internal/protocol/v1"
	"github.com/thystra/activity-relay-directory/internal/storage"
)

var _ storage.PruningRepository = (*RelayRepository)(nil)

// PruneCandidates returns one indexed, bounded page of registered relays at or
// beyond the fixed 30-day boundary. Administrative suspension is deliberately
// not a filter because moderation and soft-pruning are independent dimensions.
func (repository *RelayRepository) PruneCandidates(
	ctx context.Context,
	query storage.PruneCandidateQuery,
) (storage.PruneCandidatePage, error) {
	if repository == nil || repository.database == nil || ctx == nil {
		return storage.PruneCandidatePage{}, storage.ErrRepositoryConfiguration
	}
	if !query.After.Valid() ||
		query.Limit <= 0 || query.Limit > storage.MaximumPruneCandidatePage ||
		(query.After != (storage.PruneCandidateCursor{}) &&
			!validHealthProjectionActor(query.After.RelayActor)) {
		return storage.PruneCandidatePage{}, storage.ErrPruningReadInput
	}

	observedUnix := query.ObservedAt.UTC().Unix()
	if observedUnix < 0 {
		return storage.PruneCandidatePage{}, storage.ErrPruningReadInput
	}
	cutoffUnix := observedUnix - int64(storage.DeadBefore/time.Second)
	if query.After != (storage.PruneCandidateCursor{}) &&
		query.After.LastSeenUnix > cutoffUnix {
		return storage.PruneCandidatePage{}, storage.ErrPruningReadInput
	}

	rows, err := repository.database.QueryContext(
		ctx,
		`SELECT relay_actor,
		        public_base_url,
		        administrative_state,
		        last_seen_at_unix
		 FROM relays INDEXED BY relays_pruning_candidates_idx
		 WHERE lifecycle_state = ?
		   AND last_seen_at_unix <= ?
		   AND (last_seen_at_unix, relay_actor) > (?, ?)
		 ORDER BY last_seen_at_unix, relay_actor
		 LIMIT ?`,
		lifecycleRegistered,
		cutoffUnix,
		query.After.LastSeenUnix,
		query.After.RelayActor,
		query.Limit+1,
	)
	if err != nil {
		return storage.PruneCandidatePage{}, storageFailure("read soft-pruning candidates", err)
	}
	defer rows.Close()

	page := storage.PruneCandidatePage{
		Candidates: make([]storage.PruneCandidate, 0, query.Limit),
	}
	for rows.Next() {
		var (
			candidate      storage.PruneCandidate
			administrative string
		)
		if err := rows.Scan(
			&candidate.RelayActor,
			&candidate.PublicBaseURL,
			&administrative,
			&candidate.LastSeenUnix,
		); err != nil {
			return storage.PruneCandidatePage{}, storageFailure("decode soft-pruning candidate", err)
		}

		identity, err := v1.NormalizeRelayIdentity(
			candidate.RelayActor,
			candidate.PublicBaseURL,
		)
		if err != nil || identity.RelayActor != candidate.RelayActor ||
			identity.PublicBaseURL != candidate.PublicBaseURL {
			return storage.PruneCandidatePage{}, storageFailure(
				"validate soft-pruning candidate",
				errors.New("invalid retained relay identity"),
			)
		}
		candidate.AdministrativeState = storage.RelayAdministrativeState(administrative)
		if !candidate.AdministrativeState.Valid() || candidate.LastSeenUnix < 0 ||
			candidate.LastSeenUnix > cutoffUnix {
			return storage.PruneCandidatePage{}, storageFailure(
				"validate soft-pruning candidate",
				errors.New("invalid retained pruning state"),
			)
		}

		if len(page.Candidates) == query.Limit {
			last := page.Candidates[len(page.Candidates)-1]
			page.Next = storage.PruneCandidateCursor{
				LastSeenUnix: last.LastSeenUnix,
				RelayActor:   last.RelayActor,
			}
			break
		}
		page.Candidates = append(page.Candidates, candidate)
	}
	if err := rows.Err(); err != nil {
		return storage.PruneCandidatePage{}, storageFailure("iterate soft-pruning candidates", err)
	}
	return page, nil
}

// SoftPrune re-reads and revalidates one relay inside the same immediate SQLite
// transaction that commits the reversible lifecycle state and append-only event.
func (repository *RelayRepository) SoftPrune(
	ctx context.Context,
	intent storage.IdentityIntent,
	observedAt time.Time,
) (storage.PruneOutcome, error) {
	if err := validateIdentityIntent(intent); err != nil {
		return "", err
	}
	observedUnix, err := transitionUnix(observedAt)
	if err != nil {
		return "", err
	}
	cutoffUnix := observedUnix - int64(storage.DeadBefore/time.Second)

	transaction, err := repository.begin(ctx)
	if err != nil {
		return "", err
	}
	defer func() { _ = transaction.Rollback() }()

	relay, err := selectRelay(ctx, transaction, intent.RelayActor)
	if err != nil {
		return "", storageFailure("read relay for soft pruning", err)
	}
	if relay == nil {
		return storage.PruneNotEligible, nil
	}
	if relay.lifecycleState == lifecyclePruned {
		return storage.PruneAlreadyPruned, nil
	}
	if relay.lifecycleState != lifecycleRegistered || relay.lastSeenAtUnix > cutoffUnix {
		return storage.PruneNotEligible, nil
	}
	if err := requireMonotonicTime(
		ctx,
		transaction,
		intent.RelayActor,
		observedUnix,
		relay,
	); err != nil {
		if errors.Is(err, storage.ErrTransitionTime) {
			return storage.PruneNotEligible, nil
		}
		return "", err
	}

	result, err := transaction.ExecContext(
		ctx,
		`UPDATE relays
		 SET lifecycle_state = ?,
		     updated_at_unix = ?,
		     pruned_at_unix = ?
		 WHERE relay_actor = ?
		   AND lifecycle_state = ?
		   AND last_seen_at_unix <= ?`,
		lifecyclePruned,
		observedUnix,
		observedUnix,
		intent.RelayActor,
		lifecycleRegistered,
		cutoffUnix,
	)
	if err != nil {
		return "", storageFailure("write soft-pruning transition", err)
	}
	rowsAffected, err := result.RowsAffected()
	if err != nil {
		return "", storageFailure("inspect soft-pruning transition", err)
	}
	if rowsAffected != 1 {
		return "", storageFailure(
			"write soft-pruning transition",
			errors.New("unexpected affected row count"),
		)
	}
	if err := insertRelayEvent(
		ctx,
		transaction,
		intent.RelayActor,
		eventRelayPruned,
		observedUnix,
	); err != nil {
		return "", err
	}
	if err := transaction.Commit(); err != nil {
		return "", storageFailure("commit soft-pruning transition", err)
	}
	return storage.PruneApplied, nil
}
