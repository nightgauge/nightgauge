package github

import (
	"context"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/nightgauge/nightgauge/pkg/types"
)

// LifecycleFinding is a single detected lifecycle issue.
type LifecycleFinding struct {
	Category    string `json:"category"` // STALE_EPIC, BOARD_STATUS_DRIFT, etc.
	Severity    string `json:"severity"` // "high", "medium", "low"
	IssueNumber int    `json:"issue_number"`
	IssueTitle  string `json:"issue_title"`
	IssueState  string `json:"issue_state"`
	BoardStatus string `json:"board_status,omitempty"`
	Detail      string `json:"detail"`
	Fixed       bool   `json:"fixed"`
	FixError    string `json:"fix_error,omitempty"`
}

// AuditSummary aggregates finding counts.
type AuditSummary struct {
	Total             int `json:"total"`
	StaleEpics        int `json:"stale_epics"`
	StatusDrift       int `json:"status_drift"`
	PrematureDone     int `json:"premature_done"`
	Orphaned          int `json:"orphaned"`
	StaleBlocker      int `json:"stale_blocker"`
	ClosedWithOpenPR  int `json:"closed_with_open_pr"`
	OpenPRClosedIssue int `json:"open_pr_closed_issue"`
	Fixed             int `json:"fixed"`
	Errors            int `json:"errors"`
}

// LifecycleAuditResult is the top-level output of the audit lifecycle command.
type LifecycleAuditResult struct {
	Dimension string             `json:"dimension"` // "epic-lifecycle"
	Repo      string             `json:"repo"`
	RunAt     string             `json:"run_at"`
	FixMode   bool               `json:"fix_mode"`
	Findings  []LifecycleFinding `json:"findings"`
	Summary   AuditSummary       `json:"summary"`
}

// LifecycleAuditService detects stale epics, board status drift, orphaned issues,
// and stale blocking relationships. It composes existing services for all mutations.
type LifecycleAuditService struct {
	client        *Client
	owner         string
	ownerType     OwnerType
	projectNumber int
}

// NewLifecycleAuditService creates a LifecycleAuditService.
func NewLifecycleAuditService(client *Client, owner string, projectNumber int, ownerType ...OwnerType) *LifecycleAuditService {
	ot := OwnerTypeOrg
	if len(ownerType) > 0 {
		ot = ownerType[0]
	}
	return &LifecycleAuditService{
		client:        client,
		owner:         owner,
		ownerType:     ot,
		projectNumber: projectNumber,
	}
}

