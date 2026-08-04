package v1

import (
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

const validUnregisterBody = `{"protocol_version":1,"operation":"unregister","relay_actor":"https://relay.example/actor"}`

func TestDecodeUnregisterRequestAcceptsCanonicalFixture(t *testing.T) {
	body, err := os.ReadFile(filepath.Join(
		fixtureDirectory(),
		"unregister-request.valid.json",
	))
	if err != nil {
		t.Fatalf("read unregister fixture: %v", err)
	}
	request, err := DecodeUnregisterRequest(body, int64(len(body)))
	if err != nil {
		t.Fatalf("DecodeUnregisterRequest() error = %v", err)
	}
	if request.ProtocolVersion != Version ||
		request.Operation != OperationUnregister ||
		request.RelayActor != "https://relay.example/actor" {
		t.Fatalf("unregister request = %#v", request)
	}
}

func TestDecodeUnregisterRequestRejectsInvalidBodies(t *testing.T) {
	for _, test := range []struct {
		name string
		body string
		want error
	}{
		{name: "empty", body: "", want: ErrUnregisterRequest},
		{name: "null", body: "null", want: ErrUnregisterRequest},
		{name: "array", body: "[]", want: ErrUnregisterRequest},
		{name: "malformed", body: "{", want: ErrUnregisterRequest},
		{name: "trailing value", body: validUnregisterBody + `{}`, want: ErrUnregisterRequest},
		{name: "registration metadata", body: `{"protocol_version":1,"operation":"unregister","relay_actor":"https://relay.example/actor","public_base_url":"https://relay.example"}`, want: ErrUnregisterRequest},
		{name: "unknown field", body: `{"protocol_version":1,"operation":"unregister","relay_actor":"https://relay.example/actor","sensitive-field":"sensitive-value"}`, want: ErrUnregisterRequest},
		{name: "duplicate operation", body: `{"protocol_version":1,"operation":"unregister","operation":"heartbeat","relay_actor":"https://relay.example/actor"}`, want: ErrUnregisterRequest},
		{name: "escaped duplicate operation", body: `{"protocol_version":1,"operation":"unregister","opera\u0074ion":"heartbeat","relay_actor":"https://relay.example/actor"}`, want: ErrUnregisterRequest},
		{name: "wrong version", body: `{"protocol_version":2,"operation":"unregister","relay_actor":"https://relay.example/actor"}`, want: ErrUnregisterProtocolVersion},
		{name: "missing version", body: `{"operation":"unregister","relay_actor":"https://relay.example/actor"}`, want: ErrUnregisterProtocolVersion},
		{name: "register operation", body: `{"protocol_version":1,"operation":"register","relay_actor":"https://relay.example/actor"}`, want: ErrUnregisterRequest},
		{name: "heartbeat operation", body: `{"protocol_version":1,"operation":"heartbeat","relay_actor":"https://relay.example/actor"}`, want: ErrUnregisterRequest},
		{name: "missing actor", body: `{"protocol_version":1,"operation":"unregister"}`, want: ErrUnregisterRequest},
		{name: "noncanonical actor", body: `{"protocol_version":1,"operation":"unregister","relay_actor":"HTTPS://Relay.Example.:443/%61ctor"}`, want: ErrUnregisterRequest},
		{name: "wrong actor type", body: `{"protocol_version":1,"operation":"unregister","relay_actor":true}`, want: ErrUnregisterRequest},
	} {
		t.Run(test.name, func(t *testing.T) {
			_, err := DecodeUnregisterRequest(
				[]byte(test.body),
				MaximumUnregisterBodyBytes,
			)
			if !errors.Is(err, test.want) {
				t.Fatalf("DecodeUnregisterRequest() error = %v, want %v", err, test.want)
			}
			for _, supplied := range []string{
				"sensitive-field",
				"sensitive-value",
			} {
				if err != nil && strings.Contains(err.Error(), supplied) {
					t.Fatalf("error leaked supplied JSON material: %v", err)
				}
			}
		})
	}
}

func TestDecodeUnregisterRequestEnforcesBodyLimits(t *testing.T) {
	body := []byte(validUnregisterBody)
	if _, err := DecodeUnregisterRequest(
		body,
		int64(len(body)-1),
	); !errors.Is(err, ErrUnregisterBodyTooLarge) {
		t.Fatalf("oversized body error = %v", err)
	}
	for _, limit := range []int64{0, -1, MaximumUnregisterBodyBytes + 1} {
		if _, err := DecodeUnregisterRequest(
			body,
			limit,
		); !errors.Is(err, ErrUnregisterConfiguration) {
			t.Fatalf("limit %d error = %v", limit, err)
		}
	}
}

func TestVerifyUnregisterAndReserveAcceptsCompleteRequest(t *testing.T) {
	body := []byte(validUnregisterBody)
	request, key := signedRFC9421TestRequestForTarget(
		t,
		body,
		UnregisterEndpointPath,
		RFC9421POSTComponents(),
		nil,
	)
	resolver := newRFC9421TestResolver(&key.PublicKey)
	store := &recordingRFC9421ReplayStore{reserved: true}

	verified, err := newRFC9421TestVerifier(t, resolver).VerifyUnregisterAndReserve(
		request,
		body,
		MaximumUnregisterBodyBytes,
		store,
	)
	if err != nil {
		t.Fatalf("VerifyUnregisterAndReserve() error = %v", err)
	}
	if verified.Request.RelayActor != "https://relay.example/actor" ||
		verified.Authentication == nil ||
		verified.Authentication.KeyActor != verified.Request.RelayActor {
		t.Fatalf("verified unregister request = %#v", verified)
	}
	if resolver.calls.Load() != 1 || store.calls.Load() != 1 {
		t.Fatalf(
			"resolver calls = %d, store calls = %d",
			resolver.calls.Load(),
			store.calls.Load(),
		)
	}
}

func TestVerifyUnregisterAndReserveRejectsWrongTargetBeforeAuthentication(t *testing.T) {
	body := []byte(validUnregisterBody)
	base, key := signedRFC9421TestRequestForTarget(
		t,
		body,
		UnregisterEndpointPath,
		RFC9421POSTComponents(),
		nil,
	)
	for _, test := range []struct {
		name string
		edit func(*http.Request)
	}{
		{name: "wrong method", edit: func(request *http.Request) { request.Method = http.MethodPut }},
		{name: "register path", edit: func(request *http.Request) { request.URL.Path = RegisterEndpointPath }},
		{name: "heartbeat path", edit: func(request *http.Request) { request.URL.Path = HeartbeatEndpointPath }},
		{name: "encoded path", edit: func(request *http.Request) { request.URL.RawPath = "/v1/%72elays/unregister" }},
		{name: "query", edit: func(request *http.Request) { request.URL.RawQuery = "sensitive=query" }},
		{name: "empty query", edit: func(request *http.Request) { request.URL.ForceQuery = true }},
		{name: "fragment", edit: func(request *http.Request) { request.URL.Fragment = "sensitive-fragment" }},
		{name: "inconsistent request URI", edit: func(request *http.Request) { request.RequestURI = UnregisterEndpointPath + "?sensitive=query" }},
	} {
		t.Run(test.name, func(t *testing.T) {
			request := base.Clone(base.Context())
			request.Header = base.Header.Clone()
			copiedURL := *base.URL
			request.URL = &copiedURL
			test.edit(request)
			resolver := newRFC9421TestResolver(&key.PublicKey)
			store := &recordingRFC9421ReplayStore{reserved: true}
			_, err := newRFC9421TestVerifier(t, resolver).VerifyUnregisterAndReserve(
				request,
				body,
				MaximumUnregisterBodyBytes,
				store,
			)
			if !errors.Is(err, ErrUnregisterTarget) {
				t.Fatalf("VerifyUnregisterAndReserve() error = %v", err)
			}
			if resolver.calls.Load() != 0 || store.calls.Load() != 0 {
				t.Fatalf(
					"invalid target resolved %d keys and reserved %d nonces",
					resolver.calls.Load(),
					store.calls.Load(),
				)
			}
			for _, supplied := range []string{"sensitive=query", "sensitive-fragment"} {
				if strings.Contains(err.Error(), supplied) {
					t.Fatalf("error leaked supplied target material: %v", err)
				}
			}
		})
	}
}

func TestVerifyUnregisterAndReserveRejectsBodyBeforeAuthentication(t *testing.T) {
	validBody := []byte(validUnregisterBody)
	request, key := signedRFC9421TestRequestForTarget(
		t,
		validBody,
		UnregisterEndpointPath,
		RFC9421POSTComponents(),
		nil,
	)
	invalidBody := []byte(`{"protocol_version":1,"operation":"heartbeat"}`)
	resolver := newRFC9421TestResolver(&key.PublicKey)
	store := &recordingRFC9421ReplayStore{reserved: true}
	_, err := newRFC9421TestVerifier(t, resolver).VerifyUnregisterAndReserve(
		request,
		invalidBody,
		MaximumUnregisterBodyBytes,
		store,
	)
	if !errors.Is(err, ErrUnregisterRequest) {
		t.Fatalf("VerifyUnregisterAndReserve() error = %v", err)
	}
	if resolver.calls.Load() != 0 || store.calls.Load() != 0 {
		t.Fatalf(
			"invalid body resolved %d keys and reserved %d nonces",
			resolver.calls.Load(),
			store.calls.Load(),
		)
	}
}

func TestVerifyUnregisterAndReserveRejectsReplay(t *testing.T) {
	body := []byte(validUnregisterBody)
	request, key := signedRFC9421TestRequestForTarget(
		t,
		body,
		UnregisterEndpointPath,
		RFC9421POSTComponents(),
		nil,
	)
	resolver := newRFC9421TestResolver(&key.PublicKey)
	store, err := newMemoryRFC9421ReplayStore(4, func() time.Time {
		return rfc9421TestNow
	})
	if err != nil {
		t.Fatalf("create replay store: %v", err)
	}
	verifier := newRFC9421TestVerifier(t, resolver)
	if _, err := verifier.VerifyUnregisterAndReserve(
		request,
		body,
		MaximumUnregisterBodyBytes,
		store,
	); err != nil {
		t.Fatalf("first unregister verification: %v", err)
	}
	if _, err := verifier.VerifyUnregisterAndReserve(
		request,
		body,
		MaximumUnregisterBodyBytes,
		store,
	); !errors.Is(err, ErrRFC9421Replay) {
		t.Fatalf("replay error = %v", err)
	}
}

func FuzzDecodeUnregisterRequest(f *testing.F) {
	for _, seed := range []string{
		validUnregisterBody,
		`{"protocol_version":1,"operation":"unregister"}`,
		`{"operation":"unregister","operation":"heartbeat"}`,
		`{"sensitive-field":"sensitive-value"}`,
		`[]`,
		`{`,
		"",
	} {
		f.Add([]byte(seed))
	}

	f.Fuzz(func(t *testing.T, body []byte) {
		request, err := DecodeUnregisterRequest(body, 4096)
		if err != nil {
			for _, expected := range []error{
				ErrUnregisterRequest,
				ErrUnregisterBodyTooLarge,
				ErrUnregisterProtocolVersion,
			} {
				if errors.Is(err, expected) {
					return
				}
			}
			t.Fatalf("unexpected error class: %v", err)
		}

		if request.ProtocolVersion != Version ||
			request.Operation != OperationUnregister {
			t.Fatalf("accepted invalid envelope: %#v", request)
		}
		actor, actorErr := NormalizeRelayActorURL(request.RelayActor)
		if actorErr != nil || actor != request.RelayActor {
			t.Fatalf("accepted noncanonical actor: %#v", request)
		}
	})
}
