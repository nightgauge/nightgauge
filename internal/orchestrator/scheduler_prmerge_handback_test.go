package orchestrator

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/nightgauge/nightgauge/internal/layout/layouttest"
	pmstages "github.com/nightgauge/nightgauge/internal/orchestrator/stages"
	"github.com/nightgauge/nightgauge/internal/state"
	"github.com/nightgauge/nightgauge/pkg/types"
)

// seqPRMergeRunner returns its results in order, repeating the last.
type seqPRMergeRunner struct {
	results []pmstages.PRMergeResult
	calls   int
}

func (f *seqPRMergeRunner) Run(_ context.Context, _ int, _, _ string) (pmstages.PRMergeResult, error) {
	r := f.results[min(f.calls, len(f.results)-1)]
	f.calls++
	return r, nil
}

// fixPushingSkill stands in for the pr-merge skill: each session writes the
// fix-pushed marker (when markerPath is set) and reports some usage.
type fixPushingSkill struct {
	markerPath string
	calls      int
}

func (f *fixPushingSkill) RunStage(_ context.Context, _ StageRunParams) (*StageRunResult, error) {
	f.calls++
	if f.markerPath != "" {
		_ = os.WriteFile(f.markerPath, []byte(`{"result":"fix-pushed","pr_number":7,"head_sha":"abc","attempt":1}`), 0o644)
	}
	return &StageRunResult{ExitCode: 0, InputTokens: 10, CostUsd: 1}, nil
}

func handbackFixture(t *testing.T, withMarker bool, results ...pmstages.PRMergeResult) (*Scheduler, *fixPushingSkill, *seqPRMergeRunner, *state.RuntimeState, string) {
	t.Helper()
	root := layouttest.Repo(t)
	dir := layouttest.PipelineDir(t, root)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	skill := &fixPushingSkill{}
	if withMarker {
		skill.markerPath = filepath.Join(dir, "pr-merge-fix-42.json")
	}
	det := &seqPRMergeRunner{results: results}
	s := newSchedulerForDeterministicTest()
	s.WithPRMergeRunner(det)
	s.WithStageRunner(skill)
	rs := state.NewRuntimeState("owner/repo", 42, "item-id", testRunID())
	rs.BeginStage(state.StagePRMerge)
	return s, skill, det, rs, root
}

var (
	failedCI = pmstages.PRMergeResult{Path: pmstages.PathPunt, PRNumber: 7, PRState: "OPEN", Reason: pmstages.ReasonFailedChecks + ": lint"}
	merged   = pmstages.PRMergeResult{Path: pmstages.PathMerged, PRNumber: 7, PRState: "MERGED", Reason: pmstages.ReasonCleanMerged}
)

func TestPRMergeHandback_FixThenMerged(t *testing.T) {
	s, skill, det, rs, root := handbackFixture(t, true, merged)
	item := types.BoardItem{Number: 42, Repo: "owner/repo"}

	res, err := s.runPRMergeSkillWithFixHandback(context.Background(), StageRunParams{Stage: state.StagePRMerge}, rs, item, root)
	if err != nil || res.ExitCode != 0 {
		t.Fatalf("got exit=%d err=%v, want success", res.ExitCode, err)
	}
	if skill.calls != 1 || det.calls != 1 {
		t.Fatalf("skill calls=%d runner calls=%d, want 1/1", skill.calls, det.calls)
	}
	if got := rs.StageExecutionPath(state.StagePRMerge); got != "llm" {
		t.Errorf("execution path = %q, want llm", got)
	}
	if _, statErr := os.Stat(skill.markerPath); !os.IsNotExist(statErr) {
		t.Errorf("marker not consumed: %v", statErr)
	}
}

func TestPRMergeHandback_ExhaustsBudget(t *testing.T) {
	s, skill, det, rs, root := handbackFixture(t, true, failedCI)
	s.prAutoFixMaxAttempts = 2
	item := types.BoardItem{Number: 42, Repo: "owner/repo"}

	res, err := s.runPRMergeSkillWithFixHandback(context.Background(), StageRunParams{Stage: state.StagePRMerge}, rs, item, root)
	if err == nil || !strings.Contains(err.Error(), "auto-fix-exhausted") || res.ExitCode == 0 {
		t.Fatalf("got exit=%d err=%v, want auto-fix-exhausted failure", res.ExitCode, err)
	}
	if skill.calls != 2 || det.calls != 2 {
		t.Fatalf("skill calls=%d runner calls=%d, want 2/2", skill.calls, det.calls)
	}
	if res.InputTokens != 20 || res.CostUsd != 2 {
		t.Errorf("usage not summed across sessions: tokens=%d cost=%v", res.InputTokens, res.CostUsd)
	}
}

func TestPRMergeHandback_NoMarkerKeepsSkillResult(t *testing.T) {
	s, skill, det, rs, root := handbackFixture(t, false, merged)
	item := types.BoardItem{Number: 42, Repo: "owner/repo"}

	res, err := s.runPRMergeSkillWithFixHandback(context.Background(), StageRunParams{Stage: state.StagePRMerge}, rs, item, root)
	if err != nil || res.ExitCode != 0 {
		t.Fatalf("got exit=%d err=%v, want success", res.ExitCode, err)
	}
	if skill.calls != 1 || det.calls != 0 {
		t.Fatalf("skill calls=%d runner calls=%d, want 1/0", skill.calls, det.calls)
	}
}

func TestPRMergeHandback_OtherPuntFailsWithoutRerun(t *testing.T) {
	conflict := pmstages.PRMergeResult{Path: pmstages.PathPunt, PRNumber: 7, PRState: "OPEN", Reason: "not-mergeable: CONFLICTING"}
	s, skill, _, rs, root := handbackFixture(t, true, conflict)
	s.prAutoFixMaxAttempts = 3
	item := types.BoardItem{Number: 42, Repo: "owner/repo"}

	_, err := s.runPRMergeSkillWithFixHandback(context.Background(), StageRunParams{Stage: state.StagePRMerge}, rs, item, root)
	if err == nil || !strings.Contains(err.Error(), "CONFLICTING") {
		t.Fatalf("err=%v, want failure naming the punt", err)
	}
	if skill.calls != 1 {
		t.Fatalf("skill calls=%d, want 1", skill.calls)
	}
}
