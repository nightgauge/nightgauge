package gitlab

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/url"
	"strings"
	"time"

	"github.com/nightgauge/nightgauge/internal/forge"
	forgetypes "github.com/nightgauge/nightgauge/internal/forge/types"
)

// CIService implements forge.CIService against the GitLab pipelines + jobs
// REST surface. It mirrors the GitHub adapter's status-rollup, terminal-state
// and required-only-mode semantics so cross-forge consumers (the
// pipeline-audit skill, the pr-merge stage gate) see the same shape from
// either backend. See ADR-001/002/003/004/005 in
// .nightgauge/knowledge/features/3359-*.
type CIService struct {
	client *Client
}

// NewCIService constructs a CI service bound to client.
func NewCIService(client *Client) *CIService {
	return &CIService{client: client}
}

// CheckStatus, CheckDetail, WaitConfig, CIRunLog are aliases for the
// canonical, forge-agnostic shapes in internal/forge/types. The aliases
// mirror the GitHub adapter pattern so call sites read the same on either
// side.
type CheckStatus = forgetypes.CheckStatus

// CheckDetail is an alias for the forge-agnostic per-check detail.
type CheckDetail = forgetypes.CheckDetail

// WaitConfig is an alias for the forge-agnostic wait configuration.
type WaitConfig = forgetypes.WaitConfig

// CIRunLog is an alias for the forge-agnostic run-log shape.
type CIRunLog = forgetypes.CIRunLog

// pipelineStatusToForgeState collapses the 11 GitLab pipeline statuses onto
// the 4-state forge canonical enum. ADR-001 captures the rationale; the map
// is package-level so the parity contract test can table-drive both forges
// from one source.
var pipelineStatusToForgeState = map[string]string{
	"success":  "SUCCESS",
	"failed":   "FAILURE",
	"canceled": "ERROR",
	// "skipped" pipelines pass through as SUCCESS so required-only mode
	// treats them as passing — matches GitHub's SKIPPED conclusion semantics
	// (internal/github/ci.go:246).
	"skipped":              "SUCCESS",
	"pending":              "PENDING",
	"running":              "PENDING",
	"created":              "PENDING",
	"waiting_for_resource": "PENDING",
	"preparing":            "PENDING",
	"manual":               "PENDING",
	"scheduled":            "PENDING",
}

// mapPipelineState returns the forge canonical state for a GitLab pipeline
// or job status. Unknown values fall through to PENDING so the rollup never
// flips terminal on an unrecognised vocabulary entry.
func mapPipelineState(gitlabStatus string) string {
	if v, ok := pipelineStatusToForgeState[strings.ToLower(gitlabStatus)]; ok {
		return v
	}
	return "PENDING"
}

// pipelineLifecycleStatus maps a GitLab pipeline status onto the GitHub
// Actions lifecycle vocabulary (queued | in_progress | completed) used by
// WorkflowRun.Status. Mirrors the semantic split GitHub callers expect.
func pipelineLifecycleStatus(gitlabStatus string) string {
	switch strings.ToLower(gitlabStatus) {
	case "success", "failed", "canceled", "skipped":
		return "completed"
	case "created", "waiting_for_resource", "preparing", "scheduled", "manual":
		return "queued"
	default:
		return "in_progress"
	}
}

// pipelineConclusion maps a GitLab pipeline status onto the GitHub Actions
// conclusion vocabulary used by WorkflowRun.Conclusion. Empty when the
// pipeline is still in flight (lifecycle != completed).
func pipelineConclusion(gitlabStatus string) string {
	switch strings.ToLower(gitlabStatus) {
	case "success":
		return "success"
	case "failed":
		return "failure"
	case "canceled":
		return "cancelled"
	case "skipped":
		return "skipped"
	default:
		return ""
	}
}

// rawPipeline is the JSON shape returned by GitLab pipeline endpoints.
type rawPipeline struct {
	ID        int64  `json:"id"`
	IID       int    `json:"iid"`
	Status    string `json:"status"`
	Ref       string `json:"ref"`
	SHA       string `json:"sha"`
	WebURL    string `json:"web_url"`
	UpdatedAt string `json:"updated_at"`
	CreatedAt string `json:"created_at"`
}

