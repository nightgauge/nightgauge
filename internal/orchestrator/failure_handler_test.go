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
				as.onPipelineComplete(repo, n, false, false, kind, "exit 1: the adapter's own failure text")
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

// TestParkedKinds_ReleasedByExplicitResume — an operator who has changed the
// configuration resumes, and the issue is a candidate again.
func TestParkedKinds_ReleasedByExplicitResume(t *testing.T) {
	for _, kind := range parkedKinds {
		t.Run(kind, func(t *testing.T) {
			as := heldTestScheduler(t)
			as.state.Status = "paused"
			as.state.Failed = []FailedItem{{Repo: "acme/app", Number: 7, FailedAt: "2026-09-14T10:00:00Z", Kind: kind}}

			as.Resume()
			as.drainBackground()

			if len(as.state.Failed) != 0 {
				t.Fatalf("resume left the %s park in place", kind)
			}
			g := holdTestGraph(&depgraph.Node{Repo: "acme/app", Number: 7, State: "OPEN", BoardStatus: "Ready"})
			if !isCandidate(as.prioritize(context.Background(), g), "acme/app", 7) {
				t.Errorf("the resumed issue is not a dispatch candidate")
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
