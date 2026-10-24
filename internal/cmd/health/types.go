// Package health implements CLI-layer reading and aggregation of pipeline
// health data files (the checkout's .git/nightgauge-worktree/health/*.jsonl).
package health

// TrendSchemaVersion is the one schema of the health series (#1898).
const TrendSchemaVersion = "2"

// HealthTrendEntry mirrors the SDK's HealthTrendEntry: one entry per pipeline
// run in the checkout's health/trends.jsonl, the only health series.
//
// OverallScore is nil when no dimension had data (#1197). It is a pointer so
// that absence is never decoded as a score of 0.
type HealthTrendEntry struct {
	SchemaVersion      string             `json:"schema_version"`
	Timestamp          string             `json:"timestamp"`
	RunID              string             `json:"run_id"`
	IssueNumber        int                `json:"issue_number"`
	OverallScore       *float64           `json:"overall_score"`
	Status             string             `json:"status"`
	Dimensions         map[string]float64 `json:"dimensions"`
	ExcludedDimensions map[string]string  `json:"excluded_dimensions"`
	Findings           []string           `json:"significant_findings"`
}

// GateMetricsEntry records one gate evaluation result.
type GateMetricsEntry struct {
	GateName   string `json:"gate_name"`
	Timestamp  string `json:"timestamp"`
	Result     string `json:"result"` // "pass", "catch", "skip"
	Reason     string `json:"reason"`
	DurationMs int    `json:"duration_ms"`
}

// GateMetricsAggregate is the aggregated summary for one gate.
type GateMetricsAggregate struct {
	GateName        string  `json:"gate_name"`
	Invocations     int     `json:"invocations"`
	Catches         int     `json:"catches"`
	Skipped         int     `json:"skipped"`
	HitRate         float64 `json:"hit_rate"`
	AverageDuration float64 `json:"average_duration_ms"`
}
