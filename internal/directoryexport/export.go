package directoryexport

import (
	"context"
	"errors"
	"net/url"
	"sort"
	"strings"
	"time"

	"github.com/thystra/activity-relay-directory/internal/storage"
)

const (
	MaximumExportPages  = 256
	MaximumExportRelays = 10000
)

var (
	ErrExportConfiguration = errors.New("directory export configuration is invalid")
	ErrExportData          = errors.New("directory export data is invalid")
	ErrExportLimit         = errors.New("directory export exceeds bounded limit")
)

type Scope string

const (
	ScopeActive      Scope = "active"
	ScopeAll         Scope = "all"
	ScopeUnavailable Scope = "unavailable"
)

func (scope Scope) Valid() bool {
	switch scope {
	case ScopeActive, ScopeAll, ScopeUnavailable:
		return true
	default:
		return false
	}
}

type Format string

const (
	FormatHosts  Format = "hosts"
	FormatActors Format = "actors"
)

func (format Format) Valid() bool {
	switch format {
	case FormatHosts, FormatActors:
		return true
	default:
		return false
	}
}

type Request struct {
	Scope      Scope
	Format     Format
	ObservedAt time.Time
}

// Render walks the bounded public directory projection with one captured
// observation time and returns a newline-delimited operator/public export.
// Scope changes only which already-public tiers are emitted; it never exposes
// discovery provenance or private maintenance candidates.
func Render(
	ctx context.Context,
	repository storage.DirectoryProjectionRepository,
	request Request,
) ([]byte, error) {
	if ctx == nil || repository == nil || !request.Scope.Valid() || !request.Format.Valid() {
		return nil, ErrExportConfiguration
	}
	observedUnix := request.ObservedAt.UTC().Unix()
	if observedUnix < 0 {
		return nil, ErrExportConfiguration
	}

	after := storage.DirectoryProjectionCursor{}
	relays := make([]storage.DirectoryProjectionRelay, 0)
	seen := make(map[string]struct{})

	for pageNumber := 0; pageNumber < MaximumExportPages; pageNumber++ {
		page, err := repository.ListDirectoryRelays(ctx, storage.DirectoryProjectionQuery{
			After:      after,
			Limit:      storage.MaximumDirectoryProjectionPage,
			ObservedAt: request.ObservedAt,
		})
		if err != nil {
			return nil, err
		}

		for _, relay := range page.Relays {
			if err := storage.ValidateDirectoryProjectionRelay(relay, observedUnix); err != nil {
				return nil, errors.Join(ErrExportData, err)
			}
			if _, duplicate := seen[relay.RelayActor]; duplicate {
				return nil, ErrExportData
			}
			seen[relay.RelayActor] = struct{}{}
			if !scopeIncludes(request.Scope, relay.Tier) {
				continue
			}
			if len(relays) >= MaximumExportRelays {
				return nil, ErrExportLimit
			}
			relays = append(relays, relay)
		}

		if page.Next == (storage.DirectoryProjectionCursor{}) {
			sortDirectoryRelays(relays)
			return renderRelays(relays, request.Format)
		}
		if !cursorAdvances(after, page.Next) {
			return nil, ErrExportData
		}
		after = page.Next
	}

	return nil, ErrExportLimit
}

func scopeIncludes(scope Scope, tier storage.DirectoryTier) bool {
	switch scope {
	case ScopeActive:
		return tier == storage.DirectoryTierHeartbeatOnline || tier == storage.DirectoryTierOnline
	case ScopeUnavailable:
		return tier == storage.DirectoryTierUnavailable || tier == storage.DirectoryTierGraveyard
	case ScopeAll:
		return tier.Valid()
	default:
		return false
	}
}

func cursorAdvances(previous, next storage.DirectoryProjectionCursor) bool {
	if next == (storage.DirectoryProjectionCursor{}) || !next.Valid() {
		return false
	}
	if previous == (storage.DirectoryProjectionCursor{}) {
		return true
	}
	if next.Tier != previous.Tier {
		return next.Tier > previous.Tier
	}
	return next.RelayActor > previous.RelayActor
}

func sortDirectoryRelays(relays []storage.DirectoryProjectionRelay) {
	sort.Slice(relays, func(i, j int) bool {
		if relays[i].Tier != relays[j].Tier {
			return relays[i].Tier < relays[j].Tier
		}
		return relays[i].RelayActor < relays[j].RelayActor
	})
}

func renderRelays(relays []storage.DirectoryProjectionRelay, format Format) ([]byte, error) {
	var output strings.Builder
	for _, relay := range relays {
		value := relay.RelayActor
		if format == FormatHosts {
			parsed, err := url.Parse(relay.PublicBaseURL)
			if err != nil || parsed.Scheme != "https" || parsed.Host == "" ||
				parsed.User != nil || parsed.Path != "" || parsed.RawQuery != "" || parsed.Fragment != "" {
				return nil, ErrExportData
			}
			value = parsed.Host
		}
		output.WriteString(value)
		output.WriteByte('\n')
	}
	return []byte(output.String()), nil
}
