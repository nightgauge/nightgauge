package github

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"time"
)

// WorkflowRun is a single GitHub Actions workflow run.
//
// Mirrors the subset of fields the baseline-CI gate consumes from
// `GET /repos/{owner}/{repo}/actions/workflows/{workflow_file}/runs`.
type WorkflowRun struct {
	ID         int64  `json:"id"`
	Name       string `json:"name"`
	HeadBranch string `json:"head_branch"`
	HeadSHA    string `json:"head_sha"`
	Event      string `json:"event"`
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
	// Steps is the job's step list, in execution order. Populated by
	// `GET /repos/{owner}/{repo}/actions/jobs/{job_id}` and the run-jobs list.
	Steps []WorkflowRunStep `json:"steps"`
}

// WorkflowRunStep is one step of a workflow job. Conclusion is "success",
// "failure", "skipped", "cancelled", "timed_out" or "" while it runs.
type WorkflowRunStep struct {
	Name        string `json:"name"`
	Number      int    `json:"number"`
	Status      string `json:"status"`
	Conclusion  string `json:"conclusion"`
	StartedAt   string `json:"started_at,omitempty"`
	CompletedAt string `json:"completed_at,omitempty"`
}

// DecodeWorkflowRunJob decodes a `GET /repos/{owner}/{repo}/actions/jobs/{job_id}`
// response (#3198). A body with no job id is an error: it is not a job.
func DecodeWorkflowRunJob(body []byte) (WorkflowRunJob, error) {
	var job WorkflowRunJob
	if err := json.Unmarshal(body, &job); err != nil {
		return WorkflowRunJob{}, fmt.Errorf("decode workflow job: %w", err)
	}
	if job.ID == 0 {
		return WorkflowRunJob{}, fmt.Errorf("decode workflow job: response carries no job id")
	}
	return job, nil
}

// Failed-step excerpt bounds (#3198): enough of the end of the failed step's
// output to show the failing command and its error without carrying a whole
// log into a feedback file.
const (
	JobLogExcerptMaxLines = 60
	JobLogExcerptMaxBytes = 8 << 10
)

// jobLogStepHeader opens the output of each step that ran, after the
// timestamp: `##[group]Run <command or action>`.
const jobLogStepHeader = "##[group]Run "

var ansiEscape = regexp.MustCompile(`\x1b\[[0-9;]*[A-Za-z]`)

// FailedStepExcerpt returns the end of one step's own output from a job's
// plain-text log (`GET /actions/jobs/{id}/logs`), the step chosen by its
// number in steps. It never falls back to the job's tail: post-failure steps
// (`if: always()` uploads, summaries, post-job cleanup) follow the failed
// step, and their lines say nothing about the failure.
//
// Each step that ran opens with a `##[group]Run ` header line. Steps that
// produce none (GitHub's "Set up job", "Complete job" and "Post ..." steps,
// and skipped steps) are left out, so the failed step is the k-th header
// where k is its position among the steps that ran. The header's timestamp
// must fall inside the step's started_at..completed_at window, and the count
// of headers must equal the count of steps that ran; otherwise (a composite
// action that adds headers of its own, a log in another format) the result is
// empty. An empty excerpt is acceptable; a wrong one is not.
//
// The step's section runs from its header to the next header, ending no later
// than one second past its completed_at. Timestamps and ANSI colour codes are
// stripped, and the last JobLogExcerptMaxLines lines are kept, at most
// JobLogExcerptMaxBytes from the end.
func FailedStepExcerpt(log string, steps []WorkflowRunStep, stepNumber int) string {
	ran := make([]WorkflowRunStep, 0, len(steps))
	k := -1
	for _, st := range steps {
		if !stepProducesHeader(st) {
			continue
		}
		if st.Number == stepNumber {
			k = len(ran)
		}
		ran = append(ran, st)
	}
	if k < 0 {
		return ""
	}
	started, err1 := time.Parse(time.RFC3339, ran[k].StartedAt)
	completed, err2 := time.Parse(time.RFC3339, ran[k].CompletedAt)
	if err1 != nil || err2 != nil {
		return ""
	}
	end := completed.Add(time.Second)

	lines := strings.Split(strings.TrimPrefix(log, "\ufeff"), "\n")
	var headers []int
	for i, line := range lines {
		if _, text, ok := splitLogLine(line); ok && strings.HasPrefix(text, jobLogStepHeader) {
			headers = append(headers, i)
		}
	}
	if len(headers) != len(ran) {
		return ""
	}
	ts, _, _ := splitLogLine(lines[headers[k]])
	if ts.Before(started) || !ts.Before(end) {
		return ""
	}
	stop := len(lines)
	if k+1 < len(headers) {
		stop = headers[k+1]
	}
	var section []string
	for _, line := range lines[headers[k]:stop] {
		lt, text, ok := splitLogLine(line)
		if ok && !lt.Before(end) {
			break
		}
		if !ok {
			text = strings.TrimRight(line, "\r")
		}
		section = append(section, ansiEscape.ReplaceAllString(text, ""))
	}
	return excerptTail(strings.Join(section, "\n"))
}

