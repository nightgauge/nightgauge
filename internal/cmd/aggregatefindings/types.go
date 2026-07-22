// Package aggregatefindings reads .nightgauge/ assessment reports
// (health-check, security-audit, test-scaffold), applies severity
// normalization, deduplicates overlapping findings, and emits a stable JSON
// schema. Replaces the shell+jq extraction in modernize-plan SKILL.md
// Phase 2.1–2.4 (audit row B31).
package aggregatefindings

// Severity canonical values used across all sources.
const (
	SeverityCritical = "critical"
	SeverityHigh     = "high"
	SeverityMedium   = "medium"
	SeverityLow      = "low"
	SeverityInfo     = "info"
)

// Finding is a normalized, deduplicated finding from any assessment source.
type Finding struct {
	ID              string   `json:"id"`
	Title           string   `json:"title"`
	Description     string   `json:"description"`
	Recommendation  string   `json:"recommendation"`
	Source          string   `json:"source"`                // "health-check"|"security-audit"|"test-scaffold"
	SourceDimension string   `json:"source_dimension"`      // e.g. "dependency_health"
	Severity        string   `json:"severity"`              // canonical: critical|high|medium|low|info
	MergedFrom      []string `json:"merged_from,omitempty"` // IDs of deduplicated siblings
}

// Result is the stable JSON output of aggregate-findings (schema v1).
type Result struct {
	V              int       `json:"v"`
	SourcesRead    []string  `json:"sources_read"`
	SourcesMissing []string  `json:"sources_missing"`
	Findings       []Finding `json:"findings"`
	Summary        Summary   `json:"summary"`
	GeneratedAt    string    `json:"generated_at"`
}

// Summary holds aggregate counts for the aggregate-findings result.
type Summary struct {
	TotalFindings     int            `json:"total_findings"`
	AfterDedup        int            `json:"after_dedup"`
	BySeverity        map[string]int `json:"by_severity"`
	DeduplicationRate float64        `json:"deduplication_rate"`
}
