package v1

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/pem"
	"errors"
	"net/http"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/common-fate/httpsig/alg_rsa"
	"github.com/common-fate/httpsig/contentdigest"
	"github.com/common-fate/httpsig/sigbase"
	"github.com/common-fate/httpsig/signature"
	"github.com/common-fate/httpsig/sigparams"
	"github.com/common-fate/httpsig/sigset"
	"github.com/dunglas/httpsfv"
)

var (
	rfc9421TestKeyOnce sync.Once
	rfc9421TestKey     *rsa.PrivateKey
	rfc9421TestKeyErr  error
)

var (
	rfc9421TestCreated = time.Date(2026, 8, 4, 18, 15, 0, 0, time.UTC)
	rfc9421TestNow     = time.Date(2026, 8, 4, 18, 16, 0, 0, time.UTC)
)

type rfc9421Fixture struct {
	Method         string `json:"method"`
	Scheme         string `json:"scheme"`
	Authority      string `json:"authority"`
	Target         string `json:"target"`
	ContentType    string `json:"content_type"`
	ContentDigest  string `json:"content_digest"`
	Date           string `json:"date"`
	Body           string `json:"body"`
	SignatureInput string `json:"signature_input"`
	Signature      string `json:"signature"`
	KeyID          string `json:"key_id"`
	KeyOwner       string `json:"key_owner"`
	KeyActor       string `json:"key_actor"`
	PublicKeyPEM   string `json:"public_key_pem"`
}

type rfc9421TestResolver struct {
	resolved RFC9421ResolvedKey
	err      error
	calls    atomic.Int32
}

func (resolver *rfc9421TestResolver) ResolveRFC9421Key(
	context.Context,
	string,
) (RFC9421ResolvedKey, error) {
	resolver.calls.Add(1)
	return resolver.resolved, resolver.err
}

func testRFC9421PrivateKey(t *testing.T) *rsa.PrivateKey {
	t.Helper()
	rfc9421TestKeyOnce.Do(func() {
		rfc9421TestKey, rfc9421TestKeyErr = rsa.GenerateKey(rand.Reader, 2048)
	})
	if rfc9421TestKeyErr != nil {
		t.Fatalf("generate RFC 9421 test key: %v", rfc9421TestKeyErr)
	}
	return rfc9421TestKey
}

func newRFC9421TestResolver(key *rsa.PublicKey) *rfc9421TestResolver {
	return &rfc9421TestResolver{resolved: RFC9421ResolvedKey{
		KeyID:     "https://relay.example/actor#main-key",
		Owner:     "https://relay.example/actor",
		ActorID:   "https://relay.example/actor",
		PublicKey: key,
	}}
}

func newRFC9421TestVerifier(
	t *testing.T,
	resolver RFC9421KeyResolver,
) *RFC9421Verifier {
	t.Helper()
	verifier, err := NewRFC9421Verifier(RFC9421VerifierOptions{
		Authority:   "directory.example",
		KeyResolver: resolver,
		Now: func() time.Time {
			return rfc9421TestNow
		},
	})
	if err != nil {
		t.Fatalf("create RFC 9421 verifier: %v", err)
	}
	return verifier
}

