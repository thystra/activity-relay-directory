package config

import (
	"errors"
	"fmt"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

const (
	defaultListenAddress       = "127.0.0.1:8080"
	defaultMaxRequestBodyBytes = int64(64 * 1024)
	maxRequestBodyBytes        = int64(1024 * 1024)
)

// Config is the directory service's process configuration.
type Config struct {
	ListenAddress       string
	PublicBaseURL       string
	DatabasePath        string
	RegistrationEnabled bool
	MaxRequestBodyBytes int64
}

// Load reads configuration from the process environment.
func Load() (Config, error) {
	cfg := Config{
		ListenAddress:       envOrDefault("DIRECTORY_LISTEN_ADDRESS", defaultListenAddress),
		PublicBaseURL:       strings.TrimSpace(os.Getenv("DIRECTORY_PUBLIC_BASE_URL")),
		DatabasePath:        strings.TrimSpace(os.Getenv("DIRECTORY_DATABASE_PATH")),
		RegistrationEnabled: false,
		MaxRequestBodyBytes: defaultMaxRequestBodyBytes,
	}

	if raw := strings.TrimSpace(os.Getenv("DIRECTORY_REGISTRATION_ENABLED")); raw != "" {
		value, err := strconv.ParseBool(raw)
		if err != nil {
			return Config{}, fmt.Errorf(
				"DIRECTORY_REGISTRATION_ENABLED must be a boolean: %w",
				err,
			)
		}
		cfg.RegistrationEnabled = value
	}

	if raw := strings.TrimSpace(os.Getenv("DIRECTORY_MAX_REQUEST_BODY_BYTES")); raw != "" {
		value, err := strconv.ParseInt(raw, 10, 64)
		if err != nil {
			return Config{}, fmt.Errorf(
				"DIRECTORY_MAX_REQUEST_BODY_BYTES must be an integer: %w",
				err,
			)
		}
		cfg.MaxRequestBodyBytes = value
	}

	if err := cfg.Validate(); err != nil {
		return Config{}, err
	}

	return cfg, nil
}

// Validate checks security-sensitive and operational configuration.
func (cfg Config) Validate() error {
	if _, err := net.ResolveTCPAddr("tcp", cfg.ListenAddress); err != nil {
		return fmt.Errorf("invalid DIRECTORY_LISTEN_ADDRESS: %w", err)
	}

	if cfg.PublicBaseURL == "" {
		return errors.New("DIRECTORY_PUBLIC_BASE_URL is required")
	}
	if cfg.DatabasePath == "" {
		return errors.New("DIRECTORY_DATABASE_PATH is required")
	}
	if !filepath.IsAbs(cfg.DatabasePath) ||
		filepath.Clean(cfg.DatabasePath) != cfg.DatabasePath ||
		strings.ContainsRune(cfg.DatabasePath, '\x00') {
		return errors.New(
			"DIRECTORY_DATABASE_PATH must be a clean absolute path",
		)
	}

	parsed, err := url.Parse(cfg.PublicBaseURL)
	if err != nil {
		return fmt.Errorf("invalid DIRECTORY_PUBLIC_BASE_URL: %w", err)
	}

	if parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" {
		return errors.New(
			"DIRECTORY_PUBLIC_BASE_URL must not contain credentials, query, or fragment",
		)
	}

	if parsed.Hostname() == "" {
		return errors.New("DIRECTORY_PUBLIC_BASE_URL must include a hostname")
	}

	switch parsed.Scheme {
	case "https":
	case "http":
		if !isLoopbackHost(parsed.Hostname()) {
			return errors.New(
				"DIRECTORY_PUBLIC_BASE_URL must use HTTPS outside loopback development",
			)
		}
	default:
		return errors.New("DIRECTORY_PUBLIC_BASE_URL must use HTTP or HTTPS")
	}

	if parsed.Path != "" && parsed.Path != "/" {
		return errors.New(
			"DIRECTORY_PUBLIC_BASE_URL must not include a path",
		)
	}

	if cfg.MaxRequestBodyBytes < 1024 ||
		cfg.MaxRequestBodyBytes > maxRequestBodyBytes {
		return fmt.Errorf(
			"DIRECTORY_MAX_REQUEST_BODY_BYTES must be between 1024 and %d",
			maxRequestBodyBytes,
		)
	}

	return nil
}

func envOrDefault(name string, fallback string) string {
	if value := strings.TrimSpace(os.Getenv(name)); value != "" {
		return value
	}
	return fallback
}

func isLoopbackHost(host string) bool {
	if strings.EqualFold(host, "localhost") {
		return true
	}

	address := net.ParseIP(host)
	return address != nil && address.IsLoopback()
}
