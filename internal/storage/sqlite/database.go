// Package sqlite provides single-node SQLite migrations, readiness, relay
// state repositories, and opaque replay reservations. State and replay stores
// remain disconnected from HTTP handlers until later security tranches.
package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"

	"github.com/thystra/activity-relay-directory/internal/storage"

	_ "modernc.org/sqlite"
)

const (
	driverName          = "sqlite"
	databasePermissions = 0o600
	busyTimeoutMillis   = 5000
)

var ErrDatabasePath = errors.New("SQLite database path is invalid")

// Open opens an absolute, nonsymlink SQLite database file with per-connection
// foreign-key, WAL, synchronous, and busy-timeout settings. New files are
// created with owner-only permissions; insecure existing files are rejected.
func Open(ctx context.Context, path string) (*sql.DB, error) {
	if ctx == nil || path == "" || !filepath.IsAbs(path) {
		return nil, ErrDatabasePath
	}
	if err := secureDatabaseFile(path); err != nil {
		return nil, err
	}
	return openDatabasePool(ctx, sqliteDSN(path))
}

// OpenGuarded opens the steady-state writable runtime pool with the Tranche 17
// max_page_count backstop and cache_spill=OFF encoded in the DSN so every
// database/sql connection receives the same page ceiling and retains dirty
// pages until commit. max_page_count and cache_spill are connection-local in
// SQLite; setting either once through *sql.DB is not sufficient for a pool.
func OpenGuarded(
	ctx context.Context,
	path string,
	maxBytes int64,
) (*sql.DB, int64, int64, error) {
	return openGuarded(ctx, path, maxBytes, false)
}

// OpenMigrationGuarded opens the pre-runtime migration pool with the same
// per-connection max_page_count ceiling while leaving SQLite's default cache
// spilling enabled. Table-copy migrations may dirty a database-scale page set;
// retaining that entire set in memory merely to bound transient WAL growth would
// trade a filesystem safeguard for avoidable memory pressure. The caller must
// close this pool after migration and reopen with OpenGuarded before serving or
// admitting steady-state mutations.
func OpenMigrationGuarded(
	ctx context.Context,
	path string,
	maxBytes int64,
) (*sql.DB, int64, int64, error) {
	return openGuarded(ctx, path, maxBytes, true)
}

func openGuarded(
	ctx context.Context,
	path string,
	maxBytes int64,
	migration bool,
) (*sql.DB, int64, int64, error) {
	if ctx == nil || path == "" || !filepath.IsAbs(path) {
		return nil, 0, 0, ErrDatabasePath
	}
	if err := secureDatabaseFile(path); err != nil {
		return nil, 0, 0, err
	}

	bootstrap, err := openDatabasePool(ctx, sqliteDSN(path))
	if err != nil {
		return nil, 0, 0, err
	}
	bootstrap.SetMaxOpenConns(1)
	bootstrap.SetMaxIdleConns(1)
	desiredPages, effectivePages, mainLimit, err := databaseGrowthPageLimits(
		ctx, bootstrap, maxBytes,
	)
	if closeErr := bootstrap.Close(); err == nil && closeErr != nil {
		err = fmt.Errorf("close SQLite growth bootstrap: %w", closeErr)
	}
	if err != nil {
		return nil, 0, 0, err
	}

	dsn := sqliteDSNWithMaxPageCount(path, effectivePages)
	if migration {
		dsn = sqliteMigrationDSNWithMaxPageCount(path, effectivePages)
	}
	database, err := openDatabasePool(ctx, dsn)
	if err != nil {
		return nil, 0, 0, err
	}
	if err := verifyDatabaseGrowthConnectionPolicy(
		ctx, database, maxBytes, desiredPages, mainLimit, migration,
	); err != nil {
		_ = database.Close()
		return nil, 0, 0, err
	}
	return database, desiredPages, mainLimit, nil
}

func openDatabasePool(ctx context.Context, dsn string) (*sql.DB, error) {
	database, err := sql.Open(driverName, dsn)
	if err != nil {
		return nil, fmt.Errorf("open SQLite database: %w", err)
	}
	database.SetMaxOpenConns(4)
	database.SetMaxIdleConns(4)

	if err := database.PingContext(ctx); err != nil {
		_ = database.Close()
		return nil, fmt.Errorf("connect to SQLite database: %w", err)
	}
	return database, nil
}

// OpenReadOnly opens an existing secure database through a single query-only
// connection. It never creates a database file, applies migrations, or enables
// a write-capable journal mode. Local read-only administrative commands use
// this boundary so inspection cannot mutate durable state.
func OpenReadOnly(ctx context.Context, path string) (*sql.DB, error) {
	if ctx == nil || path == "" || !filepath.IsAbs(path) {
		return nil, ErrDatabasePath
	}
	if err := requireSecureExistingDatabaseFile(path); err != nil {
		return nil, err
	}

	database, err := sql.Open(driverName, sqliteReadOnlyDSN(path))
	if err != nil {
		return nil, fmt.Errorf("open read-only SQLite database: %w", err)
	}
	database.SetMaxOpenConns(1)
	database.SetMaxIdleConns(1)

	if err := database.PingContext(ctx); err != nil {
		_ = database.Close()
		return nil, fmt.Errorf("connect to read-only SQLite database: %w", err)
	}
	var queryOnly int
	if err := database.QueryRowContext(ctx, `PRAGMA query_only`).Scan(&queryOnly); err != nil || queryOnly != 1 {
		_ = database.Close()
		if err != nil {
			return nil, fmt.Errorf("verify read-only SQLite database: %w", err)
		}
		return nil, errors.New("verify read-only SQLite database: query_only is disabled")
	}
	return database, nil
}

