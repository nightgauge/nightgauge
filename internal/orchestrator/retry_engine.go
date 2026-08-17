package orchestrator

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"sync"

	"github.com/nightgauge/nightgauge/internal/intelligence/routing"
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
//
// ModelLadder derives from the selection query (#581): membership comes from
// the registry (a band with no live anthropic model is no escalation target),
// order from the band ladder, and the frontier exclusion from the
// routing.EscalationCeilingBand policy — replacing the hand-inlined
// ["haiku", "sonnet", "opus"] triplet whose fable exclusion drifted per-site.
// The ladder speaks the band vocabulary because that is the dispatch
// currency (#340); with today's registry it is exactly [haiku sonnet opus].
func DefaultRetryConfig() RetryConfig {
	return RetryConfig{
		MaxBacktracks:          2,
		MaxEscalationsPerStage: 1,
		OscillationDetection:   true,
		ModelLadder:            routing.EscalationLadder("anthropic"),
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
	// downgradeEfforts records the sticky EFFORT substitution that rides a
	// same-model effort descent (#606, the #532 runtime resolution):
	// descentEffortKey(provider, substituted tier) → the effort rung that
	// tier's envelope descends to. Populated ONLY when EvaluateDowngrade
	// accepted a same-model rung on a fully-collapsed provider
	// (DowngradeDecision.SameModelDescent); a cross-model downgrade records no
	// effort, so every non-descent dispatch keeps the unchanged
	// effort-resolution chain. Read by StickyEffort after ApplyDowngrades has
	// rerouted the tier. Cleared by Reset() alongside downgrades.
	//
	// KEYED BY PROVIDER, unlike downgrades (#611). The tier substitution is
	// provider-agnostic on purpose — a rejected band is rejected for the run —
	// but an EFFORT rung belongs to the ladder that produced it. Keyed by tier
	// alone, an xai descent's rung effort became the wire effort of any later
	// stage that resolved to the substituted tier on ANOTHER adapter: a legal
	// EFFORT_LEVELS value with the wrong provenance, so no error and no log.
	downgradeEfforts map[string]string
}

// descentEffortKey is the composite key of the sticky-effort map (#611):
// the provider whose ladder produced the descent, plus the tier it descended
// to. Written by RecordDowngrade from DowngradeDecision.Provider and read by
// StickyEffort from the dispatching adapter's provider — one function so the
// two spellings cannot drift.
func descentEffortKey(provider, tier string) string {
	return provider + "|" + tier
}

// resolveDescentProviderAndID answers "which provider does a dispatch of
// `model` execute on, and what concrete registry id serves it" — the single
// resolution shared by EvaluateDowngradeForProvider (which KEYS a descent) and
// StickyEffort (which READS it), so the write key and the read key are
// computed the same way by construction (#611).
//
// A bare registry band cannot name its provider (#340), so an empty hint means
// the historical anthropic inference. A concrete id names its own provider and
// overrides the hint. The id is "" when the registry does not know the model —
// user-defined local models, which no ladder descends.
func resolveDescentProviderAndID(provider, model string) (string, string) {
	if provider == "" {
		provider = "anthropic"
	}
	if desc, ok := models.Resolve(provider, model); ok {
		return desc.Provider, desc.ID
	}
	return provider, ""
}

// scopeDescentProvider applies the #606 judgment call to a resolved provider:
// only xai's ladder is threaded through the descent machinery; every other
// provider — and every unknown one — collapses to "", which
// resolveDescentProviderAndID reads as the historical anthropic inference.
//
// xai is the fully-collapsed provider whose effort rungs ARE its downgrade
// ladder (#532). Threading every provider through here would change unpinned
// downgrade outcomes for providers whose bands the anthropic inference happens
// to serve — TestDowngradeDescent_NonXaiAdaptersKeepEveryGoldenCell shows the
// drift that produces. One predicate so the two evidence sources below cannot
// scope it differently.
func scopeDescentProvider(provider string) string {
	if provider == "xai" {
		return "xai"
	}
	return ""
}

// DowngradeProviderForAdapter maps the adapter Go is ACTIVELY executing a
// dispatch on — execMgr's adapter, after applyStageAdapter re-pointed it for
// this stage — onto the provider hint the descent machinery keys on (#611).
//
// Go-direct path only. In IPC mode Go holds no adapter (SchedulerConfig.Adapter
// is nil by construction) and the extension owns per-stage selection, so there
// is nothing here to answer with; see DowngradeProviderForServedModel for the
// evidence that path actually has.
func DowngradeProviderForAdapter(adapter string) string {
	if adapter == "" {
		return ""
	}
	return scopeDescentProvider(models.ProviderForAdapter(adapter))
}

// DowngradeProviderForServedModel maps the CONCRETE model id an adapter process
// was actually spawned with — StageResultParams.ServedModel, read out of the
// adapter's own env after model preflight (#91/#340) — onto the same provider
// hint (#611).
//
// This is the IPC path's answer to "which provider is executing", and it is
// EVIDENCE rather than inference. Go cannot re-derive the extension's adapter:
// resolveStageAdapter (adapterResolver.ts) has rungs config.ResolveStageAdapter
// does not — a ConfigBridge-sourced global, the AutoProviderRouter, a hardcoded
// default — lacks the NIGHTGAUGE_ADAPTER rung Go has, and walkAdapterFallback
// can replace the whole decision at stage start when prereq validation fails.
// A Go-side mirror of that chain is a second authority that disagrees in both
// directions: silently blind for an auto-router-selected grok, and actively
// WRONG (an xai rung applied to a claude dispatch — the #611 finding-1 bleed,
// re-sourced) whenever the fallback walk hops away from the configured adapter.
// The served id is reported by the process that ran, after every one of those
// decisions, so it cannot disagree with what executed.
//
// "" when nothing was reported (the extension omits the field when the served
// id is byte-identical to the requested band — nothing to add) or when the
// registry does not know the id (user-defined local models, which no ladder
// descends). Both keep the historical anthropic inference.
func DowngradeProviderForServedModel(servedModel string) string {
	if strings.TrimSpace(servedModel) == "" {
		return ""
	}
	provider, id := resolveDescentProviderAndID("", servedModel)
	if id == "" {
		return ""
	}
	return scopeDescentProvider(provider)
}

// NewRetryEngine creates a new retry engine with the given config.
func NewRetryEngine(cfg RetryConfig) *RetryEngine {
	return &RetryEngine{
		config:           cfg,
		escalations:      make(map[string]int),
		traversedEdges:   make(map[string]bool),
		currentModels:    make(map[string]string),
		conflictEdges:    make(map[string]int),
		downgrades:       make(map[string]string),
		downgradeEfforts: make(map[string]string),
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
// The ladder is routing.TierBandsStrongestFirst, not a second copy of it: the
// performance-mode envelopes clamp against the same ordering, and two
// declarations of one ladder is how a band ends up strong in one file and weak
// in another (#340).
var downgradeLadder = routing.TierBandsStrongestFirst

// NormalizeModelTier maps a model reference (registry tier name like "opus", or a
// concrete ID like "claude-opus-4-8" / "gpt-5.5") onto its strongest registry
// band. Returns "" when the model is unknown to the registry — user-defined
// local models are never downgraded by this ladder.
func NormalizeModelTier(model string) string {
	return routing.TierBand(model)
}

// DowngradeDecision is the result of EvaluateDowngrade.
//
// NewModelID/NewEffort carry the dispatch ENVELOPE of the accepted rung
// (#606, spike #568 §4.1: the ladder is envelope-valued, so a downgrade
// decision names an envelope point, not just a band). On a cross-model
// downgrade they are attribution only; on a SameModelDescent they are the
// decision itself — the same model re-dispatched one declared effort rung
// lower, which is the only downgrade a fully-collapsed provider has (#532).
type DowngradeDecision struct {
	ShouldDowngrade bool
	FromTier        string
	NewTier         string // registry tier name — resolved to the current model by models.Get at run time
	// NewModelID is the concrete registry id serving NewTier's rung, and
	// NewEffort that rung's declared effort ("" when the rung declares none).
	NewModelID string
	NewEffort  string
	// SameModelDescent is true when NewTier is served by the REJECTED model
	// itself at a lower declared effort — the fully-collapsed-provider descent
	// (#606). Only then does the rung's effort become a sticky substitution
	// (see DescentEffort / RecordDowngrade).
	SameModelDescent bool
	// Provider is the provider whose candidate ladder produced this decision —
	// the caller's hint resolved through the registry (a concrete rejected id
	// names its own provider; a bare band falls back to the historical
	// anthropic inference). It is the SCOPE of the sticky effort half (#611):
	// RecordDowngrade keys DescentEffort() under it so a descent recorded on
	// one provider can never become another provider's wire effort. Empty only
	// when the rejected model is not in the registry at all.
	Provider string
	Reason   string
}

// DescentEffort returns the effort to record as the sticky substitution for
// NewTier: the rung effort on a same-model descent, "" otherwise. Cross-model
// downgrades deliberately record no effort — their effort resolution is the
// unchanged #581 chain, and recording the rung's declared effort would
// silently rewrite the wire effort of every post-downgrade stage.
func (d DowngradeDecision) DescentEffort() string {
	if d.SameModelDescent {
		return d.NewEffort
	}
	return ""
}

// EvaluateDowngrade resolves the next-best model tier below the rejected
// model (#42). Walks the provider's candidate-ladder RUNGS (#581 — the
// selection query's envelope-valued ladder, routing.ResolveBandEnvelope)
// below the rejected tier, skipping any tier this run has already recorded
// as rejected, any band the provider has no rung for, and any rung served by
// the rejected model itself. Returns ShouldDowngrade=false when the rejected
// model is not in the registry (user-defined local models: one-rung ladder,
// no fallback) or the ladder is exhausted — nothing weaker exists for that
// provider.
//
// SAME-MODEL RUNGS — the #532 xai case, resolved by #606: on a provider whose
// bands FULLY collapse onto one model (every ladder rung one model id — xai
// today), the rungs below the rejected tier are the SAME model at lower
// declared efforts (grok-4.6@xhigh → high → …), which spike #568 §4.1 names
// as the real downgrade ladder the band vocabulary could not express. The
// walk now ACCEPTS those rungs for fully-collapsed providers — the
// effort-execution contract reaches the grok adapters since #606 (wire effort
// on the TS path, RunOptions-threaded effort on the Go-direct path, with
// NIGHTGAUGE_GROK_EFFORT demoted to operator override), so the descended
// effort actually dispatches; the substitution is made sticky by
// RecordDowngrade's effort half and applied by StickyEffort.
//
// PARTIALLY-collapsed providers (google's pro/flash pairs, openai's
// gpt-5.6-sol) KEEP the same-model skip, pinned by
// TestEvaluateDowngrade_PartiallyCollapsedProviderKeepsSameModelSkip: their
// same-model rungs are the SYNTHESIZED effort points the PROVENANCE note on
// routing.CandidateLadder flags — points no registry field declares as a
// band's serving envelope — and those providers have a REAL weaker model to
// fall to, which stays the honest downgrade.
func (r *RetryEngine) EvaluateDowngrade(rejectedModel string) DowngradeDecision {
	return r.EvaluateDowngradeForProvider(rejectedModel, "")
}

// providerFullyCollapsed reports whether every rung of the provider's
// candidate ladder is served by one model id — the precondition for treating
// same-model effort rungs as downgrade targets (#606). An empty ladder
// (local providers) is not "collapsed": there is nothing to descend through.
func providerFullyCollapsed(provider string) bool {
	rungs := routing.CandidateLadder(provider, "")
	if len(rungs) == 0 {
		return false
	}
	for _, rung := range rungs[1:] {
		if rung.ModelID != rungs[0].ModelID {
			return false
		}
	}
	return true
}

// EvaluateDowngradeForProvider is EvaluateDowngrade with the dispatching
// provider made explicit (#606). The dispatch model is a registry BAND on the
// wire (#340), and a band alone cannot name its provider — the inference
// below resolves bands against anthropic, exactly as before. A caller that
// KNOWS the dispatch provider (the Go-direct adapter path, where the
// scheduler holds the adapter) passes it so an xai rejection of band
// "sonnet" walks the xai ladder — grok-4.6's effort rungs — instead of the
// anthropic one. provider "" preserves the historical inference bit for bit.
func (r *RetryEngine) EvaluateDowngradeForProvider(rejectedModel, provider string) DowngradeDecision {
	fromTier := NormalizeModelTier(rejectedModel)
	if fromTier == "" {
		return DowngradeDecision{Reason: "model_not_in_registry"}
	}
	provider, rejectedID := resolveDescentProviderAndID(provider, rejectedModel)

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
		return DowngradeDecision{FromTier: fromTier, Provider: provider, Reason: "tier_not_in_ladder"}
	}
	for _, tier := range downgradeLadder[start+1:] {
		if _, rejected := r.downgrades[tier]; rejected {
			continue // this tier was itself rejected earlier in the run
		}
		rung, ok := routing.ResolveBandEnvelope(provider, tier, "")
		if !ok {
			// No rung: either no live model serves the band for this
			// provider, or the band's multi-band model cannot descend to a
			// distinct envelope (a duplicate rung would re-create the #532
			// "downgrade is a no-op" lie) — no downgrade target either way.
			continue
		}
		if rung.ModelID == rejectedID {
			// Same model serves this rung — see SAME-MODEL RUNGS above.
			// Fully-collapsed provider with a real (distinct, declared)
			// effort on the rung: this IS the provider's downgrade ladder.
			if !providerFullyCollapsed(provider) || rung.Effort == "" {
				continue // partially-collapsed keeps the skip (pinned decision, #606)
			}
			return DowngradeDecision{
				ShouldDowngrade:  true,
				FromTier:         fromTier,
				NewTier:          tier,
				NewModelID:       rung.ModelID,
				NewEffort:        rung.Effort,
				SameModelDescent: true,
				Provider:         provider,
				Reason:           "model_unavailable_effort_descent",
			}
		}
		return DowngradeDecision{
			ShouldDowngrade: true,
			FromTier:        fromTier,
			NewTier:         tier,
			NewModelID:      rung.ModelID,
			NewEffort:       rung.Effort,
			Provider:        provider,
			Reason:          "model_unavailable_fallback",
		}
	}
	return DowngradeDecision{FromTier: fromTier, Provider: provider, Reason: "downgrade_ladder_exhausted"}
}

// RecordDowngrade makes a model-tier substitution sticky for the remainder of
// the run: every subsequent stage that resolves to the rejected tier is
// rerouted to dg.NewTier by ApplyDowngrades. Cleared by Reset().
//
// dg is the decision that PRODUCED the substitution, not three loose strings
// (#611). Its DescentEffort() is the sticky effort half of a same-model
// descent (#606) — "" for every cross-model downgrade, so the wire-effort
// chain stays untouched there — and its Provider is the scope that effort is
// recorded under. The decision carries the tier, the effort and the provider
// from ONE evaluation, so a caller cannot record a rung's effort against a
// provider that never produced it.
func (r *RetryEngine) RecordDowngrade(rejectedModel string, dg DowngradeDecision) {
	fromTier := NormalizeModelTier(rejectedModel)
	if fromTier == "" || dg.NewTier == "" || fromTier == dg.NewTier {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.downgrades[fromTier] = dg.NewTier
	if effort := dg.DescentEffort(); effort != "" {
		r.downgradeEfforts[descentEffortKey(dg.Provider, dg.NewTier)] = effort
	}
}

// StickyEffort returns the effort rung a same-model descent (#606) recorded
// for the tier a dispatch RESOLVED to (i.e. after ApplyDowngrades), or ""
// when no descent on THIS dispatch's provider touched that tier. The caller
// substitutes it for the resolved wire effort, LAST — after the mode's effort
// clamps — for the same reason the model floor is never re-applied over a
// sticky downgrade (#42): re-raising the effort the API-rejection descent just
// lowered would re-fail identically. The NIGHTGAUGE_GROK_EFFORT operator
// override still wins at the adapter boundary; an operator pin is an explicit
// choice, not a pipeline one.
//
// provider is the hint for the provider this dispatch will EXECUTE on —
// DowngradeProviderForAdapter on the Go-direct path, DowngradeProviderForServedModel
// on the IPC path. It is what keeps a descent inside the ladder that produced
// it (#611): a mixed-adapter run records xai's rung under xai, and a later
// claude stage resolving to the same substituted tier reads its own (absent)
// entry and keeps the wire effort its own envelope resolved.
//
// "" — a dispatch whose provider is not known first-hand — resolves to the
// anthropic inference and therefore reads no xai rung. That is the deliberate
// safe direction: applying a provider-scoped rung to a dispatch nobody can
// name IS the bleed this key exists to stop.
func (r *RetryEngine) StickyEffort(provider, model string) string {
	tier := NormalizeModelTier(model)
	if tier == "" {
		return ""
	}
	dispatchProvider, _ := resolveDescentProviderAndID(provider, model)
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.downgradeEfforts[descentEffortKey(dispatchProvider, tier)]
}

// ApplyDowngrades reroutes a model through the run's sticky tier
// substitutions (#42). Follows the chain (fable→opus recorded, then opus
// rejected too → opus→sonnet ⇒ fable resolves to sonnet), bounded by the
// ladder length. Models unknown to the registry pass through unchanged.
// Returns the substituted TIER NAME (the scheduler ladder vocabulary, which
// the Claude CLI accepts as a model alias) or the original model when no
// substitution applies.
func (r *RetryEngine) ApplyDowngrades(model string) string {
	tier := NormalizeModelTier(model)
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
	r.downgradeEfforts = make(map[string]string)
}
