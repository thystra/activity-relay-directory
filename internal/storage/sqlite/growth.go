package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"sync"
	"time"

	"github.com/thystra/activity-relay-directory/internal/storage"
)

const (
	maximumGrowthPercentReport = 999
	growthMailerSubjectPrefix  = "Activity-Relay-Directory storage"
	maximumGrowthAlertBody     = 16 * 1024
)

var (
	ErrGrowthConfiguration = errors.New("SQLite database growth guard configuration is invalid")
	ErrGrowthSample        = errors.New("SQLite database growth sample failed")
)

// GrowthMailer is the no-shell administrator notification boundary.
type GrowthMailer interface {
	Send(context.Context, string, string) error
}

// DatabaseGrowthOptions binds one runtime growth guard to a migrated database.
type DatabaseGrowthOptions struct {
	Path             string
	MaxBytes         int64
	WarningPercent   int
	RetentionDays    int
	EmailEnabled     bool
	Mailer           GrowthMailer
	Now              func() time.Time
	SampleInterval   time.Duration
	ReminderInterval time.Duration
}

// DatabaseGrowthGuard serializes runtime mutations with growth sampling.
// It owns only bounded singleton alert state and never changes retention policy.
type DatabaseGrowthGuard struct {
	database      *sql.DB
	path          string
	maxBytes      int64
	warning       int
	retentionDays int
	emailEnabled  bool
	mailer        GrowthMailer
	now           func() time.Time
	interval      time.Duration
	reminder      time.Duration

	mu                  sync.Mutex
	wake                chan struct{}
	last                storage.DatabaseGrowthSample
	loaded              growthPersistentState
	mainPageLimitBytes  int64
	desiredMaxPageCount int64
	sampleHealthy       bool
}

// growthPersistentState mirrors the singleton migration row.
type growthPersistentState struct {
	State            storage.DatabaseGrowthState
	SampledUnix      int64
	PhysicalBytes    int64
	TransitionUnix   int64
	LastEmailKind    sql.NullString
	LastEmailUnix    sql.NullInt64
	PendingKind      sql.NullString
	PendingSinceUnix sql.NullInt64
	RetryAfterUnix   sql.NullInt64
	RetryAttempt     int
}

// ConfigureDatabaseGrowthLimit applies the persistent SQLite max_page_count
// backstop before migration. The returned logical main-file limit reserves a
// bounded slice of the configured family budget for WAL/control/migration work.
func ConfigureDatabaseGrowthLimit(
	ctx context.Context,
	database *sql.DB,
	maxBytes int64,
) (int64, int64, error) {
	desiredPages, expectedEffective, mainLimit, err := databaseGrowthPageLimits(
		ctx, database, maxBytes,
	)
	if err != nil {
		return 0, 0, err
	}
	query := fmt.Sprintf("PRAGMA max_page_count = %d", desiredPages)
	var effective int64
	if err := database.QueryRowContext(ctx, query).Scan(&effective); err != nil ||
		effective <= 0 {
		return 0, 0, fmt.Errorf("%w: max page count", ErrGrowthConfiguration)
	}
	if effective != expectedEffective {
		return 0, 0, fmt.Errorf("%w: max page count not enforced", ErrGrowthConfiguration)
	}
	return desiredPages, mainLimit, nil
}

func databaseGrowthPageLimits(
	ctx context.Context,
	database *sql.DB,
	maxBytes int64,
) (int64, int64, int64, error) {
	if ctx == nil || database == nil || maxBytes <= 0 ||
		maxBytes > storage.MaximumDatabaseMaxBytes {
		return 0, 0, 0, ErrGrowthConfiguration
	}
	pageSize, err := pragmaInt64(ctx, database, "page_size")
	if err != nil || pageSize <= 0 {
		return 0, 0, 0, fmt.Errorf("%w: page size", ErrGrowthConfiguration)
	}
	pageCount, err := pragmaInt64(ctx, database, "page_count")
	if err != nil || pageCount < 0 {
		return 0, 0, 0, fmt.Errorf("%w: page count", ErrGrowthConfiguration)
	}
	headroom := maxBytes / 8
	if headroom > storage.MaximumDatabaseGrowthHeadroomBytes {
		headroom = storage.MaximumDatabaseGrowthHeadroomBytes
	}
	minimumHeadroom := pageSize * 4
	if headroom < minimumHeadroom {
		headroom = minimumHeadroom
	}
	headroom = roundUp(headroom, pageSize)
	if headroom <= 0 || headroom >= maxBytes {
		return 0, 0, 0, ErrGrowthConfiguration
	}
	mainLimit := maxBytes - headroom
	desiredPages := mainLimit / pageSize
	if desiredPages <= 0 {
		return 0, 0, 0, ErrGrowthConfiguration
	}
	effectivePages := desiredPages
	if pageCount > effectivePages {
		// SQLite cannot lower max_page_count below an existing allocation. In
		// that case every guarded connection uses the current page count as the
		// no-new-page ceiling while desiredPages remains the policy denominator.
		effectivePages = pageCount
	}
	return desiredPages, effectivePages, desiredPages * pageSize, nil
}

