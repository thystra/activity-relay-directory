package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/thystra/activity-relay-directory/internal/storage"
)

func TestConfigureDatabaseGrowthLimitReservesPageAlignedHeadroom(t *testing.T) {
	path, database := openUnguardedGrowthDatabase(t)
	ctx := context.Background()
	pageSize := growthPragma(t, database, "page_size")
	maxBytes := pageSize * 4096

	desired, mainLimit, err := ConfigureDatabaseGrowthLimit(ctx, database, maxBytes)
	if err != nil {
		t.Fatalf("ConfigureDatabaseGrowthLimit() error = %v", err)
	}
	if desired <= 0 || mainLimit != desired*pageSize || mainLimit >= maxBytes {
		t.Fatalf("growth limit = pages:%d bytes:%d max:%d", desired, mainLimit, maxBytes)
	}
	headroom := maxBytes - mainLimit
	if headroom <= 0 || headroom%pageSize != 0 || headroom > storage.MaximumDatabaseGrowthHeadroomBytes+pageSize {
		t.Fatalf("headroom = %d for page size %d", headroom, pageSize)
	}
	if got := growthPragma(t, database, "max_page_count"); got != desired {
		t.Fatalf("PRAGMA max_page_count = %d, want %d", got, desired)
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("database path disappeared: %v", err)
	}
}

func TestConfigureDatabaseGrowthLimitTreatsExistingAllocationAsNoGrowthBackstop(t *testing.T) {
	_, database := openUnguardedGrowthDatabase(t)
	ctx := context.Background()
	pageSize := growthPragma(t, database, "page_size")
	pageCount := growthPragma(t, database, "page_count")
	if pageCount <= 8 {
		t.Fatalf("fresh migrated page count = %d, need > 8 for cap test", pageCount)
	}

	// This budget intentionally computes a desired main-page cap below the
	// already-allocated database. SQLite must refuse to lower max_page_count
	// below page_count and report exactly that current allocation as the
	// effective no-growth ceiling.
	maxBytes := (pageCount - 1) * pageSize
	desired, mainLimit, err := ConfigureDatabaseGrowthLimit(ctx, database, maxBytes)
	if err != nil {
		t.Fatalf("ConfigureDatabaseGrowthLimit(existing allocation) error = %v", err)
	}
	if desired >= pageCount || mainLimit != desired*pageSize {
		t.Fatalf("desired cap = pages:%d bytes:%d, page_count=%d", desired, mainLimit, pageCount)
	}
	if effective := growthPragma(t, database, "max_page_count"); effective != pageCount {
		t.Fatalf("effective max_page_count = %d, want current page_count %d", effective, pageCount)
	}
}

func TestGrowthStateExactThresholdsAndHysteresis(t *testing.T) {
	const maximum = int64(1000)
	for _, test := range []struct {
		name     string
		logical  int64
		physical int64
		want     storage.DatabaseGrowthState
	}{
		{"below warning", 749, 0, storage.DatabaseGrowthNormal},
		{"warning logical", 750, 0, storage.DatabaseGrowthWarning},
		{"warning physical", 0, 750, storage.DatabaseGrowthWarning},
		{"critical logical", 900, 0, storage.DatabaseGrowthCritical},
		{"critical physical", 0, 900, storage.DatabaseGrowthCritical},
		{"hard logical", 1000, 0, storage.DatabaseGrowthHard},
		{"hard physical", 0, 1000, storage.DatabaseGrowthHard},
	} {
		t.Run(test.name, func(t *testing.T) {
			got := classifyGrowthState(test.logical, maximum, test.physical, maximum, 75)
			if got != test.want {
				t.Fatalf("classifyGrowthState() = %q, want %q", got, test.want)
			}
		})
	}

	if got := applyGrowthHysteresis(storage.DatabaseGrowthNormal, 70, storage.DatabaseGrowthWarning, 75); got != storage.DatabaseGrowthWarning {
		t.Fatalf("warning hysteresis at 70 = %q", got)
	}
	if got := applyGrowthHysteresis(storage.DatabaseGrowthNormal, 69, storage.DatabaseGrowthWarning, 75); got != storage.DatabaseGrowthNormal {
		t.Fatalf("warning recovery below hysteresis = %q", got)
	}
	if got := applyGrowthHysteresis(storage.DatabaseGrowthWarning, 85, storage.DatabaseGrowthCritical, 75); got != storage.DatabaseGrowthCritical {
		t.Fatalf("critical hysteresis at 85 = %q", got)
	}
	if got := applyGrowthHysteresis(storage.DatabaseGrowthCritical, 95, storage.DatabaseGrowthHard, 75); got != storage.DatabaseGrowthHard {
		t.Fatalf("hard hysteresis at 95 = %q", got)
	}
	if got := applyGrowthHysteresis(storage.DatabaseGrowthCritical, 94, storage.DatabaseGrowthHard, 75); got != storage.DatabaseGrowthCritical {
		t.Fatalf("hard recovery below hysteresis = %q", got)
	}
}

