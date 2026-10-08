package sqlite

import (
	"context"
	"database/sql"
	"testing"
	"time"

	"github.com/thystra/activity-relay-directory/internal/actorresolver"
	"github.com/thystra/activity-relay-directory/internal/storage"
)

func TestReachabilityCandidatesAreFairDeduplicatedAndSuspensionAware(t *testing.T) {
	database := openMigratedTestDatabase(t)
	repository := newTestRelayRepository(t, database)
	ctx := context.Background()
	observed := time.Unix(1_000_000, 0)
	cutoff := observed.Add(-storage.ReachabilityFreshness)

	insertPruningRelay(t, database, "https://never-a.example/actor", lifecycleRegistered, administrativeActive, 100, nil)
	insertPruningRelay(t, database, "https://never-b.example/actor", lifecycleRegistered, administrativeActive, 100, nil)
	insertPruningRelay(t, database, "https://oldest.example/actor", lifecycleRegistered, administrativeActive, 100, nil)
	insertPruningRelay(t, database, "https://older.example/actor", lifecycleRegistered, administrativeActive, 100, nil)
	insertPruningRelay(t, database, "https://fresh.example/actor", lifecycleRegistered, administrativeActive, 100, nil)
	insertPruningRelay(t, database, "https://dual.example/actor", lifecycleRegistered, administrativeActive, 100, nil)
	insertPruningRelay(t, database, "https://suspended.example/actor", lifecycleRegistered, administrativeSuspended, 100, int64Pointer(200))

	insertReachabilityDiscovery(t, database, "https://discovered.example/actor", discoveryActive, 100)
	insertReachabilityDiscovery(t, database, "https://dual.example/actor", discoveryActive, 100)
	insertReachabilityDiscovery(t, database, "https://suspended.example/actor", discoveryActive, 100)
	insertReachabilityDiscovery(t, database, "https://removed.example/actor", discoveryRemoved, 100)

	for actor, checked := range map[string]time.Time{
		"https://oldest.example/actor": cutoff.Add(-2 * time.Hour),
		"https://older.example/actor":  cutoff.Add(-time.Hour),
		"https://fresh.example/actor":  cutoff,
	} {
		if err := repository.RecordActorObservation(ctx, storage.ActorObservationIntent{
			RelayActor: actor,
			State:      storage.ReachabilityReachable,
		}, checked); err != nil {
			t.Fatalf("RecordActorObservation(%s) error = %v", actor, err)
		}
	}

	var got []string
	var after storage.ReachabilityCandidateCursor
	for {
		page, err := repository.ReachabilityCandidates(ctx, storage.ReachabilityCandidateQuery{
			After:      after,
			Limit:      2,
			ObservedAt: observed,
		})
		if err != nil {
			t.Fatalf("ReachabilityCandidates() error = %v", err)
		}
		for _, candidate := range page.Candidates {
			got = append(got, candidate.RelayActor)
		}
		if page.Next == (storage.ReachabilityCandidateCursor{}) {
			break
		}
		after = page.Next
	}
	want := []string{
		"https://discovered.example/actor",
		"https://dual.example/actor",
		"https://never-a.example/actor",
		"https://never-b.example/actor",
		"https://oldest.example/actor",
		"https://older.example/actor",
	}
	if !equalStrings(got, want) {
		t.Fatalf("candidate order = %#v, want %#v", got, want)
	}
}

