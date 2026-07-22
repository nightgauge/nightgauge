package github

import (
	"context"
	"fmt"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/nightgauge/nightgauge/internal/git"
	"github.com/nightgauge/nightgauge/pkg/types"
	"github.com/shurcooL/graphql"
)

// EpicService provides epic-level operations: completion checking, summary generation,
// and closed-to-done board synchronization.
type EpicService struct {
	client *Client
}

// NewEpicService creates an epic service.
func NewEpicService(client *Client) *EpicService {
	return &EpicService{client: client}
}

// EpicValidationGap categorizes a single validation finding.
type EpicValidationGap struct {
	SubIssueNumber int    `json:"subIssueNumber"`
	SubIssueTitle  string `json:"subIssueTitle"`
	GapType        string `json:"gapType"` // "circular_blocker" | "stale_blocker"
	BlockerNumber  int    `json:"blockerNumber,omitempty"`
	Detail         string `json:"detail"`
}

// EpicValidationResult is returned by EpicService.Validate.
type EpicValidationResult struct {
	EpicNumber     int                 `json:"epicNumber"`
	Title          string              `json:"title"`
	Repo           string              `json:"repo"`
	TotalSubIssues int                 `json:"totalSubIssues"`
	Valid          bool                `json:"valid"`
	Gaps           []EpicValidationGap `json:"gaps"`
}

// Validate checks an epic's sub-issue structure for circular blockers (sub-issue
// blocked by its own parent epic) and stale blockers (blocked by a closed issue).
// Sub-issues are fetched in one batched GraphQL request via
// IssueService.GetIssuesByNumbers — replacing the previous per-sub-issue
// GetIssue loop.
func (e *EpicService) Validate(ctx context.Context, owner, repo string, epicNumber int) (*EpicValidationResult, error) {
	issueSvc := NewIssueService(e.client)
	epic, err := issueSvc.GetIssue(ctx, owner, repo, epicNumber)
	if err != nil {
		return nil, fmt.Errorf("fetch epic #%d: %w", epicNumber, err)
	}

	result := &EpicValidationResult{
		EpicNumber:     epic.Number,
		Title:          epic.Title,
		Repo:           epic.Repo,
		TotalSubIssues: len(epic.SubIssues),
		Valid:          true,
		Gaps:           []EpicValidationGap{},
	}

	// Group sub-issue numbers by the repo they live in. Cross-repo epics fan
	// out into one batched request per repo.
	byRepo := make(map[string][]int)
	repoOwnerName := make(map[string][2]string)
	for _, si := range epic.SubIssues {
		// When SubIssueRef.Repo is empty, fall back to the parent epic's repo.
		repoKey := si.Repo
		var siOwner, siRepo string
		if repoKey == "" {
			siOwner, siRepo = owner, repo
			repoKey = owner + "/" + repo
		} else {
			siOwner, siRepo = splitOwnerRepo(repoKey)
		}
		byRepo[repoKey] = append(byRepo[repoKey], si.Number)
		repoOwnerName[repoKey] = [2]string{siOwner, siRepo}
	}

	// Fetch all sub-issues in batched requests (one per repo).
	fetched := make(map[int]*types.Issue, len(epic.SubIssues))
	for repoKey, numbers := range byRepo {
		on := repoOwnerName[repoKey]
		issues, err := issueSvc.GetIssuesByNumbers(ctx, on[0], on[1], numbers)
		if err != nil {
			fmt.Fprintf(os.Stderr, "warning: batch fetch sub-issues from %s: %v\n", repoKey, err)
			continue
		}
		for n, iss := range issues {
			fetched[n] = iss
		}
	}

	for _, si := range epic.SubIssues {
		subIssue, ok := fetched[si.Number]
		if !ok {
			fmt.Fprintf(os.Stderr, "warning: sub-issue #%d missing from batch response\n", si.Number)
			continue
		}
		for _, blocker := range subIssue.BlockedBy {
			if blocker.Number == epicNumber {
				result.Valid = false
				result.Gaps = append(result.Gaps, EpicValidationGap{
					SubIssueNumber: si.Number,
					SubIssueTitle:  si.Title,
					GapType:        "circular_blocker",
					BlockerNumber:  blocker.Number,
					Detail:         fmt.Sprintf("sub-issue #%d is blocked by its own epic #%d", si.Number, epicNumber),
				})
			} else if strings.EqualFold(blocker.State, "CLOSED") {
				result.Valid = false
				result.Gaps = append(result.Gaps, EpicValidationGap{
					SubIssueNumber: si.Number,
					SubIssueTitle:  si.Title,
					GapType:        "stale_blocker",
					BlockerNumber:  blocker.Number,
					Detail:         fmt.Sprintf("sub-issue #%d blocked by closed issue #%d", si.Number, blocker.Number),
				})
			}
		}
	}

	return result, nil
}

