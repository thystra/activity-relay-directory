package discoverycommand

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/thystra/activity-relay-directory/internal/actorresolver"
	"github.com/thystra/activity-relay-directory/internal/storage"
)

func TestParseDiscoveryActions(t *testing.T) {
	for _, test := range []struct {
		name string
		args []string
		want Action
	}{
		{
			name: "add",
			args: []string{"add", "--url", "https://relay.example/", "--operator", "alan", "--reason", "public_relay"},
			want: ActionAdd,
		},
		{
			name: "remove",
			args: []string{"remove", "--actor", "https://relay.example/actor", "--operator", "alan", "--reason", "retired", "--yes"},
			want: ActionRemove,
		},
		{
			name: "import",
			args: []string{"import", "--file", "/tmp/relays.txt", "--operator", "alan", "--reason", "public_list", "--source-label", "github_amjiddader"},
			want: ActionImport,
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			request, err := Parse(test.args)
			if err != nil || request.Action != test.want {
				t.Fatalf("Parse() = %#v, %v", request, err)
			}
		})
	}

	invalid := [][]string{
		{"add", "--url", "https://relay.example/"},
		{"add", "--url", "https://relay.example/", "--operator", "alan", "--reason", "Bad"},
		{"remove", "--actor", "https://RELAY.example/actor", "--operator", "alan", "--reason", "retired"},
		{"import", "--file", "/tmp/relays.txt", "--operator", "alan", "--reason", "public_list"},
		{"import", "--file", "/tmp/relays.txt", "--operator", "alan", "--reason", "public_list", "--source-label", "bad label"},
		{"add", "--url", "https://relay.example/", "--url", "https://two.example/", "--operator", "alan", "--reason", "public_relay"},
	}
	for _, args := range invalid {
		if request, err := Parse(args); err == nil || request != (Request{}) {
			t.Fatalf("Parse(%q) = %#v, %v", args, request, err)
		}
	}
}

func TestCandidateActorURLAcceptsOnlyReviewedForms(t *testing.T) {
	for _, test := range []struct {
		raw  string
		want string
	}{
		{raw: "relay.example", want: "https://relay.example/actor"},
		{raw: "relay.example/inbox", want: "https://relay.example/actor"},
		{raw: "https://relay.example", want: "https://relay.example/actor"},
		{raw: "https://relay.example/", want: "https://relay.example/actor"},
		{raw: "https://relay.example/inbox", want: "https://relay.example/actor"},
		{raw: "https://relay.example/actor", want: "https://relay.example/actor"},
	} {
		got, err := candidateActorURL(test.raw)
		if err != nil || got != test.want {
			t.Fatalf("candidateActorURL(%q) = %q, %v", test.raw, got, err)
		}
	}
	for _, raw := range []string{
		"http://relay.example/actor",
		"https://relay.example/other",
		"https://relay.example/actor?view=json",
	} {
		if got, err := candidateActorURL(raw); err == nil || got != "" {
			t.Fatalf("candidateActorURL(%q) = %q, %v", raw, got, err)
		}
	}
}

func TestLoadCandidatesBoundsAndIgnoresComments(t *testing.T) {
	directory := t.TempDir()
	path := filepath.Join(directory, "relays.txt")
	body := "# source\n\n  https://one.example/inbox  \n\t# note\nhttps://two.example/actor\n"
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatalf("write fixture: %v", err)
	}
	candidates, err := LoadCandidates(path)
	if err != nil || len(candidates) != 2 || candidates[0].Line != 3 || candidates[1].Line != 5 ||
		candidates[0].URL != "https://one.example/inbox" {
		t.Fatalf("LoadCandidates() = %#v, %v", candidates, err)
	}

	tooLong := filepath.Join(directory, "too-long.txt")
	if err := os.WriteFile(tooLong, []byte(strings.Repeat("x", MaximumCandidateLineBytes+1)), 0o600); err != nil {
		t.Fatalf("write long fixture: %v", err)
	}
	if _, err := LoadCandidates(tooLong); !errors.Is(err, ErrImportFile) {
		t.Fatalf("LoadCandidates(long) error = %v", err)
	}

	symlink := filepath.Join(directory, "link.txt")
	if err := os.Symlink(path, symlink); err != nil {
		t.Fatalf("symlink: %v", err)
	}
	if _, err := LoadCandidates(symlink); !errors.Is(err, ErrImportFile) {
		t.Fatalf("LoadCandidates(symlink) error = %v", err)
	}
}

