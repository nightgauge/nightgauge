// Package trace persists the per-run lifecycle decision trace: one durable,
// ordered JSONL stream per pipeline run capturing every stage boundary and
// every decision the pipeline made — with its rationale and the alternatives
// it rejected. See docs/decisions/013-run-lifecycle-trace-schema.md (ADR 013)
// for the schema contract, ordering rules, and upload/idempotency semantics.
//
// The trace is the source of record for DECISIONS. Existing stores remain the
// source of record for outcomes and forensics: the V3 RunRecord (history
// JSONL), stage-exit records, and gate metrics. Trace events reference those
// stores via the shared run_id join key (UUID v7 from runstate) instead of
// embedding them — the non-duplication rule in ADR 013.
//
// Writes are fail-open by design: a trace-write failure must never fail a
// pipeline stage. The Writer is nil-safe so call sites can emit
// unconditionally without guarding.
package trace

// SchemaVersion is the trace event envelope version. Bumped only on breaking
// envelope changes; payloads evolve additively under the same version.
const SchemaVersion = 1

// Producer identifies which writer emitted an event. The Go binary and the
// TypeScript SDK interleave events into the same per-run file; seq values are
// monotonic per producer, and total ordering is (ts, producer, seq) — see
// ADR 013 §Ordering.
const (
	ProducerGo  = "go"
	ProducerSDK = "sdk"
)

// Kind is the closed decision/boundary taxonomy for trace events (ADR 013).
// Every kind is produced by at least one emitter and rendered by the
// Lifecycle Explorer — no producer without a consumer.
type Kind string

const (
	// KindStageStart marks a pipeline stage beginning execution.
	KindStageStart Kind = "stage_start"
	// KindStageExit marks a stage exit (success or failure). Summary fields
	// only — full forensics live in the exit-records store, joined by run_id.
	KindStageExit Kind = "stage_exit"
	// KindPhaseTransition is a phase-marker transition inside a stage.
	// Emitted by the SDK producer (wave 2 of the trace epic).
	KindPhaseTransition Kind = "phase_transition"
	// KindModelRouting is a model routing decision with reasoning and
	// rejected alternatives.
	KindModelRouting Kind = "model_routing"
	// KindChangeClass is the deterministic change-class / fast-track routing
	// resolution, including which rule matched and which stages it skips.
	KindChangeClass Kind = "change_class"
	// KindStageSkip records one stage being skipped, attributed to the
	// decision that removed it.
	KindStageSkip Kind = "stage_skip"
	// KindComplexityEscalation is a model change decision mid-run: escalation
	// up the ladder, or a downgrade/fallback when the API rejects a tier.
	KindComplexityEscalation Kind = "complexity_escalation"
	// KindBacktrack is a rewind decision to an earlier stage, with the
	// feedback signal and rationale that triggered it.
	KindBacktrack Kind = "backtrack"
	// KindRecoveryRetry is one FailureRecovery registry attempt (deterministic
	// auto-triage) with its action, outcome, and reason.
	KindRecoveryRetry Kind = "recovery_retry"
	// KindGateResult is a gate outcome: a stage post-condition gate or a
	// quality gate (build/lint/test).
	KindGateResult Kind = "gate_result"
	// KindOutcome is the terminal run outcome event.
	KindOutcome Kind = "outcome"
	// KindDecisionRequest records an Action Center DecisionRequest terminal
	// transition (resolution or expiry) for a run-scoped request (ADR 015 §H).
	// It closes the loop: the node that raised the request and the node that
	// records its resolution are joined by the DecisionRequest id.
	KindDecisionRequest Kind = "decision_request"
)

// allKinds is the canonical set used for validation.
var allKinds = []Kind{
	KindStageStart,
	KindStageExit,
	KindPhaseTransition,
	KindModelRouting,
	KindChangeClass,
	KindStageSkip,
	KindComplexityEscalation,
	KindBacktrack,
	KindRecoveryRetry,
	KindGateResult,
	KindOutcome,
	KindDecisionRequest,
}

// AllKinds returns a copy of the valid kind set for CLI help/validation.
func AllKinds() []string {
	out := make([]string, len(allKinds))
	for i, k := range allKinds {
		out[i] = string(k)
	}
	return out
}

