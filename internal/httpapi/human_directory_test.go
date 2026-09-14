package httpapi

import (
	"html"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/thystra/activity-relay-directory/internal/storage"
)

func TestHumanDirectoryFixtureEscapingCachingAndAccessibility(t *testing.T) {
	now := time.Unix(100_100, 0).UTC()
	lastSeen := int64(100_000)
	checked := int64(100_050)
	declared := int64(100_040)
	verified := int64(100_000)
	repository := &publicListingRepositoryStub{directoryPage: storage.DirectoryProjectionPage{
		Relays: []storage.DirectoryProjectionRelay{{
			RelayActor:           "https://relay.example/a&b",
			PublicBaseURL:        "https://relay.example",
			Registered:           true,
			HeartbeatState:       storage.HeartbeatHealthy,
			LastSeenUnix:         &lastSeen,
			ActorState:           storage.ReachabilityReachable,
			ActorLastCheckedUnix: &checked,
			ActorLastSuccessUnix: &checked,
			InboxURL:             "https://relay.example/inbox/a&b",
			InboxDeclaredUnix:    &declared,
			InboxProbeState:      storage.InboxMethodRejected,
			InboxLastCheckedUnix: &checked,
			RFC9421VerifiedUnix:  &verified,
		}},
	}}
	handler, err := NewPublicListingHandler(repository, func() time.Time { return now })
	if err != nil {
		t.Fatalf("NewPublicListingHandler() error = %v", err)
	}

	response := httptest.NewRecorder()
	handler.serveHumanDirectory(response, httptest.NewRequest(http.MethodGet, "/", nil))
	if response.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %q", response.Code, response.Body.String())
	}
	fixture, err := os.ReadFile("../../testdata/public/v2/directory-page.valid.html")
	if err != nil {
		t.Fatalf("ReadFile(fixture) error = %v", err)
	}
	if response.Body.String() != string(fixture) {
		t.Fatalf("body = %q, want fixture %q", response.Body.String(), fixture)
	}
	if response.Header().Get("Content-Type") != humanDirectoryContentType ||
		response.Header().Get("Cache-Control") != publicListingCacheControl ||
		response.Header().Get("Content-Security-Policy") != humanDirectoryCSP {
		t.Fatalf("headers = %#v", response.Header())
	}
	etag := response.Header().Get("ETag")
	if etag == "" {
		t.Fatal("missing ETag")
	}

	body := response.Body.String()
	for _, required := range []string{
		`<html lang="en">`,
		`href="#directory">Skip to directory</a>`,
		`<main id="directory"`,
		`<nav class="pagination" aria-label="Directory pages">`,
		`<section class="evidence-help panel" aria-labelledby="evidence-heading">`,
		`Heartbeat: healthy`,
		`Reachability: reachable`,
		`Inbox diagnostic`,
		`method rejected`,
		`RFC 9421`,
		`verified`,
		`https://relay.example/a&amp;b`,
		`https://relay.example/inbox/a&amp;b`,
		`href="https://github.com/thystra/activity-relay-directory"`,
		`href="https://github.com/thystra/Activity-Relay"`,
	} {
		if !strings.Contains(body, required) {
			t.Fatalf("body missing %q", required)
		}
	}
	for _, forbidden := range []string{
		"https://relay.example/a&b", "https://relay.example/inbox/a&b",
		"<script", "analytics", "fonts.googleapis",
		"source_kind", "source_label", "reason_code", "operator_id", "discovery_added",
	} {
		if strings.Contains(body, forbidden) {
			t.Fatalf("unsafe/private content %q present: %q", forbidden, body)
		}
	}

	conditional := httptest.NewRequest(http.MethodGet, "/", nil)
	conditional.Header.Set("If-None-Match", "W/"+etag)
	conditionalResponse := httptest.NewRecorder()
	handler.serveHumanDirectory(conditionalResponse, conditional)
	if conditionalResponse.Code != http.StatusNotModified || conditionalResponse.Body.Len() != 0 {
		t.Fatalf("conditional response = status %d body %q", conditionalResponse.Code, conditionalResponse.Body.String())
	}
}

