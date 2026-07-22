package orchestrator

import (
	"encoding/json"
	"fmt"
	"os"
	"sync"

	"github.com/nightgauge/nightgauge/internal/models"
	"github.com/nightgauge/nightgauge/internal/state"
)

// RetryConfig configures the retry engine behavior.
type RetryConfig struct {
	MaxBacktracks          int      // Max total backtracks per pipeline run (default: 2)
	MaxEscalationsPerStage int      // Max model escalations per stage (default: 1)
	OscillationDetection   bool     // Detect and block A->B->A oscillation (default: true)
	ModelLadder            []string // Ordered model names ["haiku", "sonnet", "opus"]
	// MaxConflictRedispatch bounds CONFLICT_RESOLUTION_NEEDED rewinds per edge
	// (conflict-recovery's pr-merge→feature-dev loop, #4072). This edge is
	// DELIBERATELY repeated to resolve a rebase conflict, so it uses a per-edge
	// COUNT limit instead of the open-ended oscillation block + global
	// MaxBacktracks, which would otherwise cap it at a single re-dispatch.
	MaxConflictRedispatch int // default: 2
}

// DefaultRetryConfig returns safe default retry configuration.
func DefaultRetryConfig() RetryConfig {
	return RetryConfig{
		MaxBacktracks:          2,
		MaxEscalationsPerStage: 1,
		OscillationDetection:   true,
		ModelLadder:            []string{"haiku", "sonnet", "opus"},
		MaxConflictRedispatch:  2,
	}
}

// conflictResolutionSignal is the feedback signal type the conflict-recovery loop
// emits; the RetryEngine treats its edge with a per-edge count limit (#4072).
const conflictResolutionSignal = "CONFLICT_RESOLUTION_NEEDED"

// RetryEngine evaluates backtrack and model escalation decisions.
//
// Safe for concurrent use: parallel-wave subagents share a single Scheduler
// (and therefore a single RetryEngine), and would otherwise race on the
// escalations / traversedEdges / currentModels maps and the backtrackCount
// counter. See issue #3198.
type RetryEngine struct {
	mu             sync.Mutex
	config         RetryConfig
	backtrackCount int
	escalations    map[string]int    // per-stage escalation count
	traversedEdges map[string]bool   // "from->to" edges for oscillation detection
	currentModels  map[string]string // current model per stage
	// conflictEdges counts CONFLICT_RESOLUTION_NEEDED traversals per edge — these
	// are bounded by MaxConflictRedispatch, NOT the oscillation/global guard.
	conflictEdges map[string]int
	// downgrades records sticky per-RUN model-tier substitutions applied when
	// the API rejects a model (model_unavailable, #42): rejected tier → the
	// tier that replaced it. PER-RUN (not per-stage, unlike escalations): once
	// a plan refuses a model, re-attempting it on every subsequent stage would
	// re-fail identically. Cleared by Reset() so the next run re-attempts the
	// originally-requested model (caps reset; plans change).
	downgrades map[string]string
}

// NewRetryEngine creates a new retry engine with the given config.
func NewRetryEngine(cfg RetryConfig) *RetryEngine {
	return &RetryEngine{
		config:         cfg,
		escalations:    make(map[string]int),
		traversedEdges: make(map[string]bool),
		currentModels:  make(map[string]string),
		conflictEdges:  make(map[string]int),
		downgrades:     make(map[string]string),
	}
}

// downgradeLadder is the tier-fallback order applied when the API rejects a
// model (#42): strongest → weakest, the downward counterpart of the
// escalation ModelLadder. Expressed as REGISTRY TIER BANDS — never dated
// model IDs — and each rung is resolved through models.Resolve(provider,
// tier) at decision time, so the ladder is provider-relative (#56): a codex
// gpt-5.5 rejection falls to gpt-5.4 → gpt-5.4-mini, a Claude rejection
// walks the Anthropic models, and local providers (no registry entries)
// have a one-rung ladder — no fallback, the failure surfaces with
// remediation instead.
var downgradeLadder = []string{"fable", "opus", "sonnet", "haiku"}

// normalizeTier maps a model reference (registry tier name like "opus", or a
// concrete ID like "claude-opus-4-8" / "gpt-5.5") onto its strongest registry
// band. Returns "" when the model is unknown to the registry — user-defined
// local models are never downgraded by this ladder.
func normalizeTier(model string) string {
	for _, tier := range downgradeLadder {
		if model == tier {
			return tier
		}
	}
	if desc, ok := models.Get(model); ok {
		for _, tier := range downgradeLadder {
			if desc.HasTier(tier) {
				return tier
			}
		}
	}
	return ""
}

