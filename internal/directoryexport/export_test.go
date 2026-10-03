package directoryexport

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/thystra/activity-relay-directory/internal/storage"
)

const exportTestObservedUnix int64 = 20_000_000

type fakeRepository struct {
	pages      []storage.DirectoryProjectionPage
	calls      []storage.DirectoryProjectionQuery
	err        error
	profiles   map[string]storage.RelayProfile
	profileErr error
}

func (repository *fakeRepository) ListDirectoryRelays(
	_ context.Context,
	query storage.DirectoryProjectionQuery,
) (storage.DirectoryProjectionPage, error) {
	repository.calls = append(repository.calls, query)
	if repository.err != nil {
		return storage.DirectoryProjectionPage{}, repository.err
	}
	index := len(repository.calls) - 1
	if index >= len(repository.pages) {
		return storage.DirectoryProjectionPage{}, nil
	}
	return repository.pages[index], nil
}

func (repository *fakeRepository) EffectiveProfile(
	_ context.Context,
	relayActor string,
) (storage.RelayProfile, error) {
	if repository.profileErr != nil {
		return storage.RelayProfile{}, repository.profileErr
	}
	profile, ok := repository.profiles[relayActor]
	if !ok {
		return storage.RelayProfile{}, storage.ErrProfileAbsent
	}
	return profile, nil
}

func projectionRelay(actor string, tier storage.DirectoryTier) storage.DirectoryProjectionRelay {
	observed := exportTestObservedUnix
	checked := observed
	firstKnown := observed - 1_000_000
	relay := storage.DirectoryProjectionRelay{
		RelayActor:           actor,
		PublicBaseURL:        actor[:len(actor)-len("/actor")],
		Discovered:           true,
		FirstKnownUnix:       firstKnown,
		Tier:                 tier,
		HeartbeatState:       storage.HeartbeatNotObserved,
		ActorState:           storage.ReachabilityReachable,
		ActorLastCheckedUnix: &checked,
		ActorLastSuccessUnix: &checked,
		InboxProbeState:      storage.InboxNotChecked,
	}
	if tier == storage.DirectoryTierUnavailable || tier == storage.DirectoryTierGraveyard {
		relay.ActorState = storage.ReachabilityUnreachable
		relay.ActorLastSuccessUnix = nil
		if tier == storage.DirectoryTierGraveyard {
			relay.FirstKnownUnix = observed - int64(storage.DirectoryGraveyardAfter/time.Second)
		}
	}
	return relay
}

func TestRenderScopesAndFormats(t *testing.T) {
	observed := time.Unix(exportTestObservedUnix, 0).UTC()
	tier1 := projectionRelay("https://one.example/actor", storage.DirectoryTierHeartbeatOnline)
	tier1.LifecycleKnown = true
	tier1.Registered = true
	lastSeen := exportTestObservedUnix
	lastHeartbeat := exportTestObservedUnix
	tier1.LastSeenUnix = &lastSeen
	tier1.LastHeartbeatUnix = &lastHeartbeat
	tier1.HeartbeatState = storage.HeartbeatHealthy

	tier2 := projectionRelay("https://two.example:8443/actor", storage.DirectoryTierOnline)
	tier3 := projectionRelay("https://three.example/actor", storage.DirectoryTierUnavailable)
	tier4 := projectionRelay("https://four.example/actor", storage.DirectoryTierGraveyard)

	for _, test := range []struct {
		name   string
		scope  Scope
		format Format
		want   string
	}{
		{
			name:  "active hosts",
			scope: ScopeActive, format: FormatHosts,
			want: "one.example\ntwo.example:8443\n",
		},
		{
			name:  "unavailable actors",
			scope: ScopeUnavailable, format: FormatActors,
			want: "https://three.example/actor\nhttps://four.example/actor\n",
		},
		{
			name:  "all hosts",
			scope: ScopeAll, format: FormatHosts,
			want: "one.example\ntwo.example:8443\nthree.example\nfour.example\n",
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			repository := &fakeRepository{
				pages: []storage.DirectoryProjectionPage{{
					Relays: []storage.DirectoryProjectionRelay{tier1, tier2, tier3, tier4},
				}},
			}
			body, err := Render(context.Background(), repository, Request{
				Scope: test.scope, Format: test.format, ObservedAt: observed,
			})
			if err != nil || string(body) != test.want {
				t.Fatalf("Render() = %q, %v; want %q", body, err, test.want)
			}
		})
	}
}

func TestRenderSortsAlphabeticallyInsideTierWithoutRecencyRanking(t *testing.T) {
	observed := time.Unix(exportTestObservedUnix, 0).UTC()
	repository := &fakeRepository{pages: []storage.DirectoryProjectionPage{{
		Relays: []storage.DirectoryProjectionRelay{
			projectionRelay("https://z.example/actor", storage.DirectoryTierOnline),
			projectionRelay("https://a.example/actor", storage.DirectoryTierOnline),
		},
	}}}
	body, err := Render(context.Background(), repository, Request{
		Scope: ScopeActive, Format: FormatHosts, ObservedAt: observed,
	})
	if err != nil || string(body) != "a.example\nz.example\n" {
		t.Fatalf("alphabetical Render() = %q, %v", body, err)
	}
}

