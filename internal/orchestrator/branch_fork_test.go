package orchestrator

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/nightgauge/nightgauge/internal/gittest"
)

// These tests drive REAL git against a local bare "origin". A fork is a
// property of two object graphs, and a mocked git would only prove the mock
// agrees with the assertion — the pre-#163 failure was precisely that nobody
// had compared the two graphs. The bare-repo remote keeps it hermetic (no
// network) while exercising the same `ls-remote` / `merge-base --is-ancestor`
// plumbing production uses.

// commitFile writes a file and commits it, returning the new HEAD SHA.
func commitFile(t *testing.T, dir, name, content, msg string) string {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o644); err != nil {
		t.Fatalf("write %s: %v", name, err)
	}
	gittest.Run(t, dir, "add", "-A")
	gittest.Run(t, dir, "commit", "-m", msg)
	return gittest.Run(t, dir, "rev-parse", "HEAD")
}

// forkFixture is an origin bare repo with one `main` commit, plus a working
// clone. Call clone() for a second working copy standing in for the killed
// run that already pushed, or for an operator.
type forkFixture struct {
	root   string
	origin string
	work   string
	t      *testing.T
	n      int
}

func newForkFixture(t *testing.T) *forkFixture {
	t.Helper()
	root := t.TempDir()
	origin := filepath.Join(root, "origin.git")
	gittest.Run(t, root, "init", "--bare", "-b", "main", origin)

	work := filepath.Join(root, "work")
	gittest.Run(t, root, "clone", origin, work)
	gittest.Run(t, work, "config", "user.email", "test@example.com")
	gittest.Run(t, work, "config", "user.name", "Test")
	commitFile(t, work, "README.md", "base\n", "base")
	gittest.Run(t, work, "push", "origin", "main")

	return &forkFixture{root: root, origin: origin, work: work, t: t}
}

// clone returns a second working copy of origin, checked out on main.
func (f *forkFixture) clone() string {
	f.t.Helper()
	f.n++
	dir := filepath.Join(f.root, "other")
	if f.n > 1 {
		dir = filepath.Join(f.root, "other"+string(rune('0'+f.n)))
	}
	gittest.Run(f.t, f.root, "clone", f.origin, dir)
	gittest.Run(f.t, dir, "config", "user.email", "other@example.com")
	gittest.Run(f.t, dir, "config", "user.name", "Other")
	return dir
}

// -----------------------------------------------------------------------
// CheckBranchFork
// -----------------------------------------------------------------------

// TestCheckBranchFork_DetectsWorktreeBaseBehindRemoteHead is the #163 shape:
// a prior stage was killed mid-push having ALREADY pushed, so origin carries a
// commit the retry's worktree has never seen. The retry's branch sits at the
// base. Every push it makes is doomed — and the whole point is that this is
// established at the stage boundary, from the branch's base, WITHOUT the
// worktree having produced or pushed anything.
func TestCheckBranchFork_DetectsWorktreeBaseBehindRemoteHead(t *testing.T) {
	f := newForkFixture(t)
	const branch = "fix/163-orphaned-push"

	// The killed run's orphan: a commit pushed to the branch that this
	// worktree never saw.
	other := f.clone()
	gittest.Run(t, other, "checkout", "-b", branch)
	orphan := commitFile(t, other, "impl.go", "// implementation A\n", "feat: implementation A")
	gittest.Run(t, other, "push", "origin", branch)

	// The retry: branch created from main, nothing done yet, remote never fetched.
	gittest.Run(t, f.work, "checkout", "-b", branch)
	base := gittest.Run(t, f.work, "rev-parse", "HEAD")

	fork := CheckBranchFork(context.Background(), f.work, branch)
	if !fork.Forked() {
		t.Fatalf("expected the branch to be detected as forked BEFORE any work; got state=%q detail=%q",
			fork.State, fork.Detail)
	}
	if fork.RemoteSHA != orphan {
		t.Errorf("RemoteSHA = %q, want the orphaned push %q", fork.RemoteSHA, orphan)
	}
	if fork.LocalSHA != base {
		t.Errorf("LocalSHA = %q, want the worktree base %q", fork.LocalSHA, base)
	}
	if !strings.Contains(fork.Detail, "non-fast-forward") {
		t.Errorf("Detail = %q, want it to name the rejection the run would otherwise hit at push time", fork.Detail)
	}
}

// TestCheckBranchFork_DetectsForkAfterFetch covers the same divergence when the
// remote head IS present locally (the worktree fetched at some point). This
// exercises the `merge-base --is-ancestor` leg rather than the missing-object
// leg, so a fork is caught whether or not the object was ever fetched.
func TestCheckBranchFork_DetectsForkAfterFetch(t *testing.T) {
	f := newForkFixture(t)
	const branch = "fix/163-diverged"

	other := f.clone()
	gittest.Run(t, other, "checkout", "-b", branch)
	commitFile(t, other, "impl.go", "// implementation A\n", "feat: implementation A")
	gittest.Run(t, other, "push", "origin", branch)

	gittest.Run(t, f.work, "checkout", "-b", branch)
	commitFile(t, f.work, "impl.go", "// implementation B\n", "feat: implementation B")
	gittest.Run(t, f.work, "fetch", "origin", branch) // remote head now in the local object store

	fork := CheckBranchFork(context.Background(), f.work, branch)
	if !fork.Forked() {
		t.Fatalf("expected diverged-both-ways to be detected as forked; got state=%q detail=%q",
			fork.State, fork.Detail)
	}
}

