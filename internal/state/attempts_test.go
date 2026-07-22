package state

import (
	"testing"
	"time"
)

func TestComputeAttemptsUntilSuccess(t *testing.T) {
	cases := []struct {
		name        string
		ralph       map[string]int
		retries     int
		escalations int
		want        int
	}{
		{"clean run", nil, 0, 0, 1},
		{"one retry", nil, 1, 0, 2},
		{"retry + escalation", nil, 1, 1, 3},
		{"ralph loop of 3 on one stage", map[string]int{"feature-dev": 3}, 0, 0, 3}, // 1 + (3-1)
		{"single ralph iteration counts as no extra", map[string]int{"feature-dev": 1}, 0, 0, 1},
		{"combined", map[string]int{"feature-dev": 3, "feature-validate": 2}, 2, 1, 7}, // 1+2+1 + (2)+(1)
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := ComputeAttemptsUntilSuccess(tc.ralph, tc.retries, tc.escalations)
			if got != tc.want {
				t.Errorf("ComputeAttemptsUntilSuccess(%v, %d, %d) = %d, want %d",
					tc.ralph, tc.retries, tc.escalations, got, tc.want)
			}
		})
	}
}

func TestBuildV2RecordPopulatesAttempts(t *testing.T) {
	hw := NewHistoryWriter(t.TempDir())
	rs := NewRuntimeState("nightgauge/nightgauge", 4172, "item-attempts")
	rs.BeginStage(StageFeatureDev)
	rs.CompleteStage(0, 8000, 6000, "")

	rs.RetryCount = 2
	rs.EscalationHistory = []EscalationRecord{{}, {}} // 2 escalations
	rs.RalphIterations = map[string]int{string(StageFeatureDev): 3}

	rec := hw.BuildV2Record(rs, true, "", V2RunInput{Title: "attempts", Branch: "feat/4172"}, time.Now())

	// run-level: 1 + 2 retries + 2 escalations + (3-1) extra ralph = 7
	if rec.AttemptsUntilSuccess != 7 {
		t.Errorf("run AttemptsUntilSuccess = %d, want 7", rec.AttemptsUntilSuccess)
	}
	// per-stage: feature-dev had 3 Ralph iterations
	if got := rec.Stages[string(StageFeatureDev)].AttemptsUntilSuccess; got != 3 {
		t.Errorf("feature-dev AttemptsUntilSuccess = %d, want 3", got)
	}
	// QualityScore is nil for an ungraded run.
	if rec.QualityScore != nil {
		t.Errorf("QualityScore = %v, want nil for ungraded run", *rec.QualityScore)
	}
}
