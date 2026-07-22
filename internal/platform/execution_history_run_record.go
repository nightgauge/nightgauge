package platform

// executionHistoryRunRecordSchemaVersion is the schema version stamped on
// every ExecutionHistoryRunRecord — must match the platform's
// TELEMETRY_SCHEMA_VERSION_V5 (acme-platform
// packages/shared-types/src/telemetry.ts). V5 is V4 plus a per-stage
// `provider` (the executing adapter); the platform's ingest endpoint accepts
// both via a discriminated union on `schemaVersion`, and only V5 carries the
// adapter the dashboard's Adapter Mix donut aggregates (#268). Emitting V5
// unconditionally (with a nullable provider) is the single canonical shape —
// there is no reason to keep sending V4 now that the mapper populates provider.
const executionHistoryRunRecordSchemaVersion = 5

// ExecutionHistoryRunRecord is the wire body for one completed pipeline run
// POSTed to POST /v1/telemetry/pipeline-run — the single canonical
// pipeline-run telemetry sink (Issue
// It mirrors the platform's
// ExecutionHistoryRunRecordV5 Zod schema
// (acme-platform packages/shared-types/src/telemetry.ts) field for
// field — V5 is V4 plus the per-stage `provider` (executing adapter), which
// the platform persists to cost_events.provider and backfills onto
// pipeline_events.adapter for the Adapter Mix donut (#268). The endpoint
// accepts V4 and V5 via a discriminated union on `schemaVersion`; the VSCode
// extension's TypeScript mapper
// (packages/nightgauge-vscode/src/services/telemetry/pipelineRunV4Mapper.ts)
// still emits V4 (best-effort mirror of this authoritative Go push), so the
// two overlap idempotently on the platform side.
//
// Every field is a required JSON key on the wire (the platform validates
// against a `.strict()` Zod schema whose fields are `.nullable()` but NOT
// `.optional()`), so NONE of these fields use `omitempty` — a nil pointer
// must still serialize its key with a `null` value, not be dropped.
type ExecutionHistoryRunRecord struct {
	SchemaVersion int    `json:"schemaVersion"`
	IssueNumber   int    `json:"issueNumber"`
	Repo          string `json:"repo"`
	// PipelineRunID is the run's own UUID (state.V2RunRecord.RunID). The
	// platform schema declares it `.optional()` (NOT nullable) — unlike every
	// field below — so this one field uses omitempty and the mapper only sets
	// it when the local record carries a well-formed UUID. Sending it lets the
	// platform's terminal upsert converge on the SAME pipeline_runs row the
	// live event stream created, instead of minting a duplicate row under a
	// derived ID (#261).
	PipelineRunID string `json:"pipelineRunId,omitempty"`
	StartedAt     string `json:"startedAt"`
	// CompletedAt is nil for a run that has not finished — should not occur on
	// the telemetry-push path (only completed runs are pushed) but the field
	// stays nullable to match the schema.
	CompletedAt *string `json:"completedAt"`
	// Outcome is one of complete|failed|cancelled (TELEMETRY_OUTCOMES) — a
	// distinct, narrower vocabulary than the (retired) /v1/pipelines/runs
	// OUTCOMES enum (success|failure|cancelled|partial).
	Outcome             string  `json:"outcome"`
	TerminalFailureKind *string `json:"terminalFailureKind"`
	// OutcomeType refines a failed run into a first-class, needs-human outcome
	// (today: "blocked" — a pr-merge blocked by a required-check/branch-ruleset
	// config no retry can clear). null for ordinary runs. The platform stores it
	// on pipeline_runs.outcome_type so the dashboard shows "blocked" instead of a
	// generic failure.
	OutcomeType *string `json:"outcomeType"`
	// PredictedSize / ActualSize must be one of XS|S|M|L|XL (TELEMETRY_SIZES) or
	// null — see validTelemetrySize.
	PredictedSize  *string `json:"predictedSize"`
	ActualSize     *string `json:"actualSize"`
	PredictedModel *string `json:"predictedModel"`
	ActualModel    *string `json:"actualModel"`
	// ComplexityScore must be one of 1|2|3|5|8 (TELEMETRY_COMPLEXITY_SCORES) or
	// null — see validTelemetryComplexity.
	ComplexityScore *int     `json:"complexityScore"`
	Retries         int      `json:"retries"`
	DurationMs      *int64   `json:"durationMs"`
	TotalCostUsd    *float64 `json:"totalCostUsd"`
	// Stages and Agents are required non-null arrays — always initialised to a
	// non-nil (possibly empty) slice by the mapper so they serialize as `[]`
	// rather than `null`.
	Stages []ExecutionHistoryStageMetric `json:"stages"`
	Agents []any                         `json:"agents"`
	// RoutingPath is nullable (unlike Stages/Agents) — nil serializes to
	// `null`, matching `routingPath: string[] | null` on the wire.
	RoutingPath []string `json:"routingPath"`
	// IssueTitle / IssueBody / Labels carry the GitHub issue context captured at
	// pickup (#183) so the dashboard run-detail page can show what a run is doing
	// (title in the hero band, body + labels in an "Issue" section) without
	// leaving the dashboard. Unlike the fields above these are `.optional()` (NOT
	// just `.nullable()`) in the platform's additive schema — an older payload
	// that omits them still validates — so they carry `omitempty`: a run with no
	// captured context simply drops the keys rather than sending `null`. Bounded
	// to match the platform: issueTitle .max(256), issueBody .max(8192). Labels
	// mirrors the already-accepted optional `labels` field.
	IssueTitle *string  `json:"issueTitle,omitempty"`
	IssueBody  *string  `json:"issueBody,omitempty"`
	Labels     []string `json:"labels,omitempty"`
}

