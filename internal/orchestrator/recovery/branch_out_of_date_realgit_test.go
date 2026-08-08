package recovery

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/nightgauge/nightgauge/internal/orchestrator/gates"
	"github.com/nightgauge/nightgauge/internal/state"
)

// These tests run BranchOutOfDate.Execute against a REAL git repository built
// by scripts/capture-conflicted-rebase-fixture.sh — no execGit stub, no
// hand-authored conflict-context JSON. That matters for #301 specifically:
// both defects are consequences of what git actually does under a rebase, and
// both were invisible to the stubbed unit test because the stub answered
// `rev-parse --abbrev-ref HEAD` with a branch name, which real git never does
// mid-rebase (it detaches HEAD and answers "HEAD").
//
// Everything asserted here — the branch name, the conflicting path, the
// ours/theirs blobs, the "rebase refused, nothing conflicted" state — is
// produced by git at test time (#166 evidence rule).

const fixtureBranch = "feat/301-conflict-fixture"

// realGitFixture builds the fixture repo in mode and returns the working clone.
func realGitFixture(t *testing.T, mode string) string {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not on PATH")
	}
	if _, err := exec.LookPath("bash"); err != nil {
		t.Skip("bash not on PATH")
	}
	script, err := filepath.Abs(filepath.Join("..", "..", "..", "scripts", "capture-conflicted-rebase-fixture.sh"))
	if err != nil {
		t.Fatalf("resolve fixture script: %v", err)
	}
	dest := filepath.Join(t.TempDir(), "fixture")
	// bash is invoked with the script PATH plus positional args (no `-c` string),
	// so nothing here is interpreted as shell syntax; both args are test-local
	// constants regardless.
	out, err := exec.Command("bash", script, dest, mode).Output()
	if err != nil {
		stderr := ""
		var ee *exec.ExitError
		if errors.As(err, &ee) {
			stderr = string(ee.Stderr)
		}
		t.Fatalf("fixture script (%s) failed: %v\n%s", mode, err, stderr)
	}
	work := strings.TrimSpace(string(out))
	if work == "" {
		t.Fatalf("fixture script printed no workdir")
	}
	return work
}

func readConflictContext(t *testing.T, ws string, issue int) map[string]interface{} {
	t.Helper()
	data, err := os.ReadFile(conflictContextPathForTest(ws, issue))
	if err != nil {
		t.Fatalf("read conflict context: %v", err)
	}
	var doc map[string]interface{}
	if err := json.Unmarshal(data, &doc); err != nil {
		t.Fatalf("parse conflict context: %v", err)
	}
	return doc
}

func conflictContextPathForTest(ws string, issue int) string {
	return filepath.Join(ws, ".nightgauge", "pipeline", "conflict-context-"+strconv.Itoa(issue)+".json")
}

func feedbackPathForTest(ws string, issue int) string {
	return filepath.Join(ws, ".nightgauge", "pipeline", "feedback-"+strconv.Itoa(issue)+".json")
}

// hasConflictSignal reports whether feedback-{issue}.json carries a
// CONFLICT_RESOLUTION_NEEDED signal. A missing file is "no signal".
func hasConflictSignal(t *testing.T, ws string, issue int) bool {
	t.Helper()
	data, err := os.ReadFile(feedbackPathForTest(ws, issue))
	if err != nil {
		return false
	}
	var fb feedbackOnDisk
	if err := json.Unmarshal(data, &fb); err != nil {
		t.Fatalf("parse feedback: %v", err)
	}
	for _, s := range fb.Signals {
		if s.SignalType == conflictResolutionSignalType {
			return true
		}
	}
	return false
}

// rebaseInProgress reports whether the workspace is sitting mid-rebase.
func rebaseInProgress(t *testing.T, ws string) bool {
	t.Helper()
	out, err := exec.Command("git", "-C", ws, "rev-parse", "--git-path", "rebase-merge").Output()
	if err != nil {
		t.Fatalf("rev-parse --git-path: %v", err)
	}
	p := strings.TrimSpace(string(out))
	if !filepath.IsAbs(p) {
		p = filepath.Join(ws, p)
	}
	_, statErr := os.Stat(p)
	return statErr == nil
}

