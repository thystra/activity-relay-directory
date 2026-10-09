package httpapi

import (
	"html"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/thystra/activity-relay-directory/internal/actorresolver"
	"github.com/thystra/activity-relay-directory/internal/storage"
)

func TestHumanDirectoryFixtureEscapingCachingAndAccessibility(t *testing.T) {
	now := time.Unix(100_100, 0).UTC()
	lastSeen := int64(100_000)
	checked := int64(100_050)
	declared := int64(100_040)
	verified := int64(100_000)
	repository := &publicListingRepositoryStub{
		summary: storage.DirectorySummary{
			KnownRelays:         1,
			OnlineRelays:        1,
			OfflineRelays:       0,
			PendingVerification: 2,
		},
		directoryPage: storage.DirectoryProjectionPage{
			Relays: []storage.DirectoryProjectionRelay{{
				RelayActor:           "https://relay.example/a&b",
				PublicBaseURL:        "https://relay.example",
				LifecycleKnown:       true,
				Registered:           true,
				FirstKnownUnix:       lastSeen - 100,
				Tier:                 storage.DirectoryTierHeartbeatOnline,
				HeartbeatState:       storage.HeartbeatHealthy,
				LastSeenUnix:         &lastSeen,
				LastHeartbeatUnix:    &lastSeen,
				ActorState:           storage.ReachabilityReachable,
				ActorLastCheckedUnix: &checked,
				ActorLastSuccessUnix: &checked,
				InboxURL:             "https://relay.example/inbox/a&b",
				InboxDeclaredUnix:    &declared,
				InboxProbeState:      storage.InboxMethodRejected,
				InboxLastCheckedUnix: &checked,
				InboxDiagnostic:      actorresolver.ProbeDiagnostic{Stage: "inbox", Code: "http_status", HTTPStatus: 405},
				RFC9421VerifiedUnix:  &verified,
			}},
		},
	}
	handler, err := NewPublicListingHandler(repository, func() time.Time { return now })
	if err != nil {
		t.Fatalf("NewPublicListingHandler() error = %v", err)
	}

	response := httptest.NewRecorder()
	handler.serveHumanDirectory(response, httptest.NewRequest(http.MethodGet, "/", nil))
	if response.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %q", response.Code, response.Body.String())
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
		`<section class="status-help panel" aria-labelledby="status-heading">`,
		`class="relay-table"`,
		`class="relay-row"`,
		`>Healthy</span>`,
		`>Reachable</span>`,
		`Last heartbeat`,
		`>Registration</span>`,
		`>Sites</span>`,
		`aria-hidden="true">♥</span> Heartbeat`,
		`Last successful ActivityPub Actor check`,
		`ActivityPub Inbox does not support this check (HTTP 405).`,
		`Message delivery was not tested.`,
		`Checked <time datetime="1970-01-02T03:47:30Z">1970-01-02 03:47 UTC</time>.`,
		`>relay.example</a>`,
		`https://relay.example/a&amp;b`,
		`https://relay.example/inbox/a&amp;b`,
		`href="/downloads/active.txt"`,
		`href="/downloads/all.txt"`,
		`href="/downloads/unavailable.txt"`,
		`This Directory knows about 1 relay. 1 has a healthy heartbeat or a recent ActivityPub Actor check. 0 have neither.`,
		`An additional 2 relay candidates are pending verification.`,
		`Candidates are not listed publicly until their ActivityPub Actor can be verified.`,
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
		"Inbox diagnostic", "method rejected", "RFC 9421", "evidence recorded",
		"Last authenticated Directory observation", "Actor last checked",
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

func TestHumanDirectoryRendersEffectiveProfileAsEscapedTextAndHTTPSLinks(t *testing.T) {
	now := time.Unix(100_100, 0).UTC()
	relay := directoryProjectionRelayForTest(now, "https://profile.example/actor", storage.DirectoryTierOnline)
	relay.Profile = storage.RelayProfile{
		ParticipationMode: "open <community>",
		Availability:      "public",
		RelayType:         "regional",
		Languages:         []string{"en", "fr"},
		Countries:         []string{"US"},
		Regions:           []string{"North America"},
		Topics:            []string{"general"},
		ContactEmail:      "relay@example.com",
		ContactURL:        "https://profile.example/contact",
		ParticipationURL:  "https://profile.example/join",
		Notes:             "Friends & neighbors <welcome>",
	}
	repository := &publicListingRepositoryStub{
		summary: storage.DirectorySummary{
			KnownRelays:  1,
			OnlineRelays: 1,
		},
		directoryPage: storage.DirectoryProjectionPage{Relays: []storage.DirectoryProjectionRelay{relay}},
	}
	handler, err := NewPublicListingHandler(repository, func() time.Time { return now })
	if err != nil {
		t.Fatalf("NewPublicListingHandler() error = %v", err)
	}

	response := httptest.NewRecorder()
	handler.serveHumanDirectory(response, httptest.NewRequest(http.MethodGet, "/", nil))
	if response.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %q", response.Code, response.Body.String())
	}
	body := response.Body.String()
	for _, required := range []string{
		">Relay information</h4>",
		">Registration status</dt>",
		">About this relay</dt>",
		">Relay focus</h4>",
		"This relay is focused on the following languages, countries, and/or regions:",
		"open &lt;community&gt;",
		"regional",
		"general",
		"en, fr",
		"US",
		"North America",
		"relay@example.com",
		`href="https://profile.example/contact"`,
		`href="https://profile.example/join"`,
		"Friends &amp; neighbors &lt;welcome&gt;",
	} {
		if !strings.Contains(body, required) {
			t.Fatalf("body missing %q: %q", required, body)
		}
	}
	for _, forbidden := range []string{
		"<community>", "<welcome>", "source_kind", "source_label", "source_url", "operator_id",
	} {
		if strings.Contains(body, forbidden) {
			t.Fatalf("unsafe/private content %q present: %q", forbidden, body)
		}
	}
}

