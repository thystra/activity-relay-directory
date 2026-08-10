// Package prunecommand implements the local read-only soft-pruning dry-run
// command. It contains no database opener, scheduler, or public HTTP route.
package prunecommand

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"strconv"
	"time"

	v1 "github.com/thystra/activity-relay-directory/internal/protocol/v1"
	"github.com/thystra/activity-relay-directory/internal/storage"
)

const (
	ExitSuccess     = 0
	ExitOperational = 1
	ExitUsage       = 2
	ExitCanceled    = 4

	DefaultLimit = 50

	OutputHuman = OutputFormat("human")
	OutputJSON  = OutputFormat("json")

	outputSchema = "activity-relay-directory.pruning-admin.v1"
)

var ErrInvalidCommand = errors.New("local soft-pruning command is invalid")

// OutputFormat selects the stable local representation.
type OutputFormat string

func (format OutputFormat) valid() bool {
	return format == OutputHuman || format == OutputJSON
}

// Request is one fully parsed read-only dry-run request.
type Request struct {
	Limit  int
	After  storage.PruneCandidateCursor
	Format OutputFormat
}

// Repository is the private read capability required by the dry-run adapter.
type Repository interface {
	PruneCandidates(context.Context, storage.PruneCandidateQuery) (storage.PruneCandidatePage, error)
}

// Parse accepts exactly `dry-run` plus bounded cursor, limit, and format flags.
func Parse(arguments []string) (Request, error) {
	if len(arguments) == 0 || arguments[0] != "dry-run" {
		return Request{}, ErrInvalidCommand
	}
	request := Request{Limit: DefaultLimit, Format: OutputHuman}
	flags := flag.NewFlagSet("pruning dry-run", flag.ContinueOnError)
	flags.SetOutput(io.Discard)

	limit := uniqueInt{value: DefaultLimit}
	afterSeen := uniqueInt64{}
	afterActor := uniqueString{}
	format := uniqueString{value: string(OutputHuman)}
	flags.Var(&limit, "limit", "candidate page size")
	flags.Var(&afterSeen, "after-last-seen", "keyset last-seen timestamp")
	flags.Var(&afterActor, "after-actor", "keyset canonical relay actor")
	flags.Var(&format, "format", "human or json")

	if err := flags.Parse(arguments[1:]); err != nil || flags.NArg() != 0 {
		return Request{}, ErrInvalidCommand
	}
	if limit.value <= 0 || limit.value > storage.MaximumPruneCandidatePage {
		return Request{}, ErrInvalidCommand
	}
	request.Limit = limit.value
	request.Format = OutputFormat(format.value)
	if !request.Format.valid() || afterSeen.set != afterActor.set {
		return Request{}, ErrInvalidCommand
	}
	if afterSeen.set {
		canonical, err := v1.NormalizeRelayActorURL(afterActor.value)
		if err != nil || canonical != afterActor.value {
			return Request{}, ErrInvalidCommand
		}
		request.After = storage.PruneCandidateCursor{
			LastSeenUnix: afterSeen.value,
			RelayActor:   afterActor.value,
		}
		if !request.After.Valid() || request.After == (storage.PruneCandidateCursor{}) {
			return Request{}, ErrInvalidCommand
		}
	}
	return request, nil
}

