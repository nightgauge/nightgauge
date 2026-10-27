package github_test

import (
	"context"
	"slices"
	"strings"
	"testing"

	gh "github.com/nightgauge/nightgauge/internal/github"
	"github.com/nightgauge/nightgauge/internal/github/githubtest"
)

// legacyOrphanClose is the comment the pre-#2602 orphan closer left on every
// sub-issue it closed on the epic's state alone.
const legacyOrphanClose = "Auto-closed: this sub-issue's work shipped as part of epic #10, which merged and closed as completed. " +
	"Closing the orphaned sub-issue so the autonomous picker does not re-spawn conflicting work. Reopen if scope remains."

// TestEpicRollup_OrphanClosedSubHoldsReopenedEpic is #2602's rollup half: a
// human reopened epic #10 after the old orphan closer closed its unstarted
// sub-issue #11. Every sub-issue reads CLOSED, but the rollup must not close
// the epic again unless a pull request has closed #11 since, or its close
// carries evidence.
func TestEpicRollup_OrphanClosedSubHoldsReopenedEpic(t *testing.T) {
	tests := []struct {
		name       string
		sub        githubtest.Issue
		wantStatus string
		wantReason string
		wantMut    []string
	}{
		{
			name:       "legacy orphan close holds the epic",
			sub:        githubtest.Issue{State: "CLOSED", Comments: []string{legacyOrphanClose}},
			wantStatus: "skipped", wantReason: "orphan_closed_without_evidence",
		},
		{
			name: "evidenced orphan close does not",
			sub: githubtest.Issue{State: "CLOSED", Comments: []string{
				"Auto-closed: this sub-issue's work shipped in #61.\n\n<!-- nightgauge:orphan-close evidence=pr:#61 -->",
			}},
			wantStatus: "closed", wantReason: "all_closed", wantMut: []string{"closeIssue", "addComment"},
		},
		{
			name: "a later PR close does not",
			sub: githubtest.Issue{State: "CLOSED", Comments: []string{legacyOrphanClose},
				ClosedByPR: 62, ClosedByHead: "feat/11-thing"},
			wantStatus: "closed", wantReason: "all_closed", wantMut: []string{"closeIssue", "addComment"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			forge := githubtest.New(t, map[int]githubtest.Issue{
				10: {SubIssues: []int{11, 12}},
				11: tt.sub,
				12: {State: "CLOSED"},
			})
			svc := gh.NewEpicService(forge.Client())
			svc.SetCheckoutResolver(noBranchResolver(t))

			res, err := svc.AutoCloseSingle(context.Background(), gh.EpicRef{Owner: githubtest.Owner, Repo: githubtest.Repo, Number: 10}, 0)
			if err != nil {
				t.Fatal(err)
			}
			if res.Status != tt.wantStatus || res.Reason != tt.wantReason {
				t.Errorf("status/reason = %s/%s (%s), want %s/%s", res.Status, res.Reason, res.Error, tt.wantStatus, tt.wantReason)
			}
			if got := forge.Mutations(); !slices.Equal(got, tt.wantMut) {
				t.Errorf("mutations = %v, want %v", got, tt.wantMut)
			}
		})
	}
}

// TestReconcileBoard_FlagsOrphansInsteadOfClosing drives the board sweep over
// both halves of #2602: closed epic #20 (closed by a slice PR) leaves its
// unstarted sub-issue #21 open and flagged, and reopened epic #10, whose
// sub-issue #11 the old closer closed, is held open and flagged.
func TestReconcileBoard_FlagsOrphansInsteadOfClosing(t *testing.T) {
	forge := githubtest.New(t, map[int]githubtest.Issue{
		10: {SubIssues: []int{11}, Labels: []string{"type:epic"}, Status: "In progress"},
		11: {State: "CLOSED", Comments: []string{legacyOrphanClose}},
		20: {
			State: "CLOSED", StateReason: "COMPLETED", SubIssues: []int{21},
			Labels: []string{"type:epic"}, Status: "Done",
			ClosedByPR: 50, ClosedByHead: "feat/123-slice",
		},
		21: {State: "OPEN"},
	})
	svc := gh.NewEpicService(forge.Client())
	svc.SetCheckoutResolver(noBranchResolver(t))

	res, err := svc.ReconcileBoard(context.Background(), githubtest.Owner, 7)
	if err != nil {
		t.Fatal(err)
	}
	if res.EpicsClosed != 0 || res.OrphanSubsClosed != 0 || res.OrphanSubsFlagged != 1 {
		t.Errorf("result = %+v, want nothing closed and one orphan flagged", res)
	}
	var flaggedSub, flaggedEpic bool
	for _, a := range res.Actions {
		if a.Kind != "orphan_sub_flagged" {
			continue
		}
		switch {
		case a.Number == 21 && a.Epic == 20:
			flaggedSub = true
		case a.Number == 10 && a.Epic == 0:
			flaggedEpic = true
		}
	}
	if !flaggedSub || !flaggedEpic {
		t.Errorf("actions = %+v, want #21 (epic #20) and epic #10 flagged", res.Actions)
	}
	if !slices.ContainsFunc(res.Warnings, func(w string) bool { return strings.Contains(w, "epic #10: left open") }) {
		t.Errorf("warnings = %v, want epic #10 reported as held open", res.Warnings)
	}
	if slices.Contains(forge.Mutations(), "closeIssue") {
		t.Errorf("mutations = %v, want no issue closed", forge.Mutations())
	}
}
