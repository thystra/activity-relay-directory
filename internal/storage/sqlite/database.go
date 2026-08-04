// Package sqlite provides the single-node SQLite persistence contract. It is
// not connected to the directory HTTP server until later storage tranches.
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

func secureDatabaseFile(path string) error {
	information, err := os.Lstat(path)
	switch {
	case err == nil:
		if information.Mode()&os.ModeSymlink != 0 || !information.Mode().IsRegular() ||
			information.Mode().Perm()&0o077 != 0 {
			return ErrDatabasePath
		}
		return nil
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
