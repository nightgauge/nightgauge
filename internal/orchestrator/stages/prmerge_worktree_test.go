package stages

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// setupLinkedWorktreeForMerge builds the exact topology every pipeline run
// executes pr-merge from (Issue #589): a primary checkout holding `main`, and
// a linked worktree — created the way the pipeline creates one, via
// `git worktree add` off the primary checkout — checked out on the PR's own
// head branch. `.git` in worktreeDir is a FILE pointing at the shared common
// dir, exactly as it is in production; nothing here is hand-authored.
func setupLinkedWorktreeForMerge(t *testing.T, headBranch string) (mainDir, worktreeDir string) {
	t.Helper()

	root := t.TempDir()
	mainDir = filepath.Join(root, "main")
	worktreeDir = filepath.Join(root, "issue-589")

	if err := os.MkdirAll(mainDir, 0o755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	gitRun(t, mainDir, "init")
	gitRun(t, mainDir, "symbolic-ref", "HEAD", "refs/heads/main")
	gitRun(t, mainDir, "config", "user.email", "test@nightgauge.dev")
	gitRun(t, mainDir, "config", "user.name", "Nightgauge Test")
	if err := os.WriteFile(filepath.Join(mainDir, "README.md"), []byte("seed\n"), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	gitRun(t, mainDir, "add", "README.md")
	gitRun(t, mainDir, "commit", "-m", "chore: seed")

	// Mirrors production: the pipeline adds a linked worktree on the run's own
	// feature/fix branch, off the primary checkout — which stays on `main`.
	gitRun(t, mainDir, "worktree", "add", "-b", headBranch, worktreeDir, "HEAD")

	dotGit := filepath.Join(worktreeDir, ".git")
	info, err := os.Lstat(dotGit)
	if err != nil || !info.Mode().IsRegular() {
		t.Fatalf("fixture is not a linked worktree: %s is not a regular file (err=%v)", dotGit, err)
	}

	assertBranch(t, mainDir, "main")
	assertBranch(t, worktreeDir, headBranch)

	return mainDir, worktreeDir
}

func gitRun(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %s (dir=%s): %v\n%s", strings.Join(args, " "), dir, err, out)
	}
	return string(out)
}

func assertBranch(t *testing.T, dir, want string) {
	t.Helper()
	cmd := exec.Command("git", "symbolic-ref", "--short", "HEAD")
	cmd.Dir = dir
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("read current branch in %s: %v", dir, err)
	}
	if got := strings.TrimSpace(string(out)); got != want {
		t.Fatalf("checkout %s is on branch %q, want %q", dir, got, want)
	}
}

// writeFakeGh installs a fake `gh` on PATH that answers `pr view` with a
// clean, merge-eligible snapshot and `pr merge` by flipping a state marker —
// UNLESS the invocation carries `--delete-branch`/`-d`, in which case it
// reproduces the real `gh` CLI's post-merge behavior: switching the local
// checkout to the base branch before it can delete the (currently checked
// out) head branch. That is the exact call that collides with the primary
// checkout's claim on `main` in a linked worktree — the bug this test guards
// against. `git` itself is untouched: PATH keeps the real `git` behind the
// fake `gh`, so the fake script's own `git checkout main` call is real.
func writeFakeGh(t *testing.T, headBranch string) (binDir, stateDir string) {
	t.Helper()

	binDir = t.TempDir()
	stateDir = t.TempDir()

	script := `#!/bin/sh
set -e
if [ "$1" = "pr" ] && [ "$2" = "view" ]; then
  state="OPEN"
  if [ -f "$FAKE_GH_STATE_DIR/merged" ]; then
    state="MERGED"
  fi
  printf '{"state":"%s","mergeable":"MERGEABLE","mergeStateStatus":"CLEAN","reviewDecision":"","headRefName":"%s","statusCheckRollup":[]}\n' "$state" "$FAKE_GH_HEAD_REF"
  exit 0
fi
if [ "$1" = "pr" ] && [ "$2" = "merge" ]; then
  shift 2
  shift # drop the PR number
  wantDelete=0
  for a in "$@"; do
    case "$a" in
      --delete-branch|-d) wantDelete=1 ;;
    esac
  done
  if [ "$wantDelete" = "1" ]; then
    # Real gh's post-merge local cleanup: switch off the head branch before
    # deleting it. This is what fails from a linked worktree.
    git checkout main
  fi
  touch "$FAKE_GH_STATE_DIR/merged"
  exit 0
fi
echo "fake-gh: unhandled invocation: $*" >&2
exit 1
`
	ghPath := filepath.Join(binDir, "gh")
	if err := os.WriteFile(ghPath, []byte(script), 0o755); err != nil {
		t.Fatalf("write fake gh: %v", err)
	}

	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("FAKE_GH_STATE_DIR", stateDir)
	t.Setenv("FAKE_GH_HEAD_REF", headBranch)

	return binDir, stateDir
}

