package main

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/thystra/activity-relay-directory/internal/actorresolver"
	"github.com/thystra/activity-relay-directory/internal/admission"
	"github.com/thystra/activity-relay-directory/internal/buildinfo"
	"github.com/thystra/activity-relay-directory/internal/config"
	"github.com/thystra/activity-relay-directory/internal/httpapi"
	v1 "github.com/thystra/activity-relay-directory/internal/protocol/v1"
	"github.com/thystra/activity-relay-directory/internal/pruning"
	storageContract "github.com/thystra/activity-relay-directory/internal/storage"
	storage "github.com/thystra/activity-relay-directory/internal/storage/sqlite"
)

const (
	replayMaintenanceInterval = 5 * time.Minute
	replayMaintenanceBatch    = 4096
)

func lifecycleAdmissionConfig() admission.Config {
	return admission.Config{
		Source: admission.Rate{
			Burst:          60,
			RefillInterval: time.Second,
		},
		Actor: admission.Rate{
			Burst:          10,
			RefillInterval: time.Minute,
		},
		MaxSources:         10_000,
		MaxActors:          10_000,
		MaxConcurrent:      32,
		IdleTTL:            24 * time.Hour,
		CleanupLimit:       128,
		OverloadRetryAfter: 5 * time.Second,
	}
}

func lifecycleActorCacheConfig() actorresolver.CacheConfig {
	return actorresolver.CacheConfig{
		MaxEntries: 4096,
		TTL:        5 * time.Minute,
	}
}

func main() {
	os.Exit(run(os.Args))
}

