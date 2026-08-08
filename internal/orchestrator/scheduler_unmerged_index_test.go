// Tests covering Issue #301 — the uncommitted-work rescue must refuse a
// worktree whose index is unmerged.
//
// The conflicted index the pr-merge recovery path can leave behind reads as
// blocking work to every existing consumer: `git status --porcelain` reports a
// conflicted path as `UU`, reclaim.ClassifyStatus calls anything that is not
// `??` Blocking, so hasUncommittedWork returns true and the scheduler's terminal
// defer calls RecoverUncommittedWork. That rescue then `git add -A`s the tree,
// which COLLAPSES the :2:/:3: index stages, commits files full of conflict
// markers onto the rebase's detached HEAD, and — because it returns nil — makes
// the caller relabel a real failure `worktree_uncommitted`, a kind that means
// "recovered, not a failure" and skips both the lifetime-failure increment and
// the board revert.
//
// The fixture is a REAL conflicted index produced by real git commands (#166):
// nothing about the shape under test is hand-authored.
package orchestrator

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// conflictedIndexRepo builds a repo whose index is genuinely unmerged: two
// branches edit the same line, and the merge stops at the conflict. Everything
// here is git's own doing.
func conflictedIndexRepo(t *testing.T) string {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not on PATH")
	}
	dir := t.TempDir()
	run := func(args ...string) {
		t.Helper()
		cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
		cmd.Env = append(os.Environ(),
			"GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_SYSTEM=/dev/null",
			"GIT_AUTHOR_NAME=T", "GIT_AUTHOR_EMAIL=t@t.invalid",
			"GIT_COMMITTER_NAME=T", "GIT_COMMITTER_EMAIL=t@t.invalid")
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, out)
		}
	}
	write := func(name, content string) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	run("init", "-q", "--initial-branch=main")
	write("f.txt", "line-1\nshared\nline-3\n")
	run("add", "f.txt")
	run("commit", "-q", "-m", "base")

	run("checkout", "-q", "-b", "feat")
	write("f.txt", "line-1\nfeature-side\nline-3\n")
	run("add", "f.txt")
	run("commit", "-q", "-m", "feature")

	run("checkout", "-q", "main")
	write("f.txt", "line-1\nmain-side\nline-3\n")
	run("add", "f.txt")
	run("commit", "-q", "-m", "main-side")

	// Expected to fail — that failure IS the fixture.
	_ = exec.Command("git", "-C", dir, "merge", "feat").Run()
	return dir
}

// TestHasUnmergedIndex_RealConflict pins the predicate against real git: a
// conflicted index answers true, a clean one false.
func TestHasUnmergedIndex_RealConflict(t *testing.T) {
	dir := conflictedIndexRepo(t)
	if !hasUnmergedIndex(dir) {
		t.Fatal("fixture precondition: the index must be unmerged after the failed merge")
	}
	if err := exec.Command("git", "-C", dir, "merge", "--abort").Run(); err != nil {
		t.Fatalf("merge --abort: %v", err)
	}
	if hasUnmergedIndex(dir) {
		t.Error("a resolved index must not read as unmerged")
	}
}

// TestRecoverUncommittedWork_RefusesUnmergedIndex is the #301 regression. The
// rescue must refuse rather than stage the conflict away, and the refusal must
// be an ERROR so the caller does not book the run as auto-recovered.
func TestRecoverUncommittedWork_RefusesUnmergedIndex(t *testing.T) {
	dir := conflictedIndexRepo(t)

	// The scheduler's terminal defer only calls the rescue when this says yes,
	// so the collision is real, not hypothetical.
	if !hasUncommittedWork(dir) {
		t.Fatal("precondition: a conflicted worktree reads as uncommitted work, which is what routes it into the rescue")
	}

	err := RecoverUncommittedWork(dir, 301, "pr-merge")
	if err == nil {
		t.Fatal("RecoverUncommittedWork on an unmerged index = nil — the caller reads nil as 'recovered' and relabels the failure worktree_uncommitted")
	}
	if !strings.Contains(err.Error(), "unmerged index") {
		t.Errorf("error should name the unmerged index, got %v", err)
	}

	// The conflict stages must be intact: `git add -A` would have collapsed them
	// ("path 'f.txt' is in the index, but not at stage 2").
	if !hasUnmergedIndex(dir) {
		t.Error("the conflict stages were destroyed by the rescue")
	}
	if out, err := exec.Command("git", "-C", dir, "show", ":2:f.txt").Output(); err != nil {
		t.Errorf("stage 2 blob no longer readable: %v", err)
	} else if !strings.Contains(string(out), "main-side") {
		t.Errorf("stage 2 blob = %q, want the pre-rescue content", out)
	}

	// And nothing was committed onto the in-progress merge.
	if out, err := exec.Command("git", "-C", dir, "log", "--oneline").Output(); err == nil {
		if strings.Contains(string(out), "auto-recovery") {
			t.Errorf("the rescue committed conflict markers anyway:\n%s", out)
		}
	}
}
