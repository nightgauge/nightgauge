package orchestrator

import (
	"fmt"
	"testing"
	"time"
)

// Tests for the #307 user_abort terminal kind.
//
// abortAll bounds its wait at 30s. When a slot's pipeline promise is still
// unsettled at that deadline — a wedged adapter, a stage awaiting an IPC/`gh`
// call that ignores the AbortController — the extension force-clears the slot
// and books its terminal state on the run's behalf. The run's outcome is
// UNKNOWN, not failed: nothing observed a result.
//
// Booking that through the generic failure branch made the operator's own Stop
// the cause of a fleet halt. Every wedged slot on ONE Stop All fed a separate
// failure into the cascade breaker inside a single window, so a Stop over three
// stuck slots tripped it — safety_tripped, the whole fleet paused, a Discord
// embed and a cascade-pause card — while each slot also charged the per-issue
// lifetime cap that survives Resume(), and the board revert to Ready
// re-dispatched issues whose processes might still be holding their worktrees.

// The exact text ConcurrentPipelineManager.forceSettleUnsettledSlot emits. The
// marker is the load-bearing part; the rest is operator-facing prose.
const userAbortDetail = "[user-abort] Cancelled by user — abort deadline exceeded after 30000ms; " +
	"slot force-cleared while its pipeline promise was still unsettled, so this run's outcome is unknown"

// TestClassifyTerminalKind_UserAbort pins the marker → kind mapping. Without a
// matcher the text classifies to "" and NotifyComplete's defense-in-depth
// reclassify cannot rescue it either, which is precisely how the force-clear
// path fell into the generic failure branch.
func TestClassifyTerminalKind_UserAbort(t *testing.T) {
	cases := map[string]string{
		userAbortDetail:                               TerminalKindUserAbort,
		"[user-abort] slot force-cleared":             TerminalKindUserAbort,
		"terminal kind user_abort forwarded over ipc": TerminalKindUserAbort,
	}
	for text, want := range cases {
		if got := ClassifyTerminalKind(text); got != want {
			t.Errorf("ClassifyTerminalKind(%q) = %q, want %q", text, got, want)
		}
	}

	// The abort wording is a minefield of substrings the other heuristics
	// claim: "abort", "cancel", and a millisecond deadline that reads like a
	// timeout. Matching FIRST is what keeps it out of stall_kill / infra.
	if got := ClassifyTerminalKind(userAbortDetail); got == TerminalKindStallKill {
		t.Errorf("the force-clear text classified as stall_kill — the deadline wording must not be read as a stall")
	}
}

// TestOnPipelineComplete_UserAbort_NoCascadeNoLifetimeCapNoRevert is the
// property that matters: one Stop All over several wedged slots must leave the
// fleet running and every issue's lifetime budget untouched.
func TestOnPipelineComplete_UserAbort_NoCascadeNoLifetimeCapNoRevert(t *testing.T) {
	// Threshold 3 — exactly the number of wedged slots below, so a regression
	// that feeds the breaker trips it rather than merely edging toward it.
	as := newAutonomousForCascadeTest(t, 3, 30*time.Minute)
	as.state.LifetimeIssueFailures = map[string]int{}
	as.perIssueFailureCount = map[string]int{}
	as.retryBackoff = map[string]retryPlan{}

	cases := []struct {
		repo string
		num  int
	}{
		{"acme/platform", 900},
		{"acme/platform", 901},
		{"acme/dashboard", 96},
	}
	for _, c := range cases {
		addRunning(as, c.repo, c.num, "wedged when the operator pressed Stop")
		as.onPipelineComplete(c.repo, c.num, false, false, TerminalKindUserAbort, userAbortDetail)
	}

	if as.state.Status == "safety_tripped" || as.state.Status == "paused" {
		t.Fatalf("status = %q after one Stop All over %d wedged slots; want the fleet still running — pressing Stop must never halt the factory",
			as.state.Status, len(cases))
	}
	if as.cascadeTracker.IsTripped() {
		t.Errorf("cascadeTracker tripped on an operator Stop — an abort the run ignored says nothing about the health of the factory")
	}

	for _, c := range cases {
		key := fmt.Sprintf("%s#%d", c.repo, c.num)
		if got := as.state.LifetimeIssueFailures[key]; got != 0 {
			t.Errorf("LifetimeIssueFailures[%q] = %d, want 0 — a deliberate operator stop must not consume the issue's cross-session budget", key, got)
		}
		if got := as.perIssueFailureCount[key]; got != 0 {
			t.Errorf("perIssueFailureCount[%q] = %d, want 0", key, got)
		}
		// No retry is scheduled, and deliberately no board revert: the wedged
		// process may still hold the worktree and the IPC server's runtime
		// entry, so an automatic re-dispatch walks a second run into the first
		// one's state. The operator re-queueing is the way back in.
		if _, ok := retryDeadline(as, key); ok {
			t.Errorf("retryBackoff[%q] was set — re-dispatching an issue whose process may still be alive is the defect", key)
		}
	}

	// The condition is still RECORDED — silently dropping it would leave the
	// operator with no trace of which runs were force-cleared.
	if len(as.state.Failed) != len(cases) {
		t.Fatalf("expected %d recorded entries for visibility, got %d", len(cases), len(as.state.Failed))
	}
}

