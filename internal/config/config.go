package config

import (
	"errors"
	"fmt"
	"net"
	"net/netip"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/thystra/activity-relay-directory/internal/storage"
)

const (
	defaultListenAddress       = "127.0.0.1:8080"
	defaultMaxRequestBodyBytes = int64(64 * 1024)
	maxRequestBodyBytes        = int64(1024 * 1024)
	maximumTrustedProxies      = 32
)

// Config is the directory service's process configuration.
type Config struct {
	ListenAddress        string
	PublicBaseURL        string
	DatabasePath         string
	LifecycleEnabled     bool
	SoftPruningEnabled   bool
	SoftPruningInterval  time.Duration
	MaxRequestBodyBytes  int64
	TrustedProxyPrefixes []netip.Prefix
}

// Load reads configuration from the process environment.
func Load() (Config, error) {
	cfg := Config{
		ListenAddress:       envOrDefault("DIRECTORY_LISTEN_ADDRESS", defaultListenAddress),
		PublicBaseURL:       strings.TrimSpace(os.Getenv("DIRECTORY_PUBLIC_BASE_URL")),
		DatabasePath:        strings.TrimSpace(os.Getenv("DIRECTORY_DATABASE_PATH")),
		LifecycleEnabled:    false,
		SoftPruningEnabled:  false,
		SoftPruningInterval: storage.DefaultSoftPruningInterval,
		MaxRequestBodyBytes: defaultMaxRequestBodyBytes,
	}

	if strings.TrimSpace(os.Getenv("DIRECTORY_REGISTRATION_ENABLED")) != "" {
		return Config{}, errors.New(
			"DIRECTORY_REGISTRATION_ENABLED was renamed to DIRECTORY_LIFECYCLE_ENABLED",
		)
	}
	if raw := strings.TrimSpace(os.Getenv("DIRECTORY_LIFECYCLE_ENABLED")); raw != "" {
		value, err := strconv.ParseBool(raw)
		if err != nil {
			return Config{}, fmt.Errorf(
				"DIRECTORY_LIFECYCLE_ENABLED must be a boolean: %w",
				err,
			)
		}
		cfg.LifecycleEnabled = value
	}

	if raw := strings.TrimSpace(os.Getenv("DIRECTORY_SOFT_PRUNING_ENABLED")); raw != "" {
		value, err := strconv.ParseBool(raw)
		if err != nil {
			return Config{}, fmt.Errorf(
				"DIRECTORY_SOFT_PRUNING_ENABLED must be a boolean: %w",
				err,
			)
		}
		cfg.SoftPruningEnabled = value
	}
	if raw := strings.TrimSpace(os.Getenv("DIRECTORY_SOFT_PRUNING_INTERVAL")); raw != "" {
		value, err := time.ParseDuration(raw)
		if err != nil {
			return Config{}, fmt.Errorf(
				"DIRECTORY_SOFT_PRUNING_INTERVAL must be a duration: %w",
				err,
			)
		}
		cfg.SoftPruningInterval = value
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

	trustedProxyPrefixes, err := parseTrustedProxyPrefixes(
		os.Getenv("DIRECTORY_TRUSTED_PROXY_PREFIXES"),
	)
	if err != nil {
		return Config{}, err
	}
	cfg.TrustedProxyPrefixes = trustedProxyPrefixes

	if err := cfg.Validate(); err != nil {
		return Config{}, err
	}

	return cfg, nil
}

// LoadDatabasePath reads and validates the only process setting required by
// local administrative commands. It deliberately does not require a public
// URL or listener configuration.
func LoadDatabasePath() (string, error) {
	path := strings.TrimSpace(os.Getenv("DIRECTORY_DATABASE_PATH"))
	if err := ValidateDatabasePath(path); err != nil {
		return "", err
	}
	return path, nil
}

// ValidateDatabasePath applies the shared local SQLite path policy.
func ValidateDatabasePath(path string) error {
	if path == "" {
		return errors.New("DIRECTORY_DATABASE_PATH is required")
	}
	if !filepath.IsAbs(path) ||
		filepath.Clean(path) != path ||
		strings.ContainsRune(path, '\x00') {
		return errors.New(
			"DIRECTORY_DATABASE_PATH must be a clean absolute path",
		)
	}
	return nil
}

// Validate checks security-sensitive and operational configuration.
func (cfg Config) Validate() error {
	if _, err := net.ResolveTCPAddr("tcp", cfg.ListenAddress); err != nil {
		return fmt.Errorf("invalid DIRECTORY_LISTEN_ADDRESS: %w", err)
	}

	if cfg.PublicBaseURL == "" {
		return errors.New("DIRECTORY_PUBLIC_BASE_URL is required")
	}
	if err := ValidateDatabasePath(cfg.DatabasePath); err != nil {
		return err
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
	if cfg.LifecycleEnabled && parsed.Scheme != "https" {
		return errors.New(
			"DIRECTORY_LIFECYCLE_ENABLED requires an HTTPS public base URL",
		)
	}

	if parsed.Path != "" && parsed.Path != "/" {
		return errors.New(
			"DIRECTORY_PUBLIC_BASE_URL must not include a path",
		)
	}

	if cfg.SoftPruningInterval != 0 &&
		cfg.SoftPruningInterval < storage.MinimumSoftPruningInterval {
		return fmt.Errorf(
			"DIRECTORY_SOFT_PRUNING_INTERVAL must be at least %s",
			storage.MinimumSoftPruningInterval,
		)
	}
	if cfg.SoftPruningEnabled && cfg.SoftPruningInterval == 0 {
		return errors.New("DIRECTORY_SOFT_PRUNING_INTERVAL is required when soft pruning is enabled")
	}

	if cfg.MaxRequestBodyBytes < 1024 ||
		cfg.MaxRequestBodyBytes > maxRequestBodyBytes {
		return fmt.Errorf(
			"DIRECTORY_MAX_REQUEST_BODY_BYTES must be between 1024 and %d",
			maxRequestBodyBytes,
		)
	}

	if len(cfg.TrustedProxyPrefixes) > maximumTrustedProxies {
		return errors.New("too many DIRECTORY_TRUSTED_PROXY_PREFIXES")
	}
	seenPrefixes := make(map[netip.Prefix]struct{}, len(cfg.TrustedProxyPrefixes))
	for _, prefix := range cfg.TrustedProxyPrefixes {
		if !validTrustedProxyPrefix(prefix) {
			return errors.New("DIRECTORY_TRUSTED_PROXY_PREFIXES is invalid")
		}
		if _, duplicate := seenPrefixes[prefix]; duplicate {
			return errors.New("DIRECTORY_TRUSTED_PROXY_PREFIXES contains a duplicate")
		}
		seenPrefixes[prefix] = struct{}{}
	}

	return nil
}

func parseTrustedProxyPrefixes(value string) ([]netip.Prefix, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		return nil, nil
	}
	parts := strings.Split(value, ",")
	if len(parts) > maximumTrustedProxies {
		return nil, errors.New("too many DIRECTORY_TRUSTED_PROXY_PREFIXES")
	}
	prefixes := make([]netip.Prefix, 0, len(parts))
	for _, part := range parts {
		part = strings.TrimSpace(part)
		prefix, err := netip.ParsePrefix(part)
		if err != nil || !validTrustedProxyPrefix(prefix) {
			return nil, errors.New("DIRECTORY_TRUSTED_PROXY_PREFIXES is invalid")
		}
		prefixes = append(prefixes, prefix)
	}
	return prefixes, nil
}

func validTrustedProxyPrefix(prefix netip.Prefix) bool {
	if !prefix.IsValid() || prefix.Addr().Is4In6() || prefix != prefix.Masked() {
		return false
	}
	address := prefix.Addr()
	return !address.IsUnspecified() && !address.IsMulticast()
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
