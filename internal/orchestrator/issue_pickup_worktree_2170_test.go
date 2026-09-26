package orchestrator

import (
	"context"
	"github.com/nightgauge/nightgauge/internal/gittest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/nightgauge/nightgauge/internal/execution"
	"github.com/nightgauge/nightgauge/internal/state"
	"github.com/nightgauge/nightgauge/pkg/types"
)

// #2170: the deterministic issue-pickup hook runs before the first stage
// dispatch, and the run's worktree used to be provisioned only inside that
// dispatch, so the branch was checked out in the PRIMARY checkout.

func headOf(t *testing.T, dir string) string {
	t.Helper()
	out, err := gittest.Command(dir, "rev-parse", "--abbrev-ref", "HEAD").CombinedOutput()
	if err != nil {
		t.Fatalf("rev-parse HEAD in %s: %v\n%s", dir, err, out)
	}
	return strings.TrimSpace(string(out))
}

// pickupRepo is a primary checkout on main with a bare origin to push to.
func pickupRepo(t *testing.T) string {
	t.Helper()
	root := gitWorkspace(t)
	origin := filepath.Join(t.TempDir(), "origin.git")
	gitIn(t, root, "init", "--bare", "-q", origin)
	gitIn(t, root, "remote", "add", "origin", origin)
	gitIn(t, root, "push", "-q", "origin", "main")
	return root
}

func remoteHasBranch(t *testing.T, root, branch string) bool {
	t.Helper()
	out, err := gittest.Command(root, "ls-remote", "--heads", "origin", branch).CombinedOutput()
	if err != nil {
		t.Fatalf("ls-remote: %v\n%s", err, out)
	}
	return strings.TrimSpace(string(out)) != ""
}

// The hook provisions the run's worktree, stamps it, and the production
// runner creates and checks out the branch THERE; the primary checkout stays
// on main.
func TestRunWorktree_ProvisionsWorktreeAndLeavesPrimaryAlone(t *testing.T) {
	root := pickupRepo(t)
	s := &Scheduler{execMgr: execution.NewManager(root, &sleepAdapter{script: "true"})}
	rt := state.NewRuntimeState("acme/app", 1904, "item-1904", "run-2170")
	item := types.BoardItem{Repo: "acme/app", Number: 1904}

	dir, reason := s.runWorktree(rt, item)
	if reason != "" {
		t.Fatalf("runWorktree punted: %s", reason)
	}
	if dir == root || dir == "" {
		t.Fatalf("pickup dir = %q, want the run's worktree, not the primary checkout %q", dir, root)
	}
	if rt.WorktreeDir != dir {
		t.Fatalf("runtime.WorktreeDir = %q, want %q stamped", rt.WorktreeDir, dir)
	}

	res, err := NewDeterministicIssuePickupRunner().Run(context.Background(), pickupInput(dir))
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if got := headOf(t, dir); got != res.Branch {
		t.Errorf("worktree HEAD = %q, want the new branch %q", got, res.Branch)
	}
	if got := headOf(t, root); got != "main" {
		t.Errorf("primary checkout HEAD = %q, want main untouched", got)
	}
	if !res.Pushed || !remoteHasBranch(t, root, res.Branch) {
		t.Errorf("branch %s not pushed from the worktree (pushed=%t)", res.Branch, res.Pushed)
	}

	// A second resolution reuses the stamped worktree.
	again, reason := s.runWorktree(rt, item)
	if reason != "" || again != dir {
		t.Errorf("second runWorktree = (%q, %q), want (%q, \"\")", again, reason, dir)
	}
}

// Without a Go-side adapter (IPC mode) the extension owns the worktree; the
// hook must punt rather than fall back to the primary checkout.
func TestRunWorktree_PuntsWithoutARunWorktree(t *testing.T) {
	root := pickupRepo(t)
	s := &Scheduler{execMgr: execution.NewManager(root, nil)}
	rt := state.NewRuntimeState("acme/app", 1904, "item-1904", "run-2170")

	dir, reason := s.runWorktree(rt, types.BoardItem{Repo: "acme/app", Number: 1904})
	if reason == "" || dir != "" {
		t.Fatalf("runWorktree = (%q, %q), want a punt", dir, reason)
	}
	if got := headOf(t, root); got != "main" {
		t.Errorf("primary checkout HEAD = %q, want main", got)
	}
}

// The git layer already honours a linked worktree: the production runner,
// pointed at a worktree made with `git worktree add --detach`, moves only that
// worktree's HEAD. This covers EnsureIssueBranch's other caller,
// `nightgauge git branch-create`, which opens the same git.Service on its cwd.
func TestDeterministicIssuePickup_LinkedWorktreeOnly(t *testing.T) {
	root := pickupRepo(t)
	wt := filepath.Join(t.TempDir(), "wt")
	gitIn(t, root, "worktree", "add", "-q", "--detach", wt)

	res, err := NewDeterministicIssuePickupRunner().Run(context.Background(), pickupInput(wt))
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if got := headOf(t, wt); got != res.Branch {
		t.Errorf("worktree HEAD = %q, want %q", got, res.Branch)
	}
	if got := headOf(t, root); got != "main" {
		t.Errorf("primary checkout HEAD = %q, want main untouched", got)
	}
}

// A deterministic stage names no model in its completion line.
func TestCompletionLogModel(t *testing.T) {
	if got := completionLogModel("sonnet", "deterministic"); got != "none" {
		t.Errorf("deterministic = %q, want none", got)
	}
	if got := completionLogModel("sonnet", "llm"); got != "sonnet" {
		t.Errorf("llm = %q, want sonnet", got)
	}
}
