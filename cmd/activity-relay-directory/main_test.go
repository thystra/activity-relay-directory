package main

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"testing"

	storage "github.com/thystra/activity-relay-directory/internal/storage/sqlite"
)

func TestInitializeDatabaseCreatesCurrentSchema(t *testing.T) {
	path := filepath.Join(t.TempDir(), "directory.sqlite")
	database, err := initializeDatabase(context.Background(), path)
	if err != nil {
		t.Fatalf("initializeDatabase() error = %v", err)
	}
	t.Cleanup(func() { _ = database.Close() })

	version, err := storage.SchemaVersion(context.Background(), database)
	if err != nil {
		t.Fatalf("SchemaVersion() error = %v", err)
	}
	if version != storage.CurrentSchemaVersion {
		t.Fatalf(
			"SchemaVersion() = %d, want %d",
			version,
			storage.CurrentSchemaVersion,
		)
	}
}

func TestInitializeDatabaseFailsClosedOnMigrationDrift(t *testing.T) {
	path := filepath.Join(t.TempDir(), "directory.sqlite")
	database, err := initializeDatabase(context.Background(), path)
	if err != nil {
		t.Fatalf("initial initializeDatabase() error = %v", err)
	}
	if _, err := database.Exec(
		`UPDATE schema_migrations SET sha256 = ? WHERE version = 1`,
		fmt.Sprintf("%064d", 0),
	); err != nil {
		t.Fatalf("alter migration history: %v", err)
	}
	if err := database.Close(); err != nil {
		t.Fatalf("Close() error = %v", err)
	}

	database, err = initializeDatabase(context.Background(), path)
	if database != nil {
		_ = database.Close()
	}
	if !errors.Is(err, storage.ErrMigrationDrift) {
		t.Fatalf("initializeDatabase() error = %v, want ErrMigrationDrift", err)
	}
}

func TestInitializeDatabaseHonorsCanceledContext(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	database, err := initializeDatabase(
		ctx,
		filepath.Join(t.TempDir(), "directory.sqlite"),
	)
	if database != nil {
		_ = database.Close()
	}
	if err == nil {
		t.Fatal("initializeDatabase() error = nil, want canceled startup")
	}
}
