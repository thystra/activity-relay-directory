package retentioncommand

import (
	"bytes"
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/thystra/activity-relay-directory/internal/storage"
)

const commandTestActor = "https://relay.example/actor"

func TestParseRetentionCommandsAndRejectsAmbiguousFlags(t *testing.T) {
	for _, test := range []struct {
		arguments []string
		action    string
		format    OutputFormat
		backup    string
		yes       bool
	}{
		{arguments: []string{"dry-run"}, action: "dry-run", format: OutputHuman},
		{arguments: []string{"dry-run", "--format", "json"}, action: "dry-run", format: OutputJSON},
		{arguments: []string{"purge", "--backup", "/secure/pre-retention.sqlite"}, action: "purge", format: OutputHuman, backup: "/secure/pre-retention.sqlite"},
		{arguments: []string{"purge", "--backup", "/secure/pre-retention.sqlite", "--yes", "--format", "json"}, action: "purge", format: OutputJSON, backup: "/secure/pre-retention.sqlite", yes: true},
	} {
		request, err := Parse(test.arguments)
		if err != nil {
			t.Fatalf("Parse(%q) error = %v", test.arguments, err)
		}
		if request.Action != test.action || request.Format != test.format || request.BackupPath != test.backup || request.Yes != test.yes {
			t.Fatalf("Parse(%q) = %#v", test.arguments, request)
		}
	}

	for _, arguments := range [][]string{
		nil,
		{"run"},
		{"dry-run", "--backup", "/tmp/backup.sqlite"},
		{"dry-run", "--yes"},
		{"purge"},
		{"purge", "--backup", ""},
		{"purge", "--backup", "/tmp/backup.sqlite", "--format", "xml"},
		{"purge", "--backup", "/tmp/backup.sqlite", "extra"},
	} {
		if _, err := Parse(arguments); err == nil {
			t.Fatalf("Parse(%q) error = nil", arguments)
		}
	}
}

func TestConfirmRetentionPurgeRequiresExactPolicyPhrase(t *testing.T) {
	request := Request{Action: "purge", BackupPath: "/backup.sqlite", Format: OutputHuman}
	var prompt bytes.Buffer
	if err := Confirm(request, strings.NewReader("wrong\n"), &prompt, 365); err == nil {
		t.Fatal("Confirm(wrong) error = nil")
	}
	if !strings.Contains(prompt.String(), `Type "PURGE 365"`) {
		t.Fatalf("prompt = %q", prompt.String())
	}
	prompt.Reset()
	if err := Confirm(request, strings.NewReader("PURGE 365\n"), &prompt, 365); err != nil {
		t.Fatalf("Confirm(exact) error = %v", err)
	}
	request.Yes = true
	if err := Confirm(request, nil, nil, 365); err != nil {
		t.Fatalf("Confirm(--yes) error = %v", err)
	}
}

func TestExecuteDryRunIsIdentityFreeForHumanAndJSON(t *testing.T) {
	for _, format := range []OutputFormat{OutputHuman, OutputJSON} {
		t.Run(string(format), func(t *testing.T) {
			repository := &commandRepository{candidates: []storage.PurgeCandidate{{
				RelayActor: commandTestActor, LifecycleState: storage.LifecycleUnregistered,
				InactiveUnix: 100, UpdatedUnix: 100, LatestRelayEventID: 7,
			}}}
			var stdout, stderr bytes.Buffer
			code := ExecuteDryRun(
				context.Background(),
				Request{Action: "dry-run", Format: format},
				repository,
				1,
				&stdout,
				&stderr,
				func() time.Time { return time.Unix(86500, 0) },
			)
			if code != ExitSuccess || stderr.Len() != 0 {
				t.Fatalf("ExecuteDryRun() = code:%d stdout:%q stderr:%q", code, stdout.String(), stderr.String())
			}
			if strings.Contains(stdout.String(), commandTestActor) || strings.Contains(stdout.String(), "relay_actor") {
				t.Fatalf("dry-run leaked identity: %q", stdout.String())
			}
			if format == OutputJSON {
				var document dryRunDocument
				if err := json.Unmarshal(stdout.Bytes(), &document); err != nil {
					t.Fatalf("decode JSON: %v", err)
				}
				if document.Schema != outputSchema || document.Kind != "inactive_retention_dry_run" ||
					document.CandidateCount != 1 || document.OldestInactiveUnix == nil ||
					*document.OldestInactiveUnix != 100 {
					t.Fatalf("document=%#v", document)
				}
			}
		})
	}
}

