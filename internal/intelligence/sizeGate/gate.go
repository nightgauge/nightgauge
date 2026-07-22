// Package sizeGate implements the issue size preflight gate.
// It evaluates whether an issue is too large to process through the pipeline
// and should be decomposed into sub-issues first.
package sizeGate

import (
	"fmt"
	"regexp"
	"strings"
)

// GateConfig holds the configuration for the size gate evaluator.
type GateConfig struct {
	// MaxLocInTitle is the LOC threshold above which an issue title triggers rejection.
	// Default: 5000.
	MaxLocInTitle int
	// DecomposedItemsMin is the minimum number of sub-issues required for size:L/XL issues.
	// Default: 2.
	DecomposedItemsMin int
	// LocPatternEnabled controls whether the LOC-in-title heuristic is active.
	LocPatternEnabled bool
	// DecompositionCheckEnabled controls whether the size:L/XL decomposition heuristic is active.
	DecompositionCheckEnabled bool
	// RejectOnOversized controls whether the gate rejects (true) or soft-routes (false).
	RejectOnOversized bool
}

// DefaultGateConfig returns a GateConfig with safe defaults.
func DefaultGateConfig() GateConfig {
	return GateConfig{
		MaxLocInTitle:             5000,
		DecomposedItemsMin:        2,
		LocPatternEnabled:         true,
		DecompositionCheckEnabled: true,
		RejectOnOversized:         true,
	}
}

// GateResult is the outcome of a size gate evaluation.
type GateResult struct {
	// Allowed is true when the issue passes the gate.
	Allowed bool
	// Reason describes why the issue was rejected (empty when Allowed is true).
	Reason string
	// Severity is "medium" for size gate violations.
	Severity string
	// SuggestedAction describes what the user should do to unblock.
	SuggestedAction string
	// HeuristicsApplied lists which heuristics triggered.
	HeuristicsApplied []string
}

// locPattern matches LOC references in issue titles, e.g. "5,234 LOC" or "5234 LOC".
var locPattern = regexp.MustCompile(`(?i)(\d{1,3}(?:,\d{3})*|\d+)\s*LOC\b`)

// GateEvaluator evaluates issues against size thresholds.
type GateEvaluator struct {
	cfg GateConfig
}

// NewGateEvaluator creates a new GateEvaluator with the provided config.
func NewGateEvaluator(cfg GateConfig) *GateEvaluator {
	return &GateEvaluator{cfg: cfg}
}

// Evaluate checks whether an issue passes the size gate.
// issueTitle is the issue title text.
// issueLabels is the slice of label names on the issue.
// subIssuesCount is the number of sub-issues currently linked to the issue.
func (g *GateEvaluator) Evaluate(issueTitle string, issueLabels []string, subIssuesCount int) *GateResult {
	result := &GateResult{
		Allowed:           true,
		HeuristicsApplied: []string{},
	}

	// Heuristic 1: LOC count in title
	if g.cfg.LocPatternEnabled {
		if reason, ok := g.checkLocPattern(issueTitle); !ok {
			result.Allowed = false
			result.Reason = reason
			result.Severity = "medium"
			result.SuggestedAction = "Break the large feature into smaller sub-issues and link them via the GitHub sub-issue API"
			result.HeuristicsApplied = append(result.HeuristicsApplied, "loc-in-title")
			return result
		}
	}

	// Heuristic 2: size:L or size:XL without minimum decomposition
	if g.cfg.DecompositionCheckEnabled {
		if reason, ok := g.checkLargeWithoutDecomposition(issueLabels, subIssuesCount); !ok {
			result.Allowed = false
			result.Reason = reason
			result.Severity = "medium"
			result.SuggestedAction = "Create sub-issues and link them via the GitHub sub-issue API (addSubIssue mutation)"
			result.HeuristicsApplied = append(result.HeuristicsApplied, "size-without-decomposition")
			return result
		}
	}

	return result
}

// checkLocPattern detects LOC counts in the issue title that exceed the threshold.
// Returns the rejection reason and false when the issue should be rejected.
func (g *GateEvaluator) checkLocPattern(title string) (string, bool) {
	matches := locPattern.FindStringSubmatch(title)
	if len(matches) < 2 {
		return "", true
	}

	// Strip comma separators and parse as integer
	locStr := strings.ReplaceAll(matches[1], ",", "")
	var loc int
	if _, err := fmt.Sscanf(locStr, "%d", &loc); err != nil {
		return "", true // parse error → treat as non-matching
	}

	if loc > g.cfg.MaxLocInTitle {
		return fmt.Sprintf("issue title references %d LOC (threshold: %d) — issue is too large for a single pipeline run", loc, g.cfg.MaxLocInTitle), false
	}

	return "", true
}

// checkLargeWithoutDecomposition checks if a size:L or size:XL issue lacks
// the minimum number of sub-issues required for sequential work.
func (g *GateEvaluator) checkLargeWithoutDecomposition(labels []string, subIssuesCount int) (string, bool) {
	largeLabel := ""
	for _, label := range labels {
		if label == "size:L" || label == "size:XL" {
			// Use the largest label if both are present
			if label == "size:XL" || largeLabel == "" {
				largeLabel = label
			}
		}
	}

	if largeLabel == "" {
		return "", true
	}

	if subIssuesCount < g.cfg.DecomposedItemsMin {
		return fmt.Sprintf("%s issue has %d sub-issue(s) but requires at least %d for pipeline processing",
			largeLabel, subIssuesCount, g.cfg.DecomposedItemsMin), false
	}

	return "", true
}