func TestRecordReachabilityObservationRevalidatesEligibilityAndTime(t *testing.T) {
	database := openMigratedTestDatabase(t)
	repository := newTestRelayRepository(t, database)
	ctx := context.Background()
	actor := "https://relay.example/actor"
	insertPruningRelay(t, database, actor, lifecycleRegistered, administrativeActive, 100, nil)
	observed := time.Unix(1000, 0)

	outcome, err := repository.RecordReachabilityObservation(ctx, storage.ReachabilityObservationIntent{
		RelayActor: actor,
		ActorState: storage.ReachabilityReachable,
		InboxURL:   "https://relay.example/inbox",
		InboxState: storage.InboxMethodRejected,
	}, observed)
	if err != nil || outcome != storage.ReachabilityWriteApplied {
		t.Fatalf("RecordReachabilityObservation() = (%q, %v)", outcome, err)
	}
	observation, ok, err := repository.GetObservation(ctx, storage.IdentityIntent{RelayActor: actor})
	if err != nil || !ok || observation.ActorState != storage.ReachabilityReachable ||
		observation.ActorLastCheckedUnix == nil || *observation.ActorLastCheckedUnix != observed.Unix() ||
		observation.ActorLastSuccessUnix == nil || *observation.ActorLastSuccessUnix != observed.Unix() ||
		observation.InboxURL != "https://relay.example/inbox" ||
		observation.InboxProbeState != storage.InboxMethodRejected ||
		observation.InboxLastCheckedUnix == nil || *observation.InboxLastCheckedUnix != observed.Unix() {
		t.Fatalf("observation = (%#v, %t, %v)", observation, ok, err)
	}

	outcome, err = repository.RecordReachabilityObservation(ctx, storage.ReachabilityObservationIntent{
		RelayActor: actor,
		ActorState: storage.ReachabilityUnreachable,
		InboxState: storage.InboxNotChecked,
	}, observed)
	if err != nil || outcome != storage.ReachabilityWriteSkipped {
		t.Fatalf("same-time stale write = (%q, %v)", outcome, err)
	}

	if _, err := database.Exec(`UPDATE relays SET administrative_state = ?, suspended_at_unix = ?, updated_at_unix = ? WHERE relay_actor = ?`,
		administrativeSuspended, 1100, 1100, actor); err != nil {
		t.Fatalf("suspend relay: %v", err)
	}
	outcome, err = repository.RecordReachabilityObservation(ctx, storage.ReachabilityObservationIntent{
		RelayActor: actor,
		ActorState: storage.ReachabilityUnreachable,
		InboxState: storage.InboxNotChecked,
	}, observed.Add(time.Hour))
	if err != nil || outcome != storage.ReachabilityWriteSkipped {
		t.Fatalf("suspended write = (%q, %v)", outcome, err)
	}
}

func TestRecordReachabilityObservationAllowsIndependentDiscoveryPath(t *testing.T) {
	database := openMigratedTestDatabase(t)
	repository := newTestRelayRepository(t, database)
	ctx := context.Background()
	actor := "https://discovery-only.example/actor"
	insertReachabilityDiscovery(t, database, actor, discoveryActive, 100)

	outcome, err := repository.RecordReachabilityObservation(ctx, storage.ReachabilityObservationIntent{
		RelayActor: actor,
		ActorState: storage.ReachabilityReachable,
		InboxState: storage.InboxNotChecked,
	}, time.Unix(200, 0))
	if err != nil || outcome != storage.ReachabilityWriteApplied {
		t.Fatalf("active discovery write = (%q, %v)", outcome, err)
	}
	if _, err := database.Exec(`UPDATE relay_discoveries SET discovery_state = ?, removed_at_unix = ?, updated_at_unix = ? WHERE relay_actor = ?`,
		discoveryRemoved, 300, 300, actor); err != nil {
		t.Fatalf("remove discovery: %v", err)
	}
	outcome, err = repository.RecordReachabilityObservation(ctx, storage.ReachabilityObservationIntent{
		RelayActor: actor,
		ActorState: storage.ReachabilityUnreachable,
		InboxState: storage.InboxNotChecked,
	}, time.Unix(400, 0))
	if err != nil || outcome != storage.ReachabilityWriteSkipped {
		t.Fatalf("removed discovery write = (%q, %v)", outcome, err)
	}
}

