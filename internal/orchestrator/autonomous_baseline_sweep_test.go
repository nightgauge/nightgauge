package orchestrator

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/nightgauge/nightgauge/internal/depgraph"
)

// #885. `baseline-gate promote` was correctly designed, correctly implemented,
// and never automatically ran: its only caller was a CLI verb nothing invoked
// on a schedule, while the sibling `blocked_dependency` kind had been resumed
// by the daemon since #231. A deferred item therefore waited for an operator
// who had no reason to know it was waiting.
//
// These tests pin the automatic trigger — the WIRING and its pacing — plus the
// end-to-end resume. They do not re-test IsLastNGreen's own run-history rules;
// those belong to internal/intelligence/baselineGate.

// fakeBaselineEvaluator answers IsLastNGreen from a per-workflow script.
type fakeBaselineEvaluator struct {
	mu     sync.Mutex
	green  map[string]bool
	err    error
	asked  []string
	branch []string
}

func (f *fakeBaselineEvaluator) IsLastNGreen(_ context.Context, _, _, workflow, branch, _ string, _ int) (bool, []int64, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.asked = append(f.asked, workflow)
	f.branch = append(f.branch, branch)
	if f.err != nil {
		return false, nil, f.err
	}
	return f.green[workflow], []int64{101, 102}, nil
}

func (f *fakeBaselineEvaluator) questions() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]string, len(f.asked))
	copy(out, f.asked)
	return out
}

// newBaselineSweepScheduler builds an autonomous scheduler with an inner queue
// scheduler holding the given paused items, and a fake evaluator wired in.
func newBaselineSweepScheduler(t *testing.T, eval *fakeBaselineEvaluator, items ...QueueItem) *AutonomousScheduler {
	t.Helper()
	stubReconcileGh(t, func(_ context.Context, _ ...string) ([]byte, error) {
		return []byte("[]"), nil
	})
	inner := &Scheduler{
		workspaceRoot: t.TempDir(),
		repoRunning:   make(map[string]int),
		mergeLocks:    make(map[string]*sync.Mutex),
	}
	inner.queue = items

	as := NewAutonomousScheduler(inner, nil,
		[]depgraph.RepoConfig{{Owner: "acme", Name: "web", Project: 7}},
		nil, DefaultAutonomousConfig(), t.TempDir())
	as.state.Status = "running"
	as.buildGraphFn = func(context.Context) (*depgraph.Graph, error) {
		return buildTestGraph(nil, nil), nil
	}
	as.baselineGreenThreshold = 2
	as.baselineEvaluatorFn = func(_, _ string) (BaselinePromoteEvaluator, string, bool, error) {
		return eval, "main", true, nil
	}
	t.Cleanup(as.drainBackground)
	return as
}

func pausedBaselineItem(issue int, workflow string) QueueItem {
	return QueueItem{
		Repo:        "acme/web",
		IssueNumber: issue,
		Title:       "deferred",
		Status:      "paused",
		PausedReason: &QueuePausedReason{
			Kind:         "baseline_ci_red",
			Workflow:     workflow,
			Job:          "Build",
			FailedRuns:   3,
			LookbackRuns: 5,
		},
	}
}

// TestBaselineSweep_ResumesWhenBaselineGoesGreen is the acceptance criterion
// stated directly: a deferred item resumes once the baseline is green.
func TestBaselineSweep_ResumesWhenBaselineGoesGreen(t *testing.T) {
	eval := &fakeBaselineEvaluator{green: map[string]bool{"ci.yml": false}}
	as := newBaselineSweepScheduler(t, eval, pausedBaselineItem(42, "ci.yml"))

	// Baseline still red: the item stays paused.
	as.runCycle(context.Background())
	if got := as.scheduler.ListPausedByKind("baseline_ci_red"); len(got) != 1 {
		t.Fatalf("item was released while its baseline was still red; paused = %d, want 1", len(got))
	}

	// Baseline goes green. Age the pace gate so the next cycle sweeps.
	eval.mu.Lock()
	eval.green["ci.yml"] = true
	eval.mu.Unlock()
	as.mu.Lock()
	as.lastBaselineSweepAt = time.Now().Add(-baselineDeferralSweepInterval - time.Minute)
	as.mu.Unlock()

	as.runCycle(context.Background())

	if got := as.scheduler.ListPausedByKind("baseline_ci_red"); len(got) != 0 {
		t.Fatalf("baseline went green and the item is still paused (%d) — it does not resume on its own", len(got))
	}
}

// TestBaselineSweep_RunsOnTheAutonomousCycle is the load-bearing wiring test:
// delete `as.sweepBaselineDeferrals(ctx)` from runCycle and this goes red. It
// drives the real runCycle rather than the receiver, so it covers the wiring
// and not merely the function body.
func TestBaselineSweep_RunsOnTheAutonomousCycle(t *testing.T) {
	eval := &fakeBaselineEvaluator{green: map[string]bool{"ci.yml": false}}
	as := newBaselineSweepScheduler(t, eval, pausedBaselineItem(42, "ci.yml"))

	as.runCycle(context.Background())

	if q := eval.questions(); len(q) != 1 || q[0] != "ci.yml" {
		t.Fatalf("the autonomous cycle did not run the baseline-deferral sweep; asked = %v (want [ci.yml])", q)
	}
}

