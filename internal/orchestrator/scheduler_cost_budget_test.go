package orchestrator

import (
	"testing"

	"github.com/nightgauge/nightgauge/internal/state"
)

// TestStageOptionsFromParamsCarriesCostBudget pins #1749: before this issue,
// neither production StageOptions builder ever set CostBudget, so it was
// always the execution.StageOptions zero value and the OpenCode cost watchdog
// / claude,claude_sdk,lmstudio,ollama --max-budget-usd translation
// (internal/execution/manager.go, internal/execution/adapters/*.go) never
// ran. stageOptionsFromParams is the exact seam ExecutionManagerRunner.RunStage
// uses to build execution.StageOptions from StageRunParams, extracted so this
// is assertable without spawning a process.
func TestStageOptionsFromParamsCarriesCostBudget(t *testing.T) {
	params := StageRunParams{
		Stage:       state.StagePRCreate,
		IssueNumber: 1749,
		Repo:        "nightgauge/nightgauge",
		Model:       "sonnet",
		CostBudget:  42.5,
	}

	opts := stageOptionsFromParams(params)

	if opts.CostBudget != 42.5 {
		t.Errorf("CostBudget = %v, want 42.5 (params.CostBudget must reach execution.StageOptions.CostBudget)", opts.CostBudget)
	}
}

// TestStageOptionsFromParamsZeroCostBudgetStaysZero guards against a resolver
// change silently forcing a non-zero budget onto every dispatch regardless of
// what StageRunParams.CostBudget actually carries — the projection itself
// must stay a straight pass-through.
func TestStageOptionsFromParamsZeroCostBudgetStaysZero(t *testing.T) {
	opts := stageOptionsFromParams(StageRunParams{Stage: state.StagePRCreate})

	if opts.CostBudget != 0 {
		t.Errorf("CostBudget = %v, want 0 when StageRunParams.CostBudget is unset", opts.CostBudget)
	}
}