func signedRFC9421TestRequest(
	t *testing.T,
	body []byte,
	components []string,
	mutate func(*sigparams.Params),
) (*http.Request, *rsa.PrivateKey) {
	t.Helper()

	key := testRFC9421PrivateKey(t)
	request, err := http.NewRequest(
		http.MethodPost,
		"https://directory.example/v1/relays/register",
		bytes.NewReader(body),
	)
	if err != nil {
		t.Fatalf("create signed request: %v", err)
	}
	request.Host = "directory.example"
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Date", rfc9421TestCreated.Format(http.TimeFormat))
	for _, component := range components {
		if component == "x-directory-test" {
			request.Header.Set(component, "covered")
		}
	}
	digest, err := RFC9530ContentDigestSHA256(body)
	if err != nil {
		t.Fatalf("create test digest: %v", err)
	}
	request.Header.Set("Content-Digest", digest)

	params := sigparams.Params{
		CoveredComponents: append([]string(nil), components...),
		KeyID:             "https://relay.example/actor#main-key",
		Alg:               RFC9421SignatureAlgorithm,
		Tag:               RFC9421SignatureTag,
		Nonce:             "directory-test-nonce",
		Created:           rfc9421TestCreated,
		Expires:           rfc9421TestCreated.Add(3 * time.Minute),
	}
	if mutate != nil {
		mutate(&params)
	}

	base, err := sigbase.Derive(params, nil, request, contentdigest.SHA256)
	if err != nil {
		t.Fatalf("derive test signature base: %v", err)
	}
	canonical, err := base.CanonicalString(params)
	if err != nil {
		t.Fatalf("serialize test signature base: %v", err)
	}
	signed, err := alg_rsa.NewRSAPKCS256Signer(key).Sign(
		request.Context(),
		canonical,
	)
	if err != nil {
		t.Fatalf("sign test request: %v", err)
	}
	set := sigset.Set{Messages: map[string]*signature.Message{
		"directory": {Input: params, Signature: signed},
	}}
	if err := set.Include(request); err != nil {
		t.Fatalf("include test signature: %v", err)
	}

	return request, key
}

func parseFixtureRSAPublicKey(t *testing.T, value string) *rsa.PublicKey {
	t.Helper()
	block, trailing := pem.Decode([]byte(value))
	if block == nil || len(trailing) != 0 || block.Type != "PUBLIC KEY" {
		t.Fatal("fixture public key is not one PEM public-key block")
	}
	parsed, err := x509.ParsePKIXPublicKey(block.Bytes)
	if err != nil {
		t.Fatalf("parse fixture public key: %v", err)
	}
	key, ok := parsed.(*rsa.PublicKey)
	if !ok {
		t.Fatalf("fixture public key type = %T", parsed)
	}
	return key
}

func TestRFC9421FixtureVerifiesAndBindsRelayActor(t *testing.T) {
	fixture := decodeFixture[rfc9421Fixture](t, "rfc9421-register.valid.json")
	request, err := http.NewRequest(
		fixture.Method,
		fixture.Scheme+"://"+fixture.Authority+fixture.Target,
		bytes.NewBufferString(fixture.Body),
	)
	if err != nil {
		t.Fatalf("create fixture request: %v", err)
	}
	request.Host = fixture.Authority
	request.Header.Set("Content-Type", fixture.ContentType)
	request.Header.Set("Content-Digest", fixture.ContentDigest)
	request.Header.Set("Date", fixture.Date)
	request.Header.Set("Signature-Input", fixture.SignatureInput)
	request.Header.Set("Signature", fixture.Signature)

	resolver := &rfc9421TestResolver{resolved: RFC9421ResolvedKey{
		KeyID:     fixture.KeyID,
		Owner:     fixture.KeyOwner,
		ActorID:   fixture.KeyActor,
		PublicKey: parseFixtureRSAPublicKey(t, fixture.PublicKeyPEM),
	}}
	verifier := newRFC9421TestVerifier(t, resolver)
	result, err := verifier.VerifyPOST(request, []byte(fixture.Body))
	if err != nil {
		t.Fatalf("verify RFC 9421 fixture: %v", err)
	}
	if result.KeyID != fixture.KeyID ||
		result.Nonce != "directory-fixture-nonce-20260804" ||
		result.SignatureAlgorithm != RFC9421SignatureAlgorithm {
		t.Fatalf("verification result = %#v", result)
	}
	if !slices.Equal(result.CoveredComponents, RFC9421POSTComponents()) {
		t.Fatalf("covered components = %q", result.CoveredComponents)
	}
	if err := result.BindRelayActor(fixture.KeyActor); err != nil {
		t.Fatalf("bind relay actor: %v", err)
	}
	if resolver.calls.Load() != 1 {
		t.Fatalf("key resolver calls = %d, want 1", resolver.calls.Load())
	}
}

