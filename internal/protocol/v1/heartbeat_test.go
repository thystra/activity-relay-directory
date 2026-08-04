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

const validHeartbeatBody = `{"protocol_version":1,"operation":"heartbeat","relay_actor":"https://relay.example/actor"}`

func TestDecodeHeartbeatRequestAcceptsCanonicalFixture(t *testing.T) {
	body, err := os.ReadFile(filepath.Join(
		fixtureDirectory(),
		"heartbeat-request.valid.json",
	))
	if err != nil {
		t.Fatalf("read heartbeat fixture: %v", err)
	}
	request, err := DecodeHeartbeatRequest(body, int64(len(body)))
	if err != nil {
		t.Fatalf("DecodeHeartbeatRequest() error = %v", err)
	}
	if request.ProtocolVersion != Version ||
		request.Operation != OperationHeartbeat ||
		request.RelayActor != "https://relay.example/actor" {
		t.Fatalf("heartbeat request = %#v", request)
	}
}

func TestDecodeHeartbeatRequestRejectsInvalidBodies(t *testing.T) {
	for _, test := range []struct {
		name string
		body string
		want error
	}{
		{name: "empty", body: "", want: ErrHeartbeatRequest},
		{name: "null", body: "null", want: ErrHeartbeatRequest},
		{name: "array", body: "[]", want: ErrHeartbeatRequest},
		{name: "malformed", body: "{", want: ErrHeartbeatRequest},
		{name: "trailing value", body: validHeartbeatBody + `{}`, want: ErrHeartbeatRequest},
		{name: "registration metadata", body: `{"protocol_version":1,"operation":"heartbeat","relay_actor":"https://relay.example/actor","public_base_url":"https://relay.example"}`, want: ErrHeartbeatRequest},
		{name: "unknown field", body: `{"protocol_version":1,"operation":"heartbeat","relay_actor":"https://relay.example/actor","sensitive-field":"sensitive-value"}`, want: ErrHeartbeatRequest},
		{name: "duplicate actor", body: `{"protocol_version":1,"operation":"heartbeat","relay_actor":"https://relay.example/actor","relay_actor":"https://other.example/actor"}`, want: ErrHeartbeatRequest},
		{name: "escaped duplicate actor", body: `{"protocol_version":1,"operation":"heartbeat","relay_actor":"https://relay.example/actor","relay_\u0061ctor":"https://other.example/actor"}`, want: ErrHeartbeatRequest},
		{name: "wrong version", body: `{"protocol_version":2,"operation":"heartbeat","relay_actor":"https://relay.example/actor"}`, want: ErrHeartbeatProtocolVersion},
		{name: "missing version", body: `{"operation":"heartbeat","relay_actor":"https://relay.example/actor"}`, want: ErrHeartbeatProtocolVersion},
		{name: "register operation", body: `{"protocol_version":1,"operation":"register","relay_actor":"https://relay.example/actor"}`, want: ErrHeartbeatRequest},
		{name: "unregister operation", body: `{"protocol_version":1,"operation":"unregister","relay_actor":"https://relay.example/actor"}`, want: ErrHeartbeatRequest},
		{name: "missing actor", body: `{"protocol_version":1,"operation":"heartbeat"}`, want: ErrHeartbeatRequest},
		{name: "noncanonical actor", body: `{"protocol_version":1,"operation":"heartbeat","relay_actor":"HTTPS://Relay.Example.:443/%61ctor"}`, want: ErrHeartbeatRequest},
		{name: "wrong actor type", body: `{"protocol_version":1,"operation":"heartbeat","relay_actor":true}`, want: ErrHeartbeatRequest},
	} {
		t.Run(test.name, func(t *testing.T) {
			_, err := DecodeHeartbeatRequest(
				[]byte(test.body),
				MaximumHeartbeatBodyBytes,
			)
			if !errors.Is(err, test.want) {
				t.Fatalf("DecodeHeartbeatRequest() error = %v, want %v", err, test.want)
			}
			for _, supplied := range []string{
				"sensitive-field",
				"sensitive-value",
				"https://other.example/actor",
			} {
				if err != nil && strings.Contains(err.Error(), supplied) {
					t.Fatalf("error leaked supplied JSON material: %v", err)
				}
			}
		})
	}
}

func TestDecodeHeartbeatRequestEnforcesBodyLimits(t *testing.T) {
	body := []byte(validHeartbeatBody)
	if _, err := DecodeHeartbeatRequest(
		body,
		int64(len(body)-1),
	); !errors.Is(err, ErrHeartbeatBodyTooLarge) {
		t.Fatalf("oversized body error = %v", err)
	}
	for _, limit := range []int64{0, -1, MaximumHeartbeatBodyBytes + 1} {
		if _, err := DecodeHeartbeatRequest(
			body,
			limit,
		); !errors.Is(err, ErrHeartbeatConfiguration) {
			t.Fatalf("limit %d error = %v", limit, err)
		}
	}
}

func TestVerifyHeartbeatAndReserveAcceptsCompleteRequest(t *testing.T) {
	body := []byte(validHeartbeatBody)
	request, key := signedRFC9421TestRequestForTarget(
		t,
		body,
		HeartbeatEndpointPath,
		RFC9421POSTComponents(),
		nil,
	)
	resolver := newRFC9421TestResolver(&key.PublicKey)
	store := &recordingRFC9421ReplayStore{reserved: true}

	verified, err := newRFC9421TestVerifier(t, resolver).VerifyHeartbeatAndReserve(
		request,
		body,
		MaximumHeartbeatBodyBytes,
		store,
	)
	if err != nil {
		t.Fatalf("VerifyHeartbeatAndReserve() error = %v", err)
	}
	if verified.Request.RelayActor != "https://relay.example/actor" ||
		verified.Authentication == nil ||
		verified.Authentication.KeyActor != verified.Request.RelayActor {
		t.Fatalf("verified heartbeat request = %#v", verified)
	}
	if resolver.calls.Load() != 1 || store.calls.Load() != 1 {
		t.Fatalf(
			"resolver calls = %d, store calls = %d",
			resolver.calls.Load(),
			store.calls.Load(),
		)
	}
}

