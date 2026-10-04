package v2

import (
	"errors"
	"reflect"
	"testing"

	"github.com/thystra/activity-relay-directory/internal/storage"
)

func validRegisterBody() []byte {
	return []byte(`{"protocol_version":2,"operation":"register","relay_actor":"https://relay.example/actor","public_base_url":"https://relay.example","profile":{"participation_mode":" open ","availability":"public","relay_type":"general","languages":["fr","en","en"],"countries":["US"],"regions":[],"topics":["general"],"contact_fediverse":"@relay@example.social","contact_email":"relay@example.com","contact_url":"https://relay.example/contact","participation_url":"https://relay.example/join","notes":" Community relay "}}`)
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
	body := []byte(`{"protocol_version":2,"operation":"register","relay_actor":"https://relay.example/actor","public_base_url":"https://relay.example","profile":{"participation_mode":"open"}}`)
	_, err := DecodeRegisterRequest(body, MaximumRegisterBodyBytes)
	if !errors.Is(err, ErrRegisterRequest) {
		t.Fatalf("DecodeRegisterRequest() error = %v", err)
	}
}

func TestDecodeRegisterRequestRejectsNestedDuplicateName(t *testing.T) {
	body := []byte(`{"protocol_version":2,"operation":"register","relay_actor":"https://relay.example/actor","public_base_url":"https://relay.example","profile":{"participation_mode":"open","participation_mode":"closed","availability":"","relay_type":"","languages":[],"countries":[],"regions":[],"topics":[],"contact_fediverse":"","contact_email":"","contact_url":"","participation_url":"","notes":""}}`)
	_, err := DecodeRegisterRequest(body, MaximumRegisterBodyBytes)
	if !errors.Is(err, ErrRegisterRequest) {
		t.Fatalf("DecodeRegisterRequest() error = %v", err)
	}
}

func TestDecodeHeartbeatAndUnregisterRemainIdentityOnly(t *testing.T) {
	for name, test := range map[string]struct {
		body   []byte
		decode func([]byte, int64) (IdentityRequest, error)
	}{
		"heartbeat":  {[]byte(`{"protocol_version":2,"operation":"heartbeat","relay_actor":"https://relay.example/actor"}`), DecodeHeartbeatRequest},
		"unregister": {[]byte(`{"protocol_version":2,"operation":"unregister","relay_actor":"https://relay.example/actor"}`), DecodeUnregisterRequest},
	} {
		t.Run(name, func(t *testing.T) {
			request, err := test.decode(test.body, MaximumRegisterBodyBytes)
			if err != nil {
				t.Fatalf("decode() error = %v", err)
			}
			if request.RelayActor != "https://relay.example/actor" {
				t.Fatalf("relay_actor = %q", request.RelayActor)
			}
		})
	}
}
