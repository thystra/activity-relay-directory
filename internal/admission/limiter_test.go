package admission

import (
	"context"
	"errors"
	"net/netip"
	"sync"
	"testing"
	"time"

	v1 "github.com/thystra/activity-relay-directory/internal/protocol/v1"
)

const (
	testActorOne = "https://relay-one.example/actor"
	testActorTwo = "https://relay-two.example/actor"
)

type testClock struct {
	mu  sync.Mutex
	now time.Time
}

func (clock *testClock) Now() time.Time {
	clock.mu.Lock()
	defer clock.mu.Unlock()
	return clock.now
}

func (clock *testClock) Advance(duration time.Duration) {
	clock.mu.Lock()
	clock.now = clock.now.Add(duration)
	clock.mu.Unlock()
}

func validTestConfig() Config {
	return Config{
		Source:             Rate{Burst: 2, RefillInterval: time.Minute},
		Actor:              Rate{Burst: 2, RefillInterval: time.Minute},
		MaxSources:         4,
		MaxActors:          4,
		MaxConcurrent:      2,
		IdleTTL:            2 * time.Minute,
		CleanupLimit:       2,
		OverloadRetryAfter: time.Second,
	}
}

func newTestLimiter(t *testing.T, config Config, clock *testClock) *Limiter {
	t.Helper()
	limiter, err := newLimiter(config, clock.Now)
	if err != nil {
		t.Fatalf("newLimiter() error = %v", err)
	}
	return limiter
}

func admitAndReleaseSource(t *testing.T, limiter *Limiter, operation v1.Operation, source string) Result {
	t.Helper()
	permit, result := limiter.AdmitSource(context.Background(), operation, netip.MustParseAddr(source))
	if permit != nil {
		permit.Release()
	}
	return result
}

func TestSourceTokenBucketRefillAndRetryAfter(t *testing.T) {
	clock := &testClock{now: time.Unix(1_700_000_000, 0)}
	limiter := newTestLimiter(t, validTestConfig(), clock)
	for attempt := 0; attempt < 2; attempt++ {
		result := admitAndReleaseSource(t, limiter, v1.OperationRegister, "192.0.2.10")
		if !result.Allowed() {
			t.Fatalf("attempt %d result = %+v, want allowed", attempt, result)
		}
	}

	result := admitAndReleaseSource(t, limiter, v1.OperationRegister, "192.0.2.10")
	if result.Decision != DecisionSourceRateLimited || result.RetryAfter != time.Minute {
		t.Fatalf("exhausted result = %+v, want source limit and 1m retry", result)
	}
	clock.Advance(30 * time.Second)
	result = admitAndReleaseSource(t, limiter, v1.OperationRegister, "192.0.2.10")
	if result.Decision != DecisionSourceRateLimited || result.RetryAfter != 30*time.Second {
		t.Fatalf("half interval result = %+v, want source limit and 30s retry", result)
	}
	clock.Advance(30 * time.Second)
	result = admitAndReleaseSource(t, limiter, v1.OperationRegister, "192.0.2.10")
	if !result.Allowed() {
		t.Fatalf("refilled result = %+v, want allowed", result)
	}
}

func TestClockRegressionDoesNotRefillOrReorderState(t *testing.T) {
	config := validTestConfig()
	config.Source.Burst = 1
	config.MaxSources = 1
	clock := &testClock{now: time.Unix(1_700_000_000, 0)}
	limiter := newTestLimiter(t, config, clock)
	if result := admitAndReleaseSource(t, limiter, v1.OperationRegister, "192.0.2.11"); !result.Allowed() {
		t.Fatalf("initial result = %+v, want allowed", result)
	}
	clock.Advance(-time.Hour)
	result := admitAndReleaseSource(t, limiter, v1.OperationRegister, "192.0.2.11")
	if result.Decision != DecisionSourceRateLimited || result.RetryAfter != time.Minute {
		t.Fatalf("regressed-clock result = %+v, want unchanged source limit", result)
	}
	result = admitAndReleaseSource(t, limiter, v1.OperationRegister, "192.0.2.12")
	if result.Decision != DecisionCapacityLimited {
		t.Fatalf("regressed-clock capacity result = %+v, want capacity limit", result)
	}
}

