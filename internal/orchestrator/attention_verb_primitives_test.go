package orchestrator

import "testing"

func TestBudgetCeilingOverrideHonoredByResolver(t *testing.T) {
	root := t.TempDir()

	// No override → default ceiling.
	if got := getPipelineBudgetCeilingUSD(root); got != 75.0 {
		t.Fatalf("default ceiling = %.2f, want 75.00", got)
	}
	// A raised runtime override wins when higher (budget.raiseCeiling verb).
	if err := WriteBudgetCeilingOverride(root, 200.0, "octocat", "action-center"); err != nil {
		t.Fatalf("WriteBudgetCeilingOverride: %v", err)
	}
	if got := getPipelineBudgetCeilingUSD(root); got != 200.0 {
		t.Errorf("ceiling after raise = %.2f, want 200.00", got)
	}
	// A lower override never lowers the effective ceiling below config/default.
	if err := WriteBudgetCeilingOverride(root, 10.0, "octocat", "x"); err != nil {
		t.Fatalf("WriteBudgetCeilingOverride 2: %v", err)
	}
	if got := getPipelineBudgetCeilingUSD(root); got != 75.0 {
		t.Errorf("ceiling with sub-default override = %.2f, want 75.00 (max wins)", got)
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
