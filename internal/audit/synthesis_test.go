package audit

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestValidateWeights(t *testing.T) {
	// Valid: sum == 1.0
	dims := []*DimensionInput{
		{Dimension: "Dimension 1", Weight: 0.5},
		{Dimension: "Dimension 2", Weight: 0.5},
	}
	if err := ValidateWeights(dims, nil); err != nil {
		t.Errorf("expected nil error for sum=1.0, got: %v", err)
	}

	// Valid: sum within tolerance (1.005)
	dims2 := []*DimensionInput{
		{Dimension: "Dimension 1", Weight: 0.505},
		{Dimension: "Dimension 2", Weight: 0.5},
	}
	if err := ValidateWeights(dims2, nil); err != nil {
		t.Errorf("expected nil error for sum within tolerance, got: %v", err)
	}

	// Invalid: sum > 1.0 + 0.01
	dims3 := []*DimensionInput{
		{Dimension: "Dimension 1", Weight: 0.6},
		{Dimension: "Dimension 2", Weight: 0.5},
	}
	if err := ValidateWeights(dims3, nil); err == nil {
		t.Error("expected error for sum=1.1, got nil")
	}

	// Invalid: sum < 1.0 - 0.01
	dims4 := []*DimensionInput{
		{Dimension: "Dimension 1", Weight: 0.4},
		{Dimension: "Dimension 2", Weight: 0.5},
	}
	if err := ValidateWeights(dims4, nil); err == nil {
		t.Error("expected error for sum=0.9, got nil")
	}
}

func TestNormalizeDimensionName(t *testing.T) {
	cases := []struct {
		input    string
		expected string
	}{
		{"Dimension 1", "api_alignment"},
		{"Dimension 1: API Alignment", "api_alignment"},
		{"Dimension 2", "lifecycle"},
		{"Dimension 2: Epic Lifecycle", "lifecycle"},
		{"Dimension 3", "documentation"},
		{"Dimension 3: Documentation Accuracy", "documentation"},
		{"Dimension 4", "feature_parity"},
		{"Dimension 4: Feature Parity", "feature_parity"},
		{"Dimension 5", "test_coverage"},
		{"Dimension 5: Test Coverage", "test_coverage"},
		{"Dimension 6", "security"},
		{"Dimension 6: Security", "security"},
		{"Dimension 7", "dependencies"},
		{"Dimension 7: Dependencies", "dependencies"},
		{"Dimension 8", "ci_cd"},
		{"Dimension 8: CI/CD", "ci_cd"},
		// Fallback
		{"Custom Dimension", "custom_dimension"},
	}

	for _, tc := range cases {
		got := NormalizeDimensionName(tc.input)
		if got != tc.expected {
			t.Errorf("NormalizeDimensionName(%q) = %q, want %q", tc.input, got, tc.expected)
		}
	}
}

func TestComputeWeightedScore(t *testing.T) {
	dims := []*DimensionResult{
		{Score: 80.0, Weight: 0.6},
		{Score: 60.0, Weight: 0.4},
	}
	got := ComputeWeightedScore(dims)
	// 80*0.6 + 60*0.4 = 48 + 24 = 72
	want := 72.0
	if got != want {
		t.Errorf("ComputeWeightedScore = %.2f, want %.2f", got, want)
	}
}

func TestSynthesizeReport(t *testing.T) {
	dims := []*DimensionInput{
		{
			Dimension: "Dimension 1",
			Score:     80.0,
			Weight:    0.5,
			Findings: []RawFinding{
				{Category: "auth", Repository: "repo-a", File: "api.go", Severity: "high", Description: "Missing auth check"},
			},
		},
		{
			Dimension: "Dimension 2",
			Score:     60.0,
			Weight:    0.5,
			Findings: []RawFinding{
				{Category: "lifecycle", Repository: "repo-b", File: "board.go", Severity: "medium", Description: "Stale epic"},
				{Category: "lifecycle", Repository: "repo-b", File: "epic.go", Severity: "low", Description: "Missing label"},
			},
		},
	}

	report, err := SynthesizeReport(dims, nil)
	if err != nil {
		t.Fatalf("SynthesizeReport returned error: %v", err)
	}

	// Verify score: 80*0.5 + 60*0.5 = 70
	wantScore := 70.0
	if report.OverallScore != wantScore {
		t.Errorf("OverallScore = %.2f, want %.2f", report.OverallScore, wantScore)
	}

	// Verify finding count
	if report.TotalFindings != 3 {
		t.Errorf("TotalFindings = %d, want 3", report.TotalFindings)
	}

	// Verify finding IDs are non-empty
	for _, dim := range report.Dimensions {
		for _, f := range dim.Findings {
			if f.ID == "" {
				t.Errorf("finding ID is empty for category=%s", f.Category)
			}
		}
	}

	// Verify trend is "unknown"
	if report.Trend != "unknown" {
		t.Errorf("Trend = %q, want %q", report.Trend, "unknown")
	}

	// Verify dimensions count
	if len(report.Dimensions) != 2 {
		t.Errorf("len(Dimensions) = %d, want 2", len(report.Dimensions))
	}
}

