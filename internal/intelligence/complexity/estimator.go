// Package complexity estimates issue complexity (size prediction) from issue metadata.
package complexity

import (
	"strings"
)

// Score represents a complexity estimate on a 1-10 scale.
type Score struct {
	Value      int    `json:"value"`      // 1-10
	SizeLabel  string `json:"sizeLabel"`  // XS, S, M, L, XL
	Confidence string `json:"confidence"` // low, medium, high
	Reasoning  string `json:"reasoning"`
}

// Estimator predicts issue complexity from metadata signals.
type Estimator struct{}

// NewEstimator creates a complexity estimator.
func NewEstimator() *Estimator {
	return &Estimator{}
}

// Estimate produces a complexity score from issue metadata.
func (e *Estimator) Estimate(input Input) Score {
	score := 0
	reasons := make([]string, 0, 4)

	// Title length signal
	titleWords := len(strings.Fields(input.Title))
	if titleWords > 10 {
		score += 2
		reasons = append(reasons, "complex title")
	} else if titleWords > 5 {
		score += 1
	}

	// Body length signal
	bodyLen := len(input.Body)
	if bodyLen > 3000 {
		score += 3
		reasons = append(reasons, "detailed description")
	} else if bodyLen > 1000 {
		score += 2
		reasons = append(reasons, "moderate description")
	} else if bodyLen > 200 {
		score += 1
	}

	// Checklist items
	checklistItems := countChecklistItems(input.Body)
	if checklistItems > 10 {
		score += 3
		reasons = append(reasons, "many acceptance criteria")
	} else if checklistItems > 5 {
		score += 2
		reasons = append(reasons, "several acceptance criteria")
	} else if checklistItems > 0 {
		score += 1
	}

	// Label signals
	for _, label := range input.Labels {
		lower := strings.ToLower(label)
		if strings.HasPrefix(lower, "size:") {
			sizeLabel := strings.TrimPrefix(lower, "size:")
			switch sizeLabel {
			case "xs":
				// No adjustment
			case "s":
				score += 1
			case "m":
				score += 2
			case "l":
				score += 3
			case "xl":
				score += 4
			}
		}
		if lower == "type:feature" {
			score += 1
		}
		if lower == "type:refactor" {
			score += 2
			reasons = append(reasons, "refactoring work")
		}
	}

	// File count estimate
	if input.FileCountEstimate > 10 {
		score += 2
		reasons = append(reasons, "many files affected")
	} else if input.FileCountEstimate > 5 {
		score += 1
	}

	// Sub-issue count
	if input.SubIssueCount > 5 {
		score += 2
		reasons = append(reasons, "many sub-issues")
	} else if input.SubIssueCount > 0 {
		score += 1
	}

	// Clamp to 1-10
	if score < 1 {
		score = 1
	}
	if score > 10 {
		score = 10
	}

	return Score{
		Value:      score,
		SizeLabel:  scoreToSize(score),
		Confidence: estimateConfidence(input),
		Reasoning:  strings.Join(reasons, "; "),
	}
}

// Input contains the signals used for complexity estimation.
type Input struct {
	Title             string
	Body              string
	Labels            []string
	FileCountEstimate int
	SubIssueCount     int
}

func scoreToSize(score int) string {
	switch {
	case score <= 2:
		return "XS"
	case score <= 4:
		return "S"
	case score <= 6:
		return "M"
	case score <= 8:
		return "L"
	default:
		return "XL"
	}
}

func estimateConfidence(input Input) string {
	signals := 0
	if input.Body != "" {
		signals++
	}
	if len(input.Labels) > 0 {
		signals++
	}
	if input.FileCountEstimate > 0 {
		signals++
	}
	if countChecklistItems(input.Body) > 0 {
		signals++
	}

	switch {
	case signals >= 3:
		return "high"
	case signals >= 2:
		return "medium"
	default:
		return "low"
	}
}

func countChecklistItems(body string) int {
	count := 0
	for _, line := range strings.Split(body, "\n") {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "- [ ]") || strings.HasPrefix(trimmed, "- [x]") || strings.HasPrefix(trimmed, "- [X]") {
			count++
		}
	}
	return count
}
