package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"strings"

	v1 "github.com/thystra/activity-relay-directory/internal/protocol/v1"
	"github.com/thystra/activity-relay-directory/internal/storage"
)

var _ storage.DirectoryProjectionRepository = (*RelayRepository)(nil)

const (
	directoryRelayCandidateSQL = `SELECT relay_actor
FROM relays
WHERE relay_actor > ?
ORDER BY relay_actor
LIMIT ?`

	directoryDiscoveryCandidateSQL = `SELECT relay_actor
FROM relay_discoveries
WHERE relay_actor > ?
ORDER BY relay_actor
LIMIT ?`
)

// ListDirectoryRelays returns one bounded actor-ordered page for the richer
// public 1.1 projection. Candidate identity reads use each table's primary-key
// order without scanning by mutable state. At most MaximumDirectoryProjectionScan
// retained actors are then joined by primary key and evaluated in memory. This
// keeps sparse/inactive datasets bounded while allowing a cursor to advance
// across actors that are not currently public.
func (repository *RelayRepository) ListDirectoryRelays(
	ctx context.Context,
	query storage.DirectoryProjectionQuery,
) (storage.DirectoryProjectionPage, error) {
	if repository == nil || repository.database == nil || ctx == nil {
		return storage.DirectoryProjectionPage{}, storage.ErrRepositoryConfiguration
	}
	if !query.After.Valid() || query.Limit <= 0 || query.Limit > storage.MaximumDirectoryProjectionPage {
		return storage.DirectoryProjectionPage{}, storage.ErrDirectoryProjectionInput
	}
	observedUnix := query.ObservedAt.UTC().Unix()
	if observedUnix < 0 {
		return storage.DirectoryProjectionPage{}, storage.ErrDirectoryProjectionInput
	}

	readLimit := storage.MaximumDirectoryProjectionScan + 1
	relayActors, err := readDirectoryCandidateActors(
		ctx, repository.database, directoryRelayCandidateSQL, query.After.RelayActor, readLimit,
	)
	if err != nil {
		return storage.DirectoryProjectionPage{}, err
	}
	discoveryActors, err := readDirectoryCandidateActors(
		ctx, repository.database, directoryDiscoveryCandidateSQL, query.After.RelayActor, readLimit,
	)
	if err != nil {
		return storage.DirectoryProjectionPage{}, err
	}
	candidates := mergeDirectoryActors(relayActors, discoveryActors, readLimit)
	if len(candidates) == 0 {
		return storage.DirectoryProjectionPage{Relays: []storage.DirectoryProjectionRelay{}}, nil
	}

	scanCount := len(candidates)
	if scanCount > storage.MaximumDirectoryProjectionScan {
		scanCount = storage.MaximumDirectoryProjectionScan
	}
	details, err := repository.readDirectoryProjectionDetails(ctx, candidates[:scanCount], observedUnix)
	if err != nil {
		return storage.DirectoryProjectionPage{}, err
	}

	page := storage.DirectoryProjectionPage{
		Relays: make([]storage.DirectoryProjectionRelay, 0, query.Limit),
	}
	lastProcessed := ""
	for index, actor := range candidates[:scanCount] {
		lastProcessed = actor
		relay, exists := details[actor]
		if !exists {
			return storage.DirectoryProjectionPage{}, storageFailure(
				"validate public directory projection",
				errors.New("retained directory candidate disappeared"),
			)
		}
		if relay == nil {
			continue
		}
		if err := storage.ValidateDirectoryProjectionEvidence(*relay, observedUnix); err != nil {
			return storage.DirectoryProjectionPage{}, storageFailure("validate public directory projection", err)
		}
		if !relay.PublicEligible(observedUnix) {
			continue
		}
		page.Relays = append(page.Relays, *relay)
		if len(page.Relays) == query.Limit {
			if index+1 < scanCount || len(candidates) > scanCount {
				page.Next = storage.DirectoryProjectionCursor{RelayActor: lastProcessed}
			}
			return page, nil
		}
	}

	if len(candidates) > scanCount {
		page.Next = storage.DirectoryProjectionCursor{RelayActor: lastProcessed}
	}
	return page, nil
}

