package retention

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/thystra/activity-relay-directory/internal/storage"
)

const testActor = "https://relay.example/actor"

func lifecycleCandidate(actor string, inactive int64) storage.PurgeCandidate {
	return storage.PurgeCandidate{
		Kind:                    storage.PurgeCandidateLifecycle,
		RelayActor:              actor,
		LifecycleState:          storage.LifecycleUnregistered,
		InactiveUnix:            inactive,
		UpdatedUnix:             inactive,
		LatestRelayEventID:      inactive,
		LatestModerationEventID: 0,
		ObservationRevision:     0,
	}
}

func discoveryCandidate(actor string, inactive int64) storage.PurgeCandidate {
	return storage.PurgeCandidate{
		Kind:                   storage.PurgeCandidateDiscovery,
		RelayActor:             actor,
		InactiveUnix:           inactive,
		UpdatedUnix:            inactive,
		LatestDiscoveryEventID: inactive,
		ObservationRevision:    0,
	}
}

func TestSummarizeExactZeroOneDayAnd365DayCutoffs(t *testing.T) {
	day := int64(24 * time.Hour / time.Second)
	observed := time.Unix(400*day, 0).UTC()
	for _, test := range []struct {
		name           string
		days           int
		wantCutoff     int64
		wantQueries    int
		wantCandidates int
	}{
		{name: "zero disabled", days: 0, wantCutoff: observed.Unix(), wantQueries: 0, wantCandidates: 0},
		{name: "one day", days: 1, wantCutoff: observed.Unix() - day, wantQueries: 1, wantCandidates: 1},
		{name: "365 days", days: 365, wantCutoff: observed.Unix() - 365*day, wantQueries: 1, wantCandidates: 1},
	} {
		t.Run(test.name, func(t *testing.T) {
			repository := &cutoffRepository{wantCutoff: test.wantCutoff, returnCandidate: test.days > 0}
			summary, err := Summarize(context.Background(), repository, test.days, observed)
			if err != nil {
				t.Fatalf("Summarize() error = %v", err)
			}
			if summary.ObservedUnix != observed.Unix() || summary.CutoffUnix != test.wantCutoff ||
				summary.CandidateCount != test.wantCandidates || repository.queries != test.wantQueries {
				t.Fatalf("summary=%#v queries=%d", summary, repository.queries)
			}
			if test.wantCandidates == 1 {
				if summary.OldestInactiveUnix == nil || summary.NewestInactiveUnix == nil ||
					*summary.OldestInactiveUnix != test.wantCutoff || *summary.NewestInactiveUnix != test.wantCutoff {
					t.Fatalf("candidate times = %#v", summary)
				}
			}
		})
	}
}

func TestSummarizeRejectsCandidateNewerThanExactCutoff(t *testing.T) {
	observed := time.Unix(2*86400, 0).UTC()
	candidate := lifecycleCandidate(testActor, 86401)
	repository := &fixedPageRepository{page: storage.PurgeCandidatePage{Candidates: []storage.PurgeCandidate{candidate}}}
	if _, err := Summarize(context.Background(), repository, 1, observed); !errors.Is(err, ErrConfiguration) {
		t.Fatalf("Summarize(newer candidate) error = %v", err)
	}
}

func TestSummarizeIsBoundedAtOneThousandAndReportsTruncation(t *testing.T) {
	repository := &generatedRepository{total: storage.MaximumPurgeAttemptsPerRun + 1}
	summary, err := Summarize(context.Background(), repository, 1, time.Unix(200000, 0).UTC())
	if err != nil {
		t.Fatalf("Summarize() error = %v", err)
	}
	if summary.CandidateCount != storage.MaximumPurgeAttemptsPerRun || !summary.Truncated ||
		summary.Batches != 10 || repository.maximumLimit > storage.MaximumPurgeCandidatePage {
		t.Fatalf("bounded summary = %#v maxLimit=%d", summary, repository.maximumLimit)
	}
}

