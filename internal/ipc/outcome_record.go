package ipc

import (
	"sort"
	"time"

	"github.com/nightgauge/nightgauge/internal/intelligence/learning"
	"github.com/nightgauge/nightgauge/internal/intelligence/routing"
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
		// PREDICTED vs ACTUAL, in ONE vocabulary each. The calibration loop
		// (loopverdicts.analyzeCalibration) and Recorder.Calibrate both test
		// PredictedSize == ActualSize / PredictedModel == ActualModel for
		// equality, so a pair written in two vocabularies reports a *measured*
		// 0% forever — worse than no data, because the reader stops saying
		// "bootstrapping" and starts asserting a number.
		PredictedSize:   predictedSizeBucket(cls.ComplexityScore),
		ActualSize:      actualSizeBucket(record.Size),
		PredictedModel:  modelTier(cls.PredictedModel),
		ActualModel:     actualModelTier(record, snap),
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

// predictedSizeBucket expresses the router's complexity score in the corpus's
// size vocabulary. Returns "" for an unscored run — explicitly NOT
// SizeBucketForScore(0)=="small", which is why every pre-#304 corpus record
// reads predictedSize:"small" and is indistinguishable from a genuinely small
// issue.
func predictedSizeBucket(complexityScore int) string {
	if complexityScore <= 0 {
		return ""
	}
	return orchestrator.SizeBucketForScore(complexityScore)
}

// actualSizeBucket expresses the issue's size:* label (XS|S|M|L|XL) in the SAME
// small|medium|large vocabulary the prediction uses, by running it through the
// router's own size→complexity table and the same bucket thresholds.
//
// ActualSize had no production writer at all before this (grep: tests only), so
// the predicted-vs-actual size comparison every calibration consumer performs
// had never once run against real data. Filling it in the size:* vocabulary
// instead would have been worse than leaving it empty: "L" can never equal
// "medium", so the loop would report a measured 0% accuracy that no amount of
// improved routing could ever move.
//
// Returns "" when the issue carries no recognized size label.
func actualSizeBucket(size *string) string {
	score := routing.SizeBaseScore(sizeLabel(size))
	if score <= 0 {
		return ""
	}
	return orchestrator.SizeBucketForScore(score)
}

// sizeLabel dereferences the record's size:* label, returning "" for both a nil
// pointer and an empty string — "unknown", never a plausible-looking default.
func sizeLabel(size *string) string {
	if size == nil {
		return ""
	}
	return *size
}

// modelTier normalizes a model reference (registry alias like "sonnet" or a
// concrete id like "claude-sonnet-5") onto its registry band, so predicted and
// actual are comparable and so rows agree with the scheduler's corpus, which
// records the router's tier alias. Unknown models (user-defined local models
// the registry has never heard of) pass through verbatim rather than being
// dropped — an unrecognized name is still attribution; "" is not.
func modelTier(model string) string {
	if model == "" {
		return ""
	}
	if tier := orchestrator.NormalizeModelTier(model); tier != "" {
		return tier
	}
	return model
}

// actualModelTier resolves the model the run's work actually ran on.
//
// The rule is COST, not stage order: attribute the run to the stage that
// dominated its spend. Ordering heuristics are what made this field
// confidently wrong — resolving "the run's model" from the terminal stage
// attributes a $6.00 opus feature-validate to the $0.01 haiku pr-merge that
// closed the run, and falling back to the alphabetically first stage attributes
// it to issue-pickup. Both produce a plausible-looking model id that is not the
// model that did the work, and a wrong value is invisible where an empty one is
// logged.
//
// The refusal fallback wins over all of it (#91): when the CLI swapped models
// mid-run, the last served model is what produced the output, and attributing
// the run to a model that refused it is the same error in the other direction.
// Same rule as scheduler.recordOutcome.
//
// Returns "" when no stage reported a served model — an honest unknown the
// caller logs about.
func actualModelTier(record state.V2RunRecord, snap *state.RuntimeState) string {
	if snap != nil {
		if m := snap.LastRefusalServedModel(); m != "" {
			return modelTier(m)
		}
	}
	return modelTier(stageModel(record, dominantCostStage(record)))
}

// dominantCostStage names the stage that spent the most of the run's money AND
// reported a served model. Ties (including the all-zero-cost case, where every
// stage ties at 0) break on output tokens, then on stage name — deterministic,
// so the same record always maps to the same attribution.
//
// Returns "" when no stage reported a model.
func dominantCostStage(record state.V2RunRecord) string {
	best := ""
	var bestCost float64
	var bestOutput int
	for _, name := range sortedStageNames(record) {
		if stageModel(record, name) == "" {
			continue
		}
		tok := record.Tokens.PerStage[name]
		switch {
		case best == "",
			tok.CostUSD > bestCost,
			tok.CostUSD == bestCost && tok.Output > bestOutput:
			best, bestCost, bestOutput = name, tok.CostUSD, tok.Output
		}
	}
	return best
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
