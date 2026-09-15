package storage

import (
	"errors"
	"testing"
	"time"
)

func TestClassifyPublicHeartbeat(t *testing.T) {
	observed := int64(10_000_000)
	cases := []struct {
		name string
		seen *int64
		want PublicHeartbeatState
	}{
		{name: "not observed", seen: nil, want: HeartbeatNotObserved},
		{name: "healthy boundary", seen: int64Pointer(observed - int64(HealthyThrough/time.Second)), want: HeartbeatHealthy},
		{name: "stale", seen: int64Pointer(observed - int64(HealthyThrough/time.Second) - 1), want: HeartbeatStale},
		{name: "dead boundary", seen: int64Pointer(observed - int64(StaleBefore/time.Second)), want: HeartbeatDead},
		{name: "prune boundary", seen: int64Pointer(observed - int64(DeadBefore/time.Second)), want: HeartbeatPrune},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			got, err := ClassifyPublicHeartbeat(test.seen, observed)
			if err != nil || got != test.want {
				t.Fatalf("ClassifyPublicHeartbeat() = (%q, %v), want %q", got, err, test.want)
			}
		})
	}
	future := observed + 1
	if _, err := ClassifyPublicHeartbeat(&future, observed); !errors.Is(err, ErrHealthTime) {
		t.Fatalf("future heartbeat error = %v, want ErrHealthTime", err)
	}
}

func TestDirectoryProjectionEligibilityKeepsHeartbeatAndReachabilityIndependent(t *testing.T) {
	observed := int64(20_000_000)
	freshReachability := observed - int64(ReachabilityFreshness/time.Second)
	oldHeartbeat := observed - int64(DeadBefore/time.Second)

	registeredReachable := validDirectoryProjectionRelayForTest(observed)
	registeredReachable.LastSeenUnix = &oldHeartbeat
	registeredReachable.HeartbeatState = HeartbeatPrune
	registeredReachable.ActorLastCheckedUnix = &freshReachability
	registeredReachable.ActorLastSuccessUnix = &freshReachability
	if err := ValidateDirectoryProjectionRelay(registeredReachable, observed); err != nil {
		t.Fatalf("registered reachable prune-boundary relay rejected: %v", err)
	}

	registeredUnreachable := registeredReachable
	registeredUnreachable.ActorState = ReachabilityUnreachable
	if registeredUnreachable.PublicEligible(observed) {
		t.Fatal("prune-boundary registered relay remained eligible on current unreachable evidence")
	}

	discovered := registeredReachable
	discovered.Registered = false
	discovered.Discovered = true
	discovered.LastSeenUnix = nil
	discovered.HeartbeatState = HeartbeatNotObserved
	if err := ValidateDirectoryProjectionRelay(discovered, observed); err != nil {
		t.Fatalf("discovered-only reachable relay rejected: %v", err)
	}

	discovered.ActorState = ReachabilityUnreachable
	if discovered.PublicEligible(observed) {
		t.Fatal("discovered-only relay remained eligible on current unreachable evidence")
	}
}