func TestSynthesizeReportEmptyFindings(t *testing.T) {
	dims := []*DimensionInput{
		{Dimension: "Dimension 1", Score: 90.0, Weight: 0.5, Findings: []RawFinding{}},
		{Dimension: "Dimension 2", Score: 70.0, Weight: 0.5, Findings: nil},
	}

	report, err := SynthesizeReport(dims, nil)
	if err != nil {
		t.Fatalf("SynthesizeReport with empty findings returned error: %v", err)
	}
	if report.TotalFindings != 0 {
		t.Errorf("TotalFindings = %d, want 0", report.TotalFindings)
	}
}

func TestSynthesizeReportWeightMismatch(t *testing.T) {
	dims := []*DimensionInput{
		{Dimension: "Dimension 1", Score: 80.0, Weight: 0.7},
		{Dimension: "Dimension 2", Score: 60.0, Weight: 0.7},
	}

	_, err := SynthesizeReport(dims, nil)
	if err == nil {
		t.Error("expected error for weight sum=1.4, got nil")
	}
}

func TestGenerateMarkdownReport(t *testing.T) {
	dims := []*DimensionInput{
		{
			Dimension: "Dimension 1",
			Score:     75.0,
			Weight:    0.5,
			Findings: []RawFinding{
				{Category: "auth", Repository: "repo-a", File: "api.go", Severity: "high", Description: "Auth issue"},
			},
		},
		{
			Dimension: "Dimension 2",
			Score:     85.0,
			Weight:    0.5,
			Findings:  []RawFinding{},
		},
	}

	report, err := SynthesizeReport(dims, nil)
	if err != nil {
		t.Fatalf("SynthesizeReport returned error: %v", err)
	}

	md := GenerateMarkdownReport(report, nil)

	requiredSections := []string{
		"# Product Audit Report",
		"## Executive Summary",
		"## Overall Score",
		"## Dimension Scores",
		"## Findings by Severity",
		"## Detailed Findings",
	}

	for _, section := range requiredSections {
		if !strings.Contains(md, section) {
			t.Errorf("markdown missing section: %q", section)
		}
	}
}

func TestLoadDimensionFiles(t *testing.T) {
	tmpDir := t.TempDir()

	// Write two valid dimension JSON files.
	d1 := DimensionInput{
		Dimension: "Dimension 1",
		Score:     80.0,
		Weight:    0.5,
		Findings: []RawFinding{
			{Category: "auth", Repository: "repo-a", File: "main.go", Severity: "high", Description: "Test finding"},
		},
	}
	d2 := DimensionInput{
		Dimension: "Dimension 2",
		Score:     70.0,
		Weight:    0.5,
		Findings:  []RawFinding{},
	}

	for _, pair := range []struct {
		name string
		data DimensionInput
	}{
		{"dimension-1.json", d1},
		{"dimension-2.json", d2},
	} {
		b, err := json.Marshal(pair.data)
		if err != nil {
			t.Fatalf("failed to marshal test data: %v", err)
		}
		if err := os.WriteFile(filepath.Join(tmpDir, pair.name), b, 0o644); err != nil {
			t.Fatalf("failed to write test file: %v", err)
		}
	}

	// Write an invalid JSON file — should produce a warning, not a fatal error.
	if err := os.WriteFile(filepath.Join(tmpDir, "dimension-bad.json"), []byte("not json"), 0o644); err != nil {
		t.Fatalf("failed to write bad JSON file: %v", err)
	}

	dims, warnings, err := LoadDimensionFiles(tmpDir)
	if err != nil {
		t.Fatalf("LoadDimensionFiles returned error: %v", err)
	}

	if len(dims) != 2 {
		t.Errorf("loaded %d dimensions, want 2", len(dims))
	}

	if len(warnings) != 1 {
		t.Errorf("got %d warnings, want 1", len(warnings))
	}
}
