package orchestrator

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"github.com/nightgauge/nightgauge/internal/state"
	"github.com/nightgauge/nightgauge/pkg/types"
)

// stubReconcileGh swaps the package-level reconcileExecGh for the duration of a
// test and restores it on cleanup. The handler receives the gh argv so a test
// can branch on `issue view` vs `pr list`.
func stubReconcileGh(t *testing.T, fn func(ctx context.Context, args ...string) ([]byte, error)) {
	t.Helper()
	prev := reconcileExecGh
	reconcileExecGh = fn
	t.Cleanup(func() { reconcileExecGh = prev })
}

// ghArgsContain reports whether the gh argv contains a given token — used to
// distinguish the issue-state probe from the branch-PR probe in the stub.
func ghArgsContain(args []string, want string) bool {
	for _, a := range args {
		if a == want {
			return true
		}
	}
	return false
}

func acmeappItem(number int) types.BoardItem {
	return types.BoardItem{
		Number: number,
		Repo:   "nightgauge/acmeapp-platform",
		State:  "CLOSED",
	}
}

// TestReconcileIssueResolved_IssueClosed: a non-terminal stage failed but the
// issue is CLOSED on the forge → reconciled (AC #1). This is the Case 1 shape
// where the work landed and the issue was closed in a prior run.
func TestReconcileIssueResolved_IssueClosed(t *testing.T) {
	stubReconcileGh(t, func(_ context.Context, args ...string) ([]byte, error) {
		if ghArgsContain(args, "issue") && ghArgsContain(args, "view") {
			return []byte(`{"state":"CLOSED"}`), nil
		}
		return []byte(`[]`), nil
	})

	if !reconcileIssueResolved(context.Background(), acmeappItem(40), "feat/40-x") {
		t.Fatalf("issue CLOSED must reconcile to resolved")
	}
}

// TestReconcileIssueResolved_PrMerged: pre-flight-death shape — the issue is
// still OPEN but the branch's PR is MERGED → reconciled (AC #1).
func TestReconcileIssueResolved_PrMerged(t *testing.T) {
	stubReconcileGh(t, func(_ context.Context, args ...string) ([]byte, error) {
		if ghArgsContain(args, "issue") && ghArgsContain(args, "view") {
			return []byte(`{"state":"OPEN"}`), nil
		}
		// pr list
		return []byte(`[{"state":"MERGED"}]`), nil
	})

	if !reconcileIssueResolved(context.Background(), acmeappItem(40), "feat/40-x") {
		t.Fatalf("branch PR MERGED must reconcile to resolved")
	}
}

// TestReconcileIssueResolved_PrOpen: an OPEN PR for the branch (issue still
// open) also reconciles — the work has progressed past dev into review, so a
// non-terminal stage's phantom failure should not page.
func TestReconcileIssueResolved_PrOpen(t *testing.T) {
	stubReconcileGh(t, func(_ context.Context, args ...string) ([]byte, error) {
		if ghArgsContain(args, "issue") && ghArgsContain(args, "view") {
			return []byte(`{"state":"OPEN"}`), nil
		}
		return []byte(`[{"state":"OPEN"}]`), nil
	})

	if !reconcileIssueResolved(context.Background(), acmeappItem(40), "feat/40-x") {
		t.Fatalf("branch PR OPEN must reconcile to resolved")
	}
}

// TestReconcileIssueResolved_GenuinelyOpen: issue OPEN, no PR for the branch →
// NOT reconciled. Guards against masking a real failure (negative control).
func TestReconcileIssueResolved_GenuinelyOpen(t *testing.T) {
	stubReconcileGh(t, func(_ context.Context, args ...string) ([]byte, error) {
		if ghArgsContain(args, "issue") && ghArgsContain(args, "view") {
			return []byte(`{"state":"OPEN"}`), nil
		}
		return []byte(`[]`), nil
	})

	if reconcileIssueResolved(context.Background(), acmeappItem(40), "feat/40-x") {
		t.Fatalf("genuinely-open issue with no PR must NOT reconcile")
	}
}

