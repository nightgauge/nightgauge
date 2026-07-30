package recovery

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	pmstages "github.com/nightgauge/nightgauge/internal/orchestrator/stages"
	"github.com/nightgauge/nightgauge/internal/state"
)

// abandonedCommitFixture is a local (no-remote) git repo — DetectDefaultBranch/
// ResolveBaseRef fall back to the local branch when no origin exists, so a
// single repo with a base branch and a feature branch is enough to exercise
// DetectBranchAhead through this action.
func newAbandonedCommitFixture(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	runGit(t, root, "init", "-b", "main")
	runGit(t, root, "config", "user.email", "test@example.com")
	runGit(t, root, "config", "user.name", "Test")
	writeAbandonedFile(t, root, "README", "hello\n")
	runGit(t, root, "add", ".")
	runGit(t, root, "commit", "-m", "initial")

	resolved, err := filepath.EvalSymlinks(root)
	if err != nil {
		t.Fatalf("resolve root: %v", err)
	}
	return resolved
}

func runGit(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
}

func writeAbandonedFile(t *testing.T, dir, name, content string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o644); err != nil {
		t.Fatalf("write %s: %v", name, err)
	}
}

func TestAbandonedCommitRecoverable_Matches(t *testing.T) {
	a := NewAbandonedCommitRecoverable(&fakePRCreateRunner{})
	cases := []struct {
		name string
		f    StageFailure
		want bool
	}{
		{"pr-create stage excluded", StageFailure{Stage: state.StagePRCreate, Workspace: "/tmp/x"}, false},
		{"pr-merge stage excluded", StageFailure{Stage: state.StagePRMerge, Workspace: "/tmp/x"}, false},
		{"pr already exists", StageFailure{Stage: state.StageFeatureDev, PRNumber: 5, Workspace: "/tmp/x"}, false},
		{"no workspace", StageFailure{Stage: state.StageFeatureDev, Workspace: ""}, false},
		{"matches feature-dev with live workspace", StageFailure{Stage: state.StageFeatureDev, Workspace: "/tmp/x"}, true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := a.Matches(c.f); got != c.want {
				t.Errorf("Matches() = %v, want %v", got, c.want)
			}
		})
	}
}

func TestAbandonedCommitRecoverable_Execute_RunnerCreated(t *testing.T) {
	root := newAbandonedCommitFixture(t)
	runGit(t, root, "checkout", "-b", "feat/191-work")
	writeAbandonedFile(t, root, "feature.txt", "new work\n")
	runGit(t, root, "add", ".")
	runGit(t, root, "commit", "-m", "work: feature")

	runner := &fakePRCreateRunner{res: pmstages.PRCreateResult{Path: pmstages.CreatePathCreated, PRNumber: 42, Reason: pmstages.ReasonRichContext}}
	a := NewAbandonedCommitRecoverable(runner)
	failure := StageFailure{Stage: state.StageFeatureDev, Workspace: root, IssueNumber: 191}

	res := a.Execute(context.Background(), failure)
	if !res.Recovered {
		t.Fatalf("expected Recovered=true, got reason=%q", res.Reason)
	}
	if res.FollowUp != FollowUpStageCanResume {
		t.Errorf("FollowUp = %q", res.FollowUp)
	}
}

func TestAbandonedCommitRecoverable_Execute_RunnerPunts_ResumesAtPRCreate(t *testing.T) {
	root := newAbandonedCommitFixture(t)
	runGit(t, root, "checkout", "-b", "feat/191-work")
	writeAbandonedFile(t, root, "feature.txt", "new work\n")
	runGit(t, root, "add", ".")
	runGit(t, root, "commit", "-m", "work: feature")

	runner := &fakePRCreateRunner{res: pmstages.PRCreateResult{Path: pmstages.CreatePathPunt, Reason: pmstages.ReasonMissingDevContext}}
	a := NewAbandonedCommitRecoverable(runner)
	failure := StageFailure{Stage: state.StageFeatureDev, Workspace: root, IssueNumber: 191}

	res := a.Execute(context.Background(), failure)
	if res.Recovered {
		t.Fatal("expected Recovered=false on punt")
	}
	if res.FollowUp != FollowUpStageCanResume {
		t.Errorf("FollowUp = %q, want %q", res.FollowUp, FollowUpStageCanResume)
	}
	if res.BacktrackTargetStage != string(state.StagePRCreate) {
		t.Errorf("BacktrackTargetStage = %q, want %q", res.BacktrackTargetStage, state.StagePRCreate)
	}
}

func TestAbandonedCommitRecoverable_Execute_RunnerError_ResumesAtPRCreate(t *testing.T) {
	root := newAbandonedCommitFixture(t)
	runGit(t, root, "checkout", "-b", "feat/191-work")
	writeAbandonedFile(t, root, "feature.txt", "new work\n")
	runGit(t, root, "add", ".")
	runGit(t, root, "commit", "-m", "work: feature")

	runner := &fakePRCreateRunner{err: errors.New("gh api error")}
	a := NewAbandonedCommitRecoverable(runner)
	failure := StageFailure{Stage: state.StageFeatureDev, Workspace: root, IssueNumber: 191}

	res := a.Execute(context.Background(), failure)
	if res.Recovered {
		t.Fatal("expected Recovered=false on runner error")
	}
	if res.BacktrackTargetStage != string(state.StagePRCreate) {
		t.Errorf("BacktrackTargetStage = %q, want %q", res.BacktrackTargetStage, state.StagePRCreate)
	}
}

func TestAbandonedCommitRecoverable_Execute_NotAheadOfBase_NoMatch(t *testing.T) {
	root := newAbandonedCommitFixture(t)
	runGit(t, root, "checkout", "-b", "feat/191-fresh")
	// No commits of its own on the feature branch.

	a := NewAbandonedCommitRecoverable(&fakePRCreateRunner{})
	failure := StageFailure{Stage: state.StageFeatureDev, Workspace: root, IssueNumber: 191}

	res := a.Execute(context.Background(), failure)
	if res.Recovered || res.FollowUp == FollowUpStageCanResume {
		t.Errorf("expected zero-value non-match result, got %+v", res)
	}
}

func TestAbandonedCommitRecoverable_Execute_DirtyTree_NoMatch(t *testing.T) {
	root := newAbandonedCommitFixture(t)
	runGit(t, root, "checkout", "-b", "feat/191-dirty")
	writeAbandonedFile(t, root, "feature.txt", "new work\n")
	runGit(t, root, "add", ".")
	runGit(t, root, "commit", "-m", "work: feature")
	writeAbandonedFile(t, root, "scratch.txt", "uncommitted\n")

	a := NewAbandonedCommitRecoverable(&fakePRCreateRunner{})
	failure := StageFailure{Stage: state.StageFeatureDev, Workspace: root, IssueNumber: 191}

	res := a.Execute(context.Background(), failure)
	if res.Recovered || res.FollowUp == FollowUpStageCanResume {
		t.Errorf("expected zero-value non-match result for dirty tree, got %+v", res)
	}
}
