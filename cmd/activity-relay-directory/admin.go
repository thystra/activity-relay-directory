package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"time"

	"github.com/thystra/activity-relay-directory/internal/actorresolver"
	"github.com/thystra/activity-relay-directory/internal/admincommand"
	"github.com/thystra/activity-relay-directory/internal/adminnotify"
	"github.com/thystra/activity-relay-directory/internal/config"
	"github.com/thystra/activity-relay-directory/internal/discoverycommand"
	"github.com/thystra/activity-relay-directory/internal/prunecommand"
	"github.com/thystra/activity-relay-directory/internal/retentioncommand"
	"github.com/thystra/activity-relay-directory/internal/storage"
	storageSQLite "github.com/thystra/activity-relay-directory/internal/storage/sqlite"
	"github.com/thystra/activity-relay-directory/internal/storagecommand"
)

const (
	adminCommandTimeout     = 30 * time.Second
	discoveryCommandTimeout = 5 * time.Minute
	retentionPurgeTimeout   = 5 * time.Minute
)

func runAdmin(arguments []string, stdout, stderr io.Writer, now func() time.Time) int {
	return runAdminWithInput(arguments, os.Stdin, stdout, stderr, now)
}

func runAdminWithInput(
	arguments []string,
	stdin io.Reader,
	stdout, stderr io.Writer,
	now func() time.Time,
) int {
	if len(arguments) < 3 || arguments[1] != "admin" {
		writeAdminUsage(stderr)
		return admincommand.ExitUsage
	}
	if arguments[2] == "enrollment" {
		return runEnrollmentAdmin(arguments, stdout, stderr, now)
	}
	if arguments[2] == "discovery" {
		return runDiscoveryAdmin(arguments, stdin, stdout, stderr, now)
	}
	if arguments[2] == "pruning" {
		return runPruningAdmin(arguments, stdout, stderr, now)
	}
	if arguments[2] == "retention" {
		return runRetentionAdmin(arguments, stdin, stdout, stderr, now)
	}
	if arguments[2] == "storage" {
		return runStorageAdmin(arguments, stdout, stderr, now)
	}

	request, err := admincommand.Parse(arguments[2:])
	if err != nil {
		writeAdminUsage(stderr)
		return admincommand.ExitUsage
	}
	if now == nil {
		fmt.Fprintln(stderr, "administrative clock is unavailable")
		return admincommand.ExitOperational
	}
	databasePath, err := config.LoadDatabasePath()
	if err != nil {
		fmt.Fprintln(stderr, "invalid configuration")
		return admincommand.ExitUsage
	}
	if request.Action == admincommand.ActionShow || request.Action == admincommand.ActionAudit {
		ctx, cancel := context.WithTimeout(context.Background(), adminCommandTimeout)
		defer cancel()
		database, err := initializeReadOnlyDatabase(ctx, databasePath)
		if err != nil {
			fmt.Fprintln(stderr, "database initialization failed")
			return admincommand.ExitOperational
		}
		defer database.Close()
		repository, err := storageSQLite.NewRelayRepository(database, storage.DenyWrites)
		if err != nil {
			fmt.Fprintln(stderr, "moderation repository initialization failed")
			return admincommand.ExitOperational
		}
		return admincommand.Execute(ctx, request, repository, stdout, stderr, now)
	}
	growthConfig, retentionDays, growthMailer, err := loadAdminGrowthDependencies()
	if err != nil {
		fmt.Fprintln(stderr, "invalid storage growth configuration")
		return admincommand.ExitUsage
	}
	if err := admincommand.Confirm(request, stdin, stderr); err != nil {
		fmt.Fprintln(stderr)
		fmt.Fprintln(stderr, "moderation confirmation failed")
		return admincommand.ExitUsage
	}
	ctx, cancel := context.WithTimeout(context.Background(), adminCommandTimeout)
	defer cancel()
	database, growthGuard, err := initializeGuardedDatabase(
		ctx, databasePath, growthConfig, retentionDays, growthMailer, now,
	)
	if err != nil {
		fmt.Fprintln(stderr, "database initialization failed")
		return admincommand.ExitOperational
	}
	defer database.Close()
	repository, err := storageSQLite.NewRelayRepository(database, growthGuard)
	if err != nil {
		fmt.Fprintln(stderr, "moderation repository initialization failed")
		return admincommand.ExitOperational
	}
	return admincommand.Execute(ctx, request, repository, stdout, stderr, now)
}