// TestReconcileIssueResolved_PrClosedNotMerged: a CLOSED-but-not-merged PR
// (abandoned branch) does NOT reconcile — the work never landed.
func TestReconcileIssueResolved_PrClosedNotMerged(t *testing.T) {
	stubReconcileGh(t, func(_ context.Context, args ...string) ([]byte, error) {
		if ghArgsContain(args, "issue") && ghArgsContain(args, "view") {
			return []byte(`{"state":"OPEN"}`), nil
		}
		return []byte(`[{"state":"CLOSED"}]`), nil
	})

	if reconcileIssueResolved(context.Background(), acmeappItem(40), "feat/40-x") {
		t.Fatalf("abandoned (CLOSED, not MERGED) PR must NOT reconcile")
	}
}

// TestReconcileIssueResolved_ForgeError_FailsClosed: any gh error returns false
// (fail-closed) so an uncertain check never masks a genuine failure.
func TestReconcileIssueResolved_ForgeError_FailsClosed(t *testing.T) {
	stubReconcileGh(t, func(_ context.Context, _ ...string) ([]byte, error) {
		return nil, errors.New("gh: API rate limit exceeded")
	})

	if reconcileIssueResolved(context.Background(), acmeappItem(40), "feat/40-x") {
		t.Fatalf("forge query error must fail closed (return false)")
	}
}

// TestReconcileIssueResolved_UnparseableJSON_FailsClosed: a malformed gh
// response is treated as not-reconciled (fail-closed).
func TestReconcileIssueResolved_UnparseableJSON_FailsClosed(t *testing.T) {
	stubReconcileGh(t, func(_ context.Context, _ ...string) ([]byte, error) {
		return []byte(`not json`), nil
	})

	if reconcileIssueResolved(context.Background(), acmeappItem(40), "feat/40-x") {
		t.Fatalf("unparseable gh output must fail closed (return false)")
	}
}

// TestReconcileIssueResolved_MalformedRepo_FailsClosed: a repo slug that fails
// the well-formed guard never shells out and returns false.
func TestReconcileIssueResolved_MalformedRepo_FailsClosed(t *testing.T) {
	called := false
	stubReconcileGh(t, func(_ context.Context, _ ...string) ([]byte, error) {
		called = true
		return []byte(`{"state":"CLOSED"}`), nil
	})

	item := types.BoardItem{Number: 40, Repo: "not-a-valid-repo;rm -rf"}
	if reconcileIssueResolved(context.Background(), item, "feat/40-x") {
		t.Fatalf("malformed repo must fail closed")
	}
	if called {
		t.Fatalf("malformed repo must short-circuit before any gh call")
	}
}

// TestReconcileIssueResolved_EmptyBranch_OnlyIssueCheck: with no branch, only
// the issue-closed check runs; the PR probe is skipped.
func TestReconcileIssueResolved_EmptyBranch_OnlyIssueCheck(t *testing.T) {
	prListCalled := false
	stubReconcileGh(t, func(_ context.Context, args ...string) ([]byte, error) {
		if ghArgsContain(args, "pr") && ghArgsContain(args, "list") {
			prListCalled = true
		}
		if ghArgsContain(args, "issue") && ghArgsContain(args, "view") {
			return []byte(`{"state":"OPEN"}`), nil
		}
		return []byte(`[{"state":"MERGED"}]`), nil
	})

	if reconcileIssueResolved(context.Background(), acmeappItem(40), "") {
		t.Fatalf("empty branch + open issue must not reconcile via PR probe")
	}
	if prListCalled {
		t.Fatalf("empty branch must skip the pr list probe")
	}
}

// --- Integration-shape tests: replay the scheduler's #3873 non-terminal block ---

// reconcileNonTerminalForTest replays the exact decision logic from the
// scheduler's #3873 block (scheduler.go, just before writeStageExitRecord) so
// the integration contract is pinned without the full runPipeline plumbing. If
// that scheduler predicate changes, mirror it here. Returns the post-reconcile
// (err, exitCode).
func reconcileNonTerminalForTest(stage state.PipelineStage, item types.BoardItem, branch string, err error, exitCode int) (error, int) {
	if (err != nil || exitCode != 0) && !isTerminalStage(stage) {
		if reconcileIssueResolved(context.Background(), item, branch) {
			return nil, 0
		}
	}
	return err, exitCode
}