func TestHumanDirectorySuppressesLocationFocusIntroWhenNoLocationFocusIsDeclared(t *testing.T) {
	now := time.Unix(100_100, 0).UTC()
	relay := directoryProjectionRelayForTest(now, "https://profile.example/actor", storage.DirectoryTierOnline)
	relay.Profile = storage.RelayProfile{
		ParticipationMode: "open",
		RelayType:         "general",
		Topics:            []string{"general"},
	}
	repository := &publicListingRepositoryStub{
		summary:       storage.DirectorySummary{KnownRelays: 1, OnlineRelays: 1},
		directoryPage: storage.DirectoryProjectionPage{Relays: []storage.DirectoryProjectionRelay{relay}},
	}
	handler, err := NewPublicListingHandler(repository, func() time.Time { return now })
	if err != nil {
		t.Fatalf("NewPublicListingHandler() error = %v", err)
	}
	response := httptest.NewRecorder()
	handler.serveHumanDirectory(response, httptest.NewRequest(http.MethodGet, "/", nil))
	body := response.Body.String()
	if !strings.Contains(body, ">Relay focus</h4>") || !strings.Contains(body, ">Topics</dt>") {
		t.Fatalf("relay focus missing: %q", body)
	}
	if strings.Contains(body, "This relay is focused on the following languages, countries, and/or regions:") ||
		strings.Contains(body, ">Languages</dt>") || strings.Contains(body, ">Countries</dt>") || strings.Contains(body, ">Regions</dt>") {
		t.Fatalf("empty location focus unexpectedly rendered: %q", body)
	}
}

