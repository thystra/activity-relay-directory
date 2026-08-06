package pruning

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/thystra/activity-relay-directory/internal/storage"
)

func TestRunIsBoundedKeysetAndReportsTruncation(t *testing.T) {
	candidates := make([]storage.PruneCandidate, storage.MaximumSoftPruneAttemptsPerRun+1)
	for index := range candidates {
		candidates[index] = storage.PruneCandidate{
			RelayActor:          fmt.Sprintf("https://relay-%04d.example/actor", index),
			PublicBaseURL:       fmt.Sprintf("https://relay-%04d.example/", index),
			AdministrativeState: storage.AdministrativeActive,
			LastSeenUnix:        int64(index + 1),
		}
	}
	repository := &fakePruningRepository{candidates: candidates}
	observed := time.Unix(4_000_000, 0)

	result, err := Run(context.Background(), repository, observed)
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if result.ObservedUnix != observed.Unix() ||
		result.Scanned != storage.MaximumSoftPruneAttemptsPerRun ||
		result.Pruned != storage.MaximumSoftPruneAttemptsPerRun ||
		result.Skipped != 0 || !result.Truncated {
		t.Fatalf("Run() result = %#v", result)
	}
	if repository.maximumPage > storage.MaximumPruneCandidatePage ||
		len(repository.prunedActors) != storage.MaximumSoftPruneAttemptsPerRun {
		t.Fatalf(
			"repository calls = max_page:%d pruned:%d",
			repository.maximumPage,
			len(repository.prunedActors),
		)
	}
}

func TestRunExactAttemptBudgetWithoutLookaheadIsNotTruncated(t *testing.T) {
	candidates := make([]storage.PruneCandidate, storage.MaximumSoftPruneAttemptsPerRun)
	for index := range candidates {
		candidates[index] = storage.PruneCandidate{
			RelayActor:          fmt.Sprintf("https://exact-%04d.example/actor", index),
			PublicBaseURL:       fmt.Sprintf("https://exact-%04d.example/", index),
			AdministrativeState: storage.AdministrativeActive,
			LastSeenUnix:        int64(index + 1),
		}
	}
	result, err := Run(
		context.Background(),
		&fakePruningRepository{candidates: candidates},
		time.Unix(4_000_000, 0),
	)
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if result.Scanned != storage.MaximumSoftPruneAttemptsPerRun ||
		result.Pruned != storage.MaximumSoftPruneAttemptsPerRun || result.Truncated {
		t.Fatalf("Run() result = %#v", result)
	}
}

func TestRunCountsTransactionalRaceSkips(t *testing.T) {
	repository := &fakePruningRepository{
		candidates: []storage.PruneCandidate{
			{RelayActor: "https://one.example/actor", AdministrativeState: storage.AdministrativeActive, LastSeenUnix: 1},
			{RelayActor: "https://two.example/actor", AdministrativeState: storage.AdministrativeActive, LastSeenUnix: 2},
			{RelayActor: "https://three.example/actor", AdministrativeState: storage.AdministrativeActive, LastSeenUnix: 3},
		},
		outcomes: map[string]storage.PruneOutcome{
			"https://one.example/actor":   storage.PruneApplied,
			"https://two.example/actor":   storage.PruneNotEligible,
			"https://three.example/actor": storage.PruneAlreadyPruned,
		},
	}
	result, err := Run(context.Background(), repository, time.Unix(4_000_000, 0))
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if result.Scanned != 3 || result.Pruned != 1 || result.Skipped != 2 ||
		result.Truncated {
		t.Fatalf("Run() result = %#v", result)
	}
}

func TestRunHonorsCancellationBetweenTransitions(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	repository := &fakePruningRepository{
		candidates: []storage.PruneCandidate{
			{RelayActor: "https://one.example/actor", AdministrativeState: storage.AdministrativeActive, LastSeenUnix: 1},
			{RelayActor: "https://two.example/actor", AdministrativeState: storage.AdministrativeActive, LastSeenUnix: 2},
		},
		cancel: cancel,
	}
	result, err := Run(ctx, repository, time.Unix(4_000_000, 0))
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("Run() error = %v, want context.Canceled", err)
	}
	if result.Scanned != 1 || result.Pruned != 1 {
		t.Fatalf("Run() partial result = %#v", result)
	}
}

