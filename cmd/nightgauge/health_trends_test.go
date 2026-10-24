package main

import (
	"strings"
	"testing"

	healthpkg "github.com/nightgauge/nightgauge/internal/cmd/health"
)

// A run whose every dimension was starved is "N/A", never "Score: 0" (#1898),
// and a score names how many dimensions it is made of (#1900).
func TestFormatHealthTrendLine(t *testing.T) {
	unscored := healthpkg.HealthTrendEntry{
		Timestamp: "t", IssueNumber: 1, Status: "no-data",
		ExcludedDimensions: map[string]string{"reliability": "no runs"},
	}
	if got := formatHealthTrendLine(unscored); !strings.Contains(got, "Score: N/A") {
		t.Fatalf("null score rendered as %q", got)
	}
	score := 83.0
	scored := healthpkg.HealthTrendEntry{
		Timestamp: "t", IssueNumber: 2, OverallScore: &score, Status: "good",
		Dimensions:         map[string]float64{"reliability": 90, "cost-health": 75},
		ExcludedDimensions: map[string]string{"skill-drift": "no records"},
	}
	if got := formatHealthTrendLine(scored); !strings.Contains(got, "Score: 83 good (2/3 dimensions)") {
		t.Fatalf("scored entry rendered as %q", got)
	}
}
