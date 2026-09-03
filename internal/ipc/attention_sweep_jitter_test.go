package ipc

import (
	"testing"
	"time"
)

// Pin the sweep-gap jitter for the whole test binary: u = 0.5 is the identity,
// so tests that advance the sweep clock by exactly SweepMinGap keep meaning
// what they say. TestJitteredSweepGap_Bounds exercises the spread itself.
func init() {
	sweepJitterRand = func() float64 { return 0.5 }
}

func TestJitteredSweepGap_Bounds(t *testing.T) {
	prev := sweepJitterRand
	t.Cleanup(func() { sweepJitterRand = prev })

	cases := []struct {
		u    float64
		want time.Duration
	}{
		{0, 48 * time.Second},
		{0.5, 60 * time.Second},
		{0.999, 71976 * time.Millisecond},
	}
	for _, tc := range cases {
		sweepJitterRand = func() float64 { return tc.u }
		if got := jitteredSweepGap(); got != tc.want {
			t.Errorf("jitteredSweepGap() at u=%v = %s, want %s", tc.u, got, tc.want)
		}
	}

	// NewServer draws the gap once; a literal Server falls back to the constant.
	sweepJitterRand = func() float64 { return 0 }
	if got := NewServer(nil).sweepGap(); got != 48*time.Second {
		t.Errorf("NewServer gap at u=0 = %s, want 48s", got)
	}
	if got := (&Server{}).sweepGap(); got != SweepMinGap {
		t.Errorf("literal Server gap = %s, want SweepMinGap", got)
	}
}
