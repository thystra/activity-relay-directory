package exportcommand

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"time"

	"github.com/thystra/activity-relay-directory/internal/directoryexport"
	"github.com/thystra/activity-relay-directory/internal/storage"
)

const (
	ExitSuccess     = 0
	ExitOperational = 1
	ExitUsage       = 2
)

var ErrInvalidCommand = errors.New("directory export command is invalid")

type Request struct {
	Scope  directoryexport.Scope
	Format directoryexport.Format
}

func Parse(arguments []string) (Request, error) {
	flags := flag.NewFlagSet("admin export", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	scope := flags.String("scope", string(directoryexport.ScopeActive), "active, all, or unavailable")
	format := flags.String("format", string(directoryexport.FormatHosts), "hosts or actors")
	if err := flags.Parse(arguments); err != nil || flags.NArg() != 0 {
		return Request{}, ErrInvalidCommand
	}
	request := Request{
		Scope:  directoryexport.Scope(*scope),
		Format: directoryexport.Format(*format),
	}
	if !request.Scope.Valid() || !request.Format.Valid() {
		return Request{}, ErrInvalidCommand
	}
	return request, nil
}

func Execute(
	ctx context.Context,
	request Request,
	repository storage.DirectoryProjectionRepository,
	stdout, stderr io.Writer,
	observedAt time.Time,
) int {
	if ctx == nil || repository == nil || stdout == nil || stderr == nil ||
		!request.Scope.Valid() || !request.Format.Valid() {
		return writeFailure(stderr, ExitOperational, "directory export unavailable")
	}
	body, err := directoryexport.Render(ctx, repository, directoryexport.Request{
		Scope:      request.Scope,
		Format:     request.Format,
		ObservedAt: observedAt,
	})
	if err != nil {
		return writeFailure(stderr, ExitOperational, "directory export failed")
	}
	if _, err := stdout.Write(body); err != nil {
		return writeFailure(stderr, ExitOperational, "directory export output failed")
	}
	return ExitSuccess
}

func writeFailure(output io.Writer, code int, message string) int {
	if output != nil {
		_, _ = fmt.Fprintln(output, message)
	}
	return code
}