func TestGrowthRetryBackoffBecomesDailyAfterThreeFailures(t *testing.T) {
	want := []time.Duration{5 * time.Minute, 15 * time.Minute, time.Hour, 24 * time.Hour, 24 * time.Hour}
	for previous, expected := range want {
		if got := retryDelayAfterFailure(previous); got != expected {
			t.Fatalf("retryDelayAfterFailure(%d) = %s, want %s", previous, got, expected)
		}
	}
}

func TestGrowthGuardReadinessFailsClosedAfterSampleFailureAndRecovers(t *testing.T) {
	path, database := openGrowthDatabase(t)
	ctx := context.Background()
	guard, err := NewDatabaseGrowthGuard(ctx, database, DatabaseGrowthOptions{
		Path: path, MaxBytes: storage.DefaultDatabaseMaxBytes, WarningPercent: 75, Now: time.Now,
	})
	if err != nil {
		t.Fatalf("NewDatabaseGrowthGuard() error = %v", err)
	}
	if err := guard.Ready(); err != nil {
		t.Fatalf("Ready(initial) error = %v", err)
	}

	guard.mu.Lock()
	originalPath := guard.path
	guard.path = originalPath + ".missing"
	_, _, sampleErr := guard.sampleLocked(ctx, false)
	guard.path = originalPath
	guard.mu.Unlock()
	if !errors.Is(sampleErr, ErrGrowthSample) {
		t.Fatalf("sampleLocked(missing file) error = %v, want ErrGrowthSample", sampleErr)
	}
	if err := guard.Ready(); !errors.Is(err, ErrGrowthSample) {
		t.Fatalf("Ready(after sample failure) = %v, want ErrGrowthSample", err)
	}

	guard.mu.Lock()
	_, _, sampleErr = guard.sampleLocked(ctx, false)
	guard.mu.Unlock()
	if sampleErr != nil {
		t.Fatalf("sampleLocked(recovered) error = %v", sampleErr)
	}
	if err := guard.Ready(); err != nil {
		t.Fatalf("Ready(recovered) error = %v", err)
	}
}

func TestGrowthSampleObservesWALAndPassiveCheckpointPreservesReadability(t *testing.T) {
	path, database := openGrowthDatabase(t)
	ctx := context.Background()
	if _, err := database.Exec(`CREATE TABLE growth_wal (id INTEGER PRIMARY KEY, body BLOB NOT NULL)`); err != nil {
		t.Fatalf("create WAL payload table: %v", err)
	}
	for index := 0; index < 48; index++ {
		if _, err := database.Exec(`INSERT INTO growth_wal(body) VALUES (zeroblob(8192))`); err != nil {
			t.Fatalf("insert WAL payload %d: %v", index, err)
		}
	}
	before, err := InspectDatabaseGrowth(ctx, database, path, storage.DefaultDatabaseMaxBytes, 75, 0)
	if err != nil {
		t.Fatalf("InspectDatabaseGrowth(before checkpoint) error = %v", err)
	}
	if before.WALBytes <= 0 || before.PhysicalFamilyBytes < before.PhysicalMainBytes+before.WALBytes {
		t.Fatalf("WAL not represented in sample: %#v", before)
	}
	if err := PassiveGrowthCheckpoint(ctx, database); err != nil {
		t.Fatalf("PassiveGrowthCheckpoint() error = %v", err)
	}
	var rows int
	if err := database.QueryRow(`SELECT COUNT(*) FROM growth_wal`).Scan(&rows); err != nil || rows != 48 {
		t.Fatalf("database after passive checkpoint = rows:%d err:%v", rows, err)
	}
}

func TestInspectDatabaseGrowthUsesPersistedHysteresisAndPreviousPhysicalSample(t *testing.T) {
	path, database := openGrowthDatabase(t)
	ctx := context.Background()
	checkpointGrowthTest(t, database)
	normal, err := InspectDatabaseGrowth(ctx, database, path, storage.DefaultDatabaseMaxBytes, 75, 0)
	if err != nil {
		t.Fatalf("initial InspectDatabaseGrowth() error = %v", err)
	}
	budget := findGrowthBudgetForState(t, database, path, storage.DatabaseGrowthWarning)
	warning, err := InspectDatabaseGrowth(ctx, database, path, budget, 75, 0)
	if err != nil || warning.State != storage.DatabaseGrowthWarning {
		t.Fatalf("warning InspectDatabaseGrowth() = (%#v, %v)", warning, err)
	}
	state := growthPersistentState{
		State: storage.DatabaseGrowthWarning, SampledUnix: 100,
		PhysicalBytes: normal.PhysicalFamilyBytes, TransitionUnix: 100,
	}
	if err := writeGrowthPersistentState(ctx, database, state); err != nil {
		t.Fatalf("writeGrowthPersistentState() error = %v", err)
	}

	inspected, err := InspectDatabaseGrowth(ctx, database, path, budget, 75, 0)
	if err != nil {
		t.Fatalf("InspectDatabaseGrowth(persisted) error = %v", err)
	}
	if inspected.State != storage.DatabaseGrowthWarning {
		t.Fatalf("InspectDatabaseGrowth state = %q, want warning", inspected.State)
	}
	if !inspected.GrowthKnown || inspected.GrowthBytes != inspected.PhysicalFamilyBytes-normal.PhysicalFamilyBytes {
		t.Fatalf("InspectDatabaseGrowth growth = known:%t bytes:%d", inspected.GrowthKnown, inspected.GrowthBytes)
	}
}

