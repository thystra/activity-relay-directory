package v3

import (
	"errors"
	"reflect"
	"testing"

	"github.com/thystra/activity-relay-directory/internal/storage"
)

func validRegisterBody() []byte {
	return []byte(`{"protocol_version":3,"operation":"register","relay_actor":"https://relay.example/actor","public_base_url":"https://relay.example","profile":{"participation_mode":" open ","availability":"public","relay_type":"general","languages":["fr","en","en"],"countries":["US"],"regions":[],"topics":["general"],"contact_fediverse":"@relay@example.social","contact_email":"relay@example.com","contact_url":"https://relay.example/contact","participation_url":"https://relay.example/join","notes":" Community relay "}}`)
}

func TestDecodeRegisterRequestRequiresCompleteNormalizedProfile(t *testing.T) {
	request, err := DecodeRegisterRequest(validRegisterBody(), MaximumRegisterBodyBytes)
	if err != nil {
		t.Fatalf("DecodeRegisterRequest() error = %v", err)
	}
	want := storage.RelayProfile{
		ParticipationMode: "open",
		Availability:      "public",
		RelayType:         "general",
		Languages:         []string{"en", "fr"},
		Countries:         []string{"US"},
		Topics:            []string{"general"},
		ContactFediverse:  "@relay@example.social",
		ContactEmail:      "relay@example.com",
		ContactURL:        "https://relay.example/contact",
		ParticipationURL:  "https://relay.example/join",
		Notes:             "Community relay",
	}
	if !reflect.DeepEqual(request.Profile, want) {
		t.Fatalf("profile = %#v, want %#v", request.Profile, want)
	}
}

func TestDecodeRegisterRequestRejectsIncompleteProfile(t *testing.T) {
	body := []byte(`{"protocol_version":3,"operation":"register","relay_actor":"https://relay.example/actor","public_base_url":"https://relay.example","profile":{"participation_mode":"open"}}`)
	_, err := DecodeRegisterRequest(body, MaximumRegisterBodyBytes)
	if !errors.Is(err, ErrRegisterRequest) {
		t.Fatalf("DecodeRegisterRequest() error = %v", err)
	}
}

func TestDecodeRegisterRequestRejectsNestedDuplicateName(t *testing.T) {
	body := []byte(`{"protocol_version":3,"operation":"register","relay_actor":"https://relay.example/actor","public_base_url":"https://relay.example","profile":{"participation_mode":"open","participation_mode":"closed","availability":"","relay_type":"","languages":[],"countries":[],"regions":[],"topics":[],"contact_fediverse":"","contact_email":"","contact_url":"","participation_url":"","notes":""}}`)
	_, err := DecodeRegisterRequest(body, MaximumRegisterBodyBytes)
	if !errors.Is(err, ErrRegisterRequest) {
		t.Fatalf("DecodeRegisterRequest() error = %v", err)
	}
}

func TestDecodeRegisterRequestRejectsUnknownParticipationMode(t *testing.T) {
	body := []byte(`{"protocol_version":3,"operation":"register","relay_actor":"https://relay.example/actor","public_base_url":"https://relay.example","profile":{"participation_mode":"unrestricted","availability":"","relay_type":"","languages":[],"countries":[],"regions":[],"topics":[],"contact_fediverse":"","contact_email":"","contact_url":"","participation_url":"","notes":""}}`)
	if _, err := DecodeRegisterRequest(body, MaximumRegisterBodyBytes); !errors.Is(err, ErrRegisterRequest) {
		t.Fatalf("DecodeRegisterRequest() error = %v", err)
	}
}

func TestDecodeV2TelemetryIsOptionalBoundedAndHeartbeatOnly(t *testing.T) {
	heartbeat, err := DecodeHeartbeatRequest([]byte(`{"protocol_version":3,"operation":"heartbeat","relay_actor":"https://relay.example/actor","telemetry":{"participating_instance_count":12}}`), MaximumHeartbeatBodyBytes)
	if err != nil {
		t.Fatal(err)
	}
	if heartbeat.Telemetry == nil || heartbeat.Telemetry.ParticipatingInstanceCount != 12 {
		t.Fatalf("heartbeat telemetry = %#v", heartbeat.Telemetry)
	}
	for _, body := range []string{
		`{"protocol_version":3,"operation":"heartbeat","relay_actor":"https://relay.example/actor","telemetry":{"participating_instance_count":-1}}`,
		`{"protocol_version":3,"operation":"heartbeat","relay_actor":"https://relay.example/actor","telemetry":{"participating_instance_count":10000001}}`,
		`{"protocol_version":3,"operation":"heartbeat","relay_actor":"https://relay.example/actor","telemetry":{}}`,
	} {
		if _, err := DecodeHeartbeatRequest([]byte(body), MaximumHeartbeatBodyBytes); !errors.Is(err, ErrHeartbeatRequest) {
			t.Fatalf("DecodeHeartbeatRequest(%s) error = %v", body, err)
		}
	}
	if _, err := DecodeUnregisterRequest([]byte(`{"protocol_version":3,"operation":"unregister","relay_actor":"https://relay.example/actor","telemetry":{"participating_instance_count":12}}`), MaximumUnregisterBodyBytes); !errors.Is(err, ErrUnregisterRequest) {
		t.Fatalf("unregister telemetry error = %v", err)
	}
}

func TestDecodeHeartbeatAndUnregisterIdentity(t *testing.T) {
	heartbeat, err := DecodeHeartbeatRequest([]byte(`{"protocol_version":3,"operation":"heartbeat","relay_actor":"https://relay.example/actor"}`), MaximumHeartbeatBodyBytes)
	if err != nil || heartbeat.RelayActor != "https://relay.example/actor" || heartbeat.Telemetry != nil {
		t.Fatalf("heartbeat = %#v, %v", heartbeat, err)
	}
	unregister, err := DecodeUnregisterRequest([]byte(`{"protocol_version":3,"operation":"unregister","relay_actor":"https://relay.example/actor"}`), MaximumUnregisterBodyBytes)
	if err != nil || unregister.RelayActor != "https://relay.example/actor" {
		t.Fatalf("unregister = %#v, %v", unregister, err)
	}
}
