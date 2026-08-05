package actorresolver

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"errors"
	"io"
	"math/big"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	v1 "github.com/thystra/activity-relay-directory/internal/protocol/v1"
)

const (
	testActorURL = "https://relay.example/actor"
	testKeyID    = testActorURL + "#main-key"
)

type roundTripFunc func(*http.Request) (*http.Response, error)

func (function roundTripFunc) RoundTrip(
	request *http.Request,
) (*http.Response, error) {
	return function(request)
}

func TestResolverResolvesBoundActorRSAKeys(t *testing.T) {
	pkixPEM, expected := loadTestPublicKey(t)
	pkcs1PEM := string(pem.EncodeToMemory(&pem.Block{
		Type:  "RSA PUBLIC KEY",
		Bytes: x509.MarshalPKCS1PublicKey(expected),
	}))

	for _, test := range []struct {
		name        string
		actorType   any
		contentType string
		publicKey   string
	}{
		{
			name:        "Application with ActivityStreams JSON",
			actorType:   "Application",
			contentType: "application/activity+json",
			publicKey:   pkixPEM,
		},
		{
			name:        "Service in a type set",
			actorType:   []string{"Object", "Service"},
			contentType: `application/activity+json; charset="UTF-8"`,
			publicKey:   pkixPEM,
		},
		{
			name:        "JSON-LD ActivityStreams profile",
			actorType:   "Application",
			contentType: `application/ld+json; profile="https://www.w3.org/ns/activitystreams"`,
			publicKey:   pkixPEM,
		},
		{
			name:        "legacy PKCS1 RSA public key",
			actorType:   "Service",
			contentType: "application/activity+json",
			publicKey:   pkcs1PEM,
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			body := testActorDocument(t, test.actorType, []any{
				map[string]any{
					"id":           "https://relay.example/actor#retired-key",
					"owner":        testActorURL,
					"publicKeyPem": test.publicKey,
				},
				map[string]any{
					"id":           testKeyID,
					"owner":        testActorURL,
					"publicKeyPem": test.publicKey,
				},
			})
			called := false
			resolver := newTestResolver(t, func(request *http.Request) (*http.Response, error) {
				called = true
				if request.Method != http.MethodGet || request.URL.String() != testActorURL ||
					request.URL.Fragment != "" {
					t.Fatalf("request = %s %q", request.Method, request.URL.String())
				}
				if request.Header.Get("Accept") != activityStreamsAccept ||
					request.Header.Get("User-Agent") != "directory-resolver-test" {
					t.Fatalf("request headers = %#v", request.Header)
				}
				return actorResponse(http.StatusOK, test.contentType, body), nil
			})

			resolved, err := resolver.ResolveRFC9421Key(
				context.Background(),
				testKeyID,
			)
			if err != nil {
				t.Fatalf("ResolveRFC9421Key() error = %v", err)
			}
			if !called || resolved.KeyID != testKeyID ||
				resolved.Owner != testActorURL || resolved.ActorID != testActorURL ||
				resolved.PublicKey == nil ||
				resolved.PublicKey.N.Cmp(expected.N) != 0 ||
				resolved.PublicKey.E != expected.E {
				t.Fatalf("resolved key = %#v", resolved)
			}
		})
	}
}

