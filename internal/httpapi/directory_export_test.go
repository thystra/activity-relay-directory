package httpapi

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/thystra/activity-relay-directory/internal/directoryexport"
	"github.com/thystra/activity-relay-directory/internal/storage"
)

func TestDirectoryExportDownloadsUseTierScopesAndCacheValidators(t *testing.T) {
	now := time.Unix(20_000_000, 0).UTC()
	checked := now.Unix()
	old := now.Add(-storage.DirectoryGraveyardAfter).Unix()

	tier1 := directoryProjectionRelayForTest(now, "https://one.example/actor", storage.DirectoryTierHeartbeatOnline)
	tier1.LifecycleKnown = true
	tier1.Registered = true
	tier1.Discovered = false
	tier1.HeartbeatState = storage.HeartbeatHealthy
	tier1.LastSeenUnix = &checked
	tier1.LastHeartbeatUnix = &checked
	tier2 := directoryProjectionRelayForTest(now, "https://two.example:8443/actor", storage.DirectoryTierOnline)
	tier3 := directoryProjectionRelayForTest(now, "https://three.example/actor", storage.DirectoryTierUnavailable)
	tier4 := directoryProjectionRelayForTest(now, "https://four.example/actor", storage.DirectoryTierGraveyard)
	tier4.FirstKnownUnix = old

	// The helper's unavailable fixtures already carry non-fresh evidence. Keep
	// an explicit current check variable referenced here so this test catches a
	// future helper regression that accidentally makes tier 2 stale.
	tier2.ActorLastCheckedUnix = &checked
	tier2.ActorLastSuccessUnix = &checked

	repository := &publicListingRepositoryStub{directoryPage: storage.DirectoryProjectionPage{
		Relays: []storage.DirectoryProjectionRelay{tier1, tier2, tier3, tier4},
	}}
	handler, err := NewPublicListingHandler(repository, func() time.Time { return now })
	if err != nil {
		t.Fatalf("NewPublicListingHandler() error = %v", err)
	}

	for _, test := range []struct {
		name     string
		path     string
		scope    directoryexport.Scope
		filename string
		want     string
	}{
		{
			name: "active", path: directoryActiveDownloadPath, scope: directoryexport.ScopeActive,
			filename: "activity-relay-directory-active.txt",
			want:     "one.example\ntwo.example:8443\n",
		},
		{
			name: "all", path: directoryAllDownloadPath, scope: directoryexport.ScopeAll,
			filename: "activity-relay-directory-all.txt",
			want:     "one.example\ntwo.example:8443\nthree.example\nfour.example\n",
		},
		{
			name: "unavailable", path: directoryUnavailableDownloadPath, scope: directoryexport.ScopeUnavailable,
			filename: "activity-relay-directory-unavailable.txt",
			want:     "three.example\nfour.example\n",
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			response := httptest.NewRecorder()
			handler.serveDirectoryExport(
				response,
				httptest.NewRequest(http.MethodGet, test.path, nil),
				test.scope,
				test.filename,
			)
			if response.Code != http.StatusOK || response.Body.String() != test.want {
				t.Fatalf("download = status %d body %q", response.Code, response.Body.String())
			}
			if response.Header().Get("Content-Type") != "text/plain; charset=utf-8" ||
				response.Header().Get("Cache-Control") != publicListingCacheControl ||
				response.Header().Get("Content-Disposition") != `attachment; filename="`+test.filename+`"` ||
				response.Header().Get("ETag") == "" {
				t.Fatalf("headers = %#v", response.Header())
			}

			conditional := httptest.NewRequest(http.MethodGet, test.path, nil)
			conditional.Header.Set("If-None-Match", response.Header().Get("ETag"))
			conditionalResponse := httptest.NewRecorder()
			handler.serveDirectoryExport(conditionalResponse, conditional, test.scope, test.filename)
			if conditionalResponse.Code != http.StatusNotModified || conditionalResponse.Body.Len() != 0 {
				t.Fatalf("conditional = status %d body %q", conditionalResponse.Code, conditionalResponse.Body.String())
			}
		})
	}
}

func TestDirectoryExportRejectsQueryAndSuppressesHeadBody(t *testing.T) {
	now := time.Unix(20_000_000, 0).UTC()
	repository := &publicListingRepositoryStub{directoryPage: storage.DirectoryProjectionPage{}}
	handler, err := NewPublicListingHandler(repository, func() time.Time { return now })
	if err != nil {
		t.Fatalf("NewPublicListingHandler() error = %v", err)
	}

	bad := httptest.NewRecorder()
	handler.serveDirectoryExport(
		bad,
		httptest.NewRequest(http.MethodGet, directoryActiveDownloadPath+"?scope=all", nil),
		directoryexport.ScopeActive,
		"activity-relay-directory-active.txt",
	)
	if bad.Code != http.StatusBadRequest || !strings.Contains(bad.Body.String(), "invalid directory download request") ||
		bad.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("query response = status %d headers %#v body %q", bad.Code, bad.Header(), bad.Body.String())
	}

	head := httptest.NewRecorder()
	handler.serveDirectoryExport(
		head,
		httptest.NewRequest(http.MethodHead, directoryActiveDownloadPath, nil),
		directoryexport.ScopeActive,
		"activity-relay-directory-active.txt",
	)
	if head.Code != http.StatusOK || head.Body.Len() != 0 || head.Header().Get("ETag") == "" {
		t.Fatalf("HEAD response = status %d headers %#v body %q", head.Code, head.Header(), head.Body.String())
	}
}
