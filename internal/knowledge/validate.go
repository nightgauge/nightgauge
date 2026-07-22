package knowledge

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/nightgauge/nightgauge/internal/config"
)

// adrHeaderRe matches ADR block headings of the form "## ADR-NNN: ...".
var adrHeaderRe = regexp.MustCompile(`(?mi)^##\s+ADR-\d+`)

// adrRequiredFields lists the bold field labels required in a valid ADR block.
var adrRequiredFields = []*regexp.Regexp{
	regexp.MustCompile(`(?mi)\*\*Status\*\*`),
	regexp.MustCompile(`(?mi)\*\*Context\*\*`),
	regexp.MustCompile(`(?mi)\*\*Decision\*\*`),
	regexp.MustCompile(`(?mi)\*\*Consequences\*\*`),
}

// ValidateResult holds the structured result of decisions.md validation.
type ValidateResult struct {
	// Valid is true when validation passed (no action required).
	Valid bool `json:"valid"`
	// Skipped is true when the gate was bypassed (require_decisions: false).
	Skipped bool `json:"skipped"`
	// HasTradeoffs is true when the plan contained 2+ distinct tradeoff keywords.
	HasTradeoffs bool `json:"has_tradeoffs"`
	// HasADRBlocks is true when decisions.md contained at least one valid ADR block.
	HasADRBlocks bool `json:"has_adr_blocks"`
	// Signals lists the detected tradeoff keyword matches (populated when HasTradeoffs is true).
	Signals []TradeoffSignal `json:"signals,omitempty"`
	// Message is a human-readable summary of the result (suitable for stdout/stderr).
	Message string `json:"message"`
}

// ValidateDecisionsPopulation checks whether decisions.md for issueNumber is
// properly populated when its plan contains tradeoff signals.
//
// Validation passes (returns nil error) when:
//   - cfg.ResolveRequireDecisions() returns false (escape hatch), OR
//   - The plan has fewer than 2 distinct tradeoff keywords, OR
//   - The plan has 2+ tradeoff keywords AND decisions.md contains ≥1 valid ADR block.
//
// Returns a non-nil error with an actionable message when validation fails.
func ValidateDecisionsPopulation(issueNumber int, workspaceRoot string, cfg *config.KnowledgeConfig) (*ValidateResult, error) {
	if !cfg.ResolveRequireDecisions() {
		return &ValidateResult{
			Valid:   true,
			Skipped: true,
			Message: "validation skipped (require_decisions: false in config)",
		}, nil
	}

	// Load tradeoff keywords (YAML file with default fallback).
	keywords, err := config.LoadTradeoffKeywords(workspaceRoot)
	if err != nil {
		keywords = config.DefaultTradeoffKeywords()
	}

	// Read the plan text for this issue.
	planText, err := readPlanText(issueNumber, workspaceRoot)
	if err != nil {
		return nil, fmt.Errorf("read plan for issue #%d: %w", issueNumber, err)
	}

	signals := FindTradeoffSignals(planText, keywords)
	hasTradeoffs := DetectTradeoffs(planText, keywords)

	result := &ValidateResult{
		HasTradeoffs: hasTradeoffs,
		Signals:      signals,
	}

	if !hasTradeoffs {
		result.Valid = true
		result.Message = "no tradeoff signals detected — decisions.md gate not triggered"
		return result, nil
	}

	// Plan has tradeoffs: require at least one valid ADR block in decisions.md.
	decisionsText, readErr := readDecisionsText(issueNumber, workspaceRoot)
	if readErr == nil && hasValidADRBlocks(decisionsText) {
		result.Valid = true
		result.HasADRBlocks = true
		result.Message = "decisions.md contains valid ADR blocks — validation passed"
		return result, nil
	}

	result.Valid = false
	if readErr == nil {
		result.HasADRBlocks = false
	}
	result.Message = buildValidationError(issueNumber, signals, readErr)
	return result, fmt.Errorf("%s", result.Message)
}

// hasValidADRBlocks returns true when decisionsText contains at least one ADR
// block with the required fields (Status, Context, Decision, Consequences).
// Field detection is case-insensitive and does not require strict ordering.
func hasValidADRBlocks(decisionsText string) bool {
	if !adrHeaderRe.MatchString(decisionsText) {
		return false
	}
	for _, fieldRe := range adrRequiredFields {
		if !fieldRe.MatchString(decisionsText) {
			return false
		}
	}
	return true
}

// readPlanText reads the plan Markdown for the given issue number.
// Searches .nightgauge/plans/{N}-*.md and returns the first match.
func readPlanText(issueNumber int, workspaceRoot string) (string, error) {
	plansDir := filepath.Join(workspaceRoot, ".nightgauge", "plans")
	pattern := filepath.Join(plansDir, fmt.Sprintf("%d-*.md", issueNumber))
	matches, err := filepath.Glob(pattern)
	if err != nil {
		return "", fmt.Errorf("glob plan files: %w", err)
	}
	if len(matches) == 0 {
		return "", fmt.Errorf("no plan file found matching %s", pattern)
	}
	data, err := os.ReadFile(matches[0])
	if err != nil {
		return "", fmt.Errorf("read plan file %s: %w", matches[0], err)
	}
	return string(data), nil
}

// readDecisionsText reads decisions.md for the given issue from the knowledge
// base directory via FindDecisionsPath.
func readDecisionsText(issueNumber int, workspaceRoot string) (string, error) {
	relPath, err := FindDecisionsPath(workspaceRoot, issueNumber)
	if err != nil {
		return "", err
	}
	absPath := relPath
	if !filepath.IsAbs(absPath) {
		absPath = filepath.Join(workspaceRoot, relPath)
	}
	data, err := os.ReadFile(absPath)
	if err != nil {
		return "", fmt.Errorf("read decisions.md at %s: %w", relPath, err)
	}
	return string(data), nil
}

// buildValidationError produces a human-readable error message listing the
// detected tradeoff signals, an ADR block template, and the escape hatch.
func buildValidationError(issueNumber int, signals []TradeoffSignal, readErr error) string {
	var sb strings.Builder
	fmt.Fprintf(&sb, "decisions.md for issue #%d requires at least one ADR block.\n", issueNumber)
	sb.WriteString("\nTradeoff signals detected in plan:\n")
	sb.WriteString(FormatSignalList(signals))

	if readErr != nil {
		fmt.Fprintf(&sb, "\nNote: decisions.md could not be read: %v\n", readErr)
	}

	sb.WriteString(`
Add at least one ADR block to decisions.md:

  ## ADR-001: [Decision Title]

  **Status**: Proposed
  **Context**: [Background and constraints that led to this decision]
  **Decision**: [What was decided and why]
  **Consequences**: [Expected impact, trade-offs, and follow-up actions]

Reference: docs/KNOWLEDGE_BASE.md
Escape hatch: set knowledge.require_decisions: false in .nightgauge/config.yaml`)
	return sb.String()
}
