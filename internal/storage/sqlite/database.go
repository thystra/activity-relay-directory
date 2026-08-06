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

	dsn := sqliteDSN(path)
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
	dsn := &url.URL{Scheme: "file", Path: filepath.ToSlash(path)}
	parameters := dsn.Query()
	parameters.Set("_txlock", "immediate")
	parameters.Add("_pragma", fmt.Sprintf("busy_timeout(%d)", busyTimeoutMillis))
	parameters.Add("_pragma", "foreign_keys(ON)")
	parameters.Add("_pragma", "journal_mode(WAL)")
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
