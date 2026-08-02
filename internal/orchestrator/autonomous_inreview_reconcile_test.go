package orchestrator

import (
	"context"
	"testing"

	"github.com/nightgauge/nightgauge/internal/depgraph"
)

func prListJSON(t *testing.T) func(ctx context.Context, args ...string) ([]byte, error) {
	t.Helper()
	return func(_ context.Context, args ...string) ([]byte, error) {
		if !ghArgsContain(args, "list") {
			return []byte("[]"), nil
		}
		// Two open PRs; only feat/43-… belongs to issue 43 and is DIRTY.
		return []byte(`[
			{"number":64,"headRefName":"feat/43-baker-role-stats","mergeStateStatus":"DIRTY"},
			{"number":70,"headRefName":"feat/99-unrelated","mergeStateStatus":"CLEAN"}
		]`), nil
	}
}

func TestOpenPRMergeStateForIssue_MatchesBranch(t *testing.T) {
	stubReconcileGh(t, prListJSON(t))
	as := &AutonomousScheduler{}
	state, ok := as.openPRMergeStateForIssue(context.Background(), "nightgauge/acmeapp-platform", 43)
	if !ok || state != "DIRTY" {
		t.Fatalf("got (%q,%v), want (DIRTY,true)", state, ok)
	}
}

func TestOpenPRMergeStateForIssue_NoMatch(t *testing.T) {
	stubReconcileGh(t, prListJSON(t))
	as := &AutonomousScheduler{}
	if _, ok := as.openPRMergeStateForIssue(context.Background(), "nightgauge/acmeapp-platform", 12345); ok {
		t.Fatal("expected no match for an issue with no open PR")
	}
}

func TestOpenPRMergeStateForIssue_GhError(t *testing.T) {
	stubReconcileGh(t, func(_ context.Context, _ ...string) ([]byte, error) {
		return nil, context.DeadlineExceeded
	})
	as := &AutonomousScheduler{}
	if _, ok := as.openPRMergeStateForIssue(context.Background(), "nightgauge/acmeapp-platform", 43); ok {
		t.Fatal("expected (_,false) on gh error (fail-closed)")
	}
}

func TestNodeHasEpicLabel(t *testing.T) {
	if !nodeHasEpicLabel(&depgraph.Node{Labels: []string{"component:platform", "type:epic"}}) {
		t.Error("expected true for type:epic")
	}
	if nodeHasEpicLabel(&depgraph.Node{Labels: []string{"type:feature"}}) {
		t.Error("expected false for non-epic")
	}
}

// TestReconcileStuckInReview_SkipsCleanPR: an In-review issue whose PR is CLEAN
// (legitimately awaiting the merge stage) must NOT be moved/retried.
func TestReconcileStuckInReview_SkipsCleanPR(t *testing.T) {
	stubReconcileGh(t, func(_ context.Context, _ ...string) ([]byte, error) {
		return []byte(`[{"number":64,"headRefName":"feat/43-x","mergeStateStatus":"CLEAN"}]`), nil
	})
	as := &AutonomousScheduler{}
	g := &depgraph.Graph{Nodes: map[string]*depgraph.Node{
		"nightgauge/acmeapp-platform#43": {Repo: "nightgauge/acmeapp-platform", Number: 43, State: "OPEN", BoardStatus: "In review"},
	}}
	as.reconcileStuckInReviewPRs(context.Background(), g)
	if len(as.inReviewRecoveryAttempts) != 0 {
		t.Fatalf("CLEAN PR must not be recovered; attempts=%v", as.inReviewRecoveryAttempts)
	}
	if g.Nodes["nightgauge/acmeapp-platform#43"].BoardStatus != "In review" {
		t.Fatal("CLEAN PR node status should be unchanged")
	}
}

