package recovery

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	gitpkg "github.com/nightgauge/nightgauge/internal/git"
	"github.com/nightgauge/nightgauge/internal/gittest"
	"github.com/nightgauge/nightgauge/internal/layout/layouttest"
	"github.com/nightgauge/nightgauge/internal/orchestrator/gates"
	pmstages "github.com/nightgauge/nightgauge/internal/orchestrator/stages"
	"github.com/nightgauge/nightgauge/internal/state"
)

const queueConflictReason = "merge-queue-failed: merge conflict with main: PR #7 is CONFLICTING (mergeStateStatus DIRTY)"

func queueFailure(ws string) StageFailure {
	return StageFailure{
		Stage: state.StagePRMerge, GateKind: gates.KindNoOp, Reason: queueConflictReason,
		StageError: queueConflictReason, PRNumber: 7, IssueNumber: 2841, Workspace: ws,
	}
}

func stubMerge(t *testing.T, res *gitpkg.BaseMergeResult, err error) *[]string {
	t.Helper()
	calls := &[]string{}
	prev := mergeBaseIntoBranch
	mergeBaseIntoBranch = func(_ context.Context, _, branch, base string) (*gitpkg.BaseMergeResult, error) {
		*calls = append(*calls, branch+"<-"+base)
		return res, err
	}
	t.Cleanup(func() { mergeBaseIntoBranch = prev })
	return calls
}

func stubGreenBranch(t *testing.T) {
	t.Helper()
	stubExecGit(t, func(_ context.Context, _ string, args ...string) ([]byte, error) {
		if args[0] == "rev-parse" && len(args) > 1 && args[1] == "--abbrev-ref" {
			return []byte("feat/x\n"), nil
		}
		if args[0] == "rev-parse" {
			return nil, errors.New("no such path")
		}
		return nil, nil
	})
	stubExecGh(t, func(context.Context, ...string) ([]byte, error) {
		return []byte(`{"statusCheckRollup":[{"conclusion":"SUCCESS"}]}`), nil
	})
}

func TestChangelogUnionMerge_Outcomes(t *testing.T) {
	merged := pmstages.PRMergeResult{Path: pmstages.PathMerged, PRNumber: 7}
	cases := []struct {
		name       string
		merge      *gitpkg.BaseMergeResult
		mergeErr   error
		run        pmstages.PRMergeResult
		wantHandle bool
		wantRecov  bool
		wantFile   string
	}{
		{name: "changelog union merges", merge: &gitpkg.BaseMergeResult{Outcome: gitpkg.BaseMergeChangelogResolved, ConflictFiles: []string{"CHANGELOG.md"}}, run: merged, wantHandle: true, wantRecov: true},
		{name: "clean merge lands", merge: &gitpkg.BaseMergeResult{Outcome: gitpkg.BaseMergeMerged}, run: merged, wantHandle: true, wantRecov: true},
		{name: "stale read re-runs", merge: &gitpkg.BaseMergeResult{Outcome: gitpkg.BaseMergeUpToDate}, run: merged, wantHandle: true, wantRecov: true},
		{name: "runner still refuses", merge: &gitpkg.BaseMergeResult{Outcome: gitpkg.BaseMergeMerged}, run: pmstages.PRMergeResult{Path: pmstages.PathRefused, Reason: "no"}, wantHandle: true},
		{name: "real conflict falls through", merge: &gitpkg.BaseMergeResult{Outcome: gitpkg.BaseMergeConflict, ConflictFiles: []string{"a.go"}}, wantFile: "a.go"},
		{name: "merge error falls through", mergeErr: errors.New("boom")},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			stubGreenBranch(t)
			stubMerge(t, tc.merge, tc.mergeErr)
			step := NewChangelogUnionMerge(&fakePRMergeRunner{res: tc.run})
			res, handled, ev := step.Attempt(context.Background(), queueFailure(t.TempDir()), "act")
			if handled != tc.wantHandle || res.Recovered != tc.wantRecov {
				t.Fatalf("handled=%v recovered=%v (%+v)", handled, res.Recovered, res)
			}
			if tc.wantFile != "" && !strings.Contains(strings.Join(ev, " "), "conflicting_file="+tc.wantFile) {
				t.Errorf("evidence %v must name %s", ev, tc.wantFile)
			}
		})
	}
}

