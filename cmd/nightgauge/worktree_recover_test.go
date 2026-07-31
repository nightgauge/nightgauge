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
