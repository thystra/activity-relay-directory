package discoverycommand

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/thystra/activity-relay-directory/internal/actorresolver"
	"github.com/thystra/activity-relay-directory/internal/storage"
)

func TestParseImportAddDeadRelaysOnlyOnImport(t *testing.T) {
	request, err := Parse([]string{
		"import",
		"--file", "/tmp/relays.txt",
		"--operator", "alan",
		"--reason", "public_list",
		"--source-label", "curated_list",
		"--add-dead-relays",
	})
	if err != nil || !request.AddDeadRelays {
		t.Fatalf("Parse(import --add-dead-relays) = %#v, %v", request, err)
	}

	if request, err := Parse([]string{
		"add",
		"--url", "relay.example",
		"--operator", "alan",
		"--reason", "public_relay",
		"--add-dead-relays",
	}); err == nil || request != (Request{}) {
		t.Fatalf("Parse(add --add-dead-relays) = %#v, %v", request, err)
	}
}

func TestPrepareAddDeadRelaysRetainsOnlyActorFailures(t *testing.T) {
	directory := t.TempDir()
	path := filepath.Join(directory, "relays.txt")
	body := strings.Join([]string{
		"unreachable.example",
		"incompatible.example",
		"prohibited.example",
		"https://bad.example/not-relay",
	}, "\n") + "\n"
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}

	prober := &deadRelayProber{
		errors: map[string]error{
			"https://unreachable.example/actor":  actorresolver.ErrActorFetch,
			"https://incompatible.example/actor": actorresolver.ErrActorDocument,
			"https://prohibited.example/actor":   actorresolver.ErrNetworkTarget,
		},
	}
	plan, err := Prepare(context.Background(), Request{
		Action:        ActionImport,
		FilePath:      path,
		OperatorID:    "alan",
		ReasonCode:    "public_list",
		SourceLabel:   "curated_list",
		AddDeadRelays: true,
		Format:        OutputHuman,
	}, prober)
	if err != nil {
		t.Fatalf("Prepare() error = %v", err)
	}
	if len(plan.Retained) != 2 || len(plan.Failed) != 2 {
		t.Fatalf("plan = %#v", plan)
	}
	if plan.Retained[0].CandidateActorURL != "https://unreachable.example/actor" ||
		plan.Retained[0].State != storage.DiscoveryCandidateUnreachable ||
		plan.Retained[0].Failure != storage.DiscoveryCandidateActorUnreachable ||
		plan.Retained[1].CandidateActorURL != "https://incompatible.example/actor" ||
		plan.Retained[1].State != storage.DiscoveryCandidateIncompatible ||
		plan.Retained[1].Failure != storage.DiscoveryCandidateActorInvalid {
		t.Fatalf("retained = %#v", plan.Retained)
	}
	if plan.Failed[0].Code != "network_target_prohibited" ||
		plan.Failed[1].Code != "invalid_candidate" {
		t.Fatalf("failed = %#v", plan.Failed)
	}
}

func TestConfirmImportCountsReadyAndRetainedMutations(t *testing.T) {
	request := Request{Action: ActionImport}
	plan := Plan{
		Ready: []PreparedRelay{{RelayActor: "https://ready.example/actor"}},
		Retained: []RetainedCandidate{{
			CandidateActorURL: "https://dead.example/actor",
		}},
	}
	var prompt bytes.Buffer
	if err := Confirm(request, plan, strings.NewReader("IMPORT 2\n"), &prompt); err != nil {
		t.Fatalf("Confirm() error = %v", err)
	}
	if !strings.Contains(prompt.String(), "IMPORT 2") {
		t.Fatalf("prompt = %q", prompt.String())
	}
}

func TestExecuteRetainsDeadRelayWithoutTreatingItAsFailure(t *testing.T) {
	repository := &fakeCandidateRepository{}
	request := Request{
		Action: ActionImport, OperatorID: "alan", ReasonCode: "public_list",
		SourceLabel: "curated_list", AddDeadRelays: true, Format: OutputHuman,
	}
	plan := Plan{
		CandidateCount: 1,
		Retained: []RetainedCandidate{{
			Line:              2,
			CandidateActorURL: "https://dead.example/actor",
			PublicBaseURL:     "https://dead.example",
			State:             storage.DiscoveryCandidateUnreachable,
			Failure:           storage.DiscoveryCandidateActorUnreachable,
		}},
	}
	var stdout, stderr bytes.Buffer
	code := Execute(
		context.Background(),
		request,
		plan,
		repository,
		&stdout,
		&stderr,
		func() time.Time { return time.Unix(100, 0).UTC() },
	)
	if code != ExitSuccess || len(repository.candidates) != 1 {
		t.Fatalf("Execute() = %d candidates=%#v stdout=%q stderr=%q",
			code, repository.candidates, stdout.String(), stderr.String())
	}
	if !strings.Contains(stdout.String(), "status=retained") ||
		!strings.Contains(stdout.String(), "code=actor_unreachable") {
		t.Fatalf("stdout = %q", stdout.String())
	}
}

type deadRelayProber struct {
	errors map[string]error
}

func (prober *deadRelayProber) ProbeActor(
	_ context.Context,
	actor string,
) (actorresolver.ActorProbeResult, error) {
	if err, ok := prober.errors[actor]; ok {
		return actorresolver.ActorProbeResult{}, err
	}
	return actorresolver.ActorProbeResult{ActorID: actor}, nil
}

func (*deadRelayProber) ProbeInbox(
	context.Context,
	string,
) (actorresolver.InboxProbeResult, error) {
	return actorresolver.InboxProbeUnreachable, nil
}

type fakeCandidateRepository struct {
	fakeRepository
	candidates []storage.DiscoveryCandidateIntent
}

func (repository *fakeCandidateRepository) RetainDiscoveryCandidate(
	_ context.Context,
	intent storage.DiscoveryCandidateIntent,
	_ time.Time,
) (storage.DiscoveryCandidateOutcome, error) {
	repository.candidates = append(repository.candidates, intent)
	return storage.DiscoveryCandidateAdded, nil
}

func (*fakeCandidateRepository) GetDiscoveryCandidate(
	context.Context,
	string,
) (storage.DiscoveryCandidateRecord, bool, error) {
	return storage.DiscoveryCandidateRecord{}, false, nil
}