// TestUserAbort_DoesNotFeedConsecutiveFailureRail covers the SECOND breaker.
// SafetyRails.ConsecutiveFailures is independent of the cascade tracker and the
// lifetime cap: exempting those two and not this one still stops the fleet, by
// another route (the #4222 lesson).
func TestUserAbort_DoesNotFeedConsecutiveFailureRail(t *testing.T) {
	as := newAutonomousForCascadeTest(t, 3, 30*time.Minute)
	as.state.LifetimeIssueFailures = map[string]int{}
	as.perIssueFailureCount = map[string]int{}
	as.retryBackoff = map[string]retryPlan{}
	as.safetyRails = NewSafetyRails(SafetyConfig{CircuitBreakerMax: 3})

	before := as.safetyRails.State().ConsecutiveFailures

	for i, num := range []int{900, 901, 902, 903} {
		addRunning(as, "acme/platform", num, "wedged when the operator pressed Stop")
		as.onPipelineComplete("acme/platform", num, false, false, TerminalKindUserAbort, userAbortDetail)
		if got := as.safetyRails.State().ConsecutiveFailures; got != before {
			t.Fatalf("after %d force-cleared slot(s): ConsecutiveFailures = %d, want %d unchanged — an operator stop is neither a success nor a failure for rail purposes",
				i+1, got, before)
		}
	}

	if as.state.Status == "safety_tripped" || as.state.Status == "paused" {
		t.Errorf("status = %q after 4 force-cleared slots, want the fleet still running", as.state.Status)
	}
}

// TestNotifyComplete_UserAbort_ReclassifiesFromDetail covers the IPC-mode
// defense-in-depth path: if the extension's own regex ever misses, the Go side
// must still recover the kind from the forwarded failure text rather than
// falling through to the generic branch (#3442's lesson).
func TestNotifyComplete_UserAbort_ReclassifiesFromDetail(t *testing.T) {
	as := newAutonomousForCascadeTest(t, 3, 30*time.Minute)
	as.state.LifetimeIssueFailures = map[string]int{}
	as.perIssueFailureCount = map[string]int{}
	as.retryBackoff = map[string]retryPlan{}

	addRunning(as, "acme/platform", 900, "wedged when the operator pressed Stop")
	// terminalFailureKind deliberately empty — only the detail carries it.
	as.NotifyComplete("acme/platform", 900, false, false, "", userAbortDetail)

	key := "acme/platform#900"
	if got := as.state.LifetimeIssueFailures[key]; got != 0 {
		t.Errorf("LifetimeIssueFailures[%q] = %d, want 0 — the reclassify must reach the user_abort branch", key, got)
	}
	if as.cascadeTracker.IsTripped() {
		t.Errorf("cascadeTracker tripped — the reclassify did not reach the user_abort branch")
	}
}