func runDiscoveryAdmin(
	arguments []string,
	stdin io.Reader,
	stdout, stderr io.Writer,
	now func() time.Time,
) int {
	return runDiscoveryAdminWithProberFactory(
		arguments, stdin, stdout, stderr, now,
		func() (discoverycommand.Prober, error) {
			return actorresolver.New("Activity-Relay-Directory")
		},
	)
}

func runDiscoveryAdminWithProberFactory(
	arguments []string,
	stdin io.Reader,
	stdout, stderr io.Writer,
	now func() time.Time,
	newProber func() (discoverycommand.Prober, error),
) int {
	if len(arguments) < 4 {
		writeDiscoveryUsage(stderr)
		return discoverycommand.ExitUsage
	}
	request, err := discoverycommand.Parse(arguments[3:])
	if err != nil {
		writeDiscoveryUsage(stderr)
		return discoverycommand.ExitUsage
	}
	if now == nil {
		fmt.Fprintln(stderr, "administrative clock is unavailable")
		return discoverycommand.ExitOperational
	}
	databasePath, err := config.LoadDatabasePath()
	if err != nil {
		fmt.Fprintln(stderr, "invalid configuration")
		return discoverycommand.ExitUsage
	}

	growthConfig, retentionDays, growthMailer, err := loadAdminGrowthDependencies()
	if err != nil {
		fmt.Fprintln(stderr, "invalid storage growth configuration")
		return discoverycommand.ExitUsage
	}

	prepareCtx, cancelPrepare := context.WithTimeout(context.Background(), discoveryCommandTimeout)
	defer cancelPrepare()

	// Fail closed on an absent/stale database and invalid storage configuration
	// before initiating remote probes. The preparation timeout ends before the
	// operator confirmation prompt so review time cannot consume the mutation
	// budget.
	preflight, err := initializeReadOnlyDatabase(prepareCtx, databasePath)
	if err != nil {
		cancelPrepare()
		fmt.Fprintln(stderr, "database initialization failed")
		return discoverycommand.ExitOperational
	}
	if err := preflight.Close(); err != nil {
		cancelPrepare()
		fmt.Fprintln(stderr, "database initialization failed")
		return discoverycommand.ExitOperational
	}

	plan := discoverycommand.Plan{}
	if request.Action != discoverycommand.ActionRemove {
		if newProber == nil {
			fmt.Fprintln(stderr, "discovery resolver initialization failed")
			return discoverycommand.ExitOperational
		}
		prober, err := newProber()
		if err != nil || prober == nil {
			fmt.Fprintln(stderr, "discovery resolver initialization failed")
			return discoverycommand.ExitOperational
		}
		plan, err = discoverycommand.Prepare(prepareCtx, request, prober)
		if err != nil {
			cancelPrepare()
			if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
				fmt.Fprintln(stderr, "discovery preparation canceled")
				return discoverycommand.ExitCanceled
			}
			if errors.Is(err, discoverycommand.ErrImportFile) {
				fmt.Fprintln(stderr, "discovery import file is invalid")
				return discoverycommand.ExitUsage
			}
			fmt.Fprintln(stderr, "discovery preparation failed")
			return discoverycommand.ExitOperational
		}
		if err := discoverycommand.RenderPlan(stderr, request, plan); err != nil {
			cancelPrepare()
			fmt.Fprintln(stderr, "discovery prospective output failed")
			return discoverycommand.ExitOperational
		}
		if len(plan.Ready) == 0 {
			cancelPrepare()
			fmt.Fprintln(stderr, "discovery preparation produced no ready relays")
			return discoverycommand.ExitOperational
		}
	}
	cancelPrepare()

	if err := discoverycommand.Confirm(request, plan, stdin, stderr); err != nil {
		fmt.Fprintln(stderr)
		fmt.Fprintln(stderr, "discovery confirmation failed")
		return discoverycommand.ExitUsage
	}

	mutationCtx, cancelMutation := context.WithTimeout(context.Background(), discoveryCommandTimeout)
	defer cancelMutation()
	database, growthGuard, err := initializeGuardedDatabase(
		mutationCtx, databasePath, growthConfig, retentionDays, growthMailer, now,
	)
	if err != nil {
		fmt.Fprintln(stderr, "database initialization failed")
		return discoverycommand.ExitOperational
	}
	defer database.Close()
	repository, err := storageSQLite.NewRelayRepository(database, growthGuard)
	if err != nil {
		fmt.Fprintln(stderr, "discovery repository initialization failed")
		return discoverycommand.ExitOperational
	}
	return discoverycommand.Execute(mutationCtx, request, plan, repository, stdout, stderr, now)
}

