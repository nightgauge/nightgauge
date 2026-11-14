package recovery

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/nightgauge/nightgauge/internal/config"
	"github.com/nightgauge/nightgauge/internal/orchestrator/gates"
	pmstages "github.com/nightgauge/nightgauge/internal/orchestrator/stages"
	"github.com/nightgauge/nightgauge/internal/state"

	"github.com/nightgauge/nightgauge/internal/layout/layouttest"
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

// Stub index-stage blob ids, named by git's STAGE NUMBER rather than by
// ours/theirs: which stage is whose side depends on the operation (a rebase
// inverts them), and encoding the answer in the fixture's names is how the
// inversion hid (#301).
const (
	stubStage1SHA = "1111111111111111111111111111111111111111"
	stubStage2SHA = "2222222222222222222222222222222222222222"
	stubStage3SHA = "3333333333333333333333333333333333333333"
	// Under a rebase, stage 2 is the upstream and stage 3 the replayed commit.
	stubStage2Content = "base-side-content"
	stubStage3Content = "feature-side-content"
)

// stubUnmergedIndex renders `git ls-files -u -z` output for one conflicting
// path with all three stages: NUL-terminated records of "<mode> <sha> <stage>\t<path>".
func stubUnmergedIndex(path string) []byte {
	return []byte(
		"100644 " + stubStage1SHA + " 1\t" + path + "\x00" +
			"100644 " + stubStage2SHA + " 2\t" + path + "\x00" +
			"100644 " + stubStage3SHA + " 3\t" + path + "\x00")
}

// stubCatFile answers the `cat-file -s` / `cat-file blob` pair for the stub
// index stages. The size query is first and is not optional: the capture bounds
// a blob before reading it, so a stub that answers only `blob` makes every path
// unrepresentable.
func stubCatFile(joined string) ([]byte, bool) {
	for sha, content := range map[string]string{
		stubStage1SHA: "base-content",
		stubStage2SHA: stubStage2Content,
		stubStage3SHA: stubStage3Content,
	} {
		switch joined {
		case "cat-file -s " + sha:
			return []byte(strconv.Itoa(len(content)) + "\n"), true
		case "cat-file blob " + sha:
			return []byte(content), true
		}
	}
	return nil, false
}

// greenChecksGh stubs execGh so the updated PR's check rollup reads all-green.
func greenChecksGh(t *testing.T) {
	t.Helper()
	stubExecGh(t, func(_ context.Context, _ ...string) ([]byte, error) {
		return []byte(`{"statusCheckRollup":[{"name":"build","conclusion":"SUCCESS"},{"name":"test","conclusion":"SUCCESS"}]}`), nil
	})
}

// cleanMergeGit stubs execGit for a clean base merge on an attached branch.
func cleanMergeGit(t *testing.T) {
	t.Helper()
	stubExecGit(t, func(_ context.Context, _ string, args ...string) ([]byte, error) {
		if strings.Join(args, " ") == "rev-parse --abbrev-ref HEAD" {
			return []byte("feat/100-x\n"), nil
		}
		return []byte(""), nil
	})
}

// newBranchOOD builds the action with a fast poll cadence for tests.
func newBranchOOD(runner pmstages.PRMergeRunner) *BranchOutOfDate {
	a := NewBranchOutOfDate(runner)
	a.pollInterval = time.Millisecond
	a.pollMax = 3
	return a
}

