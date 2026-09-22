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
// This test instead iterates state.AllPipelineStages — the independent
// source of truth for "which stages exist" — and asserts each one resolves
// to a non-empty StageSkillDirs entry. Removing spike-materialize's
// registration (or forgetting to add a future stage's) turns this red;
// growing the map with an extra, unused key does not, because the direction
// being checked is "every constant has a map entry," not the reverse.
//
// AllPipelineStages (internal/state/board_state.go) is itself a
// hand-written slice, but a SEPARATE guard in that package
// (TestAllPipelineStagesMatchesDeclaredConstants, added alongside this test
// for #1969's follow-up review) parses board_state.go's own AST and fails
// if a new PipelineStage constant is ever declared without also being added
// there — so a stage this test needs to see cannot be silently missing from
// AllPipelineStages either. Every PipelineStage constant reaches Render
// through StageSkillDirs, whether or not the scheduler appends it
// unconditionally (spike-materialize is conditional on issue type;
// issue-refine is dispatched by the autonomous refinement loop rather than
// runPipeline's stage list) — all of them belong in this check.
func TestEveryStageConstantIsRenderable(t *testing.T) {
	for _, stage := range state.AllPipelineStages {
		dir, ok := StageSkillDirs[string(stage)]
		if !ok || dir == "" {
			t.Errorf("state.PipelineStage %q has no StageSkillDirs entry — "+
				"Locate(%q) will fail for every issue that reaches this stage, "+
				"most likely after its own work has already merged. Register it "+
				"in StageSkillDirs (internal/skillrender/render.go).", stage, stage)
		}
	}
}
