package main

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/thystra/activity-relay-directory/internal/buildinfo"
	"github.com/thystra/activity-relay-directory/internal/config"
	"github.com/thystra/activity-relay-directory/internal/httpapi"
	storage "github.com/thystra/activity-relay-directory/internal/storage/sqlite"
)

func main() {
	os.Exit(run(os.Args))
}

func run(arguments []string) int {
	if len(arguments) == 2 && arguments[1] == "--version" {
		fmt.Println(buildinfo.Version)
		return 0
	}

	logger := slog.New(slog.NewJSONHandler(os.Stderr, nil))
	slog.SetDefault(logger)

	cfg, err := config.Load()
	if err != nil {
		logger.Error("invalid configuration", "error", err)
		return 2
	}

	signals, stop := signal.NotifyContext(
		context.Background(),
		os.Interrupt,
		syscall.SIGTERM,
	)
	defer stop()

	database, err := initializeDatabase(signals, cfg.DatabasePath)
	if err != nil {
		logger.Error("database initialization failed", "error", err)
		return 1
	}
	defer func() {
		if err := database.Close(); err != nil {
			logger.Error("database close failed", "error", err)
		}
	}()

	server := &http.Server{
		Addr: cfg.ListenAddress,
		Handler: httpapi.NewHandler(
			cfg,
			buildinfo.Version,
			func(ctx context.Context) error {
				return storage.CheckReady(ctx, database)
			},
		),
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       30 * time.Second,
		WriteTimeout:      30 * time.Second,
		IdleTimeout:       90 * time.Second,
		MaxHeaderBytes:    32 * 1024,
	}

	go func() {
		<-signals.Done()

		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()

		if err := server.Shutdown(ctx); err != nil {
			logger.Error("graceful shutdown failed", "error", err)
		}
	}()

	logger.Info(
		"directory service starting",
		"address", cfg.ListenAddress,
		"public_base_url", cfg.PublicBaseURL,
		"registration_enabled", cfg.RegistrationEnabled,
		"registration_available", false,
		"database_schema_version", storage.CurrentSchemaVersion,
		"version", buildinfo.Version,
	)

	err = server.ListenAndServe()
	if err != nil && !errors.Is(err, http.ErrServerClosed) {
		logger.Error("HTTP server failed", "error", err)
		return 1
	}

	logger.Info("directory service stopped")
	return 0
}

func initializeDatabase(ctx context.Context, path string) (*sql.DB, error) {
	database, err := storage.Open(ctx, path)
	if err != nil {
		return nil, err
	}
	if err := storage.Migrate(ctx, database); err != nil {
		_ = database.Close()
		return nil, err
	}
	if err := storage.CheckReady(ctx, database); err != nil {
		_ = database.Close()
		return nil, err
	}
	return database, nil
}
