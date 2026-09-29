package execution

import (
	"github.com/nightgauge/nightgauge/internal/config"
	"path/filepath"
	"strings"
	"testing"
)

// TestWorktreePathRootsAtTargetRepo pins the #882 half that lives in the path
// builder itself.
//
// The observed run put the worktree at
// LAUNCH/.nightgauge/worktrees/target-issue-227 — created, and left EMPTY. The
// LEAF was already right: the repo qualifier exists so two repos' issue #N
// cannot collide in one workspace, and it named the target repo perfectly well.
// The BASE was wrong, because worktreePath joined m.workspaceRoot while every
// other writer in this file (stageStateDir, repoRoot, the sweep) resolved
// through the injected repo-path resolver. A worktree under the launch repo is
// not merely misfiled: it is checked out from the target repo's git dir, so
// every consumer that derives repo state from the worktree's location reads the
// wrong repository.
func TestWorktreePathRootsAtTargetRepo(t *testing.T) {
	launchRoot := initTestGitRepo(t, "main")
	targetRoot := initTestGitRepo(t, "main")

	m := NewManager(launchRoot, nil)
	m.SetRepoPathResolver(func(repo string) string {
		if repo == "owner/target" {
			return targetRoot
		}
		return ""
	})

	// The base is resolved for the TARGET repo: its default is keyed on the
	// target's git common dir, so it differs from the launch repo's (#2038).
	targetBase, err := config.ResolveWorktreeBase(targetRoot)
	if err != nil {
		t.Fatalf("ResolveWorktreeBase(target): %v", err)
	}
	launchBase, err := config.ResolveWorktreeBase(launchRoot)
	if err != nil {
		t.Fatalf("ResolveWorktreeBase(launch): %v", err)
	}
	if targetBase == launchBase {
		t.Fatalf("two clones share one default worktree base %q", targetBase)
	}

	got := mustWorktreePath(t, m, "owner/target", 227)
	want := filepath.Join(targetBase, "target-issue-227")
	if got != want {
		t.Errorf("worktreePath(owner/target, 227) = %q, want %q", got, want)
	}
	if strings.HasPrefix(got, launchBase+string(filepath.Separator)) {
		t.Errorf("the worktree for another repo's issue was placed in the LAUNCH repo's base %q: %q (#882)", launchBase, got)
	}

	// A repo the resolver does not know still resolves against the workspace
	// root — the manager is not the layer that refuses; the scheduler's
	// repo-root preflight is, and it refuses before any worktree is asked for.
	if got := mustWorktreePath(t, m, "owner/unknown", 1); got != filepath.Join(launchBase, "unknown-issue-1") {
		t.Errorf("unresolved repo worktreePath = %q", got)
	}
}
