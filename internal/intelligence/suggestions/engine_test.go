package suggestions

import (
	"testing"
)

func TestGenerateSuggestionsHighImpact(t *testing.T) {
	engine := NewEngine()
	findings := []Finding{
		{Dimension: "Cost Health", Severity: "critical", Title: "Cost too high", Score: 20},
		{Dimension: "Reliability", Severity: "critical", Title: "Failures increasing", Score: 25},
	}

	suggestions := engine.Generate(findings)
	if len(suggestions) < 2 {
		t.Fatalf("expected at least 2 suggestions, got %d", len(suggestions))
	}

	// Should be sorted by impact descending
	for i := 1; i < len(suggestions); i++ {
		if suggestions[i].Impact > suggestions[i-1].Impact {
			t.Errorf("suggestions not sorted by impact: [%d]=%f > [%d]=%f",
				i, suggestions[i].Impact, i-1, suggestions[i-1].Impact)
		}
	}
}

func TestGenerateSuggestionsVelocity(t *testing.T) {
	engine := NewEngine()
	findings := []Finding{
		{Dimension: "Pipeline Velocity", Severity: "warning", Title: "Slow pipeline", Score: 40},
	}

	suggestions := engine.Generate(findings)
	if len(suggestions) != 1 {
		t.Fatalf("expected 1 suggestion, got %d", len(suggestions))
	}
	if suggestions[0].ID != "velocity-boost" {
		t.Errorf("suggestion ID = %q, want velocity-boost", suggestions[0].ID)
	}
}

func TestGenerateNoSuggestions(t *testing.T) {
	engine := NewEngine()
	findings := []Finding{
		{Dimension: "Test", Severity: "info", Title: "All good", Score: 95},
	}

	suggestions := engine.Generate(findings)
	if len(suggestions) != 0 {
		t.Errorf("expected 0 suggestions for healthy findings, got %d", len(suggestions))
	}
}

func TestSanitizeID(t *testing.T) {
	tests := []struct {
		input string
		want  string
	}{
		{"Cost Health", "cost-health"},
		{"simple", "simple"},
		{"A/B Test", "a-b-test"},
	}

	for _, tt := range tests {
		got := sanitizeID(tt.input)
		if got != tt.want {
			t.Errorf("sanitizeID(%q) = %q, want %q", tt.input, got, tt.want)
		}
	}
}
