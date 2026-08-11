package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/thystra/activity-relay-directory/internal/storage"
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
	assertPragmaInteger(t, database, "wal_autocheckpoint", storage.DatabaseWALAutoCheckpointPages)
	assertPragmaInteger(t, database, "journal_size_limit", int(storage.DatabaseJournalSizeLimitBytes))
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

func TestOpenReadOnlyRequiresExistingDatabaseAndEnforcesQueryOnly(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "missing.sqlite")
	database, err := OpenReadOnly(context.Background(), missing)
	if database != nil {
		_ = database.Close()
	}
	if !errors.Is(err, ErrDatabasePath) {
		t.Fatalf("OpenReadOnly(missing) error = %v, want ErrDatabasePath", err)
	}
	if _, err := os.Stat(missing); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("OpenReadOnly(missing) created a file: %v", err)
	}

	path := filepath.Join(t.TempDir(), "directory.sqlite")
	writer, err := Open(context.Background(), path)
	if err != nil {
		t.Fatalf("Open(writer) error = %v", err)
	}
	if err := Migrate(context.Background(), writer); err != nil {
		_ = writer.Close()
		t.Fatalf("Migrate() error = %v", err)
	}
	t.Cleanup(func() { _ = writer.Close() })

	reader, err := OpenReadOnly(context.Background(), path)
	if err != nil {
		t.Fatalf("OpenReadOnly() error = %v", err)
	}
	t.Cleanup(func() { _ = reader.Close() })
	assertPragmaInteger(t, reader, "query_only", 1)
	assertPragmaInteger(t, reader, "foreign_keys", 1)
	assertPragmaInteger(t, reader, "busy_timeout", busyTimeoutMillis)

	if _, err := reader.Exec(
		`UPDATE directory_policy SET enrollment_open = 1 WHERE singleton = 1`,
	); err == nil {
		t.Fatal("read-only connection accepted a database write")
	}
	version, err := SchemaVersion(context.Background(), reader)
	if err != nil || version != CurrentSchemaVersion {
		t.Fatalf("SchemaVersion(read-only) = (%d, %v)", version, err)
	}
}

func TestOpenReadOnlyDoesNotMigrateOlderSchema(t *testing.T) {
	path := filepath.Join(t.TempDir(), "directory.sqlite")
	writer, err := Open(context.Background(), path)
	if err != nil {
		t.Fatalf("Open(writer) error = %v", err)
	}
	applyMigrationsThrough(t, writer, 4)
	if err := writer.Close(); err != nil {
		t.Fatalf("Close(writer) error = %v", err)
	}

	reader, err := OpenReadOnly(context.Background(), path)
	if err != nil {
		t.Fatalf("OpenReadOnly() error = %v", err)
	}
	defer reader.Close()
	if err := CheckReady(context.Background(), reader); !errors.Is(err, ErrDatabaseNotReady) {
		t.Fatalf("CheckReady(version 4) error = %v, want ErrDatabaseNotReady", err)
	}
	version, err := SchemaVersion(context.Background(), reader)
	if err != nil || version != 4 {
		t.Fatalf("SchemaVersion(read-only old database) = (%d, %v), want (4, nil)", version, err)
	}
	var prunedColumnCount int
	if err := reader.QueryRow(
		`SELECT COUNT(*) FROM pragma_table_info('relays') WHERE name = 'pruned_at_unix'`,
	).Scan(&prunedColumnCount); err != nil {
		t.Fatalf("inspect old schema: %v", err)
	}
	if prunedColumnCount != 0 {
		t.Fatalf("read-only open applied migration 5: pruned column count = %d", prunedColumnCount)
	}
}

