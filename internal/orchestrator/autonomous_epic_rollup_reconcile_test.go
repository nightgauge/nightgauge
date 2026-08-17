package orchestrator

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/nightgauge/nightgauge/internal/depgraph"
	gh "github.com/nightgauge/nightgauge/internal/github"
)

// #656. EpicService.ReconcileBoard was correctly designed, correctly
// implemented, and never automatically ran: its only production caller was the
// `nightgauge project reconcile` CLI verb. Epic rollup was therefore a side
// effect of the pipeline performing the merge, and AGENTS.md mandates manual
// squash merges as the routine path — so every hand-merged PR left its parent
// epic open forever.
//
// These tests pin the automatic trigger. They assert the WIRING (does the
// autonomous cycle call the backstop?) and its pacing, never ReconcileBoard's
// own R1/R2/R3 rules — those belong to internal/github/reconcile_test.go and
// are deliberately not duplicated here.

// boardCall records one reconcileBoardFn invocation.
type boardCall struct {
	owner     string
	project   int
	ownerType gh.OwnerType
}

// recordingBoardReconciler installs a fake board reconciler and returns a
// getter for what it was asked to sweep. The scheduler is driven through
// runCycle, so `gh` is stubbed too: the cycle's neighbouring reconcilers shell
// out to `gh pr list`, and a test that reaches the real forge is a time bomb
// (#660).
func recordingBoardReconciler(t *testing.T, as *AutonomousScheduler, err error) func() []boardCall {
	t.Helper()
	stubReconcileGh(t, func(_ context.Context, _ ...string) ([]byte, error) {
		return []byte("[]"), nil
	})
	var mu sync.Mutex
	var calls []boardCall
	as.reconcileBoardFn = func(_ context.Context, owner string, project int, ot gh.OwnerType) (*gh.ReconcileResult, error) {
		mu.Lock()
		calls = append(calls, boardCall{owner: owner, project: project, ownerType: ot})
		mu.Unlock()
		if err != nil {
			return nil, err
		}
		return &gh.ReconcileResult{Checked: 12, EpicsClosed: 1}, nil
	}
	return func() []boardCall {
		mu.Lock()
		defer mu.Unlock()
		out := make([]boardCall, len(calls))
		copy(out, calls)
		return out
	}
}

func newRollupScheduler(t *testing.T, repos ...depgraph.RepoConfig) *AutonomousScheduler {
	t.Helper()
	as := NewAutonomousScheduler(nil, nil, repos, nil, DefaultAutonomousConfig(), t.TempDir())
	as.state.Status = "running"
	as.buildGraphFn = func(context.Context) (*depgraph.Graph, error) {
		return buildTestGraph(nil, nil), nil
	}
	t.Cleanup(as.drainBackground)
	return as
}

// TestEpicRollupBackstop_RunsOnTheAutonomousCycle is the load-bearing test:
// delete the `as.reconcileEpicRollup(ctx)` call from runCycle and this goes
// red. It drives the real runCycle rather than calling the receiver directly,
// so it covers the wiring and not merely the function body.
func TestEpicRollupBackstop_RunsOnTheAutonomousCycle(t *testing.T) {
	as := newRollupScheduler(t, depgraph.RepoConfig{Owner: "acme", Name: "web", Project: 7, OwnerType: gh.OwnerTypeOrg})
	calls := recordingBoardReconciler(t, as, nil)

	as.runCycle(context.Background())

	got := calls()
	if len(got) != 1 {
		t.Fatalf("the autonomous cycle did not run the board-wide epic-rollup backstop; calls = %v (want exactly 1)", got)
	}
	if got[0].owner != "acme" || got[0].project != 7 || got[0].ownerType != gh.OwnerTypeOrg {
		t.Fatalf("swept the wrong board: %+v (want acme/7 org)", got[0])
	}
}

// TestEpicRollupBackstop_RunsEvenWithNoFreeSlots pins the PLACEMENT, not just
// the presence, of the call. runCycle returns early when no pipeline slot is
// free — above the graph build and every reconciler gated on it. A saturated
// fleet is exactly when hand-merged PRs pile up unrolled-up, so the backstop
// must sit above that gate. Moving the call below it compiles, passes the
// test above, and silently reinstates half the defect.
func TestEpicRollupBackstop_RunsEvenWithNoFreeSlots(t *testing.T) {
	as := newRollupScheduler(t, depgraph.RepoConfig{Owner: "acme", Name: "web", Project: 7})
	calls := recordingBoardReconciler(t, as, nil)

	// Fill every slot so the cycle bails before building the graph.
	as.config.MaxConcurrent = 1
	as.state.Running = []RunningItem{{Repo: "acme/web", Number: 1, Title: "busy"}}
	if as.effectiveAvailableSlots() > 0 {
		t.Fatalf("fixture failed to saturate the fleet: %d slots free", as.effectiveAvailableSlots())
	}

	as.runCycle(context.Background())

	if len(calls()) != 1 {
		t.Fatalf("backstop skipped while the fleet was saturated; calls = %d (want 1) — the wiring is below the slot gate", len(calls()))
	}
}

// TestEpicRollupBackstop_PacedNotPerCycle. The cycle ticks every 30s; this
// sweep paginates every item on the board. Running it per cycle would spend a
// board-sized GraphQL bill 120 times an hour to observe a fact that only
// changes when a human merges something.
func TestEpicRollupBackstop_PacedNotPerCycle(t *testing.T) {
	as := newRollupScheduler(t, depgraph.RepoConfig{Owner: "acme", Name: "web", Project: 7})
	calls := recordingBoardReconciler(t, as, nil)

	for i := 0; i < 5; i++ {
		as.runCycle(context.Background())
	}
	if n := len(calls()); n != 1 {
		t.Fatalf("board swept %d times across 5 cycles, want 1 — the %s pace gate is not holding", n, epicRollupReconcileInterval)
	}

	// Age the gate past its interval: the next cycle sweeps again.
	as.mu.Lock()
	as.lastEpicRollupSweepAt = time.Now().Add(-epicRollupReconcileInterval - time.Minute)
	as.mu.Unlock()

	as.runCycle(context.Background())
	if n := len(calls()); n != 2 {
		t.Fatalf("board swept %d times, want 2 — the pace gate never reopens, so rollup would converge exactly once per process", n)
	}
}

