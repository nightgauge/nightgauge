package execution

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/nightgauge/nightgauge/internal/gittest"
)

// shippedClone builds an upstream and a clone whose feature branch was pushed
// and squash-merged on the upstream.
func shippedClone(t *testing.T, branch string) (upstream, clone string) {
	t.Helper()
	upstream = initTestGitRepo(t, "main")
	gittest.Run(t, upstream, "config", "receive.denyCurrentBranch", "ignore")
	clone = filepath.Join(t.TempDir(), "clone")
	gittest.Run(t, filepath.Dir(clone), "clone", upstream, clone)
	gittest.Run(t, clone, "config", "user.email", "test@test")
	gittest.Run(t, clone, "config", "user.name", "test")
	gittest.Run(t, clone, "checkout", "-b", branch)
	if err := os.WriteFile(filepath.Join(clone, "feature.txt"), []byte("shipped"), 0o644); err != nil {
		t.Fatal(err)
	}
	gittest.Run(t, clone, "add", ".")
	gittest.Run(t, clone, "commit", "-m", "the feature")
	gittest.Run(t, clone, "push", "-u", "origin", branch)
	gittest.Run(t, clone, "checkout", "main")
	gittest.Run(t, upstream, "merge", "--squash", branch)
	gittest.Run(t, upstream, "commit", "-m", "squash: the feature")
	return upstream, clone
}

func upstreamHas(t *testing.T, upstream, branch string) bool {
	t.Helper()
	out, _ := gittest.Command(upstream, "branch", "--list", branch).CombinedOutput()
	return strings.TrimSpace(string(out)) != ""
}

// #1901: both copies already gone (merge --delete-branch removed origin's, a
// reclaim removed the local ref). Nothing to judge, nothing deleted, no error.
func TestCleanupBranchAndRemoteIfMerged_BothGone(t *testing.T) {
	const branch = "feat/1901-gone"
	upstream, clone := shippedClone(t, branch)
	gittest.Run(t, upstream, "branch", "-D", branch)
	gittest.Run(t, clone, "branch", "-D", branch)

	res, err := (&Manager{workspaceRoot: clone}).CleanupBranchAndRemoteIfMerged("acme/target", branch)
	if err != nil {
		t.Fatalf("err = %v, want nil for an already-gone branch", err)
	}
	if !res.AlreadyGone() || len(res.Survivors()) != 0 {
		t.Errorf("res = %+v, want AlreadyGone with no survivors", res)
	}
}

// #1901: only origin's copy remains. It is judged by origin's ref and deleted.
func TestCleanupBranchAndRemoteIfMerged_OriginOnly(t *testing.T) {
	const branch = "feat/1901-origin-only"
	upstream, clone := shippedClone(t, branch)
	gittest.Run(t, clone, "branch", "-D", branch)

	res, err := (&Manager{workspaceRoot: clone}).CleanupBranchAndRemoteIfMerged("acme/target", branch)
	if err != nil {
		t.Fatalf("CleanupBranchAndRemoteIfMerged: %v", err)
	}
	if res.LocalBefore || !res.RemoteBefore || res.Declined || !res.RemoteDeleted {
		t.Errorf("res = %+v, want origin copy judged merged and deleted", res)
	}
	if upstreamHas(t, upstream, branch) {
		t.Error("origin still carries the merged branch")
	}
}

// Both copies present and merged: both go.
func TestCleanupBranchAndRemoteIfMerged_Both(t *testing.T) {
	const branch = "feat/1901-both"
	upstream, clone := shippedClone(t, branch)

	res, err := (&Manager{workspaceRoot: clone}).CleanupBranchAndRemoteIfMerged("acme/target", branch)
	if err != nil {
		t.Fatalf("CleanupBranchAndRemoteIfMerged: %v", err)
	}
	if !res.LocalDeleted || !res.RemoteDeleted || len(res.Survivors()) != 0 {
		t.Errorf("res = %+v, want both copies deleted", res)
	}
	if upstreamHas(t, upstream, branch) {
		t.Error("origin still carries the merged branch")
	}
}