func runPruningAdmin(
	arguments []string,
	stdout, stderr io.Writer,
	now func() time.Time,
) int {
	if len(arguments) < 4 {
		writePruningUsage(stderr)
		return prunecommand.ExitUsage
	}
	request, err := prunecommand.Parse(arguments[3:])
	if err != nil {
		writePruningUsage(stderr)
		return prunecommand.ExitUsage
	}
	if now == nil {
		fmt.Fprintln(stderr, "administrative clock is unavailable")
		return prunecommand.ExitOperational
	}
	databasePath, err := config.LoadDatabasePath()
	if err != nil {
		fmt.Fprintln(stderr, "invalid configuration")
		return prunecommand.ExitUsage
	}
	ctx, cancel := context.WithTimeout(context.Background(), adminCommandTimeout)
	defer cancel()
	database, err := initializeReadOnlyDatabase(ctx, databasePath)
	if err != nil {
		fmt.Fprintln(stderr, "database initialization failed")
		return prunecommand.ExitOperational
	}
	defer database.Close()
	repository, err := storageSQLite.NewRelayRepository(database, storage.DenyWrites)
	if err != nil {
		fmt.Fprintln(stderr, "soft-pruning repository initialization failed")
		return prunecommand.ExitOperational
	}
	return prunecommand.Execute(ctx, request, repository, stdout, stderr, now)
}

func runRetentionAdmin(
	arguments []string,
	stdin io.Reader,
	stdout, stderr io.Writer,
	now func() time.Time,
) int {
	if len(arguments) < 4 {
		writeRetentionUsage(stderr)
		return retentioncommand.ExitUsage
	}
	request, err := retentioncommand.Parse(arguments[3:])
	if err != nil {
		writeRetentionUsage(stderr)
		return retentioncommand.ExitUsage
	}
	if now == nil {
		fmt.Fprintln(stderr, "administrative clock is unavailable")
		return retentioncommand.ExitOperational
	}
	databasePath, err := config.LoadDatabasePath()
	if err != nil {
		fmt.Fprintln(stderr, "invalid configuration")
		return retentioncommand.ExitUsage
	}
	retentionDays, err := config.LoadInactiveRetentionDays()
	if err != nil {
		fmt.Fprintln(stderr, "invalid retention configuration")
		return retentioncommand.ExitUsage
	}
	if request.Action == "dry-run" {
		ctx, cancel := context.WithTimeout(context.Background(), adminCommandTimeout)
		defer cancel()
		database, err := initializeReadOnlyDatabase(ctx, databasePath)
		if err != nil {
			fmt.Fprintln(stderr, "database initialization failed")
			return retentioncommand.ExitOperational
		}
		defer database.Close()
		repository, err := storageSQLite.NewRelayRepository(database, storage.DenyWrites)
		if err != nil {
			fmt.Fprintln(stderr, "retention repository initialization failed")
			return retentioncommand.ExitOperational
		}
		return retentioncommand.ExecuteDryRun(
			ctx, request, repository, retentionDays, stdout, stderr, now,
		)
	}

	if retentionDays == 0 {
		fmt.Fprintln(stderr, "inactive retention is disabled; DIRECTORY_INACTIVE_RETENTION_DAYS is 0")
		return retentioncommand.ExitUsage
	}
	growthConfig, err := config.LoadDatabaseGrowthConfig()
	if err != nil {
		fmt.Fprintln(stderr, "invalid storage growth configuration")
		return retentioncommand.ExitUsage
	}
	growthMailer, err := newAdminGrowthMailer(growthConfig)
	if err != nil {
		fmt.Fprintln(stderr, "invalid storage growth mailer")
		return retentioncommand.ExitUsage
	}
	ctx, cancel := context.WithTimeout(context.Background(), retentionPurgeTimeout)
	defer cancel()
	preflight, err := initializeReadOnlyDatabase(ctx, databasePath)
	if err != nil {
		fmt.Fprintln(stderr, "database initialization failed")
		return retentioncommand.ExitOperational
	}
	if err := preflight.Close(); err != nil {
		fmt.Fprintln(stderr, "database initialization failed")
		return retentioncommand.ExitOperational
	}
	database, growthGuard, err := initializeGuardedDatabase(
		ctx, databasePath, growthConfig, retentionDays, growthMailer, now,
	)
	if err != nil {
		fmt.Fprintln(stderr, "database initialization failed")
		return retentioncommand.ExitOperational
	}
	defer database.Close()
	backupDigest, err := storageSQLite.VerifyRetentionBackup(
		ctx, database, databasePath, request.BackupPath,
	)
	if err != nil {
		fmt.Fprintln(stderr, "verified pre-retention backup requirement failed")
		return retentioncommand.ExitOperational
	}
	if err := retentioncommand.Confirm(request, stdin, stderr, retentionDays); err != nil {
		fmt.Fprintln(stderr)
		fmt.Fprintln(stderr, "inactive-retention purge confirmation failed")
		return retentioncommand.ExitUsage
	}
	confirmedDigest, err := storageSQLite.VerifyRetentionBackup(
		ctx, database, databasePath, request.BackupPath,
	)
	if err != nil || confirmedDigest != backupDigest {
		fmt.Fprintln(stderr, "verified pre-retention backup changed after confirmation")
		return retentioncommand.ExitOperational
	}
	repository, err := storageSQLite.NewRelayRepository(database, growthGuard)
	if err != nil {
		fmt.Fprintln(stderr, "retention repository initialization failed")
		return retentioncommand.ExitOperational
	}
	return retentioncommand.ExecutePurge(
		ctx, request, repository, retentionDays, backupDigest, stdout, stderr, now,
	)
}

