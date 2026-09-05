package execution

import (
	"bytes"
	"encoding/json"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/nightgauge/nightgauge/internal/gittest"
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
	// #410: the reclaim must say WHICH door authorized it. This one really did
	// compare content, and it is the only door for which "content already on
	// <base>" is a true statement.
	if got := res.Reclaimed[0].Door; got != ReclaimContentMerged {
		t.Errorf("Door = %q, want %q", got, ReclaimContentMerged)
	}
	if _, err := os.Stat(wt); !os.IsNotExist(err) {
		t.Errorf("worktree directory still on disk at %s", wt)
	}
	if branches := mustGit(t, f.root, "branch", "--list", "fix/110-thing"); strings.TrimSpace(branches) != "" {
		t.Errorf("local branch survived the sweep: %q", branches)
	}
}

func TestSweepMergedWorktrees_ReclaimsUpdateBranchThenSquash(t *testing.T) {
	// #583: after `gh pr update-branch` the branch tip is a merge commit from
	// main; after squash, origin/main is a new commit and later main work
	// makes a full-tree two-dot non-empty. Path-restricted compare of the
	// branch's own files is empty — that is reclaimable.
	f := newSweepFixture(t)
	wt := f.addWorktree(583, "fix/583-update-branch")
	f.commitIn(wt, "feature.txt", "work\n")

	f.commitToMain("extra.txt", "unrelated\n")
	run(t, wt, "git", "merge", "origin/main", "-m", "merge origin/main")
	if merges := strings.TrimSpace(mustGit(t, wt, "rev-list", "--merges", "-n1", "HEAD")); merges == "" {
		t.Fatal("expected an update-branch-style merge commit on the feature branch")
	}

	f.squashMergeToMain("fix/583-update-branch")
	f.commitToMain("later.txt", "after squash\n")

	twoDot := strings.TrimSpace(mustGit(t, f.root, "diff", "--stat", "origin/main..fix/583-update-branch"))
	if twoDot == "" {
		t.Fatal("fixture did not arm the two-dot trap — origin/main..branch is empty")
	}
	if _, err := gitOutput(f.root, "merge-base", "--is-ancestor", "fix/583-update-branch", "origin/main"); err == nil {
		t.Fatal("fixture is not a real squash merge — branch tip is an ancestor of origin/main")
	}

	res, err := SweepMergedWorktrees(WorktreeSweepOptions{RepoRoot: f.root})
	if err != nil {
		t.Fatalf("sweep: %v", err)
	}
	if len(res.Reclaimed) != 1 || res.Reclaimed[0].Branch != "fix/583-update-branch" {
		t.Fatalf("expected the update-branch+squash worktree to be reclaimed, got reclaimed=%+v skipped=%+v",
			res.Reclaimed, res.Skipped)
	}
	if got := res.Reclaimed[0].Door; got != ReclaimContentMerged {
		t.Errorf("Door = %q, want %q", got, ReclaimContentMerged)
	}
	if _, err := os.Stat(wt); !os.IsNotExist(err) {
		t.Errorf("worktree directory still on disk at %s", wt)
	}
}

func TestSweepMergedWorktrees_FetchesStaleOriginMain(t *testing.T) {
	f := newSweepFixture(t)
	wt := f.addWorktree(584, "fix/584-stale-origin")
	f.commitIn(wt, "feature.txt", "work\n")

	stale := strings.TrimSpace(mustGit(t, f.root, "rev-parse", "refs/remotes/origin/main"))
	f.squashMergeToMain("fix/584-stale-origin")
	run(t, f.root, "git", "update-ref", "refs/remotes/origin/main", stale)

	res, err := SweepMergedWorktrees(WorktreeSweepOptions{RepoRoot: f.root})
	if err != nil {
		t.Fatalf("sweep: %v", err)
	}
	if res.BaseRefFetchError != "" {
		t.Fatalf("fetch should succeed against the local fixture origin, got %q", res.BaseRefFetchError)
	}
	if len(res.Reclaimed) != 1 || res.Reclaimed[0].Branch != "fix/584-stale-origin" {
		t.Fatalf("expected fetch to refresh origin/main and reclaim, got reclaimed=%+v skipped=%+v",
			res.Reclaimed, res.Skipped)
	}
	if _, err := os.Stat(wt); !os.IsNotExist(err) {
		t.Errorf("worktree directory still on disk at %s", wt)
	}
}

