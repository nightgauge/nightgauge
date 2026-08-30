package state

import (
	"os"
	"regexp"
	"testing"
	"time"
)

// Upward escalation reaches the durable record (#463).
//
// Only the two model-unavailable DOWNGRADE sites used to append to
// EscalationHistory. Every upward escalation called
// RetryEngine.RecordEscalation — a counter in process memory that nothing
// persists — and stopped there. Three records lied as a result, and this file
// is the red bar for each of them.

// TestBuildV2Record_UpwardEscalationIsAttributedToEscalation is the attribution
// half: the record used to say the stage's model came from the scheduler —
// "nothing substituted it" — about a stage whose model had just been
// substituted, and the "escalation" member of the model-selection vocabulary
// could not fire for the one case it names.
func TestBuildV2Record_UpwardEscalationIsAttributedToEscalation(t *testing.T) {
	hw := NewHistoryWriter(t.TempDir())
	now := time.Now()

	for _, reason := range []string{
		EscalationReasonStageFailed,
		EscalationReasonMissingOutput,
		EscalationReasonBudgetStall,
	} {
		t.Run(reason, func(t *testing.T) {
			rs := NewRuntimeState("nightgauge/nightgauge", 463, "item-463", testRunID())
			rs.BeginStage(StageFeatureDev)
			rs.RecordStageModel(StageFeatureDev, "opus")
			rs.AppendEscalation(EscalationRecord{
				Stage:     StageFeatureDev,
				FromModel: "sonnet",
				ToModel:   "opus",
				Reason:    reason,
				At:        now,
			})
			rs.CompleteStageWithCost(0, 100, 50, 0, 0.10)

			record := hw.BuildV2Record(rs, true, "", V2RunInput{}, now)
			sel := record.Stages[string(StageFeatureDev)].ModelSelection
			if sel == nil {
				t.Fatalf("no model_selection on the escalated stage: %+v", record.Stages)
			}
			if sel.Source != ModelSourceEscalation {
				t.Errorf("model_selection.source = %q, want %q — a stage that escalated "+
					"upward and is attributed to the scheduler claims nothing substituted "+
					"its model", sel.Source, ModelSourceEscalation)
			}
			// The mapped source is a COLLAPSE: all three causes land on the
			// same member. Without the sibling they are one indistinguishable
			// record, and the reason is unrecoverable afterwards.
			if sel.EscalationReason != reason {
				t.Errorf("model_selection.escalation_reason = %q, want %q",
					sel.EscalationReason, reason)
			}
		})
	}
}

// TestBuildV2Record_DowngradeCarriesNoEscalationReasonSibling is the negative
// arm. The sibling exists because "escalation" is a collapse; the downgrade has
// its own dedicated source member, so repeating its reason there would be a
// second copy of one fact that two readers could disagree about.
func TestBuildV2Record_DowngradeCarriesNoEscalationReasonSibling(t *testing.T) {
	hw := NewHistoryWriter(t.TempDir())
	now := time.Now()

	rs := NewRuntimeState("nightgauge/nightgauge", 463, "item-463-dg", testRunID())
	rs.BeginStage(StageFeatureDev)
	rs.RecordStageModel(StageFeatureDev, "haiku")
	rs.AppendEscalation(EscalationRecord{
		Stage:     StageFeatureDev,
		FromModel: "opus",
		ToModel:   "haiku",
		Reason:    EscalationReasonModelUnavailable,
		At:        now,
	})
	rs.CompleteStageWithCost(0, 100, 50, 0, 0.10)

	sel := hw.BuildV2Record(rs, true, "", V2RunInput{}, now).
		Stages[string(StageFeatureDev)].ModelSelection
	if sel == nil {
		t.Fatal("no model_selection on the downgraded stage")
	}
	if sel.Source != ModelSourceModelUnavailableDowngrade {
		t.Errorf("model_selection.source = %q, want %q",
			sel.Source, ModelSourceModelUnavailableDowngrade)
	}
	if sel.EscalationReason != "" {
		t.Errorf("model_selection.escalation_reason = %q, want empty — the downgrade has its "+
			"own source member and needs no sibling", sel.EscalationReason)
	}
}

// TestBuildV2Record_LastEscalationDecidesTheAttribution: a stage can now carry
// more than one escalation record, because both the downgrade sites and the
// upward sites write. The model the stage actually ran is whatever the LAST
// substitution chose, so that is the record the attribution comes from —
// first-match and last-match were indistinguishable while only one family of
// site wrote here.
func TestBuildV2Record_LastEscalationDecidesTheAttribution(t *testing.T) {
	hw := NewHistoryWriter(t.TempDir())
	now := time.Now()

	rs := NewRuntimeState("nightgauge/nightgauge", 463, "item-463-order", testRunID())
	rs.BeginStage(StageFeatureDev)
	rs.RecordStageModel(StageFeatureDev, "opus")
	// The model was first downgraded (opus unavailable), then — once it came
	// back — escalated upward after the stage failed on the weaker tier.
	rs.AppendEscalation(EscalationRecord{
		Stage: StageFeatureDev, FromModel: "opus", ToModel: "haiku",
		Reason: EscalationReasonModelUnavailable, At: now,
	})
	rs.AppendEscalation(EscalationRecord{
		Stage: StageFeatureDev, FromModel: "haiku", ToModel: "opus",
		Reason: EscalationReasonStageFailed, At: now.Add(time.Minute),
	})
	rs.CompleteStageWithCost(0, 100, 50, 0, 0.10)

	sel := hw.BuildV2Record(rs, true, "", V2RunInput{}, now).
		Stages[string(StageFeatureDev)].ModelSelection
	if sel == nil {
		t.Fatal("no model_selection on the stage")
	}
	if sel.Source != ModelSourceEscalation {
		t.Errorf("model_selection.source = %q, want %q — the LAST substitution is the one "+
			"that decided the model the stage ran", sel.Source, ModelSourceEscalation)
	}
	if sel.EscalationReason != EscalationReasonStageFailed {
		t.Errorf("model_selection.escalation_reason = %q, want %q",
			sel.EscalationReason, EscalationReasonStageFailed)
	}
}