// TestCheckBranchFork_CleanWhenRemoteHeadIsAncestor asserts the ordinary
// pipeline shape stays clean: the run pushed its own commit, so origin's head
// is reachable from the local tip and the next stage proceeds. A pre-flight
// that fires here would block every healthy run.
func TestCheckBranchFork_CleanWhenRemoteHeadIsAncestor(t *testing.T) {
	f := newForkFixture(t)
	const branch = "feat/ordinary"

	gittest.Run(t, f.work, "checkout", "-b", branch)
	commitFile(t, f.work, "impl.go", "// work\n", "feat: work")
	gittest.Run(t, f.work, "push", "origin", branch)

	if fork := CheckBranchFork(context.Background(), f.work, branch); fork.Forked() {
		t.Fatalf("a branch the run itself pushed must be clean; got %q (%s)", fork.State, fork.Detail)
	}

	// Still clean once the worktree moves ahead of what it pushed.
	commitFile(t, f.work, "impl.go", "// more work\n", "feat: more work")
	if fork := CheckBranchFork(context.Background(), f.work, branch); fork.Forked() {
		t.Fatalf("a local tip AHEAD of origin must be clean; got %q (%s)", fork.State, fork.Detail)
	}
}

// TestCheckBranchFork_CleanWhenRemoteBranchAbsent covers the first push of a
// run: origin has no such branch, so there is nothing to fork from.
func TestCheckBranchFork_CleanWhenRemoteBranchAbsent(t *testing.T) {
	f := newForkFixture(t)
	const branch = "feat/never-pushed"
	gittest.Run(t, f.work, "checkout", "-b", branch)

	fork := CheckBranchFork(context.Background(), f.work, branch)
	if fork.State != ForkStateClean {
		t.Fatalf("State = %q, want clean when origin has no such branch (detail: %s)", fork.State, fork.Detail)
	}
}

// TestCheckBranchFork_FailsOpen asserts every inability to ESTABLISH the
// comparison degrades to unknown, never to forked. The pre-flight blocks a run;
// an offline laptop or a missing branch must not be able to trigger that.
func TestCheckBranchFork_FailsOpen(t *testing.T) {
	f := newForkFixture(t)

	cases := []struct {
		name   string
		dir    string
		branch string
	}{
		{"not a git repo", t.TempDir(), "feat/x"},
		{"no such local branch", f.work, "feat/does-not-exist"},
		{"empty branch", f.work, ""},
		{"empty dir", "", "feat/x"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			fork := CheckBranchFork(context.Background(), tc.dir, tc.branch)
			if fork.State != ForkStateUnknown {
				t.Errorf("State = %q, want unknown (fail-open); detail=%q", fork.State, fork.Detail)
			}
			if fork.Forked() {
				t.Errorf("an unestablished comparison must never report forked")
			}
		})
	}
}

// -----------------------------------------------------------------------
// ReclaimOrphanedRemoteBranch
// -----------------------------------------------------------------------

// TestReclaimOrphanedRemoteBranch_DropsThePipelinesOwnPush is the kill-path
// cleanup: the run committed and pushed, then died. Its remote head is
// contained in its local history, which is the proof of ownership, so origin's
// copy is dropped and the next attempt starts from a clean base.
func TestReclaimOrphanedRemoteBranch_DropsThePipelinesOwnPush(t *testing.T) {
	f := newForkFixture(t)
	const branch = "fix/163-killed-mid-push"

	gittest.Run(t, f.work, "checkout", "-b", branch)
	commitFile(t, f.work, "impl.go", "// half-done\n", "feat: partial")
	gittest.Run(t, f.work, "push", "origin", branch)

	res := ReclaimOrphanedRemoteBranch(context.Background(), f.work, branch)
	if !res.Deleted {
		t.Fatalf("expected the pipeline's own orphaned push to be reclaimed; reason=%s", res.Reason)
	}
	if out := gittest.Run(t, f.work, "ls-remote", "--heads", "origin", "refs/heads/"+branch); out != "" {
		t.Errorf("origin still has %s after reclamation: %s", branch, out)
	}

	// And the next attempt, starting from the base, now sees a clean branch —
	// this is the whole point of the reclamation.
	gittest.Run(t, f.work, "checkout", "main")
	gittest.Run(t, f.work, "branch", "-D", branch)
	gittest.Run(t, f.work, "checkout", "-b", branch)
	if fork := CheckBranchFork(context.Background(), f.work, branch); fork.Forked() {
		t.Errorf("after reclamation the retry must see a clean branch; got %q (%s)", fork.State, fork.Detail)
	}
}