func TestPrepareDeduplicatesCanonicalActorsAndProbesInbox(t *testing.T) {
	prober := &fakeProber{
		actors: map[string]actorresolver.ActorProbeResult{
			"https://one.example/actor": {ActorID: "https://one.example/actor", InboxURL: "https://one.example/inbox"},
			"https://two.example/actor": {ActorID: "https://two.example/actor"},
		},
		inboxes: map[string]actorresolver.InboxProbeResult{
			"https://one.example/inbox": actorresolver.InboxProbeMethodRejected,
		},
	}
	plan, err := prepareCandidates(context.Background(), []Candidate{
		{Line: 1, URL: "https://one.example/"},
		{Line: 2, URL: "https://one.example/actor"},
		{Line: 3, URL: "https://one.example/inbox"},
		{Line: 4, URL: "https://two.example/actor"},
		{Line: 5, URL: "https://bad.example/not-relay"},
	}, prober)
	if err != nil {
		t.Fatalf("prepareCandidates() error = %v", err)
	}
	if plan.CandidateCount != 5 || len(plan.Ready) != 2 || len(plan.Duplicates) != 2 || len(plan.Failed) != 1 {
		t.Fatalf("plan = %#v", plan)
	}
	if plan.Ready[0].RelayActor != "https://one.example/actor" ||
		plan.Ready[0].InboxProbeState != storage.InboxMethodRejected ||
		plan.Duplicates[0].Line != 2 ||
		plan.Duplicates[1].Line != 3 ||
		plan.Failed[0].Line != 5 ||
		plan.Failed[0].Code != "invalid_candidate" {
		t.Fatalf("plan details = %#v", plan)
	}
}

func TestClassifyKnownImportSeparatesActiveStateFromHistory(t *testing.T) {
	plan := Plan{
		CandidateCount: 4,
		Ready: []PreparedRelay{
			{Line: 1, RelayActor: "https://lifecycle.example/actor"},
			{Line: 2, RelayActor: "https://discovery.example/actor"},
			{Line: 3, RelayActor: "https://both.example/actor"},
			{Line: 4, RelayActor: "https://history.example/actor"},
		},
	}
	repository := &fakeKnownRepository{
		discoveries: map[string]storage.DiscoveryRecord{
			"https://discovery.example/actor": {
				RelayActor: "https://discovery.example/actor",
				State:      storage.DiscoveryActive,
			},
			"https://both.example/actor": {
				RelayActor: "https://both.example/actor",
				State:      storage.DiscoveryActive,
			},
			"https://history.example/actor": {
				RelayActor: "https://history.example/actor",
				State:      storage.DiscoveryRemoved,
			},
		},
		lifecycle: map[string]storage.ModerationState{
			"https://lifecycle.example/actor": {
				RelayActor:     "https://lifecycle.example/actor",
				LifecycleState: storage.LifecycleRegistered,
			},
			"https://both.example/actor": {
				RelayActor:     "https://both.example/actor",
				LifecycleState: storage.LifecycleRegistered,
			},
			"https://history.example/actor": {
				RelayActor:     "https://history.example/actor",
				LifecycleState: storage.LifecycleUnregistered,
			},
		},
	}

	classified, err := ClassifyKnown(
		context.Background(),
		Request{Action: ActionImport},
		plan,
		repository,
	)
	if err != nil {
		t.Fatalf("ClassifyKnown() error = %v", err)
	}
	if len(classified.Ready) != 1 ||
		classified.Ready[0].RelayActor != "https://history.example/actor" ||
		len(classified.AlreadyKnown) != 3 {
		t.Fatalf("classified plan = %#v", classified)
	}
	wantSources := []AlreadyKnownSource{
		AlreadyKnownLifecycle,
		AlreadyKnownDiscovery,
		AlreadyKnownLifecycleDiscovery,
	}
	for index, want := range wantSources {
		if classified.AlreadyKnown[index].Source != want {
			t.Fatalf("AlreadyKnown[%d] = %#v; want source %q",
				index, classified.AlreadyKnown[index], want)
		}
	}
}

func TestClassifyKnownLeavesManualAddUnchanged(t *testing.T) {
	plan := Plan{CandidateCount: 1, Ready: []PreparedRelay{{
		Line: 1, RelayActor: "https://relay.example/actor",
	}}}
	classified, err := ClassifyKnown(
		context.Background(), Request{Action: ActionAdd}, plan, nil,
	)
	if err != nil || len(classified.Ready) != 1 || len(classified.AlreadyKnown) != 0 {
		t.Fatalf("ClassifyKnown(add) = %#v, %v", classified, err)
	}
}

