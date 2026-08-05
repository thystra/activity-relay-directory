package actorresolver

import (
	"context"
	"crypto/rsa"
	"errors"
	"math/big"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	v1 "github.com/thystra/activity-relay-directory/internal/protocol/v1"
)

type cacheTestClock struct {
	mu  sync.Mutex
	now time.Time
}

func (clock *cacheTestClock) Now() time.Time {
	clock.mu.Lock()
	defer clock.mu.Unlock()
	return clock.now
}

func (clock *cacheTestClock) Advance(duration time.Duration) {
	clock.mu.Lock()
	clock.now = clock.now.Add(duration)
	clock.mu.Unlock()
}

func cacheTestConfig() CacheConfig {
	return CacheConfig{MaxEntries: 4, TTL: time.Minute}
}

func newCacheTestResolver(
	t *testing.T,
	calls *atomic.Int64,
	response func(int64, *http.Request) *http.Response,
) *Resolver {
	t.Helper()
	publicKeyPEM, _ := loadTestPublicKey(t)
	return newTestResolver(t, func(request *http.Request) (*http.Response, error) {
		call := calls.Add(1)
		if response != nil {
			if custom := response(call, request); custom != nil {
				return custom, nil
			}
		}
		actorURL := request.URL.String()
		body := actorDocumentWith(t, actorURL, "Application", map[string]any{
			"id":           actorURL + "#main-key",
			"owner":        actorURL,
			"publicKeyPem": publicKeyPEM,
		})
		return actorResponse(http.StatusOK, "application/activity+json", body), nil
	})
}

func newCacheForTest(
	t *testing.T,
	resolver *Resolver,
	config CacheConfig,
	clock *cacheTestClock,
) *CachedResolver {
	t.Helper()
	cache, err := newCachedResolver(resolver, config, clock.Now)
	if err != nil {
		t.Fatalf("newCachedResolver() error = %v", err)
	}
	return cache
}

func TestCachedResolverCachesOnlyIsolatedSuccessfulResults(t *testing.T) {
	var calls atomic.Int64
	resolver := newCacheTestResolver(t, &calls, nil)
	clock := &cacheTestClock{now: time.Unix(1_700_000_000, 0)}
	cache := newCacheForTest(t, resolver, cacheTestConfig(), clock)
	_, expected := loadTestPublicKey(t)

	first, err := cache.ResolveRFC9421Key(context.Background(), testKeyID)
	if err != nil {
		t.Fatalf("first ResolveRFC9421Key() error = %v", err)
	}
	if first.PublicKey == nil || first.PublicKey.N.Cmp(expected.N) != 0 {
		t.Fatalf("first result = %#v", first)
	}
	first.PublicKey.N.SetInt64(3)

	second, err := cache.ResolveRFC9421Key(context.Background(), testKeyID)
	if err != nil {
		t.Fatalf("second ResolveRFC9421Key() error = %v", err)
	}
	if calls.Load() != 1 {
		t.Fatalf("resolver calls = %d, want 1", calls.Load())
	}
	if second.PublicKey == nil || second.PublicKey.N.Cmp(expected.N) != 0 ||
		second.PublicKey == first.PublicKey || second.PublicKey.N == first.PublicKey.N {
		t.Fatalf("second result was not an isolated cache copy: %#v", second)
	}
}

func TestCachedResolverExpiresAtExactTTLWithoutSliding(t *testing.T) {
	var calls atomic.Int64
	resolver := newCacheTestResolver(t, &calls, nil)
	clock := &cacheTestClock{now: time.Unix(1_700_000_000, 0)}
	config := cacheTestConfig()
	cache := newCacheForTest(t, resolver, config, clock)

	if _, err := cache.ResolveRFC9421Key(context.Background(), testKeyID); err != nil {
		t.Fatalf("initial ResolveRFC9421Key() error = %v", err)
	}
	clock.Advance(config.TTL - time.Nanosecond)
	if _, err := cache.ResolveRFC9421Key(context.Background(), testKeyID); err != nil {
		t.Fatalf("pre-expiry ResolveRFC9421Key() error = %v", err)
	}
	if calls.Load() != 1 {
		t.Fatalf("pre-expiry resolver calls = %d, want 1", calls.Load())
	}
	clock.Advance(time.Nanosecond)
	if _, err := cache.ResolveRFC9421Key(context.Background(), testKeyID); err != nil {
		t.Fatalf("exact-expiry ResolveRFC9421Key() error = %v", err)
	}
	if calls.Load() != 2 {
		t.Fatalf("exact-expiry resolver calls = %d, want 2", calls.Load())
	}
}

