package config

import (
	"fmt"
	"net/netip"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/thystra/activity-relay-directory/internal/storage"
)

func TestLoadDefaultsLifecycleDisabled(t *testing.T) {
	setRequiredEnvironment(t)
	t.Setenv("DIRECTORY_PUBLIC_BASE_URL", "https://directory.example")
	t.Setenv("DIRECTORY_LISTEN_ADDRESS", "")
	t.Setenv("DIRECTORY_REGISTRATION_ENABLED", "")
	t.Setenv("DIRECTORY_LIFECYCLE_ENABLED", "")
	t.Setenv("DIRECTORY_MAX_REQUEST_BODY_BYTES", "")
	t.Setenv("DIRECTORY_TRUSTED_PROXY_PREFIXES", "")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}

	if cfg.ListenAddress != defaultListenAddress {
		t.Fatalf("ListenAddress = %q", cfg.ListenAddress)
	}

	if cfg.LifecycleEnabled {
		t.Fatal("lifecycle graph must default to disabled")
	}

	if cfg.MaxRequestBodyBytes != defaultMaxRequestBodyBytes {
		t.Fatalf("MaxRequestBodyBytes = %d", cfg.MaxRequestBodyBytes)
	}
	if len(cfg.TrustedProxyPrefixes) != 0 {
		t.Fatalf("TrustedProxyPrefixes = %#v", cfg.TrustedProxyPrefixes)
	}
}

func TestLoadDefaultsPublicListingDisabled(t *testing.T) {
	setRequiredEnvironment(t)
	t.Setenv("DIRECTORY_PUBLIC_BASE_URL", "https://directory.example")
	t.Setenv("DIRECTORY_PUBLIC_LISTING_ENABLED", "")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if cfg.PublicListingEnabled {
		t.Fatal("PublicListingEnabled = true")
	}
}

func TestLoadParsesPublicListingEnabled(t *testing.T) {
	setRequiredEnvironment(t)
	t.Setenv("DIRECTORY_PUBLIC_BASE_URL", "https://directory.example")
	t.Setenv("DIRECTORY_PUBLIC_LISTING_ENABLED", "true")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if !cfg.PublicListingEnabled {
		t.Fatal("PublicListingEnabled = false")
	}
}

func TestLoadRejectsInvalidPublicListingFlag(t *testing.T) {
	setRequiredEnvironment(t)
	t.Setenv("DIRECTORY_PUBLIC_BASE_URL", "https://directory.example")
	t.Setenv("DIRECTORY_PUBLIC_LISTING_ENABLED", "sometimes")

	if _, err := Load(); err == nil || !strings.Contains(err.Error(), "PUBLIC_LISTING_ENABLED") {
		t.Fatalf("Load() error = %v", err)
	}
}

func TestLoadDefaultsSoftPruningDisabled(t *testing.T) {
	setRequiredEnvironment(t)
	t.Setenv("DIRECTORY_PUBLIC_BASE_URL", "https://directory.example")
	t.Setenv("DIRECTORY_PUBLIC_LISTING_ENABLED", "")
	t.Setenv("DIRECTORY_SOFT_PRUNING_ENABLED", "")
	t.Setenv("DIRECTORY_SOFT_PRUNING_INTERVAL", "")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if cfg.SoftPruningEnabled {
		t.Fatal("SoftPruningEnabled = true")
	}
	if cfg.SoftPruningInterval != storage.DefaultSoftPruningInterval {
		t.Fatalf("SoftPruningInterval = %s", cfg.SoftPruningInterval)
	}
}

func TestLoadParsesBoundedSoftPruningConfiguration(t *testing.T) {
	setRequiredEnvironment(t)
	t.Setenv("DIRECTORY_PUBLIC_BASE_URL", "https://directory.example")
	t.Setenv("DIRECTORY_SOFT_PRUNING_ENABLED", "true")
	t.Setenv("DIRECTORY_SOFT_PRUNING_INTERVAL", "6h")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if !cfg.SoftPruningEnabled || cfg.SoftPruningInterval != 6*time.Hour {
		t.Fatalf("soft-pruning configuration = enabled:%t interval:%s", cfg.SoftPruningEnabled, cfg.SoftPruningInterval)
	}
}

