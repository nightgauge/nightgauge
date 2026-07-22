package gates

import (
	"context"
	"strings"
	"testing"

	"github.com/nightgauge/nightgauge/internal/state"
)

// TestFeatureValidateGate_AdversarialCatchTrips proves the #4097 wiring
// end-to-end: when the adversarial-review critic records a "catch" via the
// deterministic writer, the existing FeatureValidateGate fails validation —
// the gate stays pure while the LLM verdict reaches it through gate-metrics.
func TestFeatureValidateGate_AdversarialCatchTrips(t *testing.T) {
	ws := t.TempDir()

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
	ws := t.TempDir()
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
