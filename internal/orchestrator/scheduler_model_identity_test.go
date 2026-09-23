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

// TestCLIRunResultCarriesTheCachePools (#1651 AC7): the executor's cache
// pools reach the stage result. Dropping them recorded every Go-direct
// stage's cache reads as 0 — 97k-182k per OpenCode feature-dev session on the
// live run — so the stage and phase token totals undercounted.
func TestCLIRunResultCarriesTheCachePools(t *testing.T) {
	got := cliRunResultToStageResult(&adapters.RunResult{
		InputTokens:         1200,
		OutputTokens:        2900,
		CacheReadTokens:     97000,
		CacheCreationTokens: 450,
	})
	if got.CacheReadTokens != 97000 || got.CacheCreationTokens != 450 {
		t.Errorf("(cache read, cache creation) = (%d, %d), want (97000, 450)", got.CacheReadTokens, got.CacheCreationTokens)
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

// #1651 review S3: the terminating-token booking takes COMBINED input and the
// scheduler holds the non-cached pool, so a failed stage with 1200 input and
// 97000 cache-read tokens booked input = 1200 - 97000 = -95800 whenever the
// run record synthesised the stage from it.
func TestRecordTerminatingStageTokens_BooksNonCachedInput(t *testing.T) {
	rs := state.NewRuntimeState("nightgauge/test", 1651, "item-1651", "run-1651")
	rs.BeginStage(state.StageFeatureDev)
	rs.SetStageError(state.StageFeatureDev, "feature-dev failed (fixture)")
	recordTerminatingStageTokens(rs, state.StageFeatureDev, 1200, 300, 97000, 0.5)
	rs.Stage = state.StageFeatureDev

	record := state.NewHistoryWriter(t.TempDir()).BuildV2Record(rs, false, "feature-dev failed (fixture)", state.V2RunInput{}, time.Now())
	tok, ok := record.Tokens.PerStage[string(state.StageFeatureDev)]
	if !ok {
		t.Fatalf("no synthesized feature-dev token entry: %+v", record.Tokens.PerStage)
	}
	if tok.Input != 1200 || tok.CacheRead != 97000 {
		t.Errorf("feature-dev tokens = input %d cache_read %d, want input 1200 cache_read 97000", tok.Input, tok.CacheRead)
	}
}