// RunAudit executes all five detection categories and optionally fixes findings.
// A single board fetch is shared across all board-related categories.
func (s *LifecycleAuditService) RunAudit(ctx context.Context, owner, repo string, fix bool) (*LifecycleAuditResult, error) {
	result := &LifecycleAuditResult{
		Dimension: "epic-lifecycle",
		Repo:      owner + "/" + repo,
		RunAt:     time.Now().UTC().Format(time.RFC3339),
		FixMode:   fix,
	}

	issueSvc := NewIssueService(s.client)
	boardSvc := NewBoardService(s.client, s.owner, s.projectNumber, s.ownerType)
	projSvc := NewProjectService(s.client, s.owner, s.projectNumber, s.ownerType)

	// Single board fetch shared by BOARD_STATUS_DRIFT, PREMATURE_DONE, ORPHANED_ISSUE, STALE_BLOCKER.
	boardItems, err := boardSvc.ListItems(ctx, "")
	if err != nil {
		return nil, fmt.Errorf("fetch board items: %w", err)
	}

	// Build set of issue numbers on the board for ORPHANED_ISSUE detection.
	boardNumbers := make(map[int]struct{}, len(boardItems))
	for _, item := range boardItems {
		if !item.IsPR {
			boardNumbers[item.Number] = struct{}{}
		}
	}

	// Fetch all open epics (filtered by label) for STALE_EPIC detection.
	openEpics, err := issueSvc.ListIssues(ctx, owner, repo, []string{"type:epic"})
	if err != nil {
		return nil, fmt.Errorf("list open epics: %w", err)
	}

	// Fetch all open issues for ORPHANED_ISSUE detection.
	allOpenIssues, err := issueSvc.ListIssues(ctx, owner, repo, nil)
	if err != nil {
		return nil, fmt.Errorf("list open issues: %w", err)
	}

	var findings []LifecycleFinding

	// --- STALE_EPIC ---
	// Open epics whose every sub-issue is already closed.
	for _, epic := range openEpics {
		fullEpic, err := issueSvc.GetIssue(ctx, owner, repo, epic.Number)
		if err != nil {
			continue // Skip inaccessible epics
		}
		if len(fullEpic.SubIssues) == 0 {
			continue // No sub-issues — not a stale epic by this definition
		}

		allClosed := true
		for _, si := range fullEpic.SubIssues {
			if !strings.EqualFold(si.State, "CLOSED") {
				allClosed = false
				break
			}
		}
		if !allClosed {
			continue
		}

		f := LifecycleFinding{
			Category:    "STALE_EPIC",
			Severity:    "high",
			IssueNumber: epic.Number,
			IssueTitle:  epic.Title,
			IssueState:  "OPEN",
			Detail:      fmt.Sprintf("epic has %d sub-issues, all closed — epic itself is still open", len(fullEpic.SubIssues)),
		}

		if fix {
			if closeErr := issueSvc.CloseIssue(ctx, fullEpic.NodeID); closeErr != nil {
				f.FixError = closeErr.Error()
			} else if syncErr := projSvc.SyncStatus(ctx, owner, repo, epic.Number, "Done"); syncErr != nil {
				f.FixError = fmt.Sprintf("closed but board sync failed: %v", syncErr)
			} else {
				f.Fixed = true
			}
		}

		findings = append(findings, f)
	}

	// --- BOARD_STATUS_DRIFT and PREMATURE_DONE ---
	// Single pass over board items detects both categories.
	for _, item := range boardItems {
		if item.IsPR {
			continue
		}

		isClosed := strings.EqualFold(item.State, "CLOSED")
		isAtDone := item.Status == "Done"

		if isClosed && !isAtDone {
			// Closed issue not marked Done on board → BOARD_STATUS_DRIFT
			f := LifecycleFinding{
				Category:    "BOARD_STATUS_DRIFT",
				Severity:    "medium",
				IssueNumber: item.Number,
				IssueTitle:  item.Title,
				IssueState:  "CLOSED",
				BoardStatus: item.Status,
				Detail:      fmt.Sprintf("issue is closed but board status is %q (expected Done)", item.Status),
			}
			if fix {
				itemOwner, itemRepo := resolveItemRepo(item.Repo, owner, repo)
				if syncErr := projSvc.SyncStatus(ctx, itemOwner, itemRepo, item.Number, "Done"); syncErr != nil {
					f.FixError = syncErr.Error()
				} else {
					f.Fixed = true
				}
			}
			findings = append(findings, f)
		} else if !isClosed && isAtDone {
			// Open issue marked Done on board → PREMATURE_DONE
			correctStatus := "In Progress"
			f := LifecycleFinding{
				Category:    "PREMATURE_DONE",
				Severity:    "high",
				IssueNumber: item.Number,
				IssueTitle:  item.Title,
				IssueState:  "OPEN",
				BoardStatus: "Done",
				Detail:      "issue is still open but board status is Done",
			}
			if fix {
				itemOwner, itemRepo := resolveItemRepo(item.Repo, owner, repo)
				if syncErr := projSvc.SyncStatus(ctx, itemOwner, itemRepo, item.Number, correctStatus); syncErr != nil {
					f.FixError = syncErr.Error()
				} else {
					f.Fixed = true
				}
			}
			findings = append(findings, f)
		}
	}

	// --- ORPHANED_ISSUE ---
	// Open issues with no corresponding entry on the project board.
	// No auto-fix: adding to board requires creating a project item (not yet implemented).
	for _, issue := range allOpenIssues {
		if _, onBoard := boardNumbers[issue.Number]; !onBoard {
			findings = append(findings, LifecycleFinding{
				Category:    "ORPHANED_ISSUE",
				Severity:    "low",
				IssueNumber: issue.Number,
				IssueTitle:  issue.Title,
				IssueState:  "OPEN",
				Detail:      "open issue has no entry on the project board",
			})
		}
	}

	// --- STALE_BLOCKER ---
	// Board items blocked by issues that are already closed.
	findings = append(findings, s.detectStaleBlockers(ctx, boardItems, issueSvc, owner, repo, fix)...)

	// --- CLOSED_WITH_OPEN_PR and OPEN_PR_CLOSED_ISSUE ---
	// Fetch all open PRs once; filter in-memory for both categories.
	prSvc := NewPRService(s.client)
	openPRs, prFetchErr := prSvc.ListPRs(ctx, owner, repo, "OPEN", "")
	if prFetchErr != nil {
		// Non-fatal: log but continue — existing categories are still valid.
		fmt.Printf("audit lifecycle: warning: could not list open PRs for orphan detection: %v\n", prFetchErr)
	} else {
		findings = append(findings, s.detectClosedWithOpenPR(ctx, boardItems, openPRs, issueSvc, projSvc, owner, repo, fix)...)
		findings = append(findings, detectOpenPRClosedIssue(ctx, openPRs, issueSvc, owner, repo)...)
	}

	result.Findings = findings
	result.Summary = buildAuditSummary(findings)
	return result, nil
}