// TestOpenPRMergeStatesForRepo_BatchesOneCall: the batched lookup returns a
// number→state map from a SINGLE gh pr list, and openPRMergeStateForIssue
// delegates to it. This is the quota-saving contract (#3896).
func TestOpenPRMergeStatesForRepo_BatchesOneCall(t *testing.T) {
	calls := 0
	stubReconcileGh(t, func(_ context.Context, _ ...string) ([]byte, error) {
		calls++
		return []byte(`[
			{"number":64,"headRefName":"feat/43-x","mergeStateStatus":"DIRTY"},
			{"number":65,"headRefName":"feat/44-y","mergeStateStatus":"BEHIND"},
			{"number":66,"headRefName":"feat/45-z","mergeStateStatus":"CLEAN"}
		]`), nil
	})
	as := &AutonomousScheduler{}
	states, ok := as.openPRMergeStatesForRepo(context.Background(), "nightgauge/acmeapp-platform")
	if !ok || calls != 1 {
		t.Fatalf("got ok=%v calls=%d, want ok=true calls=1", ok, calls)
	}
	if states[43].MergeState != "DIRTY" || states[44].MergeState != "BEHIND" || states[45].MergeState != "CLEAN" {
		t.Fatalf("unexpected states map: %v", states)
	}
}

// TestReconcileStuckInReview_OneCallPerRepo: with multiple in-review nodes in
// the same repo, the sweep must issue exactly ONE gh pr list for that repo
// (was one-per-node before #3896).
func TestReconcileStuckInReview_OneCallPerRepo(t *testing.T) {
	calls := 0
	stubReconcileGh(t, func(_ context.Context, _ ...string) ([]byte, error) {
		calls++
		// Two stuck PRs in the same repo; none mergeable (so no MoveStatus,
		// which would need a real project service) — we only assert call count.
		return []byte(`[
			{"number":64,"headRefName":"feat/43-x","mergeStateStatus":"CLEAN"},
			{"number":65,"headRefName":"feat/44-y","mergeStateStatus":"CLEAN"}
		]`), nil
	})
	as := &AutonomousScheduler{}
	g := &depgraph.Graph{Nodes: map[string]*depgraph.Node{
		"nightgauge/acmeapp-platform#43": {Repo: "nightgauge/acmeapp-platform", Number: 43, State: "OPEN", BoardStatus: "In review"},
		"nightgauge/acmeapp-platform#44": {Repo: "nightgauge/acmeapp-platform", Number: 44, State: "OPEN", BoardStatus: "In review"},
	}}
	as.reconcileStuckInReviewPRs(context.Background(), g)
	if calls != 1 {
		t.Fatalf("expected exactly 1 gh pr list for the repo, got %d", calls)
	}
}

// TestReconcileStuckInReview_SkipsNonInReviewAndEpics: only In-review,
// non-epic nodes are considered.
func TestReconcileStuckInReview_SkipsNonInReviewAndEpics(t *testing.T) {
	called := false
	stubReconcileGh(t, func(_ context.Context, _ ...string) ([]byte, error) {
		called = true
		return []byte(`[{"number":1,"headRefName":"feat/1-x","mergeStateStatus":"DIRTY"}]`), nil
	})
	as := &AutonomousScheduler{}
	g := &depgraph.Graph{Nodes: map[string]*depgraph.Node{
		"r#1": {Repo: "nightgauge/r", Number: 1, State: "OPEN", BoardStatus: "Ready"},                                    // not in review
		"r#2": {Repo: "nightgauge/r", Number: 2, State: "OPEN", BoardStatus: "In review", Labels: []string{"type:epic"}}, // epic
		"r#3": {Repo: "nightgauge/r", Number: 3, State: "CLOSED", BoardStatus: "In review"},                              // closed
	}}
	as.reconcileStuckInReviewPRs(context.Background(), g)
	if called {
		t.Fatal("no gh PR lookup should run for Ready/epic/closed nodes")
	}
}

