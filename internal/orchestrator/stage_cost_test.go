package orchestrator

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/nightgauge/nightgauge/internal/execution"
	"github.com/nightgauge/nightgauge/internal/intelligence/tokens"
	"github.com/nightgauge/nightgauge/internal/state"
	"github.com/nightgauge/nightgauge/pkg/types"
)

const stageCostTestModel = "claude-sonnet-5"

// accumulateFixture runs a recorded Claude stream through the executor's
// accumulator. The fixture carries per-block repeated usage snapshots and two
// result envelopes, so the counts it yields are the deduped per-turn sums that
// execution/stream_test.go pins — incremental, never a re-summed running total.
func accumulateFixture(t *testing.T, name string) *execution.TokenAccumulator {
	t.Helper()
	data, err := os.ReadFile("../execution/testdata/" + name)
	if err != nil {
		t.Fatalf("read fixture: %v", err)
	}
	acc := &execution.TokenAccumulator{}
	for _, line := range strings.Split(strings.TrimSpace(string(data)), "\n") {
		acc.ParseStreamLine(line)
	}
	if acc.CacheRead == 0 {
		t.Fatalf("fixture %s yielded no cache reads — the shape this test exists for", name)
	}
	return acc
}

// The #1934 shape: the stage's result arrives without its cache pools, which
// reach the runtime separately through RecordStageTokenCounts. The live line
// used to price the result alone — "(0 cached)", 4–9x under the record. It now
// reads the booking, so it cannot disagree with the run summary.
func TestStageCostFromBookingCarriesCachePoolsTheResultLacked(t *testing.T) {
	acc := accumulateFixture(t, "claude_stream_subagent_multi_result.jsonl")

	rt := state.NewRuntimeState("nightgauge/nightgauge", 1934, "item-1934", "run-1934")
	rt.BeginStage(state.StagePRMerge)
	rt.RecordStageTokenCounts(state.StagePRMerge, tokens.TokenCounts{
		CacheRead: acc.CacheRead, CacheCreation5m: acc.CacheCreated,
	})
	rt.CompleteStage(0, tokens.TokenCounts{Input: acc.InputTokens, Output: acc.OutputTokens},
		stageCostTestModel, "claude")

	got, ok := stageCostFromBooking(rt, state.StagePRMerge)
	if !ok {
		t.Fatal("no booking found for the stage just completed")
	}
	if got.Input != acc.InputTokens || got.Output != acc.OutputTokens ||
		got.CacheRead != acc.CacheRead || got.CacheCreation != acc.CacheCreated {
		t.Errorf("booked pools = %+v, want in=%d out=%d cr=%d cc=%d",
			got, acc.InputTokens, acc.OutputTokens, acc.CacheRead, acc.CacheCreated)
	}

	full, _ := tokens.CalculateCostFor("claude", stageCostTestModel, tokens.TokenCounts{
		Input: acc.InputTokens, Output: acc.OutputTokens,
		CacheRead: acc.CacheRead, CacheCreation5m: acc.CacheCreated,
	})
	cacheless, _ := tokens.CalculateCostFor("claude", stageCostTestModel, tokens.TokenCounts{
		Input: acc.InputTokens, Output: acc.OutputTokens,
	})
	if got.CostUSD != full {
		t.Errorf("live cost = %.6f, want the cache-inclusive %.6f (cache-less would be %.6f)",
			got.CostUSD, full, cacheless)
	}
	if total := rt.Snapshot().TotalCostUSD; got.CostUSD != total {
		t.Errorf("live cost %.6f disagrees with the run summary total %.6f", got.CostUSD, total)
	}
	if got.Source != state.CostSourceComputed || got.CostLabel() != "derived from tokens" {
		t.Errorf("source = %q (%q), want a labelled derivation", got.Source, got.CostLabel())
	}
	if !strings.Contains(got.TokenSummary(), " cache read") || strings.Contains(got.TokenSummary(), "+ 0 cache read") {
		t.Errorf("token summary %q does not report the stage's cache reads", got.TokenSummary())
	}
}

func TestStageCostFromBookingReadsLatestAttempt(t *testing.T) {
	rt := state.NewRuntimeState("nightgauge/nightgauge", 1934, "item-1934", "run-1934")
	if _, ok := stageCostFromBooking(rt, state.StageFeatureDev); ok {
		t.Fatal("found a booking for a stage that never ran")
	}
	rt.BeginStage(state.StageFeatureDev)
	rt.CompleteStageWithCost(1, 10, 20, 30, 0.50)
	time.Sleep(time.Millisecond) // a retry is a distinct BeginStage occurrence
	rt.BeginStage(state.StageFeatureDev)
	rt.CompleteStageWithCost(0, 11, 21, 31, 0.75)

	got, _ := stageCostFromBooking(rt, state.StageFeatureDev)
	if got.CostUSD != 0.75 || got.CacheRead != 31 || got.Input != 11 {
		t.Errorf("got %+v, want the retry's booking (cost 0.75, cr 31, in 11)", got)
	}
	if got.CostLabel() != "cli-reported" {
		t.Errorf("label = %q, want cli-reported", got.CostLabel())
	}
}

