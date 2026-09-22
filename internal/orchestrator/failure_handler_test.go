package orchestrator

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/nightgauge/nightgauge/internal/depgraph"
)

// The parked kinds (#1631): a context window the prompt outgrew, a permission
// map that rejects a tool the stage is allowed, and an adapter binary that
// cannot serve the dispatch. Each is met unchanged by the next attempt on the
// same model and adapter, and none is the issue's fault.

var parkedKinds = []string{
	TerminalKindContextWindowExceeded,
	TerminalKindAdapterPermissionRejected,
	TerminalKindAdapterIncompatible,
}

// TestParkedKinds_NoRetryNoLifetimeIncrementNoCascade drives each parked kind
// through the autonomous scheduler's real completion path, three issues per
// kind — enough to trip a threshold-3 cascade breaker if any of them fed it.
// No retry may be scheduled (the next dispatch would meet the same window,
// map or binary), nothing may be charged to the issue, and the rescan must
// neither re-admit the entry nor offer it as a candidate. Routing any of these
// kinds through the transient-retry branch that api_overloaded takes fails
// every one of those assertions.
func TestParkedKinds_NoRetryNoLifetimeIncrementNoCascade(t *testing.T) {
	for _, kind := range parkedKinds {
		t.Run(kind, func(t *testing.T) {
			// A threshold-3 breaker, so three failures of a kind that fed it
			// would trip it.
			stubReconcileGhUnreachable(t)
			as := newAutonomousForCascadeTest(t, 3, 30*time.Minute)
			as.workspaceRoot = t.TempDir()
			as.state.LifetimeIssueFailures = map[string]int{}
			as.perIssueFailureCount = map[string]int{}
			as.retryBackoff = map[string]retryPlan{}

			const repo = "acme/app"
			numbers := []int{1631, 1632, 1633}
			for _, n := range numbers {
				addRunning(as, repo, n, "a stage on a local model")
				as.onPipelineComplete(repo, n, false, false, kind, "exit 1: the adapter's own failure text", false)
				as.drainBackground()
			}

			if as.state.Status == "safety_tripped" || as.state.Status == "paused" {
				t.Fatalf("status = %q after three %s failures; a configuration fault must not pause the fleet", as.state.Status, kind)
			}
			if as.cascadeTracker.IsTripped() {
				t.Errorf("the cascade breaker tripped on %s; the kind must not feed it", kind)
			}
			g := buildTestGraph([]*depgraph.Node{
				{Repo: repo, Number: 1631, State: "OPEN", BoardStatus: "Ready"},
				{Repo: repo, Number: 1632, State: "OPEN", BoardStatus: "Ready"},
				{Repo: repo, Number: 1633, State: "OPEN", BoardStatus: "Ready"},
			}, nil)
			as.reconcileStateAgainstGraph(g)
			candidates := as.prioritize(context.Background(), g)

			for _, n := range numbers {
				key := fmt.Sprintf("%s#%d", repo, n)
				if _, ok := retryDeadline(as, key); ok {
					t.Errorf("%s: a retry was scheduled for %s — the next attempt meets the same limit", key, kind)
				}
				if got := as.state.LifetimeIssueFailures[key]; got != 0 {
					t.Errorf("%s: LifetimeIssueFailures = %d, want 0 — %s is not the issue's fault", key, got, kind)
				}
				if got := as.perIssueFailureCount[key]; got != 0 {
					t.Errorf("%s: perIssueFailureCount = %d, want 0", key, got)
				}
				if got := as.humanHoldFor(repo, n); got != HoldOperatorResume {
					t.Errorf("%s: hold = %q, want %q — the rescan re-admits an unheld entry, which is a retry", key, got, HoldOperatorResume)
				}
				if isCandidate(candidates, repo, n) {
					t.Errorf("%s: the parked issue is a dispatch candidate after a rescan", key)
				}
			}
			if len(as.state.Failed) != len(numbers) {
				t.Fatalf("state.Failed has %d entries, want %d: the park is recorded, not dropped", len(as.state.Failed), len(numbers))
			}
			for _, f := range as.state.Failed {
				if f.Kind != kind {
					t.Errorf("failed entry kind = %q, want %q", f.Kind, kind)
				}
				if !strings.Contains(f.Reason, TerminalKindRemediation(kind)) {
					t.Errorf("failed entry reason %q does not name the remediation for %s", f.Reason, kind)
				}
			}
		})
	}
}

