package github

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
)

// WorkflowRun is a single GitHub Actions workflow run.
//
// Mirrors the subset of fields the baseline-CI gate consumes from
// `GET /repos/{owner}/{repo}/actions/workflows/{workflow_file}/runs`.
type WorkflowRun struct {
	ID         int64  `json:"id"`
	Name       string `json:"name"`
	HeadBranch string `json:"head_branch"`
	// Conclusion is one of: success, failure, cancelled, timed_out, skipped, action_required, neutral, stale, "" (in-flight).
	Conclusion string `json:"conclusion"`
	// Status is one of: queued, in_progress, completed.
	Status    string `json:"status"`
	CreatedAt string `json:"created_at"`
	HTMLURL   string `json:"html_url"`
}

// WorkflowRunJob is a single job within a workflow run.
//
// Mirrors `GET /repos/{owner}/{repo}/actions/runs/{run_id}/jobs`.
type WorkflowRunJob struct {
	ID         int64  `json:"id"`
	Name       string `json:"name"`
	Status     string `json:"status"`
	Conclusion string `json:"conclusion"`
}

// ListWorkflowRuns fetches the last `perPage` completed runs of `workflowFile`
// on `branch`. The workflow_file may be a bare filename ("ci.yml") or a path
// relative to .github/workflows/; the GitHub API accepts both.
//
// Filters to status=completed so in-flight runs do not skew red/green
// threshold counting in the baseline-CI gate evaluator.
func (s *CIService) ListWorkflowRuns(ctx context.Context, owner, repo, workflowFile, branch string, perPage int) ([]WorkflowRun, error) {
	if perPage <= 0 {
		perPage = 5
	}
	if perPage > 100 {
		// GitHub API caps per_page at 100.
		perPage = 100
	}

	// Strip a leading ".github/workflows/" prefix — the GitHub endpoint expects
	// the bare workflow file basename or a path.
	wf := strings.TrimPrefix(workflowFile, ".github/workflows/")
	wf = strings.TrimPrefix(wf, "/")

	q := url.Values{}
	q.Set("branch", branch)
	q.Set("status", "completed")
	q.Set("per_page", fmt.Sprintf("%d", perPage))

	endpoint := fmt.Sprintf("https://api.github.com/repos/%s/%s/actions/workflows/%s/runs?%s",
		url.PathEscape(owner), url.PathEscape(repo), url.PathEscape(wf), q.Encode())

	req, err := http.NewRequestWithContext(ctx, "GET", endpoint, nil)
	if err != nil {
		return nil, fmt.Errorf("create request: %w", err)
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("X-GitHub-Api-Version", "2026-03-10")

	resp, err := s.client.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("list workflow runs: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode == 404 {
		return nil, fmt.Errorf("workflow %q not found in %s/%s", wf, owner, repo)
	}
	if resp.StatusCode != 200 {
		body, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("GitHub API returned %d: %s", resp.StatusCode, string(body))
	}

	var payload struct {
		WorkflowRuns []WorkflowRun `json:"workflow_runs"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&payload); err != nil {
		return nil, fmt.Errorf("decode workflow runs: %w", err)
	}
	return payload.WorkflowRuns, nil
}

// ListRunJobs fetches the jobs for a given workflow run. Used by the
// baseline-CI gate to filter pass/fail status by a specific job name when the
// AC text identifies one.
func (s *CIService) ListRunJobs(ctx context.Context, owner, repo string, runID int64) ([]WorkflowRunJob, error) {
	endpoint := fmt.Sprintf("https://api.github.com/repos/%s/%s/actions/runs/%d/jobs",
		url.PathEscape(owner), url.PathEscape(repo), runID)

	req, err := http.NewRequestWithContext(ctx, "GET", endpoint, nil)
	if err != nil {
		return nil, fmt.Errorf("create request: %w", err)
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("X-GitHub-Api-Version", "2026-03-10")

	resp, err := s.client.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("list run jobs: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != 200 {
		body, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("GitHub API returned %d: %s", resp.StatusCode, string(body))
	}

	var payload struct {
		Jobs []WorkflowRunJob `json:"jobs"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&payload); err != nil {
		return nil, fmt.Errorf("decode run jobs: %w", err)
	}
	return payload.Jobs, nil
}
