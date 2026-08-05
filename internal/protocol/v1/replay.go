package v1

import (
	"context"
	"crypto/sha256"
	"errors"
	"net/http"
	"sync"
	"time"
)

const (
	// RFC9421ReplayTTL retains an accepted key-ID/nonce pair beyond the complete
	// version 1 signature acceptance window.
	RFC9421ReplayTTL = 10 * time.Minute

	defaultRFC9421ReplayCapacity = 10_000
)

var (
	ErrRFC9421Replay          = errors.New("RFC 9421 signature nonce has already been used")
	ErrRFC9421ReplayStore     = errors.New("RFC 9421 replay store failed")
	ErrRFC9421ReplayStoreFull = errors.New("RFC 9421 replay store capacity is exhausted")
)

// RFC9421ReplayKey is an opaque digest over a key ID and nonce. Replay stores
// receive this value so they never need to persist either raw identifier.
type RFC9421ReplayKey [sha256.Size]byte

// DeriveRFC9421ReplayKey derives the opaque replay key for one signer/nonce
// pair. The separator makes the two input boundaries unambiguous.
func DeriveRFC9421ReplayKey(keyID string, nonce string) RFC9421ReplayKey {
	return sha256.Sum256([]byte(keyID + "\x00" + nonce))
}

// RFC9421ReplayStore atomically reserves an opaque replay key until expiresAt.
// A false result with no error means the key was already reserved.
type RFC9421ReplayStore interface {
	ReserveRFC9421Replay(
		context.Context,
		RFC9421ReplayKey,
		time.Time,
	) (bool, error)
}

// RFC9421ReplayStoreFunc adapts a function to RFC9421ReplayStore.
type RFC9421ReplayStoreFunc func(
	context.Context,
	RFC9421ReplayKey,
	time.Time,
) (bool, error)

func (store RFC9421ReplayStoreFunc) ReserveRFC9421Replay(
	ctx context.Context,
	key RFC9421ReplayKey,
	expiresAt time.Time,
) (bool, error) {
	if store == nil {
		return false, ErrRFC9421ReplayStore
	}
	return store(ctx, key, expiresAt)
}

// memoryRFC9421ReplayStore is a bounded process-local reference implementation
// used to prove atomic semantics. It is intentionally package-private and is
// not a production or multi-instance replay backend.
type memoryRFC9421ReplayStore struct {
	mu       sync.Mutex
	entries  map[RFC9421ReplayKey]time.Time
	capacity int
	now      func() time.Time
}

func newMemoryRFC9421ReplayStore(
	capacity int,
	now func() time.Time,
) (*memoryRFC9421ReplayStore, error) {
	if capacity == 0 {
		capacity = defaultRFC9421ReplayCapacity
	}
	if capacity < 0 {
		return nil, ErrRFC9421ReplayStore
	}
	if now == nil {
		now = time.Now
	}
	return &memoryRFC9421ReplayStore{
		entries:  make(map[RFC9421ReplayKey]time.Time),
		capacity: capacity,
		now:      now,
	}, nil
}

func (store *memoryRFC9421ReplayStore) ReserveRFC9421Replay(
	ctx context.Context,
	key RFC9421ReplayKey,
	expiresAt time.Time,
) (bool, error) {
	if store == nil || store.entries == nil || store.capacity <= 0 ||
		store.now == nil || ctx == nil {
		return false, ErrRFC9421ReplayStore
	}
	select {
	case <-ctx.Done():
		return false, ErrRFC9421ReplayStore
	default:
	}

	expiresAt = time.Unix(expiresAt.UTC().Unix(), 0)

	store.mu.Lock()
	defer store.mu.Unlock()

	select {
	case <-ctx.Done():
		return false, ErrRFC9421ReplayStore
	default:
	}
	now := time.Unix(store.now().UTC().Unix(), 0)
	if !expiresAt.After(now) {
		return false, ErrRFC9421ReplayStore
	}

	if existing, ok := store.entries[key]; ok {
		if existing.After(now) {
			return false, nil
		}
		delete(store.entries, key)
	}

	if len(store.entries) >= store.capacity {
		for candidate, expiry := range store.entries {
			if !expiry.After(now) {
				delete(store.entries, candidate)
			}
		}
	}
	if len(store.entries) >= store.capacity {
		return false, ErrRFC9421ReplayStoreFull
	}

	store.entries[key] = expiresAt
	return true, nil
}

// VerifyPOSTAndReserve performs every stateless verification and actor-binding
// gate before atomically reserving the nonce. Public handlers must eventually
// use this path with a durable store appropriate to the service topology,
// never VerifyPOST by itself.
func (verifier *RFC9421Verifier) VerifyPOSTAndReserve(
	request *http.Request,
	body []byte,
	relayActor string,
	store RFC9421ReplayStore,
) (*RFC9421Verification, error) {
	if verifier == nil || request == nil || store == nil {
		return nil, ErrRFC9421ReplayStore
	}

	verification, err := verifier.VerifyPOST(request, body)
	if err != nil {
		return nil, err
	}
	if err := verification.BindRelayActor(relayActor); err != nil {
		return nil, err
	}

	expiresAt := time.Unix(
		verifier.now().UTC().Unix(),
		0,
	).Add(RFC9421ReplayTTL)
	reserved, err := store.ReserveRFC9421Replay(
		request.Context(),
		DeriveRFC9421ReplayKey(verification.KeyID, verification.Nonce),
		expiresAt,
	)
	if err != nil {
		return nil, ErrRFC9421ReplayStore
	}
	if !reserved {
		return nil, ErrRFC9421Replay
	}

	return verification, nil
}
