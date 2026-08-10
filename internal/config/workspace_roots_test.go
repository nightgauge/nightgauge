package config

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// workspaceWithSibling builds the on-disk shape WorkspaceRepoRoots must
// understand: a primary repo carrying .vscode/nightgauge-workspace.yaml and a
// sibling repo referenced from it by a relative `../` path, exactly as the real
// manifest does. Returns (primaryRoot, siblingRoot).
func workspaceWithSibling(t *testing.T) (string, string) {
	t.Helper()
	base, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatalf("resolve temp dir: %v", err)
	}
	git := func(dir string, args ...string) {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %s (in %s): %v: %s", strings.Join(args, " "), dir, err, out)
		}
	}
	mkRepo := func(name string) string {
		root := filepath.Join(base, name)
		if err := os.MkdirAll(root, 0o755); err != nil {
			t.Fatal(err)
		}
		git(root, "init", "-b", "main")
		git(root, "config", "user.email", "test@test")
		git(root, "config", "user.name", "test")
		if err := os.WriteFile(filepath.Join(root, "README"), []byte("hi\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		git(root, "add", ".")
		git(root, "commit", "-m", "initial")
		return root
	}

	primary := mkRepo("primary")
	sibling := mkRepo("sibling")

	if err := os.MkdirAll(filepath.Join(primary, ".vscode"), 0o755); err != nil {
		t.Fatal(err)
	}
	manifest := "repositories:\n" +
		"  - name: primary\n    path: .\n    project_number: 3\n" +
		"  - name: sibling\n    path: ../sibling\n    project_number: 4\n"
	if err := os.WriteFile(
		filepath.Join(primary, ".vscode", "nightgauge-workspace.yaml"),
		[]byte(manifest), 0o644); err != nil {
		t.Fatal(err)
	}
	return primary, sibling
}

// TestWorkspaceRepoRoots_IncludesManifestSiblings is the discovery half of
// #323. `doctor` and `cleanup` have no injected repo-roots resolver — only the
// operator's cwd — so a cross-repo run's worktree, registered in its TARGET
// repo since #229, is invisible unless the manifest's siblings are enumerated.
func TestWorkspaceRepoRoots_IncludesManifestSiblings(t *testing.T) {
	primary, sibling := workspaceWithSibling(t)

	roots := WorkspaceRepoRoots(primary)

	has := func(want string) bool {
		for _, r := range roots {
			if r == want {
				return true
			}
		}
		return false
	}
	if !has(primary) {
		t.Errorf("primary repo root %s missing from %v", primary, roots)
	}
	if !has(sibling) {
		t.Errorf("sibling repo root %s missing from %v — a cross-repo run's worktree lives here and would read as orphaned", sibling, roots)
	}
}

// TestWorkspaceRepoRoots_DeduplicatesPrimary: the primary repo is both the git
// toplevel of cwd and a `path: .` manifest entry. Scanning it twice is
// harmless but scanning it twice per root count is how a "roots" list stops
// being a set; assert the contract explicitly.
func TestWorkspaceRepoRoots_DeduplicatesPrimary(t *testing.T) {
	primary, _ := workspaceWithSibling(t)

	roots := WorkspaceRepoRoots(primary)

	seen := 0
	for _, r := range roots {
		if r == primary {
			seen++
		}
	}
	if seen != 1 {
		t.Errorf("primary root appears %d times in %v, want exactly 1", seen, roots)
	}
}

// TestWorkspaceRepoRoots_SingleRepoModeUsesGitToplevel: no workspace manifest
// anywhere is the ordinary single-repo case, not a failure. The local repo must
// still be scanned, and a subdirectory must resolve to the repo root.
func TestWorkspaceRepoRoots_SingleRepoModeUsesGitToplevel(t *testing.T) {
	_, sibling := workspaceWithSibling(t) // sibling carries no manifest of its own
	sub := filepath.Join(sibling, "nested", "deeper")
	if err := os.MkdirAll(sub, 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("GIT_CEILING_DIRECTORIES", filepath.Dir(sibling))

	roots := WorkspaceRepoRoots(sub)

	if len(roots) != 1 || roots[0] != sibling {
		t.Errorf("WorkspaceRepoRoots(%s) = %v, want [%s]", sub, roots, sibling)
	}
}

// TestWorkspaceRepoRoots_OutsideAnyRepoIsEmpty is the contract that makes the
// undetermined verdict reachable: no git repo and no workspace means nothing
// was discovered, and execution.ActiveWorktreeIssues turns an empty root set
// into determined=false. Returning some plausible-looking root here instead
// would restore exactly the #296 failure — a confident empty active set.
func TestWorkspaceRepoRoots_OutsideAnyRepoIsEmpty(t *testing.T) {
	dir, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("GIT_CEILING_DIRECTORIES", filepath.Dir(dir))

	if roots := WorkspaceRepoRoots(dir); len(roots) != 0 {
		t.Errorf("WorkspaceRepoRoots outside any repo = %v, want empty", roots)
	}
}

// mainCheckoutFixture builds a repo with one linked worktree and returns
// (mainCheckout, linkedWorktree). #410's defect lives entirely in the difference
// between those two paths, so the fixture is the whole test.
func mainCheckoutFixture(t *testing.T) (string, string) {
	t.Helper()
	base, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatalf("resolve temp dir: %v", err)
	}
	git := func(dir string, args ...string) {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %s (in %s): %v: %s", strings.Join(args, " "), dir, err, out)
		}
	}
	root := filepath.Join(base, "repo")
	if err := os.MkdirAll(root, 0o755); err != nil {
		t.Fatal(err)
	}
	git(root, "init", "-b", "main")
	git(root, "config", "user.email", "test@test")
	git(root, "config", "user.name", "test")
	if err := os.WriteFile(filepath.Join(root, "README"), []byte("hi\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	git(root, "add", ".")
	git(root, "commit", "-m", "initial")
	wt := filepath.Join(root, ".worktrees", "issue-410")
	git(root, "worktree", "add", wt, "-b", "fix/410-work")
	return root, wt
}

// TestMainCheckoutRoot_FromALinkedWorktree is #410's core correction. A linked
// worktree carries its own (tracked, always empty) .nightgauge/pipeline
// directory, so a destructive caller that derives state from gitToplevel reads a
// determined "nothing is running" while git still lists — and it still
// removes — every worktree of the repository.
func TestMainCheckoutRoot_FromALinkedWorktree(t *testing.T) {
	main, wt := mainCheckoutFixture(t)

	if got := gitToplevel(wt); got != wt {
		t.Fatalf("fixture assumption broken: gitToplevel(worktree) = %q, want the worktree %q", got, wt)
	}
	if got := MainCheckoutRoot(wt); got != main {
		t.Errorf("MainCheckoutRoot(%s) = %q, want the main checkout %q", wt, got, main)
	}
}

// TestMainCheckoutRoot_FromTheMainCheckoutIsIdentity: canonicalization must be a
// no-op where the root is already right, including from a subdirectory.
func TestMainCheckoutRoot_FromTheMainCheckoutIsIdentity(t *testing.T) {
	main, _ := mainCheckoutFixture(t)
	sub := filepath.Join(main, "nested", "deeper")
	if err := os.MkdirAll(sub, 0o755); err != nil {
		t.Fatal(err)
	}

	for _, dir := range []string{main, sub} {
		if got := MainCheckoutRoot(dir); got != main {
			t.Errorf("MainCheckoutRoot(%s) = %q, want %q", dir, got, main)
		}
	}
}

// TestMainCheckoutRoot_NoWorkTreeIsEmpty: a bare repo and a non-repo directory
// both have no checkout to canonicalize to. Returning a plausible-looking parent
// directory instead is how a destructive caller ends up sweeping a path nobody
// verified — the caller must skip such a root, so the answer has to be empty.
func TestMainCheckoutRoot_NoWorkTreeIsEmpty(t *testing.T) {
	base, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("GIT_CEILING_DIRECTORIES", filepath.Dir(base))

	bare := filepath.Join(base, "origin.git")
	cmd := exec.Command("git", "init", "--bare", "-b", "main", bare)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("init bare: %v: %s", err, out)
	}
	plain := filepath.Join(base, "not-a-repo")
	if err := os.MkdirAll(plain, 0o755); err != nil {
		t.Fatal(err)
	}

	for _, dir := range []string{bare, plain, ""} {
		if got := MainCheckoutRoot(dir); got != "" {
			t.Errorf("MainCheckoutRoot(%q) = %q, want \"\" — no work tree means no root to sweep", dir, got)
		}
	}
}
