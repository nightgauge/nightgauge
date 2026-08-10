package execution

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestWorktreePathDerivation_CreationAndTeardownAgree pins the #400 agreement:
// creation and teardown derive the run's worktree directory from one function
// (Manager.worktreePath), so the directory ensureWorktree creates is exactly the
// directory CleanupWorktree removes — from disk AND from git's worktree list.
//
// The name shape itself is part of the contract: "{repo}-issue-{N}", not the
// bare "issue-{N}" the VSCode extension's WorktreeManager uses. Every run in a
// multi-repo workspace shares one {workspaceRoot}/.nightgauge/worktrees/ root,
// so the "{repo}-" prefix is what keeps two repos' issue #{N} from colliding —
// and IssueNumberFromWorktreeDir must still read the issue number back out of
// it (the single-parser contract).
func TestWorktreePathDerivation_CreationAndTeardownAgree(t *testing.T) {
	const repo = "nightgauge/nightgauge"
	const issue = 400

	repoRoot := initTestGitRepo(t, "main")

	// A docker that fails `version` makes IsAvailable false, so compose teardown
	// soft-fails and the run does not depend on a docker daemon.
	fakeDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(fakeDir, "docker"), []byte("#!/bin/sh\nexit 1\n"), 0o755); err != nil {
		t.Fatalf("write failing docker shim: %v", err)
	}
	t.Setenv("PATH", fakeDir+string(os.PathListSeparator)+os.Getenv("PATH"))

	m := &Manager{workspaceRoot: repoRoot}
	created, err := m.ensureWorktree(repo, issue)
	if err != nil {
		t.Fatalf("ensureWorktree: %v", err)
	}

	wantBase := fmt.Sprintf("%s-issue-%d", "nightgauge", issue)
	if got := filepath.Base(created); got != wantBase {
		t.Fatalf("worktree base name = %q, want %q — the Go execution layout is {repo}-issue-{N}", got, wantBase)
	}
	if got, want := filepath.Dir(created), filepath.Join(repoRoot, ".nightgauge", "worktrees"); got != want {
		t.Fatalf("worktree parent = %q, want %q", got, want)
	}
	if n, ok := IssueNumberFromWorktreeDir(wantBase); !ok || n != issue {
		t.Fatalf("IssueNumberFromWorktreeDir(%q) = (%d, %v), want (%d, true) — the single parser must read "+
			"the shape the Go layer creates", wantBase, n, ok, issue)
	}
	if _, err := os.Stat(created); err != nil {
		t.Fatalf("worktree must exist on disk after ensureWorktree: %v", err)
	}
	if !gitWorktreeListed(t, repoRoot, wantBase) {
		t.Fatalf("git worktree list must know %q after ensureWorktree", wantBase)
	}

	if err := m.CleanupWorktree(repo, issue); err != nil {
		t.Fatalf("CleanupWorktree: %v", err)
	}

	if _, err := os.Stat(created); !os.IsNotExist(err) {
		t.Errorf("CleanupWorktree must remove the directory ensureWorktree created (%s); stat err = %v",
			created, err)
	}
	if gitWorktreeListed(t, repoRoot, wantBase) {
		t.Errorf("git worktree list still knows %q after CleanupWorktree — teardown derived a different path "+
			"than creation", wantBase)
	}
}

// gitWorktreeListed reports whether repoRoot's worktree list holds an entry
// whose directory base name is base. Compared by base name because git reports
// the resolved path (macOS /var → /private/var), which is not the string the
// Manager derived.
func gitWorktreeListed(t *testing.T, repoRoot, base string) bool {
	t.Helper()
	out, err := gitOutput(repoRoot, "worktree", "list", "--porcelain")
	if err != nil {
		t.Fatalf("git worktree list: %v", err)
	}
	for _, line := range strings.Split(out, "\n") {
		path, ok := strings.CutPrefix(strings.TrimSpace(line), "worktree ")
		if ok && filepath.Base(path) == base {
			return true
		}
	}
	return false
}
