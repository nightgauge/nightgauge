package doctor

import (
	"context"
	"os"
	"path/filepath"
	"strconv"
	"testing"

	"github.com/nightgauge/nightgauge/internal/gittest"
)

// installFakeDocker puts a recording docker shim on PATH so ListIssueProjects
// returns a scripted set of issue-* compose projects. Mirrors the fixture in
// cmd/nightgauge/cleanup_test.go, embedded here so this package's tests stay
// self-contained.
func installFakeDocker(t *testing.T) {
	t.Helper()
	dir := t.TempDir()
	script := `#!/bin/sh
echo "$@" >> "$FAKE_DOCKER_LOG"
case "$1" in
  version) exit 0 ;;
  compose)
    case "$2" in
      ls) printf '%s' "${FAKE_DOCKER_LS_OUTPUT:-[]}" ; exit 0 ;;
    esac ;;
esac
exit 0
`
	if err := os.WriteFile(filepath.Join(dir, "docker"), []byte(script), 0o755); err != nil {
		t.Fatalf("write fake docker: %v", err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("FAKE_DOCKER_LOG", filepath.Join(dir, "calls.log"))
}

// crossRepoWorkspace builds the exact shape #323 is about: a primary repo
// carrying the workspace manifest, and a SIBLING repo holding the live run's
// worktree. Since #229 a cross-repo run registers its worktree in the target
// repo, so a scan rooted at the operator's cwd never sees it.
// Returns the primary repo root, which is where the operator runs `doctor`.
func crossRepoWorkspace(t *testing.T, issue int) string {
	t.Helper()
	base, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatalf("resolve temp dir: %v", err)
	}
	mkRepo := func(name string) string {
		root := filepath.Join(base, name)
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
		return root
	}

	primary := mkRepo("primary")
	sibling := mkRepo("sibling")

	// The live cross-repo run: its worktree is registered in the SIBLING.
	gittest.Run(t, sibling, "worktree", "add",
		filepath.Join(sibling, ".worktrees", "issue-"+strconv.Itoa(issue)),
		"-b", "fix/"+strconv.Itoa(issue)+"-work")

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
	return primary
}

// TestFindOrphanedComposeProjects_CrossRepoRunNotOrphaned is #323's headline
// acceptance criterion. A live cross-repo run's compose stack must not be
// reported as orphaned just because its worktree lives in a sibling repo.
//
// Against the pre-fix implementation this fails loudly: that version ran `git
// worktree list` with no cmd.Dir, saw only the primary repo, found no worktree
// for the issue, and named the live run's stack in the operator's report
// alongside "run `nightgauge cleanup`".
func TestFindOrphanedComposeProjects_CrossRepoRunNotOrphaned(t *testing.T) {
	installFakeDocker(t)
	t.Setenv("FAKE_DOCKER_LS_OUTPUT", `[{"Name":"issue-451","Status":"running(1)"}]`)
	primary := crossRepoWorkspace(t, 451)

	orphans, determined := findOrphanedComposeProjects(context.Background(), primary)

	if !determined {
		t.Fatal("a readable workspace must produce a determined answer")
	}
	for _, p := range orphans {
		if p.IssueNumber == 451 {
			t.Fatalf("issue-451 has a LIVE worktree in the sibling repo — reporting it orphaned tells the operator to tear down a running pipeline (%v)", orphans)
		}
	}
}

// TestFindOrphanedComposeProjects_UnreadableWorktreeSetIsUnverifiable is the
// #280 rule: a check that could not read its evidence must say so, never report
// a clean result. With no readable worktree set EVERY compose project looks
// orphaned, so silently returning them would name live runs.
func TestFindOrphanedComposeProjects_UnreadableWorktreeSetIsUnverifiable(t *testing.T) {
	installFakeDocker(t)
	t.Setenv("FAKE_DOCKER_LS_OUTPUT", `[{"Name":"issue-452","Status":"running(1)"}]`)
	outside, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("GIT_CEILING_DIRECTORIES", filepath.Dir(outside))

	orphans, determined := findOrphanedComposeProjects(context.Background(), outside)

	if determined {
		t.Error("no git repo and no workspace means the active-worktree set is unknown — that is not evidence that issue-452 is orphaned")
	}
	if len(orphans) != 0 {
		t.Errorf("an undetermined scan must report no orphans at all, got %v", orphans)
	}
}

// TestFindOrphanedComposeProjects_GenuineOrphanStillReported guards against the
// fix degrading into "never report anything". A compose stack whose worktree
// really is gone is exactly what this check exists to surface.
func TestFindOrphanedComposeProjects_GenuineOrphanStillReported(t *testing.T) {
	installFakeDocker(t)
	t.Setenv("FAKE_DOCKER_LS_OUTPUT", `[{"Name":"issue-999","Status":"exited(0)"}]`)
	primary := crossRepoWorkspace(t, 453) // 453 is live; 999 is not

	orphans, determined := findOrphanedComposeProjects(context.Background(), primary)

	if !determined {
		t.Fatal("a readable workspace must produce a determined answer")
	}
	found := false
	for _, p := range orphans {
		if p.IssueNumber == 999 {
			found = true
		}
	}
	if !found {
		t.Errorf("issue-999 has no worktree anywhere and must be reported orphaned, got %v", orphans)
	}
}
