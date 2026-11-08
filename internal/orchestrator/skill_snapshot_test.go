package orchestrator

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/nightgauge/nightgauge/internal/layout/layouttest"
	"github.com/nightgauge/nightgauge/internal/skillrender"
	"github.com/nightgauge/nightgauge/internal/state"
)

func TestRunSkillSnapshotCopiesOnceAndReuses(t *testing.T) {
	root := t.TempDir()
	layouttest.MkCheckoutDir(t, root)
	live := t.TempDir()
	skill := filepath.Join(live, "skills", "nightgauge-x", "SKILL.md")
	writeTestFile(t, skill, "v1")

	dst, err := runSkillSnapshot(root, live, 7, "run-a")
	if err != nil || dst == "" {
		t.Fatalf("snapshot = %q, %v", dst, err)
	}
	writeTestFile(t, skill, "v2")
	again, err := runSkillSnapshot(root, live, 7, "run-a")
	if err != nil || again != dst {
		t.Fatalf("second snapshot = %q, %v; want reuse of %q", again, err, dst)
	}
	if data, _ := os.ReadFile(filepath.Join(dst, "skills", "nightgauge-x", "SKILL.md")); string(data) != "v1" {
		t.Errorf("snapshot content = %q, want v1", data)
	}
	if info, err := os.Stat(filepath.Join(dst, "skills", "nightgauge-x", "SKILL.md")); err != nil || info.Mode().Perm() != 0o444 {
		t.Errorf("snapshot file mode = %v, %v; want 0444", info.Mode().Perm(), err)
	}
}

func TestRunSkillSnapshotSkipsSymlinks(t *testing.T) {
	root := t.TempDir()
	layouttest.MkCheckoutDir(t, root)
	live := t.TempDir()
	writeTestFile(t, filepath.Join(live, "skills", "a", "SKILL.md"), "a")
	if err := os.Symlink(filepath.Join(live, "skills", "a", "SKILL.md"), filepath.Join(live, "skills", "link.md")); err != nil {
		t.Fatal(err)
	}
	dst, err := runSkillSnapshot(root, live, 7, "run-s")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Lstat(filepath.Join(dst, "skills", "link.md")); !os.IsNotExist(err) {
		t.Errorf("symlink copied into snapshot (err=%v)", err)
	}
}

func TestRunSkillSnapshotPrunesOtherRuns(t *testing.T) {
	root := t.TempDir()
	layouttest.MkCheckoutDir(t, root)
	live := t.TempDir()
	writeTestFile(t, filepath.Join(live, "skills", "a", "SKILL.md"), "a")
	old, err := runSkillSnapshot(root, live, 7, "run-old")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := runSkillSnapshot(root, live, 7, "run-new"); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(old); !os.IsNotExist(err) {
		t.Errorf("old run's snapshot survived (err=%v)", err)
	}
}

