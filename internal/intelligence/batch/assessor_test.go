package batch

import (
	"testing"
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

func TestRecommendModel_HighComplexityReturnsOpus47(t *testing.T) {
	cases := []struct {
		score int
		want  string
	}{
		{1, "claude-haiku-4-5-20251001"},
		{3, "claude-haiku-4-5-20251001"},
		{5, "claude-sonnet-4-6"},
		{6, "claude-sonnet-4-6"},
		{7, "claude-opus-4-8"},
		{10, "claude-opus-4-8"},
	}
	for _, tc := range cases {
		got := recommendModel(tc.score)
		if got != tc.want {
			t.Errorf("recommendModel(%d) = %q, want %q", tc.score, got, tc.want)
		}
	}
}