func TestNewRFC9421VerifierRejectsInvalidConfiguration(t *testing.T) {
	resolver := RFC9421KeyResolverFunc(func(
		context.Context,
		string,
	) (RFC9421ResolvedKey, error) {
		return RFC9421ResolvedKey{}, nil
	})
	for _, options := range []RFC9421VerifierOptions{
		{},
		{Authority: "directory.example"},
		{Authority: " directory.example", KeyResolver: resolver},
		{Authority: "Directory.Example", KeyResolver: resolver},
		{Authority: "directory", KeyResolver: resolver},
		{Authority: "user@directory.example", KeyResolver: resolver},
	} {
		if _, err := NewRFC9421Verifier(options); !errors.Is(
			err,
			ErrRFC9421Configuration,
		) {
			t.Fatalf("NewRFC9421Verifier(%#v) error = %v", options, err)
		}
	}
}

func TestRFC9421VerifierEnforcesRequestProfile(t *testing.T) {
	body := []byte(`{"operation":"register"}`)
	base, key := signedRFC9421TestRequest(
		t,
		body,
		RFC9421POSTComponents(),
		nil,
	)
	for _, test := range []struct {
		name  string
		want  error
		edits func(*http.Request)
	}{
		{name: "wrong method", want: ErrRFC9421Policy, edits: func(r *http.Request) { r.Method = http.MethodGet }},
		{name: "wrong authority", want: ErrRFC9421Policy, edits: func(r *http.Request) { r.Host = "other.example" }},
		{name: "wrong URL host", want: ErrRFC9421Policy, edits: func(r *http.Request) { r.URL.Host = "other.example" }},
		{name: "wrong scheme", want: ErrRFC9421Policy, edits: func(r *http.Request) { r.URL.Scheme = "http" }},
		{name: "fragment", want: ErrRFC9421Policy, edits: func(r *http.Request) { r.URL.Fragment = "client-only" }},
		{name: "wrong content type", want: ErrRFC9421Policy, edits: func(r *http.Request) { r.Header.Set("Content-Type", "application/json; charset=utf-8") }},
		{name: "repeated content type", want: ErrRFC9421Policy, edits: func(r *http.Request) { r.Header.Add("Content-Type", "application/json") }},
		{name: "missing date", want: ErrRFC9421Time, edits: func(r *http.Request) { r.Header.Del("Date") }},
		{name: "malformed date", want: ErrRFC9421Time, edits: func(r *http.Request) { r.Header.Set("Date", "secret-date-value") }},
		{name: "stale date", want: ErrRFC9421Time, edits: func(r *http.Request) {
			r.Header.Set("Date", rfc9421TestNow.Add(-6*time.Minute).Format(http.TimeFormat))
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			request := base.Clone(base.Context())
			request.Header = base.Header.Clone()
			copiedURL := *base.URL
			request.URL = &copiedURL
			test.edits(request)
			resolver := newRFC9421TestResolver(&key.PublicKey)
			_, err := newRFC9421TestVerifier(t, resolver).VerifyPOST(request, body)
			if !errors.Is(err, test.want) {
				t.Fatalf("VerifyPOST() error = %v, want %v", err, test.want)
			}
			if resolver.calls.Load() != 0 {
				t.Fatalf("invalid request resolved %d keys", resolver.calls.Load())
			}
		})
	}
}

