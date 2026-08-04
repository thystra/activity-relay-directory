package v1

import (
	"errors"
	"strings"
	"testing"
)

func TestNormalizeRelayActorURL(t *testing.T) {
	for _, test := range []struct {
		name string
		raw  string
		want string
	}{
		{
			name: "authority and unreserved path",
			raw:  "HTTPS://Relay.Example.:443/%61ctor",
			want: "https://relay.example/actor",
		},
		{
			name: "non-default port",
			raw:  "https://relay.example:8443/actor",
			want: "https://relay.example:8443/actor",
		},
		{
			name: "IPv6 literal",
			raw:  "https://[2001:0db8::1]:8443/actor",
			want: "https://[2001:db8::1]:8443/actor",
		},
		{
			name: "IPv4 syntax without network policy",
			raw:  "https://192.0.2.1/actor",
			want: "https://192.0.2.1/actor",
		},
		{
			name: "reserved and UTF-8 escapes",
			raw:  "https://relay.example/%e2%98%83%3a",
			want: "https://relay.example/%E2%98%83%3A",
		},
		{
			name: "origin actor",
			raw:  "https://relay.example",
			want: "https://relay.example/",
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			got, err := NormalizeRelayActorURL(test.raw)
			if err != nil {
				t.Fatalf("NormalizeRelayActorURL() error = %v", err)
			}
			if got != test.want {
				t.Fatalf("NormalizeRelayActorURL() = %q, want %q", got, test.want)
			}
		})
	}
}

func TestNormalizePublicBaseURL(t *testing.T) {
	for _, test := range []struct {
		raw  string
		want string
	}{
		{raw: "https://Relay.Example.:443/", want: "https://relay.example"},
		{raw: "https://relay.example:8443", want: "https://relay.example:8443"},
		{raw: "https://[2001:0db8::1]/", want: "https://[2001:db8::1]"},
	} {
		got, err := NormalizePublicBaseURL(test.raw)
		if err != nil {
			t.Fatalf("NormalizePublicBaseURL(%q) error = %v", test.raw, err)
		}
		if got != test.want {
			t.Fatalf("NormalizePublicBaseURL(%q) = %q, want %q", test.raw, got, test.want)
		}
	}
}

func TestNormalizeRelayURLRejectsInvalidInput(t *testing.T) {
	for _, raw := range []string{
		"",
		" https://relay.example/actor",
		"http://relay.example/actor",
		"https://user@relay.example/actor",
		"https://relay.example/actor?view=full",
		"https://relay.example/actor?",
		"https://relay.example/actor#key",
		"https://relay.example/actor#",
		"https://relay.example:0/actor",
		"https://relay.example:/actor",
		"https://relay.example:bad/actor",
		"https://relay/actor",
		"https://relay_example/actor",
		"https://rélais.example/actor",
		"https://127.0.0.01/actor",
		"https://relay.123/actor",
		"https://relay.example//actor",
		"https://relay.example/./actor",
		"https://relay.example/%2e%2e/actor",
		"https://relay.example/a%2fb",
		"https://relay.example/a%5cb",
		"https://relay.example/a%25b",
		"https://relay.example/%C0%AF",
		"https://relay.example/%FF",
		"https://relay.example/%C2%85",
		"https://relay.example/a\\b",
		"https://[fe80::1%25eth0]/actor",
	} {
		t.Run(raw, func(t *testing.T) {
			_, err := NormalizeRelayActorURL(raw)
			if !errors.Is(err, ErrInvalidRelayURL) {
				t.Fatalf("NormalizeRelayActorURL() error = %v", err)
			}
			if err != nil && containsSensitiveInput(err.Error(), raw) {
				t.Fatalf("error contains supplied URL: %v", err)
			}
		})
	}
}

func TestNormalizeRelayURLErrorDoesNotEchoInput(t *testing.T) {
	raw := "https://sensitive-user:secret-password@relay.example/actor"
	_, err := NormalizeRelayActorURL(raw)
	if !errors.Is(err, ErrInvalidRelayURL) {
		t.Fatalf("NormalizeRelayActorURL() error = %v", err)
	}
	for _, secret := range []string{raw, "sensitive-user", "secret-password"} {
		if strings.Contains(err.Error(), secret) {
			t.Fatalf("error discloses supplied URL material: %v", err)
		}
	}
}

func TestNormalizePublicBaseURLRejectsPath(t *testing.T) {
	for _, raw := range []string{
		"https://relay.example/directory",
		"https://relay.example/%2F",
	} {
		if _, err := NormalizePublicBaseURL(raw); !errors.Is(err, ErrInvalidRelayURL) {
			t.Fatalf("NormalizePublicBaseURL(%q) error = %v", raw, err)
		}
	}
}

func TestNormalizeRelayIdentityBindsOrigin(t *testing.T) {
	identity, err := NormalizeRelayIdentity(
		"https://Relay.Example.:443/%61ctor",
		"https://relay.example/",
	)
	if err != nil {
		t.Fatalf("NormalizeRelayIdentity() error = %v", err)
	}
	if identity.RelayActor != "https://relay.example/actor" {
		t.Fatalf("RelayActor = %q", identity.RelayActor)
	}
	if identity.PublicBaseURL != "https://relay.example" {
		t.Fatalf("PublicBaseURL = %q", identity.PublicBaseURL)
	}

	for _, base := range []string{
		"https://other.example",
		"https://relay.example:8443",
	} {
		_, err := NormalizeRelayIdentity("https://relay.example/actor", base)
		if !errors.Is(err, ErrInvalidRelayURL) {
			t.Fatalf("NormalizeRelayIdentity(%q) error = %v", base, err)
		}
	}
}

func TestRegisterFixtureUsesCanonicalIdentity(t *testing.T) {
	request := decodeFixture[RegisterRequest](t, "register-request.valid.json")
	identity, err := NormalizeRelayIdentity(request.RelayActor, request.PublicBaseURL)
	if err != nil {
		t.Fatalf("NormalizeRelayIdentity() error = %v", err)
	}
	if identity.RelayActor != request.RelayActor {
		t.Fatalf("fixture relay_actor is not canonical: %q", request.RelayActor)
	}
	if identity.PublicBaseURL != request.PublicBaseURL {
		t.Fatalf("fixture public_base_url is not canonical: %q", request.PublicBaseURL)
	}
}

func containsSensitiveInput(message string, raw string) bool {
	return raw != "" && strings.Contains(message, raw)
}

func FuzzNormalizeRelayActorURLIsIdempotent(f *testing.F) {
	for _, seed := range []string{
		"https://relay.example/actor",
		"HTTPS://Relay.Example.:443/%61ctor",
		"https://[2001:db8::1]:8443/actor",
		"https://user:password@relay.example/actor",
		"https://relay.example/%2e%2e/actor",
		"https://relay.example/a%2fb",
		"not a URL",
	} {
		f.Add(seed)
	}

	f.Fuzz(func(t *testing.T, raw string) {
		normalized, err := NormalizeRelayActorURL(raw)
		if err != nil {
			return
		}

		again, err := NormalizeRelayActorURL(normalized)
		if err != nil {
			t.Fatalf("canonical URL rejected: %v", err)
		}
		if again != normalized {
			t.Fatalf("normalization is not idempotent: %q then %q", normalized, again)
		}
	})
}
