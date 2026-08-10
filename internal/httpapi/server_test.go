package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/thystra/activity-relay-directory/internal/config"
	v1 "github.com/thystra/activity-relay-directory/internal/protocol/v1"
	"github.com/thystra/activity-relay-directory/internal/storage"
)

func testHandler() http.Handler {
	return NewHandler(config.Config{
		ListenAddress:       "127.0.0.1:8080",
		PublicBaseURL:       "https://directory.example",
		DatabasePath:        "/var/lib/activity-relay-directory/directory.sqlite",
		LifecycleEnabled:    false,
		MaxRequestBodyBytes: 64 * 1024,
	}, "test-version", func(context.Context) error { return nil })
}

func TestReady(t *testing.T) {
	request := httptest.NewRequest(http.MethodGet, "/readyz", nil)
	response := httptest.NewRecorder()

	testHandler().ServeHTTP(response, request)

	if response.Code != http.StatusOK {
		t.Fatalf("status = %d", response.Code)
	}
	if response.Body.String() != "ready\n" {
		t.Fatalf("body = %q", response.Body.String())
	}
	if response.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("Cache-Control = %q", response.Header().Get("Cache-Control"))
	}
}

func TestReadyFailsClosedWithoutDisclosingDependencyError(t *testing.T) {
	for _, check := range []ReadinessCheck{
		nil,
		func(context.Context) error { return errors.New("sensitive database detail") },
	} {
		handler := NewHandler(config.Config{
			ListenAddress:       "127.0.0.1:8080",
			PublicBaseURL:       "https://directory.example",
			DatabasePath:        "/var/lib/activity-relay-directory/directory.sqlite",
			LifecycleEnabled:    false,
			MaxRequestBodyBytes: 64 * 1024,
		}, "test-version", check)
		request := httptest.NewRequest(http.MethodGet, "/readyz", nil)
		response := httptest.NewRecorder()

		handler.ServeHTTP(response, request)

		if response.Code != http.StatusServiceUnavailable {
			t.Fatalf("status = %d", response.Code)
		}
		if response.Body.String() != "not ready\n" {
			t.Fatalf("body = %q", response.Body.String())
		}
		if response.Header().Get("Cache-Control") != "no-store" {
			t.Fatalf("Cache-Control = %q", response.Header().Get("Cache-Control"))
		}
	}
}

func TestReadyHeadSuppressesBody(t *testing.T) {
	request := httptest.NewRequest(http.MethodHead, "/readyz", nil)
	response := httptest.NewRecorder()

	testHandler().ServeHTTP(response, request)

	if response.Code != http.StatusOK {
		t.Fatalf("status = %d", response.Code)
	}
	if response.Body.Len() != 0 {
		t.Fatalf("body = %q", response.Body.String())
	}
}

func TestHealth(t *testing.T) {
	request := httptest.NewRequest(http.MethodGet, "/healthz", nil)
	response := httptest.NewRecorder()

	testHandler().ServeHTTP(response, request)

	if response.Code != http.StatusOK {
		t.Fatalf("status = %d", response.Code)
	}

	if response.Body.String() != "ok\n" {
		t.Fatalf("body = %q", response.Body.String())
	}
}

func TestStatusReportsLifecycleAndEnrollmentUnavailable(t *testing.T) {
	request := httptest.NewRequest(http.MethodGet, "/v1/status", nil)
	response := httptest.NewRecorder()

	testHandler().ServeHTTP(response, request)

	if response.Code != http.StatusOK {
		t.Fatalf("status = %d", response.Code)
	}

	var body map[string]any
	if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil {
		t.Fatalf("json.Unmarshal() error = %v", err)
	}

	if body["schema_version"] != float64(statusSchemaVersion) {
		t.Fatalf("schema_version = %#v", body["schema_version"])
	}

	if body["lifecycle_enabled"] != false {
		t.Fatalf("lifecycle_enabled = %#v", body["lifecycle_enabled"])
	}

	if body["lifecycle_available"] != false {
		t.Fatalf("lifecycle_available = %#v", body["lifecycle_available"])
	}
	if body["enrollment_open"] != false {
		t.Fatalf("enrollment_open = %#v", body["enrollment_open"])
	}

	if body["public_listing_enabled"] != false || body["public_listing_available"] != false {
		t.Fatalf("public listing status = %#v", body)
	}

	if body["version"] != "test-version" {
		t.Fatalf("version = %#v", body["version"])
	}
}