// EpicCompletionResult holds the result of an epic completion check.
type EpicCompletionResult struct {
	EpicNumber int    `json:"epicNumber"`
	Title      string `json:"title"`
	Repo       string `json:"repo"`
	Total      int    `json:"total"`
	Closed     int    `json:"closed"`
	Open       int    `json:"open"`
	Complete   bool   `json:"complete"`
	OpenIssues []struct {
		Number int    `json:"number"`
		Title  string `json:"title"`
	} `json:"openIssues,omitempty"`
}

// CheckCompletion checks if all sub-issues of an epic are closed.
// Uses a single GraphQL query (no N+1).
func (e *EpicService) CheckCompletion(ctx context.Context, owner, repo string, epicNumber int) (*EpicCompletionResult, error) {
	issueSvc := NewIssueService(e.client)
	issue, err := issueSvc.GetIssue(ctx, owner, repo, epicNumber)
	if err != nil {
		return nil, fmt.Errorf("fetch epic #%d: %w", epicNumber, err)
	}

	result := &EpicCompletionResult{
		EpicNumber: issue.Number,
		Title:      issue.Title,
		Repo:       issue.Repo,
		Total:      len(issue.SubIssues),
	}

	for _, si := range issue.SubIssues {
		if strings.EqualFold(si.State, "CLOSED") {
			result.Closed++
		} else {
			result.Open++
			result.OpenIssues = append(result.OpenIssues, struct {
				Number int    `json:"number"`
				Title  string `json:"title"`
			}{si.Number, si.Title})
		}
	}

	result.Complete = result.Open == 0 && result.Total > 0
	return result, nil
}

// SweepEpics checks completion for all open epics in a repo concurrently.
// Individual epic check failures are logged to stderr and skipped rather than
// aborting the entire sweep — only a total failure (all epics errored) returns
// a non-nil error.
func (e *EpicService) SweepEpics(ctx context.Context, owner, repo string) ([]EpicCompletionResult, error) {
	issueSvc := NewIssueService(e.client)
	issues, err := issueSvc.ListIssues(ctx, owner, repo, []string{"type:epic"})
	if err != nil {
		return nil, fmt.Errorf("list epics: %w", err)
	}

	type checkResult struct {
		idx    int
		result *EpicCompletionResult
		err    error
	}
	ch := make(chan checkResult, len(issues))
	var wg sync.WaitGroup

	for i, issue := range issues {
		wg.Add(1)
		go func(idx int, num int) {
			defer wg.Done()
			result, err := e.CheckCompletion(ctx, owner, repo, num)
			ch <- checkResult{idx, result, err}
		}(i, issue.Number)
	}

	wg.Wait()
	close(ch)

	results := make([]EpicCompletionResult, len(issues))
	var errCount int
	for res := range ch {
		if res.err != nil {
			fmt.Fprintf(os.Stderr, "Warning: epic #%d check failed: %v\n", issues[res.idx].Number, res.err)
			errCount++
		} else {
			results[res.idx] = *res.result
		}
	}

	// Only return error when every epic check failed; partial failures are logged above.
	if errCount > 0 && errCount == len(issues) {
		return nil, fmt.Errorf("all %d epic checks failed", len(issues))
	}

	// Filter out zero-value results (from failed goroutines or empty epics)
	var valid []EpicCompletionResult
	for _, r := range results {
		if r.Total > 0 || r.EpicNumber > 0 {
			valid = append(valid, r)
		}
	}

	return valid, nil
}

