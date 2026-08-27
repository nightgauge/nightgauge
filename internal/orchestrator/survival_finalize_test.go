package orchestrator

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/nightgauge/nightgauge/internal/intelligence/survival"
)

func seedPending(t *testing.T, root, repo string, issue int, mergedAt time.Time) {
	t.Helper()
	store := survival.NewStore(root)
	rec := survival.NewPending(repo, issue, issue+1000,
		fmt.Sprintf("sha%d", issue), mergedAt.UTC().Format(time.RFC3339), "")
	if _, err := store.Append(rec); err != nil {
		t.Fatalf("append: %v", err)
	}
}

func pendingCount(t *testing.T, root string) int {
	t.Helper()
	recs, err := survival.NewStore(root).Pending()
	if err != nil {
		t.Fatalf("pending: %v", err)
	}
	return len(recs)
}

// TestRunCycle_SweepsSurvivalWithZeroFreeSlots is the slot-gate guard the issue
// asks for by name — and it drives runCycle, not the sweep.
//
// An earlier version of this test called `as.sweepSurvivalRecords(ctx)`
// directly. That proves the sweep works; it says nothing about WHERE it is
// called from, and moving it back below `if availableSlots <= 0 { return }`
// left it green. Mutation testing caught that. The whole defect in #992 is a
// placement defect, so the test has to exercise the placement.
//
// The sweep used to sit below that gate, so a saturated fleet finalized
// nothing — and a saturated fleet is exactly when merges accumulate fastest.
// The neighbouring reconcileEpicRollup was deliberately hoisted above the same
// gate with the comment that convergence must not depend on free slots; the
// identical argument was never applied here.
func TestRunCycle_SweepsSurvivalWithZeroFreeSlots(t *testing.T) {
	root := t.TempDir()
	as := NewAutonomousScheduler(nil, nil, nil, nil, DefaultAutonomousConfig(), root)

	// Saturate the fleet: every slot consumed, so runCycle returns at the slot
	// gate before it ever builds a graph.
	as.mu.Lock()
	as.state.Status = "running"
	as.config.MaxConcurrent = 1
	as.state.Running = []RunningItem{{Repo: "o/r", Number: 1}}
	as.mu.Unlock()

	if got := as.effectiveAvailableSlots(); got != 0 {
		t.Fatalf("fixture: effectiveAvailableSlots = %d, want 0", got)
	}
	seedPending(t, root, "o/r", 42, time.Now().AddDate(0, 0, -30))

	as.runCycle(context.Background())

	as.mu.Lock()
	swept := as.lastSurvivalSweepAt
	as.mu.Unlock()
	if swept.IsZero() {
		t.Error("runCycle returned at the slot gate without reaching the survival sweep — " +
			"the sweep dispatches nothing and must not be conditional on free slots")
	}
}

// TestSweepSurvivalRecords_IsPacedIndependently guards against the sweep going
// back to riding `graphWasFresh`, and against it running on every 30s tick.
func TestSweepSurvivalRecords_IsPacedIndependently(t *testing.T) {
	root := t.TempDir()
	as := NewAutonomousScheduler(nil, nil, nil, nil, DefaultAutonomousConfig(), root)
	seedPending(t, root, "o/r", 7, time.Now().AddDate(0, 0, -30))

	as.sweepSurvivalRecords(context.Background())
	as.mu.Lock()
	first := as.lastSurvivalSweepAt
	as.mu.Unlock()
	if first.IsZero() {
		t.Fatal("first sweep did not run — a zero timestamp must sweep immediately, " +
			"because a daemon starting after a long stop has the biggest backlog")
	}

	// Immediately again: the pace gate must refuse.
	as.sweepSurvivalRecords(context.Background())
	as.mu.Lock()
	second := as.lastSurvivalSweepAt
	as.mu.Unlock()
	if !second.Equal(first) {
		t.Error("a second sweep inside the interval was not paced — survival detection " +
			"makes a GitHub call per due record and must not run on every tick")
	}

	// Past the interval: it runs again.
	as.mu.Lock()
	as.lastSurvivalSweepAt = time.Now().Add(-2 * survivalSweepInterval)
	as.mu.Unlock()
	as.sweepSurvivalRecords(context.Background())
	as.mu.Lock()
	third := as.lastSurvivalSweepAt
	as.mu.Unlock()
	if third.Equal(first) || third.Before(first) {
		t.Error("the sweep did not resume after the interval elapsed")
	}
}

// TestSweepSurvivalRecords_NoPendingCostsNothing guards the property that makes
// this safe to call from the post-merge hook on every single merge.
func TestSweepSurvivalRecords_NoPendingCostsNothing(t *testing.T) {
	root := t.TempDir()
	as := NewAutonomousScheduler(nil, nil, nil, nil, DefaultAutonomousConfig(), root)

	// No records at all. Must not panic and must not need a client.
	as.sweepSurvivalRecords(context.Background())

	if pendingCount(t, root) != 0 {
		t.Error("an empty store gained records")
	}
}