func readDirectoryCandidateActors(
	ctx context.Context,
	database *sql.DB,
	statement string,
	after string,
	limit int,
) ([]string, error) {
	rows, err := database.QueryContext(ctx, statement, after, limit)
	if err != nil {
		return nil, storageFailure("read public directory candidates", err)
	}
	defer rows.Close()

	actors := make([]string, 0, limit)
	for rows.Next() {
		var actor string
		if err := rows.Scan(&actor); err != nil {
			return nil, storageFailure("decode public directory candidate", err)
		}
		canonical, err := v1.NormalizeRelayActorURL(actor)
		if err != nil || canonical != actor {
			return nil, storageFailure(
				"validate public directory candidate",
				errors.New("invalid retained actor identity"),
			)
		}
		actors = append(actors, actor)
	}
	if err := rows.Err(); err != nil {
		return nil, storageFailure("iterate public directory candidates", err)
	}
	return actors, nil
}

func mergeDirectoryActors(left, right []string, limit int) []string {
	merged := make([]string, 0, minInt(limit, len(left)+len(right)))
	for leftIndex, rightIndex := 0, 0; len(merged) < limit && (leftIndex < len(left) || rightIndex < len(right)); {
		var actor string
		switch {
		case rightIndex >= len(right):
			actor = left[leftIndex]
			leftIndex++
		case leftIndex >= len(left):
			actor = right[rightIndex]
			rightIndex++
		case left[leftIndex] < right[rightIndex]:
			actor = left[leftIndex]
			leftIndex++
		case right[rightIndex] < left[leftIndex]:
			actor = right[rightIndex]
			rightIndex++
		default:
			actor = left[leftIndex]
			leftIndex++
			rightIndex++
		}
		merged = append(merged, actor)
	}
	return merged
}

