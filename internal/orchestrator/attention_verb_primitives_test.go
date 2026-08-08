package orchestrator

import "testing"

func TestBudgetCeilingOverrideHonoredByResolver(t *testing.T) {
	root := t.TempDir()

	// No override → default ceiling.
	if got := PipelineBudgetCeilingUSD(root); got != 75.0 {
		t.Fatalf("default ceiling = %.2f, want 75.00", got)
	}
	// A raised runtime override wins when higher (budget.raiseCeiling verb).
	if err := WriteBudgetCeilingOverride(root, 200.0, "octocat", "action-center"); err != nil {
		t.Fatalf("WriteBudgetCeilingOverride: %v", err)
	}
	if got := PipelineBudgetCeilingUSD(root); got != 200.0 {
		t.Errorf("ceiling after raise = %.2f, want 200.00", got)
	}
	// A lower override never lowers the effective ceiling below config/default.
	if err := WriteBudgetCeilingOverride(root, 10.0, "octocat", "x"); err != nil {
		t.Fatalf("WriteBudgetCeilingOverride 2: %v", err)
	}
	if got := PipelineBudgetCeilingUSD(root); got != 75.0 {
		t.Errorf("ceiling with sub-default override = %.2f, want 75.00 (max wins)", got)
	}
}

// TestBudgetCeilingOverrideIsTheSameContractTheExtensionReads is the Go half of
// a two-language pair. Its twin is
// packages/nightgauge-vscode/tests/utils/resolvers/otherResolver.budgetOverride.test.ts,
// which drives the SAME file with the SAME numbers through
// getPipelineCeilingConfig → PipelineBudgetCeiling.getEffectiveCeiling.
//
// The pair exists because #305 wired a card whose primary option wrote a file
// only one of the two execution paths read (#305 round-2 finding 3). Resolving
// "Raise to $112.50 & retry" persisted the override, the re-dispatched
// EXTENSION run enforced the old $75 anyway, tripped the same between-stage
// check, and re-raised the same idempotency key — one more ceiling of tokens
// spent, no signal that the click did nothing. If either half of this pair is
// changed alone, the paths diverge again in exactly that silent way.
func TestBudgetCeilingOverrideIsTheSameContractTheExtensionReads(t *testing.T) {
	root := t.TempDir()

	if got := PipelineBudgetCeilingUSD(root); got != 75.0 {
		t.Fatalf("configured ceiling = %.2f, want 75.00", got)
	}
	if err := WriteBudgetCeilingOverride(root, 112.5, "octocat", "action-center: budget.raiseCeiling"); err != nil {
		t.Fatalf("WriteBudgetCeilingOverride: %v", err)
	}
	if got := PipelineBudgetCeilingUSD(root); got != 112.5 {
		t.Errorf("ceiling after the card's raise = %.2f, want 112.50 — the TS twin asserts the "+
			"same number through getPipelineCeilingConfig", got)
	}
	// A stale, lower override never lowers a ceiling raised elsewhere: max wins
	// on both paths. (60 is below the configured/default 75.)
	if err := WriteBudgetCeilingOverride(root, 60.0, "octocat", "stale"); err != nil {
		t.Fatalf("WriteBudgetCeilingOverride 2: %v", err)
	}
	if got := PipelineBudgetCeilingUSD(root); got != 75.0 {
		t.Errorf("ceiling with a sub-config override = %.2f, want the configured 75.00", got)
	}
}

func TestBudgetCeilingOverrideRejectsNonPositive(t *testing.T) {
	if err := WriteBudgetCeilingOverride(t.TempDir(), 0, "a", "b"); err == nil {
		t.Error("expected error for non-positive ceiling")
	}
}

func TestEscalationOverrideConsumeOnce(t *testing.T) {
	root := t.TempDir()
	if err := WriteEscalationOverride(root, 42, "opus", "octocat"); err != nil {
		t.Fatalf("WriteEscalationOverride: %v", err)
	}
	tier, ok := ConsumeEscalationOverride(root, 42)
	if !ok || tier != "opus" {
		t.Fatalf("ConsumeEscalationOverride = (%q, %v), want (opus, true)", tier, ok)
	}
	// Consume-once: the override is cleared after the first read so the
	// escalation applies to a single retry only.
	if tier2, ok2 := ConsumeEscalationOverride(root, 42); ok2 || tier2 != "" {
		t.Errorf("second consume = (%q, %v), want (\"\", false)", tier2, ok2)
	}
}

func TestOperatorSteerWritesWarningSignal(t *testing.T) {
	root := t.TempDir()
	if err := WriteOperatorSteer(root, 7, "skip the flaky test this run", "feature-dev"); err != nil {
		t.Fatalf("WriteOperatorSteer: %v", err)
	}
	// The synthetic signal must be warning severity with no backtrack target, so
	// EvaluateBacktrack ignores it (context, never a rewind).
	engine := NewRetryEngine(DefaultRetryConfig())
	dec, err := engine.EvaluateBacktrack(root + "/.nightgauge/pipeline/feedback-7.json")
	if err != nil {
		t.Fatalf("EvaluateBacktrack: %v", err)
	}
	if dec.ShouldBacktrack {
		t.Error("OPERATOR_STEER must not trigger a backtrack")
	}
}
