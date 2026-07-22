package orchestrator

import (
	"context"
	"testing"

	"github.com/nightgauge/nightgauge/internal/depgraph"
)

// TestPrioritize_SkipsOpenPRBlocked: an issue whose OPEN PR is BLOCKED (a
// failing required check / branch-protection rule) must NOT be re-dispatched.
// Re-running the whole pipeline can't clear a repo-config block — only a human
// can. This guard ends the churn where a failed pr-merge reverts the issue to
// Ready and the ENTIRE pipeline re-runs against a PR that still can't merge
// (the bowlsheet #234/#244/#254/#245 pattern).
func TestPrioritize_SkipsOpenPRBlocked(t *testing.T) {
	nodes := []*depgraph.Node{
		{Repo: "R", Number: 1, Title: "Blocked PR", State: "OPEN", BoardStatus: "Ready", Priority: "P0", Size: "XS", Weight: 1},
		{Repo: "R", Number: 2, Title: "No PR", State: "OPEN", BoardStatus: "Ready", Priority: "P2", Size: "M", Weight: 3},
	}
	g := buildTestGraph(nodes, nil)

	as := &AutonomousScheduler{
		config:               AutonomousConfig{MaxConcurrent: 5},
		state:                &AutonomousState{},
		blockedReadyPRIssues: map[string]bool{"R#1": true},
	}

	candidates := as.prioritize(context.Background(), g)
	if len(candidates) != 1 {
		t.Fatalf("expected 1 candidate (blocked-PR issue skipped), got %d", len(candidates))
	}
	if candidates[0].Number != 2 {
		t.Errorf("expected #2 (no blocked PR), got #%d", candidates[0].Number)
	}
}

// TestPrioritize_NilBlockedSetDispatchesNormally: a nil blockedReadyPRIssues set
// (never refreshed yet, or all queries failed) reads as all-false — the guard is
// fail-open and never suppresses dispatch when we have no PR knowledge.
func TestPrioritize_NilBlockedSetDispatchesNormally(t *testing.T) {
	nodes := []*depgraph.Node{
		{Repo: "R", Number: 1, Title: "A", State: "OPEN", BoardStatus: "Ready", Priority: "P0", Size: "XS", Weight: 1},
	}
	g := buildTestGraph(nodes, nil)

	as := &AutonomousScheduler{
		config: AutonomousConfig{MaxConcurrent: 5},
		state:  &AutonomousState{},
		// blockedReadyPRIssues intentionally nil
	}

	if got := len(as.prioritize(context.Background(), g)); got != 1 {
		t.Fatalf("nil set must not block dispatch; got %d candidates, want 1", got)
	}
}

// TestRefreshBlockedReadyPRs_MarksOnlyBlocked: the sweep records exactly the
// dispatchable, open, non-epic issues whose OPEN PR is BLOCKED. Mergeable/
// behind/dirty PRs, non-dispatchable statuses, epics, and issues with no open
// PR are all left dispatchable (unmarked). One gh pr list per repo.
func TestRefreshBlockedReadyPRs_MarksOnlyBlocked(t *testing.T) {
	calls := 0
	stubReconcileGh(t, func(_ context.Context, args ...string) ([]byte, error) {
		if !ghArgsContain(args, "list") {
			return []byte("[]"), nil
		}
		calls++
		return []byte(`[
			{"number":10,"headRefName":"feat/1-blocked","mergeStateStatus":"BLOCKED"},
			{"number":11,"headRefName":"feat/2-clean","mergeStateStatus":"CLEAN"},
			{"number":12,"headRefName":"feat/3-dirty","mergeStateStatus":"DIRTY"},
			{"number":15,"headRefName":"feat/5-blocked-but-in-review","mergeStateStatus":"BLOCKED"},
			{"number":16,"headRefName":"feat/6-blocked-epic","mergeStateStatus":"BLOCKED"}
		]`), nil
	})

	as := &AutonomousScheduler{config: AutonomousConfig{}, state: &AutonomousState{}}
	g := &depgraph.Graph{Nodes: map[string]*depgraph.Node{
		"O/app#1": {Repo: "O/app", Number: 1, State: "OPEN", BoardStatus: "Ready"},                                     // BLOCKED PR → marked
		"O/app#2": {Repo: "O/app", Number: 2, State: "OPEN", BoardStatus: "Ready"},                                     // CLEAN PR → not marked
		"O/app#3": {Repo: "O/app", Number: 3, State: "OPEN", BoardStatus: "Ready"},                                     // DIRTY PR → not marked (in-review reconcile's job)
		"O/app#4": {Repo: "O/app", Number: 4, State: "OPEN", BoardStatus: "Ready"},                                     // no open PR → not marked
		"O/app#5": {Repo: "O/app", Number: 5, State: "OPEN", BoardStatus: "In review"},                                 // BLOCKED PR but NOT dispatchable → not marked
		"O/app#6": {Repo: "O/app", Number: 6, State: "OPEN", BoardStatus: "Ready", Labels: []string{"type:epic"}},      // epic → not marked
	}}

	as.refreshBlockedReadyPRs(context.Background(), g)

	if calls != 1 {
		t.Fatalf("expected exactly 1 gh pr list for the repo, got %d", calls)
	}
	if !as.blockedReadyPRIssues["O/app#1"] {
		t.Error("expected O/app#1 (dispatchable, BLOCKED PR) to be marked")
	}
	for _, k := range []string{"O/app#2", "O/app#3", "O/app#4", "O/app#5", "O/app#6"} {
		if as.blockedReadyPRIssues[k] {
			t.Errorf("expected %s NOT marked, but it was", k)
		}
	}
}

// TestRefreshBlockedReadyPRs_GhErrorFailsOpen: when the gh query fails the sweep
// records nothing for that repo (fail-open) rather than marking issues blocked —
// dispatch is never suppressed on a transient GitHub error.
func TestRefreshBlockedReadyPRs_GhErrorFailsOpen(t *testing.T) {
	stubReconcileGh(t, func(_ context.Context, _ ...string) ([]byte, error) {
		return nil, context.DeadlineExceeded
	})

	as := &AutonomousScheduler{config: AutonomousConfig{}, state: &AutonomousState{}}
	g := &depgraph.Graph{Nodes: map[string]*depgraph.Node{
		"O/app#1": {Repo: "O/app", Number: 1, State: "OPEN", BoardStatus: "Ready"},
	}}

	as.refreshBlockedReadyPRs(context.Background(), g)

	if len(as.blockedReadyPRIssues) != 0 {
		t.Fatalf("gh error must leave the blocked set empty (fail-open); got %v", as.blockedReadyPRIssues)
	}
}
