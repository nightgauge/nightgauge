package git

import "testing"

// A re-run's pickup runs in the issue's worktree, already on the issue
// branch. The branch must not become its own base: the default branch is.
func TestEnsureIssueBranchNeverUsesItselfAsBase(t *testing.T) {
	r := setupLiveRunRepo(t)
	gitExecTest(t, r.primary, "checkout", "-q", "feat/123-stale")
	svc := r.service(t, r.primary)

	res, err := svc.EnsureIssueBranch("feat/123-stale", 0, nil)
	if err != nil {
		t.Fatalf("EnsureIssueBranch: %v", err)
	}
	if res.BaseBranch == res.Branch {
		t.Fatalf("BaseBranch = %q, the issue branch itself", res.BaseBranch)
	}
	if res.BaseBranch != "main" {
		t.Errorf("BaseBranch = %q, want the default branch %q", res.BaseBranch, "main")
	}
}
