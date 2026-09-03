package boardcache

import (
	"context"
	"errors"
	"testing"
	"time"
)

// The cached board's own ProjectUpdatedAt is the `board.changed` verb's path:
// a burst of extension triggers must cost one probe, not one each, and the
// memo must never outlive a mutation this process issued.
func TestCachedBoardProjectUpdatedAtMemoisesForProbeTTL(t *testing.T) {
	now := time.Date(2026, 9, 3, 12, 0, 0, 0, time.UTC)
	c := New(0)
	c.SetClock(func() time.Time { return now })
	moved := now.Add(-time.Hour)
	inner := &probingBoard{updatedAt: moved}
	board := c.Wrap(inner, "octocat", 7)

	cp, ok := board.(ChangeProbe)
	if !ok {
		t.Fatal("cached board must expose the probe capability — the verb has nothing to call otherwise")
	}
	for i := 0; i < 10; i++ {
		ts, err := cp.ProjectUpdatedAt(context.Background())
		if err != nil {
			t.Fatalf("probe %d: %v", i, err)
		}
		if !ts.Equal(moved) {
			t.Fatalf("probe %d: want %v, got %v", i, moved, ts)
		}
	}
	if got := inner.probes(); got != 1 {
		t.Fatalf("ten asks inside ProbeTTL must reach the forge once, got %d", got)
	}

	// Crossing the TTL asks again.
	now = now.Add(ProbeTTL)
	if _, err := cp.ProjectUpdatedAt(context.Background()); err != nil {
		t.Fatal(err)
	}
	if got := inner.probes(); got != 2 {
		t.Fatalf("an ask past ProbeTTL must reach the forge, got %d probes", got)
	}

	// A mutation we issued drops the memo with the snapshots — the change it
	// made must not hide behind a thirty-second-old "nothing moved".
	c.Invalidate("octocat", 7)
	if _, err := cp.ProjectUpdatedAt(context.Background()); err != nil {
		t.Fatal(err)
	}
	if got := inner.probes(); got != 3 {
		t.Fatalf("Invalidate must clear the probe memo, got %d probes", got)
	}
}

func TestCachedBoardProjectUpdatedAtNeverMemoisesAFailure(t *testing.T) {
	c := New(0)
	inner := &probingBoard{probeErr: errors.New("rate limited")}
	cp := c.Wrap(inner, "octocat", 7).(ChangeProbe)

	for i := 0; i < 3; i++ {
		if _, err := cp.ProjectUpdatedAt(context.Background()); err == nil {
			t.Fatal("a failed probe must surface its error")
		}
	}
	if got := inner.probes(); got != 3 {
		t.Fatalf("a failure must be re-asked, not held for ProbeTTL; got %d probes", got)
	}
}

func TestCachedBoardProjectUpdatedAtReportsAMissingProbe(t *testing.T) {
	c := New(0)
	cp := c.Wrap(&countingBoard{}, "octocat", 7).(ChangeProbe)
	if _, err := cp.ProjectUpdatedAt(context.Background()); !errors.Is(err, ErrNoChangeProbe) {
		t.Fatalf("want ErrNoChangeProbe for an adapter with no probe, got %v", err)
	}
}