func TestRenderPlanDoesNotEchoInvalidCandidate(t *testing.T) {
	var output bytes.Buffer
	request := Request{Action: ActionImport}
	plan := Plan{CandidateCount: 1, Failed: []FailedCandidate{{Line: 9, Code: "invalid_candidate"}}}
	if err := RenderPlan(&output, request, plan); err != nil {
		t.Fatalf("RenderPlan() error = %v", err)
	}
	if !strings.Contains(output.String(), "failed line=9 code=invalid_candidate") ||
		strings.Contains(output.String(), "secret") {
		t.Fatalf("output = %q", output.String())
	}
}

func TestConfirmUsesCanonicalActorOrImportCount(t *testing.T) {
	add := Request{Action: ActionAdd}
	plan := Plan{Ready: []PreparedRelay{{RelayActor: "https://relay.example/actor"}}}
	var prompt bytes.Buffer
	if err := Confirm(add, plan, strings.NewReader("https://relay.example/actor\n"), &prompt); err != nil {
		t.Fatalf("Confirm(add) error = %v", err)
	}
	if !strings.Contains(prompt.String(), "https://relay.example/actor") {
		t.Fatalf("prompt = %q", prompt.String())
	}

	importRequest := Request{Action: ActionImport}
	importPlan := Plan{Ready: []PreparedRelay{{}, {}}}
	if err := Confirm(importRequest, importPlan, strings.NewReader("IMPORT 2\n"), ioDiscard{}); err != nil {
		t.Fatalf("Confirm(import) error = %v", err)
	}
	if err := Confirm(importRequest, importPlan, strings.NewReader("IMPORT 1\n"), ioDiscard{}); !errors.Is(err, ErrConfirmation) {
		t.Fatalf("Confirm(wrong) error = %v", err)
	}

	knownOnly := Plan{AlreadyKnown: []AlreadyKnownCandidate{{
		Line: 1, RelayActor: "https://relay.example/actor", Source: AlreadyKnownLifecycle,
	}}}
	if err := Confirm(importRequest, knownOnly, nil, nil); err != nil {
		t.Fatalf("Confirm(known-only import) error = %v", err)
	}
}

func TestExecuteAddsDiscoveryAndObservations(t *testing.T) {
	repository := &fakeRepository{}
	request := Request{
		Action: ActionImport, OperatorID: "alan", ReasonCode: "public_list",
		SourceLabel: "github_list", AssumeYes: true, Format: OutputHuman,
	}
	plan := Plan{
		CandidateCount: 1,
		Ready: []PreparedRelay{{
			Line: 4, RelayActor: "https://relay.example/actor", PublicBaseURL: "https://relay.example",
			InboxURL: "https://relay.example/inbox", InboxProbeState: storage.InboxResponsive,
		}},
	}
	var stdout, stderr bytes.Buffer
	code := Execute(context.Background(), request, plan, repository, &stdout, &stderr,
		func() time.Time { return time.Unix(100, 0).UTC() })
	if code != ExitSuccess || len(repository.adds) != 1 || len(repository.actorObservations) != 1 ||
		len(repository.inboxObservations) != 1 {
		t.Fatalf("Execute() = %d; repo=%#v stdout=%q stderr=%q", code, repository, stdout.String(), stderr.String())
	}
	if repository.adds[0].SourceKind != storage.DiscoverySourceFile ||
		repository.adds[0].SourceLabel != "github_list" ||
		repository.actorObservations[0].State != storage.ReachabilityReachable {
		t.Fatalf("recorded intents = %#v %#v", repository.adds, repository.actorObservations)
	}
}

func TestExecuteImportReportsCandidateFailureAfterApplyingReadyEntries(t *testing.T) {
	repository := &fakeRepository{}
	request := Request{Action: ActionImport, OperatorID: "alan", ReasonCode: "public_list", SourceLabel: "list", Format: OutputHuman}
	plan := Plan{
		CandidateCount: 2,
		Ready:          []PreparedRelay{{Line: 1, RelayActor: "https://relay.example/actor", PublicBaseURL: "https://relay.example"}},
		Failed:         []FailedCandidate{{Line: 2, Code: "actor_unreachable"}},
	}
	var stdout, stderr bytes.Buffer
	code := Execute(context.Background(), request, plan, repository, &stdout, &stderr,
		func() time.Time { return time.Unix(100, 0).UTC() })
	if code != ExitOperational || len(repository.adds) != 1 || !strings.Contains(stdout.String(), "code=actor_unreachable") {
		t.Fatalf("Execute() = %d; adds=%d stdout=%q stderr=%q", code, len(repository.adds), stdout.String(), stderr.String())
	}
}