// EpicSummary holds a generated epic completion summary.
type EpicSummary struct {
	EpicNumber int      `json:"epicNumber"`
	Title      string   `json:"title"`
	Repo       string   `json:"repo"`
	Progress   float64  `json:"progress"`
	Total      int      `json:"total"`
	Closed     int      `json:"closed"`
	Open       int      `json:"open"`
	Tier       string   `json:"tier"` // "minimal", "standard", "detailed"
	Summary    string   `json:"summary"`
	Phases     []string `json:"phases,omitempty"`
}

// GenerateSummary creates a completion summary for an epic.
func (e *EpicService) GenerateSummary(ctx context.Context, owner, repo string, epicNumber int) (*EpicSummary, error) {
	issueSvc := NewIssueService(e.client)
	progress, err := issueSvc.GetEpicProgressByNumber(ctx, owner, repo, epicNumber)
	if err != nil {
		return nil, fmt.Errorf("fetch epic progress: %w", err)
	}

	summary := &EpicSummary{
		EpicNumber: progress.Number,
		Title:      progress.Title,
		Repo:       progress.Repo,
		Progress:   progress.PercentComplete / 100,
		Total:      progress.Total,
		Closed:     progress.Closed,
		Open:       progress.Open,
	}

	summary.Tier = classifyTier(summary.Total, summary.Progress)
	summary.Summary = buildSummaryText(summary)

	return summary, nil
}

// classifyTier determines the summary verbosity tier based on epic size and progress.
func classifyTier(total int, progress float64) string {
	if total <= 3 {
		return "minimal"
	}
	if total <= 10 || progress >= 0.8 {
		return "standard"
	}
	return "detailed"
}

// buildSummaryText generates human-readable summary text.
func buildSummaryText(s *EpicSummary) string {
	pct := s.Progress * 100
	if s.Progress >= 1.0 {
		return fmt.Sprintf("Epic #%d (%s) is complete — all %d sub-issues closed.", s.EpicNumber, s.Title, s.Total)
	}
	return fmt.Sprintf("Epic #%d (%s): %.0f%% complete (%d/%d sub-issues closed, %d remaining).",
		s.EpicNumber, s.Title, pct, s.Closed, s.Total, s.Open)
}

// SyncClosedToDone moves closed sub-issues to "Done" status on the project board.
// ownerType defaults to "org" when not provided (backward compatible).
func (e *EpicService) SyncClosedToDone(ctx context.Context, owner, repo string, epicNumber, projectNumber int, ownerType ...OwnerType) (int, error) {
	ot := OwnerTypeOrg
	if len(ownerType) > 0 {
		ot = ownerType[0]
	}
	issueSvc := NewIssueService(e.client)
	issue, err := issueSvc.GetIssue(ctx, owner, repo, epicNumber)
	if err != nil {
		return 0, fmt.Errorf("fetch epic #%d: %w", epicNumber, err)
	}

	// Find closed sub-issues and sync their board status. Status writes go
	// through ProjectService — the single board-writing path with one cached
	// field-ID fetch — instead of the deleted setProjectStatus, which
	// re-fetched project fields inline on every call (#61).
	projSvc := NewProjectService(e.client, owner, projectNumber, ot)
	var synced int
	for _, si := range issue.SubIssues {
		if !strings.EqualFold(si.State, "CLOSED") {
			continue
		}

		siOwner, siRepo := splitEpicOwnerRepo(si.Repo)
		itemID, err := findProjectItemID(ctx, e.client, owner, projectNumber, ot, siOwner, siRepo, si.Number)
		if err != nil {
			continue // Skip items not on the board
		}

		if err := projSvc.SetSingleSelectField(ctx, itemID, "Status", "Done"); err != nil {
			continue
		}
		synced++
	}

	return synced, nil
}