// TestAction_BranchOutOfDate_Matches_AndRecovers is the happy path: merge the base +
// CI-green + runner returns PathMerged → Recovered=true.
func TestAction_BranchOutOfDate_Matches_AndRecovers(t *testing.T) {
	calls := []string{}
	stubExecGit(t, func(_ context.Context, _ string, args ...string) ([]byte, error) {
		joined := strings.Join(args, " ")
		calls = append(calls, joined)
		if joined == "rev-parse --abbrev-ref HEAD" {
			return []byte("feat/100-x\n"), nil
		}
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
	// Order matters and every entry is load-bearing (#301, #2752):
	//
	//  1-3. the pre-existing rebase/merge probes, BEFORE any mutation — an
	//       operation this run did not start must never be merged over or
	//       aborted, and after `git merge` runs the answer is not attributable.
	//    4. the branch the plain push targets.
	//  5-7. fetch the base, merge it, push without force. No rebase, ever.
	want := []string{
		"rev-parse --git-path rebase-merge",
		"rev-parse --git-path rebase-apply",
		"rev-parse --git-path MERGE_HEAD",
		"rev-parse --abbrev-ref HEAD",
		"fetch origin main",
		"merge --no-edit origin/main",
		"push origin HEAD:refs/heads/feat/100-x",
	}
	if len(calls) != len(want) {
		t.Fatalf("expected %d git calls, got %d (%v)", len(want), len(calls), calls)
	}
	for i, w := range want {
		if calls[i] != w {
			t.Errorf("step %d = %q, want %q", i, calls[i], w)
		}
	}
	for _, c := range calls {
		if strings.Contains(c, "--force") || (strings.Contains(c, "rebase") && !strings.HasPrefix(c, "rev-parse --git-path rebase-")) {
			t.Errorf("an existing PR branch must never be rebased or force-pushed; ran %q", c)
		}
	}
}

// TestAction_BranchOutOfDate_MergesPRBaseRef is the #2843 half of #2752: the
// base is the PR's baseRefName, so an epic-branch PR merges its epic branch,
// not main.
func TestAction_BranchOutOfDate_MergesPRBaseRef(t *testing.T) {
	var calls []string
	stubExecGit(t, func(_ context.Context, _ string, args ...string) ([]byte, error) {
		joined := strings.Join(args, " ")
		calls = append(calls, joined)
		if joined == "rev-parse --abbrev-ref HEAD" {
			return []byte("feat/43-y\n"), nil
		}
		return []byte(""), nil
	})
	stubExecGh(t, func(_ context.Context, args ...string) ([]byte, error) {
		if strings.Contains(strings.Join(args, " "), "baseRefName") {
			return []byte(`{"baseRefName":"epic/42-x"}`), nil
		}
		return []byte(`{"statusCheckRollup":[{"name":"build","conclusion":"SUCCESS"}]}`), nil
	})
	runner := &fakePRMergeRunner{res: pmstages.PRMergeResult{Path: pmstages.PathMerged, PRNumber: 7}}
	res := newBranchOOD(runner).Execute(context.Background(), StageFailure{
		Stage: state.StagePRMerge, GateKind: gates.KindNoOp, PRNumber: 7, Workspace: t.TempDir(), Reason: "BEHIND",
	})
	if !res.Recovered {
		t.Fatalf("expected Recovered=true; got %q", res.Reason)
	}
	joined := strings.Join(calls, "\n")
	for _, w := range []string{"fetch origin epic/42-x", "merge --no-edit origin/epic/42-x"} {
		if !strings.Contains(joined, w) {
			t.Errorf("expected %q in git calls, got %v", w, calls)
		}
	}
	if strings.Contains(joined, "origin/main") || strings.Contains(joined, "fetch origin main") {
		t.Errorf("an epic-base PR must not be updated from main, got %v", calls)
	}
	if !strings.Contains(strings.Join(res.Evidence, " "), "base=epic/42-x") {
		t.Errorf("evidence must name the base, got %v", res.Evidence)
	}
}

// TestPRBaseRef_DefaultsToMain: an unreadable or empty baseRefName falls back
// to main.
func TestPRBaseRef_DefaultsToMain(t *testing.T) {
	for name, gh := range map[string]func(context.Context, ...string) ([]byte, error){
		"error": func(context.Context, ...string) ([]byte, error) { return nil, errors.New("gh down") },
		"empty": func(context.Context, ...string) ([]byte, error) { return []byte(`{"baseRefName":""}`), nil },
		"junk":  func(context.Context, ...string) ([]byte, error) { return []byte(`not json`), nil },
	} {
		t.Run(name, func(t *testing.T) {
			stubExecGh(t, gh)
			if got := prBaseRef(context.Background(), 1); got != "main" {
				t.Errorf("prBaseRef = %q, want main", got)
			}
		})
	}
}

// TestAction_BranchOutOfDate_DetachedHeadDoesNotPush: a clean merge on a
// detached HEAD names no branch to push to, so it escalates instead of
// guessing a ref.
func TestAction_BranchOutOfDate_DetachedHeadDoesNotPush(t *testing.T) {
	var calls []string
	stubExecGit(t, func(_ context.Context, _ string, args ...string) ([]byte, error) {
		joined := strings.Join(args, " ")
		calls = append(calls, joined)
		if joined == "rev-parse --abbrev-ref HEAD" {
			return []byte("HEAD\n"), nil
		}
		return []byte(""), nil
	})
	greenChecksGh(t)
	res := newBranchOOD(&fakePRMergeRunner{}).Execute(context.Background(), StageFailure{
		Stage: state.StagePRMerge, GateKind: gates.KindNoOp, PRNumber: 1, Workspace: t.TempDir(), Reason: "BEHIND",
	})
	if res.FollowUp != FollowUpHumanTriageRequired {
		t.Errorf("FollowUp = %q, want triage", res.FollowUp)
	}
	for _, c := range calls {
		if strings.HasPrefix(c, "push") {
			t.Errorf("must not push from a detached HEAD; ran %q", c)
		}
	}
}

// TestAction_BranchOutOfDate_MergedButRunnerPunts asserts that a successful
// base merge + green CI is NOT enough — if the runner punts (PR not actually
// merged) the action declines and routes to triage instead of advancing.
func TestAction_BranchOutOfDate_MergedButRunnerPunts(t *testing.T) {
	cleanMergeGit(t)
	greenChecksGh(t)

	runner := &fakePRMergeRunner{
		res: pmstages.PRMergeResult{Path: pmstages.PathPunt, PRNumber: 100, Reason: pmstages.ReasonReviewMissing + ": REVIEW_REQUIRED"},
	}
	a := newBranchOOD(runner)
	res := a.Execute(context.Background(), StageFailure{
		Stage: state.StagePRMerge, GateKind: gates.KindNoOp, PRNumber: 100, Workspace: "/tmp", Reason: "BEHIND",
	})
	if res.Recovered {
		t.Error("expected Recovered=false when runner punts after the base merge")
	}
	if res.FollowUp != FollowUpHumanTriageRequired {
		t.Errorf("FollowUp = %q, want triage", res.FollowUp)
	}
}

// TestAction_BranchOutOfDate_CINotGreenAfterMerge asserts that a failing check
// on the updated head blocks recovery (no merge attempted).
func TestAction_BranchOutOfDate_CINotGreenAfterMerge(t *testing.T) {
	cleanMergeGit(t)
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
		t.Error("expected Recovered=false when CI is red on updated head")
	}
	if res.FollowUp != FollowUpHumanTriageRequired {
		t.Errorf("FollowUp = %q, want triage", res.FollowUp)
	}
}

// TestAction_BranchOutOfDate_CINeverCompletes asserts that an in-flight head
// that never completes within the poll budget declines without claiming
// recovery (no runner merge).
func TestAction_BranchOutOfDate_CINeverCompletes(t *testing.T) {
	cleanMergeGit(t)
	stubExecGh(t, func(_ context.Context, _ ...string) ([]byte, error) {
		return []byte(`{"statusCheckRollup":[{"__typename":"CheckRun","name":"test","status":"IN_PROGRESS","conclusion":""}]}`), nil
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
	if !strings.Contains(res.Reason, "still pending: test") {
		t.Errorf("Reason = %q, must name the pending check", res.Reason)
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

// TestAction_BranchOutOfDate_MergeConflict locks the #4072 hand-off under the
// merge-the-base update (#2752): a conflicting merge captures the conflict
// context (files + both sides) BEFORE `git merge --abort`, emits a
// CONFLICT_RESOLUTION_NEEDED feedback signal, and returns
// FollowUpStageCanResume so the conflict-recovery loop rewinds to feature-dev —
// instead of escalating straight to human triage. The capture MUST happen
// before the abort.
func TestAction_BranchOutOfDate_MergeConflict(t *testing.T) {
	ws := layouttest.Repo(t)
	aborted := false
	capturedBeforeAbort := false
	// A REAL MERGE_HEAD, created and removed in step with the stubbed merge. The
	// pre-flight guard and the operation detection (which decides the
	// stage→side mapping) read this state, so a stub that leaves it absent
	// describes a merge git never performs.
	mergeHead := filepath.Join(ws, ".git", "MERGE_HEAD")
	stubExecGit(t, func(_ context.Context, _ string, args ...string) ([]byte, error) {
		joined := strings.Join(args, " ")
		if out, ok := stubCatFile(joined); ok {
			return out, nil
		}
		switch {
		case joined == "merge --no-edit origin/main":
			if err := os.WriteFile(mergeHead, []byte(stubStage3SHA+"\n"), 0o644); err != nil {
				return nil, err
			}
			return []byte("CONFLICT (content): Merge conflict in foo.go"), errors.New("exit 1: merge conflict")
		case joined == "rev-parse --git-path MERGE_HEAD":
			return []byte(mergeHead + "\n"), nil
		case joined == "rev-parse --abbrev-ref HEAD":
			// HEAD stays attached during a merge.
			return []byte("feat/77-thing\n"), nil
		case joined == "ls-files -u -z":
			if !aborted {
				capturedBeforeAbort = true
			}
			return stubUnmergedIndex("foo.go"), nil
		case joined == "merge --abort":
			aborted = true
			_ = os.Remove(mergeHead)
			return []byte(""), nil
		case strings.HasPrefix(joined, "rebase"), strings.HasPrefix(joined, "push"):
			t.Errorf("a conflicting base merge must not rebase or push; ran %q", joined)
		}
		return []byte(""), nil
	})

	a := NewBranchOutOfDate(&fakePRMergeRunner{})
	res := a.Execute(context.Background(), StageFailure{
		Stage: state.StagePRMerge, GateKind: gates.KindNoOp, PRNumber: 1, IssueNumber: 77, Workspace: ws, Reason: "BEHIND",
	})
	if res.Recovered {
		t.Error("expected no in-place recovery on merge conflict (LLM dev stage resolves)")
	}
	if !aborted {
		t.Error("expected merge --abort to run after conflict")
	}
	if !capturedBeforeAbort {
		t.Error("conflict context must be captured BEFORE merge --abort wipes the conflict state")
	}
	if res.FollowUp != FollowUpStageCanResume {
		t.Errorf("FollowUp = %q, want stage-can-resume (defer to conflict-recovery)", res.FollowUp)
	}

	// conflict-context-77.json must have been written, naming the REAL branch.
	ctxPath := filepath.Join(layouttest.PipelineDir(t, ws), "conflict-context-77.json")
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
	// ours = the PR branch's work, theirs = the base, which under a merge are
	// git's own stage 2 and stage 3.
	if ctxDoc["conflict_operation"] != "merge" {
		t.Errorf("conflict_operation = %v, want \"merge\"", ctxDoc["conflict_operation"])
	}
	entries, _ := ctxDoc["conflicting_files"].([]interface{})
	if len(entries) != 1 {
		t.Fatalf("conflicting_files = %v, want one entry", ctxDoc["conflicting_files"])
	}
	entry, _ := entries[0].(map[string]interface{})
	if entry["ours"] != stubStage2Content {
		t.Errorf("ours = %v, want %q (index stage 2 — the checked-out PR branch)", entry["ours"], stubStage2Content)
	}
	if entry["theirs"] != stubStage3Content {
		t.Errorf("theirs = %v, want %q (index stage 3 — the merged base)", entry["theirs"], stubStage3Content)
	}
	// feedback-77.json must carry a CONFLICT_RESOLUTION_NEEDED signal.
	fbData, err := os.ReadFile(filepath.Join(layouttest.PipelineDir(t, ws), "feedback-77.json"))
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
// above all must not run `git merge --abort`: that permanently destroys the
// :2:/:3: index blobs, which are the only surviving copy of the evidence.
//
// Nothing was enumerated, so there is nothing the evidence dump could copy out
// either — this is the one shape that still legitimately leaves the merge live.
//
// The real-git tests cover the states git produces on its own; this one covers
// the probe itself failing, which needs a stub to provoke.
func TestAction_BranchOutOfDate_ConflictProbeFails(t *testing.T) {
	ws := layouttest.Repo(t)
	aborted := false
	stubExecGit(t, func(_ context.Context, _ string, args ...string) ([]byte, error) {
		joined := strings.Join(args, " ")
		switch {
		case joined == "merge --no-edit origin/main":
			return []byte("CONFLICT (content): Merge conflict in foo.go"), errors.New("exit 1: merge conflict")
		case joined == "rev-parse --abbrev-ref HEAD":
			return []byte("feat/77-thing\n"), nil
		case joined == "ls-files -u -z":
			return nil, errors.New("exit 128: fatal: not a git repository")
		case joined == "merge --abort":
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
	if _, err := os.Stat(filepath.Join(layouttest.PipelineDir(t, ws), "conflict-context-77.json")); err == nil {
		t.Error("a failed capture must not write a conflict context that reads as a successful one")
	}
	if _, err := os.Stat(filepath.Join(layouttest.PipelineDir(t, ws), "feedback-77.json")); err == nil {
		t.Error("a failed capture must not emit CONFLICT_RESOLUTION_NEEDED")
	}
	if res.FollowUp != FollowUpHumanTriageRequired {
		t.Errorf("FollowUp = %q, want %q — a preserved conflicted index must never be paired with a resumable stage", res.FollowUp, FollowUpHumanTriageRequired)
	}
	joinedEvidence := strings.Join(res.Evidence, " ")
	if !strings.Contains(joinedEvidence, "capture=failed") {
		t.Errorf("evidence must name the capture outcome, got %v", res.Evidence)
	}
	if !strings.Contains(joinedEvidence, "merge_left_in_progress=true") {
		t.Errorf("evidence must tell the operator the worktree is intentionally mid-merge, got %v", res.Evidence)
	}
}

// blobsUnreadableStub wires a base merge whose conflicting path is listed but whose
// index blobs cannot be read, so all that could be written is a name with empty
// ours/theirs — a context feature-dev can resolve nothing from. dumpOK decides
// whether the durable evidence dump succeeds, which is the ONLY thing that
// licenses the abort.
func blobsUnreadableStub(t *testing.T, dumpOK bool, aborted *bool) {
	t.Helper()
	stubExecGit(t, func(_ context.Context, _ string, args ...string) ([]byte, error) {
		joined := strings.Join(args, " ")
		switch {
		case joined == "merge --no-edit origin/main":
			return []byte("CONFLICT"), errors.New("exit 1: merge conflict")
		case joined == "rev-parse --abbrev-ref HEAD":
			return []byte("feat/77-thing\n"), nil
		case joined == "ls-files -u -z":
			return stubUnmergedIndex("foo.go"), nil
		case strings.HasPrefix(joined, "cat-file blob "):
			return nil, errors.New("exit 128: path does not exist in the index")
		case joined == "merge --abort":
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
	ws := layouttest.Repo(t)
	aborted := false
	blobsUnreadableStub(t, false, &aborted)

	a := NewBranchOutOfDate(&fakePRMergeRunner{})
	res := a.Execute(context.Background(), StageFailure{
		Stage: state.StagePRMerge, GateKind: gates.KindNoOp, PRNumber: 1, IssueNumber: 77, Workspace: ws, Reason: "BEHIND",
	})

	if aborted {
		t.Error("nothing was preserved — aborting would leave zero record of the conflict")
	}
	if _, err := os.Stat(filepath.Join(layouttest.PipelineDir(t, ws), "conflict-context-77.json")); err == nil {
		t.Error("a name-only capture with no blobs must not be written as a successful capture")
	}
	if res.FollowUp != FollowUpHumanTriageRequired {
		t.Errorf("FollowUp = %q, want %q", res.FollowUp, FollowUpHumanTriageRequired)
	}
	joined := strings.Join(res.Evidence, " ")
	if !strings.Contains(joined, "evidence_preserved=false") || !strings.Contains(joined, "merge_left_in_progress=true") {
		t.Errorf("evidence must state that nothing was preserved and the merge is intentionally live, got %v", res.Evidence)
	}
}

// TestAction_BranchOutOfDate_ConflictBlobsUnreadable_EvidencePreserved is the
// inverse and the #301-review contract: once the raw index stages are copied out
// to conflict-evidence-{N}/, the abort is no longer destructive and MUST run.
// Leaving the worktree mid-merge is not a safe alternative in this system — the
// scheduler's terminal defer stages the conflicted index away seconds later.
func TestAction_BranchOutOfDate_ConflictBlobsUnreadable_EvidencePreserved(t *testing.T) {
	ws := layouttest.Repo(t)
	aborted := false
	blobsUnreadableStub(t, true, &aborted)

	a := NewBranchOutOfDate(&fakePRMergeRunner{})
	res := a.Execute(context.Background(), StageFailure{
		Stage: state.StagePRMerge, GateKind: gates.KindNoOp, PRNumber: 1, IssueNumber: 77, Workspace: ws, Reason: "BEHIND",
	})

	if !aborted {
		t.Error("with the raw index preserved, the abort is non-destructive and must run")
	}
	if _, err := os.Stat(filepath.Join(layouttest.PipelineDir(t, ws), "conflict-context-77.json")); err == nil {
		t.Error("a failed capture must still not write a conflict context")
	}
	if _, err := os.Stat(filepath.Join(layouttest.PipelineDir(t, ws), "feedback-77.json")); err == nil {
		t.Error("a failed capture must not emit CONFLICT_RESOLUTION_NEEDED")
	}
	if res.FollowUp != FollowUpHumanTriageRequired {
		t.Errorf("FollowUp = %q, want %q — preserved evidence is still not a resolvable conflict", res.FollowUp, FollowUpHumanTriageRequired)
	}
	manifest := filepath.Join(layouttest.PipelineDir(t, ws), "conflict-evidence-77", "manifest.json")
	if _, err := os.Stat(manifest); err != nil {
		t.Errorf("expected a durable evidence manifest at %s: %v", manifest, err)
	}
	if !strings.Contains(strings.Join(res.Evidence, " "), "evidence_preserved=true") {
		t.Errorf("evidence must point the operator at the preserved dump, got %v", res.Evidence)
	}
}

// TestAction_BranchOutOfDate_PreexistingRebaseRefused: a rebase already in
// progress belongs to whoever started it. This action must not merge over it
// and must not abort it — the abort in the no-conflict-state branch was
// documented as a "harmless no-op" and was not (#301 review). Stubbed here for
// the refusal's effect on the git call sequence; the real-git test proves the
// operator's staged work survives.
func TestAction_BranchOutOfDate_PreexistingRebaseRefused(t *testing.T) {
	ws := layouttest.Repo(t)
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
		if strings.HasPrefix(c, "rebase") || strings.HasPrefix(c, "merge") || strings.HasPrefix(c, "fetch") || strings.HasPrefix(c, "push") {
			t.Errorf("must not mutate a worktree holding someone else's rebase; ran %q (all: %v)", c, calls)
		}
	}
	if !strings.Contains(strings.Join(res.Evidence, " "), "preexisting_rebase=true") {
		t.Errorf("evidence must name the pre-existing rebase, got %v", res.Evidence)
	}
}

// TestAction_BranchOutOfDate_PreexistingMergeRefused: a merge already in
// progress (MERGE_HEAD present) belongs to whoever started it. The action must
// not merge over it, abort it, fetch or push (#2752).
func TestAction_BranchOutOfDate_PreexistingMergeRefused(t *testing.T) {
	ws := layouttest.Repo(t)
	mergeHead := filepath.Join(ws, ".git", "MERGE_HEAD")
	if err := os.WriteFile(mergeHead, []byte(stubStage3SHA+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	var calls []string
	stubExecGit(t, func(_ context.Context, _ string, args ...string) ([]byte, error) {
		joined := strings.Join(args, " ")
		calls = append(calls, joined)
		if joined == "rev-parse --git-path MERGE_HEAD" {
			return []byte(".git/MERGE_HEAD\n"), nil
		}
		return []byte(""), nil
	})

	res := NewBranchOutOfDate(&fakePRMergeRunner{}).Execute(context.Background(), StageFailure{
		Stage: state.StagePRMerge, GateKind: gates.KindNoOp, PRNumber: 1, IssueNumber: 77, Workspace: ws, Reason: "BEHIND",
	})

	if res.FollowUp != FollowUpHumanTriageRequired {
		t.Errorf("FollowUp = %q, want %q", res.FollowUp, FollowUpHumanTriageRequired)
	}
	for _, c := range calls {
		if strings.HasPrefix(c, "merge") || strings.HasPrefix(c, "fetch") || strings.HasPrefix(c, "push") {
			t.Errorf("must not mutate a worktree holding someone else's merge; ran %q (all: %v)", c, calls)
		}
	}
	if _, err := os.Stat(mergeHead); err != nil {
		t.Errorf("the operator's MERGE_HEAD must survive: %v", err)
	}
	if !strings.Contains(strings.Join(res.Evidence, " "), "preexisting_merge=true") {
		t.Errorf("evidence must name the pre-existing merge, got %v", res.Evidence)
	}
}

// TestAction_BranchOutOfDate_FetchFailStillTriages confirms a non-merge step
// failure (e.g. fetch) still escalates to human triage — only the merge-
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
// statusCheckRollup (common right after a push, before the new head's
// runs register) must be treated as PENDING, not green — otherwise waitForCI
// short-circuits and re-runs the merge before the new head is validated.
func TestSummarizeChecks_EmptyIsPending(t *testing.T) {
	if got, _ := summarizeChecks(nil); got != checksPending {
		t.Errorf("nil rollup must be pending, got %v", got)
	}
	if got, _ := summarizeChecks([]statusCheckRollupEntry{}); got != checksPending {
		t.Errorf("empty rollup must be pending, got %v", got)
	}
}

// TestSummarizeChecks_CommitStatusState locks #3026: a commit status
// (StatusContext) is judged by its state, not by its absent conclusion.
func TestSummarizeChecks_CommitStatusState(t *testing.T) {
	green := `{"__typename":"CheckRun","name":"a","status":"COMPLETED","conclusion":"SUCCESS"},` +
		`{"__typename":"CheckRun","name":"b","status":"COMPLETED","conclusion":"SUCCESS"},`
	cases := []struct {
		name        string
		row         string
		want        checksState
		wantPending []string
	}{
		{"success", `{"__typename":"StatusContext","context":"cla","state":"SUCCESS"}`, checksGreen, nil},
		{"pending", `{"__typename":"StatusContext","context":"cla","state":"PENDING"}`, checksPending, []string{"cla"}},
		{"expected", `{"__typename":"StatusContext","context":"cla","state":"EXPECTED"}`, checksPending, []string{"cla"}},
		{"failure", `{"__typename":"StatusContext","context":"cla","state":"FAILURE"}`, checksFailed, nil},
		{"error", `{"__typename":"StatusContext","context":"cla","state":"ERROR"}`, checksFailed, nil},
		{"no typename success", `{"context":"cla","state":"SUCCESS"}`, checksGreen, nil},
		{"no typename pending", `{"context":"cla","state":"PENDING"}`, checksPending, []string{"cla"}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			var snap prChecksSnapshot
			if err := json.Unmarshal([]byte(`{"statusCheckRollup":[`+green+c.row+`]}`), &snap); err != nil {
				t.Fatal(err)
			}
			got, pending := summarizeChecks(snap.StatusCheckRollup)
			if got != c.want {
				t.Errorf("state = %v, want %v", got, c.want)
			}
			if strings.Join(pending, ",") != strings.Join(c.wantPending, ",") {
				t.Errorf("pending = %v, want %v", pending, c.wantPending)
			}
		})
	}
}

// pr3016Rollup replays PR #3016's rollup: 27 green check runs plus the `cla`
// commit status in state SUCCESS.
func pr3016Rollup() string {
	rows := make([]string, 0, 28)
	for i := 0; i < 27; i++ {
		rows = append(rows, fmt.Sprintf(`{"__typename":"CheckRun","name":"check-%d","status":"COMPLETED","conclusion":"SUCCESS"}`, i))
	}
	rows = append(rows, `{"__typename":"StatusContext","context":"cla","state":"SUCCESS"}`)
	return `{"statusCheckRollup":[` + strings.Join(rows, ",") + `]}`
}

// TestBranchOutOfDate_PR3016RollupGreenFirstPoll asserts the #3016 rollup is
// green on the first poll (#3026).
func TestBranchOutOfDate_PR3016RollupGreenFirstPoll(t *testing.T) {
	polls := 0
	stubExecGh(t, func(_ context.Context, args ...string) ([]byte, error) {
		if len(args) > 1 && args[0] == "pr" && args[1] == "view" {
			polls++
		}
		return []byte(pr3016Rollup()), nil
	})
	a := newBranchOOD(&fakePRMergeRunner{})
	_, ok := a.waitForCI(context.Background(), StageFailure{PRNumber: 3016}, nil)
	if !ok {
		t.Fatal("waitForCI must succeed on the #3016 rollup")
	}
	if polls != 1 {
		t.Errorf("polls = %d, want 1", polls)
	}
}

// pendingThenGh stubs the check rollup: PENDING for pending polls, then final.
func pendingThenGh(t *testing.T, pending int, final string) *int {
	t.Helper()
	polls := 0
	stubExecGh(t, func(_ context.Context, args ...string) ([]byte, error) {
		if strings.Contains(strings.Join(args, " "), "baseRefName") {
			return []byte(`{"baseRefName":"main"}`), nil
		}
		polls++
		if polls <= pending {
			return []byte(`{"statusCheckRollup":[{"name":"build","status":"IN_PROGRESS","conclusion":""}]}`), nil
		}
		return []byte(`{"statusCheckRollup":[{"name":"build","status":"COMPLETED","conclusion":"` + final + `"}]}`), nil
	})
	return &polls
}

func behindFailure() StageFailure {
	return StageFailure{
		Stage: state.StagePRMerge, GateKind: gates.KindNoOp, PRNumber: 100,
		Workspace: "/tmp/work", Reason: "dirty-merge-state: BEHIND",
	}
}

// TestBranchOutOfDate_WaitsOutSlowCI replays #2977: CI on the updated head stays
// in flight past the old ten-poll budget, then passes, and the PR merges.
func TestBranchOutOfDate_WaitsOutSlowCI(t *testing.T) {
	cleanMergeGit(t)
	polls := pendingThenGh(t, 12, "SUCCESS")
	runner := &fakePRMergeRunner{res: pmstages.PRMergeResult{
		Path: pmstages.PathMerged, PRNumber: 100, PRState: "MERGED", Reason: pmstages.ReasonCleanMerged,
	}}
	a := NewBranchOutOfDate(runner).withPolling(time.Millisecond, 20*time.Millisecond)
	res := a.Execute(context.Background(), behindFailure())
	if !res.Recovered {
		t.Fatalf("Recovered=false after %d polls: %q", *polls, res.Reason)
	}
	if *polls <= 10 {
		t.Fatalf("polls = %d, want more than the old ten-poll budget", *polls)
	}
}

// TestBranchOutOfDate_SlowCIThenFailureDeclines: a failing check after a long
// pending run still declines to triage without merging.
func TestBranchOutOfDate_SlowCIThenFailureDeclines(t *testing.T) {
	cleanMergeGit(t)
	pendingThenGh(t, 12, "FAILURE")
	runner := &countingPRMergeRunner{}
	a := NewBranchOutOfDate(runner).withPolling(time.Millisecond, 20*time.Millisecond)
	res := a.Execute(context.Background(), behindFailure())
	if res.Recovered || res.FollowUp != FollowUpHumanTriageRequired {
		t.Fatalf("got Recovered=%v FollowUp=%q, want declined to triage", res.Recovered, res.FollowUp)
	}
	if !strings.Contains(res.Reason, "CI failed on updated head") {
		t.Errorf("Reason = %q, want a CI failure", res.Reason)
	}
	if runner.calls != 0 {
		t.Errorf("runner called %d times, want 0", runner.calls)
	}
}

// TestBranchOutOfDate_DefaultBudgetMatchesPRMergeWait pins the default budget to
// the ordinary pr-merge CI wait rather than the old five minutes.
func TestBranchOutOfDate_DefaultBudgetMatchesPRMergeWait(t *testing.T) {
	a := NewBranchOutOfDate(&fakePRMergeRunner{})
	if a.pollInterval != 30*time.Second || a.pollMax != 40 {
		t.Fatalf("default = %d polls x %v, want 40 x 30s", a.pollMax, a.pollInterval)
	}
	a.WithCIWait((&config.Config{}).ResolvePRMergeCIWait())
	if got := time.Duration(a.pollMax) * a.pollInterval; got != 100*time.Minute {
		t.Fatalf("resolved budget = %v, want 100m", got)
	}
}

type countingPRMergeRunner struct{ calls int }

func (c *countingPRMergeRunner) Run(context.Context, int, string, string) (pmstages.PRMergeResult, error) {
	c.calls++
	return pmstages.PRMergeResult{}, nil
}

// TestBranchOutOfDate_YieldsMergeLockDuringCIWait: the wait releases the merge
// lock while checks are pending and re-takes it before merging (#2980).
func TestBranchOutOfDate_YieldsMergeLockDuringCIWait(t *testing.T) {
	cleanMergeGit(t)
	held := true
	heldDuringPoll := false
	polls := 0
	stubExecGh(t, func(_ context.Context, args ...string) ([]byte, error) {
		if strings.Contains(strings.Join(args, " "), "baseRefName") {
			return []byte(`{"baseRefName":"main"}`), nil // read before the wait, under the lock
		}
		polls++
		if held {
			heldDuringPoll = true
		}
		if polls <= 2 {
			return []byte(`{"statusCheckRollup":[{"name":"build","status":"IN_PROGRESS","conclusion":""}]}`), nil
		}
		return []byte(`{"statusCheckRollup":[{"name":"build","status":"COMPLETED","conclusion":"SUCCESS"}]}`), nil
	})
	heldAtMerge := false
	runner := &lockCheckingRunner{held: &held, heldAtMerge: &heldAtMerge}
	f := behindFailure()
	f.YieldMergeLock = func() func() { held = false; return func() { held = true } }
	a := NewBranchOutOfDate(runner).withPolling(time.Millisecond, 20*time.Millisecond)
	if res := a.Execute(context.Background(), f); !res.Recovered {
		t.Fatalf("Recovered=false: %q", res.Reason)
	}
	if heldDuringPoll {
		t.Error("merge lock held while CI was polled")
	}
	if !heldAtMerge {
		t.Error("merge lock not re-taken before merge")
	}
}

type lockCheckingRunner struct{ held, heldAtMerge *bool }

func (r *lockCheckingRunner) Run(context.Context, int, string, string) (pmstages.PRMergeResult, error) {
	*r.heldAtMerge = *r.held
	return pmstages.PRMergeResult{Path: pmstages.PathMerged, PRNumber: 100, PRState: "MERGED", Reason: pmstages.ReasonCleanMerged}, nil
}