func TestRunCreatesAuditBeforePurgeAndFinalizesAggregateWithoutIdentityList(t *testing.T) {
	repository := &generatedRepository{total: 2}
	observed := time.Unix(200000, 0).UTC()
	digest := strings.Repeat("a", 64)
	result, err := Run(context.Background(), repository, 1, observed, digest)
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if result.CandidateCount != 2 || result.PurgedRelays != 2 || result.PurgedDiscoveries != 0 ||
		result.PurgedObservations != 0 || result.Skipped != 0 || result.Batches != 1 ||
		len(repository.starts) != 1 || len(repository.finishes) != 1 {
		t.Fatalf("result=%#v starts=%#v finishes=%#v", result, repository.starts, repository.finishes)
	}
	if got := strings.Join(repository.events, ","); got != "begin,purge,finish" {
		t.Fatalf("retention call order = %q", got)
	}
	start := repository.starts[0]
	finish := repository.finishes[0]
	if start.PolicyVersion != storage.RetentionPolicyVersion || start.RetentionDays != 1 ||
		start.ObservedUnix != observed.Unix() || start.CutoffUnix != observed.Unix()-86400 ||
		start.BackupSHA256 != digest || finish.RunID != 1 ||
		finish.CandidatesScanned != 2 || finish.PurgedRelays != 2 ||
		finish.Outcome != storage.RetentionCompleted {
		t.Fatalf("start=%#v finish=%#v", start, finish)
	}
	if strings.Contains(fmt.Sprintf("%#v %#v", start, finish), "relay.example") {
		t.Fatalf("audit leaked relay identity: start=%#v finish=%#v", start, finish)
	}
}

func TestRunAccumulatesMixedRetentionEffects(t *testing.T) {
	repository := &mixedRepository{}
	result, err := Run(context.Background(), repository, 1, time.Unix(200000, 0), strings.Repeat("e", 64))
	if err != nil {
		t.Fatalf("Run(mixed) error = %v", err)
	}
	if result.CandidateCount != 2 || result.PurgedRelays != 1 || result.PurgedDiscoveries != 1 ||
		result.PurgedObservations != 1 || result.Skipped != 0 || len(repository.finishes) != 1 ||
		repository.finishes[0].PurgedDiscoveries != 1 || repository.finishes[0].PurgedObservations != 1 {
		t.Fatalf("mixed result=%#v finishes=%#v", result, repository.finishes)
	}
}

func TestRunFinalizesCancellationAfterCandidateReadAndRestartCompletes(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	repository := &cancelingRepository{cancel: cancel}
	digest := strings.Repeat("b", 64)
	observed := time.Unix(200000, 0).UTC()
	result, err := Run(ctx, repository, 1, observed, digest)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("Run(canceled) error = %v", err)
	}
	if result.PurgedRelays != 0 || len(repository.starts) != 1 || len(repository.finishes) != 1 ||
		repository.finishes[0].Outcome != storage.RetentionCanceled ||
		repository.finishes[0].CandidatesScanned != 1 {
		t.Fatalf("canceled result=%#v starts=%#v finishes=%#v", result, repository.starts, repository.finishes)
	}

	restart := &generatedRepository{total: 1}
	result, err = Run(context.Background(), restart, 1, observed, digest)
	if err != nil || result.PurgedRelays != 1 || len(restart.finishes) != 1 ||
		restart.finishes[0].Outcome != storage.RetentionCompleted {
		t.Fatalf("restart result=%#v finishes=%#v err=%v", result, restart.finishes, err)
	}
}

func TestRunRejectsMalformedPageOrderingBeforeDestructiveCall(t *testing.T) {
	repository := &malformedRepository{}
	_, err := Run(context.Background(), repository, 1, time.Unix(200000, 0), strings.Repeat("c", 64))
	if !errors.Is(err, ErrConfiguration) {
		t.Fatalf("Run(malformed) error = %v", err)
	}
	if repository.purgeCalls != 0 || len(repository.starts) != 1 || len(repository.finishes) != 1 ||
		repository.finishes[0].Outcome != storage.RetentionFailed ||
		repository.finishes[0].CandidatesScanned != 0 {
		t.Fatalf("malformed repository state purgeCalls=%d starts=%#v finishes=%#v", repository.purgeCalls, repository.starts, repository.finishes)
	}
}