// TestBaselineSweep_RunsEvenWithNoFreeSlots pins the PLACEMENT. runCycle
// returns early when no pipeline slot is free; a saturated fleet is exactly
// when a deferred item waits longest, and the sweep dispatches nothing, so it
// must sit above that gate. Moving the call below it compiles and passes the
// wiring test above while silently reinstating half the defect.
func TestBaselineSweep_RunsEvenWithNoFreeSlots(t *testing.T) {
	eval := &fakeBaselineEvaluator{green: map[string]bool{"ci.yml": true}}
	as := newBaselineSweepScheduler(t, eval, pausedBaselineItem(42, "ci.yml"))

	as.config.MaxConcurrent = 1
	as.state.Running = []RunningItem{{Repo: "acme/web", Number: 1, Title: "busy"}}
	if as.effectiveAvailableSlots() > 0 {
		t.Fatalf("fixture failed to saturate the fleet: %d slots free", as.effectiveAvailableSlots())
	}

	as.runCycle(context.Background())

	if len(eval.questions()) != 1 {
		t.Fatal("sweep skipped while the fleet was saturated — the wiring is below the slot gate")
	}
}

// TestBaselineSweep_PacedNotPerCycle. The cycle ticks every 30s; this sweep
// asks GitHub for workflow run history per deferred item. Running it per cycle
// would issue the same query sixty times per baseline repair to observe a fact
// that changed once.
func TestBaselineSweep_PacedNotPerCycle(t *testing.T) {
	eval := &fakeBaselineEvaluator{green: map[string]bool{"ci.yml": false}}
	as := newBaselineSweepScheduler(t, eval, pausedBaselineItem(42, "ci.yml"))

	for i := 0; i < 5; i++ {
		as.runCycle(context.Background())
	}
	if n := len(eval.questions()); n != 1 {
		t.Fatalf("swept %d times across 5 cycles, want 1 — the %s pace gate is not holding", n, baselineDeferralSweepInterval)
	}

	as.mu.Lock()
	as.lastBaselineSweepAt = time.Now().Add(-baselineDeferralSweepInterval - time.Minute)
	as.mu.Unlock()

	as.runCycle(context.Background())
	if n := len(eval.questions()); n != 2 {
		t.Fatalf("swept %d times, want 2 — the pace gate never reopens, so a deferral would resume at most once per process", n)
	}
}

// TestBaselineSweep_NoDeferralsCostsNothing. The common case is an empty
// deferral list, and it must not pay for a forge round-trip to discover that.
func TestBaselineSweep_NoDeferralsCostsNothing(t *testing.T) {
	eval := &fakeBaselineEvaluator{green: map[string]bool{}}
	as := newBaselineSweepScheduler(t, eval) // no paused items

	as.runCycle(context.Background())

	if n := len(eval.questions()); n != 0 {
		t.Fatalf("asked the forge %d question(s) with nothing deferred, want 0", n)
	}
}

// TestBaselineSweep_ErrorLeavesTheItemPaused. One unreachable workflow must
// not strand the others, and must never release an item on an unanswered
// question — "we could not check" is not "the baseline is green".
func TestBaselineSweep_ErrorLeavesTheItemPaused(t *testing.T) {
	eval := &fakeBaselineEvaluator{err: errors.New("forge unreachable")}
	as := newBaselineSweepScheduler(t, eval,
		pausedBaselineItem(42, "ci.yml"),
		pausedBaselineItem(43, "e2e.yml"))

	as.runCycle(context.Background())

	if got := as.scheduler.ListPausedByKind("baseline_ci_red"); len(got) != 2 {
		t.Fatalf("paused = %d, want 2 — an unanswered check must not release an item", len(got))
	}
	if n := len(eval.questions()); n != 2 {
		t.Fatalf("asked %d question(s), want 2 — one failure aborted the sweep for the rest", n)
	}
}

// TestBaselineSweep_SkipsItemWithNoWorkflow. A pause record naming no workflow
// has no question to ask; it must stay paused rather than be promoted by
// default.
func TestBaselineSweep_SkipsItemWithNoWorkflow(t *testing.T) {
	eval := &fakeBaselineEvaluator{green: map[string]bool{}}
	item := pausedBaselineItem(42, "")
	as := newBaselineSweepScheduler(t, eval, item)

	as.runCycle(context.Background())

	if got := as.scheduler.ListPausedByKind("baseline_ci_red"); len(got) != 1 {
		t.Fatalf("an item naming no workflow was released; paused = %d, want 1", len(got))
	}
	if n := len(eval.questions()); n != 0 {
		t.Fatalf("asked %d question(s) about an item naming no workflow, want 0", n)
	}
}