// resolveItemRepo returns the board item's OWN owner/repo, parsed from its
// "owner/repo" NameWithOwner string, falling back to the audit's owner/repo
// when the item carries no repository. Project boards can aggregate issues
// from multiple repos (N:1 topology), so lifecycle fixes must target the
// item's home repo — using the audit's --repo for a cross-repo item fails to
// resolve the issue and silently leaves the finding unfixed. #3792.
func resolveItemRepo(itemRepo, fallbackOwner, fallbackRepo string) (string, string) {
	// item.Repo is GitHub's NameWithOwner ("owner/repo"). Only override the
	// fallback when BOTH parts are present; anything malformed (no slash, empty
	// owner) falls back defensively rather than guessing a target repo.
	o, r := splitOwnerRepo(itemRepo)
	if o == "" || r == "" {
		return fallbackOwner, fallbackRepo
	}
	return o, r
}

// detectStaleBlockers finds board items with closed blockedBy entries.
// When fix=true, it fetches the blocked issue's node ID to call RemoveBlockedBy.
func (s *LifecycleAuditService) detectStaleBlockers(
	ctx context.Context,
	boardItems []types.BoardItem,
	issueSvc *IssueService,
	owner, repo string,
	fix bool,
) []LifecycleFinding {
	var findings []LifecycleFinding

	for _, item := range boardItems {
		if item.IsPR {
			continue
		}
		for _, blocker := range item.BlockedBy {
			if !strings.EqualFold(blocker.State, "CLOSED") {
				continue
			}

			f := LifecycleFinding{
				Category:    "STALE_BLOCKER",
				Severity:    "medium",
				IssueNumber: item.Number,
				IssueTitle:  item.Title,
				IssueState:  item.State,
				Detail:      fmt.Sprintf("blocked by #%d (%s) which is already closed", blocker.Number, blocker.Title),
			}

			if fix {
				// Need the blocked issue's node ID — fetch it since board item only
				// has the project item ID. Resolve against the item's OWN repo so
				// cross-repo board items (N:1 topology) don't fail. #3792.
				itemOwner, itemRepo := resolveItemRepo(item.Repo, owner, repo)
				fullIssue, fetchErr := issueSvc.GetIssue(ctx, itemOwner, itemRepo, item.Number)
				if fetchErr != nil {
					f.FixError = fmt.Sprintf("fetch issue node ID: %v", fetchErr)
				} else if removeErr := issueSvc.RemoveBlockedBy(ctx, fullIssue.NodeID, blocker.NodeID); removeErr != nil {
					f.FixError = removeErr.Error()
				} else {
					f.Fixed = true
				}
			}

			findings = append(findings, f)
		}
	}

	return findings
}

// buildAuditSummary aggregates finding counts from a finding slice.
func buildAuditSummary(findings []LifecycleFinding) AuditSummary {
	var s AuditSummary
	s.Total = len(findings)
	for _, f := range findings {
		switch f.Category {
		case "STALE_EPIC":
			s.StaleEpics++
		case "BOARD_STATUS_DRIFT":
			s.StatusDrift++
		case "PREMATURE_DONE":
			s.PrematureDone++
		case "ORPHANED_ISSUE":
			s.Orphaned++
		case "STALE_BLOCKER":
			s.StaleBlocker++
		case "CLOSED_WITH_OPEN_PR":
			s.ClosedWithOpenPR++
		case "OPEN_PR_CLOSED_ISSUE":
			s.OpenPRClosedIssue++
		}
		if f.Fixed {
			s.Fixed++
		}
		if f.FixError != "" {
			s.Errors++
		}
	}
	return s
}

// issueBranchPattern matches branch names like feat/42-* or fix/42-*.
var issueBranchPattern = regexp.MustCompile(`^(?:feat|fix|docs|chore|refactor|test)/(\d+)[-/]`)

// prBodyClosePattern matches "closes #42", "fixes #42", "resolves #42" (case-insensitive).
var prBodyClosePattern = regexp.MustCompile(`(?i)(?:closes?|fixes?|resolves?)\s+#(\d+)`)

