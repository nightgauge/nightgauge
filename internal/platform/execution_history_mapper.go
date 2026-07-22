package platform

import (
	"fmt"
	"regexp"
	"sort"
	"strings"
	"time"
	"unicode"

	"github.com/nightgauge/nightgauge/internal/state"
)

// executionHistoryStagesMax bounds ExecutionHistoryRunRecord.Stages, matching
// the platform's TELEMETRY_STAGES_MAX.
const executionHistoryStagesMax = 32

// executionHistoryFieldMax bounds free-text fields (terminalFailureKind,
// stage id/name, model, predicted/actual model) to 100 chars, matching the
// platform's per-field .max(100) constraints.
const executionHistoryFieldMax = 100

// Issue-context bounds (#183) — match the platform's telemetry schema:
// issueTitle `.max(256)` (GitHub's own issue-title ceiling) and issueBody
// `.max(8192)` (a bounded excerpt). Exceeding either would reject the whole
// record under `.strict()`, so clip here.
const (
	executionHistoryIssueTitleMax = 256
	executionHistoryIssueBodyMax  = 8192
	// executionHistoryLabelsMax caps the number of labels emitted so a
	// pathologically-labelled issue can't bloat the wire payload. Each label is
	// truncated to executionHistoryFieldMax like every other bounded identifier.
	executionHistoryLabelsMax = 50
)

// telemetryOutcomes is the platform's TELEMETRY_OUTCOMES enum for
// POST /v1/telemetry/pipeline-run (complete | failed | cancelled) — a
// narrower, distinct vocabulary from the (retired) /v1/pipelines/runs
// OUTCOMES enum (success | failure | cancelled | partial). The local
// V2RunRecord writer (state.BuildV2Record) already emits exactly these three
// values, so — mirroring pipelineRunV4Mapper.ts's `asOutcome` — no
// translation is applied, only strict membership validation.
var telemetryOutcomes = map[string]bool{
	"complete":  true,
	"failed":    true,
	"cancelled": true,
}

func isTelemetryOutcome(local string) bool {
	return telemetryOutcomes[local]
}

// telemetrySizes is the platform's TELEMETRY_SIZES enum.
var telemetrySizes = map[string]bool{"XS": true, "S": true, "M": true, "L": true, "XL": true}

// validTelemetrySize returns size unchanged when it is a valid TELEMETRY_SIZES
// member, else nil. Mirrors pipelineRunV4Mapper.ts's `asSize`.
func validTelemetrySize(size *string) *string {
	if size == nil || !telemetrySizes[*size] {
		return nil
	}
	v := *size
	return &v
}

// telemetryComplexityScores is the platform's TELEMETRY_COMPLEXITY_SCORES
// enum (Fibonacci-style: 1, 2, 3, 5, 8).
var telemetryComplexityScores = map[int]bool{1: true, 2: true, 3: true, 5: true, 8: true}

// validTelemetryComplexity returns score unchanged when it is a valid
// TELEMETRY_COMPLEXITY_SCORES member, else nil. Mirrors
// pipelineRunV4Mapper.ts's `asComplexity`.
func validTelemetryComplexity(score int) *int {
	if !telemetryComplexityScores[score] {
		return nil
	}
	v := score
	return &v
}

// validTelemetryRunID returns id unchanged when it is a well-formed UUID,
// else "" (which json-omits the optional pipelineRunId key). The platform
// schema is `z.string().uuid().optional()` — a malformed value would reject
// the whole record under `.strict()` validation, so gate here (#261).
func validTelemetryRunID(id string) string {
	if telemetryRunIDPattern.MatchString(id) {
		return id
	}
	return ""
}

var telemetryRunIDPattern = regexp.MustCompile(
	`^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$`)

// nonEmptyTruncatedPtr returns nil for an empty string, else a pointer to s
// truncated to n runes.
func nonEmptyTruncatedPtr(s string, n int) *string {
	if s == "" {
		return nil
	}
	v := truncate(s, n)
	return &v
}

