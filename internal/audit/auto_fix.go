package audit

import (
	"fmt"
	"math"
	"strings"
)

// AutoFixConfig controls auto-fix behavior.
type AutoFixConfig struct {
	Enabled       bool
	StaleEpicDays int // default 90
	DryRun        bool
}

// CIConfig controls CI mode behavior.
type CIConfig struct {
	Enabled   bool
	Threshold float64 // minimum passing score (0-100)
}

// AutoFixResult records what was fixed.
type AutoFixResult struct {
	Fixed   []string `json:"fixed"`
	Skipped []string `json:"skipped"` // requires manual review
	Errors  []string `json:"errors"`
}

// CIResult is the output of CI mode.
type CIResult struct {
	OverallScore float64            `json:"overall_score"`
	Threshold    float64            `json:"threshold"`
	Passed       bool               `json:"passed"` // score >= threshold
	Dimensions   map[string]float64 `json:"dimensions"`
	ExitCode     int                `json:"exit_code"` // 0 = pass, 1 = fail
}

// autoFixableCategories lists finding categories that are safe to auto-fix.
var autoFixableCategories = map[string]bool{
	"STALE_EPIC":         true,
	"BOARD_STATUS_DRIFT": true,
	"STALE_BLOCKER":      true,
}

// ClassifyAutoFixable splits findings into those safe to auto-fix and those requiring manual review.
func ClassifyAutoFixable(findings []*AuditFinding) (fixable, manual []*AuditFinding) {
	for _, f := range findings {
		if autoFixableCategories[f.Category] {
			fixable = append(fixable, f)
		} else {
			manual = append(manual, f)
		}
	}
	return fixable, manual
}

// FormatCIOutput produces CI-readable output for the given CIResult.
func FormatCIOutput(result *CIResult) string {
	var sb strings.Builder

	status := "PASSED"
	if !result.Passed {
		status = "FAILED"
	}

	exitCodeMsg := "0 (score meets threshold)"
	if result.ExitCode != 0 {
		exitCodeMsg = "1 (score below threshold)"
	}

	sb.WriteString("Product Audit Summary\n")
	sb.WriteString("=====================\n")
	sb.WriteString(fmt.Sprintf("Overall Score: %.1f\n", result.OverallScore))
	sb.WriteString(fmt.Sprintf("Threshold:     %.1f\n", result.Threshold))
	sb.WriteString(fmt.Sprintf("Status:        %s\n", status))
	sb.WriteString("\nDimensions:\n")

	for key, score := range result.Dimensions {
		name := formatDimensionName(key)
		sb.WriteString(fmt.Sprintf("  %-20s %.1f/100\n", name+":", score))
	}

	sb.WriteString(fmt.Sprintf("\nExit Code: %s\n", exitCodeMsg))

	return sb.String()
}

// BuildCIResult computes a CIResult from a SynthesisReport and CIConfig.
func BuildCIResult(report *SynthesisReport, cfg CIConfig) *CIResult {
	dimensions := make(map[string]float64, len(report.Dimensions))
	for _, dim := range report.Dimensions {
		dimensions[dim.Name] = math.Round(dim.Score*10) / 10
	}

	passed := report.OverallScore >= cfg.Threshold
	exitCode := 0
	if !passed {
		exitCode = 1
	}

	return &CIResult{
		OverallScore: report.OverallScore,
		Threshold:    cfg.Threshold,
		Passed:       passed,
		Dimensions:   dimensions,
		ExitCode:     exitCode,
	}
}

// formatDimensionName converts snake_case or "Dimension N: Foo Bar" keys to a readable title.
func formatDimensionName(raw string) string {
	// Handle "Dimension N: Foo Bar" format — extract everything after the colon.
	if idx := strings.Index(raw, ":"); idx != -1 {
		return strings.TrimSpace(raw[idx+1:])
	}

	// Handle snake_case: replace underscores with spaces and title-case each word.
	parts := strings.Split(raw, "_")
	for i, p := range parts {
		if len(p) == 0 {
			continue
		}
		parts[i] = strings.ToUpper(p[:1]) + p[1:]
	}
	return strings.Join(parts, " ")
}