func TestHumanDirectoryHeadSuppressesBodyAndPreservesValidators(t *testing.T) {
	repository := &publicListingRepositoryStub{directoryPage: storage.DirectoryProjectionPage{Relays: []storage.DirectoryProjectionRelay{}}}
	handler, err := NewPublicListingHandler(repository, func() time.Time { return time.Unix(100, 0).UTC() })
	if err != nil {
		t.Fatalf("NewPublicListingHandler() error = %v", err)
	}
	response := httptest.NewRecorder()
	handler.serveHumanDirectory(response, httptest.NewRequest(http.MethodHead, "/", nil))
	if response.Code != http.StatusOK || response.Body.Len() != 0 {
		t.Fatalf("HEAD response = status %d body %q", response.Code, response.Body.String())
	}
	if response.Header().Get("ETag") == "" || response.Header().Get("Cache-Control") != publicListingCacheControl ||
		response.Header().Get("Content-Security-Policy") != humanDirectoryCSP {
		t.Fatalf("HEAD headers = %#v", response.Header())
	}
}

func TestHumanDirectoryUsesSameAuthenticatedCursorAndProjectionAsV2(t *testing.T) {
	now := time.Unix(2_000, 0).UTC()
	repository := &publicListingRepositoryStub{directoryPage: storage.DirectoryProjectionPage{
		Relays: []storage.DirectoryProjectionRelay{},
		Next:   storage.DirectoryProjectionCursor{RelayActor: "https://relay.example/actor"},
	}}
	handler, err := NewPublicListingHandler(repository, func() time.Time { return now })
	if err != nil {
		t.Fatalf("NewPublicListingHandler() error = %v", err)
	}

	htmlResponse := httptest.NewRecorder()
	handler.serveHumanDirectory(htmlResponse, httptest.NewRequest(http.MethodGet, "/?limit=7", nil))
	if htmlResponse.Code != http.StatusOK {
		t.Fatalf("HTML status = %d body = %q", htmlResponse.Code, htmlResponse.Body.String())
	}
	next := extractHTMLAttribute(t, htmlResponse.Body.String(), `rel="next" href="`, `"`)
	nextURL, err := url.Parse(next)
	if err != nil {
		t.Fatalf("url.Parse(next) error = %v", err)
	}
	cursor := nextURL.Query().Get("cursor")
	if cursor == "" || nextURL.Query().Get("limit") != "7" {
		t.Fatalf("next URL = %q", next)
	}

	repository.mu.Lock()
	repository.directoryPage.Next = storage.DirectoryProjectionCursor{}
	repository.mu.Unlock()

	jsonResponse := httptest.NewRecorder()
	handler.serveDirectoryProjection(jsonResponse, httptest.NewRequest(
		http.MethodGet, directoryProjectionPath+"?limit=7&cursor="+url.QueryEscape(cursor), nil,
	))
	if jsonResponse.Code != http.StatusOK {
		t.Fatalf("JSON status = %d body = %q", jsonResponse.Code, jsonResponse.Body.String())
	}

	repository.mu.Lock()
	defer repository.mu.Unlock()
	if len(repository.directoryQueries) != 2 {
		t.Fatalf("queries = %#v", repository.directoryQueries)
	}
	second := repository.directoryQueries[1]
	if !second.ObservedAt.Equal(now) || second.Limit != 7 || second.After.RelayActor != "https://relay.example/actor" {
		t.Fatalf("second query = %#v", second)
	}
}

