package httpapi

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/thystra/activity-relay-directory/internal/storage"
)

func TestDirectoryProjectionFixtureAndCacheValidator(t *testing.T) {
	now := time.Unix(100_100, 0).UTC()
	lastSeen := int64(100_000)
	checked := int64(100_050)
	declared := int64(100_040)
	verified := int64(100_000)
	discoveredChecked := int64(100_090)
	repository := &publicListingRepositoryStub{directoryPage: storage.DirectoryProjectionPage{
		Relays: []storage.DirectoryProjectionRelay{
			{
				RelayActor:           "https://relay.example/actor",
				PublicBaseURL:        "https://relay.example",
				Registered:           true,
				HeartbeatState:       storage.HeartbeatHealthy,
				LastSeenUnix:         &lastSeen,
				ActorState:           storage.ReachabilityReachable,
				ActorLastCheckedUnix: &checked,
				ActorLastSuccessUnix: &checked,
				InboxURL:             "https://relay.example/inbox",
				InboxDeclaredUnix:    &declared,
				InboxProbeState:      storage.InboxMethodRejected,
				InboxLastCheckedUnix: &checked,
				RFC9421VerifiedUnix:  &verified,
			},
			{
				RelayActor:           "https://relay2.example/actor",
				PublicBaseURL:        "https://relay2.example",
				Discovered:           true,
				HeartbeatState:       storage.HeartbeatNotObserved,
				ActorState:           storage.ReachabilityReachable,
				ActorLastCheckedUnix: &discoveredChecked,
				ActorLastSuccessUnix: &discoveredChecked,
				InboxProbeState:      storage.InboxNotChecked,
			},
		},
	}}
	handler, err := NewPublicListingHandler(repository, func() time.Time { return now })
	if err != nil {
		t.Fatalf("NewPublicListingHandler() error = %v", err)
	}

	request := httptest.NewRequest(http.MethodGet, directoryProjectionPath, nil)
	response := httptest.NewRecorder()
	handler.serveDirectoryProjection(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %q", response.Code, response.Body.String())
	}
	fixture, err := os.ReadFile("../../testdata/public/v2/relays-page.valid.json")
	if err != nil {
		t.Fatalf("ReadFile(fixture) error = %v", err)
	}
	if response.Body.String() != string(fixture) {
		t.Fatalf("body = %q, want fixture %q", response.Body.String(), fixture)
	}
	if response.Header().Get("Cache-Control") != publicListingCacheControl {
		t.Fatalf("Cache-Control = %q", response.Header().Get("Cache-Control"))
	}
	etag := response.Header().Get("ETag")
	if etag == "" {
		t.Fatal("missing ETag")
	}
	for _, forbidden := range []string{
		"registered", "discovered", "operator", "reason_code", "source_kind", "source_label", "manual", "file",
	} {
		if strings.Contains(response.Body.String(), forbidden) {
			t.Fatalf("projection leaked private/internal field %q: %q", forbidden, response.Body.String())
		}
	}

	conditional := httptest.NewRequest(http.MethodGet, directoryProjectionPath, nil)
	conditional.Header.Set("If-None-Match", "W/"+etag)
	conditionalResponse := httptest.NewRecorder()
	handler.serveDirectoryProjection(conditionalResponse, conditional)
	if conditionalResponse.Code != http.StatusNotModified || conditionalResponse.Body.Len() != 0 {
		t.Fatalf("conditional response = status %d body %q", conditionalResponse.Code, conditionalResponse.Body.String())
	}
}