// rawJob is the JSON shape returned by GitLab pipeline-jobs endpoints.
type rawJob struct {
	ID           int64  `json:"id"`
	Name         string `json:"name"`
	Stage        string `json:"stage"`
	Status       string `json:"status"`
	AllowFailure bool   `json:"allow_failure"`
	WebURL       string `json:"web_url"`
}

// looksLikeSHA reports whether ref looks like a 40-char hex commit SHA. The
// pipelines list endpoint accepts either ?sha= or ?ref=; we pick the right
// query parameter so callers can pass either branch names or SHAs.
func looksLikeSHA(ref string) bool {
	if len(ref) != 40 {
		return false
	}
	for _, c := range ref {
		isHex := (c >= '0' && c <= '9') || (c >= 'a' && c <= 'f') || (c >= 'A' && c <= 'F')
		if !isHex {
			return false
		}
	}
	return true
}

// GetCheckStatus fetches the current CI status for an MR (mrIID is the
// `prNumber` parameter — the forge-agnostic interface uses GitHub
// terminology, but the value is the GitLab MR IID at the boundary).
func (s *CIService) GetCheckStatus(ctx context.Context, owner, repo string, prNumber int) (*CheckStatus, error) {
	q := url.Values{}
	q.Set("per_page", "1")
	path := fmt.Sprintf("/projects/%s/merge_requests/%d/pipelines", projectPath(owner, repo), prNumber)
	full := s.client.buildURL(path, q)

	var pipelines []rawPipeline
	if _, err := s.client.do(ctx, "GET", full, nil, &pipelines, "list MR pipelines"); err != nil {
		return nil, err
	}
	if len(pipelines) == 0 {
		return &CheckStatus{
			PRNumber:   prNumber,
			State:      "PENDING",
			IsTerminal: false,
		}, nil
	}

	head := pipelines[0]
	state := mapPipelineState(head.Status)

	checks, err := s.GetIndividualCheckRuns(ctx, owner, repo, head.SHA)
	if err != nil {
		return nil, err
	}

	total, completed, successful, failed, pending := tallyChecks(checks)

	return &CheckStatus{
		PRNumber:   prNumber,
		State:      state,
		Total:      total,
		Completed:  completed,
		Successful: successful,
		Failed:     failed,
		Pending:    pending,
		Checks:     checks,
		IsTerminal: state == "SUCCESS" || state == "FAILURE" || state == "ERROR",
	}, nil
}

// tallyChecks aggregates per-check CheckDetail counts in the same shape the
// canonical CheckStatus exposes.
func tallyChecks(checks []CheckDetail) (total, completed, successful, failed, pending int) {
	for _, c := range checks {
		total++
		if c.Status == "COMPLETED" {
			completed++
			if isPassingConclusion(c.Conclusion) {
				successful++
			} else {
				failed++
			}
		} else {
			pending++
		}
	}
	return
}

// isPassingConclusion mirrors the github required-only-mode passing set
// (internal/github/ci.go:244-248). NEUTRAL is included so allow_failure
// jobs that fail still count as passing for the rollup.
func isPassingConclusion(conclusion string) bool {
	switch strings.ToUpper(conclusion) {
	case "SUCCESS", "NEUTRAL", "SKIPPED":
		return true
	default:
		return false
	}
}

