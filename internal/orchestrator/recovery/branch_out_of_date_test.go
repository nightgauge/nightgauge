package recovery

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/nightgauge/nightgauge/internal/orchestrator/gates"
	pmstages "github.com/nightgauge/nightgauge/internal/orchestrator/stages"
	"github.com/nightgauge/nightgauge/internal/state"
)

func stubExecGit(t *testing.T, fn func(ctx context.Context, dir string, args ...string) ([]byte, error)) {
	t.Helper()
	prev := execGit
	execGit = fn
	t.Cleanup(func() { execGit = prev })
}

// greenChecksGh stubs execGh so the rebased PR's check rollup reads all-green.
func greenChecksGh(t *testing.T) {
	t.Helper()
	stubExecGh(t, func(_ context.Context, _ ...string) ([]byte, error) {
		return []byte(`{"statusCheckRollup":[{"name":"build","conclusion":"SUCCESS"},{"name":"test","conclusion":"SUCCESS"}]}`), nil
	})
}

// newBranchOOD builds the action with a fast poll cadence for tests.
func newBranchOOD(runner pmstages.PRMergeRunner) *BranchOutOfDate {
	a := NewBranchOutOfDate(runner)
	a.pollInterval = time.Millisecond
	a.pollMax = 3
	return a
}

// TestAction_BranchOutOfDate_Matches_AndRecovers is the happy path: rebase +
// CI-green + runner returns PathMerged → Recovered=true.
func TestAction_BranchOutOfDate_Matches_AndRecovers(t *testing.T) {
	calls := []string{}
	stubExecGit(t, func(_ context.Context, _ string, args ...string) ([]byte, error) {
		calls = append(calls, strings.Join(args, " "))
		return []byte(""), nil
	})
	greenChecksGh(t)

	runner := &fakePRMergeRunner{
		res: pmstages.PRMergeResult{Path: pmstages.PathMerged, PRNumber: 100, PRState: "MERGED", Reason: pmstages.ReasonCleanMerged},
	}
	a := newBranchOOD(runner)
	failure := StageFailure{
		Stage:     state.StagePRMerge,
		GateKind:  gates.KindNoOp,
		PRNumber:  100,
		Workspace: "/tmp/work",
		Reason:    "dirty-merge-state: BEHIND",
	}
	if !a.Matches(failure) {
		t.Fatalf("expected match")
	}
	res := a.Execute(context.Background(), failure)
	if !res.Recovered {
		t.Fatalf("expected Recovered=true; got %q", res.Reason)
	}
	if res.FollowUp != FollowUpStageCanResume {
		t.Errorf("FollowUp = %q, want %q", res.FollowUp, FollowUpStageCanResume)
	}
	want := []string{"fetch origin main", "rebase origin/main", "push --force-with-lease"}
	if len(calls) != 3 {
		t.Fatalf("expected 3 git calls, got %d (%v)", len(calls), calls)
	}
	for i, w := range want {
		if calls[i] != w {
			t.Errorf("step %d = %q, want %q", i, calls[i], w)
		}
	}
}

// TestAction_BranchOutOfDate_RebasedButRunnerPunts asserts that a successful
// rebase + green CI is NOT enough — if the runner punts (PR not actually
// merged) the action declines and routes to triage instead of advancing.
func TestAction_BranchOutOfDate_RebasedButRunnerPunts(t *testing.T) {
	stubExecGit(t, func(_ context.Context, _ string, _ ...string) ([]byte, error) {
		return []byte(""), nil
	})
	greenChecksGh(t)

	runner := &fakePRMergeRunner{
		res: pmstages.PRMergeResult{Path: pmstages.PathPunt, PRNumber: 100, Reason: pmstages.ReasonReviewMissing + ": REVIEW_REQUIRED"},
	}
	a := newBranchOOD(runner)
	res := a.Execute(context.Background(), StageFailure{
		Stage: state.StagePRMerge, GateKind: gates.KindNoOp, PRNumber: 100, Workspace: "/tmp", Reason: "BEHIND",
	})
	if res.Recovered {
		t.Error("expected Recovered=false when runner punts after rebase")
	}
	if res.FollowUp != FollowUpHumanTriageRequired {
		t.Errorf("FollowUp = %q, want triage", res.FollowUp)
	}
}

