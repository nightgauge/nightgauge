package orchestrator

import (
	"context"
	"testing"
	"time"

	"github.com/nightgauge/nightgauge/internal/depgraph"
)

// Tests for #3579: both Backlog→Ready sweeps hold an issue carrying a label
// in autonomous.exclude_labels, as the dispatch candidate loop does (#317).

func excludedTestNode(number int, labels ...string) *depgraph.Node {
	return &depgraph.Node{
		Repo: "nightgauge/nightgauge", Number: number, State: "OPEN",
		BoardStatus: "Backlog", Priority: "P1",
		Labels: append([]string{"type:bug"}, labels...), AuthorLogin: "maintainer",
	}
}

type promoteSweep struct {
	name string
	run  func(as *AutonomousScheduler)
}

var promoteSweeps = []promoteSweep{
	{"startup", func(as *AutonomousScheduler) { as.promoteUnblockedOnStartup(context.Background()) }},
	{"cascade", func(as *AutonomousScheduler) {
		as.promoteUnblockedToReady(as.backgroundContext(), "nightgauge/nightgauge", 1)
		as.drainBackground()
	}},
}

func runSweep(t *testing.T, sw promoteSweep, exclude []string, nodes ...*depgraph.Node) *AutonomousScheduler {
	t.Helper()
	as := newAutonomousForCascadeTest(t, 3, 30*time.Minute)
	as.config.ExcludeLabels = exclude
	completed := &depgraph.Node{Repo: "nightgauge/nightgauge", Number: 1, State: "CLOSED", BoardStatus: "Done"}
	all := append([]*depgraph.Node{completed}, nodes...)
	as.buildGraphFn = func(_ context.Context) (*depgraph.Graph, error) {
		return buildTestGraph(all, nil), nil
	}
	sw.run(as)
	return as
}

// Serial: sets the environment (via newAutonomousForCascadeTest).
func TestPromotion_HoldsDefaultExcludedLabels(t *testing.T) {
	for _, sw := range promoteSweeps {
		t.Run(sw.name, func(t *testing.T) {
			as := runSweep(t, sw, nil,
				excludedTestNode(10, "Blocked"), excludedTestNode(11, "owner-action"), excludedTestNode(12))
			if got := as.state.LastPromotionEligible; got != 1 {
				t.Errorf("LastPromotionEligible = %d, want 1 (only the control node)", got)
			}
			if got := as.state.LastPromotionRejectionReasons[promotionRejectExcludedLabel]; got != 2 {
				t.Errorf("excluded-label rejections = %d, want 2", got)
			}
		})
	}
}

// Serial: sets the environment (via newAutonomousForCascadeTest).
func TestPromotion_ConfiguredExcludeLabelsReplaceDefault(t *testing.T) {
	for _, sw := range promoteSweeps {
		t.Run(sw.name, func(t *testing.T) {
			as := runSweep(t, sw, []string{"needs-human"},
				excludedTestNode(20, "NEEDS-HUMAN"), excludedTestNode(21, "blocked"))
			if got := as.state.LastPromotionEligible; got != 1 {
				t.Errorf("LastPromotionEligible = %d, want 1 (blocked-only node promotable under a configured list)", got)
			}
			if got := as.state.LastPromotionRejectionReasons[promotionRejectExcludedLabel]; got != 1 {
				t.Errorf("excluded-label rejections = %d, want 1", got)
			}
		})
	}
}

func TestPromotionExcluded(t *testing.T) {
	t.Parallel()
	closed := excludedTestNode(1, "blocked")
	closed.State = "CLOSED"
	ready := excludedTestNode(2, "blocked")
	ready.BoardStatus = "Ready"
	cases := []struct {
		name    string
		node    *depgraph.Node
		exclude []string
		want    bool
	}{
		{"nil", nil, nil, false},
		{"closed", closed, nil, false},
		{"not backlog", ready, nil, false},
		{"no excluded label", excludedTestNode(3), nil, false},
		{"case-insensitive match", excludedTestNode(4, "OWNER-ACTION"), nil, true},
		{"configured list replaces default", excludedTestNode(5, "blocked"), []string{"needs-human"}, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := promotionExcluded(tc.node, tc.exclude); got != tc.want {
				t.Errorf("promotionExcluded = %v, want %v", got, tc.want)
			}
		})
	}
}