func TestCachedResolverUsesBoundedLRUEviction(t *testing.T) {
	var calls atomic.Int64
	resolver := newCacheTestResolver(t, &calls, nil)
	clock := &cacheTestClock{now: time.Unix(1_700_000_000, 0)}
	config := cacheTestConfig()
	config.MaxEntries = 2
	cache := newCacheForTest(t, resolver, config, clock)
	keyIDs := []string{
		"https://relay-one.example/actor#main-key",
		"https://relay-two.example/actor#main-key",
		"https://relay-three.example/actor#main-key",
	}

	for _, keyID := range keyIDs[:2] {
		if _, err := cache.ResolveRFC9421Key(context.Background(), keyID); err != nil {
			t.Fatalf("ResolveRFC9421Key(%q) error = %v", keyID, err)
		}
	}
	if _, err := cache.ResolveRFC9421Key(context.Background(), keyIDs[0]); err != nil {
		t.Fatalf("refresh first key error = %v", err)
	}
	if _, err := cache.ResolveRFC9421Key(context.Background(), keyIDs[2]); err != nil {
		t.Fatalf("insert third key error = %v", err)
	}
	if len(cache.entries) != 2 || calls.Load() != 3 {
		t.Fatalf("entries/calls = %d/%d, want 2/3", len(cache.entries), calls.Load())
	}
	if _, err := cache.ResolveRFC9421Key(context.Background(), keyIDs[1]); err != nil {
		t.Fatalf("resolve evicted key error = %v", err)
	}
	if len(cache.entries) != 2 || calls.Load() != 4 {
		t.Fatalf("post-eviction entries/calls = %d/%d, want 2/4", len(cache.entries), calls.Load())
	}
}

func TestCachedResolverDoesNotCacheFailures(t *testing.T) {
	var calls atomic.Int64
	resolver := newCacheTestResolver(t, &calls, func(call int64, _ *http.Request) *http.Response {
		if call == 1 {
			return actorResponse(http.StatusServiceUnavailable, "text/plain", []byte("private failure"))
		}
		return nil
	})
	clock := &cacheTestClock{now: time.Unix(1_700_000_000, 0)}
	cache := newCacheForTest(t, resolver, cacheTestConfig(), clock)

	resolved, err := cache.ResolveRFC9421Key(context.Background(), testKeyID)
	if resolved != (v1.RFC9421ResolvedKey{}) || !errors.Is(err, ErrActorFetch) {
		t.Fatalf("failed resolution = %#v, %v", resolved, err)
	}
	if len(cache.entries) != 0 {
		t.Fatalf("failure created %d cache entries", len(cache.entries))
	}
	if _, err := cache.ResolveRFC9421Key(context.Background(), testKeyID); err != nil {
		t.Fatalf("recovery resolution error = %v", err)
	}
	if _, err := cache.ResolveRFC9421Key(context.Background(), testKeyID); err != nil {
		t.Fatalf("cached recovery error = %v", err)
	}
	if calls.Load() != 2 {
		t.Fatalf("resolver calls = %d, want 2", calls.Load())
	}
}

func TestCachedResolverHonorsCancellationAndRejectsInvalidInputBeforeLookup(t *testing.T) {
	var calls atomic.Int64
	resolver := newCacheTestResolver(t, &calls, nil)
	clock := &cacheTestClock{now: time.Unix(1_700_000_000, 0)}
	cache := newCacheForTest(t, resolver, cacheTestConfig(), clock)
	if _, err := cache.ResolveRFC9421Key(context.Background(), testKeyID); err != nil {
		t.Fatalf("warm cache error = %v", err)
	}

	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	resolved, err := cache.ResolveRFC9421Key(canceled, testKeyID)
	if resolved.PublicKey != nil || !errors.Is(err, ErrActorFetch) ||
		!errors.Is(err, context.Canceled) {
		t.Fatalf("canceled cache hit = %#v, %v", resolved, err)
	}
	for _, keyID := range []string{"", "https://marker.invalid/actor", "https://127.0.0.1/actor#key"} {
		_, err := cache.ResolveRFC9421Key(context.Background(), keyID)
		if err == nil || strings.Contains(err.Error(), "marker.invalid") ||
			strings.Contains(err.Error(), "127.0.0.1") {
			t.Fatalf("invalid key ID error = %v", err)
		}
	}
	if calls.Load() != 1 {
		t.Fatalf("resolver calls = %d, want 1", calls.Load())
	}

	var nilCache *CachedResolver
	if _, err := nilCache.ResolveRFC9421Key(context.Background(), testKeyID); !errors.Is(err, ErrCacheConfiguration) {
		t.Fatalf("nil cache error = %v, want ErrCacheConfiguration", err)
	}
	if _, err := cache.ResolveRFC9421Key(nil, testKeyID); !errors.Is(err, ErrCacheConfiguration) {
		t.Fatalf("nil context error = %v, want ErrCacheConfiguration", err)
	}
}