// TestAction_BranchOutOfDate_CINotGreenAfterRebase asserts that a failing check
// on the rebased head blocks recovery (no merge attempted).
func TestAction_BranchOutOfDate_CINotGreenAfterRebase(t *testing.T) {
	stubExecGit(t, func(_ context.Context, _ string, _ ...string) ([]byte, error) {
		return []byte(""), nil
	})
	stubExecGh(t, func(_ context.Context, _ ...string) ([]byte, error) {
		return []byte(`{"statusCheckRollup":[{"name":"test","conclusion":"FAILURE"}]}`), nil
	})

	runner := &fakePRMergeRunner{
		// Would merge if reached — but CI failure must short-circuit before this.
		res: pmstages.PRMergeResult{Path: pmstages.PathMerged, PRNumber: 100},
	}
	a := newBranchOOD(runner)
	res := a.Execute(context.Background(), StageFailure{
		Stage: state.StagePRMerge, GateKind: gates.KindNoOp, PRNumber: 100, Workspace: "/tmp", Reason: "BEHIND",
	})
	if res.Recovered {
		t.Error("expected Recovered=false when CI is red on rebased head")
	}
	if res.FollowUp != FollowUpHumanTriageRequired {
		t.Errorf("FollowUp = %q, want triage", res.FollowUp)
	}
}

// TestAction_BranchOutOfDate_CINeverCompletes asserts that an in-flight head
// that never completes within the poll budget declines without claiming
// recovery (no runner merge).
func TestAction_BranchOutOfDate_CINeverCompletes(t *testing.T) {
	stubExecGit(t, func(_ context.Context, _ string, _ ...string) ([]byte, error) {
		return []byte(""), nil
	})
	stubExecGh(t, func(_ context.Context, _ ...string) ([]byte, error) {
		return []byte(`{"statusCheckRollup":[{"name":"test","conclusion":""}]}`), nil
	})

	merged := false
	runner := &fakePRMergeRunner{res: pmstages.PRMergeResult{Path: pmstages.PathMerged}}
	a := newBranchOOD(&recordingRunner{inner: runner, ran: &merged})
	res := a.Execute(context.Background(), StageFailure{
		Stage: state.StagePRMerge, GateKind: gates.KindNoOp, PRNumber: 100, Workspace: "/tmp", Reason: "BEHIND",
	})
	if res.Recovered {
		t.Error("expected Recovered=false when CI never completes")
	}
	if merged {
		t.Error("runner must NOT be invoked when CI never goes green")
	}
	if res.FollowUp != FollowUpNoAction {
		t.Errorf("FollowUp = %q, want no-action", res.FollowUp)
	}
}

// recordingRunner flags whether Run was invoked.
type recordingRunner struct {
	inner pmstages.PRMergeRunner
	ran   *bool
}

func (r *recordingRunner) Run(ctx context.Context, issue int, repo, workdir string) (pmstages.PRMergeResult, error) {
	*r.ran = true
	return r.inner.Run(ctx, issue, repo, workdir)
}

