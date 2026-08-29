package main

import (
	"bytes"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// #223. `worktree recover` is the extension's only route to the rescue that
// previously existed solely as an in-process call on the Go scheduler's failure
// path. These drive real git for the same reason the gate tests do: the defect
// being fixed was code that looked correct and never touched the filesystem.

func recoverRepo(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	run := func(args ...string) {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, out)
		}
	}
	run("init")
	run("checkout", "-b", "main")
	run("config", "user.email", "recover-test@example.com")
	run("config", "user.name", "Recover Test")
	if err := os.WriteFile(filepath.Join(dir, "README.md"), []byte("base\n"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	run("add", ".")
	run("commit", "-m", "base")
	return dir
}

func runRecover(t *testing.T, args ...string) (string, error) {
	t.Helper()
	cmd := worktreeRecoverCmd()
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	cmd.SetArgs(args)
	err := cmd.Execute()
	return out.String(), err
}

func gitLines(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("git %s: %v", strings.Join(args, " "), err)
	}
	return string(out)
}

func TestWorktreeRecover_CommitsDeliverableWork(t *testing.T) {
	dir := recoverRepo(t)
	if err := os.WriteFile(filepath.Join(dir, "impl.go"), []byte("package main\n"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}

	out, err := runRecover(t, "--worktree", dir, "--issue", "221", "--stage", "feature-dev", "--json")
	if err != nil {
		t.Fatalf("recover: %v (%s)", err, out)
	}

	var res struct {
		Recovered bool   `json:"recovered"`
		Message   string `json:"message"`
	}
	if err := json.Unmarshal([]byte(out), &res); err != nil {
		t.Fatalf("parse %q: %v", out, err)
	}
	if !res.Recovered {
		t.Fatalf("recovered=false, want true: %s", res.Message)
	}
	if status := gitLines(t, dir, "status", "--porcelain"); strings.TrimSpace(status) != "" {
		t.Errorf("worktree still dirty after recovery:\n%s", status)
	}
	if log := gitLines(t, dir, "log", "--oneline", "-1"); !strings.Contains(log, "auto-recovery") {
		t.Errorf("commit message does not mark the rescue: %q", log)
	}
	if files := gitLines(t, dir, "show", "--name-only", "--format=", "HEAD"); !strings.Contains(files, "impl.go") {
		t.Errorf("recovery commit does not contain the work: %q", files)
	}
}

// The bookkeeping exclusion is the difference between a rescue and publishing
// pipeline exhaust into someone's PR (#202). Asserted here rather than trusted
// from the scheduler's own tests, because this command is a separate entry
// point into the same function and a future refactor could bypass it.
func TestWorktreeRecover_ExcludesBookkeeping(t *testing.T) {
	dir := recoverRepo(t)
	for _, p := range []string{".nightgauge/pipeline/dev-221.json", ".claude/notes.md"} {
		full := filepath.Join(dir, p)
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatalf("mkdir: %v", err)
		}
		if err := os.WriteFile(full, []byte("{}\n"), 0o644); err != nil {
			t.Fatalf("write: %v", err)
		}
	}
	if err := os.WriteFile(filepath.Join(dir, "impl.go"), []byte("package main\n"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}

	if out, err := runRecover(t, "--worktree", dir, "--issue", "221", "--stage", "feature-dev"); err != nil {
		t.Fatalf("recover: %v (%s)", err, out)
	}

	files := gitLines(t, dir, "show", "--name-only", "--format=", "HEAD")
	if !strings.Contains(files, "impl.go") {
		t.Errorf("deliverable missing from recovery commit: %q", files)
	}
	if strings.Contains(files, ".nightgauge") || strings.Contains(files, ".claude") {
		t.Errorf("recovery commit published bookkeeping: %q", files)
	}
}

// A workspace whose only changes are bookkeeping has produced nothing. Treating
// that as recoverable would put an empty-ish commit on the branch after every
// failed stage.
func TestWorktreeRecover_BookkeepingOnlyIsNoOp(t *testing.T) {
	dir := recoverRepo(t)
	full := filepath.Join(dir, ".nightgauge", "pipeline", "dev-221.json")
	if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(full, []byte("{}\n"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}

	out, err := runRecover(t, "--worktree", dir, "--issue", "221", "--stage", "feature-dev", "--json")
	if err != nil {
		t.Fatalf("recover: %v (%s)", err, out)
	}
	if strings.Contains(out, `"recovered":true`) {
		t.Errorf("bookkeeping-only worktree reported as recovered: %s", out)
	}
	if log := gitLines(t, dir, "log", "--oneline"); strings.Contains(log, "auto-recovery") {
		t.Errorf("created a recovery commit for bookkeeping only: %q", log)
	}
}

// #332 / #237. A stage whose entire deliverable is bookkeeping produced real
// work, and the pre-fix rescue erased exactly that work while reporting
// success: `worktreeHasChanges` scoped its probe to ci.DeliverablePathspec()
// (which excludes `.nightgauge` wholesale) so the tree read clean, and
// `RecoverUncommittedWork` ran `git reset -- .nightgauge`, which restores every
// staged deletion from HEAD.
//
// This is the shape of open issue #701: 209 staged deletions under
// `.nightgauge/pipeline/assessments/`, with origin/main still tracking them.
// The assertions are on the SIDE EFFECT — what the commit contains and what git
// tracks afterwards — because an error-only check passes against the bug.
func TestWorktreeRecover_RescuesBookkeepingOnlyDeliverable(t *testing.T) {
	dir := recoverRepo(t)
	writeAt := func(rel, content string) {
		t.Helper()
		full := filepath.Join(dir, rel)
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatalf("mkdir: %v", err)
		}
		if err := os.WriteFile(full, []byte(content), 0o644); err != nil {
			t.Fatalf("write %s: %v", rel, err)
		}
	}
	git := func(args ...string) {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, out)
		}
	}

	// The production shape: the assessment is tracked AND matched by the
	// repo's own ignore rules, which is what makes `git add` refuse to
	// re-stage it and why the fix must avoid unstaging it in the first place.
	const assessment = ".nightgauge/pipeline/assessments/issue-42.json"
	writeAt(".nightgauge/.gitignore", "pipeline/*\n")
	writeAt(assessment, "{}\n")
	git("add", "-f", ".nightgauge/.gitignore", assessment)
	git("commit", "-m", "track pipeline assessments")

	// The deliverable: untrack them. Nothing else changes in the worktree.
	git("rm", "--cached", "-q", assessment)
	// Pipeline exhaust scaffolded alongside it must still be kept out.
	writeAt(".nightgauge/knowledge/README.md", "# Knowledge Base\n")

	out, err := runRecover(t, "--worktree", dir, "--issue", "701", "--stage", "feature-dev", "--json")
	if err != nil {
		t.Fatalf("recover: %v (%s)", err, out)
	}
	if !strings.Contains(out, `"recovered":true`) {
		t.Fatalf("a bookkeeping-only deliverable must be recoverable, got %s", out)
	}

	files := gitLines(t, dir, "show", "--name-only", "--format=", "HEAD")
	if !strings.Contains(files, "assessments/issue-42.json") {
		t.Errorf("the recovery commit does not carry the deliverable: %q", files)
	}
	if strings.Contains(files, "knowledge/README.md") {
		t.Errorf("the recovery commit published pipeline exhaust: %q", files)
	}
	// The deliverable was "stop tracking this file". If the rescue restored it
	// from HEAD, git still tracks it and the work is gone.
	if tracked := gitLines(t, dir, "ls-files", "--", assessment); strings.TrimSpace(tracked) != "" {
		t.Errorf("the rescue restored the file it was meant to untrack: still tracked as %q", tracked)
	}
	if _, err := os.Stat(filepath.Join(dir, ".nightgauge", "knowledge", "README.md")); err != nil {
		t.Errorf("exhaust must be left in the worktree, not deleted: %v", err)
	}
}

