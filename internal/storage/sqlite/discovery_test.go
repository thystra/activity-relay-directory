package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"testing"
	"time"

	v1 "github.com/thystra/activity-relay-directory/internal/protocol/v1"
	"github.com/thystra/activity-relay-directory/internal/storage"
)

func discoveryAdd(actor, base string) storage.DiscoveryAddIntent {
	return storage.DiscoveryAddIntent{
		RelayActor: actor, PublicBaseURL: base,
		OperatorID: "operator", ReasonCode: "public_relay",
		SourceKind: storage.DiscoverySourceFile, SourceLabel: "relay-list",
	}
}

func discoveryRemove(actor string) storage.DiscoveryRemoveIntent {
	return storage.DiscoveryRemoveIntent{
		RelayActor: actor, OperatorID: "operator", ReasonCode: "no_longer_public",
		SourceKind: storage.DiscoverySourceManual, SourceLabel: "review",
	}
}

func TestDiscoveryAddRemoveReactivatePreservesFirstSeenAndAudit(t *testing.T) {
	database := openMigratedTestDatabase(t)
	repository := newTestRelayRepository(t, database)
	ctx := context.Background()
	actor := "https://discovery.example/actor"
	base := "https://discovery.example"

	outcome, err := repository.AddDiscovery(ctx, discoveryAdd(actor, base), time.Unix(100, 0))
	if err != nil || outcome != storage.DiscoveryAdded {
		t.Fatalf("AddDiscovery(first) = (%q, %v)", outcome, err)
	}
	outcome, err = repository.AddDiscovery(ctx, discoveryAdd(actor, base), time.Unix(110, 0))
	if err != nil || outcome != storage.DiscoveryUnchanged {
		t.Fatalf("AddDiscovery(unchanged) = (%q, %v)", outcome, err)
	}
	outcome, err = repository.RemoveDiscovery(ctx, discoveryRemove(actor), time.Unix(120, 0))
	if err != nil || outcome != storage.DiscoveryRemovedOK {
		t.Fatalf("RemoveDiscovery() = (%q, %v)", outcome, err)
	}
	outcome, err = repository.AddDiscovery(ctx, discoveryAdd(actor, base), time.Unix(130, 0))
	if err != nil || outcome != storage.DiscoveryUpdated {
		t.Fatalf("AddDiscovery(reactivate) = (%q, %v)", outcome, err)
	}

	record, ok, err := repository.GetDiscovery(ctx, storage.IdentityIntent{RelayActor: actor})
	if err != nil || !ok || record.State != storage.DiscoveryActive || record.FirstDiscoveredUnix != 100 ||
		record.UpdatedUnix != 130 || record.RemovedUnix != nil || record.PublicBaseURL != base {
		t.Fatalf("GetDiscovery() = %#v, %t, %v", record, ok, err)
	}

	rows, err := database.Query(`SELECT action,operator_id,reason_code,source_kind,source_label,recorded_at_unix
		FROM discovery_events WHERE relay_actor=? ORDER BY discovery_event_id`, actor)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var actions []string
	for rows.Next() {
		var action, operatorID, reason, kind string
		var label sql.NullString
		var recorded int64
		if err := rows.Scan(&action, &operatorID, &reason, &kind, &label, &recorded); err != nil {
			t.Fatal(err)
		}
		if operatorID != "operator" || reason == "" || (kind != "file" && kind != "manual") || !label.Valid || recorded < 100 {
			t.Fatalf("invalid discovery audit row action=%s operator=%s reason=%s kind=%s label=%v recorded=%d", action, operatorID, reason, kind, label, recorded)
		}
		actions = append(actions, action)
	}
	want := []string{discoveryAdded, discoveryUnchanged, discoveryRemovedOK, discoveryUpdated}
	if !equalStrings(actions, want) {
		t.Fatalf("discovery actions = %#v, want %#v", actions, want)
	}
}