func TestLoadRejectsInvalidSoftPruningConfiguration(t *testing.T) {
	for name, values := range map[string][2]string{
		"invalid boolean":  {"sometimes", "24h"},
		"invalid duration": {"true", "daily"},
		"below minimum":    {"true", "59m59s"},
	} {
		t.Run(name, func(t *testing.T) {
			setRequiredEnvironment(t)
			t.Setenv("DIRECTORY_PUBLIC_BASE_URL", "https://directory.example")
			t.Setenv("DIRECTORY_SOFT_PRUNING_ENABLED", values[0])
			t.Setenv("DIRECTORY_SOFT_PRUNING_INTERVAL", values[1])
			if _, err := Load(); err == nil {
				t.Fatalf("Load() accepted enabled=%q interval=%q", values[0], values[1])
			}
		})
	}
}

func TestValidateRequiresIntervalWhenSoftPruningEnabled(t *testing.T) {
	cfg := Config{
		ListenAddress:       "127.0.0.1:8080",
		PublicBaseURL:       "https://directory.example",
		DatabasePath:        filepath.Join(t.TempDir(), "directory.sqlite"),
		SoftPruningEnabled:  true,
		SoftPruningInterval: 0,
		MaxRequestBodyBytes: defaultMaxRequestBodyBytes,
	}
	if err := cfg.Validate(); err == nil || !strings.Contains(err.Error(), "SOFT_PRUNING_INTERVAL") {
		t.Fatalf("Validate() error = %v", err)
	}
}

func TestLoadAllowsLoopbackHTTP(t *testing.T) {
	setRequiredEnvironment(t)
	t.Setenv("DIRECTORY_PUBLIC_BASE_URL", "http://127.0.0.1:8080")

	if _, err := Load(); err != nil {
		t.Fatalf("Load() error = %v", err)
	}
}

func TestLoadRejectsNonLoopbackHTTP(t *testing.T) {
	setRequiredEnvironment(t)
	t.Setenv("DIRECTORY_PUBLIC_BASE_URL", "http://directory.example")

	_, err := Load()
	if err == nil || !strings.Contains(err.Error(), "must use HTTPS") {
		t.Fatalf("Load() error = %v", err)
	}
}

func TestLoadRejectsLifecycleGarbage(t *testing.T) {
	setRequiredEnvironment(t)
	t.Setenv("DIRECTORY_PUBLIC_BASE_URL", "https://directory.example")
	t.Setenv("DIRECTORY_LIFECYCLE_ENABLED", "sometimes")

	if _, err := Load(); err == nil {
		t.Fatal("Load() unexpectedly succeeded")
	}
}

func TestLoadExplicitlyEnablesHTTPSLifecycleGraph(t *testing.T) {
	setRequiredEnvironment(t)
	t.Setenv("DIRECTORY_PUBLIC_BASE_URL", "https://directory.example")
	t.Setenv("DIRECTORY_LIFECYCLE_ENABLED", "true")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if !cfg.LifecycleEnabled {
		t.Fatal("LifecycleEnabled = false")
	}
}

func TestLoadRejectsRetiredRegistrationFlag(t *testing.T) {
	setRequiredEnvironment(t)
	t.Setenv("DIRECTORY_REGISTRATION_ENABLED", "true")

	_, err := Load()
	if err == nil || !strings.Contains(err.Error(), "renamed") {
		t.Fatalf("Load() error = %v", err)
	}
}

func TestLoadParsesCanonicalTrustedProxyPrefixes(t *testing.T) {
	setRequiredEnvironment(t)
	t.Setenv("DIRECTORY_PUBLIC_BASE_URL", "https://directory.example")
	t.Setenv(
		"DIRECTORY_TRUSTED_PROXY_PREFIXES",
		"127.0.0.1/32, ::1/128, 192.0.2.10/32",
	)

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	want := []netip.Prefix{
		netip.MustParsePrefix("127.0.0.1/32"),
		netip.MustParsePrefix("::1/128"),
		netip.MustParsePrefix("192.0.2.10/32"),
	}
	if len(cfg.TrustedProxyPrefixes) != len(want) {
		t.Fatalf("TrustedProxyPrefixes = %#v", cfg.TrustedProxyPrefixes)
	}
	for index := range want {
		if cfg.TrustedProxyPrefixes[index] != want[index] {
			t.Fatalf("TrustedProxyPrefixes = %#v, want %#v", cfg.TrustedProxyPrefixes, want)
		}
	}
}

