package github

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"strings"
)

// ChecksCompleteVerdict is the closed vocabulary returned by
// EvaluateChecksComplete. Named distinctly from CheckStatus.State so a caller
// cannot accidentally treat the two enums as interchangeable.
type ChecksCompleteVerdict string

const (
	// ChecksComplete: every required check is present in the rollup,
	// concluded, and passing.
	ChecksComplete ChecksCompleteVerdict = "green"
	// ChecksIncomplete: every required check is present and concluded; at
	// least one failed.
	ChecksIncomplete ChecksCompleteVerdict = "red"
	// ChecksNotYet: the rollup is missing a required check, one is still
	// pending, or a per-run cross-check disagrees with the rollup.
	ChecksNotYet ChecksCompleteVerdict = "not-yet"
)

// isChecksCompleteConcluded reports whether a check run has a conclusion at
// all. Matches internal/hooks.isConcluded's semantics exactly (Status ==
// "COMPLETED", case-insensitive) so the two packages' notion of "done" cannot
// drift apart.
func isChecksCompleteConcluded(c CheckDetail) bool {
	return strings.EqualFold(strings.TrimSpace(c.Status), "COMPLETED")
}

// MissingRequiredChecks returns the names from requiredNames that have no
// matching CheckDetail in checks AT ALL — the #1540 defect: absence from a
// rollup is not "nothing to wait for", it is "still missing". A caller that
// has already established every PRESENT check is concluded (e.g.
// VerifyMergeCommit's pending==0 gate, computed over ALL checks) can treat a
// non-empty result as "not yet observable", independent of whatever
// conclusions the present checks carry — this is the shared primitive
// EvaluateChecksComplete and VerifyMergeCommit both build on, so the two
// have exactly one definition of "present" between them.
func MissingRequiredChecks(checks []CheckDetail, requiredNames []string) []string {
	present := make(map[string]bool, len(checks))
	for _, c := range checks {
		present[strings.ToLower(strings.TrimSpace(c.Name))] = true
	}
	var missing []string
	for _, name := range requiredNames {
		if !present[strings.ToLower(strings.TrimSpace(name))] {
			missing = append(missing, name)
		}
	}
	return missing
}

// EvaluateChecksComplete answers "are this SHA's checks complete?" by
// asserting the POSITIVE PRESENCE of every name in requiredNames among
// checks, not merely the absence of a bad conclusion among whatever showed
// up. A requiredName with no matching CheckDetail is NOT-YET — the #1540
// defect: GitHub's rollup can report total_count > 0 with zero pending while
// a required job is still in_progress and simply absent from that response.
//
// requiredNames empty means no required-check set was resolved (no branch
// protection/ruleset) — the caller falls back to "every check present,
// concluded, and none failed" (evaluateAllChecksComplete), the same shape as
// the pre-#1540 total>0/pending==0/bad==0 idiom, so an unprotected branch
// still gets a real verdict instead of a permanent NOT-YET.
func EvaluateChecksComplete(checks []CheckDetail, requiredNames []string) (ChecksCompleteVerdict, []string) {
	present := make(map[string][]CheckDetail, len(checks))
	for _, c := range checks {
		name := strings.ToLower(strings.TrimSpace(c.Name))
		present[name] = append(present[name], c)
	}

	missing := MissingRequiredChecks(checks, requiredNames)
	var pending, failed []string
	for _, name := range requiredNames {
		observations, ok := present[strings.ToLower(strings.TrimSpace(name))]
		if !ok {
			continue // already counted in `missing`
		}
		isPending := false
		isFailed := false
		for _, c := range observations {
			if !isChecksCompleteConcluded(c) {
				isPending = true
				continue
			}
			if !passingCheckConclusions[strings.ToUpper(strings.TrimSpace(c.Conclusion))] {
				isFailed = true
			}
		}
		// A required context can exist on both GitHub status surfaces. Never
		// let a passing observation mask a pending or failed observation with
		// the same name; GitHub's ruleset is the final merge authority, and
		// this local gate must fail closed when the surfaces disagree.
		if isPending {
			pending = append(pending, name)
		} else if isFailed {
			failed = append(failed, name)
		}
	}

	var reasons []string
	if len(missing) > 0 {
		reasons = append(reasons, fmt.Sprintf("required check(s) absent from rollup: %s", strings.Join(missing, ", ")))
	}
	if len(pending) > 0 {
		reasons = append(reasons, fmt.Sprintf("required check(s) still running: %s", strings.Join(pending, ", ")))
	}
	if len(missing) > 0 || len(pending) > 0 {
		return ChecksNotYet, reasons
	}
	if len(failed) > 0 {
		reasons = append(reasons, fmt.Sprintf("required check(s) failed: %s", strings.Join(failed, ", ")))
		return ChecksIncomplete, reasons
	}
	if len(requiredNames) == 0 {
		return evaluateAllChecksComplete(checks)
	}
	return ChecksComplete, nil
}