func runEnrollmentAdmin(
	arguments []string,
	stdout, stderr io.Writer,
	now func() time.Time,
) int {
	if len(arguments) < 4 {
		writeEnrollmentUsage(stderr)
		return 2
	}
	action := arguments[3]
	flags := flag.NewFlagSet("admin enrollment "+action, flag.ContinueOnError)
	flags.SetOutput(stderr)
	operatorID := flags.String("operator", "", "bounded private operator audit identifier")
	if err := flags.Parse(arguments[4:]); err != nil || flags.NArg() != 0 {
		return 2
	}
	if action != "status" && action != "open" && action != "close" {
		writeEnrollmentUsage(stderr)
		return 2
	}
	if action == "status" && *operatorID != "" {
		fmt.Fprintln(stderr, "--operator is valid only for open or close")
		return 2
	}
	if action != "status" && *operatorID == "" {
		fmt.Fprintln(stderr, "--operator is required for open or close")
		return 2
	}
	if action != "status" && !storage.ValidOperatorID(*operatorID) {
		fmt.Fprintln(stderr, "--operator is invalid")
		return 2
	}
	if now == nil {
		fmt.Fprintln(stderr, "administrative clock is unavailable")
		return 1
	}

	databasePath, err := config.LoadDatabasePath()
	if err != nil {
		fmt.Fprintf(stderr, "invalid configuration: %v\n", err)
		return 2
	}
	growthConfig, retentionDays, growthMailer, err := loadAdminGrowthDependencies()
	if err != nil {
		fmt.Fprintln(stderr, "invalid storage growth configuration")
		return 2
	}
	ctx, cancel := context.WithTimeout(context.Background(), adminCommandTimeout)
	defer cancel()
	database, growthGuard, err := initializeGuardedDatabase(
		ctx, databasePath, growthConfig, retentionDays, growthMailer, now,
	)
	if err != nil {
		fmt.Fprintln(stderr, "database initialization failed")
		return 1
	}
	defer database.Close()
	repository, err := storageSQLite.NewRelayRepository(database, growthGuard)
	if err != nil {
		fmt.Fprintln(stderr, "enrollment repository initialization failed")
		return 1
	}

	if action == "status" {
		open, err := repository.EnrollmentOpen(ctx)
		if err != nil {
			fmt.Fprintln(stderr, "enrollment status failed")
			return 1
		}
		if open {
			fmt.Fprintln(stdout, "open")
		} else {
			fmt.Fprintln(stdout, "closed")
		}
		return 0
	}

	outcome, err := repository.SetEnrollment(
		ctx,
		action == "open",
		storage.EnrollmentIntent{OperatorID: *operatorID},
		now(),
	)
	if err != nil {
		if errors.Is(err, storage.ErrTransitionInput) ||
			errors.Is(err, storage.ErrTransitionTime) {
			fmt.Fprintln(stderr, "enrollment decision is invalid")
			return 2
		}
		fmt.Fprintln(stderr, "enrollment decision failed")
		return 1
	}
	if !outcome.Valid() {
		fmt.Fprintln(stderr, "enrollment decision returned an invalid outcome")
		return 1
	}
	fmt.Fprintln(stdout, outcome)
	return 0
}