func TestRunSkillSnapshotNoSkillsAndBadRunID(t *testing.T) {
	root := t.TempDir()
	layouttest.MkCheckoutDir(t, root)
	live := t.TempDir()
	dst, err := runSkillSnapshot(root, live, 7, "run-x")
	if err != nil || dst == "" {
		t.Fatalf("no skills dir: %q, %v; want an empty frozen snapshot", dst, err)
	}
	if entries, err := os.ReadDir(filepath.Join(dst, "skills")); err != nil || len(entries) != 0 {
		t.Fatalf("snapshot skills = %v, %v; want an empty directory", entries, err)
	}
	// An earlier stage creates skills/ mid-run: the run keeps its empty copy.
	if err := os.MkdirAll(filepath.Join(live, "skills", "injected"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(live, "skills", "injected", "SKILL.md"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	again, err := runSkillSnapshot(root, live, 7, "run-x")
	if err != nil || again != dst {
		t.Fatalf("second call = %q, %v; want the run-start snapshot %q", again, err, dst)
	}
	if _, err := os.Stat(filepath.Join(again, "skills", "injected")); !os.IsNotExist(err) {
		t.Errorf("a skill created mid-run reached the snapshot: %v", err)
	}
	if _, err := runSkillSnapshot(root, t.TempDir(), 7, ""); err == nil {
		t.Error("empty run id accepted")
	}
	if dst, err := runSkillSnapshot(root, t.TempDir(), 7, "../x"); err != nil ||
		!strings.HasPrefix(filepath.Base(dst), snapshotKey("../x")) {
		t.Errorf("path-like run id: %q, %v; want the hashed key", dst, err)
	}
}

// A run of one issue never prunes a concurrent run of another issue: the
// checkout is shared, the snapshot directories are not (#2813 review).
func TestRunSkillSnapshotKeepsOtherIssuesSnapshots(t *testing.T) {
	root := t.TempDir()
	layouttest.MkCheckoutDir(t, root)
	live := t.TempDir()
	writeTestFile(t, filepath.Join(live, "skills", "a", "SKILL.md"), "a")
	other, err := runSkillSnapshot(root, live, 8, "run-8")
	if err != nil || other == "" {
		t.Fatalf("snapshot for #8 = %q, %v", other, err)
	}
	if _, err := runSkillSnapshot(root, live, 7, "run-7"); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(other, "skills", "a", "SKILL.md")); err != nil {
		t.Errorf("issue #8's snapshot was pruned by issue #7's run: %v", err)
	}
	if _, err := runSkillSnapshot(root, live, 0, "run-0"); err == nil {
		t.Error("issue 0 accepted, want an error")
	}
}

// A run that inherits a finished run's ID starts from the current skills; a
// resume of the same run keeps its frozen copy (#2813 review).
func TestResetSkillSnapshotForRunDropsInheritedCopies(t *testing.T) {
	root := t.TempDir()
	layouttest.MkCheckoutDir(t, root)
	live := t.TempDir()
	skill := filepath.Join(live, "skills", "a", "SKILL.md")
	writeTestFile(t, skill, "old")
	read := func() string {
		t.Helper()
		dst, err := runSkillSnapshot(root, live, 7, "run-7")
		if err != nil {
			t.Fatal(err)
		}
		data, _ := os.ReadFile(filepath.Join(dst, "skills", "a", "SKILL.md"))
		return string(data)
	}
	read()
	writeTestFile(t, skill, "new")

	resetSkillSnapshotForRun(root, 7, "run-7", true)
	if got := read(); got != "old" {
		t.Errorf("resumed run read %q, want the frozen %q", got, "old")
	}

	resetSkillSnapshotForRun(root, 7, "run-7", false)
	if got := read(); got != "new" {
		t.Errorf("inherited run id read %q, want the current %q", got, "new")
	}
}

// Issue A resumes run R while the checkout's run-state record names
// concurrently running issue B: A's snapshot for R is kept, because the resume
// decision is A's own lifecycle's (beginRunLifecycle), not the shared record
// (#2813).
func TestResetSkillSnapshotKeepsResumeWhileAnotherIssueOwnsRunState(t *testing.T) {
	root := t.TempDir()
	layouttest.MkCheckoutDir(t, root)
	live := t.TempDir()
	skill := filepath.Join(live, "skills", "a", "SKILL.md")
	writeTestFile(t, skill, "original")
	dst, err := runSkillSnapshot(root, live, 7, "run-R")
	if err != nil || dst == "" {
		t.Fatalf("snapshot = %q, %v", dst, err)
	}
	writeTestFile(t, skill, "edited")
	saveSkillRunState(t, root, live, 8, "run-B") // issue B owns the record

	resetSkillSnapshotForRun(root, 7, "run-R", true)

	again, err := runSkillSnapshot(root, live, 7, "run-R")
	if err != nil {
		t.Fatal(err)
	}
	if data, _ := os.ReadFile(filepath.Join(again, "skills", "a", "SKILL.md")); string(data) != "original" {
		t.Errorf("resumed issue #7 read %q, want the frozen %q", data, "original")
	}
}

// A resume whose run-state record carries a different ID from the one the run
// started under adopts the run-start copy instead of copying the edited tree
// (#2813 review: MarkRunning mints its own ID).
func TestResetSkillSnapshotResumeAdoptsCopyUnderAnotherID(t *testing.T) {
	root := t.TempDir()
	layouttest.MkCheckoutDir(t, root)
	live := t.TempDir()
	skill := filepath.Join(live, "skills", "a", "SKILL.md")
	writeTestFile(t, skill, "old")
	if _, err := runSkillSnapshot(root, live, 7, "run-start"); err != nil {
		t.Fatal(err)
	}
	writeTestFile(t, skill, "edited by a stage")

	resetSkillSnapshotForRun(root, 7, "run-record", true)
	dst, err := runSkillSnapshot(root, live, 7, "run-record")
	if err != nil {
		t.Fatal(err)
	}
	if data, _ := os.ReadFile(filepath.Join(dst, "skills", "a", "SKILL.md")); string(data) != "old" {
		t.Errorf("resumed run read %q, want the run-start %q", data, "old")
	}
	entries, _ := os.ReadDir(filepath.Dir(dst))
	if len(entries) != 1 {
		t.Errorf("issue keeps %d snapshots after adoption, want 1", len(entries))
	}
}

// A run that ends without a resume removes its snapshot; other issues keep theirs.
func TestDropSkillSnapshotRemovesOnlyThatIssue(t *testing.T) {
	root := t.TempDir()
	layouttest.MkCheckoutDir(t, root)
	live := t.TempDir()
	writeTestFile(t, filepath.Join(live, "skills", "a", "SKILL.md"), "a")
	mine, err := runSkillSnapshot(root, live, 7, "run-7")
	if err != nil {
		t.Fatal(err)
	}
	other, err := runSkillSnapshot(root, live, 8, "run-8")
	if err != nil {
		t.Fatal(err)
	}
	dropSkillSnapshot(root, 7)
	if _, err := os.Stat(filepath.Dir(mine)); !os.IsNotExist(err) {
		t.Errorf("issue #7's snapshot directory survived the drop: %v", err)
	}
	if _, err := os.Stat(filepath.Join(other, "skills", "a", "SKILL.md")); err != nil {
		t.Errorf("issue #8's snapshot was removed: %v", err)
	}
}

// A run that resumed nothing discards the issue's earlier copy, whatever its
// key, and renders the current skills (#2813).
func TestResetSkillSnapshotNotResumedDropsOtherCopies(t *testing.T) {
	root := t.TempDir()
	layouttest.MkCheckoutDir(t, root)
	live := t.TempDir()
	skill := filepath.Join(live, "skills", "a", "SKILL.md")
	writeTestFile(t, skill, "old run")
	if _, err := runSkillSnapshot(root, live, 7, "remote-1"); err != nil {
		t.Fatal(err)
	}
	writeTestFile(t, skill, "current")

	resetSkillSnapshotForRun(root, 7, "remote-2", false)
	dst, err := runSkillSnapshot(root, live, 7, "remote-2")
	if err != nil {
		t.Fatal(err)
	}
	if data, _ := os.ReadFile(filepath.Join(dst, "skills", "a", "SKILL.md")); string(data) != "current" {
		t.Errorf("fresh run read %q, want the current %q", data, "current")
	}
	if entries, _ := os.ReadDir(filepath.Dir(dst)); len(entries) != 1 {
		t.Errorf("issue keeps %d snapshots, want 1", len(entries))
	}
}

// A platform run with a new run ID that resumes this issue's paused record
// renders its later stages from the run-start snapshot, not the worktree an
// earlier stage edited; a fresh run after a completed one renders the edit
// (#2813 review: the resume decision is beginRunLifecycle's, not the run ID's
// provenance).
func TestPlatformRunResumingPausedRecordRendersFromSnapshot(t *testing.T) {
	t.Setenv(skillrender.SkillsRootEnv, "")
	root := layouttest.Repo(t)
	skill := filepath.Join(root, "skills", "nightgauge-feature-dev", "SKILL.md")
	writeTestFile(t, skill, "---\nname: feature-dev\n---\nrun-start body\n")
	render := func(runID string) string {
		t.Helper()
		r, err := skillrender.Render(skillrender.Options{
			Stage: "feature-dev", SkillsRoots: stageSkillRoots(root, root, 7, runID),
		})
		if err != nil {
			t.Fatal(err)
		}
		return r.Content
	}

	lc, _ := beginRunLifecycle(root, 7, "fix/7")
	resetSkillSnapshotForRun(root, 7, "remote-1", lc.resumed)
	if got := render("remote-1"); !strings.Contains(got, "run-start body") {
		t.Fatalf("first render = %q, want the run-start body", got)
	}
	lc.stage(state.StageIssuePickup, "")
	lc.stage(state.StageFeatureValidate, "")
	writeTestFile(t, skill, "---\nname: feature-dev\n---\nINJECTED by an earlier stage\n")
	ctx, cancel := context.WithCancelCause(context.Background())
	cancel(errors.New("terminated"))
	lc.finish(ctx, false) // paused

	lc2, _ := beginRunLifecycle(root, 7, "")
	if !lc2.resumed {
		t.Fatal("platform re-dispatch of a paused record: lc.resumed = false")
	}
	resetSkillSnapshotForRun(root, 7, "remote-2", lc2.resumed)
	if got := render("remote-2"); strings.Contains(got, "INJECTED") || !strings.Contains(got, "run-start body") {
		t.Errorf("resumed render = %q, want the run-start body, not the edit", got)
	}
	lc2.finish(context.Background(), true) // completed

	lc3, _ := beginRunLifecycle(root, 7, "fix/7")
	if lc3.resumed {
		t.Fatal("a run after a completed one reported lc.resumed")
	}
	resetSkillSnapshotForRun(root, 7, "remote-3", lc3.resumed)
	if got := render("remote-3"); !strings.Contains(got, "INJECTED") {
		t.Errorf("fresh render = %q, want the current skill", got)
	}
}
