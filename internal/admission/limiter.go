package admission

import (
	"container/list"
	"context"
	"errors"
	"net/netip"
	"strconv"
	"sync"
	"time"

	v1 "github.com/thystra/activity-relay-directory/internal/protocol/v1"
)

const (
	maximumEntries      = 1_000_000
	maximumBurst        = 100_000
	maximumConcurrent   = 100_000
	maximumCleanupLimit = 4096
	maximumInterval     = 24 * time.Hour
	maximumIdleTTL      = 30 * 24 * time.Hour
	maximumRetryAfter   = 5 * time.Minute
)

var ErrConfiguration = errors.New("request admission configuration is invalid")

// Rate defines one discrete token bucket. Each accepted request consumes one
// token, up to Burst tokens are retained, and one token is restored per
// RefillInterval.
type Rate struct {
	Burst          int
	RefillInterval time.Duration
}

// Config bounds all memory and work performed by a Limiter.
type Config struct {
	Source             Rate
	Actor              Rate
	MaxSources         int
	MaxActors          int
	MaxConcurrent      int
	IdleTTL            time.Duration
	CleanupLimit       int
	OverloadRetryAfter time.Duration
}

// Decision is a bounded admission outcome. It contains no client or actor
// identity and is safe to translate to a stable protocol response.
type Decision string

const (
	DecisionAllowed            Decision = "allowed"
	DecisionSourceRateLimited  Decision = "source_rate_limited"
	DecisionActorRateLimited   Decision = "actor_rate_limited"
	DecisionConcurrencyLimited Decision = "concurrency_limited"
	DecisionCapacityLimited    Decision = "capacity_limited"
	DecisionCanceled           Decision = "canceled"
	DecisionInvalid            Decision = "invalid"
)

// Result reports an admission decision and optional minimum retry delay.
type Result struct {
	Decision   Decision
	RetryAfter time.Duration
}

// Allowed reports whether request processing may continue.
func (result Result) Allowed() bool {
	return result.Decision == DecisionAllowed
}

// Limiter applies operation-specific source and authenticated-actor buckets,
// a global concurrency ceiling, bounded state, and bounded idle cleanup.
type Limiter struct {
	mu       sync.Mutex
	config   Config
	now      func() time.Time
	sources  bucketPool
	actors   bucketPool
	inFlight int
	lastNow  time.Time
}

// Permit represents one global concurrency slot. Release is idempotent.
type Permit struct {
	limiter      *Limiter
	operation    v1.Operation
	active       bool
	actorChecked bool
}

// New constructs a limiter using the process clock.
func New(config Config) (*Limiter, error) {
	return newLimiter(config, time.Now)
}

func newLimiter(config Config, now func() time.Time) (*Limiter, error) {
	if err := validateConfig(config); err != nil || now == nil {
		return nil, ErrConfiguration
	}
	return &Limiter{
		config:  config,
		now:     now,
		sources: newBucketPool(config.MaxSources),
		actors:  newBucketPool(config.MaxActors),
	}, nil
}

// AdmitSource is the first admission stage. It must run before expensive
// resolution or signature work. A successful call consumes one source token
// and returns a permit that the caller must release.
func (limiter *Limiter) AdmitSource(
	ctx context.Context,
	operation v1.Operation,
	source netip.Addr,
) (*Permit, Result) {
	if limiter == nil || ctx == nil || !operation.Valid() {
		return nil, Result{Decision: DecisionInvalid}
	}
	if err := ctx.Err(); err != nil {
		return nil, Result{Decision: DecisionCanceled}
	}
	source = source.Unmap()
	if !validSourceAddress(source) {
		return nil, Result{Decision: DecisionInvalid}
	}

	limiter.mu.Lock()
	defer limiter.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return nil, Result{Decision: DecisionCanceled}
	}
	if limiter.inFlight >= limiter.config.MaxConcurrent {
		return nil, Result{
			Decision:   DecisionConcurrencyLimited,
			RetryAfter: limiter.config.OverloadRetryAfter,
		}
	}

	result := limiter.sources.admit(
		bucketKey(operation, source.String()),
		limiter.currentTimeLocked(),
		limiter.config.Source,
		limiter.config.IdleTTL,
		limiter.config.CleanupLimit,
		DecisionSourceRateLimited,
		limiter.config.OverloadRetryAfter,
	)
	if !result.Allowed() {
		return nil, result
	}

	limiter.inFlight++
	return &Permit{limiter: limiter, operation: operation, active: true}, result
}

// AdmitActor is the second admission stage. Call it only after the signed
// request has been bound to this canonical relay actor. It consumes one actor
// token while requiring this source permit to remain active. Each permit may
// perform this stage exactly once.
func (permit *Permit) AdmitActor(
	ctx context.Context,
	operation v1.Operation,
	actor string,
) Result {
	if permit == nil || permit.limiter == nil || ctx == nil || !operation.Valid() {
		return Result{Decision: DecisionInvalid}
	}
	if err := ctx.Err(); err != nil {
		return Result{Decision: DecisionCanceled}
	}
	canonicalActor, err := v1.NormalizeRelayActorURL(actor)
	if err != nil || canonicalActor != actor {
		return Result{Decision: DecisionInvalid}
	}

	limiter := permit.limiter
	limiter.mu.Lock()
	defer limiter.mu.Unlock()
	if !permit.active || permit.operation != operation || permit.actorChecked {
		return Result{Decision: DecisionInvalid}
	}
	if err := ctx.Err(); err != nil {
		return Result{Decision: DecisionCanceled}
	}
	permit.actorChecked = true
	return limiter.actors.admit(
		bucketKey(operation, actor),
		limiter.currentTimeLocked(),
		limiter.config.Actor,
		limiter.config.IdleTTL,
		limiter.config.CleanupLimit,
		DecisionActorRateLimited,
		limiter.config.OverloadRetryAfter,
	)
}