// DowngradeDecision is the result of EvaluateDowngrade.
type DowngradeDecision struct {
	ShouldDowngrade bool
	FromTier        string
	NewTier         string // registry tier name — resolved to the current model by models.Get at run time
	Reason          string
}

// EvaluateDowngrade resolves the next-best model tier below the rejected
// model (#42). Walks the fable → opus → sonnet → haiku ladder WITHIN the
// rejected model's provider (#56), skipping any tier this run has already
// recorded as rejected, any band the provider has no live model for, and any
// band served by the rejected model itself (a multi-band model like gpt-5.5
// [opus+fable] or gemini-2.5-flash [haiku+sonnet] must not "fall" to
// itself). Returns ShouldDowngrade=false when the rejected model is not in
// the registry (user-defined local models: one-rung ladder, no fallback) or
// the ladder is exhausted — nothing weaker exists for that provider.
func (r *RetryEngine) EvaluateDowngrade(rejectedModel string) DowngradeDecision {
	fromTier := normalizeTier(rejectedModel)
	if fromTier == "" {
		return DowngradeDecision{Reason: "model_not_in_registry"}
	}
	provider := "anthropic"
	rejectedID := ""
	if desc, ok := models.Get(rejectedModel); ok {
		provider = desc.Provider
		rejectedID = desc.ID
	}

	r.mu.Lock()
	defer r.mu.Unlock()

	start := -1
	for i, tier := range downgradeLadder {
		if tier == fromTier {
			start = i
			break
		}
	}
	if start == -1 {
		return DowngradeDecision{FromTier: fromTier, Reason: "tier_not_in_ladder"}
	}
	for _, tier := range downgradeLadder[start+1:] {
		if _, rejected := r.downgrades[tier]; rejected {
			continue // this tier was itself rejected earlier in the run
		}
		desc, ok := models.Resolve(provider, tier)
		if !ok {
			continue // no live model for this provider band
		}
		if desc.ID == rejectedID {
			continue // same model serves this band too — not a downgrade
		}
		return DowngradeDecision{
			ShouldDowngrade: true,
			FromTier:        fromTier,
			NewTier:         tier,
			Reason:          "model_unavailable_fallback",
		}
	}
	return DowngradeDecision{FromTier: fromTier, Reason: "downgrade_ladder_exhausted"}
}

