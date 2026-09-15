package reachability

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

func TestRunIsBoundedFairPagedAndConcurrent(t *testing.T) {
	candidates := make([]storage.ReachabilityCandidate, storage.MaximumReachabilityAttemptsPerRun+1)
	for index := range candidates {
		actor := fmt.Sprintf("https://relay-%04d.example/actor", index)
		candidates[index] = storage.ReachabilityCandidate{RelayActor: actor}
	}
	repository := &fakeRepository{candidates: candidates}
	prober := &fakeProber{delay: 2 * time.Millisecond}
	observed := time.Unix(2_000_000, 0)
	result, err := Run(context.Background(), repository, prober, observed)
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if result.ObservedUnix != observed.Unix() ||
		result.Scanned != storage.MaximumReachabilityAttemptsPerRun ||
		result.Reachable != storage.MaximumReachabilityAttemptsPerRun ||
		result.Unreachable != 0 || result.Skipped != 0 || !result.Truncated {
		t.Fatalf("result = %#v", result)
	}
	if repository.maximumPage > storage.MaximumReachabilityCandidatePage {
		t.Fatalf("maximum page = %d", repository.maximumPage)
	}
	if got := prober.maximum.Load(); got > storage.MaximumConcurrentReachabilityProbes || got < 2 {
		t.Fatalf("maximum concurrency = %d", got)
	}
	if len(repository.recorded) != storage.MaximumReachabilityAttemptsPerRun {
		t.Fatalf("recorded = %d", len(repository.recorded))
	}
}

func TestRunClassifiesActorFailureInboxDiagnosticsAndWriteSkip(t *testing.T) {
	checked := int64(1)
	repository := &fakeRepository{
		candidates: []storage.ReachabilityCandidate{
			{RelayActor: "https://a.example/actor"},
			{RelayActor: "https://b.example/actor"},
			{RelayActor: "https://c.example/actor", ActorLastCheckedUnix: &checked},
		},
		outcomes: map[string]storage.ReachabilityWriteOutcome{
			"https://c.example/actor": storage.ReachabilityWriteSkipped,
		},
	}
	prober := &fakeProber{
		actorErrors: map[string]error{"https://a.example/actor": errors.New("network")},
		inboxes: map[string]string{
			"https://b.example/actor": "https://b.example/inbox",
			"https://c.example/actor": "https://c.example/inbox",
		},
		inboxResults: map[string]actorresolver.InboxProbeResult{
			"https://b.example/inbox": actorresolver.InboxProbeMethodRejected,
			"https://c.example/inbox": actorresolver.InboxProbeResponsive,
		},
	}
	result, err := Run(context.Background(), repository, prober, time.Unix(100_000, 0))
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if result.Scanned != 3 || result.Reachable != 1 || result.Unreachable != 1 ||
		result.Skipped != 1 || result.InboxMethodRejected != 1 ||
		result.InboxResponsive != 0 || result.Truncated {
		t.Fatalf("result = %#v", result)
	}
	if repository.recorded[0].ActorState != storage.ReachabilityUnreachable ||
		repository.recorded[1].InboxState != storage.InboxMethodRejected {
		t.Fatalf("recorded intents = %#v", repository.recorded)
	}
}

func TestRunRejectsInvalidPagesAndProberContract(t *testing.T) {
	observed := time.Unix(100_000, 0)
	for name, repository := range map[string]storage.ReachabilityRepository{
		"oversized": &fixedRepository{page: storage.ReachabilityCandidatePage{
			Candidates: make([]storage.ReachabilityCandidate, storage.MaximumReachabilityCandidatePage+1),
		}},
		"nonadvancing": &fixedRepository{page: storage.ReachabilityCandidatePage{
			Candidates: []storage.ReachabilityCandidate{
				{RelayActor: "https://b.example/actor"},
				{RelayActor: "https://a.example/actor"},
			},
		}},
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := Run(context.Background(), repository, &fakeProber{}, observed); !errors.Is(err, ErrConfiguration) {
				t.Fatalf("Run() error = %v", err)
			}
		})
	}

	repository := &fakeRepository{candidates: []storage.ReachabilityCandidate{{RelayActor: "https://a.example/actor"}}}
	prober := &fakeProber{wrongActor: true}
	if _, err := Run(context.Background(), repository, prober, observed); !errors.Is(err, ErrConfiguration) {
		t.Fatalf("wrong actor Run() error = %v", err)
	}
}

func TestRunHonorsCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	repository := &fakeRepository{candidates: []storage.ReachabilityCandidate{{RelayActor: "https://a.example/actor"}}}
	prober := &fakeProber{cancel: cancel}
	_, err := Run(ctx, repository, prober, time.Unix(100_000, 0))
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("Run() error = %v", err)
	}
}

type fakeRepository struct {
	candidates  []storage.ReachabilityCandidate
	outcomes    map[string]storage.ReachabilityWriteOutcome
	recorded    []storage.ReachabilityObservationIntent
	maximumPage int
	mutex       sync.Mutex
}

func (repository *fakeRepository) ReachabilityCandidates(
	_ context.Context,
	query storage.ReachabilityCandidateQuery,
) (storage.ReachabilityCandidatePage, error) {
	if query.Limit > repository.maximumPage {
		repository.maximumPage = query.Limit
	}
	start := 0
	if query.After != (storage.ReachabilityCandidateCursor{}) {
		for index, candidate := range repository.candidates {
			if candidateCursor(candidate) == query.After {
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
	page := storage.ReachabilityCandidatePage{
		Candidates: append([]storage.ReachabilityCandidate(nil), repository.candidates[start:end]...),
	}
	if end < len(repository.candidates) {
		page.Next = candidateCursor(page.Candidates[len(page.Candidates)-1])
	}
	return page, nil
}

func (repository *fakeRepository) RecordReachabilityObservation(
	_ context.Context,
	intent storage.ReachabilityObservationIntent,
	_ time.Time,
) (storage.ReachabilityWriteOutcome, error) {
	repository.mutex.Lock()
	defer repository.mutex.Unlock()
	repository.recorded = append(repository.recorded, intent)
	if outcome, ok := repository.outcomes[intent.RelayActor]; ok {
		return outcome, nil
	}
	return storage.ReachabilityWriteApplied, nil
}

type fixedRepository struct {
	page storage.ReachabilityCandidatePage
}

func (repository *fixedRepository) ReachabilityCandidates(context.Context, storage.ReachabilityCandidateQuery) (storage.ReachabilityCandidatePage, error) {
	return repository.page, nil
}
func (*fixedRepository) RecordReachabilityObservation(context.Context, storage.ReachabilityObservationIntent, time.Time) (storage.ReachabilityWriteOutcome, error) {
	return storage.ReachabilityWriteApplied, nil
}

type fakeProber struct {
	actorErrors  map[string]error
	inboxes      map[string]string
	inboxResults map[string]actorresolver.InboxProbeResult
	delay        time.Duration
	wrongActor   bool
	cancel       context.CancelFunc
	current      atomic.Int64
	maximum      atomic.Int64
}

func (prober *fakeProber) ProbeActor(ctx context.Context, actor string) (actorresolver.ActorProbeResult, error) {
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

func (prober *fakeProber) ProbeInbox(_ context.Context, inbox string) (actorresolver.InboxProbeResult, error) {
	if result, ok := prober.inboxResults[inbox]; ok {
		return result, nil
	}
	return actorresolver.InboxProbeResponsive, nil
}
