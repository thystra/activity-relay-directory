package sqlite

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"embed"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"time"
)

const CurrentSchemaVersion = 4

var (
	ErrMigrationConfiguration = errors.New("SQLite migration configuration is invalid")
	ErrMigrationDrift         = errors.New("SQLite migration history does not match embedded migrations")
	ErrMigrationTooNew        = errors.New("SQLite database schema is newer than this binary")
	ErrDatabaseNotReady       = errors.New("SQLite database is not ready")
)

//go:embed migrations/*.sql
var migrationFiles embed.FS

type migration struct {
	version int
	name    string
	sql     string
	sha256  string
}

// CheckReady verifies that the database is reachable and records exactly the
// schema version supported by this binary. Detailed database errors remain
// internal and must not be returned by public readiness handlers.
func CheckReady(ctx context.Context, database *sql.DB) error {
	if ctx == nil || database == nil {
		return ErrDatabaseNotReady
	}
	if err := database.PingContext(ctx); err != nil {
		return fmt.Errorf("%w: ping", ErrDatabaseNotReady)
	}
	version, err := SchemaVersion(ctx, database)
	if err != nil {
		return fmt.Errorf("%w: schema", ErrDatabaseNotReady)
	}
	if version != CurrentSchemaVersion {
		return ErrDatabaseNotReady
	}
	return nil
}

type appliedMigration struct {
	name   string
	sha256 string
}

var migrationManifest = []struct {
	version int
	name    string
	path    string
}{
	{version: 1, name: "initial", path: "migrations/0001_initial.sql"},
	{version: 2, name: "moderation", path: "migrations/0002_moderation.sql"},
	{version: 3, name: "enrollment", path: "migrations/0003_enrollment.sql"},
	{version: 4, name: "health_projection", path: "migrations/0004_health_projection.sql"},
}

const migrationTableSQL = `
CREATE TABLE IF NOT EXISTS schema_migrations (
    version INTEGER NOT NULL PRIMARY KEY CHECK (version > 0),
    name TEXT NOT NULL UNIQUE CHECK (length(name) BETWEEN 1 AND 128),
    sha256 TEXT NOT NULL CHECK (length(sha256) = 64),
    applied_at_unix INTEGER NOT NULL CHECK (applied_at_unix >= 0)
) STRICT, WITHOUT ROWID;
`

// Migrate applies every pending embedded migration in one immediate
// transaction and refuses missing, changed, or future migration history.
func Migrate(ctx context.Context, database *sql.DB) error {
	if ctx == nil || database == nil {
		return ErrMigrationConfiguration
	}
	migrations, err := loadMigrations()
	if err != nil {
		return err
	}

	transaction, err := database.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin SQLite migration: %w", err)
	}
	defer func() { _ = transaction.Rollback() }()

	if _, err := transaction.ExecContext(ctx, migrationTableSQL); err != nil {
		return fmt.Errorf("create SQLite migration table: %w", err)
	}
	applied, err := readAppliedMigrations(ctx, transaction)
	if err != nil {
		return err
	}
	if err := validateAppliedMigrations(applied, migrations); err != nil {
		return err
	}

	appliedAt := time.Now().UTC().Unix()
	for _, candidate := range migrations {
		if _, exists := applied[candidate.version]; exists {
			continue
		}
		if _, err := transaction.ExecContext(ctx, candidate.sql); err != nil {
			return fmt.Errorf(
				"apply SQLite migration %04d: %w",
				candidate.version,
				err,
			)
		}
		if _, err := transaction.ExecContext(
			ctx,
			`INSERT INTO schema_migrations
                (version, name, sha256, applied_at_unix)
             VALUES (?, ?, ?, ?)`,
			candidate.version,
			candidate.name,
			candidate.sha256,
			appliedAt,
		); err != nil {
			return fmt.Errorf(
				"record SQLite migration %04d: %w",
				candidate.version,
				err,
			)
		}
	}

	if err := transaction.Commit(); err != nil {
		return fmt.Errorf("commit SQLite migrations: %w", err)
	}
	return nil
}

// SchemaVersion returns the highest recorded migration version. A database
// without the migration table has version zero.
func SchemaVersion(ctx context.Context, database *sql.DB) (int, error) {
	if ctx == nil || database == nil {
		return 0, ErrMigrationConfiguration
	}
	var exists int
	if err := database.QueryRowContext(
		ctx,
		`SELECT COUNT(*) FROM sqlite_schema
         WHERE type = 'table' AND name = 'schema_migrations'`,
	).Scan(&exists); err != nil {
		return 0, fmt.Errorf("inspect SQLite migration table: %w", err)
	}
	if exists == 0 {
		return 0, nil
	}

	var version int
	if err := database.QueryRowContext(
		ctx,
		`SELECT COALESCE(MAX(version), 0) FROM schema_migrations`,
	).Scan(&version); err != nil {
		return 0, fmt.Errorf("read SQLite schema version: %w", err)
	}
	return version, nil
}

func loadMigrations() ([]migration, error) {
	if len(migrationManifest) != CurrentSchemaVersion {
		return nil, ErrMigrationConfiguration
	}

	migrations := make([]migration, 0, len(migrationManifest))
	for index, entry := range migrationManifest {
		if entry.version != index+1 || entry.name == "" || entry.path == "" {
			return nil, ErrMigrationConfiguration
		}
		contents, err := fs.ReadFile(migrationFiles, entry.path)
		if err != nil || len(contents) == 0 {
			return nil, ErrMigrationConfiguration
		}
		digest := sha256.Sum256(contents)
		migrations = append(migrations, migration{
			version: entry.version,
			name:    entry.name,
			sql:     string(contents),
			sha256:  hex.EncodeToString(digest[:]),
		})
	}
	return migrations, nil
}

func readAppliedMigrations(
	ctx context.Context,
	transaction *sql.Tx,
) (map[int]appliedMigration, error) {
	rows, err := transaction.QueryContext(
		ctx,
		`SELECT version, name, sha256
         FROM schema_migrations
         ORDER BY version`,
	)
	if err != nil {
		return nil, fmt.Errorf("read SQLite migration history: %w", err)
	}
	defer rows.Close()

	applied := make(map[int]appliedMigration)
	for rows.Next() {
		var version int
		var entry appliedMigration
		if err := rows.Scan(&version, &entry.name, &entry.sha256); err != nil {
			return nil, fmt.Errorf("decode SQLite migration history: %w", err)
		}
		applied[version] = entry
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate SQLite migration history: %w", err)
	}
	return applied, nil
}

func validateAppliedMigrations(
	applied map[int]appliedMigration,
	migrations []migration,
) error {
	for version, entry := range applied {
		if version <= 0 || version > len(migrations) {
			return ErrMigrationTooNew
		}
		expected := migrations[version-1]
		if entry.name != expected.name || entry.sha256 != expected.sha256 {
			return ErrMigrationDrift
		}
	}
	for version := 1; version <= len(applied); version++ {
		if _, exists := applied[version]; !exists {
			return ErrMigrationDrift
		}
	}
	return nil
}