func run(arguments []string) int {
	if len(arguments) == 2 && arguments[1] == "--version" {
		fmt.Println(buildinfo.Version)
		return 0
	}
	if len(arguments) >= 2 && arguments[1] == "admin" {
		return runAdminWithInput(arguments, os.Stdin, os.Stdout, os.Stderr, time.Now)
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

	lifecycle, err := initializeLifecycle(cfg, database)
	if err != nil {
		logger.Error("lifecycle initialization failed", "error", err)
		return 1
	}
	if lifecycle != nil {
		go runReplayMaintenance(
			signals,
			lifecycle.replayStore,
			replayMaintenanceInterval,
			replayMaintenanceBatch,
			func(err error) {
				logger.Error("replay maintenance failed", "error", err)
			},
		)
	}

	if cfg.SoftPruningEnabled {
		pruningRepository, err := storage.NewRelayRepository(database)
		if err != nil {
			logger.Error("soft-pruning initialization failed", "error", err)
			return 1
		}
		go runSoftPruningMaintenance(
			signals,
			pruningRepository,
			cfg.SoftPruningInterval,
			storageContract.MinimumSoftPruningInterval,
			time.Now,
			func(result pruning.Result) {
				logger.Info(
					"soft-pruning maintenance completed",
					"observed_at_unix", result.ObservedUnix,
					"scanned", result.Scanned,
					"pruned", result.Pruned,
					"skipped", result.Skipped,
					"truncated", result.Truncated,
				)
			},
			func(err error) {
				logger.Error("soft-pruning maintenance failed", "error", err)
			},
		)
	}

	var lifecycleHandler *httpapi.LifecycleHandler
	if lifecycle != nil {
		lifecycleHandler = lifecycle.handler
	}

	server := &http.Server{
		Addr: cfg.ListenAddress,
		Handler: httpapi.NewHandlerWithLifecycle(
			cfg,
			buildinfo.Version,
			func(ctx context.Context) error {
				return storage.CheckReady(ctx, database)
			},
			lifecycleHandler,
			func(ctx context.Context) (bool, error) {
				repository, err := storage.NewRelayRepository(database)
				if err != nil {
					return false, err
				}
				return repository.EnrollmentOpen(ctx)
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
		"lifecycle_enabled", cfg.LifecycleEnabled,
		"lifecycle_available", lifecycleHandler != nil,
		"soft_pruning_enabled", cfg.SoftPruningEnabled,
		"soft_pruning_interval", cfg.SoftPruningInterval,
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

type lifecycleRuntime struct {
	handler     *httpapi.LifecycleHandler
	replayStore *storage.RFC9421ReplayStore
}

func initializeLifecycle(
	cfg config.Config,
	database *sql.DB,
) (*lifecycleRuntime, error) {
	if !cfg.LifecycleEnabled {
		return nil, nil
	}
	if database == nil {
		return nil, httpapi.ErrLifecycleConfiguration
	}
	publicBase, err := url.Parse(cfg.PublicBaseURL)
	if err != nil || publicBase.Scheme != "https" || publicBase.Host == "" {
		return nil, httpapi.ErrLifecycleConfiguration
	}
	sourceResolver, err := admission.NewSourceResolver(cfg.TrustedProxyPrefixes)
	if err != nil {
		return nil, err
	}
	limiter, err := admission.New(lifecycleAdmissionConfig())
	if err != nil {
		return nil, err
	}
	resolver, err := actorresolver.New("Activity-Relay-Directory")
	if err != nil {
		return nil, err
	}
	cachedResolver, err := actorresolver.NewCachedResolver(
		resolver,
		lifecycleActorCacheConfig(),
	)
	if err != nil {
		return nil, err
	}
	verifier, err := v1.NewRFC9421Verifier(v1.RFC9421VerifierOptions{
		Authority:   publicBase.Host,
		KeyResolver: cachedResolver,
		Now:         time.Now,
	})
	if err != nil {
		return nil, err
	}
	replayStore, err := storage.NewRFC9421ReplayStore(database)
	if err != nil {
		return nil, err
	}
	repository, err := storage.NewRelayRepository(database)
	if err != nil {
		return nil, err
	}
	handler, err := httpapi.NewLifecycleHandler(httpapi.LifecycleDependencies{
		Verifier:         verifier,
		ReplayStore:      replayStore,
		Repository:       repository,
		SourceResolver:   sourceResolver,
		Limiter:          limiter,
		MaximumBodyBytes: cfg.MaxRequestBodyBytes,
		Now:              time.Now,
	})
	if err != nil {
		return nil, err
	}
	return &lifecycleRuntime{handler: handler, replayStore: replayStore}, nil
}

type replayCleaner interface {
	CleanupExpiredRFC9421Replay(context.Context, int) (int64, error)
}

func runReplayMaintenance(
	ctx context.Context,
	cleaner replayCleaner,
	interval time.Duration,
	maximum int,
	onError func(error),
) {
	if ctx == nil || cleaner == nil || interval <= 0 || maximum <= 0 {
		if onError != nil {
			onError(errors.New("replay maintenance configuration is invalid"))
		}
		return
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if _, err := cleaner.CleanupExpiredRFC9421Replay(ctx, maximum); err != nil &&
				onError != nil && ctx.Err() == nil {
				onError(err)
			}
		}
	}
}

func runSoftPruningMaintenance(
	ctx context.Context,
	repository storageContract.PruningRepository,
	interval time.Duration,
	minimumInterval time.Duration,
	now func() time.Time,
	onResult func(pruning.Result),
	onError func(error),
) {
	if ctx == nil || repository == nil || now == nil || minimumInterval <= 0 ||
		interval < minimumInterval {
		if onError != nil {
			onError(errors.New("soft-pruning maintenance configuration is invalid"))
		}
		return
	}

	for {
		if err := ctx.Err(); err != nil {
			return
		}
		result, err := pruning.Run(ctx, repository, now())
		if err != nil {
			if ctx.Err() != nil {
				return
			}
			if onError != nil {
				onError(err)
			}
		} else if onResult != nil {
			onResult(result)
		}

		timer := time.NewTimer(interval)
		select {
		case <-ctx.Done():
			if !timer.Stop() {
				select {
				case <-timer.C:
				default:
				}
			}
			return
		case <-timer.C:
		}
	}
}

func initializeReadOnlyDatabase(ctx context.Context, path string) (*sql.DB, error) {
	database, err := storage.OpenReadOnly(ctx, path)
	if err != nil {
		return nil, err
	}
	if err := storage.CheckReady(ctx, database); err != nil {
		_ = database.Close()
		return nil, err
	}
	return database, nil
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
