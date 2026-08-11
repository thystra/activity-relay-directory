package main

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/thystra/activity-relay-directory/internal/storage"
	storageSQLite "github.com/thystra/activity-relay-directory/internal/storage/sqlite"
	"github.com/thystra/activity-relay-directory/internal/storagecommand"
)

func TestAdminStorageStatusAndCheckUseBoundedLocalReadOnlySurface(t *testing.T) {
	path := createAdminStorageDatabase(t)
	setAdminStorageEnvironment(t, path)
	now := func() time.Time { return time.Unix(10_000, 0).UTC() }

	var stdout, stderr bytes.Buffer
	code := runAdmin(
		[]string{"activity-relay-directory", "admin", "storage", "status", "--format", "json"},
		&stdout, &stderr, now,
	)
	if code != storagecommand.ExitOK || stderr.Len() != 0 {
		t.Fatalf("storage status = code:%d stderr:%q", code, stderr.String())
	}
	var document map[string]any
	if err := json.Unmarshal(stdout.Bytes(), &document); err != nil {
		t.Fatalf("decode storage status: %v", err)
	}
	if document["schema"] != "activity-relay-directory.storage-admin.v1" ||
		document["kind"] != "status" || document["observed_at_unix"] != float64(10_000) ||
		document["configured_max_bytes"] != float64(storage.DefaultDatabaseMaxBytes) {
		t.Fatalf("storage status document = %#v", document)
	}
	for _, forbidden := range []string{"relay_actor", "moderator", "reason_code", path} {
		if strings.Contains(stdout.String(), forbidden) {
			t.Fatalf("storage status leaked %q: %s", forbidden, stdout.String())
		}
	}

	stdout.Reset()
	stderr.Reset()
	code = runAdmin(
		[]string{"activity-relay-directory", "admin", "storage", "check"},
		&stdout, &stderr, now,
	)
	if code != storagecommand.ExitOK || !strings.Contains(stdout.String(), "state: normal") || stderr.Len() != 0 {
		t.Fatalf("storage check = code:%d stdout:%q stderr:%q", code, stdout.String(), stderr.String())
	}
}

func TestAdminStorageCheckReturnsHardWithoutMutatingDatabase(t *testing.T) {
	path := createAdminStorageDatabase(t)
	setAdminStorageEnvironment(t, path)
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	maxBytes := info.Size()
	if maxBytes <= 32*1024 {
		t.Fatalf("test database unexpectedly small for hard-limit check: %d", maxBytes)
	}
	t.Setenv("DIRECTORY_DATABASE_MAX_BYTES", strconv.FormatInt(maxBytes, 10))

	before := adminGrowthStateRow(t, path)
	var stdout, stderr bytes.Buffer
	code := runAdmin(
		[]string{"activity-relay-directory", "admin", "storage", "check", "--format", "json"},
		&stdout, &stderr, func() time.Time { return time.Unix(20_000, 0).UTC() },
	)
	if code != storagecommand.ExitHard || stderr.Len() != 0 {
		t.Fatalf("hard storage check = code:%d stdout:%q stderr:%q", code, stdout.String(), stderr.String())
	}
	if !strings.Contains(stdout.String(), `"state":"hard"`) || !strings.Contains(stdout.String(), `"write_admission":"blocked"`) {
		t.Fatalf("hard storage check output = %q", stdout.String())
	}
	after := adminGrowthStateRow(t, path)
	if before != after {
		t.Fatalf("read-only storage check mutated growth state: before=%q after=%q", before, after)
	}
}

func TestAdminStorageTestAlertRequiresExplicitEmailAndUsesFakeCommand(t *testing.T) {
	path := createAdminStorageDatabase(t)
	setAdminStorageEnvironment(t, path)
	now := func() time.Time { return time.Unix(30_000, 0).UTC() }

	var stdout, stderr bytes.Buffer
	code := runAdmin(
		[]string{"activity-relay-directory", "admin", "storage", "test-alert"},
		&stdout, &stderr, now,
	)
	if code != storagecommand.ExitUsage || !strings.Contains(stderr.String(), "email is disabled") {
		t.Fatalf("disabled test-alert = code:%d stderr:%q", code, stderr.String())
	}

	directory := t.TempDir()
	argsPath := filepath.Join(directory, "args")
	bodyPath := filepath.Join(directory, "body")
	mailerPath := filepath.Join(directory, "mail")
	script := "#!/bin/sh\nprintf '%s\\n' \"$@\" > " + shellSingleQuote(argsPath) + "\ncat > " + shellSingleQuote(bodyPath) + "\n"
	if err := os.WriteFile(mailerPath, []byte(script), 0o700); err != nil {
		t.Fatalf("write fake mailer: %v", err)
	}
	t.Setenv("DIRECTORY_ADMIN_EMAIL", "admin@example.com")
	t.Setenv("DIRECTORY_MAIL_COMMAND", mailerPath)

	before := adminGrowthStateRow(t, path)
	stdout.Reset()
	stderr.Reset()
	code = runAdmin(
		[]string{"activity-relay-directory", "admin", "storage", "test-alert", "--format", "json"},
		&stdout, &stderr, now,
	)
	if code != storagecommand.ExitOK || stderr.Len() != 0 {
		t.Fatalf("test-alert = code:%d stdout:%q stderr:%q", code, stdout.String(), stderr.String())
	}
	args, err := os.ReadFile(argsPath)
	if err != nil {
		t.Fatalf("read fake mail args: %v", err)
	}
	body, err := os.ReadFile(bodyPath)
	if err != nil {
		t.Fatalf("read fake mail body: %v", err)
	}
	if string(args) != "-s\nActivity-Relay-Directory storage test\nadmin@example.com\n" {
		t.Fatalf("fake mail argv = %q", string(args))
	}
	for _, required := range []string{"storage state: test", "configured_max_bytes:", "Remediation checklist:"} {
		if !strings.Contains(string(body), required) {
			t.Fatalf("fake mail body missing %q: %s", required, body)
		}
	}
	after := adminGrowthStateRow(t, path)
	if before != after {
		t.Fatalf("test-alert mutated persistent growth state: before=%q after=%q", before, after)
	}
}