// TestEpicRollupBackstop_SharedBoardSweptOnce. Several repos routinely share
// one board (the N:1 workspace topology) and ReconcileBoard sweeps a BOARD,
// not a repo — paying for the same pagination once per member repo is pure
// waste against the same GraphQL budget the pipeline preflight depends on.
func TestEpicRollupBackstop_SharedBoardSweptOnce(t *testing.T) {
	as := newRollupScheduler(t,
		depgraph.RepoConfig{Owner: "acme", Name: "web", Project: 7},
		depgraph.RepoConfig{Owner: "acme", Name: "platform", Project: 7},
		depgraph.RepoConfig{Owner: "acme", Name: "mobile", Project: 9},
	)
	calls := recordingBoardReconciler(t, as, nil)

	as.runCycle(context.Background())

	got := calls()
	if len(got) != 2 {
		t.Fatalf("swept %d boards, want 2 (7 and 9, deduped) — calls = %+v", len(got), got)
	}
	seen := map[int]int{}
	for _, c := range got {
		seen[c.project]++
	}
	if seen[7] != 1 || seen[9] != 1 {
		t.Fatalf("board sweep counts = %v, want each board exactly once", seen)
	}
}

// TestEpicRollupBackstop_SkipsRepoWithNoResolvedProject. Guessing a board
// number here is not a cosmetic error: ReconcileBoard CLOSES ISSUES on the
// board it is handed. A repo whose project did not resolve is skipped loudly.
func TestEpicRollupBackstop_SkipsRepoWithNoResolvedProject(t *testing.T) {
	as := newRollupScheduler(t,
		depgraph.RepoConfig{Owner: "acme", Name: "unresolved", Project: 0},
		depgraph.RepoConfig{Owner: "acme", Name: "web", Project: 7},
	)
	calls := recordingBoardReconciler(t, as, nil)

	out := captureLog(t, func() { as.runCycle(context.Background()) })

	got := calls()
	if len(got) != 1 || got[0].project != 7 {
		t.Fatalf("unresolved repo was swept against a guessed board; calls = %+v", got)
	}
	if !strings.Contains(out, "skipping acme/unresolved") {
		t.Fatalf("skip was silent; log =\n%s", out)
	}
}

// TestEpicRollupBackstop_FailureIsNonFatal. A backstop that can wedge the
// autonomous loop is worse than no backstop: the cycle must complete, later
// boards must still be swept, and the next interval must retry.
func TestEpicRollupBackstop_FailureIsNonFatal(t *testing.T) {
	as := newRollupScheduler(t,
		depgraph.RepoConfig{Owner: "acme", Name: "web", Project: 7},
		depgraph.RepoConfig{Owner: "acme", Name: "mobile", Project: 9},
	)
	calls := recordingBoardReconciler(t, as, errors.New("boom: GraphQL 502"))

	out := captureLog(t, func() { as.runCycle(context.Background()) })

	if n := len(calls()); n != 2 {
		t.Fatalf("a failing board aborted the sweep; calls = %d (want 2 — board 9 must still be attempted)", n)
	}
	if !strings.Contains(out, "boom: GraphQL 502") {
		t.Fatalf("board failure was swallowed silently; log =\n%s", out)
	}
	// The cycle itself completed rather than unwinding.
	as.mu.Lock()
	cycles := as.state.CyclesRun
	scanned := as.state.LastScanAt
	as.mu.Unlock()
	if cycles != 1 || scanned == "" {
		t.Fatalf("cycle did not complete after a backstop failure (CyclesRun=%d LastScanAt=%q)", cycles, scanned)
	}
}

// TestEpicRollupBackstop_NoReconcilerIsANoOp. NewAutonomousScheduler is called
// with a nil forge client in several entry points and in most tests; the sweep
// must not panic there.
func TestEpicRollupBackstop_NoReconcilerIsANoOp(t *testing.T) {
	as := newRollupScheduler(t, depgraph.RepoConfig{Owner: "acme", Name: "web", Project: 7})
	stubReconcileGh(t, func(_ context.Context, _ ...string) ([]byte, error) { return []byte("[]"), nil })
	if as.reconcileBoardFn != nil {
		t.Fatal("nil gh client should leave reconcileBoardFn unwired")
	}
	as.runCycle(context.Background()) // must not panic
}

// TestEpicRollupBackstop_IntervalIsSlowerThanTheCycle guards the pacing
// decision against a future edit that quietly drops it to the cycle cadence.
func TestEpicRollupBackstop_IntervalIsSlowerThanTheCycle(t *testing.T) {
	cfg := DefaultAutonomousConfig()
	if epicRollupReconcileInterval <= cfg.GraphCacheTTL {
		t.Fatalf("epicRollupReconcileInterval=%s must be slower than the graph TTL (%s): a board-wide sweep is far heavier than the reconcilers gated on that TTL",
			epicRollupReconcileInterval, cfg.GraphCacheTTL)
	}
	if epicRollupReconcileInterval <= cfg.MaxScanInterval {
		t.Fatalf("epicRollupReconcileInterval=%s must be slower than the maximum scan interval (%s)",
			epicRollupReconcileInterval, cfg.MaxScanInterval)
	}
}
