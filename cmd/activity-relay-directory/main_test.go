package main

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/thystra/activity-relay-directory/internal/config"
	storageContract "github.com/thystra/activity-relay-directory/internal/storage"
	storage "github.com/thystra/activity-relay-directory/internal/storage/sqlite"
)

func TestAdminEnrollmentCLIStatusOpenCloseAndAudit(t *testing.T) {
	path := filepath.Join(t.TempDir(), "directory.sqlite")
	t.Setenv("DIRECTORY_DATABASE_PATH", path)
	now := func() time.Time { return time.Unix(100, 0) }
	run := func(arguments ...string) (int, string, string) {
		t.Helper()
		var stdout, stderr bytes.Buffer
		code := runAdmin(arguments, &stdout, &stderr, now)
		return code, stdout.String(), stderr.String()
	}

	code, stdout, stderr := run(
		"activity-relay-directory", "admin", "enrollment", "status",
	)
	if code != 0 || stdout != "closed\n" || stderr != "" {
		t.Fatalf("status = (%d, %q, %q)", code, stdout, stderr)
	}
	code, stdout, stderr = run(
		"activity-relay-directory", "admin", "enrollment", "open",
		"--operator", "operator@example.org",
	)
	if code != 0 || stdout != "opened\n" || stderr != "" {
		t.Fatalf("open = (%d, %q, %q)", code, stdout, stderr)
	}
	code, stdout, stderr = run(
		"activity-relay-directory", "admin", "enrollment", "open",
		"--operator", "operator@example.org",
	)
	if code != 0 || stdout != "already_open\n" || stderr != "" {
		t.Fatalf("idempotent open = (%d, %q, %q)", code, stdout, stderr)
	}
	code, stdout, stderr = run(
		"activity-relay-directory", "admin", "enrollment", "close",
		"--operator", "operator@example.org",
	)
	if code != 0 || stdout != "closed\n" || stderr != "" {
		t.Fatalf("close = (%d, %q, %q)", code, stdout, stderr)
	}

	database, err := initializeDatabase(context.Background(), path)
	if err != nil {
		t.Fatalf("initializeDatabase() error = %v", err)
	}
	defer database.Close()
	var eventCount, open int
	if err := database.QueryRow(`SELECT COUNT(*) FROM enrollment_events`).Scan(&eventCount); err != nil {
		t.Fatalf("count enrollment events: %v", err)
	}
	if err := database.QueryRow(
		`SELECT enrollment_open FROM directory_policy WHERE singleton = 1`,
	).Scan(&open); err != nil {
		t.Fatalf("read enrollment policy: %v", err)
	}
	if eventCount != 3 || open != 0 {
		t.Fatalf("enrollment state = events:%d open:%d", eventCount, open)
	}
}

func TestAdminEnrollmentCLIRejectsInvalidInvocationWithoutOpeningDatabase(t *testing.T) {
	path := filepath.Join(t.TempDir(), "directory.sqlite")
	t.Setenv("DIRECTORY_DATABASE_PATH", path)
	for _, arguments := range [][]string{
		{"activity-relay-directory", "admin"},
		{"activity-relay-directory", "admin", "enrollment", "open"},
		{"activity-relay-directory", "admin", "enrollment", "open", "--operator", "bad operator"},
		{"activity-relay-directory", "admin", "enrollment", "status", "--operator", "operator"},
	} {
		var stdout, stderr bytes.Buffer
		code := runAdmin(arguments, &stdout, &stderr, func() time.Time { return time.Unix(100, 0) })
		if code != 2 || stdout.Len() != 0 || strings.TrimSpace(stderr.String()) == "" {
			t.Fatalf("runAdmin(%q) = (%d, %q, %q)", arguments, code, stdout.String(), stderr.String())
		}
	}
	if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("invalid invocation created database: %v", err)
	}
}