func TestLoadRejectsInvalidTrustedProxyPrefixes(t *testing.T) {
	for _, value := range []string{
		"127.0.0.1",
		"127.0.0.1/24",
		"::ffff:127.0.0.1/128",
		"0.0.0.0/0",
		"ff00::/8",
		"127.0.0.1/32,127.0.0.1/32",
		"127.0.0.1/32,",
	} {
		t.Run(value, func(t *testing.T) {
			setRequiredEnvironment(t)
			t.Setenv("DIRECTORY_PUBLIC_BASE_URL", "https://directory.example")
			t.Setenv("DIRECTORY_TRUSTED_PROXY_PREFIXES", value)
			if _, err := Load(); err == nil {
				t.Fatalf("Load() accepted %q", value)
			}
		})
	}
}

func TestValidateRequiresHTTPSWhenLifecycleEnabled(t *testing.T) {
	cfg := Config{
		ListenAddress:        "127.0.0.1:8080",
		PublicBaseURL:        "http://127.0.0.1:8080",
		DatabasePath:         filepath.Join(t.TempDir(), "directory.sqlite"),
		LifecycleEnabled:     true,
		MaxRequestBodyBytes:  defaultMaxRequestBodyBytes,
		TrustedProxyPrefixes: nil,
	}

	if err := cfg.Validate(); err == nil || !strings.Contains(err.Error(), "HTTPS") {
		t.Fatalf("Validate() error = %v", err)
	}
}

func TestValidateRejectsBaseURLPath(t *testing.T) {
	cfg := Config{
		ListenAddress:       "127.0.0.1:8080",
		PublicBaseURL:       "https://directory.example/path",
		DatabasePath:        filepath.Join(t.TempDir(), "directory.sqlite"),
		MaxRequestBodyBytes: defaultMaxRequestBodyBytes,
	}

	if err := cfg.Validate(); err == nil {
		t.Fatal("Validate() unexpectedly succeeded")
	}
}

func TestValidateRejectsOversizedBodyLimit(t *testing.T) {
	cfg := Config{
		ListenAddress:       "127.0.0.1:8080",
		PublicBaseURL:       "https://directory.example",
		DatabasePath:        filepath.Join(t.TempDir(), "directory.sqlite"),
		MaxRequestBodyBytes: maxRequestBodyBytes + 1,
	}

	if err := cfg.Validate(); err == nil {
		t.Fatal("Validate() unexpectedly succeeded")
	}
}

func TestLoadRequiresDatabasePath(t *testing.T) {
	t.Setenv("DIRECTORY_PUBLIC_BASE_URL", "https://directory.example")
	t.Setenv("DIRECTORY_DATABASE_PATH", "")

	_, err := Load()
	if err == nil || !strings.Contains(err.Error(), "DIRECTORY_DATABASE_PATH") {
		t.Fatalf("Load() error = %v", err)
	}
}

func TestValidateRejectsRelativeOrUncleanDatabasePath(t *testing.T) {
	unclean := filepath.Join(t.TempDir(), "data") +
		string(filepath.Separator) + ".." +
		string(filepath.Separator) + "directory.sqlite"
	for _, path := range []string{
		"directory.sqlite",
		unclean,
	} {
		cfg := Config{
			ListenAddress:       "127.0.0.1:8080",
			PublicBaseURL:       "https://directory.example",
			DatabasePath:        path,
			MaxRequestBodyBytes: defaultMaxRequestBodyBytes,
		}
		if err := cfg.Validate(); err == nil {
			t.Fatalf("Validate() accepted database path %q", path)
		}
	}
}

func setRequiredEnvironment(t *testing.T) {
	t.Helper()
	t.Setenv("DIRECTORY_PUBLIC_LISTING_ENABLED", "")
	t.Setenv("DIRECTORY_SOFT_PRUNING_ENABLED", "")
	t.Setenv("DIRECTORY_SOFT_PRUNING_INTERVAL", "")
	t.Setenv(
		"DIRECTORY_DATABASE_PATH",
		filepath.Join(t.TempDir(), "directory.sqlite"),
	)
}

func TestLoadDefaultsInactiveRetentionToIndefinite(t *testing.T) {
	setRequiredEnvironment(t)
	t.Setenv("DIRECTORY_PUBLIC_BASE_URL", "https://directory.example")
	t.Setenv("DIRECTORY_INACTIVE_RETENTION_DAYS", "")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if cfg.InactiveRetentionDays != 0 {
		t.Fatalf("InactiveRetentionDays = %d, want 0", cfg.InactiveRetentionDays)
	}
}

