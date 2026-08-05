package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	v1 "github.com/thystra/activity-relay-directory/internal/protocol/v1"
	storageContract "github.com/thystra/activity-relay-directory/internal/storage"
	storageSQLite "github.com/thystra/activity-relay-directory/internal/storage/sqlite"
)

const (
	cliTestActor = "https://relay.example/actor"
	cliTestBase  = "https://relay.example"
)

func TestAdminModerationCLIRequiresConfirmationBeforeDatabaseMutation(t *testing.T) {
	path := filepath.Join(t.TempDir(), "directory.sqlite")
	seedAdminRelay(t, path, 100)
	t.Setenv("DIRECTORY_DATABASE_PATH", path)

	var stdout, stderr bytes.Buffer
	code := runAdminWithInput(
		[]string{
			"activity-relay-directory", "admin", "suspend",
			"--actor", cliTestActor,
			"--moderator", "operator@example.org",
			"--reason", "security_review",
		},
		strings.NewReader("wrong\n"),
		&stdout,
		&stderr,
		func() time.Time { return time.Unix(110, 0) },
	)
	if code != 2 || stdout.Len() != 0 || !strings.Contains(stderr.String(), "confirmation failed") {
		t.Fatalf("unconfirmed suspend = (%d, %q, %q)", code, stdout.String(), stderr.String())
	}
	state, events := readAdminModerationState(t, path)
	if state != "active" || events != 0 {
		t.Fatalf("unconfirmed state = %s, events = %d", state, events)
	}
}

func TestAdminModerationCLISuspendShowAuditRestoreAndJSON(t *testing.T) {
	path := filepath.Join(t.TempDir(), "directory.sqlite")
	seedAdminRelay(t, path, 100)
	t.Setenv("DIRECTORY_DATABASE_PATH", path)
	now := func() time.Time { return time.Unix(110, 0) }
	run := func(input string, arguments ...string) (int, string, string) {
		t.Helper()
		var stdout, stderr bytes.Buffer
		code := runAdminWithInput(arguments, strings.NewReader(input), &stdout, &stderr, now)
		return code, stdout.String(), stderr.String()
	}

	code, stdout, stderr := run(
		cliTestActor+"\n",
		"activity-relay-directory", "admin", "suspend",
		"--actor", cliTestActor,
		"--moderator", "operator@example.org",
		"--reason", "security_review",
	)
	if code != 0 || stdout != "action=suspend relay_actor=https://relay.example/actor outcome=suspended\n" ||
		!strings.Contains(stderr, "confirmation required") {
		t.Fatalf("suspend = (%d, %q, %q)", code, stdout, stderr)
	}

	code, stdout, stderr = run(
		"",
		"activity-relay-directory", "admin", "show",
		"--actor", cliTestActor,
	)
	if code != 0 || stderr != "" ||
		!strings.Contains(stdout, "administrative_state=suspended\n") ||
		!strings.Contains(stdout, "suspended_at_unix=110\n") {
		t.Fatalf("show = (%d, %q, %q)", code, stdout, stderr)
	}

	code, stdout, stderr = run(
		"",
		"activity-relay-directory", "admin", "audit",
		"--actor", cliTestActor,
		"--limit", "1",
		"--format", "json",
	)
	if code != 0 || stderr != "" {
		t.Fatalf("audit = (%d, %q, %q)", code, stdout, stderr)
	}
	var audit struct {
		Events []struct {
			ModeratorID string `json:"moderator_id"`
			ReasonCode  string `json:"reason_code"`
		} `json:"events"`
		NextCursor string `json:"next_cursor"`
	}
	if err := json.Unmarshal([]byte(stdout), &audit); err != nil {
		t.Fatalf("decode audit: %v", err)
	}
	if len(audit.Events) != 1 || audit.Events[0].ModeratorID != "operator@example.org" ||
		audit.Events[0].ReasonCode != "security_review" || audit.NextCursor != "" {
		t.Fatalf("audit document = %#v", audit)
	}

	now = func() time.Time { return time.Unix(120, 0) }
	code, stdout, stderr = run(
		"",
		"activity-relay-directory", "admin", "restore",
		"--actor", cliTestActor,
		"--moderator", "operator@example.org",
		"--reason", "review_complete",
		"--yes",
		"--format", "json",
	)
	if code != 0 || stderr != "" || !strings.Contains(stdout, `"outcome":"restored"`) {
		t.Fatalf("restore = (%d, %q, %q)", code, stdout, stderr)
	}
	state, events := readAdminModerationState(t, path)
	if state != "active" || events != 2 {
		t.Fatalf("restored state = %s, events = %d", state, events)
	}
}