// toTelemetryRoutingPath splits the free-text V2Routing.Path field (e.g.
// "issue-pickup,feature-planning" or "issue-pickup > feature-dev") into the
// V4 `routingPath` string[], mirroring pipelineRunV4Mapper.ts's
// `toRoutingPath`: split on runs of comma/'>'/whitespace, drop empty entries
// and the "standard" placeholder, cap at 20 entries of 50 chars each. Returns
// nil (not an empty slice) for an empty/placeholder-only path, matching the
// schema's nullable `routingPath: string[] | null`.
func toTelemetryRoutingPath(path string) []string {
	if path == "" {
		return nil
	}
	fields := strings.FieldsFunc(path, func(r rune) bool {
		return r == ',' || r == '>' || unicode.IsSpace(r)
	})
	var out []string
	for _, f := range fields {
		if f == "" || f == "standard" {
			continue
		}
		out = append(out, truncate(f, 50))
		if len(out) >= 20 {
			break
		}
	}
	return out
}

// orderedStageNames returns the keys of stages in canonicalStageOrder first,
// then any non-canonical stages sorted alphabetically — the same
// deterministic ordering convention used elsewhere in this package (see
// stagesRunInOrder), so repeated mapper runs over the same record always
// produce the same array order.
func orderedStageNames(stages map[string]state.V2StageDetail) []string {
	canonicalSet := make(map[string]bool, len(canonicalStageOrder))
	order := make([]string, 0, len(stages))
	for _, name := range canonicalStageOrder {
		canonicalSet[name] = true
		if _, ok := stages[name]; ok {
			order = append(order, name)
		}
	}
	var extra []string
	for name := range stages {
		if !canonicalSet[name] {
			extra = append(extra, name)
		}
	}
	sort.Strings(extra)
	return append(order, extra...)
}

// buildExecutionHistoryStages converts a V2RunRecord's stage map into the V4
// per-stage metric array, mirroring pipelineRunV4Mapper.ts's `mapStages`.
// Every stage is included regardless of local status (the V4 schema has no
// per-stage status field, only a `success` bool) — unlike the retired
// /v1/pipelines/runs mapper's buildIngestStages, which omitted in-flight
// stages. Capped at executionHistoryStagesMax entries. Always returns a
// non-nil slice (possibly empty) so the caller serializes `stages: []`
// rather than `stages: null` for a record with no stage data — the schema
// requires a non-null array.
//
// Also returns the summed per-stage cost so the caller can backfill
// totalCostUsd when the run-level cost estimate is absent (same rationale as
// buildIngestStages carried for the retired mapper — Issue #4009).
func buildExecutionHistoryStages(record state.V2RunRecord) ([]ExecutionHistoryStageMetric, float64) {
	stages := make([]ExecutionHistoryStageMetric, 0, len(record.Stages))
	if len(record.Stages) == 0 {
		return stages, 0
	}

	var summedCostUSD float64
	for _, name := range orderedStageNames(record.Stages) {
		if len(stages) >= executionHistoryStagesMax {
			break
		}
		detail := record.Stages[name]
		tok, hasTok := record.Tokens.PerStage[name]

		// model prefers the recorded model selection, falling back to the
		// per-stage adapter — mirrors pipelineRunV4Mapper.ts's
		// `modelSelection.model ?? tokens.adapter` fallback.
		model := ""
		if detail.ModelSelection != nil && detail.ModelSelection.Model != "" {
			model = detail.ModelSelection.Model
		} else if hasTok && tok.Adapter != "" {
			model = tok.Adapter
		}

		// provider is the executing adapter recorded on the stage (V5 —
		// StageMetricV5Schema). Unlike `model` it is NOT reused as a model
		// fallback: it is the distinct adapter identity the platform persists to
		// cost_events.provider and backfills onto pipeline_events.adapter (the
		// Adapter Mix donut). Empty adapter → nil → `"provider": null`
		// ("unknown"), never defaulted (#268).
		provider := ""
		if hasTok {
			provider = tok.Adapter
		}

		var inputTokens, outputTokens int
		var costUsd *float64
		if hasTok {
			inputTokens = tok.Input
			outputTokens = tok.Output
			if tok.CostUSD != 0 {
				c := tok.CostUSD
				costUsd = &c
				summedCostUSD += tok.CostUSD
			}
		}

		var durationMs *int64
		if detail.DurationMs > 0 {
			d := detail.DurationMs
			durationMs = &d
		}

		stages = append(stages, ExecutionHistoryStageMetric{
			StageID:      truncate(name, executionHistoryFieldMax),
			StageName:    truncate(name, executionHistoryFieldMax),
			Attempt:      1, // mirrors the reference TS mapper — no per-attempt granularity on the wire yet.
			Model:        nonEmptyTruncatedPtr(model, executionHistoryFieldMax),
			Provider:     nonEmptyTruncatedPtr(provider, executionHistoryFieldMax),
			DurationMs:   durationMs,
			InputTokens:  inputTokens,
			OutputTokens: outputTokens,
			TotalTokens:  inputTokens + outputTokens,
			CostUsd:      costUsd,
			// 'failed'/'error' are the only non-success terminal states the
			// producer writes; 'complete' and 'skipped' both count as success —
			// mirrors pipelineRunV4Mapper.ts's success predicate exactly.
			Success: detail.Status != "failed" && detail.Status != "error",
		})
	}

	return stages, summedCostUSD
}

