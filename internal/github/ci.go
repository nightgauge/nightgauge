package github

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	forgetypes "github.com/nightgauge/nightgauge/internal/forge/types"
)

// CIService provides CI check polling and log retrieval operations.
type CIService struct {
	client *Client
}

// NewCIService creates a CI service.
func NewCIService(client *Client) *CIService {
	return &CIService{client: client}
}

// CheckStatus, CheckDetail are aliases for the canonical, forge-agnostic
// shapes in internal/forge/types. The aliases preserve existing call sites
// while letting the forge interfaces share a single struct definition
// across adapters.
type CheckStatus = forgetypes.CheckStatus

// CheckDetail is an alias for the forge-agnostic per-check detail.
type CheckDetail = forgetypes.CheckDetail

// GetCheckStatus fetches the current CI check status for a PR.
func (s *CIService) GetCheckStatus(ctx context.Context, owner, repo string, prNumber int) (*CheckStatus, error) {
	prSvc := NewPRService(s.client)
	pr, err := prSvc.GetPR(ctx, owner, repo, prNumber)
	if err != nil {
		return nil, err
	}

	// Out-of-band merge: PR was merged externally while we were waiting for CI.
	// Treat as SUCCESS so WaitForChecks exits cleanly instead of spinning until timeout.
	if pr.State == "MERGED" {
		return &CheckStatus{
			PRNumber:         prNumber,
			State:            "SUCCESS",
			IsTerminal:       true,
			MergedExternally: true,
		}, nil
	}

	status := &CheckStatus{
		PRNumber: prNumber,
		State:    pr.CheckStatus,
	}

	if status.State == "" {
		status.State = "PENDING"
	}

	status.IsTerminal = status.State == "SUCCESS" || status.State == "FAILURE" || status.State == "ERROR"

	// A terminal FAILURE/ERROR verdict must always carry the failing check(s)
	// (#273): the aggregate StatusCheckRollup state alone tells a caller CI
	// failed but not which check, whether it's required, or whether anything
	// is still running. Without this, WaitForChecks could return
	// isTerminal=true, state=FAILURE with checks=nil/total=0 whenever the
	// rollup already reads FAILURE on the very first poll — the early-exit
	// path below the initial pollFn() call in WaitForChecks — leaving
	// downstream classification (e.g. pr-create's CI failure step) with no
	// evidence to work from. Populate checks[]/total/completed from the same
	// moment's check-run snapshot before returning.
	if status.State == "FAILURE" || status.State == "ERROR" {
		if checks, checksErr := s.GetIndividualCheckRuns(ctx, owner, repo, pr.HeadRef); checksErr == nil {
			populateCheckSummary(status, checks)
		}
		// Best-effort: if the check-runs fetch itself fails, still return the
		// terminal FAILURE/ERROR verdict (still meaningful) rather than
		// failing the whole poll over an auxiliary lookup.
	}

	return status, nil
}

// populateCheckSummary computes the total/completed/successful/failed/pending
// summary fields from a slice of individual check runs and attaches it to
// status, alongside the raw checks themselves. Shared by GetCheckStatus's
// terminal-failure augmentation (#273) and available for other terminal-state
// summaries built from a pre-fetched check-run snapshot.
func populateCheckSummary(status *CheckStatus, checks []CheckDetail) {
	var total, completed, successful, failed, pending int
	for _, c := range checks {
		total++
		if c.Status == "COMPLETED" {
			completed++
			if passingCheckConclusions[c.Conclusion] {
				successful++
			} else {
				failed++
			}
		} else {
			pending++
		}
	}

	status.Total = total
	status.Completed = completed
	status.Successful = successful
	status.Failed = failed
	status.Pending = pending
	status.Checks = checks
}

// passingCheckConclusions are the check-run conclusions treated as passing
// when computing successful/failed counts (shared by populateCheckSummary and
// getRequiredOnlyStatusWithChecks).
var passingCheckConclusions = map[string]bool{
	"SUCCESS": true,
	"NEUTRAL": true,
	"SKIPPED": true,
}

