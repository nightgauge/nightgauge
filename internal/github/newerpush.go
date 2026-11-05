package github

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/url"
	"regexp"
	"strings"
)

// A cancel by a newer push (#2952). A push workflow with cancel-in-progress
// (main-build.yml) cancels its run on a merge commit when the next merge lands
// on the base branch. That cancel tested nothing and is not main being red:
// the newer commit's run is the one to judge. scripts/post-merge-check.sh's
// fallback applies the same rule.
//
// A cancelled GitHub Actions check run on a merged PR's merge commit is
// dropped, and reported as INFO, only when every fact below was read:
//
//   - its check suite's workflow run on this commit was a `push` on the PR's
//     base branch, with a known creation time;
//   - the same workflow has a later `push` run on that branch, on another
//     commit;
//   - that commit is ahead of this one (compare status "ahead").
//
// Any lookup that fails drops nothing, so the cancel stays RED. A FAILURE is
// never relaxed.

// NewerPushReader reads what NewerPushCancelsFor needs beyond the runs on the
// merge commit; *CIService implements it.
type NewerPushReader interface {
	GetWorkflowPushRuns(ctx context.Context, owner, repo string, workflowID int64, branch string) ([]WorkflowRunSummary, error)
	CompareStatus(ctx context.Context, owner, repo, base, head string) (string, error)
}

var fullSHAPattern = regexp.MustCompile(`^[0-9a-f]{40}$`)

