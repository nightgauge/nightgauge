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

// TestEstimateIssueCost_PricesThroughRegistry pins the switch→registry cutover
// (#1274). The literal-id switch it replaced carried cases for the deprecated
// opus ids and claude-fable-5 only, so every model registered after it was
// written — claude-opus-5, claude-sonnet-5, claude-fable-5-1 — silently fell to
// the $0.10 unknown default, and the frontier tier estimated CHEAPER than a
// superseded opus. Switching the lookup back to the literal switch makes the
// first assertion fail.
func TestEstimateIssueCost_PricesThroughRegistry(t *testing.T) {
	fable51 := estimateIssueCost(1, "claude-fable-5-1")
	opus5 := estimateIssueCost(1, "claude-opus-5")
	sonnet5 := estimateIssueCost(1, "claude-sonnet-5")
	haiku := estimateIssueCost(1, "claude-haiku-4-5-20251001")

	if fable51 <= opus5 {
		t.Errorf("estimateIssueCost(1, claude-fable-5-1) = %f, want > claude-opus-5 (%f): "+
			"fable bills 2x opus per token, so the frontier tier must never estimate cheaper",
			fable51, opus5)
	}
	if opus5 <= sonnet5 {
		t.Errorf("estimateIssueCost(1, claude-opus-5) = %f, want > claude-sonnet-5 (%f)", opus5, sonnet5)
	}
	if sonnet5 <= haiku {
		t.Errorf("estimateIssueCost(1, claude-sonnet-5) = %f, want > claude-haiku-4-5-20251001 (%f)",
			sonnet5, haiku)
	}

	// A model the registry does not price still falls to the unknown default —
	// local ids have no entry by design and must not be priced at $0.
	wantDefault := unknownModelStageCost * 6
	if got := estimateIssueCost(1, "llama3.1:70b"); !floatsClose(got, wantDefault) {
		t.Errorf("estimateIssueCost(1, unregistered) = %f, want %f (the unknown-model default)",
			got, wantDefault)
	}

	// Complexity still scales the estimate linearly on top of the rate card.
	if got, want := estimateIssueCost(3, "claude-opus-5"), opus5*1.30; !floatsClose(got, want) {
		t.Errorf("estimateIssueCost(3, claude-opus-5) = %f, want %f (1 + 2*0.15 of the score-1 cost)",
			got, want)
	}
}

// TestEstimateIssueCost_MatchesRegistryRates proves the number is DERIVED, not
// a second hand-maintained table: it is the registry's own per-MTok rates
// applied to the nominal stage mix. A rate edit in the registry moves this
// estimate on the same commit, with no case to remember.
func TestEstimateIssueCost_MatchesRegistryRates(t *testing.T) {
	for _, id := range []string{"claude-fable-5-1", "claude-opus-5", "claude-sonnet-5"} {
		m, ok := models.Get(id)
		if !ok {
			t.Fatalf("%s missing from registry", id)
		}
		want := (m.Rates.Input*nominalStageInputMTok + m.Rates.Output*nominalStageOutputMTok) * 6
		if got := estimateIssueCost(1, id); !floatsClose(got, want) {
			t.Errorf("estimateIssueCost(1, %s) = %f, want %f (registry rates x nominal stage mix x 6 stages)",
				id, got, want)
		}
	}
}

func floatsClose(a, b float64) bool {
	diff := a - b
	if diff < 0 {
		diff = -diff
	}
	return diff < 1e-9
}
