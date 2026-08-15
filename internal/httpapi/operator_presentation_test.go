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

func newOperatorPresentationTestHandler(t *testing.T) *PublicListingHandler {
	t.Helper()
	repository := &publicListingRepositoryStub{page: storage.HealthProjectionPage{Relays: []storage.HealthProjectionRelay{}}}
	handler, err := NewPublicListingHandler(repository, func() time.Time { return time.Unix(100, 0).UTC() })
	if err != nil {
		t.Fatalf("NewPublicListingHandler() error = %v", err)
	}
	return handler
}

func renderOperatorPresentation(t *testing.T, handler *PublicListingHandler) string {
	t.Helper()
	response := httptest.NewRecorder()
	handler.serveHumanDirectory(response, httptest.NewRequest(http.MethodGet, "/", nil))
	if response.Code != http.StatusOK {
		t.Fatalf("status = %d body = %q", response.Code, response.Body.String())
	}
	return response.Body.String()
}

func TestHumanDirectoryOperatorVisibilityMatrix(t *testing.T) {
	base := newOperatorPresentationTestHandler(t)

	website := `href="https://operator.example/" rel="noreferrer">Operator website</a>`
	email := `href="mailto:operator@example.technology">operator@example.technology</a>`
	fediverse := `href="https://social.example/@operator" rel="me noreferrer">@operator@social.example</a>`

	for mask := 0; mask < 8; mask++ {
		mask := mask
		t.Run([]string{
			"none", "website", "email", "website-email",
			"fediverse", "website-fediverse", "email-fediverse", "all",
		}[mask], func(t *testing.T) {
			metadata := config.OperatorMetadata{}
			if mask&1 != 0 {
				metadata.Website = "https://operator.example/"
			}
			if mask&2 != 0 {
				metadata.Email = "operator@example.technology"
			}
			if mask&4 != 0 {
				metadata.FediverseID = "@operator@social.example"
				metadata.FediverseURL = "https://social.example/@operator"
			}

			body := renderOperatorPresentation(t, base.WithOperatorMetadata(metadata))
			checks := []struct {
				bit    int
				needle string
			}{
				{1, website},
				{2, email},
				{4, fediverse},
			}
			for _, check := range checks {
				if mask&check.bit != 0 {
					if !strings.Contains(body, check.needle) {
						t.Fatalf("mask %d missing %q: %q", mask, check.needle, body)
					}
				} else if strings.Contains(body, check.needle) {
					t.Fatalf("mask %d unexpectedly contains %q: %q", mask, check.needle, body)
				}
			}

			if mask == 0 {
				for _, forbidden := range []string{
					`aria-label="Operator contact"`,
					`aria-label="Operator configuration notice"`,
					"operator-contact",
				} {
					if strings.Contains(body, forbidden) {
						t.Fatalf("empty operator page unexpectedly contains %q: %q", forbidden, body)
					}
				}
			} else if !strings.Contains(body, `aria-label="Operator contact"`) {
				t.Fatalf("mask %d missing operator contact navigation", mask)
			}
		})
	}
}

func TestHumanDirectoryOperatorDiagnosticsRenderWithoutUnsafePartialLinks(t *testing.T) {
	base := newOperatorPresentationTestHandler(t)
	cases := []struct {
		name       string
		metadata   config.OperatorMetadata
		diagnostic string
		required   []string
		forbidden  []string
	}{
		{
			name: "missing fediverse id preserves website and email",
			metadata: config.OperatorMetadata{
				Website:     "https://operator.example/",
				Email:       "operator@example.technology",
				Diagnostics: []string{"Please configure FEDIVERSE-OPERATOR-ID in config.yml."},
			},
			diagnostic: "Please configure FEDIVERSE-OPERATOR-ID in config.yml.",
			required: []string{
				"Operator website",
				"mailto:operator@example.technology",
			},
			forbidden: []string{"rel=\"me noreferrer\"", "social.example/@operator"},
		},
		{
			name: "missing fediverse url",
			metadata: config.OperatorMetadata{
				Diagnostics: []string{"Please configure FEDIVERSE-OPERATOR-URL in config.yml."},
			},
			diagnostic: "Please configure FEDIVERSE-OPERATOR-URL in config.yml.",
			forbidden:  []string{"rel=\"me noreferrer\"", "@operator@social.example"},
		},
		{
			name: "malformed email",
			metadata: config.OperatorMetadata{
				Diagnostics: []string{"OPERATOR-EMAIL is malformed in config.yml."},
			},
			diagnostic: "OPERATOR-EMAIL is malformed in config.yml.",
			forbidden:  []string{"mailto:", "operator@example"},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			body := renderOperatorPresentation(t, base.WithOperatorMetadata(tc.metadata))
			if !strings.Contains(body, `role="status" aria-label="Operator configuration notice"`) {
				t.Fatalf("diagnostic container missing: %q", body)
			}
			if !strings.Contains(body, tc.diagnostic) {
				t.Fatalf("diagnostic missing %q: %q", tc.diagnostic, body)
			}
			for _, required := range tc.required {
				if !strings.Contains(body, required) {
					t.Fatalf("required independent presentation %q missing: %q", required, body)
				}
			}
			for _, forbidden := range tc.forbidden {
				if strings.Contains(body, forbidden) {
					t.Fatalf("unsafe/partial presentation %q rendered: %q", forbidden, body)
				}
			}
		})
	}
}

func TestHumanDirectoryOperatorMetadataDoesNotChangeJSONProjection(t *testing.T) {
	base := newOperatorPresentationTestHandler(t)
	handler := base.WithOperatorMetadata(config.OperatorMetadata{
		Website:      "https://operator.example/",
		Email:        "operator@example.org",
		FediverseID:  "@operator@social.example",
		FediverseURL: "https://social.example/@operator",
		Diagnostics: []string{
			"OPERATOR-EMAIL is malformed in config.yml.",
			"Please configure FEDIVERSE-OPERATOR-ID in config.yml.",
		},
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
		"OPERATOR-EMAIL",
		"config.yml",
		"operator_website",
		"operator_email",
	} {
		if strings.Contains(body, forbidden) {
			t.Fatalf("JSON projection leaked operator metadata/diagnostic %q: %q", forbidden, body)
		}
	}
}