// GetRequiredCheckNames returns the union of required status check context
// names enforced on the branch — classic branch protection AND repository
// rulesets. Both sources must be consulted: a ruleset-enforced required check
// is invisible to the protection endpoint, and probing only that endpoint made
// merges loop against "No required status checks found" (#184).
// Returns (nil, nil) when neither source requires any checks.
func (s *CIService) GetRequiredCheckNames(ctx context.Context, owner, repo, branch string) ([]string, error) {
	classic, err := s.getProtectionRequiredChecks(ctx, owner, repo, branch)
	if err != nil {
		return nil, err
	}

	ruleset, err := s.getRulesetRequiredChecks(ctx, owner, repo, branch)
	if err != nil {
		return nil, err
	}

	seen := make(map[string]bool)
	var union []string
	for _, name := range append(classic, ruleset...) {
		if name == "" || seen[name] {
			continue
		}
		seen[name] = true
		union = append(union, name)
	}
	return union, nil
}

// getProtectionRequiredChecks returns required check contexts from classic
// branch protection. Returns (nil, nil) when the branch has no protection
// configured (404).
func (s *CIService) getProtectionRequiredChecks(ctx context.Context, owner, repo, branch string) ([]string, error) {
	url := fmt.Sprintf("https://api.github.com/repos/%s/%s/branches/%s/protection/required_status_checks", owner, repo, branch)

	req, err := http.NewRequestWithContext(ctx, "GET", url, nil)
	if err != nil {
		return nil, fmt.Errorf("create request: %w", err)
	}
	req.Header.Set("Accept", "application/vnd.github+json")

	resp, err := s.client.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("fetch required checks: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode == 404 {
		// No branch protection configured — fall back to full-check wait
		return nil, nil
	}

	if resp.StatusCode != 200 {
		body, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("GitHub API returned %d: %s", resp.StatusCode, string(body))
	}

	var result struct {
		Contexts []string `json:"contexts"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, fmt.Errorf("decode required checks: %w", err)
	}

	return result.Contexts, nil
}

// getRulesetRequiredChecks returns required check contexts from
// required_status_checks rules active on the branch (rules API). Returns
// (nil, nil) on 404 (repo/branch unknown to the rules API) and 403 (token
// cannot read rules) — mirroring CheckRulesets' graceful degradation.
func (s *CIService) getRulesetRequiredChecks(ctx context.Context, owner, repo, branch string) ([]string, error) {
	url := fmt.Sprintf("https://api.github.com/repos/%s/%s/rules/branches/%s", owner, repo, branch)

	req, err := http.NewRequestWithContext(ctx, "GET", url, nil)
	if err != nil {
		return nil, fmt.Errorf("create request: %w", err)
	}
	req.Header.Set("Accept", "application/vnd.github+json")

	resp, err := s.client.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("fetch branch rules: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode == 404 || resp.StatusCode == 403 {
		return nil, nil
	}

	if resp.StatusCode != 200 {
		body, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("GitHub rules API returned %d: %s", resp.StatusCode, string(body))
	}

	var rules []branchRule
	if err := json.NewDecoder(resp.Body).Decode(&rules); err != nil {
		return nil, fmt.Errorf("decode branch rules: %w", err)
	}

	return requiredCheckContexts(rules), nil
}

// GetIndividualCheckRuns returns the list of check runs for a given ref via GitHub REST API.
func (s *CIService) GetIndividualCheckRuns(ctx context.Context, owner, repo, ref string) ([]CheckDetail, error) {
	url := fmt.Sprintf("https://api.github.com/repos/%s/%s/commits/%s/check-runs", owner, repo, ref)

	req, err := http.NewRequestWithContext(ctx, "GET", url, nil)
	if err != nil {
		return nil, fmt.Errorf("create request: %w", err)
	}
	req.Header.Set("Accept", "application/vnd.github+json")

	resp, err := s.client.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("fetch check runs: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != 200 {
		body, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("GitHub API returned %d: %s", resp.StatusCode, string(body))
	}

	var result struct {
		CheckRuns []struct {
			Name       string `json:"name"`
			Status     string `json:"status"`
			Conclusion string `json:"conclusion"`
		} `json:"check_runs"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, fmt.Errorf("decode check runs: %w", err)
	}

	checks := make([]CheckDetail, 0, len(result.CheckRuns))
	for _, run := range result.CheckRuns {
		checks = append(checks, CheckDetail{
			Name:       run.Name,
			Status:     strings.ToUpper(run.Status),
			Conclusion: strings.ToUpper(run.Conclusion),
		})
	}
	return checks, nil
}

// WaitConfig is an alias for the forge-agnostic wait configuration.
type WaitConfig = forgetypes.WaitConfig

// DefaultWaitConfig returns sensible defaults for CI polling.
func DefaultWaitConfig() WaitConfig {
	return WaitConfig{
		Timeout:      30 * time.Minute,
		PollInterval: 30 * time.Second,
	}
}