func TestRunRejectsInvalidConfigurationAndRepositoryOutcome(t *testing.T) {
	if _, err := Run(nil, &fakePruningRepository{}, time.Now()); !errors.Is(err, ErrConfiguration) {
		t.Fatalf("Run(nil) error = %v", err)
	}
	if _, err := Run(context.Background(), nil, time.Now()); !errors.Is(err, ErrConfiguration) {
		t.Fatalf("Run(nil repository) error = %v", err)
	}
	repository := &fakePruningRepository{
		candidates: []storage.PruneCandidate{{
			RelayActor:          "https://relay.example/actor",
			AdministrativeState: storage.AdministrativeActive,
			LastSeenUnix:        1,
		}},
		outcomes: map[string]storage.PruneOutcome{
			"https://relay.example/actor": "invalid",
		},
	}
	if _, err := Run(context.Background(), repository, time.Unix(4_000_000, 0)); !errors.Is(err, ErrConfiguration) {
		t.Fatalf("Run(invalid outcome) error = %v", err)
	}
}

func TestRunRejectsOversizedNonadvancingAndInconsistentPages(t *testing.T) {
	validCandidate := storage.PruneCandidate{
		RelayActor:          "https://relay.example/actor",
		PublicBaseURL:       "https://relay.example/",
		AdministrativeState: storage.AdministrativeActive,
		LastSeenUnix:        1,
	}
	for name, page := range map[string]storage.PruneCandidatePage{
		"oversized": {
			Candidates: make([]storage.PruneCandidate, storage.MaximumPruneCandidatePage+1),
		},
		"nonadvancing": {
			Candidates: []storage.PruneCandidate{validCandidate, validCandidate},
		},
		"bad administrative state": {
			Candidates: []storage.PruneCandidate{{
				RelayActor:          "https://relay.example/actor",
				PublicBaseURL:       "https://relay.example/",
				AdministrativeState: "invalid",
				LastSeenUnix:        1,
			}},
		},
		"inconsistent next": {
			Candidates: []storage.PruneCandidate{validCandidate},
			Next: storage.PruneCandidateCursor{
				LastSeenUnix: 2,
				RelayActor:   "https://other.example/actor",
			},
		},
	} {
		t.Run(name, func(t *testing.T) {
			repository := &fixedPagePruningRepository{page: page}
			if _, err := Run(context.Background(), repository, time.Unix(4_000_000, 0)); !errors.Is(err, ErrConfiguration) {
				t.Fatalf("Run() error = %v", err)
			}
		})
	}
}

type fakePruningRepository struct {
	candidates    []storage.PruneCandidate
	outcomes      map[string]storage.PruneOutcome
	prunedActors  []string
	maximumPage   int
	cancel        context.CancelFunc
	cancelInvoked bool
}

func (repository *fakePruningRepository) PruneCandidates(
	_ context.Context,
	query storage.PruneCandidateQuery,
) (storage.PruneCandidatePage, error) {
	if query.Limit > repository.maximumPage {
		repository.maximumPage = query.Limit
	}
	start := 0
	if query.After != (storage.PruneCandidateCursor{}) {
		for index, candidate := range repository.candidates {
			if candidate.LastSeenUnix == query.After.LastSeenUnix &&
				candidate.RelayActor == query.After.RelayActor {
				start = index + 1
				break
			}
		}
	}
	if start >= len(repository.candidates) {
		return storage.PruneCandidatePage{}, nil
	}
	end := start + query.Limit
	if end > len(repository.candidates) {
		end = len(repository.candidates)
	}
	page := storage.PruneCandidatePage{
		Candidates: append([]storage.PruneCandidate(nil), repository.candidates[start:end]...),
	}
	if end < len(repository.candidates) {
		last := page.Candidates[len(page.Candidates)-1]
		page.Next = storage.PruneCandidateCursor{
			LastSeenUnix: last.LastSeenUnix,
			RelayActor:   last.RelayActor,
		}
	}
	return page, nil
}

func (repository *fakePruningRepository) SoftPrune(
	_ context.Context,
	intent storage.IdentityIntent,
	_ time.Time,
) (storage.PruneOutcome, error) {
	repository.prunedActors = append(repository.prunedActors, intent.RelayActor)
	outcome := storage.PruneApplied
	if configured, exists := repository.outcomes[intent.RelayActor]; exists {
		outcome = configured
	}
	if repository.cancel != nil && !repository.cancelInvoked {
		repository.cancelInvoked = true
		repository.cancel()
	}
	return outcome, nil
}

type fixedPagePruningRepository struct {
	page storage.PruneCandidatePage
}

func (repository *fixedPagePruningRepository) PruneCandidates(
	context.Context,
	storage.PruneCandidateQuery,
) (storage.PruneCandidatePage, error) {
	return repository.page, nil
}

func (*fixedPagePruningRepository) SoftPrune(
	context.Context,
	storage.IdentityIntent,
	time.Time,
) (storage.PruneOutcome, error) {
	return storage.PruneApplied, nil
}
