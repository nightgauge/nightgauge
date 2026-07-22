package orchestrator

import (
	"encoding/json"
	"os"
	"sync"
)

// BudgetConfig configures the budget enforcer.
type BudgetConfig struct {
	PipelineCeilingTokens int            // Total pipeline token ceiling (0 = unlimited)
	PerStageCeilings      map[string]int // Per-stage token ceilings (0 = unlimited)
	GracePercent          int            // Soft warning threshold (% below ceiling)
	Mode                  string         // "hard" (terminate) or "soft" (warn only)

	// PerformanceMode names the active named mode (efficiency / elevated /
	// maximum) for this run. Mirrors the TS-side performance-mode resolution
	// so the enforcer's decisions / logs can carry mode context (Issue #3217).
	// Empty string means "unknown / not yet wired" and behaves identically to
	// pre-#3217 behavior — the observe-only branch is gated on the explicit
	// `maximum` value AND DisableBudgetCeiling.
	PerformanceMode string
	// DisableBudgetCeiling, when true AND PerformanceMode == "maximum", flips
	// CheckPipelineBudget / CheckStageBudget into observe-only mode: warnings
	// continue to fire (deltas still log) but ShouldTerminate is forced false.
	// This mirrors `MODE_PROFILES.maximum.pipeline.disableBudgetCeiling` from
	// the TS side. Setting this without `maximum` mode is a no-op — the gate
	// is mode-AND-flag.
	DisableBudgetCeiling bool
}

// DefaultBudgetConfig returns safe default budget configuration.
func DefaultBudgetConfig() BudgetConfig {
	return BudgetConfig{
		PipelineCeilingTokens: 0, // Unlimited by default
		PerStageCeilings:      make(map[string]int),
		GracePercent:          50,
		Mode:                  "soft",
	}
}

// BudgetDecision is the result of a budget check.
type BudgetDecision struct {
	ShouldWarn      bool
	ShouldTerminate bool
	UsedTokens      int
	CeilingTokens   int
	Reason          string
	// PerformanceMode echoes the mode that was active when the decision was
	// computed (Issue #3217). Lets log lines and metrics tag mode context
	// without re-resolving from disk per call.
	PerformanceMode string
}

// BudgetEnforcer tracks token usage and enforces budget limits.
//
// Safe for concurrent use: WaveOrchestrator runs multiple pipeline subagents
// against a single shared *Scheduler, so RecordStageTokens / Check* / Reset
// are all reachable from many goroutines at once. Without locking, the
// stageUsed map races and may panic with "concurrent map writes".
type BudgetEnforcer struct {
	mu        sync.Mutex
	config    BudgetConfig
	totalUsed int
	stageUsed map[string]int
}

// NewBudgetEnforcer creates a new budget enforcer with the given config.
func NewBudgetEnforcer(cfg BudgetConfig) *BudgetEnforcer {
	return &BudgetEnforcer{
		config:    cfg,
		stageUsed: make(map[string]int),
	}
}

// RecordStageTokens records token usage for a completed stage.
func (b *BudgetEnforcer) RecordStageTokens(stage string, input, output int) {
	total := input + output
	b.mu.Lock()
	defer b.mu.Unlock()
	b.stageUsed[stage] += total
	b.totalUsed += total
}

// TotalUsed returns the total tokens consumed across all stages.
func (b *BudgetEnforcer) TotalUsed() int {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.totalUsed
}

// StageUsed returns tokens consumed by a specific stage.
func (b *BudgetEnforcer) StageUsed(stage string) int {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.stageUsed[stage]
}

// SetPerformanceMode updates the active performance mode and the
// disable-ceiling flag for subsequent budget checks (Issue #3217). Safe to
// call from any goroutine — the scheduler invokes this after every Reset()
// at pipeline start. The gate for observe-only is `mode == "maximum" AND
// disableCeiling == true`; setting the flag with any other mode is a no-op
// for the terminate path.
func (b *BudgetEnforcer) SetPerformanceMode(mode string, disableCeiling bool) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.config.PerformanceMode = mode
	b.config.DisableBudgetCeiling = disableCeiling
}

// observeOnlyActive reports whether the current config triggers the
// observe-only branch added in Issue #3217. Caller MUST hold b.mu (or have
// snapshotted the values under the lock).
func (b *BudgetEnforcer) observeOnlyActive() bool {
	return b.config.PerformanceMode == "maximum" && b.config.DisableBudgetCeiling
}

