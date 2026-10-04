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
SUPPORT:
  - title: "Liberapay"
    url: "https://liberapay.example/operator"
  - title: "Bitcoin"
    value: "bc1qexample"
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
		Support: []SupportEntry{
			{Title: "Liberapay", URL: "https://liberapay.example/operator"},
			{Title: "Bitcoin", Value: "bc1qexample"},
		},
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

func TestLoadOperatorMetadataSupportValidation(t *testing.T) {
	path := writeOperatorConfig(t, `
SUPPORT:
  - title: "Valid link"
    url: "https://support.example/path"
  - title: "Valid value"
    value: "wallet-address"
  - title: "Insecure link"
    url: "http://support.example/"
  - title: "Ambiguous"
    url: "https://support.example/other"
    value: "also-a-value"
  - title: ""
    value: "missing-title"
`)
	got, err := loadOperatorMetadataFile(path, true)
	if err != nil {
		t.Fatalf("loadOperatorMetadataFile() error = %v", err)
	}
	wantSupport := []SupportEntry{
		{Title: "Valid link", URL: "https://support.example/path"},
		{Title: "Valid value", Value: "wallet-address"},
	}
	if !reflect.DeepEqual(got.Support, wantSupport) {
		t.Fatalf("support = %#v, want %#v", got.Support, wantSupport)
	}
	wantDiagnostics := []string{
		"SUPPORT entry 3 is malformed in config.yml.",
		"SUPPORT entry 4 is malformed in config.yml.",
		"SUPPORT entry 5 is malformed in config.yml.",
	}
	if !reflect.DeepEqual(got.Diagnostics, wantDiagnostics) {
		t.Fatalf("diagnostics = %#v, want %#v", got.Diagnostics, wantDiagnostics)
	}
}

func TestLoadOperatorMetadataSupportEntryBound(t *testing.T) {
	var body strings.Builder
	body.WriteString("SUPPORT:\n")
	for index := 0; index < maximumSupportEntries+1; index++ {
		body.WriteString("  - title: method\n    value: value\n")
	}
	got, err := loadOperatorMetadataFile(writeOperatorConfig(t, body.String()), true)
	if err != nil {
		t.Fatalf("loadOperatorMetadataFile() error = %v", err)
	}
	if len(got.Support) != 0 || !reflect.DeepEqual(got.Diagnostics, []string{supportTooManyDiagnostic}) {
		t.Fatalf("bounded support = %#v diagnostics=%#v", got.Support, got.Diagnostics)
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
