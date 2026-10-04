package httpapi

import (
	"bytes"
	"context"
	"crypto/rsa"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"github.com/thystra/activity-relay-directory/internal/admission"
	"github.com/thystra/activity-relay-directory/internal/config"
	v1 "github.com/thystra/activity-relay-directory/internal/protocol/v1"
	v2 "github.com/thystra/activity-relay-directory/internal/protocol/v2"
	"github.com/thystra/activity-relay-directory/internal/storage"
	storageSQLite "github.com/thystra/activity-relay-directory/internal/storage/sqlite"
)

func TestLifecycleV2SharedFixturePersistsRelayProfile(t *testing.T) {
	fixtureBytes, err := os.ReadFile(filepath.Join("..", "..", "testdata", "directory", "v2", "activity-relay-register.valid.json"))
	if err != nil {
		t.Fatalf("read fixture: %v", err)
	}
	var fixture fixtureDocumentV2
	if err := json.Unmarshal(fixtureBytes, &fixture); err != nil {
		t.Fatalf("decode fixture: %v", err)
	}
	fixtureNow, err := http.ParseTime(fixture.Date)
	if err != nil {
		t.Fatalf("parse fixture date: %v", err)
	}
	block, trailing := pem.Decode([]byte(fixture.PublicKeyPEM))
	if block == nil || len(trailing) != 0 || block.Type != "PUBLIC KEY" {
		t.Fatal("fixture key is invalid")
	}
	parsed, err := x509.ParsePKIXPublicKey(block.Bytes)
	if err != nil {
		t.Fatalf("parse fixture key: %v", err)
	}
	publicKey, ok := parsed.(*rsa.PublicKey)
	if !ok {
		t.Fatalf("fixture key type = %T", parsed)
	}
	resolver := v1.RFC9421KeyResolverFunc(func(context.Context, string) (v1.RFC9421ResolvedKey, error) {
		return v1.RFC9421ResolvedKey{KeyID: fixture.KeyID, Owner: fixture.KeyOwner, ActorID: fixture.KeyActor, PublicKey: publicKey}, nil
	})
	core, err := v1.NewRFC9421Verifier(v1.RFC9421VerifierOptions{
		Authority: fixture.Authority, KeyResolver: resolver, Now: func() time.Time { return fixtureNow },
	})
	if err != nil {
		t.Fatalf("NewRFC9421Verifier() error = %v", err)
	}
	v2Verifier, err := v2.NewRFC9421Verifier(core)
	if err != nil {
		t.Fatalf("v2.NewRFC9421Verifier() error = %v", err)
	}
	ctx := context.Background()
	database, err := storageSQLite.Open(ctx, filepath.Join(t.TempDir(), "directory.sqlite"))
	if err != nil {
		t.Fatalf("sqlite.Open() error = %v", err)
	}
	t.Cleanup(func() { _ = database.Close() })
	if err := storageSQLite.Migrate(ctx, database); err != nil {
		t.Fatalf("sqlite.Migrate() error = %v", err)
	}
	repository, err := storageSQLite.NewRelayRepository(database, storage.AllowWrites)
	if err != nil {
		t.Fatalf("NewRelayRepository() error = %v", err)
	}
	if _, err := repository.SetEnrollment(ctx, true, storage.EnrollmentIntent{OperatorID: "v2-test"}, fixtureNow.Add(-time.Second)); err != nil {
		t.Fatalf("SetEnrollment() error = %v", err)
	}
	sourceResolver, err := admission.NewSourceResolver(nil)
	if err != nil {
		t.Fatalf("NewSourceResolver() error = %v", err)
	}
	replayStore := &allowingReplayStore{}
	lifecycle, err := NewLifecycleHandler(LifecycleDependencies{
		Verifier:          core,
		V2Verifier:        v2Verifier,
		ReplayStore:       replayStore,
		Repository:        repository,
		ProfileRepository: repository,
		SourceResolver:    sourceResolver,
		Limiter:           generousLifecycleLimiter(t),
		MaximumBodyBytes:  4096,
		Now:               func() time.Time { return fixtureNow },
	})
	if err != nil {
		t.Fatalf("NewLifecycleHandler() error = %v", err)
	}
	handler := NewHandlerWithLifecycle(config.Config{
		PublicBaseURL: "https://directory.example", LifecycleEnabled: true, MaxRequestBodyBytes: 4096,
	}, "test-version", func(context.Context) error { return nil }, lifecycle, repository.EnrollmentOpen)

	request, err := http.NewRequest(fixture.Method, fixture.Scheme+"://"+fixture.Authority+fixture.Target, bytes.NewBufferString(fixture.Body))
	if err != nil {
		t.Fatalf("create request: %v", err)
	}
	request.Host = fixture.Authority
	request.RemoteAddr = "192.0.2.1:1234"
	request.Header.Set("Content-Type", fixture.ContentType)
	request.Header.Set("Content-Digest", fixture.ContentDigest)
	request.Header.Set("Date", fixture.Date)
	request.Header.Set("Signature-Input", fixture.SignatureInput)
	request.Header.Set("Signature", fixture.Signature)
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusCreated {
		t.Fatalf("status = %d, body = %q", response.Code, response.Body.String())
	}
	profile, err := repository.EffectiveProfile(ctx, lifecycleTestActor)
	if err != nil {
		t.Fatalf("EffectiveProfile() error = %v", err)
	}
	want := storage.RelayProfile{
		ParticipationMode: "open", Availability: "public", RelayType: "general",
		Languages: []string{"en", "fr"}, Countries: []string{"US"}, Regions: []string{"North America"}, Topics: []string{"general"},
		ContactFediverse: "@relay@example.social", ContactEmail: "relay@example.com",
		ContactURL: "https://relay.example/contact", ParticipationURL: "https://relay.example/join", Notes: "Public community relay",
	}
	if !reflect.DeepEqual(profile, want) {
		t.Fatalf("profile = %#v, want %#v", profile, want)
	}

	statusRequest := httptest.NewRequest(http.MethodGet, "/v1/status", nil)
	statusResponse := httptest.NewRecorder()
	handler.ServeHTTP(statusResponse, statusRequest)
	var statusBody struct {
		SchemaVersion             int   `json:"schema_version"`
		LifecycleProtocolVersions []int `json:"lifecycle_protocol_versions"`
	}
	if err := json.Unmarshal(statusResponse.Body.Bytes(), &statusBody); err != nil {
		t.Fatalf("decode status: %v", err)
	}
	if statusBody.SchemaVersion != 4 || !reflect.DeepEqual(statusBody.LifecycleProtocolVersions, []int{1, 2}) {
		t.Fatalf("status = %#v", statusBody)
	}
}

type fixtureDocumentV2 struct {
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
