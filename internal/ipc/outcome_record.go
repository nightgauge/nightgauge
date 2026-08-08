package ipc

import (
	"sort"
	"time"

	"github.com/nightgauge/nightgauge/internal/intelligence/learning"
	"github.com/nightgauge/nightgauge/internal/orchestrator"
	"github.com/nightgauge/nightgauge/internal/state"
)

// outcomeDecision is the three-state verdict for whether a terminal pipeline
// event should append a learning/calibration outcome (#304).
//
// Three states, three values — deliberately NOT a bool. "recorded", "skipped
// because the run was a non-failure deferral" and "skipped because the failure
// was environmental" are genuinely different, and collapsing the two skips into
// one `false` is how a caller ends up logging "failed to record" for a run that
// was never supposed to produce a record. The zero value is outcomeUnset so a
// forgotten assignment cannot masquerade as a valid decision.
type outcomeDecision int

const (
	// outcomeUnset is the zero value: no decision was made. Never returned by
	// learningOutcomeFor; present so an uninitialized variable is not silently
	// read as "record it".
	outcomeUnset outcomeDecision = iota
	// outcomeRecord: append the outcome to the learning corpus.
	outcomeRecord
	// outcomeSkipDeferred: the run was a blocked-dependency deferral (#305).
	// A deferral did no work — no model ran, no size signal exists — so booking
	// it as Success:false would poison the reliability loop with a non-failure.
	outcomeSkipDeferred
	// outcomeSkipNetworkUnavailable: the run died to an unreachable network
	// (#3296). Cost/duration/token data from a half-completed network-killed run
	// is environmental noise, not signal about model or stage performance.
	// Exact parity with the Go scheduler's skip in scheduler.go.
	outcomeSkipNetworkUnavailable
)

// String names the decision for logs. Distinct strings per state so a log line
// says which of the two skips happened.
func (d outcomeDecision) String() string {
	switch d {
	case outcomeRecord:
		return "record"
	case outcomeSkipDeferred:
		return "skip-deferred"
	case outcomeSkipNetworkUnavailable:
		return "skip-network-unavailable"
	default:
		return "unset"
	}
}

// learningOutcomeFor derives the learning/calibration outcome for one terminal
// extension-path run from the SAME authoritative V2 run record that is about to
// be written to history (#304). Deriving both sinks from one record is the
// lesson of #261: a second, independently-built "mirror" record drifts.
//
// record is the built (and, for a deferral, already-overridden) run record.
// cls is the run's issue-{N}.json classification — the PREDICTION side of every
// predicted-vs-actual pair below. snap may be nil — every field it contributes
// has a record-derived fallback, so the mapper is exercisable against captured
// fixtures with no live runtime.
//
// Returns the outcome plus the three-state decision. The outcome is only
// meaningful when the decision is outcomeRecord.
func learningOutcomeFor(
	record state.V2RunRecord,
	cls issueClassification,
	snap *state.RuntimeState,
	repo string,
	now time.Time,
) (learning.Outcome, outcomeDecision) {
	// Read the deferral off the record rather than re-deriving it from the
	// notifyComplete params: #305 owns that classification, and reading its
	// result means this follows any change there instead of drifting from it.
	if record.OutcomeType == orchestrator.OutcomeTypeDeferred {
		return learning.Outcome{}, outcomeSkipDeferred
	}
	if record.TerminalFailureKind == orchestrator.TerminalKindNetworkUnavailable {
		return learning.Outcome{}, outcomeSkipNetworkUnavailable
	}

	// Post-#266 ground truth: the record's outcome is authoritative (a run whose
	// PR merged is "complete" even if a late per-stage kill reported failure).
	success := record.Outcome == "complete"

	return learning.Outcome{
		IssueNumber: record.IssueNumber,
		Repo:        resolveOutcomeRepo(record, repo),
		// PREDICTED vs ACTUAL, through the SAME helpers the corpus's other
		// writer (Scheduler.recordOutcome) uses — see
		// internal/orchestrator/outcome_semantics.go for the three rules and why
		// each holds. Both readers test the two halves for equality, so a pair
		// written in two vocabularies reports a *measured* 0% forever, and a
		// fabricated half is counted as a measurement of nothing.
		//
		// ActualSize is deliberately unset: no lines-changed measurement reaches
		// this boundary, and the size:* label is one of the same pre-run inputs
		// the prediction is derived from.
		//
		// The size input goes in RAW, through the same resolver the scheduler
		// uses (board Size field → size:* label → absent). The board term is
		// empty here and always will be under the current wire contract:
		// issue-{N}.json carries `labels` and `routing`, never the board field.
		// Passing a pre-resolved label instead is how the two writers ended up
		// keying one corpus field's presence on two different sources.
		PredictedSize:   orchestrator.OutcomePredictedSize("", cls.Labels, cls.ComplexityScore),
		PredictedModel:  orchestrator.OutcomeModelBand(cls.PredictedModel),
		ActualModel:     servedDevModel(record, snap, cls.PredictedModel),
		Success:         success,
		DurationMs:      record.TotalDuration,
		InputTokens:     record.Tokens.TotalInput,
		OutputTokens:    record.Tokens.TotalOutput,
		CostUSD:         record.Tokens.EstimatedCostUSD,
		ComplexityScore: record.Routing.ComplexityScore,
		FailedStage:     failedStageFor(record, snap, success),
		CompletedAt:     completedAtFor(record, now),
	}, outcomeRecord
}

