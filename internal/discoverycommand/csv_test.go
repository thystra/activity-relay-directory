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

func TestParseCSVInputFormat(t *testing.T) {
	request, err := Parse([]string{
		"import", "--file", "/tmp/relays.csv", "--input-format", "csv",
		"--operator", "alan", "--reason", "public_list", "--source-label", "community_list",
	})
	if err != nil || request.InputFormat != InputCSV {
		t.Fatalf("Parse(CSV) = %#v, %v", request, err)
	}
	request, err = Parse([]string{
		"import", "--file", "/tmp/relays.txt",
		"--operator", "alan", "--reason", "public_list", "--source-label", "community_list",
	})
	if err != nil || request.InputFormat != InputLines {
		t.Fatalf("Parse(default lines) = %#v, %v", request, err)
	}
	if request, err := Parse([]string{
		"import", "--file", "/tmp/relays.csv", "--input-format", "yaml",
		"--operator", "alan", "--reason", "public_list", "--source-label", "community_list",
	}); err == nil || request != (Request{}) {
		t.Fatalf("Parse(invalid input format) = %#v, %v", request, err)
	}
}

func TestLoadCSVCandidatesParsesProfilesAndRejectsCanonicalDuplicates(t *testing.T) {
	directory := t.TempDir()
	path := filepath.Join(directory, "relays.csv")
	body := "relay,notes,languages,source_url\n" +
		"one.example,=formula,fr; en,https://source.example/list\n" +
		"https://two.example/actor,plain,en,\n"
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatalf("write CSV: %v", err)
	}
	candidates, err := LoadCSVCandidates(path, "community_list")
	if err != nil || len(candidates) != 2 || candidates[0].CSV == nil ||
		candidates[0].CSV.Profile.Notes != "=formula" ||
		strings.Join(candidates[0].CSV.Profile.Languages, ",") != "en,fr" ||
		candidates[0].CSV.SourceURL != "https://source.example/list" {
		t.Fatalf("LoadCSVCandidates() = %#v, %v", candidates, err)
	}

	duplicate := filepath.Join(directory, "duplicate.csv")
	if err := os.WriteFile(duplicate, []byte(
		"relay\none.example\nhttps://one.example/actor\n"), 0o600); err != nil {
		t.Fatalf("write duplicate CSV: %v", err)
	}
	if candidates, err := LoadCSVCandidates(duplicate, "community_list"); !errors.Is(err, ErrImportFile) || candidates != nil {
		t.Fatalf("LoadCSVCandidates(duplicate) = %#v, %v", candidates, err)
	}
}

func TestPrepareCSVRetainsProfileRowsByPhysicalLine(t *testing.T) {
	directory := t.TempDir()
	path := filepath.Join(directory, "relays.csv")
	if err := os.WriteFile(path, []byte(
		"relay,notes\nhttps://one.example/actor,hello\n"), 0o600); err != nil {
		t.Fatalf("write CSV: %v", err)
	}
	prober := &fakeProber{actors: map[string]actorresolver.ActorProbeResult{
		"https://one.example/actor": {ActorID: "https://one.example/actor"},
	}}
	plan, err := Prepare(context.Background(), Request{
		Action: ActionImport, FilePath: path, SourceLabel: "community_list", InputFormat: InputCSV,
	}, prober)
	if err != nil || len(plan.Ready) != 1 || plan.CSVRows[2].Profile.Notes != "hello" {
		t.Fatalf("Prepare(CSV) = %#v, %v", plan, err)
	}
}

func TestConfirmCSVKnownOnlyCountsProfileMutation(t *testing.T) {
	request := Request{Action: ActionImport, InputFormat: InputCSV}
	plan := Plan{AlreadyKnown: []AlreadyKnownCandidate{{
		Line: 2, RelayActor: "https://relay.example/actor", Source: AlreadyKnownLifecycle,
	}}}
	if err := Confirm(request, plan, strings.NewReader("IMPORT 1\n"), ioDiscard{}); err != nil {
		t.Fatalf("Confirm(CSV known-only) error = %v", err)
	}
}

