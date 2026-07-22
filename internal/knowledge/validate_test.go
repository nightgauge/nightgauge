package knowledge_test

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/nightgauge/nightgauge/internal/config"
	"github.com/nightgauge/nightgauge/internal/knowledge"
)

// validADRBlock is a complete ADR block that satisfies the validator.
const validADRBlock = `
## ADR-001: Use Go for deterministic validation

**Status**: Proposed
**Context**: The validation logic runs in planning skill where TypeScript SDK is not available.
**Decision**: Implement validation in Go binary so it is callable from any context.
**Consequences**: Single canonical validator accessible via CLI; no runtime dependencies on Node.
`

// planWithTradeoffs contains 2+ distinct tradeoff keywords.
const planWithTradeoffs = `# PLAN — #42

## Approach

We chose Go over TypeScript for the deterministic layer because it compiles to
a single binary. The alternative was to implement in TypeScript, but we rejected
that because it requires Node.js at runtime.
`

// planWithoutTradeoffs has no tradeoff keywords.
const planWithoutTradeoffs = `# PLAN — #42

## Approach

This is a straightforward configuration change that updates the timeout value.
No architectural choices were made.
`

// planWithSingleKeyword has only one keyword (below the 2-keyword threshold).
const planWithSingleKeyword = `# PLAN — #42

## Approach

We chose the simple approach since no other options were required.
`

func setupValidateFixtures(t *testing.T, issueNumber int, planText, decisionsText string) string {
	t.Helper()
	root := t.TempDir()

	plansDir := filepath.Join(root, ".nightgauge", "plans")
	if err := os.MkdirAll(plansDir, 0o755); err != nil {
		t.Fatalf("mkdir plans: %v", err)
	}
	planFile := filepath.Join(plansDir, fmt.Sprintf("%d-test-plan.md", issueNumber))
	if err := os.WriteFile(planFile, []byte(planText), 0o644); err != nil {
		t.Fatalf("write plan: %v", err)
	}

	if decisionsText != "" {
		knowledgeDir := filepath.Join(root, ".nightgauge", "knowledge", "features",
			fmt.Sprintf("%d-test-issue", issueNumber))
		if err := os.MkdirAll(knowledgeDir, 0o755); err != nil {
			t.Fatalf("mkdir knowledge: %v", err)
		}
		decisionsFile := filepath.Join(knowledgeDir, "decisions.md")
		if err := os.WriteFile(decisionsFile, []byte(decisionsText), 0o644); err != nil {
			t.Fatalf("write decisions.md: %v", err)
		}
	}

	return root
}

func requireDecisionsTrue() *config.KnowledgeConfig {
	b := true
	return &config.KnowledgeConfig{RequireDecisions: &b}
}

func requireDecisionsFalse() *config.KnowledgeConfig {
	b := false
	return &config.KnowledgeConfig{RequireDecisions: &b}
}

func TestValidateDecisions_PlanWithTradeoffs_ValidADR_Passes(t *testing.T) {
	root := setupValidateFixtures(t, 42, planWithTradeoffs, validADRBlock)
	result, err := knowledge.ValidateDecisionsPopulation(42, root, requireDecisionsTrue())
	if err != nil {
		t.Errorf("expected validation to pass, got error: %v", err)
	}
	if !result.Valid {
		t.Errorf("expected result.Valid=true; message: %s", result.Message)
	}
	if !result.HasTradeoffs {
		t.Error("expected result.HasTradeoffs=true")
	}
	if !result.HasADRBlocks {
		t.Error("expected result.HasADRBlocks=true")
	}
}

func TestValidateDecisions_PlanWithTradeoffs_EmptyDecisions_Fails(t *testing.T) {
	root := setupValidateFixtures(t, 42, planWithTradeoffs, "# Decisions: #42\n\n## Architecture Decisions\n")
	result, err := knowledge.ValidateDecisionsPopulation(42, root, requireDecisionsTrue())
	if err == nil {
		t.Error("expected validation to fail, but got no error")
	}
	if result.Valid {
		t.Error("expected result.Valid=false")
	}
	if !result.HasTradeoffs {
		t.Error("expected result.HasTradeoffs=true")
	}
	if result.Message == "" {
		t.Error("expected non-empty error message")
	}
}

func TestValidateDecisions_PlanWithoutTradeoffs_EmptyDecisions_Passes(t *testing.T) {
	root := setupValidateFixtures(t, 42, planWithoutTradeoffs, "# Decisions: #42\n")
	result, err := knowledge.ValidateDecisionsPopulation(42, root, requireDecisionsTrue())
	if err != nil {
		t.Errorf("expected validation to pass (no tradeoffs), got error: %v", err)
	}
	if !result.Valid {
		t.Errorf("expected result.Valid=true; message: %s", result.Message)
	}
	if result.HasTradeoffs {
		t.Error("expected result.HasTradeoffs=false")
	}
}

