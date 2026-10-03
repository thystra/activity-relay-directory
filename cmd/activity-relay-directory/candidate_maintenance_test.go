package main

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/thystra/activity-relay-directory/internal/actorresolver"
	"github.com/thystra/activity-relay-directory/internal/candidatemaintenance"
	"github.com/thystra/activity-relay-directory/internal/storage"
)

func TestRunDiscoveryCandidateMaintenanceRunsAndStops(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	repository := &maintenanceCandidateRepository{candidate: storage.DiscoveryCandidateCheck{
		CandidateActorURL: "https://relay.example/actor",
		PublicBaseURL:     "https://relay.example",
		State:             storage.DiscoveryCandidateUnreachable,
		LastFailure:       storage.DiscoveryCandidateActorUnreachable,
		FirstSeenUnix:     1,
		LastCheckedUnix:   1,
		FailureCount:      1,
		NextCheckUnix:     2,
		OperatorID:        "operator",
		ReasonCode:        "public_list",
		SourceKind:        storage.DiscoverySourceFile,
		SourceLabel:       "curated_list",
	}}
	observed := time.Unix(100, 0).UTC()
	var mutex sync.Mutex
	now := func() time.Time {
		mutex.Lock()
		defer mutex.Unlock()
		return observed
	}
	results := make(chan candidatemaintenance.Result, 1)
	done := make(chan struct{})
	go func() {
		runDiscoveryCandidateMaintenance(
			ctx,
			repository,
			&maintenanceCandidateProber{},
			storage.DiscoveryCandidateMaintenanceInterval,
			now,
			func(result candidatemaintenance.Result) {
				results <- result
				cancel()
			},
			func(err error) { t.Errorf("unexpected maintenance error: %v", err) },
		)
		close(done)
	}()

	select {
	case result := <-results:
		if result.Scanned != 1 || result.Promoted != 1 {
			t.Fatalf("result = %#v", result)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("candidate maintenance did not run")
	}
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("candidate maintenance did not stop")
	}
}

type maintenanceCandidateRepository struct {
	candidate storage.DiscoveryCandidateCheck
	served    bool
}

func (repository *maintenanceCandidateRepository) DiscoveryCandidateChecks(
	context.Context,
	storage.DiscoveryCandidateCheckQuery,
) (storage.DiscoveryCandidateCheckPage, error) {
	if repository.served {
		return storage.DiscoveryCandidateCheckPage{}, nil
	}
	repository.served = true
	return storage.DiscoveryCandidateCheckPage{Candidates: []storage.DiscoveryCandidateCheck{repository.candidate}}, nil
}

func (*maintenanceCandidateRepository) RetainDiscoveryCandidate(
	context.Context,
	storage.DiscoveryCandidateIntent,
	time.Time,
) (storage.DiscoveryCandidateOutcome, error) {
	return storage.DiscoveryCandidateUpdated, nil
}

func (*maintenanceCandidateRepository) PromoteDiscoveryCandidate(
	context.Context,
	storage.DiscoveryCandidatePromotionIntent,
	time.Time,
) (storage.DiscoveryCandidatePromotionOutcome, error) {
	return storage.DiscoveryCandidatePromoted, nil
}

type maintenanceCandidateProber struct{}

func (*maintenanceCandidateProber) ProbeActor(
	_ context.Context,
	actor string,
) (actorresolver.ActorProbeResult, error) {
	return actorresolver.ActorProbeResult{ActorID: actor}, nil
}

func (*maintenanceCandidateProber) ProbeInbox(
	context.Context,
	string,
) (actorresolver.InboxProbeResult, error) {
	return actorresolver.InboxProbeResponsive, nil
}