func TestWorktreeRecover_CleanWorktreeIsNoOp(t *testing.T) {
	dir := recoverRepo(t)

	out, err := runRecover(t, "--worktree", dir, "--issue", "221", "--stage", "feature-dev", "--json")
	if err != nil {
		t.Fatalf("recover: %v (%s)", err, out)
	}
	if !strings.Contains(out, `"recovered":false`) {
		t.Errorf("clean worktree should report recovered=false: %s", out)
	}
}

func TestWorktreeRecover_RequiresWorktreeAndIssue(t *testing.T) {
	if _, err := runRecover(t, "--issue", "221"); err == nil {
		t.Error("expected an error when --worktree is omitted")
	}
	if _, err := runRecover(t, "--worktree", t.TempDir()); err == nil {
		t.Error("expected an error when --issue is omitted")
	}
}

// TestWorktreeRecover_ReportsWithheldDeletions pins the operator-facing half of
// #1108. The rescue now publishes what it can and holds back the deletions of a
// deletion-dominated tree — and a partial rescue that printed a bare success
// would be the original defect one layer up: the operator told the work was
// committed, never learning that the deletions are still sitting in the
// worktree.
func TestWorktreeRecover_ReportsWithheldDeletions(t *testing.T) {
	dir := recoverRepo(t)
	run := func(args ...string) {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, out)
		}
	}
	for i := 0; i < 6; i++ {
		name := filepath.Join(dir, "gen"+string(rune('a'+i))+".g.txt")
		if err := os.WriteFile(name, []byte("generated\n"), 0o644); err != nil {
			t.Fatalf("write: %v", err)
		}
	}
	if err := os.WriteFile(filepath.Join(dir, "src.txt"), []byte("v1\n"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	run("add", ".")
	run("commit", "-m", "baseline")

	for i := 0; i < 6; i++ {
		if err := os.Remove(filepath.Join(dir, "gen"+string(rune('a'+i))+".g.txt")); err != nil {
			t.Fatalf("remove: %v", err)
		}
	}
	if err := os.WriteFile(filepath.Join(dir, "src.txt"), []byte("v2 — the deliverable\n"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}

	out, err := runRecover(t, "--worktree", dir, "--issue", "1108", "--stage", "feature-dev", "--json")
	if err != nil {
		t.Fatalf("recover = %v (%s)", err, out)
	}
	var result struct {
		Recovered         bool     `json:"recovered"`
		Message           string   `json:"message"`
		WithheldReason    string   `json:"withheld_reason"`
		WithheldDeletions []string `json:"withheld_deletions"`
	}
	if decErr := json.Unmarshal([]byte(out), &result); decErr != nil {
		t.Fatalf("decode %q: %v", out, decErr)
	}
	if !result.Recovered {
		t.Error("recovered=false; the modification was rescuable and must be reported as rescued")
	}
	if result.WithheldReason == "" {
		t.Error("withheld_reason is empty; a partial rescue must say what it held back")
	}
	if len(result.WithheldDeletions) != 6 {
		t.Errorf("withheld_deletions = %v, want the 6 deletions", result.WithheldDeletions)
	}
	if !strings.Contains(result.Message, "withheld") {
		t.Errorf("message = %q, want it to name the withholding", result.Message)
	}
	// The deliverable landed; the deletions did not.
	if got := gitLines(t, dir, "show", "HEAD:src.txt"); got != "v2 — the deliverable\n" {
		t.Errorf("HEAD:src.txt = %q, want the edited contents", got)
	}
	if got := gitLines(t, dir, "ls-tree", "-r", "--name-only", "HEAD"); !strings.Contains(got, "gena.g.txt") {
		t.Errorf("HEAD lost gena.g.txt; the withheld deletion was published:\n%s", got)
	}
}
