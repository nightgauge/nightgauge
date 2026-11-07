package orchestrator

import (
	"context"
	"testing"

	"github.com/nightgauge/nightgauge/internal/depgraph"
	"github.com/nightgauge/nightgauge/internal/state"
)

// Regression suite for #2878: a run that resumes at pr-merge and merges nothing
// parks at once, and a labelled or Backlog issue is not re-admitted.

const noOpRepo, noOpIssue = "acme/dashboard", 827

func noOpRun(as *AutonomousScheduler) {
	addRunning(as, noOpRepo, noOpIssue, "Partly delivered")
	as.NotifyComplete(noOpRepo, noOpIssue, true, false, TerminalKindResumeNoOp, "")
	as.drainBackground()
}

// TestResumeNoOp_Parks: the no-op parks with a hold the reconcile keeps, and no
// candidate comes back.
func TestResumeNoOp_Parks(t *testing.T) {
	as := heldTestScheduler(t)

	noOpRun(as)
	if len(as.state.Failed) != 1 || as.state.Failed[0].Kind != TerminalKindResumeNoOp {
		t.Fatalf("failed=%+v, want one %s entry", as.state.Failed, TerminalKindResumeNoOp)
	}
	if got := as.humanHoldFor(noOpRepo, noOpIssue); got != HoldOperatorResume {
		t.Errorf("hold = %q, want %q", got, HoldOperatorResume)
	}
	g := holdTestGraph(&depgraph.Node{Repo: noOpRepo, Number: noOpIssue, State: "OPEN", BoardStatus: "Ready"})
	as.reconcileStateAgainstGraph(g)
	if len(as.state.Failed) != 1 {
		t.Fatal("the reconcile re-admitted a parked no-op")
	}
	if isCandidate(as.prioritize(context.Background(), g), noOpRepo, noOpIssue) {
		t.Error("a parked no-op is still a dispatch candidate")
	}
}

// TestReconcile_ParkedOnBoardIsNotReadmitted covers both lists: an excluded
// label or Backlog keeps the entry, and lifting the park re-admits it.
func TestReconcile_ParkedOnBoardIsNotReadmitted(t *testing.T) {
	cases := []struct {
		name   string
		node   depgraph.Node
		parked bool
	}{
		{"blocked label", depgraph.Node{Labels: []string{"blocked"}, BoardStatus: "Ready"}, true},
		{"Backlog", depgraph.Node{BoardStatus: "Backlog"}, true},
		{"park lifted", depgraph.Node{BoardStatus: "Ready"}, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			as := heldTestScheduler(t)
			as.state.Completed = []CompletedItem{{Repo: noOpRepo, Number: noOpIssue}}
			as.state.Failed = []FailedItem{{Repo: noOpRepo, Number: noOpIssue + 1}}
			done, failed := tc.node, tc.node
			done.Repo, done.Number, done.State = noOpRepo, noOpIssue, "OPEN"
			failed.Repo, failed.Number, failed.State = noOpRepo, noOpIssue+1, "OPEN"
			as.reconcileStateAgainstGraph(buildTestGraph([]*depgraph.Node{&done, &failed}, nil))
			if got := len(as.state.Completed) == 1; got != tc.parked {
				t.Errorf("completed kept = %v, want %v", got, tc.parked)
			}
			if got := len(as.state.Failed) == 1; got != tc.parked {
				t.Errorf("failed kept = %v, want %v", got, tc.parked)
			}
		})
	}
}

// TestResumeNoOpKind: only a resumed run that merged nothing and left the issue
// open reports the kind; a full run with an externally merged PR does not.
func TestResumeNoOpKind(t *testing.T) {
	run := func(resumed, withheld bool) *state.RuntimeState {
		rs := &state.RuntimeState{}
		if resumed {
			rs.SetResumedAtMerge()
		}
		rs.SetMergeNoOp()
		if withheld {
			rs.SetReadyWithheld()
		}
		return rs
	}
	cases := []struct {
		name    string
		runtime *state.RuntimeState
		success bool
		want    string
	}{
		{"resumed, left open", run(true, true), true, TerminalKindResumeNoOp},
		{"failed run", run(true, true), false, ""},
		{"resumed, issue closed", run(true, false), true, ""},
		{"full run, merged elsewhere", run(false, true), true, ""},
		{"no runtime", nil, true, ""},
	}
	for _, c := range cases {
		if got := resumeNoOpKind(c.runtime, c.success); got != c.want {
			t.Errorf("%s: kind = %q, want %q", c.name, got, c.want)
		}
	}
}

// TestWithheldCompletionStatus: a withheld partial delivery never lands in In
// Review, and a closed issue stays Done.
func TestWithheldCompletionStatus(t *testing.T) {
	if got := withheldCompletionStatus(state.StatusInReview, true); got != state.StatusBacklog {
		t.Errorf("withheld = %s, want Backlog", got)
	}
	if got := withheldCompletionStatus(state.StatusDone, true); got != state.StatusDone {
		t.Errorf("closed = %s, want Done", got)
	}
	if got := withheldCompletionStatus(state.StatusInReview, false); got != state.StatusInReview {
		t.Errorf("not withheld = %s, want In Review", got)
	}
}
