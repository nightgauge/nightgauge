// Package backlogpreflight implements deterministic validation checks for
// backlog issues on a GitHub project board. It is the Go-backed implementation
// of the checks previously performed by shell in
// skills/nightgauge-backlog-preflight/SKILL.md Phase 2 (Checks 2.1–2.5).
//
// Audit reference: docs/SKILL_DETERMINISM_AUDIT.md row B26.
package backlogpreflight

// FindingType classifies what validation check failed.
type FindingType string

const (
	FindingTypeMissingTypeLabel       FindingType = "missing_type_label"
	FindingTypeMissingSize            FindingType = "missing_size_field"
	FindingTypeMissingPriority        FindingType = "missing_priority_field"
	FindingTypeWeakAcceptanceCriteria FindingType = "weak_acceptance_criteria"
	FindingTypeDependencyCycle        FindingType = "dependency_cycle"
	FindingTypeGreenfieldWarning      FindingType = "greenfield_warning"
)

// Severity levels for BacklogFinding.
const (
	SeverityHigh   = "high"
	SeverityMedium = "medium"
	SeverityLow    = "low"
)

// BacklogFinding is a single validation failure for one issue.
type BacklogFinding struct {
	IssueNumber int         `json:"issue_number"`
	IssueTitle  string      `json:"issue_title"`
	FindingType FindingType `json:"finding_type"`
	Severity    string      `json:"severity"`
	Detail      string      `json:"detail"`
	Suggestion  string      `json:"suggestion"`
}

// Summary holds aggregate counts for the preflight report.
type Summary struct {
	TotalIssues   int            `json:"total_issues"`
	IssuesClean   int            `json:"issues_clean"`
	IssuesFlagged int            `json:"issues_flagged"`
	ByFindingType map[string]int `json:"by_finding_type"`
	BySeverity    map[string]int `json:"by_severity"`
}

// BacklogPreflightReport is the top-level JSON output for `backlog preflight --json`.
type BacklogPreflightReport struct {
	V           int              `json:"v"` // schema version, currently 1
	Owner       string           `json:"owner"`
	Repo        string           `json:"repo"`
	Status      string           `json:"status"`
	Focus       string           `json:"focus"`
	Findings    []BacklogFinding `json:"findings"`
	Summary     Summary          `json:"summary"`
	GeneratedAt string           `json:"generated_at"`
}
