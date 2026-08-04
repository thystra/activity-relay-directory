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

const validRegisterBody = `{"protocol_version":1,"operation":"register","relay_actor":"https://relay.example/actor","public_base_url":"https://relay.example"}`

func TestDecodeRegisterRequestAcceptsCanonicalFixture(t *testing.T) {
	body, err := os.ReadFile(filepath.Join(
		fixtureDirectory(),
		"register-request.valid.json",
	))
	if err != nil {
		t.Fatalf("read register fixture: %v", err)
	}
	request, err := DecodeRegisterRequest(body, int64(len(body)))
	if err != nil {
		t.Fatalf("DecodeRegisterRequest() error = %v", err)
	}
	if request.ProtocolVersion != Version ||
		request.Operation != OperationRegister ||
		request.RelayActor != "https://relay.example/actor" ||
		request.PublicBaseURL != "https://relay.example" {
		t.Fatalf("register request = %#v", request)
	}
}

func TestDecodeRegisterRequestRejectsInvalidBodies(t *testing.T) {
	for _, test := range []struct {
		name string
		body string
		want error
	}{
		{name: "empty", body: "", want: ErrRegisterRequest},
		{name: "null", body: "null", want: ErrRegisterRequest},
		{name: "array", body: "[]", want: ErrRegisterRequest},
		{name: "malformed", body: "{", want: ErrRegisterRequest},
		{name: "trailing value", body: validRegisterBody + `{}`, want: ErrRegisterRequest},
		{name: "unknown field", body: `{"protocol_version":1,"operation":"register","relay_actor":"https://relay.example/actor","public_base_url":"https://relay.example","sensitive-field":"sensitive-value"}`, want: ErrRegisterRequest},
		{name: "duplicate version", body: `{"protocol_version":1,"protocol_version":1,"operation":"register","relay_actor":"https://relay.example/actor","public_base_url":"https://relay.example"}`, want: ErrRegisterRequest},
		{name: "duplicate operation", body: `{"protocol_version":1,"operation":"register","operation":"register","relay_actor":"https://relay.example/actor","public_base_url":"https://relay.example"}`, want: ErrRegisterRequest},
		{name: "escaped duplicate operation", body: `{"protocol_version":1,"operation":"register","opera\u0074ion":"register","relay_actor":"https://relay.example/actor","public_base_url":"https://relay.example"}`, want: ErrRegisterRequest},
		{name: "wrong version", body: `{"protocol_version":2,"operation":"register","relay_actor":"https://relay.example/actor","public_base_url":"https://relay.example"}`, want: ErrRegisterProtocolVersion},
		{name: "missing version", body: `{"operation":"register","relay_actor":"https://relay.example/actor","public_base_url":"https://relay.example"}`, want: ErrRegisterProtocolVersion},
		{name: "wrong operation", body: `{"protocol_version":1,"operation":"heartbeat","relay_actor":"https://relay.example/actor","public_base_url":"https://relay.example"}`, want: ErrRegisterRequest},
		{name: "missing actor", body: `{"protocol_version":1,"operation":"register","public_base_url":"https://relay.example"}`, want: ErrRegisterRequest},
		{name: "noncanonical actor", body: `{"protocol_version":1,"operation":"register","relay_actor":"HTTPS://Relay.Example.:443/%61ctor","public_base_url":"https://relay.example"}`, want: ErrRegisterRequest},
		{name: "noncanonical base", body: `{"protocol_version":1,"operation":"register","relay_actor":"https://relay.example/actor","public_base_url":"https://Relay.Example/"}`, want: ErrRegisterRequest},
		{name: "cross-origin identity", body: `{"protocol_version":1,"operation":"register","relay_actor":"https://relay.example/actor","public_base_url":"https://other.example"}`, want: ErrRegisterRequest},
		{name: "wrong version type", body: `{"protocol_version":"1","operation":"register","relay_actor":"https://relay.example/actor","public_base_url":"https://relay.example"}`, want: ErrRegisterRequest},
	} {
		t.Run(test.name, func(t *testing.T) {
			_, err := DecodeRegisterRequest(
				[]byte(test.body),
				MaximumRegisterBodyBytes,
			)
			if !errors.Is(err, test.want) {
				t.Fatalf("DecodeRegisterRequest() error = %v, want %v", err, test.want)
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

func TestDecodeRegisterRequestEnforcesBodyLimits(t *testing.T) {
	body := []byte(validRegisterBody)
	if _, err := DecodeRegisterRequest(
		body,
		int64(len(body)-1),
	); !errors.Is(err, ErrRegisterBodyTooLarge) {
		t.Fatalf("oversized body error = %v", err)
	}
	for _, limit := range []int64{0, -1, MaximumRegisterBodyBytes + 1} {
		if _, err := DecodeRegisterRequest(
			body,
			limit,
		); !errors.Is(err, ErrRegisterConfiguration) {
			t.Fatalf("limit %d error = %v", limit, err)
		}
	}
}

func TestVerifyRegisterAndReserveAcceptsCompleteRequest(t *testing.T) {
	body := []byte(validRegisterBody)
	request, key := signedRFC9421TestRequest(
		t,
		body,
		RFC9421POSTComponents(),
		nil,
	)
	resolver := newRFC9421TestResolver(&key.PublicKey)
	store := &recordingRFC9421ReplayStore{reserved: true}

	verified, err := newRFC9421TestVerifier(t, resolver).VerifyRegisterAndReserve(
		request,
		body,
		MaximumRegisterBodyBytes,
		store,
	)
	if err != nil {
		t.Fatalf("VerifyRegisterAndReserve() error = %v", err)
	}
	if verified.Request.RelayActor != "https://relay.example/actor" ||
		verified.Authentication == nil ||
		verified.Authentication.KeyActor != verified.Request.RelayActor {
		t.Fatalf("verified register request = %#v", verified)
	}
	if resolver.calls.Load() != 1 || store.calls.Load() != 1 {
		t.Fatalf(
			"resolver calls = %d, store calls = %d",
			resolver.calls.Load(),
			store.calls.Load(),
		)
	}
}

func TestVerifyRegisterAndReserveRejectsWrongTargetBeforeAuthentication(t *testing.T) {
	body := []byte(validRegisterBody)
	base, key := signedRFC9421TestRequest(
		t,
		body,
		RFC9421POSTComponents(),
		nil,
	)
	for _, test := range []struct {
		name string
		edit func(*http.Request)
	}{
		{name: "wrong method", edit: func(request *http.Request) { request.Method = http.MethodPut }},
		{name: "heartbeat path", edit: func(request *http.Request) { request.URL.Path = "/v1/relays/heartbeat" }},
		{name: "encoded path", edit: func(request *http.Request) { request.URL.RawPath = "/v1/%72elays/register" }},
		{name: "query", edit: func(request *http.Request) { request.URL.RawQuery = "sensitive=query" }},
		{name: "empty query", edit: func(request *http.Request) { request.URL.ForceQuery = true }},
		{name: "fragment", edit: func(request *http.Request) { request.URL.Fragment = "sensitive-fragment" }},
		{name: "inconsistent request URI", edit: func(request *http.Request) { request.RequestURI = "/v1/relays/register?sensitive=query" }},
	} {
		t.Run(test.name, func(t *testing.T) {
			request := base.Clone(base.Context())
			request.Header = base.Header.Clone()
			copiedURL := *base.URL
			request.URL = &copiedURL
			test.edit(request)
			resolver := newRFC9421TestResolver(&key.PublicKey)
			store := &recordingRFC9421ReplayStore{reserved: true}
			_, err := newRFC9421TestVerifier(t, resolver).VerifyRegisterAndReserve(
				request,
				body,
				MaximumRegisterBodyBytes,
				store,
			)
			if !errors.Is(err, ErrRegisterTarget) {
				t.Fatalf("VerifyRegisterAndReserve() error = %v", err)
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

func TestVerifyRegisterAndReserveRejectsMissingTarget(t *testing.T) {
	body := []byte(validRegisterBody)
	for _, test := range []struct {
		name    string
		request *http.Request
	}{
		{name: "nil request", request: nil},
		{name: "nil URL", request: &http.Request{Method: http.MethodPost}},
	} {
		t.Run(test.name, func(t *testing.T) {
			resolver := &rfc9421TestResolver{}
			store := &recordingRFC9421ReplayStore{reserved: true}
			_, err := newRFC9421TestVerifier(t, resolver).VerifyRegisterAndReserve(
				test.request,
				body,
				MaximumRegisterBodyBytes,
				store,
			)
			if !errors.Is(err, ErrRegisterTarget) {
				t.Fatalf("VerifyRegisterAndReserve() error = %v", err)
			}
			if resolver.calls.Load() != 0 || store.calls.Load() != 0 {
				t.Fatalf(
					"missing target resolved %d keys and reserved %d nonces",
					resolver.calls.Load(),
					store.calls.Load(),
				)
			}
		})
	}
}

func TestVerifyRegisterAndReserveRejectsBodyBeforeAuthentication(t *testing.T) {
	validBody := []byte(validRegisterBody)
	request, key := signedRFC9421TestRequest(
		t,
		validBody,
		RFC9421POSTComponents(),
		nil,
	)
	invalidBody := []byte(`{"protocol_version":1,"operation":"heartbeat"}`)
	resolver := newRFC9421TestResolver(&key.PublicKey)
	store := &recordingRFC9421ReplayStore{reserved: true}
	_, err := newRFC9421TestVerifier(t, resolver).VerifyRegisterAndReserve(
		request,
		invalidBody,
		MaximumRegisterBodyBytes,
		store,
	)
	if !errors.Is(err, ErrRegisterRequest) {
		t.Fatalf("VerifyRegisterAndReserve() error = %v", err)
	}
	if resolver.calls.Load() != 0 || store.calls.Load() != 0 {
		t.Fatalf(
			"invalid body resolved %d keys and reserved %d nonces",
			resolver.calls.Load(),
			store.calls.Load(),
		)
	}
}

func TestVerifyRegisterAndReserveRejectsReplay(t *testing.T) {
	body := []byte(validRegisterBody)
	request, key := signedRFC9421TestRequest(
		t,
		body,
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
	if _, err := verifier.VerifyRegisterAndReserve(
		request,
		body,
		MaximumRegisterBodyBytes,
		store,
	); err != nil {
		t.Fatalf("first register verification: %v", err)
	}
	if _, err := verifier.VerifyRegisterAndReserve(
		request,
		body,
		MaximumRegisterBodyBytes,
		store,
	); !errors.Is(err, ErrRFC9421Replay) {
		t.Fatalf("replay error = %v", err)
	}
}

func FuzzDecodeRegisterRequest(f *testing.F) {
	for _, seed := range []string{
		validRegisterBody,
		`{"protocol_version":1,"operation":"register"}`,
		`{"operation":"register","operation":"heartbeat"}`,
		`{"sensitive-field":"sensitive-value"}`,
		`[]`,
		`{`,
		"",
	} {
		f.Add([]byte(seed))
	}

	f.Fuzz(func(t *testing.T, body []byte) {
		request, err := DecodeRegisterRequest(body, 4096)
		if err != nil {
			for _, expected := range []error{
				ErrRegisterRequest,
				ErrRegisterBodyTooLarge,
				ErrRegisterProtocolVersion,
			} {
				if errors.Is(err, expected) {
					return
				}
			}
			t.Fatalf("unexpected error class: %v", err)
		}

		if request.ProtocolVersion != Version ||
			request.Operation != OperationRegister {
			t.Fatalf("accepted invalid envelope: %#v", request)
		}
		identity, identityErr := NormalizeRelayIdentity(
			request.RelayActor,
			request.PublicBaseURL,
		)
		if identityErr != nil ||
			identity.RelayActor != request.RelayActor ||
			identity.PublicBaseURL != request.PublicBaseURL {
			t.Fatalf("accepted noncanonical identity: %#v", request)
		}
	})
}