func TestResolverRejectsKeyIDBeforeFetching(t *testing.T) {
	called := false
	resolver := newTestResolver(t, func(*http.Request) (*http.Response, error) {
		called = true
		return nil, errors.New("unexpected fetch")
	})
	tooLong := "https://relay.example/actor#" + strings.Repeat("a", maximumKeyIDBytes)

	for _, keyID := range []string{
		"",
		"marker.invalid",
		"http://marker.invalid/actor#main-key",
		"https://marker.invalid/actor",
		"https://marker.invalid/actor#",
		"https://marker.invalid/actor?view=json#main-key",
		"https://MARKER.invalid/actor#main-key",
		"https://marker.invalid/actor#main/key",
		"https://marker.invalid/actor#main%2Dkey",
		"https://127.0.0.1/actor#main-key",
		"https://[::1]/actor#main-key",
		" https://marker.invalid/actor#main-key",
		tooLong,
	} {
		resolved, err := resolver.ResolveRFC9421Key(context.Background(), keyID)
		if resolved != (v1.RFC9421ResolvedKey{}) ||
			!(errors.Is(err, ErrKeyID) || errors.Is(err, ErrNetworkTarget)) {
			t.Fatalf("ResolveRFC9421Key(%q) = %#v, %v", keyID, resolved, err)
		}
		if strings.Contains(err.Error(), "marker.invalid") {
			t.Fatalf("error disclosed key ID: %v", err)
		}
	}
	if called {
		t.Fatal("invalid key ID triggered a fetch")
	}
}

func TestActorURLFromCanonicalKeyID(t *testing.T) {
	for _, test := range []struct {
		keyID string
		want  string
	}{
		{keyID: "https://relay.example/actor#main-key", want: "https://relay.example/actor"},
		{keyID: "https://relay.example:8443/actor#main-key", want: "https://relay.example:8443/actor"},
		{keyID: "https://8.8.8.8/actor#main-key", want: "https://8.8.8.8/actor"},
		{keyID: "https://[2606:4700:4700::1111]/actor#main-key", want: "https://[2606:4700:4700::1111]/actor"},
	} {
		got, err := actorURLFromKeyID(test.keyID)
		if err != nil || got != test.want {
			t.Fatalf("actorURLFromKeyID(%q) = %q, %v", test.keyID, got, err)
		}
	}
}

func TestResolverEnforcesResponseBoundary(t *testing.T) {
	validPEM, _ := loadTestPublicKey(t)
	validBody := testActorDocument(t, "Application", map[string]any{
		"id":           testKeyID,
		"owner":        testActorURL,
		"publicKeyPem": validPEM,
	})

	for _, test := range []struct {
		name     string
		response func() (*http.Response, error)
	}{
		{
			name: "transport error",
			response: func() (*http.Response, error) {
				return nil, errors.New("sensitive transport detail")
			},
		},
		{
			name: "nil response",
			response: func() (*http.Response, error) {
				return nil, nil
			},
		},
		{
			name: "non-success status",
			response: func() (*http.Response, error) {
				return actorResponse(http.StatusNotFound, "application/activity+json", validBody), nil
			},
		},
		{
			name: "generic JSON",
			response: func() (*http.Response, error) {
				return actorResponse(http.StatusOK, "application/json", validBody), nil
			},
		},
		{
			name: "JSON-LD without profile",
			response: func() (*http.Response, error) {
				return actorResponse(http.StatusOK, "application/ld+json", validBody), nil
			},
		},
		{
			name: "multiple content types",
			response: func() (*http.Response, error) {
				response := actorResponse(http.StatusOK, "application/activity+json", validBody)
				response.Header.Add("Content-Type", "application/activity+json")
				return response, nil
			},
		},
		{
			name: "declared oversized body",
			response: func() (*http.Response, error) {
				response := actorResponse(http.StatusOK, "application/activity+json", validBody)
				response.ContentLength = maximumActorBodyBytes + 1
				return response, nil
			},
		},
		{
			name: "streamed oversized body",
			response: func() (*http.Response, error) {
				return actorResponse(
					http.StatusOK,
					"application/activity+json",
					[]byte(strings.Repeat("x", maximumActorBodyBytes+1)),
				), nil
			},
		},
		{
			name: "empty body",
			response: func() (*http.Response, error) {
				return actorResponse(http.StatusOK, "application/activity+json", nil), nil
			},
		},
		{
			name: "body read error",
			response: func() (*http.Response, error) {
				response := actorResponse(http.StatusOK, "application/activity+json", nil)
				response.Body = io.NopCloser(errorReader{})
				return response, nil
			},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			resolver := newTestResolver(t, func(*http.Request) (*http.Response, error) {
				return test.response()
			})
			resolved, err := resolver.ResolveRFC9421Key(context.Background(), testKeyID)
			if resolved != (v1.RFC9421ResolvedKey{}) || !errors.Is(err, ErrActorFetch) {
				t.Fatalf("ResolveRFC9421Key() = %#v, %v", resolved, err)
			}
			if strings.Contains(err.Error(), "sensitive") {
				t.Fatalf("error disclosed transport detail: %v", err)
			}
		})
	}
}

