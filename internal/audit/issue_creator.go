package audit

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/nightgauge/nightgauge/internal/forge"
)

// IssueCreatorConfig holds configuration for issue creation.
type IssueCreatorConfig struct {
	Owner              string
	Repo               string // default repo (can be overridden per finding)
	ProjectNumber      int
	EpicLabel          string            // e.g. "type:epic"
	SeverityToPriority map[string]string // "critical" → "priority:critical"
	DryRun             bool
}

// Epic represents a grouped set of findings for a single dimension+repo.
type Epic struct {
	Title           string
	Dimension       string
	Repository      string
	Findings        []*AuditFinding
	SubIssueNumbers []int // filled after creation
}

// IssueCreationResult summarizes the run.
type IssueCreationResult struct {
	EpicsCreated   int      `json:"epics_created"`
	IssuesCreated  int      `json:"issues_created"`
	IssuesSkipped  int      `json:"issues_skipped"` // duplicates
	BlockedByAdded int      `json:"blocked_by_added"`
	Errors         []string `json:"errors,omitempty"`
}

// issueHandle is one created-or-found issue, carrying both identifiers the
// creation flow needs: the reference the issue-link REST endpoints take, and
// the node ID the ProjectV2 board operations take.
type issueHandle struct {
	NodeID string
	Ref    forge.IssueRef
}

// IssueCreator interface for GitHub operations (allows mocking in tests).
type IssueCreator interface {
	GetRepositoryID(ctx context.Context, owner, repo string) (string, error)
	CreateIssueWithID(ctx context.Context, owner, repo, title, body string, labels []string) (nodeID string, number int, err error)
	AddSubIssue(ctx context.Context, parent, child forge.IssueRef) error
	AddBlockedBy(ctx context.Context, blocked, blocker forge.IssueRef) error
	AddToProjectBoard(ctx context.Context, owner string, projectNumber int, issueNodeID string) error
	SetProjectItemStatus(ctx context.Context, owner string, projectNumber int, issueNodeID, status string) error
	SearchOpenIssueByTitle(ctx context.Context, owner, repo, title string) (number int, nodeID string, found bool, err error)
	GetLabelID(ctx context.Context, owner, repo, labelName string) (string, error)
}

// GroupFindingsByEpic groups AuditFindings by dimension + repository.
// Each epic title is "{DimensionName}: {Repository}". Findings within each
// epic are sorted by severity (critical→high→medium→low). The returned slice
// is sorted by epic title for determinism.
func GroupFindingsByEpic(report *SynthesisReport) []*Epic {
	type epicKey struct {
		dimension string
		repo      string
	}

	epicMap := make(map[epicKey]*Epic)
	order := make([]epicKey, 0)

	for _, dim := range report.Dimensions {
		for i := range dim.Findings {
			f := &dim.Findings[i]
			key := epicKey{dimension: dim.Name, repo: f.Repository}
			if _, exists := epicMap[key]; !exists {
				epicMap[key] = &Epic{
					Title:      GenerateEpicTitle(dim.Name, f.Repository),
					Dimension:  dim.Name,
					Repository: f.Repository,
				}
				order = append(order, key)
			}
			epicMap[key].Findings = append(epicMap[key].Findings, f)
		}
	}

	epics := make([]*Epic, 0, len(epicMap))
	for _, key := range order {
		epic := epicMap[key]
		// Sort findings by severity.
		sort.Slice(epic.Findings, func(i, j int) bool {
			return waveForSeverity(epic.Findings[i].Severity) < waveForSeverity(epic.Findings[j].Severity)
		})
		epics = append(epics, epic)
	}

	// Sort epics by title for determinism.
	sort.Slice(epics, func(i, j int) bool {
		return epics[i].Title < epics[j].Title
	})

	return epics
}

// waveForSeverity maps a severity string to a wave number for ordering.
func waveForSeverity(severity string) int {
	switch strings.ToLower(severity) {
	case "critical":
		return 0
	case "high":
		return 1
	case "medium":
		return 2
	case "low":
		return 3
	default:
		return 3
	}
}

