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
	if states[43] != "DIRTY" || states[44] != "BEHIND" || states[45] != "CLEAN" {
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
