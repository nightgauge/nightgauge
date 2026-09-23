package github

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/url"
	"sort"
	"strings"
	"time"
)

// ListBaselineRuns returns the newest n completed runs of workflowFile that
// describe branch's CI baseline, newest first (#2055).
//
// Since #2055 the full suites run on pull requests only: the strict ruleset
// makes the merged PR head's tree the merge commit's tree, so the PR run IS
// branch's run for that tree, and the push-triggered runs on branch stopped
// (ci.yml's last push runs are frozen and never change again). The baseline is
// therefore the union of
//
//   - the newest completed pull_request run of workflowFile on the head of each
//     of the last n pull requests merged into branch, and
//   - workflowFile's completed runs on branch itself (schedule,
//     workflow_dispatch, and the pre-#2055 push runs),
//
// ordered by creation time and cut to n. A workflow that still runs on branch
// (a scheduled one) keeps being judged by those runs; ci.yml is judged by the
// runs that gated the merges, which age the frozen push runs out of the window.
func (s *CIService) ListBaselineRuns(ctx context.Context, owner, repo, workflowFile, branch string, n int) ([]WorkflowRun, error) {
	if n <= 0 {
		n = 5
	}
	if n > 100 {
		n = 100
	}
	branchRuns, err := s.ListWorkflowRuns(ctx, owner, repo, workflowFile, branch, n)
	if err != nil {
		return nil, err
	}
	heads, err := s.lastMergedPRHeads(ctx, owner, repo, branch, n)
	if err != nil {
		return nil, err
	}
	all := append([]WorkflowRun{}, branchRuns...)
	for _, head := range heads {
		run, err := s.latestPRRunForHead(ctx, owner, repo, workflowFile, head)
		if err != nil {
			return nil, err
		}
		if run != nil {
			all = append(all, *run)
		}
	}

	seen := make(map[int64]bool, len(all))
	out := make([]WorkflowRun, 0, len(all))
	for _, r := range all {
		if seen[r.ID] {
			continue
		}
		seen[r.ID] = true
		out = append(out, r)
	}
	// RFC 3339 UTC timestamps order lexically.
	sort.SliceStable(out, func(i, j int) bool { return out[i].CreatedAt > out[j].CreatedAt })
	if len(out) > n {
		out = out[:n]
	}
	return out, nil
}

// lastMergedPRHeads returns the head SHAs of the last n pull requests merged
// into base, newest merge first.
func (s *CIService) lastMergedPRHeads(ctx context.Context, owner, repo, base string, n int) ([]string, error) {
	q := url.Values{}
	q.Set("state", "closed")
	if base != "" {
		q.Set("base", base)
	}
	q.Set("sort", "updated")
	q.Set("direction", "desc")
	perPage := 3 * n
	if perPage > 100 {
		perPage = 100
	}
	q.Set("per_page", fmt.Sprintf("%d", perPage))
	endpoint := fmt.Sprintf("https://api.github.com/repos/%s/%s/pulls?%s", url.PathEscape(owner), url.PathEscape(repo), q.Encode())

	type pull struct {
		MergedAt *time.Time `json:"merged_at"`
		Head     struct {
			SHA string `json:"sha"`
		} `json:"head"`
	}
	var merged []pull
	// One page: sorted by update time, 3n closed PRs cover the last n merges.
	_, _, err := s.getOnePage(ctx, endpoint, checkRunsStatusError, func(body io.Reader) error {
		var page []pull
		if err := json.NewDecoder(body).Decode(&page); err != nil {
			return fmt.Errorf("decode pull requests: %w", err)
		}
		for _, p := range page {
			if p.MergedAt != nil && p.Head.SHA != "" {
				merged = append(merged, p)
			}
		}
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("list merged pull requests: %w", err)
	}
	sort.SliceStable(merged, func(i, j int) bool { return merged[i].MergedAt.After(*merged[j].MergedAt) })
	heads := make([]string, 0, n)
	for _, p := range merged {
		if len(heads) == n {
			break
		}
		heads = append(heads, p.Head.SHA)
	}
	return heads, nil
}

// latestPRRunForHead returns the newest completed pull_request run of
// workflowFile on head, or nil when there is none.
func (s *CIService) latestPRRunForHead(ctx context.Context, owner, repo, workflowFile, head string) (*WorkflowRun, error) {
	wf := strings.TrimPrefix(strings.TrimPrefix(workflowFile, ".github/workflows/"), "/")
	q := url.Values{}
	q.Set("head_sha", head)
	q.Set("event", "pull_request")
	q.Set("status", "completed")
	q.Set("per_page", "1")
	endpoint := fmt.Sprintf("https://api.github.com/repos/%s/%s/actions/workflows/%s/runs?%s",
		url.PathEscape(owner), url.PathEscape(repo), url.PathEscape(wf), q.Encode())
	var run *WorkflowRun
	_, _, err := s.getOnePage(ctx, endpoint, checkRunsStatusError, func(body io.Reader) error {
		var payload struct {
			WorkflowRuns []WorkflowRun `json:"workflow_runs"`
		}
		if err := json.NewDecoder(body).Decode(&payload); err != nil {
			return fmt.Errorf("decode workflow runs: %w", err)
		}
		if len(payload.WorkflowRuns) > 0 {
			r := payload.WorkflowRuns[0]
			run = &r
		}
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("list %s runs for %s: %w", wf, shortRef(head), err)
	}
	return run, nil
}
