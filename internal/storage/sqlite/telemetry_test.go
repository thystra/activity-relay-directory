package sqlite

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/thystra/activity-relay-directory/internal/storage"
)

func TestRelayTelemetryPersistsBoundedCountWithoutChangingLifecycle(t *testing.T) {
	database := openMigratedTestDatabase(t)
	heartbeat := int64(105)
	insertRelay(t, database, testRelayActor, lifecycleRegistered, administrativeActive, 100, 105, &heartbeat, nil, nil, true)
	repository, err := NewRelayRepository(database, storage.AllowWrites)
	if err != nil {
		t.Fatal(err)
	}
	if err := repository.ReplaceRelayTelemetry(context.Background(), storage.TelemetryIntent{
		RelayActor: testRelayActor, ReceivingInstanceCount: 12,
	}, time.Unix(110, 0)); err != nil {
		t.Fatal(err)
	}
	var count int
	var reported int64
	if err := database.QueryRow(`SELECT receiving_instance_count, reported_at_unix FROM relay_telemetry WHERE relay_actor = ?`, testRelayActor).Scan(&count, &reported); err != nil {
		t.Fatal(err)
	}
	if count != 12 || reported != 110 {
		t.Fatalf("telemetry = %d @ %d", count, reported)
	}
	relay := readTestRelay(t, database, testRelayActor)
	if relay.lifecycleState != lifecycleRegistered || relay.lastSeenAtUnix != 105 || !relay.lastHeartbeat.Valid || relay.lastHeartbeat.Int64 != 105 {
		t.Fatalf("telemetry changed lifecycle state: %#v", relay)
	}
}

func TestRelayTelemetryRejectsInvalidOrAbsentIdentity(t *testing.T) {
	database := openMigratedTestDatabase(t)
	repository, err := NewRelayRepository(database, storage.AllowWrites)
	if err != nil {
		t.Fatal(err)
	}
	for _, count := range []int{-1, storage.MaximumReceivingInstanceCount + 1} {
		err := repository.ReplaceRelayTelemetry(context.Background(), storage.TelemetryIntent{RelayActor: testRelayActor, ReceivingInstanceCount: count}, time.Unix(100, 0))
		if !errors.Is(err, storage.ErrTelemetryInput) {
			t.Fatalf("count %d error = %v", count, err)
		}
	}
	if err := repository.ReplaceRelayTelemetry(context.Background(), storage.TelemetryIntent{RelayActor: testRelayActor, ReceivingInstanceCount: 1}, time.Unix(100, 0)); err == nil {
		t.Fatal("telemetry for absent lifecycle identity was accepted")
	}
}

func TestProfilePersistenceTreatsSQLMetacharactersAsData(t *testing.T) {
	database := openMigratedTestDatabase(t)
	insertRelay(t, database, testRelayActor, lifecycleRegistered, administrativeActive, 100, 100, nil, nil, nil, true)
	repository, err := NewRelayRepository(database, storage.AllowWrites)
	if err != nil {
		t.Fatal(err)
	}
	payload := `Robert'); DROP TABLE relays; -- <script>alert(1)</script> $(touch /tmp/pwned)`
	if _, err := repository.ReplaceProfileSource(context.Background(), storage.ProfileSourceIntent{
		RelayActor: testRelayActor,
		Source:     storage.ProfileSource{Kind: storage.ProfileSourceRelay},
		Profile:    storage.RelayProfile{Notes: payload},
	}, time.Unix(101, 0)); err != nil {
		t.Fatalf("store hostile-looking text: %v", err)
	}
	profile, err := repository.EffectiveProfile(context.Background(), testRelayActor)
	if err != nil || profile.Notes != payload {
		t.Fatalf("profile = %#v, %v", profile, err)
	}
	var relays int
	if err := database.QueryRow(`SELECT COUNT(*) FROM relays`).Scan(&relays); err != nil || relays != 1 {
		t.Fatalf("relays table compromised: count=%d err=%v", relays, err)
	}
}
