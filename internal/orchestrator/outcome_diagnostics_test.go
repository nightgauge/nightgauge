// The operator-facing diagnostics for an empty corpus model band (#340).
//
// `OutcomeModelBand` stopped passing an unregistered id through verbatim: it
// returns "" so an id the registry has no band for is EXCLUDED from the
// accuracy denominator instead of booked as a guaranteed miss. That gave both
// writers' log lines a THIRD cause for an empty band, which the text still
// denied — an operator whose feature-dev stage really did report a model was
// told the stage reported none, which is the exact misdiagnosis class the
// schema fix was written to remove.
package orchestrator

import (
	"bytes"
	"log"
	"strings"
	"testing"

	"github.com/nightgauge/nightgauge/internal/intelligence/tokens"
	"github.com/nightgauge/nightgauge/internal/state"
	"github.com/nightgauge/nightgauge/pkg/types"
)

// captureLog redirects the standard logger for the duration of fn.
func captureLog(t *testing.T, fn func()) string {
	t.Helper()
	var buf bytes.Buffer
	prevOut, prevFlags := log.Writer(), log.Flags()
	log.SetOutput(&buf)
	log.SetFlags(0)
	t.Cleanup(func() {
		log.SetOutput(prevOut)
		log.SetFlags(prevFlags)
	})
	fn()
	return buf.String()
}

func TestOutcomeModelDiagnosticsNameTheCause(t *testing.T) {
	t.Run("predicted: nothing was predicted", func(t *testing.T) {
		got := OutcomePredictedModelDiagnostic(340, "")
		if !strings.Contains(got, "carried no routing.pickup_recommendation.dev_model") {
			t.Errorf("diagnostic = %q, want the absent-prediction cause", got)
		}
	})

	t.Run("predicted: an id with no registry band", func(t *testing.T) {
		got := OutcomePredictedModelDiagnostic(340, "gpt-5.5")
		if strings.Contains(got, "carried no routing.pickup_recommendation.dev_model") {
			t.Errorf("diagnostic = %q — a prediction WAS recorded; this text denies it", got)
		}
		if !strings.Contains(got, "gpt-5.5") {
			t.Errorf("diagnostic = %q, want the predicted id named", got)
		}
		if !strings.Contains(got, "band") {
			t.Errorf("diagnostic = %q, want the band-only rule stated", got)
		}
	})

	t.Run("actual: the stage reported nothing", func(t *testing.T) {
		got := OutcomeActualModelDiagnostic("")
		if !strings.Contains(got, "reported no served model") {
			t.Errorf("diagnostic = %q, want the absent-serve cause", got)
		}
	})

	t.Run("actual: an id with no registry band", func(t *testing.T) {
		got := OutcomeActualModelDiagnostic("gemini-2.0-flash")
		if strings.Contains(got, "reported no served model") {
			t.Errorf("diagnostic = %q — the stage DID report a model; this text denies it", got)
		}
		if !strings.Contains(got, "gemini-2.0-flash") {
			t.Errorf("diagnostic = %q, want the served id named", got)
		}
		if !strings.Contains(got, "band") {
			t.Errorf("diagnostic = %q, want the band-only rule stated", got)
		}
	})
}

// A run whose feature-dev stage DID report a served model — one the registry
// has no band for — must not be logged as a stage that reported nothing. This
// is the shape every codex `gpt-5.5`, gemini, lm-studio and ollama workspace
// produces, so the wrong text is what an operator debugging an empty
// ModelAccuracy actually reads.
func TestRecordOutcomeDiagnosesAnUnregisteredServedModel(t *testing.T) {
	root := t.TempDir()
	s := &Scheduler{recordOutcomes: true}
	item := types.BoardItem{Number: 340, Repo: "acme/widget", Size: types.SizeM}
	snap := state.NewRuntimeState(item.Repo, item.Number, "item-id", testRunID())
	snap.BeginStage(state.StageFeatureDev)
	snap.CompleteStage(0, tokens.TokenCounts{Input: 10, Output: 20}, "gemini-2.0-flash")
	snap.RecordStageModel(state.StageFeatureDev, "gemini-2.0-flash")

	out := captureLog(t, func() {
		s.recordOutcome(item, snap, true, 5, "opus", root)
	})

	if strings.Contains(out, "reported no served model") {
		t.Errorf("log says the stage reported no served model, but it reported gemini-2.0-flash:\n%s", out)
	}
	if !strings.Contains(out, "gemini-2.0-flash") {
		t.Errorf("log does not name the served id, so the operator cannot tell which cause fired:\n%s", out)
	}
}