func TestExecuteKnownOnlyImportDoesNotWriteDiscovery(t *testing.T) {
	repository := &fakeRepository{}
	request := Request{
		Action: ActionImport, OperatorID: "alan", ReasonCode: "public_list",
		SourceLabel: "list", Format: OutputHuman,
	}
	plan := Plan{
		CandidateCount: 1,
		AlreadyKnown: []AlreadyKnownCandidate{{
			Line: 1, RelayActor: "https://relay.example/actor", Source: AlreadyKnownLifecycle,
		}},
	}
	var stdout, stderr bytes.Buffer
	code := Execute(context.Background(), request, plan, repository, &stdout, &stderr,
		func() time.Time { return time.Unix(100, 0).UTC() })
	if code != ExitSuccess || len(repository.adds) != 0 ||
		!strings.Contains(stdout.String(),
			"line=1 status=already_known actor=https://relay.example/actor") {
		t.Fatalf("Execute() = %d; adds=%d stdout=%q stderr=%q",
			code, len(repository.adds), stdout.String(), stderr.String())
	}
}

type fakeProber struct {
	actors  map[string]actorresolver.ActorProbeResult
	inboxes map[string]actorresolver.InboxProbeResult
}

func (prober *fakeProber) ProbeActor(_ context.Context, actor string) (actorresolver.ActorProbeResult, error) {
	result, ok := prober.actors[actor]
	if !ok {
		return actorresolver.ActorProbeResult{}, actorresolver.ErrActorFetch
	}
	return result, nil
}

func (prober *fakeProber) ProbeInbox(_ context.Context, inbox string) (actorresolver.InboxProbeResult, error) {
	result, ok := prober.inboxes[inbox]
	if !ok {
		return actorresolver.InboxProbeUnreachable, nil
	}
	return result, nil
}

type fakeRepository struct {
	adds              []storage.DiscoveryAddIntent
	removes           []storage.DiscoveryRemoveIntent
	actorObservations []storage.ActorObservationIntent
	inboxObservations []storage.InboxObservationIntent
}

func (repository *fakeRepository) AddDiscovery(_ context.Context, intent storage.DiscoveryAddIntent, _ time.Time) (storage.DiscoveryOutcome, error) {
	repository.adds = append(repository.adds, intent)
	return storage.DiscoveryAdded, nil
}

func (repository *fakeRepository) RemoveDiscovery(_ context.Context, intent storage.DiscoveryRemoveIntent, _ time.Time) (storage.DiscoveryOutcome, error) {
	repository.removes = append(repository.removes, intent)
	return storage.DiscoveryRemovedOK, nil
}

func (*fakeRepository) GetDiscovery(context.Context, storage.IdentityIntent) (storage.DiscoveryRecord, bool, error) {
	return storage.DiscoveryRecord{}, false, nil
}

func (repository *fakeRepository) RecordActorObservation(_ context.Context, intent storage.ActorObservationIntent, _ time.Time) error {
	repository.actorObservations = append(repository.actorObservations, intent)
	return nil
}

func (repository *fakeRepository) RecordInboxObservation(_ context.Context, intent storage.InboxObservationIntent, _ time.Time) error {
	repository.inboxObservations = append(repository.inboxObservations, intent)
	return nil
}

func (*fakeRepository) GetObservation(context.Context, storage.IdentityIntent) (storage.RelayObservation, bool, error) {
	return storage.RelayObservation{}, false, nil
}

type fakeKnownRepository struct {
	discoveries map[string]storage.DiscoveryRecord
	lifecycle   map[string]storage.ModerationState
}

func (repository *fakeKnownRepository) GetDiscovery(
	_ context.Context,
	identity storage.IdentityIntent,
) (storage.DiscoveryRecord, bool, error) {
	record, found := repository.discoveries[identity.RelayActor]
	return record, found, nil
}

func (*fakeKnownRepository) AddDiscovery(context.Context, storage.DiscoveryAddIntent, time.Time) (storage.DiscoveryOutcome, error) {
	panic("unexpected AddDiscovery")
}

func (*fakeKnownRepository) RemoveDiscovery(context.Context, storage.DiscoveryRemoveIntent, time.Time) (storage.DiscoveryOutcome, error) {
	panic("unexpected RemoveDiscovery")
}

func (repository *fakeKnownRepository) ModerationState(
	_ context.Context, actor string,
) (storage.ModerationState, error) {
	state, found := repository.lifecycle[actor]
	if !found {
		return storage.ModerationState{}, storage.ErrRelayAbsent
	}
	return state, nil
}

func (*fakeKnownRepository) ModerationAudit(context.Context, storage.ModerationAuditQuery) (storage.ModerationAuditPage, error) {
	panic("unexpected ModerationAudit")
}

type ioDiscard struct{}

func (ioDiscard) Write(buffer []byte) (int, error) { return len(buffer), nil }