func TestNativeCostDivergence(t *testing.T) {
	counts := StageCost{Input: 206, Output: 47798, CacheRead: 14466305, CacheCreation: 201473}
	derived, _ := tokens.CalculateCostFor("claude", stageCostTestModel, tokens.TokenCounts{
		Input: counts.Input, Output: counts.Output, CacheRead: counts.CacheRead,
		CacheCreation5m: counts.CacheCreation,
	})
	with := func(cost float64, source string) StageCost {
		c := counts
		c.CostUSD, c.Source = cost, source
		return c
	}
	tests := []struct {
		name  string
		cost  StageCost
		model string
		want  bool
	}{
		{"calibration-level disagreement", with(derived*0.8, state.CostSourceNative), stageCostTestModel, false},
		{"cli figure 8x under the tokens", with(derived/8, state.CostSourceNative), stageCostTestModel, true},
		{"cli figure 3x over the tokens", with(derived*3, state.CostSourceNative), stageCostTestModel, true},
		{"derived cost has nothing to compare against", with(derived/8, state.CostSourceComputed), stageCostTestModel, false},
		{"unpriced model", with(derived/8, state.CostSourceNative), "no-such-model", false},
		{"both below the floor", StageCost{Output: 10, CostUSD: 0.01, Source: state.CostSourceNative}, stageCostTestModel, false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if _, got := nativeCostDivergence(tc.cost, "claude", tc.model); got != tc.want {
				t.Errorf("diverged = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestRecordCostDivergencePersistsAnomaly(t *testing.T) {
	rt := state.NewRuntimeState("nightgauge/nightgauge", 1934, "item-1934", "run-1934")
	c := StageCost{Input: 206, Output: 47798, CacheRead: 14466305, CacheCreation: 201473,
		CostUSD: 0.7176, Source: state.CostSourceNative}

	detail, ok := recordCostDivergence(rt, state.StagePRMerge, c, "claude", stageCostTestModel, time.Now())
	if !ok {
		t.Fatal("a 9x disagreement was not reported")
	}
	got := rt.StageAnomaliesFor(state.StagePRMerge)
	if len(got) != 1 || got[0].Kind != anomalyCostSourceDivergence ||
		got[0].StageCostUSD != 0.7176 || got[0].Detail != detail {
		t.Errorf("anomalies = %+v, want one %s carrying %q", got, anomalyCostSourceDivergence, detail)
	}
}

// cachePoolsOffResultRunner reproduces how the #1927 run delivered usage: the
// stage result carries no cache pools and no CLI cost, and the pools reach the
// runtime separately — exactly as the IPC stage runner hands them over.
type cachePoolsOffResultRunner struct {
	runIDCapturingRunner
	cacheRead, cacheCreation int
}

func (r *cachePoolsOffResultRunner) RunStage(ctx context.Context, params StageRunParams) (*StageRunResult, error) {
	res, err := r.runIDCapturingRunner.RunStage(ctx, params)
	if params.Runtime != nil {
		params.Runtime.RecordStageTokenCounts(params.Stage, tokens.TokenCounts{
			CacheRead: r.cacheRead, CacheCreation5m: r.cacheCreation,
		})
	}
	return res, err
}

// Drives a real runPipeline: every stage-complete callback must report the
// same cost and cache reads the run then books, never the cache-less
// re-derivation that printed "(0 cached)" beside a 9x larger summary.
func TestRunPipeline_StageCompleteReportsTheBookedCost(t *testing.T) {
	isolateRoutingEnv(t)
	root := gitWorkspace(t)
	runner := &cachePoolsOffResultRunner{cacheRead: 3_591_812, cacheCreation: 87_443}
	s := newRunIdentityTestScheduler(t, root, runner)

	reported := map[string]StageCost{}
	s.OnStageComplete(func(_ string, _ int, stage string, _ error, cost StageCost, _ string) {
		reported[stage] = cost
	})
	var snap *state.RuntimeState
	s.OnPipelineComplete(func(_ string, _ int, rt *state.RuntimeState, _ bool) { snap = rt })

	s.runPipeline(context.Background(), types.BoardItem{Number: 991934, Repo: "nightgauge/nightgauge",
		ID: "item-991934", Title: "t", Labels: []string{"type:feature", "component:go-binary"}})
	if snap == nil {
		t.Fatal("the pipeline never completed; the fixture is wrong, not the assertion")
	}

	checked := 0
	for _, sr := range snap.AllStageAttempts() {
		got, ok := reported[string(sr.Stage)]
		if !ok || sr.CacheRead == 0 {
			continue
		}
		checked++
		if got.CacheRead != sr.CacheRead || got.CostUSD != sr.CostUSD {
			t.Errorf("%s: callback reported cr=%d $%.4f, run booked cr=%d $%.4f",
				sr.Stage, got.CacheRead, got.CostUSD, sr.CacheRead, sr.CostUSD)
		}
		if got.CacheRead != runner.cacheRead {
			t.Errorf("%s: callback reported %d cache reads, want the %d the executor observed",
				sr.Stage, got.CacheRead, runner.cacheRead)
		}
	}
	if checked == 0 {
		t.Fatal("no dispatched stage booked cache reads — the runner never reached the runtime")
	}
}