func TestRFC9421VerifierStrictlyParsesSignatureFields(t *testing.T) {
	body := []byte(`{"operation":"register"}`)
	base, key := signedRFC9421TestRequest(t, body, RFC9421POSTComponents(), nil)
	for _, test := range []struct {
		name  string
		want  error
		edits func(http.Header)
	}{
		{name: "missing input", want: ErrRFC9421Malformed, edits: func(h http.Header) { h.Del("Signature-Input") }},
		{name: "missing signature", want: ErrRFC9421Malformed, edits: func(h http.Header) { h.Del("Signature") }},
		{name: "malformed input", want: ErrRFC9421Malformed, edits: func(h http.Header) { h.Set("Signature-Input", "secret malformed input") }},
		{name: "input is not inner list", want: ErrRFC9421Malformed, edits: func(h http.Header) { h.Set("Signature-Input", `directory="not-an-inner-list"`) }},
		{name: "mismatched labels", want: ErrRFC9421Policy, edits: func(h http.Header) {
			h.Set("Signature", strings.Replace(h.Get("Signature"), "directory=", "other=", 1))
		}},
		{name: "component parameters", want: ErrRFC9421Malformed, edits: func(h http.Header) {
			h.Set("Signature-Input", strings.Replace(h.Get("Signature-Input"), `"date")`, `"date";sf)`, 1))
		}},
		{name: "unknown signature parameter", want: ErrRFC9421Policy, edits: func(h http.Header) { h.Set("Signature-Input", h.Get("Signature-Input")+`;secret="value"`) }},
		{name: "signature parameters", want: ErrRFC9421Malformed, edits: func(h http.Header) { h.Set("Signature", h.Get("Signature")+";test=1") }},
		{name: "multiple signatures", want: ErrRFC9421Policy, edits: func(h http.Header) {
			h.Set("Signature-Input", h.Get("Signature-Input")+`, other=("@method");created=1785867300`)
			h.Set("Signature", h.Get("Signature")+", other=:AA==:")
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			request := base.Clone(base.Context())
			request.Header = base.Header.Clone()
			test.edits(request.Header)
			resolver := newRFC9421TestResolver(&key.PublicKey)
			_, err := newRFC9421TestVerifier(t, resolver).VerifyPOST(request, body)
			if !errors.Is(err, test.want) {
				t.Fatalf("VerifyPOST() error = %v, want %v", err, test.want)
			}
			if resolver.calls.Load() != 0 {
				t.Fatalf("invalid fields resolved %d keys", resolver.calls.Load())
			}
		})
	}
}

func TestRFC9421VerifierEnforcesSignatureParameters(t *testing.T) {
	body := []byte(`{"operation":"register"}`)
	for _, test := range []struct {
		name   string
		want   error
		mutate func(*sigparams.Params)
	}{
		{name: "wrong tag", want: ErrRFC9421Policy, mutate: func(p *sigparams.Params) { p.Tag = "activitypub" }},
		{name: "wrong algorithm", want: ErrRFC9421Policy, mutate: func(p *sigparams.Params) { p.Alg = "rsa-pss-sha512" }},
		{name: "missing key ID", want: ErrRFC9421Policy, mutate: func(p *sigparams.Params) { p.KeyID = "" }},
		{name: "long key ID", want: ErrRFC9421Policy, mutate: func(p *sigparams.Params) { p.KeyID = strings.Repeat("k", maxRFC9421KeyIDBytes+1) }},
		{name: "missing nonce", want: ErrRFC9421Policy, mutate: func(p *sigparams.Params) { p.Nonce = "" }},
		{name: "long nonce", want: ErrRFC9421Policy, mutate: func(p *sigparams.Params) { p.Nonce = strings.Repeat("n", maxRFC9421NonceBytes+1) }},
		{name: "missing created", want: ErrRFC9421Time, mutate: func(p *sigparams.Params) { p.Created = time.Time{} }},
		{name: "missing expires", want: ErrRFC9421Time, mutate: func(p *sigparams.Params) { p.Expires = time.Time{} }},
		{name: "stale created", want: ErrRFC9421Time, mutate: func(p *sigparams.Params) {
			p.Created = rfc9421TestNow.Add(-RFC9421MaximumAge - time.Second)
			p.Expires = rfc9421TestNow.Add(time.Minute)
		}},
		{name: "future created", want: ErrRFC9421Time, mutate: func(p *sigparams.Params) {
			p.Created = rfc9421TestNow.Add(RFC9421FutureSkew + time.Second)
			p.Expires = p.Created.Add(time.Minute)
		}},
		{name: "expires at created", want: ErrRFC9421Time, mutate: func(p *sigparams.Params) { p.Expires = p.Created }},
		{name: "expired", want: ErrRFC9421Time, mutate: func(p *sigparams.Params) { p.Expires = rfc9421TestNow }},
		{name: "long lifetime", want: ErrRFC9421Time, mutate: func(p *sigparams.Params) { p.Expires = p.Created.Add(RFC9421MaximumLifetime + time.Second) }},
	} {
		t.Run(test.name, func(t *testing.T) {
			request, key := signedRFC9421TestRequest(
				t,
				body,
				RFC9421POSTComponents(),
				test.mutate,
			)
			resolver := newRFC9421TestResolver(&key.PublicKey)
			_, err := newRFC9421TestVerifier(t, resolver).VerifyPOST(request, body)
			if !errors.Is(err, test.want) {
				t.Fatalf("VerifyPOST() error = %v, want %v", err, test.want)
			}
			if resolver.calls.Load() != 0 {
				t.Fatalf("invalid parameters resolved %d keys", resolver.calls.Load())
			}
		})
	}
}