// findProjectItemID finds a project item by issue number (utility for epic service).
func findProjectItemID(ctx context.Context, client *Client, orgOwner string, projectNumber int, ownerType OwnerType, issueOwner, issueRepo string, issueNumber int) (string, error) {
	repoFull := issueOwner + "/" + issueRepo
	var cursor *graphql.String

	// Inner struct for both org and user query result nodes
	type itemNode struct {
		ID      graphql.String
		Content struct {
			TypeName string `graphql:"__typename"`
			Issue    struct {
				Number     graphql.Int
				Repository struct {
					NameWithOwner graphql.String
				}
			} `graphql:"... on Issue"`
		}
	}

	for {
		vars := map[string]interface{}{
			"owner":         graphql.String(orgOwner),
			"projectNumber": graphql.Int(projectNumber),
			"after":         cursor,
		}

		var nodes []itemNode
		var hasNextPage graphql.Boolean
		var endCursorVal graphql.String

		if ownerType.IsUser() {
			var q struct {
				User struct {
					ProjectV2 struct {
						Items struct {
							PageInfo pageInfo
							Nodes    []itemNode
						} `graphql:"items(first: 100, after: $after)"`
					} `graphql:"projectV2(number: $projectNumber)"`
				} `graphql:"user(login: $owner)"`
			}
			if err := client.query(ctx, &q, vars); err != nil {
				return "", err
			}
			nodes = q.User.ProjectV2.Items.Nodes
			hasNextPage = q.User.ProjectV2.Items.PageInfo.HasNextPage
			endCursorVal = q.User.ProjectV2.Items.PageInfo.EndCursor
		} else {
			var q struct {
				Organization struct {
					ProjectV2 struct {
						Items struct {
							PageInfo pageInfo
							Nodes    []itemNode
						} `graphql:"items(first: 100, after: $after)"`
					} `graphql:"projectV2(number: $projectNumber)"`
				} `graphql:"organization(login: $owner)"`
			}
			if err := client.query(ctx, &q, vars); err != nil {
				return "", err
			}
			nodes = q.Organization.ProjectV2.Items.Nodes
			hasNextPage = q.Organization.ProjectV2.Items.PageInfo.HasNextPage
			endCursorVal = q.Organization.ProjectV2.Items.PageInfo.EndCursor
		}

		for _, node := range nodes {
			if node.Content.TypeName == "Issue" &&
				int(node.Content.Issue.Number) == issueNumber &&
				string(node.Content.Issue.Repository.NameWithOwner) == repoFull {
				return string(node.ID), nil
			}
		}

		if !bool(hasNextPage) {
			break
		}
		cursor = &endCursorVal
	}

	return "", fmt.Errorf("issue #%d not found on project board", issueNumber)
}

// EpicTransitionResult holds the result of an epic status transition.
type EpicTransitionResult struct {
	EpicNumber    int    `json:"epicNumber"`
	NewStatus     string `json:"newStatus"`
	EpicSynced    bool   `json:"epicSynced"`
	SubIssueTotal int    `json:"subIssueTotal"`
	SubIssueMoved int    `json:"subIssueMoved"`
	Failures      []struct {
		Number int    `json:"number"`
		Error  string `json:"error"`
	} `json:"failures,omitempty"`
}

// TransitionStatus moves an epic and all its sub-issues to a new status on the project board.
// This is the deterministic function called when an epic is dragged between columns.
// It handles the full cascade: epic status + all sub-issue statuses in a single call.
// ownerType defaults to "org" when not provided (backward compatible).
func (e *EpicService) TransitionStatus(ctx context.Context, owner, repo string, epicNumber, projectNumber int, newStatus string, ownerType ...OwnerType) (*EpicTransitionResult, error) {
	ot := OwnerTypeOrg
	if len(ownerType) > 0 {
		ot = ownerType[0]
	}
	_ = ot // used below
	issueSvc := NewIssueService(e.client)
	issue, err := issueSvc.GetIssue(ctx, owner, repo, epicNumber)
	if err != nil {
		return nil, fmt.Errorf("fetch epic #%d: %w", epicNumber, err)
	}

	result := &EpicTransitionResult{
		EpicNumber:    epicNumber,
		NewStatus:     newStatus,
		SubIssueTotal: len(issue.SubIssues),
	}

	// Use ProjectService for field mutations — it handles field introspection and caching
	projSvc := NewProjectService(e.client, owner, projectNumber, ot)

	// Move the epic itself
	if err := projSvc.SyncStatus(ctx, owner, repo, epicNumber, newStatus); err != nil {
		return nil, fmt.Errorf("set epic #%d status: %w", epicNumber, err)
	}
	result.EpicSynced = true

	// Move all sub-issues concurrently
	type subResult struct {
		number int
		err    error
	}
	ch := make(chan subResult, len(issue.SubIssues))

	for _, si := range issue.SubIssues {
		go func(siNumber int, siRepo string) {
			siOwner, siRepoName := splitEpicOwnerRepo(siRepo)
			setErr := projSvc.SyncStatus(ctx, siOwner, siRepoName, siNumber, newStatus)
			ch <- subResult{siNumber, setErr}
		}(si.Number, si.Repo)
	}

	for range issue.SubIssues {
		r := <-ch
		if r.err != nil {
			result.Failures = append(result.Failures, struct {
				Number int    `json:"number"`
				Error  string `json:"error"`
			}{r.number, r.err.Error()})
		} else {
			result.SubIssueMoved++
		}
	}

	return result, nil
}