func TestBucketsAreIndependentByOperationAndIdentity(t *testing.T) {
	config := validTestConfig()
	config.Source.Burst = 1
	config.Actor.Burst = 1
	config.MaxSources = 8
	clock := &testClock{now: time.Unix(1_700_000_000, 0)}
	limiter := newTestLimiter(t, config, clock)

	for _, operation := range []v1.Operation{
		v1.OperationRegister,
		v1.OperationHeartbeat,
		v1.OperationUnregister,
	} {
		result := admitAndReleaseSource(t, limiter, operation, "192.0.2.20")
		if !result.Allowed() {
			t.Fatalf("%s source result = %+v, want allowed", operation, result)
		}
	}
	result := admitAndReleaseSource(t, limiter, v1.OperationRegister, "192.0.2.21")
	if !result.Allowed() {
		t.Fatalf("second source result = %+v, want allowed", result)
	}

	for index, operation := range []v1.Operation{
		v1.OperationRegister,
		v1.OperationHeartbeat,
		v1.OperationUnregister,
	} {
		source := netip.AddrFrom4([4]byte{192, 0, 2, byte(30 + index)})
		permit, result := limiter.AdmitSource(context.Background(), operation, source)
		if !result.Allowed() {
			t.Fatalf("%s actor-stage source result = %+v, want allowed", operation, result)
		}
		if result := permit.AdmitActor(context.Background(), operation, testActorOne); !result.Allowed() {
			t.Fatalf("%s actor result = %+v, want allowed", operation, result)
		}
		permit.Release()
	}
	permit, result := limiter.AdmitSource(
		context.Background(),
		v1.OperationRegister,
		netip.MustParseAddr("192.0.2.33"),
	)
	if !result.Allowed() {
		t.Fatalf("second actor source result = %+v, want allowed", result)
	}
	if result := permit.AdmitActor(context.Background(), v1.OperationRegister, testActorTwo); !result.Allowed() {
		t.Fatalf("second actor result = %+v, want allowed", result)
	}
	permit.Release()
}

func TestActorStageRequiresActivePermitAndCanonicalActor(t *testing.T) {
	clock := &testClock{now: time.Unix(1_700_000_000, 0)}
	limiter := newTestLimiter(t, validTestConfig(), clock)
	permit, result := limiter.AdmitSource(
		context.Background(),
		v1.OperationHeartbeat,
		netip.MustParseAddr("192.0.2.40"),
	)
	if !result.Allowed() {
		t.Fatalf("AdmitSource() = %+v, want allowed", result)
	}
	if result := permit.AdmitActor(context.Background(), v1.OperationHeartbeat, "HTTPS://RELAY-ONE.EXAMPLE/actor"); result.Decision != DecisionInvalid {
		t.Fatalf("noncanonical actor result = %+v, want invalid", result)
	}
	if result := permit.AdmitActor(context.Background(), v1.OperationRegister, testActorOne); result.Decision != DecisionInvalid {
		t.Fatalf("mismatched operation result = %+v, want invalid", result)
	}
	if result := permit.AdmitActor(context.Background(), v1.OperationHeartbeat, testActorOne); !result.Allowed() {
		t.Fatalf("first actor result = %+v, want allowed", result)
	}
	if result := permit.AdmitActor(context.Background(), v1.OperationHeartbeat, testActorOne); result.Decision != DecisionInvalid {
		t.Fatalf("repeated actor stage = %+v, want invalid", result)
	}
	permit.Release()

	permit, result = limiter.AdmitSource(context.Background(), v1.OperationHeartbeat, netip.MustParseAddr("192.0.2.41"))
	if !result.Allowed() {
		t.Fatalf("second actor source result = %+v, want allowed", result)
	}
	if result := permit.AdmitActor(context.Background(), v1.OperationHeartbeat, testActorOne); !result.Allowed() {
		t.Fatalf("second actor result = %+v, want allowed", result)
	}
	permit.Release()

	permit, result = limiter.AdmitSource(context.Background(), v1.OperationHeartbeat, netip.MustParseAddr("192.0.2.42"))
	if !result.Allowed() {
		t.Fatalf("limited actor source result = %+v, want allowed", result)
	}
	if result := permit.AdmitActor(context.Background(), v1.OperationHeartbeat, testActorOne); result.Decision != DecisionActorRateLimited || result.RetryAfter != time.Minute {
		t.Fatalf("exhausted actor result = %+v", result)
	}
	permit.Release()
	permit.Release()
	if result := permit.AdmitActor(context.Background(), v1.OperationHeartbeat, testActorTwo); result.Decision != DecisionInvalid {
		t.Fatalf("released permit actor result = %+v, want invalid", result)
	}
	if limiter.inFlight != 0 {
		t.Fatalf("inFlight = %d, want 0", limiter.inFlight)
	}
}