func runStorageAdmin(
	arguments []string,
	stdout, stderr io.Writer,
	now func() time.Time,
) int {
	if len(arguments) < 4 {
		writeStorageUsage(stderr)
		return storagecommand.ExitUsage
	}
	request, err := storagecommand.Parse(arguments[3:])
	if err != nil {
		writeStorageUsage(stderr)
		return storagecommand.ExitUsage
	}
	if now == nil {
		fmt.Fprintln(stderr, "administrative clock is unavailable")
		return storagecommand.ExitOperational
	}
	databasePath, err := config.LoadDatabasePath()
	if err != nil {
		fmt.Fprintln(stderr, "invalid configuration")
		return storagecommand.ExitUsage
	}
	growthConfig, err := config.LoadDatabaseGrowthConfig()
	if err != nil {
		fmt.Fprintln(stderr, "invalid storage growth configuration")
		return storagecommand.ExitUsage
	}
	retentionDays, err := config.LoadInactiveRetentionDays()
	if err != nil {
		fmt.Fprintln(stderr, "invalid retention configuration")
		return storagecommand.ExitUsage
	}

	ctx, cancel := context.WithTimeout(context.Background(), adminCommandTimeout)
	defer cancel()
	database, err := initializeReadOnlyDatabase(ctx, databasePath)
	if err != nil {
		fmt.Fprintln(stderr, "database initialization failed")
		return storagecommand.ExitOperational
	}
	defer database.Close()

	sample, err := storageSQLite.InspectDatabaseGrowth(
		ctx,
		database,
		databasePath,
		growthConfig.MaxBytes,
		growthConfig.WarningPercent,
		retentionDays,
	)
	if err != nil {
		fmt.Fprintln(stderr, storageSQLite.RedactedGrowthError(err))
		return storagecommand.ExitOperational
	}
	sample.ObservedUnix = now().UTC().Unix()
	if sample.ObservedUnix < 0 {
		fmt.Fprintln(stderr, "administrative clock is unavailable")
		return storagecommand.ExitOperational
	}

	if request.Action == storagecommand.ActionTestAlert {
		if !growthConfig.EmailEnabled() {
			fmt.Fprintln(stderr, "administrator email is disabled")
			return storagecommand.ExitUsage
		}
		mailer, err := newAdminGrowthMailer(growthConfig)
		if err != nil || mailer == nil {
			fmt.Fprintln(stderr, "administrator mailer is unavailable")
			return storagecommand.ExitOperational
		}
		subject, body := storageSQLite.RenderGrowthAlert(
			storage.DatabaseGrowthAlertTest,
			sample,
		)
		if err := mailer.Send(ctx, subject, body); err != nil {
			fmt.Fprintln(stderr, "test alert failed")
			return storagecommand.ExitOperational
		}
	}

	if err := storagecommand.Render(stdout, request, sample); err != nil {
		fmt.Fprintln(stderr, "storage output failed")
		return storagecommand.ExitOperational
	}
	return storagecommand.ExitForSample(request.Action, sample)
}

