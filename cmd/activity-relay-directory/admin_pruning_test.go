package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	storageContract "github.com/thystra/activity-relay-directory/internal/storage"
)

func TestAdminPruningDryRunIsBoundedAndReadOnly(t *testing.T) {
	path := filepath.Join(t.TempDir(), "directory.sqlite")
	seedAdminRelay(t, path, 100)
	t.Setenv("DIRECTORY_DATABASE_PATH", path)
	observed := time.Unix(100+int64(storageContract.DeadBefore/time.Second), 0)

	var stdout, stderr bytes.Buffer
	code := runAdmin(
		[]string{
			"activity-relay-directory", "admin", "pruning", "dry-run",
			"--limit", "1",
			"--format", "json",
		},
		&stdout,
		&stderr,
		func() time.Time { return observed },
	)
	if code != 0 || stderr.Len() != 0 {
		t.Fatalf("dry-run = (%d, %q, %q)", code, stdout.String(), stderr.String())
	}
	var document struct {
		Schema       string `json:"schema"`
		Kind         string `json:"kind"`
		ObservedUnix int64  `json:"observed_at_unix"`
		Candidates   []struct {
			RelayActor string `json:"relay_actor"`
		} `json:"candidates"`
	}
	if err := json.Unmarshal(stdout.Bytes(), &document); err != nil {
		t.Fatalf("decode dry-run JSON: %v", err)
	}
	if document.Schema != "activity-relay-directory.pruning-admin.v1" ||
		document.Kind != "soft_pruning_dry_run" ||
		document.ObservedUnix != observed.Unix() || len(document.Candidates) != 1 ||
		document.Candidates[0].RelayActor != cliTestActor {
		t.Fatalf("dry-run document = %#v", document)
	}

	database, err := initializeDatabase(context.Background(), path)
	if err != nil {
		t.Fatalf("initializeDatabase() error = %v", err)
	}
	defer database.Close()
	var lifecycle string
	var prunedAt any
	var pruneEvents int
	if err := database.QueryRow(
		`SELECT lifecycle_state, pruned_at_unix FROM relays WHERE relay_actor = ?`,
		cliTestActor,
	).Scan(&lifecycle, &prunedAt); err != nil {
		t.Fatalf("read relay after dry-run: %v", err)
	}
	if err := database.QueryRow(
		`SELECT COUNT(*) FROM relay_events WHERE relay_actor = ? AND event_kind = 'relay_pruned'`,
		cliTestActor,
	).Scan(&pruneEvents); err != nil {
		t.Fatalf("count prune events: %v", err)
	}
	if lifecycle != "registered" || prunedAt != nil || pruneEvents != 0 {
		t.Fatalf("dry-run mutated relay = lifecycle:%q pruned:%v events:%d", lifecycle, prunedAt, pruneEvents)
	}
}

func TestAdminPruningRejectsInvalidInvocationWithoutOpeningDatabase(t *testing.T) {
	path := filepath.Join(t.TempDir(), "directory.sqlite")
	t.Setenv("DIRECTORY_DATABASE_PATH", path)
	for _, arguments := range [][]string{
		{"activity-relay-directory", "admin", "pruning"},
		{"activity-relay-directory", "admin", "pruning", "run"},
		{"activity-relay-directory", "admin", "pruning", "dry-run", "--limit", "101"},
		{"activity-relay-directory", "admin", "pruning", "dry-run", "--after-last-seen", "100"},
	} {
		var stdout, stderr bytes.Buffer
		code := runAdmin(arguments, &stdout, &stderr, time.Now)
		if code != 2 || stdout.Len() != 0 || !strings.Contains(stderr.String(), "usage:") {
			t.Fatalf("runAdmin(%q) = (%d, %q, %q)", arguments, code, stdout.String(), stderr.String())
		}
	}
	if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("invalid pruning invocation created database: %v", err)
	}
}

func TestAdminPruningValidDryRunDoesNotCreateMissingDatabase(t *testing.T) {
	path := filepath.Join(t.TempDir(), "missing.sqlite")
	t.Setenv("DIRECTORY_DATABASE_PATH", path)

	var stdout, stderr bytes.Buffer
	code := runAdmin(
		[]string{
			"activity-relay-directory", "admin", "pruning", "dry-run",
			"--limit", "1",
		},
		&stdout,
		&stderr,
		time.Now,
	)
	if code != 1 || stdout.Len() != 0 || stderr.String() != "database initialization failed\n" {
		t.Fatalf("dry-run on missing database = (%d, %q, %q)", code, stdout.String(), stderr.String())
	}
	if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("dry-run created missing database: %v", err)
	}
}