// GetIndividualCheckRuns returns per-job CheckDetail entries for the
// pipeline at ref (branch name or SHA). The returned Status field follows
// the GitHub-style COMPLETED/IN_PROGRESS/QUEUED vocabulary; Conclusion uses
// the same canonical SUCCESS/FAILURE/ERROR/PENDING enum the rollup uses,
// with allow_failure jobs collapsed to NEUTRAL.
func (s *CIService) GetIndividualCheckRuns(ctx context.Context, owner, repo, ref string) ([]CheckDetail, error) {
	if ref == "" {
		return nil, nil
	}
	q := url.Values{}
	if looksLikeSHA(ref) {
		q.Set("sha", ref)
	} else {
		q.Set("ref", ref)
	}
	q.Set("per_page", "1")
	path := fmt.Sprintf("/projects/%s/pipelines", projectPath(owner, repo))
	full := s.client.buildURL(path, q)

	var pipelines []rawPipeline
	if _, err := s.client.do(ctx, "GET", full, nil, &pipelines, "list pipelines for ref"); err != nil {
		if errors.Is(err, forge.ErrNotFound) {
			return nil, nil
		}
		return nil, err
	}
	if len(pipelines) == 0 {
		return nil, nil
	}

	jobs, err := s.listPipelineJobs(ctx, owner, repo, pipelines[0].ID)
	if err != nil {
		return nil, err
	}

	out := make([]CheckDetail, 0, len(jobs))
	for _, j := range jobs {
		conclusion := mapPipelineState(j.Status)
		// allow_failure jobs that fail count as NEUTRAL so the
		// required-only-mode rollup (passingConclusions in
		// getRequiredOnlyStatusWithChecks) treats them as passing — same
		// semantics GitHub applies at internal/github/ci.go:244-248.
		if j.AllowFailure && conclusion != "SUCCESS" && conclusion != "PENDING" {
			conclusion = "NEUTRAL"
		}
		name := j.Name
		if j.Stage != "" {
			name = j.Stage + "/" + j.Name
		}
		out = append(out, CheckDetail{
			Name:       name,
			Status:     jobLifecycleStatus(j.Status),
			Conclusion: conclusion,
		})
	}
	return out, nil
}

// jobLifecycleStatus maps a GitLab job status onto the GitHub
// COMPLETED/IN_PROGRESS/QUEUED vocabulary used by CheckDetail.Status.
func jobLifecycleStatus(jobStatus string) string {
	switch strings.ToLower(jobStatus) {
	case "success", "failed", "canceled", "skipped":
		return "COMPLETED"
	case "created", "waiting_for_resource", "preparing", "scheduled", "manual":
		return "QUEUED"
	default:
		return "IN_PROGRESS"
	}
}

// listPipelineJobs paginates the /pipelines/:id/jobs endpoint, walking
// link-header next links until exhausted.
func (s *CIService) listPipelineJobs(ctx context.Context, owner, repo string, pipelineID int64) ([]rawJob, error) {
	q := url.Values{}
	q.Set("per_page", "100")
	path := fmt.Sprintf("/projects/%s/pipelines/%d/jobs", projectPath(owner, repo), pipelineID)
	full := s.client.buildURL(path, q)

	var all []rawJob
	for full != "" {
		var page []rawJob
		resp, err := s.client.do(ctx, "GET", full, nil, &page, "list pipeline jobs")
		if err != nil {
			return nil, err
		}
		all = append(all, page...)
		links := parseLinkHeader(resp.Header.Get("Link"))
		if links.Next == nil {
			break
		}
		full = links.Next.String()
	}
	return all, nil
}

