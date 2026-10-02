package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"testing"
	"time"

	"github.com/thystra/activity-relay-directory/internal/storage"
)

func TestDiscoveryCandidateRetainUpdatesStateAndPreservesFirstSeen(t *testing.T) {
	database := openMigratedTestDatabase(t)
	repository := newTestRelayRepository(t, database)
	ctx := context.Background()
	actor := "https://dead.example/actor"

	first := storage.DiscoveryCandidateIntent{
		CandidateActorURL: actor,
		PublicBaseURL:     "https://dead.example",
		State:             storage.DiscoveryCandidateUnreachable,
		Failure:           storage.DiscoveryCandidateActorUnreachable,
		OperatorID:        "operator",
		ReasonCode:        "public_list",
		SourceKind:        storage.DiscoverySourceFile,
		SourceLabel:       "curated_list",
	}
	outcome, err := repository.RetainDiscoveryCandidate(ctx, first, time.Unix(100, 0))
	if err != nil || outcome != storage.DiscoveryCandidateAdded {
		t.Fatalf("RetainDiscoveryCandidate(first) = (%q, %v)", outcome, err)
	}

	second := first
	second.State = storage.DiscoveryCandidateIncompatible
	second.Failure = storage.DiscoveryCandidateActorInvalid
	outcome, err = repository.RetainDiscoveryCandidate(ctx, second, time.Unix(110, 0))
	if err != nil || outcome != storage.DiscoveryCandidateUpdated {
		t.Fatalf("RetainDiscoveryCandidate(second) = (%q, %v)", outcome, err)
	}

	record, found, err := repository.GetDiscoveryCandidate(ctx, actor)
	if err != nil || !found {
		t.Fatalf("GetDiscoveryCandidate() = %#v, %t, %v", record, found, err)
	}
	if record.State != storage.DiscoveryCandidateIncompatible ||
		record.LastFailure != storage.DiscoveryCandidateActorInvalid ||
		record.FirstSeenUnix != 100 ||
		record.LastCheckedUnix != 110 ||
		record.LastSuccessUnix != nil ||
		record.FailureCount != 2 ||
		record.UpdatedUnix != 110 {
		t.Fatalf("candidate record = %#v", record)
	}

	rows, err := database.Query(`SELECT action, candidate_state, failure_code,
		operator_id, reason_code, source_kind, source_label, recorded_at_unix
		FROM discovery_candidate_events
		WHERE candidate_actor_url = ?
		ORDER BY discovery_candidate_event_id`, actor)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var actions []string
	for rows.Next() {
		var action, state, failure, operatorID, reason, sourceKind string
		var label sql.NullString
		var recorded int64
		if err := rows.Scan(
			&action, &state, &failure, &operatorID, &reason,
			&sourceKind, &label, &recorded,
		); err != nil {
			t.Fatal(err)
		}
		if operatorID != "operator" ||
			reason != "public_list" ||
			sourceKind != "file" ||
			!label.Valid ||
			label.String != "curated_list" ||
			recorded < 100 ||
			state == "" ||
			failure == "" {
			t.Fatalf("invalid candidate event: action=%s state=%s failure=%s operator=%s reason=%s source=%s label=%v recorded=%d",
				action, state, failure, operatorID, reason, sourceKind, label, recorded)
		}
		actions = append(actions, action)
	}
	if !equalStrings(actions, []string{discoveryCandidateAdded, discoveryCandidateUpdated}) {
		t.Fatalf("candidate actions = %#v", actions)
	}
}

func TestDiscoveryCandidateRejectsInvalidAndRegressingInput(t *testing.T) {
	database := openMigratedTestDatabase(t)
	repository := newTestRelayRepository(t, database)
	ctx := context.Background()
	intent := storage.DiscoveryCandidateIntent{
		CandidateActorURL: "https://dead.example/actor",
		PublicBaseURL:     "https://dead.example",
		State:             storage.DiscoveryCandidateUnreachable,
		Failure:           storage.DiscoveryCandidateActorUnreachable,
		OperatorID:        "operator",
		ReasonCode:        "public_list",
		SourceKind:        storage.DiscoverySourceFile,
		SourceLabel:       "curated_list",
	}
	if _, err := repository.RetainDiscoveryCandidate(ctx, intent, time.Unix(100, 0)); err != nil {
		t.Fatal(err)
	}
	if _, err := repository.RetainDiscoveryCandidate(ctx, intent, time.Unix(99, 0)); !errors.Is(err, storage.ErrDiscoveryCandidateTime) {
		t.Fatalf("RetainDiscoveryCandidate(regressing) error = %v", err)
	}

	bad := intent
	bad.CandidateActorURL = "http://dead.example/actor"
	if _, err := repository.RetainDiscoveryCandidate(ctx, bad, time.Unix(101, 0)); !errors.Is(err, storage.ErrDiscoveryCandidateInput) {
		t.Fatalf("RetainDiscoveryCandidate(http) error = %v", err)
	}

	bad = intent
	bad.State = storage.DiscoveryCandidateIncompatible
	if _, err := repository.RetainDiscoveryCandidate(ctx, bad, time.Unix(101, 0)); !errors.Is(err, storage.ErrDiscoveryCandidateInput) {
		t.Fatalf("RetainDiscoveryCandidate(mismatched state) error = %v", err)
	}
}
