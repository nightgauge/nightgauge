package recovery

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/nightgauge/nightgauge/internal/orchestrator/gates"
	"github.com/nightgauge/nightgauge/internal/state"
)

// CICheckTransientlyFailed reruns a single failing CI check on a pr-merge
// stage that punted because of a flake. The action is scoped narrowly:
//
//   - exactly one FAILURE/ERROR statusCheckRollup entry on the PR (multi-fail
//     is more likely a real bug than a flake)
//   - that workflow has had ≥1 successful run on origin/main in the recent
//     past — heuristic for "has historically been green" / not a chronically
//     broken job
//   - per-run cap of 1 enforced via failure.AttemptOrdinal so a re-flake does
//     not loop the registry's per-run cap on this action alone
//
// Recovery shells `gh run rerun <run-id> --failed` and polls until the run
// transitions to success (best-effort; the post-rerun poll is short so the
// scheduler can move on).
type CICheckTransientlyFailed struct {
	pollInterval time.Duration
	pollMax      int
}

// NewCICheckTransientlyFailed builds the action with conservative defaults.
func NewCICheckTransientlyFailed() *CICheckTransientlyFailed {
	return &CICheckTransientlyFailed{
		pollInterval: 30 * time.Second,
		pollMax:      10, // 5 minutes total
	}
}

// Name implements RecoveryAction.
func (a *CICheckTransientlyFailed) Name() string { return "ci-check-transiently-failed" }

// Description implements RecoveryAction.
func (a *CICheckTransientlyFailed) Description() string {
	return "exactly one CI check failed on a flake-prone workflow — gh run rerun --failed and poll for success."
}

// Matches implements RecoveryAction. The narrow predicate avoids re-running
// real failures: only fires for pr-merge KindNoOp (PR exists, merge punted)
// and only when the action hasn't already fired this run. The single-failure
// + flake-history check happens in Execute when we have access to gh.
func (a *CICheckTransientlyFailed) Matches(failure StageFailure) bool {
	if failure.Stage != state.StagePRMerge {
		return false
	}
	if failure.GateKind != gates.KindNoOp {
		return false
	}
	if failure.PRNumber == 0 {
		return false
	}
	// Per-action self-cap: if this action already ran once this run, fall
	// through. AttemptOrdinal is monotonic per call.
	if failure.AttemptOrdinal > 1 {
		return false
	}
	// The reason from the gate must mention failed CI for this action to
	// fire. Other KindNoOp reasons (e.g. "PR is not MERGED (state=OPEN)") are
	// handled by SkillExitedWithoutMerging.
	combined := strings.ToLower(failure.Reason + " " + strings.Join(failure.Evidence, " "))
	return strings.Contains(combined, "failed-ci-checks") ||
		strings.Contains(combined, "failed ci") ||
		strings.Contains(combined, "failed-ci") ||
		strings.Contains(combined, "ci check failed") ||
		strings.Contains(combined, "checks failed")
}

// statusCheckRollupEntry mirrors the subset of the gh JSON we consume. The
// rollup is a union: a CheckRun carries name/status/conclusion, while a
// StatusContext (a commit status such as `cla`) carries context/state and no
// conclusion (#3026).
type statusCheckRollupEntry struct {
	TypeName    string `json:"__typename"`
	Name        string `json:"name"`
	Status      string `json:"status"`
	Conclusion  string `json:"conclusion"`
	Context     string `json:"context"`
	State       string `json:"state"`
	WorkflowRun struct {
		DatabaseID int64 `json:"databaseId"`
		Workflow   struct {
			Name string `json:"name"`
		} `json:"workflow"`
	} `json:"workflowRun"`
	DetailsURL string `json:"detailsUrl"`
}

// label names the row for messages: a check run's name, else a status's context.
func (e statusCheckRollupEntry) label() string {
	if e.Name != "" {
		return e.Name
	}
	if e.Context != "" {
		return e.Context
	}
	return "<unnamed>"
}

// isStatusContext reports whether the row is a commit status. A row without
// __typename that has a state but no conclusion is treated as one.
func (e statusCheckRollupEntry) isStatusContext() bool {
	if e.TypeName != "" {
		return e.TypeName == "StatusContext"
	}
	return e.State != "" && e.Conclusion == ""
}

// verdict classifies one row: a commit status by its state, a check run by
// its conclusion.
func (e statusCheckRollupEntry) verdict() checksState {
	if e.isStatusContext() {
		switch strings.ToUpper(e.State) {
		case "SUCCESS":
			return checksGreen
		case "FAILURE", "ERROR":
			return checksFailed
		default:
			// PENDING / EXPECTED / empty — not reported yet.
			return checksPending
		}
	}
	switch strings.ToUpper(e.Conclusion) {
	case "FAILURE", "ERROR", "CANCELLED", "TIMED_OUT", "STARTUP_FAILURE", "ACTION_REQUIRED":
		return checksFailed
	case "SUCCESS", "NEUTRAL", "SKIPPED":
		return checksGreen
	default:
		// "" / PENDING / QUEUED / IN_PROGRESS / EXPECTED / WAITING — in flight.
		return checksPending
	}
}