// GetCommitStatuses returns the combined legacy Commit Status contexts for a
// ref. GitHub rulesets may require contexts published through either this API
// or the Check Runs API; omitting this surface makes a successful status such
// as `cla` look permanently absent. The combined status is paginated like any
// REST list (30 contexts per page by default), so every page is read (#1681).
func (s *CIService) GetCommitStatuses(ctx context.Context, owner, repo, ref string) ([]CheckDetail, error) {
	url := fmt.Sprintf("https://api.github.com/repos/%s/%s/commits/%s/status", owner, repo, ref)

	statuses := []CheckDetail{}
	err := s.getAllPages(ctx, url, checkRunsStatusError, func(body io.Reader) error {
		var page struct {
			SHA      string `json:"sha"`
			Statuses []struct {
				Context   string `json:"context"`
				State     string `json:"state"`
				UpdatedAt string `json:"updated_at"`
				TargetURL string `json:"target_url"`
			} `json:"statuses"`
		}
		if err := json.NewDecoder(body).Decode(&page); err != nil {
			return fmt.Errorf("decode commit statuses: %w", err)
		}
		for _, status := range page.Statuses {
			state := strings.ToUpper(strings.TrimSpace(status.State))
			detail := CheckDetail{
				Name:        status.Context,
				Status:      "COMPLETED",
				Conclusion:  state,
				CompletedAt: status.UpdatedAt,
				DetailsURL:  status.TargetURL,
				HeadSHA:     page.SHA,
			}
			if state == "PENDING" {
				detail.Status = "IN_PROGRESS"
				detail.Conclusion = ""
				detail.CompletedAt = ""
			}
			statuses = append(statuses, detail)
		}
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("fetch commit statuses: %w", err)
	}
	return statuses, nil
}

// evaluateAllChecksComplete is the fallback used when no required-check set
// was resolved: every check present must be concluded, and none may have
// failed. An empty rollup is NOT-YET (the #1027/#1038 lesson: right after a
// push GitHub has not created check runs yet, which is "not observed",
// never "nothing to fail").
func evaluateAllChecksComplete(checks []CheckDetail) (ChecksCompleteVerdict, []string) {
	if len(checks) == 0 {
		return ChecksNotYet, []string{"no checks present on the rollup"}
	}
	var pendingNames, failedNames []string
	for _, c := range checks {
		if !isChecksCompleteConcluded(c) {
			pendingNames = append(pendingNames, c.Name)
			continue
		}
		if !passingCheckConclusions[strings.ToUpper(strings.TrimSpace(c.Conclusion))] {
			failedNames = append(failedNames, c.Name)
		}
	}
	if len(pendingNames) > 0 {
		return ChecksNotYet, []string{fmt.Sprintf("check(s) still running: %s", strings.Join(pendingNames, ", "))}
	}
	if len(failedNames) > 0 {
		return ChecksIncomplete, []string{fmt.Sprintf("check(s) failed: %s", strings.Join(failedNames, ", "))}
	}
	return ChecksComplete, nil
}

// WorkflowRunSummary is the per-run status/conclusion from
// GET /repos/{owner}/{repo}/actions/runs?head_sha={sha} — the endpoint the
// #1540 incident report found consistently truthful when the check-runs
// rollup was not.
type WorkflowRunSummary struct {
	ID         int64  `json:"id"`
	Name       string `json:"name"`
	Status     string `json:"status"`
	Conclusion string `json:"conclusion"`
	HeadSHA    string `json:"headSha"`
}

// GetWorkflowRunsForRef returns the workflow runs attached to sha via the
// actions/runs REST endpoint, for cross-checking against the check-runs
// rollup (CrossCheckWorkflowRuns). Every page is read (#1681): a run still in
// flight on page 2 is exactly the disagreement the cross-check exists to see.
func (s *CIService) GetWorkflowRunsForRef(ctx context.Context, owner, repo, sha string) ([]WorkflowRunSummary, error) {
	url := fmt.Sprintf("https://api.github.com/repos/%s/%s/actions/runs?head_sha=%s", owner, repo, sha)

	runs := []WorkflowRunSummary{}
	err := s.getAllPages(ctx, url, checkRunsStatusError, func(body io.Reader) error {
		var page struct {
			WorkflowRuns []struct {
				ID         int64  `json:"id"`
				Name       string `json:"name"`
				Status     string `json:"status"`
				Conclusion string `json:"conclusion"`
				HeadSHA    string `json:"head_sha"`
			} `json:"workflow_runs"`
		}
		if err := json.NewDecoder(body).Decode(&page); err != nil {
			return fmt.Errorf("decode workflow runs: %w", err)
		}
		for _, r := range page.WorkflowRuns {
			runs = append(runs, WorkflowRunSummary{
				ID:         r.ID,
				Name:       r.Name,
				Status:     strings.ToUpper(r.Status),
				Conclusion: strings.ToUpper(r.Conclusion),
				HeadSHA:    r.HeadSHA,
			})
		}
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("fetch workflow runs: %w", err)
	}
	return runs, nil
}

// CrossCheckWorkflowRuns compares the check-runs rollup against the per-run
// actions/runs endpoint for the same SHA. It answers whether the two agree
// that everything is COMPLETE — not whether they agree on pass/fail, because
// the #1540 defect is the rollup silently dropping an in-flight required job,
// not misreporting a conclusion it did report.
//
// A workflow run still in flight (status != "completed") while the rollup
// shows every check-run it has as COMPLETED is exactly that defect: the two
// readers disagree about whether CI is still running. Returns agree=false in
// that case.
//
// runs empty/nil means no per-run data was available (endpoint unreachable,
// rate limited, insufficient scope) — this is a deliberately separable check
// (see EvaluateCommitChecksCrossChecked): a caller without actions/runs
// access still gets the required-name guarantee and simply skips this layer,
// rather than being blocked on an auxiliary lookup.
func CrossCheckWorkflowRuns(checks []CheckDetail, runs []WorkflowRunSummary) (agree bool, reasons []string) {
	if len(runs) == 0 {
		return true, nil
	}

	allChecksConcluded := true
	for _, c := range checks {
		if !isChecksCompleteConcluded(c) {
			allChecksConcluded = false
			break
		}
	}

	var stillRunning []string
	for _, r := range runs {
		if !strings.EqualFold(strings.TrimSpace(r.Status), "completed") {
			stillRunning = append(stillRunning, r.Name)
		}
	}

	if len(stillRunning) > 0 && allChecksConcluded {
		return false, []string{fmt.Sprintf(
			"rollup shows all checks concluded but workflow run(s) still in flight: %s",
			strings.Join(stillRunning, ", "))}
	}
	return true, nil
}

// EvaluateCommitChecks answers "did this commit's own CI go green?" — the
// contract of `nightgauge ci checks-complete`, scripts/post-merge-check.sh and
// the post-merge hook's verification of a merge commit, which all evaluate a
// commit through this one function.
//
// Every context on the commit counts, required or not: a check run or commit
// status that concluded outside success / skipped / neutral makes the commit
// red, because "main's own run is green" means all of it. Required-ness only
// adds a presence assertion (#1540): a required name with no observation at
// all is not-yet, never "nothing to fail". EvaluateChecksComplete, by
// contrast, is the required-only PR merge gate, where a failing advisory check
// must not block a merge (#1248).
//
// The order is the post-merge contract's:
//
//	empty            -> not-yet (checks have not been created yet)
//	any pending      -> not-yet (a queued or running check has no conclusion)
//	required missing -> not-yet (absent is not done)
//	any failed       -> red
//	otherwise        -> green
//
// A failure already observed while other checks still run is named in the
// not-yet reasons, so it is visible before the verdict can be final.
func EvaluateCommitChecks(checks []CheckDetail, requiredNames []string) (ChecksCompleteVerdict, []string) {
	if len(checks) == 0 {
		return ChecksNotYet, []string{"no checks present on the commit yet"}
	}

	required := make(map[string]bool, len(requiredNames))
	for _, name := range requiredNames {
		required[strings.ToLower(strings.TrimSpace(name))] = true
	}
	label := func(name string) string {
		if required[strings.ToLower(strings.TrimSpace(name))] {
			return name + " (required)"
		}
		return name
	}

	var pending, failed []string
	seenFailed := make(map[string]bool)
	for _, c := range checks {
		if !isChecksCompleteConcluded(c) {
			pending = append(pending, label(c.Name))
			continue
		}
		if !passingCheckConclusions[strings.ToUpper(strings.TrimSpace(c.Conclusion))] && !seenFailed[c.Name] {
			seenFailed[c.Name] = true
			failed = append(failed, fmt.Sprintf("%s: %s", label(c.Name), strings.ToLower(strings.TrimSpace(c.Conclusion))))
		}
	}
	missing := MissingRequiredChecks(checks, requiredNames)

	var reasons []string
	if len(pending) > 0 {
		reasons = append(reasons, fmt.Sprintf("check(s) still running: %s", strings.Join(pending, ", ")))
	}
	if len(missing) > 0 {
		reasons = append(reasons, fmt.Sprintf("required check(s) absent from the commit: %s", strings.Join(missing, ", ")))
	}
	if len(failed) > 0 {
		reasons = append(reasons, fmt.Sprintf("check(s) failed: %s", strings.Join(failed, ", ")))
	}
	switch {
	case len(pending) > 0 || len(missing) > 0:
		return ChecksNotYet, reasons
	case len(failed) > 0:
		return ChecksIncomplete, reasons
	default:
		return ChecksComplete, nil
	}
}

// EvaluateCommitChecksCrossChecked layers the per-run cross-check on top of
// EvaluateCommitChecks: the cross-check only runs once the commit would
// otherwise read terminal (ChecksComplete or ChecksIncomplete) — a check that
// is still legitimately pending does not need a second outbound API call to
// tell it so. runs may be nil when the caller has no access to actions/runs;
// CrossCheckWorkflowRuns then leaves the verdict untouched.
func EvaluateCommitChecksCrossChecked(checks []CheckDetail, requiredNames []string, runs []WorkflowRunSummary) (ChecksCompleteVerdict, []string) {
	verdict, reasons := EvaluateCommitChecks(checks, requiredNames)
	if verdict == ChecksNotYet {
		return verdict, reasons
	}
	if agree, crossReasons := CrossCheckWorkflowRuns(checks, runs); !agree {
		return ChecksNotYet, append(reasons, crossReasons...)
	}
	return verdict, reasons
}