func TestVerifyHeartbeatAndReserveRejectsWrongTargetBeforeAuthentication(t *testing.T) {
	body := []byte(validHeartbeatBody)
	base, key := signedRFC9421TestRequestForTarget(
		t,
		body,
		HeartbeatEndpointPath,
		RFC9421POSTComponents(),
		nil,
	)
	for _, test := range []struct {
		name string
		edit func(*http.Request)
	}{
		{name: "wrong method", edit: func(request *http.Request) { request.Method = http.MethodPut }},
		{name: "register path", edit: func(request *http.Request) { request.URL.Path = RegisterEndpointPath }},
		{name: "unregister path", edit: func(request *http.Request) { request.URL.Path = "/v1/relays/unregister" }},
		{name: "encoded path", edit: func(request *http.Request) { request.URL.RawPath = "/v1/%72elays/heartbeat" }},
		{name: "query", edit: func(request *http.Request) { request.URL.RawQuery = "sensitive=query" }},
		{name: "empty query", edit: func(request *http.Request) { request.URL.ForceQuery = true }},
		{name: "fragment", edit: func(request *http.Request) { request.URL.Fragment = "sensitive-fragment" }},
		{name: "inconsistent request URI", edit: func(request *http.Request) { request.RequestURI = HeartbeatEndpointPath + "?sensitive=query" }},
	} {
		t.Run(test.name, func(t *testing.T) {
			request := base.Clone(base.Context())
			request.Header = base.Header.Clone()
			copiedURL := *base.URL
			request.URL = &copiedURL
			test.edit(request)
			resolver := newRFC9421TestResolver(&key.PublicKey)
			store := &recordingRFC9421ReplayStore{reserved: true}
			_, err := newRFC9421TestVerifier(t, resolver).VerifyHeartbeatAndReserve(
				request,
				body,
				MaximumHeartbeatBodyBytes,
				store,
			)
			if !errors.Is(err, ErrHeartbeatTarget) {
				t.Fatalf("VerifyHeartbeatAndReserve() error = %v", err)
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

func TestVerifyHeartbeatAndReserveRejectsBodyBeforeAuthentication(t *testing.T) {
	validBody := []byte(validHeartbeatBody)
	request, key := signedRFC9421TestRequestForTarget(
		t,
		validBody,
		HeartbeatEndpointPath,
		RFC9421POSTComponents(),
		nil,
	)
	invalidBody := []byte(`{"protocol_version":1,"operation":"register"}`)
	resolver := newRFC9421TestResolver(&key.PublicKey)
	store := &recordingRFC9421ReplayStore{reserved: true}
	_, err := newRFC9421TestVerifier(t, resolver).VerifyHeartbeatAndReserve(
		request,
		invalidBody,
		MaximumHeartbeatBodyBytes,
		store,
	)
	if !errors.Is(err, ErrHeartbeatRequest) {
		t.Fatalf("VerifyHeartbeatAndReserve() error = %v", err)
	}
	if resolver.calls.Load() != 0 || store.calls.Load() != 0 {
		t.Fatalf(
			"invalid body resolved %d keys and reserved %d nonces",
			resolver.calls.Load(),
			store.calls.Load(),
		)
	}
}

func TestVerifyHeartbeatAndReserveRejectsReplay(t *testing.T) {
	body := []byte(validHeartbeatBody)
	request, key := signedRFC9421TestRequestForTarget(
		t,
		body,
		HeartbeatEndpointPath,
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
	if _, err := verifier.VerifyHeartbeatAndReserve(
		request,
		body,
		MaximumHeartbeatBodyBytes,
		store,
	); err != nil {
		t.Fatalf("first heartbeat verification: %v", err)
	}
	if _, err := verifier.VerifyHeartbeatAndReserve(
		request,
		body,
		MaximumHeartbeatBodyBytes,
		store,
	); !errors.Is(err, ErrRFC9421Replay) {
		t.Fatalf("replay error = %v", err)
	}
}

func FuzzDecodeHeartbeatRequest(f *testing.F) {
	for _, seed := range []string{
		validHeartbeatBody,
		`{"protocol_version":1,"operation":"heartbeat"}`,
		`{"operation":"heartbeat","operation":"unregister"}`,
		`{"sensitive-field":"sensitive-value"}`,
		`[]`,
		`{`,
		"",
	} {
		f.Add([]byte(seed))
	}

	f.Fuzz(func(t *testing.T, body []byte) {
		request, err := DecodeHeartbeatRequest(body, 4096)
		if err != nil {
			for _, expected := range []error{
				ErrHeartbeatRequest,
				ErrHeartbeatBodyTooLarge,
				ErrHeartbeatProtocolVersion,
			} {
				if errors.Is(err, expected) {
					return
				}
			}
			t.Fatalf("unexpected error class: %v", err)
		}

		if request.ProtocolVersion != Version ||
			request.Operation != OperationHeartbeat {
			t.Fatalf("accepted invalid envelope: %#v", request)
		}
		actor, actorErr := NormalizeRelayActorURL(request.RelayActor)
		if actorErr != nil || actor != request.RelayActor {
			t.Fatalf("accepted noncanonical actor: %#v", request)
		}
	})
}