func TestInspectDatabaseGrowthCountsFreelistAsReusableWithoutClaimingShrink(t *testing.T) {
	path, database := openGrowthDatabase(t)
	ctx := context.Background()
	if _, err := database.Exec(`CREATE TABLE growth_payload (id INTEGER PRIMARY KEY, body BLOB NOT NULL)`); err != nil {
		t.Fatalf("create growth payload table: %v", err)
	}
	for index := 0; index < 96; index++ {
		if _, err := database.Exec(`INSERT INTO growth_payload(body) VALUES (zeroblob(16384))`); err != nil {
			t.Fatalf("insert payload %d: %v", index, err)
		}
	}
	checkpointGrowthTest(t, database)
	before, err := InspectDatabaseGrowth(ctx, database, path, storage.DefaultDatabaseMaxBytes, 75, 0)
	if err != nil {
		t.Fatalf("InspectDatabaseGrowth(before) error = %v", err)
	}
	if _, err := database.Exec(`DELETE FROM growth_payload`); err != nil {
		t.Fatalf("delete payloads: %v", err)
	}
	checkpointGrowthTest(t, database)
	after, err := InspectDatabaseGrowth(ctx, database, path, storage.DefaultDatabaseMaxBytes, 75, 0)
	if err != nil {
		t.Fatalf("InspectDatabaseGrowth(after) error = %v", err)
	}
	if after.ReusablePages <= before.ReusablePages {
		t.Fatalf("reusable pages did not increase: before=%d after=%d", before.ReusablePages, after.ReusablePages)
	}
	if after.UsedPages >= before.UsedPages || after.UsedLogicalBytes >= before.UsedLogicalBytes {
		t.Fatalf("logical use did not fall: before=%d/%d after=%d/%d", before.UsedPages, before.UsedLogicalBytes, after.UsedPages, after.UsedLogicalBytes)
	}
	if after.PhysicalMainBytes != before.PhysicalMainBytes {
		t.Fatalf("delete changed main-file size without VACUUM: before=%d after=%d", before.PhysicalMainBytes, after.PhysicalMainBytes)
	}

	for index := 0; index < 12; index++ {
		if _, err := database.Exec(`INSERT INTO growth_payload(body) VALUES (zeroblob(16384))`); err != nil {
			t.Fatalf("reuse payload %d: %v", index, err)
		}
	}
	checkpointGrowthTest(t, database)
	reused, err := InspectDatabaseGrowth(ctx, database, path, storage.DefaultDatabaseMaxBytes, 75, 0)
	if err != nil {
		t.Fatalf("InspectDatabaseGrowth(reused) error = %v", err)
	}
	if reused.ReusablePages >= after.ReusablePages {
		t.Fatalf("reused inserts did not consume free pages: after=%d reused=%d", after.ReusablePages, reused.ReusablePages)
	}
	if reused.PageCount != after.PageCount || reused.PhysicalMainBytes != after.PhysicalMainBytes {
		t.Fatalf("reused inserts grew main allocation: pages %d->%d bytes %d->%d", after.PageCount, reused.PageCount, after.PhysicalMainBytes, reused.PhysicalMainBytes)
	}
}

func TestSQLiteMaxPageCountBackstopFailsClosedAndKeepsDatabaseReadable(t *testing.T) {
	_, database := openUnguardedGrowthDatabase(t)
	ctx := context.Background()
	if _, err := database.Exec(`CREATE TABLE growth_cap (id INTEGER PRIMARY KEY, body BLOB NOT NULL)`); err != nil {
		t.Fatalf("create cap table: %v", err)
	}
	checkpointGrowthTest(t, database)
	if _, err := database.Exec(`VACUUM`); err != nil {
		t.Fatalf("VACUUM test database: %v", err)
	}
	checkpointGrowthTest(t, database)
	pageSize := growthPragma(t, database, "page_size")
	currentPages := growthPragma(t, database, "page_count")
	// Pick a total family budget whose page cap is only a small distance above
	// the compact current database. The exact cap is returned and asserted below.
	totalPages := ((currentPages + 12) * 8 / 7) + 8
	maxBytes := totalPages * pageSize
	desired, _, err := ConfigureDatabaseGrowthLimit(ctx, database, maxBytes)
	if err != nil {
		t.Fatalf("ConfigureDatabaseGrowthLimit() error = %v", err)
	}
	if desired <= currentPages {
		t.Fatalf("desired max pages = %d, current = %d", desired, currentPages)
	}

	var writeErr error
	for index := 0; index < 256; index++ {
		_, writeErr = database.Exec(`INSERT INTO growth_cap(body) VALUES (zeroblob(32768))`)
		if writeErr != nil {
			break
		}
	}
	if writeErr == nil {
		t.Fatal("writes never reached SQLite max_page_count backstop")
	}
	pageCount := growthPragma(t, database, "page_count")
	if pageCount > desired {
		t.Fatalf("page_count = %d, exceeds desired max_page_count %d", pageCount, desired)
	}
	var rows int
	if err := database.QueryRow(`SELECT COUNT(*) FROM growth_cap`).Scan(&rows); err != nil {
		t.Fatalf("database unreadable after full-class failure: %v", err)
	}
	if rows <= 0 {
		t.Fatalf("no committed rows survived before full-class failure")
	}
}

