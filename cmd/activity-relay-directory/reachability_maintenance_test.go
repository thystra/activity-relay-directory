package main

import (
	"context"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/thystra/activity-relay-directory/internal/actorresolver"
	"github.com/thystra/activity-relay-directory/internal/reachability"
	"github.com/thystra/activity-relay-directory/internal/storage"
)

func TestReachabilityCoverageGateUsesCapturedObservationAndExpires(t *testing.T) {
	gate := &reachabilityCoverageGate{}
	observed := time.Unix(1_000_000, 0)
	completed := observed.Add(time.Minute)
	if !gate.RecordComplete(observed, completed) {
		t.Fatal("RecordComplete() = false")
	}
	got, ok := gate.ObservationForPruning(completed.Add(5 * time.Hour))
	if !ok || !got.Equal(observed) {
		t.Fatalf("covered observation = (%s, %t)", got, ok)
	}
	if _, ok := gate.ObservationForPruning(observed.Add(storage.ReachabilityFreshness + time.Second)); ok {
		t.Fatal("expired coverage remained usable")
	}
	if _, ok := gate.ObservationForPruning(completed.Add(-time.Second)); ok {
		t.Fatal("clock-regressed coverage remained usable")
	}
}

func TestRunReachabilityMaintenanceRecordsOnlyCompleteCoverage(t *testing.T) {
	for _, count := range []int{0, storage.MaximumReachabilityAttemptsPerRun + 1} {
		t.Run(fmt.Sprintf("candidates-%d", count), func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			repository := &maintenanceReachabilityRepository{}
			for index := 0; index < count; index++ {
				repository.candidates = append(repository.candidates, storage.ReachabilityCandidate{
					RelayActor: fmt.Sprintf("https://relay-%04d.example/actor", index),
				})
			}
			gate := &reachabilityCoverageGate{}
			observed := time.Unix(2_000_000, 0)
			times := []time.Time{observed, observed.Add(time.Second)}
			var timeMutex sync.Mutex
			now := func() time.Time {
				timeMutex.Lock()
				defer timeMutex.Unlock()
				if len(times) == 0 {
					return observed.Add(time.Second)
				}
				value := times[0]
				times = times[1:]
				return value
			}
			done := make(chan struct{})
			results := make(chan reachability.Result, 1)
			go func() {
				runReachabilityMaintenance(
					ctx,
					repository,
					&maintenanceReachabilityProber{},
					storage.ReachabilityMaintenanceInterval,
					now,
					gate,
					func(result reachability.Result) {
						results <- result
						cancel()
					},
					func(err error) { t.Errorf("unexpected maintenance error: %v", err) },
				)
				close(done)
			}()
			var result reachability.Result
			select {
			case result = <-results:
			case <-time.After(2 * time.Second):
				t.Fatal("reachability maintenance did not run")
			}
			_, covered := gate.ObservationForPruning(observed.Add(time.Second))
			if result.Truncated == covered {
				t.Fatalf("truncated=%t covered=%t", result.Truncated, covered)
			}
			select {
			case <-done:
			case <-time.After(time.Second):
				t.Fatal("reachability maintenance did not stop")
			}
		})
	}
}

type maintenanceReachabilityRepository struct {
	candidates []storage.ReachabilityCandidate
}

func (repository *maintenanceReachabilityRepository) ReachabilityCandidates(
	_ context.Context,
	query storage.ReachabilityCandidateQuery,
) (storage.ReachabilityCandidatePage, error) {
	start := 0
	if query.After != (storage.ReachabilityCandidateCursor{}) {
		for index, candidate := range repository.candidates {
			if candidate.RelayActor == query.After.RelayActor {
				start = index + 1
				break
			}
		}
	}
	if start >= len(repository.candidates) {
		return storage.ReachabilityCandidatePage{}, nil
	}
	end := start + query.Limit
	if end > len(repository.candidates) {
		end = len(repository.candidates)
	}
	page := storage.ReachabilityCandidatePage{Candidates: append([]storage.ReachabilityCandidate(nil), repository.candidates[start:end]...)}
	if end < len(repository.candidates) {
		page.Next = storage.ReachabilityCandidateCursor{RelayActor: page.Candidates[len(page.Candidates)-1].RelayActor}
	}
	return page, nil
}

func (*maintenanceReachabilityRepository) RecordReachabilityObservation(
	context.Context,
	storage.ReachabilityObservationIntent,
	time.Time,
) (storage.ReachabilityWriteOutcome, error) {
	return storage.ReachabilityWriteApplied, nil
}

type maintenanceReachabilityProber struct{}

func (*maintenanceReachabilityProber) ProbeActor(_ context.Context, actor string) (actorresolver.ActorProbeResult, error) {
	return actorresolver.ActorProbeResult{ActorID: actor}, nil
}
func (*maintenanceReachabilityProber) ProbeInbox(context.Context, string) (actorresolver.InboxProbeResult, error) {
	return actorresolver.InboxProbeResponsive, nil
}