func TestStatusReportsEnrollmentIndependentlyOfLifecycleGraph(t *testing.T) {
	handler := NewHandlerWithLifecycle(
		config.Config{
			PublicBaseURL:    "https://directory.example",
			LifecycleEnabled: false,
		},
		"test-version",
		func(context.Context) error { return nil },
		nil,
		func(context.Context) (bool, error) { return true, nil },
	)
	request := httptest.NewRequest(http.MethodGet, "/v1/status", nil)
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %q", response.Code, response.Body.String())
	}
	var body map[string]any
	if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode status: %v", err)
	}
	if body["lifecycle_available"] != false || body["enrollment_open"] != true {
		t.Fatalf("status body = %#v", body)
	}
}

func TestStatusFailsClosedWhenEnrollmentStateCannotBeRead(t *testing.T) {
	handler := NewHandlerWithLifecycle(
		config.Config{PublicBaseURL: "https://directory.example"},
		"test-version",
		func(context.Context) error { return nil },
		nil,
		func(context.Context) (bool, error) { return false, errors.New("private database detail") },
	)
	request := httptest.NewRequest(http.MethodGet, "/v1/status", nil)
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	assertProtocolError(t, response, http.StatusServiceUnavailable, v1.ErrorInternal)
	if strings.Contains(response.Body.String(), "private") {
		t.Fatalf("status disclosed backend error: %q", response.Body.String())
	}
}

func TestStatusRejectsPost(t *testing.T) {
	request := httptest.NewRequest(http.MethodPost, "/v1/status", nil)
	response := httptest.NewRecorder()

	testHandler().ServeHTTP(response, request)

	if response.Code != http.StatusMethodNotAllowed {
		t.Fatalf("status = %d", response.Code)
	}

	if response.Header().Get("Allow") != "GET, HEAD" {
		t.Fatalf("Allow = %q", response.Header().Get("Allow"))
	}
}

func TestSecurityHeaders(t *testing.T) {
	request := httptest.NewRequest(http.MethodGet, "/healthz", nil)
	response := httptest.NewRecorder()

	testHandler().ServeHTTP(response, request)

	if response.Header().Get("X-Content-Type-Options") != "nosniff" {
		t.Fatal("missing X-Content-Type-Options")
	}

	if response.Header().Get("Referrer-Policy") != "no-referrer" {
		t.Fatal("missing Referrer-Policy")
	}

	if response.Header().Get("Content-Security-Policy") == "" {
		t.Fatal("missing Content-Security-Policy")
	}
}

func TestPublicStatusOmitsPrivateModerationFields(t *testing.T) {
	request := httptest.NewRequest(http.MethodGet, "/v1/status", nil)
	response := httptest.NewRecorder()
	testHandler().ServeHTTP(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("status = %d", response.Code)
	}
	body := response.Body.String()
	for _, private := range []string{
		"moderator_id",
		"reason_code",
		"moderation_event_id",
		"suspended_at_unix",
	} {
		if strings.Contains(body, private) {
			t.Fatalf("public status disclosed %q: %s", private, body)
		}
	}
}

func TestPublicHTTPDoesNotExposeLocalMaintenance(t *testing.T) {
	for _, method := range []string{http.MethodGet, http.MethodHead, http.MethodPost, http.MethodDelete} {
		for _, path := range []string{
			"/v1/pruning",
			"/v1/pruning/run",
			"/admin/pruning",
			"/v1/retention",
			"/v1/retention/purge",
			"/admin/retention",
			"/v1/purge",
		} {
			request := httptest.NewRequest(method, path, nil)
			response := httptest.NewRecorder()
			testHandler().ServeHTTP(response, request)
			if response.Code != http.StatusNotFound {
				t.Fatalf("%s %s status = %d, want 404", method, path, response.Code)
			}
		}
	}
}

func TestPublicListingRouteIsDefaultOff(t *testing.T) {
	request := httptest.NewRequest(http.MethodGet, "/v1/relays", nil)
	response := httptest.NewRecorder()
	testHandler().ServeHTTP(response, request)
	if response.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", response.Code)
	}
}

func TestPublicListingIsIndependentOfLifecycleAvailability(t *testing.T) {
	repository := &publicListingRepositoryStub{page: storage.HealthProjectionPage{Relays: []storage.HealthProjectionRelay{}}}
	listing, err := NewPublicListingHandler(repository, func() time.Time { return time.Unix(100, 0).UTC() })
	if err != nil {
		t.Fatalf("NewPublicListingHandler() error = %v", err)
	}
	handler := NewHandlerWithRuntime(
		config.Config{PublicBaseURL: "https://directory.example", PublicListingEnabled: true},
		"test-version",
		func(context.Context) error { return nil },
		nil,
		func(context.Context) (bool, error) { return false, nil },
		listing,
	)

	listingRequest := httptest.NewRequest(http.MethodGet, "/v1/relays", nil)
	listingResponse := httptest.NewRecorder()
	handler.ServeHTTP(listingResponse, listingRequest)
	if listingResponse.Code != http.StatusOK {
		t.Fatalf("listing status = %d, body = %q", listingResponse.Code, listingResponse.Body.String())
	}

	lifecycleRequest := httptest.NewRequest(http.MethodPost, v1.HeartbeatEndpointPath, nil)
	lifecycleResponse := httptest.NewRecorder()
	handler.ServeHTTP(lifecycleResponse, lifecycleRequest)
	assertProtocolError(t, lifecycleResponse, http.StatusServiceUnavailable, v1.ErrorLifecycleUnavailable)

	statusRequest := httptest.NewRequest(http.MethodGet, "/v1/status", nil)
	statusResponse := httptest.NewRecorder()
	handler.ServeHTTP(statusResponse, statusRequest)
	var body map[string]any
	if err := json.Unmarshal(statusResponse.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode status: %v", err)
	}
	if body["public_listing_enabled"] != true || body["public_listing_available"] != true ||
		body["lifecycle_available"] != false {
		t.Fatalf("status body = %#v", body)
	}
}

