package state_test

// This file is package state_test, not state, for one reason: it drives the
// real numerator. The per-step peak is computed by internal/execution's
// opencode stream parser, and internal/execution imports internal/state, so
// only an external test package can feed step_finish lines through that
// parser and read the V2 record the builder makes of the result (#1653).

import (
	"encoding/json"
	"fmt"
	"testing"
	"time"

	"github.com/nightgauge/nightgauge/internal/execution"
	"github.com/nightgauge/nightgauge/internal/intelligence/tokens"
	"github.com/nightgauge/nightgauge/internal/runstate"
	"github.com/nightgauge/nightgauge/internal/state"
)

// openCodeStepFinish is one step_finish line in the shape opencode 1.18.30
// emits (internal/execution/testdata/opencode_stream_local_capture.jsonl):
// part.tokens = {total, input, output, reasoning, cache: {write, read}}.
func openCodeStepFinish(input, cacheRead, cacheWrite int) string {
	return fmt.Sprintf(`{"type":"step_finish","sessionID":"ses_1","part":{"type":"step-finish","reason":"tool-calls",`+
		`"tokens":{"total":%d,"input":%d,"output":40,"reasoning":0,"cache":{"write":%d,"read":%d}},"cost":0}}`,
		input+40, input, cacheWrite, cacheRead)
}

// TestV2Record_ContextUtilizationIsThePeakStepNotTheSum: three steps whose
// prompts are 7550, 12010 and 9800 tokens, in a 131072-token window, give a
// utilization of 0.0916 (12010 / 131072) with a peak of 12010. Summing the
// steps (29360) would give 0.2240, which is how much the stage SPENT, not how
// close any one prompt came to the window. The middle step's prompt is split
// across input, cache.read and cache.write, as a hosted provider reports it,
// so dropping either cache pool from the per-step sum also turns this red.
func TestV2Record_ContextUtilizationIsThePeakStepNotTheSum(t *testing.T) {
	acc := &execution.TokenAccumulator{}
	for _, line := range []string{
		openCodeStepFinish(7550, 0, 0),
		openCodeStepFinish(2010, 9000, 1000),
		openCodeStepFinish(9800, 0, 0),
	} {
		if _, ok := acc.ParseOpenCodeStreamLine(line); !ok {
			t.Fatalf("test premise broken: the parser did not take %s", line)
		}
	}

	runID, err := runstate.NewRunID()
	if err != nil {
		t.Fatal(err)
	}
	rs := state.NewRuntimeState("nightgauge/nightgauge", 1653, "item", runID)
	rs.BeginStage(state.StageFeatureDev)
	rs.RecordStageContext(state.StageFeatureDev, acc.PeakStepInputTokens, 131072, nil)
	rs.CompleteStage(0, tokens.TokenCounts{Input: acc.InputTokens, Output: acc.OutputTokens}, "", "opencode")

	rec := state.NewHistoryWriter(t.TempDir()).BuildV2Record(rs, true, "", state.V2RunInput{Title: "t"}, time.Now())
	data, err := json.Marshal(rec)
	if err != nil {
		t.Fatal(err)
	}
	var wire struct {
		Stages map[string]map[string]any `json:"stages"`
	}
	if err := json.Unmarshal(data, &wire); err != nil {
		t.Fatal(err)
	}
	stage := wire.Stages["feature-dev"]
	if got := stage["context_window_utilization"]; got != 0.0916 {
		t.Errorf("context_window_utilization = %v, want 0.0916 (peak 12010 / 131072; the sum of the steps would be 0.2240)", got)
	}
	if got := stage["peak_step_input_tokens"]; got != float64(12010) {
		t.Errorf("peak_step_input_tokens = %v, want 12010", got)
	}
	if got := stage["context_window_tokens"]; got != float64(131072) {
		t.Errorf("context_window_tokens = %v, want 131072", got)
	}
}
