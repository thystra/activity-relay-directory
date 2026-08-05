package storage

import (
	"context"
	"errors"
)

const (
	// MaximumModerationAuditPage limits one private local audit read.
	MaximumModerationAuditPage = 100

	// MaximumModerationReasonCodeBytes bounds private classification tokens.
	MaximumModerationReasonCodeBytes = 64

	LifecycleRegistered   RelayLifecycleState = "registered"
	LifecycleUnregistered RelayLifecycleState = "unregistered"

	AdministrativeActive    RelayAdministrativeState = "active"
	AdministrativeSuspended RelayAdministrativeState = "suspended"

	ModerationActionSuspendApplied   ModerationAction = "suspend_applied"
	ModerationActionSuspendUnchanged ModerationAction = "suspend_unchanged"
	ModerationActionRestoreApplied   ModerationAction = "restore_applied"
	ModerationActionRestoreUnchanged ModerationAction = "restore_unchanged"
)

var (
	// ErrAdministrativeReadInput identifies an invalid canonical actor, cursor,
	// or page limit supplied to a private local administrative read.
	ErrAdministrativeReadInput = errors.New("administrative read input is invalid")
)

// RelayLifecycleState is the retained lifecycle state visible to local
// operators. It is not a public directory projection.
type RelayLifecycleState string

// Valid reports whether a lifecycle state belongs to the current storage
// contract.
func (state RelayLifecycleState) Valid() bool {
	switch state {
	case LifecycleRegistered, LifecycleUnregistered:
		return true
	default:
		return false
	}
}

// RelayAdministrativeState is the current private moderation state.
type RelayAdministrativeState string

// Valid reports whether an administrative state belongs to the current
// storage contract.
func (state RelayAdministrativeState) Valid() bool {
	switch state {
	case AdministrativeActive, AdministrativeSuspended:
		return true
	default:
		return false
	}
}

// ModerationAction is the closed private audit action vocabulary.
type ModerationAction string

// Valid reports whether an action belongs to the current moderation audit
// contract.
func (action ModerationAction) Valid() bool {
	switch action {
	case ModerationActionSuspendApplied,
		ModerationActionSuspendUnchanged,
		ModerationActionRestoreApplied,
		ModerationActionRestoreUnchanged:
		return true
	default:
		return false
	}
}

// ValidModerationReasonCode reports whether a private classification token
// uses the shared bounded lowercase grammar.
func ValidModerationReasonCode(value string) bool {
	if len(value) == 0 || len(value) > MaximumModerationReasonCodeBytes {
		return false
	}
	for index := 0; index < len(value); index++ {
		character := value[index]
		if index == 0 {
			if character < 'a' || character > 'z' {
				return false
			}
			continue
		}
		if (character >= 'a' && character <= 'z') ||
			(character >= '0' && character <= '9') ||
			character == '_' || character == '-' {
			continue
		}
		return false
	}
	return true
}

// ModerationState is the bounded retained state returned only to a local
// operator. Nullable timestamps are represented by nil pointers.
type ModerationState struct {
	RelayActor          string
	PublicBaseURL       string
	LifecycleState      RelayLifecycleState
	AdministrativeState RelayAdministrativeState
	FirstRegisteredUnix int64
	UpdatedUnix         int64
	LastHeartbeatUnix   *int64
	UnregisteredUnix    *int64
	SuspendedUnix       *int64
}

// ModerationAuditCursor is an indexed keyset position. The zero value means
// start from the first event.
type ModerationAuditCursor struct {
	RecordedUnix int64
	EventID      int64
}

// Valid reports whether the cursor is either the zero starting cursor or a
// complete positive event position.
func (cursor ModerationAuditCursor) Valid() bool {
	if cursor == (ModerationAuditCursor{}) {
		return true
	}
	return cursor.RecordedUnix >= 0 && cursor.EventID > 0
}

// ModerationAuditQuery requests one bounded page for a canonical retained
// relay.
type ModerationAuditQuery struct {
	RelayActor string
	After      ModerationAuditCursor
	Limit      int
}

// ModerationAuditEvent contains private moderator and reason tokens. It must
// never be passed to public HTTP or listing code.
type ModerationAuditEvent struct {
	EventID      int64
	RelayActor   string
	Action       ModerationAction
	ModeratorID  string
	ReasonCode   string
	RecordedUnix int64
}

// ModerationAuditPage is one bounded keyset page. Next is the zero value when
// no later event was observed during this read.
type ModerationAuditPage struct {
	Events []ModerationAuditEvent
	Next   ModerationAuditCursor
}

// ModerationReadRepository exposes private local state and bounded audit reads.
// Implementations must not mutate relay or audit state.
type ModerationReadRepository interface {
	ModerationState(context.Context, string) (ModerationState, error)
	ModerationAudit(context.Context, ModerationAuditQuery) (ModerationAuditPage, error)
}
