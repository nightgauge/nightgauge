package git

import "testing"

// An SSH checkout with GITHUB_TOKEN set is the topology #2081 was observed on:
// go-git rejected the token's auth as "invalid auth method" before connecting.
func TestFetch_FetchesThroughAnSSHRemoteWithATokenSet(t *testing.T) {
	r := setupLiveRunRepo(t)
	sshShapedOrigin(t, r)
	gitExecTest(t, r.origin, "branch", "feat/2081-new", "main")

	if err := r.service(t, r.primary).Fetch(false); err != nil {
		t.Fatalf("Fetch(false): %v", err)
	}
	gitExecTest(t, r.primary, "rev-parse", "--verify", "refs/remotes/origin/feat/2081-new")
}

func TestFetch_PruneDropsATrackingRefOriginNoLongerCarries(t *testing.T) {
	r := setupLiveRunRepo(t)
	sshShapedOrigin(t, r)
	gitExecTest(t, r.origin, "branch", "feat/2081-gone", "main")
	svc := r.service(t, r.primary)
	if err := svc.Fetch(true); err != nil {
		t.Fatalf("Fetch(true): %v", err)
	}
	gitExecTest(t, r.origin, "branch", "-D", "feat/2081-gone")

	if err := svc.Fetch(true); err != nil {
		t.Fatalf("Fetch(true) after delete: %v", err)
	}
	if out, err := r.service(t, r.primary).gitExec("rev-parse", "--verify", "--quiet", "refs/remotes/origin/feat/2081-gone"); err == nil {
		t.Errorf("origin/feat/2081-gone survived a pruning fetch: %s", out)
	}
}
