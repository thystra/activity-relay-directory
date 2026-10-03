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
  AND lifecycle_state IN (?, ?)
  AND administrative_state = ?
ORDER BY relay_actor
LIMIT ?`

	directoryDiscoveryCandidateSQL = `SELECT discovery.relay_actor
FROM relay_discoveries AS discovery
WHERE discovery.relay_actor > ?
  AND discovery.discovery_state = ?
  AND NOT EXISTS (
      SELECT 1 FROM relays AS suspended
      WHERE suspended.relay_actor = discovery.relay_actor
        AND suspended.administrative_state = ?
  )
ORDER BY discovery.relay_actor
LIMIT ?`

	directoryRelayPreviousCandidateSQL = `SELECT relay_actor
FROM relays
WHERE relay_actor < ?
  AND lifecycle_state IN (?, ?)
  AND administrative_state = ?
ORDER BY relay_actor DESC
LIMIT ?`

	directoryDiscoveryPreviousCandidateSQL = `SELECT discovery.relay_actor
FROM relay_discoveries AS discovery
WHERE discovery.relay_actor < ?
  AND discovery.discovery_state = ?
  AND NOT EXISTS (
      SELECT 1 FROM relays AS suspended
      WHERE suspended.relay_actor = discovery.relay_actor
        AND suspended.administrative_state = ?
  )
ORDER BY discovery.relay_actor DESC
LIMIT ?`

	directoryRelayLastCandidateSQL = `SELECT relay_actor
FROM relays
WHERE lifecycle_state IN (?, ?)
  AND administrative_state = ?
ORDER BY relay_actor DESC
LIMIT ?`

	directoryDiscoveryLastCandidateSQL = `SELECT discovery.relay_actor
FROM relay_discoveries AS discovery
WHERE discovery.discovery_state = ?
  AND NOT EXISTS (
      SELECT 1 FROM relays AS suspended
      WHERE suspended.relay_actor = discovery.relay_actor
        AND suspended.administrative_state = ?
  )