func TestDirectoryProjectionHeadSuppressesBodyAndPreservesValidators(t *testing.T) {
	repository := &publicListingRepositoryStub{directoryPage: storage.DirectoryProjectionPage{Relays: []storage.DirectoryProjectionRelay{}}}
	handler, err := NewPublicListingHandler(repository, func() time.Time { return time.Unix(100, 0).UTC() })
	if err != nil {
		t.Fatalf("NewPublicListingHandler() error = %v", err)
	}
	request := httptest.NewRequest(http.MethodHead, directoryProjectionPath, nil)
	response := httptest.NewRecorder()
	handler.serveDirectoryProjection(response, request)
	if response.Code != http.StatusOK || response.Body.Len() != 0 {
		t.Fatalf("HEAD response = status %d body %q", response.Code, response.Body.String())
	}
	if response.Header().Get("ETag") == "" || response.Header().Get("Cache-Control") != publicListingCacheControl {
		t.Fatalf("HEAD validators = %#v", response.Header())
	}
}

func TestDirectoryProjectionCursorKeepsActorPositionButUsesCurrentEvidenceTime(t *testing.T) {
	now := time.Unix(2_000, 0).UTC()
	repository := &publicListingRepositoryStub{directoryPage: storage.DirectoryProjectionPage{
		Relays: []storage.DirectoryProjectionRelay{},
		Next:   storage.DirectoryProjectionCursor{RelayActor: "https://relay.example/actor"},
	}}
	handler, err := NewPublicListingHandler(repository, func() time.Time { return now })
	if err != nil {
		t.Fatalf("NewPublicListingHandler() error = %v", err)
	}
	request := httptest.NewRequest(http.MethodGet, directoryProjectionPath+"?limit=7", nil)
	response := httptest.NewRecorder()
	handler.serveDirectoryProjection(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("first page status = %d body = %q", response.Code, response.Body.String())
	}
	var first directoryProjectionResponse
	if err := jsonUnmarshalStrict(response.Body.Bytes(), &first); err != nil {
		t.Fatalf("decode first page: %v", err)
	}
	cursor := first.Pagination.NextCursor
	if cursor == "" {
		t.Fatal("missing next cursor")
	}

	repository.mu.Lock()
	newEvidence := int64(2_025)
	repository.directoryPage = storage.DirectoryProjectionPage{Relays: []storage.DirectoryProjectionRelay{{
		RelayActor:           "https://z.example/actor",
		PublicBaseURL:        "https://z.example",
		Registered:           true,
		HeartbeatState:       storage.HeartbeatHealthy,
		LastSeenUnix:         &newEvidence,
		ActorState:           storage.ReachabilityReachable,
		ActorLastCheckedUnix: &newEvidence,
		ActorLastSuccessUnix: &newEvidence,
		InboxProbeState:      storage.InboxNotChecked,
	}}}
	repository.mu.Unlock()
	now = now.Add(30 * time.Second)
	request = httptest.NewRequest(http.MethodGet, directoryProjectionPath+"?limit=7&cursor="+url.QueryEscape(cursor), nil)
	response = httptest.NewRecorder()
	handler.serveDirectoryProjection(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("second page status = %d body = %q", response.Code, response.Body.String())
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
	decoded, err := handler.decodeDirectoryProjectionCursor(cursor)
	if err != nil || decoded.IssuedUnix != now.Add(-30*time.Second).Unix() {
		t.Fatalf("cursor = %#v error=%v, want original issue time", decoded, err)
	}
}

func TestDirectoryProjectionCursorRejectsTamperingExpiryForeignKeyAndV1Cursor(t *testing.T) {
	current := time.Unix(2_000, 0).UTC()
	repository := &publicListingRepositoryStub{directoryPage: storage.DirectoryProjectionPage{
		Relays: []storage.DirectoryProjectionRelay{},
		Next:   storage.DirectoryProjectionCursor{RelayActor: "https://relay.example/actor"},
	}}
	handler, err := NewPublicListingHandler(repository, func() time.Time { return current })
	if err != nil {
		t.Fatalf("NewPublicListingHandler() error = %v", err)
	}
	firstResponse := httptest.NewRecorder()
	handler.serveDirectoryProjection(firstResponse, httptest.NewRequest(http.MethodGet, directoryProjectionPath, nil))
	var first directoryProjectionResponse
	if err := jsonUnmarshalStrict(firstResponse.Body.Bytes(), &first); err != nil {
		t.Fatalf("decode first page: %v", err)
	}
	cursor := first.Pagination.NextCursor
	parts := strings.Split(cursor, ".")
	if len(parts) != 2 {
		t.Fatalf("cursor parts = %d", len(parts))
	}
	raw, err := base64.RawURLEncoding.DecodeString(parts[0])
	if err != nil {
		t.Fatalf("decode cursor payload: %v", err)
	}
	var decoded directoryProjectionCursor
	if err := jsonUnmarshalStrict(raw, &decoded); err != nil {
		t.Fatalf("decode cursor JSON: %v", err)
	}
	decoded.IssuedUnix = 1_500
	tamperedPayload, err := json.Marshal(decoded)
	if err != nil {
		t.Fatalf("marshal tampered cursor: %v", err)
	}
	tampered := base64.RawURLEncoding.EncodeToString(tamperedPayload) + "." + parts[1]

	other, err := NewPublicListingHandler(repository, func() time.Time { return current })
	if err != nil {
		t.Fatalf("second NewPublicListingHandler() error = %v", err)
	}
	foreign, err := other.encodeDirectoryProjectionCursor(directoryProjectionCursor{
		Version: directoryProjectionCursorVersion, IssuedUnix: 2_000, RelayActor: "https://relay.example/actor",
	})
	if err != nil {
		t.Fatalf("foreign encodeDirectoryProjectionCursor() error = %v", err)
	}
	v1Cursor, err := handler.encodePublicListingCursor(publicListingCursor{
		Version: publicListingCursorVersion, ObservedUnix: 2_000, LastSeenUnix: 1_000, RelayActor: "https://relay.example/actor",
	})
	if err != nil {
		t.Fatalf("encodePublicListingCursor() error = %v", err)
	}

	for name, candidate := range map[string]string{
		"tampered":  tampered,
		"foreign":   foreign,
		"v1-cursor": v1Cursor,
	} {
		t.Run(name, func(t *testing.T) {
			response := httptest.NewRecorder()
			handler.serveDirectoryProjection(response, httptest.NewRequest(http.MethodGet, directoryProjectionPath+"?cursor="+url.QueryEscape(candidate), nil))
			if response.Code != http.StatusBadRequest {
				t.Fatalf("status = %d body = %q", response.Code, response.Body.String())
			}
		})
	}

	current = current.Add(publicListingCursorMaxAge + time.Second)
	expired := httptest.NewRecorder()
	handler.serveDirectoryProjection(expired, httptest.NewRequest(http.MethodGet, directoryProjectionPath+"?cursor="+url.QueryEscape(cursor), nil))
	if expired.Code != http.StatusBadRequest {
		t.Fatalf("expired status = %d body = %q", expired.Code, expired.Body.String())
	}
}

func TestV2CursorIsRejectedByV1Listing(t *testing.T) {
	repository := &publicListingRepositoryStub{directoryPage: storage.DirectoryProjectionPage{
		Relays: []storage.DirectoryProjectionRelay{},
		Next:   storage.DirectoryProjectionCursor{RelayActor: "https://relay.example/actor"},
	}}
	handler, err := NewPublicListingHandler(repository, func() time.Time { return time.Unix(2_000, 0).UTC() })
	if err != nil {
		t.Fatalf("NewPublicListingHandler() error = %v", err)
	}
	response := httptest.NewRecorder()
	handler.serveDirectoryProjection(response, httptest.NewRequest(http.MethodGet, directoryProjectionPath, nil))
	var first directoryProjectionResponse
	if err := jsonUnmarshalStrict(response.Body.Bytes(), &first); err != nil {
		t.Fatalf("decode v2 response: %v", err)
	}
	v1Response := httptest.NewRecorder()
	handler.serve(v1Response, httptest.NewRequest(http.MethodGet, "/v1/relays?cursor="+url.QueryEscape(first.Pagination.NextCursor), nil))
	if v1Response.Code != http.StatusBadRequest {
		t.Fatalf("v1 accepted v2 cursor: status=%d body=%q", v1Response.Code, v1Response.Body.String())
	}
}

func TestDirectoryProjectionRejectsInvalidQueryWithFixedRedactedError(t *testing.T) {
	repository := &publicListingRepositoryStub{directoryPage: storage.DirectoryProjectionPage{Relays: []storage.DirectoryProjectionRelay{}}}
	handler, err := NewPublicListingHandler(repository, func() time.Time { return time.Unix(2_000, 0).UTC() })
	if err != nil {
		t.Fatalf("NewPublicListingHandler() error = %v", err)
	}
	for _, target := range []string{
		directoryProjectionPath + "?limit=0",
		directoryProjectionPath + "?limit=101",
		directoryProjectionPath + "?limit=01",
		directoryProjectionPath + "?limit=1&limit=2",
		directoryProjectionPath + "?cursor=not-base64!",
		directoryProjectionPath + "?before=not-base64!",
		directoryProjectionPath + "?unknown=1",
	} {
		response := httptest.NewRecorder()
		handler.serveDirectoryProjection(response, httptest.NewRequest(http.MethodGet, target, nil))
		if response.Code != http.StatusBadRequest {
			t.Fatalf("%s status = %d", target, response.Code)
		}
		want := "{\"schema_version\":2,\"error\":{\"code\":\"invalid_request\",\"message\":\"invalid directory projection request\"}}\n"
		if response.Body.String() != want {
			t.Fatalf("%s body = %q", target, response.Body.String())
		}
	}
}

func TestDirectoryProjectionRejectsWriteMethods(t *testing.T) {
	repository := &publicListingRepositoryStub{directoryPage: storage.DirectoryProjectionPage{Relays: []storage.DirectoryProjectionRelay{}}}
	handler, err := NewPublicListingHandler(repository, func() time.Time { return time.Unix(2_000, 0).UTC() })
	if err != nil {
		t.Fatalf("NewPublicListingHandler() error = %v", err)
	}
	response := httptest.NewRecorder()
	handler.serveDirectoryProjection(response, httptest.NewRequest(http.MethodPost, directoryProjectionPath, strings.NewReader("{}")))
	if response.Code != http.StatusMethodNotAllowed || response.Header().Get("Allow") != "GET, HEAD" {
		t.Fatalf("response = status %d Allow %q", response.Code, response.Header().Get("Allow"))
	}
}

func TestDirectoryProjectionRepositoryFailureAndInvalidRowsAreRedacted(t *testing.T) {
	now := time.Unix(2_000, 0).UTC()
	for name, repository := range map[string]*publicListingRepositoryStub{
		"backend": {directoryErr: errors.New("secret sqlite path /srv/private.sqlite")},
		"invalid row": {directoryPage: storage.DirectoryProjectionPage{Relays: []storage.DirectoryProjectionRelay{{
			RelayActor: "not-an-actor",
		}}}},
	} {
		t.Run(name, func(t *testing.T) {
			handler, err := NewPublicListingHandler(repository, func() time.Time { return now })
			if err != nil {
				t.Fatalf("NewPublicListingHandler() error = %v", err)
			}
			response := httptest.NewRecorder()
			handler.serveDirectoryProjection(response, httptest.NewRequest(http.MethodGet, directoryProjectionPath, nil))
			if response.Code != http.StatusServiceUnavailable {
				t.Fatalf("status = %d body=%q", response.Code, response.Body.String())
			}
			if strings.Contains(response.Body.String(), "sqlite") || strings.Contains(response.Body.String(), "/srv") || strings.Contains(response.Body.String(), "not-an-actor") {
				t.Fatalf("body disclosed backend/row detail: %q", response.Body.String())
			}
		})
	}
}

func TestDirectoryProjectionRejectsRepositoryPaginationDrift(t *testing.T) {
	now := time.Unix(2_000, 0).UTC()
	valid := func(actor string) storage.DirectoryProjectionRelay {
		seen := now.Unix() - 1
		return storage.DirectoryProjectionRelay{
			RelayActor:      actor,
			PublicBaseURL:   strings.TrimSuffix(actor, "/actor"),
			Registered:      true,
			HeartbeatState:  storage.HeartbeatHealthy,
			LastSeenUnix:    &seen,
			ActorState:      storage.ReachabilityUnknown,
			InboxProbeState: storage.InboxNotChecked,
		}
	}

	cases := map[string]storage.DirectoryProjectionPage{
		"too many": {Relays: []storage.DirectoryProjectionRelay{
			valid("https://a.example/actor"), valid("https://b.example/actor"),
		}},
		"duplicate": {Relays: []storage.DirectoryProjectionRelay{
			valid("https://a.example/actor"), valid("https://a.example/actor"),
		}},
		"unordered": {Relays: []storage.DirectoryProjectionRelay{
			valid("https://b.example/actor"), valid("https://a.example/actor"),
		}},
		"rewinding next": {
			Relays: []storage.DirectoryProjectionRelay{valid("https://b.example/actor")},
			Next:   storage.DirectoryProjectionCursor{RelayActor: "https://a.example/actor"},
		},
		"noncanonical next": {
			Relays: []storage.DirectoryProjectionRelay{valid("https://a.example/actor")},
			Next:   storage.DirectoryProjectionCursor{RelayActor: "HTTPS://b.example/actor"},
		},
	}

	for name, page := range cases {
		t.Run(name, func(t *testing.T) {
			repository := &publicListingRepositoryStub{directoryPage: page}
			handler, err := NewPublicListingHandler(repository, func() time.Time { return now })
			if err != nil {
				t.Fatalf("NewPublicListingHandler() error = %v", err)
			}
			target := directoryProjectionPath + "?limit=2"
			if name == "too many" {
				target = directoryProjectionPath + "?limit=1"
			}
			response := httptest.NewRecorder()
			handler.serveDirectoryProjection(response, httptest.NewRequest(http.MethodGet, target, nil))
			if response.Code != http.StatusServiceUnavailable {
				t.Fatalf("status = %d body=%q", response.Code, response.Body.String())
			}
		})
	}
}

func TestPublicListingAdmissionBudgetIsSharedWithDirectoryProjection(t *testing.T) {
	block := make(chan struct{})
	repository := &publicListingRepositoryStub{block: block}
	handler, err := newPublicListingHandler(repository, func() time.Time { return time.Unix(2_000, 0).UTC() }, 1)
	if err != nil {
		t.Fatalf("newPublicListingHandler() error = %v", err)
	}

	started := make(chan struct{})
	go func() {
		close(started)
		response := httptest.NewRecorder()
		handler.serve(response, httptest.NewRequest(http.MethodGet, "/v1/relays", nil))
	}()
	<-started
	deadline := time.Now().Add(time.Second)
	for {
		repository.mu.Lock()
		calls := len(repository.queries)
		repository.mu.Unlock()
		if calls == 1 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("v1 request did not enter repository")
		}
		time.Sleep(time.Millisecond)
	}

	response := httptest.NewRecorder()
	handler.serveDirectoryProjection(response, httptest.NewRequest(http.MethodGet, directoryProjectionPath, nil))
	if response.Code != http.StatusTooManyRequests || response.Header().Get("Retry-After") != "1" {
		t.Fatalf("bounded v2 response = status %d headers %#v body %q", response.Code, response.Header(), response.Body.String())
	}
	close(block)
}