func (repository *RelayRepository) readDirectoryProjectionDetails(
	ctx context.Context,
	actors []string,
	observedUnix int64,
) (map[string]*storage.DirectoryProjectionRelay, error) {
	if len(actors) == 0 || len(actors) > storage.MaximumDirectoryProjectionScan {
		return nil, storage.ErrDirectoryProjectionInput
	}

	placeholders := make([]string, len(actors))
	arguments := make([]any, len(actors))
	for index, actor := range actors {
		placeholders[index] = "(?)"
		arguments[index] = actor
	}
	statement := `WITH seed(relay_actor) AS (VALUES ` + strings.Join(placeholders, ",") + `)
SELECT seed.relay_actor,
       relay.public_base_url,
       relay.lifecycle_state,
       relay.administrative_state,
       relay.last_seen_at_unix,
       discovery.public_base_url,
       discovery.discovery_state,
       observation.actor_state,
       observation.actor_last_checked_at_unix,
       observation.actor_last_success_at_unix,
       observation.inbox_url,
       observation.inbox_declared_at_unix,
       observation.inbox_probe_state,
       observation.inbox_last_checked_at_unix,
       observation.rfc9421_verified_at_unix
FROM seed
LEFT JOIN relays AS relay ON relay.relay_actor = seed.relay_actor
LEFT JOIN relay_discoveries AS discovery ON discovery.relay_actor = seed.relay_actor
LEFT JOIN relay_observations AS observation ON observation.relay_actor = seed.relay_actor
ORDER BY seed.relay_actor`

	rows, err := repository.database.QueryContext(ctx, statement, arguments...)
	if err != nil {
		return nil, storageFailure("read public directory details", err)
	}
	defer rows.Close()

	result := make(map[string]*storage.DirectoryProjectionRelay, len(actors))
	for rows.Next() {
		var (
			actor               string
			relayBase           sql.NullString
			relayLifecycle      sql.NullString
			relayAdministrative sql.NullString
			lastSeen            sql.NullInt64
			discoveryBase       sql.NullString
			discoveryState      sql.NullString
			actorState          sql.NullString
			actorLastChecked    sql.NullInt64
			actorLastSuccess    sql.NullInt64
			inboxURL            sql.NullString
			inboxDeclared       sql.NullInt64
			inboxState          sql.NullString
			inboxLastChecked    sql.NullInt64
			rfc9421Verified     sql.NullInt64
		)
		if err := rows.Scan(
			&actor,
			&relayBase,
			&relayLifecycle,
			&relayAdministrative,
			&lastSeen,
			&discoveryBase,
			&discoveryState,
			&actorState,
			&actorLastChecked,
			&actorLastSuccess,
			&inboxURL,
			&inboxDeclared,
			&inboxState,
			&inboxLastChecked,
			&rfc9421Verified,
		); err != nil {
			return nil, storageFailure("decode public directory details", err)
		}
		if _, duplicate := result[actor]; duplicate {
			return nil, storageFailure("validate public directory details", errors.New("duplicate retained actor"))
		}

		if relayBase.Valid {
			identity, err := v1.NormalizeRelayIdentity(actor, relayBase.String)
			if err != nil || identity.RelayActor != actor || identity.PublicBaseURL != relayBase.String {
				return nil, storageFailure("validate public directory lifecycle identity", errors.New("invalid retained relay identity"))
			}
		}
		if discoveryBase.Valid {
			identity, err := v1.NormalizeRelayIdentity(actor, discoveryBase.String)
			if err != nil || identity.RelayActor != actor || identity.PublicBaseURL != discoveryBase.String {
				return nil, storageFailure("validate public directory discovery identity", errors.New("invalid retained discovery identity"))
			}
		}
		if relayBase.Valid && discoveryBase.Valid && relayBase.String != discoveryBase.String {
			return nil, storageFailure("validate public directory identity", errors.New("retained identity origins disagree"))
		}

		relayExists := relayBase.Valid || relayLifecycle.Valid || relayAdministrative.Valid || lastSeen.Valid
		if relayExists {
			if !relayBase.Valid || !relayLifecycle.Valid || !relayAdministrative.Valid || !lastSeen.Valid ||
				!validDirectoryLifecycleState(relayLifecycle.String) || !validDirectoryAdministrativeState(relayAdministrative.String) {
				return nil, storageFailure("validate public directory lifecycle state", errors.New("invalid retained lifecycle state"))
			}
		}
		discoveryExists := discoveryBase.Valid || discoveryState.Valid
		if discoveryExists {
			if !discoveryBase.Valid || !discoveryState.Valid || !validDirectoryDiscoveryState(discoveryState.String) {
				return nil, storageFailure("validate public directory discovery state", errors.New("invalid retained discovery state"))
			}
		}

		if relayAdministrative.Valid && relayAdministrative.String == administrativeSuspended {
			result[actor] = nil
			continue
		}
		registered := relayLifecycle.Valid && relayLifecycle.String == lifecycleRegistered &&
			relayAdministrative.Valid && relayAdministrative.String == administrativeActive
		discovered := discoveryState.Valid && discoveryState.String == discoveryActive
		if !registered && !discovered {
			result[actor] = nil
			continue
		}

		relay := &storage.DirectoryProjectionRelay{
			RelayActor:           actor,
			Registered:           registered,
			Discovered:           discovered,
			LastSeenUnix:         nullableInt64Pointer(lastSeen),
			ActorState:           storage.ReachabilityUnknown,
			ActorLastCheckedUnix: nullableInt64Pointer(actorLastChecked),
			ActorLastSuccessUnix: nullableInt64Pointer(actorLastSuccess),
			InboxDeclaredUnix:    nullableInt64Pointer(inboxDeclared),
			InboxProbeState:      storage.InboxNotChecked,
			InboxLastCheckedUnix: nullableInt64Pointer(inboxLastChecked),
			RFC9421VerifiedUnix:  nullableInt64Pointer(rfc9421Verified),
		}
		if registered {
			if !relayBase.Valid {
				return nil, storageFailure("validate public directory details", errors.New("registered relay missing public base URL"))
			}
			relay.PublicBaseURL = relayBase.String
		} else {
			if !discoveryBase.Valid {
				return nil, storageFailure("validate public directory details", errors.New("discovery missing public base URL"))
			}
			relay.PublicBaseURL = discoveryBase.String
		}
		if actorState.Valid {
			relay.ActorState = storage.ReachabilityState(actorState.String)
		}
		if inboxURL.Valid {
			relay.InboxURL = inboxURL.String
		}
		if inboxState.Valid {
			relay.InboxProbeState = storage.InboxProbeState(inboxState.String)
		}
		var err error
		relay.HeartbeatState, err = storage.ClassifyPublicHeartbeat(relay.LastSeenUnix, observedUnix)
		if err != nil {
			return nil, storageFailure("classify public directory heartbeat", err)
		}
		result[actor] = relay
	}
	if err := rows.Err(); err != nil {
		return nil, storageFailure("iterate public directory details", err)
	}
	if len(result) != len(actors) {
		return nil, storageFailure("validate public directory details", errors.New("retained actor detail count changed"))
	}
	return result, nil
}

func nullableInt64Pointer(value sql.NullInt64) *int64 {
	if !value.Valid {
		return nil
	}
	result := value.Int64
	return &result
}

func minInt(left, right int) int {
	if left < right {
		return left
	}
	return right
}

func validDirectoryLifecycleState(value string) bool {
	switch value {
	case lifecycleRegistered, lifecycleUnregistered, lifecyclePruned:
		return true
	default:
		return false
	}
}

func validDirectoryAdministrativeState(value string) bool {
	return value == administrativeActive || value == administrativeSuspended
}

func validDirectoryDiscoveryState(value string) bool {
	return value == discoveryActive || value == discoveryRemoved
}
