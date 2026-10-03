package sqlite

import (
	"context"
	"errors"
	"reflect"
	"testing"
	"time"

	"github.com/thystra/activity-relay-directory/internal/storage"
)

func TestProfileSourcePrecedenceReplacementAndClear(t *testing.T) {
	database := openMigratedTestDatabase(t)
	insertRelay(t, database, testRelayActor, lifecycleRegistered, administrativeActive, 100, 100, nil, nil, nil, true)
	repository, err := NewRelayRepository(database, storage.AllowWrites)
	if err != nil {
		t.Fatalf("NewRelayRepository() error = %v", err)
	}
	ctx := context.Background()

	csv := storage.ProfileSource{Kind: storage.ProfileSourceCSV, SourceLabel: "community", SourceURL: "https://example.org/list.csv"}
	relay := storage.ProfileSource{Kind: storage.ProfileSourceRelay}
	override := storage.ProfileSource{Kind: storage.ProfileSourceOverride, SourceLabel: "operator"}

	if summary, err := repository.ReplaceProfileSource(ctx, storage.ProfileSourceIntent{
		RelayActor: testRelayActor,
		Source:     csv,
		Profile: storage.RelayProfile{
			Availability: "open",
			RelayType:    "community",
			Languages:    []string{"en", "de"},
			Topics:       []string{"general"},
		},
	}, time.Unix(110, 0)); err != nil || summary.Created != 4 {
		t.Fatalf("CSV ReplaceProfileSource() = (%#v, %v)", summary, err)
	}
	if summary, err := repository.ReplaceProfileSource(ctx, storage.ProfileSourceIntent{
		RelayActor: testRelayActor,
		Source:     relay,
		Profile: storage.RelayProfile{
			RelayType: "authenticated",
			Topics:    []string{"tech", "art"},
		},
	}, time.Unix(120, 0)); err != nil || summary.Created != 2 {
		t.Fatalf("relay ReplaceProfileSource() = (%#v, %v)", summary, err)
	}
	if summary, err := repository.ReplaceProfileSource(ctx, storage.ProfileSourceIntent{
		RelayActor: testRelayActor,
		Source:     override,
		Profile:    storage.RelayProfile{RelayType: "operator-corrected"},
	}, time.Unix(130, 0)); err != nil || summary.Created != 1 {
		t.Fatalf("override ReplaceProfileSource() = (%#v, %v)", summary, err)
	}

	profile, err := repository.EffectiveProfile(ctx, testRelayActor)
	if err != nil {
		t.Fatalf("EffectiveProfile() error = %v", err)
	}
	if profile.Availability != "open" || profile.RelayType != "operator-corrected" ||
		!reflect.DeepEqual(profile.Languages, []string{"de", "en"}) ||
		!reflect.DeepEqual(profile.Topics, []string{"art", "tech"}) {
		t.Fatalf("effective profile = %#v", profile)
	}

	if summary, err := repository.ReplaceProfileSource(ctx, storage.ProfileSourceIntent{
		RelayActor: testRelayActor,
		Source:     override,
		Profile:    storage.RelayProfile{},
	}, time.Unix(140, 0)); err != nil || summary.Cleared != 1 {
		t.Fatalf("clear override = (%#v, %v)", summary, err)
	}
	profile, err = repository.EffectiveProfile(ctx, testRelayActor)
	if err != nil || profile.RelayType != "authenticated" {
		t.Fatalf("effective after override clear = (%#v, %v)", profile, err)
	}

	if summary, err := repository.ReplaceProfileSource(ctx, storage.ProfileSourceIntent{
		RelayActor: testRelayActor,
		Source:     relay,
		Profile:    storage.RelayProfile{Topics: []string{"art", "tech"}},
	}, time.Unix(150, 0)); err != nil || summary.Cleared != 1 {
		t.Fatalf("clear relay type = (%#v, %v)", summary, err)
	}
	profile, err = repository.EffectiveProfile(ctx, testRelayActor)
	if err != nil || profile.RelayType != "community" {
		t.Fatalf("effective after relay clear = (%#v, %v)", profile, err)
	}

	var setEvents, clearEvents int
	if err := database.QueryRow(`SELECT
		SUM(action = 'set'), SUM(action = 'clear')
		FROM relay_profile_events WHERE relay_actor = ?`, testRelayActor).Scan(&setEvents, &clearEvents); err != nil {
		t.Fatalf("count profile events: %v", err)
	}
	if setEvents != 7 || clearEvents != 2 {
		t.Fatalf("profile events = set:%d clear:%d", setEvents, clearEvents)
	}
}

