package config

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"unicode"

	"gopkg.in/yaml.v3"
)

const (
	defaultOperatorConfigPath  = "/etc/activity-relay-directory/config.yml"
	maximumOperatorConfigBytes = 64 * 1024

	operatorWebsiteMalformedDiagnostic = "OPERATOR-WEBSITE is malformed in config.yml."
	operatorEmailMalformedDiagnostic   = "OPERATOR-EMAIL is malformed in config.yml."
	fediverseIDMalformedDiagnostic     = "FEDIVERSE-OPERATOR-ID is malformed in config.yml."
	fediverseURLMalformedDiagnostic    = "FEDIVERSE-OPERATOR-URL is malformed in config.yml."
	fediverseIDMissingDiagnostic       = "Please configure FEDIVERSE-OPERATOR-ID in config.yml."
	fediverseURLMissingDiagnostic      = "Please configure FEDIVERSE-OPERATOR-URL in config.yml."
)

var (
	fediverseOperatorID = regexp.MustCompile(`^@[^@\s/]+@[^@\s/]+$`)
	operatorEmail       = regexp.MustCompile(`^[^@\s<>]+@[^@\s<>.]+(?:\.[^@\s<>.]+)+$`)
)

// OperatorMetadata is optional public contact information for the human
// Directory page. It is never copied into the JSON directory or status API.
// Diagnostics describe non-blocking presentation-value problems without
// publishing the malformed or incomplete value itself.
type OperatorMetadata struct {
	Website      string
	Email        string
	FediverseID  string
	FediverseURL string
	Diagnostics  []string
}

type operatorConfigFile struct {
	Website      string `yaml:"OPERATOR-WEBSITE"`
	Email        string `yaml:"OPERATOR-EMAIL"`
	FediverseID  string `yaml:"FEDIVERSE-OPERATOR-ID"`
	FediverseURL string `yaml:"FEDIVERSE-OPERATOR-URL"`
}

// LoadOperatorMetadata reads the optional presentation-only YAML configuration.
// When DIRECTORY_CONFIG_PATH is unset, a missing default file means that no
// operator links are rendered. An explicitly configured path is required to
// exist and be a clean absolute path. Structural file/YAML failures remain
// startup errors; malformed nice-to-have field values are converted to human-
// visible diagnostics and never prevent the core service from running.
func LoadOperatorMetadata() (OperatorMetadata, error) {
	raw := strings.TrimSpace(os.Getenv("DIRECTORY_CONFIG_PATH"))
	path := raw
	explicit := raw != ""
	if path == "" {
		path = defaultOperatorConfigPath
	}
	if explicit && (!filepath.IsAbs(path) || filepath.Clean(path) != path || strings.ContainsRune(path, '\x00')) {
		return OperatorMetadata{}, errors.New("DIRECTORY_CONFIG_PATH must be a clean absolute path")
	}
	return loadOperatorMetadataFile(path, explicit)
}

func loadOperatorMetadataFile(path string, explicit bool) (OperatorMetadata, error) {
	file, err := os.Open(path)
	if err != nil {
		if !explicit && errors.Is(err, os.ErrNotExist) {
			return OperatorMetadata{}, nil
		}
		return OperatorMetadata{}, fmt.Errorf("open operator config %q: %w", path, err)
	}
	defer file.Close()

	body, err := io.ReadAll(io.LimitReader(file, maximumOperatorConfigBytes+1))
	if err != nil {
		return OperatorMetadata{}, fmt.Errorf("read operator config %q: %w", path, err)
	}
	if len(body) > maximumOperatorConfigBytes {
		return OperatorMetadata{}, errors.New("operator config exceeds 64 KiB")
	}
	if len(bytes.TrimSpace(body)) == 0 {
		return OperatorMetadata{}, nil
	}

	decoder := yaml.NewDecoder(bytes.NewReader(body))
	decoder.KnownFields(true)
	var decoded operatorConfigFile
	if err := decoder.Decode(&decoded); err != nil {
		return OperatorMetadata{}, fmt.Errorf("decode operator config %q: %w", path, err)
	}
	var extra any
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		if err == nil {
			return OperatorMetadata{}, errors.New("operator config must contain exactly one YAML document")
		}
		return OperatorMetadata{}, fmt.Errorf("decode trailing operator config %q: %w", path, err)
	}

	return sanitizeOperatorMetadata(operatorConfigFile{
		Website:      strings.TrimSpace(decoded.Website),
		Email:        strings.TrimSpace(decoded.Email),
		FediverseID:  strings.TrimSpace(decoded.FediverseID),
		FediverseURL: strings.TrimSpace(decoded.FediverseURL),
	}), nil
}

