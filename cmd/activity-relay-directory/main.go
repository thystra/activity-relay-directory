package main

import (
	"context"
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
)

func main() {
	if len(os.Args) == 2 && os.Args[1] == "--version" {
		fmt.Println(buildinfo.Version)
		return
	}

	logger := slog.New(slog.NewJSONHandler(os.Stderr, nil))
	slog.SetDefault(logger)

	cfg, err := config.Load()
	if err != nil {
		logger.Error("invalid configuration", "error", err)
		os.Exit(2)
	}

	server := &http.Server{
		Addr:              cfg.ListenAddress,
		Handler:           httpapi.NewHandler(cfg, buildinfo.Version),
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       30 * time.Second,
		WriteTimeout:      30 * time.Second,
		IdleTimeout:       90 * time.Second,
		MaxHeaderBytes:    32 * 1024,
	}

	signals, stop := signal.NotifyContext(
		context.Background(),
		os.Interrupt,
		syscall.SIGTERM,
	)
	defer stop()

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
		"version", buildinfo.Version,
	)

	err = server.ListenAndServe()
	if err != nil && !errors.Is(err, http.ErrServerClosed) {
		logger.Error("HTTP server failed", "error", err)
		os.Exit(1)
	}

	logger.Info("directory service stopped")
}
