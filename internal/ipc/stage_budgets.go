package ipc

import (
	"context"
	"fmt"
	"log"

	"github.com/nightgauge/nightgauge/internal/config"
	"github.com/nightgauge/nightgauge/internal/execution"
	"github.com/nightgauge/nightgauge/internal/orchestrator"
	"github.com/nightgauge/nightgauge/internal/state"
)

// resolveStageBudgets answers pipeline.resolveStageBudgets (#1668): the
// non-USD stage budget the Go executor would enforce for a dispatch of
// p.Stage on p.Adapter with p.Model, resolved by the same function the
// execution manager calls (#1652), so an editor-launched stage never
// re-implements the defaults or the zero-cost floor. A config that cannot be
// read is an error, not a default: the caller refuses a zero-cost stage on it.
func (s *Server) resolveStageBudgets(ctx context.Context, p PipelineResolveStageBudgetsParams) (*PipelineResolveStageBudgetsResult, error) {
	if p.Stage == "" || p.Adapter == "" {
		return nil, fmt.Errorf("stage and adapter are required")
	}
	root := s.repoRoot(p.Repo)
	var budgets map[string]config.StageBudget
	if root != "" {
		cfg, err := config.Load(root)
		if err != nil {
			return nil, fmt.Errorf("load config: %w", err)
		}
		if cfg != nil && cfg.Pipeline != nil {
			budgets = cfg.Pipeline.StageBudgets
		}
	}
	budget, cost := execution.ResolveDispatchStageBudget(budgets, p.Stage, p.Adapter, p.Model, root)
	wall := int64(budget.MaxWallClock / 1e6)
	if budget.MaxWallClock == config.StageBudgetUnlimited {
		wall = config.StageBudgetUnlimited
	}
	return &PipelineResolveStageBudgetsResult{
		MaxTurns:            budget.MaxTurns,
		MaxWallClockMs:      wall,
		MaxTokens:           budget.MaxTokens,
		ZeroCost:            cost == config.StageZeroCost,
		ContextWindowTokens: orchestrator.DispatchContextWindow(ctx, root, p.Adapter, p.Model),
		Warnings:            budget.Warnings,
	}, nil
}

// maxPeakToWindowRatio bounds a notified peak against its window: a prompt
// more than ten times the window a stage ran with is not a measurement.
const maxPeakToWindowRatio = 10

// notifyStageContext validates a "complete" notify's context-window telemetry
// (#1668). The fields are untrusted input: a negative value, a zero window
// beside a peak, or a peak over 10x its window drops the whole set, logged,
// and records nothing. A notify with no peak or no window records nothing
// either, so a payload without the fields records exactly as before.
func notifyStageContext(p PipelineNotifyStageTransitionParams) (state.StageContext, bool) {
	if p.PeakStepInputTokens == 0 && p.ContextWindowTokens == 0 && p.CompactionCount == nil {
		return state.StageContext{}, false
	}
	compactionsInvalid := p.CompactionCount != nil && *p.CompactionCount < 0
	if p.PeakStepInputTokens < 0 || p.ContextWindowTokens < 0 || compactionsInvalid ||
		(p.PeakStepInputTokens > 0 && p.ContextWindowTokens > 0 &&
			p.PeakStepInputTokens > maxPeakToWindowRatio*p.ContextWindowTokens) {
		log.Printf("notifyStageTransition: dropping context telemetry for %s#%d %s: peak=%d window=%d compactions=%v",
			p.Repo, p.IssueNumber, p.Stage, p.PeakStepInputTokens, p.ContextWindowTokens, compactionLog(p.CompactionCount))
		return state.StageContext{}, false
	}
	if p.PeakStepInputTokens == 0 || p.ContextWindowTokens == 0 {
		return state.StageContext{}, false
	}
	sc := state.StageContext{PeakStepInputTokens: p.PeakStepInputTokens, ContextWindowTokens: p.ContextWindowTokens}
	if p.CompactionCount != nil {
		n := *p.CompactionCount
		sc.Compactions = &n
	}
	return sc, true
}

func compactionLog(n *int) any {
	if n == nil {
		return "absent"
	}
	return *n
}