// TestExecGhClient_OldDeleteBranchFlag_CollidesInLinkedWorktree pins the bug
// this issue fixes: proves the fixture is faithful by showing the PRE-#589
// invocation (`gh pr merge --squash --delete-branch`) really does fail from a
// linked worktree whose primary checkout holds `main`, with exactly the error
// text captured in the live evidence (run 01a007d5's punt_reason).
func TestExecGhClient_OldDeleteBranchFlag_CollidesInLinkedWorktree(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("fake gh is a POSIX shell script")
	}
	headBranch := "fix/589-old-behavior"
	mainDir, worktreeDir := setupLinkedWorktreeForMerge(t, headBranch)
	_, _ = mainDir, worktreeDir
	writeFakeGh(t, headBranch)

	cmd := exec.Command("gh", "pr", "merge", "42", "--squash", "--delete-branch")
	cmd.Dir = worktreeDir
	out, err := cmd.CombinedOutput()
	if err == nil {
		t.Fatalf("fixture did not reproduce the collision: `gh pr merge --delete-branch` succeeded from " +
			"a linked worktree while the primary checkout held main — this test would pass even without the fix")
	}
	if !strings.Contains(string(out), "already used by worktree") {
		t.Fatalf("expected git's worktree-occupancy error, got: %s", out)
	}

	// The worktree must still be on its own branch — the failed `git
	// checkout main` never took effect.
	assertBranch(t, worktreeDir, headBranch)
	assertBranch(t, mainDir, "main")
}

// TestExecGhClient_Merge_SucceedsFromLinkedWorktree is the Issue #589
// regression test (AC1/AC2): the deterministic pr-merge path must complete a
// squash merge from a linked worktree — primary checkout holding `main`, run
// worktree on the PR's own head branch — without ever checking out `main`.
func TestExecGhClient_Merge_SucceedsFromLinkedWorktree(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("fake gh is a POSIX shell script")
	}
	headBranch := "fix/589-prmerge-deterministic-worktree"
	mainDir, worktreeDir := setupLinkedWorktreeForMerge(t, headBranch)
	writeFakeGh(t, headBranch)

	// pr-{N}.json — what pr-create leaves behind in the worktree.
	pipelineDir := filepath.Join(worktreeDir, ".nightgauge", "pipeline")
	if err := os.MkdirAll(pipelineDir, 0o755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	if err := os.WriteFile(filepath.Join(pipelineDir, "pr-589.json"), []byte(`{"pr_number":42}`), 0o644); err != nil {
		t.Fatalf("write pr context: %v", err)
	}

	r := NewDeterministicRunner()
	r.pollInterval = 0
	r.ciPollInterval = 0

	res, err := r.Run(context.Background(), 589, "nightgauge/nightgauge", worktreeDir)
	if err != nil {
		t.Fatalf("Run returned unexpected error: %v", err)
	}
	if res.Path != PathMerged {
		t.Fatalf("Path = %q (reason=%q), want %q — the deterministic path must succeed from a linked "+
			"worktree for a merge-eligible PR", res.Path, res.Reason, PathMerged)
	}
	if res.PRNumber != 42 {
		t.Errorf("PRNumber = %d, want 42", res.PRNumber)
	}
	if res.HeadRefName != headBranch {
		t.Errorf("HeadRefName = %q, want %q", res.HeadRefName, headBranch)
	}

	// The whole point: neither checkout moved. The primary checkout still
	// holds main and the worktree is still on its own branch — the merge
	// completed without ever touching either.
	assertBranch(t, mainDir, "main")
	assertBranch(t, worktreeDir, headBranch)
}
