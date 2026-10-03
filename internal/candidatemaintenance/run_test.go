package candidatemaintenance

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/thystra/activity-relay-directory/internal/actorresolver"
	"github.com/thystra/activity-relay-directory/internal/storage"
)

func TestRunPromotesReachableAndRetainsFailures(t *testing.T) {
	observed := time.Unix(1_000_000, 0).UTC()
	repository := &fakeCandidateRepository{candidates: []storage.DiscoveryCandidateCheck{
		candidateCheck("https://a.example/actor", observed.Unix()-30),
		candidateCheck("https://b.example/actor", observed.Unix()-20),
		candidateCheck("https://c.example/actor", observed.Unix()-10),
	}}
	prober := &fakeCandidateProber{
		actorErrors: map[string]error{
			"https://a.example/actor": actorresolver.ErrActorFetch,
			"https://b.example/actor": actorresolver.ErrActorDocument,
		},
		inboxes: map[string]string{
			"https://c.example/actor": "https://c.example/inbox",
		},
		inboxResults: map[string]actorresolver.InboxProbeResult{
			"https://c.example/inbox": actorresolver.InboxProbeMethodRejected,
		},
	}

	result, err := Run(context.Background(), repository, prober, observed)
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if result.Scanned != 3 || result.Promoted != 1 || result.Unreachable != 1 ||
		result.Incompatible != 1 || result.Skipped != 0 || result.Truncated {
		t.Fatalf("result = %#v", result)
	}
	if len(repository.retained) != 2 ||
		repository.retained[0].State != storage.DiscoveryCandidateUnreachable ||
		repository.retained[0].Failure != storage.DiscoveryCandidateActorUnreachable ||
		repository.retained[1].State != storage.DiscoveryCandidateIncompatible ||
		repository.retained[1].Failure != storage.DiscoveryCandidateActorInvalid {
		t.Fatalf("retained intents = %#v", repository.retained)
	}
	if len(repository.promoted) != 1 ||
		repository.promoted[0].CandidateActorURL != "https://c.example/actor" ||
		repository.promoted[0].InboxState != storage.InboxMethodRejected ||
		repository.promoted[0].OperatorID != "operator" ||
		repository.promoted[0].SourceLabel != "curated_list" {
		t.Fatalf("promotion intents = %#v", repository.promoted)
	}
}

func TestRunTreatsActorIdentityMismatchAsIncompatible(t *testing.T) {
	observed := time.Unix(1_000_000, 0).UTC()
	repository := &fakeCandidateRepository{candidates: []storage.DiscoveryCandidateCheck{
		candidateCheck("https://relay.example/actor", observed.Unix()-1),
	}}
	prober := &fakeCandidateProber{wrongActor: true}
	result, err := Run(context.Background(), repository, prober, observed)
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if result.Incompatible != 1 || len(repository.retained) != 1 ||
		repository.retained[0].Failure != storage.DiscoveryCandidateActorInvalid {
		t.Fatalf("result = %#v retained=%#v", result, repository.retained)
	}
}

func TestRunIsBoundedAndConcurrent(t *testing.T) {
	observed := time.Unix(2_000_000, 0).UTC()
	candidates := make([]storage.DiscoveryCandidateCheck, storage.MaximumDiscoveryCandidateAttempts+1)
	for index := range candidates {
		candidates[index] = candidateCheck(
			fmt.Sprintf("https://relay-%04d.example/actor", index),
			observed.Unix()-int64(len(candidates)-index),
		)
	}
	repository := &fakeCandidateRepository{candidates: candidates}
	prober := &fakeCandidateProber{delay: 2 * time.Millisecond}
	result, err := Run(context.Background(), repository, prober, observed)
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if result.Scanned != storage.MaximumDiscoveryCandidateAttempts ||
		result.Promoted != storage.MaximumDiscoveryCandidateAttempts || !result.Truncated {
		t.Fatalf("result = %#v", result)
	}
	if repository.maximumPage > storage.MaximumDiscoveryCandidatePage {
		t.Fatalf("maximum page = %d", repository.maximumPage)
	}
	if got := prober.maximum.Load(); got > storage.MaximumConcurrentCandidateProbes || got < 2 {
		t.Fatalf("maximum concurrency = %d", got)
	}
}

func TestRunRejectsInvalidPageAndHonorsCancellation(t *testing.T) {
	observed := time.Unix(1_000_000, 0).UTC()
	invalid := &fixedCandidateRepository{page: storage.DiscoveryCandidateCheckPage{
		Candidates: []storage.DiscoveryCandidateCheck{
			candidateCheck("https://b.example/actor", observed.Unix()-10),
			candidateCheck("https://a.example/actor", observed.Unix()-20),
		},
	}}
	if _, err := Run(context.Background(), invalid, &fakeCandidateProber{}, observed); !errors.Is(err, ErrConfiguration) {
		t.Fatalf("invalid page Run() error = %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	repository := &fakeCandidateRepository{candidates: []storage.DiscoveryCandidateCheck{
		candidateCheck("https://a.example/actor", observed.Unix()-1),
	}}
	prober := &fakeCandidateProber{cancel: cancel}
	if _, err := Run(ctx, repository, prober, observed); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled Run() error = %v", err)
	}
}