func TestMigrationSevenRollsBackWhenPageCapLeavesNoRoom(t *testing.T) {
	database := openTestDatabase(t)
	applyMigrationsThrough(t, database, 6)
	checkpointGrowthTest(t, database)
	if _, err := database.Exec(`VACUUM`); err != nil {
		t.Fatalf("VACUUM version 6 database: %v", err)
	}
	checkpointGrowthTest(t, database)
	if free := growthPragma(t, database, "freelist_count"); free != 0 {
		t.Fatalf("freelist_count before near-limit migration = %d, want 0", free)
	}
	pages := growthPragma(t, database, "page_count")
	var effective int64
	if err := database.QueryRow(fmt.Sprintf("PRAGMA max_page_count = %d", pages)).Scan(&effective); err != nil {
		t.Fatalf("set max_page_count: %v", err)
	}
	if effective != pages {
		t.Fatalf("effective max_page_count = %d, want %d", effective, pages)
	}

	if err := Migrate(context.Background(), database); err == nil {
		t.Fatal("near-limit migration unexpectedly succeeded")
	}
	version, err := SchemaVersion(context.Background(), database)
	if err != nil || version != 6 {
		t.Fatalf("SchemaVersion() = (%d, %v), want (6, nil)", version, err)
	}
	var tableCount int
	if err := database.QueryRow(`SELECT COUNT(*) FROM sqlite_schema WHERE type='table' AND name='storage_growth_state'`).Scan(&tableCount); err != nil {
		t.Fatalf("inspect growth table: %v", err)
	}
	if tableCount != 0 {
		t.Fatalf("migration 7 partially created storage_growth_state")
	}
}

func TestMigrationGrowthAdmissionBlocksHardBeforeSchemaMutation(t *testing.T) {
	path := filepath.Join(t.TempDir(), "directory.sqlite")
	database, err := Open(context.Background(), path)
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	t.Cleanup(func() { _ = database.Close() })
	applyMigrationsThrough(t, database, 6)
	checkpointGrowthTest(t, database)

	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat version 6 database: %v", err)
	}
	maxBytes := info.Size()
	pageSize := growthPragma(t, database, "page_size")
	if maxBytes <= pageSize*4 {
		t.Fatalf("version 6 database too small for growth preflight test: %d", maxBytes)
	}
	desired, mainLimit, err := ConfigureDatabaseGrowthLimit(context.Background(), database, maxBytes)
	if err != nil {
		t.Fatalf("ConfigureDatabaseGrowthLimit() error = %v", err)
	}
	err = CheckMigrationGrowthAdmission(
		context.Background(), database, path, maxBytes, 75, desired, mainLimit,
	)
	if !errors.Is(err, storage.ErrWriteAdmissionHard) {
		t.Fatalf("CheckMigrationGrowthAdmission() error = %v, want hard limit", err)
	}
	version, err := SchemaVersion(context.Background(), database)
	if err != nil || version != 6 {
		t.Fatalf("SchemaVersion() = (%d, %v), want (6, nil)", version, err)
	}
	var growthTable int
	if err := database.QueryRow(`SELECT COUNT(*) FROM sqlite_schema WHERE type='table' AND name='storage_growth_state'`).Scan(&growthTable); err != nil {
		t.Fatalf("inspect growth table: %v", err)
	}
	if growthTable != 0 {
		t.Fatalf("hard migration preflight created growth table count=%d", growthTable)
	}
}

func TestGrowthMigrationCreatesBoundedImmutableSingleton(t *testing.T) {
	database := openMigratedTestDatabase(t)
	var count int
	if err := database.QueryRow(`SELECT COUNT(*) FROM storage_growth_state`).Scan(&count); err != nil || count != 1 {
		t.Fatalf("growth state count = %d, %v", count, err)
	}
	if _, err := database.Exec(`DELETE FROM storage_growth_state WHERE singleton_id=1`); err == nil {
		t.Fatal("growth singleton deletion was accepted")
	}
	if _, err := database.Exec(`INSERT INTO storage_growth_state(singleton_id,state,sampled_at_unix,physical_bytes,transition_at_unix) VALUES(2,'normal',0,0,0)`); err == nil {
		t.Fatal("second growth singleton was accepted")
	}
	if _, err := database.Exec(`UPDATE storage_growth_state SET pending_kind='warning', pending_since_unix=NULL WHERE singleton_id=1`); err == nil {
		t.Fatal("inconsistent pending alert state was accepted")
	}
}

