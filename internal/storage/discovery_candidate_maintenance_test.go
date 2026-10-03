package storage

import (
	"testing"
	"time"
)

func TestDiscoveryCandidateRetryDelay(t *testing.T) {
	cases := []struct {
		failures int64
		want     time.Duration
	}{
		{0, 0},
		{1, 6 * time.Hour},
		{2, 12 * time.Hour},
		{3, 24 * time.Hour},
		{4, 72 * time.Hour},
		{5, 7 * 24 * time.Hour},
		{50, 7 * 24 * time.Hour},
	}
	for _, test := range cases {
		if got := DiscoveryCandidateRetryDelay(test.failures); got != test.want {
			t.Fatalf("DiscoveryCandidateRetryDelay(%d) = %s, want %s", test.failures, got, test.want)
		}
	}
}
