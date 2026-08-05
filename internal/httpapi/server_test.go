package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/thystra/activity-relay-directory/internal/config"
	v1 "github.com/thystra/activity-relay-directory/internal/protocol/v1"
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
