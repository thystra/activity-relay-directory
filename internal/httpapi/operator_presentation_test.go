package httpapi

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/thystra/activity-relay-directory/internal/config"
	"github.com/thystra/activity-relay-directory/internal/storage"
)

func TestHumanDirectoryOperatorLinksAreConditionalAndExplicit(t *testing.T) {
	repository := &publicListingRepositoryStub{page: storage.HealthProjectionPage{Relays: []storage.HealthProjectionRelay{}}}
	base, err := NewPublicListingHandler(repository, func() time.Time { return time.Unix(100, 0).UTC() })
	if err != nil {
		t.Fatalf("NewPublicListingHandler() error = %v", err)
	}

	render := func(handler *PublicListingHandler) string {
		t.Helper()
		response := httptest.NewRecorder()
		handler.serveHumanDirectory(response, httptest.NewRequest(http.MethodGet, "/", nil))
		if response.Code != http.StatusOK {
			t.Fatalf("status = %d body = %q", response.Code, response.Body.String())
		}
		return response.Body.String()
	}

	empty := render(base)
	for _, forbidden := range []string{
		`aria-label="Operator contact"`,
		"Operator website",
		"mailto:",
		"FEDIVERSE-OPERATOR",
		"JSON API",
		"View JSON",
		"Public metadata only",
		"Privacy boundary",
	} {
		if strings.Contains(empty, forbidden) {
			t.Fatalf("default human page unexpectedly contains %q: %q", forbidden, empty)
		}
	}

	all := render(base.WithOperatorMetadata(config.OperatorMetadata{
		Website:      "https://operator.example/",
		Email:        "operator@example.org",
		FediverseID:  "@operator@social.example",
		FediverseURL: "https://social.example/@operator",
	}))
	for _, required := range []string{
		`aria-label="Operator contact"`,
		`href="https://operator.example/" rel="noreferrer">Operator website</a>`,
		`href="mailto:operator@example.org">operator@example.org</a>`,
		`href="https://social.example/@operator" rel="me noreferrer">@operator@social.example</a>`,
	} {
		if !strings.Contains(all, required) {
			t.Fatalf("configured operator page missing %q: %q", required, all)
		}
	}
	for _, forbidden := range []string{"JSON API", "View JSON", "Public metadata only", "Privacy boundary"} {
		if strings.Contains(all, forbidden) {
			t.Fatalf("configured operator page unexpectedly contains %q", forbidden)
		}
	}

	websiteOnly := render(base.WithOperatorMetadata(config.OperatorMetadata{
		Website: "https://operator.example/",
	}))
	if !strings.Contains(websiteOnly, "Operator website") ||
		strings.Contains(websiteOnly, "mailto:") ||
		strings.Contains(websiteOnly, "@operator@") {
		t.Fatalf("website-only operator rendering = %q", websiteOnly)
	}
}

func TestHumanDirectoryOperatorMetadataDoesNotChangeJSONProjection(t *testing.T) {
	repository := &publicListingRepositoryStub{page: storage.HealthProjectionPage{Relays: []storage.HealthProjectionRelay{}}}
	base, err := NewPublicListingHandler(repository, func() time.Time { return time.Unix(100, 0).UTC() })
	if err != nil {
		t.Fatal(err)
	}
	handler := base.WithOperatorMetadata(config.OperatorMetadata{
		Website:      "https://operator.example/",
		Email:        "operator@example.org",
		FediverseID:  "@operator@social.example",
		FediverseURL: "https://social.example/@operator",
	})
	response := httptest.NewRecorder()
	handler.serve(response, httptest.NewRequest(http.MethodGet, "/v1/relays", nil))
	if response.Code != http.StatusOK {
		t.Fatalf("JSON status = %d body = %q", response.Code, response.Body.String())
	}
	body := response.Body.String()
	for _, forbidden := range []string{
		"operator.example",
		"operator@example.org",
		"social.example",
		"FEDIVERSE",
		"operator_website",
		"operator_email",
	} {
		if strings.Contains(body, forbidden) {
			t.Fatalf("JSON projection leaked operator metadata %q: %q", forbidden, body)
		}
	}
}