func TestHardGuardRejectsMutationBeforeTransactionAndPreservesData(t *testing.T) {
	path, database := openGrowthDatabase(t)
	ctx := context.Background()
	if _, err := database.Exec(`UPDATE directory_policy SET enrollment_open=0 WHERE singleton=1`); err != nil {
		t.Fatalf("close enrollment: %v", err)
	}
	checkpointGrowthTest(t, database)
	maxBytes := findGrowthBudgetForState(t, database, path, storage.DatabaseGrowthHard)
	database = reopenGrowthDatabaseWithBudget(t, database, path, maxBytes)
	clock := time.Unix(1000, 0)
	guard, err := NewDatabaseGrowthGuard(ctx, database, DatabaseGrowthOptions{
		Path:           path,
		MaxBytes:       maxBytes,
		WarningPercent: 75,
		RetentionDays:  0,
		Now:            func() time.Time { return clock },
	})
	if err != nil {
		t.Fatalf("NewDatabaseGrowthGuard() error = %v", err)
	}
	if err := guard.Ready(); !errors.Is(err, storage.ErrWriteAdmissionHard) {
		t.Fatalf("Ready() error = %v, want hard limit", err)
	}
	repository, err := NewRelayRepository(database, guard)
	if err != nil {
		t.Fatalf("NewRelayRepository() error = %v", err)
	}
	_, err = repository.SetEnrollment(ctx, true, storage.EnrollmentIntent{OperatorID: "growth-test"}, clock)
	if !errors.Is(err, storage.ErrWriteAdmissionHard) {
		t.Fatalf("SetEnrollment() error = %v, want hard limit", err)
	}
	open, err := repository.EnrollmentOpen(ctx)
	if err != nil || open {
		t.Fatalf("EnrollmentOpen() = (%t, %v), want closed", open, err)
	}
	var eventCount int
	if err := database.QueryRow(`SELECT COUNT(*) FROM enrollment_events`).Scan(&eventCount); err != nil {
		t.Fatalf("count enrollment events: %v", err)
	}
	if eventCount != 0 {
		t.Fatalf("hard-rejected mutation recorded %d enrollment events", eventCount)
	}
}

func TestHardRejectedPreWriteSamplesDoNotChurnPendingAlertState(t *testing.T) {
	path, database := openGrowthDatabase(t)
	checkpointGrowthTest(t, database)
	ctx := context.Background()
	clock := time.Unix(9_000, 0)
	mailer := &growthMailerRecorder{}
	hardBudget := findGrowthBudgetForState(t, database, path, storage.DatabaseGrowthHard)
	database = reopenGrowthDatabaseWithBudget(t, database, path, hardBudget)
	guard, err := NewDatabaseGrowthGuard(ctx, database, DatabaseGrowthOptions{
		Path: path, MaxBytes: hardBudget, WarningPercent: 75,
		EmailEnabled: true, Mailer: mailer, Now: func() time.Time { return clock },
	})
	if err != nil {
		t.Fatalf("NewDatabaseGrowthGuard(hard) error = %v", err)
	}
	before, err := readGrowthPersistentState(ctx, database)
	if err != nil {
		t.Fatalf("read persistent state before rejected writes: %v", err)
	}
	if before.State != storage.DatabaseGrowthHard || !before.PendingKind.Valid {
		t.Fatalf("initial hard pending state = %#v", before)
	}

	for index := 0; index < 3; index++ {
		clock = clock.Add(time.Second)
		lease, err := guard.AcquireWrite(ctx)
		if lease != nil {
			lease.Release()
			t.Fatalf("AcquireWrite(hard) returned lease on attempt %d", index)
		}
		if !errors.Is(err, storage.ErrWriteAdmissionHard) {
			t.Fatalf("AcquireWrite(hard) error = %v", err)
		}
	}
	after, err := readGrowthPersistentState(ctx, database)
	if err != nil {
		t.Fatalf("read persistent state after rejected writes: %v", err)
	}
	if after != before {
		t.Fatalf("hard rejected pre-write samples churned control state:\nbefore=%#v\nafter=%#v", before, after)
	}
}

