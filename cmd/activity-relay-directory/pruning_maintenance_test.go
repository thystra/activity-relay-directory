package main

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/thystra/activity-relay-directory/internal/pruning"
	storageContract "github.com/thystra/activity-relay-directory/internal/storage"
)

func TestRunSoftPruningMaintenanceRunsImmediatelyAndStops(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	repository := &maintenancePruningRepository{queries: make(chan storageContract.PruneCandidateQuery, 1)}
	results := make(chan pruning.Result, 1)
	errorsSeen := make(chan error, 1)
	done := make(chan struct{})
	observed := time.Unix(4_000_000, 0)

	go func() {
		runSoftPruningMaintenance(
			ctx,
			repository,
			time.Hour,
			time.Hour,
			func() time.Time { return observed },
			func(result pruning.Result) {
				results <- result
				cancel()
			},
			func(err error) { errorsSeen <- err },
		)
		close(done)
	}()

	select {
	case query := <-repository.queries:
		if !query.ObservedAt.Equal(observed) || query.Limit != storageContract.MaximumPruneCandidatePage {
			t.Fatalf("query = %#v", query)
		}
	case <-time.After(time.Second):
		t.Fatal("soft-pruning maintenance did not run immediately")
	}
	select {
	case result := <-results:
		if result.ObservedUnix != observed.Unix() || result.Scanned != 0 {
			t.Fatalf("result = %#v", result)
		}
	case <-time.After(time.Second):
		t.Fatal("soft-pruning result was not reported")
	}
	select {
	case err := <-errorsSeen:
		t.Fatalf("unexpected maintenance error: %v", err)
	default:
	}
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("soft-pruning maintenance did not stop")
	}
}

func TestRunSoftPruningMaintenanceRejectsTooFrequentOrIncompleteConfiguration(t *testing.T) {
	for name, test := range map[string]struct {
		repository storageContract.PruningRepository
		interval   time.Duration
		minimum    time.Duration
		now        func() time.Time
	}{
		"missing repository": {interval: time.Hour, minimum: time.Hour, now: time.Now},
		"missing clock":      {repository: &maintenancePruningRepository{}, interval: time.Hour, minimum: time.Hour},
		"missing minimum":    {repository: &maintenancePruningRepository{}, interval: time.Hour, now: time.Now},
		"below minimum":      {repository: &maintenancePruningRepository{}, interval: time.Hour - time.Second, minimum: time.Hour, now: time.Now},
	} {
		t.Run(name, func(t *testing.T) {
			errorsSeen := make(chan error, 1)
			runSoftPruningMaintenance(
				context.Background(),
				test.repository,
				test.interval,
				test.minimum,
				test.now,
				nil,
				func(err error) { errorsSeen <- err },
			)
			select {
			case err := <-errorsSeen:
				if err == nil {
					t.Fatal("configuration error = nil")
				}
			default:
				t.Fatal("configuration error was not reported")
			}
		})
	}
}

func TestRunSoftPruningMaintenanceReportsRunErrorThenWaitsForCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	repository := &maintenancePruningRepository{
		queries: make(chan storageContract.PruneCandidateQuery, 1),
		err:     errors.New("private pruning failure"),
	}
	errorsSeen := make(chan error, 1)
	done := make(chan struct{})
	go func() {
		runSoftPruningMaintenance(
			ctx,
			repository,
			time.Hour,
			time.Hour,
			time.Now,
			nil,
			func(err error) {
				errorsSeen <- err
				cancel()
			},
		)
		close(done)
	}()

	select {
	case err := <-errorsSeen:
		if err == nil || err.Error() != "private pruning failure" {
			t.Fatalf("run error = %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("run error was not reported")
	}
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("maintenance did not stop after cancellation")
	}
}

type maintenancePruningRepository struct {
	queries chan storageContract.PruneCandidateQuery
	err     error
}

func (repository *maintenancePruningRepository) PruneCandidates(
	_ context.Context,
	query storageContract.PruneCandidateQuery,
) (storageContract.PruneCandidatePage, error) {
	if repository.queries != nil {
		repository.queries <- query
	}
	return storageContract.PruneCandidatePage{}, repository.err
}

func (*maintenancePruningRepository) SoftPrune(
	context.Context,
	storageContract.IdentityIntent,
	time.Time,
) (storageContract.PruneOutcome, error) {
	return storageContract.PruneApplied, nil
}
