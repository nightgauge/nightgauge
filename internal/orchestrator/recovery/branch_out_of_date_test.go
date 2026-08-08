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

// stubExecGitToFile replaces the streaming blob writer used by the durable
// conflict-evidence dump. Without it a stubbed test would fall through to the
// real `git cat-file`, and whether the dump "succeeded" would depend on whether
// the temp dir happened to be a git repo.
func stubExecGitToFile(t *testing.T, fn func(ctx context.Context, dir, dest string, args ...string) error) {
	t.Helper()
	prev := execGitToFile
	execGitToFile = fn
	t.Cleanup(func() { execGitToFile = prev })
}

// Stub index-stage blob ids. Real object ids so isBlobSHA accepts them — the
// capture path reads every blob BY ID now, never by path.
const (
	stubBaseSHA   = "1111111111111111111111111111111111111111"
	stubOursSHA   = "2222222222222222222222222222222222222222"
	stubTheirsSHA = "3333333333333333333333333333333333333333"
)

// stubUnmergedIndex renders `git ls-files -u -z` output for one conflicting
// path with all three stages: NUL-terminated records of "<mode> <sha> <stage>\t<path>".
func stubUnmergedIndex(path string) []byte {
	return []byte(
		"100644 " + stubBaseSHA + " 1\t" + path + "\x00" +
			"100644 " + stubOursSHA + " 2\t" + path + "\x00" +
			"100644 " + stubTheirsSHA + " 3\t" + path + "\x00")
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
	// Order matters and every entry is load-bearing (#301):
	//
	//  1-2. the pre-existing-rebase probe, BEFORE any mutation — a rebase this
	//       run did not start must never be rebased over or aborted, and after
	//       `git rebase origin/main` runs the answer is no longer attributable.
	//    3. the branch, while HEAD is still attached — `git rebase` detaches HEAD
	//       for its duration, so a later lookup cannot name the branch.
	//  4-6. the three rebase steps.
	want := []string{
		"rev-parse --git-path rebase-merge",
		"rev-parse --git-path rebase-apply",
		"rev-parse --abbrev-ref HEAD",
		"fetch origin main",
		"rebase origin/main",
		"push --force-with-lease",
	}
	if len(calls) != len(want) {
		t.Fatalf("expected %d git calls, got %d (%v)", len(want), len(calls), calls)
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
//
// The `rev-parse --abbrev-ref HEAD` stub is STATEFUL on purpose (#301). Real git
// detaches HEAD for the duration of a rebase and answers the literal "HEAD"; the
// old stub answered "feat/77-thing" unconditionally — a response git never
// gives mid-rebase — which is precisely why the branch always landed in the
// context file as "unknown" while this test stayed green. With the honest stub,
// moving the branch resolution back inside the failure handler makes the branch
// unresolvable and this test fails.
func TestAction_BranchOutOfDate_RebaseConflict(t *testing.T) {
	ws := t.TempDir()
	aborted := false
	rebasing := false
	capturedBeforeAbort := false
	stubExecGit(t, func(_ context.Context, _ string, args ...string) ([]byte, error) {
		joined := strings.Join(args, " ")
		switch {
		case joined == "fetch origin main":
			return []byte(""), nil
		case joined == "rebase origin/main":
			rebasing = true
			return []byte("CONFLICT (content): Merge conflict in foo.go"), errors.New("exit 1: rebase conflict")
		case joined == "rev-parse --abbrev-ref HEAD":
			if rebasing {
				// Detached HEAD — what git actually answers mid-rebase.
				return []byte("HEAD\n"), nil
			}
			return []byte("feat/77-thing\n"), nil
		case joined == "ls-files -u -z":
			// Conflict capture happens before the abort.
			if !aborted {
				capturedBeforeAbort = true
			}
			return stubUnmergedIndex("foo.go"), nil
		case joined == "cat-file blob "+stubOursSHA:
			return []byte("ours-content"), nil
		case joined == "cat-file blob "+stubTheirsSHA:
			return []byte("theirs-content"), nil
		case joined == "rebase --abort":
			aborted = true
			rebasing = false
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

	// conflict-context-77.json must have been written, naming the REAL branch.
	ctxPath := filepath.Join(ws, ".nightgauge", "pipeline", "conflict-context-77.json")
	ctxData, err := os.ReadFile(ctxPath)
	if err != nil {
		t.Fatalf("expected conflict-context-77.json written, read err: %v", err)
	}
	var ctxDoc map[string]interface{}
	if err := json.Unmarshal(ctxData, &ctxDoc); err != nil {
		t.Fatalf("parse conflict context: %v", err)
	}
	if ctxDoc["branch"] != "feat/77-thing" {
		t.Errorf("conflict-context branch = %v, want %q", ctxDoc["branch"], "feat/77-thing")
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

// TestAction_BranchOutOfDate_ConflictProbeFails is the #301 regression for the
// literal defect in the issue: the unmerged-path enumeration (`git ls-files -u
// -z`) errors, so the conflicting files are UNKNOWN — which is not the same
// thing as "there are none". A failed probe must not produce a context file that
// reads as a successful capture, must not emit CONFLICT_RESOLUTION_NEEDED, and
// above all must not run `git rebase --abort`: that permanently destroys the
// :2:/:3: index blobs, which are the only surviving copy of the evidence.
//
// Nothing was enumerated, so there is nothing the evidence dump could copy out
// either — this is the one shape that still legitimately leaves the rebase live.
//
// The real-git tests cover the states git produces on its own; this one covers
// the probe itself failing, which needs a stub to provoke.
func TestAction_BranchOutOfDate_ConflictProbeFails(t *testing.T) {
	ws := t.TempDir()
	aborted := false
	rebasing := false
	stubExecGit(t, func(_ context.Context, _ string, args ...string) ([]byte, error) {
		joined := strings.Join(args, " ")
		switch {
		case joined == "rebase origin/main":
			rebasing = true
			return []byte("CONFLICT (content): Merge conflict in foo.go"), errors.New("exit 1: rebase conflict")
		case joined == "rev-parse --abbrev-ref HEAD":
			if rebasing {
				return []byte("HEAD\n"), nil
			}
			return []byte("feat/77-thing\n"), nil
		case joined == "ls-files -u -z":
			return nil, errors.New("exit 128: fatal: not a git repository")
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

	if aborted {
		t.Error("a failed capture must NOT abort — the conflicted index is the only evidence left")
	}
	if _, err := os.Stat(filepath.Join(ws, ".nightgauge", "pipeline", "conflict-context-77.json")); err == nil {
		t.Error("a failed capture must not write a conflict context that reads as a successful one")
	}
	if _, err := os.Stat(filepath.Join(ws, ".nightgauge", "pipeline", "feedback-77.json")); err == nil {
		t.Error("a failed capture must not emit CONFLICT_RESOLUTION_NEEDED")
	}
	if res.FollowUp != FollowUpHumanTriageRequired {
		t.Errorf("FollowUp = %q, want %q — a preserved conflicted index must never be paired with a resumable stage", res.FollowUp, FollowUpHumanTriageRequired)
	}
	joinedEvidence := strings.Join(res.Evidence, " ")
	if !strings.Contains(joinedEvidence, "capture=failed") {
		t.Errorf("evidence must name the capture outcome, got %v", res.Evidence)
	}
	if !strings.Contains(joinedEvidence, "rebase_left_in_progress=true") {
		t.Errorf("evidence must tell the operator the worktree is intentionally mid-rebase, got %v", res.Evidence)
	}
}

// blobsUnreadableStub wires a rebase whose conflicting path is listed but whose
// index blobs cannot be read, so all that could be written is a name with empty
// ours/theirs — a context feature-dev can resolve nothing from. dumpOK decides
// whether the durable evidence dump succeeds, which is the ONLY thing that
// licenses the abort.
func blobsUnreadableStub(t *testing.T, dumpOK bool, aborted *bool) {
	t.Helper()
	rebasing := false
	stubExecGit(t, func(_ context.Context, _ string, args ...string) ([]byte, error) {
		joined := strings.Join(args, " ")
		switch {
		case joined == "rebase origin/main":
			rebasing = true
			return []byte("CONFLICT"), errors.New("exit 1: rebase conflict")
		case joined == "rev-parse --abbrev-ref HEAD":
			if rebasing {
				return []byte("HEAD\n"), nil
			}
			return []byte("feat/77-thing\n"), nil
		case joined == "ls-files -u -z":
			return stubUnmergedIndex("foo.go"), nil
		case strings.HasPrefix(joined, "cat-file blob "):
			return nil, errors.New("exit 128: path does not exist in the index")
		case joined == "rebase --abort":
			*aborted = true
			return []byte(""), nil
		}
		return []byte(""), nil
	})
	stubExecGitToFile(t, func(_ context.Context, _, dest string, _ ...string) error {
		if !dumpOK {
			return errors.New("exit 128: cannot read blob")
		}
		return os.WriteFile(dest, []byte("raw-index-bytes"), 0o644)
	})
}

// TestAction_BranchOutOfDate_ConflictBlobsUnreadable_NoEvidence: the capture
// failed AND the raw index could not be copied out either, so the in-index
// stages are the last copy of the conflict. Do not abort.
func TestAction_BranchOutOfDate_ConflictBlobsUnreadable_NoEvidence(t *testing.T) {
	ws := t.TempDir()
	aborted := false
	blobsUnreadableStub(t, false, &aborted)

	a := NewBranchOutOfDate(&fakePRMergeRunner{})
	res := a.Execute(context.Background(), StageFailure{
		Stage: state.StagePRMerge, GateKind: gates.KindNoOp, PRNumber: 1, IssueNumber: 77, Workspace: ws, Reason: "BEHIND",
	})

	if aborted {
		t.Error("nothing was preserved — aborting would leave zero record of the conflict")
	}
	if _, err := os.Stat(filepath.Join(ws, ".nightgauge", "pipeline", "conflict-context-77.json")); err == nil {
		t.Error("a name-only capture with no blobs must not be written as a successful capture")
	}
	if res.FollowUp != FollowUpHumanTriageRequired {
		t.Errorf("FollowUp = %q, want %q", res.FollowUp, FollowUpHumanTriageRequired)
	}
	joined := strings.Join(res.Evidence, " ")
	if !strings.Contains(joined, "evidence_preserved=false") || !strings.Contains(joined, "rebase_left_in_progress=true") {
		t.Errorf("evidence must state that nothing was preserved and the rebase is intentionally live, got %v", res.Evidence)
	}
}

// TestAction_BranchOutOfDate_ConflictBlobsUnreadable_EvidencePreserved is the
// inverse and the #301-review contract: once the raw index stages are copied out
// to conflict-evidence-{N}/, the abort is no longer destructive and MUST run.
// Leaving the worktree mid-rebase is not a safe alternative in this system — the
// scheduler's terminal defer stages the conflicted index away seconds later.
func TestAction_BranchOutOfDate_ConflictBlobsUnreadable_EvidencePreserved(t *testing.T) {
	ws := t.TempDir()
	aborted := false
	blobsUnreadableStub(t, true, &aborted)

	a := NewBranchOutOfDate(&fakePRMergeRunner{})
	res := a.Execute(context.Background(), StageFailure{
		Stage: state.StagePRMerge, GateKind: gates.KindNoOp, PRNumber: 1, IssueNumber: 77, Workspace: ws, Reason: "BEHIND",
	})

	if !aborted {
		t.Error("with the raw index preserved, the abort is non-destructive and must run")
	}
	if _, err := os.Stat(filepath.Join(ws, ".nightgauge", "pipeline", "conflict-context-77.json")); err == nil {
		t.Error("a failed capture must still not write a conflict context")
	}
	if _, err := os.Stat(filepath.Join(ws, ".nightgauge", "pipeline", "feedback-77.json")); err == nil {
		t.Error("a failed capture must not emit CONFLICT_RESOLUTION_NEEDED")
	}
	if res.FollowUp != FollowUpHumanTriageRequired {
		t.Errorf("FollowUp = %q, want %q — preserved evidence is still not a resolvable conflict", res.FollowUp, FollowUpHumanTriageRequired)
	}
	manifest := filepath.Join(ws, ".nightgauge", "pipeline", "conflict-evidence-77", "manifest.json")
	if _, err := os.Stat(manifest); err != nil {
		t.Errorf("expected a durable evidence manifest at %s: %v", manifest, err)
	}
	if !strings.Contains(strings.Join(res.Evidence, " "), "evidence_preserved=true") {
		t.Errorf("evidence must point the operator at the preserved dump, got %v", res.Evidence)
	}
}

// TestAction_BranchOutOfDate_PreexistingRebaseRefused: a rebase already in
// progress belongs to whoever started it. This action must not rebase over it
// and must not abort it — the abort in the no-conflict-state branch was
// documented as a "harmless no-op" and was not (#301 review). Stubbed here for
// the refusal's effect on the git call sequence; the real-git test proves the
// operator's staged work survives.
func TestAction_BranchOutOfDate_PreexistingRebaseRefused(t *testing.T) {
	ws := t.TempDir()
	if err := os.MkdirAll(filepath.Join(ws, ".git", "rebase-merge"), 0o755); err != nil {
		t.Fatal(err)
	}
	var calls []string
	stubExecGit(t, func(_ context.Context, _ string, args ...string) ([]byte, error) {
		joined := strings.Join(args, " ")
		calls = append(calls, joined)
		if joined == "rev-parse --git-path rebase-merge" {
			return []byte(".git/rebase-merge\n"), nil
		}
		return []byte(""), nil
	})

	a := NewBranchOutOfDate(&fakePRMergeRunner{})
	res := a.Execute(context.Background(), StageFailure{
		Stage: state.StagePRMerge, GateKind: gates.KindNoOp, PRNumber: 1, IssueNumber: 77, Workspace: ws, Reason: "BEHIND",
	})

	if res.FollowUp != FollowUpHumanTriageRequired {
		t.Errorf("FollowUp = %q, want %q", res.FollowUp, FollowUpHumanTriageRequired)
	}
	for _, c := range calls {
		if c == "rebase --abort" || c == "rebase origin/main" || strings.HasPrefix(c, "push") {
			t.Errorf("must not mutate a worktree holding someone else's rebase; ran %q (all: %v)", c, calls)
		}
	}
	if !strings.Contains(strings.Join(res.Evidence, " "), "preexisting_rebase=true") {
		t.Errorf("evidence must name the pre-existing rebase, got %v", res.Evidence)
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
