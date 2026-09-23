// Package sizeGate implements the issue size preflight gate.
// It evaluates whether an issue is too large to process through the pipeline
// and should be decomposed into sub-issues first.
package sizeGate

import (
	"fmt"
	"log"
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
	// CapacityEnabled allows the capacity check (#1655) to run when a caller
	// supplies a context window. False only when pipeline.size_gate.enabled
	// is false, which turns every size-gate heuristic off.
	CapacityEnabled bool
	// SoftRoute is pipeline.size_gate.routes.reject_action == "soft-route":
	// an over-capacity issue routes to the first fallback in
	// CapacityFallbackModels whose window admits its size instead of being
	// rejected. It governs the capacity check only.
	SoftRoute bool
	// CapacityFallbackModels is pipeline.size_gate.routes.capacity_fallback_models,
	// the ordered models a soft-routed over-capacity issue may move to. Each
	// is a model string for the adapter the check is made against.
	CapacityFallbackModels []string
}

// DefaultGateConfig returns a GateConfig with safe defaults.
func DefaultGateConfig() GateConfig {
	return GateConfig{
		MaxLocInTitle:             5000,
		DecomposedItemsMin:        2,
		LocPatternEnabled:         true,
		DecompositionCheckEnabled: true,
		RejectOnOversized:         true,
		CapacityEnabled:           true,
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
	// Capacity is the capacity check's verdict, nil when no check was
	// requested (GateInput.Capacity nil) or the config disables it.
	Capacity *CapacityResult
	// RoutedModel is the fallback a soft-routed over-capacity issue moves
	// to, "" otherwise.
	RoutedModel string
}

// GateInput is the issue a gate evaluation judges.
type GateInput struct {
	Title     string
	Labels    []string
	SubIssues int
	// Body is read for CapacityDecomposedMarker only.
	Body string
	// Size overrides the size:* labels as the capacity check's size when
	// set (a board field, or a planner assessment).
	Size string
	// Capacity requests the capacity check; nil leaves the gate exactly as
	// it was before #1655.
	Capacity *CapacityInput
}

// CapacityInput is the model side of a capacity check.
type CapacityInput struct {
	// Window is the context window, in tokens, of the model that will run
	// the issue; 0 when it did not resolve.
	Window int
	// Fallbacks are CapacityFallbackModels with their resolved windows, in
	// order, consulted only under SoftRoute.
	Fallbacks []CapacityCandidate
}

// locPattern matches LOC references in issue titles, e.g. "5,234 LOC" or "5234 LOC".
var locPattern = regexp.MustCompile(`(?i)(\d{1,3}(?:,\d{3})*|\d+)\s*LOC\b`)

// GateEvaluator evaluates issues against size thresholds.
type GateEvaluator struct {
	cfg  GateConfig
	logf func(format string, args ...any)
}

// NewGateEvaluator creates a new GateEvaluator with the provided config.
func NewGateEvaluator(cfg GateConfig) *GateEvaluator {
	return &GateEvaluator{cfg: cfg}
}

// WithLogger replaces the logger the capacity check writes its one line to
// (log.Printf by default).
func (g *GateEvaluator) WithLogger(logf func(format string, args ...any)) *GateEvaluator {
	g.logf = logf
	return g
}

// Evaluate checks whether an issue passes the size gate.
// issueTitle is the issue title text.
// issueLabels is the slice of label names on the issue.
// subIssuesCount is the number of sub-issues currently linked to the issue.
func (g *GateEvaluator) Evaluate(issueTitle string, issueLabels []string, subIssuesCount int) *GateResult {
	return g.EvaluateIssue(GateInput{Title: issueTitle, Labels: issueLabels, SubIssues: subIssuesCount})
}

// EvaluateIssue is Evaluate over a GateInput. The heuristics run in order —
// LOC in title, capacity (only when in.Capacity is set), size:L/XL without
// decomposition — and the first rejection wins.
func (g *GateEvaluator) EvaluateIssue(in GateInput) *GateResult {
	result := &GateResult{
		Allowed:           true,
		HeuristicsApplied: []string{},
	}

	// Heuristic 1: LOC count in title
	if g.cfg.LocPatternEnabled {
		if reason, ok := g.checkLocPattern(in.Title); !ok {
			result.Allowed = false
			result.Reason = reason
			result.Severity = "medium"
			result.SuggestedAction = "Break the large feature into smaller sub-issues and link them via the GitHub sub-issue API"
			result.HeuristicsApplied = append(result.HeuristicsApplied, "loc-in-title")
			return result
		}
	}

	// Heuristic 2: the issue's size against the model's capacity (#1655).
	if in.Capacity != nil && g.cfg.CapacityEnabled {
		if g.checkCapacity(in, result) {
			return result
		}
	}

	// Heuristic 3: size:L or size:XL without minimum decomposition
	if g.cfg.DecompositionCheckEnabled {
		if reason, ok := g.checkLargeWithoutDecomposition(in.Labels, in.SubIssues); !ok {
			result.Allowed = false
			result.Reason = reason
			result.Severity = "medium"
			result.SuggestedAction = "Create sub-issues and link them via the GitHub sub-issue API"
			result.HeuristicsApplied = append(result.HeuristicsApplied, "size-without-decomposition")
			return result
		}
	}

	return result
}

// checkCapacity runs the capacity check into result and logs its one line.
// It returns true when the issue is rejected. Under SoftRoute an
// over-capacity issue that a fallback admits is allowed, with RoutedModel set.
func (g *GateEvaluator) checkCapacity(in GateInput, result *GateResult) bool {
	size := NormalizeSize(in.Size)
	if size == "" {
		size = SizeFromLabels(in.Labels)
	}
	capRes := CheckCapacity(size, in.Capacity.Window, IsCapacityDecomposedChild(in.Body))
	result.Capacity = &capRes
	logf := g.logf
	if logf == nil {
		logf = log.Printf
	}
	if capRes.Allowed {
		logf("%s", capRes.Note)
		return false
	}
	if g.cfg.SoftRoute {
		if alt, ok := FirstAdmittingFallback(capRes.Size, in.Capacity.Fallbacks); ok {
			result.RoutedModel = alt.Model
			result.HeuristicsApplied = append(result.HeuristicsApplied, "capacity-soft-route")
			logf("capacity: size %s exceeds the cap %s for a %d-token window — soft-routed to %s (window %d)",
				capRes.Size, capRes.MaxSize, capRes.Window, alt.Model, alt.Window)
			return false
		}
	}
	logf("%s", capRes.Note)
	result.Allowed = false
	result.Reason = capRes.Reason
	if g.cfg.SoftRoute {
		result.Reason += "; no capacity_fallback_models entry admits it"
	}
	result.Severity = "medium"
	if capRes.Recovery == RecoveryHumanDecomposition {
		result.SuggestedAction = "This issue is already a capacity-forced sub-issue: decompose it by hand, or run it on a model with a larger context window"
	} else {
		result.SuggestedAction = "Decompose the issue into sub-issues within the cap, or run it on a model with a larger context window"
	}
	result.HeuristicsApplied = append(result.HeuristicsApplied, "capacity")
	return true
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