func verifyDatabaseGrowthConnectionPolicy(
	ctx context.Context,
	database *sql.DB,
	maxBytes int64,
	desiredPages int64,
	mainLimit int64,
	cacheSpillEnabled bool,
) error {
	computedDesired, expectedEffective, computedMainLimit, err :=
		databaseGrowthPageLimits(ctx, database, maxBytes)
	if err != nil {
		return err
	}
	if computedDesired != desiredPages || computedMainLimit != mainLimit {
		return fmt.Errorf("%w: growth limit changed during open", ErrGrowthConfiguration)
	}
	effective, err := pragmaInt64(ctx, database, "max_page_count")
	if err != nil || effective != expectedEffective {
		return fmt.Errorf("%w: pooled max page count not enforced", ErrGrowthConfiguration)
	}
	cacheSpill, err := pragmaInt64(ctx, database, "cache_spill")
	if err != nil || (cacheSpillEnabled && cacheSpill == 0) ||
		(!cacheSpillEnabled && cacheSpill != 0) {
		return fmt.Errorf("%w: pooled cache spill policy not enforced", ErrGrowthConfiguration)
	}
	return nil
}

// CheckMigrationGrowthAdmission samples an open database before schema mutation.
// A hard state blocks migration; normal/warning/critical states retain the
// reviewed reserved headroom.
func CheckMigrationGrowthAdmission(
	ctx context.Context,
	database *sql.DB,
	path string,
	maxBytes int64,
	warningPercent int,
	desiredMaxPageCount int64,
	mainPageLimitBytes int64,
) error {
	sample, err := sampleDatabaseGrowth(
		ctx, database, path, maxBytes, warningPercent, 0,
		desiredMaxPageCount, mainPageLimitBytes, nil, storage.DatabaseGrowthNormal,
	)
	if err != nil {
		return err
	}
	if sample.State == storage.DatabaseGrowthHard {
		return storage.ErrWriteAdmissionHard
	}
	return nil
}

// NewDatabaseGrowthGuard loads the bounded persistent state, samples
// immediately, and schedules any transition notification without requiring
// email to be configured.
func NewDatabaseGrowthGuard(
	ctx context.Context,
	database *sql.DB,
	options DatabaseGrowthOptions,
) (*DatabaseGrowthGuard, error) {
	if ctx == nil || database == nil || options.Path == "" ||
		options.MaxBytes <= 0 || options.MaxBytes > storage.MaximumDatabaseMaxBytes ||
		options.WarningPercent <= 0 ||
		options.WarningPercent >= storage.DatabaseCriticalPercent ||
		options.RetentionDays < 0 ||
		options.Now == nil {
		return nil, ErrGrowthConfiguration
	}
	if options.SampleInterval == 0 {
		options.SampleInterval = storage.DatabaseGrowthSampleInterval
	}
	if options.ReminderInterval == 0 {
		options.ReminderInterval = storage.DatabaseGrowthReminderInterval
	}
	if options.SampleInterval <= 0 || options.ReminderInterval <= 0 ||
		(options.EmailEnabled && options.Mailer == nil) ||
		(!options.EmailEnabled && options.Mailer != nil) {
		return nil, ErrGrowthConfiguration
	}

	desiredPages, _, mainLimit, err := databaseGrowthPageLimits(
		ctx, database, options.MaxBytes,
	)
	if err != nil {
		return nil, err
	}
	if err := verifyDatabaseGrowthConnectionPolicy(
		ctx, database, options.MaxBytes, desiredPages, mainLimit, false,
	); err != nil {
		return nil, err
	}
	state, err := readGrowthPersistentState(ctx, database)
	if err != nil {
		return nil, err
	}

	guard := &DatabaseGrowthGuard{
		database:            database,
		path:                options.Path,
		maxBytes:            options.MaxBytes,
		warning:             options.WarningPercent,
		retentionDays:       options.RetentionDays,
		emailEnabled:        options.EmailEnabled,
		mailer:              options.Mailer,
		now:                 options.Now,
		interval:            options.SampleInterval,
		reminder:            options.ReminderInterval,
		wake:                make(chan struct{}, 1),
		loaded:              state,
		mainPageLimitBytes:  mainLimit,
		desiredMaxPageCount: desiredPages,
	}
	guard.mu.Lock()
	_, _, err = guard.sampleLocked(ctx, true)
	guard.mu.Unlock()
	if err != nil {
		return nil, err
	}
	guard.signalNotification()
	return guard, nil
}