// GenerateEpicTitle returns the title for an epic issue.
// Format: "{dimension} ({repo})" truncated to 200 chars.
func GenerateEpicTitle(dimension, repo string) string {
	title := fmt.Sprintf("%s (%s)", dimension, repo)
	if len(title) > 200 {
		title = title[:200]
	}
	return title
}

// GenerateEpicBody returns the markdown body for an epic issue.
func GenerateEpicBody(epic *Epic) string {
	sevCounts := map[string]int{
		"critical": 0,
		"high":     0,
		"medium":   0,
		"low":      0,
	}
	for _, f := range epic.Findings {
		sev := strings.ToLower(f.Severity)
		if _, ok := sevCounts[sev]; ok {
			sevCounts[sev]++
		}
	}

	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("## Audit Epic: %s\n\n", epic.Title))
	sb.WriteString(fmt.Sprintf("**Dimension:** %s\n\n", epic.Dimension))
	sb.WriteString(fmt.Sprintf("**Repository:** %s\n\n", epic.Repository))
	sb.WriteString(fmt.Sprintf("**Total Findings:** %d\n\n", len(epic.Findings)))
	sb.WriteString("### Severity Breakdown\n\n")
	sb.WriteString("| Severity | Count |\n")
	sb.WriteString("|----------|------:|\n")
	for _, sev := range []string{"critical", "high", "medium", "low"} {
		sb.WriteString(fmt.Sprintf("| %s | %d |\n", strings.Title(sev), sevCounts[sev]))
	}
	sb.WriteString("\n")
	sb.WriteString(fmt.Sprintf("_Generated: %s_\n\n", time.Now().UTC().Format("2006-01-02")))
	sb.WriteString("<!-- wave: 0 -->")
	return sb.String()
}

// GenerateSubIssueTitle returns the title for a sub-issue.
// Format: "[{Category}] {Description}" truncated to 200 chars total.
func GenerateSubIssueTitle(finding *AuditFinding) string {
	title := fmt.Sprintf("[%s] %s", finding.Category, finding.Description)
	if len(title) > 200 {
		title = title[:200]
	}
	return title
}

// GenerateSubIssueBody returns the markdown body for a sub-issue.
func GenerateSubIssueBody(finding *AuditFinding, wave int) string {
	var sb strings.Builder
	sb.WriteString("## Finding Description\n\n")
	sb.WriteString(finding.Description)
	sb.WriteString("\n\n")
	sb.WriteString("## Acceptance Criteria\n\n")
	if len(finding.AcceptanceCriteria) == 0 {
		sb.WriteString("- [ ] Resolve finding and verify fix\n")
	} else {
		for _, ac := range finding.AcceptanceCriteria {
			sb.WriteString(fmt.Sprintf("- [ ] %s\n", ac))
		}
	}
	sb.WriteString("\n")
	sb.WriteString(fmt.Sprintf("<!-- wave: %d -->", wave))
	return sb.String()
}