func TestDiscoveryRejectsInvalidAndRegressingInput(t *testing.T) {
	database := openMigratedTestDatabase(t)
	repository := newTestRelayRepository(t, database)
	ctx := context.Background()
	actor := "https://discovery.example/actor"
	intent := discoveryAdd(actor, "https://discovery.example")
	if _, err := repository.AddDiscovery(ctx, intent, time.Unix(100, 0)); err != nil {
		t.Fatal(err)
	}
	if _, err := repository.AddDiscovery(ctx, intent, time.Unix(99, 0)); !errors.Is(err, storage.ErrTransitionTime) {
		t.Fatalf("AddDiscovery(regressing) error = %v", err)
	}
	bad := intent
	bad.SourceLabel = "/home/operator/relays.txt"
	if _, err := repository.AddDiscovery(ctx, bad, time.Unix(101, 0)); !errors.Is(err, storage.ErrTransitionInput) {
		t.Fatalf("AddDiscovery(path label) error = %v", err)
	}
	bad = intent
	bad.RelayActor = "http://discovery.example/actor"
	if _, err := repository.AddDiscovery(ctx, bad, time.Unix(101, 0)); !errors.Is(err, storage.ErrTransitionInput) {
		t.Fatalf("AddDiscovery(http actor) error = %v", err)
	}
}

func TestTranche23AcceptanceParticipationTransitionsRemainIndependent(t *testing.T) {
	database := openMigratedTestDatabase(t)
	repository := newTestRelayRepository(t, database)
	ctx := context.Background()
	actor := "https://independent-paths.example/actor"
	base := "https://independent-paths.example"

	if _, err := repository.Register(
		ctx,
		storage.RegisterIntent{
			RelayActor:    actor,
			PublicBaseURL: base,
		},
		time.Unix(100, 0),
	); err != nil {
		t.Fatalf("Register() error = %v", err)
	}

	if _, err := repository.AddDiscovery(
		ctx,
		discoveryAdd(actor, base),
		time.Unix(110, 0),
	); err != nil {
		t.Fatalf("AddDiscovery() error = %v", err)
	}

	// Removing discovery leaves the independent lifecycle registration.
	if outcome, err := repository.RemoveDiscovery(
		ctx,
		discoveryRemove(actor),
		time.Unix(120, 0),
	); err != nil || outcome != storage.DiscoveryRemovedOK {
		t.Fatalf("RemoveDiscovery() = (%q, %v)", outcome, err)
	}

	if relay := readTestRelay(t, database, actor); relay.lifecycleState != lifecycleRegistered {
		t.Fatalf("discovery removal changed lifecycle = %#v", relay)
	}

	var state string
	if err := database.QueryRow(
		`SELECT discovery_state FROM relay_discoveries WHERE relay_actor=?`,
		actor,
	).Scan(&state); err != nil || state != discoveryRemoved {
		t.Fatalf("removed discovery state = %q, %v", state, err)
	}

	// Reactivate discovery, then unregister lifecycle participation.
	if _, err := repository.AddDiscovery(
		ctx,
		discoveryAdd(actor, base),
		time.Unix(130, 0),
	); err != nil {
		t.Fatalf("reactivate discovery error = %v", err)
	}

	if _, err := repository.Unregister(
		ctx,
		storage.IdentityIntent{RelayActor: actor},
		time.Unix(140, 0),
	); err != nil {
		t.Fatalf("Unregister() error = %v", err)
	}

	if relay := readTestRelay(t, database, actor); relay.lifecycleState != lifecycleUnregistered {
		t.Fatalf("unregister lifecycle = %#v", relay)
	}

	state = ""
	if err := database.QueryRow(
		`SELECT discovery_state FROM relay_discoveries WHERE relay_actor=?`,
		actor,
	).Scan(&state); err != nil || state != discoveryActive {
		t.Fatalf("active discovery after unregister = %q, %v", state, err)
	}
}

