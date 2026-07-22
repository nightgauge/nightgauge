// Package health implements CLI-layer reading and aggregation of pipeline
// health data files (.nightgauge/health/*.jsonl).
package health

// HealthTrendEntry mirrors the schema written by the SDK's HealthTrendsWriter.
type HealthTrendEntry struct {
	SchemaVersion string             `json:"schema_version"`
	Timestamp     string             `json:"timestamp"`
	RunID         string             `json:"run_id"`
	IssueNumber   int                `json:"issue_number"`
	OverallScore  float64            `json:"overall_score"`
	Dimensions    map[string]float64 `json:"dimensions"`
	Findings      []string           `json:"significant_findings"`
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
