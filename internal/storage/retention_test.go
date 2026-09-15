package storage

import "testing"

func TestInactiveRetentionBoundsAreFixed(t *testing.T) {
	if MaximumInactiveRetentionDays != 36500 || MaximumPurgeCandidatePage != 100 ||
		MaximumPurgeAttemptsPerRun != 1000 || RetentionPolicyVersion != 2 {
		t.Fatalf(
			"retention bounds = days:%d page:%d run:%d policy:%d",
			MaximumInactiveRetentionDays,
			MaximumPurgeCandidatePage,
			MaximumPurgeAttemptsPerRun,
			RetentionPolicyVersion,
		)
	}
}

func TestPurgeCandidateCursorValidity(t *testing.T) {
	if !(PurgeCandidateCursor{}).Valid() {
		t.Fatal("zero cursor must be valid")
	}
	if !(PurgeCandidateCursor{InactiveUnix: 0, RelayActor: "https://relay.example/actor", Kind: PurgeCandidateLifecycle}).Valid() {
		t.Fatal("complete cursor must be valid")
	}
	for _, cursor := range []PurgeCandidateCursor{
		{InactiveUnix: -1, RelayActor: "https://relay.example/actor", Kind: PurgeCandidateLifecycle},
		{InactiveUnix: 1, Kind: PurgeCandidateLifecycle},
		{InactiveUnix: 1, RelayActor: "https://relay.example/actor"},
		{InactiveUnix: 1, RelayActor: "https://relay.example/actor", Kind: PurgeCandidateKind("other")},
	} {
		if cursor.Valid() {
			t.Fatalf("cursor %#v unexpectedly valid", cursor)
		}
	}
}

func TestPurgeCandidateRequiresReviewedInactiveState(t *testing.T) {
	for _, state := range []RelayLifecycleState{LifecycleUnregistered, LifecyclePruned} {
		candidate := PurgeCandidate{
			Kind: PurgeCandidateLifecycle, RelayActor: "https://relay.example/actor",
			LifecycleState: state, InactiveUnix: 1, UpdatedUnix: 1, ObservationRevision: 0,
		}
		if !candidate.Valid() {
			t.Fatalf("candidate %#v invalid", candidate)
		}
	}
	if (PurgeCandidate{
		Kind: PurgeCandidateLifecycle, RelayActor: "https://relay.example/actor",
		LifecycleState: LifecycleRegistered, InactiveUnix: 1, UpdatedUnix: 1, ObservationRevision: 0,
	}).Valid() {
		t.Fatal("registered relay must not be a purge candidate")
	}
	discovery := PurgeCandidate{
		Kind: PurgeCandidateDiscovery, RelayActor: "https://relay.example/actor",
		InactiveUnix: 1, UpdatedUnix: 1, LatestDiscoveryEventID: 1, ObservationRevision: 0,
	}
	if !discovery.Valid() {
		t.Fatalf("discovery candidate %#v invalid", discovery)
	}
}

func TestRetentionOutcomeVocabulary(t *testing.T) {
	for _, outcome := range []RetentionOutcome{RetentionCompleted, RetentionCanceled, RetentionFailed} {
		if !outcome.Valid() {
			t.Fatalf("outcome %q invalid", outcome)
		}
	}
	if RetentionOutcome("unknown").Valid() {
		t.Fatal("unknown outcome unexpectedly valid")
	}
}
