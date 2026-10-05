package storage

import (
	"context"
	"errors"
	"slices"
	"time"

	v1 "github.com/thystra/activity-relay-directory/internal/protocol/v1"
)

const (
	// DefaultDirectoryProjectionPage is the public GET /v2/relays page size when
	// the caller omits an explicit limit.
	DefaultDirectoryProjectionPage = 50
	// MaximumDirectoryProjectionPage is the hard public page-size ceiling.
	MaximumDirectoryProjectionPage = 100
	// MaximumDirectoryProjectionScan bounds the retained identities examined by
	// one public directory request, including identities that belong to another
	// tier than the one currently being scanned. Sparse tier scans therefore
	// return a continuation cursor instead of walking the whole database.
	MaximumDirectoryProjectionScan                      = 400
	HeartbeatNotObserved           PublicHeartbeatState = "not_observed"
	HeartbeatHealthy               PublicHeartbeatState = "healthy"
	HeartbeatStale                 PublicHeartbeatState = "stale"
	HeartbeatDead                  PublicHeartbeatState = "dead"
	HeartbeatPrune                 PublicHeartbeatState = "prune"

	DirectoryTierHeartbeatOnline DirectoryTier = 1
	DirectoryTierOnline          DirectoryTier = 2
	DirectoryTierUnavailable     DirectoryTier = 3
	DirectoryTierGraveyard       DirectoryTier = 4

	DirectoryGraveyardAfter = 30 * 24 * time.Hour
)

var (
	ErrDirectoryProjectionInput = errors.New("directory projection input is invalid")
	ErrDirectoryProjectionData  = errors.New("directory projection data is invalid")
)

type PublicHeartbeatState string

type DirectoryTier uint8

func (tier DirectoryTier) Valid() bool {
	return tier >= DirectoryTierHeartbeatOnline && tier <= DirectoryTierGraveyard
}

func (state PublicHeartbeatState) Valid() bool {
	switch state {
	case HeartbeatNotObserved, HeartbeatHealthy, HeartbeatStale, HeartbeatDead, HeartbeatPrune:
		return true
	default:
		return false
	}
}

