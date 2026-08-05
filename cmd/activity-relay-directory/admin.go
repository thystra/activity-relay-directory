package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"time"

	"github.com/thystra/activity-relay-directory/internal/admincommand"
	"github.com/thystra/activity-relay-directory/internal/config"
	"github.com/thystra/activity-relay-directory/internal/storage"
	storageSQLite "github.com/thystra/activity-relay-directory/internal/storage/sqlite"
)

const adminCommandTimeout = 30 * time.Second

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
	if err := admincommand.Confirm(request, stdin, stderr); err != nil {
		fmt.Fprintln(stderr)
		fmt.Fprintln(stderr, "moderation confirmation failed")
		return admincommand.ExitUsage
	}
	ctx, cancel := context.WithTimeout(context.Background(), adminCommandTimeout)
	defer cancel()
	database, err := initializeDatabase(ctx, databasePath)
	if err != nil {
		fmt.Fprintln(stderr, "database initialization failed")
		return admincommand.ExitOperational
	}
	defer database.Close()
	repository, err := storageSQLite.NewRelayRepository(database)
	if err != nil {
		fmt.Fprintln(stderr, "moderation repository initialization failed")
		return admincommand.ExitOperational
	}
	return admincommand.Execute(ctx, request, repository, stdout, stderr, now)
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
	ctx, cancel := context.WithTimeout(context.Background(), adminCommandTimeout)
	defer cancel()
	database, err := initializeDatabase(ctx, databasePath)
	if err != nil {
		fmt.Fprintln(stderr, "database initialization failed")
		return 1
	}
	defer database.Close()
	repository, err := storageSQLite.NewRelayRepository(database)
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

func writeAdminUsage(output io.Writer) {
	fmt.Fprintln(output, "usage: activity-relay-directory admin enrollment status|open|close [--operator ID]")
	fmt.Fprintln(output, "       activity-relay-directory admin suspend --actor URL --moderator ID --reason CODE [--yes] [--format human|json]")
	fmt.Fprintln(output, "       activity-relay-directory admin restore --actor URL --moderator ID --reason CODE [--yes] [--format human|json]")
	fmt.Fprintln(output, "       activity-relay-directory admin show --actor URL [--format human|json]")
	fmt.Fprintln(output, "       activity-relay-directory admin audit --actor URL [--limit 1..100] [--after UNIX:ID] [--format human|json]")
}

func writeEnrollmentUsage(output io.Writer) {
	fmt.Fprintln(output, "usage: activity-relay-directory admin enrollment status|open|close [--operator ID]")
}