func TestResolverRejectsAmbiguousActorDocuments(t *testing.T) {
	validPEM, _ := loadTestPublicKey(t)
	validKey := map[string]any{
		"id":           testKeyID,
		"owner":        testActorURL,
		"publicKeyPem": validPEM,
	}
	tooDeep := strings.Repeat(`{"nested":`, maximumActorJSONDepth+2) +
		`null` + strings.Repeat(`}`, maximumActorJSONDepth+2)

	for _, test := range []struct {
		name string
		body []byte
		err  error
	}{
		{name: "non-object", body: []byte(`[]`), err: ErrActorDocument},
		{name: "trailing value", body: append(append([]byte{}, testActorDocument(t, "Application", validKey)...), []byte(` {}`)...), err: ErrActorDocument},
		{name: "duplicate member", body: []byte(`{"id":"https://relay.example/actor","id":"https://relay.example/actor","type":"Application","publicKey":{}}`), err: ErrActorDocument},
		{name: "excessive depth", body: []byte(tooDeep), err: ErrActorDocument},
		{name: "wrong actor ID", body: actorDocumentWith(t, "https://relay.example/other", "Application", validKey), err: ErrActorDocument},
		{name: "person actor", body: testActorDocument(t, "Person", validKey), err: ErrActorDocument},
		{name: "missing matching key", body: testActorDocument(t, "Application", map[string]any{"id": testActorURL + "#other", "owner": testActorURL, "publicKeyPem": validPEM}), err: ErrActorDocument},
		{name: "wrong owner", body: testActorDocument(t, "Application", map[string]any{"id": testKeyID, "owner": "https://relay.example/other", "publicKeyPem": validPEM}), err: ErrActorDocument},
		{name: "duplicate matching key", body: testActorDocument(t, "Application", []any{validKey, validKey}), err: ErrActorDocument},
		{name: "too many keys", body: testActorDocument(t, "Application", repeatPublicKeys(validKey, maximumPublicKeys+1)), err: ErrActorDocument},
		{name: "invalid PEM", body: testActorDocument(t, "Application", map[string]any{"id": testKeyID, "owner": testActorURL, "publicKeyPem": "not a key"}), err: ErrPublicKey},
	} {
		t.Run(test.name, func(t *testing.T) {
			resolved, err := resolveActorDocument(test.body, testActorURL, testKeyID)
			if resolved != (v1.RFC9421ResolvedKey{}) || !errors.Is(err, test.err) {
				t.Fatalf("resolveActorDocument() = %#v, %v", resolved, err)
			}
		})
	}
}

