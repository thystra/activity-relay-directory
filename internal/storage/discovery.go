package storage

import (
	"context"
	"errors"
	"time"
)

const (
	MaximumDiscoverySourceLabelBytes = 128

	DiscoveryActive  DiscoveryState = "active"
	DiscoveryRemoved DiscoveryState = "removed"

	DiscoverySourceManual DiscoverySourceKind = "manual"
	DiscoverySourceFile   DiscoverySourceKind = "file"

	DiscoveryAdded     DiscoveryOutcome = "added"
	DiscoveryUpdated   DiscoveryOutcome = "updated"
	DiscoveryUnchanged DiscoveryOutcome = "unchanged"
	DiscoveryRemovedOK DiscoveryOutcome = "removed"
	DiscoveryAbsent    DiscoveryOutcome = "absent"

	ReachabilityUnknown     ReachabilityState = "unknown"
	ReachabilityReachable   ReachabilityState = "reachable"
	ReachabilityUnreachable ReachabilityState = "unreachable"

	InboxNotChecked     InboxProbeState = "not_checked"
	InboxResponsive     InboxProbeState = "responsive"
	InboxMethodRejected InboxProbeState = "method_rejected"
	InboxUnreachable    InboxProbeState = "unreachable"
)

var (
	ErrDiscoveryReadInput  = errors.New("discovery read input is invalid")
	ErrObservationAbsent   = errors.New("relay observation identity is not retained")
	ErrObservationInput    = errors.New("relay observation input is invalid")
	ErrObservationTime     = errors.New("relay observation time is invalid")
	ErrObservationConflict = errors.New("relay observation target changed")
)

type DiscoveryState string

func (state DiscoveryState) Valid() bool {
	return state == DiscoveryActive || state == DiscoveryRemoved
}

type DiscoverySourceKind string

func (kind DiscoverySourceKind) Valid() bool {
	return kind == DiscoverySourceManual || kind == DiscoverySourceFile
}

type DiscoveryOutcome string

func (outcome DiscoveryOutcome) Valid() bool {
	switch outcome {
	case DiscoveryAdded, DiscoveryUpdated, DiscoveryUnchanged, DiscoveryRemovedOK, DiscoveryAbsent:
		return true
	default:
		return false
	}
}

// ValidDiscoverySourceLabel applies the same bounded ASCII token grammar used
// for local operator identifiers. Empty means that no private label was supplied.
func ValidDiscoverySourceLabel(value string) bool {
	if value == "" {
		return true
	}
	if len(value) > MaximumDiscoverySourceLabelBytes {
		return false
	}
	for index := 0; index < len(value); index++ {
		character := value[index]
		if (character >= 'A' && character <= 'Z') ||
			(character >= 'a' && character <= 'z') ||
			(character >= '0' && character <= '9') {
			continue
		}
		if index > 0 && (character == '@' || character == '.' ||
			character == '_' || character == ':' || character == '+' ||
			character == '-') {
			continue
		}
		return false
	}
	return true
}

type DiscoveryAddIntent struct {
	RelayActor    string
	PublicBaseURL string
	OperatorID    string
	ReasonCode    string
	SourceKind    DiscoverySourceKind
	SourceLabel   string
}

type DiscoveryRemoveIntent struct {
	RelayActor  string
	OperatorID  string
	ReasonCode  string
	SourceKind  DiscoverySourceKind
	SourceLabel string
}

type DiscoveryRecord struct {
	RelayActor          string
	PublicBaseURL       string
	State               DiscoveryState
	FirstDiscoveredUnix int64
	UpdatedUnix         int64
	RemovedUnix         *int64
}

type DiscoveryRepository interface {
	AddDiscovery(context.Context, DiscoveryAddIntent, time.Time) (DiscoveryOutcome, error)
	RemoveDiscovery(context.Context, DiscoveryRemoveIntent, time.Time) (DiscoveryOutcome, error)
	GetDiscovery(context.Context, IdentityIntent) (DiscoveryRecord, bool, error)
}

type ReachabilityState string

func (state ReachabilityState) Valid() bool {
	return state == ReachabilityUnknown || state == ReachabilityReachable || state == ReachabilityUnreachable
}

type InboxProbeState string

func (state InboxProbeState) Valid() bool {
	switch state {
	case InboxNotChecked, InboxResponsive, InboxMethodRejected, InboxUnreachable:
		return true
	default:
		return false
	}
}

// ActorObservationIntent records the result of one canonical actor fetch. A
// successful reachable observation replaces the actor-declared inbox; an empty
// InboxURL therefore intentionally clears a previously declared inbox.
type ActorObservationIntent struct {
	RelayActor string
	State      ReachabilityState
	InboxURL   string
}

type InboxObservationIntent struct {
	RelayActor string
	InboxURL   string
	State      InboxProbeState
}

type RelayObservation struct {
	RelayActor           string
	ActorState           ReachabilityState
	ActorLastCheckedUnix *int64
	ActorLastSuccessUnix *int64
	InboxURL             string
	InboxDeclaredUnix    *int64
	InboxProbeState      InboxProbeState
	InboxLastCheckedUnix *int64
	RFC9421VerifiedUnix  *int64
	UpdatedUnix          int64
}

type ObservationRepository interface {
	RecordActorObservation(context.Context, ActorObservationIntent, time.Time) error
	RecordInboxObservation(context.Context, InboxObservationIntent, time.Time) error
	GetObservation(context.Context, IdentityIntent) (RelayObservation, bool, error)
}