func TestHumanDirectoryPlainLanguageHelpers(t *testing.T) {
	if got := humanRelayLabel("https://relay.example/path/"); got != "relay.example/path" {
		t.Fatalf("humanRelayLabel() = %q", got)
	}
	stamp := "2026-09-15T23:12:28Z"
	if got := humanDirectoryTime(&stamp); got != "2026-09-15 23:12 UTC" {
		t.Fatalf("humanDirectoryTime() = %q", got)
	}
	for state, want := range map[storage.PublicHeartbeatState]string{
		storage.HeartbeatHealthy:     "Healthy",
		storage.HeartbeatStale:       "Stale",
		storage.HeartbeatDead:        "Stale",
		storage.HeartbeatPrune:       "Stale",
		storage.HeartbeatNotObserved: "No heartbeat received",
	} {
		if got := humanHeartbeatLabel(state); got != want {
			t.Fatalf("humanHeartbeatLabel(%q) = %q, want %q", state, got, want)
		}
	}
	for state, want := range map[storage.ReachabilityState]string{
		storage.ReachabilityReachable:   "Reachable",
		storage.ReachabilityUnreachable: "Check failed",
		storage.ReachabilityUnknown:     "Not checked yet",
	} {
		if got := humanReachabilityLabel(state); got != want {
			t.Fatalf("humanReachabilityLabel(%q) = %q, want %q", state, got, want)
		}
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
		Relays: []storage.DirectoryProjectionRelay{
			directoryProjectionRelayForTest(now, "https://relay.example/actor", storage.DirectoryTierOnline),
		},
		Next: storage.DirectoryProjectionCursor{Tier: storage.DirectoryTierOnline, RelayActor: "https://relay.example/actor"},
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
	if cursor == "" || nextURL.Query().Get("limit") != "7" || nextURL.Fragment != "relay-list" {
		t.Fatalf("next URL = %q", next)
	}
	if !strings.Contains(htmlResponse.Body.String(), `class="button-link pagination-next"`) {
		t.Fatalf("next-page control is not right-alignment scoped: %q", htmlResponse.Body.String())
	}

	repository.mu.Lock()
	repository.directoryPage = storage.DirectoryProjectionPage{Relays: []storage.DirectoryProjectionRelay{
		directoryProjectionRelayForTest(now, "https://z.example/actor", storage.DirectoryTierOnline),
	}}
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
	if !second.ObservedAt.Equal(now) || second.Limit != 7 || second.After.Tier != storage.DirectoryTierOnline || second.After.RelayActor != "https://relay.example/actor" {
		t.Fatalf("second query = %#v", second)
	}
}

func TestHumanDirectoryProvidesPreviousPageWithoutChangingV2QuerySurface(t *testing.T) {
	now := time.Unix(2_000, 0).UTC()
	repository := &publicListingRepositoryStub{directoryPage: storage.DirectoryProjectionPage{
		Relays: []storage.DirectoryProjectionRelay{
			directoryProjectionRelayForTest(now, "https://c.example/actor", storage.DirectoryTierOnline),
		},
		Previous: storage.DirectoryProjectionCursor{Tier: storage.DirectoryTierOnline, RelayActor: "https://c.example/actor"},
	}}
	handler, err := NewPublicListingHandler(repository, func() time.Time { return now })
	if err != nil {
		t.Fatalf("NewPublicListingHandler() error = %v", err)
	}
	current, err := handler.encodeDirectoryProjectionCursor(directoryProjectionCursor{
		Version: directoryProjectionCursorVersion, IssuedUnix: now.Unix(), Tier: storage.DirectoryTierOnline, RelayActor: "https://b.example/actor",
	})
	if err != nil {
		t.Fatalf("encode current cursor: %v", err)
	}

	response := httptest.NewRecorder()
	handler.serveHumanDirectory(response, httptest.NewRequest(
		http.MethodGet, "/?limit=7&cursor="+url.QueryEscape(current), nil,
	))
	if response.Code != http.StatusOK {
		t.Fatalf("forward HTML status = %d body = %q", response.Code, response.Body.String())
	}
	previous := extractHTMLAttribute(t, response.Body.String(), `rel="prev" href="`, `"`)
	previousURL, err := url.Parse(previous)
	if err != nil {
		t.Fatalf("url.Parse(previous) error = %v", err)
	}
	before := previousURL.Query().Get("before")
	if before == "" || previousURL.Query().Get("limit") != "7" || previousURL.Query().Get("cursor") != "" ||
		previousURL.Fragment != "relay-list" {
		t.Fatalf("previous URL = %q", previous)
	}
	decodedBefore, err := handler.decodeDirectoryProjectionCursor(before)
	if err != nil ||
		decodedBefore.IssuedUnix != now.Unix() ||
		decodedBefore.Tier != storage.DirectoryTierOnline || decodedBefore.RelayActor != "https://c.example/actor" {
		t.Fatalf("previous cursor = %#v error=%v", decodedBefore, err)
	}
	originalIssuedUnix := decodedBefore.IssuedUnix
	if !strings.Contains(response.Body.String(), `class="button-link pagination-previous"`) {
		t.Fatalf("previous-page control is not left-alignment scoped: %q", response.Body.String())
	}

	now = now.Add(30 * time.Second)
	repository.mu.Lock()
	repository.directoryPage = storage.DirectoryProjectionPage{
		Relays: []storage.DirectoryProjectionRelay{
			directoryProjectionRelayForTest(now, "https://b.example/actor", storage.DirectoryTierOnline),
		},
		Next: storage.DirectoryProjectionCursor{Tier: storage.DirectoryTierOnline, RelayActor: "https://b.example/actor"},
	}
	repository.mu.Unlock()

	backResponse := httptest.NewRecorder()
	handler.serveHumanDirectory(backResponse, httptest.NewRequest(
		http.MethodGet, "/?limit=7&before="+url.QueryEscape(before), nil,
	))
	if backResponse.Code != http.StatusOK {
		t.Fatalf("reverse HTML status = %d body = %q", backResponse.Code, backResponse.Body.String())
	}
	next := extractHTMLAttribute(t, backResponse.Body.String(), `rel="next" href="`, `"`)
	nextURL, err := url.Parse(next)
	if err != nil {
		t.Fatalf("url.Parse(reverse next) error = %v", err)
	}
	nextCursor := nextURL.Query().Get("cursor")
	if nextCursor == "" || nextURL.Query().Get("limit") != "7" || nextURL.Query().Get("before") != "" ||
		nextURL.Fragment != "relay-list" {
		t.Fatalf("reverse next URL = %q", next)
	}
	decodedNext, err := handler.decodeDirectoryProjectionCursor(nextCursor)
	if err != nil ||
		decodedNext.IssuedUnix != originalIssuedUnix ||
		decodedNext.Tier != storage.DirectoryTierOnline || decodedNext.RelayActor != "https://b.example/actor" {
		t.Fatalf("reverse next cursor = %#v error=%v", decodedNext, err)
	}

	repository.mu.Lock()
	if len(repository.directoryQueries) != 2 {
		repository.mu.Unlock()
		t.Fatalf("queries = %#v", repository.directoryQueries)
	}
	second := repository.directoryQueries[1]
	repository.mu.Unlock()
	if !second.ObservedAt.Equal(now) || second.Limit != 7 || second.After != (storage.DirectoryProjectionCursor{}) ||
		second.Before.Tier != storage.DirectoryTierOnline || second.Before.RelayActor != "https://c.example/actor" {
		t.Fatalf("reverse query = %#v", second)
	}

	v2Response := httptest.NewRecorder()
	handler.serveDirectoryProjection(v2Response, httptest.NewRequest(
		http.MethodGet, directoryProjectionPath+"?limit=7&before="+url.QueryEscape(before), nil,
	))
	if v2Response.Code != http.StatusBadRequest {
		t.Fatalf("v2 accepted human-only before cursor: status=%d body=%q", v2Response.Code, v2Response.Body.String())
	}

	now = time.Unix(originalIssuedUnix, 0).UTC().Add(publicListingCursorMaxAge + time.Second)
	expiredResponse := httptest.NewRecorder()
	handler.serveHumanDirectory(expiredResponse, httptest.NewRequest(
		http.MethodGet, "/?limit=7&before="+url.QueryEscape(before), nil,
	))
	if expiredResponse.Code != http.StatusBadRequest ||
		expiredResponse.Body.String() != "invalid directory request\n" {
		t.Fatalf("expired previous cursor = status %d body=%q", expiredResponse.Code, expiredResponse.Body.String())
	}
}

func TestHumanDirectoryRejectsTamperedCursorAndBackendFailureWithoutDisclosure(t *testing.T) {
	now := time.Unix(2_000, 0).UTC()
	repository := &publicListingRepositoryStub{directoryPage: storage.DirectoryProjectionPage{
		Relays: []storage.DirectoryProjectionRelay{
			directoryProjectionRelayForTest(now, "https://relay.example/actor", storage.DirectoryTierOnline),
		},
		Next: storage.DirectoryProjectionCursor{Tier: storage.DirectoryTierOnline, RelayActor: "https://relay.example/actor"},
	}}
	handler, err := NewPublicListingHandler(repository, func() time.Time { return now })
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

	repository.mu.Lock()
	repository.directoryErr = nil
	repository.summaryErr = errorsForHumanTest{}
	repository.mu.Unlock()

	response = httptest.NewRecorder()
	handler.serveHumanDirectory(response, httptest.NewRequest(http.MethodGet, "/", nil))
	if response.Code != http.StatusServiceUnavailable || strings.Contains(response.Body.String(), "sqlite") || strings.Contains(response.Body.String(), "/srv") {
		t.Fatalf("summary backend response = status %d body %q", response.Code, response.Body.String())
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

func TestBuildHumanDirectoryTierBlocksPreservesTierAndAlphabeticalOrder(t *testing.T) {
	relays := []directoryProjectionRelay{
		{RelayActor: "https://a.example/actor", Tier: storage.DirectoryTierHeartbeatOnline},
		{RelayActor: "https://b.example/actor", Tier: storage.DirectoryTierHeartbeatOnline},
		{RelayActor: "https://c.example/actor", Tier: storage.DirectoryTierOnline},
		{RelayActor: "https://d.example/actor", Tier: storage.DirectoryTierUnavailable},
		{RelayActor: "https://e.example/actor", Tier: storage.DirectoryTierGraveyard},
	}
	blocks := buildHumanDirectoryTierBlocks(relays)
	if len(blocks) != 4 {
		t.Fatalf("blocks = %#v", blocks)
	}
	for index, block := range blocks {
		wantTier := storage.DirectoryTier(index + 1)
		if block.Tier != wantTier || block.Title == "" || block.Description == "" {
			t.Fatalf("block[%d] = %#v", index, block)
		}
	}
	if len(blocks[0].Relays) != 2 ||
		blocks[0].Relays[0].RelayActor != "https://a.example/actor" ||
		blocks[0].Relays[1].RelayActor != "https://b.example/actor" {
		t.Fatalf("tier 1 relays = %#v", blocks[0].Relays)
	}
}

func TestHumanDirectoryRegistrationFilterAndWithinTierOrder(t *testing.T) {
	relays := []directoryProjectionRelay{
		{RelayActor: "https://z.example/actor", Tier: storage.DirectoryTierHeartbeatOnline, Profile: directoryProjectionProfile{ParticipationMode: "closed"}},
		{RelayActor: "https://b.example/actor", Tier: storage.DirectoryTierHeartbeatOnline, Profile: directoryProjectionProfile{ParticipationMode: "open"}},
		{RelayActor: "https://a.example/actor", Tier: storage.DirectoryTierHeartbeatOnline, Profile: directoryProjectionProfile{ParticipationMode: "open"}},
		{RelayActor: "https://r.example/actor", Tier: storage.DirectoryTierHeartbeatOnline, Profile: directoryProjectionProfile{ParticipationMode: "restricted"}},
		{RelayActor: "https://u.example/actor", Tier: storage.DirectoryTierHeartbeatOnline},
	}
	blocks := buildHumanDirectoryTierBlocks(relays)
	if len(blocks) != 1 || len(blocks[0].Relays) != 5 {
		t.Fatalf("blocks = %#v", blocks)
	}
	want := []string{"https://a.example/actor", "https://b.example/actor", "https://r.example/actor", "https://z.example/actor", "https://u.example/actor"}
	for i, actor := range want {
		if blocks[0].Relays[i].RelayActor != actor {
			t.Fatalf("order[%d] = %s, want %s", i, blocks[0].Relays[i].RelayActor, actor)
		}
	}
	filtered := filterHumanDirectoryRelays(relays, "open")
	if len(filtered) != 2 {
		t.Fatalf("open filter = %#v", filtered)
	}
}

func TestHumanDirectoryPresentsReceivingSiteTelemetryOnCompactRow(t *testing.T) {
	count := 12
	reported := int64(100050)
	relay := presentDirectoryProjectionRelay(storage.DirectoryProjectionRelay{
		RelayActor: "https://relay.example/actor", PublicBaseURL: "https://relay.example",
		Tier: storage.DirectoryTierHeartbeatOnline, Profile: storage.RelayProfile{ParticipationMode: "open"},
		ReceivingInstanceCount: &count, TelemetryReportedUnix: &reported,
	})
	if relay.Telemetry.ReceivingInstanceCount == nil || *relay.Telemetry.ReceivingInstanceCount != 12 || relay.Telemetry.ReportedAt == nil {
		t.Fatalf("telemetry = %#v", relay.Telemetry)
	}
}

func TestHumanDirectoryHeartbeatAliveWithFailedActorProbe(t *testing.T) {
	now := time.Unix(10_000_000, 0).UTC()
	actor := "https://heartbeat-alive.example/actor"
	relay := directoryProjectionRelayForTest(now, actor, storage.DirectoryTierOnline)
	relay.Discovered = false
	relay.LifecycleKnown = true
	relay.Registered = true
	relay.FirstKnownUnix = now.Add(-2 * time.Hour).Unix()
	lastHeartbeat := now.Add(-time.Hour).Unix()
	relay.LastHeartbeatUnix = &lastHeartbeat
	relay.LastSeenUnix = &lastHeartbeat
	relay.HeartbeatState = storage.HeartbeatHealthy
	relay.ActorState = storage.ReachabilityUnreachable
	relay.ActorLastSuccessUnix = nil
	repo := &publicListingRepositoryStub{
		summary:       storage.DirectorySummary{KnownRelays: 1, OnlineRelays: 1},
		directoryPage: storage.DirectoryProjectionPage{Relays: []storage.DirectoryProjectionRelay{relay}},
	}
	handler, err := NewPublicListingHandler(repo, func() time.Time { return now })
	if err != nil {
		t.Fatal(err)
	}
	response := httptest.NewRecorder()
	handler.serveHumanDirectory(response, httptest.NewRequest(http.MethodGet, "/", nil))
	if response.Code != http.StatusOK {
		t.Fatalf("unexpected status=%d body=%q", response.Code, response.Body.String())
	}
	body := response.Body.String()
	for _, want := range []string{"Other Known Relays", "Check failed", "1 has a healthy heartbeat or a recent ActivityPub Actor check", "0 have neither"} {
		if !strings.Contains(body, want) {
			t.Fatalf("missing %q in output", want)
		}
	}
}
