package backlogpreflight

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/nightgauge/nightgauge/internal/intelligence/acparse"
	"github.com/nightgauge/nightgauge/pkg/types"
)

// boardLister is satisfied by *github.BoardService.
type boardLister interface {
	ListItems(ctx context.Context, statusFilter string) ([]types.BoardItem, error)
}

// issueGetter is satisfied by *github.IssueService.
type issueGetter interface {
	GetIssue(ctx context.Context, owner, repo string, number int) (*types.Issue, error)
}

// Validator runs deterministic preflight checks on board items.
type Validator struct {
	board  boardLister
	issues issueGetter
	owner  string
	repo   string
}

// New creates a Validator with the given board and issue services.
func New(board boardLister, issues issueGetter, owner, repo string) *Validator {
	return &Validator{board: board, issues: issues, owner: owner, repo: repo}
}

// validTypeLabels is the set of accepted type:* label prefixes.
var validTypeLabelPrefixes = []string{
	"type:feature",
	"type:bug",
	"type:docs",
	"type:refactor",
	"type:chore",
	"type:epic",
	"type:spike",
}

// CheckLabels returns a finding for each item missing a type:* label.
func (v *Validator) CheckLabels(items []types.BoardItem) []BacklogFinding {
	var findings []BacklogFinding
	for _, item := range items {
		if hasTypeLabel(item.Labels) {
			continue
		}
		findings = append(findings, BacklogFinding{
			IssueNumber: item.Number,
			IssueTitle:  item.Title,
			FindingType: FindingTypeMissingTypeLabel,
			Severity:    SeverityHigh,
			Detail:      fmt.Sprintf("#%d %q has no type:* label", item.Number, item.Title),
			Suggestion:  "Add one of: type:feature, type:bug, type:docs, type:refactor, type:chore, type:epic, type:spike",
		})
	}
	return findings
}

// hasTypeLabel returns true if any label in labels starts with "type:".
func hasTypeLabel(labels []string) bool {
	for _, l := range labels {
		for _, prefix := range validTypeLabelPrefixes {
			if l == prefix {
				return true
			}
		}
		// Also accept any label that starts with "type:" for forward-compatibility.
		if strings.HasPrefix(l, "type:") {
			return true
		}
	}
	return false
}

// CheckBoardFields returns a finding for each item missing Size or Priority.
func (v *Validator) CheckBoardFields(items []types.BoardItem) []BacklogFinding {
	var findings []BacklogFinding
	for _, item := range items {
		if string(item.Size) == "" {
			findings = append(findings, BacklogFinding{
				IssueNumber: item.Number,
				IssueTitle:  item.Title,
				FindingType: FindingTypeMissingSize,
				Severity:    SeverityMedium,
				Detail:      fmt.Sprintf("#%d %q has no Size board field", item.Number, item.Title),
				Suggestion:  "Set the Size field on the project board (XS, S, M, L, XL)",
			})
		}
		if string(item.Priority) == "" {
			findings = append(findings, BacklogFinding{
				IssueNumber: item.Number,
				IssueTitle:  item.Title,
				FindingType: FindingTypeMissingPriority,
				Severity:    SeverityMedium,
				Detail:      fmt.Sprintf("#%d %q has no Priority board field", item.Number, item.Title),
				Suggestion:  "Set the Priority field on the project board (P0, P1, P2, P3)",
			})
		}
	}
	return findings
}

// minBodyLen is the minimum character count for an acceptable issue body.
const minBodyLen = 100

// minCheckboxCount is the minimum number of AC checkboxes required.
const minCheckboxCount = 2