// TestReclaimOrphanedRemoteBranch_LeavesACommitTheRunNeverAuthored is the
// safety property that makes the reclamation legitimate. The second observed
// #163 cause is an operator pushing to a pipeline-owned branch; deleting that
// would destroy work the pipeline cannot recreate. Containment is what tells
// the two apart, and a commit that is not contained is left standing for the
// fork pre-flight to report.
func TestReclaimOrphanedRemoteBranch_LeavesACommitTheRunNeverAuthored(t *testing.T) {
	f := newForkFixture(t)
	const branch = "fix/163-operator-push"

	other := f.clone()
	gittest.Run(t, other, "checkout", "-b", branch)
	foreign := commitFile(t, other, "operator.md", "operator work\n", "docs: operator edit")
	gittest.Run(t, other, "push", "origin", branch)

	// The run's local branch sits at the base and has fetched the remote head,
	// so the object is present — the decision must rest on ancestry, not on
	// whether the object happens to be local.
	gittest.Run(t, f.work, "checkout", "-b", branch)
	gittest.Run(t, f.work, "fetch", "origin", branch)

	res := ReclaimOrphanedRemoteBranch(context.Background(), f.work, branch)
	if res.Deleted {
		t.Fatalf("reclamation must NOT delete a commit this run never authored")
	}
	if !strings.Contains(res.Reason, "never authored") {
		t.Errorf("Reason = %q, want it to say why the deletion was declined", res.Reason)
	}
	if out := gittest.Run(t, f.work, "ls-remote", "--heads", "origin", "refs/heads/"+branch); !strings.Contains(out, foreign) {
		t.Fatalf("the operator's commit %s must survive; ls-remote = %q", foreign, out)
	}
	// The declined case is exactly the one the pre-flight must report.
	if fork := CheckBranchFork(context.Background(), f.work, branch); !fork.Forked() {
		t.Errorf("a foreign remote head must still be reported as a fork; got %q", fork.State)
	}
}

// TestReclaimOrphanedRemoteBranch_RefusesProtectedBranches covers the branches
// no failed run may ever delete. Epic branches are included: they are the
// shared base for every sub-issue in the epic, so one sub-issue's failure must
// not remove the ground its siblings stand on.
func TestReclaimOrphanedRemoteBranch_RefusesProtectedBranches(t *testing.T) {
	f := newForkFixture(t)
	for _, branch := range []string{"main", "master", "develop", "HEAD", "epic/142-wave-one"} {
		res := ReclaimOrphanedRemoteBranch(context.Background(), f.work, branch)
		if res.Deleted {
			t.Errorf("reclaimed protected branch %q — must never happen", branch)
		}
		if !strings.Contains(res.Reason, "protected") {
			t.Errorf("branch %q: Reason = %q, want it to name the protection", branch, res.Reason)
		}
	}
	// main must still be on origin.
	if out := gittest.Run(t, f.work, "ls-remote", "--heads", "origin", "refs/heads/main"); out == "" {
		t.Fatal("origin/main was deleted")
	}
}

// TestReclaimOrphanedRemoteBranch_NoopWhenNothingWasPushed covers the common
// failed run: it never got as far as pushing, so there is no orphan.
func TestReclaimOrphanedRemoteBranch_NoopWhenNothingWasPushed(t *testing.T) {
	f := newForkFixture(t)
	const branch = "feat/never-pushed"
	gittest.Run(t, f.work, "checkout", "-b", branch)
	commitFile(t, f.work, "impl.go", "// local only\n", "feat: local only")

	res := ReclaimOrphanedRemoteBranch(context.Background(), f.work, branch)
	if res.Deleted {
		t.Fatalf("nothing was pushed — there is nothing to reclaim")
	}
	if !strings.Contains(res.Reason, "nothing orphaned") {
		t.Errorf("Reason = %q, want it to say origin has no such branch", res.Reason)
	}
}

// TestReclaimOrphanedRemoteBranch_DeclinesWhenLocalBranchIsGone asserts the
// reclamation cannot fall back to "delete it anyway" once ownership is
// unprovable — without the local branch there is no ancestry to check, and a
// remote head of unknown provenance is exactly what must be preserved.
func TestReclaimOrphanedRemoteBranch_DeclinesWhenLocalBranchIsGone(t *testing.T) {
	f := newForkFixture(t)
	const branch = "fix/163-local-gone"

	gittest.Run(t, f.work, "checkout", "-b", branch)
	commitFile(t, f.work, "impl.go", "// work\n", "feat: work")
	gittest.Run(t, f.work, "push", "origin", branch)
	gittest.Run(t, f.work, "checkout", "main")
	gittest.Run(t, f.work, "branch", "-D", branch)

	res := ReclaimOrphanedRemoteBranch(context.Background(), f.work, branch)
	if res.Deleted {
		t.Fatalf("ownership is unprovable without the local branch — must decline")
	}
	if !strings.Contains(res.Reason, "cannot prove") {
		t.Errorf("Reason = %q, want it to name the missing proof", res.Reason)
	}
}