func TestConflictLoop_ChangelogUnionRunsBeforeRebaseFallback(t *testing.T) {
	stubGreenBranch(t)
	calls := stubMerge(t, &gitpkg.BaseMergeResult{Outcome: gitpkg.BaseMergeChangelogResolved}, nil)
	fallback := &recordingFallback{}
	loop := NewConflictRecoveryLoop(2).
		WithChangelogUnion(NewChangelogUnionMerge(&fakePRMergeRunner{res: pmstages.PRMergeResult{Path: pmstages.PathMerged}})).
		WithRebaseFallback(fallback)

	res := loop.Execute(context.Background(), queueFailure(layouttest.Repo(t)))
	if !res.Recovered || fallback.calls != 0 || len(*calls) != 1 || (*calls)[0] != "feat/x<-main" {
		t.Fatalf("res=%+v fallback=%d calls=%v", res, fallback.calls, *calls)
	}
}

func TestConflictLoop_RealConflictContinuesToRebaseFallback(t *testing.T) {
	stubGreenBranch(t)
	stubMerge(t, &gitpkg.BaseMergeResult{Outcome: gitpkg.BaseMergeConflict, ConflictFiles: []string{"a.go"}}, nil)
	fallback := &recordingFallback{}
	loop := NewConflictRecoveryLoop(2).
		WithChangelogUnion(NewChangelogUnionMerge(&fakePRMergeRunner{})).
		WithRebaseFallback(fallback)

	res := loop.Execute(context.Background(), queueFailure(layouttest.Repo(t)))
	if fallback.calls != 1 || !strings.Contains(strings.Join(res.Evidence, " "), "conflicting_file=a.go") {
		t.Fatalf("fallback=%d evidence=%v", fallback.calls, res.Evidence)
	}
}

type recordingFallback struct{ calls int }

func (r *recordingFallback) Name() string              { return "fallback" }
func (r *recordingFallback) Description() string       { return "" }
func (r *recordingFallback) Matches(StageFailure) bool { return true }
func (r *recordingFallback) Execute(context.Context, StageFailure) RecoveryResult {
	r.calls++
	return RecoveryResult{Action: "fallback", FollowUp: FollowUpHumanTriageRequired}
}

// TestMergeBaseSeam_RealGit runs the production seam against real git: a
// changelog-only collision reaches origin as a union, with no force.
func TestMergeBaseSeam_RealGit(t *testing.T) {
	root := t.TempDir()
	run := func(dir string, args ...string) string {
		t.Helper()
		cmd := gittest.Command(dir, args...)
		cmd.Env = append(cmd.Env, "GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@t", "GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@t")
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
		return string(out)
	}
	origin := filepath.Join(root, "origin.git")
	run(root, "init", "--bare", "-b", "main", origin)
	work := filepath.Join(root, "work")
	run(root, "clone", origin, work)
	write := func(body string) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(work, "CHANGELOG.md"), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	head := "# Changelog\n\n## [Unreleased]\n\n"
	write(head + "- base\n")
	run(work, "checkout", "-b", "main")
	run(work, "add", ".")
	run(work, "commit", "-m", "init")
	run(work, "push", "origin", "main")
	run(work, "checkout", "-b", "feat/x")
	write(head + "- base\n- ours\n")
	run(work, "commit", "-am", "ours")
	run(work, "push", "origin", "feat/x")
	run(work, "checkout", "main")
	write(head + "- base\n- theirs\n")
	run(work, "commit", "-am", "theirs")
	run(work, "push", "origin", "main")
	run(work, "checkout", "feat/x")

	res, err := mergeBaseIntoBranch(context.Background(), work, "feat/x", "main")
	if err != nil {
		t.Fatal(err)
	}
	if res.Outcome != gitpkg.BaseMergeChangelogResolved {
		t.Fatalf("outcome = %s", res.Outcome)
	}
	got := run(work, "show", "origin/feat/x:CHANGELOG.md")
	if !strings.Contains(got, "- ours") || !strings.Contains(got, "- theirs") {
		t.Errorf("union lost a side:\n%s", got)
	}
}

// Default is what both the scheduler and `pr-stage recover` run, so the union
// step only counts if Default wires it in (#2841).
func TestDefaultRegistry_WiresChangelogUnion(t *testing.T) {
	reg := Default("", &fakePRMergeRunner{}, nil)
	for _, a := range reg.Actions() {
		if loop, ok := a.(*ConflictRecoveryLoop); ok {
			if loop.changelogUnion == nil {
				t.Fatal("Default must wire the changelog union into conflict recovery")
			}
			return
		}
	}
	t.Fatal("conflict-recovery-loop is not registered")
}
