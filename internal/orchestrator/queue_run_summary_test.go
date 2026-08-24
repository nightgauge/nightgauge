package orchestrator

import (
	"context"
	"strings"
	"testing"

	"github.com/nightgauge/nightgauge/pkg/types"
)

// TestQueueRunSummary_ExitStatusAndReport pins the two halves of #875's first
// acceptance criterion: a queue pass in which nothing succeeded must not report
// success, and it must say what happened.
//
// The observed run's ENTIRE visible output was "Processing 1 queued issues..."
// followed by exit 0 — which a script and a first-time user both read as
// success — on a run whose only issue terminated in failure.
func TestQueueRunSummary_ExitStatusAndReport(t *testing.T) {
	tests := []struct {
		name        string
		summary     QueueRunSummary
		wantFailure bool
		wantLines   []string
	}{
		{
			name:        "empty run reports nothing and is not a failure",
			summary:     QueueRunSummary{},
			wantFailure: false,
		},
		{
			name: "the observed #875 run: one issue, terminal failure",
			summary: QueueRunSummary{Outcomes: []QueueOutcome{
				{Repo: "o/r", IssueNumber: 12, Kind: QueueOutcomeFailed, TerminalKind: "validation_error"},
			}},
			wantFailure: true,
			wantLines:   []string{"0 of 1 queued issues completed.", "o/r#12: failed (validation_error)"},
		},
		{
			name: "a failure among successes still fails the run and is named",
			summary: QueueRunSummary{Outcomes: []QueueOutcome{
				{Repo: "o/r", IssueNumber: 1, Kind: QueueOutcomeCompleted},
				{Repo: "o/r", IssueNumber: 2, Kind: QueueOutcomeFailed, TerminalKind: "stall_kill"},
				{Repo: "o/r", IssueNumber: 3, Kind: QueueOutcomeCompleted},
			}},
			wantFailure: true,
			wantLines:   []string{"2 of 3 queued issues completed.", "o/r#2: failed (stall_kill)"},
		},
		{
			name: "an issue that was never dispatched is not a success either",
			summary: QueueRunSummary{Outcomes: []QueueOutcome{
				{Repo: "o/r", IssueNumber: 7, Kind: QueueOutcomeNotDispatched, Detail: "not found on project board"},
			}},
			wantFailure: true,
			wantLines:   []string{"o/r#7: not-dispatched — not found on project board"},
		},
		{
			name: "a dependency hold is the queue working, not a failure",
			summary: QueueRunSummary{Outcomes: []QueueOutcome{
				{Repo: "o/r", IssueNumber: 8, Kind: QueueOutcomeBlocked, Detail: "has open blockers"},
			}},
			wantFailure: false,
			wantLines:   []string{"o/r#8: blocked — has open blockers"},
		},
		{
			name: "an unclassified terminal failure says so rather than reading as an omission",
			summary: QueueRunSummary{Outcomes: []QueueOutcome{
				{Repo: "o/r", IssueNumber: 9, Kind: QueueOutcomeFailed},
			}},
			wantFailure: true,
			wantLines:   []string{"o/r#9: failed (terminal kind unclassified)"},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.summary.HasFailures(); got != tc.wantFailure {
				t.Errorf("HasFailures() = %v, want %v", got, tc.wantFailure)
			}
			out := tc.summary.Format()
			for _, want := range tc.wantLines {
				if !strings.Contains(out, want) {
					t.Errorf("Format() = %q\nwant it to contain %q", out, want)
				}
			}
			if len(tc.summary.Outcomes) == 0 && out != "" {
				t.Errorf("Format() = %q, want empty for an empty run", out)
			}
		})
	}
}

// TestQueueRunFailedError_NamesTheCounts checks the one-line form cobra prints
// to stderr. It complements, and must not duplicate, the stdout summary.
func TestQueueRunFailedError_NamesTheCounts(t *testing.T) {
	err := &QueueRunFailedError{Summary: QueueRunSummary{Outcomes: []QueueOutcome{
		{Repo: "o/r", IssueNumber: 1, Kind: QueueOutcomeCompleted},
		{Repo: "o/r", IssueNumber: 2, Kind: QueueOutcomeFailed, TerminalKind: "stall_kill"},
		{Repo: "o/r", IssueNumber: 3, Kind: QueueOutcomeNotDispatched},
	}}}
	got := err.Error()
	for _, want := range []string{"1 failed", "1 never dispatched", "of 3 queued issues"} {
		if !strings.Contains(got, want) {
			t.Errorf("Error() = %q, want it to contain %q", got, want)
		}
	}
	if strings.Contains(got, "\n") {
		t.Errorf("Error() = %q, want a single line — the per-issue detail is the stdout summary's job", got)
	}
}

// TestRunPipelineReportsItsTerminalOutcome pins the plumbing the exit status
// rests on. RunQueue cannot report what it was never told: before #875,
// runPipeline returned nothing at all, so a terminal failure was invisible to
// its own caller and `queue run` had no way to exit non-zero even in principle.
func TestRunPipelineReportsItsTerminalOutcome(t *testing.T) {
	root := t.TempDir()
	seedRefusalRepo(t, root, allRefusalStageSkills)

	runner := &firstCauseStageRunner{tail: authFailureTail}
	s := newFirstCauseScheduler(root, runner)
	ok, kind := s.runPipeline(context.Background(),
		types.BoardItem{Number: 875, Repo: "nightgauge/nightgauge", ID: "item-875"})

	if ok {
		t.Fatal("runPipeline reported success for a run that never produced a stage output context")
	}
	if kind == "" {
		t.Error("runPipeline reported an empty terminal kind — the summary would have nothing to name")
	}
}