// TestComputeAttemptsUntilSuccess_CountsUpwardEscalation pins the second
// consumer: the canonical "tries until green" the calibration corpus reads. It
// sums len(EscalationHistory), so while upward escalations never landed there,
// every escalating run reported fewer attempts than it took — under-reporting
// difficulty on exactly the issues that were hardest.
func TestComputeAttemptsUntilSuccess_CountsUpwardEscalation(t *testing.T) {
	hw := NewHistoryWriter(t.TempDir())
	now := time.Now()

	rs := NewRuntimeState("nightgauge/nightgauge", 463, "item-463-attempts", testRunID())
	rs.BeginStage(StageFeatureDev)
	rs.RecordStageModel(StageFeatureDev, "opus")
	rs.AppendEscalation(EscalationRecord{
		Stage: StageFeatureDev, FromModel: "sonnet", ToModel: "opus",
		Reason: EscalationReasonStageFailed, At: now,
	})
	rs.CompleteStageWithCost(0, 100, 50, 0, 0.10)

	if got := ComputeAttemptsUntilSuccess(nil, 0, len(rs.EscalationHistory)); got != 2 {
		t.Errorf("ComputeAttemptsUntilSuccess = %d, want 2 — the first try plus the "+
			"escalated one", got)
	}
	// The pure function is not the reading that matters; the durable record is.
	if got := hw.BuildV2Record(rs, true, "", V2RunInput{}, now).AttemptsUntilSuccess; got != 2 {
		t.Errorf("record.attempts_until_success = %d, want 2 — this is the field the "+
			"calibration corpus reads", got)
	}
}

// tsEscalationReasonSiblingRegexp lifts the `escalation_reason` declaration out
// of the TypeScript execution-history schema. Deliberately anchored on the
// exact declared FORM — an optional free string, not an enum. Making it an enum
// on either side would re-close the vocabulary this field exists to sit outside
// of.
var tsEscalationReasonSiblingRegexp = regexp.MustCompile(
	`(?m)^\s*escalation_reason:\s*z\.string\(\)\.optional\(\),\s*$`)

// TestEscalationReasonSiblingPinnedToExecutionHistorySchema is the
// cross-language half of #463, in the shape
// TestModelSelectionThinkingPinnedToExecutionHistorySchema established.
//
// Go is the only writer of model_selection and TypeScript validates it
// STRICTLY, so a field Go emits that the schema does not declare fails the
// parse for every record carrying one — which is how six drifted copies of
// `source` once cost the extension its whole history corpus. "Declared on both
// sides" therefore has to be a mechanism rather than a promise: nothing else in
// the repo would notice the TypeScript half being deleted.
//
// A missing declaration is a FAILURE, never a skip. A pin that quietly stops
// checking hides exactly the drift it exists to catch.
func TestEscalationReasonSiblingPinnedToExecutionHistorySchema(t *testing.T) {
	source, err := os.ReadFile(tsExecutionHistorySchemaPath)
	if err != nil {
		t.Fatalf("cannot read %s: %v\n"+
			"This pin is path-coupled: if executionHistory.ts moved, move "+
			"tsExecutionHistorySchemaPath with it — do NOT delete the pin.",
			tsExecutionHistorySchemaPath, err)
	}

	matches := tsEscalationReasonSiblingRegexp.FindAllString(string(source), -1)
	switch len(matches) {
	case 1:
		// pinned shape
	case 0:
		t.Fatalf("no `escalation_reason: z.string().optional(),` field found in %s.\n"+
			"Go writes V2ModelSelect.EscalationReason (internal/state/history.go) on every "+
			"upward escalation, and this schema validates model_selection strictly — an "+
			"undeclared field fails the parse for every record that carries one. If the "+
			"field was renamed on one side, rename it on both.",
			tsExecutionHistorySchemaPath)
	default:
		t.Fatalf("found %d `escalation_reason` declarations in %s; expected exactly 1 — "+
			"this pin cannot tell which one the schema actually declares.",
			len(matches), tsExecutionHistorySchemaPath)
	}

	// The Go half of the same fact. Every upward reason must be able to REACH
	// the sibling, which means mapping to the collapsing source; a reason that
	// mapped elsewhere would never populate the field, and the pin above would
	// still pass against a schema declaring a field nothing ever fills.
	for _, reason := range []string{
		EscalationReasonStageFailed,
		EscalationReasonMissingOutput,
		EscalationReasonBudgetStall,
	} {
		if got := modelSelectionSourceForEscalationReason(reason); got != ModelSourceEscalation {
			t.Errorf("reason %q maps to source %q, not %q — it would never populate "+
				"escalation_reason, leaving the field declared on both sides and always "+
				"empty", reason, got, ModelSourceEscalation)
		}
	}
}
