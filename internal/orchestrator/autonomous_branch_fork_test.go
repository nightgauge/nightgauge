package orchestrator

import (
	"fmt"
	"testing"
	"time"
)

// Tests for the #163 branch_forked terminal kind on the autonomous side.
//
// A forked branch is the one failure class where retrying is strictly harmful:
// the next run rebuilds the same local history against the same unchanged
// remote and is rejected identically, after regenerating a full implementation
// to get there. Pre-fix it classified as subagent_crash, so every cycle spent a
// pipeline's tokens, incremented LifetimeIssueFailures, and fed the cascade
// breaker — burning the issue's whole lifetime budget on a condition the
// pipeline itself created.

// TestOnPipelineComplete_BranchForked_NoRetryNoLifetimeIncrementNoCascade
// verifies the kind is excluded from every automatic-retry mechanism: no
// lifetime-cap increment, no per-issue failure count, no backoff entry (there
// is nothing to back off INTO), and no cascade-breaker feed even when several
// arrive inside the window.
func TestOnPipelineComplete_BranchForked_NoRetryNoLifetimeIncrementNoCascade(t *testing.T) {
	as := newAutonomousForCascadeTest(t, 3, 30*time.Minute)
	as.state.LifetimeIssueFailures = map[string]int{}
	as.perIssueFailureCount = map[string]int{}
	as.retryBackoff = map[string]retryPlan{}

	cases := []struct {
		repo string
		num  int
	}{
		{"nightgauge/nightgauge", 163},
		{"nightgauge/nightgauge", 171},
		{"acme/dashboard", 96},
		{"acme/infra", 162},
	}
	for _, c := range cases {
		addRunning(as, c.repo, c.num, "forked branch")
		as.onPipelineComplete(c.repo, c.num, false, false,
			TerminalKindBranchForked,
			"[branch-forked] origin/fix/163-x is at abc12345, which is NOT an ancestor of the local tip def67890")
		as.drainBackground()
	}

	if as.state.Status == "safety_tripped" || as.state.Status == "paused" {
		t.Fatalf("scheduler tripped/paused on branch-forked; want still running — a forked branch says nothing about the factory")
	}
	if as.cascadeTracker.IsTripped() {
		t.Errorf("cascadeTracker tripped on branch-forked; want excluded from cascade")
	}

	for _, c := range cases {
		key := fmt.Sprintf("%s#%d", c.repo, c.num)
		if got := as.state.LifetimeIssueFailures[key]; got != 0 {
			t.Errorf("LifetimeIssueFailures[%q] = %d, want 0 — a fork must not consume the issue's lifetime budget", key, got)
		}
		if got := as.perIssueFailureCount[key]; got != 0 {
			t.Errorf("perIssueFailureCount[%q] = %d, want 0", key, got)
		}
		if _, ok := retryDeadline(as, key); ok {
			t.Errorf("retryBackoff[%q] was set — no retry can clear a fork, so scheduling one is the defect", key)
		}
	}
	if len(as.state.Failed) != len(cases) {
		t.Fatalf("expected %d failed entries (the condition is still recorded), got %d", len(cases), len(as.state.Failed))
	}
}

// TestNotifyComplete_EmptyKindBranchForkedDetail_Reclassifies is the
// defense-in-depth contract: an IPC caller that passes terminalFailureKind=""
// but whose failureDetail carries the push rejection must still route to the
// no-retry branch via the Go-side ClassifyTerminalKind fallback. The observed
// #163 evidence was exactly this shape — `echo "PUSH REJECTED:
// non-fast-forward."` in the bash ring, with no structured kind attached.
func TestNotifyComplete_EmptyKindBranchForkedDetail_Reclassifies(t *testing.T) {
	as := newAutonomousForCascadeTest(t, 3, 30*time.Minute)
	as.state.LifetimeIssueFailures = map[string]int{}
	as.perIssueFailureCount = map[string]int{}
	as.retryBackoff = map[string]retryPlan{}

	addRunning(as, "nightgauge/nightgauge", 163, "push rejected")
	as.NotifyComplete("nightgauge/nightgauge", 163, false, false,
		"", // no structured kind — only the raw text
		"feature-validate exit 1: PUSH REJECTED: non-fast-forward.")
	as.drainBackground()

	key := "nightgauge/nightgauge#163"
	if got := as.state.LifetimeIssueFailures[key]; got != 0 {
		t.Errorf("LifetimeIssueFailures[%q] = %d, want 0 — the raw rejection must reclassify to branch_forked", key, got)
	}
	if _, ok := retryDeadline(as, key); ok {
		t.Errorf("retryBackoff[%q] was set — the reclassified fork must not be queued for retry", key)
	}
}