func TestLoadParsesInactiveRetentionDays(t *testing.T) {
	for _, raw := range []string{"0", "1", "365", "36500"} {
		t.Run(raw, func(t *testing.T) {
			setRequiredEnvironment(t)
			t.Setenv("DIRECTORY_PUBLIC_BASE_URL", "https://directory.example")
			t.Setenv("DIRECTORY_INACTIVE_RETENTION_DAYS", raw)
			cfg, err := Load()
			if err != nil {
				t.Fatalf("Load() error = %v", err)
			}
			want, _ := strconv.Atoi(raw)
			if cfg.InactiveRetentionDays != want {
				t.Fatalf("InactiveRetentionDays = %d, want %d", cfg.InactiveRetentionDays, want)
			}
		})
	}
}

func TestLoadRejectsInvalidInactiveRetentionDays(t *testing.T) {
	for _, raw := range []string{"-1", "1.5", "+1", "01", "36501", "4294967296", "not-days"} {
		t.Run(raw, func(t *testing.T) {
			setRequiredEnvironment(t)
			t.Setenv("DIRECTORY_PUBLIC_BASE_URL", "https://directory.example")
			t.Setenv("DIRECTORY_INACTIVE_RETENTION_DAYS", raw)
			if _, err := Load(); err == nil || !strings.Contains(err.Error(), "INACTIVE_RETENTION_DAYS") {
				t.Fatalf("Load() error = %v", err)
			}
		})
	}
}

func TestLoadInactiveRetentionDaysNeedsNoPublicConfiguration(t *testing.T) {
	t.Setenv("DIRECTORY_INACTIVE_RETENTION_DAYS", "365")
	got, err := LoadInactiveRetentionDays()
	if err != nil || got != 365 {
		t.Fatalf("LoadInactiveRetentionDays() = (%d, %v), want (365, nil)", got, err)
	}
}

func TestLoadDefaultsDatabaseGrowthGuardAndDisablesEmail(t *testing.T) {
	setRequiredEnvironment(t)
	clearDatabaseGrowthEnvironment(t)
	t.Setenv("DIRECTORY_PUBLIC_BASE_URL", "https://directory.example")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	growth := cfg.DatabaseGrowth
	if growth.MaxBytes != storage.DefaultDatabaseMaxBytes ||
		growth.WarningPercent != storage.DefaultDatabaseWarningPercent ||
		growth.EmailEnabled() || len(growth.AdministratorEmails) != 0 ||
		growth.MailBackend != defaultMailBackend ||
		growth.MailCommand != defaultMailCommand ||
		growth.MailTimeout != defaultMailTimeoutSeconds*time.Second {
		t.Fatalf("DatabaseGrowth defaults = %#v", growth)
	}
}

func TestLoadParsesDatabaseGrowthAndMailerSettings(t *testing.T) {
	setRequiredEnvironment(t)
	clearDatabaseGrowthEnvironment(t)
	t.Setenv("DIRECTORY_PUBLIC_BASE_URL", "https://directory.example")
	t.Setenv("DIRECTORY_DATABASE_MAX_BYTES", "2147483648")
	t.Setenv("DIRECTORY_DATABASE_WARNING_PERCENT", "80")
	t.Setenv("DIRECTORY_ADMIN_EMAIL", "admin@example.com,ops@example.com")
	t.Setenv("DIRECTORY_MAIL_BACKEND", "mail")
	t.Setenv("DIRECTORY_MAIL_COMMAND", "/opt/directory/bin/mail")
	t.Setenv("DIRECTORY_MAIL_TIMEOUT_SECONDS", "45")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	growth := cfg.DatabaseGrowth
	if growth.MaxBytes != 2147483648 || growth.WarningPercent != 80 ||
		!growth.EmailEnabled() || growth.MailBackend != "mail" ||
		growth.MailCommand != "/opt/directory/bin/mail" || growth.MailTimeout != 45*time.Second {
		t.Fatalf("DatabaseGrowth = %#v", growth)
	}
	want := []string{"admin@example.com", "ops@example.com"}
	if !reflect.DeepEqual(growth.AdministratorEmails, want) {
		t.Fatalf("AdministratorEmails = %#v, want %#v", growth.AdministratorEmails, want)
	}
}

func TestLoadDatabaseGrowthConfigNeedsNoPublicConfiguration(t *testing.T) {
	clearDatabaseGrowthEnvironment(t)
	t.Setenv("DIRECTORY_DATABASE_MAX_BYTES", "1073741824")
	growth, err := LoadDatabaseGrowthConfig()
	if err != nil {
		t.Fatalf("LoadDatabaseGrowthConfig() error = %v", err)
	}
	if growth.MaxBytes != storage.DefaultDatabaseMaxBytes || growth.EmailEnabled() {
		t.Fatalf("DatabaseGrowth = %#v", growth)
	}
}