func TestRenderFollowsEmptyContinuationAndRejectsNonProgress(t *testing.T) {
	observed := time.Unix(exportTestObservedUnix, 0).UTC()
	cursor := storage.DirectoryProjectionCursor{
		Tier: storage.DirectoryTierOnline, RelayActor: "https://middle.example/actor",
	}
	repository := &fakeRepository{pages: []storage.DirectoryProjectionPage{
		{Next: cursor},
		{Relays: []storage.DirectoryProjectionRelay{
			projectionRelay("https://z.example/actor", storage.DirectoryTierOnline),
		}},
	}}
	body, err := Render(context.Background(), repository, Request{
		Scope: ScopeActive, Format: FormatHosts, ObservedAt: observed,
	})
	if err != nil || string(body) != "z.example\n" || len(repository.calls) != 2 ||
		repository.calls[1].After != cursor {
		t.Fatalf("Render continuation = %q, calls=%#v, err=%v", body, repository.calls, err)
	}

	repository = &fakeRepository{pages: []storage.DirectoryProjectionPage{
		{Next: cursor},
		{Next: cursor},
	}}
	if _, err := Render(context.Background(), repository, Request{
		Scope: ScopeActive, Format: FormatHosts, ObservedAt: observed,
	}); !errors.Is(err, ErrExportData) {
		t.Fatalf("non-progress Render() error = %v", err)
	}
}

func TestRenderRejectsInvalidConfigurationAndRepositoryError(t *testing.T) {
	observed := time.Unix(exportTestObservedUnix, 0).UTC()
	if _, err := Render(context.Background(), nil, Request{
		Scope: ScopeActive, Format: FormatHosts, ObservedAt: observed,
	}); !errors.Is(err, ErrExportConfiguration) {
		t.Fatalf("nil repository error = %v", err)
	}
	repository := &fakeRepository{err: errors.New("boom")}
	if _, err := Render(context.Background(), repository, Request{
		Scope: ScopeActive, Format: FormatHosts, ObservedAt: observed,
	}); err == nil || errors.Is(err, ErrExportConfiguration) {
		t.Fatalf("repository error = %v", err)
	}
}

func TestRenderCSVUsesEffectiveProfilesAndProtectsSpreadsheetCells(t *testing.T) {
	observed := time.Unix(exportTestObservedUnix, 0).UTC()
	one := projectionRelay("https://one.example/actor", storage.DirectoryTierOnline)
	two := projectionRelay("https://two.example/actor", storage.DirectoryTierOnline)
	repository := &fakeRepository{
		pages: []storage.DirectoryProjectionPage{{Relays: []storage.DirectoryProjectionRelay{two, one}}},
		profiles: map[string]storage.RelayProfile{
			one.RelayActor: {Languages: []string{"en"}, Notes: "=formula"},
			two.RelayActor: {Topics: []string{"federation", "technology"}},
		},
	}
	body, err := Render(context.Background(), repository, Request{
		Scope: ScopeAll, Format: FormatCSV, ObservedAt: observed,
	})
	if err != nil {
		t.Fatalf("Render(CSV) error = %v", err)
	}
	text := string(body)
	if !strings.HasPrefix(text, "relay,participation_mode,availability,relay_type,languages,countries,regions,topics,contact_fediverse,contact_email,contact_url,participation_url,notes\n") ||
		!strings.Contains(text, "https://one.example/actor,,,,en,,,,,,,,'=formula\n") ||
		!strings.Contains(text, "https://two.example/actor,,,,,,,federation;technology,,,,,\n") ||
		strings.Index(text, "one.example") > strings.Index(text, "two.example") {
		t.Fatalf("Render(CSV) = %q", text)
	}
}

type projectionOnlyRepository struct {
	page storage.DirectoryProjectionPage
}

func (repository *projectionOnlyRepository) ListDirectoryRelays(
	_ context.Context,
	_ storage.DirectoryProjectionQuery,
) (storage.DirectoryProjectionPage, error) {
	return repository.page, nil
}

func TestRenderCSVRequiresProfileRepository(t *testing.T) {
	observed := time.Unix(exportTestObservedUnix, 0).UTC()
	repository := &projectionOnlyRepository{page: storage.DirectoryProjectionPage{
		Relays: []storage.DirectoryProjectionRelay{
			projectionRelay("https://one.example/actor", storage.DirectoryTierOnline),
		},
	}}
	if _, err := Render(context.Background(), repository, Request{
		Scope: ScopeAll, Format: FormatCSV, ObservedAt: observed,
	}); !errors.Is(err, ErrExportConfiguration) {
		t.Fatalf("Render(CSV projection-only) error = %v", err)
	}
}