func candidateCheck(actor string, next int64) storage.DiscoveryCandidateCheck {
	return storage.DiscoveryCandidateCheck{
		CandidateActorURL: actor,
		PublicBaseURL:     actor[:len(actor)-len("/actor")],
		State:             storage.DiscoveryCandidateUnreachable,
		LastFailure:       storage.DiscoveryCandidateActorUnreachable,
		FirstSeenUnix:     1,
		LastCheckedUnix:   1,
		FailureCount:      1,
		NextCheckUnix:     next,
		OperatorID:        "operator",
		ReasonCode:        "public_list",
		SourceKind:        storage.DiscoverySourceFile,
		SourceLabel:       "curated_list",
	}
}

type fakeCandidateRepository struct {
	candidates  []storage.DiscoveryCandidateCheck
	retained    []storage.DiscoveryCandidateIntent
	promoted    []storage.DiscoveryCandidatePromotionIntent
	promoteSkip map[string]bool
	maximumPage int
	mutex       sync.Mutex
}

func (repository *fakeCandidateRepository) DiscoveryCandidateChecks(
	_ context.Context,
	query storage.DiscoveryCandidateCheckQuery,
) (storage.DiscoveryCandidateCheckPage, error) {
	if query.Limit > repository.maximumPage {
		repository.maximumPage = query.Limit
	}
	start := 0
	if query.After != (storage.DiscoveryCandidateCheckCursor{}) {
		for index, candidate := range repository.candidates {
			cursor := storage.DiscoveryCandidateCheckCursor{
				NextCheckUnix: candidate.NextCheckUnix,
				CandidateURL:  candidate.CandidateActorURL,
			}
			if cursor == query.After {
				start = index + 1
				break
			}
		}
	}
	if start >= len(repository.candidates) {
		return storage.DiscoveryCandidateCheckPage{}, nil
	}
	end := start + query.Limit
	if end > len(repository.candidates) {
		end = len(repository.candidates)
	}
	page := storage.DiscoveryCandidateCheckPage{
		Candidates: append([]storage.DiscoveryCandidateCheck(nil), repository.candidates[start:end]...),
	}
	if end < len(repository.candidates) {
		last := page.Candidates[len(page.Candidates)-1]
		page.Next = storage.DiscoveryCandidateCheckCursor{
			NextCheckUnix: last.NextCheckUnix,
			CandidateURL:  last.CandidateActorURL,
		}
	}
	return page, nil
}

func (repository *fakeCandidateRepository) RetainDiscoveryCandidate(
	_ context.Context,
	intent storage.DiscoveryCandidateIntent,
	_ time.Time,
) (storage.DiscoveryCandidateOutcome, error) {
	repository.mutex.Lock()
	defer repository.mutex.Unlock()
	repository.retained = append(repository.retained, intent)
	return storage.DiscoveryCandidateUpdated, nil
}

func (repository *fakeCandidateRepository) PromoteDiscoveryCandidate(
	_ context.Context,
	intent storage.DiscoveryCandidatePromotionIntent,
	_ time.Time,
) (storage.DiscoveryCandidatePromotionOutcome, error) {
	repository.mutex.Lock()
	defer repository.mutex.Unlock()
	repository.promoted = append(repository.promoted, intent)
	if repository.promoteSkip[intent.CandidateActorURL] {
		return storage.DiscoveryCandidateSkipped, nil
	}
	return storage.DiscoveryCandidatePromoted, nil
}

type fixedCandidateRepository struct {
	page storage.DiscoveryCandidateCheckPage
}

func (repository *fixedCandidateRepository) DiscoveryCandidateChecks(
	context.Context,
	storage.DiscoveryCandidateCheckQuery,
) (storage.DiscoveryCandidateCheckPage, error) {
	return repository.page, nil
}
func (*fixedCandidateRepository) RetainDiscoveryCandidate(
	context.Context,
	storage.DiscoveryCandidateIntent,
	time.Time,
) (storage.DiscoveryCandidateOutcome, error) {
	return storage.DiscoveryCandidateUpdated, nil
}
func (*fixedCandidateRepository) PromoteDiscoveryCandidate(
	context.Context,
	storage.DiscoveryCandidatePromotionIntent,
	time.Time,
) (storage.DiscoveryCandidatePromotionOutcome, error) {
	return storage.DiscoveryCandidatePromoted, nil
}

type fakeCandidateProber struct {
	actorErrors  map[string]error
	inboxes      map[string]string
	inboxResults map[string]actorresolver.InboxProbeResult
	wrongActor   bool
	cancel       context.CancelFunc
	delay        time.Duration
	current      atomic.Int64
	maximum      atomic.Int64
}

func (prober *fakeCandidateProber) ProbeActor(
	ctx context.Context,
	actor string,
) (actorresolver.ActorProbeResult, error) {
	current := prober.current.Add(1)
	for {
		maximum := prober.maximum.Load()
		if current <= maximum || prober.maximum.CompareAndSwap(maximum, current) {
			break
		}
	}
	defer prober.current.Add(-1)
	if prober.cancel != nil {
		prober.cancel()
		return actorresolver.ActorProbeResult{}, ctx.Err()
	}
	if prober.delay > 0 {
		time.Sleep(prober.delay)
	}
	if err := prober.actorErrors[actor]; err != nil {
		return actorresolver.ActorProbeResult{}, err
	}
	result := actorresolver.ActorProbeResult{ActorID: actor, InboxURL: prober.inboxes[actor]}
	if prober.wrongActor {
		result.ActorID = "https://wrong.example/actor"
	}
	return result, nil
}

func (prober *fakeCandidateProber) ProbeInbox(
	_ context.Context,
	inbox string,
) (actorresolver.InboxProbeResult, error) {
	if result, ok := prober.inboxResults[inbox]; ok {
		return result, nil
	}
	return actorresolver.InboxProbeResponsive, nil
}
