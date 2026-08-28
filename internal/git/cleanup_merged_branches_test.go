package git

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// CleanupMergedBranches keys entirely on git's "[gone]" upstream marker, which
// only appears once the stale remote-tracking ref has been pruned. That prune
// used to be fire-and-forget (`_ = prune.Run()`).
//
// The failure that produces is the #166 shape: when the fetch cannot run — no
// network, expired credentials, a remote that no longer resolves — nothing is
// ever marked gone, the scan matches nothing, and the function returns
// ([], nil). Over IPC that renders as "0 branches cleaned", which is
// indistinguishable from "there was nothing to clean". The operator concludes
// the repo is tidy; it is not, and never will be, because the same fetch fails
// on every subsequent run.
//
// Nothing crashes and no error surfaces. That is exactly what makes it costly.
func TestCleanupMergedBranches_ReportsUnreachableRemoteInsteadOfEmptySuccess(t *testing.T) {
	repoRoot := initCleanupRepo(t, "main")

	// A remote that cannot possibly resolve. `git fetch --prune` fails, so no
	// branch can ever be marked [gone].
	runGitForCleanup(t, repoRoot, "remote", "add", "origin",
		filepath.Join(t.TempDir(), "definitely-not-a-repo"))

	svc, err := NewService(repoRoot)
	if err != nil {
		t.Fatalf("NewService: %v", err)
	}

	deleted, err := svc.CleanupMergedBranches()

	if err == nil {
		t.Fatalf("expected an error when the prune cannot run; got nil with deleted=%v.\n"+
			"A silent ([], nil) here reports 'nothing to clean' for a repo that was never scanned.", deleted)
	}
	if len(deleted) != 0 {
		t.Errorf("no branch should be reported deleted when the scan never ran, got %v", deleted)
	}
	// The message must name the cause; "fetch failed" alone sends the reader
	// looking in the wrong place.
	if !strings.Contains(err.Error(), "prune") {
		t.Errorf("error should name the prune as the failed precondition, got: %v", err)
	}
}

// The healthy path must still work — a guard that turns every call into an
// error would "fix" the silent no-op by breaking the feature.
func TestCleanupMergedBranches_DeletesGoneBranchesWhenPruneSucceeds(t *testing.T) {
	// A real upstream repo so `git fetch --prune` genuinely succeeds.
	upstream := initCleanupRepo(t, "main")
	runGitForCleanup(t, upstream, "config", "receive.denyCurrentBranch", "ignore")

	clone := t.TempDir()
	if out, err := exec.Command("git", "clone", upstream, clone).CombinedOutput(); err != nil {
		t.Fatalf("clone: %v: %s", err, out)
	}
	runGitForCleanup(t, clone, "config", "user.email", "test@test")
	runGitForCleanup(t, clone, "config", "user.name", "test")

	// Publish a branch, then delete it upstream — the post-merge shape.
	runGitForCleanup(t, clone, "checkout", "-b", "feat/shipped")
	if err := os.WriteFile(filepath.Join(clone, "f.txt"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	runGitForCleanup(t, clone, "add", ".")
	runGitForCleanup(t, clone, "commit", "-m", "work")
	runGitForCleanup(t, clone, "push", "-u", "origin", "feat/shipped")
	runGitForCleanup(t, upstream, "branch", "-D", "feat/shipped")

	// Must not be the checked-out branch, or it is protected.
	runGitForCleanup(t, clone, "checkout", "main")

	svc := mustService(t, clone)
	deleted, err := svc.CleanupMergedBranches()
	if err != nil {
		t.Fatalf("healthy path must not error: %v", err)
	}

	var found bool
	for _, b := range deleted {
		if b == "feat/shipped" {
			found = true
		}
	}
	if !found {
		t.Errorf("expected feat/shipped to be cleaned up, got %v", deleted)
	}
}

func runGitForCleanup(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %s: %v: %s", strings.Join(args, " "), err, out)
	}
}

// initCleanupRepo makes a throwaway repo with one commit on branchName.
func initCleanupRepo(t *testing.T, branchName string) string {
	t.Helper()
	dir := t.TempDir()
	runGitForCleanup(t, dir, "init", "-b", branchName)
	runGitForCleanup(t, dir, "config", "user.email", "test@test")
	runGitForCleanup(t, dir, "config", "user.name", "test")
	if err := os.WriteFile(filepath.Join(dir, "README"), []byte("hi"), 0o644); err != nil {
		t.Fatal(err)
	}
	runGitForCleanup(t, dir, "add", ".")
	runGitForCleanup(t, dir, "commit", "-m", "initial")
	return dir
}

// mustService opens a Service or fails the test.
func mustService(t *testing.T, repoRoot string) *Service {
	t.Helper()
	svc, err := NewService(repoRoot)
	if err != nil {
		t.Fatalf("NewService(%s): %v", repoRoot, err)
	}
	return svc
}