// RecordDowngrade makes a model-tier substitution sticky for the remainder of
// the run: every subsequent stage that resolves to the rejected tier is
// rerouted to newTier by ApplyDowngrades. Cleared by Reset().
func (r *RetryEngine) RecordDowngrade(rejectedModel, newTier string) {
	fromTier := normalizeTier(rejectedModel)
	if fromTier == "" || newTier == "" || fromTier == newTier {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.downgrades[fromTier] = newTier
}

// ApplyDowngrades reroutes a model through the run's sticky tier
// substitutions (#42). Follows the chain (fable→opus recorded, then opus
// rejected too → opus→sonnet ⇒ fable resolves to sonnet), bounded by the
// ladder length. Models unknown to the registry pass through unchanged.
// Returns the substituted TIER NAME (the scheduler ladder vocabulary, which
// the Claude CLI accepts as a model alias) or the original model when no
// substitution applies.
func (r *RetryEngine) ApplyDowngrades(model string) string {
	tier := normalizeTier(model)
	if tier == "" {
		return model
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if len(r.downgrades) == 0 {
		return model
	}
	current := tier
	for range downgradeLadder { // bound chain-following to ladder length
		next, ok := r.downgrades[current]
		if !ok {
			break
		}
		current = next
	}
	if current == tier {
		return model
	}
	return current
}

// Downgrades returns a copy of the run's sticky tier substitutions
// (rejected tier → substituted tier), for telemetry and notifications.
func (r *RetryEngine) Downgrades() map[string]string {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make(map[string]string, len(r.downgrades))
	for k, v := range r.downgrades {
		out[k] = v
	}
	return out
}

// BacktrackDecision is the result of EvaluateBacktrack.
type BacktrackDecision struct {
	ShouldBacktrack    bool
	TargetStage        state.PipelineStage
	SignalType         string
	Rationale          string
	OscillationBlocked bool
	LimitReached       bool
}

// EscalationDecision is the result of EvaluateEscalation.
type EscalationDecision struct {
	ShouldEscalate bool
	NewModel       string
	Reason         string
	LimitReached   bool
}

// FeedbackSignal represents a feedback signal from a stage context file.
type FeedbackSignal struct {
	SignalType           string   `json:"signal_type"`
	EmittedByStage       string   `json:"emitted_by_stage"`
	BacktrackTargetStage string   `json:"backtrack_target_stage,omitempty"`
	Rationale            string   `json:"rationale"`
	Evidence             []string `json:"evidence"`
	Severity             string   `json:"severity"` // "blocking" or "warning"
}

// FeedbackContext is the structure of a feedback-N.json file.
type FeedbackContext struct {
	SchemaVersion string           `json:"schema_version"`
	IssueNumber   int              `json:"issue_number"`
	Signals       []FeedbackSignal `json:"signals"`
}

// EvaluateBacktrack reads the feedback context file and decides whether to
// backtrack on a GENERIC revision signal (feature-validate → feature-dev, stall
// rewind, etc.). CONFLICT_RESOLUTION_NEEDED signals are deliberately SKIPPED
// here — that edge is owned exclusively by the recovery-resume path, which calls
// EvaluateConflictBacktrack. Letting the generic post-stage "stage succeeded"
// and stall sites consume the lingering conflict signal made them re-rewind
// feature-dev → feature-dev on a mismatched edge key and never terminate (#4072).
func (r *RetryEngine) EvaluateBacktrack(feedbackFile string) (BacktrackDecision, error) {
	return r.evaluateBacktrack(feedbackFile, false)
}

// EvaluateConflictBacktrack handles ONLY the conflict-recovery edge
// (CONFLICT_RESOLUTION_NEEDED): the deliberately-repeated pr-merge → feature-dev
// rebase-resolution loop. It is bounded by a PER-EDGE count
// (MaxConflictRedispatch) rather than the oscillation block + global
// MaxBacktracks, which would otherwise cap the loop at a single re-dispatch and
// make the configured max_dev_redispatch bound dead (#4072 review). Called from
// the recovery-resume path after conflict-recovery-loop defers; that path always
// runs with stage == pr-merge, so the RecordBacktrack edge key matches the check
// key here ("pr-merge->feature-dev").
func (r *RetryEngine) EvaluateConflictBacktrack(feedbackFile string) (BacktrackDecision, error) {
	return r.evaluateBacktrack(feedbackFile, true)
}

// evaluateBacktrack is the shared reader. conflictMode selects which signal
// family this caller owns: the generic path (false) skips conflict signals and
// applies the oscillation + global-budget guard; the conflict path (true)
// handles only conflict signals under the per-edge count bound.
func (r *RetryEngine) evaluateBacktrack(feedbackFile string, conflictMode bool) (BacktrackDecision, error) {
	data, err := os.ReadFile(feedbackFile)
	if err != nil {
		if os.IsNotExist(err) {
			return BacktrackDecision{}, nil // No feedback file = no backtrack
		}
		return BacktrackDecision{}, fmt.Errorf("read feedback file: %w", err)
	}

	var ctx FeedbackContext
	if err := json.Unmarshal(data, &ctx); err != nil {
		return BacktrackDecision{}, fmt.Errorf("parse feedback: %w", err)
	}

	// Find first blocking signal with a backtrack target (excluding MODEL_ESCALATION_NEEDED)
	for _, signal := range ctx.Signals {
		if signal.Severity != "blocking" {
			continue
		}
		if signal.BacktrackTargetStage == "" {
			continue
		}
		if signal.SignalType == "MODEL_ESCALATION_NEEDED" {
			continue
		}

		isConflict := signal.SignalType == conflictResolutionSignal
		// Each path owns exactly one signal family: skip the other so the conflict
		// edge is never consumed by a generic rewind site (and vice versa).
		if isConflict != conflictMode {
			continue
		}

		targetStage := state.PipelineStage(signal.BacktrackTargetStage)
		edgeKey := fmt.Sprintf("%s->%s", signal.EmittedByStage, signal.BacktrackTargetStage)

		r.mu.Lock()
		// Conflict-recovery edge: a deliberately-repeated rebase-conflict resolution
		// loop. Bound it by a PER-EDGE count (MaxConflictRedispatch) instead of the
		// oscillation block + global MaxBacktracks, which would cap it at one
		// re-dispatch and make the configured bound dead (#4072 review).
		if isConflict {
			limit := r.config.MaxConflictRedispatch
			if limit <= 0 {
				limit = 2
			}
			if r.conflictEdges[edgeKey] >= limit {
				r.mu.Unlock()
				return BacktrackDecision{
					ShouldBacktrack: false,
					TargetStage:     targetStage,
					SignalType:      signal.SignalType,
					Rationale:       signal.Rationale,
					LimitReached:    true,
				}, nil
			}
			r.mu.Unlock()
			return BacktrackDecision{
				ShouldBacktrack: true,
				TargetStage:     targetStage,
				SignalType:      signal.SignalType,
				Rationale:       signal.Rationale,
			}, nil
		}

		// Check hard limit
		if r.backtrackCount >= r.config.MaxBacktracks {
			r.mu.Unlock()
			return BacktrackDecision{
				ShouldBacktrack: false,
				TargetStage:     targetStage,
				SignalType:      signal.SignalType,
				Rationale:       signal.Rationale,
				LimitReached:    true,
			}, nil
		}

		// Check oscillation guard
		if r.config.OscillationDetection {
			if r.traversedEdges[edgeKey] {
				r.mu.Unlock()
				return BacktrackDecision{
					ShouldBacktrack:    false,
					TargetStage:        targetStage,
					SignalType:         signal.SignalType,
					Rationale:          signal.Rationale,
					OscillationBlocked: true,
				}, nil
			}
		}
		r.mu.Unlock()

		return BacktrackDecision{
			ShouldBacktrack: true,
			TargetStage:     targetStage,
			SignalType:      signal.SignalType,
			Rationale:       signal.Rationale,
		}, nil
	}

	return BacktrackDecision{}, nil
}

// RecordBacktrack records that a backtrack was executed. signalType lets the
// conflict-recovery edge use its own per-edge counter (bounded by
// MaxConflictRedispatch) instead of the oscillation set + global backtrack
// budget, so a deliberately-repeated conflict loop isn't blocked after one
// re-dispatch (#4072).
func (r *RetryEngine) RecordBacktrack(fromStage, toStage, signalType string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	edgeKey := fmt.Sprintf("%s->%s", fromStage, toStage)
	if signalType == conflictResolutionSignal {
		r.conflictEdges[edgeKey]++
		return
	}
	r.backtrackCount++
	r.traversedEdges[edgeKey] = true
}

// BacktrackCount returns the number of backtracks executed so far.
func (r *RetryEngine) BacktrackCount() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.backtrackCount
}

// EvaluateEscalation checks if model escalation is warranted based on a feedback signal.
func (r *RetryEngine) EvaluateEscalation(stage string, currentModel string) EscalationDecision {
	// Check per-stage limit
	r.mu.Lock()
	count := r.escalations[stage]
	r.mu.Unlock()
	if count >= r.config.MaxEscalationsPerStage {
		return EscalationDecision{
			ShouldEscalate: false,
			Reason:         "max_escalations_per_stage_exceeded",
			LimitReached:   true,
		}
	}

	// Find next model in ladder
	nextModel, ok := r.NextModel(currentModel)
	if !ok {
		return EscalationDecision{
			ShouldEscalate: false,
			Reason:         "escalation_ceiling_reached",
			LimitReached:   true,
		}
	}

	return EscalationDecision{
		ShouldEscalate: true,
		NewModel:       nextModel,
		Reason:         "escalation_available",
	}
}

// RecordEscalation records that an escalation was applied to a stage.
func (r *RetryEngine) RecordEscalation(stage, newModel string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.escalations[stage]++
	r.currentModels[stage] = newModel
}

// CurrentModel returns the current model for a stage, or empty string if not overridden.
func (r *RetryEngine) CurrentModel(stage string) string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.currentModels[stage]
}