func TestParseRSAPublicKeyRejectsUnsafeForms(t *testing.T) {
	validPEM, validKey := loadTestPublicKey(t)
	weakPrivate, err := rsa.GenerateKey(rand.Reader, 1024)
	if err != nil {
		t.Fatalf("generate weak RSA key: %v", err)
	}
	weakDER, err := x509.MarshalPKIXPublicKey(&weakPrivate.PublicKey)
	if err != nil {
		t.Fatalf("marshal weak RSA key: %v", err)
	}
	hugeModulus := new(big.Int).Lsh(big.NewInt(1), maximumRSAKeyBits)
	hugeModulus.Add(hugeModulus, big.NewInt(1))
	hugeDER, err := x509.MarshalPKIXPublicKey(&rsa.PublicKey{
		N: hugeModulus,
		E: validKey.E,
	})
	if err != nil {
		t.Fatalf("marshal oversized RSA key: %v", err)
	}
	evenExponentDER, err := x509.MarshalPKIXPublicKey(&rsa.PublicKey{
		N: validKey.N,
		E: 2,
	})
	if err != nil {
		t.Fatalf("marshal even-exponent RSA key: %v", err)
	}
	privateDER, err := x509.MarshalPKCS8PrivateKey(weakPrivate)
	if err != nil {
		t.Fatalf("marshal private RSA key: %v", err)
	}

	for _, test := range []struct {
		name  string
		value string
	}{
		{name: "empty"},
		{name: "leading whitespace", value: " \n" + validPEM},
		{name: "trailing text", value: validPEM + "not whitespace"},
		{name: "multiple blocks", value: validPEM + validPEM},
		{name: "PEM headers", value: string(pem.EncodeToMemory(&pem.Block{Type: "PUBLIC KEY", Headers: map[string]string{"Comment": "unexpected"}, Bytes: weakDER}))},
		{name: "weak key", value: string(pem.EncodeToMemory(&pem.Block{Type: "PUBLIC KEY", Bytes: weakDER}))},
		{name: "oversized modulus", value: string(pem.EncodeToMemory(&pem.Block{Type: "PUBLIC KEY", Bytes: hugeDER}))},
		{name: "even exponent", value: string(pem.EncodeToMemory(&pem.Block{Type: "PUBLIC KEY", Bytes: evenExponentDER}))},
		{name: "private key", value: string(pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: privateDER}))},
		{name: "oversized PEM", value: strings.Repeat("x", maximumPublicKeyPEM+1)},
	} {
		t.Run(test.name, func(t *testing.T) {
			publicKey, err := parseRSAPublicKey(test.value)
			if publicKey != nil || !errors.Is(err, ErrPublicKey) {
				t.Fatalf("parseRSAPublicKey() = %#v, %v", publicKey, err)
			}
		})
	}
}

func TestResolverPreservesContextCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	resolver := newTestResolver(t, func(*http.Request) (*http.Response, error) {
		t.Fatal("canceled resolution reached transport")
		return nil, nil
	})
	resolved, err := resolver.ResolveRFC9421Key(ctx, testKeyID)
	if resolved != (v1.RFC9421ResolvedKey{}) || !errors.Is(err, ErrActorFetch) ||
		!errors.Is(err, context.Canceled) {
		t.Fatalf("canceled resolution = %#v, %v", resolved, err)
	}
}

func TestResolverConfiguration(t *testing.T) {
	for _, userAgent := range []string{
		"",
		" resolver",
		"resolver ",
		"resolver\r\ninjected",
		"resolver\x00injected",
		"résolveur",
		strings.Repeat("x", maximumUserAgentBytes+1),
	} {
		resolver, err := New(userAgent)
		if resolver != nil || !errors.Is(err, ErrConfiguration) {
			t.Fatalf("New(%q) = %#v, %v", userAgent, resolver, err)
		}
	}
	if resolver, err := newResolver("valid", nil); resolver != nil ||
		!errors.Is(err, ErrConfiguration) {
		t.Fatalf("newResolver(nil client) = %#v, %v", resolver, err)
	}
	resolver, err := New("Activity-Relay-Directory/test")
	if err != nil || resolver == nil {
		t.Fatalf("New(valid) = %#v, %v", resolver, err)
	}
}