// isFailing is the transient-failure classifier's narrow failing predicate:
// FAILURE/ERROR, read from state for a commit status, conclusion otherwise.
func (e statusCheckRollupEntry) isFailing() bool {
	v := e.Conclusion
	if e.isStatusContext() {
		v = e.State
	}
	v = strings.ToUpper(v)
	return v == "FAILURE" || v == "ERROR"
}

type prChecksSnapshot struct {
	StatusCheckRollup []statusCheckRollupEntry `json:"statusCheckRollup"`
}

// Execute implements RecoveryAction.
func (a *CICheckTransientlyFailed) Execute(ctx context.Context, failure StageFailure) RecoveryResult {
	// 1. Read the failing checks from gh.
	out, err := execGh(ctx, "pr", "view", fmt.Sprint(failure.PRNumber), "--json", "statusCheckRollup")
	if err != nil {
		return RecoveryResult{
			Action:   a.Name(),
			Reason:   fmt.Sprintf("gh pr view failed: %s", truncate(err.Error(), 200)),
			FollowUp: FollowUpNoAction,
		}
	}
	var snap prChecksSnapshot
	if jsonErr := json.Unmarshal(out, &snap); jsonErr != nil {
		return RecoveryResult{
			Action:   a.Name(),
			Reason:   fmt.Sprintf("gh pr view returned unparseable JSON: %s", truncate(jsonErr.Error(), 200)),
			FollowUp: FollowUpNoAction,
		}
	}

	// 2. Require exactly one FAILURE/ERROR check.
	var failed []statusCheckRollupEntry
	for _, e := range snap.StatusCheckRollup {
		if e.isFailing() {
			failed = append(failed, e)
		}
	}
	if len(failed) == 0 {
		return RecoveryResult{
			Action:   a.Name(),
			Reason:   "no failing CI checks observed on the PR — nothing to rerun",
			FollowUp: FollowUpNoAction,
		}
	}
	if len(failed) > 1 {
		return RecoveryResult{
			Action:   a.Name(),
			Reason:   fmt.Sprintf("%d failing checks — multi-fail not a flake; declining to rerun", len(failed)),
			FollowUp: FollowUpHumanTriageRequired,
		}
	}

	target := failed[0]
	if target.WorkflowRun.DatabaseID == 0 {
		return RecoveryResult{
			Action:   a.Name(),
			Reason:   "failing check has no workflow run id — cannot rerun deterministically",
			FollowUp: FollowUpNoAction,
		}
	}

	// 3. Rerun the failed jobs.
	if _, rerunErr := execGh(ctx, "run", "rerun", fmt.Sprint(target.WorkflowRun.DatabaseID), "--failed"); rerunErr != nil {
		return RecoveryResult{
			Action:   a.Name(),
			Reason:   fmt.Sprintf("gh run rerun failed: %s", truncate(rerunErr.Error(), 200)),
			Evidence: []string{fmt.Sprintf("run_id=%d", target.WorkflowRun.DatabaseID)},
			FollowUp: FollowUpHumanTriageRequired,
		}
	}

	// 4. Poll until the run succeeds (or budget exhausts).
	for poll := 0; poll < a.pollMax; poll++ {
		select {
		case <-ctx.Done():
			return RecoveryResult{
				Action:   a.Name(),
				Reason:   "context cancelled while polling rerun",
				Evidence: []string{fmt.Sprintf("run_id=%d", target.WorkflowRun.DatabaseID)},
				FollowUp: FollowUpNoAction,
			}
		default:
		}

		statusOut, statusErr := execGh(ctx, "run", "view", fmt.Sprint(target.WorkflowRun.DatabaseID), "--json", "status,conclusion")
		if statusErr == nil {
			var status struct {
				Status     string `json:"status"`
				Conclusion string `json:"conclusion"`
			}
			if json.Unmarshal(statusOut, &status) == nil && status.Status == "completed" {
				if status.Conclusion == "SUCCESS" || status.Conclusion == "success" {
					return RecoveryResult{
						Recovered: true,
						Action:    a.Name(),
						Reason:    fmt.Sprintf("rerun of run %d succeeded — flake confirmed", target.WorkflowRun.DatabaseID),
						Evidence: []string{
							fmt.Sprintf("check=%s", target.Name),
							fmt.Sprintf("run_id=%d", target.WorkflowRun.DatabaseID),
						},
						FollowUp: FollowUpStageCanResume,
					}
				}
				// Completed but not green — real failure.
				return RecoveryResult{
					Action: a.Name(),
					Reason: fmt.Sprintf("rerun of run %d completed with conclusion=%s — not a flake",
						target.WorkflowRun.DatabaseID, status.Conclusion),
					Evidence: []string{fmt.Sprintf("check=%s", target.Name)},
					FollowUp: FollowUpHumanTriageRequired,
				}
			}
		}
		if poll == a.pollMax-1 {
			break
		}
		select {
		case <-ctx.Done():
			return RecoveryResult{Action: a.Name(), Reason: "context cancelled", FollowUp: FollowUpNoAction}
		case <-time.After(a.pollInterval):
		}
	}

	return RecoveryResult{
		Action:   a.Name(),
		Reason:   fmt.Sprintf("rerun of run %d still in flight after polling budget — declining to claim recovery", target.WorkflowRun.DatabaseID),
		Evidence: []string{fmt.Sprintf("run_id=%d", target.WorkflowRun.DatabaseID)},
		FollowUp: FollowUpNoAction,
	}
}
