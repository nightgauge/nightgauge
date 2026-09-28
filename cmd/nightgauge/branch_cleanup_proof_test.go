package main

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	gitpkg "github.com/nightgauge/nightgauge/internal/git"
	"github.com/nightgauge/nightgauge/internal/gittest"
)

// #2259 — branch-cleanup deletes on merged-content proof, never on issue state.

func commitFile(t *testing.T, root, name, body string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(root, name), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	gitIn(t, root, "add", ".")
	gitIn(t, root, "commit", "-m", "change "+name)
}

func proofInputs() gitpkg.MergedProofInputs {
	return gitpkg.MergedProofInputs{Base: "origin/main"}
}

func runOne(t *testing.T, svc *gitpkg.Service, root, branch string, in gitpkg.MergedProofInputs) branchCleanupResult {
	t.Helper()
	gitIn(t, root, "fetch", "origin")
	res := runBranchCleanup(svc, []string{branch}, "main", "main", in, false)
	if len(res) != 1 {
		t.Fatalf("results = %+v, want one", res)
	}
	return res[0]
}

func remoteHas(t *testing.T, root, branch string) bool {
	return strings.TrimSpace(gitIn(t, root, "ls-remote", "--heads", "origin", branch)) != ""
}

func localHas(t *testing.T, root, branch string) bool {
	return strings.TrimSpace(gitIn(t, root, "branch", "--list", branch)) != ""
}

func TestRunBranchCleanup_UnmergedBranchOfClosedIssueIsKept(t *testing.T) {
	svc, root := branchCleanupFixture(t)
	branch := "fix/801-unmerged"
	gitIn(t, root, "checkout", "-b", branch)
	commitFile(t, root, "work.txt", "unmerged\n")
	gitIn(t, root, "push", "-u", "origin", branch)
	gitIn(t, root, "checkout", "main")

	// Issue #801 being CLOSED is irrelevant: nothing here consults it.
	got := runOne(t, svc, root, branch, proofInputs())
	if got.Action != "kept" || got.Verdict != string(gitpkg.VerdictKeep) {
		t.Fatalf("got %+v, want kept/KEEP", got)
	}
	if got.Reason == "" {
		t.Error("a kept branch must carry a reason")
	}
	if !localHas(t, root, branch) || !remoteHas(t, root, branch) {
		t.Error("unmerged branch must survive locally and on origin")
	}
}

func TestRunBranchCleanup_MergedBranchIsDeleted(t *testing.T) {
	svc, root := branchCleanupFixture(t)
	branch := "feat/802-merged"
	gitIn(t, root, "checkout", "-b", branch)
	commitFile(t, root, "feature.txt", "done\n")
	gitIn(t, root, "push", "-u", "origin", branch)
	gitIn(t, root, "checkout", "main")
	gitIn(t, root, "merge", "--ff-only", branch)
	gitIn(t, root, "push", "origin", "main")

	got := runOne(t, svc, root, branch, proofInputs())
	if got.Action != "deleted" {
		t.Fatalf("got %+v, want deleted", got)
	}
	if localHas(t, root, branch) || remoteHas(t, root, branch) {
		t.Error("merged branch must be gone locally and on origin")
	}
}

func TestRunBranchCleanup_SquashMergedBranchIsDeleted(t *testing.T) {
	svc, root := branchCleanupFixture(t)
	branch := "fix/803-squashed"
	gitIn(t, root, "checkout", "-b", branch)
	commitFile(t, root, "a.txt", "a\n")
	commitFile(t, root, "b.txt", "b\n")
	gitIn(t, root, "push", "-u", "origin", branch)
	gitIn(t, root, "checkout", "main")
	gitIn(t, root, "merge", "--squash", branch)
	gitIn(t, root, "commit", "-m", "squash")
	gitIn(t, root, "push", "origin", "main")

	got := runOne(t, svc, root, branch, proofInputs())
	if got.Action != "deleted" {
		t.Fatalf("got %+v, want deleted", got)
	}
}

