package orchestrator

import (
	"context"
	"testing"
	"time"

	"github.com/nightgauge/nightgauge/internal/execution/adapters"
	"github.com/nightgauge/nightgauge/internal/state"
	"github.com/nightgauge/nightgauge/pkg/types"
)

// identityStageRunner is runIDCapturingRunner whose feature-dev stage
// reports the ADR-022 § 2 identity a multi-provider adapter puts on its
// result: a model other than the dispatched one served it, on a declared
// endpoint.
type identityStageRunner struct {
	runIDCapturingRunner
}

func (r *identityStageRunner) RunStage(ctx context.Context, params StageRunParams) (*StageRunResult, error) {
	out, err := r.runIDCapturingRunner.RunStage(ctx, params)
	if params.Stage == state.StageFeatureDev && out != nil {
		out.ServedModel = "lm-studio/qwen/qwen3.8-27b"
		out.ModelProvider = "lm-studio"
		out.UpstreamModel = "anthropic/claude-sonnet-5"
		out.Endpoint = "lmstudio"
	}
	return out, err
}

// TestCLIRunResultCarriesTheModelIdentity: the projection of the executor's
// result carries the model provider, the upstream model and the endpoint,
// so the stage record can be written from them.
func TestCLIRunResultCarriesTheModelIdentity(t *testing.T) {
	got := cliRunResultToStageResult(&adapters.RunResult{
		ServedModel:   "claude-sonnet-5",
		ModelProvider: "anthropic",
		UpstreamModel: "anthropic/claude-sonnet-5",
		Endpoint:      "lmstudio",
	})
	if got.ModelProvider != "anthropic" || got.UpstreamModel != "anthropic/claude-sonnet-5" || got.Endpoint != "lmstudio" {
		t.Errorf("(model provider, upstream model, endpoint) = (%q, %q, %q), want the executor's", got.ModelProvider, got.UpstreamModel, got.Endpoint)
	}
}

// TestRunPipeline_RecordsTheStageModelIdentity: the identity a stage's result
// carries reaches the run's record of that stage (ADR-022 § 2), and the
// dispatched -m survives as the upstream model when another model served the
// stage. A stage whose result carries none records none.
func TestRunPipeline_RecordsTheStageModelIdentity(t *testing.T) {
	root := gitWorkspace(t)
	runner := &identityStageRunner{}
	s := newRunIdentityTestScheduler(t, root, runner)
	var snap *state.RuntimeState
	s.OnPipelineComplete(func(_ string, _ int, rt *state.RuntimeState, _ bool) { snap = rt })

	s.runPipeline(context.Background(), types.BoardItem{Number: 1630, Repo: "nightgauge/nightgauge", ID: "item-1630"})

	if snap == nil {
		t.Fatal("the pipeline never completed; the fixture is wrong, not the assertion")
	}
	want := state.StageModelIdentity{Provider: "lm-studio", Upstream: "anthropic/claude-sonnet-5", Endpoint: "lmstudio"}
	if got := snap.StageModelIdentityOf(state.StageFeatureDev); got != want {
		t.Errorf("feature-dev identity = %+v, want %+v", got, want)
	}
	if got := snap.StageModelIdentityOf(state.StageFeaturePlanning); got != (state.StageModelIdentity{}) {
		t.Errorf("feature-planning identity = %+v, want none", got)
	}
	sel := state.NewHistoryWriter(t.TempDir()).BuildV2Record(snap, true, "", state.V2RunInput{}, time.Now()).
		Stages[string(state.StageFeatureDev)].ModelSelection
	if sel == nil || sel.Model != "lm-studio/qwen/qwen3.8-27b" || sel.ModelProvider != "lm-studio" ||
		sel.UpstreamModel != "anthropic/claude-sonnet-5" || sel.Endpoint != "lmstudio" {
		t.Errorf("feature-dev model_selection = %+v, want the served model with its identity", sel)
	}
}