func TestAdminModerationCLIHandlesAbsentUnregisteredIdempotentAndRegressing(t *testing.T) {
	path := filepath.Join(t.TempDir(), "directory.sqlite")
	seedAdminRelay(t, path, 100)
	t.Setenv("DIRECTORY_DATABASE_PATH", path)

	run := func(now int64, arguments ...string) (int, string, string) {
		t.Helper()
		var stdout, stderr bytes.Buffer
		code := runAdminWithInput(
			arguments,
			strings.NewReader(""),
			&stdout,
			&stderr,
			func() time.Time { return time.Unix(now, 0) },
		)
		return code, stdout.String(), stderr.String()
	}

	code, _, stderr := run(
		110,
		"activity-relay-directory", "admin", "show",
		"--actor", "https://absent.example/actor",
	)
	if code != 3 || stderr != "relay is not retained\n" {
		t.Fatalf("absent show = (%d, %q)", code, stderr)
	}

	for index, want := range []string{"suspended", "already_suspended"} {
		code, stdout, stderr := run(
			int64(110+index),
			"activity-relay-directory", "admin", "suspend",
			"--actor", cliTestActor,
			"--moderator", "operator",
			"--reason", "security",
			"--yes",
		)
		if code != 0 || stderr != "" || !strings.Contains(stdout, "outcome="+want) {
			t.Fatalf("suspend %d = (%d, %q, %q)", index, code, stdout, stderr)
		}
	}

	code, _, stderr = run(
		99,
		"activity-relay-directory", "admin", "restore",
		"--actor", cliTestActor,
		"--moderator", "operator",
		"--reason", "security",
		"--yes",
	)
	if code != 2 || stderr != "moderation decision is invalid\n" {
		t.Fatalf("regressing restore = (%d, %q)", code, stderr)
	}

	unregisterAdminRelay(t, path, 120)
	code, stdout, stderr := run(
		130,
		"activity-relay-directory", "admin", "restore",
		"--actor", cliTestActor,
		"--moderator", "operator",
		"--reason", "review_complete",
		"--yes",
	)
	if code != 0 || stderr != "" || !strings.Contains(stdout, "outcome=restored") {
		t.Fatalf("restore unregistered = (%d, %q, %q)", code, stdout, stderr)
	}
	code, stdout, stderr = run(
		140,
		"activity-relay-directory", "admin", "show",
		"--actor", cliTestActor,
	)
	if code != 0 || stderr != "" ||
		!strings.Contains(stdout, "lifecycle_state=unregistered") ||
		!strings.Contains(stdout, "administrative_state=active") {
		t.Fatalf("show unregistered = (%d, %q, %q)", code, stdout, stderr)
	}
}

func TestAdminModerationCLIConcurrentIdempotentSuspend(t *testing.T) {
	path := filepath.Join(t.TempDir(), "directory.sqlite")
	seedAdminRelay(t, path, 100)
	t.Setenv("DIRECTORY_DATABASE_PATH", path)

	const callers = 8
	results := make(chan string, callers)
	var wait sync.WaitGroup
	for range callers {
		wait.Add(1)
		go func() {
			defer wait.Done()
			var stdout, stderr bytes.Buffer
			code := runAdminWithInput(
				[]string{
					"activity-relay-directory", "admin", "suspend",
					"--actor", cliTestActor,
					"--moderator", "operator",
					"--reason", "concurrent_review",
					"--yes",
				},
				strings.NewReader(""),
				&stdout,
				&stderr,
				func() time.Time { return time.Unix(110, 0) },
			)
			if code != 0 || stderr.Len() != 0 {
				results <- "error:" + stderr.String()
				return
			}
			results <- stdout.String()
		}()
	}
	wait.Wait()
	close(results)

	changed, unchanged := 0, 0
	for result := range results {
		switch {
		case strings.Contains(result, "outcome=suspended"):
			changed++
		case strings.Contains(result, "outcome=already_suspended"):
			unchanged++
		default:
			t.Fatalf("concurrent result = %q", result)
		}
	}
	if changed != 1 || unchanged != callers-1 {
		t.Fatalf("concurrent outcomes = changed:%d unchanged:%d", changed, unchanged)
	}
	state, events := readAdminModerationState(t, path)
	if state != "suspended" || events != callers {
		t.Fatalf("concurrent state = %s, events = %d", state, events)
	}
}

