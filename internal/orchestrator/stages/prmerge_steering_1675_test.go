package stages

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/nightgauge/nightgauge/internal/execution/codexprovision"
)

// leakedPR builds a bare remote plus a clone on fix/1179-x whose pushed head
// commits AGENTS.md with the managed steering block — the PR head a Codex
// stage's agent leaves behind when it commits and pushes with the block
// present.
func leakedPR(t *testing.T) (remote, clone string) {
	t.Helper()
	remote = t.TempDir()
	gitOut(t, remote, "init", "-q", "--bare", "-b", "main")
	clone = initRepo(t)
	gitOut(t, clone, "remote", "add", "origin", remote)
	gitOut(t, clone, "push", "-q", "origin", "main")
	if err := os.WriteFile(filepath.Join(clone, "AGENTS.md"), []byte("# Rules\n\n"+leakBlock), 0o644); err != nil {
		t.Fatal(err)
	}
	gitOut(t, clone, "add", "-A")
	gitOut(t, clone, "commit", "-qm", "feat: agent commit with steering present")
	gitOut(t, clone, "push", "-qu", "origin", "fix/1179-x")
	return remote, clone
}

func cleanOpenPR() *fakeGh {
	snap := PRViewSnapshot{State: "OPEN", Mergeable: "MERGEABLE", MergeStateStatus: "CLEAN", HeadRefName: "fix/1179-x"}
	return &fakeGh{preMerge: snap, postMerge: PRViewSnapshot{State: "MERGED", HeadRefName: "fix/1179-x"}}
}

func remoteAgents(t *testing.T, remote string) string {
	t.Helper()
	return gitOut(t, remote, "show", "fix/1179-x:AGENTS.md")
}

// With a worktree on the head branch the gate repairs, pushes, and refuses:
// the head changed, so required checks must re-run before any merge.
func TestPRMerge_SteeringGate_RepairsPushesAndRefuses_1675(t *testing.T) {
	remote, clone := leakedPR(t)
	gh := cleanOpenPR()
	r := newRunnerWith(gh, 5)
	r.knowledgeConformance = nil

	res, err := r.Run(context.Background(), 1179, "owner/repo", clone)
	if err != nil {
		t.Fatal(err)
	}
	if gh.mergeCalls != 0 {
		t.Fatalf("merged a PR whose head carries generated steering")
	}
	if res.Path != PathRefused || !strings.HasPrefix(res.Reason, ReasonGeneratedSteering+": ") ||
		!strings.Contains(res.Reason, "was pushed") || !strings.Contains(res.Reason, "checks must re-run") {
		t.Fatalf("result = %+v, want a refusal saying the repair was pushed", res)
	}
	if got := remoteAgents(t, remote); strings.Contains(got, "MANAGED STEERING") || !strings.Contains(got, "# Rules") {
		t.Fatalf("remote head still carries the block or lost user content:\n%s", got)
	}

	// The retry: the head is clean now, so the gate passes and the merge runs.
	gh2 := cleanOpenPR()
	r2 := newRunnerWith(gh2, 5)
	r2.knowledgeConformance = nil
	if res, _ := r2.Run(context.Background(), 1179, "owner/repo", clone); res.Path != PathMerged || gh2.mergeCalls != 1 {
		t.Fatalf("retry after the repair = %+v (merge calls %d), want merged", res, gh2.mergeCalls)
	}
}

// A local HEAD repaired earlier (for example by the post-stage repair, which
// does not always push) while the remote head is still leaked: the gate
// publishes the existing repair rather than giving up.
func TestPRMerge_SteeringGate_PublishesEarlierLocalRepair_1675(t *testing.T) {
	remote, clone := leakedPR(t)
	if res, err := codexprovision.RepairCommittedSteering(context.Background(), clone, false); err != nil || !res.Repaired {
		t.Fatalf("local repair: %+v, %v", res, err)
	}
	gh := cleanOpenPR()
	r := newRunnerWith(gh, 5)
	r.knowledgeConformance = nil

	res, _ := r.Run(context.Background(), 1179, "owner/repo", clone)
	if gh.mergeCalls != 0 || res.Path != PathRefused || !strings.Contains(res.Reason, "was pushed") {
		t.Fatalf("result = %+v (merge calls %d), want refused with the repair pushed", res, gh.mergeCalls)
	}
	if strings.Contains(remoteAgents(t, remote), "MANAGED STEERING") {
		t.Fatal("the earlier local repair was not published")
	}
}

// Without a worktree on the head branch nothing can be repaired here: refuse,
// name the command, and leave the remote alone.
func TestPRMerge_SteeringGate_NoWorktreeRefusesWithCommand_1675(t *testing.T) {
	remote, clone := leakedPR(t)
	gitOut(t, clone, "checkout", "-q", "main")
	gh := cleanOpenPR()
	r := newRunnerWith(gh, 5)
	r.knowledgeConformance = nil

	res, _ := r.Run(context.Background(), 1179, "owner/repo", clone)
	if gh.mergeCalls != 0 || res.Path != PathRefused {
		t.Fatalf("result = %+v (merge calls %d), want refused", res, gh.mergeCalls)
	}
	if !strings.Contains(res.Reason, "nightgauge preflight managed-steering --fix") ||
		!strings.Contains(res.Reason, "no local worktree is on fix/1179-x") {
		t.Errorf("reason = %q, want it to name the fix command and the missing worktree", res.Reason)
	}
	if !strings.Contains(remoteAgents(t, remote), "MANAGED STEERING") {
		t.Error("with no worktree the gate must not touch the remote")
	}
}

// The gate runs before any verdict that could punt, so a head that would punt
// (here: failed CI) is still refused rather than handed to the LLM skill.
func TestPRMerge_SteeringGate_RunsBeforePunts_1675(t *testing.T) {
	_, clone := leakedPR(t)
	gitOut(t, clone, "checkout", "-q", "main")
	gh := cleanOpenPR()
	gh.preMerge.StatusCheckRollup = []PRStatusCheckRow{{Name: "build", Conclusion: "FAILURE"}}
	r := newRunnerWith(gh, 5)

	if res, _ := r.Run(context.Background(), 1179, "owner/repo", clone); res.Path != PathRefused {
		t.Fatalf("result = %+v, want refused ahead of the failed-checks punt", res)
	}
}
