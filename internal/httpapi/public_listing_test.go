package httpapi

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	v1 "github.com/thystra/activity-relay-directory/internal/protocol/v1"
	"github.com/thystra/activity-relay-directory/internal/storage"
)

type publicListingRepositoryStub struct {
	mu      sync.Mutex
	queries []storage.HealthProjectionQuery
	page    storage.HealthProjectionPage
	err     error
	block   <-chan struct{}
}

func (repository *publicListingRepositoryStub) ListPublicRelays(
	ctx context.Context,
	query storage.HealthProjectionQuery,
) (storage.HealthProjectionPage, error) {
	if repository == nil {
		return storage.HealthProjectionPage{}, storage.ErrRepositoryConfiguration
	}
	repository.mu.Lock()
	repository.queries = append(repository.queries, query)
	page, err, block := repository.page, repository.err, repository.block
	repository.mu.Unlock()
	if block != nil {
		select {
		case <-ctx.Done():
			return storage.HealthProjectionPage{}, ctx.Err()
		case <-block:
		}
	}
	return page, err
}

func TestPublicListingFixtureAndCacheValidator(t *testing.T) {
	now := time.Unix(100_100, 0).UTC()
	repository := &publicListingRepositoryStub{page: storage.HealthProjectionPage{
		Relays: []storage.HealthProjectionRelay{{
			RelayActor:    "https://relay.example/actor",
			PublicBaseURL: "https://relay.example",
			HealthState:   v1.HealthHealthy,
			LastSeenUnix:  100_000,
		}},
	}}
	handler, err := NewPublicListingHandler(repository, func() time.Time { return now })
	if err != nil {
		t.Fatalf("NewPublicListingHandler() error = %v", err)
	}

	request := httptest.NewRequest(http.MethodGet, "/v1/relays", nil)
	response := httptest.NewRecorder()
	handler.serve(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %q", response.Code, response.Body.String())
	}
	fixture, err := os.ReadFile("../../testdata/public/v1/relays-page.valid.json")
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

	conditional := httptest.NewRequest(http.MethodGet, "/v1/relays", nil)
	conditional.Header.Set("If-None-Match", "W/"+etag)
	conditionalResponse := httptest.NewRecorder()
	handler.serve(conditionalResponse, conditional)
	if conditionalResponse.Code != http.StatusNotModified || conditionalResponse.Body.Len() != 0 {
		t.Fatalf("conditional response = status %d body %q", conditionalResponse.Code, conditionalResponse.Body.String())
	}
}

func TestPublicListingHeadSuppressesBodyAndPreservesValidators(t *testing.T) {
	repository := &publicListingRepositoryStub{page: storage.HealthProjectionPage{Relays: []storage.HealthProjectionRelay{}}}
	handler, err := NewPublicListingHandler(repository, func() time.Time { return time.Unix(100, 0).UTC() })
	if err != nil {
		t.Fatalf("NewPublicListingHandler() error = %v", err)
	}
	request := httptest.NewRequest(http.MethodHead, "/v1/relays", nil)
	response := httptest.NewRecorder()
	handler.serve(response, request)
	if response.Code != http.StatusOK || response.Body.Len() != 0 {
		t.Fatalf("HEAD response = status %d body %q", response.Code, response.Body.String())
	}
	if response.Header().Get("ETag") == "" || response.Header().Get("Cache-Control") != publicListingCacheControl {
		t.Fatalf("HEAD validators = %#v", response.Header())
	}
}

