package storage

import (
	"context"
	"errors"
	"time"
)

const (
	DiscoveryCandidateMaintenanceInterval = time.Hour
	MaximumDiscoveryCandidatePage         = 24
	MaximumDiscoveryCandidateAttempts     = 96
	MaximumConcurrentCandidateProbes      = 8

	DiscoveryCandidateRetryOne   = 6 * time.Hour
	DiscoveryCandidateRetryTwo   = 12 * time.Hour
	DiscoveryCandidateRetryThree = 24 * time.Hour
	DiscoveryCandidateRetryFour  = 72 * time.Hour
	DiscoveryCandidateRetryLong  = 7 * 24 * time.Hour
)

var (
	ErrDiscoveryCandidateMaintenanceRead  = errors.New("discovery candidate maintenance read input is invalid")
	ErrDiscoveryCandidateMaintenanceWrite = errors.New("discovery candidate maintenance write input is invalid")
)

func DiscoveryCandidateRetryDelay(failureCount int64) time.Duration {
	switch failureCount {
	case 1:
		return DiscoveryCandidateRetryOne
	case 2:
		return DiscoveryCandidateRetryTwo
	case 3:
		return DiscoveryCandidateRetryThree
	case 4:
		return DiscoveryCandidateRetryFour
	default:
		if failureCount >= 5 {
			return DiscoveryCandidateRetryLong
		}
		return 0
	}
}

type DiscoveryCandidateCheckCursor struct {
	NextCheckUnix int64
	CandidateURL  string
}

func (cursor DiscoveryCandidateCheckCursor) Valid() bool {
	if cursor == (DiscoveryCandidateCheckCursor{}) {
		return true
	}
	return cursor.NextCheckUnix >= 0 && cursor.CandidateURL != ""
}

type DiscoveryCandidateCheckQuery struct {
	After      DiscoveryCandidateCheckCursor
	Limit      int
	ObservedAt time.Time
}

type DiscoveryCandidateCheck struct {
	CandidateActorURL string
	PublicBaseURL     string
	State             DiscoveryCandidateState
	LastFailure       DiscoveryCandidateFailure
	FirstSeenUnix     int64
	LastCheckedUnix   int64
	LastSuccessUnix   *int64
	FailureCount      int64
	NextCheckUnix     int64
	OperatorID        string
	ReasonCode        string
	SourceKind        DiscoverySourceKind
	SourceLabel       string
}

type DiscoveryCandidateCheckPage struct {
	Candidates []DiscoveryCandidateCheck
	Next       DiscoveryCandidateCheckCursor
}

type DiscoveryCandidatePromotionIntent struct {
	CandidateActorURL string
	PublicBaseURL     string
	InboxURL          string
	InboxState        InboxProbeState
	OperatorID        string
	ReasonCode        string
	SourceKind        DiscoverySourceKind
	SourceLabel       string
}

type DiscoveryCandidatePromotionOutcome string

const (
	DiscoveryCandidatePromoted DiscoveryCandidatePromotionOutcome = "promoted"
	DiscoveryCandidateSkipped  DiscoveryCandidatePromotionOutcome = "skipped"
)

func (outcome DiscoveryCandidatePromotionOutcome) Valid() bool {
	return outcome == DiscoveryCandidatePromoted || outcome == DiscoveryCandidateSkipped
}

type DiscoveryCandidateMaintenanceRepository interface {
	DiscoveryCandidateChecks(context.Context, DiscoveryCandidateCheckQuery) (DiscoveryCandidateCheckPage, error)
	RetainDiscoveryCandidate(context.Context, DiscoveryCandidateIntent, time.Time) (DiscoveryCandidateOutcome, error)
	PromoteDiscoveryCandidate(context.Context, DiscoveryCandidatePromotionIntent, time.Time) (DiscoveryCandidatePromotionOutcome, error)
}
