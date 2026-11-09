package gates

import (
	"context"
	"strings"
	"testing"

	"github.com/nightgauge/nightgauge/internal/layout/layouttest"
	"github.com/nightgauge/nightgauge/internal/state"
)

// TestFeatureValidateGate_AdversarialCatchTrips proves the #4097 wiring
// end-to-end: when the adversarial-review critic records a "catch" via the
// deterministic writer, the existing FeatureValidateGate fails validation —
// the gate stays pure while the LLM verdict reaches it through gate-metrics.
func TestFeatureValidateGate_AdversarialCatchTrips(t *testing.T) {
	ws := layouttest.Repo(t)

	// Deterministic gates all passed...
	mustAppend(t, ws, 4097, "build", "pass", "")
	mustAppend(t, ws, 4097, "unit-tests", "pass", "")
	// ...but the adversarial-review critic caught a real correctness defect.
	mustAppend(t, ws, 4097, "adversarial-review", "catch", "correctness: nil-deref on empty slice")

	res := FeatureValidateGate{}.Verify(context.Background(), 4097, ws)
	if res.Passed {
		t.Fatal("gate passed despite an adversarial-review catch — the judge verdict must fail validation")
	}
	joined := strings.Join(res.Evidence, " ")
	if !strings.Contains(joined, "adversarial-review") {
		t.Errorf("evidence should name the failing adversarial-review gate, got %q", joined)
	}
}

// TestFeatureValidateGate_AllPassIncludingAdversarial confirms a clean
// adversarial pass does not block validation.
func TestFeatureValidateGate_AllPassIncludingAdversarial(t *testing.T) {
	ws := layouttest.Repo(t)
	mustAppend(t, ws, 4097, "build", "pass", "")
	mustAppend(t, ws, 4097, "adversarial-review", "pass", "")

	res := FeatureValidateGate{}.Verify(context.Background(), 4097, ws)
	if !res.Passed {
		t.Errorf("gate failed despite all-pass (incl. adversarial-review): %s", res.Reason)
	}
}

func mustAppend(t *testing.T, ws string, issue int, gate, result, errSummary string) {
	t.Helper()
	if err := state.AppendGateMetric(ws, issue, gate, result, errSummary, "2026-06-25T00:00:00Z"); err != nil {
		t.Fatalf("append gate metric: %v", err)
	}
}

// TestFeatureValidateGate_ReviewCapParks: the cap is one issue's outcome, so the
// gate must not report the repository-halting validation_failed (#2865).
func TestFeatureValidateGate_ReviewCapParks(t *testing.T) {
	ws := layouttest.Repo(t)
	mustAppend(t, ws, 2865, "build", "pass", "")
	mustAppend(t, ws, 2865, ReviewGateName, "catch", ReviewCapMarker+" review round cap of 3 reached")

	res := FeatureValidateGate{}.Verify(context.Background(), 2865, ws)
	if res.Passed || res.TerminalKind != TerminalKindReviewCapReached {
		t.Fatalf("passed=%v kind=%q, want a failure of kind %q", res.Passed, res.TerminalKind, TerminalKindReviewCapReached)
	}

	// A later review of a new run supersedes the cap row, and an ordinary catch
	// keeps the ordinary kind.
	mustAppend(t, ws, 2865, ReviewGateName, "catch", "1 to fix; correctness: nil-deref")
	res = FeatureValidateGate{}.Verify(context.Background(), 2865, ws)
	if res.TerminalKind != TerminalKindValidationFailed {
		t.Fatalf("kind = %q, want %q", res.TerminalKind, TerminalKindValidationFailed)
	}
}

// #2944: a dropped marker followed only by passing judges rows from a partial
// or attempt-less set still fails: judges rows never release it.
func TestFeatureValidateGate_JudgeDroppedMarker_NotClearedByPartialJudges(t *testing.T) {
	ws := layouttest.Repo(t)
	writeGateMetrics(t, ws, 2944, []map[string]any{
		{"gate_name": "build", "result": "pass"},
		droppedMarker(""),
		{"gate_name": "judges", "result": "pass"},
	})

	gr := FeatureValidateGate{}.Verify(context.Background(), 2944, ws)
	if gr.Passed || !strings.Contains(gr.Reason, "a judge attempt was dropped") {
		t.Fatalf("expected the dropped reason; passed=%v reason=%q", gr.Passed, gr.Reason)
	}
}