func TestGlobalConcurrencyLimitAndIdempotentRelease(t *testing.T) {
	config := validTestConfig()
	config.MaxConcurrent = 1
	clock := &testClock{now: time.Unix(1_700_000_000, 0)}
	limiter := newTestLimiter(t, config, clock)
	first, result := limiter.AdmitSource(context.Background(), v1.OperationRegister, netip.MustParseAddr("192.0.2.50"))
	if !result.Allowed() {
		t.Fatalf("first result = %+v, want allowed", result)
	}
	second, result := limiter.AdmitSource(context.Background(), v1.OperationRegister, netip.MustParseAddr("192.0.2.51"))
	if second != nil || result.Decision != DecisionConcurrencyLimited || result.RetryAfter != time.Second {
		t.Fatalf("second permit/result = %v/%+v, want concurrency limit", second, result)
	}
	first.Release()
	first.Release()
	second, result = limiter.AdmitSource(context.Background(), v1.OperationRegister, netip.MustParseAddr("192.0.2.51"))
	if !result.Allowed() || second == nil {
		t.Fatalf("after release permit/result = %v/%+v, want allowed", second, result)
	}
	second.Release()
}

func TestCapacityFailsClosedAndIdleCleanupIsBounded(t *testing.T) {
	config := validTestConfig()
	config.Source.Burst = 2
	config.Actor.Burst = 1
	config.MaxSources = 2
	config.MaxActors = 1
	config.CleanupLimit = 1
	clock := &testClock{now: time.Unix(1_700_000_000, 0)}
	limiter := newTestLimiter(t, config, clock)

	for _, source := range []string{"192.0.2.60", "192.0.2.61"} {
		if result := admitAndReleaseSource(t, limiter, v1.OperationRegister, source); !result.Allowed() {
			t.Fatalf("source %s result = %+v, want allowed", source, result)
		}
	}
	result := admitAndReleaseSource(t, limiter, v1.OperationRegister, "192.0.2.62")
	if result.Decision != DecisionCapacityLimited || result.RetryAfter != time.Second {
		t.Fatalf("full source pool result = %+v, want capacity limit", result)
	}
	clock.Advance(config.IdleTTL)
	result = admitAndReleaseSource(t, limiter, v1.OperationRegister, "192.0.2.62")
	if !result.Allowed() {
		t.Fatalf("post-cleanup source result = %+v, want allowed", result)
	}
	if len(limiter.sources.entries) != 2 {
		t.Fatalf("source entries = %d, want 2 after one bounded cleanup", len(limiter.sources.entries))
	}

	permit, result := limiter.AdmitSource(context.Background(), v1.OperationHeartbeat, netip.MustParseAddr("192.0.2.70"))
	if !result.Allowed() {
		t.Fatalf("actor source result = %+v, want allowed", result)
	}
	defer permit.Release()
	if result := permit.AdmitActor(context.Background(), v1.OperationHeartbeat, testActorOne); !result.Allowed() {
		t.Fatalf("first actor result = %+v, want allowed", result)
	}
	permit.Release()
	permit, result = limiter.AdmitSource(context.Background(), v1.OperationHeartbeat, netip.MustParseAddr("192.0.2.70"))
	if !result.Allowed() {
		t.Fatalf("second actor source result = %+v, want allowed", result)
	}
	defer permit.Release()
	if result := permit.AdmitActor(context.Background(), v1.OperationHeartbeat, testActorTwo); result.Decision != DecisionCapacityLimited {
		t.Fatalf("second actor result = %+v, want capacity limit", result)
	}
}

