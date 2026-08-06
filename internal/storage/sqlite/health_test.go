package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"testing"
	"time"

	v1 "github.com/thystra/activity-relay-directory/internal/protocol/v1"
	"github.com/thystra/activity-relay-directory/internal/storage"
)

func TestHealthProjectionUsesFixedBoundariesAndExcludesIneligibleRows(t *testing.T) {
	database := openMigratedTestDatabase(t)
	repository := newTestRelayRepository(t, database)
	observed := time.Unix(4_000_000, 0).UTC()

	fixtures := []struct {
		actor          string
		age            time.Duration
		lifecycle      string
		administrative string
		wantState      v1.HealthState
		wantIncluded   bool
	}{
		{actor: "https://prune.example/actor", age: storage.DeadBefore, lifecycle: lifecycleRegistered, administrative: administrativeActive, wantState: v1.HealthPrune, wantIncluded: true},
		{actor: "https://dead.example/actor", age: storage.StaleBefore, lifecycle: lifecycleRegistered, administrative: administrativeActive, wantState: v1.HealthDead, wantIncluded: true},
		{actor: "https://stale.example/actor", age: storage.HealthyThrough + time.Second, lifecycle: lifecycleRegistered, administrative: administrativeActive, wantState: v1.HealthStale, wantIncluded: true},
		{actor: "https://healthy.example/actor", age: storage.HealthyThrough, lifecycle: lifecycleRegistered, administrative: administrativeActive, wantState: v1.HealthHealthy, wantIncluded: true},
		{actor: "https://fresh.example/actor", age: 0, lifecycle: lifecycleRegistered, administrative: administrativeActive, wantState: v1.HealthHealthy, wantIncluded: true},
		{actor: "https://suspended.example/actor", age: 0, lifecycle: lifecycleRegistered, administrative: administrativeSuspended},
		{actor: "https://unregistered.example/actor", age: 0, lifecycle: lifecycleUnregistered, administrative: administrativeActive},
	}

	for _, fixture := range fixtures {
		lastSeen := observed.Add(-fixture.age).Unix()
		firstRegistered := lastSeen
		updated := lastSeen
		var unregistered, suspended *int64
		if fixture.lifecycle == lifecycleUnregistered {
			value := lastSeen
			unregistered = &value
		}
		if fixture.administrative == administrativeSuspended {
			value := lastSeen
			suspended = &value
		}
		insertHealthRelay(
			t,
			database,
			fixture.actor,
			fixture.lifecycle,
			fixture.administrative,
			firstRegistered,
			updated,
			lastSeen,
			unregistered,
			suspended,
		)
	}

	before := totalChanges(t, database)
	page, err := repository.ProjectHealth(
		context.Background(),
		storage.HealthProjectionQuery{
			Limit:      storage.MaximumHealthProjectionPage,
			ObservedAt: observed,
		},
	)
	if err != nil {
		t.Fatalf("ProjectHealth() error = %v", err)
	}
	after := totalChanges(t, database)
	if after != before {
		t.Fatalf("ProjectHealth() changed database: before=%d after=%d", before, after)
	}

	want := make(map[string]v1.HealthState)
	for _, fixture := range fixtures {
		if fixture.wantIncluded {
			want[fixture.actor] = fixture.wantState
		}
	}
	if len(page.Relays) != len(want) || page.Next != (storage.HealthProjectionCursor{}) {
		t.Fatalf("health page = %#v, want %d terminal relays", page, len(want))
	}
	for _, relay := range page.Relays {
		state, exists := want[relay.RelayActor]
		if !exists {
			t.Fatalf("ineligible relay entered projection: %#v", relay)
		}
		if relay.HealthState != state {
			t.Fatalf("health state for %s = %q, want %q", relay.RelayActor, relay.HealthState, state)
		}
		delete(want, relay.RelayActor)
	}
	if len(want) != 0 {
		t.Fatalf("missing projected relays: %#v", want)
	}
}

func TestHealthProjectionPaginatesByLastSeenAndActor(t *testing.T) {
	database := openMigratedTestDatabase(t)
	repository := newTestRelayRepository(t, database)
	observed := time.Unix(10_000, 0).UTC()

	actors := []string{
		"https://a.example/actor",
		"https://b.example/actor",
		"https://c.example/actor",
		"https://d.example/actor",
		"https://e.example/actor",
	}
	lastSeen := []int64{100, 100, 200, 300, 300}
	for index := range actors {
		insertHealthRelay(
			t,
			database,
			actors[index],
			lifecycleRegistered,
			administrativeActive,
			lastSeen[index],
			lastSeen[index],
			lastSeen[index],
			nil,
			nil,
		)
	}

	var got []string
	cursor := storage.HealthProjectionCursor{}
	for {
		page, err := repository.ProjectHealth(
			context.Background(),
			storage.HealthProjectionQuery{
				After:      cursor,
				Limit:      2,
				ObservedAt: observed,
			},
		)
		if err != nil {
			t.Fatalf("ProjectHealth(page after %#v) error = %v", cursor, err)
		}
		for _, relay := range page.Relays {
			got = append(got, relay.RelayActor)
		}
		if page.Next == (storage.HealthProjectionCursor{}) {
			break
		}
		if page.Next == cursor {
			t.Fatalf("cursor did not advance: %#v", cursor)
		}
		cursor = page.Next
	}

	if !equalStrings(got, actors) {
		t.Fatalf("paginated actors = %#v, want %#v", got, actors)
	}
}