func TestSweepMergedWorktrees_ReportsFetchFailureAndContinues(t *testing.T) {
	f := newSweepFixture(t)
	wt := f.addWorktree(585, "fix/585-fetch-fail")
	f.commitIn(wt, "feature.txt", "work\n")

	stale := strings.TrimSpace(mustGit(t, f.root, "rev-parse", "refs/remotes/origin/main"))
	f.squashMergeToMain("fix/585-fetch-fail")
	run(t, f.root, "git", "update-ref", "refs/remotes/origin/main", stale)
	run(t, f.root, "git", "remote", "set-url", "origin", "file:///dev/null")

	res, err := SweepMergedWorktrees(WorktreeSweepOptions{RepoRoot: f.root})
	if err != nil {
		t.Fatalf("sweep must continue after fetch failure, got %v", err)
	}
	if res.BaseRefFetchError == "" {
		t.Fatal("expected BaseRefFetchError when origin is unreachable")
	}
	assertSkipped(t, res, wt, SkipUnmergedContent)
	if _, err := os.Stat(wt); err != nil {
		t.Errorf("worktree was removed despite a stale base ref: %v", err)
	}
}

func TestSweepMergedWorktrees_ReclaimsViaMergedPRLookupWhenBaseRewroteFiles(t *testing.T) {
	f := newSweepFixture(t)
	wt := f.addWorktree(586, "fix/586-pr-door")
	f.commitIn(wt, "feature.txt", "original\n")
	f.squashMergeToMain("fix/586-pr-door")
	f.commitToMain("feature.txt", "rewritten on main\n")

	tip := strings.TrimSpace(mustGit(t, f.root, "rev-parse", "fix/586-pr-door"))

	res, err := SweepMergedWorktrees(WorktreeSweepOptions{RepoRoot: f.root})
	if err != nil {
		t.Fatalf("sweep: %v", err)
	}
	assertSkipped(t, res, wt, SkipUnmergedContent)

	res, err = SweepMergedWorktrees(WorktreeSweepOptions{
		RepoRoot: f.root,
		// #593: the lookup contract changed from (branch, tip)->(headSHA, ok)
		// to (branch)->(headSHA, parents, ok) so ancestry-containment (tip is
		// one of head's parents), not just SHA equality, can authorize this
		// door. The equal-SHA case this test exercises is still accepted —
		// headSHA == tip is the first branch lookupMergedPR checks.
		MergedPRLookup: func(branch string) (string, []string, bool) {
			if branch != "fix/586-pr-door" {
				return "", nil, false
			}
			return tip, nil, true
		},
	})
	if err != nil {
		t.Fatalf("sweep with lookup: %v", err)
	}
	if len(res.Reclaimed) != 1 || res.Reclaimed[0].Branch != "fix/586-pr-door" {
		t.Fatalf("expected PR-tip door to reclaim, got reclaimed=%+v skipped=%+v",
			res.Reclaimed, res.Skipped)
	}
	if got := res.Reclaimed[0].Door; got != ReclaimContentMerged {
		t.Errorf("Door = %q, want %q", got, ReclaimContentMerged)
	}
}

