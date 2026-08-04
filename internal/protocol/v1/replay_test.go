package v1

import (
	"context"
	"errors"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

type recordingRFC9421ReplayStore struct {
	calls     atomic.Int32
	mu        sync.Mutex
	key       RFC9421ReplayKey
	expiresAt time.Time
	reserved  bool
	err       error
}

func (store *recordingRFC9421ReplayStore) ReserveRFC9421Replay(
	_ context.Context,
	key RFC9421ReplayKey,
	expiresAt time.Time,
) (bool, error) {
	store.calls.Add(1)
	store.mu.Lock()
	store.key = key
	store.expiresAt = expiresAt
	store.mu.Unlock()
	return store.reserved, store.err
}

func TestDeriveRFC9421ReplayKeyIsStableAndSeparated(t *testing.T) {
	keyID := "https://relay.example/actor#main-key"
	nonce := "directory-nonce"
	got := DeriveRFC9421ReplayKey(keyID, nonce)
	if got != DeriveRFC9421ReplayKey(keyID, nonce) {
		t.Fatal("replay-key derivation is not deterministic")
	}
	for _, other := range []RFC9421ReplayKey{
		DeriveRFC9421ReplayKey(keyID+nonce, ""),
		DeriveRFC9421ReplayKey(keyID, nonce+"-other"),
		DeriveRFC9421ReplayKey(keyID+"-other", nonce),
	} {
		if got == other {
			t.Fatal("distinct key-ID/nonce pair produced the same replay key")
		}
	}
	for _, raw := range []string{keyID, nonce} {
		if strings.Contains(string(got[:]), raw) {
			t.Fatalf("derived replay key contains raw input %q", raw)
		}
	}
}

func TestMemoryRFC9421ReplayStoreReservesUntilExpiry(t *testing.T) {
	now := time.Date(2026, 8, 4, 20, 0, 0, 0, time.UTC)
	store, err := newMemoryRFC9421ReplayStore(4, func() time.Time {
		return now
	})
	if err != nil {
		t.Fatalf("create replay store: %v", err)
	}
	key := DeriveRFC9421ReplayKey("key", "nonce")
	expiresAt := now.Add(time.Minute)

	reserved, err := store.ReserveRFC9421Replay(
		context.Background(),
		key,
		expiresAt,
	)
	if err != nil || !reserved {
		t.Fatalf("first reserve = %t, %v", reserved, err)
	}
	reserved, err = store.ReserveRFC9421Replay(
		context.Background(),
		key,
		expiresAt,
	)
	if err != nil || reserved {
		t.Fatalf("duplicate reserve = %t, %v", reserved, err)
	}

	now = expiresAt
	reserved, err = store.ReserveRFC9421Replay(
		context.Background(),
		key,
		now.Add(time.Minute),
	)
	if err != nil || !reserved {
		t.Fatalf("post-expiry reserve = %t, %v", reserved, err)
	}
}

func TestMemoryRFC9421ReplayStoreIsAtomic(t *testing.T) {
	now := time.Date(2026, 8, 4, 20, 0, 0, 0, time.UTC)
	store, err := newMemoryRFC9421ReplayStore(128, func() time.Time {
		return now
	})
	if err != nil {
		t.Fatalf("create replay store: %v", err)
	}
	key := DeriveRFC9421ReplayKey("key", "shared-nonce")

	const attempts = 128
	var successes atomic.Int32
	var duplicates atomic.Int32
	var failures atomic.Int32
	var wait sync.WaitGroup
	wait.Add(attempts)
	for range attempts {
		go func() {
			defer wait.Done()
			reserved, err := store.ReserveRFC9421Replay(
				context.Background(),
				key,
				now.Add(RFC9421ReplayTTL),
			)
			if err != nil {
				failures.Add(1)
				return
			}
			if reserved {
				successes.Add(1)
			} else {
				duplicates.Add(1)
			}
		}()
	}
	wait.Wait()
	if successes.Load() != 1 || duplicates.Load() != attempts-1 ||
		failures.Load() != 0 {
		t.Fatalf(
			"atomic reservations: successes=%d duplicates=%d failures=%d",
			successes.Load(),
			duplicates.Load(),
			failures.Load(),
		)
	}
}

func TestMemoryRFC9421ReplayStoreFailsClosedAtCapacity(t *testing.T) {
	now := time.Date(2026, 8, 4, 20, 0, 0, 0, time.UTC)
	store, err := newMemoryRFC9421ReplayStore(2, func() time.Time {
		return now
	})
	if err != nil {
		t.Fatalf("create replay store: %v", err)
	}
	for _, nonce := range []string{"one", "two"} {
		reserved, err := store.ReserveRFC9421Replay(
			context.Background(),
			DeriveRFC9421ReplayKey("key", nonce),
			now.Add(time.Minute),
		)
		if err != nil || !reserved {
			t.Fatalf("reserve %q = %t, %v", nonce, reserved, err)
		}
	}
	if reserved, err := store.ReserveRFC9421Replay(
		context.Background(),
		DeriveRFC9421ReplayKey("key", "three"),
		now.Add(time.Minute),
	); reserved || !errors.Is(err, ErrRFC9421ReplayStoreFull) {
		t.Fatalf("full-store reserve = %t, %v", reserved, err)
	}

	now = now.Add(time.Minute)
	if reserved, err := store.ReserveRFC9421Replay(
		context.Background(),
		DeriveRFC9421ReplayKey("key", "three"),
		now.Add(time.Minute),
	); err != nil || !reserved {
		t.Fatalf("reserve after pruning = %t, %v", reserved, err)
	}
}

func TestMemoryRFC9421ReplayStoreRejectsInvalidUse(t *testing.T) {
	now := time.Date(2026, 8, 4, 20, 0, 0, 0, time.UTC)
	if _, err := newMemoryRFC9421ReplayStore(-1, nil); !errors.Is(
		err,
		ErrRFC9421ReplayStore,
	) {
		t.Fatalf("negative capacity error = %v", err)
	}
	store, err := newMemoryRFC9421ReplayStore(1, func() time.Time {
		return now
	})
	if err != nil {
		t.Fatalf("create replay store: %v", err)
	}
	key := DeriveRFC9421ReplayKey("key", "nonce")

	cancelled, cancel := context.WithCancel(context.Background())
	cancel()
	if reserved, err := store.ReserveRFC9421Replay(
		cancelled,
		key,
		now.Add(time.Minute),
	); reserved || !errors.Is(err, ErrRFC9421ReplayStore) {
		t.Fatalf("cancelled reserve = %t, %v", reserved, err)
	}
	if reserved, err := store.ReserveRFC9421Replay(
		context.Background(),
		key,
		now,
	); reserved || !errors.Is(err, ErrRFC9421ReplayStore) {
		t.Fatalf("expired reserve = %t, %v", reserved, err)
	}
	if reserved, err := store.ReserveRFC9421Replay(
		nil,
		key,
		now.Add(time.Minute),
	); reserved || !errors.Is(err, ErrRFC9421ReplayStore) {
		t.Fatalf("nil-context reserve = %t, %v", reserved, err)
	}
	if reserved, err := (RFC9421ReplayStoreFunc(nil)).ReserveRFC9421Replay(
		context.Background(),
		key,
		now.Add(time.Minute),
	); reserved || !errors.Is(err, ErrRFC9421ReplayStore) {
		t.Fatalf("nil function reserve = %t, %v", reserved, err)
	}
}

func TestRFC9421VerifyPOSTAndReserveRejectsReplay(t *testing.T) {
	body := []byte(`{"operation":"register"}`)
	request, key := signedRFC9421TestRequest(t, body, RFC9421POSTComponents(), nil)
	resolver := newRFC9421TestResolver(&key.PublicKey)
	verifier := newRFC9421TestVerifier(t, resolver)
	store, err := newMemoryRFC9421ReplayStore(8, func() time.Time {
		return rfc9421TestNow
	})
	if err != nil {
		t.Fatalf("create replay store: %v", err)
	}

	result, err := verifier.VerifyPOSTAndReserve(
		request,
		body,
		"https://relay.example/actor",
		store,
	)
	if err != nil {
		t.Fatalf("first verified reservation: %v", err)
	}
	if result.Nonce != "directory-test-nonce" {
		t.Fatalf("verified nonce = %q", result.Nonce)
	}
	if _, err := verifier.VerifyPOSTAndReserve(
		request,
		body,
		"https://relay.example/actor",
		store,
	); !errors.Is(err, ErrRFC9421Replay) {
		t.Fatalf("replay error = %v", err)
	}
}

func TestRFC9421VerifyPOSTAndReserveUsesOpaqueKeyAndFixedTTL(t *testing.T) {
	body := []byte(`{"operation":"register"}`)
	request, key := signedRFC9421TestRequest(t, body, RFC9421POSTComponents(), nil)
	resolver := newRFC9421TestResolver(&key.PublicKey)
	verifier := newRFC9421TestVerifier(t, resolver)
	store := &recordingRFC9421ReplayStore{reserved: true}

	result, err := verifier.VerifyPOSTAndReserve(
		request,
		body,
		"https://relay.example/actor",
		store,
	)
	if err != nil {
		t.Fatalf("verify and reserve: %v", err)
	}
	store.mu.Lock()
	gotKey := store.key
	gotExpiry := store.expiresAt
	store.mu.Unlock()
	if gotKey != DeriveRFC9421ReplayKey(result.KeyID, result.Nonce) {
		t.Fatal("replay store received an unexpected opaque key")
	}
	wantExpiry := rfc9421TestNow.Add(RFC9421ReplayTTL)
	if !gotExpiry.Equal(wantExpiry) {
		t.Fatalf("replay expiry = %v, want %v", gotExpiry, wantExpiry)
	}
}

func TestRFC9421VerifyPOSTAndReserveDoesNotReserveBeforeAllGates(t *testing.T) {
	body := []byte(`{"operation":"register"}`)
	request, key := signedRFC9421TestRequest(t, body, RFC9421POSTComponents(), nil)
	for _, test := range []struct {
		name       string
		body       []byte
		relayActor string
		want       error
	}{
		{name: "tampered body", body: []byte(`{"operation":"heartbeat"}`), relayActor: "https://relay.example/actor", want: ErrRFC9421Digest},
		{name: "actor mismatch", body: body, relayActor: "https://other.example/actor", want: ErrRFC9421ActorBinding},
	} {
		t.Run(test.name, func(t *testing.T) {
			resolver := newRFC9421TestResolver(&key.PublicKey)
			verifier := newRFC9421TestVerifier(t, resolver)
			store := &recordingRFC9421ReplayStore{reserved: true}
			_, err := verifier.VerifyPOSTAndReserve(
				request,
				test.body,
				test.relayActor,
				store,
			)
			if !errors.Is(err, test.want) {
				t.Fatalf("error = %v, want %v", err, test.want)
			}
			if store.calls.Load() != 0 {
				t.Fatalf("failed request made %d replay reservations", store.calls.Load())
			}
		})
	}
}

func TestRFC9421VerifyPOSTAndReserveRedactsStoreErrors(t *testing.T) {
	body := []byte(`{"operation":"register"}`)
	request, key := signedRFC9421TestRequest(t, body, RFC9421POSTComponents(), nil)
	resolver := newRFC9421TestResolver(&key.PublicKey)
	store := &recordingRFC9421ReplayStore{
		err: errors.New("sensitive storage detail"),
	}
	_, err := newRFC9421TestVerifier(t, resolver).VerifyPOSTAndReserve(
		request,
		body,
		"https://relay.example/actor",
		store,
	)
	if !errors.Is(err, ErrRFC9421ReplayStore) {
		t.Fatalf("store error = %v", err)
	}
	if strings.Contains(err.Error(), "sensitive storage detail") {
		t.Fatalf("store error leaked backend detail: %v", err)
	}
}

func TestRFC9421VerifyPOSTAndReserveIsAtomicUnderConcurrency(t *testing.T) {
	body := []byte(`{"operation":"register"}`)
	request, key := signedRFC9421TestRequest(t, body, RFC9421POSTComponents(), nil)
	resolver := newRFC9421TestResolver(&key.PublicKey)
	verifier := newRFC9421TestVerifier(t, resolver)
	store, err := newMemoryRFC9421ReplayStore(64, func() time.Time {
		return rfc9421TestNow
	})
	if err != nil {
		t.Fatalf("create replay store: %v", err)
	}

	const attempts = 32
	var successes atomic.Int32
	var replays atomic.Int32
	var otherErrors atomic.Int32
	var wait sync.WaitGroup
	wait.Add(attempts)
	for range attempts {
		go func() {
			defer wait.Done()
			_, err := verifier.VerifyPOSTAndReserve(
				request,
				body,
				"https://relay.example/actor",
				store,
			)
			switch {
			case err == nil:
				successes.Add(1)
			case errors.Is(err, ErrRFC9421Replay):
				replays.Add(1)
			default:
				otherErrors.Add(1)
			}
		}()
	}
	wait.Wait()
	if successes.Load() != 1 || replays.Load() != attempts-1 ||
		otherErrors.Load() != 0 {
		t.Fatalf(
			"concurrent verification: successes=%d replays=%d other=%d",
			successes.Load(),
			replays.Load(),
			otherErrors.Load(),
		)
	}
}