// AcquireWrite samples immediately before a mutation and holds the guard mutex
// until Release, so no second guarded writer can race a hard-limit transition.
func (guard *DatabaseGrowthGuard) AcquireWrite(ctx context.Context) (storage.WriteLease, error) {
	if guard == nil || ctx == nil {
		return nil, storage.ErrRepositoryConfiguration
	}
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	default:
	}
	guard.mu.Lock()
	sample, transitioned, err := guard.sampleLocked(ctx, false)
	if err != nil {
		guard.mu.Unlock()
		return nil, err
	}
	if transitioned {
		guard.signalNotification()
	}
	if sample.State == storage.DatabaseGrowthHard {
		guard.mu.Unlock()
		return nil, storage.ErrWriteAdmissionHard
	}
	return &growthLease{guard: guard}, nil
}

type growthLease struct {
	guard *DatabaseGrowthGuard
	once  sync.Once
}

func (lease *growthLease) Release() {
	if lease == nil || lease.guard == nil {
		return
	}
	lease.once.Do(func() { lease.guard.mu.Unlock() })
}

// Ready returns the most recently sampled hard-limit state. Schema readiness
// remains a separate check owned by migrate.go.
func (guard *DatabaseGrowthGuard) Ready() error {
	if guard == nil {
		return ErrGrowthConfiguration
	}
	guard.mu.Lock()
	defer guard.mu.Unlock()
	if !guard.sampleHealthy {
		return ErrGrowthSample
	}
	if guard.last.State == storage.DatabaseGrowthHard {
		return storage.ErrWriteAdmissionHard
	}
	return nil
}

// SampleReadOnly returns a fresh sample without changing persistent alert state.
func (guard *DatabaseGrowthGuard) SampleReadOnly(ctx context.Context) (storage.DatabaseGrowthSample, error) {
	if guard == nil || ctx == nil {
		return storage.DatabaseGrowthSample{}, ErrGrowthConfiguration
	}
	guard.mu.Lock()
	defer guard.mu.Unlock()
	return sampleDatabaseGrowth(
		ctx,
		guard.database,
		guard.path,
		guard.maxBytes,
		guard.warning,
		guard.retentionDays,
		guard.desiredMaxPageCount,
		guard.mainPageLimitBytes,
		&guard.last,
		guard.loaded.State,
	)
}

// Run performs the fixed five-minute sample/checkpoint cycle and wakes promptly
// for pending transition mail. It owns no destructive maintenance.
func (guard *DatabaseGrowthGuard) Run(ctx context.Context) {
	if guard == nil || ctx == nil {
		return
	}
	ticker := time.NewTicker(guard.interval)
	defer ticker.Stop()

	guard.processPending(ctx)
	for {
		select {
		case <-ctx.Done():
			return
		case <-guard.wake:
			guard.processPending(ctx)
		case <-ticker.C:
			guard.tick(ctx)
		}
	}
}

func (guard *DatabaseGrowthGuard) tick(ctx context.Context) {
	guard.mu.Lock()
	if ctx.Err() != nil {
		guard.mu.Unlock()
		return
	}
	// PASSIVE never waits for readers and bounds normal WAL accumulation without
	// truncating or vacuuming the database. A checkpoint failure is observable,
	// but does not substitute for the authoritative growth sample below.
	if err := PassiveGrowthCheckpoint(ctx, guard.database); err != nil {
		slog.Warn("database growth passive checkpoint failed", "error", err)
	}
	sample, transitioned, err := guard.sampleLocked(ctx, true)
	guard.mu.Unlock()
	if err != nil {
		slog.Error("database growth sample failed", "error", err)
		return
	}
	slog.Info(
		"database growth sample",
		"state", sample.State,
		"pressure_percent", sample.PressurePercent,
		"used_logical_bytes", sample.UsedLogicalBytes,
		"reusable_pages", sample.ReusablePages,
		"physical_family_bytes", sample.PhysicalFamilyBytes,
		"max_bytes", sample.MaxBytes,
	)
	if transitioned {
		guard.signalNotification()
	}
	guard.processPending(ctx)
}