// TestSweepMergedWorktrees_ReclaimsViaAncestryWhenUpdateBranchNeverReachedLocal
// is the #593 regression: branch B is cut BEFORE another PR (P) lands on
// main and evolves a file B also touches; B's PR is `gh pr update-branch`'d
// and squash-merged. update-branch creates its merge commit on the PR's
// REMOTE head — the pipeline's local worktree never fetches it, so the local
// branch ref stays at its pre-update-branch tip forever. mergedIntoBase's
// content diff then compares that stale tip's copy of the shared file
// (missing P's edit) against main's post-squash copy (carrying both edits)
// and correctly reports them as different — content alone cannot tell "tip
// is missing P's later change" apart from "tip carries unmerged work" here.
// Only the forge knows the real merged PR head and its parents.
func TestSweepMergedWorktrees_ReclaimsViaAncestryWhenUpdateBranchNeverReachedLocal(t *testing.T) {
	f := newSweepFixture(t)
	// Establish the shared file before the branch is cut, so both B and P can
	// edit different regions of it without conflicting.
	f.commitToMain("shared.txt", "line1\nline2\nline3\nline4\nline5\n")

	wt := f.addWorktree(585, "fix/585-cost-stamps")
	writeFile(t, filepath.Join(wt, "shared.txt"), "line1\nline2\nline3\nline4\nline5-edited-by-B\n")
	run(t, wt, "git", "add", "shared.txt")
	run(t, wt, "git", "commit", "-m", "work: cost stamps")
	// tip is the LOCAL branch ref's commit — it never advances past this,
	// because update-branch happens on the forge side, not in this worktree.
	tip := strings.TrimSpace(mustGit(t, wt, "rev-parse", "HEAD"))

	// P — an unrelated PR — lands directly on main, evolving a DIFFERENT
	// region of the same shared file after B was cut.
	f.commitToMain("shared.txt", "line1-edited-by-P\nline2\nline3\nline4\nline5\n")

	// Simulate `gh pr update-branch`: build the real 3-way merge commit GitHub
	// would have produced on the PR's remote head, WITHOUT touching
	// refs/heads/fix/585-cost-stamps — a detached scratch worktree keeps the
	// pipeline's actual branch ref untouched, which is the whole bug: local
	// git has no ref that ever points at this commit.
	scratchWt := filepath.Join(t.TempDir(), "pr-remote-head")
	run(t, f.root, "git", "worktree", "add", "--detach", scratchWt, tip)
	run(t, scratchWt, "git", "merge", "origin/main", "-m", "merge origin/main (update-branch)")
	prHead := strings.TrimSpace(mustGit(t, scratchWt, "rev-parse", "HEAD"))
	prParents := strings.Fields(strings.TrimSpace(mustGit(t, f.root, "log", "-1", "--format=%P", prHead)))
	run(t, f.root, "git", "worktree", "remove", scratchWt, "--force")
	if len(prParents) != 2 {
		t.Fatalf("expected the update-branch merge commit to have 2 parents, got %v", prParents)
	}
	found := false
	for _, p := range prParents {
		if p == tip {
			found = true
		}
	}
	if !found {
		t.Fatalf("fixture bug: local branch tip %s is not among the simulated PR head's parents %v", tip, prParents)
	}

	// GitHub's squash merge applies the PR head's TREE (both edits combined)
	// as one new commit on main — not the local branch's tree.
	run(t, f.root, "git", "merge", "--squash", prHead)
	run(t, f.root, "git", "commit", "-m", "squash: fix/585-cost-stamps")
	run(t, f.root, "git", "push", "origin", "main")
	run(t, f.root, "git", "fetch", "origin")

	// Arm the trap: content diff alone must still read this as unmerged,
	// exactly like the live evidence in #593.
	res, err := SweepMergedWorktrees(WorktreeSweepOptions{RepoRoot: f.root})
	if err != nil {
		t.Fatalf("sweep: %v", err)
	}
	assertSkipped(t, res, wt, SkipUnmergedContent)

	res, err = SweepMergedWorktrees(WorktreeSweepOptions{
		RepoRoot: f.root,
		MergedPRLookup: func(branch string) (string, []string, bool) {
			if branch != "fix/585-cost-stamps" {
				return "", nil, false
			}
			return prHead, prParents, true
		},
	})
	if err != nil {
		t.Fatalf("sweep with ancestry lookup: %v", err)
	}
	if len(res.Reclaimed) != 1 || res.Reclaimed[0].Branch != "fix/585-cost-stamps" {
		t.Fatalf("expected ancestry-containment to reclaim the branch, got reclaimed=%+v skipped=%+v",
			res.Reclaimed, res.Skipped)
	}
	if got := res.Reclaimed[0].Door; got != ReclaimContentMerged {
		t.Errorf("Door = %q, want %q", got, ReclaimContentMerged)
	}
	if _, err := os.Stat(wt); !os.IsNotExist(err) {
		t.Errorf("worktree directory still on disk at %s", wt)
	}
}

