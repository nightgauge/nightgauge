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

// TestSweepMergedWorktrees_ZeroRootsIsLoud pins the #302 guard. Zero resolved
// scan roots is not a benign "nothing to do": even a single-repo workspace
// resolves its primary root, so an empty set means the root lookup itself
// failed. This IS the leak-detection pass (#110) — returning bare leaves
// worktree accumulation invisible for as long as the misconfiguration lasts,
// and the sweep looks like it ran clean every cycle.
func TestSweepMergedWorktrees_ZeroRootsIsLoud(t *testing.T) {
	// No workspace root and no roots resolver → repoScanRoots() is empty.
	as := &AutonomousScheduler{
		scheduler: &Scheduler{},
		state:     &AutonomousState{},
	}

	out := captureLog(t, func() { as.sweepMergedWorktrees() })

	if strings.TrimSpace(out) == "" {
		t.Fatal("zero resolved scan roots skipped the sweep in total silence — the leak detector cannot report its own absence")
	}
	if !strings.Contains(out, "no repo scan roots resolved") {
		t.Errorf("skip log does not name the cause; got %q", out)
	}
	if !strings.Contains(out, "WARN") {
		t.Errorf("a failed root lookup is a warning, not a debug line; got %q", out)
	}
}

// TestSchedulerSweepMergedWorktrees_ZeroRootsIsLoud is the mirror of the test
// above for (*Scheduler).sweepMergedWorktrees — the non-autonomous entry point
// carries an identical copy of the guard, and the same copy of the silence.
// The two live side by side deliberately: a fix applied to one and not the
// other reproduces the defect on the path nobody looked at.
func TestSchedulerSweepMergedWorktrees_ZeroRootsIsLoud(t *testing.T) {
	s := &Scheduler{}

	out := captureLog(t, func() { s.sweepMergedWorktrees() })

	if strings.TrimSpace(out) == "" {
		t.Fatal("zero resolved scan roots skipped the sweep in total silence — the leak detector cannot report its own absence")
	}
	if !strings.Contains(out, "no repo scan roots resolved") {
		t.Errorf("skip log does not name the cause; got %q", out)
	}
	// The same function already logs its undetermined-worktree-set skip as
	// `worktree-reconcile: WARN ...`; the zero-roots skip must not be quieter.
	if !strings.Contains(out, "worktree-reconcile: WARN") {
		t.Errorf("skip log does not match the sibling skip's idiom; got %q", out)
	}
}

// unreadableRoot returns a directory that is not a git repository and cannot
// reach one by walking up, so `git worktree list` there fails and
// execution.ActiveWorktreeIssues reports UNDETERMINED. The ceiling is its own
// private parent, so repos built by mergedWorktreeRepo (a different TempDir)
// keep working normally.
func unreadableRoot(t *testing.T) string {
	t.Helper()
	box := t.TempDir()
	dir := filepath.Join(box, "not-a-repo")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("GIT_CEILING_DIRECTORIES", box)
	return dir
}

// TestSweepMergedWorktrees_UndeterminedSkipsAutonomousSweep is the #403 guard
// on the path that actually runs in production. The autonomous sweep REMOVES
// DIRECTORIES; an active-worktree set it could not read means it cannot trust
// its own candidate enumeration either (one unreadable sibling root hides
// whatever that root held). Skip loudly — never reclaim on an answer that was
// never obtained (#296).
func TestSweepMergedWorktrees_UndeterminedSkipsAutonomousSweep(t *testing.T) {
	root, wt := mergedWorktreeRepo(t, 403) // reclaimable: content already on main
	notARepo := unreadableRoot(t)

	as := &AutonomousScheduler{
		scheduler: &Scheduler{
			workspaceRoot:     root,
			repoRootsResolver: func() []string { return []string{notARepo} },
		},
		state: &AutonomousState{},
	}

	out := captureLog(t, func() { as.sweepMergedWorktrees() })

	if _, err := os.Stat(wt); err != nil {
		t.Errorf("worktree %s was reclaimed on an undetermined active set: %v", wt, err)
	}
	if !strings.Contains(out, "autonomous: worktree sweep: WARN") {
		t.Errorf("the skip is not loud on the autonomous path; got %q", out)
	}
	if !strings.Contains(out, "active-worktree set is undetermined") {
		t.Errorf("skip log does not name the cause; got %q", out)
	}
}

// TestSchedulerSweepMergedWorktrees_ReclaimsUnqueuedWorktree pins the OTHER
// half of #403: the Scheduler copy used to union activeWorktreeIssues() into
// its protected set, and that scan walks the same roots with the same lister
// and the same issue-number parser as the sweep's own candidate enumeration —
// so every candidate protected itself and the sweep could never reclaim
// anything. A merged worktree for an issue nobody has queued must go.
func TestSchedulerSweepMergedWorktrees_ReclaimsUnqueuedWorktree(t *testing.T) {
	root, wt := mergedWorktreeRepo(t, 704)

	s := &Scheduler{workspaceRoot: root}
	s.sweepMergedWorktrees()

	if _, err := os.Stat(wt); !os.IsNotExist(err) {
		t.Errorf("merged worktree %s survived the scheduler sweep (err=%v) — the sweep is a no-op again", wt, err)
	}
}

// TestSchedulerSweepMergedWorktrees_ProtectsQueuedIssues is the counterweight
// to the test above: dropping the self-protecting union must not drop the
// protection that matters. A queued issue's worktree survives even though its
// branch content is already on the default branch — the run may still have
// stages to execute in that directory.
func TestSchedulerSweepMergedWorktrees_ProtectsQueuedIssues(t *testing.T) {
	root, wt := mergedWorktreeRepo(t, 705)

	s := &Scheduler{workspaceRoot: root}
	s.queue = []QueueItem{{Repo: "acme/app", IssueNumber: 705, Status: "processing"}}
	s.sweepMergedWorktrees()

	if _, err := os.Stat(wt); err != nil {
		t.Errorf("a queued run's worktree must survive the sweep: %v", err)
	}
}