// CheckPipelineBudget checks the total pipeline token budget.
func (b *BudgetEnforcer) CheckPipelineBudget() BudgetDecision {
	b.mu.Lock()
	ceiling := b.config.PipelineCeilingTokens
	mode := b.config.Mode
	perfMode := b.config.PerformanceMode
	gracePercent := b.config.GracePercent
	totalUsed := b.totalUsed
	observeOnly := b.observeOnlyActive()
	b.mu.Unlock()

	if ceiling <= 0 {
		// Even without a ceiling, decisions still tag the active mode so
		// downstream callers / metrics can attribute the no-op to a mode.
		return BudgetDecision{PerformanceMode: perfMode}
	}

	effectiveLimit := ceiling + (ceiling * gracePercent / 100)
	overBase := totalUsed > ceiling
	overEffective := totalUsed > effectiveLimit

	switch mode {
	case "hard":
		if overEffective {
			// Issue #3217: in maximum mode with disableBudgetCeiling, the
			// enforcer runs observe-only — warn for log visibility but never
			// terminate. Reason carries an `_observe_only` suffix so log
			// scrapers can distinguish suppressed terminations from regular
			// warnings.
			if observeOnly {
				return BudgetDecision{
					ShouldWarn:      true,
					UsedTokens:      totalUsed,
					CeilingTokens:   ceiling,
					Reason:          "pipeline_budget_exceeded_observe_only",
					PerformanceMode: perfMode,
				}
			}
			return BudgetDecision{
				ShouldTerminate: true,
				UsedTokens:      totalUsed,
				CeilingTokens:   ceiling,
				Reason:          "pipeline_budget_exceeded",
				PerformanceMode: perfMode,
			}
		}
		if overBase {
			return BudgetDecision{
				ShouldWarn:      true,
				UsedTokens:      totalUsed,
				CeilingTokens:   ceiling,
				Reason:          "pipeline_budget_warning",
				PerformanceMode: perfMode,
			}
		}
	case "soft":
		if overBase {
			return BudgetDecision{
				ShouldWarn:      true,
				UsedTokens:      totalUsed,
				CeilingTokens:   ceiling,
				Reason:          "pipeline_budget_warning",
				PerformanceMode: perfMode,
			}
		}
	}

	return BudgetDecision{PerformanceMode: perfMode}
}

// CheckStageBudget checks the per-stage token budget before running a stage.
func (b *BudgetEnforcer) CheckStageBudget(stage string) BudgetDecision {
	b.mu.Lock()
	ceiling, ok := b.config.PerStageCeilings[stage]
	mode := b.config.Mode
	perfMode := b.config.PerformanceMode
	gracePercent := b.config.GracePercent
	used := b.stageUsed[stage]
	observeOnly := b.observeOnlyActive()
	b.mu.Unlock()

	if !ok || ceiling <= 0 {
		return BudgetDecision{PerformanceMode: perfMode}
	}

	effectiveLimit := ceiling + (ceiling * gracePercent / 100)
	overBase := used > ceiling
	overEffective := used > effectiveLimit

	switch mode {
	case "hard":
		if overEffective {
			if observeOnly {
				return BudgetDecision{
					ShouldWarn:      true,
					UsedTokens:      used,
					CeilingTokens:   ceiling,
					Reason:          "stage_budget_exceeded_observe_only",
					PerformanceMode: perfMode,
				}
			}
			return BudgetDecision{
				ShouldTerminate: true,
				UsedTokens:      used,
				CeilingTokens:   ceiling,
				Reason:          "stage_budget_exceeded",
				PerformanceMode: perfMode,
			}
		}
		if overBase {
			return BudgetDecision{
				ShouldWarn:      true,
				UsedTokens:      used,
				CeilingTokens:   ceiling,
				Reason:          "stage_budget_warning",
				PerformanceMode: perfMode,
			}
		}
	case "soft":
		if overBase {
			return BudgetDecision{
				ShouldWarn:      true,
				UsedTokens:      used,
				CeilingTokens:   ceiling,
				Reason:          "stage_budget_warning",
				PerformanceMode: perfMode,
			}
		}
	}

	return BudgetDecision{PerformanceMode: perfMode}
}

// Reset clears all usage state for a new pipeline run.
func (b *BudgetEnforcer) Reset() {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.totalUsed = 0
	b.stageUsed = make(map[string]int)
}

// BudgetOverrunContext is written by TypeScript when a stage is killed due to
// budget overrun. The Go scheduler reads it to decide whether to retry with
// existing partial work.
//
// See: Issue #2338 - Intelligent budget management
// See: Issue #3666 - shipped_partially / shipped_pr_number for recoverable kills
type BudgetOverrunContext struct {
	// SchemaVersion was bumped to "1.1" with Issue #3666 (added
	// ShippedPartially / ShippedPRNumber). Readers tolerate the older "1.0"
	// shape — the new fields default to zero values and behave like the
	// pre-#3666 path (treat as non-shipped).
	SchemaVersion  string  `json:"schema_version"`
	IssueNumber    int     `json:"issue_number"`
	Stage          string  `json:"stage"`
	EstimatedUSD   float64 `json:"estimated_budget_usd"`
	ActualUSD      float64 `json:"actual_cost_usd"`
	EffectiveLimit float64 `json:"effective_limit_usd"`
	OverrunRatio   float64 `json:"overrun_ratio"`
	WIPCommitted   bool    `json:"wip_committed"`
	WIPBranch      string  `json:"wip_branch"`
	// ShippedPartially is true when the budget killed the stage AFTER its
	// work product shipped (e.g. pr-create successfully opened the PR before
	// the cost cap fired). The scheduler routes through the recoverable
	// failure path — no LifetimeIssueFailures increment, no cascade-breaker
	// contribution, no autonomous pause — and the next dispatch will resume
	// from the next stage (pr-merge for pr-create kills).
	// Issue #3666
	ShippedPartially bool `json:"shipped_partially,omitempty"`
	// ShippedPRNumber identifies the PR the killed stage produced. Zero when
	// ShippedPartially is false. Surfaced in log lines so the autonomous
	// panel / operator can verify the reclassification was justified.
	// Issue #3666
	ShippedPRNumber int    `json:"shipped_pr_number,omitempty"`
	Timestamp       string `json:"timestamp"`
}

// ReadBudgetOverrun reads a budget-overrun-{N}.json file written by TypeScript.
func ReadBudgetOverrun(path string) (*BudgetOverrunContext, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var ctx BudgetOverrunContext
	if err := json.Unmarshal(data, &ctx); err != nil {
		return nil, err
	}
	return &ctx, nil
}
