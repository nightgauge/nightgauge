package reclaim

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// #1105. These drive real git for the same reason the stash tests do: the
// defect is state that exists only in a repository — refs under
// refs/nightgauge/wip/ that nothing ever read — and a mocked scan proves
// nothing about whether the real one looks in the right place.

type wipRepo struct {
	t   *testing.T
	dir string
}

func newWipRepo(t *testing.T) *wipRepo {
	t.Helper()
	dir := t.TempDir()
	r := &wipRepo{t: t, dir: dir}
	r.git("init", "-b", "main")
	r.git("config", "user.email", "test@test")
	r.git("config", "user.name", "test")
	r.write("README", "base\n")
	r.git("add", ".")
	r.git("commit", "-m", "initial")
	// Stand in for the remote so base-ref resolution takes the production
	// path (origin/<default>) without needing a second repository.
	r.git("update-ref", "refs/remotes/origin/main", strings.TrimSpace(r.git("rev-parse", "main")))
	r.git("symbolic-ref", "refs/remotes/origin/HEAD", "refs/remotes/origin/main")
	return r
}

func (r *wipRepo) git(args ...string) string {
	r.t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = r.dir
	out, err := cmd.CombinedOutput()
	if err != nil {
		r.t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, out)
	}
	return string(out)
}

func (r *wipRepo) write(name, content string) {
	r.t.Helper()
	full := filepath.Join(r.dir, name)
	if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
		r.t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(full, []byte(content), 0o644); err != nil {
		r.t.Fatalf("write %s: %v", name, err)
	}
}

// preserve reproduces exactly what preserveWorkInProgress does on a guard
// kill: branch off main, commit the dirty tree with the writer's message
// shape, and anchor it under refs/nightgauge/wip/<sanitized-branch>-<unix>.
//
// The message is built here rather than imported because it lives in the
// TypeScript writer; a hand-rolled body that drifted from it is precisely the
// failure this reader has to survive, so the shape is spelled out.
func (r *wipRepo) preserve(branch string, issue int, stage string, ts int64, files map[string]string) (ref, sha string) {
	r.t.Helper()
	r.git("checkout", "-q", "-b", branch, "main")
	for name, content := range files {
		r.write(name, content)
	}
	r.git("add", "-A")
	body := fmt.Sprintf(`wip(%s): preserve uncommitted work from a terminated stage

The %s stage was terminated by the pipeline (stall-kill) while its work was
still uncommitted.

Refs: #%d
%s: %s`, stage, stage, issue, wipCommitTrailer, stage)
	r.git("commit", "-q", "-m", body)
	sha = strings.TrimSpace(r.git("rev-parse", "HEAD"))
	ref = fmt.Sprintf("%s/%s-%d", WipRefNamespace, sanitizeWipRefComponent(branch), ts)
	r.git("update-ref", ref, sha)
	r.git("checkout", "-q", "main")
	return ref, sha
}

// land replays the squash-merge shape: the same CONTENT arrives on main as a
// brand-new commit that is not an ancestor of the preserved one.
func (r *wipRepo) land(files map[string]string) {
	r.t.Helper()
	r.git("checkout", "-q", "main")
	for name, content := range files {
		r.write(name, content)
	}
	r.git("add", "-A")
	r.git("commit", "-q", "-m", "squash: land the work")
	r.git("update-ref", "refs/remotes/origin/main", strings.TrimSpace(r.git("rev-parse", "main")))
}

func (r *wipRepo) refExists(ref string) bool {
	r.t.Helper()
	cmd := exec.Command("git", "rev-parse", "--verify", "--quiet", ref)
	cmd.Dir = r.dir
	return cmd.Run() == nil
}

// The listing is the discoverability floor: before this existed the operator
// had to already know the namespace was there to find anything in it.
func TestListWipRefs_ReportsIssueBranchCommitAndPathCount(t *testing.T) {
	r := newWipRepo(t)
	ref, sha := r.preserve("feat/338-guest-auth", 338, "feature-validate", 1787939337, map[string]string{
		"lib/auth.dart":       "guest\n",
		"test/auth_test.dart": "expect\n",
	})

	refs, err := ListWipRefs(r.dir)
	if err != nil {
		t.Fatalf("ListWipRefs: %v", err)
	}
	if len(refs) != 1 {
		t.Fatalf("expected exactly the one preserved ref, got %d: %+v", len(refs), refs)
	}
	got := refs[0]
	if got.Ref != ref {
		t.Errorf("ref = %q, want %q", got.Ref, ref)
	}
	if got.Commit != sha {
		t.Errorf("commit = %q, want %q", got.Commit, sha)
	}
	if got.Issue != 338 {
		t.Errorf("issue = %d, want 338 — an anchor whose issue is unknown cannot be matched to a re-run", got.Issue)
	}
	if got.Stage != "feature-validate" {
		t.Errorf("stage = %q, want feature-validate", got.Stage)
	}
	if got.FilesChanged != 2 {
		t.Errorf("filesChanged = %d, want 2 — the path count is the magnitude of what is at stake", got.FilesChanged)
	}
	if got.Branch != "feat/338-guest-auth" || !got.BranchExists {
		t.Errorf("branch = %q exists=%v, want feat/338-guest-auth exists=true", got.Branch, got.BranchExists)
	}
	if got.CommittedAt.IsZero() {
		t.Error("committedAt is zero — age is what separates a live run from abandoned work")
	}
}

