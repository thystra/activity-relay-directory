package storage

import (
	"context"
	"errors"
	"net/mail"
	"sort"
	"strings"
	"time"
	"unicode/utf8"

	v1 "github.com/thystra/activity-relay-directory/internal/protocol/v1"
)

// Profile persistence bounds keep descriptive values finite before they reach
// SQLite, CSV, protocol, or public presentation adapters.
const (
	MaximumProfileScalarBytes      = 256
	MaximumProfileNoteBytes        = 1024
	MaximumProfileListItems        = 16
	MaximumProfileListItemBytes    = 128
	MaximumProfileURLBytes         = 2048
	MaximumProfileEmailBytes       = 320
	MaximumProfileFediverseIDBytes = 256
	MaximumProfileSourceURLBytes   = 2048
	MaximumProfileStoredValueBytes = 4096
)

var (
	// ErrProfileInput identifies malformed, noncanonical, or oversized profile input.
	ErrProfileInput = errors.New("relay profile input is invalid")
	// ErrProfileAbsent means no retained lifecycle/discovery identity owns the profile.
	ErrProfileAbsent = errors.New("relay profile identity is not retained")
	// ErrProfileTime identifies a negative or source-relative regressing acceptance time.
	ErrProfileTime = errors.New("relay profile time is invalid")
)

// ProfileSourceKind identifies one private assertion authority.
type ProfileSourceKind string

const (
	ProfileSourceCSV      ProfileSourceKind = "csv"
	ProfileSourceRelay    ProfileSourceKind = "relay"
	ProfileSourceOverride ProfileSourceKind = "override"
)

// Valid reports whether kind belongs to the closed profile source vocabulary.
func (kind ProfileSourceKind) Valid() bool {
	switch kind {
	case ProfileSourceCSV, ProfileSourceRelay, ProfileSourceOverride:
		return true
	default:
		return false
	}
}

// ProfileSourcePriority returns the fixed per-field precedence rank.
func ProfileSourcePriority(kind ProfileSourceKind) int {
	switch kind {
	case ProfileSourceOverride:
		return 3
	case ProfileSourceRelay:
		return 2
	case ProfileSourceCSV:
		return 1
	default:
		return 0
	}
}

// ProfileField identifies one descriptive profile field.
type ProfileField string

const (
	ProfileFieldParticipationMode ProfileField = "participation_mode"
	ProfileFieldAvailability      ProfileField = "availability"
	ProfileFieldRelayType         ProfileField = "relay_type"
	ProfileFieldLanguages         ProfileField = "languages"
	ProfileFieldCountries         ProfileField = "countries"
	ProfileFieldRegions           ProfileField = "regions"
	ProfileFieldTopics            ProfileField = "topics"
	ProfileFieldContactFediverse  ProfileField = "contact_fediverse"
	ProfileFieldContactEmail      ProfileField = "contact_email"
	ProfileFieldContactURL        ProfileField = "contact_url"
	ProfileFieldParticipationURL  ProfileField = "participation_url"
	ProfileFieldNotes             ProfileField = "notes"
)

var profileFields = [...]ProfileField{
	ProfileFieldParticipationMode,
	ProfileFieldAvailability,
	ProfileFieldRelayType,
	ProfileFieldLanguages,
	ProfileFieldCountries,
	ProfileFieldRegions,
	ProfileFieldTopics,
	ProfileFieldContactFediverse,
	ProfileFieldContactEmail,
	ProfileFieldContactURL,
	ProfileFieldParticipationURL,
	ProfileFieldNotes,
}

// ProfileFields returns the closed profile field set in deterministic order.
func ProfileFields() []ProfileField {
	result := make([]ProfileField, len(profileFields))
	copy(result, profileFields[:])
	return result
}

// Valid reports whether field belongs to the closed profile field vocabulary.
func (field ProfileField) Valid() bool {
	for _, candidate := range profileFields {
		if field == candidate {
			return true
		}
	}
	return false
}