// GetWorkflowPushRuns returns the most recent push runs of one workflow on a
// branch (one page of 20, newest first).
func (s *CIService) GetWorkflowPushRuns(ctx context.Context, owner, repo string, workflowID int64, branch string) ([]WorkflowRunSummary, error) {
	u := fmt.Sprintf("https://api.github.com/repos/%s/%s/actions/workflows/%d/runs?branch=%s&event=push&per_page=20",
		owner, repo, workflowID, url.QueryEscape(branch))
	var runs []WorkflowRunSummary
	_, _, err := s.getOnePage(ctx, u, checkRunsStatusError, func(body io.Reader) error {
		var page struct {
			WorkflowRuns []struct {
				ID           int64  `json:"id"`
				Name         string `json:"name"`
				Status       string `json:"status"`
				Conclusion   string `json:"conclusion"`
				HeadSHA      string `json:"head_sha"`
				CheckSuiteID int64  `json:"check_suite_id"`
				WorkflowID   int64  `json:"workflow_id"`
				Event        string `json:"event"`
				HeadBranch   string `json:"head_branch"`
				CreatedAt    string `json:"created_at"`
			} `json:"workflow_runs"`
		}
		if err := json.NewDecoder(body).Decode(&page); err != nil {
			return fmt.Errorf("decode workflow runs: %w", err)
		}
		for _, r := range page.WorkflowRuns {
			runs = append(runs, toWorkflowRunSummary(r.ID, r.Name, r.Status, r.Conclusion, r.HeadSHA,
				r.CheckSuiteID, r.WorkflowID, r.Event, r.HeadBranch, r.CreatedAt))
		}
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("fetch workflow push runs: %w", err)
	}
	return runs, nil
}

// CompareStatus returns GitHub's compare status of head relative to base
// ("ahead", "behind", "diverged", "identical").
func (s *CIService) CompareStatus(ctx context.Context, owner, repo, base, head string) (string, error) {
	u := fmt.Sprintf("https://api.github.com/repos/%s/%s/compare/%s...%s", owner, repo, base, head)
	var status string
	_, done, err := s.getOnePage(ctx, u, checkRunsStatusError, func(body io.Reader) error {
		var c struct {
			Status string `json:"status"`
		}
		if err := json.NewDecoder(body).Decode(&c); err != nil {
			return fmt.Errorf("decode compare: %w", err)
		}
		status = c.Status
		return nil
	})
	if err != nil {
		return "", fmt.Errorf("compare commits: %w", err)
	}
	if done || status == "" {
		return "", fmt.Errorf("compare commits %s...%s: no status", shortRef(base), shortRef(head))
	}
	return status, nil
}

func isCancelledActionsRun(c CheckDetail) bool {
	return isChecksCompleteConcluded(c) &&
		strings.EqualFold(strings.TrimSpace(c.Conclusion), "CANCELLED") &&
		strings.EqualFold(strings.TrimSpace(c.App), GitHubActionsApp) &&
		c.CheckSuiteID != 0
}

// NewerPushCancelsFor maps each check suite on the merge commit whose
// cancelled run a newer push superseded to the newer commit (see the file
// comment). It is nil when nothing qualifies or anything cannot be read.
// runs are the workflow runs on sha (GetWorkflowRunsForRef); nil reads them.
func NewerPushCancelsFor(ctx context.Context, reader any, owner, repo, sha string, prov *MergeProvenance,
	checks []CheckDetail, runs []WorkflowRunSummary) map[int64]string {
	if prov == nil || !prov.TreesMatch() || prov.BaseRef == "" {
		return nil
	}
	suites := map[int64]bool{}
	for _, c := range checks {
		if isCancelledActionsRun(c) {
			suites[c.CheckSuiteID] = true
		}
	}
	if len(suites) == 0 {
		return nil
	}
	nr, ok := reader.(NewerPushReader)
	if !ok {
		return nil
	}
	if runs == nil {
		rr, ok := reader.(WorkflowRunsReader)
		if !ok {
			return nil
		}
		var err error
		if runs, err = rr.GetWorkflowRunsForRef(ctx, owner, repo, sha); err != nil {
			return nil
		}
	}
	out := map[int64]string{}
	for _, r := range runs {
		if !suites[r.CheckSuiteID] || r.WorkflowID == 0 || r.Event != "push" ||
			r.HeadBranch != prov.BaseRef || r.CreatedAt.IsZero() {
			continue
		}
		later, err := nr.GetWorkflowPushRuns(ctx, owner, repo, r.WorkflowID, prov.BaseRef)
		if err != nil {
			continue
		}
		for _, l := range later {
			cand := strings.ToLower(l.HeadSHA)
			if !l.CreatedAt.After(r.CreatedAt) || !fullSHAPattern.MatchString(cand) ||
				strings.EqualFold(cand, sha) || l.Event != "push" || l.HeadBranch != prov.BaseRef {
				continue
			}
			if st, err := nr.CompareStatus(ctx, owner, repo, sha, cand); err == nil && st == "ahead" {
				out[r.CheckSuiteID] = cand
				break
			}
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// dropCancelledByNewerPush removes the cancelled check runs and workflow runs
// whose suite is in cancels, and returns one INFO line per suite.
func dropCancelledByNewerPush(checks []CheckDetail, runs []WorkflowRunSummary, cancels map[int64]string,
	mergeSHA string) ([]CheckDetail, []WorkflowRunSummary, []string) {
	if len(cancels) == 0 {
		return checks, runs, nil
	}
	names := map[int64][]string{}
	var order []int64
	kept := make([]CheckDetail, 0, len(checks))
	for _, c := range checks {
		if _, hit := cancels[c.CheckSuiteID]; hit && isCancelledActionsRun(c) {
			if names[c.CheckSuiteID] == nil {
				order = append(order, c.CheckSuiteID)
			}
			names[c.CheckSuiteID] = append(names[c.CheckSuiteID], c.Name)
			continue
		}
		kept = append(kept, c)
	}
	var keptRuns []WorkflowRunSummary
	if runs != nil {
		keptRuns = make([]WorkflowRunSummary, 0, len(runs))
		for _, r := range runs {
			if _, hit := names[r.CheckSuiteID]; hit {
				continue
			}
			keptRuns = append(keptRuns, r)
		}
	}
	var info []string
	for _, s := range order {
		info = append(info, fmt.Sprintf("merge commit %s: %s cancelled by a newer push; judge %s",
			shortRef(mergeSHA), strings.Join(names[s], ", "), shortRef(cancels[s])))
	}
	return kept, keptRuns, info
}