// RunIssueCreation is the main orchestration function for creating GitHub issues
// from a SynthesisReport. It groups findings into epics, creates epic and
// sub-issues, links them, adds them to the project board, and wires up any
// blocking relationships declared in findings.
func RunIssueCreation(ctx context.Context, report *SynthesisReport, cfg IssueCreatorConfig, creator IssueCreator) (*IssueCreationResult, error) {
	result := &IssueCreationResult{}

	epics := GroupFindingsByEpic(report)

	// Track each created issue per finding ID so we can wire blockedBy later.
	//
	// BOTH identifiers are kept, and both are needed. The link endpoints
	// address issues by owner/repo/number; AddToProjectBoard and
	// SetProjectItemStatus are ProjectV2 operations, which are GraphQL without
	// exception and take node IDs. Keeping only one would force a read to
	// recover the other.
	findingIssues := make(map[string]issueHandle) // findingID → handle

	// epicNodeIDs maps epic title → nodeID (for board addition).
	epicNodeIDs := make(map[string]string)

	// Resolve target repo: use cfg.Repo as default.
	repoFor := func(finding *AuditFinding) string {
		if finding.Repository != "" {
			return finding.Repository
		}
		return cfg.Repo
	}

	epicLabels := []string{}
	if cfg.EpicLabel != "" {
		epicLabels = append(epicLabels, cfg.EpicLabel)
	}

	for _, epic := range epics {
		epicRepo := cfg.Repo
		if epic.Repository != "" {
			epicRepo = epic.Repository
		}

		epicTitle := epic.Title
		epicBody := GenerateEpicBody(epic)

		// Check for existing epic.
		existingNumber, existingNodeID, found, err := creator.SearchOpenIssueByTitle(ctx, cfg.Owner, epicRepo, epicTitle)
		if err != nil {
			result.Errors = append(result.Errors, fmt.Sprintf("search epic %q: %v", epicTitle, err))
		}

		var epicNodeID string
		epicRef := forge.IssueRef{Owner: cfg.Owner, Repo: epicRepo}

		if found {
			result.IssuesSkipped++
			epicNodeID = existingNodeID
			epicRef.Number = existingNumber
			fmt.Printf("[SKIP] Epic already exists: %s\n", epicTitle)
		} else if cfg.DryRun {
			fmt.Printf("[DRY-RUN] Would create epic: %s (repo: %s)\n", epicTitle, epicRepo)
			fmt.Printf("  Body preview: %d chars\n", len(epicBody))
		} else {
			nodeID, number, createErr := creator.CreateIssueWithID(ctx, cfg.Owner, epicRepo, epicTitle, epicBody, epicLabels)
			if createErr != nil {
				result.Errors = append(result.Errors, fmt.Sprintf("create epic %q: %v", epicTitle, createErr))
				continue
			}
			epicNodeID = nodeID
			epicRef.Number = number
			result.EpicsCreated++
		}

		if epicNodeID != "" {
			epicNodeIDs[epicTitle] = epicNodeID
		}

		// Create sub-issues.
		for _, finding := range epic.Findings {
			wave := waveForSeverity(finding.Severity)
			subTitle := GenerateSubIssueTitle(finding)
			subBody := GenerateSubIssueBody(finding, wave)
			subRepo := repoFor(finding)

			labels := []string{}
			if cfg.SeverityToPriority != nil {
				if pLabel, ok := cfg.SeverityToPriority[strings.ToLower(finding.Severity)]; ok {
					labels = append(labels, pLabel)
				}
			}

			existingSubNumber, existingSubNodeID, subFound, searchErr := creator.SearchOpenIssueByTitle(ctx, cfg.Owner, subRepo, subTitle)
			if searchErr != nil {
				result.Errors = append(result.Errors, fmt.Sprintf("search sub-issue %q: %v", subTitle, searchErr))
			}

			if subFound {
				result.IssuesSkipped++
				findingIssues[finding.ID] = issueHandle{
					NodeID: existingSubNodeID,
					Ref:    forge.IssueRef{Owner: cfg.Owner, Repo: subRepo, Number: existingSubNumber},
				}
				fmt.Printf("[SKIP] Sub-issue already exists: %s\n", subTitle)
				continue
			}

			if cfg.DryRun {
				fmt.Printf("[DRY-RUN] Would create sub-issue: %s (repo: %s, wave: %d)\n", subTitle, subRepo, wave)
				fmt.Printf("  Body preview: %d chars\n", len(subBody))
				if epicNodeID != "" {
					fmt.Printf("[DRY-RUN] Would link sub-issue to epic: %s\n", epicTitle)
				}
				continue
			}

			subNodeID, subNumber, createErr := creator.CreateIssueWithID(ctx, cfg.Owner, subRepo, subTitle, subBody, labels)
			if createErr != nil {
				result.Errors = append(result.Errors, fmt.Sprintf("create sub-issue %q: %v", subTitle, createErr))
				continue
			}
			result.IssuesCreated++
			subHandle := issueHandle{
				NodeID: subNodeID,
				Ref:    forge.IssueRef{Owner: cfg.Owner, Repo: subRepo, Number: subNumber},
			}
			findingIssues[finding.ID] = subHandle

			// Link as sub-issue under epic.
			//
			// These two refs always name the SAME repository today, and that
			// is a property of GroupFindingsByEpic rather than of this call:
			// it keys epics on {dimension, repository}, so every finding in an
			// epic carries the epic's own Repository. Written as two refs
			// anyway, because the constraint lives in a different function and
			// a caller here cannot see it -- the blocked-by pass below is the
			// one that genuinely crosses repositories.
			if epicRef.Number > 0 {
				if linkErr := creator.AddSubIssue(ctx, epicRef, subHandle.Ref); linkErr != nil {
					result.Errors = append(result.Errors, fmt.Sprintf("add sub-issue %q to epic %q: %v", subTitle, epicTitle, linkErr))
				}
			}
		}

		// Add epic + sub-issues to project board and set status "Ready".
		if !cfg.DryRun && cfg.ProjectNumber > 0 {
			if epicNodeID != "" {
				if addErr := creator.AddToProjectBoard(ctx, cfg.Owner, cfg.ProjectNumber, epicNodeID); addErr != nil {
					result.Errors = append(result.Errors, fmt.Sprintf("add epic %q to board: %v", epicTitle, addErr))
				} else {
					if statusErr := creator.SetProjectItemStatus(ctx, cfg.Owner, cfg.ProjectNumber, epicNodeID, "Ready"); statusErr != nil {
						result.Errors = append(result.Errors, fmt.Sprintf("set status for epic %q: %v", epicTitle, statusErr))
					}
				}
			}

			for _, finding := range epic.Findings {
				sub, ok := findingIssues[finding.ID]
				subNodeID := sub.NodeID
				if !ok || subNodeID == "" {
					continue
				}
				if addErr := creator.AddToProjectBoard(ctx, cfg.Owner, cfg.ProjectNumber, subNodeID); addErr != nil {
					result.Errors = append(result.Errors, fmt.Sprintf("add sub-issue %q to board: %v", finding.ID, addErr))
					continue
				}
				if statusErr := creator.SetProjectItemStatus(ctx, cfg.Owner, cfg.ProjectNumber, subNodeID, "Ready"); statusErr != nil {
					result.Errors = append(result.Errors, fmt.Sprintf("set status for sub-issue %q: %v", finding.ID, statusErr))
				}
			}
		} else if cfg.DryRun && cfg.ProjectNumber > 0 {
			fmt.Printf("[DRY-RUN] Would add epic and sub-issues to project board #%d with status Ready\n", cfg.ProjectNumber)
		}
	}

	// Wire blocking relationships declared in findings.
	if !cfg.DryRun {
		for _, dim := range report.Dimensions {
			for _, finding := range dim.Findings {
				if len(finding.BlockedBy) == 0 {
					continue
				}
				blocked, ok := findingIssues[finding.ID]
				if !ok || blocked.Ref.Number == 0 {
					continue
				}
				for _, blockerID := range finding.BlockedBy {
					blocker, ok := findingIssues[blockerID]
					if !ok || blocker.Ref.Number == 0 {
						result.Errors = append(result.Errors, fmt.Sprintf("blocker finding %q not found for finding %q", blockerID, finding.ID))
						continue
					}
					// Crosses epic AND repository boundaries by construction:
					// this pass walks every dimension, so a finding may be
					// blocked by one filed in a different repository.
					if err := creator.AddBlockedBy(ctx, blocked.Ref, blocker.Ref); err != nil {
						result.Errors = append(result.Errors, fmt.Sprintf("addBlockedBy %q → %q: %v", finding.ID, blockerID, err))
					} else {
						result.BlockedByAdded++
					}
				}
			}
		}
	} else {
		// Dry-run: report what blocking relationships would be created.
		for _, dim := range report.Dimensions {
			for _, finding := range dim.Findings {
				for _, blockerID := range finding.BlockedBy {
					fmt.Printf("[DRY-RUN] Would add blockedBy: %s → %s\n", finding.ID, blockerID)
				}
			}
		}
	}

	return result, nil
}

// ensure time import is used (GenerateEpicBody uses time.Now).
var _ = time.Now