func TestGrowthAlertStateSurvivesRestartSuppressesDuplicateAndRecoversWithHysteresis(t *testing.T) {
	path, database := openGrowthDatabase(t)
	checkpointGrowthTest(t, database)
	ctx := context.Background()
	clock := time.Unix(10_000, 0)
	mailer := &growthMailerRecorder{}

	warningBudget := findGrowthBudgetForState(t, database, path, storage.DatabaseGrowthWarning)
	database = reopenGrowthDatabaseWithBudget(t, database, path, warningBudget)
	guard, err := NewDatabaseGrowthGuard(ctx, database, DatabaseGrowthOptions{
		Path:             path,
		MaxBytes:         warningBudget,
		WarningPercent:   75,
		RetentionDays:    0,
		EmailEnabled:     true,
		Mailer:           mailer,
		Now:              func() time.Time { return clock },
		ReminderInterval: 24 * time.Hour,
	})
	if err != nil {
		t.Fatalf("NewDatabaseGrowthGuard(warning) error = %v", err)
	}
	guard.processPending(ctx)
	if got := mailer.count(); got != 1 || mailer.lastKind() != "warning" {
		t.Fatalf("initial warning mail = count:%d kind:%q", got, mailer.lastKind())
	}

	// A restart in the same state must not resend the transition.
	restarted, err := NewDatabaseGrowthGuard(ctx, database, DatabaseGrowthOptions{
		Path:             path,
		MaxBytes:         warningBudget,
		WarningPercent:   75,
		RetentionDays:    0,
		EmailEnabled:     true,
		Mailer:           mailer,
		Now:              func() time.Time { return clock },
		ReminderInterval: 24 * time.Hour,
	})
	if err != nil {
		t.Fatalf("NewDatabaseGrowthGuard(restart) error = %v", err)
	}
	restarted.processPending(ctx)
	if got := mailer.count(); got != 1 {
		t.Fatalf("restart duplicated warning mail: count=%d", got)
	}

	clock = clock.Add(24*time.Hour + time.Second)
	restarted.mu.Lock()
	_, _, err = restarted.sampleLocked(ctx, true)
	restarted.mu.Unlock()
	if err != nil {
		t.Fatalf("reminder sample error = %v", err)
	}
	restarted.processPending(ctx)
	if got := mailer.count(); got != 2 || mailer.lastKind() != "warning" {
		t.Fatalf("24h reminder = count:%d kind:%q", got, mailer.lastKind())
	}

	// Increase the budget enough to move below warning minus the 5-point
	// recovery hysteresis. The persisted prior state should produce one recovered
	// transition on restart, not another warning.
	normalBudget := warningBudget * 2
	clock = clock.Add(time.Minute)
	database = reopenGrowthDatabaseWithBudget(t, database, path, normalBudget)
	recovered, err := NewDatabaseGrowthGuard(ctx, database, DatabaseGrowthOptions{
		Path:             path,
		MaxBytes:         normalBudget,
		WarningPercent:   75,
		RetentionDays:    0,
		EmailEnabled:     true,
		Mailer:           mailer,
		Now:              func() time.Time { return clock },
		ReminderInterval: 24 * time.Hour,
	})
	if err != nil {
		t.Fatalf("NewDatabaseGrowthGuard(recovered) error = %v", err)
	}
	recovered.processPending(ctx)
	if got := mailer.count(); got != 3 || mailer.lastKind() != "recovered" {
		t.Fatalf("recovery mail = count:%d kind:%q", got, mailer.lastKind())
	}
}

func TestGrowthMailerFailureRetryStateIsBoundedAndRestartSafe(t *testing.T) {
	path, database := openGrowthDatabase(t)
	checkpointGrowthTest(t, database)
	ctx := context.Background()
	clock := time.Unix(20_000, 0)
	mailer := &growthMailerRecorder{err: errors.New("mail failed")}
	warningBudget := findGrowthBudgetForState(t, database, path, storage.DatabaseGrowthWarning)
	database = reopenGrowthDatabaseWithBudget(t, database, path, warningBudget)
	guard, err := NewDatabaseGrowthGuard(ctx, database, DatabaseGrowthOptions{
		Path: path, MaxBytes: warningBudget, WarningPercent: 75,
		EmailEnabled: true, Mailer: mailer, Now: func() time.Time { return clock },
	})
	if err != nil {
		t.Fatalf("NewDatabaseGrowthGuard() error = %v", err)
	}
	for attempt, delay := range []time.Duration{5 * time.Minute, 15 * time.Minute, time.Hour, 24 * time.Hour} {
		guard.processPending(ctx)
		state, err := readGrowthPersistentState(ctx, database)
		if err != nil {
			t.Fatalf("read state after attempt %d: %v", attempt+1, err)
		}
		wantCounter := attempt + 1
		if wantCounter > 3 {
			wantCounter = 3
		}
		if state.RetryAttempt != wantCounter || !state.PendingKind.Valid ||
			state.RetryAfterUnix.Int64 != clock.Unix()+int64(delay/time.Second) {
			t.Fatalf("retry state after attempt %d = %#v, delay=%s", attempt+1, state, delay)
		}
		clock = clock.Add(delay)
	}
	var rowCount int
	if err := database.QueryRow(`SELECT COUNT(*) FROM storage_growth_state`).Scan(&rowCount); err != nil || rowCount != 1 {
		t.Fatalf("bounded growth state rows = %d, %v", rowCount, err)
	}
}

