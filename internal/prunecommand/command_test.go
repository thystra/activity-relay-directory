package prunecommand

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/thystra/activity-relay-directory/internal/storage"
)

const pruneCommandActor = "https://relay.example/actor"

func TestParseDryRunDefaultsAndCanonicalCursor(t *testing.T) {
	request, err := Parse([]string{"dry-run"})
	if err != nil {
		t.Fatalf("Parse(default) error = %v", err)
	}
	if request.Limit != DefaultLimit || request.Format != OutputHuman ||
		request.After != (storage.PruneCandidateCursor{}) {
		t.Fatalf("Parse(default) = %#v", request)
	}

	request, err = Parse([]string{
		"dry-run",
		"--limit", "25",
		"--after-last-seen", "100",
		"--after-actor", pruneCommandActor,
		"--format", "json",
	})
	if err != nil {
		t.Fatalf("Parse(cursor) error = %v", err)
	}
	if request.Limit != 25 || request.Format != OutputJSON ||
		request.After != (storage.PruneCandidateCursor{
			LastSeenUnix: 100,
			RelayActor:   pruneCommandActor,
		}) {
		t.Fatalf("Parse(cursor) = %#v", request)
	}
}

func TestParseDryRunRejectsInvalidAndDuplicateArguments(t *testing.T) {
	for _, arguments := range [][]string{
		nil,
		{"run"},
		{"dry-run", "extra"},
		{"dry-run", "--limit", "0"},
		{"dry-run", "--limit", "101"},
		{"dry-run", "--limit", "01"},
		{"dry-run", "--limit", "1", "--limit", "2"},
		{"dry-run", "--format", "yaml"},
		{"dry-run", "--after-last-seen", "100"},
		{"dry-run", "--after-actor", pruneCommandActor},
		{"dry-run", "--after-last-seen", "-1", "--after-actor", pruneCommandActor},
		{"dry-run", "--after-last-seen", "100", "--after-actor", "HTTPS://relay.example/actor"},
		{"dry-run", "--after-last-seen", "100", "--after-actor", pruneCommandActor, "--after-actor", pruneCommandActor},
	} {
		if request, err := Parse(arguments); !errors.Is(err, ErrInvalidCommand) || request != (Request{}) {
			t.Fatalf("Parse(%q) = (%#v, %v)", arguments, request, err)
		}
	}
}

func TestExecuteDryRunHumanUsesOneCapturedObservation(t *testing.T) {
	repository := &fakePruneReadRepository{
		page: storage.PruneCandidatePage{
			Candidates: []storage.PruneCandidate{
				{
					RelayActor:          "https://one.example/actor",
					PublicBaseURL:       "https://one.example/",
					AdministrativeState: storage.AdministrativeActive,
					LastSeenUnix:        100,
				},
				{
					RelayActor:          "https://two.example/actor",
					PublicBaseURL:       "https://two.example/",
					AdministrativeState: storage.AdministrativeSuspended,
					LastSeenUnix:        101,
				},
			},
			Next: storage.PruneCandidateCursor{
				LastSeenUnix: 101,
				RelayActor:   "https://two.example/actor",
			},
		},
	}
	var stdout, stderr bytes.Buffer
	calls := 0
	code := Execute(
		context.Background(),
		Request{Limit: 2, Format: OutputHuman},
		repository,
		&stdout,
		&stderr,
		func() time.Time {
			calls++
			return time.Unix(4_000_000, 999)
		},
	)
	if code != ExitSuccess || stderr.Len() != 0 || calls != 1 {
		t.Fatalf("Execute() = code:%d stderr:%q calls:%d", code, stderr.String(), calls)
	}
	if repository.query.ObservedAt.Unix() != 4_000_000 || repository.query.Limit != 2 {
		t.Fatalf("query = %#v", repository.query)
	}
	want := strings.Join([]string{
		"observed_at_unix=4000000",
		"count=2",
		"last_seen_at_unix=100 administrative_state=active relay_actor=https://one.example/actor public_base_url=https://one.example/",
		"last_seen_at_unix=101 administrative_state=suspended relay_actor=https://two.example/actor public_base_url=https://two.example/",
		"next_after_last_seen_at_unix=101",
		"next_after_actor=https://two.example/actor",
		"",
	}, "\n")
	if stdout.String() != want {
		t.Fatalf("human output = %q, want %q", stdout.String(), want)
	}
}

