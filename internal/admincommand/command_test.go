package admincommand

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

const testActor = "https://relay.example/actor"

func TestParseAcceptsStrictActionSpecificCommands(t *testing.T) {
	tests := []struct {
		arguments []string
		action    Action
		format    OutputFormat
		limit     int
	}{
		{[]string{"suspend", "--actor", testActor, "--moderator", "operator@example.org", "--reason", "security_review"}, ActionSuspend, OutputHuman, DefaultAuditLimit},
		{[]string{"restore", "--actor", testActor, "--moderator", "operator", "--reason", "manual_restore", "--yes", "--format", "json"}, ActionRestore, OutputJSON, DefaultAuditLimit},
		{[]string{"show", "--actor", testActor}, ActionShow, OutputHuman, DefaultAuditLimit},
		{[]string{"audit", "--actor", testActor, "--limit", "10", "--after", "100:7", "--format", "json"}, ActionAudit, OutputJSON, 10},
	}
	for _, test := range tests {
		request, err := Parse(test.arguments)
		if err != nil {
			t.Fatalf("Parse(%q) error = %v", test.arguments, err)
		}
		if request.Action != test.action || request.Format != test.format ||
			request.AuditLimit != test.limit || request.RelayActor != testActor {
			t.Fatalf("Parse(%q) = %#v", test.arguments, request)
		}
	}
}

func TestParseRejectsInvalidDuplicateAndNoncanonicalInput(t *testing.T) {
	invalid := [][]string{
		nil,
		{"unknown", "--actor", testActor},
		{"suspend", "--actor", testActor, "--moderator", "operator", "--reason", "Security"},
		{"suspend", "--actor", testActor, "--actor", testActor, "--moderator", "operator", "--reason", "security"},
		{"suspend", "--actor", testActor, "--moderator", "bad operator", "--reason", "security"},
		{"suspend", "--actor", testActor, "--moderator", "operator", "--reason", "security", "--yes=false"},
		{"restore", "--actor", "HTTPS://relay.example/actor", "--moderator", "operator", "--reason", "security"},
		{"show", "--actor", testActor, "extra"},
		{"show", "--actor", testActor, "--yes"},
		{"audit", "--actor", testActor, "--limit", "0"},
		{"audit", "--actor", testActor, "--limit", "101"},
		{"audit", "--actor", testActor, "--limit", "01"},
		{"audit", "--actor", testActor, "--after", "0100:7"},
		{"audit", "--actor", testActor, "--after", "100:0"},
		{"audit", "--actor", testActor, "--after", "100"},
		{"audit", "--actor", testActor, "--format", "yaml"},
	}
	for _, arguments := range invalid {
		if request, err := Parse(arguments); !errors.Is(err, ErrInvalidCommand) {
			t.Fatalf("Parse(%q) = (%#v, %v), want ErrInvalidCommand", arguments, request, err)
		}
	}
}

func TestConfirmationRequiresExactActorUnlessYes(t *testing.T) {
	request, err := Parse([]string{
		"suspend", "--actor", testActor, "--moderator", "operator", "--reason", "security",
	})
	if err != nil {
		t.Fatalf("Parse() error = %v", err)
	}
	var prompt bytes.Buffer
	if err := Confirm(request, strings.NewReader(testActor+"\n"), &prompt); err != nil {
		t.Fatalf("Confirm() error = %v", err)
	}
	if !strings.Contains(prompt.String(), testActor) || !strings.Contains(prompt.String(), "suspend") {
		t.Fatalf("prompt = %q", prompt.String())
	}
	if err := Confirm(request, strings.NewReader("yes\n"), &bytes.Buffer{}); !errors.Is(err, ErrConfirmation) {
		t.Fatalf("Confirm(mismatch) error = %v", err)
	}

	request.AssumeYes = true
	if err := Confirm(request, nil, nil); err != nil {
		t.Fatalf("Confirm(--yes) error = %v", err)
	}
	show, _ := Parse([]string{"show", "--actor", testActor})
	if err := Confirm(show, nil, nil); err != nil {
		t.Fatalf("Confirm(show) error = %v", err)
	}
}

