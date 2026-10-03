package exportcommand

import (
	"bytes"
	"context"
	"testing"
	"time"

	"github.com/thystra/activity-relay-directory/internal/directoryexport"
	"github.com/thystra/activity-relay-directory/internal/storage"
)

type fakeRepository struct {
	page storage.DirectoryProjectionPage
}

func (repository fakeRepository) ListDirectoryRelays(
	_ context.Context,
	_ storage.DirectoryProjectionQuery,
) (storage.DirectoryProjectionPage, error) {
	return repository.page, nil
}

func TestParseDefaultsAndValidates(t *testing.T) {
	request, err := Parse(nil)
	if err != nil || request.Scope != directoryexport.ScopeActive ||
		request.Format != directoryexport.FormatHosts {
		t.Fatalf("Parse defaults = %#v, %v", request, err)
	}
	request, err = Parse([]string{"--scope", "unavailable", "--format", "actors"})
	if err != nil || request.Scope != directoryexport.ScopeUnavailable ||
		request.Format != directoryexport.FormatActors {
		t.Fatalf("Parse explicit = %#v, %v", request, err)
	}
	for _, arguments := range [][]string{
		{"--scope", "unknown"},
		{"--format", "json"},
		{"extra"},
	} {
		if request, err := Parse(arguments); err == nil || request != (Request{}) {
			t.Fatalf("Parse(%q) = %#v, %v", arguments, request, err)
		}
	}
}

func TestExecuteWritesRequestedExport(t *testing.T) {
	observed := time.Unix(2_000_000, 0).UTC()
	checked := observed.Unix()
	repository := fakeRepository{page: storage.DirectoryProjectionPage{
		Relays: []storage.DirectoryProjectionRelay{{
			RelayActor:           "https://relay.example/actor",
			PublicBaseURL:        "https://relay.example",
			Discovered:           true,
			FirstKnownUnix:       observed.Unix() - 10,
			Tier:                 storage.DirectoryTierOnline,
			HeartbeatState:       storage.HeartbeatNotObserved,
			ActorState:           storage.ReachabilityReachable,
			ActorLastCheckedUnix: &checked,
			ActorLastSuccessUnix: &checked,
			InboxProbeState:      storage.InboxNotChecked,
		}},
	}}
	var stdout, stderr bytes.Buffer
	code := Execute(context.Background(), Request{
		Scope: directoryexport.ScopeActive, Format: directoryexport.FormatHosts,
	}, repository, &stdout, &stderr, observed)
	if code != ExitSuccess || stdout.String() != "relay.example\n" || stderr.Len() != 0 {
		t.Fatalf("Execute() = %d, %q, %q", code, stdout.String(), stderr.String())
	}
}
