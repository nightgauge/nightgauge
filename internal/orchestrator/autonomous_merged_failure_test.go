package orchestrator

import (
	"fmt"
	"strings"
	"testing"
	"time"
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
