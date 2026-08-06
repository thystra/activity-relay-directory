package storage

import (
	"errors"
	"testing"
	"time"

	v1 "github.com/thystra/activity-relay-directory/internal/protocol/v1"
)

func TestClassifyHealthVersionOneBoundaries(t *testing.T) {
	const observed = int64(4_000_000)

	tests := []struct {
		name string
		age  time.Duration
		want v1.HealthState
	}{
		{name: "zero", age: 0, want: v1.HealthHealthy},
		{name: "exactly 36 hours", age: HealthyThrough, want: v1.HealthHealthy},
		{name: "after 36 hours", age: HealthyThrough + time.Second, want: v1.HealthStale},
		{name: "before 7 days", age: StaleBefore - time.Second, want: v1.HealthStale},
		{name: "exactly 7 days", age: StaleBefore, want: v1.HealthDead},
		{name: "before 30 days", age: DeadBefore - time.Second, want: v1.HealthDead},
		{name: "exactly 30 days", age: DeadBefore, want: v1.HealthPrune},
		{name: "after 30 days", age: DeadBefore + time.Second, want: v1.HealthPrune},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			lastSeen := observed - int64(test.age/time.Second)
			got, err := ClassifyHealth(lastSeen, observed)
			if err != nil || got != test.want {
				t.Fatalf("ClassifyHealth() = (%q, %v), want (%q, nil)", got, err, test.want)
			}
		})
	}
}

func TestClassifyHealthRejectsInvalidOrRegressingTime(t *testing.T) {
	for _, test := range []struct {
		name     string
		lastSeen int64
		observed int64
	}{
		{name: "negative last seen", lastSeen: -1, observed: 0},
		{name: "negative observation", lastSeen: 0, observed: -1},
		{name: "future last seen", lastSeen: 101, observed: 100},
	} {
		t.Run(test.name, func(t *testing.T) {
			state, err := ClassifyHealth(test.lastSeen, test.observed)
			if state != "" || !errors.Is(err, ErrHealthTime) {
				t.Fatalf("ClassifyHealth() = (%q, %v), want ErrHealthTime", state, err)
			}
		})
	}
}

func TestHealthProjectionCursorValidation(t *testing.T) {
	if !(HealthProjectionCursor{}).Valid() {
		t.Fatal("zero cursor is invalid")
	}
	if !(HealthProjectionCursor{LastSeenUnix: 0, RelayActor: "https://relay.example/actor"}).Valid() {
		t.Fatal("complete epoch cursor is invalid")
	}
	for _, cursor := range []HealthProjectionCursor{
		{LastSeenUnix: -1, RelayActor: "https://relay.example/actor"},
		{LastSeenUnix: 1},
	} {
		if cursor.Valid() {
			t.Fatalf("cursor %#v is valid", cursor)
		}
	}
}

func TestHealthProjectionPublicEligibilityFailsClosedAtPruneBoundary(t *testing.T) {
	for _, test := range []struct {
		state v1.HealthState
		want  bool
	}{
		{state: v1.HealthHealthy, want: true},
		{state: v1.HealthStale, want: true},
		{state: v1.HealthDead, want: true},
		{state: v1.HealthPrune, want: false},
		{state: v1.HealthState("unknown"), want: false},
	} {
		relay := HealthProjectionRelay{HealthState: test.state}
		if got := relay.PublicEligible(); got != test.want {
			t.Fatalf("PublicEligible(%q) = %t, want %t", test.state, got, test.want)
		}
	}
}