// ExecutionHistoryStageMetric is one element of ExecutionHistoryRunRecord.Stages.
// Mirrors the platform's StageMetricSchema / TS V4StageMetric. All fields are
// required keys (no omitempty) for the same reason as ExecutionHistoryRunRecord.
type ExecutionHistoryStageMetric struct {
	StageID      string   `json:"stageId"`
	StageName    string   `json:"stageName"`
	Attempt      int      `json:"attempt"`
	Model        *string  `json:"model"`
	DurationMs   *int64   `json:"durationMs"`
	InputTokens  int      `json:"inputTokens"`
	OutputTokens int      `json:"outputTokens"`
	TotalTokens  int      `json:"totalTokens"`
	CostUsd      *float64 `json:"costUsd"`
	Success      bool     `json:"success"`
	// Provider is the adapter that executed the stage (claude | codex | gemini
	// | …), the V5-only field (StageMetricV5Schema). The platform persists it to
	// cost_events.provider and backfills it onto pipeline_events.adapter, which
	// powers the dashboard's Adapter Mix donut (#268). Like Model it is
	// `.nullable()` but NOT `.optional()` in the strict V5 schema, so it carries
	// no omitempty — a nil pointer must still serialize the key as `null`
	// ("provider unknown"), never be dropped.
	Provider *string `json:"provider"`
}

// ExecutionHistoryMapperInput provides fields required for
// ExecutionHistoryRunRecord that are not present on state.V2RunRecord itself
// — sourced from RuntimeState at the call site (mirrors the role the retired
// PipelineRunMapperInput played for the old /v1/pipelines/runs mapper).
//
// Note there is no Retries field here (unlike the retired mapper's
// PipelineRunMapperInput.Backtracks): the V4 schema's `retries` is derived
// inside the mapper itself from record.AttemptsUntilSuccess, which — unlike a
// raw RuntimeState.RetryCount — is already persisted on V2RunRecord (Issue
// #4172) and survives onto backfilled/re-synced records too, not just the
// live single-push path. See V2RunRecordToExecutionHistoryRunRecord.
type ExecutionHistoryMapperInput struct {
	// Repo is the "owner/repo" this run belongs to — required by the schema.
	Repo string
}