func TestDirectoryProjectionEligibilityBoundaries(t *testing.T) {
	observed := int64(40_000_000)

	// A registered relay inside the original v1 heartbeat window remains public
	// even when the latest independent actor check is unreachable.
	deadSeen := observed - int64(StaleBefore/time.Second)
	checked := observed - 1
	oldSuccess := observed - int64(ReachabilityFreshness/time.Second) - 1
	registered := validDirectoryProjectionRelayForTest(observed)
	registered.LastSeenUnix = &deadSeen
	registered.HeartbeatState = HeartbeatDead
	registered.ActorState = ReachabilityUnreachable
	registered.ActorLastCheckedUnix = &checked
	registered.ActorLastSuccessUnix = &oldSuccess
	if err := ValidateDirectoryProjectionRelay(registered, observed); err != nil {
		t.Fatalf("dead registered relay with independent unreachable evidence rejected: %v", err)
	}

	// Discovery eligibility includes the exact six-hour boundary but expires one
	// second later. No heartbeat is fabricated for this path.
	fresh := observed - int64(ReachabilityFreshness/time.Second)
	discovered := validDirectoryProjectionRelayForTest(observed)
	discovered.Registered = false
	discovered.Discovered = true
	discovered.LastSeenUnix = nil
	discovered.HeartbeatState = HeartbeatNotObserved
	discovered.ActorState = ReachabilityReachable
	discovered.ActorLastCheckedUnix = &fresh
	discovered.ActorLastSuccessUnix = &fresh
	if err := ValidateDirectoryProjectionRelay(discovered, observed); err != nil {
		t.Fatalf("discovery at freshness boundary rejected: %v", err)
	}

	expired := fresh - 1
	discovered.ActorLastCheckedUnix = &expired
	discovered.ActorLastSuccessUnix = &expired
	if discovered.PublicEligible(observed) {
		t.Fatal("discovery older than freshness boundary remained public")
	}
	if err := ValidateDirectoryProjectionRelay(discovered, observed); !errors.Is(err, ErrDirectoryProjectionData) {
		t.Fatalf("expired discovered relay error = %v, want ErrDirectoryProjectionData", err)
	}

	// A registered relay at the 30-day heartbeat boundary may use current actor
	// reachability, but not actor evidence older than the same freshness bound.
	pruneSeen := observed - int64(DeadBefore/time.Second)
	registered = validDirectoryProjectionRelayForTest(observed)
	registered.LastSeenUnix = &pruneSeen
	registered.HeartbeatState = HeartbeatPrune
	registered.ActorLastCheckedUnix = &fresh
	registered.ActorLastSuccessUnix = &fresh
	if err := ValidateDirectoryProjectionRelay(registered, observed); err != nil {
		t.Fatalf("prune-boundary relay with fresh reachability rejected: %v", err)
	}
	registered.ActorLastCheckedUnix = &expired
	registered.ActorLastSuccessUnix = &expired
	if registered.PublicEligible(observed) {
		t.Fatal("prune-boundary relay remained public with expired reachability")
	}
}

func TestValidateDirectoryProjectionRelayRejectsInconsistentEvidence(t *testing.T) {
	observed := int64(30_000_000)
	base := validDirectoryProjectionRelayForTest(observed)
	cases := []struct {
		name string
		edit func(*DirectoryProjectionRelay)
	}{
		{name: "no path", edit: func(r *DirectoryProjectionRelay) { r.Registered = false }},
		{name: "wrong heartbeat", edit: func(r *DirectoryProjectionRelay) { r.HeartbeatState = HeartbeatDead }},
		{name: "future actor check", edit: func(r *DirectoryProjectionRelay) {
			future := observed + 1
			r.ActorLastCheckedUnix = &future
			r.ActorLastSuccessUnix = &future
		}},
		{name: "reachable timestamps differ", edit: func(r *DirectoryProjectionRelay) { earlier := observed - 2; r.ActorLastSuccessUnix = &earlier }},
		{name: "noncanonical inbox", edit: func(r *DirectoryProjectionRelay) { r.InboxURL = "HTTPS://relay.example/inbox" }},
		{name: "checked inbox without time", edit: func(r *DirectoryProjectionRelay) { r.InboxProbeState = InboxResponsive; r.InboxLastCheckedUnix = nil }},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			relay := base
			test.edit(&relay)
			if err := ValidateDirectoryProjectionRelay(relay, observed); !errors.Is(err, ErrDirectoryProjectionData) {
				t.Fatalf("ValidateDirectoryProjectionRelay() error = %v, want ErrDirectoryProjectionData", err)
			}
		})
	}
}

func validDirectoryProjectionRelayForTest(observed int64) DirectoryProjectionRelay {
	lastSeen := observed - 10
	checked := observed - 5
	inboxDeclared := observed - 5
	inboxChecked := observed - 5
	verified := observed - 10
	return DirectoryProjectionRelay{
		RelayActor:           "https://relay.example/actor",
		PublicBaseURL:        "https://relay.example",
		Registered:           true,
		HeartbeatState:       HeartbeatHealthy,
		LastSeenUnix:         &lastSeen,
		ActorState:           ReachabilityReachable,
		ActorLastCheckedUnix: &checked,
		ActorLastSuccessUnix: &checked,
		InboxURL:             "https://relay.example/inbox",
		InboxDeclaredUnix:    &inboxDeclared,
		InboxProbeState:      InboxMethodRejected,
		InboxLastCheckedUnix: &inboxChecked,
		RFC9421VerifiedUnix:  &verified,
	}
}

func int64Pointer(value int64) *int64 { return &value }