func TestReachabilityInputValidation(t *testing.T) {
	database := openMigratedTestDatabase(t)
	repository := newTestRelayRepository(t, database)
	ctx := context.Background()
	if _, err := repository.ReachabilityCandidates(ctx, storage.ReachabilityCandidateQuery{
		Limit: storage.MaximumReachabilityCandidatePage + 1, ObservedAt: time.Now(),
	}); err != storage.ErrReachabilityReadInput {
		t.Fatalf("oversized candidate query error = %v", err)
	}
	if _, err := repository.RecordReachabilityObservation(ctx, storage.ReachabilityObservationIntent{
		RelayActor: "https://relay.example/actor",
		ActorState: storage.ReachabilityReachable,
		InboxURL:   "https://relay.example/inbox",
		InboxState: storage.InboxNotChecked,
	}, time.Now()); err != storage.ErrReachabilityWriteInput {
		t.Fatalf("incomplete inbox observation error = %v", err)
	}
}

func insertReachabilityDiscovery(t *testing.T, database *sql.DB, actor, state string, at int64) {
	t.Helper()
	var removed any
	if state == discoveryRemoved {
		removed = at
	}
	if _, err := database.Exec(`INSERT INTO relay_discoveries (
		relay_actor, public_base_url, discovery_state,
		first_discovered_at_unix, updated_at_unix, removed_at_unix
	) VALUES (?, ?, ?, ?, ?, ?)`, actor, publicBaseForActor(actor), state, at, at, removed); err != nil {
		t.Fatalf("insert discovery %s: %v", actor, err)
	}
}

func TestReachabilityCandidatesSlowLongOfflineRelaysToWeeklyAndRetainPrunedRecovery(t *testing.T) {
	database := openMigratedTestDatabase(t)
	repository := newTestRelayRepository(t, database)
	ctx := context.Background()
	observed := time.Unix(2_000_000, 0).UTC()
	week := int64(storage.ReachabilityUnreachableRetry / time.Second)

	recentActor := "https://recent-offline.example/actor"
	insertPruningRelay(
		t, database, recentActor, lifecycleRegistered, administrativeActive,
		observed.Add(-48*time.Hour).Unix(), nil,
	)
	recentChecked := observed.Add(-storage.ReachabilityFreshness - time.Second).Unix()
	recentSuccess := observed.Add(-48 * time.Hour).Unix()
	insertDirectoryObservation(
		t, database, recentActor, storage.ReachabilityUnreachable,
		recentChecked, &recentSuccess, false,
	)

	weeklyActor := "https://weekly-offline.example/actor"
	weeklySeen := observed.Unix() - week - 1
	insertPruningRelay(
		t, database, weeklyActor, lifecycleRegistered, administrativeActive,
		weeklySeen, nil,
	)
	weeklyChecked := observed.Unix() - week
	insertDirectoryObservation(
		t, database, weeklyActor, storage.ReachabilityUnreachable,
		weeklyChecked, &weeklySeen, false,
	)

	prunedActor := "https://pruned-offline.example/actor"
	prunedSeen := observed.Unix() - week - 100
	prunedAt := observed.Unix() - 10
	insertPruningRelayWithPrunedTime(
		t, database, prunedActor, administrativeActive,
		prunedSeen, &prunedAt, nil,
	)
	prunedChecked := observed.Unix() - week - 1
	insertDirectoryObservation(
		t, database, prunedActor, storage.ReachabilityUnreachable,
		prunedChecked, &prunedSeen, false,
	)

	page, err := repository.ReachabilityCandidates(ctx, storage.ReachabilityCandidateQuery{
		Limit: storage.MaximumReachabilityCandidatePage, ObservedAt: observed,
	})
	if err != nil {
		t.Fatalf("ReachabilityCandidates() error = %v", err)
	}
	got := make(map[string]bool)
	for _, candidate := range page.Candidates {
		got[candidate.RelayActor] = true
	}
	if !got[recentActor] {
		t.Fatalf("recent offline relay was not due: %#v", got)
	}
	if got[weeklyActor] {
		t.Fatalf("weekly relay became due at exact seven-day boundary: %#v", got)
	}
	if !got[prunedActor] {
		t.Fatalf("soft-pruned relay was not retained for recovery checks: %#v", got)
	}

	page, err = repository.ReachabilityCandidates(ctx, storage.ReachabilityCandidateQuery{
		Limit: storage.MaximumReachabilityCandidatePage, ObservedAt: observed.Add(time.Second),
	})
	if err != nil {
		t.Fatalf("ReachabilityCandidates(+1s) error = %v", err)
	}
	got = make(map[string]bool)
	for _, candidate := range page.Candidates {
		got[candidate.RelayActor] = true
	}
	if !got[weeklyActor] {
		t.Fatalf("weekly relay was not due after seven days: %#v", got)
	}

	outcome, err := repository.RecordReachabilityObservation(ctx, storage.ReachabilityObservationIntent{
		RelayActor: prunedActor,
		ActorState: storage.ReachabilityReachable,
		InboxState: storage.InboxNotChecked,
	}, observed.Add(2*time.Second))
	if err != nil || outcome != storage.ReachabilityWriteApplied {
		t.Fatalf("pruned recovery write = (%q, %v)", outcome, err)
	}
}

