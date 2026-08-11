package state

// Model-selection source vocabulary — the Go half of the single authority
// (#446).
//
// `model_selection.source` on a V2/V3 history record answers ONE question:
// how did the stage end up on the model it actually ran? Go is the sole
// writer of that field (BuildV2Record, history.go, is the only
// `V2ModelSelect` construction), so this list is the complete set of values
// that can ever reach disk — every reader may treat anything else as
// impossible.
//
// The TypeScript peer is MODEL_SELECTION_SOURCES in
// packages/nightgauge-sdk/src/analysis/types.ts, which is the AUTHORITY: every
// TypeScript surface derives its enum from that array rather than re-listing
// it, and Go follows it in the same order.
// TestModelSelectionSourcesPinnedToSDK lifts the SDK literal and requires
// ModelSelectionSources to equal it element-for-element, so the two lists
// cannot drift by hand. Before #446 they had: the TS copies listed nine values
// (env/config/stage-default/auto/…) that no writer could emit, and every real
// record fell out of strict validation because of it.
//
// Adding a value is therefore a two-file change: this slice and the SDK array,
// in the same commit.
const (
	// ModelSourceScheduler is the default attribution: the scheduler resolved
	// the model and nothing substituted it afterwards. Every real stage entry
	// written so far says this.
	ModelSourceScheduler = "scheduler"

	// ModelSourceCLIRefusalFallback marks a stage whose model was swapped by
	// the claude CLI's internal refusal fallback (#91) — the CLI served a
	// different model than the one dispatched and still exited 0.
	ModelSourceCLIRefusalFallback = "cli-refusal-fallback"

	// ModelSourceModelUnavailableDowngrade marks a stage that ran on a
	// downgraded model because the requested one was unavailable (#42) — the
	// escalation reason EscalationReasonModelUnavailable.
	ModelSourceModelUnavailableDowngrade = "model-unavailable-downgrade"

	// ModelSourceEscalation is the CLOSED DEFAULT for the escalation path: any
	// escalation reason without a dedicated label above maps here. It exists so
	// a new EscalationRecord reason can never leak an out-of-vocabulary string
	// onto disk the way the raw `source = esc.Reason` passthrough did before
	// #446. A reason that deserves its own label gets one here AND in the SDK
	// array — it never arrives by accident.
	ModelSourceEscalation = "escalation"
)

// ModelSelectionSources is the ordered vocabulary, mirroring
// MODEL_SELECTION_SOURCES in packages/nightgauge-sdk/src/analysis/types.ts.
// Order is part of the pin: the two declarations are compared
// element-for-element.
var ModelSelectionSources = []string{
	ModelSourceScheduler,
	ModelSourceCLIRefusalFallback,
	ModelSourceModelUnavailableDowngrade,
	ModelSourceEscalation,
}

// modelSelectionSourceForEscalationReason maps an EscalationRecord.Reason onto
// the model-selection vocabulary. It is TOTAL by construction: every input,
// including reasons that do not exist yet, returns a member of
// ModelSelectionSources. The two vocabularies are deliberately separate —
// reasons are snake_case terminal kinds that already exist in telemetry
// (EscalationReasonModelUnavailable), sources are kebab attribution labels —
// and this function is the only bridge between them.
func modelSelectionSourceForEscalationReason(reason string) string {
	switch reason {
	case EscalationReasonModelUnavailable:
		return ModelSourceModelUnavailableDowngrade
	default:
		return ModelSourceEscalation
	}
}
