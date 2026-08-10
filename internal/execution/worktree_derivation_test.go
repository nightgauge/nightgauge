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

// TestCleanupWorktree_NeverCreatedWorktreeIsSilent pins the other half of #400:
// the teardown WARN is a leak signal, so it may not fire for a run that had no
// Go-layer worktree to begin with.
//
// The scheduler calls CleanupWorktree on every terminal outcome, including
// IPC/extension-dispatched runs that never went through RunStage. Those have no
// {repo}-issue-{N} directory, and the pre-fix teardown still ran `git worktree
// remove` against the path anyway: exit 128, "[WARN] … falling back to manual
// removal", every single time. A warning that fires on the majority of runs is
// not a signal, which is what made the real one (#110) unreadable.
func TestCleanupWorktree_NeverCreatedWorktreeIsSilent(t *testing.T) {
	const repo = "nightgauge/nightgauge"
	const issue = 4001

	repoRoot := initTestGitRepo(t, "main")
	m := &Manager{workspaceRoot: repoRoot}

	// Precondition: nothing was ever provisioned for this issue.
	if _, err := os.Stat(m.worktreePath(repo, issue)); !os.IsNotExist(err) {
		t.Fatalf("precondition: %s must not exist; stat err = %v", m.worktreePath(repo, issue), err)
	}

	logged := captureLog(t, func() {
		if err := m.CleanupWorktree(repo, issue); err != nil {
			t.Fatalf("CleanupWorktree on a never-created worktree must succeed: %v", err)
		}
	})
	if logged != "" {
		t.Errorf("teardown of a worktree that never existed must say nothing, got:\n%s", logged)
	}
}

// TestCleanupWorktree_StaleRegistrationIsStillReclaimed guards the other edge of
// the same short-circuit: "the directory is gone" is not "git has nothing to
// clean". A worktree whose directory was removed out from under the pipeline is
// still registered, `git worktree remove` clears exactly that (quietly, and
// successfully — no WARN was ever involved in this case), and this teardown is
// the thing that runs it on a terminal outcome.
//
// A short-circuit keyed on os.Stat alone would leave the phantom entry in `git
// worktree list`, which is the set the active-worktree scan and the compose
// reconciler answer from — a run's issue would read as active with nothing on
// disk.
func TestCleanupWorktree_StaleRegistrationIsStillReclaimed(t *testing.T) {
	const repo = "nightgauge/nightgauge"
	const issue = 4002

	repoRoot := initTestGitRepo(t, "main")
	m := &Manager{workspaceRoot: repoRoot}

	created, err := m.ensureWorktree(repo, issue)
	if err != nil {
		t.Fatalf("ensureWorktree: %v", err)
	}
	base := filepath.Base(created)
	if err := os.RemoveAll(created); err != nil {
		t.Fatalf("remove worktree directory: %v", err)
	}
	if !gitWorktreeListed(t, repoRoot, base) {
		t.Fatalf("precondition: git must still list %q after its directory was deleted", base)
	}

	logged := captureLog(t, func() {
		if err := m.CleanupWorktree(repo, issue); err != nil {
			t.Fatalf("CleanupWorktree: %v", err)
		}
	})
	if gitWorktreeListed(t, repoRoot, base) {
		t.Errorf("git worktree list still knows %q — teardown stopped reclaiming stale registrations", base)
	}
	if logged != "" {
		t.Errorf("reclaiming a stale registration is not a failure and must stay quiet, got:\n%s", logged)
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