// openImmutableReadOnly opens a secure standalone SQLite snapshot without
// creating or consulting WAL/shared-memory sidecars. It is reserved for backup
// verification; normal read-only administration must continue to observe the
// live database and therefore uses OpenReadOnly.
func openImmutableReadOnly(ctx context.Context, path string) (*sql.DB, error) {
	if ctx == nil || path == "" || !filepath.IsAbs(path) {
		return nil, ErrDatabasePath
	}
	if err := requireSecureExistingDatabaseFile(path); err != nil {
		return nil, err
	}

	database, err := sql.Open(driverName, sqliteImmutableReadOnlyDSN(path))
	if err != nil {
		return nil, fmt.Errorf("open immutable read-only SQLite database: %w", err)
	}
	database.SetMaxOpenConns(1)
	database.SetMaxIdleConns(1)

	if err := database.PingContext(ctx); err != nil {
		_ = database.Close()
		return nil, fmt.Errorf("connect to immutable read-only SQLite database: %w", err)
	}
	var queryOnly int
	if err := database.QueryRowContext(ctx, `PRAGMA query_only`).Scan(&queryOnly); err != nil || queryOnly != 1 {
		_ = database.Close()
		if err != nil {
			return nil, fmt.Errorf("verify immutable read-only SQLite database: %w", err)
		}
		return nil, errors.New("verify immutable read-only SQLite database: query_only is disabled")
	}
	return database, nil
}

func secureDatabaseFile(path string) error {
	information, err := os.Lstat(path)
	switch {
	case err == nil:
		return validateSecureDatabaseInformation(information)
	case !errors.Is(err, os.ErrNotExist):
		return fmt.Errorf("inspect SQLite database: %w", err)
	}

	file, err := os.OpenFile(
		path,
		os.O_CREATE|os.O_EXCL|os.O_RDWR,
		databasePermissions,
	)
	if err != nil {
		if errors.Is(err, os.ErrExist) {
			return secureDatabaseFile(path)
		}
		return fmt.Errorf("create SQLite database: %w", err)
	}
	if err := file.Close(); err != nil {
		return fmt.Errorf("close new SQLite database: %w", err)
	}
	return nil
}

func requireSecureExistingDatabaseFile(path string) error {
	information, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return ErrDatabasePath
	}
	if err != nil {
		return fmt.Errorf("inspect SQLite database: %w", err)
	}
	return validateSecureDatabaseInformation(information)
}

func validateSecureDatabaseInformation(information os.FileInfo) error {
	if information == nil || information.Mode()&os.ModeSymlink != 0 ||
		!information.Mode().IsRegular() || information.Mode().Perm()&0o077 != 0 {
		return ErrDatabasePath
	}
	return nil
}

func sqliteDSN(path string) string {
	return sqliteDSNWithMaxPageCount(path, 0)
}

func sqliteDSNWithMaxPageCount(path string, maxPageCount int64) string {
	return sqliteGrowthDSN(path, maxPageCount, false)
}

func sqliteMigrationDSNWithMaxPageCount(path string, maxPageCount int64) string {
	return sqliteGrowthDSN(path, maxPageCount, true)
}

func sqliteGrowthDSN(path string, maxPageCount int64, migration bool) string {
	dsn := &url.URL{Scheme: "file", Path: filepath.ToSlash(path)}
	parameters := dsn.Query()
	parameters.Set("_txlock", "immediate")
	parameters.Add("_pragma", fmt.Sprintf("busy_timeout(%d)", busyTimeoutMillis))
	parameters.Add("_pragma", "foreign_keys(ON)")
	parameters.Add("_pragma", "journal_mode(WAL)")
	parameters.Add("_pragma", fmt.Sprintf("wal_autocheckpoint(%d)", storage.DatabaseWALAutoCheckpointPages))
	parameters.Add("_pragma", fmt.Sprintf("journal_size_limit(%d)", storage.DatabaseJournalSizeLimitBytes))
	if maxPageCount > 0 {
		if migration {
			parameters.Add("_pragma", "cache_spill(ON)")
		} else {
			parameters.Add("_pragma", "cache_spill(OFF)")
		}
		parameters.Add("_pragma", fmt.Sprintf("max_page_count(%d)", maxPageCount))
	}
	parameters.Add("_pragma", "synchronous(NORMAL)")
	dsn.RawQuery = parameters.Encode()
	return dsn.String()
}

func sqliteReadOnlyDSN(path string) string {
	dsn := &url.URL{Scheme: "file", Path: filepath.ToSlash(path)}
	parameters := dsn.Query()
	parameters.Set("mode", "ro")
	parameters.Add("_pragma", fmt.Sprintf("busy_timeout(%d)", busyTimeoutMillis))
	parameters.Add("_pragma", "foreign_keys(ON)")
	parameters.Add("_pragma", "query_only(ON)")
	dsn.RawQuery = parameters.Encode()
	return dsn.String()
}

func sqliteImmutableReadOnlyDSN(path string) string {
	dsn := &url.URL{Scheme: "file", Path: filepath.ToSlash(path)}
	parameters := dsn.Query()
	parameters.Set("mode", "ro")
	parameters.Set("immutable", "1")
	parameters.Add("_pragma", "foreign_keys(ON)")
	parameters.Add("_pragma", "query_only(ON)")
	dsn.RawQuery = parameters.Encode()
	return dsn.String()
}