// stepProducesHeader reports whether a step that the job lists opens its log
// output with a `##[group]Run ` header.
func stepProducesHeader(st WorkflowRunStep) bool {
	switch {
	case st.Name == "Set up job", st.Name == "Complete job", strings.HasPrefix(st.Name, "Post "):
		return false
	}
	switch strings.ToLower(st.Conclusion) {
	case "", "skipped":
		return false
	}
	return true
}

// splitLogLine splits a job-log line into its leading RFC 3339 timestamp and
// its text. ok is false for a line without one.
func splitLogLine(line string) (time.Time, string, bool) {
	line = strings.TrimRight(line, "\r")
	stamp, text, found := strings.Cut(line, " ")
	if !found {
		return time.Time{}, line, false
	}
	t, err := time.Parse(time.RFC3339Nano, stamp)
	if err != nil {
		return time.Time{}, line, false
	}
	return t, text, true
}

// excerptTail keeps the last JobLogExcerptMaxLines lines of s, trimmed to at
// most JobLogExcerptMaxBytes from the end, on a line boundary where one
// exists.
func excerptTail(s string) string {
	s = strings.TrimRight(s, "\r\n")
	lines := strings.Split(s, "\n")
	if len(lines) > JobLogExcerptMaxLines {
		lines = lines[len(lines)-JobLogExcerptMaxLines:]
	}
	out := strings.Join(lines, "\n")
	if len(out) > JobLogExcerptMaxBytes {
		out = out[len(out)-JobLogExcerptMaxBytes:]
		if i := strings.IndexByte(out, '\n'); i >= 0 && i < len(out)-1 {
			out = out[i+1:]
		}
	}
	return out
}

// ListWorkflowRuns fetches the last `perPage` completed runs of `workflowFile`
// on `branch`. The workflow_file may be a bare filename ("ci.yml") or a path
// relative to .github/workflows/; the GitHub API accepts both.
//
// Filters to status=completed so in-flight runs do not skew red/green
// threshold counting in the baseline-CI gate evaluator.
func (s *CIService) ListWorkflowRuns(ctx context.Context, owner, repo, workflowFile, branch string, perPage int) ([]WorkflowRun, error) {
	return s.listWorkflowRuns(ctx, owner, repo, workflowFile, branch, "", perPage)
}

// ListWorkflowRunsByEvent is ListWorkflowRuns restricted to runs triggered by a
// specific event ("schedule", "push", "workflow_dispatch", …).
//
// The distinction is load-bearing for cadence checking (#996): a workflow whose
// cron is broken but which someone dispatched by hand has runs, and a check that
// counts any run reads it as healthy. `org-security-audit.yml` is exactly that
// shape — every run to date is `workflow_dispatch` or `pull_request`, and its
// weekly cron has never fired once.
//
// An empty event means "any", which is what ListWorkflowRuns passes.
func (s *CIService) ListWorkflowRunsByEvent(ctx context.Context, owner, repo, workflowFile, branch, event string, perPage int) ([]WorkflowRun, error) {
	return s.listWorkflowRuns(ctx, owner, repo, workflowFile, branch, event, perPage)
}

func (s *CIService) listWorkflowRuns(ctx context.Context, owner, repo, workflowFile, branch, event string, perPage int) ([]WorkflowRun, error) {
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
	if branch != "" {
		q.Set("branch", branch)
	}
	q.Set("status", "completed")
	if event != "" {
		q.Set("event", event)
	}
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
