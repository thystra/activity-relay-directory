package httpapi

import (
	"context"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"github.com/thystra/activity-relay-directory/internal/admission"
	"github.com/thystra/activity-relay-directory/internal/config"
	v1 "github.com/thystra/activity-relay-directory/internal/protocol/v1"
	"github.com/thystra/activity-relay-directory/internal/storage"
	sqlitestore "github.com/thystra/activity-relay-directory/internal/storage/sqlite"
)

func TestLifecycleOperationsDriveAcceleratedHealthProjection(t *testing.T) {
	ctx := context.Background()
	database, err := sqlitestore.Open(
		ctx,
		filepath.Join(t.TempDir(), "directory.sqlite"),
	)
	if err != nil {
		t.Fatalf("sqlite.Open() error = %v", err)
	}
	t.Cleanup(func() { _ = database.Close() })
	if err := sqlitestore.Migrate(ctx, database); err != nil {
		t.Fatalf("sqlite.Migrate() error = %v", err)
	}
	if _, err := database.Exec(
		`UPDATE directory_policy SET enrollment_open = 1 WHERE singleton = 1`,
	); err != nil {
		t.Fatalf("open enrollment: %v", err)
	}

	repository, err := sqlitestore.NewRelayRepository(database, storage.AllowWrites)
	if err != nil {
		t.Fatalf("NewRelayRepository() error = %v", err)
	}
	sourceResolver, err := admission.NewSourceResolver(nil)
	if err != nil {
		t.Fatalf("NewSourceResolver() error = %v", err)
	}

	now := time.Unix(2_000_000, 0).UTC()
	lifecycle, err := NewLifecycleHandler(LifecycleDependencies{
		Verifier:         &recordingLifecycleVerifier{},
		ReplayStore:      &allowingReplayStore{},
		Repository:       repository,
		SourceResolver:   sourceResolver,
		Limiter:          generousLifecycleLimiter(t),
		MaximumBodyBytes: 4096,
		Now:              func() time.Time { return now },
	})
	if err != nil {
		t.Fatalf("NewLifecycleHandler() error = %v", err)
	}
	handler := NewHandlerWithLifecycle(
		config.Config{
			PublicBaseURL:       "https://directory.example",
			LifecycleEnabled:    true,
			MaxRequestBodyBytes: 4096,
		},
		"health-test",
		func(context.Context) error { return nil },
		lifecycle,
		func(context.Context) (bool, error) { return true, nil },
	)

	postLifecycle(t, handler, v1.RegisterEndpointPath, http.StatusCreated)
	assertProjectedHealth(t, repository, now, v1.HealthHealthy)

	now = now.Add(storage.HealthyThrough + time.Second)
	assertProjectedHealth(t, repository, now, v1.HealthStale)

	now = time.Unix(2_000_000, 0).UTC().Add(storage.StaleBefore)
	assertProjectedHealth(t, repository, now, v1.HealthDead)

	postLifecycle(t, handler, v1.HeartbeatEndpointPath, http.StatusOK)
	assertProjectedHealth(t, repository, now, v1.HealthHealthy)
}

func postLifecycle(
	t *testing.T,
	handler http.Handler,
	path string,
	wantStatus int,
) {
	t.Helper()
	request := httptest.NewRequest(http.MethodPost, path, nil)
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != wantStatus {
		t.Fatalf("POST %s status = %d, body = %q", path, response.Code, response.Body.String())
	}
}

func assertProjectedHealth(
	t *testing.T,
	repository storage.HealthProjectionRepository,
	observedAt time.Time,
	want v1.HealthState,
) {
	t.Helper()
	page, err := repository.ProjectHealth(
		context.Background(),
		storage.HealthProjectionQuery{Limit: 10, ObservedAt: observedAt},
	)
	if err != nil {
		t.Fatalf("ProjectHealth() error = %v", err)
	}
	if len(page.Relays) != 1 || page.Relays[0].RelayActor != lifecycleTestActor ||
		page.Relays[0].HealthState != want {
		t.Fatalf("health page = %#v, want actor %s state %s", page, lifecycleTestActor, want)
	}
}