func TestAction_BranchOutOfDate_Matches(t *testing.T) {
	a := NewBranchOutOfDate(&fakePRMergeRunner{})
	matchCases := []struct {
		name string
		f    StageFailure
	}{
		{"behind keyword", StageFailure{Stage: state.StagePRMerge, GateKind: gates.KindNoOp, Workspace: "/x", Reason: "BEHIND"}},
		{"dirty keyword", StageFailure{Stage: state.StagePRMerge, GateKind: gates.KindNoOp, Workspace: "/x", Reason: "DIRTY"}},
		{"dirty-merge-state reason", StageFailure{Stage: state.StagePRMerge, GateKind: gates.KindNoOp, Workspace: "/x", Reason: pmstages.ReasonDirtyState + ": DIRTY"}},
		{"dirty in evidence", StageFailure{Stage: state.StagePRMerge, GateKind: gates.KindNoOp, Workspace: "/x", Evidence: []string{"mergeStateStatus=DIRTY"}}},
	}
	for _, c := range matchCases {
		t.Run("match/"+c.name, func(t *testing.T) {
			if !a.Matches(c.f) {
				t.Errorf("expected match for %s", c.name)
			}
		})
	}
}

func TestAction_BranchOutOfDate_NoMatch_FallsThrough(t *testing.T) {
	a := NewBranchOutOfDate(&fakePRMergeRunner{})
	cases := []struct {
		name string
		f    StageFailure
	}{
		{"wrong stage", StageFailure{Stage: state.StagePRCreate, GateKind: gates.KindNoOp, Workspace: "/x", Reason: "BEHIND"}},
		{"gate ok", StageFailure{Stage: state.StagePRMerge, GateKind: gates.KindOK, Workspace: "/x", Reason: "BEHIND"}},
		{"no behind/dirty keyword", StageFailure{Stage: state.StagePRMerge, GateKind: gates.KindNoOp, Workspace: "/x", Reason: "CLEAN"}},
		{"no workspace", StageFailure{Stage: state.StagePRMerge, GateKind: gates.KindNoOp, Reason: "BEHIND"}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if a.Matches(c.f) {
				t.Errorf("expected no match")
			}
		})
	}
}

// TestAction_BranchOutOfDate_RebaseConflict locks the #4072 hand-off: a rebase
// conflict captures the conflict context (files + both sides) BEFORE
// `git rebase --abort`, emits a CONFLICT_RESOLUTION_NEEDED feedback signal, and
// returns FollowUpStageCanResume so the conflict-recovery loop rewinds to
// feature-dev — instead of escalating straight to human triage. The capture
// MUST happen before the abort.
func TestAction_BranchOutOfDate_RebaseConflict(t *testing.T) {
	ws := t.TempDir()
	aborted := false
	capturedBeforeAbort := false
	stubExecGit(t, func(_ context.Context, _ string, args ...string) ([]byte, error) {
		joined := strings.Join(args, " ")
		switch {
		case joined == "fetch origin main":
			return []byte(""), nil
		case joined == "rebase origin/main":
			return []byte("CONFLICT (content): Merge conflict in foo.go"), errors.New("exit 1: rebase conflict")
		case joined == "rev-parse --abbrev-ref HEAD":
			return []byte("feat/77-thing\n"), nil
		case joined == "diff --name-only --diff-filter=U":
			// Conflict capture happens before the abort.
			if !aborted {
				capturedBeforeAbort = true
			}
			return []byte("foo.go\n"), nil
		case strings.HasPrefix(joined, "show :2:"):
			return []byte("ours-content"), nil
		case strings.HasPrefix(joined, "show :3:"):
			return []byte("theirs-content"), nil
		case joined == "rebase --abort":
			aborted = true
			return []byte(""), nil
		}
		return []byte(""), nil
	})

	a := NewBranchOutOfDate(&fakePRMergeRunner{})
	res := a.Execute(context.Background(), StageFailure{
		Stage: state.StagePRMerge, GateKind: gates.KindNoOp, PRNumber: 1, IssueNumber: 77, Workspace: ws, Reason: "BEHIND",
	})
	if res.Recovered {
		t.Error("expected no in-place recovery on rebase conflict (LLM dev stage resolves)")
	}
	if !aborted {
		t.Error("expected rebase --abort to run after conflict")
	}
	if !capturedBeforeAbort {
		t.Error("conflict context must be captured BEFORE rebase --abort wipes the conflict state")
	}
	if res.FollowUp != FollowUpStageCanResume {
		t.Errorf("FollowUp = %q, want stage-can-resume (defer to conflict-recovery)", res.FollowUp)
	}

	// conflict-context-77.json must have been written.
	ctxPath := filepath.Join(ws, ".nightgauge", "pipeline", "conflict-context-77.json")
	if _, err := os.Stat(ctxPath); err != nil {
		t.Errorf("expected conflict-context-77.json written, stat err: %v", err)
	}
	// feedback-77.json must carry a CONFLICT_RESOLUTION_NEEDED signal.
	fbData, err := os.ReadFile(filepath.Join(ws, ".nightgauge", "pipeline", "feedback-77.json"))
	if err != nil {
		t.Fatalf("read feedback-77.json: %v", err)
	}
	var fb feedbackOnDisk
	if err := json.Unmarshal(fbData, &fb); err != nil {
		t.Fatalf("parse feedback: %v", err)
	}
	hasConflict := false
	for _, s := range fb.Signals {
		if s.SignalType == "CONFLICT_RESOLUTION_NEEDED" && s.BacktrackTargetStage == "feature-dev" {
			hasConflict = true
		}
	}
	if !hasConflict {
		t.Errorf("expected CONFLICT_RESOLUTION_NEEDED signal targeting feature-dev, got %+v", fb.Signals)
	}
}