// TestScheduler_NonTerminalFailure_IssueClosed_Reconciled: feature-validate
// exits 1 while the issue is CLOSED → reconciled to success:true (no page). AC #1.
func TestScheduler_NonTerminalFailure_IssueClosed_Reconciled(t *testing.T) {
	stubReconcileGh(t, func(_ context.Context, args ...string) ([]byte, error) {
		if ghArgsContain(args, "issue") && ghArgsContain(args, "view") {
			return []byte(`{"state":"CLOSED"}`), nil
		}
		return []byte(`[]`), nil
	})

	err, exitCode := reconcileNonTerminalForTest(
		state.StageFeatureValidate, acmeappItem(40), "feat/40-x",
		errors.New("schema validation failed"), 1)

	if err != nil || exitCode != 0 {
		t.Errorf("non-terminal failure on a CLOSED issue must reconcile; err=%v exit=%d", err, exitCode)
	}
}

// TestScheduler_NonTerminalFailure_PrMerged_Reconciled: feature-dev exits 1
// (pre-flight-death shape) while the branch PR is MERGED → reconciled. AC #1.
func TestScheduler_NonTerminalFailure_PrMerged_Reconciled(t *testing.T) {
	stubReconcileGh(t, func(_ context.Context, args ...string) ([]byte, error) {
		if ghArgsContain(args, "issue") && ghArgsContain(args, "view") {
			return []byte(`{"state":"OPEN"}`), nil
		}
		return []byte(`[{"state":"MERGED"}]`), nil
	})

	// Pre-flight death: non-zero exit, no stage error text (empty err).
	err, exitCode := reconcileNonTerminalForTest(
		state.StageFeatureDev, acmeappItem(40), "feat/40-x", nil, 1)

	if err != nil || exitCode != 0 {
		t.Errorf("pre-flight death on a merged-PR issue must reconcile; err=%v exit=%d", err, exitCode)
	}
}

// TestScheduler_NonTerminalFailure_GenuinelyOpen_NotReconciled: feature-dev
// exits 1, issue OPEN, no PR → NOT reconciled, success:false preserved. Guards
// against masking real failures (negative control).
func TestScheduler_NonTerminalFailure_GenuinelyOpen_NotReconciled(t *testing.T) {
	stubReconcileGh(t, func(_ context.Context, args ...string) ([]byte, error) {
		if ghArgsContain(args, "issue") && ghArgsContain(args, "view") {
			return []byte(`{"state":"OPEN"}`), nil
		}
		return []byte(`[]`), nil
	})

	stageErr := errors.New("subagent crashed")
	err, exitCode := reconcileNonTerminalForTest(
		state.StageFeatureDev, acmeappItem(40), "feat/40-x", stageErr, 1)

	if err == nil || exitCode == 0 {
		t.Errorf("genuine non-terminal failure must NOT be reconciled; err=%v exit=%d", err, exitCode)
	}
}

// TestScheduler_NonTerminalFailure_TerminalStageUnaffected: the #3873 block is
// guarded on !isTerminalStage, so a terminal stage never enters it (the #3835
// terminal block owns those). Even with a CLOSED issue, this helper leaves a
// terminal failure untouched.
func TestScheduler_NonTerminalFailure_TerminalStageUnaffected(t *testing.T) {
	stubReconcileGh(t, func(_ context.Context, _ ...string) ([]byte, error) {
		return []byte(`{"state":"CLOSED"}`), nil
	})

	stageErr := fmt.Errorf("pr-create failed")
	err, exitCode := reconcileNonTerminalForTest(
		state.StagePRCreate, acmeappItem(40), "feat/40-x", stageErr, 1)

	if err == nil || exitCode == 0 {
		t.Errorf("terminal stage must not be reconciled by the non-terminal block; err=%v exit=%d", err, exitCode)
	}
}