// The mirror case: a prediction that IS present but unregistered must not be
// reported as an issue-{N}.json that carried no recommendation.
func TestRecordOutcomeDiagnosesAnUnregisteredPrediction(t *testing.T) {
	root := t.TempDir()
	s := &Scheduler{recordOutcomes: true}
	item := types.BoardItem{Number: 340, Repo: "acme/widget", Size: types.SizeM}
	snap := state.NewRuntimeState(item.Repo, item.Number, "item-id", testRunID())
	snap.BeginStage(state.StageFeatureDev)
	snap.CompleteStage(0, tokens.TokenCounts{Input: 10, Output: 20}, "claude-sonnet-5")
	snap.RecordStageModel(state.StageFeatureDev, "claude-sonnet-5")

	out := captureLog(t, func() {
		s.recordOutcome(item, snap, true, 5, "my-local-llama", root)
	})

	if strings.Contains(out, "carried no routing.pickup_recommendation.dev_model") {
		t.Errorf("log says no prediction was recorded, but the router recommended my-local-llama:\n%s", out)
	}
	if !strings.Contains(out, "my-local-llama") {
		t.Errorf("log does not name the predicted id:\n%s", out)
	}
}

// The genuinely-absent cases keep their original, correct text — the fix
// distinguishes causes, it does not blur them together.
func TestRecordOutcomeStillReportsGenuineAbsence(t *testing.T) {
	root := t.TempDir()
	s := &Scheduler{recordOutcomes: true}
	item := types.BoardItem{Number: 340, Repo: "acme/widget", Size: types.SizeM}
	snap := state.NewRuntimeState(item.Repo, item.Number, "item-id", testRunID())

	out := captureLog(t, func() {
		s.recordOutcome(item, snap, false, 5, "", root)
	})

	if !strings.Contains(out, "carried no routing.pickup_recommendation.dev_model") {
		t.Errorf("an absent prediction must still say so:\n%s", out)
	}
	if !strings.Contains(out, "reported no served model") {
		t.Errorf("an absent serve must still say so:\n%s", out)
	}
}

// TestOutcomeActualBandWorkedTable pins every row of the worked table in
// docs/OUTCOME_RECORDING.md § "The model pair's vocabulary". The doc's job is
// to tell an operator what their corpus will contain on their adapter, so a row
// that drifts from the code is worse than no table — the last four rows are the
// COMMON case on a non-Claude workspace and were previously described as if the
// band were translated for every adapter.
func TestOutcomeActualBandWorkedTable(t *testing.T) {
	rows := []struct {
		served, predicted, want, why string
	}{
		{"claude-opus-5", "opus", "opus", "single-band id"},
		{"gpt-5.6-sol", "opus", "opus", "the model the opus band maps to — a HIT"},
		{"gpt-5.6-sol", "fable", "fable", "same id, and the request says which band"},
		{"gpt-5.6-terra", "opus", "sonnet", "genuinely weaker serve — a MISS"},
		{"gemini-2.5-pro", "opus", "opus", "Maximum-mode gemini: the opus band's id — a HIT"},
		{"gemini-2.5-flash", "opus", "sonnet", "the shipped gemini default serves [haiku, sonnet] — a real MISS"},
		{"gemini-2.0-flash", "opus", "", "no registry band: excluded, never a miss"},
		{"gpt-5.5", "opus", "", "a configurable codex model the registry carries no band for"},
	}
	for _, r := range rows {
		if got := OutcomeActualBand(r.served, r.predicted); got != r.want {
			t.Errorf("OutcomeActualBand(%q, %q) = %q, want %q — %s (docs/OUTCOME_RECORDING.md)",
				r.served, r.predicted, got, r.want, r.why)
		}
	}
}