func TestPublicListingCursorPinsObservationTimeAndPosition(t *testing.T) {
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
	request := httptest.NewRequest(http.MethodGet, "/v1/relays?limit=7", nil)
	response := httptest.NewRecorder()
	handler.serve(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("first page status = %d, body = %q", response.Code, response.Body.String())
	}
	var first publicListingResponse
	if err := jsonUnmarshalStrict(response.Body.Bytes(), &first); err != nil {
		t.Fatalf("decode first page: %v", err)
	}
	cursor := first.Pagination.NextCursor
	if cursor == "" {
		t.Fatal("missing next cursor")
	}

	repository.mu.Lock()
	repository.page.Next = storage.HealthProjectionCursor{}
	repository.mu.Unlock()
	request = httptest.NewRequest(http.MethodGet, "/v1/relays?limit=7&cursor="+cursor, nil)
	response = httptest.NewRecorder()
	handler.serve(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("second page status = %d, body = %q", response.Code, response.Body.String())
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

func TestPublicListingCursorRejectsTamperingExpiryAndForeignKey(t *testing.T) {
	current := time.Unix(2_000, 0).UTC()
	repository := &publicListingRepositoryStub{page: storage.HealthProjectionPage{
		Relays: []storage.HealthProjectionRelay{},
		Next: storage.HealthProjectionCursor{
			LastSeenUnix: 1_000,
			RelayActor:   "https://relay.example/actor",
		},
	}}
	handler, err := NewPublicListingHandler(repository, func() time.Time { return current })
	if err != nil {
		t.Fatalf("NewPublicListingHandler() error = %v", err)
	}
	request := httptest.NewRequest(http.MethodGet, "/v1/relays", nil)
	response := httptest.NewRecorder()
	handler.serve(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("first page status = %d, body = %q", response.Code, response.Body.String())
	}
	var first publicListingResponse
	if err := jsonUnmarshalStrict(response.Body.Bytes(), &first); err != nil {
		t.Fatalf("decode first page: %v", err)
	}
	cursor := first.Pagination.NextCursor
	if cursor == "" {
		t.Fatal("missing next cursor")
	}

	parts := strings.Split(cursor, ".")
	if len(parts) != 2 {
		t.Fatalf("cursor parts = %d", len(parts))
	}
	payload, err := base64.RawURLEncoding.DecodeString(parts[0])
	if err != nil {
		t.Fatalf("decode cursor payload: %v", err)
	}
	var decoded publicListingCursor
	if err := jsonUnmarshalStrict(payload, &decoded); err != nil {
		t.Fatalf("decode cursor JSON: %v", err)
	}
	decoded.ObservedUnix = 1_500
	tamperedPayload, err := json.Marshal(decoded)
	if err != nil {
		t.Fatalf("marshal tampered cursor: %v", err)
	}
	tampered := base64.RawURLEncoding.EncodeToString(tamperedPayload) + "." + parts[1]

	for name, candidate := range map[string]string{
		"tampered": tampered,
		"foreign": func() string {
			other, err := NewPublicListingHandler(repository, func() time.Time { return current })
			if err != nil {
				t.Fatalf("second NewPublicListingHandler() error = %v", err)
			}
			foreign, err := other.encodePublicListingCursor(decoded)
			if err != nil {
				t.Fatalf("foreign encodePublicListingCursor() error = %v", err)
			}
			return foreign
		}(),
	} {
		t.Run(name, func(t *testing.T) {
			request := httptest.NewRequest(http.MethodGet, "/v1/relays?cursor="+url.QueryEscape(candidate), nil)
			response := httptest.NewRecorder()
			handler.serve(response, request)
			if response.Code != http.StatusBadRequest {
				t.Fatalf("status = %d, body = %q", response.Code, response.Body.String())
			}
		})
	}

	current = current.Add(publicListingCursorMaxAge + time.Second)
	expiredRequest := httptest.NewRequest(http.MethodGet, "/v1/relays?cursor="+url.QueryEscape(cursor), nil)
	expiredResponse := httptest.NewRecorder()
	handler.serve(expiredResponse, expiredRequest)
	if expiredResponse.Code != http.StatusBadRequest {
		t.Fatalf("expired status = %d, body = %q", expiredResponse.Code, expiredResponse.Body.String())
	}
}

func TestPublicListingRejectsInvalidQueryWithFixedRedactedError(t *testing.T) {
	repository := &publicListingRepositoryStub{page: storage.HealthProjectionPage{Relays: []storage.HealthProjectionRelay{}}}
	handler, err := NewPublicListingHandler(repository, func() time.Time { return time.Unix(2_000, 0).UTC() })
	if err != nil {
		t.Fatalf("NewPublicListingHandler() error = %v", err)
	}
	for _, target := range []string{
		"/v1/relays?limit=0",
		"/v1/relays?limit=101",
		"/v1/relays?limit=01",
		"/v1/relays?limit=1&limit=2",
		"/v1/relays?cursor=not-base64!",
		"/v1/relays?unknown=1",
	} {
		request := httptest.NewRequest(http.MethodGet, target, nil)
		response := httptest.NewRecorder()
		handler.serve(response, request)
		if response.Code != http.StatusBadRequest {
			t.Fatalf("%s status = %d", target, response.Code)
		}
		want := "{\"schema_version\":1,\"error\":{\"code\":\"invalid_request\",\"message\":\"invalid public listing request\"}}\n"
		if response.Body.String() != want {
			t.Fatalf("%s body = %q", target, response.Body.String())
		}
		if strings.Contains(response.Body.String(), "cursor") || strings.Contains(response.Body.String(), "limit=") {
			t.Fatalf("%s disclosed request detail: %q", target, response.Body.String())
		}
	}
}

func TestPublicListingRejectsWriteMethods(t *testing.T) {
	repository := &publicListingRepositoryStub{page: storage.HealthProjectionPage{Relays: []storage.HealthProjectionRelay{}}}
	handler, err := NewPublicListingHandler(repository, func() time.Time { return time.Unix(2_000, 0).UTC() })
	if err != nil {
		t.Fatalf("NewPublicListingHandler() error = %v", err)
	}
	request := httptest.NewRequest(http.MethodPost, "/v1/relays", strings.NewReader("{}"))
	response := httptest.NewRecorder()
	handler.serve(response, request)
	if response.Code != http.StatusMethodNotAllowed || response.Header().Get("Allow") != "GET, HEAD" {
		t.Fatalf("response = status %d Allow %q", response.Code, response.Header().Get("Allow"))
	}
}

func TestPublicListingRepositoryFailureIsRedacted(t *testing.T) {
	repository := &publicListingRepositoryStub{err: errors.New("secret sqlite path /srv/private.sqlite")}
	handler, err := NewPublicListingHandler(repository, func() time.Time { return time.Unix(2_000, 0).UTC() })
	if err != nil {
		t.Fatalf("NewPublicListingHandler() error = %v", err)
	}
	request := httptest.NewRequest(http.MethodGet, "/v1/relays", nil)
	response := httptest.NewRecorder()
	handler.serve(response, request)
	if response.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d", response.Code)
	}
	if strings.Contains(response.Body.String(), "sqlite") || strings.Contains(response.Body.String(), "/srv") {
		t.Fatalf("body disclosed backend detail: %q", response.Body.String())
	}
}

func TestPublicListingAdmissionIsIndependentlyBounded(t *testing.T) {
	block := make(chan struct{})
	repository := &publicListingRepositoryStub{block: block}
	handler, err := newPublicListingHandler(repository, func() time.Time { return time.Unix(2_000, 0).UTC() }, 1)
	if err != nil {
		t.Fatalf("newPublicListingHandler() error = %v", err)
	}

	started := make(chan struct{})
	go func() {
		close(started)
		request := httptest.NewRequest(http.MethodGet, "/v1/relays", nil)
		response := httptest.NewRecorder()
		handler.serve(response, request)
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
			t.Fatal("first request did not enter repository")
		}
		time.Sleep(time.Millisecond)
	}

	request := httptest.NewRequest(http.MethodGet, "/v1/relays", nil)
	response := httptest.NewRecorder()
	handler.serve(response, request)
	if response.Code != http.StatusTooManyRequests || response.Header().Get("Retry-After") != "1" {
		t.Fatalf("bounded response = status %d headers %#v", response.Code, response.Header())
	}
	close(block)
}

func jsonUnmarshalStrict(data []byte, target any) error {
	decoder := json.NewDecoder(strings.NewReader(string(data)))
	decoder.DisallowUnknownFields()
	return decoder.Decode(target)
}
