// Package batchfailures implements deterministic extraction of pipeline
// failure rows from .nightgauge/pipeline/batch-state.json AND
// .nightgauge/pipeline/history/YYYY-MM-DD.jsonl. It is the Go-backed
// implementation of the inline-Python parsers previously embedded in
// skills/nightgauge-retro/SKILL.md Phases 2.1, 2.2, and 2.4.
//
// Audit reference: docs/SKILL_DETERMINISM_AUDIT.md row B29.
package batchfailures

// SchemaVersion is the JSON schema version emitted by Extract. Additive
// evolution only — renames or removed fields require a bump. Mirrors the
// versioning discipline of internal/pipeline/aggregator.go (B2).
const SchemaVersion = 1

// SourceType labels the data source a failure row came from. Phase 3 of the
// retro skill uses this to deduplicate by issue number, preferring history
// over batch-state over context-files.
const (
	SourceBatchState   = "batch-state"
	SourceHistory      = "history"
	SourceContextFiles = "context-files"
)

// AppliedFilters echoes the input flags so consumers can confirm what was
// applied without re-parsing CLI args.
type AppliedFilters struct {
	Issue       int    `json:"issue"`
	Since       string `json:"since"`
	AllFailures bool   `json:"all_failures"`
	Workdir     string `json:"workdir"`
}

// BatchFailure mirrors the failure row that retro Phase 2.1 wrote to
// /tmp/retro_batch.json. Field names are locked at v1 — Phase 3 of the retro
// skill consumes these JSON keys directly.
type BatchFailure struct {
	IssueNumber     int            `json:"issue_number"`
	Title           string         `json:"title"`
	Status          string         `json:"status"`
	CompletedStages []string       `json:"completed_stages"`
	FailedStages    []string       `json:"failed_stages"`
	DurationMs      int64          `json:"duration_ms"`
	TokenUsage      map[string]any `json:"token_usage"`
	Source          string         `json:"source"`
}

// BatchSummary captures the batch-state.json metadata that retro Phase 2.1
// emitted alongside the failure rows.
type BatchSummary struct {
	BatchStatus    string `json:"batch_status"`
	BatchStartedAt string `json:"batch_started_at"`
	BatchUpdatedAt string `json:"batch_updated_at"`
	TotalIssues    int    `json:"total_issues"`
}

// HistoryFailure mirrors the failure row that retro Phase 2.2 wrote to
// /tmp/retro_history.json. Field names are locked at v1.
type HistoryFailure struct {
	IssueNumber      int               `json:"issue_number"`
	Title            string            `json:"title"`
	Outcome          string            `json:"outcome"`
	StartedAt        string            `json:"started_at"`
	TotalDurationMs  int64             `json:"total_duration_ms"`
	StageFailures    map[string]string `json:"stage_failures"`
	EstimatedCostUSD float64           `json:"estimated_cost_usd"`
	Source           string            `json:"source"`
}

// ContextFileFailure mirrors the row retro Phase 2.4 wrote to
// /tmp/retro_context.json (fallback when no history or batch state exists).
type ContextFileFailure struct {
	IssueNumber     int    `json:"issue_number"`
	HasDevContext   bool   `json:"has_dev_context"`
	Source          string `json:"source"`
	InferredFailure string `json:"inferred_failure"`
}

// Result is the stable JSON output schema for
// `nightgauge pipeline batch-failures`. Schema version 1.
//
// Field-name stability is the contract: retro Phase 3 reads these keys
// directly when building the unified failure event list.
type Result struct {
	V               int                  `json:"v"`
	Filters         AppliedFilters       `json:"filters"`
	Batch           *BatchSummary        `json:"batch,omitempty"`
	BatchFailures   []BatchFailure       `json:"batch_failures"`
	HistoryFailures []HistoryFailure     `json:"history_failures"`
	ContextFailures []ContextFileFailure `json:"context_failures"`
	SkippedRecords  int                  `json:"skipped_records"`
	Warnings        []string             `json:"warnings"`
}