func TestExecuteDryRunJSONUsesStableSchemaAndEmptyCursor(t *testing.T) {
	repository := &fakePruneReadRepository{
		page: storage.PruneCandidatePage{
			Candidates: []storage.PruneCandidate{{
				RelayActor:          pruneCommandActor,
				PublicBaseURL:       "https://relay.example/",
				AdministrativeState: storage.AdministrativeActive,
				LastSeenUnix:        100,
			}},
		},
	}
	var stdout, stderr bytes.Buffer
	code := Execute(
		context.Background(),
		Request{Limit: 1, Format: OutputJSON},
		repository,
		&stdout,
		&stderr,
		func() time.Time { return time.Unix(4_000_000, 0) },
	)
	if code != ExitSuccess || stderr.Len() != 0 {
		t.Fatalf("Execute() = (%d, %q)", code, stderr.String())
	}
	var document struct {
		Schema           string `json:"schema"`
		Kind             string `json:"kind"`
		ObservedUnix     int64  `json:"observed_at_unix"`
		NextLastSeenUnix int64  `json:"next_after_last_seen_at_unix"`
		NextRelayActor   string `json:"next_after_actor"`
		Candidates       []struct {
			RelayActor          string                           `json:"relay_actor"`
			AdministrativeState storage.RelayAdministrativeState `json:"administrative_state"`
		} `json:"candidates"`
	}
	if err := json.Unmarshal(stdout.Bytes(), &document); err != nil {
		t.Fatalf("decode JSON: %v", err)
	}
	if document.Schema != outputSchema || document.Kind != "soft_pruning_dry_run" ||
		document.ObservedUnix != 4_000_000 || document.NextLastSeenUnix != 0 ||
		document.NextRelayActor != "" || len(document.Candidates) != 1 ||
		document.Candidates[0].RelayActor != pruneCommandActor ||
		document.Candidates[0].AdministrativeState != storage.AdministrativeActive {
		t.Fatalf("JSON document = %#v", document)
	}
}

func TestExecuteDryRunMapsRepositoryErrorsAndRejectsConfiguration(t *testing.T) {
	for name, repositoryError := range map[string]struct {
		err  error
		code int
	}{
		"usage":       {err: storage.ErrPruningReadInput, code: ExitUsage},
		"canceled":    {err: context.Canceled, code: ExitCanceled},
		"deadline":    {err: context.DeadlineExceeded, code: ExitCanceled},
		"operational": {err: errors.New("private failure"), code: ExitOperational},
	} {
		t.Run(name, func(t *testing.T) {
			var stdout, stderr bytes.Buffer
			code := Execute(
				context.Background(),
				Request{Limit: 1, Format: OutputHuman},
				&fakePruneReadRepository{err: repositoryError.err},
				&stdout,
				&stderr,
				func() time.Time { return time.Unix(100, 0) },
			)
			if code != repositoryError.code || stdout.Len() != 0 ||
				strings.Contains(stderr.String(), "private failure") {
				t.Fatalf("Execute() = (%d, %q, %q)", code, stdout.String(), stderr.String())
			}
		})
	}

	var stdout, stderr bytes.Buffer
	if code := Execute(
		context.Background(),
		Request{Limit: 0, Format: OutputHuman},
		&fakePruneReadRepository{},
		&stdout,
		&stderr,
		func() time.Time { return time.Now() },
	); code != ExitOperational {
		t.Fatalf("Execute(invalid config) code = %d", code)
	}
}

type fakePruneReadRepository struct {
	query storage.PruneCandidateQuery
	page  storage.PruneCandidatePage
	err   error
}

func (repository *fakePruneReadRepository) PruneCandidates(
	_ context.Context,
	query storage.PruneCandidateQuery,
) (storage.PruneCandidatePage, error) {
	repository.query = query
	return repository.page, repository.err
}