func TestRunReturnsFinalizationFailureAfterSuccessfulPurge(t *testing.T) {
	repository := &generatedRepository{total: 1, finishErr: errors.New("audit finalization unavailable")}
	result, err := Run(context.Background(), repository, 1, time.Unix(200000, 0), strings.Repeat("d", 64))
	if err == nil || err.Error() != "audit finalization unavailable" || result.PurgedRelays != 1 {
		t.Fatalf("Run(finalization failure) = result:%#v err:%v", result, err)
	}
	if got := strings.Join(repository.events, ","); got != "begin,purge,finish" {
		t.Fatalf("retention call order = %q", got)
	}
}

func TestValidatePageOrdersSameActorByCandidateKind(t *testing.T) {
	page := storage.PurgeCandidatePage{Candidates: []storage.PurgeCandidate{
		discoveryCandidate(testActor, 100),
		lifecycleCandidate(testActor, 100),
	}}
	// SQL lexical order is discovery, then lifecycle.
	if err := validatePage(page, storage.PurgeCandidateCursor{}, 2, 100); err != nil {
		t.Fatalf("validatePage(mixed kinds) error = %v", err)
	}
	page.Candidates[0], page.Candidates[1] = page.Candidates[1], page.Candidates[0]
	if err := validatePage(page, storage.PurgeCandidateCursor{}, 2, 100); !errors.Is(err, ErrConfiguration) {
		t.Fatalf("validatePage(reversed kinds) error = %v", err)
	}
}

type cutoffRepository struct {
	wantCutoff      int64
	returnCandidate bool
	queries         int
}

func (repository *cutoffRepository) PurgeCandidates(_ context.Context, query storage.PurgeCandidateQuery) (storage.PurgeCandidatePage, error) {
	repository.queries++
	if query.CutoffAt.Unix() != repository.wantCutoff {
		return storage.PurgeCandidatePage{}, fmt.Errorf("cutoff=%d want=%d", query.CutoffAt.Unix(), repository.wantCutoff)
	}
	if !repository.returnCandidate {
		return storage.PurgeCandidatePage{}, nil
	}
	return storage.PurgeCandidatePage{Candidates: []storage.PurgeCandidate{lifecycleCandidate(testActor, repository.wantCutoff)}}, nil
}

type fixedPageRepository struct{ page storage.PurgeCandidatePage }

func (repository *fixedPageRepository) PurgeCandidates(context.Context, storage.PurgeCandidateQuery) (storage.PurgeCandidatePage, error) {
	return repository.page, nil
}

type generatedRepository struct {
	total        int
	maximumLimit int
	starts       []storage.RetentionRunStart
	finishes     []storage.RetentionRunFinish
	events       []string
	finishErr    error
}

func (repository *generatedRepository) PurgeCandidates(_ context.Context, query storage.PurgeCandidateQuery) (storage.PurgeCandidatePage, error) {
	if query.Limit > repository.maximumLimit {
		repository.maximumLimit = query.Limit
	}
	start := int(query.After.InactiveUnix)
	if start >= repository.total {
		return storage.PurgeCandidatePage{}, nil
	}
	end := start + query.Limit
	if end > repository.total {
		end = repository.total
	}
	page := storage.PurgeCandidatePage{Candidates: make([]storage.PurgeCandidate, 0, end-start)}
	for index := start + 1; index <= end; index++ {
		page.Candidates = append(page.Candidates, lifecycleCandidate(testActor, int64(index)))
	}
	if end < repository.total {
		last := page.Candidates[len(page.Candidates)-1]
		page.Next = storage.PurgeCandidateCursor{InactiveUnix: last.InactiveUnix, RelayActor: last.RelayActor, Kind: last.Kind}
	}
	return page, nil
}

func (repository *generatedRepository) BeginRetentionRun(_ context.Context, start storage.RetentionRunStart) (int64, error) {
	repository.events = append(repository.events, "begin")
	repository.starts = append(repository.starts, start)
	return 1, nil
}

func (repository *generatedRepository) PurgeBatch(_ context.Context, runID int64, candidates []storage.PurgeCandidate, _ time.Time) (storage.PurgeBatchResult, error) {
	if runID != 1 {
		return storage.PurgeBatchResult{}, fmt.Errorf("runID=%d", runID)
	}
	repository.events = append(repository.events, "purge")
	return storage.PurgeBatchResult{Attempted: len(candidates), PurgedRelays: len(candidates)}, nil
}

