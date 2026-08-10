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

	v1 "github.com/thystra/activity-relay-directory/internal/protocol/v1"
	"github.com/thystra/activity-relay-directory/internal/storage"
)

func TestHumanDirectoryFixtureEscapingCachingAndAccessibility(t *testing.T) {
	now := time.Unix(100_100, 0).UTC()
	repository := &publicListingRepositoryStub{page: storage.HealthProjectionPage{
		Relays: []storage.HealthProjectionRelay{{
			RelayActor:    "https://relay.example/a&b",
			PublicBaseURL: "https://relay.example",
			HealthState:   v1.HealthHealthy,
			LastSeenUnix:  100_000,
		}},
	}}
	handler, err := NewPublicListingHandler(repository, func() time.Time { return now })
	if err != nil {
		t.Fatalf("NewPublicListingHandler() error = %v", err)
	}

	request := httptest.NewRequest(http.MethodGet, "/", nil)
	response := httptest.NewRecorder()
	handler.serveHumanDirectory(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %q", response.Code, response.Body.String())
	}
	fixture, err := os.ReadFile("../../testdata/public/v1/relays-page.valid.html")
	if err != nil {
		t.Fatalf("ReadFile(fixture) error = %v", err)
	}
	if response.Body.String() != string(fixture) {
		t.Fatalf("body = %q, want fixture %q", response.Body.String(), fixture)
	}
	if response.Header().Get("Content-Type") != humanDirectoryContentType {
		t.Fatalf("Content-Type = %q", response.Header().Get("Content-Type"))
	}
	if response.Header().Get("Cache-Control") != publicListingCacheControl {
		t.Fatalf("Cache-Control = %q", response.Header().Get("Cache-Control"))
	}
	if response.Header().Get("Content-Security-Policy") != humanDirectoryCSP {
		t.Fatalf("Content-Security-Policy = %q", response.Header().Get("Content-Security-Policy"))
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
		`<section class="health-help" aria-labelledby="health-heading">`,
		`<dt>healthy</dt>`,
		`<dt>stale</dt>`,
		`<dt>dead</dt>`,
		`https://relay.example/a&amp;b`,
	} {
		if !strings.Contains(body, required) {
			t.Fatalf("body missing %q", required)
		}
	}
	if strings.Contains(body, "https://relay.example/a&b") || strings.Contains(body, "<script") ||
		strings.Contains(body, "analytics") || strings.Contains(body, "fonts.googleapis") {
		t.Fatalf("unsafe or remote content present: %q", body)
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
	repository := &publicListingRepositoryStub{page: storage.HealthProjectionPage{Relays: []storage.HealthProjectionRelay{}}}
	handler, err := NewPublicListingHandler(repository, func() time.Time { return time.Unix(100, 0).UTC() })
	if err != nil {
		t.Fatalf("NewPublicListingHandler() error = %v", err)
	}
	request := httptest.NewRequest(http.MethodHead, "/", nil)
	response := httptest.NewRecorder()
	handler.serveHumanDirectory(response, request)
	if response.Code != http.StatusOK || response.Body.Len() != 0 {
		t.Fatalf("HEAD response = status %d body %q", response.Code, response.Body.String())
	}
	if response.Header().Get("ETag") == "" ||
		response.Header().Get("Cache-Control") != publicListingCacheControl ||
		response.Header().Get("Content-Security-Policy") != humanDirectoryCSP {
		t.Fatalf("HEAD headers = %#v", response.Header())
	}
}

func TestHumanDirectoryUsesSameAuthenticatedCursorAndProjection(t *testing.T) {
	now := time.Unix(2_000, 0).UTC()
	repository := &publicListingRepositoryStub{page: storage.HealthProjectionPage{
		Relays: []storage.HealthProjectionRelay{},
		Next: storage.HealthProjectionCursor{
			LastSeenUnix: 1_000,
			RelayActor:   "https://relay.example/actor",
		},
	}}
	handler, err := NewPublicListingHandler(repository, func() time.Time { return now })
	if err != nil {
		t.Fatalf("NewPublicListingHandler() error = %v", err)
	}

	htmlRequest := httptest.NewRequest(http.MethodGet, "/?limit=7", nil)
	htmlResponse := httptest.NewRecorder()
	handler.serveHumanDirectory(htmlResponse, htmlRequest)
	if htmlResponse.Code != http.StatusOK {
		t.Fatalf("HTML status = %d body = %q", htmlResponse.Code, htmlResponse.Body.String())
	}
	next := extractHTMLAttribute(t, htmlResponse.Body.String(), `class="button-link" href="`, `"`)
	nextURL, err := url.Parse(next)
	if err != nil {
		t.Fatalf("url.Parse(next) error = %v", err)
	}
	cursor := nextURL.Query().Get("cursor")
	if cursor == "" || nextURL.Query().Get("limit") != "7" {
		t.Fatalf("next URL = %q", next)
	}

	repository.mu.Lock()
	repository.page.Next = storage.HealthProjectionCursor{}
	repository.mu.Unlock()

	jsonRequest := httptest.NewRequest(http.MethodGet, "/v1/relays?limit=7&cursor="+url.QueryEscape(cursor), nil)
	jsonResponse := httptest.NewRecorder()
	handler.serve(jsonResponse, jsonRequest)
	if jsonResponse.Code != http.StatusOK {
		t.Fatalf("JSON status = %d body = %q", jsonResponse.Code, jsonResponse.Body.String())
	}

	repository.mu.Lock()
	defer repository.mu.Unlock()
	if len(repository.queries) != 2 {
		t.Fatalf("queries = %#v", repository.queries)
	}
	second := repository.queries[1]
	if !second.ObservedAt.Equal(now) || second.Limit != 7 ||
		second.After.LastSeenUnix != 1_000 || second.After.RelayActor != "https://relay.example/actor" {
		t.Fatalf("second query = %#v", second)
	}
}

func TestHumanDirectoryRejectsTamperedCursorAndBackendFailureWithoutDisclosure(t *testing.T) {
	repository := &publicListingRepositoryStub{err: nil, page: storage.HealthProjectionPage{
		Relays: []storage.HealthProjectionRelay{},
		Next: storage.HealthProjectionCursor{
			LastSeenUnix: 1_000,
			RelayActor:   "https://relay.example/actor",
		},
	}}
	handler, err := NewPublicListingHandler(repository, func() time.Time { return time.Unix(2_000, 0).UTC() })
	if err != nil {
		t.Fatalf("NewPublicListingHandler() error = %v", err)
	}
	first := httptest.NewRecorder()
	handler.serveHumanDirectory(first, httptest.NewRequest(http.MethodGet, "/", nil))
	next := extractHTMLAttribute(t, first.Body.String(), `class="button-link" href="`, `"`)
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
	repository.err = errorsForHumanTest{}
	repository.page.Next = storage.HealthProjectionCursor{}
	repository.mu.Unlock()

	response = httptest.NewRecorder()
	handler.serveHumanDirectory(response, httptest.NewRequest(http.MethodGet, "/", nil))
	if response.Code != http.StatusServiceUnavailable ||
		strings.Contains(response.Body.String(), "sqlite") ||
		strings.Contains(response.Body.String(), "/srv") {
		t.Fatalf("backend response = status %d body %q", response.Code, response.Body.String())
	}
}

func TestHumanDirectoryEmptyStateAndStylesheet(t *testing.T) {
	repository := &publicListingRepositoryStub{page: storage.HealthProjectionPage{Relays: []storage.HealthProjectionRelay{}}}
	handler, err := NewPublicListingHandler(repository, func() time.Time { return time.Unix(100, 0).UTC() })
	if err != nil {
		t.Fatalf("NewPublicListingHandler() error = %v", err)
	}
	response := httptest.NewRecorder()
	handler.serveHumanDirectory(response, httptest.NewRequest(http.MethodGet, "/", nil))
	if !strings.Contains(response.Body.String(), "No relays are currently available") ||
		!strings.Contains(response.Body.String(), "End of directory") {
		t.Fatalf("empty state body = %q", response.Body.String())
	}

	styleResponse := httptest.NewRecorder()
	serveDirectoryStylesheet(styleResponse, httptest.NewRequest(http.MethodGet, directoryStylesheetPath, nil))
	if styleResponse.Code != http.StatusOK ||
		styleResponse.Header().Get("Content-Type") != "text/css; charset=utf-8" ||
		styleResponse.Header().Get("ETag") == "" ||
		styleResponse.Header().Get("Cache-Control") != publicListingCacheControl {
		t.Fatalf("stylesheet response = status %d headers %#v", styleResponse.Code, styleResponse.Header())
	}
	if strings.Contains(styleResponse.Body.String(), "@import") ||
		strings.Contains(styleResponse.Body.String(), "url(") {
		t.Fatalf("stylesheet contains remote-capable fetch: %q", styleResponse.Body.String())
	}
}

func TestHumanDirectoryRejectsWriteMethods(t *testing.T) {
	repository := &publicListingRepositoryStub{page: storage.HealthProjectionPage{Relays: []storage.HealthProjectionRelay{}}}
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

func (errorsForHumanTest) Error() string {
	return "secret sqlite path /srv/private.sqlite"
}

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
	value := body[start : start+end]
	return html.UnescapeString(value)
}