// Execute performs a bounded, read-only candidate projection at one captured
// server time.
func Execute(
	ctx context.Context,
	request Request,
	repository Repository,
	standardOutput io.Writer,
	errorOutput io.Writer,
	now func() time.Time,
) int {
	if ctx == nil || repository == nil || standardOutput == nil ||
		errorOutput == nil || now == nil || request.Limit <= 0 ||
		request.Limit > storage.MaximumPruneCandidatePage ||
		!request.After.Valid() || !request.Format.valid() {
		return writeFailure(errorOutput, ExitOperational, "soft-pruning dry-run unavailable")
	}

	observedAt := now().UTC()
	page, err := repository.PruneCandidates(ctx, storage.PruneCandidateQuery{
		After:      request.After,
		Limit:      request.Limit,
		ObservedAt: observedAt,
	})
	if err != nil {
		switch {
		case errors.Is(err, context.Canceled), errors.Is(err, context.DeadlineExceeded):
			return writeFailure(errorOutput, ExitCanceled, "soft-pruning dry-run canceled")
		case errors.Is(err, storage.ErrPruningReadInput):
			return writeFailure(errorOutput, ExitUsage, "soft-pruning dry-run is invalid")
		default:
			return writeFailure(errorOutput, ExitOperational, "soft-pruning dry-run failed")
		}
	}

	if request.Format == OutputJSON {
		candidates := make([]candidateDocument, 0, len(page.Candidates))
		for _, candidate := range page.Candidates {
			candidates = append(candidates, candidateDocument{
				RelayActor:          candidate.RelayActor,
				PublicBaseURL:       candidate.PublicBaseURL,
				AdministrativeState: candidate.AdministrativeState,
				LastSeenUnix:        candidate.LastSeenUnix,
			})
		}
		return writeJSON(errorOutput, standardOutput, dryRunDocument{
			Schema:           outputSchema,
			Kind:             "soft_pruning_dry_run",
			ObservedUnix:     observedAt.Unix(),
			Candidates:       candidates,
			NextLastSeenUnix: page.Next.LastSeenUnix,
			NextRelayActor:   page.Next.RelayActor,
		})
	}

	if _, err := fmt.Fprintf(
		standardOutput,
		"observed_at_unix=%d\ncount=%d\n",
		observedAt.Unix(),
		len(page.Candidates),
	); err != nil {
		return writeFailure(errorOutput, ExitOperational, "soft-pruning output failed")
	}
	for _, candidate := range page.Candidates {
		if _, err := fmt.Fprintf(
			standardOutput,
			"last_seen_at_unix=%d administrative_state=%s relay_actor=%s public_base_url=%s\n",
			candidate.LastSeenUnix,
			candidate.AdministrativeState,
			candidate.RelayActor,
			candidate.PublicBaseURL,
		); err != nil {
			return writeFailure(errorOutput, ExitOperational, "soft-pruning output failed")
		}
	}
	if page.Next == (storage.PruneCandidateCursor{}) {
		_, err = fmt.Fprint(standardOutput, "next_after_last_seen_at_unix=-\nnext_after_actor=-\n")
	} else {
		_, err = fmt.Fprintf(
			standardOutput,
			"next_after_last_seen_at_unix=%d\nnext_after_actor=%s\n",
			page.Next.LastSeenUnix,
			page.Next.RelayActor,
		)
	}
	if err != nil {
		return writeFailure(errorOutput, ExitOperational, "soft-pruning output failed")
	}
	return ExitSuccess
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
		return writeFailure(errorOutput, ExitOperational, "soft-pruning output failed")
	}
	return ExitSuccess
}

type dryRunDocument struct {
	Schema           string              `json:"schema"`
	Kind             string              `json:"kind"`
	ObservedUnix     int64               `json:"observed_at_unix"`
	Candidates       []candidateDocument `json:"candidates"`
	NextLastSeenUnix int64               `json:"next_after_last_seen_at_unix"`
	NextRelayActor   string              `json:"next_after_actor"`
}

type candidateDocument struct {
	RelayActor          string                           `json:"relay_actor"`
	PublicBaseURL       string                           `json:"public_base_url"`
	AdministrativeState storage.RelayAdministrativeState `json:"administrative_state"`
	LastSeenUnix        int64                            `json:"last_seen_at_unix"`
}

type uniqueString struct {
	set   bool
	value string
}

func (value *uniqueString) String() string { return value.value }
func (value *uniqueString) Set(raw string) error {
	if value.set {
		return ErrInvalidCommand
	}
	value.set = true
	value.value = raw
	return nil
}

type uniqueInt struct {
	set   bool
	value int
}

func (value *uniqueInt) String() string { return strconv.Itoa(value.value) }
func (value *uniqueInt) Set(raw string) error {
	if value.set {
		return ErrInvalidCommand
	}
	parsed, err := strconv.Atoi(raw)
	if err != nil || strconv.Itoa(parsed) != raw {
		return ErrInvalidCommand
	}
	value.set = true
	value.value = parsed
	return nil
}

type uniqueInt64 struct {
	set   bool
	value int64
}

func (value *uniqueInt64) String() string { return strconv.FormatInt(value.value, 10) }
func (value *uniqueInt64) Set(raw string) error {
	if value.set {
		return ErrInvalidCommand
	}
	parsed, err := strconv.ParseInt(raw, 10, 64)
	if err != nil || strconv.FormatInt(parsed, 10) != raw || parsed < 0 {
		return ErrInvalidCommand
	}
	value.set = true
	value.value = parsed
	return nil
}