func TestRFC9421VerifierPreservesSignatureParameterOrder(t *testing.T) {
	body := []byte(`{"operation":"register"}`)
	request, key := signedRFC9421TestRequest(t, body, RFC9421POSTComponents(), nil)

	dictionary, err := httpsfv.UnmarshalDictionary(
		request.Header.Values("Signature-Input"),
	)
	if err != nil {
		t.Fatalf("parse generated Signature-Input: %v", err)
	}
	member, _ := dictionary.Get("directory")
	innerList, ok := member.(httpsfv.InnerList)
	if !ok {
		t.Fatalf("Signature-Input member type = %T", member)
	}
	reordered := httpsfv.InnerList{
		Items:  innerList.Items,
		Params: httpsfv.NewParams(),
	}
	for _, name := range []string{
		"created",
		"expires",
		"keyid",
		"alg",
		"tag",
		"nonce",
	} {
		value, found := innerList.Params.Get(name)
		if !found {
			t.Fatalf("generated Signature-Input has no %s parameter", name)
		}
		reordered.Params.Add(name, value)
	}
	inputDictionary := httpsfv.NewDictionary()
	inputDictionary.Add("directory", reordered)
	inputValue, err := httpsfv.Marshal(inputDictionary)
	if err != nil {
		t.Fatalf("marshal reordered Signature-Input: %v", err)
	}
	request.Header.Set("Signature-Input", inputValue)

	params, err := sigparams.UnmarshalInnerList(reordered)
	if err != nil {
		t.Fatalf("parse reordered parameters: %v", err)
	}
	serializedParams, err := httpsfv.Marshal(reordered)
	if err != nil {
		t.Fatalf("serialize reordered parameters: %v", err)
	}
	canonical, err := canonicalRFC9421SigningString(
		request,
		"directory.example",
		params.CoveredComponents,
		serializedParams,
	)
	if err != nil {
		t.Fatalf("create reordered signing string: %v", err)
	}
	signed, err := alg_rsa.NewRSAPKCS256Signer(key).Sign(
		request.Context(),
		canonical,
	)
	if err != nil {
		t.Fatalf("sign reordered parameters: %v", err)
	}
	signatureDictionary := httpsfv.NewDictionary()
	signatureDictionary.Add("directory", httpsfv.NewItem(signed))
	signatureValue, err := httpsfv.Marshal(signatureDictionary)
	if err != nil {
		t.Fatalf("marshal reordered signature: %v", err)
	}
	request.Header.Set("Signature", signatureValue)

	resolver := newRFC9421TestResolver(&key.PublicKey)
	if _, err := newRFC9421TestVerifier(t, resolver).VerifyPOST(
		request,
		body,
	); err != nil {
		t.Fatalf("verify reordered signature parameters: %v", err)
	}
}

func TestRFC9421VerifierRequiresComponentsAndAllowsAdditionalCoverage(t *testing.T) {
	body := []byte(`{"operation":"register"}`)
	missingDate := RFC9421POSTComponents()[:len(RFC9421POSTComponents())-1]
	request, key := signedRFC9421TestRequest(t, body, missingDate, nil)
	resolver := newRFC9421TestResolver(&key.PublicKey)
	if _, err := newRFC9421TestVerifier(t, resolver).VerifyPOST(
		request,
		body,
	); !errors.Is(err, ErrRFC9421Policy) {
		t.Fatalf("missing component error = %v", err)
	}

	additional := append(RFC9421POSTComponents(), "x-directory-test")
	request, key = signedRFC9421TestRequest(t, body, additional, nil)
	resolver = newRFC9421TestResolver(&key.PublicKey)
	result, err := newRFC9421TestVerifier(t, resolver).VerifyPOST(request, body)
	if err != nil {
		t.Fatalf("verify additional coverage: %v", err)
	}
	if !slices.Equal(result.CoveredComponents, additional) {
		t.Fatalf("covered components = %q, want %q", result.CoveredComponents, additional)
	}

	request, key = signedRFC9421TestRequest(t, body, RFC9421POSTComponents(), nil)
	request.Header.Set(
		"Signature-Input",
		strings.Replace(
			request.Header.Get("Signature-Input"),
			`"date")`,
			`"date" "date")`,
			1,
		),
	)
	resolver = newRFC9421TestResolver(&key.PublicKey)
	if _, err := newRFC9421TestVerifier(t, resolver).VerifyPOST(
		request,
		body,
	); !errors.Is(err, ErrRFC9421Policy) {
		t.Fatalf("duplicate component error = %v", err)
	}
}

