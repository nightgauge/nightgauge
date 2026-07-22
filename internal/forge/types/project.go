package forgetypes

import pkgtypes "github.com/nightgauge/nightgauge/pkg/types"

// BoardItem represents an item on a forge's project/board view.
// Aliased from pkg/types.BoardItem.
type BoardItem = pkgtypes.BoardItem

// StatusCounts holds per-status item counts from the project board.
type StatusCounts = pkgtypes.StatusCounts

// BulkAddResult summarizes the outcome of a bulk add operation against the
// project board.
type BulkAddResult struct {
	Total   int      `json:"total"`
	Added   int      `json:"added"`
	Skipped int      `json:"skipped"`
	Failed  int      `json:"failed"`
	Errors  []string `json:"errors"`
	Mode    string   `json:"mode"`
}

// FieldDrift represents a detected field value mismatch between a labeled
// state and the corresponding board field value.
type FieldDrift struct {
	IssueNumber int    `json:"issueNumber"`
	Repo        string `json:"repo"`
	Title       string `json:"title"`
	FieldName   string `json:"fieldName"`
	Expected    string `json:"expected"`
	Actual      string `json:"actual"`
}

// FieldInfo is a read-only view of a single project field's metadata.
type FieldInfo struct {
	ID      string
	Type    string
	Options map[string]string
}

// FieldsSnapshot is a deep-copy of the project ID and field metadata at a
// point in time. The snapshot is safe to mutate without affecting the
// underlying ProjectService.
type FieldsSnapshot struct {
	ProjectID string
	Fields    map[string]FieldInfo
}

// FieldSchema describes the required field set for a forge's project board.
type FieldSchema struct {
	SingleSelectFields []SingleSelectFieldDef
	DateFields         []string
	NumberFields       []string
}

// SingleSelectFieldDef defines a single-select field and its required options.
type SingleSelectFieldDef struct {
	Name    string
	Options []SingleSelectOptionDef
}

// SingleSelectOptionDef defines one option within a single-select field.
// Color is forge-specific (GitHub uses ProjectV2SingleSelectFieldOptionColor
// enum names like BLUE, RED).
type SingleSelectOptionDef struct {
	Name  string
	Color string
}

// EnsureFieldsResult reports per-field outcomes from EnsureFields.
type EnsureFieldsResult struct {
	Created  []string          `json:"created"`
	Updated  []string          `json:"updated"`
	Already  []string          `json:"already"`
	FieldIDs map[string]string `json:"field_ids"`
}

// Iteration is the cross-forge view of a sprint / iteration. GitHub Projects
// V2 surfaces iteration cadence configuration; GitLab EE has native iterations
// at the group level; CE falls back to project milestones. The Edition tag
// records which source produced the iteration so callers can distinguish a
// real EE iteration from a CE milestone fallback.
type Iteration struct {
	ID        string `json:"id"`        // adapter-opaque
	Title     string `json:"title"`     //
	State     string `json:"state"`     // upcoming|current|closed (GitLab) | open|closed (GitHub)
	StartDate string `json:"startDate"` // ISO 8601 (empty when source is a GitHub iteration)
	DueDate   string `json:"dueDate"`   // ISO 8601
	Edition   string `json:"edition"`   // "ee" | "ce-milestone-fallback" | "github"
}

// HealthStatus is the cross-forge issue health enum. GitHub Projects V2 carries
// it as a single-select option; GitLab EE has it as a native field with the
// values on_track/needs_attention/at_risk. CE lacks the field and falls back
// to a scoped label.
type HealthStatus string

const (
	HealthOnTrack        HealthStatus = "on_track"
	HealthNeedsAttention HealthStatus = "needs_attention"
	HealthAtRisk         HealthStatus = "at_risk"
)

// MapHealthFromGitHub translates a GitHub Projects V2 single-select option
// name (the canonical UI labels "On Track", "Needs Attention", "At Risk") to
// the adapter-agnostic HealthStatus enum. Unknown labels return an empty
// HealthStatus so callers can decide whether to fail or skip.
func MapHealthFromGitHub(label string) HealthStatus {
	switch label {
	case "On Track", "On track", "on_track":
		return HealthOnTrack
	case "Needs Attention", "Needs attention", "needs_attention":
		return HealthNeedsAttention
	case "At Risk", "At risk", "at_risk":
		return HealthAtRisk
	}
	return ""
}

// MapHealthToGitHub returns the canonical GitHub Projects V2 single-select
// option name corresponding to the given HealthStatus. The empty value maps
// to an empty string so callers can decide whether to skip the write.
func MapHealthToGitHub(h HealthStatus) string {
	switch h {
	case HealthOnTrack:
		return "On Track"
	case HealthNeedsAttention:
		return "Needs Attention"
	case HealthAtRisk:
		return "At Risk"
	}
	return ""
}
