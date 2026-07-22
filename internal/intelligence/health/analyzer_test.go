package health

import (
	"testing"
	"time"
)

func TestAnalyzer_EmptyRuns(t *testing.T) {
	a := NewAnalyzer()
	report := a.Analyze(nil)

	if len(report.Dimensions) != 7 {
		t.Errorf("dimensions = %d, want 7", len(report.Dimensions))
	}
	// All should have "no data" or similar findings
	for _, d := range report.Dimensions {
		if d.Score < 0 || d.Score > 1 {
			t.Errorf("dimension %s score = %f, want 0-1", d.Dimension, d.Score)
		}
	}
}

func TestAnalyzer_AllSuccessful(t *testing.T) {
	a := NewAnalyzer()
	runs := make([]RunData, 10)
	for i := range runs {
		runs[i] = RunData{
			IssueNumber:  i + 1,
			Success:      true,
			DurationMs:   600_000, // 10 min
			InputTokens:  10000,
			OutputTokens: 5000,
			CostUSD:      0.50,
			Model:        "claude-sonnet-4-6",
			CompletedAt:  time.Now(),
		}
	}

	report := a.Analyze(runs)

	if report.OverallScore < 0.5 {
		t.Errorf("all-success overall = %f, want >= 0.5", report.OverallScore)
	}

	// Reliability should be 1.0
	for _, d := range report.Dimensions {
		if d.Dimension == DimReliability && d.Score != 1.0 {
			t.Errorf("reliability = %f, want 1.0", d.Score)
		}
	}
}

func TestAnalyzer_HighFailureRate(t *testing.T) {
	a := NewAnalyzer()
	runs := make([]RunData, 10)
	for i := range runs {
		runs[i] = RunData{
			IssueNumber: i + 1,
			Success:     i < 3, // Only 30% success
			DurationMs:  300_000,
			CostUSD:     0.50,
			Model:       "claude-sonnet-4-6",
		}
	}

	report := a.Analyze(runs)
	for _, d := range report.Dimensions {
		if d.Dimension == DimReliability {
			if d.Score > 0.5 {
				t.Errorf("low reliability score = %f, want <= 0.5", d.Score)
			}
		}
	}
}

func TestScoreToGrade(t *testing.T) {
	tests := []struct {
		score float64
		want  string
	}{
		{0.95, "A"}, {0.85, "B"}, {0.75, "C"}, {0.6, "D"}, {0.3, "F"},
	}
	for _, tt := range tests {
		got := scoreToGrade(tt.score)
		if got != tt.want {
			t.Errorf("scoreToGrade(%f) = %s, want %s", tt.score, got, tt.want)
		}
	}
}

func TestAnalyzer_SelfImprovement_Improving(t *testing.T) {
	a := NewAnalyzer()
	runs := make([]RunData, 10)
	for i := range runs {
		// First half: 20% success, second half: 80% success
		runs[i] = RunData{
			IssueNumber: i + 1,
			Success:     i >= 5 || i == 0,
			Model:       "claude-sonnet-4-6",
		}
	}

	report := a.Analyze(runs)
	for _, d := range report.Dimensions {
		if d.Dimension == DimLearningEffectiveness {
			if d.Score < 0.7 {
				t.Errorf("improving trend score = %f, want >= 0.7", d.Score)
			}
		}
	}
}
