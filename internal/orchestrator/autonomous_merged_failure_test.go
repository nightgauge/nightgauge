package orchestrator

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/nightgauge/nightgauge/internal/depgraph"
	"github.com/nightgauge/nightgauge/internal/hooks"
	"github.com/nightgauge/nightgauge/internal/state"
)

// Tests for the #1969 Medium-1 follow-up: a later stage failing after this
// run's own PR was confirmed MERGED must not feed the autonomous scheduler's
// failure accounting, cascade breaker, or board revert.
//
// scheduler.go's runPipeline terminal defer already skips its OWN board
// write on this signal (shouldSkipBoardRevert, gated on
// RuntimeState.MergedCommitSha != ""), but that defer returns before
// as.onPipelineComplete is ever called — the two are sequential, not nested.
// Without threading the same signal through as a `merged` parameter, this
// wrapper independently re-applies perIssueFailureCount++,
// LifetimeIssueFailures++, cascadeTracker.RecordFailure, and its own
// goTrackedBoardOp(revertFailedIssueStatus) call in the GENERIC branch —
// exactly #1650's repro: PR #1966 merged, spike-materialize failed
// downstream (unregistered in StageSkillDirs, Ask 1 of the same issue), and
// Status still reverted to Ready.

// TestOnPipelineComplete_MergedFailure_NoRevertNoLifetimeIncrementNoCascade
// drives the merged=true path directly and asserts every automatic-retry
// mechanism is skipped, while the failure is still visible for human triage
// (an Action Center card via as.state.Failed).
func TestOnPipelineComplete_MergedFailure_NoRevertNoLifetimeIncrementNoCascade(t *testing.T) {
	as := newAutonomousForCascadeTest(t, 3, 30*time.Minute)
	as.state.LifetimeIssueFailures = map[string]int{}
	as.perIssueFailureCount = map[string]int{}
	as.retryBackoff = map[string]retryPlan{}

	const repo = "nightgauge/nightgauge"
	const num = 1650
	addRunning(as, repo, num, "spike merged then failed at spike-materialize")

	buf := withCapturedLog(t)
	as.onPipelineComplete(repo, num, false, false, "validation_error",
		`skill render failed: stage spike-materialize could not compose its SKILL.md (model="sonnet", adapter="claude"): no skill directory for stage "spike-materialize"`,
		true)
	as.drainBackground()

	// No board revert: revertFailedIssueStatus (reached only via
	// goTrackedBoardOp in the GENERIC branch) would have logged
	// "revert-status: no project config for ... — skipping #1650" in this
	// harness (as.repos is empty, so it always hits that early return rather
	// than a real network call) — its ABSENCE from the log proves the
	// GENERIC branch's goTrackedBoardOp call never ran, not merely that the
	// network call it would have made was itself a no-op.
	if logs := buf.String(); strings.Contains(logs, "revert-status") {
		t.Errorf("board status was reverted for a merged run's later-stage failure — log:\n%s", logs)
	}

	key := fmt.Sprintf("%s#%d", repo, num)
	if got := as.state.LifetimeIssueFailures[key]; got != 0 {
		t.Errorf("LifetimeIssueFailures[%q] = %d, want 0 — a merged run's later-stage failure must not consume the issue's lifetime budget", key, got)
	}
	if got := as.perIssueFailureCount[key]; got != 0 {
		t.Errorf("perIssueFailureCount[%q] = %d, want 0", key, got)
	}
	if as.cascadeTracker.IsTripped() {
		t.Errorf("cascadeTracker tripped on a merged run's later-stage failure; want excluded from cascade — a merged run says nothing about the health of the factory")
	}

	// Still visible for human triage: the Action Center is the way back in,
	// not an automatic retry.
	found := false
	for _, f := range as.state.Failed {
		if f.Repo == repo && f.Number == num {
			found = true
		}
	}
	if !found {
		t.Errorf("no Failed entry recorded for %s#%d — the merged-but-failed run must still be visible for human triage", repo, num)
	}
}

// TestOnPipelineComplete_MergedFailure_FalsePositiveGuard proves the
// assertions above are not vacuous: the SAME failure, on the SAME harness,
// with merged=false, DOES hit the GENERIC branch's revert attempt (logged),
// and DOES increment the counters. If this test failed, the merged=true
// test above would prove nothing — the harness might simply never reach the
// GENERIC branch at all.
func TestOnPipelineComplete_MergedFailure_FalsePositiveGuard(t *testing.T) {
	as := newAutonomousForCascadeTest(t, 3, 30*time.Minute)
	as.state.LifetimeIssueFailures = map[string]int{}
	as.perIssueFailureCount = map[string]int{}
	as.retryBackoff = map[string]retryPlan{}

	const repo = "nightgauge/nightgauge"
	const num = 1651
	addRunning(as, repo, num, "an ordinary failure, no merge")

	buf := withCapturedLog(t)
	as.onPipelineComplete(repo, num, false, false, "validation_error", "some other stage error", false)
	as.drainBackground()

	if logs := buf.String(); !strings.Contains(logs, "revert-status") {
		t.Fatalf("fixture bug: an ordinary failure never reached the GENERIC branch's revert attempt — log:\n%s", logs)
	}

	key := fmt.Sprintf("%s#%d", repo, num)
	if got := as.perIssueFailureCount[key]; got != 1 {
		t.Fatalf("fixture bug: perIssueFailureCount[%q] = %d, want 1 for an ordinary failure", key, got)
	}
}

