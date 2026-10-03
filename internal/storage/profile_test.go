package storage

import (
	"errors"
	"reflect"
	"strings"
	"testing"
)

func TestNormalizeRelayProfileCanonicalizesBoundedValues(t *testing.T) {
	profile, err := NormalizeRelayProfile(RelayProfile{
		ParticipationMode: "  moderated  ",
		Availability:      "open",
		RelayType:         "general",
		Languages:         []string{"en", "de", "en"},
		Countries:         []string{"US", "CA"},
		Regions:           []string{"North America"},
		Topics:            []string{"tech", "art", "tech"},
		ContactFediverse:  "@relay@example.social",
		ContactEmail:      "relay@example.org",
		ContactURL:        "https://EXAMPLE.org:443/contact",
		ParticipationURL:  "https://example.org/join",
		Notes:             "  Public note  ",
	})
	if err != nil {
		t.Fatalf("NormalizeRelayProfile() error = %v", err)
	}
	if profile.ParticipationMode != "moderated" || profile.Notes != "Public note" ||
		profile.ContactURL != "https://example.org/contact" {
		t.Fatalf("normalized scalar profile = %#v", profile)
	}
	if !reflect.DeepEqual(profile.Languages, []string{"de", "en"}) ||
		!reflect.DeepEqual(profile.Topics, []string{"art", "tech"}) ||
		!reflect.DeepEqual(profile.Countries, []string{"CA", "US"}) {
		t.Fatalf("normalized lists = languages:%#v topics:%#v countries:%#v", profile.Languages, profile.Topics, profile.Countries)
	}
}

func TestNormalizeRelayProfileRejectsUnsafeValues(t *testing.T) {
	for name, profile := range map[string]RelayProfile{
		"control":    {Notes: "bad\nvalue"},
		"http url":   {ContactURL: "http://example.org/contact"},
		"userinfo":   {ContactURL: "https://user@example.org/contact"},
		"fragment":   {ContactURL: "https://example.org/contact#private"},
		"query":      {ParticipationURL: "https://example.org/join?mode=relay"},
		"bad host":   {ContactURL: "https://bad_host.example/contact"},
		"email name": {ContactEmail: "Relay <relay@example.org>"},
		"long note":  {Notes: strings.Repeat("x", MaximumProfileNoteBytes+1)},
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := NormalizeRelayProfile(profile); !errors.Is(err, ErrProfileInput) {
				t.Fatalf("NormalizeRelayProfile() error = %v, want ErrProfileInput", err)
			}
		})
	}
}

func TestNormalizeProfileSourceEnforcesPrivateProvenanceShape(t *testing.T) {
	csv, err := NormalizeProfileSource(ProfileSource{
		Kind:        ProfileSourceCSV,
		SourceLabel: "community-list",
		SourceURL:   "https://EXAMPLE.org:443/relays.csv",
	})
	if err != nil {
		t.Fatalf("NormalizeProfileSource(csv) error = %v", err)
	}
	if csv.SourceURL != "https://example.org/relays.csv" {
		t.Fatalf("CSV source URL = %q", csv.SourceURL)
	}
	if _, err := NormalizeProfileSource(ProfileSource{Kind: ProfileSourceCSV}); !errors.Is(err, ErrProfileInput) {
		t.Fatalf("unlabeled CSV source error = %v", err)
	}
	if _, err := NormalizeProfileSource(ProfileSource{Kind: ProfileSourceRelay, SourceLabel: "operator"}); !errors.Is(err, ErrProfileInput) {
		t.Fatalf("labeled relay source error = %v", err)
	}
	if _, err := NormalizeProfileSource(ProfileSource{Kind: ProfileSourceOverride, SourceLabel: "-operator"}); !errors.Is(err, ErrProfileInput) {
		t.Fatalf("invalid override label error = %v", err)
	}
}

func TestProfileSourcePriorityIsClosed(t *testing.T) {
	if ProfileSourcePriority(ProfileSourceOverride) <= ProfileSourcePriority(ProfileSourceRelay) ||
		ProfileSourcePriority(ProfileSourceRelay) <= ProfileSourcePriority(ProfileSourceCSV) ||
		ProfileSourcePriority(ProfileSourceKind("other")) != 0 {
		t.Fatal("profile source precedence changed")
	}
}
