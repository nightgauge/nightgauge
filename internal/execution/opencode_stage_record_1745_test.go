package execution

import (
	"testing"

	"github.com/nightgauge/nightgauge/internal/execution/adapters"
	"github.com/nightgauge/nightgauge/internal/intelligence/tokens"
	"github.com/nightgauge/nightgauge/internal/state"
)

// bookOpenCodeStage books a replayed stage the way the scheduler's CLI path
// does: cliRunResultToStageResult carries the RunResult's token pools and
// ServedModel, the scheduler takes ServedModel as servedModel, and — the CLI
// path reporting no native cost (StageRunResult.CostUsd stays 0) — books it
// with RuntimeState.CompleteStage(exitCode, counts, servedModel, adapterName)
// where adapterName is "opencode". Pricing therefore runs through
// tokens.CalculateCostFor("opencode", <recorded served model>), the
// production call, not openCodeStageCost's provider-keyed model of it.
func bookOpenCodeStage(t *testing.T, result *adapters.RunResult) state.StageResult {
	t.Helper()
	rs := state.NewRuntimeState("nightgauge/nightgauge", 1745, "item", "run-1745")
	rs.BeginStage(state.StageFeatureDev)
	rs.CompleteStage(result.ExitCode, tokens.TokenCounts{
		Input: result.InputTokens, Output: result.OutputTokens, CacheRead: result.CacheReadTokens,
		CacheCreation5m: result.CacheCreationTokens,
	}, result.ServedModel, "opencode")
	if len(rs.CompletedStages) != 1 {
		t.Fatalf("completed stages = %d, want 1", len(rs.CompletedStages))
	}
	return rs.CompletedStages[0]
}

// TestOpenCodeRealCaptures_ProductionStageRecordStamping is #1745: the
// #1629/#1743 real captures, replayed through the production finish/apply
// path to a RunResult, then booked through the production stage-record
// pricing call. The local capture's record is a stamped zero; the cloud
// stand-in's is priced from registry rates, stamped and non-zero, and not
// OpenCode's own catalog figure.
func TestOpenCodeRealCaptures_ProductionStageRecordStamping(t *testing.T) {
	t.Run("local capture is a stamped zero", func(t *testing.T) {
		_, result, _ := replayOpenCodeFold(t, "opencode_stream_local_capture.jsonl", "lmstudio/qwen/qwen3.8-27b",
			[]openCodeCaptureSession{
				{id: openCodeCaptureParent, tokens: openCodeTokens(17365, 144, 47, 0, 0), provider: "lmstudio", model: "qwen/qwen3.8-27b"},
			})
		rec := bookOpenCodeStage(t, result)
		if rec.CostUnstamped || rec.CostUSD != 0 || rec.CostSource != state.CostSourceComputed {
			t.Errorf("stage record cost=%v unstamped=%v source=%q; want a stamped (computed) zero for a local provider",
				rec.CostUSD, rec.CostUnstamped, rec.CostSource)
		}
		if rec.InputTokens != 17365 || rec.OutputTokens != 191 {
			t.Errorf("stage record input/output = %d/%d, want 17365/191", rec.InputTokens, rec.OutputTokens)
		}
		// The test-local model of the rule agrees with production here.
		if cost, stamped := openCodeStageCost(result); cost != rec.CostUSD || stamped == rec.CostUnstamped {
			t.Errorf("openCodeStageCost = %v/%v disagrees with the production record %v/unstamped=%v",
				cost, stamped, rec.CostUSD, rec.CostUnstamped)
		}
	})

	t.Run("cloud stand-in capture is priced from the registry", func(t *testing.T) {
		_, result, _ := replayOpenCodeFold(t, "opencode_stream_cloud_capture.jsonl", "xai/grok-4.6",
			[]openCodeCaptureSession{
				{id: openCodeCaptureParent, tokens: openCodeTokens(3089, 14, 0, 0, 0), cost: 0.006262, provider: "xai", model: "grok-4.6"},
			})
		rec := bookOpenCodeStage(t, result)
		if rec.CostUnstamped || rec.CostUSD <= 0 || rec.CostSource != state.CostSourceComputed {
			t.Errorf("stage record cost=%v unstamped=%v source=%q; want a stamped, non-zero registry price",
				rec.CostUSD, rec.CostUnstamped, rec.CostSource)
		}
		if closeUSD(rec.CostUSD, result.AdapterReportedCostUSD) {
			t.Errorf("stage record cost %v is OpenCode's catalog figure; ADR-022 § 3 prices from the registry", rec.CostUSD)
		}
		if cost, stamped := openCodeStageCost(result); !closeUSD(cost, rec.CostUSD) || stamped == rec.CostUnstamped {
			t.Errorf("openCodeStageCost = %v/%v disagrees with the production record %v/unstamped=%v",
				cost, stamped, rec.CostUSD, rec.CostUnstamped)
		}
	})
}