func (repository *generatedRepository) FinishRetentionRun(_ context.Context, finish storage.RetentionRunFinish) error {
	repository.events = append(repository.events, "finish")
	repository.finishes = append(repository.finishes, finish)
	return repository.finishErr
}

type mixedRepository struct {
	starts   []storage.RetentionRunStart
	finishes []storage.RetentionRunFinish
	served   bool
}

func (repository *mixedRepository) PurgeCandidates(context.Context, storage.PurgeCandidateQuery) (storage.PurgeCandidatePage, error) {
	if repository.served {
		return storage.PurgeCandidatePage{}, nil
	}
	repository.served = true
	return storage.PurgeCandidatePage{Candidates: []storage.PurgeCandidate{
		discoveryCandidate("https://a.example/actor", 100),
		lifecycleCandidate("https://b.example/actor", 100),
	}}, nil
}
func (repository *mixedRepository) BeginRetentionRun(_ context.Context, start storage.RetentionRunStart) (int64, error) {
	repository.starts = append(repository.starts, start)
	return 1, nil
}
func (*mixedRepository) PurgeBatch(_ context.Context, _ int64, candidates []storage.PurgeCandidate, _ time.Time) (storage.PurgeBatchResult, error) {
	return storage.PurgeBatchResult{Attempted: len(candidates), PurgedRelays: 1, PurgedDiscoveries: 1, PurgedObservations: 1}, nil
}
func (repository *mixedRepository) FinishRetentionRun(_ context.Context, finish storage.RetentionRunFinish) error {
	repository.finishes = append(repository.finishes, finish)
	return nil
}

type cancelingRepository struct {
	cancel   context.CancelFunc
	starts   []storage.RetentionRunStart
	finishes []storage.RetentionRunFinish
}

func (repository *cancelingRepository) PurgeCandidates(_ context.Context, _ storage.PurgeCandidateQuery) (storage.PurgeCandidatePage, error) {
	repository.cancel()
	return storage.PurgeCandidatePage{Candidates: []storage.PurgeCandidate{lifecycleCandidate(testActor, 1)}}, nil
}
func (repository *cancelingRepository) BeginRetentionRun(_ context.Context, start storage.RetentionRunStart) (int64, error) {
	repository.starts = append(repository.starts, start)
	return 1, nil
}
func (*cancelingRepository) PurgeBatch(ctx context.Context, _ int64, candidates []storage.PurgeCandidate, _ time.Time) (storage.PurgeBatchResult, error) {
	if err := ctx.Err(); err != nil {
		return storage.PurgeBatchResult{}, err
	}
	return storage.PurgeBatchResult{Attempted: len(candidates), PurgedRelays: len(candidates)}, nil
}
func (repository *cancelingRepository) FinishRetentionRun(_ context.Context, finish storage.RetentionRunFinish) error {
	repository.finishes = append(repository.finishes, finish)
	return nil
}

type malformedRepository struct {
	purgeCalls int
	starts     []storage.RetentionRunStart
	finishes   []storage.RetentionRunFinish
}

func (*malformedRepository) PurgeCandidates(context.Context, storage.PurgeCandidateQuery) (storage.PurgeCandidatePage, error) {
	return storage.PurgeCandidatePage{Candidates: []storage.PurgeCandidate{
		lifecycleCandidate("https://z.example/actor", 2),
		lifecycleCandidate("https://a.example/actor", 1),
	}}, nil
}
func (repository *malformedRepository) BeginRetentionRun(_ context.Context, start storage.RetentionRunStart) (int64, error) {
	repository.starts = append(repository.starts, start)
	return 1, nil
}
func (repository *malformedRepository) PurgeBatch(context.Context, int64, []storage.PurgeCandidate, time.Time) (storage.PurgeBatchResult, error) {
	repository.purgeCalls++
	return storage.PurgeBatchResult{}, nil
}
func (repository *malformedRepository) FinishRetentionRun(_ context.Context, finish storage.RetentionRunFinish) error {
	repository.finishes = append(repository.finishes, finish)
	return nil
}
