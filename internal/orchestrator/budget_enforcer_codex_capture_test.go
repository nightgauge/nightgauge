package orchestrator

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/nightgauge/nightgauge/internal/execution"
	"github.com/nightgauge/nightgauge/internal/intelligence/tokens"
	"github.com/nightgauge/nightgauge/internal/layout/layouttest"
	"github.com/nightgauge/nightgauge/internal/state"
)

// TestCodexLiveCaptureReachesBudgetAndHistory drives the live codex-cli
// capture (#119, internal/execution/testdata/codex_stream_real_capture.jsonl)
// through the real parser, the budget enforcer and the run-history writer,
// calling each hop directly with the arguments scheduler.go passes for a codex
// stage (it does not drive the scheduler itself). It proves a codex
// stage's spend is non-zero at the enforcer and in tokens.per_stage, priced.
func TestCodexLiveCaptureReachesBudgetAndHistory(t *testing.T) {
	t.Parallel()
	data, err := os.ReadFile(filepath.Join("..", "execution", "testdata", "codex_stream_real_capture.jsonl"))
	if err != nil {
		t.Fatalf("read capture: %v", err)
	}
	acc := &execution.TokenAccumulator{}
	for _, line := range strings.Split(strings.TrimSpace(string(data)), "\n") {
		if ev, _ := acc.ParseLine(execution.StreamFormatCodex, line); ev == nil {
			t.Fatalf("capture line did not parse: %q", line)
		}
	}
	if acc.InputTokens <= 0 || acc.OutputTokens <= 0 || acc.CacheRead <= 0 {
		t.Fatalf("parsed tokens in=%d out=%d cache=%d, want all > 0",
			acc.InputTokens, acc.OutputTokens, acc.CacheRead)
	}

	be := NewBudgetEnforcer(BudgetConfig{
		PipelineCeilingTokens: 1_000_000,
		PerStageCeilings:      map[string]int{"feature-dev": 1_000_000},
		GracePercent:          10,
		Mode:                  "hard",
	})
	be.RecordStageTokens("feature-dev", acc.InputTokens, acc.OutputTokens)
	want := acc.InputTokens + acc.OutputTokens
	if got := be.StageUsed("feature-dev"); got != want {
		t.Errorf("StageUsed = %d, want %d", got, want)
	}
	if got := be.TotalUsed(); got != want {
		t.Errorf("TotalUsed = %d, want %d", got, want)
	}
	// A ceiling below the capture's spend must trip: the enforcer saw it.
	tight := NewBudgetEnforcer(BudgetConfig{
		PipelineCeilingTokens: 1_000_000,
		PerStageCeilings:      map[string]int{"feature-dev": want / 2},
		GracePercent:          0,
		Mode:                  "hard",
	})
	tight.RecordStageTokens("feature-dev", acc.InputTokens, acc.OutputTokens)
	if d := tight.CheckStageBudget("feature-dev"); !d.ShouldTerminate || d.UsedTokens != want {
		t.Errorf("CheckStageBudget = %+v for %d tokens against a %d ceiling", d, want, want/2)
	}

	dir := layouttest.Repo(t)
	rs := state.NewRuntimeState("nightgauge/nightgauge", 119, "item-119", "run-119-codex")
	rs.BeginStage(state.StageFeatureDev)
	rs.RecordStageAdapter(state.StageFeatureDev, "codex")
	rs.CompleteStage(0, tokens.TokenCounts{
		Input: acc.InputTokens, Output: acc.OutputTokens, CacheRead: acc.CacheRead,
	}, "gpt-5.5", "codex")
	if err := state.NewHistoryWriter(dir).WriteV2(rs, true, "", state.V2RunInput{Title: "codex #119"}); err != nil {
		t.Fatalf("WriteV2: %v", err)
	}
	day := time.Now().Format("2006-01-02") + ".jsonl"
	raw, err := os.ReadFile(filepath.Join(layouttest.PipelineDir(t, dir), "history", day))
	if err != nil {
		t.Fatalf("read history: %v", err)
	}
	var rec state.V2RunRecord
	if err := json.Unmarshal([]byte(strings.TrimSpace(string(raw))), &rec); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	ps, ok := rec.Tokens.PerStage[string(state.StageFeatureDev)]
	if !ok {
		t.Fatalf("tokens.per_stage has no feature-dev entry: %+v", rec.Tokens.PerStage)
	}
	if ps.Adapter != "codex" {
		t.Errorf("adapter = %q, want codex", ps.Adapter)
	}
	if ps.Input <= 0 || ps.Output <= 0 || ps.CacheRead != acc.CacheRead {
		t.Errorf("per_stage tokens in=%d out=%d cache=%d", ps.Input, ps.Output, ps.CacheRead)
	}
	if ps.CostUSD <= 0 || rec.Tokens.EstimatedCostUSD <= 0 {
		t.Errorf("cost per_stage=%v run=%v (source %q), want > 0",
			ps.CostUSD, rec.Tokens.EstimatedCostUSD, ps.CostSource)
	}
}
