package doctor

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

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
	cmd := exec.Command("git", args...)
	cmd.Dir = r.dir
	out, err := cmd.CombinedOutput()
	if err != nil {
		r.t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, out)
	}
	return string(out)
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

	item, warning := checkLeakedWorktrees(r.dir, aged())

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

	item, _ := checkLeakedWorktrees(r.dir, aged())

	if strings.Contains(item.Error, "uncommitted-changes") {
		t.Errorf("pipeline exhaust was reported as a blocker: %q", item.Error)
	}
}

func TestCheckLeakedWorktrees_HealthyRepoPasses(t *testing.T) {
	r := newLeakRepo(t)

	item, warning := checkLeakedWorktrees(r.dir, aged())

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
	item, warning := checkLeakedWorktrees(t.TempDir(), aged())

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