func (guard *DatabaseGrowthGuard) sampleLocked(
	ctx context.Context,
	persistPeriodic bool,
) (storage.DatabaseGrowthSample, bool, error) {
	nowUnix := guard.now().UTC().Unix()
	if nowUnix < 0 {
		guard.sampleHealthy = false
		return storage.DatabaseGrowthSample{}, false, ErrGrowthSample
	}
	var previous *storage.DatabaseGrowthSample
	if guard.last.ObservedUnix != 0 || guard.loaded.SampledUnix != 0 {
		previous = &guard.last
		if guard.last.ObservedUnix == 0 {
			previous = &storage.DatabaseGrowthSample{
				ObservedUnix:        nowUnix,
				PhysicalFamilyBytes: guard.loaded.PhysicalBytes,
			}
		}
	}
	sample, err := sampleDatabaseGrowth(
		ctx,
		guard.database,
		guard.path,
		guard.maxBytes,
		guard.warning,
		guard.retentionDays,
		guard.desiredMaxPageCount,
		guard.mainPageLimitBytes,
		previous,
		guard.loaded.State,
	)
	if err != nil {
		guard.sampleHealthy = false
		return storage.DatabaseGrowthSample{}, false, err
	}
	sample.ObservedUnix = nowUnix
	transitioned := sample.State != guard.loaded.State
	alertStateChanged := false
	if transitioned {
		old := guard.loaded.State
		guard.loaded.State = sample.State
		guard.loaded.TransitionUnix = nowUnix
		if guard.emailEnabled {
			kind := alertKindForTransition(old, sample.State)
			guard.loaded.PendingKind = sql.NullString{String: string(kind), Valid: true}
			guard.loaded.PendingSinceUnix = sql.NullInt64{Int64: nowUnix, Valid: true}
			guard.loaded.RetryAfterUnix = sql.NullInt64{Int64: nowUnix, Valid: true}
			guard.loaded.RetryAttempt = 0
		} else {
			clearPending(&guard.loaded)
		}
		alertStateChanged = true
		slog.Warn(
			"database growth state changed",
			"from", old,
			"to", sample.State,
			"pressure_percent", sample.PressurePercent,
			"physical_family_bytes", sample.PhysicalFamilyBytes,
			"used_logical_bytes", sample.UsedLogicalBytes,
			"max_bytes", sample.MaxBytes,
		)
	}

	// Enabling email while already in a non-normal state schedules one alert if
	// that state has never been successfully mailed.
	if guard.emailEnabled && sample.State != storage.DatabaseGrowthNormal &&
		!guard.loaded.PendingKind.Valid &&
		(!guard.loaded.LastEmailKind.Valid ||
			guard.loaded.LastEmailKind.String != string(alertKindForState(sample.State))) {
		kind := alertKindForState(sample.State)
		guard.loaded.PendingKind = sql.NullString{String: string(kind), Valid: true}
		guard.loaded.PendingSinceUnix = sql.NullInt64{Int64: nowUnix, Valid: true}
		guard.loaded.RetryAfterUnix = sql.NullInt64{Int64: nowUnix, Valid: true}
		guard.loaded.RetryAttempt = 0
		alertStateChanged = true
	}

	// At most one reminder per active non-normal state in the configured
	// interval. A pending failed notification takes precedence.
	if guard.emailEnabled && sample.State != storage.DatabaseGrowthNormal &&
		!guard.loaded.PendingKind.Valid && guard.loaded.LastEmailUnix.Valid &&
		nowUnix-guard.loaded.LastEmailUnix.Int64 >= int64(guard.reminder/time.Second) {
		kind := alertKindForState(sample.State)
		guard.loaded.PendingKind = sql.NullString{String: string(kind), Valid: true}
		guard.loaded.PendingSinceUnix = sql.NullInt64{Int64: nowUnix, Valid: true}
		guard.loaded.RetryAfterUnix = sql.NullInt64{Int64: nowUnix, Valid: true}
		guard.loaded.RetryAttempt = 0
		alertStateChanged = true
	}

	guard.loaded.SampledUnix = nowUnix
	guard.loaded.PhysicalBytes = sample.PhysicalFamilyBytes
	guard.last = sample

	if transitioned || persistPeriodic || alertStateChanged {
		if err := writeGrowthPersistentState(ctx, guard.database, guard.loaded); err != nil {
			guard.sampleHealthy = false
			return storage.DatabaseGrowthSample{}, false, err
		}
	}
	guard.sampleHealthy = true
	return sample, transitioned, nil
}

func (guard *DatabaseGrowthGuard) signalNotification() {
	if guard == nil {
		return
	}
	select {
	case guard.wake <- struct{}{}:
	default:
	}
}

