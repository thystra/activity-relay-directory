package storage

import (
	"testing"
	"time"
)

func TestPruneOutcomeAndCursorValidation(t *testing.T) {
	for _, outcome := range []PruneOutcome{
		PruneApplied,
		PruneAlreadyPruned,
		PruneNotEligible,
	} {
		if !outcome.Valid() {
			t.Fatalf("outcome %q is invalid", outcome)
		}
	}
	if PruneOutcome("unknown").Valid() {
		t.Fatal("unknown prune outcome is valid")
	}

	if !(PruneCandidateCursor{}).Valid() {
		t.Fatal("zero cursor is invalid")
	}
	if !(PruneCandidateCursor{
		LastSeenUnix: 0,
		RelayActor:   "https://relay.example/actor",
	}).Valid() {
		t.Fatal("complete cursor is invalid")
	}
	for _, cursor := range []PruneCandidateCursor{
		{LastSeenUnix: -1, RelayActor: "https://relay.example/actor"},
		{LastSeenUnix: 1},
	} {
		if cursor.Valid() {
			t.Fatalf("cursor %#v is valid", cursor)
		}
	}
}

func TestSoftPruningBoundsAreFixedAndConservative(t *testing.T) {
	if MaximumPruneCandidatePage != 100 || MaximumSoftPruneAttemptsPerRun != 1000 {
		t.Fatalf(
			"pruning bounds = page:%d run:%d",
			MaximumPruneCandidatePage,
			MaximumSoftPruneAttemptsPerRun,
		)
	}
	if MinimumSoftPruningInterval != time.Hour ||
		DefaultSoftPruningInterval != 24*time.Hour {
		t.Fatalf(
			"pruning intervals = minimum:%s default:%s",
			MinimumSoftPruningInterval,
			DefaultSoftPruningInterval,
		)
	}
}