// outcomePredictionFrom projects the outcome's predicted-vs-actual routing
// fields onto the run record, mirroring what scheduler.recordOutcome returns to
// recordV2History. One derivation, both sinks.
func outcomePredictionFrom(o learning.Outcome) *state.OutcomePrediction {
	return &state.OutcomePrediction{
		PredictedSize:  o.PredictedSize,
		ActualSize:     o.ActualSize,
		PredictedModel: o.PredictedModel,
		ActualModel:    o.ActualModel,
	}
}

// resolveOutcomeRepo prefers the record's resolved repo and falls back to the
// repo the extension named on the wire.
func resolveOutcomeRepo(record state.V2RunRecord, repo string) string {
	if record.Repo != "" {
		return record.Repo
	}
	return repo
}

// servedDevModel resolves the model the run's IMPLEMENTATION stage actually
// served, normalized onto its registry band. Returns "" when that stage never
// ran or reported no model — an honest unknown the caller logs about, and one
// every reader excludes from its denominator.
//
// `predicted` is the pair's other half, and it is an INPUT because the served
// value can be an adapter's concrete id whose provider serves several bands
// (codex gpt-5.6-sol is both opus and fable). Collapsing that to its strongest
// band books a MISS for a run served exactly as predicted — see
// orchestrator.OutcomeActualBand.
//
// Apples to apples: the prediction half of this pair is the router's
// pickup_recommendation.dev_model, which is a recommendation FOR feature-dev.
// This field used to be the served model of whichever stage dominated the run's
// COST — a different quantity. On this machine's real history the dominant-cost
// stage is feature-dev in well under half of runs, so a run that died in
// issue-pickup on opus booked a routing MISS against a dev-stage prediction for
// a stage that never ran, and no routing improvement could move it. The
// mismatch is invisible today only because model diversity is nil — i.e. it
// would surface exactly when the routing feature the corpus exists to calibrate
// started being used.
//
// The record is the authority (it is the same artifact notifyComplete is about
// to write, and its per-stage model_selection already carries the #91 CLI
// refusal-fallback served model); the runtime is the fallback for a snapshot
// that observed a dev-stage refusal swap the record has not yet absorbed.
func servedDevModel(record state.V2RunRecord, snap *state.RuntimeState, predicted string) string {
	if m := stageModel(record, string(orchestrator.OutcomeModelStage)); m != "" {
		return orchestrator.OutcomeActualBand(m, predicted)
	}
	return orchestrator.OutcomeActualBand(orchestrator.OutcomeServedDevModel(snap), predicted)
}

// sortedStageNames returns the record's stage names in a stable order.
func sortedStageNames(record state.V2RunRecord) []string {
	names := make([]string, 0, len(record.Stages))
	for name := range record.Stages {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

func stageModel(record state.V2RunRecord, stage string) string {
	if stage == "" {
		return ""
	}
	detail, ok := record.Stages[stage]
	if !ok || detail.ModelSelection == nil {
		return ""
	}
	return detail.ModelSelection.Model
}

// failedStageFor names the stage a failed run died in — empty on success, as on
// the Go path. Prefers the live runtime's terminal stage; falls back to the
// record's own failed stage (sorted for determinism) so a captured record maps
// identically with no runtime.
func failedStageFor(record state.V2RunRecord, snap *state.RuntimeState, success bool) string {
	if success {
		return ""
	}
	if snap != nil && snap.Stage != "" {
		return string(snap.Stage)
	}
	for _, name := range sortedStageNames(record) {
		if record.Stages[name].Status == "failed" {
			return name
		}
	}
	return ""
}

// completedAtFor parses the record's completion timestamp so the corpus entry
// and the history record agree to the second. Falls back to now when the record
// carries no parseable timestamp — never the zero time, which would sort every
// such outcome to the front of the corpus.
func completedAtFor(record state.V2RunRecord, now time.Time) time.Time {
	if record.CompletedAt != "" {
		if t, err := time.Parse(time.RFC3339, record.CompletedAt); err == nil {
			return t
		}
	}
	return now
}
