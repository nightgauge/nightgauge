// Package suggestions generates actionable suggestions from health analysis.
package suggestions

import (
	"sort"
	"strings"
)

// Priority levels for suggestions.
type Priority string

const (
	PriorityHigh   Priority = "high"
	PriorityMedium Priority = "medium"
	PriorityLow    Priority = "low"
)

// Suggestion is an actionable recommendation.
type Suggestion struct {
	ID          string   `json:"id"`
	Title       string   `json:"title"`
	Description string   `json:"description"`
	Priority    Priority `json:"priority"`
	Impact      float64  `json:"impact"` // 0.0-1.0 expected impact
	Category    string   `json:"category"`
	Actions     []string `json:"actions,omitempty"`
}

// Finding represents a health analysis finding that triggers suggestions.
type Finding struct {
	Dimension string  `json:"dimension"`
	Severity  string  `json:"severity"` // "critical", "warning", "info"
	Title     string  `json:"title"`
	Score     float64 `json:"score"` // 0-100
}

// Engine generates suggestions from health findings.
type Engine struct{}

// NewEngine creates a suggestion engine.
func NewEngine() *Engine {
	return &Engine{}
}

// Generate produces prioritized suggestions from health findings.
func (e *Engine) Generate(findings []Finding) []Suggestion {
	var suggestions []Suggestion

	for _, f := range findings {
		s := e.findingSuggestion(f)
		if s != nil {
			suggestions = append(suggestions, *s)
		}
	}

	// Sort by impact descending
	sort.Slice(suggestions, func(i, j int) bool {
		return suggestions[i].Impact > suggestions[j].Impact
	})

	return suggestions
}

// findingSuggestion maps a finding to a suggestion.
func (e *Engine) findingSuggestion(f Finding) *Suggestion {
	dim := strings.ToLower(f.Dimension)

	switch {
	case f.Score < 30 && strings.Contains(dim, "cost"):
		return &Suggestion{
			ID:          "cost-reduce",
			Title:       "Reduce pipeline costs",
			Description: "Cost health score is critically low. Review model routing and token usage.",
			Priority:    PriorityHigh,
			Impact:      0.9,
			Category:    "cost",
			Actions: []string{
				"Switch low-complexity issues to cheaper models",
				"Enable token budget caps per stage",
				"Review stages with highest token consumption",
			},
		}

	case f.Score < 30 && strings.Contains(dim, "reliab"):
		return &Suggestion{
			ID:          "reliability-improve",
			Title:       "Improve pipeline reliability",
			Description: "Reliability score is critically low. Address recurring failures.",
			Priority:    PriorityHigh,
			Impact:      0.95,
			Category:    "reliability",
			Actions: []string{
				"Review failure patterns in recent runs",
				"Add retry logic for transient failures",
				"Increase timeout for complex stages",
			},
		}

	case f.Score < 50 && strings.Contains(dim, "velocity"):
		return &Suggestion{
			ID:          "velocity-boost",
			Title:       "Boost pipeline velocity",
			Description: "Pipeline velocity is below target. Optimize stage execution.",
			Priority:    PriorityMedium,
			Impact:      0.7,
			Category:    "velocity",
			Actions: []string{
				"Parallelize independent validation steps",
				"Cache dependency installations",
				"Reduce plan granularity for simple issues",
			},
		}

	case f.Score < 50 && strings.Contains(dim, "token"):
		return &Suggestion{
			ID:          "token-optimize",
			Title:       "Optimize token usage",
			Description: "Token economics are suboptimal. Review context injection.",
			Priority:    PriorityMedium,
			Impact:      0.6,
			Category:    "tokens",
			Actions: []string{
				"Trim redundant context from stage handoffs",
				"Use adaptive documentation reading",
				"Limit file content in planning stage",
			},
		}

	case f.Severity == "critical":
		return &Suggestion{
			ID:          "critical-" + sanitizeID(f.Dimension),
			Title:       "Address critical: " + f.Title,
			Description: f.Title,
			Priority:    PriorityHigh,
			Impact:      0.8,
			Category:    f.Dimension,
		}

	case f.Severity == "warning" && f.Score < 60:
		return &Suggestion{
			ID:          "warning-" + sanitizeID(f.Dimension),
			Title:       "Investigate: " + f.Title,
			Description: f.Title,
			Priority:    PriorityLow,
			Impact:      0.3,
			Category:    f.Dimension,
		}
	}

	return nil
}

func sanitizeID(s string) string {
	return strings.Map(func(r rune) rune {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') || r == '-' {
			return r
		}
		if r >= 'A' && r <= 'Z' {
			return r + 32 // lowercase
		}
		return '-'
	}, s)
}
