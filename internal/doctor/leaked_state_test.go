package doctor

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/nightgauge/nightgauge/internal/gittest"
	"github.com/nightgauge/nightgauge/internal/reclaim"
)

// #330 / #332. `doctor` reported none of the nine leaked worktrees and none of
// the five leaked stashes an operator found by hand. These tests drive real git
// for the same reason the sweep's do: the defect is state that exists only in a
// repository, and a mocked scan proves nothing about whether the real one looks.

type leakRepo struct {
	t   *testing.T
	dir string
}

func newLeakRepo(t *testing.T) *leakRepo {
	t.Helper()
	isolateMachineState(t)
	base := t.TempDir()
	// Resolve symlinks up front: on macOS t.TempDir() hands back a /var path
	// that is really /private/var, and git reports the resolved form.
	resolved, err := filepath.EvalSymlinks(base)
	if err != nil {
		t.Fatalf("resolve temp dir: %v", err)
	}
	r := &leakRepo{t: t, dir: resolved}
	r.git("init", "-b", "main")
	r.git("config", "user.email", "test@test")
	r.git("config", "user.name", "test")
	r.write("README", "base\n")
	r.git("add", ".")
	r.git("commit", "-m", "initial")
	// Stand in for the remote so the sweep's base ref resolves the way it does
	// in production, without needing a second repository.
	head := strings.TrimSpace(r.git("rev-parse", "main"))
	r.git("update-ref", "refs/remotes/origin/main", head)
	return r
}

func (r *leakRepo) git(args ...string) string {
	r.t.Helper()
	return gittest.Run(r.t, r.dir, args...)
}

func (r *leakRepo) write(name, content string) {
	r.t.Helper()
	full := filepath.Join(r.dir, name)
	if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
		r.t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(full, []byte(content), 0o644); err != nil {
		r.t.Fatalf("write %s: %v", name, err)
	}
}

// strandedWorktree creates a pipeline worktree holding uncommitted deliverable
// work — the shape that can never clear itself.
func (r *leakRepo) strandedWorktree(issue int) string {
	r.t.Helper()
	path := filepath.Join(r.dir, ".worktrees", "issue-"+itoa(issue))
	r.git("worktree", "add", "-q", path, "-b", "fix/"+itoa(issue), "main")
	if err := os.WriteFile(filepath.Join(path, "unfinished.txt"), []byte("half-done\n"), 0o644); err != nil {
		r.t.Fatalf("write: %v", err)
	}
	return path
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var b []byte
	for n > 0 {
		b = append([]byte{byte('0' + n%10)}, b...)
		n /= 10
	}
	return string(b)
}

// aged returns a clock far enough ahead that anything created during the test
// reads as stale. Age is derived from the worktree's mtime, so advancing the
// clock is how a test reaches the staleness threshold without sleeping or
// back-dating the filesystem.
func aged() time.Time { return time.Now().Add(30 * 24 * time.Hour) }

func TestCheckLeakedWorktrees_ReportsAStrandedWorktree(t *testing.T) {
	r := newLeakRepo(t)
	wt := r.strandedWorktree(1181)

	item, warning := checkLeakedWorktrees(r.dir, aged(), nil)

	if item.OK {
		t.Fatalf("a stranded worktree must not read as healthy: %+v", item)
	}
	if !strings.Contains(item.Error, wt) {
		t.Errorf("the check does not name the worktree: %q", item.Error)
	}
	// Naming what blocked it is what turns "uncommitted-changes" from an
	// unfalsifiable verdict into something an operator can act on without
	// opening the directory — the step nobody took for nine worktrees.
	if !strings.Contains(item.Error, "unfinished.txt") {
		t.Errorf("the check does not name the blocking path: %q", item.Error)
	}
	if warning == "" {
		t.Error("a leak must produce a warning, not just a check entry")
	}
}