func TestProfileReplacementIsAtomicPrivateAndOperationallyIndependent(t *testing.T) {
	database := openMigratedTestDatabase(t)
	heartbeat := int64(105)
	insertRelay(t, database, testRelayActor, lifecycleRegistered, administrativeActive, 100, 105, &heartbeat, nil, nil, true)
	repository, err := NewRelayRepository(database, storage.AllowWrites)
	if err != nil {
		t.Fatalf("NewRelayRepository() error = %v", err)
	}
	ctx := context.Background()
	intent := storage.ProfileSourceIntent{
		RelayActor: testRelayActor,
		Source: storage.ProfileSource{
			Kind:        storage.ProfileSourceCSV,
			SourceLabel: "private-source",
			SourceURL:   "https://example.org/private.csv",
		},
		Profile: storage.RelayProfile{
			Availability: "open",
			Notes:        "descriptive only",
		},
	}
	if _, err := repository.ReplaceProfileSource(ctx, intent, time.Unix(110, 0)); err != nil {
		t.Fatalf("ReplaceProfileSource() error = %v", err)
	}

	relay := readTestRelay(t, database, testRelayActor)
	if relay.lifecycleState != lifecycleRegistered || relay.administrativeState != administrativeActive ||
		relay.updatedAtUnix != 105 || relay.lastSeenAtUnix != 105 ||
		!relay.lastHeartbeat.Valid || relay.lastHeartbeat.Int64 != 105 {
		t.Fatalf("profile mutation changed operational relay state: %#v", relay)
	}
	var sourceLabel, sourceURL string
	if err := database.QueryRow(`SELECT source_label, source_url
		FROM relay_profile_values
		WHERE relay_actor = ? AND source_kind = 'csv' AND field_name = 'notes'`, testRelayActor).Scan(&sourceLabel, &sourceURL); err != nil {
		t.Fatalf("read private profile provenance: %v", err)
	}
	if sourceLabel != "private-source" || sourceURL != "https://example.org/private.csv" {
		t.Fatalf("profile provenance = %q %q", sourceLabel, sourceURL)
	}

	if _, err := database.Exec(`UPDATE relay_profile_events SET action = 'clear' WHERE relay_actor = ?`, testRelayActor); err == nil {
		t.Fatal("profile event update was accepted")
	}
	if _, err := database.Exec(`DELETE FROM relay_profile_events WHERE relay_actor = ?`, testRelayActor); err == nil {
		t.Fatal("profile event delete was accepted")
	}
}

func TestProfileRejectsAbsentIdentityAndRegressingSourceTime(t *testing.T) {
	database := openMigratedTestDatabase(t)
	repository, err := NewRelayRepository(database, storage.AllowWrites)
	if err != nil {
		t.Fatalf("NewRelayRepository() error = %v", err)
	}
	ctx := context.Background()
	intent := storage.ProfileSourceIntent{
		RelayActor: testRelayActor,
		Source:     storage.ProfileSource{Kind: storage.ProfileSourceRelay},
		Profile:    storage.RelayProfile{RelayType: "general"},
	}
	if _, err := repository.ReplaceProfileSource(ctx, intent, time.Unix(100, 0)); !errors.Is(err, storage.ErrProfileAbsent) {
		t.Fatalf("absent ReplaceProfileSource() error = %v", err)
	}
	insertRelay(t, database, testRelayActor, lifecycleRegistered, administrativeActive, 100, 100, nil, nil, nil, true)
	if _, err := repository.ReplaceProfileSource(ctx, intent, time.Unix(110, 0)); err != nil {
		t.Fatalf("first ReplaceProfileSource() error = %v", err)
	}
	intent.Profile.RelayType = "updated"
	if _, err := repository.ReplaceProfileSource(ctx, intent, time.Unix(109, 0)); !errors.Is(err, storage.ErrProfileTime) {
		t.Fatalf("regressing ReplaceProfileSource() error = %v", err)
	}
}