func TestRFC9421VerifierChecksDigestBeforeResolvingKey(t *testing.T) {
	body := []byte(`{"operation":"register"}`)
	request, key := signedRFC9421TestRequest(t, body, RFC9421POSTComponents(), nil)
	resolver := newRFC9421TestResolver(&key.PublicKey)
	_, err := newRFC9421TestVerifier(t, resolver).VerifyPOST(
		request,
		[]byte(`{"operation":"heartbeat"}`),
	)
	if !errors.Is(err, ErrRFC9421Digest) {
		t.Fatalf("tampered body error = %v", err)
	}
	if resolver.calls.Load() != 0 {
		t.Fatalf("tampered body resolved %d keys", resolver.calls.Load())
	}
}

func TestRFC9421VerifierSignsThePresentedContentDigestField(t *testing.T) {
	body := []byte(`{"operation":"register"}`)
	request, key := signedRFC9421TestRequest(t, body, RFC9421POSTComponents(), nil)
	request.Header.Set(
		"Content-Digest",
		"sha-512=:AA==:, "+request.Header.Get("Content-Digest"),
	)

	resolver := newRFC9421TestResolver(&key.PublicKey)
	_, err := newRFC9421TestVerifier(t, resolver).VerifyPOST(request, body)
	if !errors.Is(err, ErrRFC9421Crypto) {
		t.Fatalf("modified Content-Digest field error = %v", err)
	}
	if resolver.calls.Load() != 1 {
		t.Fatalf("key resolver calls = %d, want 1", resolver.calls.Load())
	}
}

func TestRFC9421VerifierValidatesResolvedKey(t *testing.T) {
	body := []byte(`{"operation":"register"}`)
	request, key := signedRFC9421TestRequest(t, body, RFC9421POSTComponents(), nil)
	weakKey, err := rsa.GenerateKey(rand.Reader, 1024)
	if err != nil {
		t.Fatalf("generate weak test key: %v", err)
	}
	for _, test := range []struct {
		name     string
		resolved RFC9421ResolvedKey
		err      error
	}{
		{name: "resolver error", err: errors.New("secret resolver detail")},
		{name: "wrong key ID", resolved: RFC9421ResolvedKey{KeyID: "https://relay.example/other#key", Owner: "https://relay.example/actor", ActorID: "https://relay.example/actor", PublicKey: &key.PublicKey}},
		{name: "nil key", resolved: RFC9421ResolvedKey{KeyID: "https://relay.example/actor#main-key", Owner: "https://relay.example/actor", ActorID: "https://relay.example/actor"}},
		{name: "weak key", resolved: RFC9421ResolvedKey{KeyID: "https://relay.example/actor#main-key", Owner: "https://relay.example/actor", ActorID: "https://relay.example/actor", PublicKey: &weakKey.PublicKey}},
		{name: "noncanonical owner", resolved: RFC9421ResolvedKey{KeyID: "https://relay.example/actor#main-key", Owner: "HTTPS://RELAY.EXAMPLE/actor", ActorID: "https://relay.example/actor", PublicKey: &key.PublicKey}},
		{name: "actor mismatch", resolved: RFC9421ResolvedKey{KeyID: "https://relay.example/actor#main-key", Owner: "https://relay.example/actor", ActorID: "https://relay.example/other", PublicKey: &key.PublicKey}},
	} {
		t.Run(test.name, func(t *testing.T) {
			resolver := &rfc9421TestResolver{resolved: test.resolved, err: test.err}
			_, err := newRFC9421TestVerifier(t, resolver).VerifyPOST(request, body)
			if !errors.Is(err, ErrRFC9421Key) {
				t.Fatalf("VerifyPOST() error = %v", err)
			}
		})
	}
}