func TestRunFailsStartupBeforeDatabaseWhenAdministratorMailerUnavailable(t *testing.T) {
	path := filepath.Join(t.TempDir(), "directory.sqlite")
	t.Setenv("DIRECTORY_LISTEN_ADDRESS", "127.0.0.1:0")
	t.Setenv("DIRECTORY_PUBLIC_BASE_URL", "https://directory.example")
	t.Setenv("DIRECTORY_DATABASE_PATH", path)
	t.Setenv("DIRECTORY_ADMIN_EMAIL", "admin@example.com")
	t.Setenv("DIRECTORY_MAIL_COMMAND", filepath.Join(t.TempDir(), "missing-mail"))
	t.Setenv("DIRECTORY_MAIL_BACKEND", "mail")
	t.Setenv("DIRECTORY_MAIL_TIMEOUT_SECONDS", "30")
	t.Setenv("DIRECTORY_DATABASE_MAX_BYTES", "1073741824")
	t.Setenv("DIRECTORY_DATABASE_WARNING_PERCENT", "75")

	if code := run([]string{"activity-relay-directory"}); code != 2 {
		t.Fatalf("run() code = %d, want 2", code)
	}
	if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("mailer startup failure created database: %v", err)
	}
}

func TestInitializeDatabaseCreatesCurrentSchema(t *testing.T) {
	path := filepath.Join(t.TempDir(), "directory.sqlite")
	database, err := initializeDatabase(context.Background(), path)
	if err != nil {
		t.Fatalf("initializeDatabase() error = %v", err)
	}
	t.Cleanup(func() { _ = database.Close() })

	version, err := storage.SchemaVersion(context.Background(), database)
	if err != nil {
		t.Fatalf("SchemaVersion() error = %v", err)
	}
	if version != storage.CurrentSchemaVersion {
		t.Fatalf(
			"SchemaVersion() = %d, want %d",
			version,
			storage.CurrentSchemaVersion,
		)
	}
}

func TestInitializeDatabaseFailsClosedOnMigrationDrift(t *testing.T) {
	path := filepath.Join(t.TempDir(), "directory.sqlite")
	database, err := initializeDatabase(context.Background(), path)
	if err != nil {
		t.Fatalf("initial initializeDatabase() error = %v", err)
	}
	if _, err := database.Exec(
		`UPDATE schema_migrations SET sha256 = ? WHERE version = 1`,
		fmt.Sprintf("%064d", 0),
	); err != nil {
		t.Fatalf("alter migration history: %v", err)
	}
	if err := database.Close(); err != nil {
		t.Fatalf("Close() error = %v", err)
	}

	database, err = initializeDatabase(context.Background(), path)
	if database != nil {
		_ = database.Close()
	}
	if !errors.Is(err, storage.ErrMigrationDrift) {
		t.Fatalf("initializeDatabase() error = %v, want ErrMigrationDrift", err)
	}
}

func TestInitializeDatabaseHonorsCanceledContext(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	database, err := initializeDatabase(
		ctx,
		filepath.Join(t.TempDir(), "directory.sqlite"),
	)
	if database != nil {
		_ = database.Close()
	}
	if err == nil {
		t.Fatal("initializeDatabase() error = nil, want canceled startup")
	}
}

func TestInitializeLifecycleIsDisabledWithoutConstructingDependencies(t *testing.T) {
	runtime, err := initializeLifecycle(config.Config{
		LifecycleEnabled: false,
	}, nil, storageContract.AllowWrites)
	if err != nil || runtime != nil {
		t.Fatalf("initializeLifecycle(disabled) = (%#v, %v)", runtime, err)
	}
}

func TestInitializeLifecycleFailsClosedWithoutEnabledStorage(t *testing.T) {
	runtime, err := initializeLifecycle(config.Config{
		PublicBaseURL:       "https://directory.example",
		LifecycleEnabled:    true,
		MaxRequestBodyBytes: 64 * 1024,
	}, nil, storageContract.AllowWrites)
	if runtime != nil || err == nil {
		t.Fatalf("initializeLifecycle(incomplete) = (%#v, %v)", runtime, err)
	}
}