func (guard *DatabaseGrowthGuard) processPending(ctx context.Context) {
	if guard == nil || !guard.emailEnabled || guard.mailer == nil || ctx.Err() != nil {
		return
	}

	guard.mu.Lock()
	nowUnix := guard.now().UTC().Unix()
	if nowUnix < 0 || !guard.loaded.PendingKind.Valid ||
		!guard.loaded.RetryAfterUnix.Valid ||
		nowUnix < guard.loaded.RetryAfterUnix.Int64 {
		guard.mu.Unlock()
		return
	}
	kind := storage.DatabaseGrowthAlertKind(guard.loaded.PendingKind.String)
	pendingSince := guard.loaded.PendingSinceUnix.Int64
	sample := guard.last
	guard.mu.Unlock()

	subject, body := RenderGrowthAlert(kind, sample)
	mailCtx, cancel := context.WithCancel(ctx)
	err := guard.mailer.Send(mailCtx, subject, body)
	cancel()

	guard.mu.Lock()
	defer guard.mu.Unlock()
	// A newer transition supersedes this delivery attempt.
	if !guard.loaded.PendingKind.Valid ||
		guard.loaded.PendingKind.String != string(kind) ||
		!guard.loaded.PendingSinceUnix.Valid ||
		guard.loaded.PendingSinceUnix.Int64 != pendingSince {
		return
	}
	nowUnix = guard.now().UTC().Unix()
	if nowUnix < 0 {
		return
	}
	if err == nil {
		guard.loaded.LastEmailKind = sql.NullString{String: string(kind), Valid: true}
		guard.loaded.LastEmailUnix = sql.NullInt64{Int64: nowUnix, Valid: true}
		clearPending(&guard.loaded)
		if persistErr := writeGrowthPersistentState(ctx, guard.database, guard.loaded); persistErr != nil {
			guard.sampleHealthy = false
			slog.Error("database growth notification state persistence failed", "error", persistErr)
		}
		slog.Info("database growth administrator notification sent", "kind", kind)
		return
	}

	previousAttempt := guard.loaded.RetryAttempt
	attempt := previousAttempt + 1
	if attempt > 3 {
		attempt = 3
	}
	guard.loaded.RetryAttempt = attempt
	guard.loaded.RetryAfterUnix = sql.NullInt64{
		Int64: nowUnix + int64(retryDelayAfterFailure(previousAttempt)/time.Second),
		Valid: true,
	}
	if persistErr := writeGrowthPersistentState(ctx, guard.database, guard.loaded); persistErr != nil {
		guard.sampleHealthy = false
		slog.Error("database growth notification state persistence failed", "error", persistErr)
	}
	slog.Error(
		"database growth administrator notification failed",
		"kind", kind,
		"retry_after_unix", guard.loaded.RetryAfterUnix.Int64,
	)
}

func retryDelayAfterFailure(previousAttempt int) time.Duration {
	switch previousAttempt {
	case 0:
		return 5 * time.Minute
	case 1:
		return 15 * time.Minute
	case 2:
		return 60 * time.Minute
	default:
		return 24 * time.Hour
	}
}

func clearPending(state *growthPersistentState) {
	state.PendingKind = sql.NullString{}
	state.PendingSinceUnix = sql.NullInt64{}
	state.RetryAfterUnix = sql.NullInt64{}
	state.RetryAttempt = 0
}

func alertKindForState(state storage.DatabaseGrowthState) storage.DatabaseGrowthAlertKind {
	switch state {
	case storage.DatabaseGrowthWarning:
		return storage.DatabaseGrowthAlertWarning
	case storage.DatabaseGrowthCritical:
		return storage.DatabaseGrowthAlertCritical
	case storage.DatabaseGrowthHard:
		return storage.DatabaseGrowthAlertHard
	default:
		return storage.DatabaseGrowthAlertRecovered
	}
}

func alertKindForTransition(
	oldState, newState storage.DatabaseGrowthState,
) storage.DatabaseGrowthAlertKind {
	if newState == storage.DatabaseGrowthNormal && oldState != storage.DatabaseGrowthNormal {
		return storage.DatabaseGrowthAlertRecovered
	}
	return alertKindForState(newState)
}