// CheckAcceptanceCriteria fetches each item's body and returns findings for
// issues with < 100 chars of body or fewer than 2 checkbox ACs.
// Skips GetIssue calls when the focus flag indicates criteria checks are off.
func (v *Validator) CheckAcceptanceCriteria(ctx context.Context, items []types.BoardItem) []BacklogFinding {
	var findings []BacklogFinding
	for _, item := range items {
		issue, err := v.issues.GetIssue(ctx, v.owner, v.repo, item.Number)
		if err != nil {
			// Non-fatal: emit a finding noting the body could not be fetched.
			findings = append(findings, BacklogFinding{
				IssueNumber: item.Number,
				IssueTitle:  item.Title,
				FindingType: FindingTypeWeakAcceptanceCriteria,
				Severity:    SeverityMedium,
				Detail:      fmt.Sprintf("#%d %q body could not be fetched: %v", item.Number, item.Title, err),
				Suggestion:  "Ensure the issue is accessible and has a body with acceptance criteria",
			})
			continue
		}

		body := issue.Body
		if len(body) < minBodyLen {
			findings = append(findings, BacklogFinding{
				IssueNumber: item.Number,
				IssueTitle:  item.Title,
				FindingType: FindingTypeWeakAcceptanceCriteria,
				Severity:    SeverityHigh,
				Detail:      fmt.Sprintf("#%d %q body is too short (%d chars, minimum %d)", item.Number, item.Title, len(body), minBodyLen),
				Suggestion:  "Add a description and acceptance criteria checkboxes to the issue body",
			})
			continue
		}

		result := acparse.Parse(body)
		if result.Total < minCheckboxCount {
			findings = append(findings, BacklogFinding{
				IssueNumber: item.Number,
				IssueTitle:  item.Title,
				FindingType: FindingTypeWeakAcceptanceCriteria,
				Severity:    SeverityHigh,
				Detail:      fmt.Sprintf("#%d %q has fewer than %d acceptance criteria checkboxes (found: %d)", item.Number, item.Title, minCheckboxCount, result.Total),
				Suggestion:  "Add at least 2 checkbox acceptance criteria (- [ ] ...) to the issue body",
			})
		}
	}
	return findings
}

// CheckDependencyCycles returns findings for any dependency cycles among items.
// Uses a plain in-memory DFS over BlockedBy edges — no external package needed.
// Only considers OPEN blockedBy entries to avoid false positives from resolved blockers.
func (v *Validator) CheckDependencyCycles(items []types.BoardItem) []BacklogFinding {
	// Build index: issue number → item
	byNumber := make(map[int]types.BoardItem, len(items))
	for _, item := range items {
		byNumber[item.Number] = item
	}

	// adjacency: number → set of numbers it is blocked by (only OPEN, in-set)
	adj := make(map[int][]int, len(items))
	for _, item := range items {
		for _, ref := range item.BlockedBy {
			if strings.EqualFold(ref.State, "open") {
				if _, inSet := byNumber[ref.Number]; inSet {
					adj[item.Number] = append(adj[item.Number], ref.Number)
				}
			}
		}
	}

	// DFS cycle detection: white=0, gray=1, black=2
	color := make(map[int]int, len(items))
	var findings []BacklogFinding
	reported := make(map[string]bool)

	var dfs func(n int, path []int) bool
	dfs = func(n int, path []int) bool {
		color[n] = 1 // gray — in current path
		for _, next := range adj[n] {
			if color[next] == 1 {
				// Found a cycle — build cycle string
				cycleStart := -1
				for i, p := range path {
					if p == next {
						cycleStart = i
						break
					}
				}
				cyclePath := append(path[cycleStart:], next)
				key := cycleKey(cyclePath)
				if !reported[key] {
					reported[key] = true
					parts := make([]string, len(cyclePath))
					for i, p := range cyclePath {
						parts[i] = fmt.Sprintf("#%d", p)
					}
					detail := fmt.Sprintf("Dependency cycle: %s", strings.Join(parts, " → "))
					findings = append(findings, BacklogFinding{
						IssueNumber: cyclePath[0],
						IssueTitle:  byNumber[cyclePath[0]].Title,
						FindingType: FindingTypeDependencyCycle,
						Severity:    SeverityHigh,
						Detail:      detail,
						Suggestion:  "Remove one of the blocking relationships to break the cycle",
					})
				}
				return true
			}
			if color[next] == 0 {
				dfs(next, append(path, next))
			}
		}
		color[n] = 2 // black — done
		return false
	}

	for _, item := range items {
		if color[item.Number] == 0 {
			dfs(item.Number, []int{item.Number})
		}
	}
	return findings
}