// EpicCompleteResult holds the result of the full epic completion flow.
type EpicCompleteResult struct {
	EpicNumber int    `json:"epicNumber"`
	Complete   bool   `json:"complete"`
	Total      int    `json:"total"`
	Closed     int    `json:"closed"`
	Open       int    `json:"open"`
	Action     string `json:"action"` // "not_complete", "closed_and_merged", "closed_pr_created", "already_merged", "no_epic_branch"
	PRURL      string `json:"prUrl,omitempty"`
	PRNumber   int    `json:"prNumber,omitempty"`
	Error      string `json:"error,omitempty"`
	OpenIssues []struct {
		Number int    `json:"number"`
		Title  string `json:"title"`
	} `json:"openIssues,omitempty"`
}

// CompleteEpic runs the full epic completion flow:
// 1. Check if all sub-issues are closed
// 2. If complete: close the epic issue, create epic→main PR, merge it, cleanup branches
// This is the CLI equivalent of the OnEpicComplete callback in server.go.
func (e *EpicService) CompleteEpic(ctx context.Context, owner, repo string, epicNumber int, repoPath string) (*EpicCompleteResult, error) {
	// Step 1: Check completion
	check, err := e.CheckCompletion(ctx, owner, repo, epicNumber)
	if err != nil {
		return nil, err
	}

	result := &EpicCompleteResult{
		EpicNumber: epicNumber,
		Complete:   check.Complete,
		Total:      check.Total,
		Closed:     check.Closed,
		Open:       check.Open,
		OpenIssues: check.OpenIssues,
	}

	if !check.Complete {
		result.Action = "not_complete"
		return result, nil
	}

	// Step 2: Close the epic issue
	issueSvc := NewIssueService(e.client)
	epicIssue, err := issueSvc.GetIssue(ctx, owner, repo, epicNumber)
	if err != nil {
		return nil, fmt.Errorf("fetch epic #%d: %w", epicNumber, err)
	}

	if !strings.EqualFold(epicIssue.State, "CLOSED") {
		if err := issueSvc.CloseIssue(ctx, epicIssue.NodeID); err != nil {
			result.Error = fmt.Sprintf("failed to close epic: %v", err)
			return result, nil
		}
	}

	// Step 3: Find epic branch
	gitSvc, gitErr := git.NewService(repoPath)
	if gitErr != nil {
		// No git service — still report completion but can't create PR
		result.Action = "no_epic_branch"
		result.Error = fmt.Sprintf("git service unavailable: %v", gitErr)
		return result, nil
	}

	epicBranch, err := gitSvc.FindEpicBranch(epicNumber)
	if err != nil {
		result.Action = "no_epic_branch"
		result.Error = fmt.Sprintf("find epic branch: %v", err)
		return result, nil
	}

	// Step 4: Create epic PR (epic branch → main)
	prSvc := NewPRService(e.client)
	prResult, err := prSvc.CreateEpicPR(ctx, owner, repo, epicNumber, epicIssue.Title, epicBranch, "main")
	if err != nil {
		result.Error = fmt.Sprintf("failed to create epic PR: %v", err)
		return result, nil
	}

	result.PRURL = prResult.PRURL
	result.PRNumber = prResult.PRNumber

	if prResult.Action == "already_merged" {
		result.Action = "already_merged"
		_ = gitSvc.BranchCleanup(epicBranch)
		return result, nil
	}

	// Step 5: Merge the epic PR
	if prResult.PRNodeID == "" {
		result.Action = "closed_pr_created"
		return result, nil
	}

	if err := prSvc.MergeEpicPR(ctx, owner, repo, prResult.PRNodeID, epicBranch); err != nil {
		result.Action = "closed_pr_created"
		result.Error = fmt.Sprintf("PR created but merge failed: %v", err)
		return result, nil
	}

	// Step 6: Cleanup branches
	_ = gitSvc.BranchCleanup(epicBranch)

	result.Action = "closed_and_merged"
	return result, nil
}