func TestRunBranchCleanup_OriginOnlyMergedBranchIsDeleted(t *testing.T) {
	svc, root := branchCleanupFixture(t)
	branch := "chore/804-origin-only"
	gitIn(t, root, "checkout", "-b", branch)
	commitFile(t, root, "chore.txt", "c\n")
	gitIn(t, root, "push", "-u", "origin", branch)
	gitIn(t, root, "checkout", "main")
	gitIn(t, root, "merge", "--ff-only", branch)
	gitIn(t, root, "push", "origin", "main")
	gitIn(t, root, "branch", "-D", branch)

	got := runOne(t, svc, root, branch, proofInputs())
	if got.Action != "deleted" || !got.RemoteOnly {
		t.Fatalf("got %+v, want deleted remote-only", got)
	}
	if remoteHas(t, root, branch) {
		t.Error("origin-only merged branch must be deleted on origin")
	}
}

func TestRunBranchCleanup_OriginOnlyUnmergedBranchIsKept(t *testing.T) {
	svc, root := branchCleanupFixture(t)
	branch := "fix/805-origin-only"
	gitIn(t, root, "checkout", "-b", branch)
	commitFile(t, root, "x.txt", "x\n")
	gitIn(t, root, "push", "-u", "origin", branch)
	gitIn(t, root, "checkout", "main")
	gitIn(t, root, "branch", "-D", branch)

	got := runOne(t, svc, root, branch, proofInputs())
	if got.Action != "kept" || !remoteHas(t, root, branch) {
		t.Fatalf("got %+v, want kept with origin intact", got)
	}
}

func TestRunBranchCleanup_MergedPRDoorAndDifferentTip(t *testing.T) {
	svc, root := branchCleanupFixture(t)
	branch := "fix/806-pr"
	gitIn(t, root, "checkout", "-b", branch)
	commitFile(t, root, "README", "branch edit\n")
	gitIn(t, root, "push", "-u", "origin", branch)
	tip := strings.TrimSpace(gitIn(t, root, "rev-parse", "HEAD"))
	gitIn(t, root, "checkout", "main")
	commitFile(t, root, "README", "main moved on\n")
	gitIn(t, root, "push", "origin", "main")

	in := proofInputs()
	in.MergedPR = func(string) (string, []string, bool) { return "deadbeef", nil, true }
	if got := runOne(t, svc, root, branch, in); got.Action != "kept" {
		t.Fatalf("different merged tip: got %+v, want kept", got)
	}

	in.MergedPR = func(string) (string, []string, bool) { return tip, nil, true }
	if got := runOne(t, svc, root, branch, in); got.Action != "deleted" {
		t.Fatalf("merged PR at this tip: got %+v, want deleted", got)
	}
}

func TestRunBranchCleanup_OpenPRAndDryRunKeep(t *testing.T) {
	svc, root := branchCleanupFixture(t)
	branch := "feat/807-open-pr"
	gitIn(t, root, "branch", branch, "main") // ancestor of base: content-merged
	gitIn(t, root, "push", "origin", branch)

	in := proofInputs()
	in.OpenPRHeads = map[string]int{branch: 9}
	if got := runOne(t, svc, root, branch, in); got.Action != "kept" ||
		!strings.Contains(got.Reason, "open PR #9") {
		t.Fatalf("got %+v, want kept for open PR", got)
	}

	res := runBranchCleanup(svc, []string{branch}, "main", "main", proofInputs(), true)
	if len(res) != 1 || res[0].Action != "would_delete" || !localHas(t, root, branch) {
		t.Fatalf("dry run: got %+v, want would_delete with branch intact", res)
	}
}

