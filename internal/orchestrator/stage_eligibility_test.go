package orchestrator

import (
	"context"
	"testing"

	"github.com/nightgauge/nightgauge/internal/state"
	"github.com/nightgauge/nightgauge/internal/trust"
	"github.com/nightgauge/nightgauge/pkg/types"
)

// A run re-decides eligibility before every stage on a fresh cache (#2130).
// Here the issue is an outsider's whose approval was voided by an edit after
// it was queued: the run stops before its first stage dispatches anything.
func TestRunPipeline_StopsBeforeAStageWhenTheIssueIsNoLongerVouchedFor(t *testing.T) {
	root := gitWorkspace(t)
	s := terminalSkipScheduler(t, root, "unused")
	runner := newStallStageRunner(map[state.PipelineStage]int{}, "")
	s.stageRunner = runner
	s.trustGate = trust.NewGate(&fakeTrustForge{
		levels:  map[string]trust.Level{"stranger": trust.LevelRead},
		signals: map[string]trust.Signals{"acme/widget#77": {Author: "stranger"}},
	}, nil)

	s.runPipeline(context.Background(), types.BoardItem{Number: 77, Repo: "acme/widget", ID: "item-77",
		Title: "voided approval", Labels: []string{"type:feature"}})

	runner.mu.Lock()
	defer runner.mu.Unlock()
	for stage, n := range runner.callCount {
		if n > 0 {
			t.Fatalf("stage %s dispatched %d time(s) for an issue no writer vouches for", stage, n)
		}
	}
}

// The same run proceeds when a writer vouches for the issue.
func TestRunPipeline_RunsAStageWhenTheIssueIsVouchedFor(t *testing.T) {
	root := gitWorkspace(t)
	s := terminalSkipScheduler(t, root, "unused")
	runner := newStallStageRunner(map[state.PipelineStage]int{}, "")
	s.stageRunner = runner
	s.trustGate = trust.NewGate(&fakeTrustForge{
		levels:  map[string]trust.Level{"maintainer": trust.LevelWrite},
		signals: map[string]trust.Signals{"acme/widget#78": {Author: "maintainer"}},
	}, nil)

	s.runPipeline(context.Background(), types.BoardItem{Number: 78, Repo: "acme/widget", ID: "item-78",
		Title: "vouched", Labels: []string{"type:feature"}})

	runner.mu.Lock()
	defer runner.mu.Unlock()
	if runner.callCount[state.StageIssuePickup] == 0 {
		t.Fatal("issue-pickup never dispatched for a vouched-for issue")
	}
}
