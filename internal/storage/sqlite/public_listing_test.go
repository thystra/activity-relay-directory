package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	v1 "github.com/thystra/activity-relay-directory/internal/protocol/v1"
	"github.com/thystra/activity-relay-directory/internal/storage"
)

func TestPublicListingQueryExcludesAllPrivateAndExpiredRowsAtSQLBoundary(t *testing.T) {
	database := openMigratedTestDatabase(t)
	repository := newTestRelayRepository(t, database)
	observed := time.Unix(4_000_000, 0).UTC()

	fixtures := []struct {
		actor          string
		age            time.Duration
		lifecycle      string
		administrative string
		want           v1.HealthState
	}{
		{"https://fresh.example/actor", 0, lifecycleRegistered, administrativeActive, v1.HealthHealthy},
		{"https://healthy.example/actor", storage.HealthyThrough, lifecycleRegistered, administrativeActive, v1.HealthHealthy},
		{"https://stale.example/actor", storage.HealthyThrough + time.Second, lifecycleRegistered, administrativeActive, v1.HealthStale},
		{"https://dead.example/actor", storage.DeadBefore - time.Second, lifecycleRegistered, administrativeActive, v1.HealthDead},
		{"https://boundary.example/actor", storage.DeadBefore, lifecycleRegistered, administrativeActive, ""},
		{"https://older.example/actor", storage.DeadBefore + time.Second, lifecycleRegistered, administrativeActive, ""},
		{"https://suspended.example/actor", 0, lifecycleRegistered, administrativeSuspended, ""},
		{"https://unregistered.example/actor", 0, lifecycleUnregistered, administrativeActive, ""},
		{"https://pruned.example/actor", storage.DeadBefore, lifecyclePruned, administrativeActive, ""},
	}
	for _, fixture := range fixtures {
		lastSeen := observed.Add(-fixture.age).Unix()
		insertPublicListingRelay(t, database, fixture.actor, fixture.lifecycle, fixture.administrative, lastSeen)
	}

	page, err := repository.ListPublicRelays(context.Background(), storage.HealthProjectionQuery{
		Limit: storage.MaximumPublicListingPage, ObservedAt: observed,
	})
	if err != nil {
		t.Fatalf("ListPublicRelays() error = %v", err)
	}
	got := make(map[string]v1.HealthState)
	for _, relay := range page.Relays {
		got[relay.RelayActor] = relay.HealthState
	}
	for _, fixture := range fixtures {
		state, exists := got[fixture.actor]
		if fixture.want == "" {
			if exists {
				t.Fatalf("ineligible relay %s entered public query as %s", fixture.actor, state)
			}
			continue
		}
		if !exists || state != fixture.want {
			t.Fatalf("eligible relay %s = %q/%t, want %q", fixture.actor, state, exists, fixture.want)
		}
	}
}

