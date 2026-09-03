package orchestrator

import (
	"context"
	"strings"
	"testing"

	"github.com/nightgauge/nightgauge/internal/depgraph"
)

// TestPrioritize_RefusesItemWithTruncatedLabels encodes the fail-closed
// polarity of #998 at the board-scan entry point: a node whose label list is
// known to be incomplete is NOT dispatched, even though none of its VISIBLE
// labels is excluded. The exclusion cannot be evaluated on a partial set, so
// the answer is "excluded" — the same polarity the refinement scan chose in
// #993 — not "nothing matched".
func TestPrioritize_RefusesItemWithTruncatedLabels(t *testing.T) {
	nodes := []*depgraph.Node{
		{Repo: "R", Number: 1, Title: "Heavily labelled", State: "OPEN", BoardStatus: "Ready",
			Labels: []string{"type:bug", "priority:high"}, LabelsTruncated: true, Priority: "P0", Size: "XS", Weight: 1},
		{Repo: "R", Number: 2, Title: "Regular", State: "OPEN", BoardStatus: "Ready", Priority: "P1", Size: "M", Weight: 3},
	}
	g := buildTestGraph(nodes, nil)

	as := &AutonomousScheduler{
		config: AutonomousConfig{MaxConcurrent: 5},
		state:  &AutonomousState{},
	}

	var candidates []CandidateItem
	out := captureLog(t, func() {
		candidates = as.prioritize(context.Background(), g)
	})
	if len(candidates) != 1 {
		t.Fatalf("expected 1 candidate (truncated-label item refused), got %d: %+v", len(candidates), candidates)
	}
	if candidates[0].Number != 2 {
		t.Errorf("expected #2, got #%d", candidates[0].Number)
	}
	if !strings.Contains(out, "R#1") || !strings.Contains(out, "truncated") {
		t.Errorf("refusal is not visible in the log: %q", out)
	}
}

// TestDispatch_RefusesItemWithTruncatedLabels is the same polarity at the
// dequeue chokepoint every enqueue route funnels through (#1146): an item
// flagged LabelsTruncated is held with a machine-readable reason, not
// dispatched, even when its visible labels are all harmless.
func TestDispatch_RefusesItemWithTruncatedLabels(t *testing.T) {
	s := newDequeueExcludeFixture(t, []string{"owner-action"})
	s.QueueAddItem(
		QueueItem{Repo: "Org/repo", IssueNumber: 401, Title: "Heavily labelled", Labels: []string{"type:bug"}, LabelsTruncated: true},
		QueueItem{Repo: "Org/repo", IssueNumber: 402, Title: "Ordinary work", Labels: []string{"type:bug"}},
	)

	got := s.DequeueIndependent(t.Context(), 5, nil)
	for _, item := range got {
		if item.IssueNumber == 401 {
			t.Fatal("dequeued #401 for dispatch although its label list is known to be incomplete")
		}
	}
	if len(got) != 1 || got[0].IssueNumber != 402 {
		t.Fatalf("dequeued %+v, want exactly #402", got)
	}

	var held *QueueItem
	for i := range s.queue {
		if s.queue[i].IssueNumber == 401 {
			held = &s.queue[i]
		}
	}
	if held == nil {
		t.Fatal("#401 was removed from the queue; want it held as paused")
	}
	if held.Status != "paused" {
		t.Errorf("status = %q, want paused", held.Status)
	}
	if held.PausedReason == nil || held.PausedReason.Kind != "labels_truncated" {
		t.Fatalf("PausedReason = %+v, want Kind labels_truncated", held.PausedReason)
	}
}