// Release gives back the global concurrency slot. It is safe to call more than
// once and safe to call concurrently.
func (permit *Permit) Release() {
	if permit == nil {
		return
	}
	limiter := permit.limiter
	if limiter == nil {
		return
	}
	limiter.mu.Lock()
	if permit.active {
		permit.active = false
		if limiter.inFlight > 0 {
			limiter.inFlight--
		}
	}
	limiter.mu.Unlock()
}

func validateConfig(config Config) error {
	if !validRate(config.Source) || !validRate(config.Actor) ||
		config.MaxSources < 1 || config.MaxSources > maximumEntries ||
		config.MaxActors < 1 || config.MaxActors > maximumEntries ||
		config.MaxConcurrent < 1 || config.MaxConcurrent > maximumConcurrent ||
		config.CleanupLimit < 1 || config.CleanupLimit > maximumCleanupLimit ||
		config.IdleTTL < minimumFullRefill(config.Source) ||
		config.IdleTTL < minimumFullRefill(config.Actor) ||
		config.IdleTTL > maximumIdleTTL ||
		config.OverloadRetryAfter <= 0 ||
		config.OverloadRetryAfter > maximumRetryAfter {
		return ErrConfiguration
	}
	return nil
}

func validRate(rate Rate) bool {
	return rate.Burst >= 1 && rate.Burst <= maximumBurst &&
		rate.RefillInterval > 0 && rate.RefillInterval <= maximumInterval
}

func minimumFullRefill(rate Rate) time.Duration {
	if !validRate(rate) || time.Duration(rate.Burst) > maximumIdleTTL/rate.RefillInterval {
		return maximumIdleTTL + 1
	}
	return time.Duration(rate.Burst) * rate.RefillInterval
}

func bucketKey(operation v1.Operation, identity string) string {
	return strconv.Itoa(len(operation)) + ":" + string(operation) + identity
}

func (limiter *Limiter) currentTimeLocked() time.Time {
	now := limiter.now()
	if now.Before(limiter.lastNow) {
		return limiter.lastNow
	}
	limiter.lastNow = now
	return now
}

type bucketPool struct {
	entries map[string]*bucket
	order   *list.List
	maximum int
}

type bucket struct {
	key        string
	tokens     int
	lastRefill time.Time
	lastSeen   time.Time
	element    *list.Element
}

func newBucketPool(maximum int) bucketPool {
	return bucketPool{
		entries: make(map[string]*bucket),
		order:   list.New(),
		maximum: maximum,
	}
}

func (pool *bucketPool) admit(
	key string,
	now time.Time,
	rate Rate,
	idleTTL time.Duration,
	cleanupLimit int,
	rateDecision Decision,
	overloadRetryAfter time.Duration,
) Result {
	pool.cleanup(now, idleTTL, cleanupLimit)
	entry, exists := pool.entries[key]
	if !exists {
		if len(pool.entries) >= pool.maximum {
			return Result{
				Decision:   DecisionCapacityLimited,
				RetryAfter: overloadRetryAfter,
			}
		}
		entry = &bucket{
			key:        key,
			tokens:     rate.Burst,
			lastRefill: now,
			lastSeen:   now,
		}
		entry.element = pool.order.PushBack(entry)
		pool.entries[key] = entry
	} else {
		entry.refill(now, rate)
		entry.lastSeen = monotonicMaximum(entry.lastSeen, now)
		pool.order.MoveToBack(entry.element)
	}

	if entry.tokens < 1 {
		return Result{
			Decision:   rateDecision,
			RetryAfter: entry.retryAfter(now, rate.RefillInterval),
		}
	}
	entry.tokens--
	return Result{Decision: DecisionAllowed}
}

func (pool *bucketPool) cleanup(now time.Time, idleTTL time.Duration, limit int) {
	for removed := 0; removed < limit; removed++ {
		oldest := pool.order.Front()
		if oldest == nil {
			return
		}
		entry := oldest.Value.(*bucket)
		if now.Before(entry.lastSeen) || now.Sub(entry.lastSeen) < idleTTL {
			return
		}
		delete(pool.entries, entry.key)
		pool.order.Remove(oldest)
	}
}

func (entry *bucket) refill(now time.Time, rate Rate) {
	if !now.After(entry.lastRefill) || entry.tokens >= rate.Burst {
		if entry.tokens >= rate.Burst && now.After(entry.lastRefill) {
			entry.lastRefill = now
		}
		return
	}
	elapsed := now.Sub(entry.lastRefill)
	steps := elapsed / rate.RefillInterval
	if steps < 1 {
		return
	}
	available := rate.Burst - entry.tokens
	if steps >= time.Duration(available) {
		entry.tokens = rate.Burst
		entry.lastRefill = now
		return
	}
	entry.tokens += int(steps)
	entry.lastRefill = entry.lastRefill.Add(steps * rate.RefillInterval)
}

func (entry *bucket) retryAfter(now time.Time, interval time.Duration) time.Duration {
	if !now.After(entry.lastRefill) {
		return interval
	}
	remaining := interval - now.Sub(entry.lastRefill)
	if remaining <= 0 || remaining > interval {
		return interval
	}
	return remaining
}

func monotonicMaximum(left time.Time, right time.Time) time.Time {
	if right.After(left) {
		return right
	}
	return left
}
