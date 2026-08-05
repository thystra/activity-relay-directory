package actorresolver

import (
	"container/list"
	"context"
	"crypto/rsa"
	"errors"
	"math/big"
	"sync"
	"time"

	v1 "github.com/thystra/activity-relay-directory/internal/protocol/v1"
)

const (
	maximumCacheEntries = 100_000
	maximumCacheTTL     = 5 * time.Minute
)

// ErrCacheConfiguration reports an invalid actor-key cache configuration.
var ErrCacheConfiguration = errors.New("actor resolver cache configuration is invalid")

// CacheConfig bounds the successful actor-key cache.
type CacheConfig struct {
	MaxEntries int
	TTL        time.Duration
}

// CachedResolver wraps the production resolver with a bounded, success-only
// actor-key cache. It does not cache failures or perform background work.
type CachedResolver struct {
	mu       sync.Mutex
	resolver *Resolver
	config   CacheConfig
	now      func() time.Time
	entries  map[string]*cacheEntry
	order    *list.List
	lastNow  time.Time
}

type cacheEntry struct {
	keyID    string
	resolved v1.RFC9421ResolvedKey
	expires  time.Time
	element  *list.Element
}

var _ v1.RFC9421KeyResolver = (*CachedResolver)(nil)

// NewCachedResolver constructs a bounded cache around the production resolver.
func NewCachedResolver(
	resolver *Resolver,
	config CacheConfig,
) (*CachedResolver, error) {
	return newCachedResolver(resolver, config, time.Now)
}

func newCachedResolver(
	resolver *Resolver,
	config CacheConfig,
	now func() time.Time,
) (*CachedResolver, error) {
	if resolver == nil || resolver.client == nil || now == nil ||
		config.MaxEntries < 1 || config.MaxEntries > maximumCacheEntries ||
		config.TTL <= 0 || config.TTL > maximumCacheTTL {
		return nil, ErrCacheConfiguration
	}
	return &CachedResolver{
		resolver: resolver,
		config:   config,
		now:      now,
		entries:  make(map[string]*cacheEntry),
		order:    list.New(),
	}, nil
}

// ResolveRFC9421Key returns an isolated copy of a cached successful result or
// delegates to the safe resolver. Cache TTL starts only after retrieval and
// validation finish. Cancellation is honored even on a cache hit.
func (cache *CachedResolver) ResolveRFC9421Key(
	ctx context.Context,
	keyID string,
) (v1.RFC9421ResolvedKey, error) {
	if cache == nil || cache.resolver == nil || cache.now == nil || ctx == nil {
		return v1.RFC9421ResolvedKey{}, ErrCacheConfiguration
	}
	if err := ctx.Err(); err != nil {
		return v1.RFC9421ResolvedKey{}, errors.Join(ErrActorFetch, err)
	}
	actorURL, err := actorURLFromKeyID(keyID)
	if err != nil {
		return v1.RFC9421ResolvedKey{}, err
	}

	cache.mu.Lock()
	now := cache.currentTimeLocked()
	if entry, found := cache.entries[keyID]; found {
		if now.Before(entry.expires) {
			cache.order.MoveToBack(entry.element)
			resolved := cloneResolvedKey(entry.resolved)
			cache.mu.Unlock()
			return resolved, nil
		}
		cache.removeLocked(entry)
	}
	cache.mu.Unlock()

	resolved, err := cache.resolver.ResolveRFC9421Key(ctx, keyID)
	if err != nil {
		return v1.RFC9421ResolvedKey{}, err
	}
	if err := ctx.Err(); err != nil {
		return v1.RFC9421ResolvedKey{}, errors.Join(ErrActorFetch, err)
	}
	if err := validateCacheResult(resolved, keyID, actorURL); err != nil {
		return v1.RFC9421ResolvedKey{}, err
	}
	resolved = cloneResolvedKey(resolved)

	cache.mu.Lock()
	now = cache.currentTimeLocked()
	if entry, found := cache.entries[keyID]; found {
		if now.Before(entry.expires) {
			cache.order.MoveToBack(entry.element)
			resolved = cloneResolvedKey(entry.resolved)
			cache.mu.Unlock()
			return resolved, nil
		}
		cache.removeLocked(entry)
	}
	if len(cache.entries) >= cache.config.MaxEntries {
		oldest := cache.order.Front()
		if oldest != nil {
			cache.removeLocked(oldest.Value.(*cacheEntry))
		}
	}
	entry := &cacheEntry{
		keyID:    keyID,
		resolved: resolved,
		expires:  now.Add(cache.config.TTL),
	}
	entry.element = cache.order.PushBack(entry)
	cache.entries[keyID] = entry
	result := cloneResolvedKey(entry.resolved)
	cache.mu.Unlock()
	return result, nil
}

func (cache *CachedResolver) currentTimeLocked() time.Time {
	now := cache.now()
	if now.Before(cache.lastNow) {
		return cache.lastNow
	}
	cache.lastNow = now
	return now
}

func (cache *CachedResolver) removeLocked(entry *cacheEntry) {
	delete(cache.entries, entry.keyID)
	cache.order.Remove(entry.element)
}

func validateCacheResult(
	resolved v1.RFC9421ResolvedKey,
	keyID string,
	actorURL string,
) error {
	if resolved.KeyID != keyID || resolved.Owner != actorURL ||
		resolved.ActorID != actorURL {
		return ErrActorDocument
	}
	if resolved.PublicKey == nil || resolved.PublicKey.N == nil ||
		resolved.PublicKey.N.BitLen() < minimumRSAKeyBits ||
		resolved.PublicKey.N.BitLen() > maximumRSAKeyBits ||
		resolved.PublicKey.E < 3 || resolved.PublicKey.E%2 == 0 {
		return ErrPublicKey
	}
	return nil
}

func cloneResolvedKey(resolved v1.RFC9421ResolvedKey) v1.RFC9421ResolvedKey {
	cloned := resolved
	if resolved.PublicKey != nil {
		cloned.PublicKey = &rsa.PublicKey{E: resolved.PublicKey.E}
		if resolved.PublicKey.N != nil {
			cloned.PublicKey.N = new(big.Int).Set(resolved.PublicKey.N)
		}
	}
	return cloned
}
