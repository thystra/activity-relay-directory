package config

import (
	"errors"
	"fmt"
	"net"
	"net/mail"
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
	defaultMailBackend         = "mail"
	defaultMailCommand         = "/usr/bin/mail"
	defaultMailTimeoutSeconds  = 30
	maximumMailTimeoutSeconds  = 300
	maximumAdministratorEmails = 8
)

// DatabaseGrowthConfig is the process-independent Tranche 17 storage budget
// and optional administrator notification configuration used by the service and
// local storage commands.
type DatabaseGrowthConfig struct {
	MaxBytes            int64
	WarningPercent      int
	AdministratorEmails []string
	MailBackend         string
	MailCommand         string
	MailTimeout         time.Duration
}

func (cfg DatabaseGrowthConfig) EmailEnabled() bool {
	return len(cfg.AdministratorEmails) > 0
}

// Config is the directory service's process configuration.
type Config struct {
	ListenAddress         string
	PublicBaseURL         string
	DatabasePath          string
	LifecycleEnabled      bool
	PublicListingEnabled  bool
	SoftPruningEnabled    bool
	SoftPruningInterval   time.Duration
	InactiveRetentionDays int
	DatabaseGrowth        DatabaseGrowthConfig
	MaxRequestBodyBytes   int64
	TrustedProxyPrefixes  []netip.Prefix
}