// MultiValue reports whether field is represented as a normalized string list.
func (field ProfileField) MultiValue() bool {
	switch field {
	case ProfileFieldLanguages, ProfileFieldCountries, ProfileFieldRegions, ProfileFieldTopics:
		return true
	default:
		return false
	}
}

// RelayProfile is the normalized descriptive profile shared by persistence and
// later CSV/protocol/public adapters. It contains no operational evidence.
type RelayProfile struct {
	ParticipationMode string
	Availability      string
	RelayType         string
	Languages         []string
	Countries         []string
	Regions           []string
	Topics            []string
	ContactFediverse  string
	ContactEmail      string
	ContactURL        string
	ParticipationURL  string
	Notes             string
}

// Empty reports whether no descriptive field is asserted.
func (profile RelayProfile) Empty() bool {
	return profile.ParticipationMode == "" && profile.Availability == "" &&
		profile.RelayType == "" && len(profile.Languages) == 0 &&
		len(profile.Countries) == 0 && len(profile.Regions) == 0 &&
		len(profile.Topics) == 0 && profile.ContactFediverse == "" &&
		profile.ContactEmail == "" && profile.ContactURL == "" &&
		profile.ParticipationURL == "" && profile.Notes == ""
}

// ProfileSource carries private provenance for one source-scoped replacement.
type ProfileSource struct {
	Kind        ProfileSourceKind
	SourceLabel string
	SourceURL   string
}

// ProfileSourceIntent replaces one source's complete current assertion set.
type ProfileSourceIntent struct {
	RelayActor string
	Source     ProfileSource
	Profile    RelayProfile
}

// ProfileMutationSummary reports field-level current-state changes.
type ProfileMutationSummary struct {
	Created   int
	Updated   int
	Cleared   int
	Unchanged int
}

// Changed reports whether the source replacement changed any current assertion.
func (summary ProfileMutationSummary) Changed() bool {
	return summary.Created > 0 || summary.Updated > 0 || summary.Cleared > 0
}

// ProfileRepository stores source-scoped assertions and resolves effective
// descriptive values without changing relay operational state.
type ProfileRepository interface {
	ReplaceProfileSource(context.Context, ProfileSourceIntent, time.Time) (ProfileMutationSummary, error)
	EffectiveProfile(context.Context, string) (RelayProfile, error)
}

// NormalizeProfileSource validates and canonicalizes private source provenance.
func NormalizeProfileSource(source ProfileSource) (ProfileSource, error) {
	if !source.Kind.Valid() {
		return ProfileSource{}, ErrProfileInput
	}
	source.SourceLabel = strings.TrimSpace(source.SourceLabel)
	source.SourceURL = strings.TrimSpace(source.SourceURL)

	switch source.Kind {
	case ProfileSourceRelay:
		if source.SourceLabel != "" || source.SourceURL != "" {
			return ProfileSource{}, ErrProfileInput
		}
	case ProfileSourceCSV:
		if !ValidDiscoverySourceLabel(source.SourceLabel) || source.SourceLabel == "" {
			return ProfileSource{}, ErrProfileInput
		}
		if source.SourceURL != "" {
			canonical, err := normalizeProfileHTTPSURL(source.SourceURL, MaximumProfileSourceURLBytes)
			if err != nil {
				return ProfileSource{}, err
			}
			source.SourceURL = canonical
		}
	case ProfileSourceOverride:
		if !ValidOperatorID(source.SourceLabel) || source.SourceURL != "" {
			return ProfileSource{}, ErrProfileInput
		}
	}
	return source, nil
}

