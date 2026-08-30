package main

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/nightgauge/nightgauge/internal/gittest"
)

// `knowledge scaffold` must anchor at the main checkout, not the cwd (#1205).
//
// The verb resolved its root with a bare os.Getwd(). The issue-pickup skill ran
// it from the stage's cwd, which on the scheduler path is the run's worktree, so
// the knowledge base was written into <worktree>/.nightgauge/knowledge — a
// gitignored directory that is deleted with the worktree at reclamation.
//
// The fixture builds a REAL linked worktree: against two unrelated temp
// directories the broken and the fixed code are indistinguishable, because the
// difference is entirely in what `git rev-parse --git-common-dir` reports.
func scaffoldWorktreeFixture(t *testing.T) (string, string) {
	t.Helper()
	base, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatalf("resolve temp dir: %v", err)
	}
	root := filepath.Join(base, "repo")
	if err := os.MkdirAll(root, 0o755); err != nil {
		t.Fatal(err)
	}
	gittest.Run(t, root, "init", "-b", "main")
	gittest.Run(t, root, "config", "user.email", "test@test")
	gittest.Run(t, root, "config", "user.name", "test")
	if err := os.WriteFile(filepath.Join(root, "README"), []byte("hi\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	gittest.Run(t, root, "add", ".")
	gittest.Run(t, root, "commit", "-m", "initial")
	wt := filepath.Join(root, ".worktrees", "issue-1205")
	gittest.Run(t, root, "worktree", "add", wt, "-b", "fix/1205-work")
	return root, wt
}

func TestKnowledgeScaffoldCmd_AnchorsAtTheMainCheckout(t *testing.T) {
	root, wt := scaffoldWorktreeFixture(t)

	// --workdir is the worktree — the same value os.Getwd() returned on the
	// path that produced the bug.
	cmd := knowledgeScaffoldCmd()
	cmd.SetArgs([]string{
		"--issue-number", "1205",
		"--title", "scaffold lands in the run worktree",
		"--workdir", wt,
		"--knowledge-enabled", "true",
		"--workspace-scoped", "false",
	})
	cmd.SetOut(os.Stderr)
	if err := cmd.Execute(); err != nil {
		t.Fatalf("scaffold: %v", err)
	}

	rootDirs, _ := filepath.Glob(filepath.Join(root, ".nightgauge", "knowledge", "features", "1205-*"))
	if len(rootDirs) != 1 {
		t.Fatalf("scaffold at the main checkout = %v, want exactly one directory", rootDirs)
	}
	if _, err := os.Stat(filepath.Join(rootDirs[0], "PRD.md")); err != nil {
		t.Errorf("PRD.md missing: %v", err)
	}

	// This is the assertion that goes red when the MainCheckoutRoot call is
	// removed from the verb.
	wtDirs, _ := filepath.Glob(filepath.Join(wt, ".nightgauge", "knowledge", "features", "1205-*"))
	if len(wtDirs) != 0 {
		t.Errorf("scaffold landed in the worktree at %v — reclaimed with the "+
			"worktree after merge, which is #1205", wtDirs)
	}
}

// Outside a git repository there is nothing to canonicalize to, and the verb
// must still work rather than fail or write somewhere surprising.
func TestKnowledgeScaffoldCmd_OutsideARepoUsesTheGivenWorkdir(t *testing.T) {
	dir, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("GIT_CEILING_DIRECTORIES", filepath.Dir(dir))

	cmd := knowledgeScaffoldCmd()
	cmd.SetArgs([]string{
		"--issue-number", "1205", "--title", "t",
		"--workdir", dir, "--knowledge-enabled", "true", "--workspace-scoped", "false",
	})
	cmd.SetOut(os.Stderr)
	if err := cmd.Execute(); err != nil {
		t.Fatalf("scaffold outside a repo: %v", err)
	}
	if dirs, _ := filepath.Glob(filepath.Join(dir, ".nightgauge", "knowledge", "features", "1205-*")); len(dirs) != 1 {
		t.Errorf("scaffold = %v, want it created under the given workdir", dirs)
	}
}