func sampleDatabaseGrowth(
	ctx context.Context,
	database *sql.DB,
	path string,
	maxBytes int64,
	warningPercent int,
	retentionDays int,
	desiredMaxPageCount int64,
	mainPageLimitBytes int64,
	previous *storage.DatabaseGrowthSample,
	previousState storage.DatabaseGrowthState,
) (storage.DatabaseGrowthSample, error) {
	if ctx == nil || database == nil || path == "" || maxBytes <= 0 ||
		warningPercent <= 0 || warningPercent >= storage.DatabaseCriticalPercent ||
		desiredMaxPageCount <= 0 || mainPageLimitBytes <= 0 {
		return storage.DatabaseGrowthSample{}, ErrGrowthConfiguration
	}
	pageSize, err := pragmaInt64(ctx, database, "page_size")
	if err != nil || pageSize <= 0 {
		return storage.DatabaseGrowthSample{}, fmt.Errorf("%w: page_size", ErrGrowthSample)
	}
	pageCount, err := pragmaInt64(ctx, database, "page_count")
	if err != nil || pageCount < 0 {
		return storage.DatabaseGrowthSample{}, fmt.Errorf("%w: page_count", ErrGrowthSample)
	}
	freelistCount, err := pragmaInt64(ctx, database, "freelist_count")
	if err != nil || freelistCount < 0 || freelistCount > pageCount {
		return storage.DatabaseGrowthSample{}, fmt.Errorf("%w: freelist_count", ErrGrowthSample)
	}
	effectiveMaxPages, err := pragmaInt64(ctx, database, "max_page_count")
	if err != nil || effectiveMaxPages <= 0 {
		return storage.DatabaseGrowthSample{}, fmt.Errorf("%w: max_page_count", ErrGrowthSample)
	}

	mainBytes, err := secureSidecarSize(path, true)
	if err != nil {
		return storage.DatabaseGrowthSample{}, err
	}
	walBytes, err := secureSidecarSize(path+"-wal", false)
	if err != nil {
		return storage.DatabaseGrowthSample{}, err
	}
	shmBytes, err := secureSidecarSize(path+"-shm", false)
	if err != nil {
		return storage.DatabaseGrowthSample{}, err
	}

	usedPages := pageCount - freelistCount
	usedBytes, ok := checkedMultiply(usedPages, pageSize)
	if !ok {
		return storage.DatabaseGrowthSample{}, ErrGrowthSample
	}
	allocatedBytes, ok := checkedMultiply(pageCount, pageSize)
	if !ok {
		return storage.DatabaseGrowthSample{}, ErrGrowthSample
	}
	family, ok := checkedAdd(mainBytes, walBytes, shmBytes)
	if !ok {
		return storage.DatabaseGrowthSample{}, ErrGrowthSample
	}

	logicalPct := boundedPercent(usedBytes, mainPageLimitBytes)
	physicalPct := boundedPercent(family, maxBytes)
	pressure := logicalPct
	if physicalPct > pressure {
		pressure = physicalPct
	}

	baseState := classifyGrowthState(
		usedBytes,
		mainPageLimitBytes,
		family,
		maxBytes,
		warningPercent,
	)
	state := applyGrowthHysteresis(baseState, pressure, previousState, warningPercent)

	sample := storage.DatabaseGrowthSample{
		State:                 state,
		PressurePercent:       pressure,
		PageSize:              pageSize,
		PageCount:             pageCount,
		UsedPages:             usedPages,
		ReusablePages:         freelistCount,
		MaxPageCount:          effectiveMaxPages,
		UsedLogicalBytes:      usedBytes,
		AllocatedLogicalBytes: allocatedBytes,
		MainPageLimitBytes:    mainPageLimitBytes,
		PhysicalMainBytes:     mainBytes,
		WALBytes:              walBytes,
		SHMBytes:              shmBytes,
		PhysicalFamilyBytes:   family,
		MaxBytes:              maxBytes,
		WarningPercent:        warningPercent,
		RetentionDays:         retentionDays,
		WriteAllowed:          state != storage.DatabaseGrowthHard,
	}
	if previous != nil {
		sample.GrowthKnown = true
		sample.GrowthBytes = family - previous.PhysicalFamilyBytes
	}
	return sample, nil
}

func classifyGrowthState(
	usedLogical, mainLimit, physicalFamily, maxBytes int64,
	warningPercent int,
) storage.DatabaseGrowthState {
	if atLeastPercent(usedLogical, mainLimit, storage.DatabaseHardPercent) ||
		atLeastPercent(physicalFamily, maxBytes, storage.DatabaseHardPercent) {
		return storage.DatabaseGrowthHard
	}
	if atLeastPercent(usedLogical, mainLimit, storage.DatabaseCriticalPercent) ||
		atLeastPercent(physicalFamily, maxBytes, storage.DatabaseCriticalPercent) {
		return storage.DatabaseGrowthCritical
	}
	if atLeastPercent(usedLogical, mainLimit, warningPercent) ||
		atLeastPercent(physicalFamily, maxBytes, warningPercent) {
		return storage.DatabaseGrowthWarning
	}
	return storage.DatabaseGrowthNormal
}

func applyGrowthHysteresis(
	base storage.DatabaseGrowthState,
	pressure int,
	previous storage.DatabaseGrowthState,
	warning int,
) storage.DatabaseGrowthState {
	if !previous.Valid() {
		return base
	}
	// Upward transitions are immediate.
	if growthRank(base) >= growthRank(previous) {
		return base
	}
	hysteresis := storage.DatabaseGrowthRecoveryHysteresisPercent
	switch previous {
	case storage.DatabaseGrowthHard:
		if pressure >= storage.DatabaseHardPercent-hysteresis {
			return storage.DatabaseGrowthHard
		}
	case storage.DatabaseGrowthCritical:
		if pressure >= storage.DatabaseCriticalPercent-hysteresis {
			return storage.DatabaseGrowthCritical
		}
	case storage.DatabaseGrowthWarning:
		if pressure >= warning-hysteresis {
			return storage.DatabaseGrowthWarning
		}
	}
	return base
}

func growthRank(state storage.DatabaseGrowthState) int {
	switch state {
	case storage.DatabaseGrowthNormal:
		return 0
	case storage.DatabaseGrowthWarning:
		return 1
	case storage.DatabaseGrowthCritical:
		return 2
	case storage.DatabaseGrowthHard:
		return 3
	default:
		return -1
	}
}