// TestParkedKinds_ReleasedByTheCommandTheReasonNames parks each kind through
// the real completion path, which leaves the fleet running, and then does
// what the failed entry's reason tells the operator to do. The park never
// pauses the fleet, so `autonomous resume` (which acts only on a pause) would
// leave the entry held; the reason must name the act that releases it from a
// running fleet, clearing the issue's failures, and that act must make the
// issue a candidate again.
func TestParkedKinds_ReleasedByTheCommandTheReasonNames(t *testing.T) {
	for _, kind := range parkedKinds {
		t.Run(kind, func(t *testing.T) {
			stubReconcileGhUnreachable(t)
			as := newAutonomousForCascadeTest(t, 3, 30*time.Minute)
			as.workspaceRoot = t.TempDir()
			as.state.LifetimeIssueFailures = map[string]int{}
			as.perIssueFailureCount = map[string]int{}
			as.retryBackoff = map[string]retryPlan{}

			const repo, n = "acme/app", 7
			key := fmt.Sprintf("%s#%d", repo, n)
			addRunning(as, repo, n, "a stage on a local model")
			as.onPipelineComplete(repo, n, false, false, kind, "exit 1: the adapter's own failure text", false)
			as.drainBackground()

			if as.state.Status != "running" {
				t.Fatalf("status = %q after a %s park, want running — the park does not pause", as.state.Status, kind)
			}
			if len(as.state.Failed) != 1 {
				t.Fatalf("state.Failed has %d entries, want the one park", len(as.state.Failed))
			}
			const release = "nightgauge autonomous clear-failures"
			if !strings.Contains(as.state.Failed[0].Reason, release) {
				t.Fatalf("the %s reason does not name `%s`, the act that releases a park from a running fleet:\n%s",
					kind, release, as.state.Failed[0].Reason)
			}

			if cleared, _ := as.ClearIssueFailures(key); cleared != 1 {
				t.Errorf("ClearIssueFailures(%s) cleared %d issues, want 1", key, cleared)
			}
			if len(as.state.Failed) != 0 {
				t.Fatalf("clearing %s's failures left the %s park in place", key, kind)
			}
			g := holdTestGraph(&depgraph.Node{Repo: repo, Number: n, State: "OPEN", BoardStatus: "Ready"})
			if !isCandidate(as.prioritize(context.Background(), g), repo, n) {
				t.Errorf("the released issue is not a dispatch candidate")
			}
		})
	}
}

// TestTerminalKindParks_OnlyTheThreeKinds pins the set, so a kind joining it
// by accident (and losing its retries) is a visible change here.
func TestTerminalKindParks_OnlyTheThreeKinds(t *testing.T) {
	for _, kind := range parkedKinds {
		if !TerminalKindParks(kind) {
			t.Errorf("TerminalKindParks(%q) = false, want true", kind)
		}
		if TerminalKindRemediation(kind) == "" {
			t.Errorf("TerminalKindRemediation(%q) is empty; a parked issue must say what to change", kind)
		}
	}
	for _, kind := range []string{
		"", TerminalKindPermissionDenied, TerminalKindModelUnavailable, TerminalKindNetworkUnavailable,
		TerminalKindAdapterAuthFailed, TerminalKindSubagentCrash, TerminalKindNotPipelineActionable,
	} {
		if TerminalKindParks(kind) {
			t.Errorf("TerminalKindParks(%q) = true; that kind keeps its own recovery", kind)
		}
		if TerminalKindRemediation(kind) != "" {
			t.Errorf("TerminalKindRemediation(%q) = %q, want empty", kind, TerminalKindRemediation(kind))
		}
	}
}
