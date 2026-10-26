package orchestrator

import (
	"testing"

	"github.com/nightgauge/nightgauge/internal/intelligence/routing"
	"github.com/nightgauge/nightgauge/internal/state"
)

// TestPlannedStageModel_EqualsDispatch is the #2537 contract: the corpus's
// routing prediction (PlannedStageModel) and the model dispatch serves
// (resolveDispatchModel) are one selection. With no runtime override in force
// (fresh retry engine, no downgrade), they must agree for every mode, size,
// routed tier, configured stage model and floor.
func TestPlannedStageModel_EqualsDispatch(t *testing.T) {
	modes := []string{"elevated", "efficiency", "maximum"}
	decisions := map[string]routing.Decision{
		"size S":   decisionFor("type:feature", "size:S"),
		"size M":   decisionFor("type:feature", "size:M"),
		"unsized":  decisionFor("type:feature"),
		"zero":     {},
		"risk bug": decisionFor("type:bug", "size:L", "priority:critical"),
	}
	tiers := []string{"", "haiku", "sonnet", "opus"}
	configs := map[string]string{
		"no stage model":       "",
		"stage model haiku":    "pipeline:\n  stage_models:\n    feature-dev: haiku\n",
		"stage model fable":    "pipeline:\n  stage_models:\n    feature-dev: fable\n",
		"minimum model floors": "model_routing:\n  minimum_model:\n    feature-dev: opus\n",
	}
	floorSets := map[string]map[string]string{
		"no floors":   nil,
		"opus floor":  raiseStageFloors(nil, "opus"),
		"haiku floor": raiseStageFloors(nil, "haiku"),
	}

	for _, mode := range modes {
		for cfgName, cfg := range configs {
			for dName, d := range decisions {
				for _, tier := range tiers {
					for fName, floors := range floorSets {
						name := mode + "/" + cfgName + "/" + dName + "/" + tier + "/" + fName
						t.Run(name, func(t *testing.T) {
							var dir string
							if cfg != "" {
								dir = routedWorkspace(t, cfg)
							} else {
								dir = isolatedWorkspace(t)
							}
							t.Setenv("NIGHTGAUGE_PERFORMANCE_MODE", mode)
							for _, stage := range []state.PipelineStage{
								state.StageFeatureDev, state.StageFeaturePlanning, state.StageSpikeMaterialize,
							} {
								planned := PlannedStageModel(dir, stage, tier, d, floors, "")
								served := testScheduler(t).resolveDispatchModel(stage, 1, dir, tier, d, floors, "")
								if planned != served {
									t.Errorf("%s: PlannedStageModel = %q, dispatch = %q — prediction and dispatch diverged",
										stage, planned, served)
								}
							}
						})
					}
				}
			}
		}
	}
}

// TestPlannedStageModel_ElevatedSonnetPredictsOpus is the regression the issue
// reported: under the default elevated mode the router's run-wide tier says
// sonnet, but feature-dev's built-in stage default (#2512) dispatches opus. The
// recorded prediction must be opus, or every such run books a phantom miss.
func TestPlannedStageModel_ElevatedSonnetPredictsOpus(t *testing.T) {
	dir := isolatedWorkspace(t)
	t.Setenv("NIGHTGAUGE_PERFORMANCE_MODE", "elevated")
	if got := PlannedStageModel(dir, state.StageFeatureDev, "sonnet", decisionFor("type:bug", "size:S"), nil, ""); got != "opus" {
		t.Errorf("planned feature-dev = %q, want opus — the stage default dispatch actually serves", got)
	}
}