func TestPublicListingPaginatesWithoutDuplicatesOrOmissions(t *testing.T) {
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
		insertPublicListingRelay(t, database, actors[index], lifecycleRegistered, administrativeActive, lastSeen[index])
	}

	var got []string
	cursor := storage.HealthProjectionCursor{}
	for {
		page, err := repository.ListPublicRelays(context.Background(), storage.HealthProjectionQuery{
			After: cursor, Limit: 2, ObservedAt: observed,
		})
		if err != nil {
			t.Fatalf("ListPublicRelays(%#v) error = %v", cursor, err)
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

func TestPublicListingRejectsInvalidInputAndFutureRows(t *testing.T) {
	database := openMigratedTestDatabase(t)
	repository := newTestRelayRepository(t, database)
	observed := time.Unix(100, 0).UTC()
	insertPublicListingRelay(t, database, "https://future.example/actor", lifecycleRegistered, administrativeActive, 101)

	_, err := repository.ListPublicRelays(context.Background(), storage.HealthProjectionQuery{Limit: 10, ObservedAt: observed})
	if !errors.Is(err, storage.ErrHealthTime) {
		t.Fatalf("future row error = %v, want ErrHealthTime", err)
	}

	for _, query := range []storage.HealthProjectionQuery{
		{},
		{Limit: storage.MaximumPublicListingPage + 1, ObservedAt: observed},
		{Limit: 1, ObservedAt: time.Unix(-1, 0)},
		{Limit: 1, ObservedAt: observed, After: storage.HealthProjectionCursor{LastSeenUnix: 1, RelayActor: "HTTPS://relay.example/actor"}},
	} {
		_, err := repository.ListPublicRelays(context.Background(), query)
		if !errors.Is(err, storage.ErrHealthReadInput) {
			t.Fatalf("ListPublicRelays(%#v) error = %v, want ErrHealthReadInput", query, err)
		}
	}
}

func TestPublicListingMaximumPageUsesIndexedBoundedRead(t *testing.T) {
	database := openMigratedTestDatabase(t)
	repository := newTestRelayRepository(t, database)
	observed := time.Unix(2_000_000, 0).UTC()
	for index := 0; index < 1000; index++ {
		actor := fmt.Sprintf("https://relay-%04d.example/actor", index)
		insertPublicListingRelay(t, database, actor, lifecycleRegistered, administrativeActive, observed.Unix()-int64(1000-index))
	}
	page, err := repository.ListPublicRelays(context.Background(), storage.HealthProjectionQuery{
		Limit: storage.MaximumPublicListingPage, ObservedAt: observed,
	})
	if err != nil {
		t.Fatalf("ListPublicRelays(max) error = %v", err)
	}
	if len(page.Relays) != storage.MaximumPublicListingPage || page.Next == (storage.HealthProjectionCursor{}) {
		t.Fatalf("max page = len %d next %#v", len(page.Relays), page.Next)
	}

	rows, err := database.Query(
		`EXPLAIN QUERY PLAN
		 SELECT relay_actor, public_base_url, last_seen_at_unix
		 FROM relays INDEXED BY relays_health_projection_idx
		 WHERE lifecycle_state = ?
		   AND administrative_state = ?
		   AND last_seen_at_unix > ?
		   AND (last_seen_at_unix, relay_actor) > (?, ?)
		 ORDER BY last_seen_at_unix, relay_actor
		 LIMIT ?`,
		lifecycleRegistered,
		administrativeActive,
		observed.Add(-storage.DeadBefore).Unix(),
		0,
		"",
		storage.MaximumPublicListingPage+1,
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
	joined := strings.Join(details, "\n")
	if !strings.Contains(joined, "relays_health_projection_idx") || strings.Contains(strings.ToUpper(joined), "TEMP B-TREE") {
		t.Fatalf("query plan is not bounded by projection index:\n%s", joined)
	}
}

func insertPublicListingRelay(
	t *testing.T,
	database *sql.DB,
	actor, lifecycle, administrative string,
	lastSeen int64,
) {
	t.Helper()
	var unregistered, pruned, suspended any
	updated := lastSeen
	if lifecycle == lifecycleUnregistered {
		unregistered = lastSeen
	}
	if lifecycle == lifecyclePruned {
		pruned = lastSeen
	}
	if administrative == administrativeSuspended {
		suspended = lastSeen
	}
	_, err := database.Exec(
		`INSERT INTO relays (
		    relay_actor, public_base_url, lifecycle_state, administrative_state,
		    first_registered_at_unix, updated_at_unix, last_seen_at_unix,
		    unregistered_at_unix, pruned_at_unix, suspended_at_unix
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		actor,
		publicBaseForActor(actor),
		lifecycle,
		administrative,
		lastSeen,
		updated,
		lastSeen,
		unregistered,
		pruned,
		suspended,
	)
	if err != nil {
		t.Fatalf("insert public listing relay %s: %v", actor, err)
	}
}