// Load reads configuration from the process environment.
func Load() (Config, error) {
	cfg := Config{
		ListenAddress:         envOrDefault("DIRECTORY_LISTEN_ADDRESS", defaultListenAddress),
		PublicBaseURL:         strings.TrimSpace(os.Getenv("DIRECTORY_PUBLIC_BASE_URL")),
		DatabasePath:          strings.TrimSpace(os.Getenv("DIRECTORY_DATABASE_PATH")),
		LifecycleEnabled:      false,
		PublicListingEnabled:  false,
		SoftPruningEnabled:    false,
		SoftPruningInterval:   storage.DefaultSoftPruningInterval,
		InactiveRetentionDays: 0,
		DatabaseGrowth:        defaultDatabaseGrowthConfig(),
		MaxRequestBodyBytes:   defaultMaxRequestBodyBytes,
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

	if raw := strings.TrimSpace(os.Getenv("DIRECTORY_PUBLIC_LISTING_ENABLED")); raw != "" {
		value, err := strconv.ParseBool(raw)
		if err != nil {
			return Config{}, fmt.Errorf(
				"DIRECTORY_PUBLIC_LISTING_ENABLED must be a boolean: %w",
				err,
			)
		}
		cfg.PublicListingEnabled = value
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

	retentionDays, err := parseInactiveRetentionDays(os.Getenv("DIRECTORY_INACTIVE_RETENTION_DAYS"))
	if err != nil {
		return Config{}, err
	}
	cfg.InactiveRetentionDays = retentionDays

	growth, err := loadDatabaseGrowthConfig()
	if err != nil {
		return Config{}, err
	}
	cfg.DatabaseGrowth = growth

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

// LoadInactiveRetentionDays reads only the hard-retention policy required by
// local retention commands. It does not require public HTTP configuration.
func LoadInactiveRetentionDays() (int, error) {
	return parseInactiveRetentionDays(os.Getenv("DIRECTORY_INACTIVE_RETENTION_DAYS"))
}

// LoadDatabaseGrowthConfig reads the Tranche 17 storage budget and optional
// local-command notification settings without requiring HTTP configuration.
func LoadDatabaseGrowthConfig() (DatabaseGrowthConfig, error) {
	return loadDatabaseGrowthConfig()
}

func defaultDatabaseGrowthConfig() DatabaseGrowthConfig {
	return DatabaseGrowthConfig{
		MaxBytes:       storage.DefaultDatabaseMaxBytes,
		WarningPercent: storage.DefaultDatabaseWarningPercent,
		MailBackend:    defaultMailBackend,
		MailCommand:    defaultMailCommand,
		MailTimeout:    defaultMailTimeoutSeconds * time.Second,
	}
}

func loadDatabaseGrowthConfig() (DatabaseGrowthConfig, error) {
	cfg := defaultDatabaseGrowthConfig()

	if raw := os.Getenv("DIRECTORY_DATABASE_MAX_BYTES"); raw != "" {
		value, err := parseStrictPositiveInt64(raw, storage.MaximumDatabaseMaxBytes)
		if err != nil {
			return DatabaseGrowthConfig{}, fmt.Errorf("DIRECTORY_DATABASE_MAX_BYTES is invalid: %w", err)
		}
		cfg.MaxBytes = value
	}
	if raw := os.Getenv("DIRECTORY_DATABASE_WARNING_PERCENT"); raw != "" {
		value, err := parseStrictPositiveInt64(raw, storage.DatabaseCriticalPercent-1)
		if err != nil {
			return DatabaseGrowthConfig{}, fmt.Errorf("DIRECTORY_DATABASE_WARNING_PERCENT is invalid: %w", err)
		}
		cfg.WarningPercent = int(value)
	}

	emails, err := parseAdministratorEmails(os.Getenv("DIRECTORY_ADMIN_EMAIL"))
	if err != nil {
		return DatabaseGrowthConfig{}, err
	}
	cfg.AdministratorEmails = emails

	if raw := os.Getenv("DIRECTORY_MAIL_BACKEND"); raw != "" {
		cfg.MailBackend = raw
	}
	if raw := os.Getenv("DIRECTORY_MAIL_COMMAND"); raw != "" {
		cfg.MailCommand = raw
	}
	if raw := os.Getenv("DIRECTORY_MAIL_TIMEOUT_SECONDS"); raw != "" {
		value, err := parseStrictPositiveInt64(raw, maximumMailTimeoutSeconds)
		if err != nil {
			return DatabaseGrowthConfig{}, fmt.Errorf("DIRECTORY_MAIL_TIMEOUT_SECONDS is invalid: %w", err)
		}
		cfg.MailTimeout = time.Duration(value) * time.Second
	}
	if err := cfg.Validate(); err != nil {
		return DatabaseGrowthConfig{}, err
	}
	return cfg, nil
}

func (cfg DatabaseGrowthConfig) Validate() error {
	if cfg.MaxBytes <= 0 || cfg.MaxBytes > storage.MaximumDatabaseMaxBytes {
		return fmt.Errorf(
			"DIRECTORY_DATABASE_MAX_BYTES must be between 1 and %d",
			storage.MaximumDatabaseMaxBytes,
		)
	}
	if cfg.WarningPercent <= 0 || cfg.WarningPercent >= storage.DatabaseCriticalPercent {
		return fmt.Errorf(
			"DIRECTORY_DATABASE_WARNING_PERCENT must be between 1 and %d",
			storage.DatabaseCriticalPercent-1,
		)
	}
	if len(cfg.AdministratorEmails) > maximumAdministratorEmails {
		return errors.New("too many DIRECTORY_ADMIN_EMAIL recipients")
	}
	for _, recipient := range cfg.AdministratorEmails {
		if !validAdministratorEmail(recipient) {
			return errors.New("DIRECTORY_ADMIN_EMAIL contains an invalid recipient")
		}
	}
	if cfg.MailBackend != defaultMailBackend {
		return errors.New("DIRECTORY_MAIL_BACKEND must be mail")
	}
	if !filepath.IsAbs(cfg.MailCommand) || filepath.Clean(cfg.MailCommand) != cfg.MailCommand ||
		strings.ContainsRune(cfg.MailCommand, '\x00') || containsControl(cfg.MailCommand) {
		return errors.New("DIRECTORY_MAIL_COMMAND must be a clean absolute path")
	}
	if cfg.MailTimeout <= 0 || cfg.MailTimeout > maximumMailTimeoutSeconds*time.Second {
		return fmt.Errorf(
			"DIRECTORY_MAIL_TIMEOUT_SECONDS must be between 1 and %d",
			maximumMailTimeoutSeconds,
		)
	}
	return nil
}

func parseStrictPositiveInt64(raw string, maximum int64) (int64, error) {
	if raw == "" || strings.HasPrefix(raw, "+") || (len(raw) > 1 && raw[0] == '0') {
		return 0, errors.New("must be a canonical positive integer")
	}
	value, err := strconv.ParseInt(raw, 10, 64)
	if err != nil || value <= 0 || value > maximum || strconv.FormatInt(value, 10) != raw {
		return 0, errors.New("must be a canonical positive bounded integer")
	}
	return value, nil
}

func parseAdministratorEmails(raw string) ([]string, error) {
	if raw == "" {
		return nil, nil
	}
	parts := strings.Split(raw, ",")
	if len(parts) > maximumAdministratorEmails {
		return nil, errors.New("too many DIRECTORY_ADMIN_EMAIL recipients")
	}
	result := make([]string, 0, len(parts))
	seen := make(map[string]struct{}, len(parts))
	for _, value := range parts {
		if !validAdministratorEmail(value) {
			return nil, errors.New("DIRECTORY_ADMIN_EMAIL contains an invalid recipient")
		}
		if _, exists := seen[value]; exists {
			return nil, errors.New("DIRECTORY_ADMIN_EMAIL contains a duplicate recipient")
		}
		seen[value] = struct{}{}
		result = append(result, value)
	}
	return result, nil
}

func validAdministratorEmail(value string) bool {
	if value == "" || len(value) > 254 || strings.HasPrefix(value, "-") ||
		containsControl(value) || strings.ContainsAny(value, " 	\r\n") {
		return false
	}
	parsed, err := mail.ParseAddress(value)
	return err == nil && parsed.Name == "" && parsed.Address == value
}

func containsControl(value string) bool {
	for _, r := range value {
		if r < 0x20 || r == 0x7f {
			return true
		}
	}
	return false
}

func parseInactiveRetentionDays(raw string) (int, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return 0, nil
	}
	value, err := strconv.ParseUint(raw, 10, 32)
	if err != nil || strconv.FormatUint(value, 10) != raw || value > uint64(storage.MaximumInactiveRetentionDays) {
		return 0, fmt.Errorf(
			"DIRECTORY_INACTIVE_RETENTION_DAYS must be an integer between 0 and %d",
			storage.MaximumInactiveRetentionDays,
		)
	}
	return int(value), nil
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
	if cfg.InactiveRetentionDays < 0 || cfg.InactiveRetentionDays > storage.MaximumInactiveRetentionDays {
		return fmt.Errorf(
			"DIRECTORY_INACTIVE_RETENTION_DAYS must be between 0 and %d",
			storage.MaximumInactiveRetentionDays,
		)
	}

	growthConfig := cfg.DatabaseGrowth
	if growthConfig.MaxBytes == 0 &&
		growthConfig.WarningPercent == 0 &&
		len(growthConfig.AdministratorEmails) == 0 &&
		growthConfig.MailBackend == "" &&
		growthConfig.MailCommand == "" &&
		growthConfig.MailTimeout == 0 {
		growthConfig = defaultDatabaseGrowthConfig()
	}
	if err := growthConfig.Validate(); err != nil {
		return err
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
