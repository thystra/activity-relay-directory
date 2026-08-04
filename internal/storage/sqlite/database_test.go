package sqlite

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

func TestOpenCreatesSecureDatabaseWithRequiredPragmas(t *testing.T) {
	path := filepath.Join(t.TempDir(), "directory.sqlite")
	database, err := Open(context.Background(), path)
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	t.Cleanup(func() { _ = database.Close() })

	information, err := os.Stat(path)
	if err != nil {
		t.Fatalf("Stat() error = %v", err)
	}
	if got := information.Mode().Perm(); got != databasePermissions {
		t.Fatalf("database permissions = %#o, want %#o", got, databasePermissions)
	}

	assertPragmaInteger(t, database, "foreign_keys", 1)
	assertPragmaInteger(t, database, "busy_timeout", busyTimeoutMillis)
	assertPragmaInteger(t, database, "synchronous", 1)

	var journalMode string
	if err := database.QueryRow(`PRAGMA journal_mode`).Scan(&journalMode); err != nil {
		t.Fatalf("read journal_mode: %v", err)
	}
	if journalMode != "wal" {
		t.Fatalf("journal_mode = %q, want wal", journalMode)
	}
}

func TestOpenRejectsUnsafePaths(t *testing.T) {
	tests := []struct {
		name string
		ctx  context.Context
		path string
	}{
		{name: "nil context", path: filepath.Join(t.TempDir(), "nil.sqlite")},
		{name: "empty path", ctx: context.Background()},
		{name: "relative path", ctx: context.Background(), path: "directory.sqlite"},
		{name: "directory", ctx: context.Background(), path: t.TempDir()},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			database, err := Open(test.ctx, test.path)
			if database != nil {
				_ = database.Close()
			}
			if !errors.Is(err, ErrDatabasePath) {
				t.Fatalf("Open() error = %v, want ErrDatabasePath", err)
			}
		})
	}
}

func TestOpenRejectsInsecureFilePermissions(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Unix permission semantics are required")
	}

	path := filepath.Join(t.TempDir(), "directory.sqlite")
	if err := os.WriteFile(path, nil, 0o600); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}
	if err := os.Chmod(path, 0o640); err != nil {
		t.Fatalf("Chmod() error = %v", err)
	}

	database, err := Open(context.Background(), path)
	if database != nil {
		_ = database.Close()
	}
	if !errors.Is(err, ErrDatabasePath) {
		t.Fatalf("Open() error = %v, want ErrDatabasePath", err)
	}
}

func TestOpenRejectsSymbolicLink(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symbolic-link privileges vary on Windows")
	}

	directory := t.TempDir()
	target := filepath.Join(directory, "target.sqlite")
	if err := os.WriteFile(target, nil, databasePermissions); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}
	link := filepath.Join(directory, "link.sqlite")
	if err := os.Symlink(target, link); err != nil {
		t.Fatalf("Symlink() error = %v", err)
	}

	database, err := Open(context.Background(), link)
	if database != nil {
		_ = database.Close()
	}
	if !errors.Is(err, ErrDatabasePath) {
		t.Fatalf("Open() error = %v, want ErrDatabasePath", err)
	}
}