func TestGrowthGuardSerializesConcurrentRepositoryWriters(t *testing.T) {
	path, database := openGrowthDatabase(t)
	if _, err := database.Exec(`UPDATE directory_policy SET enrollment_open=1 WHERE singleton=1`); err != nil {
		t.Fatalf("open enrollment: %v", err)
	}
	guard, err := NewDatabaseGrowthGuard(context.Background(), database, DatabaseGrowthOptions{
		Path: path, MaxBytes: storage.DefaultDatabaseMaxBytes, WarningPercent: 75,
		Now: time.Now,
	})
	if err != nil {
		t.Fatalf("NewDatabaseGrowthGuard() error = %v", err)
	}
	repository, err := NewRelayRepository(database, guard)
	if err != nil {
		t.Fatalf("NewRelayRepository() error = %v", err)
	}
	const writers = 12
	var wg sync.WaitGroup
	errorsCh := make(chan error, writers)
	for index := 0; index < writers; index++ {
		wg.Add(1)
		go func(index int) {
			defer wg.Done()
			actor := fmt.Sprintf("https://growth-%02d.example/actor", index)
			_, err := repository.Register(context.Background(), storage.RegisterIntent{
				RelayActor: actor, PublicBaseURL: fmt.Sprintf("https://growth-%02d.example", index),
			}, time.Unix(int64(1000+index), 0))
			errorsCh <- err
		}(index)
	}
	wg.Wait()
	close(errorsCh)
	for err := range errorsCh {
		if err != nil {
			t.Fatalf("concurrent Register() error = %v", err)
		}
	}
	var count int
	if err := database.QueryRow(`SELECT COUNT(*) FROM relays WHERE relay_actor LIKE 'https://growth-%'`).Scan(&count); err != nil || count != writers {
		t.Fatalf("concurrent relay count = %d, %v", count, err)
	}
}

func TestRenderGrowthAlertContainsMetricsNoIdentityVocabulary(t *testing.T) {
	sample := storage.DatabaseGrowthSample{
		ObservedUnix: 1, State: storage.DatabaseGrowthHard, PressurePercent: 100,
		PageSize: 4096, PageCount: 100, UsedPages: 90, ReusablePages: 10,
		UsedLogicalBytes: 90 * 4096, AllocatedLogicalBytes: 100 * 4096,
		MainPageLimitBytes: 90 * 4096, PhysicalMainBytes: 100 * 4096,
		WALBytes: 8192, SHMBytes: 32768, PhysicalFamilyBytes: 450560,
		GrowthKnown: true, GrowthBytes: 4096, MaxBytes: 450560,
		WarningPercent: 75, RetentionDays: 365, WriteAllowed: false,
	}
	subject, body := RenderGrowthAlert(storage.DatabaseGrowthAlertHard, sample)
	if subject != "Activity-Relay-Directory storage hard-limit" {
		t.Fatalf("subject = %q", subject)
	}
	for _, required := range []string{
		"used_logical_pages: 90", "reusable_pages: 10", "wal_bytes: 8192",
		"growth_since_previous_sample_bytes: 4096", "inactive_retention_days: 365",
		"write_admission: blocked", "Remediation checklist:",
	} {
		if !containsString(body, required) {
			t.Fatalf("alert body missing %q:\n%s", required, body)
		}
	}
	for _, forbidden := range []string{"relay_actor", "moderator", "operator_id", "audit_event", "database_path"} {
		if containsString(body, forbidden) {
			t.Fatalf("alert body contains forbidden %q", forbidden)
		}
	}
}

func TestInspectDatabaseGrowthReportsPolicyEffectiveCapOnReadOnlyConnection(t *testing.T) {
	path, database := openGrowthDatabase(t)
	ctx := context.Background()
	var pageSize, pageCount int64
	if err := database.QueryRow(`PRAGMA page_size`).Scan(&pageSize); err != nil {
		t.Fatalf("page_size: %v", err)
	}
	if err := database.QueryRow(`PRAGMA page_count`).Scan(&pageCount); err != nil {
		t.Fatalf("page_count: %v", err)
	}
	desired, expectedEffective, _, err := databaseGrowthPageLimits(
		ctx, database, storage.DefaultDatabaseMaxBytes,
	)
	if err != nil {
		t.Fatalf("databaseGrowthPageLimits() error = %v", err)
	}
	if expectedEffective < desired || expectedEffective < pageCount {
		t.Fatalf(
			"computed cap = desired:%d effective:%d page_count:%d page_size:%d",
			desired, expectedEffective, pageCount, pageSize,
		)
	}
	if err := database.Close(); err != nil {
		t.Fatalf("Close(writable) error = %v", err)
	}

	reader, err := OpenReadOnly(ctx, path)
	if err != nil {
		t.Fatalf("OpenReadOnly() error = %v", err)
	}
	defer reader.Close()
	var connectionLocal int64
	if err := reader.QueryRow(`PRAGMA max_page_count`).Scan(&connectionLocal); err != nil {
		t.Fatalf("read-only max_page_count: %v", err)
	}
	if connectionLocal == expectedEffective {
		t.Fatalf("test requires read-only connection-local cap to differ from policy cap")
	}
	sample, err := InspectDatabaseGrowth(
		ctx, reader, path, storage.DefaultDatabaseMaxBytes, 75, 0,
	)
	if err != nil {
		t.Fatalf("InspectDatabaseGrowth() error = %v", err)
	}
	if sample.MaxPageCount != expectedEffective {
		t.Fatalf(
			"InspectDatabaseGrowth MaxPageCount = %d, want policy-effective %d (connection-local %d)",
			sample.MaxPageCount, expectedEffective, connectionLocal,
		)
	}
}

type growthMailerRecorder struct {
	mu       sync.Mutex
	subjects []string
	err      error
}

