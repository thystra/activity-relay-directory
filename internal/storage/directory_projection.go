package storage

import (
	"context"
	"errors"
	"time"

	v1 "github.com/thystra/activity-relay-directory/internal/protocol/v1"
)

const (
	// DefaultDirectoryProjectionPage is the public GET /v2/relays page size when
	// the caller omits an explicit limit.
	DefaultDirectoryProjectionPage = 50
	// MaximumDirectoryProjectionPage is the hard public page-size ceiling.
	MaximumDirectoryProjectionPage = 100
	// MaximumDirectoryProjectionScan bounds retained identities examined by one
	// public request when inactive rows are interleaved with eligible actors.
	MaximumDirectoryProjectionScan = 400

	HeartbeatNotObserved PublicHeartbeatState = "not_observed"
	HeartbeatHealthy     PublicHeartbeatState = "healthy"
	HeartbeatStale       PublicHeartbeatState = "stale"
	HeartbeatDead        PublicHeartbeatState = "dead"
	HeartbeatPrune       PublicHeartbeatState = "prune"
)

var (
	ErrDirectoryProjectionInput = errors.New("directory projection input is invalid")
	ErrDirectoryProjectionData  = errors.New("directory projection data is invalid")
)

type PublicHeartbeatState string

func (state PublicHeartbeatState) Valid() bool {
	switch state {
	case HeartbeatNotObserved, HeartbeatHealthy, HeartbeatStale, HeartbeatDead, HeartbeatPrune:
		return true
	default:
		return false
	}
}

// ClassifyPublicHeartbeat applies the frozen version-1 last-seen boundaries to
// the richer public projection. A nil last-seen value means no retained
// authenticated lifecycle observation exists for this actor.
func ClassifyPublicHeartbeat(lastSeenUnix *int64, observedUnix int64) (PublicHeartbeatState, error) {
	if observedUnix < 0 {
		return "", ErrHealthTime
	}
	if lastSeenUnix == nil {
		return HeartbeatNotObserved, nil
	}
	health, err := ClassifyHealth(*lastSeenUnix, observedUnix)
	if err != nil {
		return "", err
	}
	switch health {
	case v1.HealthHealthy:
		return HeartbeatHealthy, nil
	case v1.HealthStale:
		return HeartbeatStale, nil
	case v1.HealthDead:
		return HeartbeatDead, nil
	case v1.HealthPrune:
		return HeartbeatPrune, nil
	default:
		return "", ErrHealthTime
	}
}

// DirectoryProjectionCursor is a stable public keyset ordered by canonical
// relay actor. The zero value starts at the first actor.
type DirectoryProjectionCursor struct {
	RelayActor string
}

func (cursor DirectoryProjectionCursor) Valid() bool {
	if cursor == (DirectoryProjectionCursor{}) {
		return true
	}
	if cursor.RelayActor == "" {
		return false
	}
	canonical, err := v1.NormalizeRelayActorURL(cursor.RelayActor)
	return err == nil && canonical == cursor.RelayActor
}

// DirectoryProjectionQuery captures one observation time for one bounded read.
// HTTP pagination is actor-keyset based, while each request evaluates the latest
// retained evidence against its own current server time.
type DirectoryProjectionQuery struct {
	After      DirectoryProjectionCursor
	Limit      int
	ObservedAt time.Time
}

// DirectoryProjectionRelay contains the complete public 1.1 evidence model.
// Registered and Discovered are internal eligibility facts only; HTTP
// serializers must never expose either value or any discovery provenance.
type DirectoryProjectionRelay struct {
	RelayActor    string
	PublicBaseURL string

	Registered bool
	Discovered bool

	HeartbeatState PublicHeartbeatState
	LastSeenUnix   *int64

	ActorState           ReachabilityState
	ActorLastCheckedUnix *int64
	ActorLastSuccessUnix *int64

	InboxURL             string
	InboxDeclaredUnix    *int64
	InboxProbeState      InboxProbeState
	InboxLastCheckedUnix *int64

	RFC9421VerifiedUnix *int64
}

// PublicEligible independently verifies that at least one reviewed
// participation path authorizes this already administratively-filtered row.
// Registration may rely on the original <30-day heartbeat window or on current
// fresh reachability. Discovery requires current fresh successful reachability.
func (relay DirectoryProjectionRelay) PublicEligible(observedUnix int64) bool {
	if observedUnix < 0 || !relay.HeartbeatState.Valid() || !relay.ActorState.Valid() || !relay.InboxProbeState.Valid() {
		return false
	}
	freshReachability := false
	if relay.ActorState == ReachabilityReachable && relay.ActorLastCheckedUnix != nil && relay.ActorLastSuccessUnix != nil &&
		*relay.ActorLastCheckedUnix == *relay.ActorLastSuccessUnix && *relay.ActorLastSuccessUnix <= observedUnix {
		cutoff := observedUnix - int64(ReachabilityFreshness/time.Second)
		freshReachability = *relay.ActorLastSuccessUnix >= cutoff
	}
	registrationEligible := relay.Registered && relay.HeartbeatState != HeartbeatNotObserved &&
		(relay.HeartbeatState != HeartbeatPrune || freshReachability)
	discoveryEligible := relay.Discovered && freshReachability
	return registrationEligible || discoveryEligible
}

