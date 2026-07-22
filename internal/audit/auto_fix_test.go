package audit

import (
	"strings"
	"testing"
)

func TestClassifyAutoFixable(t *testing.T) {
	findings := []*AuditFinding{
		{ID: "f1", Category: "STALE_EPIC", Severity: "medium"},
		{ID: "f2", Category: "API_MISMATCH", Severity: "high"},
		{ID: "f3", Category: "BOARD_STATUS_DRIFT", Severity: "low"},
		{ID: "f4", Category: "MISSING_TEST", Severity: "critical"},
	}

	fixable, manual := ClassifyAutoFixable(findings)

	if len(fixable) != 2 {
		t.Errorf("expected 2 fixable findings, got %d", len(fixable))
	}
	if len(manual) != 2 {
		t.Errorf("expected 2 manual findings, got %d", len(manual))
	}

	// Verify STALE_EPIC is in fixable.
	fixableIDs := make(map[string]bool)
	for _, f := range fixable {
		fixableIDs[f.Category] = true
	}
	if !fixableIDs["STALE_EPIC"] {
		t.Error("expected STALE_EPIC to be fixable")
	}
	if !fixableIDs["BOARD_STATUS_DRIFT"] {
		t.Error("expected BOARD_STATUS_DRIFT to be fixable")
	}

	// Verify API_MISMATCH is in manual.
	manualIDs := make(map[string]bool)
	for _, f := range manual {
		manualIDs[f.Category] = true
	}
	if !manualIDs["API_MISMATCH"] {
		t.Error("expected API_MISMATCH to be manual")
	}
}

func TestClassifyAutoFixableEmpty(t *testing.T) {
	fixable, manual := ClassifyAutoFixable(nil)
	if len(fixable) != 0 || len(manual) != 0 {
		t.Errorf("expected empty slices for nil input, got fixable=%d manual=%d", len(fixable), len(manual))
	}
}

func TestBuildCIResult_Passed(t *testing.T) {
	report := &SynthesisReport{
		OverallScore: 80.0,
		Dimensions: []*DimensionResult{
			{Name: "Dimension 1: API Alignment", Score: 80.0, Weight: 1.0},
		},
	}
	cfg := CIConfig{Enabled: true, Threshold: 75.0}

	result := BuildCIResult(report, cfg)

	if !result.Passed {
		t.Errorf("expected Passed=true for score 80 >= threshold 75")
	}
	if result.ExitCode != 0 {
		t.Errorf("expected ExitCode=0 for passing result, got %d", result.ExitCode)
	}
	if result.OverallScore != 80.0 {
		t.Errorf("expected OverallScore=80.0, got %.1f", result.OverallScore)
	}
	if result.Threshold != 75.0 {
		t.Errorf("expected Threshold=75.0, got %.1f", result.Threshold)
	}
}

func TestBuildCIResult_Failed(t *testing.T) {
	report := &SynthesisReport{
		OverallScore: 70.0,
		Dimensions: []*DimensionResult{
			{Name: "Dimension 1: API Alignment", Score: 70.0, Weight: 1.0},
		},
	}
	cfg := CIConfig{Enabled: true, Threshold: 75.0}

	result := BuildCIResult(report, cfg)

	if result.Passed {
		t.Errorf("expected Passed=false for score 70 < threshold 75")
	}
	if result.ExitCode != 1 {
		t.Errorf("expected ExitCode=1 for failing result, got %d", result.ExitCode)
	}
}

func TestFormatCIOutput_Passed(t *testing.T) {
	result := &CIResult{
		OverallScore: 80.0,
		Threshold:    75.0,
		Passed:       true,
		ExitCode:     0,
		Dimensions: map[string]float64{
			"Dimension 1: API Alignment": 80.0,
		},
	}

	output := FormatCIOutput(result)

	if !strings.Contains(output, "Product Audit Summary") {
		t.Errorf("output missing 'Product Audit Summary', got:\n%s", output)
	}
	if !strings.Contains(output, "PASSED") {
		t.Errorf("output missing 'PASSED' status, got:\n%s", output)
	}
	if strings.Contains(output, "FAILED") {
		t.Errorf("output should not contain 'FAILED' for passing result, got:\n%s", output)
	}
	if !strings.Contains(output, "Exit Code: 0") {
		t.Errorf("output missing 'Exit Code: 0' line, got:\n%s", output)
	}
}

func TestFormatCIOutput_Failed(t *testing.T) {
	result := &CIResult{
		OverallScore: 60.0,
		Threshold:    75.0,
		Passed:       false,
		ExitCode:     1,
		Dimensions:   map[string]float64{},
	}

	output := FormatCIOutput(result)

	if !strings.Contains(output, "Product Audit Summary") {
		t.Errorf("output missing 'Product Audit Summary', got:\n%s", output)
	}
	if !strings.Contains(output, "FAILED") {
		t.Errorf("output missing 'FAILED' status, got:\n%s", output)
	}
	if strings.Contains(output, "PASSED") {
		t.Errorf("output should not contain 'PASSED' for failing result, got:\n%s", output)
	}
	if !strings.Contains(output, "Exit Code: 1") {
		t.Errorf("output missing 'Exit Code: 1' line, got:\n%s", output)
	}
}