// IsValidKind reports whether k is one of the declared kind constants.
func IsValidKind(k Kind) bool {
	for _, candidate := range allKinds {
		if candidate == k {
			return true
		}
	}
	return false
}

// Event is the trace event envelope (ADR 013). Every line in a per-run trace
// JSONL is one Event. JSON tags are contract: renames or removals are
// breaking; additive fields must use omitempty.
type Event struct {
	SchemaVersion int `json:"schema_version"`
	// RunID is the UUID v7 join key shared with run-state, exit records, and
	// the V3 RunRecord.
	RunID string `json:"run_id"`
	// Repo is the "owner/name" of the repository the run executes against.
	Repo string `json:"repo,omitempty"`
	// Issue is the GitHub issue number the run is dispatched for.
	Issue int `json:"issue,omitempty"`
	// Seq is monotonically increasing per (run_id, producer). Together with
	// Ts and Producer it forms the total order and the upload idempotency key.
	Seq int64 `json:"seq"`
	// Ts is RFC3339Nano UTC, captured at emit time.
	Ts string `json:"ts"`
	// Stage is the pipeline stage the event belongs to; empty for run-scoped
	// events (change_class, outcome).
	Stage string `json:"stage,omitempty"`
	// Phase is the phase-marker name for phase_transition events.
	Phase string `json:"phase,omitempty"`
	// Kind is the event taxonomy entry.
	Kind Kind `json:"kind"`
	// Producer is "go" or "sdk".
	Producer string `json:"producer"`
	// Payload is the kind-specific structured body. Writers pass the typed
	// payload structs below; readers decode into map[string]any.
	Payload any `json:"payload,omitempty"`
}

// ModelRoutingPayload carries a router Recommendation: the chosen model, the
// router's reasoning, and the alternatives it rejected.
type ModelRoutingPayload struct {
	// ForStage is the stage the recommendation targets (routing decisions are
	// made ahead of the stage they apply to, so this can differ from the
	// envelope Stage).
	ForStage         string               `json:"for_stage"`
	Model            string               `json:"model"`
	Reasoning        string               `json:"reasoning"`
	EstimatedCostUSD float64              `json:"estimated_cost_usd,omitempty"`
	Alternatives     []RoutingAlternative `json:"alternatives,omitempty"`
	// Trigger names the decision point: "scheduler_pickup" (run start) or
	// "performance_mode_reroute" (perf-mode changed mid-flight).
	Trigger string `json:"trigger"`
}

// RoutingAlternative is one model the router considered and rejected.
type RoutingAlternative struct {
	Model    string `json:"model"`
	TradeOff string `json:"trade_off"`
}

// ChangeClassPayload records the deterministic routing Decision (#4126):
// change-class resolution, the matched change rule, and the fast-track
// stage skips it authorizes.
type ChangeClassPayload struct {
	SuggestedRoute    string   `json:"suggested_route"`
	MatchedChangeRule string   `json:"matched_change_rule,omitempty"`
	SkipStages        []string `json:"skip_stages,omitempty"`
	Rationale         string   `json:"rationale"`
	RiskHigh          bool     `json:"risk_high,omitempty"`
	RiskReasons       []string `json:"risk_reasons,omitempty"`
	ChangeType        string   `json:"change_type,omitempty"`
	ComplexityScore   int      `json:"complexity_score,omitempty"`
}

// StageSkipPayload records one stage removed from the run, attributed to the
// deciding source.
type StageSkipPayload struct {
	// Source is "routing" (fast-track Decision) or "dependabot".
	Source            string `json:"source"`
	Reason            string `json:"reason"`
	MatchedChangeRule string `json:"matched_change_rule,omitempty"`
}

// StageStartPayload records the dispatch context a stage started with.
type StageStartPayload struct {
	Model           string `json:"model"`
	PerformanceMode string `json:"performance_mode,omitempty"`
	// EscalatedRetry is true when this dispatch is a retry on an escalated
	// model (the retry engine holds an override for this stage).
	EscalatedRetry bool `json:"escalated_retry,omitempty"`
}