// AutoCloseResult holds the result of the auto-close operation.
type AutoCloseResult struct {
	Checked int `json:"checked"`
	Closed  int `json:"closed"`
	Skipped int `json:"skipped"`
	Summary []struct {
		EpicNumber int    `json:"epicNumber"`
		Title      string `json:"title"`
		Status     string `json:"status"` // "closed", "skipped", "error"
		Reason     string `json:"reason,omitempty"`
		Error      string `json:"error,omitempty"`
	} `json:"summary,omitempty"`
}

// AutoClose checks all open epics, closes completed ones, and moves them to Done.
// If listing epics fails (e.g. label absent, access error), the error is logged
// to stderr and an empty result is returned so the caller can exit 0 and the
// nightly sweep can continue to the next repo.
func (e *EpicService) AutoClose(ctx context.Context, owner, repo string, projectNumber int) (*AutoCloseResult, error) {
	issueSvc := NewIssueService(e.client)

	// states: OPEN is already enforced by the GraphQL query inside ListIssues;
	// "state:open" must not appear as a label filter or it silently drops all results
	// on repos that lack that label.
	issues, err := issueSvc.ListIssues(ctx, owner, repo, []string{"type:epic"})
	if err != nil {
		fmt.Fprintf(os.Stderr, "Warning: failed to list open epics for %s/%s: %v\n", owner, repo, err)
		return &AutoCloseResult{Checked: 0}, nil
	}

	result := &AutoCloseResult{
		Checked: len(issues),
	}

	type closeResult struct {
		index  int
		status string
		reason string
		err    error
	}

	ch := make(chan closeResult, len(issues))
	var wg sync.WaitGroup

	for idx, epic := range issues {
		wg.Add(1)
		go func(i int, epicNumber int) {
			defer wg.Done()
			status, reason, cerr := e.closeOneEpic(ctx, owner, repo, epicNumber, projectNumber)
			ch <- closeResult{i, status, reason, cerr}
		}(idx, epic.Number)
	}

	wg.Wait()
	close(ch)

	type summaryItem struct {
		EpicNumber int
		Title      string
		Status     string
		Reason     string
		Error      string
	}
	summaryByIndex := make(map[int]summaryItem)

	for res := range ch {
		item := summaryItem{
			EpicNumber: issues[res.index].Number,
			Title:      issues[res.index].Title,
			Status:     res.status,
			Reason:     res.reason,
		}
		if res.err != nil {
			item.Status = "error"
			item.Error = res.err.Error()
		} else if res.status == "closed" {
			result.Closed++
		} else if res.status == "skipped" {
			result.Skipped++
		}
		summaryByIndex[res.index] = item
	}

	for i := 0; i < len(issues); i++ {
		item := summaryByIndex[i]
		result.Summary = append(result.Summary, struct {
			EpicNumber int    `json:"epicNumber"`
			Title      string `json:"title"`
			Status     string `json:"status"`
			Reason     string `json:"reason,omitempty"`
			Error      string `json:"error,omitempty"`
		}{item.EpicNumber, item.Title, item.Status, item.Reason, item.Error})
	}

	return result, nil
}