// sanitizeOperatorMetadata implements the nice-to-have presentation contract:
// invalid values are not published and do not prevent the service from running.
// Each invalid or missing member gets a deterministic diagnostic so multi-key
// logical objects cannot fail silently.
func sanitizeOperatorMetadata(raw operatorConfigFile) OperatorMetadata {
	metadata := OperatorMetadata{}

	if raw.Website != "" {
		if containsOperatorControl(raw.Website) || validatePublicHTTPSURL("OPERATOR-WEBSITE", raw.Website) != nil {
			metadata.Diagnostics = append(metadata.Diagnostics, operatorWebsiteMalformedDiagnostic)
		} else {
			metadata.Website = raw.Website
		}
	}

	if raw.Email != "" {
		if !validOperatorEmail(raw.Email) {
			metadata.Diagnostics = append(metadata.Diagnostics, operatorEmailMalformedDiagnostic)
		} else {
			metadata.Email = raw.Email
		}
	}

	idProvided := raw.FediverseID != ""
	urlProvided := raw.FediverseURL != ""
	idValid := false
	urlValid := false

	if idProvided {
		if containsOperatorControl(raw.FediverseID) || !fediverseOperatorID.MatchString(raw.FediverseID) {
			metadata.Diagnostics = append(metadata.Diagnostics, fediverseIDMalformedDiagnostic)
		} else {
			idValid = true
		}
	}
	if urlProvided {
		if containsOperatorControl(raw.FediverseURL) || validatePublicHTTPSURL("FEDIVERSE-OPERATOR-URL", raw.FediverseURL) != nil {
			metadata.Diagnostics = append(metadata.Diagnostics, fediverseURLMalformedDiagnostic)
		} else {
			urlValid = true
		}
	}

	if !idProvided && urlProvided {
		metadata.Diagnostics = append(metadata.Diagnostics, fediverseIDMissingDiagnostic)
	}
	if idProvided && !urlProvided {
		metadata.Diagnostics = append(metadata.Diagnostics, fediverseURLMissingDiagnostic)
	}
	if idProvided && urlProvided && idValid && urlValid {
		metadata.FediverseID = raw.FediverseID
		metadata.FediverseURL = raw.FediverseURL
	}

	return metadata
}

func validOperatorEmail(value string) bool {
	return value != "" && len(value) <= 254 &&
		!containsOperatorControl(value) && operatorEmail.MatchString(value)
}

func containsOperatorControl(value string) bool {
	return strings.IndexFunc(value, unicode.IsControl) >= 0
}

func (metadata OperatorMetadata) HasLinks() bool {
	return metadata.Website != "" || metadata.Email != "" || metadata.FediverseID != ""
}

func (metadata OperatorMetadata) Empty() bool {
	return !metadata.HasLinks() && len(metadata.Diagnostics) == 0
}

func validatePublicHTTPSURL(name, value string) error {
	parsed, err := url.Parse(value)
	if err != nil || parsed.Scheme != "https" || parsed.Host == "" ||
		parsed.User != nil || !parsed.IsAbs() {
		return fmt.Errorf("%s must be an absolute HTTPS URL without credentials", name)
	}
	return nil
}