// The case the incident actually produced: branch and worktree cleaned up, so
// the ref is the only path back to the work.
func TestListWipRefs_ReportsWorkWhoseBranchIsGone(t *testing.T) {
	r := newWipRepo(t)
	_, _ = r.preserve("feat/338-guest-auth", 338, "feature-dev", 1787939338, map[string]string{"a.txt": "x\n"})
	r.git("branch", "-D", "feat/338-guest-auth")

	refs, err := ListWipRefs(r.dir)
	if err != nil {
		t.Fatalf("ListWipRefs: %v", err)
	}
	if len(refs) != 1 {
		t.Fatalf("preserved work vanished from the listing once its branch was deleted: %+v", refs)
	}
	if refs[0].BranchExists {
		t.Error("branchExists = true after `git branch -D` — the report would send the operator to a branch that no longer resolves")
	}
	if refs[0].Branch != "feat-338-guest-auth" {
		t.Errorf("branch = %q, want the sanitized component a deleted branch leaves behind", refs[0].Branch)
	}
}

func TestListWipRefs_EmptyNamespaceIsNotAnError(t *testing.T) {
	r := newWipRepo(t)
	refs, err := ListWipRefs(r.dir)
	if err != nil {
		t.Fatalf("a repo with no preserved work must not error: %v", err)
	}
	if len(refs) != 0 {
		t.Fatalf("expected no refs, got %+v", refs)
	}
}

func TestWipRefsForIssue_NarrowsToOneIssue(t *testing.T) {
	r := newWipRepo(t)
	r.preserve("feat/338-a", 338, "feature-dev", 1787939340, map[string]string{"a.txt": "a\n"})
	r.preserve("feat/912-b", 912, "feature-dev", 1787939341, map[string]string{"b.txt": "b\n"})

	refs, err := ListWipRefs(r.dir)
	if err != nil {
		t.Fatalf("ListWipRefs: %v", err)
	}
	only := WipRefsForIssue(refs, 338)
	if len(only) != 1 || only[0].Issue != 338 {
		t.Fatalf("WipRefsForIssue(338) = %+v, want exactly the #338 anchor", only)
	}
	if len(WipRefsForIssue(refs, 0)) != 2 {
		t.Fatalf("issue 0 must mean every issue, got %d", len(WipRefsForIssue(refs, 0)))
	}
}

// The lifecycle AC: a ref whose work is already in main must go away, or the
// namespace grows without bound and pins objects git gc would reclaim.
//
// Note the shape: the preserved commit is NOT an ancestor of main here. An
// ancestry-based landed test would keep this ref forever and look correct.
func TestPruneWipRefs_RemovesRefsWhoseContentLanded(t *testing.T) {
	r := newWipRepo(t)
	files := map[string]string{"lib/auth.dart": "guest\n"}
	ref, sha := r.preserve("feat/338-guest-auth", 338, "feature-validate", 1787939342, files)
	r.land(files)

	if isAncestor(t, r.dir, sha, "main") {
		t.Fatal("fixture is wrong: the preserved commit must not be an ancestor of main, or this proves nothing about squash merges")
	}

	res, err := PruneWipRefs(WipPruneOptions{RepoRoot: r.dir})
	if err != nil {
		t.Fatalf("PruneWipRefs: %v", err)
	}
	if len(res.Pruned) != 1 || res.Pruned[0].Ref != ref {
		t.Fatalf("expected the landed ref to be pruned, got pruned=%+v kept=%+v", res.Pruned, res.Kept)
	}
	if res.Pruned[0].Reason != WipPrunedLanded {
		t.Errorf("reason = %q, want %q", res.Pruned[0].Reason, WipPrunedLanded)
	}
	if r.refExists(ref) {
		t.Fatal("the ref still resolves after prune — nothing was actually deleted, so git gc still cannot reclaim the objects")
	}
}