// GetRequiredCheckNames extracts required-check names from GitLab's split
// surfaces:
//   - protected_branches (existence — GitLab has no per-branch status-check
//     names list; reading proves the branch is protected).
//   - approval_rules (Premium; rule names map to required reviewers but
//     forge consumers treat them as required-check identifiers).
//   - external_status_checks (Ultimate; named status checks).
//
// 404 / 403 on any branch surface returns the dedup'd subset reachable —
// matches the GitHub adapter's "no protection configured → (nil, nil)"
// graceful fallback (internal/github/ci.go:74).
func (s *CIService) GetRequiredCheckNames(ctx context.Context, owner, repo, branch string) ([]string, error) {
	// Probe the protected-branch entry to confirm protection exists. 404
	// here is "no protection at all" — return (nil, nil) so callers fall
	// back to wait-on-all mode.
	bpath := fmt.Sprintf("/projects/%s/protected_branches/%s", projectPath(owner, repo), url.PathEscape(branch))
	bfull := s.client.buildURL(bpath, nil)
	var probe map[string]any
	if _, err := s.client.do(ctx, "GET", bfull, nil, &probe, "get protected branch"); err != nil {
		if errors.Is(err, forge.ErrNotFound) || errors.Is(err, forge.ErrPermissionDenied) {
			return nil, nil
		}
		return nil, err
	}

	names := map[string]struct{}{}

	// Approval rules (EE / Premium). 404/403 → skip silently.
	rpath := fmt.Sprintf("/projects/%s/approval_rules", projectPath(owner, repo))
	var rules []struct {
		Name string `json:"name"`
	}
	if _, err := s.client.do(ctx, "GET", s.client.buildURL(rpath, nil), nil, &rules, "list approval rules"); err == nil {
		for _, r := range rules {
			if r.Name != "" {
				names[r.Name] = struct{}{}
			}
		}
	} else if !errors.Is(err, forge.ErrNotFound) && !errors.Is(err, forge.ErrPermissionDenied) {
		return nil, err
	}

	// External status checks (Ultimate). 404/403 → skip silently.
	epath := fmt.Sprintf("/projects/%s/external_status_checks", projectPath(owner, repo))
	var external []struct {
		Name string `json:"name"`
	}
	if _, err := s.client.do(ctx, "GET", s.client.buildURL(epath, nil), nil, &external, "list external status checks"); err == nil {
		for _, e := range external {
			if e.Name != "" {
				names[e.Name] = struct{}{}
			}
		}
	} else if !errors.Is(err, forge.ErrNotFound) && !errors.Is(err, forge.ErrPermissionDenied) {
		return nil, err
	}

	if len(names) == 0 {
		return nil, nil
	}
	out := make([]string, 0, len(names))
	for n := range names {
		out = append(out, n)
	}
	return out, nil
}

// DefaultWaitConfig returns sensible defaults for CI polling. Mirrors the
// GitHub adapter (30 min / 30 s).
func DefaultWaitConfig() WaitConfig {
	return WaitConfig{
		Timeout:      30 * time.Minute,
		PollInterval: 30 * time.Second,
	}
}

