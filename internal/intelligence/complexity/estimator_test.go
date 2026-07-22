package complexity

import (
	"strings"
	"testing"
)

func TestEstimator_SimpleIssue(t *testing.T) {
	e := NewEstimator()
	score := e.Estimate(Input{
		Title: "Fix typo",
		Body:  "Change the misspelled word.",
	})

	if score.Value > 3 {
		t.Errorf("simple issue score = %d, want <= 3", score.Value)
	}
	if score.SizeLabel != "XS" && score.SizeLabel != "S" {
		t.Errorf("simple issue size = %s, want XS or S", score.SizeLabel)
	}
}

func TestEstimator_ComplexIssue(t *testing.T) {
	e := NewEstimator()
	body := strings.Repeat("This is a long detailed description of a complex feature. ", 100)
	body += "\n- [ ] acceptance criteria 1\n- [ ] criteria 2\n- [ ] criteria 3\n- [ ] criteria 4\n- [ ] criteria 5\n- [ ] criteria 6"

	score := e.Estimate(Input{
		Title:             "Implement comprehensive authentication system with OAuth2 and SSO support",
		Body:              body,
		Labels:            []string{"type:feature", "size:XL"},
		FileCountEstimate: 15,
		SubIssueCount:     6,
	})

	if score.Value < 7 {
		t.Errorf("complex issue score = %d, want >= 7", score.Value)
	}
	if score.SizeLabel != "L" && score.SizeLabel != "XL" {
		t.Errorf("complex issue size = %s, want L or XL", score.SizeLabel)
	}
}

func TestEstimator_SizeLabelSignal(t *testing.T) {
	e := NewEstimator()
	tests := []struct {
		label    string
		minScore int
	}{
		{"size:xs", 1},
		{"size:m", 2},
		{"size:xl", 4},
	}
	for _, tt := range tests {
		score := e.Estimate(Input{
			Title:  "Test issue",
			Body:   "Some body text",
			Labels: []string{tt.label},
		})
		if score.Value < tt.minScore {
			t.Errorf("label %s: score = %d, want >= %d", tt.label, score.Value, tt.minScore)
		}
	}
}

func TestEstimator_Confidence(t *testing.T) {
	e := NewEstimator()

	// Low confidence: title only
	low := e.Estimate(Input{Title: "Test"})
	if low.Confidence != "low" {
		t.Errorf("empty body confidence = %s, want low", low.Confidence)
	}

	// High confidence: body, labels, checklist, file count
	high := e.Estimate(Input{
		Title:             "Test",
		Body:              "Description\n- [ ] check 1",
		Labels:            []string{"size:M"},
		FileCountEstimate: 5,
	})
	if high.Confidence != "high" {
		t.Errorf("rich input confidence = %s, want high", high.Confidence)
	}
}

func TestCountChecklistItems(t *testing.T) {
	body := "Some text\n- [ ] unchecked\n- [x] checked\n- [X] also checked\nNot a checkbox"
	count := countChecklistItems(body)
	if count != 3 {
		t.Errorf("checklist count = %d, want 3", count)
	}
}

func TestScoreToSize(t *testing.T) {
	tests := []struct {
		score int
		want  string
	}{
		{1, "XS"}, {2, "XS"}, {3, "S"}, {5, "M"}, {7, "L"}, {9, "XL"}, {10, "XL"},
	}
	for _, tt := range tests {
		got := scoreToSize(tt.score)
		if got != tt.want {
			t.Errorf("scoreToSize(%d) = %s, want %s", tt.score, got, tt.want)
		}
	}
}