func TestAdminModerationCLIRejectsInvalidInvocationWithoutOpeningDatabase(t *testing.T) {
	path := filepath.Join(t.TempDir(), "directory.sqlite")
	t.Setenv("DIRECTORY_DATABASE_PATH", path)
	invalid := [][]string{
		{"activity-relay-directory", "admin", "suspend", "--actor", cliTestActor},
		{"activity-relay-directory", "admin", "show", "--actor", "HTTPS://relay.example/actor"},
		{"activity-relay-directory", "admin", "audit", "--actor", cliTestActor, "--limit", "101"},
	}
	for _, arguments := range invalid {
		var stdout, stderr bytes.Buffer
		code := runAdminWithInput(arguments, strings.NewReader(""), &stdout, &stderr, time.Now)
		if code != 2 || stdout.Len() != 0 || !strings.Contains(stderr.String(), "usage:") {
			t.Fatalf("invalid run = (%d, %q, %q)", code, stdout.String(), stderr.String())
		}
	}
	if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("invalid invocation created database: %v", err)
	}
}

func seedAdminRelay(t *testing.T, path string, acceptedAt int64) {
	t.Helper()
	database, err := initializeDatabase(context.Background(), path)
	if err != nil {
		t.Fatalf("initializeDatabase() error = %v", err)
	}
	repository, err := storageSQLite.NewRelayRepository(database)
	if err != nil {
		_ = database.Close()
		t.Fatalf("NewRelayRepository() error = %v", err)
	}
	if _, err := repository.SetEnrollment(
		context.Background(),
		true,
		storageContract.EnrollmentIntent{OperatorID: "test-setup"},
		time.Unix(acceptedAt-1, 0),
	); err != nil {
		_ = database.Close()
		t.Fatalf("SetEnrollment() error = %v", err)
	}
	outcome, err := repository.Register(
		context.Background(),
		storageContract.RegisterIntent{RelayActor: cliTestActor, PublicBaseURL: cliTestBase},
		time.Unix(acceptedAt, 0),
	)
	if err != nil || outcome != v1.OutcomeCreated {
		_ = database.Close()
		t.Fatalf("Register() = (%q, %v)", outcome, err)
	}
	if err := database.Close(); err != nil {
		t.Fatalf("Close() error = %v", err)
	}
}

func unregisterAdminRelay(t *testing.T, path string, acceptedAt int64) {
	t.Helper()
	database, err := initializeDatabase(context.Background(), path)
	if err != nil {
		t.Fatalf("initializeDatabase() error = %v", err)
	}
	defer database.Close()
	repository, err := storageSQLite.NewRelayRepository(database)
	if err != nil {
		t.Fatalf("NewRelayRepository() error = %v", err)
	}
	outcome, err := repository.Unregister(
		context.Background(),
		storageContract.IdentityIntent{RelayActor: cliTestActor},
		time.Unix(acceptedAt, 0),
	)
	if err != nil || outcome != v1.OutcomeRemoved {
		t.Fatalf("Unregister() = (%q, %v)", outcome, err)
	}
}

func readAdminModerationState(t *testing.T, path string) (string, int) {
	t.Helper()
	database, err := initializeDatabase(context.Background(), path)
	if err != nil {
		t.Fatalf("initializeDatabase() error = %v", err)
	}
	defer database.Close()
	var state string
	if err := database.QueryRow(
		`SELECT administrative_state FROM relays WHERE relay_actor = ?`,
		cliTestActor,
	).Scan(&state); err != nil {
		t.Fatalf("read administrative state: %v", err)
	}
	var events int
	if err := database.QueryRow(
		`SELECT COUNT(*) FROM moderation_events WHERE relay_actor = ?`,
		cliTestActor,
	).Scan(&events); err != nil {
		t.Fatalf("count moderation events: %v", err)
	}
	return state, events
}