// NormalizeRelayProfile validates bounds and returns deterministic profile values.
func NormalizeRelayProfile(profile RelayProfile) (RelayProfile, error) {
	var result RelayProfile
	var err error
	if result.ParticipationMode, err = normalizeProfileText(profile.ParticipationMode, MaximumProfileScalarBytes); err != nil {
		return RelayProfile{}, err
	}
	if result.Availability, err = normalizeProfileText(profile.Availability, MaximumProfileScalarBytes); err != nil {
		return RelayProfile{}, err
	}
	if result.RelayType, err = normalizeProfileText(profile.RelayType, MaximumProfileScalarBytes); err != nil {
		return RelayProfile{}, err
	}
	if result.Languages, err = normalizeProfileList(profile.Languages); err != nil {
		return RelayProfile{}, err
	}
	if result.Countries, err = normalizeProfileList(profile.Countries); err != nil {
		return RelayProfile{}, err
	}
	if result.Regions, err = normalizeProfileList(profile.Regions); err != nil {
		return RelayProfile{}, err
	}
	if result.Topics, err = normalizeProfileList(profile.Topics); err != nil {
		return RelayProfile{}, err
	}
	if result.ContactFediverse, err = normalizeProfileText(profile.ContactFediverse, MaximumProfileFediverseIDBytes); err != nil {
		return RelayProfile{}, err
	}
	if result.ContactEmail, err = normalizeProfileEmail(profile.ContactEmail); err != nil {
		return RelayProfile{}, err
	}
	if profile.ContactURL != "" {
		if result.ContactURL, err = normalizeProfileHTTPSURL(profile.ContactURL, MaximumProfileURLBytes); err != nil {
			return RelayProfile{}, err
		}
	}
	if profile.ParticipationURL != "" {
		if result.ParticipationURL, err = normalizeProfileHTTPSURL(profile.ParticipationURL, MaximumProfileURLBytes); err != nil {
			return RelayProfile{}, err
		}
	}
	if result.Notes, err = normalizeProfileText(profile.Notes, MaximumProfileNoteBytes); err != nil {
		return RelayProfile{}, err
	}
	return result, nil
}

// ValidProfileRelayActor reports whether value is an already-canonical relay actor.
func ValidProfileRelayActor(value string) bool {
	if value == "" {
		return false
	}
	canonical, err := v1.NormalizeRelayActorURL(value)
	return err == nil && canonical == value
}

func normalizeProfileText(value string, maximum int) (string, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		return "", nil
	}
	if !utf8.ValidString(value) || len(value) > maximum || containsProfileControl(value) {
		return "", ErrProfileInput
	}
	return value, nil
}

func normalizeProfileList(values []string) ([]string, error) {
	if len(values) == 0 {
		return nil, nil
	}
	if len(values) > MaximumProfileListItems {
		return nil, ErrProfileInput
	}
	unique := make(map[string]struct{}, len(values))
	for _, value := range values {
		normalized, err := normalizeProfileText(value, MaximumProfileListItemBytes)
		if err != nil || normalized == "" {
			return nil, ErrProfileInput
		}
		unique[normalized] = struct{}{}
	}
	if len(unique) > MaximumProfileListItems {
		return nil, ErrProfileInput
	}
	result := make([]string, 0, len(unique))
	for value := range unique {
		result = append(result, value)
	}
	sort.Strings(result)
	return result, nil
}

func normalizeProfileEmail(value string) (string, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		return "", nil
	}
	if !utf8.ValidString(value) || len(value) > MaximumProfileEmailBytes || containsProfileControl(value) {
		return "", ErrProfileInput
	}
	parsed, err := mail.ParseAddress(value)
	if err != nil || parsed.Address != value || strings.ContainsAny(value, "<>\"") {
		return "", ErrProfileInput
	}
	return value, nil
}

func normalizeProfileHTTPSURL(value string, maximum int) (string, error) {
	value = strings.TrimSpace(value)
	if value == "" || !utf8.ValidString(value) || len(value) > maximum || containsProfileControl(value) {
		return "", ErrProfileInput
	}
	canonical, err := v1.NormalizeRelayActorURL(value)
	if err != nil || len(canonical) > maximum {
		return "", ErrProfileInput
	}
	return canonical, nil
}

func containsProfileControl(value string) bool {
	for _, character := range value {
		if character < 0x20 || character == 0x7f {
			return true
		}
	}
	return false
}
