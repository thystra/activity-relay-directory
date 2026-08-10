package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestAdminRetentionDryRunIsReadOnlyAndIdentityFree(t *testing.T) {
	path := filepath.Join(t.TempDir(), "directory.sqlite")
	seedAdminRelay(t, path, 100)
	unregisterAdminRelay(t, path, 200)
	t.Setenv("DIRECTORY_DATABASE_PATH", path)
	t.Setenv("DIRECTORY_INACTIVE_RETENTION_DAYS", "1")

	var stdout, stderr bytes.Buffer
	code := runAdmin(
		[]string{"activity-relay-directory", "admin", "retention", "dry-run", "--format", "json"},
		&stdout,
		&stderr,
		func() time.Time { return time.Unix(200+86400, 0) },
	)
	if code != 0 || stderr.Len() != 0 || !strings.Contains(stdout.String(), `"candidate_count":1`) {
		t.Fatalf("retention dry-run = (%d, %q, %q)", code, stdout.String(), stderr.String())
	}
	if strings.Contains(stdout.String(), cliTestActor) || strings.Contains(stdout.String(), "relay_actor") {
		t.Fatalf("retention dry-run leaked identity: %q", stdout.String())
	}

	database, err := initializeDatabase(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	var relayCount, runCount int
	if err := database.QueryRow(`SELECT COUNT(*) FROM relays WHERE relay_actor=?`, cliTestActor).Scan(&relayCount); err != nil {
		t.Fatal(err)
	}
	if err := database.QueryRow(`SELECT COUNT(*) FROM retention_runs`).Scan(&runCount); err != nil {
		t.Fatal(err)
	}
	if relayCount != 1 || runCount != 0 {
		t.Fatalf("dry-run mutation relayCount=%d runCount=%d", relayCount, runCount)
	}
}

func TestAdminRetentionRejectsInvalidInvocationWithoutOpeningDatabase(t *testing.T) {
	path := filepath.Join(t.TempDir(), "missing.sqlite")
	t.Setenv("DIRECTORY_DATABASE_PATH", path)
	t.Setenv("DIRECTORY_INACTIVE_RETENTION_DAYS", "365")
	for _, arguments := range [][]string{
		{"activity-relay-directory", "admin", "retention"},
		{"activity-relay-directory", "admin", "retention", "run"},
		{"activity-relay-directory", "admin", "retention", "purge"},
		{"activity-relay-directory", "admin", "retention", "dry-run", "--backup", "/tmp/backup.sqlite"},
		{"activity-relay-directory", "admin", "retention", "purge", "--backup", "/tmp/backup.sqlite", "extra"},
	} {
		var stdout, stderr bytes.Buffer
		code := runAdmin(arguments, &stdout, &stderr, time.Now)
		if code != 2 || stdout.Len() != 0 || !strings.Contains(stderr.String(), "usage:") {
			t.Fatalf("runAdmin(%q) = (%d, %q, %q)", arguments, code, stdout.String(), stderr.String())
		}
	}
	if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("invalid retention invocation created database: %v", err)
	}
}

func TestAdminRetentionValidDryRunDoesNotCreateMissingDatabase(t *testing.T) {
	path := filepath.Join(t.TempDir(), "missing.sqlite")
	t.Setenv("DIRECTORY_DATABASE_PATH", path)
	t.Setenv("DIRECTORY_INACTIVE_RETENTION_DAYS", "365")
	var stdout, stderr bytes.Buffer
	code := runAdmin(
		[]string{"activity-relay-directory", "admin", "retention", "dry-run"},
		&stdout,
		&stderr,
		time.Now,
	)
	if code != 1 || stdout.Len() != 0 || stderr.String() != "database initialization failed\n" {
		t.Fatalf("missing-db dry-run = (%d, %q, %q)", code, stdout.String(), stderr.String())
	}
	if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("retention dry-run created missing database: %v", err)
	}
}

func TestAdminRetentionPositivePurgeDoesNotCreateMissingDatabase(t *testing.T) {
	path := filepath.Join(t.TempDir(), "missing.sqlite")
	t.Setenv("DIRECTORY_DATABASE_PATH", path)
	t.Setenv("DIRECTORY_INACTIVE_RETENTION_DAYS", "365")
	var stdout, stderr bytes.Buffer
	code := runAdminWithInput(
		[]string{"activity-relay-directory", "admin", "retention", "purge", "--backup", "/tmp/backup.sqlite", "--yes"},
		strings.NewReader(""),
		&stdout,
		&stderr,
		time.Now,
	)
	if code != 1 || stdout.Len() != 0 || stderr.String() != "database initialization failed\n" {
		t.Fatalf("missing-db purge = (%d, %q, %q)", code, stdout.String(), stderr.String())
	}
	if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("positive retention purge created missing database: %v", err)
	}
}