// NextModel returns the next model in the escalation ladder.
// Returns the model and true if found, or empty string and false if at ceiling.
// If current model is empty or not found in the ladder, returns the first rung
// (typically "sonnet" for an unknown starting point, since haiku is stage-default
// for pr-create/pr-merge and sonnet is the general default).
func (r *RetryEngine) NextModel(current string) (string, bool) {
	ladder := r.config.ModelLadder
	if len(ladder) == 0 {
		return "", false
	}

	// If current model is empty or not in ladder, start from the second rung.
	// This handles the common case where predictedModel is unset ("") and the
	// stage was running with the default model (sonnet). Escalate to opus.
	if current == "" {
		// Default assumption: unknown model ≈ sonnet (the general default).
		// Return the model after sonnet in the ladder.
		for i, m := range ladder {
			if m == "sonnet" && i+1 < len(ladder) {
				return ladder[i+1], true
			}
		}
		// No sonnet in ladder — return the last rung as ceiling attempt
		return ladder[len(ladder)-1], true
	}

	for i, m := range ladder {
		if m == current && i+1 < len(ladder) {
			return ladder[i+1], true
		}
	}
	return "", false
}

// Reset clears all state for a new pipeline run. Every per-run counter MUST be
// cleared here — the RetryEngine is constructed once per Scheduler and reused for
// every issue, so a missed map leaks budget across runs. conflictEdges in
// particular uses a non-issue-scoped edge key ("pr-merge->feature-dev"), so a
// stale entry would silently deny a later issue its first conflict re-dispatch
// (#4072 review).
func (r *RetryEngine) Reset() {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.backtrackCount = 0
	r.escalations = make(map[string]int)
	r.traversedEdges = make(map[string]bool)
	r.currentModels = make(map[string]string)
	r.conflictEdges = make(map[string]int)
	r.downgrades = make(map[string]string)
}
