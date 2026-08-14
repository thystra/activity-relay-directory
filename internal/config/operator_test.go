package config

import (
	"os"
	"path/filepath"
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
OPERATOR-EMAIL: "operator@example.org"
FEDIVERSE-OPERATOR-ID: "@operator@social.example"
FEDIVERSE-OPERATOR-URL: "https://social.example/@operator"
`)
	got, err := loadOperatorMetadataFile(path, true)
	if err != nil {
		t.Fatalf("loadOperatorMetadataFile() error = %v", err)
	}
	if got.Website != "https://operator.example/" ||
		got.Email != "operator@example.org" ||
		got.FediverseID != "@operator@social.example" ||
		got.FediverseURL != "https://social.example/@operator" {
		t.Fatalf("metadata = %#v", got)
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
		"email":   `OPERATOR-EMAIL: "operator@example.org"`,
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

func TestLoadOperatorMetadataRejectsUnsafeOrAmbiguousValues(t *testing.T) {
	cases := map[string]string{
		"unknown field": `
OPERATOR-WEBSITE: "https://operator.example/"
NOT-A-DIRECTORY-FIELD: "x"
`,
		"http website":       `OPERATOR-WEBSITE: "http://operator.example/"`,
		"email display name": `OPERATOR-EMAIL: "Operator <operator@example.org>"`,
		"fediverse id only":  `FEDIVERSE-OPERATOR-ID: "@operator@social.example"`,
		"fediverse url only": `FEDIVERSE-OPERATOR-URL: "https://social.example/@operator"`,
		"bad fediverse id": `
FEDIVERSE-OPERATOR-ID: "operator@social.example"
FEDIVERSE-OPERATOR-URL: "https://social.example/@operator"
`,
		"http fediverse url": `
FEDIVERSE-OPERATOR-ID: "@operator@social.example"
FEDIVERSE-OPERATOR-URL: "http://social.example/@operator"
`,
		"multiple documents": `
OPERATOR-WEBSITE: "https://operator.example/"
---
OPERATOR-EMAIL: "operator@example.org"
`,
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