func TestCanceledAndInvalidRequestsDoNotConsumeAdmission(t *testing.T) {
	config := validTestConfig()
	config.Source.Burst = 1
	clock := &testClock{now: time.Unix(1_700_000_000, 0)}
	limiter := newTestLimiter(t, config, clock)
	canceled, cancel := context.WithCancel(context.Background())
	cancel()

	invalidCases := []struct {
		name      string
		ctx       context.Context
		operation v1.Operation
		source    netip.Addr
		decision  Decision
	}{
		{name: "nil context", ctx: nil, operation: v1.OperationRegister, source: netip.MustParseAddr("192.0.2.80"), decision: DecisionInvalid},
		{name: "canceled", ctx: canceled, operation: v1.OperationRegister, source: netip.MustParseAddr("192.0.2.80"), decision: DecisionCanceled},
		{name: "operation", ctx: context.Background(), operation: v1.Operation("other"), source: netip.MustParseAddr("192.0.2.80"), decision: DecisionInvalid},
		{name: "source", ctx: context.Background(), operation: v1.OperationRegister, source: netip.Addr{}, decision: DecisionInvalid},
		{name: "unspecified", ctx: context.Background(), operation: v1.OperationRegister, source: netip.MustParseAddr("0.0.0.0"), decision: DecisionInvalid},
	}
	for _, test := range invalidCases {
		t.Run(test.name, func(t *testing.T) {
			permit, result := limiter.AdmitSource(test.ctx, test.operation, test.source)
			if permit != nil || result.Decision != test.decision {
				t.Fatalf("permit/result = %v/%+v, want nil/%s", permit, result, test.decision)
			}
		})
	}
	if result := admitAndReleaseSource(t, limiter, v1.OperationRegister, "192.0.2.80"); !result.Allowed() {
		t.Fatalf("valid result after invalid attempts = %+v, want allowed", result)
	}
}

func TestParallelAdmissionNeverExceedsConcurrencyLimit(t *testing.T) {
	const (
		workers = 64
		maximum = 8
	)
	config := validTestConfig()
	config.Source.Burst = workers
	config.MaxConcurrent = maximum
	config.MaxSources = workers
	config.IdleTTL = time.Duration(workers) * config.Source.RefillInterval
	clock := &testClock{now: time.Unix(1_700_000_000, 0)}
	limiter := newTestLimiter(t, config, clock)
	start := make(chan struct{})
	release := make(chan struct{})
	results := make(chan Result, workers)
	var wait sync.WaitGroup

	for worker := 0; worker < workers; worker++ {
		wait.Add(1)
		go func(worker int) {
			defer wait.Done()
			<-start
			address := netip.AddrFrom4([4]byte{192, 0, 2, byte(worker + 1)})
			permit, result := limiter.AdmitSource(context.Background(), v1.OperationRegister, address)
			results <- result
			if permit != nil {
				<-release
				permit.Release()
			}
		}(worker)
	}
	close(start)
	allowed := 0
	for range workers {
		result := <-results
		if result.Allowed() {
			allowed++
		} else if result.Decision != DecisionConcurrencyLimited {
			t.Fatalf("parallel result = %+v, want allowed or concurrency limit", result)
		}
	}
	if allowed != maximum {
		t.Fatalf("allowed = %d, want %d", allowed, maximum)
	}
	close(release)
	wait.Wait()
	if limiter.inFlight != 0 {
		t.Fatalf("inFlight = %d, want 0", limiter.inFlight)
	}
}