func TestReachabilityDiagnosticEvidenceSurvivesActorFailureWithoutHeartbeatMutation(t *testing.T) {
	database := openMigratedTestDatabase(t)
	repo := newTestRelayRepository(t, database)
	ctx := context.Background()
	actor := "https://diagnostic.example/actor"
	insertPruningRelay(t, database, actor, lifecycleRegistered, administrativeActive, 100, nil)
	first := time.Unix(100000, 0)
	_, err := repo.RecordReachabilityObservation(ctx, storage.ReachabilityObservationIntent{
		RelayActor: actor, ActorState: storage.ReachabilityReachable,
		InboxURL: "https://diagnostic.example/inbox", InboxState: storage.InboxMethodRejected,
		InboxDiagnostic: actorresolver.ProbeDiagnostic{Stage: "inbox", Code: "http_status", HTTPStatus: 405},
	}, first)
	if err != nil {
		t.Fatal(err)
	}
	second := first.Add(7 * time.Hour)
	_, err = repo.RecordReachabilityObservation(ctx, storage.ReachabilityObservationIntent{
		RelayActor: actor, ActorState: storage.ReachabilityUnreachable,
		InboxState:      storage.InboxNotChecked,
		ActorDiagnostic: actorresolver.ProbeDiagnostic{Stage: "actor", Code: "http_status", HTTPStatus: 410},
	}, second)
	if err != nil {
		t.Fatal(err)
	}
	var actorStage, actorCode, inboxStage, inboxCode string
	var actorStatus, inboxStatus int
	var next int64
	if err := database.QueryRow(`SELECT actor_stage,actor_code,actor_http_status,
        inbox_stage,inbox_code,inbox_http_status,next_check_at_unix
        FROM relay_probe_diagnostics WHERE relay_actor=?`, actor).Scan(
		&actorStage, &actorCode, &actorStatus, &inboxStage, &inboxCode, &inboxStatus, &next,
	); err != nil {
		t.Fatal(err)
	}
	if actorStage != "actor" || actorCode != "http_status" || actorStatus != 410 ||
		inboxStage != "inbox" || inboxCode != "http_status" || inboxStatus != 405 ||
		next != second.Add(storage.ReachabilityFailureRetryMinimum).Unix() {
		t.Fatalf("diagnostic actor=%s/%s/%d inbox=%s/%s/%d next=%d", actorStage, actorCode, actorStatus, inboxStage, inboxCode, inboxStatus, next)
	}
	observation, ok, err := repo.GetObservation(ctx, storage.IdentityIntent{RelayActor: actor})
	if err != nil || !ok || observation.ActorState != storage.ReachabilityUnreachable ||
		observation.ActorLastSuccessUnix == nil || *observation.ActorLastSuccessUnix != first.Unix() ||
		observation.InboxProbeState != storage.InboxMethodRejected {
		t.Fatalf("previous observations lost: %#v ok=%v err=%v", observation, ok, err)
	}
}