func TestInitializeLifecycleBuildsCompleteEnabledGraph(t *testing.T) {
	database, err := initializeDatabase(
		context.Background(),
		filepath.Join(t.TempDir(), "directory.sqlite"),
	)
	if err != nil {
		t.Fatalf("initializeDatabase() error = %v", err)
	}
	t.Cleanup(func() { _ = database.Close() })

	runtime, err := initializeLifecycle(config.Config{
		PublicBaseURL:        "https://directory.example",
		LifecycleEnabled:     true,
		MaxRequestBodyBytes:  64 * 1024,
		TrustedProxyPrefixes: nil,
	}, database, storageContract.AllowWrites)
	if err != nil {
		t.Fatalf("initializeLifecycle() error = %v", err)
	}
	if runtime == nil || runtime.handler == nil || runtime.replayStore == nil {
		t.Fatalf("lifecycle runtime = %#v", runtime)
	}
}

func TestLifecycleRuntimeBoundsRemainConservative(t *testing.T) {
	admissionConfig := lifecycleAdmissionConfig()
	if admissionConfig.Source.Burst != 60 ||
		admissionConfig.Source.RefillInterval != time.Second ||
		admissionConfig.Actor.Burst != 10 ||
		admissionConfig.Actor.RefillInterval != time.Minute ||
		admissionConfig.MaxSources != 10_000 ||
		admissionConfig.MaxActors != 10_000 ||
		admissionConfig.MaxConcurrent != 32 ||
		admissionConfig.IdleTTL != 24*time.Hour ||
		admissionConfig.CleanupLimit != 128 ||
		admissionConfig.OverloadRetryAfter != 5*time.Second {
		t.Fatalf("lifecycle admission config = %#v", admissionConfig)
	}
	cacheConfig := lifecycleActorCacheConfig()
	if cacheConfig.MaxEntries != 4096 || cacheConfig.TTL != 5*time.Minute {
		t.Fatalf("lifecycle actor cache config = %#v", cacheConfig)
	}
	if replayMaintenanceInterval != 5*time.Minute || replayMaintenanceBatch != 4096 {
		t.Fatalf(
			"replay maintenance = (%s, %d)",
			replayMaintenanceInterval,
			replayMaintenanceBatch,
		)
	}
}

type recordingReplayCleaner struct {
	calls chan int
	err   error
}

func (cleaner *recordingReplayCleaner) CleanupExpiredRFC9421Replay(
	ctx context.Context,
	maximum int,
) (int64, error) {
	select {
	case cleaner.calls <- maximum:
	case <-ctx.Done():
		return 0, ctx.Err()
	}
	return 0, cleaner.err
}

func TestRunReplayMaintenanceIsBoundedAndStops(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cleaner := &recordingReplayCleaner{calls: make(chan int, 1)}
	done := make(chan struct{})
	go func() {
		runReplayMaintenance(ctx, cleaner, time.Millisecond, 37, nil)
		close(done)
	}()

	select {
	case maximum := <-cleaner.calls:
		if maximum != 37 {
			t.Fatalf("cleanup maximum = %d", maximum)
		}
	case <-time.After(time.Second):
		t.Fatal("maintenance did not run")
	}
	cancel()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("maintenance did not stop")
	}
}

func TestRunReplayMaintenanceReportsConfigurationAndCleanupErrors(t *testing.T) {
	errorsSeen := make(chan error, 2)
	runReplayMaintenance(
		context.Background(),
		nil,
		time.Minute,
		1,
		func(err error) { errorsSeen <- err },
	)
	select {
	case err := <-errorsSeen:
		if err == nil {
			t.Fatal("configuration error = nil")
		}
	default:
		t.Fatal("configuration error was not reported")
	}

	ctx, cancel := context.WithCancel(context.Background())
	cleaner := &recordingReplayCleaner{
		calls: make(chan int, 1),
		err:   errors.New("private cleanup detail"),
	}
	done := make(chan struct{})
	go func() {
		runReplayMaintenance(
			ctx,
			cleaner,
			time.Millisecond,
			1,
			func(err error) {
				errorsSeen <- err
				cancel()
			},
		)
		close(done)
	}()
	select {
	case err := <-errorsSeen:
		if err == nil || err.Error() != "private cleanup detail" {
			t.Fatalf("cleanup error = %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("cleanup error was not reported")
	}
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("maintenance did not stop after cancellation")
	}
}