func TestParallelActorStageRunsOncePerPermit(t *testing.T) {
	const workers = 32
	config := validTestConfig()
	config.Actor.Burst = workers
	config.IdleTTL = time.Duration(workers) * config.Actor.RefillInterval
	clock := &testClock{now: time.Unix(1_700_000_000, 0)}
	limiter := newTestLimiter(t, config, clock)
	permit, result := limiter.AdmitSource(
		context.Background(),
		v1.OperationRegister,
		netip.MustParseAddr("192.0.2.90"),
	)
	if !result.Allowed() {
		t.Fatalf("source result = %+v, want allowed", result)
	}
	defer permit.Release()

	start := make(chan struct{})
	results := make(chan Result, workers)
	var wait sync.WaitGroup
	for range workers {
		wait.Add(1)
		go func() {
			defer wait.Done()
			<-start
			results <- permit.AdmitActor(context.Background(), v1.OperationRegister, testActorOne)
		}()
	}
	close(start)
	wait.Wait()
	close(results)

	allowed := 0
	for result := range results {
		if result.Allowed() {
			allowed++
		} else if result.Decision != DecisionInvalid {
			t.Fatalf("actor result = %+v, want allowed or invalid", result)
		}
	}
	if allowed != 1 {
		t.Fatalf("allowed actor stages = %d, want 1", allowed)
	}
}

func TestConfigurationBounds(t *testing.T) {
	valid := validTestConfig()
	if _, err := New(valid); err != nil {
		t.Fatalf("New(valid) error = %v", err)
	}
	if _, err := newLimiter(valid, nil); !errors.Is(err, ErrConfiguration) {
		t.Fatalf("newLimiter(nil clock) error = %v, want ErrConfiguration", err)
	}

	tests := []struct {
		name   string
		mutate func(*Config)
	}{
		{name: "source burst zero", mutate: func(c *Config) { c.Source.Burst = 0 }},
		{name: "actor burst too large", mutate: func(c *Config) { c.Actor.Burst = maximumBurst + 1 }},
		{name: "source interval zero", mutate: func(c *Config) { c.Source.RefillInterval = 0 }},
		{name: "actor interval too large", mutate: func(c *Config) { c.Actor.RefillInterval = maximumInterval + 1 }},
		{name: "sources zero", mutate: func(c *Config) { c.MaxSources = 0 }},
		{name: "actors too large", mutate: func(c *Config) { c.MaxActors = maximumEntries + 1 }},
		{name: "concurrency zero", mutate: func(c *Config) { c.MaxConcurrent = 0 }},
		{name: "cleanup zero", mutate: func(c *Config) { c.CleanupLimit = 0 }},
		{name: "cleanup too large", mutate: func(c *Config) { c.CleanupLimit = maximumCleanupLimit + 1 }},
		{name: "idle too short for source", mutate: func(c *Config) { c.IdleTTL = time.Minute }},
		{name: "idle too long", mutate: func(c *Config) { c.IdleTTL = maximumIdleTTL + 1 }},
		{name: "retry zero", mutate: func(c *Config) { c.OverloadRetryAfter = 0 }},
		{name: "retry too long", mutate: func(c *Config) { c.OverloadRetryAfter = maximumRetryAfter + 1 }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			config := valid
			test.mutate(&config)
			_, err := New(config)
			if !errors.Is(err, ErrConfiguration) {
				t.Fatalf("New() error = %v, want ErrConfiguration", err)
			}
		})
	}
}
