package recovery

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/nightgauge/nightgauge/internal/orchestrator/gates"
	pmstages "github.com/nightgauge/nightgauge/internal/orchestrator/stages"
	"github.com/nightgauge/nightgauge/internal/state"

	"github.com/nightgauge/nightgauge/internal/gittest"
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
	cmd := gittest.Command(dir, args...)
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
		{"matches killed feature-validate (gate never ran)", StageFailure{Stage: state.StageFeatureValidate, GateKind: gates.KindOK, Workspace: "/tmp/x"}, true},
		// #1890: a gate no-op is a stage that ran to completion and produced
		// nothing — not a killed stage with surviving work. Resuming at
		// pr-create would skip the stage whose emptiness was the failure.
		{"feature-validate gate no-op excluded", StageFailure{
			Stage:      state.StageFeatureValidate,
			GateName:   "feature-validate",
			GateKind:   gates.KindNoOp,
			Reason:     "no quality-gate results recorded",
			StageError: "premature turn end: stage exited 0 with no state change (gate no-op): no quality-gate results recorded",
			Workspace:  "/tmp/x",
		}, false},
		{"feature-dev gate no-op excluded", StageFailure{Stage: state.StageFeatureDev, GateKind: gates.KindNoOp, Workspace: "/tmp/x"}, false},
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

// TestDefaultRegistry_FeatureValidateGateNoOp_NotResumedAtPRCreate is the
// #1890 regression: on the #1787 run a feature-validate that exited 0 with no
// quality-gate results was claimed by abandoned-commit-recoverable and resumed
// at pr-create, so the PR merged unvalidated. The workspace here satisfies
// every precondition that action's Execute checks (committed, unmerged work on
// a clean tree, no PR), so the only thing keeping the registry from rewinding
// past validation is the gate-no-op exclusion. No action may claim it: the
// failure must fall through to model escalation, which re-runs the stage.
func TestDefaultRegistry_FeatureValidateGateNoOp_NotResumedAtPRCreate(t *testing.T) {
	root := newAbandonedCommitFixture(t)
	runGit(t, root, "checkout", "-b", "feat/1787-work")
	writeAbandonedFile(t, root, "feature.txt", "new work\n")
	runGit(t, root, "add", ".")
	runGit(t, root, "commit", "-m", "work: feature")

	reg := Default("", nil, &fakePRCreateRunner{res: pmstages.PRCreateResult{Path: pmstages.CreatePathPunt, Reason: pmstages.ReasonMissingDevContext}})
	failure := StageFailure{
		Stage:       state.StageFeatureValidate,
		GateName:    "feature-validate",
		GateKind:    gates.KindNoOp,
		Reason:      "no quality-gate results recorded",
		StageError:  "premature turn end: stage exited 0 with no state change (gate no-op): no quality-gate results recorded",
		Workspace:   root,
		IssueNumber: 1787,
	}
	res, matched := reg.TryRecover(context.Background(), failure, 0)
	if matched {
		t.Fatalf("gate no-op was claimed by %q (backtrack=%q, reason=%q); it must fall through to escalation",
			res.Action, res.BacktrackTargetStage, res.Reason)
	}

	// The killed-stage case (#191) on the same workspace still resumes at
	// pr-create: the gate never ran, so GateKind is KindOK.
	killed := failure
	killed.GateName, killed.GateKind, killed.Reason = "", gates.KindOK, ""
	killed.StageError = "signal: killed"
	res, matched = reg.TryRecover(context.Background(), killed, 0)
	if !matched || res.Action != "abandoned-commit-recoverable" || res.BacktrackTargetStage != string(state.StagePRCreate) {
		t.Fatalf("killed feature-validate: matched=%v action=%q backtrack=%q, want abandoned-commit-recoverable → pr-create",
			matched, res.Action, res.BacktrackTargetStage)
	}
}