func atLeastPercent(value, maximum int64, percent int) bool {
	if maximum <= 0 || value < 0 || percent <= 0 {
		return false
	}
	if value >= maximum {
		return true
	}
	threshold := (maximum*int64(percent) + 99) / 100
	return value >= threshold
}

func boundedPercent(value, maximum int64) int {
	if maximum <= 0 || value <= 0 {
		return 0
	}
	if value >= maximum*int64(maximumGrowthPercentReport)/100 {
		return maximumGrowthPercentReport
	}
	return int((value * 100) / maximum)
}

func checkedMultiply(a, b int64) (int64, bool) {
	if a < 0 || b < 0 || (a != 0 && b > (1<<63-1)/a) {
		return 0, false
	}
	return a * b, true
}

func checkedAdd(values ...int64) (int64, bool) {
	var total int64
	for _, value := range values {
		if value < 0 || total > (1<<63-1)-value {
			return 0, false
		}
		total += value
	}
	return total, true
}

func roundUp(value, unit int64) int64 {
	if value <= 0 || unit <= 0 {
		return 0
	}
	remainder := value % unit
	if remainder == 0 {
		return value
	}
	if value > (1<<63-1)-(unit-remainder) {
		return 0
	}
	return value + (unit - remainder)
}

func pragmaInt64(ctx context.Context, database *sql.DB, name string) (int64, error) {
	switch name {
	case "page_size", "page_count", "freelist_count", "max_page_count", "cache_spill":
	default:
		return 0, ErrGrowthConfiguration
	}
	var value int64
	if err := database.QueryRowContext(ctx, "PRAGMA "+name).Scan(&value); err != nil {
		return 0, err
	}
	return value, nil
}

