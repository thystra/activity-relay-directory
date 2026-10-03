package main

import (
	"bytes"
	"context"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestAdminExportUsesReadOnlyDirectoryProjection(t *testing.T) {
	path := filepath.Join(t.TempDir(), "directory.sqlite")
	database, err := initializeDatabase(context.Background(), path)
	if err != nil {
		t.Fatalf("initializeDatabase() error = %v", err)
	}
	if err := database.Close(); err != nil {
		t.Fatalf("close seed database: %v", err)
	}
	t.Setenv("DIRECTORY_DATABASE_PATH", path)

	var stdout, stderr bytes.Buffer
	code := runAdmin(
		[]string{
			"activity-relay-directory", "admin", "export",
			"--scope", "active",
			"--format", "hosts",
		},
		&stdout,
		&stderr,
		func() time.Time { return time.Unix(2_000_000, 0).UTC() },
	)
	if code != 0 || stdout.Len() != 0 || stderr.Len() != 0 {
		t.Fatalf("admin export = (%d, %q, %q)", code, stdout.String(), stderr.String())
	}
}

func TestAdminExportRejectsInvalidScope(t *testing.T) {
	var stdout, stderr bytes.Buffer
	code := runAdmin(
		[]string{
			"activity-relay-directory", "admin", "export",
			"--scope", "private",
		},
		&stdout,
		&stderr,
		func() time.Time { return time.Unix(2_000_000, 0).UTC() },
	)
	if code != 2 || stdout.Len() != 0 ||
		!strings.Contains(stderr.String(), "activity-relay-directory admin export") {
		t.Fatalf("invalid admin export = (%d, %q, %q)", code, stdout.String(), stderr.String())
	}
}