func prMergeConflictFailure(ws string, issue int) StageFailure {
	return StageFailure{
		Stage:       state.StagePRMerge,
		GateKind:    gates.KindNoOp,
		PRNumber:    1,
		IssueNumber: issue,
		Workspace:   ws,
		Reason:      "dirty-merge-state: BEHIND",
	}
}

// TestRealGit_RebaseConflict_RecordsRealBranch is the #301 defect-2 regression:
// git detaches HEAD for the duration of a rebase, so resolving the branch from
// inside the conflict handler reads "HEAD" and degrades to the "unknown"
// sentinel. feature-dev's conflict intake explicitly SKIPS the branch checkout
// when the context says "unknown", so an "unknown" context silently discards
// the same-branch guarantee the whole conflict-recovery loop exists to provide.
//
// The branch is knowable here — the fixture is on feat/301-conflict-fixture and
// git itself records refs/heads/feat/301-conflict-fixture in
// rebase-merge/head-name — so the context file must name it.
func TestRealGit_RebaseConflict_RecordsRealBranch(t *testing.T) {
	ws := realGitFixture(t, "conflict")

	a := NewBranchOutOfDate(&fakePRMergeRunner{})
	res := a.Execute(context.Background(), prMergeConflictFailure(ws, 301))

	doc := readConflictContext(t, ws, 301)
	if got := doc["branch"]; got != fixtureBranch {
		t.Errorf("conflict-context branch = %v, want %q (real branch under rebase)", got, fixtureBranch)
	}
	if !strings.Contains(strings.Join(res.Evidence, " "), "branch="+fixtureBranch) {
		t.Errorf("evidence should name the real branch, got %v", res.Evidence)
	}
	if strings.Contains(res.Reason, unknownBranch) {
		t.Errorf("reason must not fall back to the %q sentinel: %q", unknownBranch, res.Reason)
	}
}

// TestRealGit_RebaseConflict_CapturesThenAborts locks the capture-before-destroy
// ordering against real git: the ours/theirs blobs only resolve while the
// conflict is in the index, so a genuine capture must read them BEFORE
// `git rebase --abort`. Asserting on the blobs' CONTENT (not merely that a file
// was written) is what proves the ordering held — post-abort those `git show
// :2:` / `:3:` lookups fail and the fields would be empty.
func TestRealGit_RebaseConflict_CapturesThenAborts(t *testing.T) {
	ws := realGitFixture(t, "conflict")

	a := NewBranchOutOfDate(&fakePRMergeRunner{})
	res := a.Execute(context.Background(), prMergeConflictFailure(ws, 301))

	if res.Recovered {
		t.Error("a rebase conflict is not recovered in place — the dev stage resolves it")
	}
	if res.FollowUp != FollowUpStageCanResume {
		t.Errorf("FollowUp = %q, want %q on a captured conflict", res.FollowUp, FollowUpStageCanResume)
	}

	doc := readConflictContext(t, ws, 301)
	rawFiles, _ := doc["conflicting_files"].([]interface{})
	if len(rawFiles) != 1 {
		t.Fatalf("conflicting_files = %v, want exactly f.txt", doc["conflicting_files"])
	}
	entry, _ := rawFiles[0].(map[string]interface{})
	if entry["path"] != "f.txt" {
		t.Errorf("conflicting file path = %v, want f.txt", entry["path"])
	}
	ours, _ := entry["ours"].(string)
	theirs, _ := entry["theirs"].(string)
	if !strings.Contains(ours, "main-side") {
		t.Errorf("ours blob missing — captured after the abort? got %q", ours)
	}
	if !strings.Contains(theirs, "feature-side") {
		t.Errorf("theirs blob missing — captured after the abort? got %q", theirs)
	}

	if !hasConflictSignal(t, ws, 301) {
		t.Error("a captured conflict must emit CONFLICT_RESOLUTION_NEEDED so the scheduler rewinds")
	}
	if rebaseInProgress(t, ws) {
		t.Error("a captured conflict must abort the rebase — the evidence is already on disk")
	}
}

