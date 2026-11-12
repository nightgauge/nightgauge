package main

import (
	"testing"

	gitpkg "github.com/nightgauge/nightgauge/internal/git"
	"github.com/nightgauge/nightgauge/internal/gittest"
)

// TestBranchCreateDetachedHead verifies that branch-create succeeds when HEAD
// is detached (the standard pipeline worktree execution environment).
func TestBranchCreateDetachedHead(t *testing.T) {
	dir := t.TempDir()

	gittest.InitRepo(t, dir, "-q", "-b", "master")
	gittest.InitialCommit(t, dir)

	svc, err := gitpkg.NewService(dir)
	if err != nil {
		t.Fatalf("NewService: %v", err)
	}

	// Detach HEAD by checking out the commit itself.
	gittest.Run(t, dir, "checkout", "-q", "--detach", "HEAD")

	// Confirm HEAD is detached (CurrentBranch should error).
	if _, err := svc.CurrentBranch(); err == nil {
		t.Fatal("expected CurrentBranch to error on detached HEAD")
	}

	// DefaultBranch should resolve to "master" via local ref.
	defaultBranch, err := svc.DefaultBranch()
	if err != nil {
		t.Fatalf("DefaultBranch in detached HEAD context: %v", err)
	}
	if defaultBranch != "master" {
		t.Errorf("DefaultBranch = %q, want 'master'", defaultBranch)
	}

	// BranchCreateFrom should succeed using the default branch as base.
	const newBranch = "feat/3485-detached-test"
	if err := svc.BranchCreateFrom(newBranch, defaultBranch); err != nil {
		t.Fatalf("BranchCreateFrom in detached HEAD context: %v", err)
	}

	// Verify branch was created.
	exists, err := svc.LocalBranchExists(newBranch)
	if err != nil {
		t.Fatalf("LocalBranchExists: %v", err)
	}
	if !exists {
		t.Errorf("branch %q was not created", newBranch)
	}
}
