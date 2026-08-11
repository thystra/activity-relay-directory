// Package storage defines persistence contracts shared by directory backends.
package storage

import (
	"context"
	"errors"
	"time"
)

const (
	// DefaultDatabaseMaxBytes is the initial managed SQLite database-family
	// budget from roadmap Tranche 17.
	DefaultDatabaseMaxBytes int64 = 1 << 30
	// MaximumDatabaseMaxBytes prevents configuration overflow and accidental
	// effectively-unbounded limits.
	MaximumDatabaseMaxBytes int64 = 1 << 40

	DefaultDatabaseWarningPercent = 75
	DatabaseCriticalPercent       = 90
	DatabaseHardPercent           = 100

	// DatabaseGrowthSampleInterval is the fixed runtime observation interval.
	DatabaseGrowthSampleInterval = 5 * time.Minute
	// DatabaseGrowthReminderInterval bounds repeated notification while a state
	// remains active.
	DatabaseGrowthReminderInterval = 24 * time.Hour
	// DatabaseGrowthRecoveryHysteresisPercent prevents oscillation at downward
	// state boundaries.
	DatabaseGrowthRecoveryHysteresisPercent = 5

	// MaximumDatabaseGrowthHeadroomBytes is reserved from SQLite max_page_count
	// as application planning headroom for WAL/control/migration work. It is not
	// an active-WAL or migration-WAL quota. Smaller configured budgets reserve one
	// eighth of the total instead.
	MaximumDatabaseGrowthHeadroomBytes int64 = 128 << 20
	// DatabaseJournalSizeLimitBytes is the post-checkpoint WAL/journal retention
	// target. It does not replace the total database-family hard limit.
	DatabaseJournalSizeLimitBytes int64 = 16 << 20
	// DatabaseWALAutoCheckpointPages requests frequent ordinary WAL checkpoints;
	// it is not an active-transaction WAL byte quota.
	DatabaseWALAutoCheckpointPages = 256
)

var (
	// ErrWriteAdmissionHard means the managed database has reached its hard
	// growth boundary. Callers must preserve existing data and fail the write
	// before beginning its transaction.
	ErrWriteAdmissionHard = errors.New("database growth hard limit reached")
	// ErrWriteAdmissionDenied is used for intentionally read-only repository
	// adapters.
	ErrWriteAdmissionDenied = errors.New("database writes are disabled for this repository")
)

// WriteLease serializes one admitted database mutation with the growth sampler.
// Release must be called after the transaction has committed or rolled back.
type WriteLease interface {
	Release()
}

// WriteAdmission is the common pre-transaction gate for all runtime database
// mutations.
type WriteAdmission interface {
	AcquireWrite(context.Context) (WriteLease, error)
}

type writeAdmissionFunc func(context.Context) (WriteLease, error)

func (f writeAdmissionFunc) AcquireWrite(ctx context.Context) (WriteLease, error) {
	return f(ctx)
}

type writeLeaseFunc func()

func (f writeLeaseFunc) Release() {
	if f != nil {
		f()
	}
}

// AllowWrites is intended for isolated tests that do not exercise Tranche 17.
// Production mutation paths must use the configured growth guard.
var AllowWrites WriteAdmission = writeAdmissionFunc(func(ctx context.Context) (WriteLease, error) {
	if ctx == nil {
		return nil, ErrRepositoryConfiguration
	}
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	default:
	}
	return writeLeaseFunc(func() {}), nil
})

// DenyWrites is suitable for repositories created only for public or local
// read-only inspection.
var DenyWrites WriteAdmission = writeAdmissionFunc(func(ctx context.Context) (WriteLease, error) {
	if ctx == nil {
		return nil, ErrRepositoryConfiguration
	}
	return nil, ErrWriteAdmissionDenied
})

// DatabaseGrowthState is the closed operational storage-pressure vocabulary.
type DatabaseGrowthState string

const (
	DatabaseGrowthNormal   DatabaseGrowthState = "normal"
	DatabaseGrowthWarning  DatabaseGrowthState = "warning"
	DatabaseGrowthCritical DatabaseGrowthState = "critical"
	DatabaseGrowthHard     DatabaseGrowthState = "hard"
)

func (state DatabaseGrowthState) Valid() bool {
	switch state {
	case DatabaseGrowthNormal, DatabaseGrowthWarning, DatabaseGrowthCritical, DatabaseGrowthHard:
		return true
	default:
		return false
	}
}

// DatabaseGrowthSample is one identity-free SQLite storage observation.
type DatabaseGrowthSample struct {
	ObservedUnix          int64
	State                 DatabaseGrowthState
	PressurePercent       int
	PageSize              int64
	PageCount             int64
	UsedPages             int64
	ReusablePages         int64
	MaxPageCount          int64
	UsedLogicalBytes      int64
	AllocatedLogicalBytes int64
	MainPageLimitBytes    int64
	PhysicalMainBytes     int64
	WALBytes              int64
	SHMBytes              int64
	PhysicalFamilyBytes   int64
	GrowthBytes           int64
	GrowthKnown           bool
	MaxBytes              int64
	WarningPercent        int
	RetentionDays         int
	WriteAllowed          bool
}

// DatabaseGrowthAlertKind is the bounded notification vocabulary.
type DatabaseGrowthAlertKind string

const (
	DatabaseGrowthAlertWarning   DatabaseGrowthAlertKind = "warning"
	DatabaseGrowthAlertCritical  DatabaseGrowthAlertKind = "critical"
	DatabaseGrowthAlertHard      DatabaseGrowthAlertKind = "hard-limit"
	DatabaseGrowthAlertRecovered DatabaseGrowthAlertKind = "recovered"
	DatabaseGrowthAlertTest      DatabaseGrowthAlertKind = "test"
)

func (kind DatabaseGrowthAlertKind) Valid() bool {
	switch kind {
	case DatabaseGrowthAlertWarning,
		DatabaseGrowthAlertCritical,
		DatabaseGrowthAlertHard,
		DatabaseGrowthAlertRecovered,
		DatabaseGrowthAlertTest:
		return true
	default:
		return false
	}
}
