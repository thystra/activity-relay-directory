// Package retentioncommand implements the local inactive-retention dry-run and
// explicitly confirmed destructive purge adapters. It owns no database opener
// and exposes no public HTTP route.
package retentioncommand

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/thystra/activity-relay-directory/internal/retention"
	"github.com/thystra/activity-relay-directory/internal/storage"
)

const (
	ExitSuccess     = 0
	ExitOperational = 1
	ExitUsage       = 2
	ExitCanceled    = 4

	OutputHuman = OutputFormat("human")
	OutputJSON  = OutputFormat("json")

	outputSchema = "activity-relay-directory.retention-admin.v1"
)

var ErrInvalidCommand = errors.New("local inactive-retention command is invalid")

// OutputFormat selects the stable identity-free local representation.
type OutputFormat string

func (format OutputFormat) valid() bool { return format == OutputHuman || format == OutputJSON }

// Request is one parsed local retention command.
type Request struct {
	Action     string
	BackupPath string
	Yes        bool
	Format     OutputFormat
}

// Parse accepts exactly dry-run or purge. Purge requires a backup path; dry-run
// remains read-only and rejects destructive-only flags.
func Parse(arguments []string) (Request, error) {
	if len(arguments) == 0 || (arguments[0] != "dry-run" && arguments[0] != "purge") {
		return Request{}, ErrInvalidCommand
	}
	request := Request{Action: arguments[0], Format: OutputHuman}
	flags := flag.NewFlagSet("retention "+arguments[0], flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	backup := flags.String("backup", "", "verified pre-retention SQLite backup")
	yes := flags.Bool("yes", false, "confirm destructive purge")
	format := flags.String("format", string(OutputHuman), "human or json")
	if err := flags.Parse(arguments[1:]); err != nil || flags.NArg() != 0 {
		return Request{}, ErrInvalidCommand
	}
	request.BackupPath = strings.TrimSpace(*backup)
	request.Yes = *yes
	request.Format = OutputFormat(*format)
	if !request.Format.valid() {
		return Request{}, ErrInvalidCommand
	}
	if request.Action == "dry-run" {
		if request.BackupPath != "" || request.Yes {
			return Request{}, ErrInvalidCommand
		}
		return request, nil
	}
	if request.BackupPath == "" {
		return Request{}, ErrInvalidCommand
	}
	return request, nil
}

// Confirm requires an explicit policy-specific phrase unless --yes was given.
func Confirm(request Request, input io.Reader, output io.Writer, retentionDays int) error {
	if request.Action != "purge" || retentionDays <= 0 {
		return ErrInvalidCommand
	}
	if request.Yes {
		return nil
	}
	if input == nil || output == nil {
		return ErrInvalidCommand
	}
	phrase := fmt.Sprintf("PURGE %d", retentionDays)
	if _, err := fmt.Fprintf(
		output,
		"This permanently deletes eligible inactive relay rows and lifecycle events.\nType %q to continue: ",
		phrase,
	); err != nil {
		return err
	}
	scanner := bufio.NewScanner(input)
	if !scanner.Scan() {
		if err := scanner.Err(); err != nil {
			return err
		}
		return ErrInvalidCommand
	}
	if scanner.Text() != phrase {
		return ErrInvalidCommand
	}
	return nil
}

// ExecuteDryRun performs an identity-free bounded summary with no writes.
func ExecuteDryRun(
	ctx context.Context,
	request Request,
	repository interface {
		PurgeCandidates(context.Context, storage.PurgeCandidateQuery) (storage.PurgeCandidatePage, error)
	},
	retentionDays int,
	standardOutput io.Writer,
	errorOutput io.Writer,
	now func() time.Time,
) int {
	if ctx == nil || repository == nil || standardOutput == nil || errorOutput == nil ||
		now == nil || request.Action != "dry-run" || !request.Format.valid() ||
		retentionDays < 0 || retentionDays > storage.MaximumInactiveRetentionDays {
		return writeFailure(errorOutput, ExitOperational, "inactive-retention dry-run unavailable")
	}
	summary, err := retention.Summarize(ctx, repository, retentionDays, now())
	if err != nil {
		return classifyFailure(errorOutput, err, "inactive-retention dry-run")
	}
	return writeSummary(request.Format, standardOutput, errorOutput, retentionDays, summary)
}

// ExecutePurge runs bounded destructive retention only after the caller has
// verified the backup and supplied its lowercase SHA-256 digest.
func ExecutePurge(
	ctx context.Context,
	request Request,
	repository storage.RetentionRepository,
	retentionDays int,
	backupSHA256 string,
	standardOutput io.Writer,
	errorOutput io.Writer,
	now func() time.Time,
) int {
	if ctx == nil || repository == nil || standardOutput == nil || errorOutput == nil ||
		now == nil || request.Action != "purge" || !request.Format.valid() ||
		retentionDays <= 0 || retentionDays > storage.MaximumInactiveRetentionDays ||
		backupSHA256 == "" {
		return writeFailure(errorOutput, ExitOperational, "inactive-retention purge unavailable")
	}
	result, err := retention.Run(ctx, repository, retentionDays, now(), backupSHA256)
	if err != nil {
		return classifyFailure(errorOutput, err, "inactive-retention purge")
	}
	if request.Format == OutputJSON {
		return writeJSON(errorOutput, standardOutput, purgeDocument{
			Schema:                outputSchema,
			Kind:                  "inactive_retention_purge",
			RetentionDays:         retentionDays,
			ObservedUnix:          result.ObservedUnix,
			CutoffUnix:            result.CutoffUnix,
			CandidatesScanned:     result.CandidateCount,
			PurgedRelays:          result.PurgedRelays,
			PurgedLifecycleEvents: result.PurgedLifecycleEvents,
			Skipped:               result.Skipped,
			Batches:               result.Batches,
			Truncated:             result.Truncated,
			BackupSHA256:          backupSHA256,
		})
	}
	_, err = fmt.Fprintf(
		standardOutput,
		"retention_days=%d\nobserved_at_unix=%d\ncutoff_at_unix=%d\ncandidates_scanned=%d\npurged_relays=%d\npurged_lifecycle_events=%d\nskipped=%d\nbatches=%d\ntruncated=%t\nbackup_sha256=%s\n",
		retentionDays,
		result.ObservedUnix,
		result.CutoffUnix,
		result.CandidateCount,
		result.PurgedRelays,
		result.PurgedLifecycleEvents,
		result.Skipped,
		result.Batches,
		result.Truncated,
		backupSHA256,
	)
	if err != nil {
		return writeFailure(errorOutput, ExitOperational, "inactive-retention output failed")
	}
	return ExitSuccess
}

func writeSummary(
	format OutputFormat,
	standardOutput, errorOutput io.Writer,
	retentionDays int,
	summary retention.Summary,
) int {
	enabled := retentionDays > 0
	if format == OutputJSON {
		return writeJSON(errorOutput, standardOutput, dryRunDocument{
			Schema:             outputSchema,
			Kind:               "inactive_retention_dry_run",
			Enabled:            enabled,
			RetentionDays:      retentionDays,
			ObservedUnix:       summary.ObservedUnix,
			CutoffUnix:         summary.CutoffUnix,
			CandidateCount:     summary.CandidateCount,
			OldestInactiveUnix: summary.OldestInactiveUnix,
			NewestInactiveUnix: summary.NewestInactiveUnix,
			Batches:            summary.Batches,
			Truncated:          summary.Truncated,
		})
	}
	oldest, newest := "-", "-"
	if summary.OldestInactiveUnix != nil {
		oldest = fmt.Sprintf("%d", *summary.OldestInactiveUnix)
	}
	if summary.NewestInactiveUnix != nil {
		newest = fmt.Sprintf("%d", *summary.NewestInactiveUnix)
	}
	_, err := fmt.Fprintf(
		standardOutput,
		"enabled=%t\nretention_days=%d\nobserved_at_unix=%d\ncutoff_at_unix=%d\ncandidate_count=%d\noldest_candidate_at_unix=%s\nnewest_candidate_at_unix=%s\nbatches=%d\ntruncated=%t\n",
		enabled,
		retentionDays,
		summary.ObservedUnix,
		summary.CutoffUnix,
		summary.CandidateCount,
		oldest,
		newest,
		summary.Batches,
		summary.Truncated,
	)
	if err != nil {
		return writeFailure(errorOutput, ExitOperational, "inactive-retention output failed")
	}
	return ExitSuccess
}

func classifyFailure(output io.Writer, err error, operation string) int {
	switch {
	case errors.Is(err, context.Canceled), errors.Is(err, context.DeadlineExceeded):
		return writeFailure(output, ExitCanceled, operation+" canceled")
	case errors.Is(err, retention.ErrConfiguration),
		errors.Is(err, storage.ErrRetentionReadInput),
		errors.Is(err, storage.ErrRetentionWriteInput):
		return writeFailure(output, ExitUsage, operation+" is invalid")
	default:
		return writeFailure(output, ExitOperational, operation+" failed")
	}
}

func writeFailure(output io.Writer, code int, message string) int {
	if output != nil {
		_, _ = fmt.Fprintln(output, message)
	}
	return code
}

func writeJSON(errorOutput, standardOutput io.Writer, document any) int {
	encoder := json.NewEncoder(standardOutput)
	encoder.SetEscapeHTML(false)
	if err := encoder.Encode(document); err != nil {
		return writeFailure(errorOutput, ExitOperational, "inactive-retention output failed")
	}
	return ExitSuccess
}

type dryRunDocument struct {
	Schema             string `json:"schema"`
	Kind               string `json:"kind"`
	Enabled            bool   `json:"enabled"`
	RetentionDays      int    `json:"retention_days"`
	ObservedUnix       int64  `json:"observed_at_unix"`
	CutoffUnix         int64  `json:"cutoff_at_unix"`
	CandidateCount     int    `json:"candidate_count"`
	OldestInactiveUnix *int64 `json:"oldest_candidate_at_unix"`
	NewestInactiveUnix *int64 `json:"newest_candidate_at_unix"`
	Batches            int    `json:"batches"`
	Truncated          bool   `json:"truncated"`
}

type purgeDocument struct {
	Schema                string `json:"schema"`
	Kind                  string `json:"kind"`
	RetentionDays         int    `json:"retention_days"`
	ObservedUnix          int64  `json:"observed_at_unix"`
	CutoffUnix            int64  `json:"cutoff_at_unix"`
	CandidatesScanned     int    `json:"candidates_scanned"`
	PurgedRelays          int    `json:"purged_relays"`
	PurgedLifecycleEvents int    `json:"purged_lifecycle_events"`
	Skipped               int    `json:"skipped"`
	Batches               int    `json:"batches"`
	Truncated             bool   `json:"truncated"`
	BackupSHA256          string `json:"backup_sha256"`
}
