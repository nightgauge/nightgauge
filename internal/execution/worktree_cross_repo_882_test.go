package execution

import (
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
	launchRoot := t.TempDir()
	targetRoot := t.TempDir()

	m := NewManager(launchRoot, nil)
	m.SetRepoPathResolver(func(repo string) string {
		if repo == "owner/target" {
			return targetRoot
		}
		return ""
	})

	got := m.worktreePath("owner/target", 227)
	want := filepath.Join(targetRoot, ".nightgauge", "worktrees", "target-issue-227")
	if got != want {
		t.Errorf("worktreePath(owner/target, 227) = %q, want %q", got, want)
	}
	if strings.HasPrefix(got, launchRoot+string(filepath.Separator)) {
		t.Errorf("the worktree for another repo's issue was placed inside the LAUNCH repo %q: %q (#882)", launchRoot, got)
	}
	// It must agree with every other writer's root for the same repo, or the
	// run's worktree and its pipeline state split across two repositories.
	if base := m.RepoRoot("owner/target"); !strings.HasPrefix(got, base+string(filepath.Separator)) {
		t.Errorf("worktreePath %q is not under RepoRoot %q", got, base)
	}

	// A repo the resolver does not know still lands at the workspace root —
	// the manager is not the layer that refuses; the scheduler's repo-root
	// preflight is, and it refuses before any worktree is asked for.
	if got := m.worktreePath("owner/unknown", 1); got != filepath.Join(launchRoot, ".nightgauge", "worktrees", "unknown-issue-1") {
		t.Errorf("unresolved repo worktreePath = %q", got)
	}
}