func TestReachabilityDiagnosticLongOfflineRetryAndInputValidation(t *testing.T) {
	database := openMigratedTestDatabase(t)
	repo := newTestRelayRepository(t, database)
	actor := "https://long-offline.example/actor"
	insertPruningRelay(t, database, actor, lifecycleRegistered, administrativeActive, 100, nil)
	at := time.Unix(100+int64(storage.ReachabilityUnreachableRetry/time.Second)+10, 0)
	_, err := repo.RecordReachabilityObservation(context.Background(), storage.ReachabilityObservationIntent{
		RelayActor: actor, ActorState: storage.ReachabilityUnreachable, InboxState: storage.InboxNotChecked,
		ActorDiagnostic: actorresolver.ProbeDiagnostic{Stage: "dns", Code: "nxdomain"},
	}, at)
	if err != nil {
		t.Fatal(err)
	}
	var next int64
	if err := database.QueryRow(`SELECT next_check_at_unix FROM relay_probe_diagnostics WHERE relay_actor=?`, actor).Scan(&next); err != nil {
		t.Fatal(err)
	}
	if next != at.Add(storage.ReachabilityUnreachableRetry).Unix() {
		t.Fatalf("retry=%d", next)
	}
	_, err = repo.RecordReachabilityObservation(context.Background(), storage.ReachabilityObservationIntent{
		RelayActor: actor, ActorState: storage.ReachabilityUnreachable, InboxState: storage.InboxNotChecked,
		ActorDiagnostic: actorresolver.ProbeDiagnostic{Stage: "actor", Code: "http_status", HTTPStatus: 999},
	}, at.Add(time.Hour))
	if err != storage.ErrReachabilityWriteInput {
		t.Fatalf("unbounded status accepted: %v", err)
	}
}

func TestActorFailureRetryEscalationAndRecoveryResetsStreak(t *testing.T) {
	database := openMigratedTestDatabase(t)
	repository := newTestRelayRepository(t, database)
	ctx := context.Background()
	actor := "https://retry-sequence.example/actor"
	base := time.Unix(8_000_000, 0).UTC()
	insertPruningRelay(t, database, actor, lifecycleRegistered, administrativeActive, base.Unix()-100, nil)

	attempt := base
	for index, interval := range []time.Duration{time.Hour, 2 * time.Hour, 4 * time.Hour, 6 * time.Hour, 6 * time.Hour} {
		outcome, err := repository.RecordReachabilityObservation(ctx, storage.ReachabilityObservationIntent{
			RelayActor: actor, ActorState: storage.ReachabilityUnreachable,
			InboxState:      storage.InboxNotChecked,
			ActorDiagnostic: actorresolver.ProbeDiagnostic{Stage: "network", Code: "timeout"},
		}, attempt)
		if err != nil || outcome != storage.ReachabilityWriteApplied {
			t.Fatalf("failure %d write = (%q, %v)", index+1, outcome, err)
		}
		var streak int
		var due int64
		if err := database.QueryRow(`SELECT actor_failure_streak, next_check_at_unix
			FROM relay_probe_diagnostics WHERE relay_actor=?`, actor).Scan(&streak, &due); err != nil {
			t.Fatal(err)
		}
		wantStreak := min(index+1, storage.MaximumReachabilityFailureStreak)
		if streak != wantStreak || due != attempt.Add(interval).Unix() {
			t.Fatalf("failure %d streak=%d due=%d want streak=%d due=%d", index+1, streak, due, wantStreak, attempt.Add(interval).Unix())
		}
		for _, check := range []struct {
			at   time.Time
			want bool
		}{{attempt.Add(interval - time.Second), false}, {attempt.Add(interval), true}} {
			page, err := repository.ReachabilityCandidates(ctx, storage.ReachabilityCandidateQuery{ObservedAt: check.at, Limit: 10})
			if err != nil {
				t.Fatal(err)
			}
			found := false
			for _, candidate := range page.Candidates {
				if candidate.RelayActor == actor {
					found = true
				}
			}
			if found != check.want {
				t.Fatalf("failure %d due check at %s found=%v want %v", index+1, check.at, found, check.want)
			}
		}
		attempt = attempt.Add(interval)
	}

	if _, err := repository.RecordReachabilityObservation(ctx, storage.ReachabilityObservationIntent{
		RelayActor: actor, ActorState: storage.ReachabilityReachable, InboxState: storage.InboxNotChecked,
	}, attempt); err != nil {
		t.Fatal(err)
	}
	var streak int
	var due int64
	if err := database.QueryRow(`SELECT actor_failure_streak, next_check_at_unix
		FROM relay_probe_diagnostics WHERE relay_actor=?`, actor).Scan(&streak, &due); err != nil {
		t.Fatal(err)
	}
	if streak != 0 || due != attempt.Add(storage.ReachabilityFreshness).Unix() {
		t.Fatalf("recovered actor streak=%d due=%d", streak, due)
	}
	attempt = attempt.Add(storage.ReachabilityFreshness)
	if _, err := repository.RecordReachabilityObservation(ctx, storage.ReachabilityObservationIntent{
		RelayActor: actor, ActorState: storage.ReachabilityUnreachable,
		InboxState:      storage.InboxNotChecked,
		ActorDiagnostic: actorresolver.ProbeDiagnostic{Stage: "tls", Code: "certificate"},
	}, attempt); err != nil {
		t.Fatal(err)
	}
	if err := database.QueryRow(`SELECT actor_failure_streak, next_check_at_unix
		FROM relay_probe_diagnostics WHERE relay_actor=?`, actor).Scan(&streak, &due); err != nil {
		t.Fatal(err)
	}
	if streak != 1 || due != attempt.Add(time.Hour).Unix() {
		t.Fatalf("post-recovery first failure streak=%d due=%d", streak, due)
	}
}