// ClassifyPublicHeartbeat applies the frozen version-1 health boundaries to
// retained heartbeat/liveness evidence. A nil heartbeat value means no retained
// authenticated heartbeat-equivalent lifecycle observation exists for this actor.
func ClassifyPublicHeartbeat(lastHeartbeatUnix *int64, observedUnix int64) (PublicHeartbeatState, error) {
	if observedUnix < 0 {
		return "", ErrHealthTime
	}
	if lastHeartbeatUnix == nil {
		return HeartbeatNotObserved, nil
	}
	health, err := ClassifyHealth(*lastHeartbeatUnix, observedUnix)
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

// DirectoryProjectionCursor is a stable public scan position ordered first by
// operational tier and then by canonical relay actor. Canonical relay actors
// are HTTPS URLs, so actor order is hostname order with the actor path as a
// deterministic tie-breaker. A continuation cursor may name a retained actor
// that was scanned but not returned because it belongs to another tier. The
// zero value starts at Tier 1.
type DirectoryProjectionCursor struct {
	Tier       DirectoryTier
	RelayActor string
}

func (cursor DirectoryProjectionCursor) Valid() bool {
	if cursor == (DirectoryProjectionCursor{}) {
		return true
	}
	if !cursor.Tier.Valid() || cursor.RelayActor == "" {
		return false
	}
	canonical, err := v1.NormalizeRelayActorURL(cursor.RelayActor)
	return err == nil && canonical == cursor.RelayActor
}

// DirectoryProjectionQuery captures one observation time for one bounded read.
// HTTP pagination uses the tier+actor keyset, while each request evaluates the
// latest retained evidence against its own current server time.
type DirectoryProjectionQuery struct {
	After      DirectoryProjectionCursor
	Before     DirectoryProjectionCursor
	Limit      int
	ObservedAt time.Time
}

// DirectoryProjectionRelay contains the richer public directory evidence model.
// Registered and Discovered are internal eligibility facts only; HTTP
// serializers must never expose either value or any discovery provenance.
type DirectoryProjectionRelay struct {
	RelayActor                 string
	PublicBaseURL              string
	Profile                    RelayProfile
	ReceivingInstanceCount     *int
	TelemetryReportedUnix      *int64
	ParticipatingInstanceCount *int
	ParticipatingReportedUnix  *int64

	LifecycleKnown bool
	Registered     bool
	Discovered     bool
	FirstKnownUnix int64
	Tier           DirectoryTier

	HeartbeatState    PublicHeartbeatState
	LastSeenUnix      *int64
	LastHeartbeatUnix *int64

	ActorState           ReachabilityState
	ActorLastCheckedUnix *int64
	ActorLastSuccessUnix *int64

	InboxURL             string
	InboxDeclaredUnix    *int64
	InboxProbeState      InboxProbeState
	InboxLastCheckedUnix *int64

	RFC9421VerifiedUnix *int64
}

// ClassifyDirectoryTier maps current public evidence into the directory's four
// non-prestige operational tiers. Tier ordering is explicit; alphabetical actor
// order is used inside each tier. A fresh reachable actor is online. A healthy
// authenticated heartbeat plus fresh reachability is Tier 1. Relays not seen
// online for 30 days enter the graveyard.
func ClassifyDirectoryTier(relay DirectoryProjectionRelay, observedUnix int64) (DirectoryTier, error) {
	if observedUnix < 0 || relay.FirstKnownUnix < 0 || relay.FirstKnownUnix > observedUnix ||
		!relay.HeartbeatState.Valid() || !relay.ActorState.Valid() || !relay.InboxProbeState.Valid() {
		return 0, ErrDirectoryProjectionData
	}

	freshReachability := false
	if relay.ActorState == ReachabilityReachable && relay.ActorLastCheckedUnix != nil && relay.ActorLastSuccessUnix != nil &&
		*relay.ActorLastCheckedUnix == *relay.ActorLastSuccessUnix && *relay.ActorLastSuccessUnix <= observedUnix {
		cutoff := observedUnix - int64(ReachabilityFreshness/time.Second)
		freshReachability = *relay.ActorLastSuccessUnix >= cutoff
	}
	currentHeartbeat := false
	if relay.Registered && relay.LastHeartbeatUnix != nil && *relay.LastHeartbeatUnix <= observedUnix {
		heartbeatCutoff := observedUnix - int64(HealthyThrough/time.Second)
		currentHeartbeat = *relay.LastHeartbeatUnix >= heartbeatCutoff
	}
	if freshReachability && currentHeartbeat {
		return DirectoryTierHeartbeatOnline, nil
	}
	if freshReachability {
		return DirectoryTierOnline, nil
	}

	lastOnline := relay.FirstKnownUnix
	if relay.LastSeenUnix != nil && *relay.LastSeenUnix > lastOnline {
		lastOnline = *relay.LastSeenUnix
	}
	if relay.ActorLastSuccessUnix != nil && *relay.ActorLastSuccessUnix > lastOnline {
		lastOnline = *relay.ActorLastSuccessUnix
	}
	graveyardCutoff := observedUnix - int64(DirectoryGraveyardAfter/time.Second)
	if lastOnline <= graveyardCutoff {
		return DirectoryTierGraveyard, nil
	}
	return DirectoryTierUnavailable, nil
}

// PublicEligible verifies that the retained identity belongs to a public
// participation path and that its assigned tier agrees with current evidence.
// An explicit unregister or discovery removal is not public; a soft-pruned
// lifecycle row remains known and can appear in the unavailable/graveyard tiers.
func (relay DirectoryProjectionRelay) PublicEligible(observedUnix int64) bool {
	if observedUnix < 0 || (!relay.LifecycleKnown && !relay.Discovered) || !relay.Tier.Valid() {
		return false
	}
	tier, err := ClassifyDirectoryTier(relay, observedUnix)
	return err == nil && tier == relay.Tier
}

// ValidateDirectoryProjectionEvidence checks canonical public identity, the
// internal evidence relationships, and timestamp bounds against one captured
// server time. It deliberately knows only the boolean participation paths,
// never private discovery provenance.
func ValidateDirectoryProjectionEvidence(relay DirectoryProjectionRelay, observedUnix int64) error {
	if observedUnix < 0 || (!relay.LifecycleKnown && !relay.Discovered) ||
		relay.Registered && !relay.LifecycleKnown || !relay.Tier.Valid() ||
		relay.FirstKnownUnix < 0 || relay.FirstKnownUnix > observedUnix {
		return ErrDirectoryProjectionData
	}
	identity, err := v1.NormalizeRelayIdentity(relay.RelayActor, relay.PublicBaseURL)
	if err != nil || identity.RelayActor != relay.RelayActor || identity.PublicBaseURL != relay.PublicBaseURL {
		return ErrDirectoryProjectionData
	}
	normalizedProfile, err := NormalizeRelayProfile(relay.Profile)
	if err != nil || !equalRelayProfile(relay.Profile, normalizedProfile) {
		return ErrDirectoryProjectionData
	}
	if (relay.ReceivingInstanceCount == nil) != (relay.TelemetryReportedUnix == nil) ||
		(relay.ParticipatingInstanceCount == nil) != (relay.ParticipatingReportedUnix == nil) {
		return ErrDirectoryProjectionData
	}
	if relay.ReceivingInstanceCount != nil && (*relay.ReceivingInstanceCount < 0 || *relay.ReceivingInstanceCount > MaximumReceivingInstanceCount) {
		return ErrDirectoryProjectionData
	}
	if relay.ParticipatingInstanceCount != nil && (*relay.ParticipatingInstanceCount < 0 || *relay.ParticipatingInstanceCount > MaximumParticipatingInstanceCount) {
		return ErrDirectoryProjectionData
	}
	if !validObservedTime(relay.TelemetryReportedUnix, observedUnix) || !validObservedTime(relay.ParticipatingReportedUnix, observedUnix) {
		return ErrDirectoryProjectionData
	}

	heartbeat, err := ClassifyPublicHeartbeat(relay.LastHeartbeatUnix, observedUnix)
	if err != nil || heartbeat != relay.HeartbeatState {
		return ErrDirectoryProjectionData
	}
	if relay.LifecycleKnown && relay.LastSeenUnix == nil {
		return ErrDirectoryProjectionData
	}
	if !validObservedTime(relay.LastHeartbeatUnix, observedUnix) ||
		relay.LastHeartbeatUnix != nil && (relay.LastSeenUnix == nil || *relay.LastHeartbeatUnix > *relay.LastSeenUnix) {
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
	tier, err := ClassifyDirectoryTier(relay, observedUnix)
	if err != nil || tier != relay.Tier {
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

func equalRelayProfile(left, right RelayProfile) bool {
	return left.ParticipationMode == right.ParticipationMode &&
		left.Availability == right.Availability &&
		left.RelayType == right.RelayType &&
		slices.Equal(left.Languages, right.Languages) &&
		slices.Equal(left.Countries, right.Countries) &&
		slices.Equal(left.Regions, right.Regions) &&
		slices.Equal(left.Topics, right.Topics) &&
		left.ContactFediverse == right.ContactFediverse &&
		left.ContactEmail == right.ContactEmail &&
		left.ContactURL == right.ContactURL &&
		left.ParticipationURL == right.ParticipationURL &&
		left.Notes == right.Notes
}

func validObservedTime(value *int64, observedUnix int64) bool {
	return value == nil || (*value >= 0 && *value <= observedUnix)
}

type DirectoryProjectionPage struct {
	Relays   []DirectoryProjectionRelay
	Previous DirectoryProjectionCursor
	Next     DirectoryProjectionCursor
}

// DirectorySummary contains aggregate public relay counts for the human
// directory. PendingVerification is aggregate-only: unresolved candidate
// identities remain private until canonical actor validation succeeds.
type DirectorySummary struct {
	KnownRelays         int
	OnlineRelays        int
	OfflineRelays       int
	PendingVerification int
}

func (summary DirectorySummary) Valid() bool {
	return summary.KnownRelays >= 0 && summary.OnlineRelays >= 0 &&
		summary.OfflineRelays >= 0 && summary.PendingVerification >= 0 &&
		summary.OnlineRelays+summary.OfflineRelays == summary.KnownRelays
}

// DirectorySummaryRepository reads aggregate public relay counts plus the
// number of unresolved private discovery candidates. It never exposes candidate
// identities or provenance.
type DirectorySummaryRepository interface {
	ReadDirectorySummary(context.Context, time.Time) (DirectorySummary, error)
}

// DirectoryProjectionRepository reads the richer public directory projection.
// Implementations must apply administrative suspension and both authorization
// paths before any row reaches HTTP presentation code.
type DirectoryProjectionRepository interface {
	ListDirectoryRelays(context.Context, DirectoryProjectionQuery) (DirectoryProjectionPage, error)
}