// TestAction_BranchOutOfDate_FetchFailStillTriages confirms a non-rebase step
// failure (e.g. fetch) still escalates to human triage — only the rebase-
// conflict branch defers to conflict-recovery.
func TestAction_BranchOutOfDate_FetchFailStillTriages(t *testing.T) {
	stubExecGit(t, func(_ context.Context, _ string, args ...string) ([]byte, error) {
		if strings.Join(args, " ") == "fetch origin main" {
			return []byte("fatal: unable to access"), errors.New("exit 128")
		}
		return []byte(""), nil
	})
	a := NewBranchOutOfDate(&fakePRMergeRunner{})
	res := a.Execute(context.Background(), StageFailure{
		Stage: state.StagePRMerge, GateKind: gates.KindNoOp, PRNumber: 1, IssueNumber: 5, Workspace: t.TempDir(), Reason: "BEHIND",
	})
	if res.Recovered {
		t.Error("fetch failure cannot recover")
	}
	if res.FollowUp != FollowUpHumanTriageRequired {
		t.Errorf("FollowUp = %q, want triage on fetch failure", res.FollowUp)
	}
}

// TestAction_BranchOutOfDate_NilRunner guards the wiring invariant.
func TestAction_BranchOutOfDate_NilRunner(t *testing.T) {
	a := NewBranchOutOfDate(nil)
	res := a.Execute(context.Background(), StageFailure{
		Stage: state.StagePRMerge, GateKind: gates.KindNoOp, PRNumber: 1, Workspace: "/tmp", Reason: "BEHIND",
	})
	if res.Recovered {
		t.Error("nil runner cannot recover")
	}
	if res.FollowUp != FollowUpHumanTriageRequired {
		t.Errorf("FollowUp = %q, want triage", res.FollowUp)
	}
}

// TestSummarizeChecks_EmptyIsPending locks the #4071 review fix: an empty
// statusCheckRollup (common right after a force-push, before the rebased head's
// runs register) must be treated as PENDING, not green — otherwise waitForCI
// short-circuits and re-runs the merge before the rebased commits are validated.
func TestSummarizeChecks_EmptyIsPending(t *testing.T) {
	if got := summarizeChecks(nil); got != checksPending {
		t.Errorf("nil rollup must be pending, got %v", got)
	}
	if got := summarizeChecks([]statusCheckRollupEntry{}); got != checksPending {
		t.Errorf("empty rollup must be pending, got %v", got)
	}
}