func TestCachedResolverClampsClockRegression(t *testing.T) {
	var calls atomic.Int64
	resolver := newCacheTestResolver(t, &calls, nil)
	clock := &cacheTestClock{now: time.Unix(1_700_000_000, 0)}
	config := cacheTestConfig()
	cache := newCacheForTest(t, resolver, config, clock)
	if _, err := cache.ResolveRFC9421Key(context.Background(), testKeyID); err != nil {
		t.Fatalf("initial resolution error = %v", err)
	}
	clock.Advance(-time.Hour)
	if _, err := cache.ResolveRFC9421Key(context.Background(), testKeyID); err != nil {
		t.Fatalf("regressed-clock resolution error = %v", err)
	}
	if calls.Load() != 1 {
		t.Fatalf("regressed-clock calls = %d, want 1", calls.Load())
	}
	clock.Advance(time.Hour + config.TTL)
	if _, err := cache.ResolveRFC9421Key(context.Background(), testKeyID); err != nil {
		t.Fatalf("post-expiry resolution error = %v", err)
	}
	if calls.Load() != 2 {
		t.Fatalf("post-expiry calls = %d, want 2", calls.Load())
	}
}

func TestCachedResolverConcurrentHitsReturnIndependentKeys(t *testing.T) {
	const workers = 64
	var calls atomic.Int64
	resolver := newCacheTestResolver(t, &calls, nil)
	clock := &cacheTestClock{now: time.Unix(1_700_000_000, 0)}
	cache := newCacheForTest(t, resolver, cacheTestConfig(), clock)
	_, expected := loadTestPublicKey(t)
	if _, err := cache.ResolveRFC9421Key(context.Background(), testKeyID); err != nil {
		t.Fatalf("warm cache error = %v", err)
	}

	start := make(chan struct{})
	errorsChannel := make(chan error, workers)
	var wait sync.WaitGroup
	for range workers {
		wait.Add(1)
		go func() {
			defer wait.Done()
			<-start
			resolved, err := cache.ResolveRFC9421Key(context.Background(), testKeyID)
			if err == nil && (resolved.PublicKey == nil || resolved.PublicKey.N.Cmp(expected.N) != 0) {
				err = errors.New("cached public key changed")
			}
			if resolved.PublicKey != nil {
				resolved.PublicKey.N.SetInt64(3)
			}
			errorsChannel <- err
		}()
	}
	close(start)
	wait.Wait()
	close(errorsChannel)
	for err := range errorsChannel {
		if err != nil {
			t.Fatal(err)
		}
	}
	if calls.Load() != 1 {
		t.Fatalf("resolver calls = %d, want 1", calls.Load())
	}
	resolved, err := cache.ResolveRFC9421Key(context.Background(), testKeyID)
	if err != nil || resolved.PublicKey == nil || resolved.PublicKey.N.Cmp(expected.N) != 0 {
		t.Fatalf("post-concurrency result = %#v, %v", resolved, err)
	}
}

func TestCachedResolverConcurrentColdMissesKeepOneBoundedEntry(t *testing.T) {
	const workers = 32
	publicKeyPEM, expected := loadTestPublicKey(t)
	body := testActorDocument(t, "Application", map[string]any{
		"id":           testKeyID,
		"owner":        testActorURL,
		"publicKeyPem": publicKeyPEM,
	})
	var calls atomic.Int64
	allStarted := make(chan struct{})
	release := make(chan struct{})
	resolver := newTestResolver(t, func(*http.Request) (*http.Response, error) {
		if calls.Add(1) == workers {
			close(allStarted)
		}
		<-release
		return actorResponse(http.StatusOK, "application/activity+json", body), nil
	})
	clock := &cacheTestClock{now: time.Unix(1_700_000_000, 0)}
	cache := newCacheForTest(t, resolver, cacheTestConfig(), clock)
	results := make(chan v1.RFC9421ResolvedKey, workers)
	errorsChannel := make(chan error, workers)
	var wait sync.WaitGroup
	for range workers {
		wait.Add(1)
		go func() {
			defer wait.Done()
			resolved, err := cache.ResolveRFC9421Key(context.Background(), testKeyID)
			results <- resolved
			errorsChannel <- err
		}()
	}
	<-allStarted
	close(release)
	wait.Wait()
	close(results)
	close(errorsChannel)

	for err := range errorsChannel {
		if err != nil {
			t.Fatal(err)
		}
	}
	for resolved := range results {
		if resolved.PublicKey == nil || resolved.PublicKey.N.Cmp(expected.N) != 0 {
			t.Fatalf("cold-miss result = %#v", resolved)
		}
	}
	if calls.Load() != workers || len(cache.entries) != 1 || cache.order.Len() != 1 {
		t.Fatalf(
			"calls/entries/order = %d/%d/%d, want %d/1/1",
			calls.Load(), len(cache.entries), cache.order.Len(), workers,
		)
	}
}