// TestTransientKill_PRMergedWhileOffline_PostMergeRunsOnceOnReconnect is the
// #2607 regression. The connection is lost during a run after pr-create; the
// merge queue merges its PR while the forge is unreachable; the merge closes
// the issue, so it leaves the board graph. When the forge answers again the
// post-merge path runs exactly once, through Scheduler.runPostMerge, and the
// validation marker is written. Nothing asks a human to move the issue.
func TestTransientKill_PRMergedWhileOffline_PostMergeRunsOnceOnReconnect(t *testing.T) {
	buf := withCapturedLog(t)
	// No open-PR pages: every list read fails, as it does offline.
	as, _, _ := revertFixture(t)
	online := false
	as.prStateFn = func(_ context.Context, owner, repo string, number int) (string, error) {
		if !online {
			return "", errors.New("dial tcp api.github.com:443: connect: network is unreachable")
		}
		if owner != "O" || repo != "a" || number != 42 {
			t.Errorf("PR state read for %s/%s#%d, want O/a#42", owner, repo, number)
		}
		return "MERGED", nil
	}
	var inputs []hooks.PostMergeInput
	as.scheduler = &Scheduler{evaluatePostMergeFn: func(_ context.Context, _ hooks.IssueFetcher, _ hooks.IssueCloser,
		_ hooks.EpicAutoCloser, _ hooks.PRVerifier, _ hooks.BoardSyncer, in hooks.PostMergeInput) hooks.PostMergeResult {
		inputs = append(inputs, in)
		return hooks.PostMergeResult{IssueClosed: true, ValidationMarker: hooks.ValidationMarkerWritten}
	}}

	// 1. The run dies on a lost connection after pr-create opened PR #42.
	rt := state.NewRuntimeState("O/a", 1, "item-1", testRunID())
	rt.SetPrUrl("https://github.com/O/a/pull/42")
	addRunning(as, "O/a", 1, "the issue")
	as.onPipelineComplete("O/a", 1, false, false, TerminalKindApiConnectionLost, "API connection lost", false)
	as.recordTransientRunPR("O/a", 1, rt, TerminalKindApiConnectionLost)
	as.waitBackground()

	if e, ok := pendingRecheck(as, "O/a", 1); !ok || e.PRNumber != 42 {
		t.Fatalf("pending recheck = %+v (found %v), want O/a#1 carrying PR #42", e, ok)
	}

	// 2. Still offline: the sweep keeps the entry and runs nothing.
	as.reconcilePRRechecks(context.Background())
	if len(inputs) != 0 {
		t.Fatalf("post-merge ran %d times while the forge was unreachable", len(inputs))
	}

	// 3. The merge closed the issue; the board graph no longer has it.
	as.reconcileStateAgainstGraph(depgraph.NewGraph())
	if len(as.state.Failed) != 1 {
		t.Fatalf("Failed = %+v, want the entry kept while its recheck is pending", as.state.Failed)
	}

	// 4. Connectivity returns.
	online = true
	as.reconcilePRRechecks(context.Background())
	as.reconcilePRRechecks(context.Background())

	if len(inputs) != 1 {
		t.Fatalf("post-merge ran %d times, want exactly 1", len(inputs))
	}
	if in := inputs[0]; in.IssueNumber != 1 || in.PRNumber != 42 || in.RepositoryOwner != "O" || in.RepositoryName != "a" {
		t.Errorf("post-merge input = %+v, want O/a issue 1, PR 42", in)
	}
	logs := buf.String()
	if !strings.Contains(logs, `validation marker "written"`) {
		t.Errorf("the recovered post-merge did not report the marker written; log:\n%s", logs)
	}
	if strings.Contains(logs, "by hand") {
		t.Errorf("a connectivity-only blocker left an instruction for a human; log:\n%s", logs)
	}
	if len(as.state.Failed) != 0 || len(as.state.PendingPRRechecks) != 0 {
		t.Errorf("Failed = %+v, pending = %+v; want both empty", as.state.Failed, as.state.PendingPRRechecks)
	}
	if len(as.state.Completed) != 1 || as.state.Completed[0].Number != 1 {
		t.Errorf("Completed = %+v, want O/a#1", as.state.Completed)
	}
}