func TestExecuteTransitionsUseStableHumanAndJSONOutput(t *testing.T) {
	repository := &fakeRepository{
		transitionOutcome: storage.ModerationSuspended,
	}
	request, _ := Parse([]string{
		"suspend", "--actor", testActor, "--moderator", "operator", "--reason", "security", "--yes",
	})
	var stdout, stderr bytes.Buffer
	code := Execute(
		context.Background(),
		request,
		repository,
		&stdout,
		&stderr,
		func() time.Time { return time.Unix(100, 0) },
	)
	if code != ExitSuccess || stderr.Len() != 0 ||
		stdout.String() != "action=suspend relay_actor=https://relay.example/actor outcome=suspended\n" {
		t.Fatalf("human transition = (%d, %q, %q)", code, stdout.String(), stderr.String())
	}
	if repository.lastIntent.RelayActor != testActor ||
		repository.lastIntent.ModeratorID != "operator" ||
		repository.lastIntent.ReasonCode != "security" ||
		repository.lastTime.Unix() != 100 {
		t.Fatalf("transition input = %#v at %s", repository.lastIntent, repository.lastTime)
	}

	repository.transitionOutcome = storage.ModerationRestored
	request, _ = Parse([]string{
		"restore", "--actor", testActor, "--moderator", "operator", "--reason", "review_complete", "--yes", "--format", "json",
	})
	stdout.Reset()
	code = Execute(context.Background(), request, repository, &stdout, &stderr, time.Now)
	if code != ExitSuccess || stderr.Len() != 0 {
		t.Fatalf("json transition = (%d, %q, %q)", code, stdout.String(), stderr.String())
	}
	var document map[string]any
	if err := json.Unmarshal(stdout.Bytes(), &document); err != nil {
		t.Fatalf("decode JSON: %v", err)
	}
	if document["schema"] != outputSchema || document["kind"] != "moderation_decision" ||
		document["action"] != "restore" || document["outcome"] != "restored" {
		t.Fatalf("transition JSON = %#v", document)
	}
}

func TestExecuteShowAndAuditEmitStablePrivateOperatorViews(t *testing.T) {
	lastHeartbeat := int64(105)
	suspended := int64(110)
	repository := &fakeRepository{
		state: storage.ModerationState{
			RelayActor:          testActor,
			PublicBaseURL:       "https://relay.example",
			LifecycleState:      storage.LifecycleRegistered,
			AdministrativeState: storage.AdministrativeSuspended,
			FirstRegisteredUnix: 100,
			UpdatedUnix:         110,
			LastHeartbeatUnix:   &lastHeartbeat,
			SuspendedUnix:       &suspended,
		},
		audit: storage.ModerationAuditPage{
			Events: []storage.ModerationAuditEvent{{
				EventID:      7,
				RelayActor:   testActor,
				Action:       storage.ModerationActionSuspendApplied,
				ModeratorID:  "operator@example.org",
				ReasonCode:   "security_review",
				RecordedUnix: 110,
			}},
			Next: storage.ModerationAuditCursor{RecordedUnix: 110, EventID: 7},
		},
	}

	show, _ := Parse([]string{"show", "--actor", testActor})
	var stdout, stderr bytes.Buffer
	if code := Execute(context.Background(), show, repository, &stdout, &stderr, time.Now); code != ExitSuccess {
		t.Fatalf("show code = %d, stderr = %q", code, stderr.String())
	}
	wantShow := "relay_actor=https://relay.example/actor\n" +
		"public_base_url=https://relay.example\n" +
		"lifecycle_state=registered\n" +
		"administrative_state=suspended\n" +
		"first_registered_at_unix=100\n" +
		"updated_at_unix=110\n" +
		"last_heartbeat_at_unix=105\n" +
		"unregistered_at_unix=-\n" +
		"pruned_at_unix=-\n" +
		"suspended_at_unix=110\n"
	if stdout.String() != wantShow || stderr.Len() != 0 {
		t.Fatalf("show = (%q, %q)", stdout.String(), stderr.String())
	}

	audit, _ := Parse([]string{"audit", "--actor", testActor, "--limit", "1", "--format", "json"})
	stdout.Reset()
	if code := Execute(context.Background(), audit, repository, &stdout, &stderr, time.Now); code != ExitSuccess {
		t.Fatalf("audit code = %d, stderr = %q", code, stderr.String())
	}
	var document struct {
		Schema     string `json:"schema"`
		Kind       string `json:"kind"`
		NextCursor string `json:"next_cursor"`
		Events     []struct {
			ModeratorID string `json:"moderator_id"`
			ReasonCode  string `json:"reason_code"`
		} `json:"events"`
	}
	if err := json.Unmarshal(stdout.Bytes(), &document); err != nil {
		t.Fatalf("decode audit JSON: %v", err)
	}
	if document.Schema != outputSchema || document.Kind != "moderation_audit" ||
		document.NextCursor != "110:7" || len(document.Events) != 1 ||
		document.Events[0].ModeratorID != "operator@example.org" ||
		document.Events[0].ReasonCode != "security_review" {
		t.Fatalf("audit JSON = %#v", document)
	}
	if repository.lastAudit.Limit != 1 || repository.lastAudit.RelayActor != testActor {
		t.Fatalf("audit query = %#v", repository.lastAudit)
	}
}

