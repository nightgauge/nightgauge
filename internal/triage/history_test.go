package triage

import (
	"strings"
	"testing"

	gh "github.com/nightgauge/nightgauge/internal/github"
)

func run(created, conclusion string) gh.WorkflowRun {
	return gh.WorkflowRun{CreatedAt: created, Conclusion: conclusion, Status: "completed"}
}

// TestSummarizeHistory_NeverPassed is the case that misled two sessions: a
// check that has failed on every run since the day it was added. The verdict
// must say so, and must not read as a regression.
func TestSummarizeHistory_NeverPassed(t *testing.T) {
	got := SummarizeHistory("e2e.yml", "main", []gh.WorkflowRun{
		run("2026-07-28T02:00:00Z", "failure"),
		run("2026-07-29T02:00:00Z", "failure"),
		run("2026-07-30T02:00:00Z", "failure"),
	})
	if got.EverPassed {
		t.Fatal("EverPassed must be false")
	}
	if !strings.Contains(got.Verdict, "never passed") {
		t.Errorf("verdict = %q, want it to say never passed", got.Verdict)
	}
	if strings.Contains(strings.ToLower(got.Verdict), "regression:") {
		t.Errorf("a never-green check must not be described as a regression: %q", got.Verdict)
	}
	if got.ConsecutiveFailures != 3 {
		t.Errorf("ConsecutiveFailures = %d, want 3", got.ConsecutiveFailures)
	}
	if got.FirstRun == nil || got.FirstRun.CreatedAt != "2026-07-28T02:00:00Z" {
		t.Errorf("FirstRun = %+v, want the oldest examined run", got.FirstRun)
	}
}

func TestSummarizeHistory_Regression(t *testing.T) {
	got := SummarizeHistory("ci.yml", "main", []gh.WorkflowRun{
		run("2026-08-01T02:00:00Z", "success"),
		run("2026-08-02T02:00:00Z", "failure"),
		run("2026-08-03T02:00:00Z", "failure"),
	})
	if !got.EverPassed || got.ConsecutiveFailures != 2 {
		t.Fatalf("summary = %+v", got)
	}
	if !strings.HasPrefix(got.Verdict, "regression:") {
		t.Errorf("verdict = %q", got.Verdict)
	}
}

// TestSummarizeHistory_SortsByCreatedAt — "consecutive failures counted back
// from the newest run" is wrong in exactly the silent way if the slice arrives
// oldest-first, so the summarizer sorts rather than trusting its caller.
func TestSummarizeHistory_SortsByCreatedAt(t *testing.T) {
	reversed := SummarizeHistory("ci.yml", "main", []gh.WorkflowRun{
		run("2026-08-03T02:00:00Z", "failure"),
		run("2026-08-02T02:00:00Z", "failure"),
		run("2026-08-01T02:00:00Z", "success"),
	})
	if reversed.ConsecutiveFailures != 2 || !reversed.EverPassed {
		t.Fatalf("summary = %+v, want the same reading regardless of input order", reversed)
	}
}

func TestSummarizeHistory_NewestRunPassed(t *testing.T) {
	got := SummarizeHistory("ci.yml", "", []gh.WorkflowRun{
		run("2026-08-01T02:00:00Z", "failure"),
		run("2026-08-02T02:00:00Z", "success"),
	})
	if got.ConsecutiveFailures != 0 || !strings.Contains(got.Verdict, "still current") {
		t.Fatalf("summary = %+v", got)
	}
}

// TestSummarizeHistory_IgnoresInFlightRuns — a queued run has no verdict, and
// counting it as a failure would invent one.
func TestSummarizeHistory_IgnoresInFlightRuns(t *testing.T) {
	got := SummarizeHistory("ci.yml", "", []gh.WorkflowRun{
		run("2026-08-01T02:00:00Z", "success"),
		{CreatedAt: "2026-08-02T02:00:00Z", Status: "in_progress"},
	})
	if got.Examined != 1 || got.ConsecutiveFailures != 0 {
		t.Fatalf("summary = %+v", got)
	}
}

func TestSummarizeHistory_NoRuns(t *testing.T) {
	got := SummarizeHistory("ci.yml", "", nil)
	if got.EverPassed || !strings.Contains(got.Verdict, "no completed runs") {
		t.Fatalf("summary = %+v", got)
	}
	if h := got.ToRecordHistory(); h.Checked {
		t.Error("an empty window has not answered the question")
	}
}

func TestToRecordHistory(t *testing.T) {
	h := SummarizeHistory("e2e.yml", "main", []gh.WorkflowRun{run("2026-07-28T02:00:00Z", "failure")}).ToRecordHistory()
	if !h.Checked || h.EverPassed || h.Detail == "" {
		t.Fatalf("history = %+v", h)
	}
}