func TestActivityStreamsContentTypes(t *testing.T) {
	for _, test := range []struct {
		value string
		valid bool
	}{
		{value: "application/activity+json", valid: true},
		{value: `application/activity+json; profile="https://www.w3.org/ns/activitystreams"`, valid: true},
		{value: `application/activity+json; profile="https://example.test/extension"`, valid: true},
		{value: `application/ld+json; profile="https://example.test/other https://www.w3.org/ns/activitystreams"`, valid: true},
		{value: `application/ld+json; charset=utf-8; profile="https://www.w3.org/ns/activitystreams"`, valid: true},
		{value: "application/json", valid: false},
		{value: "application/ld+json", valid: false},
		{value: `application/ld+json; profile="https://example.test/other"`, valid: false},
		{value: `application/activity+json; charset=iso-8859-1`, valid: false},
		{value: `application/activity+json; boundary=unexpected`, valid: false},
	} {
		if got := validActivityStreamsContentType([]string{test.value}); got != test.valid {
			t.Fatalf("validActivityStreamsContentType(%q) = %t", test.value, got)
		}
	}
	if validActivityStreamsContentType(nil) ||
		validActivityStreamsContentType([]string{"application/activity+json", "application/activity+json"}) {
		t.Fatal("missing or repeated Content-Type was accepted")
	}
}

type errorReader struct{}

func (errorReader) Read([]byte) (int, error) {
	return 0, errors.New("sensitive read detail")
}

func newTestResolver(
	t *testing.T,
	roundTrip roundTripFunc,
) *Resolver {
	t.Helper()
	resolver, err := newResolver(
		"directory-resolver-test",
		&http.Client{Transport: roundTrip},
	)
	if err != nil {
		t.Fatalf("newResolver() error = %v", err)
	}
	return resolver
}

func actorResponse(status int, contentType string, body []byte) *http.Response {
	header := make(http.Header)
	if contentType != "" {
		header.Set("Content-Type", contentType)
	}
	return &http.Response{
		StatusCode:    status,
		Header:        header,
		Body:          io.NopCloser(strings.NewReader(string(body))),
		ContentLength: int64(len(body)),
	}
}

func testActorDocument(t *testing.T, actorType any, publicKey any) []byte {
	t.Helper()
	return actorDocumentWith(t, testActorURL, actorType, publicKey)
}

func actorDocumentWith(
	t *testing.T,
	id string,
	actorType any,
	publicKey any,
) []byte {
	t.Helper()
	body, err := json.Marshal(map[string]any{
		"@context": []any{
			"https://www.w3.org/ns/activitystreams",
			map[string]any{"custom": map[string]any{"nested": true}},
		},
		"id":        id,
		"type":      actorType,
		"inbox":     testActorURL + "/inbox",
		"publicKey": publicKey,
	})
	if err != nil {
		t.Fatalf("marshal actor document: %v", err)
	}
	return body
}

func repeatPublicKeys(key map[string]any, count int) []any {
	keys := make([]any, count)
	for index := range keys {
		copy := make(map[string]any, len(key))
		for name, value := range key {
			copy[name] = value
		}
		copy["id"] = testActorURL + "#other-" + strings.Repeat("x", index+1)
		keys[index] = copy
	}
	return keys
}

func loadTestPublicKey(t *testing.T) (string, *rsa.PublicKey) {
	t.Helper()
	body, err := os.ReadFile(filepath.Join(
		"..", "..", "testdata", "directory", "v1", "rfc9421-register.valid.json",
	))
	if err != nil {
		t.Fatalf("read RFC 9421 fixture: %v", err)
	}
	var fixture struct {
		PublicKeyPEM string `json:"public_key_pem"`
	}
	if err := json.Unmarshal(body, &fixture); err != nil {
		t.Fatalf("decode RFC 9421 fixture: %v", err)
	}
	block, trailing := pem.Decode([]byte(fixture.PublicKeyPEM))
	if block == nil || len(trailing) != 0 {
		t.Fatal("fixture contains invalid public key PEM")
	}
	parsed, err := x509.ParsePKIXPublicKey(block.Bytes)
	if err != nil {
		t.Fatalf("parse fixture public key: %v", err)
	}
	publicKey, ok := parsed.(*rsa.PublicKey)
	if !ok {
		t.Fatalf("fixture public key type = %T", parsed)
	}
	return fixture.PublicKeyPEM, publicKey
}
