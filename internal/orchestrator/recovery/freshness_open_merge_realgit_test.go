package recovery

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/nightgauge/nightgauge/internal/gittest"
)

// These tests run the shipped FRESHNESS_CHECK and STALE_BRANCH_MERGE shell
// blocks, lifted verbatim with skillShellFragment, against a real repository
// holding an open merge (#2755).

// openMergeRepo builds a clone whose feature branch is one commit behind
// origin/main, with the same line of f.txt changed on both sides, plus a stub
// `nightgauge` directory to put first on PATH. The stub really pushes on
// `nightgauge git push-branch` (a plain push, never forced) and exits 0 on
// anything else.
func openMergeRepo(t *testing.T) (string, string) {
	t.Helper()
	root := t.TempDir()
	bare := filepath.Join(root, "origin.git")
	gittest.Run(t, root, "init", "-q", "--bare", bare)
	ws := filepath.Join(root, "ws")
	gittest.Run(t, root, "clone", "-q", bare, ws)
	write := func(content string) {
		if err := os.WriteFile(filepath.Join(ws, "f.txt"), []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	gittest.Run(t, ws, "checkout", "-q", "-b", "main")
	write("base\n")
	gittest.Run(t, ws, "add", "f.txt")
	gittest.Run(t, ws, "commit", "-q", "-m", "base")
	gittest.Run(t, ws, "push", "-q", "origin", "main")
	gittest.Run(t, ws, "checkout", "-q", "-b", testBranch)
	write("feature\n")
	gittest.Run(t, ws, "commit", "-q", "-am", "feature")
	gittest.Run(t, ws, "checkout", "-q", "main")
	write("advanced\n")
	gittest.Run(t, ws, "commit", "-q", "-am", "advance base")
	gittest.Run(t, ws, "push", "-q", "origin", "main")
	gittest.Run(t, ws, "checkout", "-q", testBranch)

	stub := filepath.Join(root, "bin")
	if err := os.MkdirAll(stub, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(stub, "nightgauge"), []byte(pushStub), 0o755); err != nil {
		t.Fatal(err)
	}
	return ws, stub
}

const testBranch = "feat/2755-open-merge"

const pushStub = `#!/bin/sh
if [ "$1" = git ] && [ "$2" = push-branch ]; then
  exec git push -q origin HEAD
fi
exit 0
`

// remoteBranch returns the bare origin's ref for the feature branch, or "" if
// it was never pushed. It reads the bare repository itself, not the local
// tracking ref.
func remoteBranch(ws string) string {
	bare := filepath.Join(filepath.Dir(ws), "origin.git")
	out, err := gittest.Command(bare, "rev-parse", "-q", "--verify", "refs/heads/"+testBranch).Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}

// assertPushed fails unless origin's feature branch is local HEAD and contains
// origin's main: the concluded merge reached the remote that pr-merge reads.
func assertPushed(t *testing.T, ws, out string) {
	t.Helper()
	head, err := gittest.Command(ws, "rev-parse", "HEAD").Output()
	if err != nil {
		t.Fatal(err)
	}
	remote := remoteBranch(ws)
	if remote != strings.TrimSpace(string(head)) {
		t.Fatalf("origin %s = %q, want local HEAD %s: the concluded merge was not pushed\n%s",
			testBranch, remote, strings.TrimSpace(string(head)), out)
	}
	bare := filepath.Join(filepath.Dir(ws), "origin.git")
	if err := gittest.Command(bare, "merge-base", "--is-ancestor", "refs/heads/main", remote).Run(); err != nil {
		t.Errorf("origin %s does not contain origin main\n%s", testBranch, out)
	}
}

// assertNotPushed fails if anything reached origin's feature branch.
func assertNotPushed(t *testing.T, ws, out string) {
	t.Helper()
	if remote := remoteBranch(ws); remote != "" {
		t.Errorf("an open merge must push nothing, origin %s = %s\n%s", testBranch, remote, out)
	}
}

// runSkillBlock runs a shipped shell block under sh in ws, stub first on PATH.
func runSkillBlock(t *testing.T, sh, ws, stub, block string) (string, error) {
	t.Helper()
	script := filepath.Join(t.TempDir(), "block.sh")
	if err := os.WriteFile(script, []byte(block), 0o644); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(sh, script)
	cmd.Dir = ws
	cmd.Env = append(gittest.Env(), "PATH="+stub+string(os.PathListSeparator)+os.Getenv("PATH"))
	out, err := cmd.CombinedOutput()
	return string(out), err
}

// leaveConflictOpen opens the base merge and stops on its conflict, nothing
// resolved.
func leaveConflictOpen(t *testing.T, ws string) {
	t.Helper()
	_ = gittest.Command(ws, "merge", "origin/main").Run() // conflicts by construction
	if unmergedEntries(ws) == "" {
		t.Fatal("expected an unresolved conflict")
	}
}

// assertConcluded fails unless the open merge was committed with want as f.txt.
func assertConcluded(t *testing.T, ws, want, out string) {
	t.Helper()
	if mergeHeadOpen(ws) {
		t.Fatalf("a fully staged merge was left open\n%s", out)
	}
	got, err := gittest.Command(ws, "show", "HEAD:f.txt").Output()
	if err != nil {
		t.Fatalf("git show HEAD:f.txt: %v", err)
	}
	if string(got) != want {
		t.Errorf("committed f.txt = %q, want %q", got, want)
	}
	if err := gittest.Command(ws, "merge-base", "--is-ancestor", "origin/main", "HEAD").Run(); err != nil {
		t.Errorf("the concluded merge does not contain origin/main\n%s", out)
	}
}

// stageOpenMerge reproduces feature-dev's hand-off: the base merged, the
// conflict resolved and staged, MERGE_HEAD deliberately left open.
func stageOpenMerge(t *testing.T, ws, resolution string) {
	t.Helper()
	_ = gittest.Command(ws, "merge", "origin/main").Run() // conflicts by construction
	if err := os.WriteFile(filepath.Join(ws, "f.txt"), []byte(resolution), 0o644); err != nil {
		t.Fatal(err)
	}
	gittest.Run(t, ws, "add", "f.txt")
}

func mergeHeadOpen(ws string) bool {
	return gittest.Command(ws, "rev-parse", "-q", "--verify", "MERGE_HEAD").Run() == nil
}

func unmergedEntries(ws string) string {
	u, _ := gittest.Command(ws, "ls-files", "-u").Output()
	return string(u)
}

func blockShells() []string {
	shells := []string{"bash"}
	if _, err := exec.LookPath("zsh"); err == nil {
		shells = append(shells, "zsh")
	}
	return shells
}

// TestRealGit_FreshnessCheck_OpenMergeSurvives is the #2755 regression: the
// shipped FRESHNESS_CHECK block must never abort a merge it did not start: a
// fully staged one is concluded, one with unmerged paths is left as found.
func TestRealGit_FreshnessCheck_OpenMergeSurvives(t *testing.T) {
	src, err := filepath.Abs(filepath.Join("..", "..", "..", "skills", "_shared", "FRESHNESS_CHECK.md"))
	if err != nil {
		t.Fatal(err)
	}
	block := skillShellFragment(t, src, "FRESHNESS_BASE=\"${BASE_BRANCH:-main}\"", "\nfi\n")
	if !strings.Contains(block, "FRESHNESS_CHECK_FAILED") {
		t.Fatalf("fragment terminated early:\n%s", block)
	}
	if strings.Contains(block, "git rebase") || strings.Contains(block, "--force") {
		t.Fatal("the freshness block must merge, never rebase or force-push (#2699)")
	}
	for _, sh := range blockShells() {
		t.Run("staged merge is concluded/"+sh, func(t *testing.T) {
			ws, stub := openMergeRepo(t)
			stageOpenMerge(t, ws, "resolved\n")
			out, err := runSkillBlock(t, sh, ws, stub, block)
			if err != nil {
				t.Fatalf("%s: %v\n%s", sh, err, out)
			}
			assertConcluded(t, ws, "resolved\n", out)
			assertPushed(t, ws, out)
			if strings.Contains(out, "FRESHNESS_CHECK_FAILED=true") {
				t.Errorf("a concluded merge is not a failure:\n%s", out)
			}
		})
		t.Run("unmerged paths left intact/"+sh, func(t *testing.T) {
			ws, stub := openMergeRepo(t)
			leaveConflictOpen(t, ws)
			before := unmergedEntries(ws)
			out, err := runSkillBlock(t, sh, ws, stub, block)
			if err != nil {
				t.Fatalf("%s: %v\n%s", sh, err, out)
			}
			if !mergeHeadOpen(ws) {
				t.Fatalf("the open merge was aborted (MERGE_HEAD gone)\n%s", out)
			}
			if after := unmergedEntries(ws); after != before {
				t.Errorf("unmerged entries changed: %q -> %q", before, after)
			}
			assertNotPushed(t, ws, out)
			if !strings.Contains(out, "FRESHNESS_CHECK_FAILED=true") {
				t.Errorf("an open merge must report FRESHNESS_CHECK_FAILED=true:\n%s", out)
			}
		})
		t.Run("commit fails after resolution/"+sh, func(t *testing.T) {
			ws, stub := openMergeRepo(t)
			hook := filepath.Join(ws, ".git", "hooks", "pre-commit")
			if err := os.WriteFile(hook, []byte("#!/bin/sh\nexit 1\n"), 0o755); err != nil {
				t.Fatal(err)
			}
			out, err := runSkillBlock(t, sh, ws, stub, block)
			if err != nil {
				t.Fatalf("%s: %v\n%s", sh, err, out)
			}
			if !mergeHeadOpen(ws) {
				t.Fatalf("a failed commit aborted the staged resolution\n%s", out)
			}
			if u := unmergedEntries(ws); u != "" {
				t.Errorf("the resolution was not left staged: %q", u)
			}
			if !strings.Contains(out, "FRESHNESS_CHECK_FAILED=true") {
				t.Errorf("a failed commit must report FRESHNESS_CHECK_FAILED=true:\n%s", out)
			}
		})
		t.Run("open merge on a branch not behind base/"+sh, func(t *testing.T) {
			ws, stub := openMergeRepo(t)
			// Bring the base in for good, then leave an unrelated merge open on
			// conflicts: the branch is level with base yet not committable.
			gittest.Run(t, ws, "branch", "side", "HEAD")
			stageOpenMerge(t, ws, "resolved\n")
			gittest.Run(t, ws, "commit", "-q", "--no-edit")
			gittest.Run(t, ws, "checkout", "-q", "side")
			if err := os.WriteFile(filepath.Join(ws, "f.txt"), []byte("side\n"), 0o644); err != nil {
				t.Fatal(err)
			}
			gittest.Run(t, ws, "commit", "-q", "-am", "side")
			gittest.Run(t, ws, "checkout", "-q", testBranch)
			_ = gittest.Command(ws, "merge", "side").Run() // conflicts by construction
			if unmergedEntries(ws) == "" {
				t.Fatal("expected an unresolved conflict")
			}
			out, err := runSkillBlock(t, sh, ws, stub, block)
			if err != nil {
				t.Fatalf("%s: %v\n%s", sh, err, out)
			}
			if !mergeHeadOpen(ws) {
				t.Fatalf("the open merge was aborted (MERGE_HEAD gone)\n%s", out)
			}
			if !strings.Contains(out, "FRESHNESS_CHECK_FAILED=true") {
				t.Errorf("an open merge on a fresh branch must report FRESHNESS_CHECK_FAILED=true:\n%s", out)
			}
		})
	}
}

// TestRealGit_StaleBranchMerge_OpenMergeSurvives pins the same guard in
// pr-create's STALE_BRANCH_MERGE block: an open merge fails the stage without
// touching it and without claiming a base conflict.
func TestRealGit_StaleBranchMerge_OpenMergeSurvives(t *testing.T) {
	src, err := filepath.Abs(filepath.Join("..", "..", "..", "skills", "_shared", "STALE_BRANCH_MERGE.md"))
	if err != nil {
		t.Fatal(err)
	}
	block := skillShellFragment(t, src, "MERGE_BASE=\"${BASE_BRANCH:-main}\"", "\nfi\n")
	if !strings.Contains(block, "merge-in-progress") {
		t.Fatalf("fragment terminated early:\n%s", block)
	}
	for _, sh := range blockShells() {
		t.Run("staged merge is concluded/"+sh, func(t *testing.T) {
			ws, stub := openMergeRepo(t)
			stageOpenMerge(t, ws, "resolved\n")
			out, err := runSkillBlock(t, sh, ws, stub, block)
			if err != nil {
				t.Fatalf("%s: %v\n%s", sh, err, out)
			}
			assertConcluded(t, ws, "resolved\n", out)
			assertPushed(t, ws, out)
			if !strings.Contains(out, "Concluded merge pushed") {
				t.Errorf("a concluded merge must report status merged, not up-to-date:\n%s", out)
			}
		})
		t.Run("unmerged paths fail the stage/"+sh, func(t *testing.T) {
			ws, stub := openMergeRepo(t)
			leaveConflictOpen(t, ws)
			before := unmergedEntries(ws)
			out, err := runSkillBlock(t, sh, ws, stub, block)
			var exit *exec.ExitError
			if !errors.As(err, &exit) || exit.ExitCode() != 1 {
				t.Fatalf("want exit 1 on an unresolved open merge, got %v\n%s", err, out)
			}
			if !mergeHeadOpen(ws) {
				t.Fatalf("the open merge was aborted (MERGE_HEAD gone)\n%s", out)
			}
			if after := unmergedEntries(ws); after != before {
				t.Errorf("unmerged entries changed: %q -> %q", before, after)
			}
			assertNotPushed(t, ws, out)
			if strings.Contains(out, "stale-branch-merge-conflict") {
				t.Errorf("an open merge is not a base conflict:\n%s", out)
			}
		})
	}
}