func TestCachedResolverConfigurationBounds(t *testing.T) {
	var calls atomic.Int64
	resolver := newCacheTestResolver(t, &calls, nil)
	valid := cacheTestConfig()
	if _, err := NewCachedResolver(resolver, valid); err != nil {
		t.Fatalf("NewCachedResolver(valid) error = %v", err)
	}
	if _, err := newCachedResolver(resolver, valid, nil); !errors.Is(err, ErrCacheConfiguration) {
		t.Fatalf("newCachedResolver(nil clock) error = %v", err)
	}

	tests := []struct {
		name     string
		resolver *Resolver
		config   CacheConfig
	}{
		{name: "nil resolver", resolver: nil, config: valid},
		{name: "uninitialized resolver", resolver: &Resolver{}, config: valid},
		{name: "zero entries", resolver: resolver, config: CacheConfig{TTL: time.Minute}},
		{name: "excess entries", resolver: resolver, config: CacheConfig{MaxEntries: maximumCacheEntries + 1, TTL: time.Minute}},
		{name: "zero TTL", resolver: resolver, config: CacheConfig{MaxEntries: 1}},
		{name: "negative TTL", resolver: resolver, config: CacheConfig{MaxEntries: 1, TTL: -1}},
		{name: "excess TTL", resolver: resolver, config: CacheConfig{MaxEntries: 1, TTL: maximumCacheTTL + 1}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			cache, err := NewCachedResolver(test.resolver, test.config)
			if cache != nil || !errors.Is(err, ErrCacheConfiguration) {
				t.Fatalf("NewCachedResolver() = %#v, %v", cache, err)
			}
		})
	}
}

func TestValidateCacheResultRejectsUnboundOrInvalidKeys(t *testing.T) {
	_, publicKey := loadTestPublicKey(t)
	valid := resolvedKeyForTest(publicKey)
	if err := validateCacheResult(valid, testKeyID, testActorURL); err != nil {
		t.Fatalf("validateCacheResult(valid) error = %v", err)
	}

	tests := []struct {
		name   string
		mutate func(*v1.RFC9421ResolvedKey)
		want   error
	}{
		{name: "key ID", mutate: func(value *v1.RFC9421ResolvedKey) { value.KeyID = testActorURL + "#other" }, want: ErrActorDocument},
		{name: "owner", mutate: func(value *v1.RFC9421ResolvedKey) { value.Owner = "https://other.example/actor" }, want: ErrActorDocument},
		{name: "actor", mutate: func(value *v1.RFC9421ResolvedKey) { value.ActorID = "https://other.example/actor" }, want: ErrActorDocument},
		{name: "nil key", mutate: func(value *v1.RFC9421ResolvedKey) { value.PublicKey = nil }, want: ErrPublicKey},
		{name: "nil modulus", mutate: func(value *v1.RFC9421ResolvedKey) { value.PublicKey.N = nil }, want: ErrPublicKey},
		{name: "weak modulus", mutate: func(value *v1.RFC9421ResolvedKey) { value.PublicKey.N = big.NewInt(3) }, want: ErrPublicKey},
		{name: "oversized modulus", mutate: func(value *v1.RFC9421ResolvedKey) {
			value.PublicKey.N = new(big.Int).Lsh(big.NewInt(1), maximumRSAKeyBits)
		}, want: ErrPublicKey},
		{name: "invalid exponent", mutate: func(value *v1.RFC9421ResolvedKey) { value.PublicKey.E = 2 }, want: ErrPublicKey},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			candidate := cloneResolvedKey(valid)
			test.mutate(&candidate)
			if err := validateCacheResult(candidate, testKeyID, testActorURL); !errors.Is(err, test.want) {
				t.Fatalf("validateCacheResult() error = %v, want %v", err, test.want)
			}
		})
	}
}

func resolvedKeyForTest(publicKey *rsa.PublicKey) v1.RFC9421ResolvedKey {
	return v1.RFC9421ResolvedKey{
		KeyID:     testKeyID,
		Owner:     testActorURL,
		ActorID:   testActorURL,
		PublicKey: publicKey,
	}
}