// WaitForChecks polls CI status until terminal or timeout. When
// cfg.RequiredCheckNames is non-empty, switches to required-only mode
// (poll individual check runs and exit when all required checks complete).
// Mirrors the GitHub adapter's WaitForChecks structure (internal/github/ci.go:152).
func (s *CIService) WaitForChecks(ctx context.Context, owner, repo string, prNumber int, cfg WaitConfig) (*CheckStatus, error) {
	if cfg.Timeout == 0 {
		cfg.Timeout = 30 * time.Minute
	}
	if cfg.PollInterval == 0 {
		cfg.PollInterval = 30 * time.Second
	}

	start := time.Now()
	ticker := time.NewTicker(cfg.PollInterval)
	defer ticker.Stop()
	deadline := time.After(cfg.Timeout)

	pollFn := func() (*CheckStatus, error) {
		if len(cfg.RequiredCheckNames) > 0 {
			return s.getRequiredOnlyStatus(ctx, owner, repo, prNumber, cfg.RequiredCheckNames)
		}
		return s.GetCheckStatus(ctx, owner, repo, prNumber)
	}

	status, err := pollFn()
	if err != nil {
		return nil, err
	}
	status.ElapsedSecs = int(time.Since(start).Seconds())
	if cfg.OnProgress != nil {
		cfg.OnProgress(status)
	}
	if status.IsTerminal {
		return status, nil
	}

	for {
		select {
		case <-ticker.C:
			status, err = pollFn()
			if err != nil {
				return nil, err
			}
			status.ElapsedSecs = int(time.Since(start).Seconds())
			if cfg.OnProgress != nil {
				cfg.OnProgress(status)
			}
			if status.IsTerminal {
				return status, nil
			}
		case <-deadline:
			return &CheckStatus{
				PRNumber:    prNumber,
				State:       "TIMEOUT",
				IsTerminal:  true,
				ElapsedSecs: int(time.Since(start).Seconds()),
			}, fmt.Errorf("CI checks timed out after %s", cfg.Timeout)
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
}

// getRequiredOnlyStatus polls individual check runs and computes terminal
// state from required-only checks. Mirrors the GitHub adapter; the helper
// at the bottom is duplicated verbatim per ADR-005 — flagged for follow-up
// extraction into a shared package.
func (s *CIService) getRequiredOnlyStatus(ctx context.Context, owner, repo string, prNumber int, requiredNames []string) (*CheckStatus, error) {
	prSvc := NewPRService(s.client)
	pr, err := prSvc.GetPR(ctx, owner, repo, prNumber)
	if err != nil {
		return nil, err
	}
	checks, err := s.GetIndividualCheckRuns(ctx, owner, repo, pr.HeadRef)
	if err != nil {
		return nil, err
	}
	return s.getRequiredOnlyStatusWithChecks(checks, requiredNames, prNumber)
}

// getRequiredOnlyStatusWithChecks computes required-only terminal state
// from a pre-fetched slice of CheckDetail. Verbatim copy of the GitHub
// helper (internal/github/ci.go:236) per ADR-005; follow-up to extract into
// internal/forge/citerm is logged in the issue body.
func (s *CIService) getRequiredOnlyStatusWithChecks(checks []CheckDetail, requiredNames []string, prNumber int) (*CheckStatus, error) {
	requiredSet := make(map[string]bool, len(requiredNames))
	for _, name := range requiredNames {
		requiredSet[strings.ToLower(name)] = true
	}

	passingConclusions := map[string]bool{
		"SUCCESS": true,
		"NEUTRAL": true,
		"SKIPPED": true,
	}

	var (
		total           int
		completed       int
		successful      int
		failed          int
		pending         int
		requiredTotal   int
		requiredDone    int
		requiredPassed  int
		annotatedChecks []CheckDetail
	)

	for _, c := range checks {
		total++
		isRequired := requiredSet[strings.ToLower(c.Name)]
		c.Required = isRequired

		if c.Status == "COMPLETED" {
			completed++
			if passingConclusions[c.Conclusion] {
				successful++
			} else {
				failed++
			}
		} else {
			pending++
		}

		if isRequired {
			requiredTotal++
			if c.Status == "COMPLETED" {
				requiredDone++
				if passingConclusions[c.Conclusion] {
					requiredPassed++
				}
			}
		}

		annotatedChecks = append(annotatedChecks, c)
	}

	status := &CheckStatus{
		PRNumber:           prNumber,
		Total:              total,
		Completed:          completed,
		Successful:         successful,
		Failed:             failed,
		Pending:            pending,
		Checks:             annotatedChecks,
		RequiredCheckNames: requiredNames,
	}

	allRequiredDone := requiredTotal > 0 && requiredDone == requiredTotal
	switch {
	case allRequiredDone && requiredPassed == requiredTotal:
		status.State = "SUCCESS"
		status.IsTerminal = true
		status.RequiredPassed = true
	case allRequiredDone && requiredPassed < requiredTotal:
		status.State = "FAILURE"
		status.IsTerminal = true
	default:
		status.State = "PENDING"
		status.IsTerminal = false
	}
	return status, nil
}

// GetRunLogs fetches the log trace for a single GitLab job. The runID is
// the GitLab job ID (pipelines themselves have no log surface — logs live
// per-job). Trace bytes are capped at 1 MiB to match the GitHub adapter's
// io.LimitReader cap (internal/github/ci.go:391).
func (s *CIService) GetRunLogs(ctx context.Context, owner, repo string, runID int64) (*CIRunLog, error) {
	jpath := fmt.Sprintf("/projects/%s/jobs/%d", projectPath(owner, repo), runID)
	var job rawJob
	if _, err := s.client.do(ctx, "GET", s.client.buildURL(jpath, nil), nil, &job, "get job"); err != nil {
		return nil, err
	}

	out := &CIRunLog{
		RunID:  runID,
		Status: job.Status,
		URL:    job.WebURL,
	}

	if !strings.EqualFold(job.Status, "failed") {
		return out, nil
	}

	tpath := fmt.Sprintf("/projects/%s/jobs/%d/trace", projectPath(owner, repo), runID)
	tfull := s.client.buildURL(tpath, nil)
	body, _, err := s.client.doRaw(ctx, "GET", tfull, nil, "get job trace")
	if err != nil {
		// Trace fetch failure is informational — preserve job metadata.
		out.Content = fmt.Sprintf("Failed to fetch trace: %v", err)
		return out, nil
	}
	limited := io.LimitReader(strings.NewReader(string(body)), 1<<20)
	data, _ := io.ReadAll(limited)
	out.Content = string(data)
	return out, nil
}

// WorkflowRun mirrors the GitHub-adapter WorkflowRun shape so cross-forge
// callers see the same JSON surface from either backend. Concrete-only —
// not exposed via the forge.CIService interface (see ADR-002).
type WorkflowRun struct {
	ID         int64  `json:"id"`
	Name       string `json:"name"`
	HeadBranch string `json:"head_branch"`
	Conclusion string `json:"conclusion"`
	Status     string `json:"status"`
	CreatedAt  string `json:"created_at"`
	HTMLURL    string `json:"html_url"`
}

// ListWorkflowRuns queries pipelines for branch (ref) optionally bounded by
// since (updated_after). perPage caps each page; defaults to 5 when zero,
// caps at 100 (GitLab's per_page max).
//
// Concrete-only — matches the GitHub adapter's
// `*github.CIService.ListWorkflowRuns` shape so audit consumers can read
// either side via type assertion.
func (s *CIService) ListWorkflowRuns(ctx context.Context, owner, repo, branch string, since time.Time, perPage int) ([]WorkflowRun, error) {
	if perPage <= 0 {
		perPage = 5
	}
	if perPage > 100 {
		perPage = 100
	}
	q := url.Values{}
	if branch != "" {
		q.Set("ref", branch)
	}
	if !since.IsZero() {
		q.Set("updated_after", since.UTC().Format(time.RFC3339))
	}
	q.Set("per_page", fmt.Sprintf("%d", perPage))

	path := fmt.Sprintf("/projects/%s/pipelines", projectPath(owner, repo))
	full := s.client.buildURL(path, q)

	var pipes []rawPipeline
	if _, err := s.client.do(ctx, "GET", full, nil, &pipes, "list workflow runs"); err != nil {
		return nil, err
	}
	out := make([]WorkflowRun, 0, len(pipes))
	for _, p := range pipes {
		out = append(out, rawPipelineToWorkflowRun(p))
	}
	return out, nil
}

// IteratePipelines returns a streaming iterator over pipelines for branch
// optionally bounded by since. Reuses the generic pageIterator[T] from
// issues.go so link-header walking, EOF and Close-idempotency come for
// free (ADR-003).
func (s *CIService) IteratePipelines(ctx context.Context, owner, repo, branch string, since time.Time) forge.Iterator[WorkflowRun] {
	q := url.Values{}
	if branch != "" {
		q.Set("ref", branch)
	}
	if !since.IsZero() {
		q.Set("updated_after", since.UTC().Format(time.RFC3339))
	}
	q.Set("per_page", "100")

	path := fmt.Sprintf("/projects/%s/pipelines", projectPath(owner, repo))
	startURL := s.client.buildURL(path, q)
	return newPageIterator(s.client, startURL, "iterate pipelines", owner, repo, decodePipelinePage)
}

func decodePipelinePage(body []byte, _, _ string) ([]WorkflowRun, error) {
	var raws []rawPipeline
	if err := json.Unmarshal(body, &raws); err != nil {
		return nil, fmt.Errorf("decode pipelines page: %w", err)
	}
	out := make([]WorkflowRun, 0, len(raws))
	for _, r := range raws {
		out = append(out, rawPipelineToWorkflowRun(r))
	}
	return out, nil
}

func rawPipelineToWorkflowRun(p rawPipeline) WorkflowRun {
	return WorkflowRun{
		ID:         p.ID,
		Name:       p.Ref,
		HeadBranch: p.Ref,
		Status:     pipelineLifecycleStatus(p.Status),
		Conclusion: pipelineConclusion(p.Status),
		CreatedAt:  p.CreatedAt,
		HTMLURL:    p.WebURL,
	}
}