func loadAdminGrowthDependencies() (
	config.DatabaseGrowthConfig,
	int,
	storageSQLite.GrowthMailer,
	error,
) {
	growthConfig, err := config.LoadDatabaseGrowthConfig()
	if err != nil {
		return config.DatabaseGrowthConfig{}, 0, nil, err
	}
	retentionDays, err := config.LoadInactiveRetentionDays()
	if err != nil {
		return config.DatabaseGrowthConfig{}, 0, nil, err
	}
	mailer, err := newAdminGrowthMailer(growthConfig)
	if err != nil {
		return config.DatabaseGrowthConfig{}, 0, nil, err
	}
	return growthConfig, retentionDays, mailer, nil
}

func newAdminGrowthMailer(
	growthConfig config.DatabaseGrowthConfig,
) (storageSQLite.GrowthMailer, error) {
	if !growthConfig.EmailEnabled() {
		return nil, nil
	}
	return adminnotify.NewCommandMailer(
		growthConfig.MailCommand,
		growthConfig.AdministratorEmails,
		growthConfig.MailTimeout,
	)
}

func writeStorageUsage(output io.Writer) {
	fmt.Fprintln(output, "usage: activity-relay-directory admin storage status [--format human|json]")
	fmt.Fprintln(output, "       activity-relay-directory admin storage check [--format human|json]")
	fmt.Fprintln(output, "       activity-relay-directory admin storage test-alert [--format human|json]")
}

func writeAdminUsage(output io.Writer) {
	fmt.Fprintln(output, "usage: activity-relay-directory admin enrollment status|open|close [--operator ID]")
	fmt.Fprintln(output, "       activity-relay-directory admin suspend --actor URL --moderator ID --reason CODE [--yes] [--format human|json]")
	fmt.Fprintln(output, "       activity-relay-directory admin restore --actor URL --moderator ID --reason CODE [--yes] [--format human|json]")
	fmt.Fprintln(output, "       activity-relay-directory admin show --actor URL [--format human|json]")
	fmt.Fprintln(output, "       activity-relay-directory admin audit --actor URL [--limit 1..100] [--after UNIX:ID] [--format human|json]")
	fmt.Fprintln(output, "       activity-relay-directory admin discovery add --url URL --operator ID --reason CODE [--source-label LABEL] [--yes] [--format human|json]")
	fmt.Fprintln(output, "       activity-relay-directory admin discovery remove --actor URL --operator ID --reason CODE [--source-label LABEL] [--yes] [--format human|json]")
	fmt.Fprintln(output, "       activity-relay-directory admin discovery import --file PATH --operator ID --reason CODE --source-label LABEL [--yes] [--format human|json]")
	fmt.Fprintln(output, "       activity-relay-directory admin pruning dry-run [--limit 1..100] [--after-last-seen UNIX --after-actor URL] [--format human|json]")
	fmt.Fprintln(output, "       activity-relay-directory admin retention dry-run [--format human|json]")
	fmt.Fprintln(output, "       activity-relay-directory admin retention purge --backup PATH [--yes] [--format human|json]")
	fmt.Fprintln(output, "       activity-relay-directory admin storage status|check|test-alert [--format human|json]")
}

func writeDiscoveryUsage(output io.Writer) {
	fmt.Fprintln(output, "usage: activity-relay-directory admin discovery add --url URL --operator ID --reason CODE [--source-label LABEL] [--yes] [--format human|json]")
	fmt.Fprintln(output, "       activity-relay-directory admin discovery remove --actor URL --operator ID --reason CODE [--source-label LABEL] [--yes] [--format human|json]")
	fmt.Fprintln(output, "       activity-relay-directory admin discovery import --file PATH --operator ID --reason CODE --source-label LABEL [--yes] [--format human|json]")
}

func writePruningUsage(output io.Writer) {
	fmt.Fprintln(output, "usage: activity-relay-directory admin pruning dry-run [--limit 1..100] [--after-last-seen UNIX --after-actor URL] [--format human|json]")
}

func writeRetentionUsage(output io.Writer) {
	fmt.Fprintln(output, "usage: activity-relay-directory admin retention dry-run [--format human|json]")
	fmt.Fprintln(output, "       activity-relay-directory admin retention purge --backup PATH [--yes] [--format human|json]")
}

func writeEnrollmentUsage(output io.Writer) {
	fmt.Fprintln(output, "usage: activity-relay-directory admin enrollment status|open|close [--operator ID]")
}