func TestRunBranchCleanup_EpicBranchOfOpenIssueIsKept(t *testing.T) {
	svc, root := branchCleanupFixture(t)
	branch := "epic/808-big"
	gitIn(t, root, "branch", branch, "main")

	in := proofInputs()
	in.EpicIssueState = func(int) (string, error) { return "OPEN", nil }
	if got := runOne(t, svc, root, branch, in); got.Action != "kept" {
		t.Fatalf("got %+v, want kept", got)
	}
	in.EpicIssueState = nil
	if got := runOne(t, svc, root, branch, in); got.Verdict != string(gitpkg.VerdictUnknown) {
		t.Fatalf("got %+v, want UNKNOWN without a forge", got)
	}
}

// currentTips pins a direct cleanupMergedBranch call to the refs as they are
// now, standing in for a proof judged immediately beforehand.
func currentTips(t *testing.T, svc *gitpkg.Service, branch string) gitpkg.MergedProof {
	t.Helper()
	dir := svc.RepoPath()
	local, _ := gittest.Command(dir, "rev-parse", "--verify", "--quiet", "refs/heads/"+branch).Output()
	remote, _ := gittest.Command(dir, "ls-remote", "origin", "refs/heads/"+branch).Output()
	origin := ""
	if f := strings.Fields(string(remote)); len(f) >= 1 {
		origin = f[0]
	}
	return gitpkg.MergedProof{LocalTip: strings.TrimSpace(string(local)), OriginTip: origin}
}

func TestRunBranchCleanup_OriginMovedAfterJudgmentIsKept(t *testing.T) {
	svc, root := branchCleanupFixture(t)
	branch := "feat/809-moved"
	gitIn(t, root, "checkout", "-b", branch)
	commitFile(t, root, "f.txt", "f\n")
	gitIn(t, root, "push", "-u", "origin", branch)
	gitIn(t, root, "checkout", "main")
	gitIn(t, root, "merge", "--ff-only", branch)
	gitIn(t, root, "push", "origin", "main")
	gitIn(t, root, "fetch", "origin")

	proof := svc.ProveBranchMerged(branch, proofInputs())
	if proof.Verdict != gitpkg.VerdictSafeDelete {
		t.Fatalf("proof = %+v, want SAFE-DELETE", proof)
	}

	// Someone pushes new work to origin between judgment and delete.
	other := filepath.Join(t.TempDir(), "other")
	origin := strings.TrimSpace(gitIn(t, root, "remote", "get-url", "origin"))
	gitIn(t, filepath.Dir(other), "clone", "-b", branch, origin, other)
	gitIn(t, other, "config", "user.email", "t@t")
	gitIn(t, other, "config", "user.name", "t")
	commitFile(t, other, "late.txt", "late\n")
	gitIn(t, other, "push", "origin", branch)

	action, reason, err := cleanupMergedBranch(svc, branch, proof)
	if err != nil || action != "kept" || !strings.Contains(reason, "moved") {
		t.Fatalf("action=%q reason=%q err=%v, want kept/moved", action, reason, err)
	}
	if !remoteHas(t, root, branch) || !localHas(t, root, branch) {
		t.Error("a branch that moved after judgment must survive on both sides")
	}
}

func TestBranchCleanupPinned_LeaseRefusesMovedOrigin(t *testing.T) {
	// Pin to a SHA origin no longer holds: nothing may be deleted.
	svc, root := branchCleanupFixture(t)
	branch := "fix/810-lease"
	gitIn(t, root, "push", "origin", "main:refs/heads/"+branch)
	stale := strings.TrimSpace(gitIn(t, root, "rev-parse", "main"))
	commitFile(t, root, "g.txt", "g\n")
	gitIn(t, root, "push", "origin", "HEAD:refs/heads/"+branch)

	err := svc.BranchCleanupPinned(branch, "", stale)
	var moved *gitpkg.BranchMovedError
	if !errors.As(err, &moved) {
		t.Fatalf("err = %v, want *BranchMovedError", err)
	}
	if !remoteHas(t, root, branch) {
		t.Error("origin branch must survive a moved pin")
	}
}