func TestExecuteMapsFixedExitClassesWithoutLeakingErrors(t *testing.T) {
	tests := []struct {
		err  error
		code int
		want string
	}{
		{storage.ErrRelayAbsent, ExitAbsent, "relay is not retained\n"},
		{storage.ErrTransitionInput, ExitUsage, "moderation state is invalid\n"},
		{context.Canceled, ExitCanceled, "moderation state canceled\n"},
		{errors.New("private database path /secret"), ExitOperational, "moderation state failed\n"},
	}
	request, _ := Parse([]string{"show", "--actor", testActor})
	for _, test := range tests {
		repository := &fakeRepository{err: test.err}
		var stdout, stderr bytes.Buffer
		code := Execute(context.Background(), request, repository, &stdout, &stderr, time.Now)
		if code != test.code || stdout.Len() != 0 || stderr.String() != test.want ||
			strings.Contains(stderr.String(), "/secret") {
			t.Fatalf("Execute(error %v) = (%d, %q, %q)", test.err, code, stdout.String(), stderr.String())
		}
	}
}

func TestCursorRoundTrip(t *testing.T) {
	cursor := storage.ModerationAuditCursor{RecordedUnix: 123, EventID: 456}
	encoded := FormatCursor(cursor)
	decoded, err := ParseCursor(encoded)
	if err != nil || decoded != cursor {
		t.Fatalf("cursor round trip = (%#v, %v) from %q", decoded, err, encoded)
	}
	if FormatCursor(storage.ModerationAuditCursor{}) != "" {
		t.Fatal("zero cursor is not empty")
	}
}

type fakeRepository struct {
	transitionOutcome storage.ModerationOutcome
	state             storage.ModerationState
	audit             storage.ModerationAuditPage
	err               error
	lastIntent        storage.ModerationIntent
	lastTime          time.Time
	lastAudit         storage.ModerationAuditQuery
}

func (repository *fakeRepository) Suspend(
	_ context.Context,
	intent storage.ModerationIntent,
	acceptedAt time.Time,
) (storage.ModerationOutcome, error) {
	repository.lastIntent = intent
	repository.lastTime = acceptedAt
	return repository.transitionOutcome, repository.err
}

func (repository *fakeRepository) Restore(
	_ context.Context,
	intent storage.ModerationIntent,
	acceptedAt time.Time,
) (storage.ModerationOutcome, error) {
	repository.lastIntent = intent
	repository.lastTime = acceptedAt
	return repository.transitionOutcome, repository.err
}

func (repository *fakeRepository) ModerationState(
	_ context.Context,
	_ string,
) (storage.ModerationState, error) {
	return repository.state, repository.err
}

func (repository *fakeRepository) ModerationAudit(
	_ context.Context,
	query storage.ModerationAuditQuery,
) (storage.ModerationAuditPage, error) {
	repository.lastAudit = query
	return repository.audit, repository.err
}