// TestRealGit_RebaseRefused_NoConflictState is the #301 defect-1 regression on
// its most reachable route: the rebase fails for a reason that is NOT a content
// conflict (a staged, uncommitted change makes git refuse before it starts).
// The unmerged-path probe then SUCCEEDS and reports zero files — an empty
// capture that is indistinguishable from a successful one on unfixed code.
//
// There is nothing for feature-dev to resolve, so this must not write a
// conflict context and must not emit CONFLICT_RESOLUTION_NEEDED: doing so burns
// the whole max_dev_redispatch budget re-running the dev stage against a
// context naming zero files.
func TestRealGit_RebaseRefused_NoConflictState(t *testing.T) {
	ws := realGitFixture(t, "dirty-index")

	a := NewBranchOutOfDate(&fakePRMergeRunner{})
	res := a.Execute(context.Background(), prMergeConflictFailure(ws, 301))

	if _, err := os.Stat(conflictContextPathForTest(ws, 301)); err == nil {
		data, _ := os.ReadFile(conflictContextPathForTest(ws, 301))
		t.Errorf("a rebase that conflicted with nothing must not write a conflict context; got:\n%s", data)
	}
	if hasConflictSignal(t, ws, 301) {
		t.Error("no conflict happened — CONFLICT_RESOLUTION_NEEDED must not be emitted")
	}
	if res.FollowUp != FollowUpHumanTriageRequired {
		t.Errorf("FollowUp = %q, want %q (nothing to re-dispatch feature-dev for)", res.FollowUp, FollowUpHumanTriageRequired)
	}
	if res.Recovered {
		t.Error("expected Recovered=false")
	}
}

// TestRealGit_DetachedHead_BranchGenuinelyUnknown pins the ONE state where the
// branch is legitimately undeterminable: the workspace is on a detached HEAD, so
// there is no branch, and git's own rebase-merge/head-name says the literal
// "detached HEAD" rather than a refs/heads/ ref.
//
// A conflict context naming "unknown" is not actionable — feature-dev refuses to
// check the branch out — so this is a FAILED capture, not a successful one: no
// context file, no rewind signal, human triage. And because the capture failed,
// the rebase is deliberately left in progress: `rebase --abort` would destroy
// the :2:/:3: blobs that are now the only copy of the conflict.
func TestRealGit_DetachedHead_BranchGenuinelyUnknown(t *testing.T) {
	ws := realGitFixture(t, "detached")

	a := NewBranchOutOfDate(&fakePRMergeRunner{})
	res := a.Execute(context.Background(), prMergeConflictFailure(ws, 301))

	if _, err := os.Stat(conflictContextPathForTest(ws, 301)); err == nil {
		data, _ := os.ReadFile(conflictContextPathForTest(ws, 301))
		t.Errorf("no branch is resolvable — must not write a context feature-dev cannot act on; got:\n%s", data)
	}
	if hasConflictSignal(t, ws, 301) {
		t.Error("a failed capture must not emit CONFLICT_RESOLUTION_NEEDED")
	}
	if res.FollowUp != FollowUpHumanTriageRequired {
		t.Errorf("FollowUp = %q, want %q", res.FollowUp, FollowUpHumanTriageRequired)
	}
	if !rebaseInProgress(t, ws) {
		t.Error("a failed capture must NOT abort — the conflicted index is the only surviving evidence")
	}
	if !strings.Contains(strings.Join(res.Evidence, " "), "rebase_left_in_progress=true") {
		t.Errorf("evidence must tell the operator the worktree is intentionally mid-rebase, got %v", res.Evidence)
	}
}
