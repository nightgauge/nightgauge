package orchestrator

import (
	"strings"
	"testing"
)

func readinessCodes(rs []SkipReason) []string {
	out := []string{}
	for _, r := range rs {
		out = append(out, r.Code)
	}
	return out
}

func TestDispatchSkipReasons(t *testing.T) {
	target := ReadinessTarget{Repo: "acme/app", Number: 7, Project: 3}
	cases := []struct {
		name string
		in   ReadinessInput
		want []string
	}{
		{"ready and refined", ReadinessInput{OnBoard: true, BoardStatus: "Ready", Labels: []string{"pipeline:refined"}}, nil},
		// Tier 1: refined before dispatch, so unrefined is not a reason.
		{"ready unrefined", ReadinessInput{OnBoard: true, BoardStatus: "Ready"}, nil},
		{"off board unrefined", ReadinessInput{}, []string{SkipNotOnBoard, SkipBacklogRefinementOff}},
		{"off board refined", ReadinessInput{Labels: []string{"pipeline:refined"}}, []string{SkipNotOnBoard}},
		{"off board, backlog sweep on", ReadinessInput{RefinementBacklog: true}, []string{SkipNotOnBoard}},
		{"backlog, pickup off", ReadinessInput{OnBoard: true, BoardStatus: "Backlog", Priority: "P1"}, []string{SkipBoardStatus}},
		{"backlog, pickup on", ReadinessInput{OnBoard: true, BoardStatus: "Backlog", Priority: "P1", PickupBacklog: true}, nil},
		{"unprioritized backlog unrefined", ReadinessInput{OnBoard: true, BoardStatus: "Backlog", PickupBacklog: true}, []string{SkipBacklogRefinementOff}},
		{"auto-process exempt", ReadinessInput{OnBoard: true, BoardStatus: "Backlog", PickupBacklog: true, Labels: []string{"auto-process"}}, nil},
		{"in progress", ReadinessInput{OnBoard: true, BoardStatus: "In progress", Labels: []string{"pipeline:refined"}}, []string{SkipBoardStatus}},
		{"default excluded label", ReadinessInput{OnBoard: true, BoardStatus: "Ready", Labels: []string{"Owner-Action"}}, []string{SkipExcludedLabel}},
		{"configured excluded label", ReadinessInput{OnBoard: true, BoardStatus: "Ready", Labels: []string{"hold"}, ExcludeLabels: []string{"hold"}}, []string{SkipExcludedLabel}},
		{"epic", ReadinessInput{OnBoard: true, BoardStatus: "Ready", Labels: []string{"type:epic"}}, []string{SkipEpic}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := readinessCodes(DispatchSkipReasons(tc.in, target))
			if strings.Join(got, ",") != strings.Join(tc.want, ",") {
				t.Fatalf("codes = %v, want %v", got, tc.want)
			}
		})
	}
}

// Every reason carries a runnable remedy naming the issue, repo and board.
func TestDispatchSkipReasons_Remedies(t *testing.T) {
	rs := DispatchSkipReasons(ReadinessInput{Labels: []string{"blocked"}}, ReadinessTarget{Repo: "acme/app", Number: 7, Project: 3})
	if len(rs) != 3 {
		t.Fatalf("reasons = %v", rs)
	}
	want := map[string]string{
		SkipExcludedLabel:        `gh issue edit 7 --repo acme/app --remove-label "blocked"`,
		SkipNotOnBoard:           "nightgauge project add 7 --repo acme/app --project 3 --status Ready",
		SkipBacklogRefinementOff: "nightgauge issue mark-refined 7 --repo acme/app",
	}
	for _, r := range rs {
		if r.Remedy != want[r.Code] {
			t.Errorf("%s remedy = %q, want %q", r.Code, r.Remedy, want[r.Code])
		}
		if r.Detail == "" {
			t.Errorf("%s has no detail", r.Code)
		}
	}
}

// The codes shared with the refinement scan are the scan's own class names,
// so the CLI and the scheduler log name a skip identically.
func TestDispatchSkipReasons_CodesAreSchedulerClasses(t *testing.T) {
	if SkipBoardStatus != refinementSkipBoardStatus || SkipExcludedLabel != refinementSkipExcludedLabel ||
		SkipBacklogRefinementOff != refinementSkipBacklogOff {
		t.Fatal("dispatch-readiness codes drifted from the refinement skip classes")
	}
}
