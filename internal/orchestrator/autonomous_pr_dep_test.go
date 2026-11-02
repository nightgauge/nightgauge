package orchestrator

import (
	"context"
	"strings"
	"testing"

	"github.com/nightgauge/nightgauge/internal/depgraph"
	"github.com/nightgauge/nightgauge/internal/forge"
)

// A dependency key may name a pull request or nothing at all (#2854). Each case
// runs through prioritize, with the resolver reading the mock, and evaluateDeps
// supplies the reason the log prints.
func TestPrioritize_DependencyOnPullRequestOrMissingNumber(t *testing.T) {
	const repo, dep = "owner/repo", "owner/repo#7"
	for name, tc := range map[string]struct {
		state      string
		dispatches bool
		reason     string
	}{
		"merged":          {state: forge.DepStatePRMerged, dispatches: true},
		"open":            {state: forge.DepStatePROpen, reason: "pull request still open"},
		"closed unmerged": {state: forge.DepStatePRClosed, reason: "closed without merging"},
		"missing":         {state: forge.DepStateMissing, reason: "does not exist"},
	} {
		t.Run(name, func(t *testing.T) {
			g := buildTestGraph(
				[]*depgraph.Node{{Repo: repo, Number: 100, Title: "dependent", State: "OPEN", BoardStatus: "Ready", Priority: "P2", Size: "S", Weight: 1}},
				[]depgraph.Edge{{From: depgraph.NodeID{Repo: repo, Number: 100}, To: depgraph.NodeID{Repo: repo, Number: 7}, Type: "crossRepo"}},
			)
			mock := newMockIssueSvc()
			mock.depStates = map[string]string{dep: tc.state}
			validationCalls := 0
			as := &AutonomousScheduler{
				trustGateOverride: allWritersGate(),
				config:            AutonomousConfig{MaxConcurrent: 5},
				repos:             []depgraph.RepoConfig{{Owner: "owner", Name: "repo", Project: 1}},
				state:             &AutonomousState{},
			}
			as.resolveDepStatesFn = func(ctx context.Context, keys []string) map[string]string {
				return ResolveIssueStatesByKey(ctx, mock, keys)
			}
			as.resolveValidationFn = func(context.Context, map[string][]string, []string) map[string]ValidationState {
				validationCalls++
				return nil
			}

			dispatched := false
			for _, c := range as.prioritize(context.Background(), g) {
				dispatched = dispatched || c.Number == 100
			}
			if dispatched != tc.dispatches {
				t.Fatalf("dispatched = %v, want %v (rejections %+v)", dispatched, tc.dispatches, as.state.LastRejectionReasons)
			}

			res := evaluateDeps([]string{dep}, g, map[string]string{dep: tc.state}, nil)
			if tc.dispatches {
				if validationCalls != 0 {
					t.Fatalf("a merged pull request reached the validation gate %d time(s)", validationCalls)
				}
				if res.blocked {
					t.Fatalf("evaluateDeps = %+v, want satisfied with no validation standing", res)
				}
				return
			}
			if got := as.state.LastRejectionReasons["blocked-by-offboard-dep"]; got != 1 {
				t.Errorf("rejection reasons = %+v, want blocked-by-offboard-dep=1", as.state.LastRejectionReasons)
			}
			if !res.blocked || !res.offBoard || !strings.Contains(res.status, tc.reason) {
				t.Errorf("evaluateDeps = %+v, want off-board block saying %q", res, tc.reason)
			}
		})
	}
}

func TestCountConfirmedDeps_ExcludesMissing(t *testing.T) {
	got := countConfirmedDeps(map[string]string{"a": forge.DepStateMissing, "b": forge.DepStatePRMerged, "c": "OPEN"})
	if got != 2 {
		t.Fatalf("count = %d, want 2", got)
	}
}