func TestObservationTracksActorAndInboxIndependently(t *testing.T) {
	database := openMigratedTestDatabase(t)
	repository := newTestRelayRepository(t, database)
	ctx := context.Background()
	actor := "https://discovery.example/actor"
	base := "https://discovery.example"
	inbox := "https://discovery.example/inbox"
	if _, err := repository.AddDiscovery(ctx, discoveryAdd(actor, base), time.Unix(100, 0)); err != nil {
		t.Fatal(err)
	}

	if err := repository.RecordActorObservation(ctx, storage.ActorObservationIntent{
		RelayActor: actor, State: storage.ReachabilityReachable, InboxURL: inbox,
	}, time.Unix(110, 0)); err != nil {
		t.Fatal(err)
	}
	if err := repository.RecordInboxObservation(ctx, storage.InboxObservationIntent{
		RelayActor: actor, InboxURL: inbox, State: storage.InboxMethodRejected,
	}, time.Unix(111, 0)); err != nil {
		t.Fatal(err)
	}
	if err := repository.RecordActorObservation(ctx, storage.ActorObservationIntent{
		RelayActor: actor, State: storage.ReachabilityUnreachable,
	}, time.Unix(120, 0)); err != nil {
		t.Fatal(err)
	}
	observation, ok, err := repository.GetObservation(ctx, storage.IdentityIntent{RelayActor: actor})
	if err != nil || !ok || observation.ActorState != storage.ReachabilityUnreachable ||
		observation.ActorLastCheckedUnix == nil || *observation.ActorLastCheckedUnix != 120 ||
		observation.ActorLastSuccessUnix == nil || *observation.ActorLastSuccessUnix != 110 ||
		observation.InboxURL != inbox || observation.InboxProbeState != storage.InboxMethodRejected ||
		observation.RFC9421VerifiedUnix != nil {
		t.Fatalf("observation after failure = %#v, %t, %v", observation, ok, err)
	}

	// A later successful actor document is authoritative for the currently
	// declared inbox. Empty means the actor no longer declares one.
	if err := repository.RecordActorObservation(ctx, storage.ActorObservationIntent{
		RelayActor: actor, State: storage.ReachabilityReachable,
	}, time.Unix(130, 0)); err != nil {
		t.Fatal(err)
	}
	observation, _, err = repository.GetObservation(ctx, storage.IdentityIntent{RelayActor: actor})
	if err != nil || observation.InboxURL != "" || observation.InboxDeclaredUnix != nil ||
		observation.InboxProbeState != storage.InboxNotChecked || observation.InboxLastCheckedUnix != nil {
		t.Fatalf("observation after inbox clear = %#v, %v", observation, err)
	}

}

func TestObservationRejectsChangedInboxTargetAndTimeRegression(t *testing.T) {
	database := openMigratedTestDatabase(t)
	repository := newTestRelayRepository(t, database)
	ctx := context.Background()
	actor := "https://discovery.example/actor"
	inbox := "https://discovery.example/inbox"
	if _, err := repository.AddDiscovery(ctx, discoveryAdd(actor, "https://discovery.example"), time.Unix(100, 0)); err != nil {
		t.Fatal(err)
	}
	if err := repository.RecordActorObservation(ctx, storage.ActorObservationIntent{
		RelayActor: actor, State: storage.ReachabilityReachable, InboxURL: inbox,
	}, time.Unix(110, 0)); err != nil {
		t.Fatal(err)
	}
	if err := repository.RecordInboxObservation(ctx, storage.InboxObservationIntent{
		RelayActor: actor, InboxURL: "https://discovery.example/other", State: storage.InboxResponsive,
	}, time.Unix(111, 0)); !errors.Is(err, storage.ErrObservationConflict) {
		t.Fatalf("RecordInboxObservation(changed target) error = %v", err)
	}
	if err := repository.RecordInboxObservation(ctx, storage.InboxObservationIntent{
		RelayActor: actor, InboxURL: inbox, State: storage.InboxResponsive,
	}, time.Unix(109, 0)); !errors.Is(err, storage.ErrObservationTime) {
		t.Fatalf("RecordInboxObservation(before declaration) error = %v", err)
	}
	if err := repository.RecordActorObservation(ctx, storage.ActorObservationIntent{
		RelayActor: actor, State: storage.ReachabilityUnreachable,
	}, time.Unix(109, 0)); !errors.Is(err, storage.ErrObservationTime) {
		t.Fatalf("RecordActorObservation(regressing) error = %v", err)
	}
}

func TestLifecycleAcceptedRequestsAdvanceRFC9421EvidenceWithoutChangingHeartbeatSemantics(t *testing.T) {
	database := openMigratedTestDatabase(t)
	repository := newTestRelayRepository(t, database)
	ctx := context.Background()

	if outcome, err := repository.Register(ctx, storage.RegisterIntent{RelayActor: testRelayActor, PublicBaseURL: testPublicBase}, time.Unix(100, 0)); err != nil || outcome != v1.OutcomeCreated {
		t.Fatalf("Register() = (%q, %v)", outcome, err)
	}
	if outcome, err := repository.Heartbeat(ctx, storage.IdentityIntent{RelayActor: testRelayActor}, time.Unix(110, 0)); err != nil || outcome != v1.OutcomeRecorded {
		t.Fatalf("Heartbeat() = (%q, %v)", outcome, err)
	}
	if err := repository.RecordActorObservation(ctx, storage.ActorObservationIntent{
		RelayActor: testRelayActor, State: storage.ReachabilityReachable, InboxURL: "https://relay.example/inbox",
	}, time.Unix(120, 0)); err != nil {
		t.Fatal(err)
	}
	if outcome, err := repository.Unregister(ctx, storage.IdentityIntent{RelayActor: testRelayActor}, time.Unix(130, 0)); err != nil || outcome != v1.OutcomeRemoved {
		t.Fatalf("Unregister() = (%q, %v)", outcome, err)
	}

	relay := readTestRelay(t, database, testRelayActor)
	if relay.lastSeenAtUnix != 110 || !relay.lastHeartbeat.Valid || relay.lastHeartbeat.Int64 != 110 {
		t.Fatalf("lifecycle heartbeat semantics changed = %#v", relay)
	}
	observation, ok, err := repository.GetObservation(ctx, storage.IdentityIntent{RelayActor: testRelayActor})
	if err != nil || !ok || observation.RFC9421VerifiedUnix == nil || *observation.RFC9421VerifiedUnix != 130 ||
		observation.ActorLastSuccessUnix == nil || *observation.ActorLastSuccessUnix != 120 {
		t.Fatalf("lifecycle observation = %#v, %t, %v", observation, ok, err)
	}
}

