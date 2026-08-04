package config

import (
	"strings"
	"testing"
)

func TestLoadDefaultsRegistrationDisabled(t *testing.T) {
	t.Setenv("DIRECTORY_PUBLIC_BASE_URL", "https://directory.example")
	t.Setenv("DIRECTORY_LISTEN_ADDRESS", "")
	t.Setenv("DIRECTORY_REGISTRATION_ENABLED", "")
	t.Setenv("DIRECTORY_MAX_REQUEST_BODY_BYTES", "")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}

	if cfg.ListenAddress != defaultListenAddress {
		t.Fatalf("ListenAddress = %q", cfg.ListenAddress)
	}

	if cfg.RegistrationEnabled {
		t.Fatal("registration must default to disabled")
	}

	if cfg.MaxRequestBodyBytes != defaultMaxRequestBodyBytes {
		t.Fatalf("MaxRequestBodyBytes = %d", cfg.MaxRequestBodyBytes)
	}
}

func TestLoadAllowsLoopbackHTTP(t *testing.T) {
	t.Setenv("DIRECTORY_PUBLIC_BASE_URL", "http://127.0.0.1:8080")

	if _, err := Load(); err != nil {
		t.Fatalf("Load() error = %v", err)
	}
}

func TestLoadRejectsNonLoopbackHTTP(t *testing.T) {
	t.Setenv("DIRECTORY_PUBLIC_BASE_URL", "http://directory.example")

	_, err := Load()
	if err == nil || !strings.Contains(err.Error(), "must use HTTPS") {
		t.Fatalf("Load() error = %v", err)
	}
}

func TestLoadRejectsRegistrationGarbage(t *testing.T) {
	t.Setenv("DIRECTORY_PUBLIC_BASE_URL", "https://directory.example")
	t.Setenv("DIRECTORY_REGISTRATION_ENABLED", "sometimes")

	if _, err := Load(); err == nil {
		t.Fatal("Load() unexpectedly succeeded")
	}
}

func TestValidateRejectsBaseURLPath(t *testing.T) {
	cfg := Config{
		ListenAddress:       "127.0.0.1:8080",
		PublicBaseURL:       "https://directory.example/path",
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
		MaxRequestBodyBytes: maxRequestBodyBytes + 1,
	}

	if err := cfg.Validate(); err == nil {
		t.Fatal("Validate() unexpectedly succeeded")
	}
}
