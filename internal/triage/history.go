package triage

import (
	"fmt"
	"sort"

	gh "github.com/nightgauge/nightgauge/internal/github"
)

// HistorySummary answers "has this check ever been green, and when?" over a
// bounded window of workflow runs.
//
// The question is separated out and made deterministic because getting it wrong
// silently reframes an entire investigation. A check that has never passed is
// not a regression: there is no "what changed" to find, and a session that
// assumes otherwise spends itself bisecting a history in which nothing was ever
// different. The nightly sweep that motivated #1262 had failed on literally
// every run since the day it was added, and two sessions treated it as
// something that broke.
type HistorySummary struct {
	// Workflow is the workflow file that was queried.
	Workflow string `json:"workflow"`
	Branch   string `json:"branch,omitempty"`
	// Examined is how many completed runs were inspected. EverPassed is only
	// ever a claim about this window, and the number is reported so the claim
	// can be read at its true strength.
	Examined int `json:"examined"`
	// EverPassed is true when at least one run in the window concluded success.
	EverPassed bool `json:"ever_passed"`
	// LastSuccess is the newest successful run, when there is one.
	LastSuccess *gh.WorkflowRun `json:"last_success,omitempty"`
	// ConsecutiveFailures counts back from the newest run.
	ConsecutiveFailures int `json:"consecutive_failures"`
	// FirstRun is the oldest run in the window — the anchor for "has never
	// passed since it was added", which is a stronger statement than "has not
	// passed in the last 30 runs" and needs the window's edge to be visible.
	FirstRun *gh.WorkflowRun `json:"first_run,omitempty"`
	// Verdict is the one-line reading, phrased so a report cannot accidentally
	// describe a never-green check as a regression.
	Verdict string `json:"verdict"`
}

// SummarizeHistory reads a window of completed runs, newest first or oldest
// first — it sorts by CreatedAt itself rather than trusting the caller's order,
// because "consecutive failures counted from the newest run" is wrong in
// exactly the silent way if the slice arrives reversed.
func SummarizeHistory(workflow, branch string, runs []gh.WorkflowRun) HistorySummary {
	sum := HistorySummary{Workflow: workflow, Branch: branch}

	completed := make([]gh.WorkflowRun, 0, len(runs))
	for _, r := range runs {
		if r.Conclusion == "" {
			continue // still in flight: no verdict to count
		}
		completed = append(completed, r)
	}
	sum.Examined = len(completed)
	if len(completed) == 0 {
		sum.Verdict = "no completed runs found — nothing can be said about this check's history yet"
		return sum
	}

	// Newest last.
	sort.SliceStable(completed, func(i, j int) bool { return completed[i].CreatedAt < completed[j].CreatedAt })
	first := completed[0]
	sum.FirstRun = &first

	for i := len(completed) - 1; i >= 0; i-- {
		if completed[i].Conclusion == "success" {
			if sum.LastSuccess == nil {
				r := completed[i]
				sum.LastSuccess = &r
				sum.EverPassed = true
			}
			break
		}
		sum.ConsecutiveFailures++
	}

	switch {
	case !sum.EverPassed:
		sum.Verdict = fmt.Sprintf(
			"never passed: %d of %d examined runs concluded, none successfully (oldest examined: %s). This is not a regression — do not look for what changed.",
			sum.Examined, sum.Examined, first.CreatedAt)
	case sum.ConsecutiveFailures == 0:
		sum.Verdict = "the newest run passed — confirm the failure is still current before investigating"
	default:
		sum.Verdict = fmt.Sprintf("regression: last passed at %s, %d consecutive failures since",
			sum.LastSuccess.CreatedAt, sum.ConsecutiveFailures)
	}
	return sum
}

// ToRecordHistory converts the summary into the record's History field.
func (h HistorySummary) ToRecordHistory() History {
	return History{
		EverPassed: h.EverPassed,
		Checked:    h.Examined > 0,
		Detail:     h.Verdict,
	}
}