func TestRFC9421VerifierRejectsInvalidSignature(t *testing.T) {
	body := []byte(`{"operation":"register"}`)
	request, key := signedRFC9421TestRequest(t, body, RFC9421POSTComponents(), nil)
	dictionary := httpsfv.NewDictionary()
	dictionary.Add("directory", httpsfv.NewItem(make([]byte, key.Size())))
	value, err := httpsfv.Marshal(dictionary)
	if err != nil {
		t.Fatalf("marshal invalid signature: %v", err)
	}
	request.Header.Set("Signature", value)

	resolver := newRFC9421TestResolver(&key.PublicKey)
	if _, err := newRFC9421TestVerifier(t, resolver).VerifyPOST(
		request,
		body,
	); !errors.Is(err, ErrRFC9421Crypto) {
		t.Fatalf("invalid signature error = %v", err)
	}
}

func TestRFC9421VerificationBindsCanonicalRelayActor(t *testing.T) {
	result := &RFC9421Verification{
		KeyOwner: "https://relay.example/actor",
		KeyActor: "https://relay.example/actor",
	}
	if err := result.BindRelayActor("https://relay.example/actor"); err != nil {
		t.Fatalf("bind canonical actor: %v", err)
	}
	for _, actor := range []string{
		"HTTPS://RELAY.EXAMPLE/actor",
		"https://relay.example/other",
		"https://other.example/actor",
		"secret invalid actor",
	} {
		if err := result.BindRelayActor(actor); !errors.Is(
			err,
			ErrRFC9421ActorBinding,
		) {
			t.Fatalf("BindRelayActor(%q) error = %v", actor, err)
		}
	}
	if err := (*RFC9421Verification)(nil).BindRelayActor(
		"https://relay.example/actor",
	); !errors.Is(err, ErrRFC9421ActorBinding) {
		t.Fatalf("nil verification error = %v", err)
	}
}

func TestRFC9421ErrorsDoNotEchoUntrustedValues(t *testing.T) {
	sensitiveKeyID := "https://relay.example/actor#sensitive-key-marker"
	sensitiveNonce := "sensitive-nonce-marker"
	body := []byte(`{"operation":"register"}`)
	request, _ := signedRFC9421TestRequest(
		t,
		body,
		RFC9421POSTComponents(),
		func(params *sigparams.Params) {
			params.KeyID = sensitiveKeyID
			params.Nonce = sensitiveNonce
		},
	)
	resolver := &rfc9421TestResolver{err: errors.New("sensitive resolver detail")}
	_, err := newRFC9421TestVerifier(t, resolver).VerifyPOST(request, body)
	if !errors.Is(err, ErrRFC9421Key) {
		t.Fatalf("verification error = %v", err)
	}
	for _, sensitive := range []string{sensitiveKeyID, sensitiveNonce, "sensitive resolver detail"} {
		if strings.Contains(err.Error(), sensitive) {
			t.Fatalf("error discloses untrusted value %q: %v", sensitive, err)
		}
	}
}

func FuzzParseRFC9421Message(f *testing.F) {
	for _, seed := range [][2]string{
		{"", ""},
		{"not structured", "also not structured"},
		{`directory=("@method");tag="activity-relay-directory-v1"`, "directory=:AA==:"},
		{`directory=("@method" "@authority" "@target-uri" "content-digest" "content-type" "date");keyid="https://relay.example/actor#main-key";alg="rsa-v1_5-sha256";tag="activity-relay-directory-v1";nonce="nonce";created=1785867300;expires=1785867480`, "directory=:AA==:"},
	} {
		f.Add(seed[0], seed[1])
	}

	f.Fuzz(func(t *testing.T, input string, signatureValue string) {
		request, err := http.NewRequest(http.MethodPost, "https://directory.example/", nil)
		if err != nil {
			t.Fatal(err)
		}
		request.Header.Set("Signature-Input", input)
		request.Header.Set("Signature", signatureValue)
		_, _ = parseRFC9421Message(request)
	})
}