func TestHumanDirectoryRejectsTamperedCursorAndBackendFailureWithoutDisclosure(t *testing.T) {
	repository := &publicListingRepositoryStub{directoryPage: storage.DirectoryProjectionPage{
		Relays: []storage.DirectoryProjectionRelay{},
		Next:   storage.DirectoryProjectionCursor{RelayActor: "https://relay.example/actor"},
	}}
	handler, err := NewPublicListingHandler(repository, func() time.Time { return time.Unix(2_000, 0).UTC() })
	if err != nil {
		t.Fatalf("NewPublicListingHandler() error = %v", err)
	}
	first := httptest.NewRecorder()
	handler.serveHumanDirectory(first, httptest.NewRequest(http.MethodGet, "/", nil))
	next := extractHTMLAttribute(t, first.Body.String(), `rel="next" href="`, `"`)
	nextURL, err := url.Parse(next)
	if err != nil {
		t.Fatalf("url.Parse(next) error = %v", err)
	}
	cursor := nextURL.Query().Get("cursor")
	if cursor == "" {
		t.Fatal("missing cursor")
	}
	tampered := cursor[:len(cursor)-1] + "A"
	if tampered == cursor {
		tampered = cursor[:len(cursor)-1] + "B"
	}

	response := httptest.NewRecorder()
	handler.serveHumanDirectory(response, httptest.NewRequest(http.MethodGet, "/?cursor="+url.QueryEscape(tampered), nil))
	if response.Code != http.StatusBadRequest || response.Body.String() != "invalid directory request\n" {
		t.Fatalf("tampered response = status %d body %q", response.Code, response.Body.String())
	}

	repository.mu.Lock()
	repository.directoryErr = errorsForHumanTest{}
	repository.directoryPage.Next = storage.DirectoryProjectionCursor{}
	repository.mu.Unlock()

	response = httptest.NewRecorder()
	handler.serveHumanDirectory(response, httptest.NewRequest(http.MethodGet, "/", nil))
	if response.Code != http.StatusServiceUnavailable || strings.Contains(response.Body.String(), "sqlite") || strings.Contains(response.Body.String(), "/srv") {
		t.Fatalf("backend response = status %d body %q", response.Code, response.Body.String())
	}
}

func TestHumanDirectoryEmptyStateAndStylesheet(t *testing.T) {
	repository := &publicListingRepositoryStub{directoryPage: storage.DirectoryProjectionPage{Relays: []storage.DirectoryProjectionRelay{}}}
	handler, err := NewPublicListingHandler(repository, func() time.Time { return time.Unix(100, 0).UTC() })
	if err != nil {
		t.Fatalf("NewPublicListingHandler() error = %v", err)
	}
	response := httptest.NewRecorder()
	handler.serveHumanDirectory(response, httptest.NewRequest(http.MethodGet, "/", nil))
	if !strings.Contains(response.Body.String(), "No relays are listed yet") || !strings.Contains(response.Body.String(), "End of directory") {
		t.Fatalf("empty state body = %q", response.Body.String())
	}

	styleResponse := httptest.NewRecorder()
	serveDirectoryStylesheet(styleResponse, httptest.NewRequest(http.MethodGet, directoryStylesheetPath, nil))
	if styleResponse.Code != http.StatusOK || styleResponse.Header().Get("Content-Type") != "text/css; charset=utf-8" ||
		styleResponse.Header().Get("ETag") == "" || styleResponse.Header().Get("Cache-Control") != publicListingCacheControl {
		t.Fatalf("stylesheet response = status %d headers %#v", styleResponse.Code, styleResponse.Header())
	}
	if strings.Contains(styleResponse.Body.String(), "@import") || strings.Contains(styleResponse.Body.String(), "url(") {
		t.Fatalf("stylesheet contains remote-capable fetch: %q", styleResponse.Body.String())
	}
}

func TestHumanDirectoryRejectsWriteMethods(t *testing.T) {
	repository := &publicListingRepositoryStub{directoryPage: storage.DirectoryProjectionPage{Relays: []storage.DirectoryProjectionRelay{}}}
	handler, err := NewPublicListingHandler(repository, func() time.Time { return time.Unix(100, 0).UTC() })
	if err != nil {
		t.Fatalf("NewPublicListingHandler() error = %v", err)
	}
	response := httptest.NewRecorder()
	handler.serveHumanDirectory(response, httptest.NewRequest(http.MethodPost, "/", strings.NewReader("x")))
	if response.Code != http.StatusMethodNotAllowed || response.Header().Get("Allow") != "GET, HEAD" {
		t.Fatalf("POST response = status %d Allow %q", response.Code, response.Header().Get("Allow"))
	}
}

type errorsForHumanTest struct{}

func (errorsForHumanTest) Error() string { return "secret sqlite path /srv/private.sqlite" }

func extractHTMLAttribute(t *testing.T, body, prefix, suffix string) string {
	t.Helper()
	start := strings.Index(body, prefix)
	if start < 0 {
		t.Fatalf("missing HTML attribute prefix %q in %q", prefix, body)
	}
	start += len(prefix)
	end := strings.Index(body[start:], suffix)
	if end < 0 {
		t.Fatalf("missing HTML attribute suffix %q in %q", suffix, body)
	}
	return html.UnescapeString(body[start : start+end])
}
