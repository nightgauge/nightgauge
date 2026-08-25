package orchestrator

import (
	"context"
	"time"
)

// BaselinePromoteEvaluator is the CI-history question the promote sweep asks,
// narrowed to the one method it needs. Satisfied by
// intelligence/baselineGate.Evaluator; declared here as an interface so this
// package does not depend on that one (and so tests can answer it without a
// forge).
type BaselinePromoteEvaluator interface {
	IsLastNGreen(ctx context.Context, owner, repo, workflow, branch, job string, n int) (bool, []int64, error)
}

// BaselinePromoteSummary is the result of one promote sweep, and the JSON
// shape of `baseline-gate promote --json`. The tags are load-bearing: this
// type moved here from cmd/nightgauge so the CLI and the autonomous daemon
// share ONE implementation (#885), and the CLI's output must not change shape
// as a side effect of that move.
type BaselinePromoteSummary struct {
	Owner       string                 `json:"owner"`
	Repo        string                 `json:"repo"`
	Branch      string                 `json:"branch"`
	Total       int                    `json:"total"`
	Promoted    []BaselinePromoteEntry `json:"promoted"`
	StillPaused []BaselinePromoteEntry `json:"still_paused"`
	Errors      []BaselinePromoteEntry `json:"errors"`
	Disabled    bool                   `json:"disabled,omitempty"`
	EvaluatedAt string                 `json:"evaluated_at"`
}

// BaselinePromoteEntry is one row in a BaselinePromoteSummary list.
type BaselinePromoteEntry struct {
	IssueNumber int     `json:"issue_number"`
	Workflow    string  `json:"workflow,omitempty"`
	Job         string  `json:"job,omitempty"`
	RunIDs      []int64 `json:"run_ids,omitempty"`
	Error       string  `json:"error,omitempty"`
}

// PromoteBaselineDeferrals re-evaluates every queue item paused with
// kind=baseline_ci_red and resumes those whose referenced workflow has gone
// green on the given branch.
//
// THE promote sweep (#885). It has exactly two callers — the
// `baseline-gate promote` CLI verb and the autonomous daemon's periodic sweep
// — and they share this body rather than each carrying a copy. That is the
// whole point: the sibling `blocked_dependency` kind was already swept by the
// daemon while `baseline_ci_red` had no automatic trigger anywhere, and the
// resumer this wraps already existed and worked. Only the trigger was
// missing, so duplicating the logic to add one would have been the wrong
// repair twice over.
//
// The trigger must be local. The queue lives in
// `.nightgauge/pipeline/queue-state.json`, which is local-first and
// gitignored, so a CI cron has no queue to promote and anything it wrote
// would die with the runner — that is what #881 established when it deleted
// the claim that such a cron existed.
//
// Every per-item failure is collected into Errors and never aborts the sweep:
// one unreachable workflow must not strand every other deferred item.
func PromoteBaselineDeferrals(
	ctx context.Context,
	sched *Scheduler,
	eval BaselinePromoteEvaluator,
	owner, repo, branch string,
	greenThreshold int,
	enabled bool,
) BaselinePromoteSummary {
	items := sched.ListPausedByKind("baseline_ci_red")
	summary := BaselinePromoteSummary{
		Owner:       owner,
		Repo:        repo,
		Branch:      branch,
		Total:       len(items),
		Promoted:    []BaselinePromoteEntry{},
		StillPaused: []BaselinePromoteEntry{},
		Errors:      []BaselinePromoteEntry{},
		EvaluatedAt: time.Now().UTC().Format(time.RFC3339),
	}
	if !enabled {
		summary.Disabled = true
		return summary
	}

	for _, item := range items {
		if item.PausedReason == nil || item.PausedReason.Workflow == "" {
			// Nothing names a workflow to re-check, so there is no question to
			// ask. Left paused rather than promoted: a deferral whose evidence
			// is missing is not a deferral whose evidence went green.
			continue
		}
		green, runIDs, err := eval.IsLastNGreen(ctx,
			owner, repo,
			item.PausedReason.Workflow, branch, item.PausedReason.Job,
			greenThreshold)

		entry := BaselinePromoteEntry{
			IssueNumber: item.IssueNumber,
			Workflow:    item.PausedReason.Workflow,
			Job:         item.PausedReason.Job,
			RunIDs:      runIDs,
		}
		switch {
		case err != nil:
			entry.Error = err.Error()
			summary.Errors = append(summary.Errors, entry)
		case !green:
			summary.StillPaused = append(summary.StillPaused, entry)
		case sched.ResumeByIssueNumber(item.IssueNumber):
			summary.Promoted = append(summary.Promoted, entry)
		default:
			entry.Error = "resume failed: queue entry not found or already resumed"
			summary.Errors = append(summary.Errors, entry)
		}
	}
	return summary
}