// WaitForChecks polls CI check status until terminal or timeout.
// When cfg.RequiredCheckNames is non-empty, uses required-only mode: polls
// individual check runs and exits as soon as all required checks complete,
// regardless of non-required check state.
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

	// Check immediately before first tick
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

// getRequiredOnlyStatus polls individual check runs and computes terminal state
// based only on required checks. Non-required checks contribute to counts but
// do not affect terminal state.
func (s *CIService) getRequiredOnlyStatus(ctx context.Context, owner, repo string, prNumber int, requiredNames []string) (*CheckStatus, error) {
	// Get PR to find HeadRef for check-runs lookup
	prSvc := NewPRService(s.client)
	pr, err := prSvc.GetPR(ctx, owner, repo, prNumber)
	if err != nil {
		return nil, err
	}

	// Out-of-band merge: PR was merged externally while we were waiting for CI.
	if pr.State == "MERGED" {
		return &CheckStatus{
			PRNumber:           prNumber,
			State:              "SUCCESS",
			IsTerminal:         true,
			MergedExternally:   true,
			RequiredCheckNames: requiredNames,
		}, nil
	}

	checks, err := s.GetIndividualCheckRuns(ctx, owner, repo, pr.HeadRef)
	if err != nil {
		return nil, err
	}

	return s.getRequiredOnlyStatusWithChecks(checks, requiredNames, prNumber)
}

// getRequiredOnlyStatusWithChecks computes required-only terminal state from a
// pre-fetched slice of CheckDetail. Extracted for testability.
func (s *CIService) getRequiredOnlyStatusWithChecks(checks []CheckDetail, requiredNames []string, prNumber int) (*CheckStatus, error) {
	// Build lookup set for required names (case-insensitive)
	requiredSet := make(map[string]bool, len(requiredNames))
	for _, name := range requiredNames {
		requiredSet[strings.ToLower(name)] = true
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
			if passingCheckConclusions[c.Conclusion] {
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
				if passingCheckConclusions[c.Conclusion] {
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

// CIRunLog is an alias for the forge-agnostic run-log shape.
type CIRunLog = forgetypes.CIRunLog

// GetRunLogs fetches CI failure logs for a workflow run via GitHub REST API.
func (s *CIService) GetRunLogs(ctx context.Context, owner, repo string, runID int64) (*CIRunLog, error) {
	url := fmt.Sprintf("https://api.github.com/repos/%s/%s/actions/runs/%d", owner, repo, runID)

	req, err := http.NewRequestWithContext(ctx, "GET", url, nil)
	if err != nil {
		return nil, fmt.Errorf("create request: %w", err)
	}
	req.Header.Set("Accept", "application/vnd.github+json")

	resp, err := s.client.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("fetch run %d: %w", runID, err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != 200 {
		body, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("GitHub API returned %d: %s", resp.StatusCode, string(body))
	}

	var run struct {
		ID         int64  `json:"id"`
		Status     string `json:"status"`
		Conclusion string `json:"conclusion"`
		HTMLURL    string `json:"html_url"`
		LogsURL    string `json:"logs_url"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&run); err != nil {
		return nil, fmt.Errorf("decode run: %w", err)
	}

	result := &CIRunLog{
		RunID:  run.ID,
		Status: run.Conclusion,
		URL:    run.HTMLURL,
	}

	// Only fetch logs for failed runs
	if strings.EqualFold(run.Conclusion, "failure") && run.LogsURL != "" {
		logContent, err := s.fetchLogContent(ctx, run.LogsURL)
		if err != nil {
			result.Content = fmt.Sprintf("Failed to fetch logs: %v", err)
		} else {
			result.Content = logContent
		}
	}

	return result, nil
}

// fetchLogContent downloads log content from a GitHub Actions logs URL.
func (s *CIService) fetchLogContent(ctx context.Context, logsURL string) (string, error) {
	req, err := http.NewRequestWithContext(ctx, "GET", logsURL, nil)
	if err != nil {
		return "", err
	}

	resp, err := s.client.http.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()

	if resp.StatusCode != 200 {
		return "", fmt.Errorf("logs returned status %d", resp.StatusCode)
	}

	// Read up to 1MB of log content
	limited := io.LimitReader(resp.Body, 1<<20)
	data, err := io.ReadAll(limited)
	if err != nil {
		return "", err
	}

	return string(data), nil
}