func TestSchema14PromotesRecentExistingFailureToHourlyRetry(t *testing.T) {
	database := openTestDatabase(t)
	migrations, err := loadMigrations()
	if err != nil {
		t.Fatal(err)
	}
	if len(migrations) != 14 || migrations[13].version != 14 {
		t.Fatalf("wrong migration sequence (%d)", len(migrations))
	}
	if _, err := database.Exec(migrationTableSQL); err != nil {
		t.Fatal(err)
	}
	for _, migration := range migrations[:13] {
		if _, err := database.Exec(migration.sql); err != nil {
			t.Fatalf("migration %d: %v", migration.version, err)
		}
		if _, err := database.Exec(`INSERT INTO schema_migrations(version,name,sha256,applied_at_unix)
			VALUES (?,?,?,0)`, migration.version, migration.name, migration.sha256); err != nil {
			t.Fatal(err)
		}
	}
	actor := "https://existing-offline.example/actor"
	insertPruningRelay(t, database, actor, lifecycleRegistered, administrativeActive, 990_000, nil)
	insertDirectoryObservation(t, database, actor, storage.ReachabilityUnreachable, 1_000_000, nil, false)
	if _, err := database.Exec(`INSERT INTO relay_probe_diagnostics(
		relay_actor, actor_stage, actor_code, next_check_at_unix, updated_at_unix
	) VALUES (?, 'network', 'timeout', ?, ?)`, actor, 1_000_000+6*3600, 1_000_000); err != nil {
		t.Fatal(err)
	}
	if err := Migrate(context.Background(), database); err != nil {
		t.Fatal(err)
	}
	var streak int
	var due int64
	if err := database.QueryRow(`SELECT actor_failure_streak, next_check_at_unix
		FROM relay_probe_diagnostics WHERE relay_actor=?`, actor).Scan(&streak, &due); err != nil {
		t.Fatal(err)
	}
	if streak != 1 || due != 1_000_000+3600 {
		t.Fatalf("schema14 existing failure streak=%d due=%d", streak, due)
	}
}
