package boardcache

import (
	"testing"
	"time"
)

// Pin the deadline jitter for the whole test binary: u = 0.5 is the identity,
// so every test that advances a clock by exactly DefaultTTL or MaxRenewedAge
// keeps meaning what it says. TestJitterBounds exercises the spread itself.
func init() {
	jitterRand = func() float64 { return 0.5 }
}

func TestJitterBounds(t *testing.T) {
	const d = 100 * time.Second
	cases := []struct {
		u    float64
		want time.Duration
	}{
		{0, 80 * time.Second},
		{0.5, 100 * time.Second},
		{0.999, 119960 * time.Millisecond},
	}
	for _, tc := range cases {
		if got := jitter(d, tc.u); got != tc.want {
			t.Errorf("jitter(%s, %v) = %s, want %s", d, tc.u, got, tc.want)
		}
	}
	// A cache built under a pinned variate carries the jittered deadlines.
	prev := jitterRand
	t.Cleanup(func() { jitterRand = prev })
	jitterRand = func() float64 { return 0 }
	c := New(0)
	if c.ttl != jitter(DefaultTTL, 0) || c.maxRenewedAge != jitter(MaxRenewedAge, 0) {
		t.Errorf("New(0) under u=0: ttl=%s maxRenewedAge=%s, want %s / %s",
			c.ttl, c.maxRenewedAge, jitter(DefaultTTL, 0), jitter(MaxRenewedAge, 0))
	}
}