func TestEnabledListingWithMissingGraphFailsClosed(t *testing.T) {
	handler := NewHandlerWithRuntime(
		config.Config{PublicBaseURL: "https://directory.example", PublicListingEnabled: true},
		"test-version",
		func(context.Context) error { return nil },
		nil,
		nil,
		nil,
	)
	request := httptest.NewRequest(http.MethodGet, "/v1/relays", nil)
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusServiceUnavailable || strings.Contains(response.Body.String(), "nil") {
		t.Fatalf("response = status %d body %q", response.Code, response.Body.String())
	}

	post := httptest.NewRequest(http.MethodPost, "/v1/relays", strings.NewReader("{}"))
	postResponse := httptest.NewRecorder()
	handler.ServeHTTP(postResponse, post)
	if postResponse.Code != http.StatusMethodNotAllowed || postResponse.Header().Get("Allow") != "GET, HEAD" {
		t.Fatalf("POST response = status %d Allow %q", postResponse.Code, postResponse.Header().Get("Allow"))
	}
}

func TestHumanDirectoryRouteIsDefaultOff(t *testing.T) {
	for _, path := range []string{"/", directoryStylesheetPath} {
		request := httptest.NewRequest(http.MethodGet, path, nil)
		response := httptest.NewRecorder()
		testHandler().ServeHTTP(response, request)
		if response.Code != http.StatusNotFound {
			t.Fatalf("%s status = %d, want 404", path, response.Code)
		}
	}
}

func TestHumanDirectorySharesPublicListingGateAndProjection(t *testing.T) {
	repository := &publicListingRepositoryStub{page: storage.HealthProjectionPage{Relays: []storage.HealthProjectionRelay{}}}
	listing, err := NewPublicListingHandler(repository, func() time.Time { return time.Unix(100, 0).UTC() })
	if err != nil {
		t.Fatalf("NewPublicListingHandler() error = %v", err)
	}
	handler := NewHandlerWithRuntime(
		config.Config{PublicBaseURL: "https://directory.example", PublicListingEnabled: true},
		"test-version",
		func(context.Context) error { return nil },
		nil,
		nil,
		listing,
	)

	for _, path := range []string{"/", "/v1/relays", directoryStylesheetPath} {
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, path, nil))
		if response.Code != http.StatusOK {
			t.Fatalf("%s status = %d body = %q", path, response.Code, response.Body.String())
		}
	}

	unknown := httptest.NewRecorder()
	handler.ServeHTTP(unknown, httptest.NewRequest(http.MethodGet, "/not-a-directory-route", nil))
	if unknown.Code != http.StatusNotFound {
		t.Fatalf("unknown route status = %d", unknown.Code)
	}
}

func TestEnabledHumanDirectoryWithMissingGraphFailsClosed(t *testing.T) {
	handler := NewHandlerWithRuntime(
		config.Config{PublicBaseURL: "https://directory.example", PublicListingEnabled: true},
		"test-version",
		func(context.Context) error { return nil },
		nil,
		nil,
		nil,
	)

	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/", nil))
	if response.Code != http.StatusServiceUnavailable ||
		response.Body.String() != "directory temporarily unavailable\n" {
		t.Fatalf("response = status %d body %q", response.Code, response.Body.String())
	}
	if response.Header().Get("Content-Security-Policy") != humanDirectoryCSP {
		t.Fatalf("CSP = %q", response.Header().Get("Content-Security-Policy"))
	}

	post := httptest.NewRecorder()
	handler.ServeHTTP(post, httptest.NewRequest(http.MethodPost, "/", strings.NewReader("x")))
	if post.Code != http.StatusMethodNotAllowed || post.Header().Get("Allow") != "GET, HEAD" {
		t.Fatalf("POST response = status %d Allow %q", post.Code, post.Header().Get("Allow"))
	}
}