func TestExecuteDryRunAtZeroDoesNotQueryRepository(t *testing.T) {
	repository := &commandRepository{failIfQueried: true}
	var stdout, stderr bytes.Buffer
	code := ExecuteDryRun(
		context.Background(),
		Request{Action: "dry-run", Format: OutputHuman},
		repository,
		0,
		&stdout,
		&stderr,
		func() time.Time { return time.Unix(1000, 0) },
	)
	if code != ExitSuccess || repository.queryCalls != 0 || stderr.Len() != 0 ||
		!strings.Contains(stdout.String(), "enabled=false") || !strings.Contains(stdout.String(), "candidate_count=0") {
		t.Fatalf("zero dry-run = code:%d calls:%d stdout:%q stderr:%q", code, repository.queryCalls, stdout.String(), stderr.String())
	}
}

func TestExecutePurgeOutputsAggregatesAndAuditWithoutRelayIdentity(t *testing.T) {
	repository := &commandRepository{candidates: []storage.PurgeCandidate{{
		RelayActor: commandTestActor, LifecycleState: storage.LifecyclePruned,
		InactiveUnix: 100, UpdatedUnix: 100, LatestRelayEventID: 9,
	}}}
	digest := strings.Repeat("d", 64)
	var stdout, stderr bytes.Buffer
	code := ExecutePurge(
		context.Background(),
		Request{Action: "purge", BackupPath: "/backup.sqlite", Yes: true, Format: OutputJSON},
		repository,
		1,
		digest,
		&stdout,
		&stderr,
		func() time.Time { return time.Unix(86500, 0) },
	)
	if code != ExitSuccess || stderr.Len() != 0 {
		t.Fatalf("ExecutePurge() = code:%d stdout:%q stderr:%q", code, stdout.String(), stderr.String())
	}
	if strings.Contains(stdout.String(), commandTestActor) || len(repository.starts) != 1 || len(repository.finishes) != 1 ||
		repository.finishes[0].PurgedRelays != 1 || repository.starts[0].BackupSHA256 != digest {
		t.Fatalf("purge output/audit stdout=%q starts=%#v finishes=%#v", stdout.String(), repository.starts, repository.finishes)
	}
}

type commandRepository struct {
	candidates    []storage.PurgeCandidate
	queryCalls    int
	failIfQueried bool
	starts        []storage.RetentionRunStart
	finishes      []storage.RetentionRunFinish
}

func (repository *commandRepository) PurgeCandidates(_ context.Context, query storage.PurgeCandidateQuery) (storage.PurgeCandidatePage, error) {
	repository.queryCalls++
	if repository.failIfQueried {
		panic("disabled retention policy queried repository")
	}
	if query.After != (storage.PurgeCandidateCursor{}) {
		return storage.PurgeCandidatePage{}, nil
	}
	candidates := append([]storage.PurgeCandidate(nil), repository.candidates...)
	return storage.PurgeCandidatePage{Candidates: candidates}, nil
}

func (repository *commandRepository) BeginRetentionRun(_ context.Context, start storage.RetentionRunStart) (int64, error) {
	repository.starts = append(repository.starts, start)
	return 1, nil
}

func (*commandRepository) PurgeBatch(_ context.Context, runID int64, candidates []storage.PurgeCandidate, _ time.Time) (storage.PurgeBatchResult, error) {
	if runID != 1 {
		panic("unexpected retention run id")
	}
	return storage.PurgeBatchResult{
		Attempted: len(candidates), PurgedRelays: len(candidates), PurgedLifecycleEvents: len(candidates) * 2,
	}, nil
}

func (repository *commandRepository) FinishRetentionRun(_ context.Context, finish storage.RetentionRunFinish) error {
	repository.finishes = append(repository.finishes, finish)
	return nil
}