func secureSidecarSize(path string, required bool) (int64, error) {
	info, err := os.Lstat(path)
	if err != nil {
		if !required && errors.Is(err, os.ErrNotExist) {
			return 0, nil
		}
		return 0, fmt.Errorf("%w: database family file", ErrGrowthSample)
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() || info.Size() < 0 {
		return 0, fmt.Errorf("%w: database family file", ErrGrowthSample)
	}
	return info.Size(), nil
}

func readGrowthPersistentState(
	ctx context.Context,
	database *sql.DB,
) (growthPersistentState, error) {
	if ctx == nil || database == nil {
		return growthPersistentState{}, ErrGrowthConfiguration
	}
	var state growthPersistentState
	var stateString string
	if err := database.QueryRowContext(
		ctx,
		`SELECT state,
		        sampled_at_unix,
		        physical_bytes,
		        transition_at_unix,
		        last_email_kind,
		        last_email_at_unix,
		        pending_kind,
		        pending_since_unix,
		        retry_after_unix,
		        retry_attempt
		   FROM storage_growth_state
		  WHERE singleton_id = 1`,
	).Scan(
		&stateString,
		&state.SampledUnix,
		&state.PhysicalBytes,
		&state.TransitionUnix,
		&state.LastEmailKind,
		&state.LastEmailUnix,
		&state.PendingKind,
		&state.PendingSinceUnix,
		&state.RetryAfterUnix,
		&state.RetryAttempt,
	); err != nil {
		return growthPersistentState{}, fmt.Errorf("%w: read persistent state", ErrGrowthSample)
	}
	state.State = storage.DatabaseGrowthState(stateString)
	if !state.State.Valid() || state.SampledUnix < 0 || state.PhysicalBytes < 0 ||
		state.TransitionUnix < 0 || state.RetryAttempt < 0 || state.RetryAttempt > 3 {
		return growthPersistentState{}, ErrGrowthSample
	}
	return state, nil
}

func writeGrowthPersistentState(
	ctx context.Context,
	database *sql.DB,
	state growthPersistentState,
) error {
	if ctx == nil || database == nil || !state.State.Valid() ||
		state.SampledUnix < 0 || state.PhysicalBytes < 0 ||
		state.TransitionUnix < 0 || state.RetryAttempt < 0 || state.RetryAttempt > 3 {
		return ErrGrowthConfiguration
	}
	result, err := database.ExecContext(
		ctx,
		`UPDATE storage_growth_state
		    SET state = ?,
		        sampled_at_unix = ?,
		        physical_bytes = ?,
		        transition_at_unix = ?,
		        last_email_kind = ?,
		        last_email_at_unix = ?,
		        pending_kind = ?,
		        pending_since_unix = ?,
		        retry_after_unix = ?,
		        retry_attempt = ?
		  WHERE singleton_id = 1`,
		string(state.State),
		state.SampledUnix,
		state.PhysicalBytes,
		state.TransitionUnix,
		state.LastEmailKind,
		state.LastEmailUnix,
		state.PendingKind,
		state.PendingSinceUnix,
		state.RetryAfterUnix,
		state.RetryAttempt,
	)
	if err != nil {
		return fmt.Errorf("%w: persist alert state", ErrGrowthSample)
	}
	count, err := result.RowsAffected()
	if err != nil || count != 1 {
		return fmt.Errorf("%w: persist alert state count", ErrGrowthSample)
	}
	return nil
}

func RenderGrowthAlert(
	kind storage.DatabaseGrowthAlertKind,
	sample storage.DatabaseGrowthSample,
) (string, string) {
	subject := growthMailerSubjectPrefix + " " + string(kind)
	growth := "unknown"
	if sample.GrowthKnown {
		growth = fmt.Sprintf("%d", sample.GrowthBytes)
	}
	body := fmt.Sprintf(
		`Activity-Relay-Directory storage state: %s

sampled_at_unix: %d
pressure_percent: %d
used_logical_pages: %d
allocated_logical_pages: %d
reusable_pages: %d
page_size_bytes: %d
used_logical_bytes: %d
allocated_logical_bytes: %d
main_page_limit_bytes: %d
physical_main_bytes: %d
wal_bytes: %d
shm_bytes: %d
physical_database_family_bytes: %d
growth_since_previous_sample_bytes: %s
configured_max_bytes: %d
inactive_retention_days: %d
write_admission: %s

Remediation checklist:
1. Check host filesystem free space and filesystem monitoring.
2. Preserve a fresh verified backup before any destructive retention action.
3. Review inactive-retention policy and candidate dry-run before approaching the hard limit.
4. If logical pages were already freed, schedule explicit offline checkpoint/VACUUM maintenance with adequate free space.
5. Increase DIRECTORY_DATABASE_MAX_BYTES only after reviewing host capacity and the reserved WAL/migration headroom.
`,
		kind,
		sample.ObservedUnix,
		sample.PressurePercent,
		sample.UsedPages,
		sample.PageCount,
		sample.ReusablePages,
		sample.PageSize,
		sample.UsedLogicalBytes,
		sample.AllocatedLogicalBytes,
		sample.MainPageLimitBytes,
		sample.PhysicalMainBytes,
		sample.WALBytes,
		sample.SHMBytes,
		sample.PhysicalFamilyBytes,
		growth,
		sample.MaxBytes,
		sample.RetentionDays,
		map[bool]string{true: "allowed", false: "blocked"}[sample.WriteAllowed],
	)
	if len(body) > maximumGrowthAlertBody {
		body = body[:maximumGrowthAlertBody]
	}
	return subject, body
}

// InspectDatabaseGrowth opens no new write transaction and is used by local
// storage status/check commands.
func InspectDatabaseGrowth(
	ctx context.Context,
	database *sql.DB,
	path string,
	maxBytes int64,
	warningPercent int,
	retentionDays int,
) (storage.DatabaseGrowthSample, error) {
	if ctx == nil || database == nil {
		return storage.DatabaseGrowthSample{}, ErrGrowthConfiguration
	}
	desired, effective, mainLimit, err := databaseGrowthPageLimits(
		ctx, database, maxBytes,
	)
	if err != nil {
		return storage.DatabaseGrowthSample{}, err
	}
	persisted, err := readGrowthPersistentState(ctx, database)
	if err != nil {
		return storage.DatabaseGrowthSample{}, err
	}
	var previous *storage.DatabaseGrowthSample
	if persisted.SampledUnix != 0 {
		previous = &storage.DatabaseGrowthSample{
			ObservedUnix:        persisted.SampledUnix,
			PhysicalFamilyBytes: persisted.PhysicalBytes,
		}
	}
	sample, err := sampleDatabaseGrowth(
		ctx, database, path, maxBytes, warningPercent, retentionDays,
		desired, mainLimit, previous, persisted.State,
	)
	if err != nil {
		return storage.DatabaseGrowthSample{}, err
	}
	// max_page_count is connection-local in SQLite. A read-only inspection
	// connection therefore reports the policy-effective ceiling derived from the
	// current allocation rather than its own unrelated connection-local default.
	sample.MaxPageCount = effective
	return sample, nil
}

// PassiveGrowthCheckpoint performs only SQLite's bounded PASSIVE WAL checkpoint.
// It never VACUUMs or changes retention.
func PassiveGrowthCheckpoint(ctx context.Context, database *sql.DB) error {
	if ctx == nil || database == nil {
		return ErrGrowthConfiguration
	}
	if _, err := database.ExecContext(ctx, `PRAGMA wal_checkpoint(PASSIVE)`); err != nil {
		return fmt.Errorf("%w: passive checkpoint", ErrGrowthSample)
	}
	return nil
}

// RedactedGrowthError returns a stable operator-facing class without database
// paths or SQLite details.
func RedactedGrowthError(err error) string {
	switch {
	case errors.Is(err, storage.ErrWriteAdmissionHard):
		return "database growth hard limit reached"
	case errors.Is(err, ErrGrowthConfiguration):
		return "database growth configuration is invalid"
	default:
		return "database growth check failed"
	}
}