func TestCheckLeakedWorktrees_IgnoresAWorktreeHoldingOnlyExhaust(t *testing.T) {
	// The #332 case. Since the sweep can now reclaim these, `doctor` must
	// report them as reclaimable work rather than as a permanent leak — and
	// must not describe the pipeline's own scaffold as a blocker.
	r := newLeakRepo(t)
	path := filepath.Join(r.dir, ".worktrees", "issue-1182")
	r.git("worktree", "add", "-q", path, "-b", "fix/1182", "main")
	if err := os.MkdirAll(filepath.Join(path, ".nightgauge", "knowledge"), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(path, ".nightgauge", "knowledge", "README.md"),
		[]byte("# Knowledge Base\n"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}

	item, _ := checkLeakedWorktrees(r.dir, aged(), nil)

	if strings.Contains(item.Error, "uncommitted-changes") {
		t.Errorf("pipeline exhaust was reported as a blocker: %q", item.Error)
	}
}

func TestCheckLeakedWorktrees_HealthyRepoPasses(t *testing.T) {
	r := newLeakRepo(t)

	item, warning := checkLeakedWorktrees(r.dir, aged(), nil)

	if !item.OK {
		t.Errorf("a repo with no worktrees must pass: %+v", item)
	}
	if warning != "" {
		t.Errorf("unexpected warning: %q", warning)
	}
}

func TestCheckLeakedWorktrees_UnverifiableIsNeverHealthy(t *testing.T) {
	// Not a git repository and not a workspace, so no roots resolve. #296's
	// lesson: "I could not look" must never render as "there is nothing wrong",
	// because the operator cannot tell the two apart from the output.
	item, warning := checkLeakedWorktrees(t.TempDir(), aged(), nil)

	if item.OK {
		t.Fatalf("an unverifiable scan reported healthy: %+v", item)
	}
	if !strings.Contains(item.Error, "unverifiable") {
		t.Errorf("the error does not say the scan could not run: %q", item.Error)
	}
	if warning == "" {
		t.Error("an unverifiable scan must warn")
	}
}

func TestCheckPipelineStashes_ReportsAnUnreclaimedStashWithItsAge(t *testing.T) {
	r := newLeakRepo(t)
	r.write("README", "modified\n")
	r.git("stash", "push", "-m", reclaim.StashName(reclaim.StashBaseline, 692, "feature-validate"))

	// 45 days on from creation — every stash the audit found was months old,
	// and an age-less report reads as "probably from the run that just
	// finished" and gets ignored.
	item, warning := checkPipelineStashes(r.dir, time.Now().Add(45*24*time.Hour))

	if item.OK {
		t.Fatalf("an unreclaimed pipeline stash must not read as healthy: %+v", item)
	}
	if !strings.Contains(item.Error, "#692") {
		t.Errorf("the check does not name the issue: %q", item.Error)
	}
	if !strings.Contains(item.Error, "45d") || !strings.Contains(item.Detail, "oldest 45d") {
		t.Errorf("the check does not report the stash's age: detail=%q error=%q", item.Detail, item.Error)
	}
	if !strings.Contains(item.Error, "nightgauge stash sweep") {
		t.Errorf("the check does not say how to reclaim it: %q", item.Error)
	}
	if warning == "" {
		t.Error("a leaked stash must produce a warning")
	}
}

func TestCheckPipelineStashes_IgnoresAnOperatorStash(t *testing.T) {
	r := newLeakRepo(t)
	r.write("README", "my own work\n")
	r.git("stash", "push", "-m", "wip before the refactor")

	item, warning := checkPipelineStashes(r.dir, aged())

	if !item.OK {
		t.Fatalf("an operator's stash is not a pipeline leak: %+v", item)
	}
	if warning != "" {
		t.Errorf("unexpected warning about an operator's stash: %q", warning)
	}
}

func TestCheckPipelineStashes_NoRootsIsNeverHealthy(t *testing.T) {
	// Not a git repository and not a workspace, so no roots resolve at all.
	item, warning := checkPipelineStashes(t.TempDir(), aged())

	if item.OK {
		t.Fatalf("a scan with no roots reported healthy: %+v", item)
	}
	if !strings.Contains(item.Error, "unverifiable") {
		t.Errorf("the error does not say the scan could not run: %q", item.Error)
	}
	if warning == "" {
		t.Error("an unverifiable scan must warn")
	}
}

// A root that resolves but cannot be READ is the harder half, and the one that
// actually reaches the `git stash list` error branch: the workspace manifest
// names a path that exists but is not a git repository. Nothing about that is
// exotic — a manifest entry outliving the repo it pointed at produces it — and
// treating the failure as "no stashes here" would report a clean stash stack
// for a repo the check never read (#296).
func TestCheckPipelineStashes_UnreadableRootIsNeverHealthy(t *testing.T) {
	r := newLeakRepo(t)
	notARepo := filepath.Join(r.dir, "..", "not-a-repo")
	if err := os.MkdirAll(notARepo, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	r.write(".vscode/nightgauge-workspace.yaml",
		"workspace:\n  name: Test\nrepositories:\n"+
			"  - name: primary\n    path: .\n    role: primary\n"+
			"  - name: broken\n    path: ../not-a-repo\n    role: primary\n")

	item, warning := checkPipelineStashes(r.dir, aged())

	if item.OK {
		t.Fatalf("an unreadable root reported healthy: %+v", item)
	}
	if !strings.Contains(item.Error, "unverifiable") {
		t.Errorf("the error does not say the scan could not run: %q", item.Error)
	}
	if warning == "" {
		t.Error("an unverifiable scan must warn")
	}
}

// #912. The worktree arm above drives off `git worktree list`, so a merged
// branch whose worktree is already gone is outside its reach permanently —
// three of them sat in the core repo while `doctor` reported "healthy".

// strandMergedBranch squash-merges a branch into main and then removes its
// worktree, reproducing the production sequence exactly. Ancestry reports the
// result as unmerged; only a content diff sees the truth.
func (r *leakRepo) strandMergedBranch(issue int, branch string) {
	r.t.Helper()
	path := filepath.Join(r.dir, ".worktrees", "issue-"+itoa(issue))
	r.git("worktree", "add", "-q", path, "-b", branch, "main")
	if err := os.WriteFile(filepath.Join(path, "landed.txt"), []byte("shipped\n"), 0o644); err != nil {
		r.t.Fatalf("write: %v", err)
	}
	r.git("-C", path, "add", ".")
	r.git("-C", path, "commit", "-m", "work on "+branch)
	r.git("merge", "--squash", branch)
	r.git("commit", "-m", "squash: "+branch)
	r.git("update-ref", "refs/remotes/origin/main", strings.TrimSpace(r.git("rev-parse", "main")))
	r.git("worktree", "remove", path)
}

func TestCheckStrandedBranches_ReportsAMergedBranchNoWorktreeHolds(t *testing.T) {
	r := newLeakRepo(t)
	r.strandMergedBranch(912, "fix/912-landed")

	item, warning := checkStrandedBranches(r.dir, nil)

	if item.OK {
		t.Fatalf("a stranded merged branch must not read as healthy: %+v", item)
	}
	if !strings.Contains(item.Error, "fix/912-landed") {
		t.Errorf("the check does not name the branch: %q", item.Error)
	}
	// The check deletes nothing itself; the delete it offers is a confirm
	// remedy whose verb re-derives the merged proof at apply time.
	if !strings.Contains(item.Error, "[confirm: Delete the merged local branch") ||
		!strings.Contains(item.Error, "re-derived first") {
		t.Errorf("the check does not offer a confirm delete that re-derives its proof: %q", item.Error)
	}
	if warning == "" {
		t.Error("a stranded branch must produce a warning, not just a check entry")
	}
}

func TestCheckStrandedBranches_KeepsUnmergedWork(t *testing.T) {
	// The failure that costs something: a human deletes real work on this
	// report's say-so.
	r := newLeakRepo(t)
	path := filepath.Join(r.dir, ".worktrees", "issue-919")
	r.git("worktree", "add", "-q", path, "-b", "feat/919-unlanded", "main")
	if err := os.WriteFile(filepath.Join(path, "wip.txt"), []byte("not merged anywhere\n"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	r.git("-C", path, "add", ".")
	r.git("-C", path, "commit", "-m", "unlanded work")
	r.git("worktree", "remove", path)

	fs, _ := strandedBranchFindings(r.dir, nil)

	// It is reported, but only ever with a manual remedy: nothing proves it
	// safe to delete, so no fix is offered.
	if len(fs) != 1 || fs[0].Evidence["branch"] != "feat/919-unlanded" {
		t.Fatalf("want one finding for the unmerged branch, got %s", findingsText(fs))
	}
	for _, rem := range fs[0].Remedies {
		if rem.Kind != RemedyManual || rem.Verb != "" {
			t.Errorf("an unmerged branch was offered a %s remedy %q", rem.Kind, rem.Verb)
		}
	}
}

func TestCheckStrandedBranches_HealthyRepoPasses(t *testing.T) {
	r := newLeakRepo(t)

	item, warning := checkStrandedBranches(r.dir, nil)

	if !item.OK {
		t.Errorf("a repo with only main must pass: %+v", item)
	}
	if warning != "" {
		t.Errorf("unexpected warning: %q", warning)
	}
}

func TestCheckStrandedBranches_UnverifiableIsNeverHealthy(t *testing.T) {
	// #296 again: no roots resolve, so the scan never ran. A clean bill of
	// health here would be an assertion about nothing.
	item, warning := checkStrandedBranches(t.TempDir(), nil)

	if item.OK {
		t.Fatalf("an unverifiable scan reported healthy: %+v", item)
	}
	if !strings.Contains(item.Error, "unverifiable") {
		t.Errorf("the error does not say the scan could not run: %q", item.Error)
	}
	if warning == "" {
		t.Error("an unverifiable scan must warn")
	}
}

// hygieneFixture is one repo holding one leaked worktree, one merged and one
// unmerged stranded branch, and one pipeline stash on the default branch.
func hygieneFixture(t *testing.T) *leakRepo {
	t.Helper()
	r := newLeakRepo(t)
	r.strandMergedBranch(912, "fix/912-landed")
	path := filepath.Join(r.dir, ".worktrees", "issue-919")
	r.git("worktree", "add", "-q", path, "-b", "feat/919-unlanded", "main")
	if err := os.WriteFile(filepath.Join(path, "wip.txt"), []byte("not merged anywhere\n"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	r.git("-C", path, "add", ".")
	r.git("-C", path, "commit", "-m", "unlanded work")
	r.git("worktree", "remove", path)
	r.strandedWorktree(1181)
	r.write("README", "modified\n")
	r.git("stash", "push", "-m", reclaim.StashName(reclaim.StashBaseline, 692, "feature-validate"))
	return r
}

func hygieneScan(r *leakRepo, now time.Time) []Finding {
	var all []Finding
	fs, _ := worktreeLeakFindings(r.dir, now, nil)
	all = append(all, fs...)
	fs, _ = strandedBranchFindings(r.dir, nil)
	all = append(all, fs...)
	fs, _ = pipelineStashFindings(r.dir, now)
	return append(all, fs...)
}

// TestHygieneFindings_FixtureYieldsExactlyTheLeaks: each leaked object is its
// own finding with its code, housekeeping severity and remedy kind. The
// unmerged branch's remedy is manual — it cannot be proven safe to delete.
func TestHygieneFindings_FixtureYieldsExactlyTheLeaks(t *testing.T) {
	r := hygieneFixture(t)
	fs := hygieneScan(r, aged())

	type want struct {
		code, object, verb string
		kind               RemedyKind
	}
	wants := []want{
		{"NGD017", filepath.Join(r.dir, ".worktrees", "issue-1181"), "", RemedyManual},
		{"NGD018", "feat/919-unlanded", "", RemedyManual},
		{"NGD018", "fix/912-landed", verbBranchDelete, RemedyConfirm},
		{"NGD019", "stash@{0}", verbStashSweep, RemedyAuto},
	}
	if len(fs) != len(wants) {
		t.Fatalf("got %d findings, want %d: %s", len(fs), len(wants), findingsText(fs))
	}
	for i, w := range wants {
		f := fs[i]
		obj := f.Evidence["branch"]
		switch f.Code {
		case "NGD017":
			obj = f.Evidence["path"]
		case "NGD019":
			obj = f.Evidence["stash_ref"]
		}
		if f.Code != w.code || obj != w.object {
			t.Errorf("finding %d = %s %q, want %s %q", i, f.Code, obj, w.code, w.object)
		}
		if f.Severity != SeverityHousekeeping {
			t.Errorf("%s: severity %s, want housekeeping", f.Code, f.Severity)
		}
		if len(f.Remedies) == 0 {
			t.Fatalf("%s %s: no remedy declared", f.Code, obj)
		}
		r0 := f.Remedies[0]
		if r0.Kind != w.kind || r0.Verb != w.verb {
			t.Errorf("%s %s: remedy %s/%q, want %s/%q", f.Code, obj, r0.Kind, r0.Verb, w.kind, w.verb)
		}
		for _, rem := range f.Remedies {
			if rem.Verify != f.Check {
				t.Errorf("%s: remedy %s verifies %q, want %q", f.Code, rem.ID, rem.Verify, f.Check)
			}
			if rem.Kind != RemedyManual && rem.Preview == "" {
				t.Errorf("%s: remedy %s has no preview", f.Code, rem.ID)
			}
			if rem.Kind == RemedyManual && (rem.Verb != "" || len(rem.Steps) == 0) {
				t.Errorf("%s: manual remedy %s must carry steps and no verb", f.Code, rem.ID)
			}
		}
	}
}

// TestHygieneFindings_FingerprintsAreStable: the same objects yield the same
// fingerprints on a second run, even when their ages have moved.
func TestHygieneFindings_FingerprintsAreStable(t *testing.T) {
	r := hygieneFixture(t)
	first := hygieneScan(r, aged())
	second := hygieneScan(r, aged().Add(72*time.Hour))
	if len(first) != len(second) {
		t.Fatalf("finding count changed between runs: %d vs %d", len(first), len(second))
	}
	seen := map[string]bool{}
	for i := range first {
		if first[i].Fingerprint != second[i].Fingerprint {
			t.Errorf("%s: fingerprint %s then %s", first[i].Title, first[i].Fingerprint, second[i].Fingerprint)
		}
		if seen[first[i].Fingerprint] {
			t.Errorf("two objects share fingerprint %s", first[i].Fingerprint)
		}
		seen[first[i].Fingerprint] = true
	}
}

// TestHygieneFindings_ReclaimableWorktreeIsAuto: a worktree the sweep would
// reclaim now is offered the auto `worktree sweep` remedy.
func TestHygieneFindings_ReclaimableWorktreeIsAuto(t *testing.T) {
	r := newLeakRepo(t)
	path := filepath.Join(r.dir, ".worktrees", "issue-1182")
	r.git("worktree", "add", "-q", path, "-b", "fix/1182", "main")
	r.write("landed.txt", "shipped\n")
	if err := os.WriteFile(filepath.Join(path, "landed.txt"), []byte("shipped\n"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	r.git("-C", path, "add", ".")
	r.git("-C", path, "commit", "-m", "work")
	r.git("add", ".")
	r.git("commit", "-m", "squash: fix/1182")
	r.git("update-ref", "refs/remotes/origin/main", strings.TrimSpace(r.git("rev-parse", "main")))

	fs, _ := worktreeLeakFindings(r.dir, aged(), nil)
	if len(fs) != 1 {
		t.Fatalf("want one reclaimable worktree finding, got %s", findingsText(fs))
	}
	if rem := fs[0].Remedies[0]; rem.Kind != RemedyAuto || rem.Verb != verbWorktreeSweep {
		t.Errorf("remedy = %+v, want auto worktree.sweep", rem)
	}
}

// TestHygieneFindings_BranchDeleteReDerivesProof: the delete precondition
// re-derives the merged proof at apply time, and refuses an unmerged branch, a
// branch a worktree holds, and main.
func TestHygieneFindings_BranchDeleteReDerivesProof(t *testing.T) {
	r := hygieneFixture(t)
	if err := branchDeletePrecondition(r.dir, "fix/912-landed", nil); err != nil {
		t.Errorf("merged, unheld branch refused: %v", err)
	}
	for _, b := range []string{"feat/919-unlanded", "fix/1181", "main", "master", "gone/branch"} {
		if err := branchDeletePrecondition(r.dir, b, nil); err == nil {
			t.Errorf("branch %s was allowed", b)
		}
	}
}
