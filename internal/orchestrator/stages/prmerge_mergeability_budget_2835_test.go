package stages

import (
	"testing"
	"time"

	"github.com/nightgauge/nightgauge/internal/github"
)

// TestMergeabilityScheduleMatchesWaitBudget is #2835: the CLI timeout budgets
// github.MergeabilityWaitBudget for the mergeability wait, so the runner's real
// schedule must sum to exactly that.
func TestMergeabilityScheduleMatchesWaitBudget(t *testing.T) {
	r := NewDeterministicRunner()
	var total time.Duration
	for n := 0; n < r.mergeabilityPollMax; n++ {
		total += r.mergeabilityDelay(n)
	}
	if want := github.MergeabilityWaitBudget(); total != want {
		t.Fatalf("runner schedule sums to %v, budget is %v", total, want)
	}
}
