package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/thystra/activity-relay-directory/internal/config"
)

func testHandler() http.Handler {
	return NewHandler(config.Config{
		ListenAddress:       "127.0.0.1:8080",
		PublicBaseURL:       "https://directory.example",
		DatabasePath:        "/var/lib/activity-relay-directory/directory.sqlite",
		RegistrationEnabled: false,
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
			RegistrationEnabled: false,
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

func TestStatusReportsRegistrationUnavailable(t *testing.T) {
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

	if body["registration_enabled"] != false {
		t.Fatalf("registration_enabled = %#v", body["registration_enabled"])
	}

	if body["registration_available"] != false {
		t.Fatalf("registration_available = %#v", body["registration_available"])
	}

	if body["version"] != "test-version" {
		t.Fatalf("version = %#v", body["version"])
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