func TestAdminRetentionPurgeDisabledAtZeroBeforeDatabaseOpen(t *testing.T) {
	path := filepath.Join(t.TempDir(), "missing.sqlite")
	t.Setenv("DIRECTORY_DATABASE_PATH", path)
	t.Setenv("DIRECTORY_INACTIVE_RETENTION_DAYS", "0")
	var stdout, stderr bytes.Buffer
	code := runAdminWithInput(
		[]string{"activity-relay-directory", "admin", "retention", "purge", "--backup", "/tmp/backup.sqlite", "--yes"},
		strings.NewReader(""),
		&stdout,
		&stderr,
		time.Now,
	)
	if code != 2 || stdout.Len() != 0 || !strings.Contains(stderr.String(), "retention is disabled") {
		t.Fatalf("zero-policy purge = (%d, %q, %q)", code, stdout.String(), stderr.String())
	}
	if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("zero-policy purge created database: %v", err)
	}
}

func TestAdminRetentionPurgeRequiresVerifiedBackupAndExactConfirmation(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "directory.sqlite")
	backup := filepath.Join(dir, "pre-retention.sqlite")
	seedAdminRelay(t, path, 100)
	unregisterAdminRelay(t, path, 200)
	checkpointAndCopyAdminDatabase(t, path, backup)
	t.Setenv("DIRECTORY_DATABASE_PATH", path)
	t.Setenv("DIRECTORY_INACTIVE_RETENTION_DAYS", "1")
	observed := func() time.Time { return time.Unix(200+86400, 0) }

	var stdout, stderr bytes.Buffer
	code := runAdminWithInput(
		[]string{"activity-relay-directory", "admin", "retention", "purge", "--backup", backup},
		strings.NewReader("wrong\n"),
		&stdout,
		&stderr,
		observed,
	)
	if code != 2 || stdout.Len() != 0 || !strings.Contains(stderr.String(), "confirmation failed") {
		t.Fatalf("unconfirmed purge = (%d, %q, %q)", code, stdout.String(), stderr.String())
	}
	assertAdminRelayCount(t, path, 1)

	stdout.Reset()
	stderr.Reset()
	code = runAdminWithInput(
		[]string{"activity-relay-directory", "admin", "retention", "purge", "--backup", backup, "--format", "json"},
		strings.NewReader("PURGE 1\n"),
		&stdout,
		&stderr,
		observed,
	)
	if code != 0 || !strings.Contains(stdout.String(), `"purged_relays":1`) ||
		!strings.Contains(stderr.String(), `Type "PURGE 1"`) {
		t.Fatalf("confirmed purge = (%d, %q, %q)", code, stdout.String(), stderr.String())
	}
	if strings.Contains(stdout.String(), cliTestActor) {
		t.Fatalf("purge output leaked identity: %q", stdout.String())
	}
	assertAdminRelayCount(t, path, 0)

	database, err := initializeDatabase(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	var runCount, purged, days int
	var digest string
	if err := database.QueryRow(`SELECT retention_days,purged_relays,backup_sha256 FROM retention_runs ORDER BY retention_run_id DESC LIMIT 1`).Scan(&days, &purged, &digest); err != nil {
		t.Fatal(err)
	}
	if err := database.QueryRow(`SELECT COUNT(*) FROM retention_runs`).Scan(&runCount); err != nil {
		t.Fatal(err)
	}
	wantDigest := fileSHA256(t, backup)
	if runCount != 1 || days != 1 || purged != 1 || digest != wantDigest {
		t.Fatalf("retention audit count=%d days=%d purged=%d digest=%q want=%q", runCount, days, purged, digest, wantDigest)
	}
}

