package batch

import (
	"testing"

	"github.com/nightgauge/nightgauge/internal/models"
)

func TestAssessor_EmptyInput(t *testing.T) {
	a := NewAssessor()
	result := a.Assess(nil)
	if result.Strategy != StrategySequential {
		t.Errorf("empty strategy = %s, want sequential", result.Strategy)
	}
}

func TestAssessor_SmallBatch_Sequential(t *testing.T) {
	a := NewAssessor()
	issues := []IssueInput{
		{Number: 1, Title: "Fix typo", Body: "Simple fix"},
		{Number: 2, Title: "Update docs", Body: "Documentation update"},
	}
	result := a.Assess(issues)
	if result.Strategy != StrategySequential {
		t.Errorf("small batch strategy = %s, want sequential", result.Strategy)
	}
}

func TestAssessor_IndependentIssues_Parallel(t *testing.T) {
	a := NewAssessor()
	issues := []IssueInput{
		{Number: 1, Title: "Fix typo", Body: "Simple fix"},
		{Number: 2, Title: "Update docs", Body: "Docs"},
		{Number: 3, Title: "Add test", Body: "New test"},
		{Number: 4, Title: "Fix lint", Body: "Lint warning"},
	}
	result := a.Assess(issues)
	if result.Strategy != StrategyParallel {
		t.Errorf("independent batch strategy = %s, want parallel", result.Strategy)
	}
}

func TestAssessor_AllDependencies_Sequential(t *testing.T) {
	a := NewAssessor()
	issues := []IssueInput{
		{Number: 1, Title: "Base feature", Body: "Foundation", BlockedBy: []int{99}},
		{Number: 2, Title: "Extension A", Body: "Depends on base", BlockedBy: []int{1}},
		{Number: 3, Title: "Extension B", Body: "Depends on A", BlockedBy: []int{2}},
	}
	result := a.Assess(issues)
	if result.Strategy != StrategySequential {
		t.Errorf("all-deps strategy = %s, want sequential", result.Strategy)
	}
}

func TestAssessor_MixedDependencies(t *testing.T) {
	a := NewAssessor()
	issues := []IssueInput{
		{Number: 1, Title: "Base feature", Body: "Foundation"},
		{Number: 2, Title: "Independent fix", Body: "No deps"},
		{Number: 3, Title: "Depends on base", Body: "Extension", BlockedBy: []int{1}},
		{Number: 4, Title: "Another independent", Body: "No deps"},
	}
	result := a.Assess(issues)
	if result.Strategy != StrategyMixed {
		t.Errorf("mixed deps strategy = %s, want mixed", result.Strategy)
	}
}

func TestAssessor_CostEstimates(t *testing.T) {
	a := NewAssessor()
	issues := []IssueInput{
		{Number: 1, Title: "Fix typo", Body: "Simple"},
	}
	result := a.Assess(issues)
	if result.EstimatedCostUSD <= 0 {
		t.Errorf("cost = %f, want > 0", result.EstimatedCostUSD)
	}
	if result.EstimatedMinutes <= 0 {
		t.Errorf("minutes = %f, want > 0", result.EstimatedMinutes)
	}
	if len(result.IssueAssessments) != 1 {
		t.Errorf("assessments = %d, want 1", len(result.IssueAssessments))
	}
}

// TestRecommendModel_ResolvesCurrentBandModel asserts the score→BAND mapping
// and that each band resolves through the registry — deliberately not a pinned
// concrete id. The previous version of this test pinned ids and had to be
// chased forward on every model release; by 2026-07 it was asserting a
// deprecated sonnet and a superseded opus, which is how the drift in #74 went
// unnoticed. A test that pins the answer cannot detect a stale answer.
func TestRecommendModel_ResolvesCurrentBandModel(t *testing.T) {
	cases := []struct {
		score int
		tier  string
	}{
		{1, "haiku"},
		{3, "haiku"},
		{5, "sonnet"},
		{6, "sonnet"},
		{7, "opus"},
		{10, "opus"},
	}
	for _, tc := range cases {
		want, ok := models.Get(tc.tier)
		if !ok {
			t.Fatalf("registry has no non-deprecated model for tier %q", tc.tier)
		}
		got := recommendModel(tc.score)
		if got != want.ID {
			t.Errorf("recommendModel(%d) = %q, want %q (current %s band)", tc.score, got, want.ID, tc.tier)
		}
		if want.Deprecated {
			t.Errorf("tier %q resolves to deprecated model %q", tc.tier, want.ID)
		}
	}
}
