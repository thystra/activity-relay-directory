package httpapi

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/thystra/activity-relay-directory/internal/config"
	"github.com/thystra/activity-relay-directory/internal/storage"
)

func TestHumanDirectoryUsesStatusVersion(t *testing.T) {
	repository := &publicListingRepositoryStub{
		page: storage.HealthProjectionPage{Relays: []storage.HealthProjectionRelay{}},
	}
	listing, err := NewPublicListingHandler(repository, func() time.Time { return time.Unix(100, 0).UTC() })
	if err != nil {
		t.Fatalf("NewPublicListingHandler() error = %v", err)
	}

	const version = "1.3.0-test"
	handler := NewHandlerWithRuntime(
		config.Config{PublicBaseURL: "https://directory.example", PublicListingEnabled: true},
		version,
		func(context.Context) error { return nil },
		nil,
		nil,
		listing,
	)

	statusResponse := httptest.NewRecorder()
	handler.ServeHTTP(statusResponse, httptest.NewRequest(http.MethodGet, "/v1/status", nil))
	if statusResponse.Code != http.StatusOK {
		t.Fatalf("status response = %d body %q", statusResponse.Code, statusResponse.Body.String())
	}
	var status map[string]any
	if err := json.Unmarshal(statusResponse.Body.Bytes(), &status); err != nil {
		t.Fatalf("decode status: %v", err)
	}
	if status["version"] != version {
		t.Fatalf("status version = %#v, want %q", status["version"], version)
	}

	pageResponse := httptest.NewRecorder()
	handler.ServeHTTP(pageResponse, httptest.NewRequest(http.MethodGet, "/", nil))
	if pageResponse.Code != http.StatusOK {
		t.Fatalf("page response = %d body %q", pageResponse.Code, pageResponse.Body.String())
	}
	if !strings.Contains(pageResponse.Body.String(), "Activity-Relay Directory "+version) {
		t.Fatalf("public-facing Directory does not show runtime version: %q", pageResponse.Body.String())
	}
}