func TestProfileCurrentStateIsRemovedWithLastRetainedIdentity(t *testing.T) {
	database := openMigratedTestDatabase(t)
	insertRelay(t, database, testRelayActor, lifecycleRegistered, administrativeActive, 100, 100, nil, nil, nil, true)
	repository, err := NewRelayRepository(database, storage.AllowWrites)
	if err != nil {
		t.Fatalf("NewRelayRepository() error = %v", err)
	}
	if _, err := repository.ReplaceProfileSource(context.Background(), storage.ProfileSourceIntent{
		RelayActor: testRelayActor,
		Source:     storage.ProfileSource{Kind: storage.ProfileSourceRelay},
		Profile:    storage.RelayProfile{RelayType: "general"},
	}, time.Unix(110, 0)); err != nil {
		t.Fatalf("ReplaceProfileSource() error = %v", err)
	}
	if _, err := database.Exec(`DELETE FROM relays WHERE relay_actor = ?`, testRelayActor); err != nil {
		t.Fatalf("delete retained identity: %v", err)
	}
	var current, history int
	if err := database.QueryRow(`SELECT COUNT(*) FROM relay_profile_values WHERE relay_actor = ?`, testRelayActor).Scan(&current); err != nil {
		t.Fatalf("count current profile values: %v", err)
	}
	if err := database.QueryRow(`SELECT COUNT(*) FROM relay_profile_events WHERE relay_actor = ?`, testRelayActor).Scan(&history); err != nil {
		t.Fatalf("count profile history: %v", err)
	}
	if current != 0 || history != 1 {
		t.Fatalf("profile retention after identity purge = current:%d history:%d", current, history)
	}
	if _, err := repository.EffectiveProfile(context.Background(), testRelayActor); !errors.Is(err, storage.ErrProfileAbsent) {
		t.Fatalf("EffectiveProfile(purged) error = %v", err)
	}

	insertRelay(t, database, testRelayActor, lifecycleRegistered, administrativeActive, 120, 120, nil, nil, nil, true)
	if _, err := repository.ReplaceProfileSource(context.Background(), storage.ProfileSourceIntent{
		RelayActor: testRelayActor,
		Source:     storage.ProfileSource{Kind: storage.ProfileSourceRelay},
		Profile:    storage.RelayProfile{RelayType: "returned"},
	}, time.Unix(120, 0)); err != nil {
		t.Fatalf("ReplaceProfileSource(returned) error = %v", err)
	}
	rows, err := database.Query(`SELECT revision FROM relay_profile_events
		WHERE relay_actor = ? AND source_kind = 'relay' AND field_name = 'relay_type'
		ORDER BY profile_event_id`, testRelayActor)
	if err != nil {
		t.Fatalf("read profile revisions: %v", err)
	}
	defer rows.Close()
	var revisions []int64
	for rows.Next() {
		var revision int64
		if err := rows.Scan(&revision); err != nil {
			t.Fatalf("decode profile revision: %v", err)
		}
		revisions = append(revisions, revision)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("iterate profile revisions: %v", err)
	}
	if !reflect.DeepEqual(revisions, []int64{1, 2}) {
		t.Fatalf("profile revisions after retained-history reappearance = %#v", revisions)
	}
}

func TestProfileAcceptsVerifiedDiscoveryIdentityWithoutLifecycleState(t *testing.T) {
	database := openMigratedTestDatabase(t)
	insertReachabilityDiscovery(t, database, testRelayActor, discoveryActive, 100)
	repository, err := NewRelayRepository(database, storage.AllowWrites)
	if err != nil {
		t.Fatalf("NewRelayRepository() error = %v", err)
	}
	if _, err := repository.ReplaceProfileSource(context.Background(), storage.ProfileSourceIntent{
		RelayActor: testRelayActor,
		Source: storage.ProfileSource{
			Kind:        storage.ProfileSourceCSV,
			SourceLabel: "community",
		},
		Profile: storage.RelayProfile{Availability: "open"},
	}, time.Unix(110, 0)); err != nil {
		t.Fatalf("ReplaceProfileSource(discovery) error = %v", err)
	}
	profile, err := repository.EffectiveProfile(context.Background(), testRelayActor)
	if err != nil || profile.Availability != "open" {
		t.Fatalf("EffectiveProfile(discovery) = (%#v, %v)", profile, err)
	}
}

