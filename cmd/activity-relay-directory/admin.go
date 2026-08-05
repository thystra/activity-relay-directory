package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"time"

	"github.com/thystra/activity-relay-directory/internal/config"
	"github.com/thystra/activity-relay-directory/internal/storage"
	storageSQLite "github.com/thystra/activity-relay-directory/internal/storage/sqlite"
)

const adminCommandTimeout = 30 * time.Second

func runAdmin(arguments []string, stdout, stderr io.Writer, now func() time.Time) int {
	if len(arguments) < 4 || arguments[1] != "admin" ||
		arguments[2] != "enrollment" {
		fmt.Fprintln(stderr, "usage: activity-relay-directory admin enrollment status|open|close [--operator ID]")
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
		fmt.Fprintln(stderr, "usage: activity-relay-directory admin enrollment status|open|close [--operator ID]")
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
