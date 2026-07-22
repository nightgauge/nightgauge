package main

import (
	"testing"

	gitpkg "github.com/nightgauge/nightgauge/internal/git"
	gogit "github.com/go-git/go-git/v5"
)

// TestBranchCreateDetachedHead verifies that branch-create succeeds when HEAD
// is detached (the standard pipeline worktree execution environment).
func TestBranchCreateDetachedHead(t *testing.T) {
	dir := t.TempDir()

	repo, err := gitpkg.InitRepo(dir)
	if err != nil {
		t.Fatalf("InitRepo: %v", err)
	}
	if err := gitpkg.CreateInitialCommit(repo, dir); err != nil {
		t.Fatalf("CreateInitialCommit: %v", err)
	}

	svc := gitpkg.NewServiceFromRepo(repo, dir)

	// Detach HEAD by checking out via commit hash.
	head, err := repo.Head()
	if err != nil {
		t.Fatalf("Head: %v", err)
	}
	wt, err := repo.Worktree()
	if err != nil {
		t.Fatalf("Worktree: %v", err)
	}
	if err = wt.Checkout(&gogit.CheckoutOptions{Hash: head.Hash()}); err != nil {
		t.Fatalf("detach HEAD: %v", err)
	}

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
