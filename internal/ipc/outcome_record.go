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

// primaryModelStage is the stage whose served model best proxies the run's
// routed model: the implementation stage the router sized the issue for.
const primaryModelStage = "feature-dev"

// learningOutcomeFor derives the learning/calibration outcome for one terminal
// extension-path run from the SAME authoritative V2 run record that is about to
// be written to history (#304). Deriving both sinks from one record is the
// lesson of #261: a second, independently-built "mirror" record drifts.
//
// record is the built (and, for a deferral, already-overridden) run record.
// snap may be nil — every field it contributes has a record-derived fallback,
// so the mapper is exercisable against a captured record fixture with no live
// runtime.
//
// Returns the outcome plus the three-state decision. The outcome is only
// meaningful when the decision is outcomeRecord.
func learningOutcomeFor(
	record state.V2RunRecord,
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

	predictedModel := predictedModelFor(record, snap)
	// ActualModel: the predicted model is the proxy — EXCEPT when the CLI's
	// refusal fallback swapped models mid-run (#91), in which case the last
	// served model is recorded so learning data is not attributed to a model
	// that never produced the output. Same rule as scheduler.recordOutcome.
	actualModel := predictedModel
	if snap != nil {
		if m := snap.LastRefusalServedModel(); m != "" {
			actualModel = m
		}
	}

	return learning.Outcome{
		IssueNumber: record.IssueNumber,
		Repo:        resolveOutcomeRepo(record, repo),
		// PredictedSize is the issue's size:* label, left EMPTY when unknown.
		// Deliberately NOT run through the scheduler's predictedSizeLabel(score),
		// which maps an unknown score 0 onto "small" — that is why every outcome
		// in the existing corpus reads predictedSize:"small" and is
		// indistinguishable from a genuinely small issue.
		PredictedSize:   sizeLabel(record.Size),
		PredictedModel:  predictedModel,
		ActualModel:     actualModel,
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

// sizeLabel dereferences the record's size:* label, returning "" for both a nil
// pointer and an empty string — "unknown", never a plausible-looking default.
func sizeLabel(size *string) string {
	if size == nil {
		return ""
	}
	return *size
}

// predictedModelFor resolves the model the run is attributed to, in descending
// order of fidelity: the implementation stage's served model, the failing
// stage's served model, then the first stage (in sorted-name order, so the
// result is deterministic) that recorded one. Returns "" when no stage recorded
// a model — an honest unknown the caller logs about.
func predictedModelFor(record state.V2RunRecord, snap *state.RuntimeState) string {
	if m := stageModel(record, primaryModelStage); m != "" {
		return m
	}
	if snap != nil {
		if m := stageModel(record, string(snap.Stage)); m != "" {
			return m
		}
	}
	names := make([]string, 0, len(record.Stages))
	for name := range record.Stages {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		if m := stageModel(record, name); m != "" {
			return m
		}
	}
	return ""
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
	names := make([]string, 0, len(record.Stages))
	for name := range record.Stages {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
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
