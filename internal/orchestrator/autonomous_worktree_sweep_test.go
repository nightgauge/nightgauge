package orchestrator

import (
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// mergedWorktreeRepo builds a repo with an "origin" remote and one
// pipeline-shaped worktree (issue-<n>) whose branch was squash-merged into
// main — the shape the reconcile sweep exists to reclaim. Returns the repo
// root and the worktree path.
func mergedWorktreeRepo(t *testing.T, issue int) (string, string) {
	t.Helper()
	base := t.TempDir()
	origin := filepath.Join(base, "origin.git")
	clone := filepath.Join(base, "clone")

	git := func(dir string, args ...string) {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %s (in %s): %v: %s", strings.Join(args, " "), dir, err, out)
		}
	}
	write := func(path, content string) {
		t.Helper()
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	git(base, "init", "--bare", "-b", "main", origin)
	seed := filepath.Join(base, "seed")
	if err := os.MkdirAll(seed, 0o755); err != nil {
		t.Fatal(err)
	}
	git(seed, "init", "-b", "main")
	git(seed, "config", "user.email", "test@test")
	git(seed, "config", "user.name", "test")
	write(filepath.Join(seed, "README"), "hello\n")
	git(seed, "add", ".")
	git(seed, "commit", "-m", "initial")
	git(seed, "remote", "add", "origin", origin)
	git(seed, "push", "-u", "origin", "main")

	git(base, "clone", origin, clone)
	git(clone, "config", "user.email", "test@test")
	git(clone, "config", "user.name", "test")

	root, err := filepath.EvalSymlinks(clone)
	if err != nil {
		t.Fatalf("resolve clone path: %v", err)
	}

	branch := "fix/" + strconv.Itoa(issue) + "-work"
	wt := filepath.Join(root, ".worktrees", "issue-"+strconv.Itoa(issue))
	git(root, "worktree", "add", wt, "-b", branch, "origin/main")
	write(filepath.Join(wt, "fix.txt"), "fixed\n")
	git(wt, "add", ".")
	git(wt, "commit", "-m", "work")
	git(root, "merge", "--squash", branch)
	git(root, "commit", "-m", "squash: "+branch)
	git(root, "push", "origin", "main")
	git(root, "fetch", "origin")

	return root, wt
}

func TestSweepMergedWorktrees_ReclaimsAcrossEveryRegisteredRepo(t *testing.T) {
	rootA, wtA := mergedWorktreeRepo(t, 201)
	rootB, wtB := mergedWorktreeRepo(t, 202)

	as := &AutonomousScheduler{
		scheduler: &Scheduler{
			workspaceRoot:     rootA,
			repoRootsResolver: func() []string { return []string{rootB} },
		},
		state: &AutonomousState{},
	}
	as.sweepMergedWorktrees()

	for _, wt := range []string{wtA, wtB} {
		if _, err := os.Stat(wt); !os.IsNotExist(err) {
			t.Errorf("worktree %s survived the sweep (err=%v)", wt, err)
		}
	}
}

func TestSweepMergedWorktrees_ProtectsRunningIssues(t *testing.T) {
	root, wt := mergedWorktreeRepo(t, 203)

	as := &AutonomousScheduler{
		scheduler: &Scheduler{workspaceRoot: root},
		// The PR landed but the run still has stages to execute in there.
		state: &AutonomousState{Running: []RunningItem{{Repo: "acme/app", Number: 203}}},
	}
	as.sweepMergedWorktrees()

	if _, err := os.Stat(wt); err != nil {
		t.Errorf("an in-flight run's worktree must survive the sweep: %v", err)
	}
}

func TestSweepMergedWorktrees_NoSchedulerIsNoOp(t *testing.T) {
	as := &AutonomousScheduler{state: &AutonomousState{}}
	as.sweepMergedWorktrees() // must not panic
}