func TestAdminRetentionPurgeReverifiesBackupAfterConfirmation(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "directory.sqlite")
	backup := filepath.Join(dir, "pre-retention.sqlite")
	seedAdminRelay(t, path, 100)
	unregisterAdminRelay(t, path, 200)
	checkpointAndCopyAdminDatabase(t, path, backup)
	t.Setenv("DIRECTORY_DATABASE_PATH", path)
	t.Setenv("DIRECTORY_INACTIVE_RETENTION_DAYS", "1")

	input := &callbackReader{
		reader: strings.NewReader("PURGE 1\n"),
		beforeFirstRead: func() {
			file, err := os.OpenFile(backup, os.O_WRONLY|os.O_APPEND, 0)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := file.Write([]byte{0}); err != nil {
				_ = file.Close()
				t.Fatal(err)
			}
			if err := file.Close(); err != nil {
				t.Fatal(err)
			}
		},
	}
	var stdout, stderr bytes.Buffer
	code := runAdminWithInput(
		[]string{"activity-relay-directory", "admin", "retention", "purge", "--backup", backup},
		input,
		&stdout,
		&stderr,
		func() time.Time { return time.Unix(200+86400, 0) },
	)
	if code != 1 || stdout.Len() != 0 || !strings.Contains(stderr.String(), "backup changed after confirmation") {
		t.Fatalf("changed-backup purge = (%d, %q, %q)", code, stdout.String(), stderr.String())
	}
	assertAdminRelayCount(t, path, 1)

	database, err := initializeDatabase(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	var runs int
	if err := database.QueryRow(`SELECT COUNT(*) FROM retention_runs`).Scan(&runs); err != nil {
		t.Fatal(err)
	}
	if runs != 0 {
		t.Fatalf("changed backup created retention audit rows = %d", runs)
	}
}

func TestAdminRetentionPurgeRejectsBackupFromDifferentDatabase(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "directory.sqlite")
	other := filepath.Join(dir, "other.sqlite")
	seedAdminRelay(t, path, 100)
	unregisterAdminRelay(t, path, 200)
	seedAdminRelay(t, other, 100)
	checkpointAdminDatabase(t, other)
	t.Setenv("DIRECTORY_DATABASE_PATH", path)
	t.Setenv("DIRECTORY_INACTIVE_RETENTION_DAYS", "1")

	var stdout, stderr bytes.Buffer
	code := runAdminWithInput(
		[]string{"activity-relay-directory", "admin", "retention", "purge", "--backup", other, "--yes"},
		strings.NewReader(""),
		&stdout,
		&stderr,
		func() time.Time { return time.Unix(200+86400, 0) },
	)
	if code != 1 || stdout.Len() != 0 || stderr.String() != "verified pre-retention backup requirement failed\n" {
		t.Fatalf("different-db backup = (%d, %q, %q)", code, stdout.String(), stderr.String())
	}
	assertAdminRelayCount(t, path, 1)
}

type callbackReader struct {
	reader          io.Reader
	beforeFirstRead func()
	read            bool
}

func (reader *callbackReader) Read(buffer []byte) (int, error) {
	if !reader.read {
		reader.read = true
		if reader.beforeFirstRead != nil {
			reader.beforeFirstRead()
		}
	}
	return reader.reader.Read(buffer)
}

func checkpointAdminDatabase(t *testing.T, path string) {
	t.Helper()
	database, err := initializeDatabase(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := database.Exec(`PRAGMA wal_checkpoint(TRUNCATE)`); err != nil {
		_ = database.Close()
		t.Fatal(err)
	}
	if err := database.Close(); err != nil {
		t.Fatal(err)
	}
}

func checkpointAndCopyAdminDatabase(t *testing.T, path, backup string) {
	t.Helper()
	checkpointAdminDatabase(t, path)
	copyAdminFile(t, path, backup)
}

func copyAdminFile(t *testing.T, source, destination string) {
	t.Helper()
	in, err := os.Open(source)
	if err != nil {
		t.Fatal(err)
	}
	defer in.Close()
	out, err := os.OpenFile(destination, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := io.Copy(out, in); err != nil {
		_ = out.Close()
		t.Fatal(err)
	}
	if err := out.Sync(); err != nil {
		_ = out.Close()
		t.Fatal(err)
	}
	if err := out.Close(); err != nil {
		t.Fatal(err)
	}
}

func assertAdminRelayCount(t *testing.T, path string, want int) {
	t.Helper()
	database, err := initializeDatabase(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	var count int
	if err := database.QueryRow(`SELECT COUNT(*) FROM relays WHERE relay_actor=?`, cliTestActor).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != want {
		t.Fatalf("relay count = %d, want %d", count, want)
	}
}

func fileSHA256(t *testing.T, path string) string {
	t.Helper()
	file, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	digest := sha256.New()
	if _, err := io.Copy(digest, file); err != nil {
		t.Fatal(err)
	}
	return hex.EncodeToString(digest.Sum(nil))
}
