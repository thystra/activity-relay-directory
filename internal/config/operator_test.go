package config

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func writeOperatorConfig(t *testing.T, body string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "config.yml")
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestLoadOperatorMetadataAllFields(t *testing.T) {
	path := writeOperatorConfig(t, `
OPERATOR-WEBSITE: "https://operator.example/"
OPERATOR-EMAIL: "operator@example.museum"
FEDIVERSE-OPERATOR-ID: "@operator@social.example"
FEDIVERSE-OPERATOR-URL: "https://social.example/@operator"
`)
	got, err := loadOperatorMetadataFile(path, true)
	if err != nil {
		t.Fatalf("loadOperatorMetadataFile() error = %v", err)
	}
	want := OperatorMetadata{
		Website:      "https://operator.example/",
		Email:        "operator@example.museum",
		FediverseID:  "@operator@social.example",
		FediverseURL: "https://social.example/@operator",
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("metadata = %#v, want %#v", got, want)
	}
}

func TestLoadOperatorMetadataMissingDefaultSuppressesOperator(t *testing.T) {
	path := filepath.Join(t.TempDir(), "missing.yml")
	got, err := loadOperatorMetadataFile(path, false)
	if err != nil || !got.Empty() {
		t.Fatalf("loadOperatorMetadataFile(missing default) = (%#v, %v)", got, err)
	}
	if _, err := loadOperatorMetadataFile(path, true); err == nil {
		t.Fatal("explicit missing operator config unexpectedly succeeded")
	}
}

func TestLoadOperatorMetadataAllowsIndependentWebsiteAndEmail(t *testing.T) {
	for name, body := range map[string]string{
		"website": `OPERATOR-WEBSITE: "https://operator.example/"`,
		"email":   `OPERATOR-EMAIL: "operator@example.technology"`,
		"empty":   `OPERATOR-WEBSITE: ""`,
	} {
		t.Run(name, func(t *testing.T) {
			got, err := loadOperatorMetadataFile(writeOperatorConfig(t, body), true)
			if err != nil {
				t.Fatalf("loadOperatorMetadataFile() error = %v", err)
			}
			if name == "empty" && !got.Empty() {
				t.Fatalf("empty metadata = %#v", got)
			}
		})
	}
}

func TestOperatorEmailValidationIsLooseButRequiresDomainSuffix(t *testing.T) {
	for _, value := range []string{
		"operator@example.org",
		"operator@example.museum",
		"ops+directory@subdomain.example.technology",
		"first.last@example.photography",
	} {
		if !validOperatorEmail(value) {
			t.Errorf("validOperatorEmail(%q) = false", value)
		}
	}
	for _, value := range []string{
		"operator@example",
		"operator@",
		"@example.org",
		"operator@@example.org",
		"operator @example.org",
		"operator@example.",
	} {
		if validOperatorEmail(value) {
			t.Errorf("validOperatorEmail(%q) = true", value)
		}
	}
}

func TestLoadOperatorMetadataReportsNonBlockingValueProblems(t *testing.T) {
	cases := []struct {
		name             string
		body             string
		wantWebsite      string
		wantEmail        string
		wantFediverseID  string
		wantFediverseURL string
		wantDiagnostics  []string
	}{
		{
			name:            "malformed website",
			body:            `OPERATOR-WEBSITE: "http://operator.example/"`,
			wantDiagnostics: []string{operatorWebsiteMalformedDiagnostic},
		},
		{
			name:            "malformed email without tld",
			body:            `OPERATOR-EMAIL: "operator@example"`,
			wantDiagnostics: []string{operatorEmailMalformedDiagnostic},
		},
		{
			name:            "fediverse id missing",
			body:            `FEDIVERSE-OPERATOR-URL: "https://social.example/@operator"`,
			wantDiagnostics: []string{fediverseIDMissingDiagnostic},
		},
		{
			name:            "fediverse url missing",
			body:            `FEDIVERSE-OPERATOR-ID: "@operator@social.example"`,
			wantDiagnostics: []string{fediverseURLMissingDiagnostic},
		},
		{
			name: "fediverse id malformed",
			body: `
FEDIVERSE-OPERATOR-ID: "operator@social.example"
FEDIVERSE-OPERATOR-URL: "https://social.example/@operator"
`,
			wantDiagnostics: []string{fediverseIDMalformedDiagnostic},
		},
		{
			name: "fediverse url malformed",
			body: `
FEDIVERSE-OPERATOR-ID: "@operator@social.example"
FEDIVERSE-OPERATOR-URL: "http://social.example/@operator"
`,
			wantDiagnostics: []string{fediverseURLMalformedDiagnostic},
		},
		{
			name: "valid independent fields survive partial fediverse",
			body: `
OPERATOR-WEBSITE: "https://operator.example/"
OPERATOR-EMAIL: "operator@example.solutions"
FEDIVERSE-OPERATOR-URL: "https://social.example/@operator"
`,
			wantWebsite:     "https://operator.example/",
			wantEmail:       "operator@example.solutions",
			wantDiagnostics: []string{fediverseIDMissingDiagnostic},
		},
		{
			name: "every bad member is diagnosed",
			body: `
OPERATOR-WEBSITE: "http://operator.example/"
OPERATOR-EMAIL: "operator@example"
FEDIVERSE-OPERATOR-ID: "bad-handle"
`,
			wantDiagnostics: []string{
				operatorWebsiteMalformedDiagnostic,
				operatorEmailMalformedDiagnostic,
				fediverseIDMalformedDiagnostic,
				fediverseURLMissingDiagnostic,
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := loadOperatorMetadataFile(writeOperatorConfig(t, tc.body), true)
			if err != nil {
				t.Fatalf("loadOperatorMetadataFile() error = %v", err)
			}
			if got.Website != tc.wantWebsite || got.Email != tc.wantEmail ||
				got.FediverseID != tc.wantFediverseID || got.FediverseURL != tc.wantFediverseURL {
				t.Fatalf("metadata = %#v", got)
			}
			if !reflect.DeepEqual(got.Diagnostics, tc.wantDiagnostics) {
				t.Fatalf("diagnostics = %#v, want %#v", got.Diagnostics, tc.wantDiagnostics)
			}
		})
	}
}

func TestLoadOperatorMetadataRejectsStructuralConfigurationFailures(t *testing.T) {
	cases := map[string]string{
		"unknown field": `
OPERATOR-WEBSITE: "https://operator.example/"
NOT-A-DIRECTORY-FIELD: "x"
`,
		"multiple documents": `
OPERATOR-WEBSITE: "https://operator.example/"
---
OPERATOR-EMAIL: "operator@example.org"
`,
		"malformed yaml": `OPERATOR-WEBSITE: [`,
	}
	for name, body := range cases {
		t.Run(name, func(t *testing.T) {
			if got, err := loadOperatorMetadataFile(writeOperatorConfig(t, body), true); err == nil {
				t.Fatalf("unexpected success: %#v", got)
			}
		})
	}
}

func TestLoadOperatorMetadataExplicitPathMustBeAbsolute(t *testing.T) {
	t.Setenv("DIRECTORY_CONFIG_PATH", "relative/config.yml")
	if got, err := LoadOperatorMetadata(); err == nil || !strings.Contains(err.Error(), "clean absolute") {
		t.Fatalf("LoadOperatorMetadata() = (%#v, %v)", got, err)
	}
}