func TestHealthProjectionRejectsFutureLastSeenAndInvalidInput(t *testing.T) {
	database := openMigratedTestDatabase(t)
	repository := newTestRelayRepository(t, database)
	observed := time.Unix(100, 0).UTC()

	insertHealthRelay(
		t,
		database,
		"https://future.example/actor",
		lifecycleRegistered,
		administrativeActive,
		101,
		101,
		101,
		nil,
		nil,
	)

	_, err := repository.ProjectHealth(
		context.Background(),
		storage.HealthProjectionQuery{Limit: 10, ObservedAt: observed},
	)
	if !errors.Is(err, storage.ErrHealthTime) {
		t.Fatalf("ProjectHealth(future row) error = %v, want ErrHealthTime", err)
	}

	for _, query := range []storage.HealthProjectionQuery{
		{},
		{Limit: storage.MaximumHealthProjectionPage + 1, ObservedAt: observed},
		{Limit: 1, ObservedAt: time.Unix(-1, 0)},
		{Limit: 1, ObservedAt: observed, After: storage.HealthProjectionCursor{LastSeenUnix: observed.Unix() + 1, RelayActor: testRelayActor}},
		{Limit: 1, ObservedAt: observed, After: storage.HealthProjectionCursor{LastSeenUnix: -1, RelayActor: testRelayActor}},
		{Limit: 1, ObservedAt: observed, After: storage.HealthProjectionCursor{LastSeenUnix: 1, RelayActor: "HTTPS://relay.example/actor"}},
	} {
		_, err := repository.ProjectHealth(context.Background(), query)
		if !errors.Is(err, storage.ErrHealthReadInput) {
			t.Fatalf("ProjectHealth(%#v) error = %v, want ErrHealthReadInput", query, err)
		}
	}

	var nilRepository *RelayRepository
	_, err = nilRepository.ProjectHealth(
		context.Background(),
		storage.HealthProjectionQuery{Limit: 1, ObservedAt: observed},
	)
	if !errors.Is(err, storage.ErrRepositoryConfiguration) {
		t.Fatalf("nil repository error = %v, want ErrRepositoryConfiguration", err)
	}
	_, err = repository.ProjectHealth(
		nil,
		storage.HealthProjectionQuery{Limit: 1, ObservedAt: observed},
	)
	if !errors.Is(err, storage.ErrRepositoryConfiguration) {
		t.Fatalf("nil context error = %v, want ErrRepositoryConfiguration", err)
	}
}

func TestHealthProjectionQueryPlanUsesCompositeRangeIndex(t *testing.T) {
	database := openMigratedTestDatabase(t)

	rows, err := database.Query(
		`EXPLAIN QUERY PLAN
		 SELECT relay_actor,
		        public_base_url,
		        last_seen_at_unix
		 FROM relays INDEXED BY relays_health_projection_idx
		 WHERE lifecycle_state = ?
		   AND administrative_state = ?
		   AND (last_seen_at_unix, relay_actor) > (?, ?)
		 ORDER BY last_seen_at_unix, relay_actor
		 LIMIT ?`,
		lifecycleRegistered,
		administrativeActive,
		100,
		"https://cursor.example/actor",
		storage.MaximumHealthProjectionPage+1,
	)
	if err != nil {
		t.Fatalf("EXPLAIN QUERY PLAN error = %v", err)
	}
	defer rows.Close()

	var details []string
	for rows.Next() {
		var id, parent, unused int
		var detail string
		if err := rows.Scan(&id, &parent, &unused, &detail); err != nil {
			t.Fatalf("scan query plan: %v", err)
		}
		details = append(details, detail)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("iterate query plan: %v", err)
	}

	joined := strings.Join(details, "\n")
	compact := strings.ReplaceAll(joined, " ", "")
	if !strings.Contains(compact, "relays_health_projection_idx") ||
		!strings.Contains(compact, "(last_seen_at_unix,relay_actor)>(?,?)") {
		t.Fatalf("query plan does not use composite health range:\n%s", joined)
	}
}

func insertHealthRelay(
	t *testing.T,
	database *sql.DB,
	actor, lifecycle, administrative string,
	firstRegistered, updated, lastSeen int64,
	unregistered, suspended *int64,
) {
	t.Helper()
	_, err := database.Exec(
		`INSERT INTO relays (
		    relay_actor,
		    public_base_url,
		    lifecycle_state,
		    administrative_state,
		    first_registered_at_unix,
		    updated_at_unix,
		    last_seen_at_unix,
		    unregistered_at_unix,
		    suspended_at_unix
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		actor,
		publicBaseForActor(actor),
		lifecycle,
		administrative,
		firstRegistered,
		updated,
		lastSeen,
		unregistered,
		suspended,
	)
	if err != nil {
		t.Fatalf("insert health relay %s: %v", actor, err)
	}
}

func publicBaseForActor(actor string) string {
	return strings.TrimSuffix(actor, "/actor")
}

func totalChanges(t *testing.T, database *sql.DB) int64 {
	t.Helper()
	var changes int64
	if err := database.QueryRow(`SELECT total_changes()`).Scan(&changes); err != nil {
		t.Fatalf("read total_changes(): %v", err)
	}
	return changes
}
