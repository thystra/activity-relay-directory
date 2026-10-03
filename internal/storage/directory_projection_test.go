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

func TestClassifyDirectoryTierUsesHeartbeatReachabilityAndGraveyardAge(t *testing.T) {
	if DirectoryGraveyardAfter != 30*24*time.Hour {
		t.Fatalf("DirectoryGraveyardAfter = %s, want 30 days", DirectoryGraveyardAfter)
	}
	observed := int64(40_000_000)

	tierOne := validDirectoryProjectionRelayForTest(observed)
	if got, err := ClassifyDirectoryTier(tierOne, observed); err != nil || got != DirectoryTierHeartbeatOnline {
		t.Fatalf("Tier 1 = (%d, %v)", got, err)
	}

	tierTwo := tierOne
	tierTwo.Registered = false
	tierTwo.Discovered = true
	tierTwo.LastSeenUnix = nil
	tierTwo.LastHeartbeatUnix = nil
	tierTwo.HeartbeatState = HeartbeatNotObserved
	if got, err := ClassifyDirectoryTier(tierTwo, observed); err != nil || got != DirectoryTierOnline {
		t.Fatalf("Tier 2 = (%d, %v)", got, err)
	}

	staleHeartbeat := observed - int64(HealthyThrough/time.Second) - 1
	tierTwo = tierOne
	tierTwo.LastHeartbeatUnix = &staleHeartbeat
	if got, err := ClassifyDirectoryTier(tierTwo, observed); err != nil || got != DirectoryTierOnline {
		t.Fatalf("stale-heartbeat Tier 2 = (%d, %v)", got, err)
	}

	tierThree := tierOne
	tierThree.ActorState = ReachabilityUnreachable
	checked := observed - 1
	lastSuccess := observed - int64(30*24*time.Hour/time.Second)
	tierThree.ActorLastCheckedUnix = &checked
	tierThree.ActorLastSuccessUnix = &lastSuccess
	tierThree.Tier = DirectoryTierUnavailable
	if got, err := ClassifyDirectoryTier(tierThree, observed); err != nil || got != DirectoryTierUnavailable {
		t.Fatalf("Tier 3 = (%d, %v)", got, err)
	}
	if err := ValidateDirectoryProjectionRelay(tierThree, observed); err != nil {
		t.Fatalf("Tier 3 rejected: %v", err)
	}

	tierFour := tierThree
	graveyardSeen := observed - int64(DirectoryGraveyardAfter/time.Second)
	tierFour.LastSeenUnix = &graveyardSeen
	tierFour.LastHeartbeatUnix = &graveyardSeen
	tierFour.HeartbeatState = HeartbeatPrune
	tierFour.ActorLastSuccessUnix = &graveyardSeen
	tierFour.FirstKnownUnix = graveyardSeen - 1
	tierFour.Tier = DirectoryTierGraveyard
	if got, err := ClassifyDirectoryTier(tierFour, observed); err != nil || got != DirectoryTierGraveyard {
		t.Fatalf("Tier 4 = (%d, %v)", got, err)
	}
	if err := ValidateDirectoryProjectionRelay(tierFour, observed); err != nil {
		t.Fatalf("Tier 4 rejected: %v", err)
	}
}

func TestDirectoryProjectionTierOneRequiresActualRecentHeartbeat(t *testing.T) {
	observed := int64(50_000_000)
	relay := validDirectoryProjectionRelayForTest(observed)

	// A recent register advances last_seen but does not create heartbeat evidence.
	relay.LastHeartbeatUnix = nil
	relay.Tier = DirectoryTierOnline
	if got, err := ClassifyDirectoryTier(relay, observed); err != nil || got != DirectoryTierOnline {
		t.Fatalf("registered without heartbeat = (%d, %v), want Tier 2", got, err)
	}

	boundary := observed - int64(HealthyThrough/time.Second)
	relay.LastHeartbeatUnix = &boundary
	relay.Tier = DirectoryTierHeartbeatOnline
	if got, err := ClassifyDirectoryTier(relay, observed); err != nil || got != DirectoryTierHeartbeatOnline {
		t.Fatalf("heartbeat boundary = (%d, %v), want Tier 1", got, err)
	}
}

func TestValidateDirectoryProjectionRelayRejectsInconsistentEvidence(t *testing.T) {
	observed := int64(30_000_000)
	base := validDirectoryProjectionRelayForTest(observed)
	cases := []struct {
		name string
		edit func(*DirectoryProjectionRelay)
	}{
		{name: "no path", edit: func(r *DirectoryProjectionRelay) { r.LifecycleKnown = false; r.Registered = false }},
		{name: "registered without lifecycle", edit: func(r *DirectoryProjectionRelay) { r.LifecycleKnown = false }},
		{name: "wrong heartbeat", edit: func(r *DirectoryProjectionRelay) { r.HeartbeatState = HeartbeatDead }},
		{name: "heartbeat after last seen", edit: func(r *DirectoryProjectionRelay) { future := *r.LastSeenUnix + 1; r.LastHeartbeatUnix = &future }},
		{name: "future actor check", edit: func(r *DirectoryProjectionRelay) {
			future := observed + 1
			r.ActorLastCheckedUnix = &future
			r.ActorLastSuccessUnix = &future
		}},
		{name: "reachable timestamps differ", edit: func(r *DirectoryProjectionRelay) { earlier := observed - 2; r.ActorLastSuccessUnix = &earlier }},
		{name: "noncanonical inbox", edit: func(r *DirectoryProjectionRelay) { r.InboxURL = "HTTPS://relay.example/inbox" }},
		{name: "checked inbox without time", edit: func(r *DirectoryProjectionRelay) { r.InboxProbeState = InboxResponsive; r.InboxLastCheckedUnix = nil }},
		{name: "wrong tier", edit: func(r *DirectoryProjectionRelay) { r.Tier = DirectoryTierOnline }},
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
	lastHeartbeat := observed - 10
	checked := observed - 5
	inboxDeclared := observed - 5
	inboxChecked := observed - 5
	verified := observed - 10
	return DirectoryProjectionRelay{
		RelayActor:           "https://relay.example/actor",
		PublicBaseURL:        "https://relay.example",
		LifecycleKnown:       true,
		Registered:           true,
		FirstKnownUnix:       observed - 100,
		Tier:                 DirectoryTierHeartbeatOnline,
		HeartbeatState:       HeartbeatHealthy,
		LastSeenUnix:         &lastSeen,
		LastHeartbeatUnix:    &lastHeartbeat,
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
