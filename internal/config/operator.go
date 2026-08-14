package config

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"net/mail"
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
)

var fediverseOperatorID = regexp.MustCompile(`^@[^@\s/]+@[^@\s/]+$`)

// OperatorMetadata is optional public contact information for the human
// Directory page.  It is never copied into the JSON directory or status API.
type OperatorMetadata struct {
	Website      string
	Email        string
	FediverseID  string
	FediverseURL string
}

type operatorConfigFile struct {
	Website      string `yaml:"OPERATOR-WEBSITE"`
	Email        string `yaml:"OPERATOR-EMAIL"`
	FediverseID  string `yaml:"FEDIVERSE-OPERATOR-ID"`
	FediverseURL string `yaml:"FEDIVERSE-OPERATOR-URL"`
}

// LoadOperatorMetadata reads the optional presentation-only YAML configuration.
// When DIRECTORY_CONFIG_PATH is unset, a missing default file means that no
// operator links are rendered.  An explicitly configured path is required to
// exist and be a clean absolute path.
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

	metadata := OperatorMetadata{
		Website:      strings.TrimSpace(decoded.Website),
		Email:        strings.TrimSpace(decoded.Email),
		FediverseID:  strings.TrimSpace(decoded.FediverseID),
		FediverseURL: strings.TrimSpace(decoded.FediverseURL),
	}
	if err := metadata.Validate(); err != nil {
		return OperatorMetadata{}, err
	}
	return metadata, nil
}

// Validate prevents unsafe or misleading public links.  Fediverse profile URLs
// are explicit because profile URL shapes differ among Fediverse applications;
// the URL is never derived from the displayed @user@host identifier.
func (metadata OperatorMetadata) Validate() error {
	for name, value := range map[string]string{
		"OPERATOR-WEBSITE":       metadata.Website,
		"OPERATOR-EMAIL":         metadata.Email,
		"FEDIVERSE-OPERATOR-ID":  metadata.FediverseID,
		"FEDIVERSE-OPERATOR-URL": metadata.FediverseURL,
	} {
		if strings.IndexFunc(value, unicode.IsControl) >= 0 {
			return fmt.Errorf("%s must not contain control characters", name)
		}
	}

	if metadata.Website != "" {
		if err := validatePublicHTTPSURL("OPERATOR-WEBSITE", metadata.Website); err != nil {
			return err
		}
	}
	if metadata.Email != "" {
		address, err := mail.ParseAddress(metadata.Email)
		if err != nil || address.Name != "" || address.Address != metadata.Email ||
			strings.Count(metadata.Email, "@") != 1 {
			return errors.New("OPERATOR-EMAIL must be one plain email address")
		}
	}

	if (metadata.FediverseID == "") != (metadata.FediverseURL == "") {
		return errors.New("FEDIVERSE-OPERATOR-ID and FEDIVERSE-OPERATOR-URL must be configured together")
	}
	if metadata.FediverseID != "" {
		if !fediverseOperatorID.MatchString(metadata.FediverseID) {
			return errors.New("FEDIVERSE-OPERATOR-ID must use @user@host form")
		}
		if err := validatePublicHTTPSURL("FEDIVERSE-OPERATOR-URL", metadata.FediverseURL); err != nil {
			return err
		}
	}
	return nil
}

func (metadata OperatorMetadata) Empty() bool {
	return metadata.Website == "" && metadata.Email == "" &&
		metadata.FediverseID == "" && metadata.FediverseURL == ""
}

func validatePublicHTTPSURL(name, value string) error {
	parsed, err := url.Parse(value)
	if err != nil || parsed.Scheme != "https" || parsed.Host == "" ||
		parsed.User != nil || !parsed.IsAbs() {
		return fmt.Errorf("%s must be an absolute HTTPS URL without credentials", name)
	}
	return nil
}