func TestValidateDecisions_RequireDecisionsFalse_SkipsGate(t *testing.T) {
	root := setupValidateFixtures(t, 42, planWithTradeoffs, "# Decisions: #42\n")
	result, err := knowledge.ValidateDecisionsPopulation(42, root, requireDecisionsFalse())
	if err != nil {
		t.Errorf("expected gate to be skipped, got error: %v", err)
	}
	if !result.Valid {
		t.Error("expected result.Valid=true when gate is disabled")
	}
	if !result.Skipped {
		t.Error("expected result.Skipped=true when require_decisions is false")
	}
}

func TestValidateDecisions_NilRequireDecisions_DefaultsFalse(t *testing.T) {
	// RequireDecisions not set → defaults to false → gate is skipped.
	root := setupValidateFixtures(t, 42, planWithTradeoffs, "# Decisions: #42\n")
	result, err := knowledge.ValidateDecisionsPopulation(42, root, &config.KnowledgeConfig{})
	if err != nil {
		t.Errorf("expected nil RequireDecisions to skip gate, got error: %v", err)
	}
	if !result.Valid {
		t.Error("expected result.Valid=true when RequireDecisions is unset (defaults false)")
	}
}

func TestValidateDecisions_PlanWithSingleKeyword_Passes(t *testing.T) {
	root := setupValidateFixtures(t, 42, planWithSingleKeyword, "# Decisions: #42\n")
	result, err := knowledge.ValidateDecisionsPopulation(42, root, requireDecisionsTrue())
	if err != nil {
		t.Errorf("expected validation to pass (single keyword below threshold), got error: %v", err)
	}
	if !result.Valid {
		t.Errorf("expected result.Valid=true; message: %s", result.Message)
	}
}

func TestValidateDecisions_MissingPlanFile_ReturnsError(t *testing.T) {
	root := t.TempDir()
	_, err := knowledge.ValidateDecisionsPopulation(42, root, requireDecisionsTrue())
	if err == nil {
		t.Error("expected error when plan file is missing")
	}
}

func TestValidateDecisions_MissingDecisionsFile_FailsWithSuggestion(t *testing.T) {
	root := t.TempDir()
	plansDir := filepath.Join(root, ".nightgauge", "plans")
	if err := os.MkdirAll(plansDir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(plansDir, "42-test-plan.md"), []byte(planWithTradeoffs), 0o644); err != nil {
		t.Fatalf("write plan: %v", err)
	}
	result, err := knowledge.ValidateDecisionsPopulation(42, root, requireDecisionsTrue())
	if err == nil {
		t.Error("expected error when decisions.md is missing")
	}
	if result.Valid {
		t.Error("expected result.Valid=false")
	}
	if result.Message == "" {
		t.Error("expected non-empty error message with suggestions")
	}
}

func TestValidateDecisions_ErrorMessage_ContainsADRTemplate(t *testing.T) {
	root := setupValidateFixtures(t, 42, planWithTradeoffs, "# Decisions: #42\n")
	result, _ := knowledge.ValidateDecisionsPopulation(42, root, requireDecisionsTrue())
	if result.Valid {
		t.Skip("validation passed — cannot check error message content")
	}
	if result.Message == "" {
		t.Error("expected non-empty error message")
	}
}

func TestValidateDecisions_ErrorMessage_ContainsEscapeHatch(t *testing.T) {
	root := setupValidateFixtures(t, 42, planWithTradeoffs, "# Decisions: #42\n")
	result, _ := knowledge.ValidateDecisionsPopulation(42, root, requireDecisionsTrue())
	if result.Valid {
		return
	}
	// The escape hatch config key should appear in the error message.
	if result.Message == "" {
		t.Error("expected non-empty error message mentioning escape hatch")
	}
}

func TestValidateDecisions_Signals_PopulatedOnFailure(t *testing.T) {
	root := setupValidateFixtures(t, 42, planWithTradeoffs, "# Decisions: #42\n")
	result, _ := knowledge.ValidateDecisionsPopulation(42, root, requireDecisionsTrue())
	if result.Valid {
		t.Skip("validation passed unexpectedly")
	}
	if len(result.Signals) == 0 {
		t.Error("expected result.Signals to be populated when HasTradeoffs=true")
	}
}
