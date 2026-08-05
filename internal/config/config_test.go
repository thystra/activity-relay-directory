package config

import (
	"net/netip"
	"path/filepath"
	"strings"
	"testing"
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
	t.Setenv(
		"DIRECTORY_DATABASE_PATH",
		filepath.Join(t.TempDir(), "directory.sqlite"),
	)
}