func TestCSVJSONUsesV2ResultSchemaWithoutChangingLineImportSchema(t *testing.T) {
	repository := &fakeCSVRepository{}
	plan := Plan{
		CandidateCount: 1,
		AlreadyKnown: []AlreadyKnownCandidate{{
			Line: 2, RelayActor: "https://known.example/actor", Source: AlreadyKnownLifecycle,
		}},
		CSVRows: map[int]CSVProfileRow{2: {Profile: storage.RelayProfile{Notes: "known"}}},
	}
	var csvOutput, stderr bytes.Buffer
	csvCode := Execute(context.Background(), Request{
		Action: ActionImport, InputFormat: InputCSV, SourceLabel: "community_list", Format: OutputJSON,
	}, plan, repository, &csvOutput, &stderr, func() time.Time { return time.Unix(100, 0).UTC() })
	if csvCode != ExitSuccess || !strings.Contains(csvOutput.String(), `"schema":"activity-relay-directory.discovery-admin.v2"`) ||
		!strings.Contains(csvOutput.String(), `"profile":{"created":1`) {
		t.Fatalf("CSV JSON = (%d, %q, %q)", csvCode, csvOutput.String(), stderr.String())
	}

	lineRepository := &fakeRepository{}
	var lineOutput bytes.Buffer
	lineCode := Execute(context.Background(), Request{
		Action: ActionImport, InputFormat: InputLines, SourceLabel: "community_list", Format: OutputJSON,
	}, Plan{
		CandidateCount: 1,
		AlreadyKnown:   []AlreadyKnownCandidate{{Line: 1, RelayActor: "https://known.example/actor"}},
	}, lineRepository, &lineOutput, &stderr, func() time.Time { return time.Unix(101, 0).UTC() })
	if lineCode != ExitSuccess || !strings.Contains(lineOutput.String(), `"schema":"activity-relay-directory.discovery-admin.v1"`) ||
		strings.Contains(lineOutput.String(), `"profile"`) {
		t.Fatalf("line JSON = (%d, %q, %q)", lineCode, lineOutput.String(), stderr.String())
	}
}

type fakeCSVRepository struct {
	fakeRepository
	profileWrites []storage.ProfileSourceIntent
}

func (repository *fakeCSVRepository) ReplaceProfileSource(
	_ context.Context,
	intent storage.ProfileSourceIntent,
	_ time.Time,
) (storage.ProfileMutationSummary, error) {
	repository.profileWrites = append(repository.profileWrites, intent)
	return storage.ProfileMutationSummary{Created: 1, Unchanged: len(storage.ProfileFields()) - 1}, nil
}

func (*fakeCSVRepository) EffectiveProfile(context.Context, string) (storage.RelayProfile, error) {
	return storage.RelayProfile{}, nil
}

func TestExecuteCSVAppliesProfilesToReadyAndAlreadyKnownRelays(t *testing.T) {
	repository := &fakeCSVRepository{}
	request := Request{
		Action: ActionImport, InputFormat: InputCSV, OperatorID: "alan", ReasonCode: "public_list",
		SourceLabel: "community_list", AssumeYes: true, Format: OutputHuman,
	}
	plan := Plan{
		CandidateCount: 2,
		Ready: []PreparedRelay{{
			Line: 2, RelayActor: "https://new.example/actor", PublicBaseURL: "https://new.example",
		}},
		AlreadyKnown: []AlreadyKnownCandidate{{
			Line: 3, RelayActor: "https://known.example/actor", Source: AlreadyKnownLifecycle,
		}},
		CSVRows: map[int]CSVProfileRow{
			2: {Profile: storage.RelayProfile{Notes: "new"}, SourceURL: "https://source.example/list"},
			3: {Profile: storage.RelayProfile{Notes: "known"}},
		},
	}
	var stdout, stderr bytes.Buffer
	code := Execute(context.Background(), request, plan, repository, &stdout, &stderr,
		func() time.Time { return time.Unix(100, 0).UTC() })
	if code != ExitSuccess || len(repository.adds) != 1 || len(repository.profileWrites) != 2 ||
		repository.profileWrites[0].RelayActor != "https://known.example/actor" ||
		repository.profileWrites[1].RelayActor != "https://new.example/actor" ||
		repository.profileWrites[1].Source.SourceURL != "https://source.example/list" ||
		!strings.Contains(stdout.String(), "profile_changes=1") {
		t.Fatalf("Execute(CSV) = %d; repo=%#v stdout=%q stderr=%q",
			code, repository, stdout.String(), stderr.String())
	}
}