func TestSignedUnregisterCanVerifyDiscoveredOnlyRelayWithoutFabricatingLifecycle(t *testing.T) {
	database := openMigratedTestDatabase(t)
	repository := newTestRelayRepository(t, database)
	ctx := context.Background()
	actor := "https://discovery.example/actor"
	if _, err := repository.AddDiscovery(ctx, discoveryAdd(actor, "https://discovery.example"), time.Unix(100, 0)); err != nil {
		t.Fatal(err)
	}
	before, ok, err := repository.GetObservation(
		ctx,
		storage.IdentityIntent{RelayActor: actor},
	)
	if err != nil || (ok && before.RFC9421VerifiedUnix != nil) {
		t.Fatalf(
			"pre-lifecycle RFC 9421 evidence = %#v, %t, %v",
			before,
			ok,
			err,
		)
	}

	outcome, err := repository.Unregister(ctx, storage.IdentityIntent{RelayActor: actor}, time.Unix(110, 0))
	if err != nil || outcome != v1.OutcomeAbsent {
		t.Fatalf("Unregister(discovered only) = (%q, %v)", outcome, err)
	}
	var relayCount int
	if err := database.QueryRow(`SELECT COUNT(*) FROM relays WHERE relay_actor=?`, actor).Scan(&relayCount); err != nil || relayCount != 0 {
		t.Fatalf("discovered unregister fabricated lifecycle count=%d err=%v", relayCount, err)
	}
	observation, ok, err := repository.GetObservation(ctx, storage.IdentityIntent{RelayActor: actor})
	if err != nil || !ok || observation.RFC9421VerifiedUnix == nil || *observation.RFC9421VerifiedUnix != 110 {
		t.Fatalf("discovered RFC 9421 evidence = %#v, %t, %v", observation, ok, err)
	}
}

func TestDiscoveryAuditAppendOnlyAndObservationRequiresRetainedIdentity(t *testing.T) {
	database := openMigratedTestDatabase(t)
	repository := newTestRelayRepository(t, database)
	ctx := context.Background()
	actor := "https://discovery.example/actor"
	if _, err := repository.AddDiscovery(ctx, discoveryAdd(actor, "https://discovery.example"), time.Unix(100, 0)); err != nil {
		t.Fatal(err)
	}
	var eventID int64
	if err := database.QueryRow(`SELECT discovery_event_id FROM discovery_events WHERE relay_actor=?`, actor).Scan(&eventID); err != nil {
		t.Fatal(err)
	}
	if _, err := database.Exec(`UPDATE discovery_events SET reason_code='changed' WHERE discovery_event_id=?`, eventID); err == nil {
		t.Fatal("discovery event update was accepted")
	}
	if _, err := database.Exec(`DELETE FROM discovery_events WHERE discovery_event_id=?`, eventID); err == nil {
		t.Fatal("discovery event deletion was accepted")
	}
	if _, err := database.Exec(`INSERT INTO relay_observations (relay_actor,actor_state,inbox_probe_state,updated_at_unix)
		VALUES ('https://orphan.example/actor','unknown','not_checked',1)`); err == nil {
		t.Fatal("orphan observation insert was accepted")
	}
}

func TestDecodeObservationRejectsInvalidRevision(t *testing.T) {
	_, err := decodeObservation(testRelayActor, &observationRecord{
		actorState:      "unknown",
		inboxProbeState: "not_checked",
		updatedUnix:     1,
		revision:        0,
	})
	if err == nil {
		t.Fatal("decodeObservation(revision 0) error = nil")
	}
}