// prReferencesIssue returns true when a PR's head branch or body references the
// given issue number via the standard branch-naming pattern or a GitHub close keyword.
func prReferencesIssue(pr types.PullRequest, issueNumber int) bool {
	issueStr := strconv.Itoa(issueNumber)

	// Check branch name: feat/42-* or fix/42-*
	if m := issueBranchPattern.FindStringSubmatch(pr.HeadRef); len(m) == 2 && m[1] == issueStr {
		return true
	}

	// Check PR body for close keywords
	for _, m := range prBodyClosePattern.FindAllStringSubmatch(pr.Body, -1) {
		if len(m) == 2 && m[1] == issueStr {
			return true
		}
	}

	return false
}

// extractIssueNumber extracts the first issue number from a PR's head branch
// name or body using the same patterns as prReferencesIssue.
func extractIssueNumber(pr types.PullRequest) int {
	if m := issueBranchPattern.FindStringSubmatch(pr.HeadRef); len(m) == 2 {
		n, err := strconv.Atoi(m[1])
		if err == nil {
			return n
		}
	}
	if m := prBodyClosePattern.FindStringSubmatch(pr.Body); len(m) == 2 {
		n, err := strconv.Atoi(m[1])
		if err == nil {
			return n
		}
	}
	return 0
}

// detectClosedWithOpenPR finds closed issues on the project board that have
// an open, unmerged PR. When fix=true, the issue is reopened and moved back
// to "In review" on the project board.
func (s *LifecycleAuditService) detectClosedWithOpenPR(
	ctx context.Context,
	boardItems []types.BoardItem,
	openPRs []types.PullRequest,
	issueSvc *IssueService,
	projSvc *ProjectService,
	owner, repo string,
	fix bool,
) []LifecycleFinding {
	var findings []LifecycleFinding

	for _, item := range boardItems {
		if item.IsPR {
			continue
		}
		if !strings.EqualFold(item.State, "CLOSED") {
			continue
		}

		for _, pr := range openPRs {
			if !prReferencesIssue(pr, item.Number) {
				continue
			}

			f := LifecycleFinding{
				Category:    "CLOSED_WITH_OPEN_PR",
				Severity:    "high",
				IssueNumber: item.Number,
				IssueTitle:  item.Title,
				IssueState:  "CLOSED",
				Detail:      fmt.Sprintf("issue is CLOSED but PR #%d (%s) is still OPEN and unmerged", pr.Number, pr.Title),
			}

			if fix {
				// Resolve against the item's OWN repo (N:1 boards aggregate
				// cross-repo issues) so a cross-repo item is not left unfixed. #3792.
				itemOwner, itemRepo := resolveItemRepo(item.Repo, owner, repo)
				fullIssue, fetchErr := issueSvc.GetIssue(ctx, itemOwner, itemRepo, item.Number)
				if fetchErr != nil {
					f.FixError = fmt.Sprintf("fetch issue node ID: %v", fetchErr)
				} else if reopenErr := issueSvc.ReopenIssue(ctx, fullIssue.NodeID); reopenErr != nil {
					f.FixError = reopenErr.Error()
				} else if syncErr := projSvc.SyncStatus(ctx, itemOwner, itemRepo, item.Number, "In review"); syncErr != nil {
					f.FixError = fmt.Sprintf("reopened but board sync failed: %v", syncErr)
				} else {
					f.Fixed = true
				}
			}

			findings = append(findings, f)
			break // one finding per issue — first matching open PR is enough
		}
	}

	return findings
}

// detectOpenPRClosedIssue finds open PRs whose linked issue is already closed.
// This is an orphan PR — the issue was closed without the PR being merged. It
// produces an informational finding only (no auto-fix: orphan PRs need human
// review to determine whether to close or revert).
func detectOpenPRClosedIssue(
	ctx context.Context,
	openPRs []types.PullRequest,
	issueSvc *IssueService,
	owner, repo string,
) []LifecycleFinding {
	var findings []LifecycleFinding

	for _, pr := range openPRs {
		issueNum := extractIssueNumber(pr)
		if issueNum == 0 {
			continue
		}

		issue, err := issueSvc.GetIssue(ctx, owner, repo, issueNum)
		if err != nil || !strings.EqualFold(issue.State, "CLOSED") {
			continue
		}

		findings = append(findings, LifecycleFinding{
			Category:    "OPEN_PR_CLOSED_ISSUE",
			Severity:    "high",
			IssueNumber: issueNum,
			IssueTitle:  issue.Title,
			IssueState:  "CLOSED",
			Detail:      fmt.Sprintf("PR #%d (%s) is OPEN but its linked issue #%d is already CLOSED — possible premature issue close or orphan PR", pr.Number, pr.Title, issueNum),
		})
	}

	return findings
}