// TestRefreshBlockedReadyPRs_PopulatesInReviewPRBacked (#281): the sweep
// records "In review" nodes with a confirmed OPEN PR into inReviewPRBacked,
// and leaves out "In review" nodes with no confirmed PR — sharing one
// gh-pr-list call per repo with the existing blockedReadyPRIssues sweep.
func TestRefreshBlockedReadyPRs_PopulatesInReviewPRBacked(t *testing.T) {
	calls := 0
	stubReconcileGh(t, func(_ context.Context, args ...string) ([]byte, error) {
		if !ghArgsContain(args, "list") {
			return []byte("[]"), nil
		}
		calls++
		return []byte(`[{"number":10,"headRefName":"feat/1-backed","mergeStateStatus":"CLEAN"}]`), nil
	})

	as := &AutonomousScheduler{config: AutonomousConfig{}, state: &AutonomousState{}}
	g := &depgraph.Graph{Nodes: map[string]*depgraph.Node{
		"O/app#1": {Repo: "O/app", Number: 1, State: "OPEN", BoardStatus: "In review"}, // confirmed PR → backed
		"O/app#2": {Repo: "O/app", Number: 2, State: "OPEN", BoardStatus: "In review"}, // no PR → not backed
	}}

	as.refreshBlockedReadyPRs(context.Background(), g)

	if calls != 1 {
		t.Fatalf("expected exactly 1 gh pr list for the repo, got %d", calls)
	}
	if !as.inReviewPRBacked["O/app#1"] {
		t.Error("expected O/app#1 (In review, confirmed PR) to be marked backed")
	}
	if as.inReviewPRBacked["O/app#2"] {
		t.Error("expected O/app#2 (In review, no PR) NOT marked backed")
	}
}

// TestRefreshBlockedReadyPRs_QueryFailure_LeavesInReviewPRBackedEmpty: a
// failed gh query must fail closed — the repo's "In review" nodes contribute
// nothing to inReviewPRBacked, never a silent "satisfied".
func TestRefreshBlockedReadyPRs_QueryFailure_LeavesInReviewPRBackedEmpty(t *testing.T) {
	stubReconcileGh(t, func(_ context.Context, _ ...string) ([]byte, error) {
		return nil, context.DeadlineExceeded
	})

	as := &AutonomousScheduler{config: AutonomousConfig{}, state: &AutonomousState{}}
	g := &depgraph.Graph{Nodes: map[string]*depgraph.Node{
		"O/app#1": {Repo: "O/app", Number: 1, State: "OPEN", BoardStatus: "In review"},
	}}

	as.refreshBlockedReadyPRs(context.Background(), g)

	if len(as.inReviewPRBacked) != 0 {
		t.Fatalf("gh error must leave inReviewPRBacked empty (fail-closed); got %v", as.inReviewPRBacked)
	}
}

// TestEvaluateDeps_InReviewWithoutPRBacking_Blocks (#281): a dep whose board
// status is "In review" but has no confirmed OPEN PR must NOT satisfy the
// blockedBy edge — the status string alone is not evidence.
func TestEvaluateDeps_InReviewWithoutPRBacking_Blocks(t *testing.T) {
	g := &depgraph.Graph{Nodes: map[string]*depgraph.Node{
		"O/app#1": {Repo: "O/app", Number: 1, State: "OPEN", BoardStatus: "In review"},
	}}
	res := evaluateDeps([]string{"O/app#1"}, g, nil, nil)
	if !res.blocked {
		t.Fatal("expected blocked=true for In-review dep with no PR-backing evidence")
	}
}

// TestEvaluateDeps_InReviewWithPRBacking_Satisfies is the regression guard:
// a dep confirmed PR-backed still satisfies the edge exactly as before.
func TestEvaluateDeps_InReviewWithPRBacking_Satisfies(t *testing.T) {
	g := &depgraph.Graph{Nodes: map[string]*depgraph.Node{
		"O/app#1": {Repo: "O/app", Number: 1, State: "OPEN", BoardStatus: "In review"},
	}}
	prBacked := map[string]bool{"O/app#1": true}
	res := evaluateDeps([]string{"O/app#1"}, g, nil, prBacked)
	if res.blocked {
		t.Fatalf("expected blocked=false for In-review dep with confirmed PR backing, got %+v", res)
	}
}