// toTelemetryLabels converts the run record's labels into the wire `labels`
// string[] (#183): drops empty entries, truncates each to executionHistoryFieldMax,
// and caps the count at executionHistoryLabelsMax. Returns nil (not an empty
// slice) for no labels so the omitempty field drops the key entirely, matching
// the platform's optional `labels` shape.
func toTelemetryLabels(labels []string) []string {
	if len(labels) == 0 {
		return nil
	}
	out := make([]string, 0, len(labels))
	for _, l := range labels {
		if l == "" {
			continue
		}
		out = append(out, truncate(l, executionHistoryFieldMax))
		if len(out) >= executionHistoryLabelsMax {
			break
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// V2RunRecordToExecutionHistoryRunRecord converts a completed V2RunRecord
// (local execution history) into an ExecutionHistoryRunRecord — the wire body
// for POST /v1/telemetry/pipeline-run (Issue
// Consolidate pipeline-run telemetry
// onto a single canonical sink"). Returns an error when StartedAt/CompletedAt
// cannot be parsed, or when Outcome is not one of complete|failed|cancelled —
// such a record is permanently unmappable (mirrors pipelineRunV4Mapper.ts's
// `MapResult`), so the caller should skip and log it rather than retry.
//
// Stage-level token/cost/provider data (the open question carried in
// #1146 — "does the autonomous scheduler have stage-level data available at
// recordOutcome() to populate ExecutionHistoryRunRecord.stages[]?") is
// already present on `record` at every call site: recordOutcome →
// buildRunRecordForTelemetry (internal/orchestrator/scheduler.go) builds
// `record` via the same state.HistoryWriter.BuildV2Record used for the
// on-disk JSONL history, which populates record.Tokens.PerStage and
// record.Stages[name].ModelSelection *before* PushPipelineRun or
// SyncTelemetry ever runs — the same data buildExecutionHistoryStages reads
// here. No additional threading of stage data was required; the answer is
// yes, it is available.
func V2RunRecordToExecutionHistoryRunRecord(record state.V2RunRecord, input ExecutionHistoryMapperInput) (ExecutionHistoryRunRecord, error) {
	startedAt, err := time.Parse(time.RFC3339, record.StartedAt)
	if err != nil {
		return ExecutionHistoryRunRecord{}, fmt.Errorf("parse started_at %q: %w", record.StartedAt, err)
	}

	completedAtTime, err := parseOptionalTime(record.CompletedAt)
	if err != nil {
		return ExecutionHistoryRunRecord{}, fmt.Errorf("parse completed_at %q: %w", record.CompletedAt, err)
	}
	var completedAt *string
	if completedAtTime != nil {
		s := completedAtTime.UTC().Format(time.RFC3339Nano)
		completedAt = &s
	}

	if !isTelemetryOutcome(record.Outcome) {
		return ExecutionHistoryRunRecord{}, fmt.Errorf(
			"outcome %q is not a valid telemetry outcome (want complete|failed|cancelled)", record.Outcome)
	}

	stages, summedStageCostUSD := buildExecutionHistoryStages(record)

	var totalCostUsd *float64
	switch {
	case record.Tokens.EstimatedCostUSD != 0:
		v := record.Tokens.EstimatedCostUSD
		totalCostUsd = &v
	case summedStageCostUSD > 0:
		// Run-level estimate is absent but the per-stage costs sum to a real
		// value — backfill so totalCostUsd populates instead of staying null
		// (same rationale as the retired mapper's TotalCostUsd backfill,
		// Issue #4009).
		v := summedStageCostUSD
		totalCostUsd = &v
	}

	var totalDurationMs *int64
	if record.TotalDuration != 0 {
		v := record.TotalDuration
		totalDurationMs = &v
	}

	// predictedModel/actualModel: the live orchestrator call site
	// (buildRunRecordForTelemetry) already attaches OutcomePrediction to
	// `record` before mapping, so this data is available here even though the
	// reference TS mapper (which reads after-the-fact JSONL with no
	// OutcomePrediction wiring) hardcodes both to null. Real, already-computed
	// signal should not be discarded — see the ExecutionHistoryMapperInput.Retries
	// doc comment for the same principle applied to `retries`.
	var predictedModel, actualModel *string
	if record.OutcomePrediction != nil {
		predictedModel = nonEmptyTruncatedPtr(record.OutcomePrediction.PredictedModel, executionHistoryFieldMax)
		actualModel = nonEmptyTruncatedPtr(record.OutcomePrediction.ActualModel, executionHistoryFieldMax)
	}

	// predictedSize is intentionally left nil: the local predictedSizeLabel()
	// vocabulary (small|medium|large, from learning.Outcome) does not overlap
	// the platform's TELEMETRY_SIZES (XS|S|M|L|XL) — validTelemetrySize would
	// always reject it. This is a real local/platform vocabulary mismatch, not
	// a mapper bug; reconciling the two vocabularies is out of scope here.

	return ExecutionHistoryRunRecord{
		SchemaVersion:       executionHistoryRunRecordSchemaVersion,
		IssueNumber:         record.IssueNumber,
		Repo:                input.Repo,
		PipelineRunID:       validTelemetryRunID(record.RunID),
		StartedAt:           startedAt.UTC().Format(time.RFC3339Nano),
		CompletedAt:         completedAt,
		Outcome:             record.Outcome,
		TerminalFailureKind: nonEmptyTruncatedPtr(record.TerminalFailureKind, executionHistoryFieldMax),
		OutcomeType:         nonEmptyTruncatedPtr(record.OutcomeType, executionHistoryFieldMax),
		PredictedSize:       nil,
		ActualSize:          validTelemetrySize(record.Size),
		PredictedModel:      predictedModel,
		ActualModel:         actualModel,
		ComplexityScore:     validTelemetryComplexity(record.Routing.ComplexityScore),
		Retries:             retriesFromAttempts(record.AttemptsUntilSuccess),
		DurationMs:          totalDurationMs,
		TotalCostUsd:        totalCostUsd,
		Stages:              stages,
		Agents:              []any{},
		RoutingPath:         toTelemetryRoutingPath(record.Routing.Path),
		// Issue context captured at pickup (#183) — title/body/labels so the
		// dashboard run-detail page shows what the run is doing. Optional on the
		// wire (omitempty): a record with no captured context drops the keys.
		// Bounded to the platform's issueTitle .max(256) / issueBody .max(8192).
		IssueTitle: nonEmptyTruncatedPtr(record.Title, executionHistoryIssueTitleMax),
		IssueBody:  nonEmptyTruncatedPtr(record.Body, executionHistoryIssueBodyMax),
		Labels:     toTelemetryLabels(record.Labels),
	}, nil
}

// retriesFromAttempts derives the wire `retries` count from
// V2RunRecord.AttemptsUntilSuccess (Issue #4172: 1 + stage retries + model
// escalations + extra Ralph iterations beyond the first, summed across
// stages; 0/absent on a clean run — see state.ComputeAttemptsUntilSuccess).
// Subtracting the guaranteed first attempt yields "how much extra effort this
// run required" — a reasonable proxy for `retries`, though it conflates pure
// stage retries with model escalations and Ralph loop iterations (the local
// history schema does not track those three separately at the run level).
// This is real, already-computed signal available on every V2RunRecord
// (live-pushed or backfilled from disk) — better than the reference TS
// mapper's hardcoded 0, which reflects that mapper reading raw JSONL with a
// narrower field set in mind, not an inherent unavailability of the data.
func retriesFromAttempts(attemptsUntilSuccess int) int {
	if attemptsUntilSuccess > 1 {
		return attemptsUntilSuccess - 1
	}
	return 0
}