func TestLoadRejectsInvalidDatabaseGrowthSettings(t *testing.T) {
	tests := []struct {
		name  string
		key   string
		value string
	}{
		{name: "zero max", key: "DIRECTORY_DATABASE_MAX_BYTES", value: "0"},
		{name: "negative max", key: "DIRECTORY_DATABASE_MAX_BYTES", value: "-1"},
		{name: "plus max", key: "DIRECTORY_DATABASE_MAX_BYTES", value: "+1073741824"},
		{name: "leading zero max", key: "DIRECTORY_DATABASE_MAX_BYTES", value: "01073741824"},
		{name: "whitespace max", key: "DIRECTORY_DATABASE_MAX_BYTES", value: " 1073741824"},
		{name: "oversized max", key: "DIRECTORY_DATABASE_MAX_BYTES", value: "1099511627777"},
		{name: "zero warning", key: "DIRECTORY_DATABASE_WARNING_PERCENT", value: "0"},
		{name: "critical warning", key: "DIRECTORY_DATABASE_WARNING_PERCENT", value: "90"},
		{name: "leading zero warning", key: "DIRECTORY_DATABASE_WARNING_PERCENT", value: "075"},
		{name: "whitespace warning", key: "DIRECTORY_DATABASE_WARNING_PERCENT", value: "75 "},
		{name: "display name recipient", key: "DIRECTORY_ADMIN_EMAIL", value: "Admin <admin@example.com>"},
		{name: "option recipient", key: "DIRECTORY_ADMIN_EMAIL", value: "-x@example.com"},
		{name: "duplicate recipient", key: "DIRECTORY_ADMIN_EMAIL", value: "admin@example.com,admin@example.com"},
		{name: "recipient whitespace", key: "DIRECTORY_ADMIN_EMAIL", value: "admin@example.com, ops@example.com"},
		{name: "control recipient", key: "DIRECTORY_ADMIN_EMAIL", value: "admin@example.com\n-bcc@example.com"},
		{name: "unsupported backend", key: "DIRECTORY_MAIL_BACKEND", value: "smtp"},
		{name: "backend whitespace", key: "DIRECTORY_MAIL_BACKEND", value: "mail "},
		{name: "relative command", key: "DIRECTORY_MAIL_COMMAND", value: "usr/bin/mail"},
		{name: "unclean command", key: "DIRECTORY_MAIL_COMMAND", value: "/usr/bin/../bin/mail"},
		{name: "zero timeout", key: "DIRECTORY_MAIL_TIMEOUT_SECONDS", value: "0"},
		{name: "oversized timeout", key: "DIRECTORY_MAIL_TIMEOUT_SECONDS", value: "301"},
		{name: "timeout whitespace", key: "DIRECTORY_MAIL_TIMEOUT_SECONDS", value: "30 "},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			setRequiredEnvironment(t)
			clearDatabaseGrowthEnvironment(t)
			t.Setenv("DIRECTORY_PUBLIC_BASE_URL", "https://directory.example")
			t.Setenv(test.key, test.value)
			if _, err := Load(); err == nil {
				t.Fatalf("Load() accepted %s=%q", test.key, test.value)
			}
		})
	}
}

func TestLoadRejectsTooManyAdministratorEmailRecipients(t *testing.T) {
	setRequiredEnvironment(t)
	clearDatabaseGrowthEnvironment(t)
	t.Setenv("DIRECTORY_PUBLIC_BASE_URL", "https://directory.example")
	var recipients []string
	for index := 0; index < maximumAdministratorEmails+1; index++ {
		recipients = append(recipients, fmt.Sprintf("admin%d@example.com", index))
	}
	t.Setenv("DIRECTORY_ADMIN_EMAIL", strings.Join(recipients, ","))
	if _, err := Load(); err == nil {
		t.Fatal("Load() accepted too many administrator recipients")
	}
}

func clearDatabaseGrowthEnvironment(t *testing.T) {
	t.Helper()
	for _, name := range []string{
		"DIRECTORY_DATABASE_MAX_BYTES",
		"DIRECTORY_DATABASE_WARNING_PERCENT",
		"DIRECTORY_ADMIN_EMAIL",
		"DIRECTORY_MAIL_BACKEND",
		"DIRECTORY_MAIL_COMMAND",
		"DIRECTORY_MAIL_TIMEOUT_SECONDS",
	} {
		t.Setenv(name, "")
	}
}