func TestAdminStorageRejectsInvalidInvocationAndMissingMailer(t *testing.T) {
	path := createAdminStorageDatabase(t)
	setAdminStorageEnvironment(t, path)
	for _, args := range [][]string{
		{"activity-relay-directory", "admin", "storage"},
		{"activity-relay-directory", "admin", "storage", "unknown"},
		{"activity-relay-directory", "admin", "storage", "status", "--format", "yaml"},
	} {
		var stdout, stderr bytes.Buffer
		if code := runAdmin(args, &stdout, &stderr, time.Now); code != storagecommand.ExitUsage {
			t.Fatalf("runAdmin(%#v) code = %d", args, code)
		}
	}

	t.Setenv("DIRECTORY_ADMIN_EMAIL", "admin@example.com")
	t.Setenv("DIRECTORY_MAIL_COMMAND", filepath.Join(t.TempDir(), "missing-mail"))
	var stdout, stderr bytes.Buffer
	code := runAdmin(
		[]string{"activity-relay-directory", "admin", "storage", "test-alert"},
		&stdout, &stderr, time.Now,
	)
	if code != storagecommand.ExitOperational || !strings.Contains(stderr.String(), "mailer is unavailable") {
		t.Fatalf("missing mailer = code:%d stderr:%q", code, stderr.String())
	}
}

func createAdminStorageDatabase(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "directory.sqlite")
	database, err := initializeDatabase(context.Background(), path)
	if err != nil {
		t.Fatalf("initializeDatabase() error = %v", err)
	}
	if _, err := database.Exec(`PRAGMA wal_checkpoint(TRUNCATE)`); err != nil {
		_ = database.Close()
		t.Fatalf("checkpoint database: %v", err)
	}
	if err := database.Close(); err != nil {
		t.Fatalf("Close() error = %v", err)
	}
	return path
}

func setAdminStorageEnvironment(t *testing.T, path string) {
	t.Helper()
	t.Setenv("DIRECTORY_DATABASE_PATH", path)
	t.Setenv("DIRECTORY_DATABASE_MAX_BYTES", "")
	t.Setenv("DIRECTORY_DATABASE_WARNING_PERCENT", "")
	t.Setenv("DIRECTORY_ADMIN_EMAIL", "")
	t.Setenv("DIRECTORY_MAIL_BACKEND", "")
	t.Setenv("DIRECTORY_MAIL_COMMAND", "")
	t.Setenv("DIRECTORY_MAIL_TIMEOUT_SECONDS", "")
	t.Setenv("DIRECTORY_INACTIVE_RETENTION_DAYS", "0")
}

func adminGrowthStateRow(t *testing.T, path string) string {
	t.Helper()
	database, err := storageSQLite.OpenReadOnly(context.Background(), path)
	if err != nil {
		t.Fatalf("OpenReadOnly() error = %v", err)
	}
	defer database.Close()
	var state string
	var sampled, physical, transition int64
	var lastKind, lastAt, pendingKind, pendingSince, retryAfter any
	var retryAttempt int
	if err := database.QueryRow(`SELECT state,sampled_at_unix,physical_bytes,transition_at_unix,last_email_kind,last_email_at_unix,pending_kind,pending_since_unix,retry_after_unix,retry_attempt FROM storage_growth_state WHERE singleton_id=1`).Scan(
		&state, &sampled, &physical, &transition, &lastKind, &lastAt, &pendingKind, &pendingSince, &retryAfter, &retryAttempt,
	); err != nil {
		t.Fatalf("read storage_growth_state: %v", err)
	}
	return strings.Join([]string{
		state, strconv.FormatInt(sampled, 10), strconv.FormatInt(physical, 10), strconv.FormatInt(transition, 10),
		valueString(lastKind), valueString(lastAt), valueString(pendingKind), valueString(pendingSince), valueString(retryAfter), strconv.Itoa(retryAttempt),
	}, "|")
}

func valueString(value any) string {
	if value == nil {
		return "NULL"
	}
	return strings.TrimSpace(strings.ReplaceAll(strings.ReplaceAll(strings.TrimSpace(toString(value)), "\n", ""), "\r", ""))
}

func toString(value any) string {
	switch typed := value.(type) {
	case string:
		return typed
	case []byte:
		return string(typed)
	case int64:
		return strconv.FormatInt(typed, 10)
	default:
		return "?"
	}
}

func shellSingleQuote(value string) string {
	return "'" + strings.ReplaceAll(value, "'", "'\\''") + "'"
}
