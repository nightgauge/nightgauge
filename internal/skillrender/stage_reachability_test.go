package skillrender

import (
	"testing"

	"github.com/nightgauge/nightgauge/internal/state"
)

// TestEveryStageConstantIsRenderable is the guard #1969 needed and did not
// have.
//
// TestBundleShipsEverySkillTheGoDirectPathRenders (bundle_manifest_test.go)
// iterates StageSkillDirs itself, so a stage the scheduler can append but
// that was never ADDED to the map is invisible to it — the loop simply never
// visits the missing key. #1969 was exactly this: internal/orchestrator/
// scheduler.go appends state.StageSpikeMaterialize to the stage list for
// every type:spike issue, and internal/state/board_state.go defines the
// constant, but StageSkillDirs never gained the entry, so
// Locate("spike-materialize") failed for every spike issue after its work
// had already merged.
//
// This test instead iterates the PipelineStage constants declared in
// internal/state/board_state.go — the independent source of truth for
// "which stages exist" — and asserts each one resolves to a non-empty
// StageSkillDirs entry. Removing spike-materialize's registration (or
// forgetting to add a future stage's) turns this red; growing the map with
// an extra, unused key does not, because the direction being checked is
// "every constant has a map entry," not the reverse.
func TestEveryStageConstantIsRenderable(t *testing.T) {
	// Every PipelineStage constant board_state.go declares, whether or not
	// the scheduler appends it unconditionally (spike-materialize is
	// conditional on issue type; issue-refine is dispatched by the
	// autonomous refinement loop rather than runPipeline's stage list) —
	// all of them reach Render through this same map, so all of them belong
	// in this list.
	stages := []state.PipelineStage{
		state.StageIssuePickup,
		state.StageFeaturePlanning,
		state.StageFeatureDev,
		state.StageFeatureValidate,
		state.StagePRCreate,
		state.StagePRMerge,
		state.StageSpikeMaterialize,
		state.StageIssueRefine,
	}

	for _, stage := range stages {
		dir, ok := StageSkillDirs[string(stage)]
		if !ok || dir == "" {
			t.Errorf("state.PipelineStage %q has no StageSkillDirs entry — "+
				"Locate(%q) will fail for every issue that reaches this stage, "+
				"most likely after its own work has already merged. Register it "+
				"in StageSkillDirs (internal/skillrender/render.go).", stage, stage)
		}
	}
}