func TestBranchHeldByWorktree(t *testing.T) {
	f := newSweepFixture(t)
	wt := f.addWorktree(587, "fix/587-held")

	path, held, err := BranchHeldByWorktree(f.root, "fix/587-held")
	if err != nil {
		t.Fatalf("BranchHeldByWorktree: %v", err)
	}
	if !held || path != wt {
		t.Fatalf("BranchHeldByWorktree = (%q, %v), want (%q, true)", path, held, wt)
	}

	_, held, err = BranchHeldByWorktree(f.root, "fix/no-such-branch")
	if err != nil {
		t.Fatalf("BranchHeldByWorktree: %v", err)
	}
	if held {
		t.Fatal("a branch no worktree holds must report held=false")
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
	// #410: the dry-run append site is a SECOND place the verdict is projected
	// onto the result, and it dropped KeepBranch before it dropped Door. A
	// preview whose door is empty is a preview an operator cannot audit.
	if got := res.Reclaimed[0].Door; got != ReclaimContentMerged {
		t.Errorf("dry-run Door = %q, want %q", got, ReclaimContentMerged)
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
	// A worktree directory that EXISTS but that git does not know about (the
	// root is not a git repo): `git worktree remove` fails, the manual fallback
	// succeeds and CleanupWorktree returns nil. The only observable signal is
	// the log line — and this is the population it has to mean something for.
	//
	// The directory is created deliberately (#400): teardown of a worktree that
	// was never created returns early and says nothing, so pointing this case at
	// a missing directory would pin the false alarm instead of the real signal.
	root := t.TempDir()
	m := &Manager{workspaceRoot: root}
	if err := os.MkdirAll(m.worktreePath("nightgauge/nightgauge", 110), 0o755); err != nil {
		t.Fatalf("create worktree dir: %v", err)
	}

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
	// #410: this door compared NOTHING — the default branch is the comparison
	// base, so mergedIntoBase structurally cannot apply. The record must say so,
	// or the sweep's only operator-visible line about its most destructive
	// reclaim claims a check that never ran.
	if got := res.Reclaimed[0].Door; got != ReclaimDefaultBranchCheckout {
		t.Errorf("Door = %q, want %q", got, ReclaimDefaultBranchCheckout)
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

// commandOutput runs name(args...) in dir. Every call site in this package
// passes "git" — routed through gittest.Command so it inherits the
// background-maintenance disarming and ambient-config isolation every
// test-created repo in this suite needs (#680, #542). A non-git name falls
// back to a plain exec.Command; nothing currently exercises that path, but
// the signature stays generic rather than narrowing to git-only.
func commandOutput(dir, name string, args ...string) (string, error) {
	var cmd *exec.Cmd
	if name == "git" {
		cmd = gittest.Command(dir, args...)
	} else {
		cmd = exec.Command(name, args...)
		cmd.Dir = dir
	}
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

// TestSweepMergedWorktrees_ActiveRunSkipCarriesTheProtectingArm is the operator
// half of #443. Every protected worktree reported the identical `active-run`
// skip, so "the stage child is executing right now" and "a paused snapshot from
// thirteen days ago exists" were indistinguishable in both the text and the
// --json surface — the operator had to open the state directory to audit a
// refusal. The arm travels with the skip or the refusal is unfalsifiable.
func TestSweepMergedWorktrees_ActiveRunSkipCarriesTheProtectingArm(t *testing.T) {
	f := newSweepFixture(t)
	arms := map[int]string{
		1141: "stage-child, pid 4312",
		1142: "paused-snapshot, 13d",
		1143: "corrupt-snapshot, 1h",
	}
	paths := map[int]string{}
	active := map[int]bool{}
	for issue := range arms {
		branch := "fix/" + strconv.Itoa(issue) + "-protected"
		wt := f.addWorktree(issue, branch)
		f.commitIn(wt, "fix-"+strconv.Itoa(issue)+".txt", "fixed\n")
		f.squashMergeToMain(branch)
		paths[issue] = wt
		active[issue] = true
	}

	res, err := SweepMergedWorktrees(WorktreeSweepOptions{
		RepoRoot:     f.root,
		ActiveIssues: active,
		Protected:    arms,
	})
	if err != nil {
		t.Fatalf("sweep: %v", err)
	}
	if len(res.Reclaimed) != 0 {
		t.Fatalf("no protected worktree may be reclaimed, got %+v", res.Reclaimed)
	}
	for issue, want := range arms {
		assertSkippedDetail(t, res, paths[issue], SkipActiveRun, want)
	}

	// The field is omitempty and every other skip leaves it empty: a detail on a
	// skip nobody attributed would be a claim with no source.
	blob, err := json.Marshal(res.Skipped)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(blob), `"reasonDetail":"paused-snapshot, 13d"`) {
		t.Errorf("--json must carry the arm beside the reason; got %s", blob)
	}
	if strings.Contains(string(blob), `"reason":"primary-checkout","reasonDetail"`) {
		t.Errorf("a skip nobody attributed must carry no detail; got %s", blob)
	}
}

// TestSweepMergedWorktrees_ActiveRunWithNoArmSaysNothing: the in-process
// scheduler sweep protects from its own registry and has no snapshot arm to
// name. An empty detail must stay empty rather than inventing an attribution.
func TestSweepMergedWorktrees_ActiveRunWithNoArmSaysNothing(t *testing.T) {
	f := newSweepFixture(t)
	wt := f.addWorktree(1144, "fix/1144-registry-protected")
	f.commitIn(wt, "fix.txt", "fixed\n")
	f.squashMergeToMain("fix/1144-registry-protected")

	res, err := SweepMergedWorktrees(WorktreeSweepOptions{
		RepoRoot:     f.root,
		ActiveIssues: map[int]bool{1144: true},
	})
	if err != nil {
		t.Fatalf("sweep: %v", err)
	}
	assertSkippedDetail(t, res, wt, SkipActiveRun, "")
}

func assertSkippedDetail(t *testing.T, res WorktreeSweepResult, path string, want SkipReason, wantDetail string) {
	t.Helper()
	for _, s := range res.Skipped {
		if s.Path != path {
			continue
		}
		if s.Reason != want {
			t.Errorf("skip reason for %s = %q, want %q", path, s.Reason, want)
		}
		if s.ReasonDetail != wantDetail {
			t.Errorf("skip detail for %s = %q, want %q", path, s.ReasonDetail, wantDetail)
		}
		return
	}
	t.Errorf("%s not reported as skipped; skipped=%+v", path, res.Skipped)
}