// cycleKey returns a canonical string key for a cycle path (sorted smallest number first).
func cycleKey(path []int) string {
	if len(path) == 0 {
		return ""
	}
	// Find the minimum element and rotate to start there.
	minIdx := 0
	for i, v := range path {
		if v < path[minIdx] {
			minIdx = i
		}
	}
	rotated := append(path[minIdx:], path[:minIdx]...)
	parts := make([]string, len(rotated))
	for i, p := range rotated {
		parts[i] = fmt.Sprintf("%d", p)
	}
	return strings.Join(parts, "-")
}

// CheckGreenfield checks for expected project structure files.
// Returns one finding per missing item.
func (v *Validator) CheckGreenfield(workdir string) []BacklogFinding {
	type check struct {
		path       string
		detail     string
		suggestion string
	}
	checks := []check{
		{
			path:       filepath.Join(workdir, ".nightgauge", "complexity-model.yaml"),
			detail:     "Missing .nightgauge/complexity-model.yaml — pipeline size gate has no calibration data",
			suggestion: "Run: nightgauge size calibrate to generate the complexity model",
		},
		{
			path:       filepath.Join(workdir, "docs"),
			detail:     "Missing docs/ directory — project has no documentation structure",
			suggestion: "Create a docs/ directory with at minimum README.md and CODE_STANDARDS.md",
		},
		{
			path:       filepath.Join(workdir, "docs", "CODE_STANDARDS.md"),
			detail:     "Missing docs/CODE_STANDARDS.md — pipeline cannot apply code standards during feature-dev",
			suggestion: "Create docs/CODE_STANDARDS.md with naming, structure, and style conventions",
		},
	}

	// At least one SECURITY*.md must exist under docs/
	securityFound := false
	securityGlob := filepath.Join(workdir, "docs", "SECURITY*.md")
	if matches, err := filepath.Glob(securityGlob); err == nil && len(matches) > 0 {
		securityFound = true
	}

	var findings []BacklogFinding
	for _, c := range checks {
		if _, err := os.Stat(c.path); os.IsNotExist(err) {
			findings = append(findings, BacklogFinding{
				IssueNumber: 0, // not issue-specific
				IssueTitle:  "",
				FindingType: FindingTypeGreenfieldWarning,
				Severity:    SeverityLow,
				Detail:      c.detail,
				Suggestion:  c.suggestion,
			})
		}
	}
	if !securityFound {
		findings = append(findings, BacklogFinding{
			IssueNumber: 0,
			IssueTitle:  "",
			FindingType: FindingTypeGreenfieldWarning,
			Severity:    SeverityLow,
			Detail:      "Missing docs/SECURITY*.md — pipeline has no security standards to apply during feature-dev",
			Suggestion:  "Create docs/SECURITY.md or docs/SECURITY_AND_ERROR_HANDLING.md",
		})
	}
	return findings
}

// BuildReport aggregates findings into a BacklogPreflightReport.
func BuildReport(owner, repo, status, focus string, items []types.BoardItem, findings []BacklogFinding) BacklogPreflightReport {
	byType := make(map[string]int)
	bySeverity := make(map[string]int)
	flaggedNums := make(map[int]bool)

	for _, f := range findings {
		byType[string(f.FindingType)]++
		bySeverity[f.Severity]++
		if f.IssueNumber > 0 {
			flaggedNums[f.IssueNumber] = true
		}
	}

	issuesFlagged := len(flaggedNums)
	// Greenfield findings have IssueNumber=0; count separately.
	for _, f := range findings {
		if f.IssueNumber == 0 {
			issuesFlagged = 0 // reset to avoid double-count; greenfield is not per-issue
			break
		}
	}
	issuesFlagged = len(flaggedNums)

	return BacklogPreflightReport{
		V:        1,
		Owner:    owner,
		Repo:     repo,
		Status:   status,
		Focus:    focus,
		Findings: findings,
		Summary: Summary{
			TotalIssues:   len(items),
			IssuesClean:   len(items) - issuesFlagged,
			IssuesFlagged: issuesFlagged,
			ByFindingType: byType,
			BySeverity:    bySeverity,
		},
	}
}
