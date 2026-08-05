package execution

import (
	"bytes"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// sweepFixture is an "origin + clone" pair: a bare upstream and a working
// clone, so origin/main is a real remote-tracking ref and the sweep exercises
// the same base-ref resolution it uses in production.
type sweepFixture struct {
	t    *testing.T
	root string // the clone — the repo whose worktrees are swept
}

func newSweepFixture(t *testing.T) *sweepFixture {
	t.Helper()
	base := t.TempDir()
	origin := filepath.Join(base, "origin.git")
	clone := filepath.Join(base, "clone")

	run(t, base, "git", "init", "--bare", "-b", "main", origin)

	seed := filepath.Join(base, "seed")
	mustMkdir(t, seed)
	run(t, seed, "git", "init", "-b", "main")
	configureGit(t, seed)
	writeFile(t, filepath.Join(seed, "README"), "hello\n")
	run(t, seed, "git", "add", ".")
	run(t, seed, "git", "commit", "-m", "initial")
	run(t, seed, "git", "remote", "add", "origin", origin)
	run(t, seed, "git", "push", "-u", "origin", "main")

	run(t, base, "git", "clone", origin, clone)
	configureGit(t, clone)

	// git reports fully-resolved paths; on macOS t.TempDir() hands back a
	// /var path that is really a symlink to /private/var, so resolve it here
	// or every path comparison in these tests is a false mismatch.
	resolved, err := filepath.EvalSymlinks(clone)
	if err != nil {
		t.Fatalf("resolve clone path: %v", err)
	}

	return &sweepFixture{t: t, root: resolved}
}

// addWorktree creates a pipeline-shaped worktree (issue-<n>) on a new branch.
func (f *sweepFixture) addWorktree(issue int, branch string) string {
	f.t.Helper()
	path := filepath.Join(f.root, ".worktrees", "issue-"+strconv.Itoa(issue))
	run(f.t, f.root, "git", "worktree", "add", path, "-b", branch, "origin/main")
	return path
}

// commitIn adds a file and commits it inside a worktree.
func (f *sweepFixture) commitIn(path, name, content string) {
	f.t.Helper()
	writeFile(f.t, filepath.Join(path, name), content)
	run(f.t, path, "git", "add", ".")
	run(f.t, path, "git", "commit", "-m", "work: "+name)
}

// squashMergeToMain reproduces `gh pr merge --squash`: the branch's content
// lands on main as one NEW commit, so the branch tip is not an ancestor of
// main and only a content diff can tell that the work merged.
func (f *sweepFixture) squashMergeToMain(branch string) {
	f.t.Helper()
	run(f.t, f.root, "git", "merge", "--squash", branch)
	run(f.t, f.root, "git", "commit", "-m", "squash: "+branch)
	run(f.t, f.root, "git", "push", "origin", "main")
	run(f.t, f.root, "git", "fetch", "origin")
}

func TestSweepMergedWorktrees_ReclaimsSquashMergedBranch(t *testing.T) {
	f := newSweepFixture(t)
	wt := f.addWorktree(110, "fix/110-thing")
	f.commitIn(wt, "fix.txt", "fixed\n")
	f.squashMergeToMain("fix/110-thing")

	// Ancestry says "unmerged" here — that is the false negative the sweep
	// must not rely on. Assert the trap is actually armed before testing it.
	if _, err := gitOutput(f.root, "merge-base", "--is-ancestor", "fix/110-thing", "origin/main"); err == nil {
		t.Fatal("fixture is not a real squash merge — branch tip is an ancestor of origin/main")
	}

	res, err := SweepMergedWorktrees(WorktreeSweepOptions{RepoRoot: f.root})
	if err != nil {
		t.Fatalf("sweep: %v", err)
	}
	if len(res.Reclaimed) != 1 || res.Reclaimed[0].Branch != "fix/110-thing" {
		t.Fatalf("expected the squash-merged worktree to be reclaimed, got reclaimed=%+v skipped=%+v",
			res.Reclaimed, res.Skipped)
	}
	if res.Reclaimed[0].IssueNumber != 110 {
		t.Errorf("IssueNumber = %d, want 110", res.Reclaimed[0].IssueNumber)
	}
	if _, err := os.Stat(wt); !os.IsNotExist(err) {
		t.Errorf("worktree directory still on disk at %s", wt)
	}
	if branches := mustGit(t, f.root, "branch", "--list", "fix/110-thing"); strings.TrimSpace(branches) != "" {
		t.Errorf("local branch survived the sweep: %q", branches)
	}
}

func TestSweepMergedWorktrees_KeepsBranchWithUniqueWork(t *testing.T) {
	f := newSweepFixture(t)
	wt := f.addWorktree(111, "feat/111-unmerged")
	f.commitIn(wt, "feature.txt", "not merged anywhere\n")

	res, err := SweepMergedWorktrees(WorktreeSweepOptions{RepoRoot: f.root})
	if err != nil {
		t.Fatalf("sweep: %v", err)
	}
	if len(res.Reclaimed) != 0 {
		t.Fatalf("branch with unmerged work must never be reclaimed, got %+v", res.Reclaimed)
	}
	assertSkipped(t, res, wt, SkipUnmergedContent)
	if _, err := os.Stat(wt); err != nil {
		t.Errorf("worktree was removed despite unmerged work: %v", err)
	}
}

func TestSweepMergedWorktrees_KeepsDirtyWorktree(t *testing.T) {
	f := newSweepFixture(t)
	wt := f.addWorktree(112, "fix/112-dirty")
	f.commitIn(wt, "fix.txt", "fixed\n")
	f.squashMergeToMain("fix/112-dirty")
	// Uncommitted edit made after the merge — reclaiming would destroy it.
	writeFile(t, filepath.Join(wt, "fix.txt"), "local edit not committed\n")

	res, err := SweepMergedWorktrees(WorktreeSweepOptions{RepoRoot: f.root})
	if err != nil {
		t.Fatalf("sweep: %v", err)
	}
	if len(res.Reclaimed) != 0 {
		t.Fatalf("dirty worktree must never be reclaimed, got %+v", res.Reclaimed)
	}
	assertSkipped(t, res, wt, SkipDirty)
	if _, err := os.Stat(filepath.Join(wt, "fix.txt")); err != nil {
		t.Errorf("uncommitted work was destroyed: %v", err)
	}
}

func TestSweepMergedWorktrees_KeepsUntrackedFiles(t *testing.T) {
	f := newSweepFixture(t)
	wt := f.addWorktree(113, "fix/113-untracked")
	f.commitIn(wt, "fix.txt", "fixed\n")
	f.squashMergeToMain("fix/113-untracked")
	writeFile(t, filepath.Join(wt, "scratch-notes.md"), "unsaved analysis\n")

	res, err := SweepMergedWorktrees(WorktreeSweepOptions{RepoRoot: f.root})
	if err != nil {
		t.Fatalf("sweep: %v", err)
	}
	if len(res.Reclaimed) != 0 {
		t.Fatalf("untracked files must block reclamation, got %+v", res.Reclaimed)
	}
	assertSkipped(t, res, wt, SkipDirty)
}

func TestSweepMergedWorktrees_NeverRemovesPrimaryCheckout(t *testing.T) {
	f := newSweepFixture(t)

	res, err := SweepMergedWorktrees(WorktreeSweepOptions{RepoRoot: f.root})
	if err != nil {
		t.Fatalf("sweep: %v", err)
	}
	if len(res.Reclaimed) != 0 {
		t.Fatalf("primary checkout must never be reclaimed, got %+v", res.Reclaimed)
	}
	assertSkipped(t, res, f.root, SkipPrimary)
	if _, err := os.Stat(filepath.Join(f.root, "README")); err != nil {
		t.Fatalf("primary checkout was damaged: %v", err)
	}
}

func TestSweepMergedWorktrees_KeepsActiveRun(t *testing.T) {
	f := newSweepFixture(t)
	wt := f.addWorktree(114, "fix/114-still-running")
	f.commitIn(wt, "fix.txt", "fixed\n")
	f.squashMergeToMain("fix/114-still-running")

	// The PR landed but the run has stages left to execute in this directory.
	res, err := SweepMergedWorktrees(WorktreeSweepOptions{
		RepoRoot:     f.root,
		ActiveIssues: map[int]bool{114: true},
	})
	if err != nil {
		t.Fatalf("sweep: %v", err)
	}
	if len(res.Reclaimed) != 0 {
		t.Fatalf("an in-flight run's worktree must never be reclaimed, got %+v", res.Reclaimed)
	}
	assertSkipped(t, res, wt, SkipActiveRun)
}

func TestSweepMergedWorktrees_KeepsFreshWorktreeWithNoCommits(t *testing.T) {
	f := newSweepFixture(t)
	// A worktree created at origin/main that has not committed yet also has an
	// empty content diff — it must not be mistaken for merged work.
	wt := f.addWorktree(115, "feat/115-just-started")

	res, err := SweepMergedWorktrees(WorktreeSweepOptions{RepoRoot: f.root})
	if err != nil {
		t.Fatalf("sweep: %v", err)
	}
	if len(res.Reclaimed) != 0 {
		t.Fatalf("a worktree with no commits of its own must not be reclaimed, got %+v", res.Reclaimed)
	}
	assertSkipped(t, res, wt, SkipNoOwnCommits)
}

func TestSweepMergedWorktrees_IgnoresNonPipelineWorktrees(t *testing.T) {
	f := newSweepFixture(t)
	wt := filepath.Join(f.root, ".worktrees", "my-experiment")
	run(t, f.root, "git", "worktree", "add", wt, "-b", "feat/handmade", "origin/main")
	f.commitIn(wt, "notes.txt", "hand-made\n")
	f.squashMergeToMain("feat/handmade")

	res, err := SweepMergedWorktrees(WorktreeSweepOptions{RepoRoot: f.root})
	if err != nil {
		t.Fatalf("sweep: %v", err)
	}
	if len(res.Reclaimed) != 0 {
		t.Fatalf("only pipeline-created worktrees may be reclaimed, got %+v", res.Reclaimed)
	}
	assertSkipped(t, res, wt, SkipNotPipelineManaged)
}

func TestSweepMergedWorktrees_DryRunRemovesNothing(t *testing.T) {
	f := newSweepFixture(t)
	wt := f.addWorktree(116, "fix/116-dry")
	f.commitIn(wt, "fix.txt", "fixed\n")
	f.squashMergeToMain("fix/116-dry")

	res, err := SweepMergedWorktrees(WorktreeSweepOptions{RepoRoot: f.root, DryRun: true})
	if err != nil {
		t.Fatalf("sweep: %v", err)
	}
	if len(res.Reclaimed) != 1 {
		t.Fatalf("dry run must still classify, got %+v", res.Reclaimed)
	}
	if _, err := os.Stat(wt); err != nil {
		t.Errorf("dry run removed the worktree: %v", err)
	}
}

func TestReclaimWorktree_LogsWarningOnRemovalFailure(t *testing.T) {
	f := newSweepFixture(t)
	// A path git has never heard of: `git worktree remove` fails, the manual
	// fallback succeeds. Before #110 that combination produced no output at
	// all, which is how leaks went unobserved.
	phantom := filepath.Join(f.root, ".worktrees", "issue-999")

	logged := captureLog(t, func() {
		if err := reclaimWorktree(f.root, worktreeRecord{Path: phantom, Branch: "fix/999-gone"}, false); err != nil {
			t.Fatalf("reclaimWorktree must soft-fail through the manual path: %v", err)
		}
	})
	if !strings.Contains(logged, "[WARN]") || !strings.Contains(logged, "git worktree remove") {
		t.Errorf("removal failure must be logged at WARN, got:\n%s", logged)
	}
}

func TestCleanupWorktree_LogsWarningOnRemovalFailure(t *testing.T) {
	// Not a git repo, so `git worktree remove` fails; the worktree directory
	// does not exist, so the manual fallback succeeds and CleanupWorktree
	// returns nil. The only observable signal is the log line.
	root := t.TempDir()
	m := &Manager{workspaceRoot: root}

	logged := captureLog(t, func() {
		if err := m.CleanupWorktree("nightgauge/nightgauge", 110); err != nil {
			t.Fatalf("CleanupWorktree: %v", err)
		}
	})
	if !strings.Contains(logged, "[WARN] worktree teardown") {
		t.Errorf("teardown failure must be logged at WARN, got:\n%s", logged)
	}
}

func TestDetectBranchAhead_AheadAndClean(t *testing.T) {
	f := newSweepFixture(t)
	wt := f.addWorktree(191, "fix/191-abandoned")
	f.commitIn(wt, "fix.txt", "committed but no pr yet\n")

	info, err := DetectBranchAhead(wt, "fix/191-abandoned", "origin/main")
	if err != nil {
		t.Fatalf("DetectBranchAhead: %v", err)
	}
	if !info.HasOwnCommits || !info.Clean || !info.AheadOfBase {
		t.Fatalf("expected HasOwnCommits=true Clean=true AheadOfBase=true, got %+v", info)
	}
}

func TestDetectBranchAhead_AheadAndDirty(t *testing.T) {
	f := newSweepFixture(t)
	wt := f.addWorktree(192, "fix/192-dirty")
	f.commitIn(wt, "fix.txt", "committed\n")
	writeFile(t, filepath.Join(wt, "scratch.txt"), "uncommitted\n")

	info, err := DetectBranchAhead(wt, "fix/192-dirty", "origin/main")
	if err != nil {
		t.Fatalf("DetectBranchAhead: %v", err)
	}
	if !info.HasOwnCommits || info.Clean || !info.AheadOfBase {
		t.Fatalf("expected HasOwnCommits=true Clean=false AheadOfBase=true, got %+v", info)
	}
}

func TestDetectBranchAhead_NoOwnCommits(t *testing.T) {
	f := newSweepFixture(t)
	wt := f.addWorktree(193, "fix/193-fresh")

	info, err := DetectBranchAhead(wt, "fix/193-fresh", "origin/main")
	if err != nil {
		t.Fatalf("DetectBranchAhead: %v", err)
	}
	if info.HasOwnCommits || !info.Clean || info.AheadOfBase {
		t.Fatalf("expected HasOwnCommits=false Clean=true AheadOfBase=false, got %+v", info)
	}
}

func TestDetectBranchAhead_SquashMergedContentNotAhead(t *testing.T) {
	f := newSweepFixture(t)
	wt := f.addWorktree(194, "fix/194-merged")
	f.commitIn(wt, "fix.txt", "fixed\n")
	f.squashMergeToMain("fix/194-merged")

	info, err := DetectBranchAhead(wt, "fix/194-merged", "origin/main")
	if err != nil {
		t.Fatalf("DetectBranchAhead: %v", err)
	}
	if !info.HasOwnCommits || !info.Clean || info.AheadOfBase {
		t.Fatalf("squash-merged content must not read as ahead of base, got %+v", info)
	}
}

func TestParseWorktreeList(t *testing.T) {
	out := strings.Join([]string{
		"worktree /repo",
		"HEAD aaaa",
		"branch refs/heads/main",
		"",
		"worktree /repo/.worktrees/issue-1",
		"HEAD bbbb",
		"branch refs/heads/fix/1-x",
		"",
		"worktree /repo/.worktrees/issue-2",
		"HEAD cccc",
		"detached",
		"",
		"worktree /repo/.worktrees/issue-3",
		"HEAD dddd",
		"branch refs/heads/fix/3-x",
		"locked reason goes here",
		"prunable gitdir file points to non-existent location",
		"",
	}, "\n")

	got := parseWorktreeList(out)
	if len(got) != 4 {
		t.Fatalf("parsed %d records, want 4: %+v", len(got), got)
	}
	if got[0].Branch != "main" {
		t.Errorf("record 0 branch = %q, want main", got[0].Branch)
	}
	if got[1].Branch != "fix/1-x" || got[1].Detached {
		t.Errorf("record 1 = %+v, want branch fix/1-x, not detached", got[1])
	}
	if !got[2].Detached || got[2].Branch != "" {
		t.Errorf("record 2 = %+v, want detached with no branch", got[2])
	}
	if !got[3].Locked || !got[3].Prunable {
		t.Errorf("record 3 = %+v, want locked and prunable", got[3])
	}
}

// #332 — pipeline exhaust must not veto reclamation, and tracked bookkeeping
// must still block it. ------------------------------------------------------

// writeExhaust scaffolds the pipeline's own untracked bookkeeping into a
// worktree. This is the literal file that deadlocked nine worktrees: the
// knowledge scaffold written at issue pickup, untracked because the sibling
// repos' `.nightgauge/.gitignore` predates the #326 generator and carries no
// `/knowledge/` rule.
func (f *sweepFixture) writeExhaust(worktree string) {
	f.t.Helper()
	writeFile(f.t, filepath.Join(worktree, ".nightgauge", "knowledge", "README.md"), "# Knowledge Base\n")
}

// commitToMain lands a file on main and pushes it, so a worktree created from
// origin/main afterwards has it TRACKED.
func (f *sweepFixture) commitToMain(relPath, content string) {
	f.t.Helper()
	writeFile(f.t, filepath.Join(f.root, relPath), content)
	run(f.t, f.root, "git", "add", relPath)
	run(f.t, f.root, "git", "commit", "-m", "seed: "+relPath)
	run(f.t, f.root, "git", "push", "origin", "main")
	run(f.t, f.root, "git", "fetch", "origin")
}

func TestSweepMergedWorktrees_ReclaimsWorktreeHoldingOnlyPipelineExhaust(t *testing.T) {
	f := newSweepFixture(t)
	wt := f.addWorktree(1181, "fix/1181-thing")
	f.commitIn(wt, "fix.txt", "fixed\n")
	f.squashMergeToMain("fix/1181-thing")
	f.writeExhaust(wt)

	// Arm the trap: git must genuinely report this worktree as dirty, or the
	// test would pass against the pre-#332 code for the wrong reason.
	//
	// `--untracked-files=all` matches what blockingChanges passes, and pinning
	// it is load-bearing rather than tidy: porcelain's DEFAULT collapses an
	// untracked directory to a single `.nightgauge/` entry, so this assertion
	// depends on the ambient `status.showUntrackedFiles` git config — green on
	// a machine that sets `all`, red in CI, for a fixture that is identical
	// either way. Exactly the #223 trap, reached from the test side.
	if status := mustGit(t, wt, "status", "--porcelain", "--untracked-files=all"); !strings.Contains(status, ".nightgauge/knowledge/README.md") {
		t.Fatalf("fixture produced no untracked pipeline exhaust; status = %q", status)
	}

	res, err := SweepMergedWorktrees(WorktreeSweepOptions{RepoRoot: f.root})
	if err != nil {
		t.Fatalf("sweep: %v", err)
	}
	if len(res.Reclaimed) != 1 || res.Reclaimed[0].Path != wt {
		t.Fatalf("a worktree whose only change is pipeline exhaust must be reclaimed, got reclaimed=%+v skipped=%+v",
			res.Reclaimed, res.Skipped)
	}
	// The side effect, not the verdict: the pre-#332 sweep also returned no
	// error while leaving every one of nine worktrees on disk forever.
	if _, err := os.Stat(wt); !os.IsNotExist(err) {
		t.Errorf("worktree still on disk at %s", wt)
	}
	if branches := mustGit(t, f.root, "branch", "--list", "fix/1181-thing"); strings.TrimSpace(branches) != "" {
		t.Errorf("the branch the worktree held survived, so `git branch -D` is still blocked: %q", branches)
	}
}

func TestSweepMergedWorktrees_KeepsTrackedBookkeepingDeliverable(t *testing.T) {
	f := newSweepFixture(t)
	// origin/main tracks pipeline assessments — the #701 shape. Untracking
	// them IS that issue's deliverable, so it must survive the sweep even
	// though every path involved is under .nightgauge/.
	assessment := filepath.Join(".nightgauge", "pipeline", "assessments", "issue-42.json")
	f.commitToMain(assessment, "{}\n")

	wt := f.addWorktree(701, "feat/701-untrack-assessments")
	f.commitIn(wt, "fix.txt", "fixed\n")
	f.squashMergeToMain("feat/701-untrack-assessments")
	run(t, wt, "git", "rm", "--cached", "-q", assessment)
	f.writeExhaust(wt) // exhaust sits alongside the real work; it must not decide

	res, err := SweepMergedWorktrees(WorktreeSweepOptions{RepoRoot: f.root})
	if err != nil {
		t.Fatalf("sweep: %v", err)
	}
	if len(res.Reclaimed) != 0 {
		t.Fatalf("a staged change to a TRACKED bookkeeping file is the deliverable, not exhaust; got %+v", res.Reclaimed)
	}
	assertSkipped(t, res, wt, SkipDirty)
	if _, err := os.Stat(wt); err != nil {
		t.Errorf("the unlanded deliverable was destroyed: %v", err)
	}
	if staged := mustGit(t, wt, "diff", "--cached", "--name-only"); !strings.Contains(staged, "assessments") {
		t.Errorf("staged deletion did not survive; staged = %q", staged)
	}
}

func TestSweepMergedWorktrees_NamesTheBlockingPaths(t *testing.T) {
	f := newSweepFixture(t)
	wt := f.addWorktree(1182, "fix/1182-dirty")
	f.commitIn(wt, "fix.txt", "fixed\n")
	f.squashMergeToMain("fix/1182-dirty")
	writeFile(t, filepath.Join(wt, "fix.txt"), "local edit not committed\n")
	f.writeExhaust(wt)

	res, err := SweepMergedWorktrees(WorktreeSweepOptions{RepoRoot: f.root})
	if err != nil {
		t.Fatalf("sweep: %v", err)
	}
	var skipped SkippedWorktree
	for _, s := range res.Skipped {
		if s.Path == wt {
			skipped = s
		}
	}
	if len(skipped.Blocking) != 1 || skipped.Blocking[0] != "fix.txt" {
		t.Fatalf("a skip must name exactly what blocked it, and exhaust is not a blocker; got %+v", skipped.Blocking)
	}
}

func TestSweepMergedWorktrees_ReclaimsPipelineWorktreeParkedOnDefaultBranch(t *testing.T) {
	f := newSweepFixture(t)
	// Reproduce the production state exactly: the primary is parked on some
	// OTHER branch, because `.worktrees/issue-696` holds `main` and
	// `git checkout main` therefore fails outright ("fatal: 'main' is already
	// used by worktree at …"). The pre-#332 sweep protected it as
	// `protected-branch` forever, so it could never self-heal.
	//
	// Parking the primary elsewhere is load-bearing for the assertion below,
	// not scene-setting: with the primary sitting ON main, git refuses
	// `git branch -D main` all by itself, and a sweep that tried to delete the
	// trunk would look correct here for a reason that does not hold in the
	// workspace this bug was found in.
	run(t, f.root, "git", "checkout", "-q", "-b", "fix/parked-elsewhere")
	wt := filepath.Join(f.root, ".worktrees", "issue-696")
	run(t, f.root, "git", "worktree", "add", wt, "main")
	if _, err := commandOutput(f.root, "git", "checkout", "main"); err == nil {
		t.Fatal("fixture is not the #332 state — the primary can still check out main")
	}

	res, err := SweepMergedWorktrees(WorktreeSweepOptions{RepoRoot: f.root})
	if err != nil {
		t.Fatalf("sweep: %v", err)
	}
	if len(res.Reclaimed) != 1 || res.Reclaimed[0].Path != wt {
		t.Fatalf("a pipeline worktree parked on the default branch must be reclaimable, got reclaimed=%+v skipped=%+v",
			res.Reclaimed, res.Skipped)
	}
	if _, err := os.Stat(wt); !os.IsNotExist(err) {
		t.Errorf("worktree still on disk at %s", wt)
	}
	// The branch is the repository's trunk — removing the stray checkout must
	// never take it with it. This is the assertion that separates the fix from
	// the catastrophe: reclaimWorktree ends in `git branch -D <branch>`.
	if branches := strings.TrimSpace(mustGit(t, f.root, "branch", "--list", "main")); branches == "" {
		t.Fatal("the sweep deleted the default branch")
	}
	if _, err := commandOutput(f.root, "git", "rev-parse", "--verify", "main"); err != nil {
		t.Fatalf("main no longer resolves in the primary checkout: %v", err)
	}
}

func TestSweepMergedWorktrees_KeepsHandmadeWorktreeOnDefaultBranch(t *testing.T) {
	f := newSweepFixture(t)
	// Same state, but the directory name encodes no issue number, so a
	// developer made it deliberately. Reordering the pipeline-managed test
	// ahead of the protected-branch test must not widen the sweep to these.
	wt := filepath.Join(f.root, ".worktrees", "my-main-checkout")
	run(t, f.root, "git", "worktree", "add", "--force", wt, "main")

	res, err := SweepMergedWorktrees(WorktreeSweepOptions{RepoRoot: f.root})
	if err != nil {
		t.Fatalf("sweep: %v", err)
	}
	if len(res.Reclaimed) != 0 {
		t.Fatalf("a hand-made worktree on the default branch must stay protected, got %+v", res.Reclaimed)
	}
	assertSkipped(t, res, wt, SkipProtectedBranch)
	if _, err := os.Stat(wt); err != nil {
		t.Errorf("hand-made worktree was removed: %v", err)
	}
}

func TestCleanupWorktree_RemovesWorktreeHoldingOnlyPipelineExhaust(t *testing.T) {
	// The inline half of the same defect. CleanupWorktree runs on the path a
	// finished run walks, so exhaust preserving the worktree here is how the
	// leak is manufactured — one completed run at a time.
	f := newSweepFixture(t)
	m := &Manager{workspaceRoot: f.root}
	wt := m.worktreePath("owner/clone", 1252)
	run(t, f.root, "git", "worktree", "add", wt, "-b", "fix/1252", "origin/main")
	f.writeExhaust(wt)

	if err := m.CleanupWorktree("owner/clone", 1252); err != nil {
		t.Fatalf("CleanupWorktree: %v", err)
	}
	if _, err := os.Stat(wt); !os.IsNotExist(err) {
		t.Errorf("teardown preserved a worktree holding only the pipeline's own exhaust: %s", wt)
	}
}

func TestCleanupWorktree_PreservesTrackedBookkeepingChange(t *testing.T) {
	f := newSweepFixture(t)
	assessment := filepath.Join(".nightgauge", "pipeline", "assessments", "issue-9.json")
	f.commitToMain(assessment, "{}\n")

	m := &Manager{workspaceRoot: f.root}
	wt := m.worktreePath("owner/clone", 1253)
	run(t, f.root, "git", "worktree", "add", wt, "-b", "fix/1253", "origin/main")
	run(t, wt, "git", "rm", "--cached", "-q", assessment)

	if err := m.CleanupWorktree("owner/clone", 1253); err != nil {
		t.Fatalf("CleanupWorktree: %v", err)
	}
	if _, err := os.Stat(wt); err != nil {
		t.Errorf("teardown destroyed a bookkeeping-only deliverable: %v", err)
	}
}

// helpers ---------------------------------------------------------------

func assertSkipped(t *testing.T, res WorktreeSweepResult, path string, want SkipReason) {
	t.Helper()
	for _, s := range res.Skipped {
		if s.Path == path {
			if s.Reason != want {
				t.Errorf("skip reason for %s = %q, want %q", path, s.Reason, want)
			}
			return
		}
	}
	t.Errorf("%s not reported as skipped; skipped=%+v", path, res.Skipped)
}

// captureLog collects everything the standard logger emits while fn runs.
func captureLog(t *testing.T, fn func()) string {
	t.Helper()
	var buf bytes.Buffer
	origOut := log.Writer()
	origFlags := log.Flags()
	log.SetOutput(&buf)
	log.SetFlags(0)
	defer func() {
		log.SetOutput(origOut)
		log.SetFlags(origFlags)
	}()
	fn()
	return buf.String()
}

func commandOutput(dir, name string, args ...string) (string, error) {
	cmd := exec.Command(name, args...)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	return string(out), err
}

func run(t *testing.T, dir string, name string, args ...string) {
	t.Helper()
	if out, err := commandOutput(dir, name, args...); err != nil {
		t.Fatalf("%s %s (in %s): %v: %s", name, strings.Join(args, " "), dir, err, out)
	}
}

func mustGit(t *testing.T, dir string, args ...string) string {
	t.Helper()
	out, err := commandOutput(dir, "git", args...)
	if err != nil {
		t.Fatalf("git %s: %v: %s", strings.Join(args, " "), err, out)
	}
	return out
}

func configureGit(t *testing.T, dir string) {
	t.Helper()
	run(t, dir, "git", "config", "user.email", "test@test")
	run(t, dir, "git", "config", "user.name", "test")
}

func mustMkdir(t *testing.T, dir string) {
	t.Helper()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", dir, err)
	}
}

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	mustMkdir(t, filepath.Dir(path))
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}