func TestProfileCurrentStateSurvivesUntilLastIdentityOwnerIsRemoved(t *testing.T) {
	database := openMigratedTestDatabase(t)
	insertRelay(t, database, testRelayActor, lifecycleRegistered, administrativeActive, 100, 100, nil, nil, nil, true)
	insertReachabilityDiscovery(t, database, testRelayActor, discoveryActive, 100)
	repository, err := NewRelayRepository(database, storage.AllowWrites)
	if err != nil {
		t.Fatalf("NewRelayRepository() error = %v", err)
	}
	if _, err := repository.ReplaceProfileSource(context.Background(), storage.ProfileSourceIntent{
		RelayActor: testRelayActor,
		Source:     storage.ProfileSource{Kind: storage.ProfileSourceRelay},
		Profile:    storage.RelayProfile{RelayType: "general"},
	}, time.Unix(110, 0)); err != nil {
		t.Fatalf("ReplaceProfileSource() error = %v", err)
	}
	if _, err := database.Exec(`DELETE FROM relays WHERE relay_actor = ?`, testRelayActor); err != nil {
		t.Fatalf("delete lifecycle identity: %v", err)
	}
	var current int
	if err := database.QueryRow(`SELECT COUNT(*) FROM relay_profile_values WHERE relay_actor = ?`, testRelayActor).Scan(&current); err != nil {
		t.Fatalf("count profile after lifecycle delete: %v", err)
	}
	if current != 1 {
		t.Fatalf("current profile after one owner delete = %d, want 1", current)
	}
	if _, err := database.Exec(`DELETE FROM relay_discoveries WHERE relay_actor = ?`, testRelayActor); err != nil {
		t.Fatalf("delete discovery identity: %v", err)
	}
	if err := database.QueryRow(`SELECT COUNT(*) FROM relay_profile_values WHERE relay_actor = ?`, testRelayActor).Scan(&current); err != nil {
		t.Fatalf("count profile after final owner delete: %v", err)
	}
	if current != 0 {
		t.Fatalf("current profile after final owner delete = %d, want 0", current)
	}
}

func TestProfileRoundTripsAllFields(t *testing.T) {
	database := openMigratedTestDatabase(t)
	insertRelay(t, database, testRelayActor, lifecycleRegistered, administrativeActive, 100, 100, nil, nil, nil, true)
	repository, err := NewRelayRepository(database, storage.AllowWrites)
	if err != nil {
		t.Fatalf("NewRelayRepository() error = %v", err)
	}
	input := storage.RelayProfile{
		ParticipationMode: "moderated",
		Availability:      "open",
		RelayType:         "community",
		Languages:         []string{"en", "de"},
		Countries:         []string{"US", "CA"},
		Regions:           []string{"North America"},
		Topics:            []string{"tech", "art"},
		ContactFediverse:  "@relay@example.social",
		ContactEmail:      "relay@example.org",
		ContactURL:        "https://example.org/contact",
		ParticipationURL:  "https://example.org/join",
		Notes:             "Public note",
	}
	want, err := storage.NormalizeRelayProfile(input)
	if err != nil {
		t.Fatalf("NormalizeRelayProfile() error = %v", err)
	}
	summary, err := repository.ReplaceProfileSource(context.Background(), storage.ProfileSourceIntent{
		RelayActor: testRelayActor,
		Source:     storage.ProfileSource{Kind: storage.ProfileSourceRelay},
		Profile:    input,
	}, time.Unix(110, 0))
	if err != nil {
		t.Fatalf("ReplaceProfileSource() error = %v", err)
	}
	if summary.Created != len(storage.ProfileFields()) || summary.Changed() != true {
		t.Fatalf("profile mutation summary = %#v", summary)
	}
	got, err := repository.EffectiveProfile(context.Background(), testRelayActor)
	if err != nil {
		t.Fatalf("EffectiveProfile() error = %v", err)
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("round-trip profile = %#v, want %#v", got, want)
	}
}

func TestProfileMutationUsesSharedWriteAdmission(t *testing.T) {
	database := openMigratedTestDatabase(t)
	insertRelay(t, database, testRelayActor, lifecycleRegistered, administrativeActive, 100, 100, nil, nil, nil, true)
	repository, err := NewRelayRepository(database, storage.DenyWrites)
	if err != nil {
		t.Fatalf("NewRelayRepository() error = %v", err)
	}
	_, err = repository.ReplaceProfileSource(context.Background(), storage.ProfileSourceIntent{
		RelayActor: testRelayActor,
		Source:     storage.ProfileSource{Kind: storage.ProfileSourceRelay},
		Profile:    storage.RelayProfile{RelayType: "general"},
	}, time.Unix(110, 0))
	if !errors.Is(err, storage.ErrWriteAdmissionDenied) || !errors.Is(err, storage.ErrStorageFailure) {
		t.Fatalf("ReplaceProfileSource(DenyWrites) error = %v", err)
	}
	var count int
	if err := database.QueryRow(`SELECT COUNT(*) FROM relay_profile_values`).Scan(&count); err != nil {
		t.Fatalf("count profile values: %v", err)
	}
	if count != 0 {
		t.Fatalf("profile values after denied mutation = %d", count)
	}
}
