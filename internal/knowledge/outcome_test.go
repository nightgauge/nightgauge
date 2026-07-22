package knowledge

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func setupOutcomeDir(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	return dir
}

func makeFeatureDir(t *testing.T, root string, issueNumber int, slug string) string {
	t.Helper()
	p := filepath.Join(root, ".nightgauge", "knowledge", "features", fmt.Sprintf("%d-%s", issueNumber, slug))
	if err := os.MkdirAll(p, 0o755); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestRecordOutcome_AppendsToDecisionsWhenPresent(t *testing.T) {
	root := setupOutcomeDir(t)
	featureDir := makeFeatureDir(t, root, 42, "test-feature")

	// Pre-create decisions.md with some content.
	decisionsPath := filepath.Join(featureDir, "decisions.md")
	if err := os.WriteFile(decisionsPath, []byte("# Decisions\n\nSome existing content.\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	result, err := RecordOutcome(root, RecordOutcomeInput{
		IssueNumber:    42,
		Status:         "complete",
		DurationMins:   30,
		Tokens:         5000,
		CostUSD:        1.23,
		WhatWentWell:   "Everything worked well.",
		WhatDidnt:      "None.",
		LessonsLearned: "Always write tests first.",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if !result.Appended {
		t.Error("expected Appended=true")
	}
	if result.FileCreated {
		t.Error("expected FileCreated=false (decisions.md already existed)")
	}
	if result.Status != "complete" {
		t.Errorf("expected status=complete, got %s", result.Status)
	}
	if !strings.HasSuffix(result.TargetFile, "decisions.md") {
		t.Errorf("expected target to be decisions.md, got %s", result.TargetFile)
	}

	content, err := os.ReadFile(decisionsPath)
	if err != nil {
		t.Fatal(err)
	}
	body := string(content)
	if !strings.Contains(body, "## Outcome") {
		t.Error("expected ## Outcome section in decisions.md")
	}
	if !strings.Contains(body, "**Issue**: #42") {
		t.Error("expected issue marker in decisions.md")
	}
	if !strings.Contains(body, "5000 tokens") {
		t.Error("expected token usage in decisions.md")
	}
	if !strings.Contains(body, "$1.23") {
		t.Error("expected cost in decisions.md")
	}
	if !strings.Contains(body, "Everything worked well.") {
		t.Error("expected WhatWentWell in decisions.md")
	}
}

func TestRecordOutcome_CreatesOutcomesMdWhenNoDecisions(t *testing.T) {
	root := setupOutcomeDir(t)
	makeFeatureDir(t, root, 99, "no-decisions")

	result, err := RecordOutcome(root, RecordOutcomeInput{
		IssueNumber:  99,
		Status:       "partial",
		DurationMins: 15,
		Tokens:       1000,
		CostUSD:      0.50,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if !result.Appended {
		t.Error("expected Appended=true")
	}
	if !result.FileCreated {
		t.Error("expected FileCreated=true (outcomes.md created)")
	}
	if !strings.HasSuffix(result.TargetFile, "outcomes.md") {
		t.Errorf("expected target to be outcomes.md, got %s", result.TargetFile)
	}
}

func TestRecordOutcome_CreatesKnowledgeDirWhenMissing(t *testing.T) {
	root := setupOutcomeDir(t)
	// No knowledge directory created — RecordOutcome should create one.

	result, err := RecordOutcome(root, RecordOutcomeInput{
		IssueNumber:  200,
		Status:       "failed",
		DurationMins: 5,
		Tokens:       200,
		CostUSD:      0.05,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if !result.Appended {
		t.Error("expected Appended=true")
	}
	if !result.FileCreated {
		t.Error("expected FileCreated=true")
	}

	// Verify the directory was actually created.
	if _, err := os.Stat(filepath.Join(root, ".nightgauge", "knowledge", "features", "200-outcome")); err != nil {
		t.Errorf("expected knowledge directory to be created: %v", err)
	}
}

func TestRecordOutcome_Idempotent(t *testing.T) {
	root := setupOutcomeDir(t)
	makeFeatureDir(t, root, 77, "idempotent")

	input := RecordOutcomeInput{
		IssueNumber:  77,
		Status:       "complete",
		DurationMins: 20,
		Tokens:       3000,
		CostUSD:      0.75,
	}

	// First call — should append.
	r1, err := RecordOutcome(root, input)
	if err != nil {
		t.Fatalf("first call error: %v", err)
	}
	if !r1.Appended {
		t.Error("expected first call to append")
	}

	// Second call — should be a no-op.
	r2, err := RecordOutcome(root, input)
	if err != nil {
		t.Fatalf("second call error: %v", err)
	}
	if r2.Appended {
		t.Error("expected second call to be idempotent (Appended=false)")
	}

	// Read target file — should contain exactly one ## Outcome section.
	targetPath := filepath.Join(root, ".nightgauge", "knowledge", "features", "77-idempotent", "outcomes.md")
	content, err := os.ReadFile(targetPath)
	if err != nil {
		t.Fatal(err)
	}
	count := strings.Count(string(content), "## Outcome")
	if count != 1 {
		t.Errorf("expected exactly 1 ## Outcome block, got %d", count)
	}
}

func TestRecordOutcome_NarrativeDefaults(t *testing.T) {
	root := setupOutcomeDir(t)
	makeFeatureDir(t, root, 55, "narrative-defaults")

	_, err := RecordOutcome(root, RecordOutcomeInput{
		IssueNumber: 55,
		Status:      "complete",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	outcomesPath := filepath.Join(root, ".nightgauge", "knowledge", "features", "55-narrative-defaults", "outcomes.md")
	content, err := os.ReadFile(outcomesPath)
	if err != nil {
		t.Fatal(err)
	}
	body := string(content)
	if !strings.Contains(body, "No positive signals recorded.") {
		t.Error("expected default WhatWentWell")
	}
	if !strings.Contains(body, "No failure signals recorded.") {
		t.Error("expected default WhatDidnt")
	}
	if !strings.Contains(body, "No lessons recorded.") {
		t.Error("expected default LessonsLearned")
	}
}

func TestRecordOutcome_InvalidStatus(t *testing.T) {
	root := setupOutcomeDir(t)
	_, err := RecordOutcome(root, RecordOutcomeInput{
		IssueNumber: 1,
		Status:      "unknown-status",
	})
	if err == nil {
		t.Error("expected error for invalid status")
	}
}

func TestRecordOutcome_InvalidIssueNumber(t *testing.T) {
	root := setupOutcomeDir(t)
	_, err := RecordOutcome(root, RecordOutcomeInput{
		IssueNumber: 0,
		Status:      "complete",
	})
	if err == nil {
		t.Error("expected error for issue number 0")
	}
}

func TestRecordOutcome_NoCostInOutput(t *testing.T) {
	root := setupOutcomeDir(t)
	makeFeatureDir(t, root, 88, "no-cost")

	_, err := RecordOutcome(root, RecordOutcomeInput{
		IssueNumber:  88,
		Status:       "complete",
		DurationMins: 10,
		Tokens:       2000,
		// CostUSD intentionally zero
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	outcomesPath := filepath.Join(root, ".nightgauge", "knowledge", "features", "88-no-cost", "outcomes.md")
	content, err := os.ReadFile(outcomesPath)
	if err != nil {
		t.Fatal(err)
	}
	body := string(content)
	if !strings.Contains(body, "2000 tokens") {
		t.Error("expected token count in output")
	}
	if strings.Contains(body, "$") {
		t.Error("expected no cost in output when CostUSD=0")
	}
}

func TestFormatOutcomeBlock_Structure(t *testing.T) {
	block := formatOutcomeBlock(RecordOutcomeInput{
		IssueNumber:    123,
		Status:         "complete",
		DurationMins:   45,
		Tokens:         10000,
		CostUSD:        2.50,
		WhatWentWell:   "- Tests passed on first run.",
		WhatDidnt:      "- CI took longer than expected.",
		LessonsLearned: "- Pre-warm the build cache.",
	})

	checks := []string{
		"## Outcome",
		"**Issue**: #123",
		"**Status**: complete",
		"**Pipeline Duration**: 45 min",
		"**Token Usage**: 10000 tokens (~$2.50)",
		"### What Went Well",
		"- Tests passed on first run.",
		"### What Didn't Go Well",
		"- CI took longer than expected.",
		"### Lessons Learned",
		"- Pre-warm the build cache.",
		"---",
	}
	for _, want := range checks {
		if !strings.Contains(block, want) {
			t.Errorf("expected block to contain %q", want)
		}
	}
}
