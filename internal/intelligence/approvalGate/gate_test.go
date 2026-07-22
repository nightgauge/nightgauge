package approvalGate

import (
	"strings"
	"testing"
)

func TestCountTradeoffSignals(t *testing.T) {
	cases := []struct {
		text string
		want int
	}{
		{"Just a normal bugfix, nothing architectural.", 0},
		{"We must weigh consistency vs availability here.", 1},
		{"This is a build vs buy and reuse vs build decision.", 2},
		{"consistency vs availability, sync vs async, sql vs nosql", 3},
		// Repeating the same phrase does not inflate the distinct count.
		{"build vs buy build vs buy build vs buy", 1},
	}
	for _, c := range cases {
		if got := CountTradeoffSignals(c.text); got != c.want {
			t.Errorf("CountTradeoffSignals(%q) = %d, want %d", c.text, got, c.want)
		}
	}
}

func TestEvaluate(t *testing.T) {
	cases := []struct {
		name         string
		in           ApprovalInput
		wantHigh     bool
		wantRequires bool
	}{
		{"low impact → no approval needed", ApprovalInput{TradeoffKeywordHits: 1, RiskHigh: false}, false, false},
		{"2 tradeoffs, unapproved → requires", ApprovalInput{TradeoffKeywordHits: 2}, true, true},
		{"2 tradeoffs, approved → no block", ApprovalInput{TradeoffKeywordHits: 2, ApprovalGranted: true}, true, false},
		{"risk_high alone, unapproved → requires", ApprovalInput{RiskHigh: true}, true, true},
		{"risk_high, approved → no block", ApprovalInput{RiskHigh: true, ApprovalGranted: true}, true, false},
		{"neither → not high-impact", ApprovalInput{TradeoffKeywordHits: 0, RiskHigh: false}, false, false},
		// #4135 — dependency major bump + production-change triggers.
		{"1 major bump, unapproved → requires", ApprovalInput{DependencyMajorBumpCount: 1}, true, true},
		{"3 major bumps, approved → no block", ApprovalInput{DependencyMajorBumpCount: 3, ApprovalGranted: true}, true, false},
		{"0 major bumps → not high-impact (no over-fire)", ApprovalInput{DependencyMajorBumpCount: 0}, false, false},
		{"production change, unapproved → requires", ApprovalInput{IsProductionChange: true}, true, true},
		{"production change, approved → no block", ApprovalInput{IsProductionChange: true, ApprovalGranted: true}, true, false},
		{"both new triggers, unapproved → requires", ApprovalInput{DependencyMajorBumpCount: 2, IsProductionChange: true}, true, true},
		{"both new triggers, approved → no block", ApprovalInput{DependencyMajorBumpCount: 2, IsProductionChange: true, ApprovalGranted: true}, true, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := Evaluate(c.in)
			if got.HighImpact != c.wantHigh {
				t.Errorf("HighImpact = %v, want %v", got.HighImpact, c.wantHigh)
			}
			if got.RequiresApproval != c.wantRequires {
				t.Errorf("RequiresApproval = %v, want %v (reasons: %v)", got.RequiresApproval, c.wantRequires, got.Reasons)
			}
			if c.wantRequires && !strings.Contains(strings.Join(got.Reasons, " "), "not yet human-approved") {
				t.Errorf("expected an approval reason, got %v", got.Reasons)
			}
		})
	}
}

// TestEvaluateNewTriggerReasons asserts the #4135 triggers surface their own
// distinct, actionable reason text (not just a generic high-impact flag).
func TestEvaluateNewTriggerReasons(t *testing.T) {
	t.Run("dependency major bump reason", func(t *testing.T) {
		got := Evaluate(ApprovalInput{DependencyMajorBumpCount: 2})
		joined := strings.Join(got.Reasons, " ")
		if !strings.Contains(joined, "major-version bump") {
			t.Errorf("expected a major-bump reason, got %v", got.Reasons)
		}
	})
	t.Run("production change reason", func(t *testing.T) {
		got := Evaluate(ApprovalInput{IsProductionChange: true})
		joined := strings.Join(got.Reasons, " ")
		if !strings.Contains(joined, "production-touching") {
			t.Errorf("expected a production-change reason, got %v", got.Reasons)
		}
	})
}