// The other half, and the one that matters more: unsalvaged work must survive
// a prune. A prune that removes this is the exact loss #128 exists to prevent.
func TestPruneWipRefs_KeepsWorkThatHasNotLanded(t *testing.T) {
	r := newWipRepo(t)
	ref, _ := r.preserve("feat/338-guest-auth", 338, "feature-dev", 1787939343, map[string]string{"lib/auth.dart": "guest\n"})

	res, err := PruneWipRefs(WipPruneOptions{RepoRoot: r.dir})
	if err != nil {
		t.Fatalf("PruneWipRefs: %v", err)
	}
	if len(res.Pruned) != 0 {
		t.Fatalf("unlanded work was pruned: %+v", res.Pruned)
	}
	if len(res.Kept) != 1 || res.Kept[0].Reason != WipKeepNotLanded {
		t.Fatalf("kept = %+v, want one entry with reason %q", res.Kept, WipKeepNotLanded)
	}
	if !r.refExists(ref) {
		t.Fatal("the only remaining copy of a killed stage's work was deleted")
	}
}

func TestPruneWipRefs_DryRunDeletesNothing(t *testing.T) {
	r := newWipRepo(t)
	files := map[string]string{"lib/auth.dart": "guest\n"}
	ref, _ := r.preserve("feat/338-guest-auth", 338, "feature-validate", 1787939344, files)
	r.land(files)

	res, err := PruneWipRefs(WipPruneOptions{RepoRoot: r.dir, DryRun: true})
	if err != nil {
		t.Fatalf("PruneWipRefs: %v", err)
	}
	if len(res.Pruned) != 1 {
		t.Fatalf("a dry run must still classify, got pruned=%+v", res.Pruned)
	}
	if !r.refExists(ref) {
		t.Fatal("--dry-run deleted the ref")
	}
}

// Explicit discard is the second door: an operator abandoning work by name.
func TestPruneWipRefs_DiscardRemovesNamedRefEvenUnlanded(t *testing.T) {
	r := newWipRepo(t)
	ref, _ := r.preserve("feat/338-guest-auth", 338, "feature-dev", 1787939345, map[string]string{"a.txt": "x\n"})
	keepRef, _ := r.preserve("feat/912-other", 912, "feature-dev", 1787939346, map[string]string{"b.txt": "y\n"})

	res, err := PruneWipRefs(WipPruneOptions{RepoRoot: r.dir, Issue: 338, Discard: true})
	if err != nil {
		t.Fatalf("PruneWipRefs: %v", err)
	}
	if len(res.Pruned) != 1 || res.Pruned[0].Reason != WipPrunedDiscarded {
		t.Fatalf("pruned = %+v, want exactly the discarded #338 anchor", res.Pruned)
	}
	if r.refExists(ref) {
		t.Error("--discard left the named ref in place")
	}
	if !r.refExists(keepRef) {
		t.Error("--issue 338 --discard also removed another issue's preserved work")
	}
}

// A bare --discard would delete every preserved commit in the repository,
// which is the outcome the namespace exists to prevent.
func TestPruneWipRefs_DiscardWithoutASelectorIsRefused(t *testing.T) {
	r := newWipRepo(t)
	ref, _ := r.preserve("feat/338-guest-auth", 338, "feature-dev", 1787939347, map[string]string{"a.txt": "x\n"})

	if _, err := PruneWipRefs(WipPruneOptions{RepoRoot: r.dir, Discard: true}); err == nil {
		t.Fatal("a bare --discard was accepted — it would delete every preserved commit in the repo")
	}
	if !r.refExists(ref) {
		t.Fatal("the refused discard deleted work anyway")
	}
}

func isAncestor(t *testing.T, dir, maybeAncestor, ref string) bool {
	t.Helper()
	cmd := exec.Command("git", "merge-base", "--is-ancestor", maybeAncestor, ref)
	cmd.Dir = dir
	return cmd.Run() == nil
}

func TestWipRefAge_IsMeasuredFromTheCommit(t *testing.T) {
	ref := WipRef{CommittedAt: time.Now().Add(-72 * time.Hour)}
	if got := ref.AgeDays(time.Now()); got != 3 {
		t.Fatalf("AgeDays = %d, want 3", got)
	}
	if (WipRef{}).AgeDays(time.Now()) != 0 {
		t.Fatal("an unknown commit date must read as age 0, not a negative or huge age")
	}
}