// closeOneEpic closes a single epic if all its sub-issues are closed.
// Returns (status, reason, error).
//
// When the initial check finds open sub-issues but the total is non-zero,
// this is likely a GitHub eventual-consistency window after the final sub-issue
// merge. We retry up to 3 times with exponential backoff (2s, 4s, 8s) to let
// the API catch up before giving up.
func (e *EpicService) closeOneEpic(ctx context.Context, owner, repo string, epicNumber, projectNumber int) (string, string, error) {
	completion, err := e.CheckCompletion(ctx, owner, repo, epicNumber)
	if err != nil {
		return "", "check_failed", err
	}

	if completion.Total == 0 {
		return "skipped", "no_subs", nil
	}

	// Retry on eventual-consistency: GitHub may not have propagated the final
	// sub-issue close by the time the on-merge trigger fires.
	const maxRetries = 3
	baseDelay := 2 * time.Second
	for attempt := 1; attempt <= maxRetries && !completion.Complete && completion.Open > 0; attempt++ {
		select {
		case <-ctx.Done():
			return "", "check_failed", ctx.Err()
		case <-time.After(baseDelay * time.Duration(1<<uint(attempt-1))):
		}
		completion, err = e.CheckCompletion(ctx, owner, repo, epicNumber)
		if err != nil {
			return "", "check_failed", err
		}
	}

	if !completion.Complete {
		return "skipped", "has_open", nil
	}

	issueSvc := NewIssueService(e.client)
	epicIssue, err := issueSvc.GetIssue(ctx, owner, repo, epicNumber)
	if err != nil {
		return "", "fetch_failed", err
	}

	if strings.EqualFold(epicIssue.State, "CLOSED") {
		return "skipped", "already_closed", nil
	}

	if err := issueSvc.CloseIssue(ctx, epicIssue.NodeID); err != nil {
		return "", "close_failed", fmt.Errorf("close issue: %w", err)
	}

	commentBody := fmt.Sprintf("Auto-closed: all %d sub-issues are complete.", completion.Total)
	if err := issueSvc.AddComment(ctx, epicIssue.NodeID, commentBody); err != nil {
		fmt.Fprintf(os.Stderr, "Warning: failed to add comment to #%d: %v\n", epicNumber, err)
	}

	if projectNumber > 0 {
		if err := e.moveToProjectDone(ctx, owner, repo, epicNumber, projectNumber); err != nil {
			fmt.Fprintf(os.Stderr, "Warning: failed to move #%d to Done: %v\n", epicNumber, err)
		}
	}

	return "closed", "all_closed", nil
}

// moveToProjectDone moves an epic to "Done" status on the project board.
func (e *EpicService) moveToProjectDone(ctx context.Context, owner, repo string, epicNumber, projectNumber int, ownerType ...OwnerType) error {
	projSvc := NewProjectService(e.client, owner, projectNumber, ownerType...)
	return projSvc.SyncStatus(ctx, owner, repo, epicNumber, "Done")
}

// AutoCloseSingleResult holds the result of auto-closing a single epic.
type AutoCloseSingleResult struct {
	EpicNumber int    `json:"epicNumber"`
	Status     string `json:"status"` // "closed", "skipped", "error"
	Reason     string `json:"reason,omitempty"`
	Error      string `json:"error,omitempty"`
}

// AutoCloseSingle checks and closes a single epic if all its sub-issues are closed.
// It wraps closeOneEpic for use by the post-merge hook and other callers that target
// a specific epic rather than sweeping all open epics.
func (e *EpicService) AutoCloseSingle(ctx context.Context, owner, repo string, epicNumber, projectNumber int) (*AutoCloseSingleResult, error) {
	status, reason, err := e.closeOneEpic(ctx, owner, repo, epicNumber, projectNumber)
	result := &AutoCloseSingleResult{
		EpicNumber: epicNumber,
		Status:     status,
		Reason:     reason,
	}
	if err != nil {
		result.Status = "error"
		result.Error = err.Error()
	}
	return result, nil
}

func splitEpicOwnerRepo(full string) (string, string) {
	parts := strings.SplitN(full, "/", 2)
	if len(parts) != 2 {
		return "", full
	}
	return parts[0], parts[1]
}

// FilterByEpicNumber returns a new LifecycleAuditResult scoped to findings for a
// single epic. Used by epicCheckLifecycleCmd to narrow a full audit run to one issue.
func (r *LifecycleAuditResult) FilterByEpicNumber(epicNumber int) *LifecycleAuditResult {
	filtered := &LifecycleAuditResult{
		Dimension: r.Dimension,
		Repo:      r.Repo,
		RunAt:     r.RunAt,
		FixMode:   r.FixMode,
		Findings:  []LifecycleFinding{},
	}
	for _, f := range r.Findings {
		if f.IssueNumber == epicNumber {
			filtered.Findings = append(filtered.Findings, f)
		}
	}
	filtered.Summary = buildAuditSummary(filtered.Findings)
	return filtered
}