// StageExitPayload summarizes a stage exit. Full forensics (signal, stderr
// tail, idle time, ...) live in the exit-records store, joined by run_id.
type StageExitPayload struct {
	Success      bool    `json:"success"`
	ExitCode     int     `json:"exit_code"`
	ElapsedMs    int64   `json:"elapsed_ms,omitempty"`
	Model        string  `json:"model,omitempty"`
	CostUSD      float64 `json:"cost_usd,omitempty"`
	TerminalKind string  `json:"terminal_kind,omitempty"`
	GateKind     string  `json:"gate_kind,omitempty"`
}

// GateResultPayload records one gate outcome.
type GateResultPayload struct {
	GateName string `json:"gate_name"`
	// Source is "stage_gate" (post-condition gates, #3266) or "quality_gate"
	// (build/lint/test gates from feature-validate).
	Source string `json:"source"`
	Passed bool   `json:"passed"`
	// ResultKind is the gate's outcome shape: "ok" | "no_op" | "fail" for
	// stage gates; empty for quality gates.
	ResultKind string   `json:"result_kind,omitempty"`
	Reason     string   `json:"reason,omitempty"`
	Evidence   []string `json:"evidence,omitempty"`
	DurationMs int64    `json:"duration_ms,omitempty"`
	// Trigger distinguishes the normal post-stage gate run from the terminal
	// reconcile re-run (#3835): "post_stage" | "terminal_reconcile".
	Trigger string `json:"trigger,omitempty"`
}

// EscalationPayload records a mid-run model change decision.
type EscalationPayload struct {
	// Direction is "up" (escalation) or "down" (downgrade/fallback).
	Direction string `json:"direction"`
	FromModel string `json:"from_model,omitempty"`
	ToModel   string `json:"to_model"`
	Reasoning string `json:"reasoning"`
	// Trigger names the decision point: "stage_failure", "stall_budget",
	// "missing_output", "model_unavailable", "runner_recorded".
	Trigger string `json:"trigger"`
}

// BacktrackPayload records a rewind decision to an earlier stage.
type BacktrackPayload struct {
	FromStage   string `json:"from_stage"`
	TargetStage string `json:"target_stage"`
	SignalType  string `json:"signal_type"`
	Rationale   string `json:"rationale,omitempty"`
	// Trigger names the rewind path: "feedback" (post-success feedback
	// signal), "stall_recovery" (#3005), "conflict_recovery" (#4072).
	Trigger string `json:"trigger"`
}

// RecoveryRetryPayload records one FailureRecovery registry attempt (#3268).
type RecoveryRetryPayload struct {
	Action         string   `json:"action"`
	Recovered      bool     `json:"recovered"`
	Reason         string   `json:"reason,omitempty"`
	Evidence       []string `json:"evidence,omitempty"`
	FollowUp       string   `json:"follow_up,omitempty"`
	AttemptOrdinal int      `json:"attempt_ordinal,omitempty"`
	DurationMs     int64    `json:"duration_ms,omitempty"`
}

// OutcomePayload is the terminal run outcome.
type OutcomePayload struct {
	Success             bool    `json:"success"`
	TerminalFailureKind string  `json:"terminal_failure_kind,omitempty"`
	TotalCostUSD        float64 `json:"total_cost_usd,omitempty"`
}

// DecisionRequestPayload records an Action Center DecisionRequest terminal
// transition (ADR 015 §H). Resolution carries option_id/actor/note; expiry
// carries applied=default_action. OriginatingSeq links back to the trace node
// that raised the request, when known.
type DecisionRequestPayload struct {
	ID       string `json:"id"`
	Kind     string `json:"kind"`
	Producer string `json:"producer"`
	// Transition is "resolved" or "expired".
	Transition string `json:"transition"`
	OptionID   string `json:"option_id,omitempty"`
	Actor      string `json:"actor,omitempty"`
	Note       string `json:"note,omitempty"`
	// Applied is the default_action option id for an expiry transition.
	Applied string `json:"applied,omitempty"`
	// OriginatingSeq is the seq of the trace node that raised the request
	// (context.trace_ref.seq), 0 when unknown.
	OriginatingSeq int64 `json:"originating_seq,omitempty"`
}