func (mailer *growthMailerRecorder) Send(_ context.Context, subject, _ string) error {
	mailer.mu.Lock()
	defer mailer.mu.Unlock()
	mailer.subjects = append(mailer.subjects, subject)
	return mailer.err
}

func (mailer *growthMailerRecorder) count() int {
	mailer.mu.Lock()
	defer mailer.mu.Unlock()
	return len(mailer.subjects)
}

func (mailer *growthMailerRecorder) lastKind() string {
	mailer.mu.Lock()
	defer mailer.mu.Unlock()
	if len(mailer.subjects) == 0 {
		return ""
	}
	subject := mailer.subjects[len(mailer.subjects)-1]
	const prefix = "Activity-Relay-Directory storage "
	if len(subject) >= len(prefix) && subject[:len(prefix)] == prefix {
		return subject[len(prefix):]
	}
	return subject
}

func openGrowthDatabase(t *testing.T) (string, *sql.DB) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "directory.sqlite")
	bootstrap, err := Open(context.Background(), path)
	if err != nil {
		t.Fatalf("Open(bootstrap) error = %v", err)
	}
	if err := Migrate(context.Background(), bootstrap); err != nil {
		_ = bootstrap.Close()
		t.Fatalf("Migrate(bootstrap) error = %v", err)
	}
	if err := bootstrap.Close(); err != nil {
		t.Fatalf("Close(bootstrap) error = %v", err)
	}
	database, _, _, err := OpenGuarded(
		context.Background(), path, storage.DefaultDatabaseMaxBytes,
	)
	if err != nil {
		t.Fatalf("OpenGuarded() error = %v", err)
	}
	t.Cleanup(func() { _ = database.Close() })
	return path, database
}

func openUnguardedGrowthDatabase(t *testing.T) (string, *sql.DB) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "directory.sqlite")
	database, err := Open(context.Background(), path)
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	t.Cleanup(func() { _ = database.Close() })
	if err := Migrate(context.Background(), database); err != nil {
		t.Fatalf("Migrate() error = %v", err)
	}
	return path, database
}

func reopenGrowthDatabaseWithBudget(
	t *testing.T,
	database *sql.DB,
	path string,
	maxBytes int64,
) *sql.DB {
	t.Helper()
	if err := database.Close(); err != nil {
		t.Fatalf("Close(before guarded reopen) error = %v", err)
	}
	reopened, _, _, err := OpenGuarded(context.Background(), path, maxBytes)
	if err != nil {
		t.Fatalf("OpenGuarded(test budget %d) error = %v", maxBytes, err)
	}
	t.Cleanup(func() { _ = reopened.Close() })
	return reopened
}

func checkpointGrowthTest(t *testing.T, database *sql.DB) {
	t.Helper()
	if _, err := database.Exec(`PRAGMA wal_checkpoint(TRUNCATE)`); err != nil {
		t.Fatalf("wal_checkpoint(TRUNCATE): %v", err)
	}
}

func growthPragma(t *testing.T, database *sql.DB, name string) int64 {
	t.Helper()
	value, err := pragmaInt64(context.Background(), database, name)
	if err != nil {
		t.Fatalf("PRAGMA %s: %v", name, err)
	}
	return value
}

func findGrowthBudgetForState(
	t *testing.T,
	database *sql.DB,
	path string,
	want storage.DatabaseGrowthState,
) int64 {
	t.Helper()
	base, err := InspectDatabaseGrowth(
		context.Background(), database, path, storage.DefaultDatabaseMaxBytes, 75, 0,
	)
	if err != nil {
		t.Fatalf("baseline growth inspect: %v", err)
	}
	minimum := base.PhysicalFamilyBytes
	if base.UsedLogicalBytes > minimum {
		minimum = base.UsedLogicalBytes
	}
	if minimum < base.PageSize*16 {
		minimum = base.PageSize * 16
	}

	targetPressure := map[storage.DatabaseGrowthState]int{
		storage.DatabaseGrowthNormal:   60,
		storage.DatabaseGrowthWarning:  80,
		storage.DatabaseGrowthCritical: 92,
		storage.DatabaseGrowthHard:     105,
	}[want]
	if targetPressure == 0 {
		t.Fatalf("unsupported growth state %q", want)
	}

	bestBudget := int64(0)
	bestDistance := int(^uint(0) >> 1)
	for multiplier := int64(50); multiplier <= 500; multiplier++ {
		budget := (minimum*multiplier + 99) / 100
		sample, err := InspectDatabaseGrowth(context.Background(), database, path, budget, 75, 0)
		if err != nil || sample.State != want {
			continue
		}
		distance := sample.PressurePercent - targetPressure
		if distance < 0 {
			distance = -distance
		}
		if distance < bestDistance {
			bestDistance = distance
			bestBudget = budget
		}
	}
	if bestBudget != 0 {
		return bestBudget
	}
	t.Fatalf("unable to find budget producing state %q", want)
	return 0
}

func containsString(value, substring string) bool {
	for index := 0; index+len(substring) <= len(value); index++ {
		if value[index:index+len(substring)] == substring {
			return true
		}
	}
	return false
}