// ValidateDirectoryProjectionEvidence checks canonical public identity, the
// internal evidence relationships, and timestamp bounds against one captured
// server time. It deliberately knows only the boolean participation paths,
// never private discovery provenance.
func ValidateDirectoryProjectionEvidence(relay DirectoryProjectionRelay, observedUnix int64) error {
	if observedUnix < 0 || (!relay.Registered && !relay.Discovered) {
		return ErrDirectoryProjectionData
	}
	identity, err := v1.NormalizeRelayIdentity(relay.RelayActor, relay.PublicBaseURL)
	if err != nil || identity.RelayActor != relay.RelayActor || identity.PublicBaseURL != relay.PublicBaseURL {
		return ErrDirectoryProjectionData
	}

	heartbeat, err := ClassifyPublicHeartbeat(relay.LastSeenUnix, observedUnix)
	if err != nil || heartbeat != relay.HeartbeatState {
		return ErrDirectoryProjectionData
	}
	if relay.Registered && relay.LastSeenUnix == nil {
		return ErrDirectoryProjectionData
	}

	if !relay.ActorState.Valid() || !relay.InboxProbeState.Valid() {
		return ErrDirectoryProjectionData
	}
	if !validObservedTime(relay.ActorLastCheckedUnix, observedUnix) ||
		!validObservedTime(relay.ActorLastSuccessUnix, observedUnix) ||
		!validObservedTime(relay.InboxDeclaredUnix, observedUnix) ||
		!validObservedTime(relay.InboxLastCheckedUnix, observedUnix) ||
		!validObservedTime(relay.RFC9421VerifiedUnix, observedUnix) {
		return ErrDirectoryProjectionData
	}
	switch relay.ActorState {
	case ReachabilityUnknown:
		if relay.ActorLastCheckedUnix != nil || relay.ActorLastSuccessUnix != nil {
			return ErrDirectoryProjectionData
		}
	case ReachabilityReachable:
		if relay.ActorLastCheckedUnix == nil || relay.ActorLastSuccessUnix == nil ||
			*relay.ActorLastCheckedUnix != *relay.ActorLastSuccessUnix {
			return ErrDirectoryProjectionData
		}
	case ReachabilityUnreachable:
		if relay.ActorLastCheckedUnix == nil ||
			(relay.ActorLastSuccessUnix != nil && *relay.ActorLastSuccessUnix > *relay.ActorLastCheckedUnix) {
			return ErrDirectoryProjectionData
		}
	default:
		return ErrDirectoryProjectionData
	}

	if (relay.InboxURL == "") != (relay.InboxDeclaredUnix == nil) {
		return ErrDirectoryProjectionData
	}
	if relay.InboxURL != "" {
		canonical, err := v1.NormalizeRelayActorURL(relay.InboxURL)
		if err != nil || canonical != relay.InboxURL {
			return ErrDirectoryProjectionData
		}
	}
	if relay.InboxProbeState == InboxNotChecked {
		if relay.InboxLastCheckedUnix != nil {
			return ErrDirectoryProjectionData
		}
	} else if relay.InboxLastCheckedUnix == nil {
		return ErrDirectoryProjectionData
	}
	if relay.InboxDeclaredUnix != nil && relay.InboxLastCheckedUnix != nil &&
		*relay.InboxLastCheckedUnix < *relay.InboxDeclaredUnix {
		return ErrDirectoryProjectionData
	}

	return nil
}

// ValidateDirectoryProjectionRelay additionally requires that the structurally
// valid evidence authorizes current public inclusion. HTTP presentation uses
// this stronger check on every row returned by a repository.
func ValidateDirectoryProjectionRelay(relay DirectoryProjectionRelay, observedUnix int64) error {
	if err := ValidateDirectoryProjectionEvidence(relay, observedUnix); err != nil {
		return err
	}
	if !relay.PublicEligible(observedUnix) {
		return ErrDirectoryProjectionData
	}
	return nil
}

func validObservedTime(value *int64, observedUnix int64) bool {
	return value == nil || (*value >= 0 && *value <= observedUnix)
}

type DirectoryProjectionPage struct {
	Relays []DirectoryProjectionRelay
	Next   DirectoryProjectionCursor
}

// DirectoryProjectionRepository reads the richer public 1.1 projection.
// Implementations must apply administrative suspension and both authorization
// paths before any row reaches HTTP presentation code.
type DirectoryProjectionRepository interface {
	ListDirectoryRelays(context.Context, DirectoryProjectionQuery) (DirectoryProjectionPage, error)
}