ORDER BY discovery.relay_actor DESC
LIMIT ?`
)

// ListDirectoryRelays returns one bounded tier-ordered page for the richer
// public projection. Each request examines at most MaximumDirectoryProjectionScan
// retained identities. The tier+actor cursor is a scan position and therefore
// may refer to a retained identity that is not itself returned on that page.
func (repository *RelayRepository) ListDirectoryRelays(
	ctx context.Context,
	query storage.DirectoryProjectionQuery,
) (storage.DirectoryProjectionPage, error) {
	if repository == nil || repository.database == nil || ctx == nil {
		return storage.DirectoryProjectionPage{}, storage.ErrRepositoryConfiguration
	}
	if !query.After.Valid() || !query.Before.Valid() ||
		(query.After != (storage.DirectoryProjectionCursor{}) && query.Before != (storage.DirectoryProjectionCursor{})) ||
		query.Limit <= 0 || query.Limit > storage.MaximumDirectoryProjectionPage {
		return storage.DirectoryProjectionPage{}, storage.ErrDirectoryProjectionInput
	}
	observedUnix := query.ObservedAt.UTC().Unix()
	if observedUnix < 0 {
		return storage.DirectoryProjectionPage{}, storage.ErrDirectoryProjectionInput
	}

	if query.Before != (storage.DirectoryProjectionCursor{}) {
		return repository.listDirectoryRelaysBefore(ctx, query, observedUnix)
	}
	return repository.listDirectoryRelaysAfter(ctx, query, observedUnix)
}

func (repository *RelayRepository) listDirectoryRelaysAfter(
	ctx context.Context,
	query storage.DirectoryProjectionQuery,
	observedUnix int64,
) (storage.DirectoryProjectionPage, error) {
	page := storage.DirectoryProjectionPage{
		Relays: make([]storage.DirectoryProjectionRelay, 0, query.Limit),
	}
	tier := storage.DirectoryTierHeartbeatOnline
	boundary := ""
	if query.After != (storage.DirectoryProjectionCursor{}) {
		tier = query.After.Tier
		boundary = query.After.RelayActor
	}

	scanned := 0
	for tier.Valid() && scanned < storage.MaximumDirectoryProjectionScan {
		remaining := storage.MaximumDirectoryProjectionScan - scanned
		candidates, err := repository.readDirectoryCandidateActorsAfter(
			ctx,
			boundary,
			remaining+1,
		)
		if err != nil {
			return storage.DirectoryProjectionPage{}, err
		}
		if len(candidates) == 0 {
			if tier == storage.DirectoryTierGraveyard {
				setDirectoryForwardPrevious(&page, query.After)
				return page, nil
			}
			tier++
			boundary = ""
			continue
		}

		scanCount := minInt(len(candidates), remaining)
		details, err := repository.readDirectoryProjectionDetails(
			ctx,
			candidates[:scanCount],
			observedUnix,
		)
		if err != nil {
			return storage.DirectoryProjectionPage{}, err
		}

		for _, actor := range candidates[:scanCount] {
			scanned++
			boundary = actor
			relay, exists := details[actor]
			if !exists {
				return storage.DirectoryProjectionPage{}, storageFailure(
					"validate public directory projection",
					errors.New("retained directory candidate disappeared"),
				)
			}
			if relay == nil || relay.Tier != tier {
				continue
			}
			if err := storage.ValidateDirectoryProjectionRelay(*relay, observedUnix); err != nil {
				return storage.DirectoryProjectionPage{}, storageFailure("validate public directory projection", err)
			}
			page.Relays = append(page.Relays, *relay)
			if len(page.Relays) == query.Limit {
				cursor := directoryProjectionCursorForRelay(page.Relays[len(page.Relays)-1])
				more, err := repository.hasDirectoryRelayAfter(ctx, cursor, observedUnix)
				if err != nil {
					return storage.DirectoryProjectionPage{}, err
				}
				if more {
					page.Next = cursor
				}
				setDirectoryForwardPrevious(&page, query.After)
				return page, nil
			}
		}

		if len(candidates) > scanCount {
			page.Next = storage.DirectoryProjectionCursor{Tier: tier, RelayActor: boundary}
			setDirectoryForwardPrevious(&page, query.After)
			return page, nil
		}
		if scanned == storage.MaximumDirectoryProjectionScan {
			if tier < storage.DirectoryTierGraveyard {
				page.Next = storage.DirectoryProjectionCursor{Tier: tier, RelayActor: boundary}
			}
			setDirectoryForwardPrevious(&page, query.After)
			return page, nil
		}
		if tier == storage.DirectoryTierGraveyard {
			setDirectoryForwardPrevious(&page, query.After)
			return page, nil
		}
		tier++
		boundary = ""
	}

	setDirectoryForwardPrevious(&page, query.After)
	return page, nil
}

func setDirectoryForwardPrevious(
	page *storage.DirectoryProjectionPage,
	after storage.DirectoryProjectionCursor,
) {
	if page == nil || after == (storage.DirectoryProjectionCursor{}) {
		return
	}
	if len(page.Relays) == 0 {
		page.Previous = after
		return
	}
	page.Previous = directoryProjectionCursorForRelay(page.Relays[0])
}

func (repository *RelayRepository) listDirectoryRelaysBefore(
	ctx context.Context,
	query storage.DirectoryProjectionQuery,
	observedUnix int64,
) (storage.DirectoryProjectionPage, error) {
	page := storage.DirectoryProjectionPage{
		Relays: make([]storage.DirectoryProjectionRelay, 0, query.Limit),
	}
	tier := query.Before.Tier
	boundary := query.Before.RelayActor
	scanned := 0

	for tier.Valid() && scanned < storage.MaximumDirectoryProjectionScan {
		remaining := storage.MaximumDirectoryProjectionScan - scanned
		candidates, err := repository.readDirectoryCandidateActorsBefore(
			ctx,
			boundary,
			remaining+1,
		)
		if err != nil {
			return storage.DirectoryProjectionPage{}, err
		}
		if len(candidates) == 0 {
			if tier == storage.DirectoryTierHeartbeatOnline {
				reverseDirectoryRelays(page.Relays)
				setDirectoryReverseNext(&page, storage.DirectoryProjectionCursor{})
				return page, nil
			}
			tier--
			boundary = ""
			continue
		}

		scanCount := minInt(len(candidates), remaining)
		details, err := repository.readDirectoryProjectionDetails(
			ctx,
			candidates[:scanCount],
			observedUnix,
		)
		if err != nil {
			return storage.DirectoryProjectionPage{}, err
		}

		for _, actor := range candidates[:scanCount] {
			scanned++
			boundary = actor
			relay, exists := details[actor]
			if !exists {
				return storage.DirectoryProjectionPage{}, storageFailure(
					"validate public directory projection",
					errors.New("retained directory candidate disappeared"),
				)
			}
			if relay == nil || relay.Tier != tier {
				continue
			}
			if err := storage.ValidateDirectoryProjectionRelay(*relay, observedUnix); err != nil {
				return storage.DirectoryProjectionPage{}, storageFailure("validate public directory projection", err)
			}
			page.Relays = append(page.Relays, *relay)
			if len(page.Relays) == query.Limit {
				reverseDirectoryRelays(page.Relays)
				page.Next = directoryProjectionCursorForRelay(page.Relays[len(page.Relays)-1])
				first := directoryProjectionCursorForRelay(page.Relays[0])
				moreEarlier, err := repository.hasDirectoryRelayBefore(ctx, first, observedUnix)
				if err != nil {
					return storage.DirectoryProjectionPage{}, err
				}
				if moreEarlier {
					page.Previous = first
				}
				return page, nil
			}
		}

		if len(candidates) > scanCount {
			reverseDirectoryRelays(page.Relays)
			continuation := storage.DirectoryProjectionCursor{Tier: tier, RelayActor: boundary}
			page.Previous = continuation
			setDirectoryReverseNext(&page, continuation)
			return page, nil
		}
		if scanned == storage.MaximumDirectoryProjectionScan {
			reverseDirectoryRelays(page.Relays)
			continuation := storage.DirectoryProjectionCursor{Tier: tier, RelayActor: boundary}
			if tier > storage.DirectoryTierHeartbeatOnline {
				page.Previous = continuation
			}
			setDirectoryReverseNext(&page, continuation)
			return page, nil
		}
		if tier == storage.DirectoryTierHeartbeatOnline {
			reverseDirectoryRelays(page.Relays)
			setDirectoryReverseNext(&page, storage.DirectoryProjectionCursor{})
			return page, nil
		}
		tier--
		boundary = ""
	}

	reverseDirectoryRelays(page.Relays)
	setDirectoryReverseNext(&page, storage.DirectoryProjectionCursor{})
	return page, nil
}

func setDirectoryReverseNext(
	page *storage.DirectoryProjectionPage,
	fallback storage.DirectoryProjectionCursor,
) {
	if page == nil {
		return
	}
	if len(page.Relays) != 0 {
		page.Next = directoryProjectionCursorForRelay(page.Relays[len(page.Relays)-1])
		return
	}
	page.Next = fallback
}

func (repository *RelayRepository) hasDirectoryRelayAfter(
	ctx context.Context,
	cursor storage.DirectoryProjectionCursor,
	observedUnix int64,
) (bool, error) {
	if !cursor.Valid() || cursor == (storage.DirectoryProjectionCursor{}) {
		return false, storage.ErrDirectoryProjectionInput
	}
	scanned := 0
	for tier := cursor.Tier; tier.Valid(); tier++ {
		boundary := ""
		if tier == cursor.Tier {
			boundary = cursor.RelayActor
		}
		remaining := storage.MaximumDirectoryProjectionScan - scanned
		if remaining <= 0 {
			return true, nil
		}
		candidates, err := repository.readDirectoryCandidateActorsAfter(
			ctx,
			boundary,
			remaining+1,
		)
		if err != nil {
			return false, err
		}
		scanCount := minInt(len(candidates), remaining)
		if scanCount > 0 {
			details, err := repository.readDirectoryProjectionDetails(
				ctx,
				candidates[:scanCount],
				observedUnix,
			)
			if err != nil {
				return false, err
			}
			for _, actor := range candidates[:scanCount] {
				scanned++
				relay, exists := details[actor]
				if !exists {
					return false, storageFailure(
						"validate public directory projection",
						errors.New("retained directory candidate disappeared"),
					)
				}
				if relay != nil && relay.Tier == tier {
					return true, nil
				}
			}
		}
		if len(candidates) > scanCount {
			return true, nil
		}
		if scanned >= storage.MaximumDirectoryProjectionScan && tier < storage.DirectoryTierGraveyard {
			return true, nil
		}
		if tier == storage.DirectoryTierGraveyard {
			break
		}
	}
	return false, nil
}

func (repository *RelayRepository) hasDirectoryRelayBefore(
	ctx context.Context,
	cursor storage.DirectoryProjectionCursor,
	observedUnix int64,
) (bool, error) {
	if !cursor.Valid() || cursor == (storage.DirectoryProjectionCursor{}) {
		return false, storage.ErrDirectoryProjectionInput
	}
	scanned := 0
	for tier := cursor.Tier; ; tier-- {
		boundary := ""
		if tier == cursor.Tier {
			boundary = cursor.RelayActor
		}
		remaining := storage.MaximumDirectoryProjectionScan - scanned
		if remaining <= 0 {
			return true, nil
		}
		candidates, err := repository.readDirectoryCandidateActorsBefore(
			ctx,
			boundary,
			remaining+1,
		)
		if err != nil {
			return false, err
		}
		scanCount := minInt(len(candidates), remaining)
		if scanCount > 0 {
			details, err := repository.readDirectoryProjectionDetails(
				ctx,
				candidates[:scanCount],
				observedUnix,
			)
			if err != nil {
				return false, err
			}
			for _, actor := range candidates[:scanCount] {
				scanned++
				relay, exists := details[actor]
				if !exists {
					return false, storageFailure(
						"validate public directory projection",
						errors.New("retained directory candidate disappeared"),
					)
				}
				if relay != nil && relay.Tier == tier {
					return true, nil
				}
			}
		}
		if len(candidates) > scanCount {
			return true, nil
		}
		if scanned >= storage.MaximumDirectoryProjectionScan && tier > storage.DirectoryTierHeartbeatOnline {
			return true, nil
		}
		if tier == storage.DirectoryTierHeartbeatOnline {
			break
		}
	}
	return false, nil
}

func (repository *RelayRepository) readDirectoryCandidateActorsAfter(
	ctx context.Context,
	boundary string,
	limit int,
) ([]string, error) {
	relays, err := readDirectoryCandidateActors(
		ctx,
		repository.database,
		directoryRelayCandidateSQL,
		boundary,
		lifecycleRegistered,
		lifecyclePruned,
		administrativeActive,
		limit,
	)
	if err != nil {
		return nil, err
	}
	discoveries, err := readDirectoryCandidateActors(
		ctx,
		repository.database,
		directoryDiscoveryCandidateSQL,
		boundary,
		discoveryActive,
		administrativeSuspended,
		limit,
	)
	if err != nil {
		return nil, err
	}
	return mergeDirectoryActors(relays, discoveries, limit), nil
}

func (repository *RelayRepository) readDirectoryCandidateActorsBefore(
	ctx context.Context,
	boundary string,
	limit int,
) ([]string, error) {
	var (
		relays      []string
		discoveries []string
		err         error
	)
	if boundary == "" {
		relays, err = readDirectoryCandidateActors(
			ctx,
			repository.database,
			directoryRelayLastCandidateSQL,
			lifecycleRegistered,
			lifecyclePruned,
			administrativeActive,
			limit,
		)
		if err != nil {
			return nil, err
		}
		discoveries, err = readDirectoryCandidateActors(
			ctx,
			repository.database,
			directoryDiscoveryLastCandidateSQL,
			discoveryActive,
			administrativeSuspended,
			limit,
		)
	} else {
		relays, err = readDirectoryCandidateActors(
			ctx,
			repository.database,
			directoryRelayPreviousCandidateSQL,
			boundary,
			lifecycleRegistered,
			lifecyclePruned,
			administrativeActive,
			limit,
		)
		if err != nil {
			return nil, err
		}
		discoveries, err = readDirectoryCandidateActors(
			ctx,
			repository.database,
			directoryDiscoveryPreviousCandidateSQL,
			boundary,
			discoveryActive,
			administrativeSuspended,
			limit,
		)
	}
	if err != nil {
		return nil, err
	}
	return mergeDirectoryActorsDescending(relays, discoveries, limit), nil
}

func readDirectoryCandidateActors(
	ctx context.Context,
	database *sql.DB,
	statement string,
	arguments ...any,
) ([]string, error) {
	rows, err := database.QueryContext(ctx, statement, arguments...)
	if err != nil {
		return nil, storageFailure("read public directory candidates", err)
	}
	defer rows.Close()

	limit := storage.MaximumDirectoryProjectionScan + 1
	actors := make([]string, 0, minInt(limit, 64))
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
		if len(actors) >= limit {
			return nil, storageFailure(
				"validate public directory candidates",
				errors.New("directory candidate read exceeded bound"),
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

func mergeDirectoryActorsDescending(left, right []string, limit int) []string {
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
		case left[leftIndex] > right[rightIndex]:
			actor = left[leftIndex]
			leftIndex++
		case right[rightIndex] > left[leftIndex]:
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
       relay.first_registered_at_unix,
       relay.last_seen_at_unix,
       relay.last_heartbeat_at_unix,
       discovery.public_base_url,
       discovery.discovery_state,
       discovery.first_discovered_at_unix,
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
			firstRegistered     sql.NullInt64
			lastSeen            sql.NullInt64
			lastHeartbeat       sql.NullInt64
			discoveryBase       sql.NullString
			discoveryState      sql.NullString
			firstDiscovered     sql.NullInt64
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
			&firstRegistered,
			&lastSeen,
			&lastHeartbeat,
			&discoveryBase,
			&discoveryState,
			&firstDiscovered,
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

		relayExists := relayBase.Valid || relayLifecycle.Valid || relayAdministrative.Valid ||
			firstRegistered.Valid || lastSeen.Valid || lastHeartbeat.Valid
		if relayExists {
			if !relayBase.Valid || !relayLifecycle.Valid || !relayAdministrative.Valid ||
				!firstRegistered.Valid || !lastSeen.Valid ||
				!validDirectoryLifecycleState(relayLifecycle.String) ||
				!validDirectoryAdministrativeState(relayAdministrative.String) {
				return nil, storageFailure("validate public directory lifecycle state", errors.New("invalid retained lifecycle state"))
			}
		}
		discoveryExists := discoveryBase.Valid || discoveryState.Valid || firstDiscovered.Valid
		if discoveryExists {
			if !discoveryBase.Valid || !discoveryState.Valid || !firstDiscovered.Valid ||
				!validDirectoryDiscoveryState(discoveryState.String) {
				return nil, storageFailure("validate public directory discovery state", errors.New("invalid retained discovery state"))
			}
		}

		if relayAdministrative.Valid && relayAdministrative.String == administrativeSuspended {
			result[actor] = nil
			continue
		}
		lifecycleKnown := relayLifecycle.Valid &&
			(relayLifecycle.String == lifecycleRegistered || relayLifecycle.String == lifecyclePruned) &&
			relayAdministrative.Valid && relayAdministrative.String == administrativeActive
		registered := relayLifecycle.Valid && relayLifecycle.String == lifecycleRegistered &&
			relayAdministrative.Valid && relayAdministrative.String == administrativeActive
		discovered := discoveryState.Valid && discoveryState.String == discoveryActive
		if !lifecycleKnown && !discovered {
			result[actor] = nil
			continue
		}

		firstKnown, ok := firstDirectoryKnownUnix(firstRegistered, firstDiscovered)
		if !ok {
			return nil, storageFailure("validate public directory details", errors.New("public identity has no first-known time"))
		}
		relay := &storage.DirectoryProjectionRelay{
			RelayActor:           actor,
			LifecycleKnown:       lifecycleKnown,
			Registered:           registered,
			Discovered:           discovered,
			FirstKnownUnix:       firstKnown,
			LastSeenUnix:         nullableInt64Pointer(lastSeen),
			LastHeartbeatUnix:    nullableInt64Pointer(lastHeartbeat),
			ActorState:           storage.ReachabilityUnknown,
			ActorLastCheckedUnix: nullableInt64Pointer(actorLastChecked),
			ActorLastSuccessUnix: nullableInt64Pointer(actorLastSuccess),
			InboxDeclaredUnix:    nullableInt64Pointer(inboxDeclared),
			InboxProbeState:      storage.InboxNotChecked,
			InboxLastCheckedUnix: nullableInt64Pointer(inboxLastChecked),
			RFC9421VerifiedUnix:  nullableInt64Pointer(rfc9421Verified),
		}
		if lifecycleKnown {
			if !relayBase.Valid {
				return nil, storageFailure("validate public directory details", errors.New("known lifecycle relay missing public base URL"))
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
		relay.Tier, err = storage.ClassifyDirectoryTier(*relay, observedUnix)
		if err != nil {
			return nil, storageFailure("classify public directory tier", err)
		}
		if err := storage.ValidateDirectoryProjectionEvidence(*relay, observedUnix); err != nil {
			return nil, storageFailure("validate public directory evidence", err)
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

func firstDirectoryKnownUnix(firstRegistered, firstDiscovered sql.NullInt64) (int64, bool) {
	switch {
	case firstRegistered.Valid && firstDiscovered.Valid:
		if firstRegistered.Int64 <= firstDiscovered.Int64 {
			return firstRegistered.Int64, true
		}
		return firstDiscovered.Int64, true
	case firstRegistered.Valid:
		return firstRegistered.Int64, true
	case firstDiscovered.Valid:
		return firstDiscovered.Int64, true
	default:
		return 0, false
	}
}

func directoryProjectionCursorForRelay(relay storage.DirectoryProjectionRelay) storage.DirectoryProjectionCursor {
	return storage.DirectoryProjectionCursor{Tier: relay.Tier, RelayActor: relay.RelayActor}
}

func reverseDirectoryRelays(relays []storage.DirectoryProjectionRelay) {
	for left, right := 0, len(relays)-1; left < right; left, right = left+1, right-1 {
		relays[left], relays[right] = relays[right], relays[left]
	}
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
