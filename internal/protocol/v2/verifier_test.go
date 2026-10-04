package v2

import (
	"bytes"
	"context"
	"crypto/rsa"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"net/http"
	"os"
	"path/filepath"
	"testing"
	"time"

	v1 "github.com/thystra/activity-relay-directory/internal/protocol/v1"
)

type fixtureDocument struct {
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

type fixtureReplayStore struct{ calls int }

func (store *fixtureReplayStore) ReserveRFC9421Replay(
	context.Context,
	v1.RFC9421ReplayKey,
	time.Time,
) (bool, error) {
	store.calls++
	return true, nil
}

func TestSharedActivityRelayRegisterFixture(t *testing.T) {
	body, err := os.ReadFile(filepath.Join("..", "..", "..", "testdata", "directory", "v2", "activity-relay-register.valid.json"))
	if err != nil {
		t.Fatalf("read fixture: %v", err)
	}
	var fixture fixtureDocument
	if err := json.Unmarshal(body, &fixture); err != nil {
		t.Fatalf("decode fixture: %v", err)
	}
	fixtureNow, err := http.ParseTime(fixture.Date)
	if err != nil {
		t.Fatalf("parse fixture date: %v", err)
	}
	block, trailing := pem.Decode([]byte(fixture.PublicKeyPEM))
	if block == nil || len(trailing) != 0 || block.Type != "PUBLIC KEY" {
		t.Fatal("fixture key is not one public-key block")
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
		return v1.RFC9421ResolvedKey{
			KeyID: fixture.KeyID, Owner: fixture.KeyOwner, ActorID: fixture.KeyActor, PublicKey: publicKey,
		}, nil
	})
	core, err := v1.NewRFC9421Verifier(v1.RFC9421VerifierOptions{
		Authority: fixture.Authority, KeyResolver: resolver, Now: func() time.Time { return fixtureNow },
	})
	if err != nil {
		t.Fatalf("NewRFC9421Verifier() error = %v", err)
	}
	verifier, err := NewRFC9421Verifier(core)
	if err != nil {
		t.Fatalf("NewRFC9421Verifier(v2) error = %v", err)
	}
	request, err := http.NewRequest(fixture.Method, fixture.Scheme+"://"+fixture.Authority+fixture.Target, bytes.NewBufferString(fixture.Body))
	if err != nil {
		t.Fatalf("create request: %v", err)
	}
	request.Host = fixture.Authority
	request.Header.Set("Content-Type", fixture.ContentType)
	request.Header.Set("Content-Digest", fixture.ContentDigest)
	request.Header.Set("Date", fixture.Date)
	request.Header.Set("Signature-Input", fixture.SignatureInput)
	request.Header.Set("Signature", fixture.Signature)
	store := &fixtureReplayStore{}
	verified, err := verifier.VerifyRegisterAndReserve(request, []byte(fixture.Body), MaximumRegisterBodyBytes, store)
	if err != nil {
		t.Fatalf("VerifyRegisterAndReserve() error = %v", err)
	}
	if verified.Request.RelayActor != fixture.KeyActor || verified.Request.Profile.ParticipationMode != "open" {
		t.Fatalf("verified request = %#v", verified.Request)
	}
	if store.calls != 1 {
		t.Fatalf("replay reservations = %d", store.calls)
	}
}