func TestOpenGuardedAppliesMaxPageCountToEveryPooledConnection(t *testing.T) {
	path := filepath.Join(t.TempDir(), "directory.sqlite")
	bootstrap, err := Open(context.Background(), path)
	if err != nil {
		t.Fatalf("Open(bootstrap) error = %v", err)
	}
	if err := Migrate(context.Background(), bootstrap); err != nil {
		_ = bootstrap.Close()
		t.Fatalf("Migrate(bootstrap) error = %v", err)
	}
	var pageSize int64
	if err := bootstrap.QueryRow(`PRAGMA page_size`).Scan(&pageSize); err != nil || pageSize <= 0 {
		_ = bootstrap.Close()
		t.Fatalf("bootstrap page_size = %d, %v", pageSize, err)
	}
	if err := bootstrap.Close(); err != nil {
		t.Fatalf("Close(bootstrap) error = %v", err)
	}

	maxBytes := pageSize * 4096
	database, desired, _, err := OpenGuarded(context.Background(), path, maxBytes)
	if err != nil {
		t.Fatalf("OpenGuarded() error = %v", err)
	}
	t.Cleanup(func() { _ = database.Close() })

	var pageCount int64
	if err := database.QueryRow(`PRAGMA page_count`).Scan(&pageCount); err != nil {
		t.Fatalf("page_count: %v", err)
	}
	expected := desired
	if pageCount > expected {
		expected = pageCount
	}

	connections := make([]*sql.Conn, 0, 4)
	defer func() {
		for _, connection := range connections {
			_ = connection.Close()
		}
	}()
	for index := 0; index < 4; index++ {
		connection, err := database.Conn(context.Background())
		if err != nil {
			t.Fatalf("Conn(%d) error = %v", index, err)
		}
		connections = append(connections, connection)

		var effective int64
		if err := connection.QueryRowContext(
			context.Background(), `PRAGMA max_page_count`,
		).Scan(&effective); err != nil {
			t.Fatalf("connection %d max_page_count: %v", index, err)
		}
		if effective != expected {
			t.Fatalf(
				"connection %d max_page_count = %d, want %d",
				index, effective, expected,
			)
		}
		var cacheSpill int
		if err := connection.QueryRowContext(
			context.Background(), `PRAGMA cache_spill`,
		).Scan(&cacheSpill); err != nil {
			t.Fatalf("connection %d cache_spill: %v", index, err)
		}
		if cacheSpill != 0 {
			t.Fatalf("connection %d cache_spill = %d, want 0", index, cacheSpill)
		}
	}
}

func TestOpenMigrationGuardedKeepsCacheSpillEnabled(t *testing.T) {
	path := filepath.Join(t.TempDir(), "directory.sqlite")
	bootstrap, err := Open(context.Background(), path)
	if err != nil {
		t.Fatalf("Open(bootstrap) error = %v", err)
	}
	if err := Migrate(context.Background(), bootstrap); err != nil {
		_ = bootstrap.Close()
		t.Fatalf("Migrate(bootstrap) error = %v", err)
	}
	var pageSize int64
	if err := bootstrap.QueryRow(`PRAGMA page_size`).Scan(&pageSize); err != nil || pageSize <= 0 {
		_ = bootstrap.Close()
		t.Fatalf("bootstrap page_size = %d, %v", pageSize, err)
	}
	if err := bootstrap.Close(); err != nil {
		t.Fatalf("Close(bootstrap) error = %v", err)
	}

	database, desired, _, err := OpenMigrationGuarded(
		context.Background(), path, pageSize*4096,
	)
	if err != nil {
		t.Fatalf("OpenMigrationGuarded() error = %v", err)
	}
	t.Cleanup(func() { _ = database.Close() })

	var pageCount int64
	if err := database.QueryRow(`PRAGMA page_count`).Scan(&pageCount); err != nil {
		t.Fatalf("page_count: %v", err)
	}
	expected := desired
	if pageCount > expected {
		expected = pageCount
	}
	connections := make([]*sql.Conn, 0, 4)
	defer func() {
		for _, connection := range connections {
			_ = connection.Close()
		}
	}()
	for index := 0; index < 4; index++ {
		connection, err := database.Conn(context.Background())
		if err != nil {
			t.Fatalf("Conn(%d) error = %v", index, err)
		}
		connections = append(connections, connection)
		var effective int64
		if err := connection.QueryRowContext(
			context.Background(), `PRAGMA max_page_count`,
		).Scan(&effective); err != nil {
			t.Fatalf("connection %d max_page_count: %v", index, err)
		}
		if effective != expected {
			t.Fatalf(
				"connection %d max_page_count = %d, want %d",
				index, effective, expected,
			)
		}
		var cacheSpill int
		if err := connection.QueryRowContext(
			context.Background(), `PRAGMA cache_spill`,
		).Scan(&cacheSpill); err != nil {
			t.Fatalf("connection %d cache_spill: %v", index, err)
		}
		if cacheSpill == 0 {
			t.Fatalf("connection %d cache_spill = 0, want enabled", index)
		}
	}
}
