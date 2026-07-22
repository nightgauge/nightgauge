package state

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/nightgauge/nightgauge/internal/history"
)

// v2RunBodyMax bounds V2RunRecord.Body (#183) — matches the platform's
// telemetry issueBody `.max(8192)` so a body already capped at pickup is never
// re-expanded past the wire ceiling, and any caller-supplied body is clipped
// before it lands in the JSONL history.
const v2RunBodyMax = 8192

// clipHistoryRunes truncates s to at most n runes (rune-safe — never splits a
// multi-byte character), returning s unchanged when it already fits.
func clipHistoryRunes(s string, n int) string {
	if n <= 0 {
		return ""
	}
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n])
}

// HistoryEntry is the legacy execution record format (executions.jsonl).
// Retained for backward compatibility with ReadRecent.
type HistoryEntry struct {
	Timestamp     time.Time     `json:"timestamp"`
	Repo          string        `json:"repo"`
	IssueNumber   int           `json:"issueNumber"`
	Duration      time.Duration `json:"duration"`
	InputTokens   int           `json:"inputTokens"`
	OutputTokens  int           `json:"outputTokens"`
	TotalCostUSD  float64       `json:"totalCostUsd"`
	Stages        []StageResult `json:"stages"`
	SkippedStages []string      `json:"skippedStages,omitempty"`
	Success       bool          `json:"success"`
	Error         string        `json:"error,omitempty"`
}

// V2RunRecord matches the TypeScript ExecutionHistoryRunRecordV2 / V3 Zod
// schemas exactly. Written to daily YYYY-MM-DD.jsonl files that the VSCode
// dashboard reads.
//
// SchemaVersion is "2" for normal runs and "3" when terminal failure
// preservation fields (TerminalFailureKind, per-stage LastOutputLines) are
// populated (Issue #3001). The TS reader uses a Zod union (V1 ∪ V2 ∪ V3) so
// older daily JSONLs remain valid without migration (ADR-002).
type V2RunRecord struct {
	SchemaVersion string `json:"schema_version"`
	RecordType    string `json:"record_type"`
	IssueNumber   int    `json:"issue_number"`
	// RunID is the UUID v7 join key threading this record to its run's
	// lifecycle trace, exit records, and platform telemetry (#179 / ADR 013).
	// Additive `omitempty`: records written before trace capture omit it, and
	// the TS reader's non-strict z.object() strips it for older readers.
	RunID string `json:"run_id,omitempty"`
	// Repo is the "owner/name" this run belongs to. Required by the platform's
	// strict ExecutionHistoryRunRecordV4 telemetry contract — without it the
	// VSCode uploader cannot map a run to a repo and the dashboard run list
	// (pipeline_runs) cannot be populated for multi-repo workspaces, where one
	// history file holds runs from several repos. Additive: the local TS reader
	// uses non-strict z.object() (unknown keys are stripped), so older readers
	// ignore it. omitempty keeps it absent on records with no resolved repo.
	Repo  string `json:"repo,omitempty"`
	Title string `json:"title"`
	// Body is the GitHub issue body captured at pickup (#183), a bounded excerpt.
	// Additive `omitempty` — records written before capture omit it, and the TS
	// reader's non-strict z.object() strips it for older readers. Emitted on the
	// telemetry wire as `issueBody` so the dashboard run-detail page can show the
	// issue context without leaving the dashboard.
	Body              string                   `json:"body,omitempty"`
	Branch            string                   `json:"branch"`
	BaseBranch        string                   `json:"base_branch"`
	ExecutionMode     string                   `json:"execution_mode"`
	StartedAt         string                   `json:"started_at"`
	CompletedAt       string                   `json:"completed_at"`
	TotalDuration     int64                    `json:"total_duration_ms"`
	Outcome           string                   `json:"outcome"`
	Labels            []string                 `json:"labels,omitempty"`
	Size              *string                  `json:"size"`
	Type              *string                  `json:"type"`
	Priority          *string                  `json:"priority,omitempty"`
	Stages            map[string]V2StageDetail `json:"stages"`
	Tokens            V2Tokens                 `json:"tokens"`
	OutcomeType       string                   `json:"outcome_type,omitempty"`
	Files             V2Files                  `json:"files"`
	Routing           V2Routing                `json:"routing"`
	IsRecovery        bool                     `json:"is_recovery,omitempty"`
	GateResults       []GateResult             `json:"gate_results,omitempty"`
	OutcomePrediction *OutcomePrediction       `json:"outcome_prediction,omitempty"`
	// TerminalFailureKind names what aborted the run (Issue #3001 / V3 only).
	// Absent for successful runs. One of: stall_kill, budget_exceeded,
	// validation_error, subagent_crash, orchestrator_crash. Independent of
	// per-stage failure_category (which buckets failures by responsibility).
	TerminalFailureKind string `json:"terminal_failure_kind,omitempty"`
	// RecoveryEvents records each Recovery Dialog interaction during a run
	// (Issue #3239). Empty/omitted on runs that did not surface recovery.
	// Additive — schema_version is not bumped.
	RecoveryEvents []RecoveryEvent `json:"recovery_events,omitempty"`
	// PerformanceMode is the dominant mode for this run (Issue #3218).
	// Derived from per-stage modes: most common non-empty value. Matches the
	// HistoryIndexEntry.performance_mode written to index.json.
	PerformanceMode string `json:"performance_mode,omitempty"`
	// AttemptsUntilSuccess is the canonical "how many tries until green" metric
	// (Issue #4172): 1 (the first attempt) + stage retries + model escalations +
	// extra Ralph iterations (iterations beyond the first, summed across stages).
	// Additive `omitempty` — records emitted before #4172 omit it; readers default
	// to 0 (unknown). Computed by ComputeAttemptsUntilSuccess. Local-only for now;
	// the platform/shared-types mirror is coordinated with S8 (#1158).
	AttemptsUntilSuccess int `json:"attempts_until_success,omitempty"`
	// QualityScore is the normalized 0–100 composite score from the model-eval
	// grading engine (Issue #4173), when this run was graded. Absent for ordinary
	// (ungraded) pipeline runs. Additive `omitempty`.
	QualityScore *float64 `json:"quality_score,omitempty"`
	RecordedAt   string   `json:"recorded_at"`
}

// RecoveryEvent is one Recovery Dialog interaction. Issue #3239.
// Fields mirror the TypeScript telemetry shape so JSONL records correlate
// with VS Code-side audit logs.
type RecoveryEvent struct {
	IssueNumber int    `json:"issue_number"`
	ErrorKind   string `json:"error_kind"`
	Action      string `json:"action"`
	DurationMs  int64  `json:"duration_ms"`
	At          string `json:"at"`
}

// V2StageDetail matches HistoryStageDetailSchema.
type V2StageDetail struct {
	Status         string         `json:"status"`
	StartedAt      string         `json:"started_at,omitempty"`
	CompletedAt    string         `json:"completed_at,omitempty"`
	DurationMs     int64          `json:"duration_ms,omitempty"`
	Error          string         `json:"error,omitempty"`
	ExecutionMode  string         `json:"execution_mode,omitempty"`
	ModelSelection *V2ModelSelect `json:"model_selection,omitempty"`
	// ModelEffort and ModelReasoning record the effort/reasoning provenance the
	// stage ran under (Issue #4172), so eval and routing analysis can attribute
	// outcomes to a {model × effort × reasoning} cell. Additive `omitempty`;
	// absent when the pipeline did not thread effort/reasoning into the record.
	ModelEffort    string `json:"model_effort,omitempty"`
	ModelReasoning string `json:"model_reasoning,omitempty"`
	// AttemptsUntilSuccess is the per-stage tries-until-green count (Issue #4172):
	// max(1, Ralph iterations) for this stage. Additive `omitempty`.
	AttemptsUntilSuccess int `json:"attempts_until_success,omitempty"`
	// GateResults records the per-stage post-condition gate outcomes (Issue
	// #3266). One entry per gate that ran for this stage — typically one,
	// since stages have a single registered gate. Additive `omitempty`:
	// records emitted before #3266 omit the field; readers default to nil.
	// This is distinct from V2RunRecord.GateResults (quality gates: build /
	// lint / unit-tests / type-check) — see GateResult vs StageGateResult.
	GateResults []StageGateResult `json:"gate_results,omitempty"`
	// LastOutputLines is the tail of subagent stdout/stderr captured at
	// terminal failure (Issue #3001). Bounded by the runtime ring buffer
	// (≤200 lines × ≤1KB/line ≈ 200KB). Only populated for the stage that
	// failed terminally; absent on success or non-failed stages.
	LastOutputLines string `json:"last_output_lines,omitempty"`
	// FailureCategory is the per-stage classification used by reliability
	// scoring (e.g., `stall-killed-after-retry` from Issue #3005). Free-string
	// at the schema level; the SDK classifier in
	// `packages/nightgauge-sdk/src/analysis/health/failureClassifier.ts`
	// maps known values to weight buckets. Absent on success or for
	// uncategorized failures.
	FailureCategory string `json:"failure_category,omitempty"`
	// PerformanceMode is the performance mode active when the stage began
	// (Issue #3215). One of "efficiency" | "elevated" | "maximum". Captured
	// per-stage because the user can toggle the mode mid-run; the run-level
	// performance_mode field is insufficient for calibration bucketing.
	// Absent on records emitted before #3215 — readers must treat the absence
	// as mode-unknown.
	PerformanceMode string `json:"performance_mode,omitempty"`
	// ExecutionPath records whether this stage ran via the deterministic Go
	// path or the LLM skill path (Issue #3264). One of "deterministic" | "llm".
	// Set on records emitted ≥ PR #3264; absent on older records — readers
	// must treat absence as `unknown` rather than defaulting to a value. The
	// deterministic-first pr-merge runner is the first producer of this field;
	// future stages (pr-create has been suggested in epic #3261) can adopt it
	// without schema growth.
	ExecutionPath string `json:"execution_path,omitempty"`
	// PuntReason is the machine-readable reason the deterministic-first hook
	// declined and this stage fell through to the LLM path (Issue #297). Only
	// set alongside ExecutionPath=="llm" when a deterministic hook actually ran
	// and punted (e.g. "missing-dev-context", "dirty-merge-state: BLOCKED",
	// "ci-wait-timeout"); absent on deterministic successes, on LLM-only stages
	// with no deterministic hook, and on records emitted before #297. Lets
	// pipeline-health / retro answer WHY the expensive path ran without the
	// forensic log archaeology #288 required. Readers treat absence as unknown.
	PuntReason string `json:"punt_reason,omitempty"`
	// Anomalies records orchestrator-detected anomalies for this stage
	// (Issue #3267). Currently used by the atomic-eligible-stage LLM-overrun
	// detector. Additive `omitempty` per ADR-002 — older records omit the
	// field; readers default to nil/empty.
	Anomalies []Anomaly `json:"anomalies,omitempty"`
	// RecoveryAttempts records each FailureRecovery registry attempt that ran
	// for this stage (Issue #3268). Distinct from V2RunRecord.RecoveryEvents
	// (Recovery Dialog interactions, #3239) — RecoveryAttempts is the canonical
	// stage-level audit trail for the deterministic auto-triage framework.
	// Additive `omitempty` per ADR-002 — older records omit the field;
	// readers default to nil/empty.
	RecoveryAttempts []RecoveryAttempt `json:"recovery_attempts,omitempty"`
}

// RecoveryAttempt records the outcome of one FailureRecovery registry attempt
// (Issue #3268). Mirrors recovery.RecoveryResult — copied across the package
// boundary because state sits below recovery in the import graph.
type RecoveryAttempt struct {
	Action     string   `json:"action"`              // canonical id (e.g. "skill-exited-without-merging")
	Recovered  bool     `json:"recovered"`           // true = stage marked recovered
	Reason     string   `json:"reason,omitempty"`    // short human-readable explanation
	Evidence   []string `json:"evidence,omitempty"`  // optional detail lines
	FollowUp   string   `json:"follow_up,omitempty"` // "stage can resume" | "issue requires human triage" | …
	CostUSD    float64  `json:"cost_usd"`            // ~0 for deterministic actions
	DurationMs int64    `json:"duration_ms,omitempty"`
	At         string   `json:"at"` // ISO 8601
}

// V2ModelSelect matches the model_selection sub-schema.
type V2ModelSelect struct {
	Model  string `json:"model"`
	Source string `json:"source"`
}

// V2Tokens matches TokensSchema.
type V2Tokens struct {
	TotalInput         int                      `json:"total_input"`
	TotalOutput        int                      `json:"total_output"`
	TotalCacheRead     int                      `json:"total_cache_read"`
	TotalCacheCreation int                      `json:"total_cache_creation"`
	EstimatedCostUSD   float64                  `json:"estimated_cost_usd"`
	PerStage           map[string]V2StageTokens `json:"per_stage,omitempty"`
}

// V2StageTokens matches HistoryStageTokenUsageSchema.
type V2StageTokens struct {
	Input         int      `json:"input"`
	Output        int      `json:"output"`
	CacheRead     int      `json:"cache_read"`
	CacheCreation int      `json:"cache_creation"`
	CostUSD       float64  `json:"cost_usd"`
	CacheHitRate  *float64 `json:"cache_hit_rate,omitempty"` // ratio of cache_read / (input + cache_read); nil when no tokens
	// Adapter is the adapter that executed this stage (Issue #3224). One of
	// "claude" | "codex" | "gemini" | "gemini-sdk" | "lm-studio" | "ollama" |
	// "copilot". Free-string at the schema level so adding adapters does not
	// require a Go-side enum bump; the TypeScript Zod schema enforces the
	// canonical set. Empty string maps to absent on the wire via omitempty —
	// readers must treat absence as adapter-unknown rather than defaulting.
	Adapter string `json:"adapter,omitempty"`
}

// V2Files matches the v2 files sub-schema.
type V2Files struct {
	ReadCount    int `json:"read_count"`
	WrittenCount int `json:"written_count"`
}

// V2Routing matches the v2 routing sub-schema.
type V2Routing struct {
	ComplexityScore int      `json:"complexity_score"`
	Path            string   `json:"path"`
	SkipStages      []string `json:"skip_stages"`
	// ChangeClass is the authoritative post-dev change classification
	// (docs_only|config_only|source|mixed|empty) computed from the real diff at
	// run completion (#4129). Enables `nightgauge cost by-class`. Additive;
	// omitempty keeps it absent on pre-#4129 records.
	ChangeClass string `json:"change_class,omitempty"`
}

// GateResult records the pass/catch outcome for a single quality gate.
// Mirrors the TypeScript GateMetricRecord Zod schema.
//
// NOTE: This is the *quality-gate* GateResult — build / lint / type-check /
// unit-tests outcomes recorded by feature-validate. It is distinct from
// StageGateResult (Issue #3266), which records the pass/fail outcome of a
// stage post-condition gate run by the orchestrator. The two types coexist:
// the same V2RunRecord can carry both (V2RunRecord.GateResults at the run
// level, V2StageDetail.GateResults at the per-stage level).
type GateResult struct {
	GateName     string `json:"gate_name"` // build, unit-tests, type-check, lint
	Result       string `json:"result"`    // pass | catch
	DurationMs   int64  `json:"duration_ms,omitempty"`
	ErrorSummary string `json:"error_summary,omitempty"` // first line of error when result="catch"
	Timestamp    string `json:"timestamp"`               // ISO 8601
}

// StageGateResult records the outcome of a stage post-condition gate (Issue
// #3266). Mirrors the in-process gates.GateResult value type and is the
// persistence shape written to V2StageDetail.GateResults.
//
// Distinct from GateResult above (quality gates) by both shape (Passed bool
// vs Result string) and semantics (stage post-condition vs build/test pass).
// Adding a new stage gate does not require changing this struct — gates are
// keyed by GateName.
type StageGateResult struct {
	GateName   string   `json:"gate_name"` // e.g. "issue-pickup", "pr-merge"
	Passed     bool     `json:"passed"`    // true = post-condition satisfied
	Reason     string   `json:"reason"`    // short human-readable explanation
	Evidence   []string `json:"evidence,omitempty"`
	DurationMs int64    `json:"duration_ms,omitempty"`
	Timestamp  string   `json:"timestamp"` // ISO 8601
	// Kind discriminates pass/no-op/fail (Issue #3267). One of "ok", "no_op",
	// "fail". When absent on persisted records the classifier infers from
	// Passed: true → "ok", false → "fail" (i.e. legacy records cannot
	// produce a "skill-no-op" outcome retroactively without a backfill).
	Kind string `json:"kind,omitempty"`
}

// Anomaly is a per-stage anomaly record (Issue #3267) appended to
// V2StageDetail.Anomalies when the orchestrator detects a suspicious
// execution pattern that the gate framework couldn't classify as a hard
// failure (e.g. an atomic-eligible stage running through the LLM path
// while the gate still passed). Mirrors the gates.Anomaly in-process
// shape; copied across the package boundary because state sits below
// gates in the import graph.
type Anomaly struct {
	Kind                   string  `json:"kind"`                              // anomaly identifier (e.g. "atomic_llm_overrun")
	Stage                  string  `json:"stage"`                             // pipeline stage name
	ExecutionPath          string  `json:"execution_path"`                    // "deterministic" | "llm"
	StageCostUSD           float64 `json:"stage_cost_usd"`                    // observed stage cost
	DeterministicPredicate string  `json:"deterministic_predicate,omitempty"` // human-readable predicate that should have matched
	Timestamp              string  `json:"timestamp"`                         // ISO 8601
}

// OutcomePrediction captures predicted vs actual routing decisions for calibration.
type OutcomePrediction struct {
	PredictedSize  string `json:"predicted_size"`        // xs, s, m, l, xl
	ActualSize     string `json:"actual_size,omitempty"` // populated post-merge
	PredictedModel string `json:"predicted_model"`
	ActualModel    string `json:"actual_model,omitempty"`
}

// V2IndexEntry matches the TypeScript HistoryIndexEntry interface.
type V2IndexEntry struct {
	IssueNumber int `json:"issue_number"`
	// RunID mirrors V2RunRecord.RunID so the index can be de-duplicated by run
	// identity (Issue #313 — one entry per run). Additive `omitempty`: entries
	// written before this field, and records with no assigned run_id, omit it,
	// and the TS reader's non-strict schema ignores unknown keys.
	RunID       string `json:"run_id,omitempty"`
	Title       string `json:"title"`
	Outcome     string `json:"outcome"`
	OutcomeType string `json:"outcome_type,omitempty"`
	IsRecovery  bool   `json:"is_recovery,omitempty"`
	// PerformanceMode is the dominant performance mode for this run (Issue #3218).
	// Derived from per-stage PerformanceMode values — the most common non-empty
	// value wins; "elevated" breaks ties over "efficiency". Absent on pre-fix records.
	PerformanceMode        string   `json:"performance_mode,omitempty"`
	CostUSD                float64  `json:"cost_usd"`
	TotalInputTokens       int      `json:"total_input_tokens"`
	TotalOutputTokens      int      `json:"total_output_tokens"`
	TotalCacheReadTokens   int      `json:"total_cache_read_tokens"`
	TotalCacheCreateTokens int      `json:"total_cache_creation_tokens"`
	DurationMs             int64    `json:"duration_ms"`
	StageCount             int      `json:"stage_count"`
	StartedAt              string   `json:"started_at"`
	RecordedAt             string   `json:"recorded_at"`
	Labels                 []string `json:"labels,omitempty"`
	Size                   *string  `json:"size"`
	Type                   *string  `json:"type"`
	Branch                 string   `json:"branch"`
}

// V2Index matches the TypeScript HistoryIndex interface.
type V2Index struct {
	SchemaVersion string         `json:"schema_version"`
	UpdatedAt     string         `json:"updated_at"`
	TotalRuns     int            `json:"total_runs"`
	Entries       []V2IndexEntry `json:"entries"`
}

// V2RunInput provides the metadata the scheduler passes alongside the
// RuntimeState snapshot for building V2 history records.
type V2RunInput struct {
	Title string
	// Body is the GitHub issue body captured at pickup (#183), sourced from
	// RuntimeState.Body at the call site. Bounded again in BuildV2Record as a
	// safety net. Empty when no issue body was captured.
	Body            string
	Branch          string
	BaseBranch      string
	Labels          []string
	Size            string
	IssueType       string
	ComplexityScore int
	RoutingPath     string
	SkipStages      []string
	// ChangeClass is the authoritative post-dev change classification recorded
	// on V2Routing.ChangeClass for `cost by-class` (#4129). Empty when unknown.
	ChangeClass string
	IsRecovery  bool
	// TerminalFailureKind, when set, bumps the emitted SchemaVersion to "3"
	// and populates the terminal_failure_kind field. Only meaningful for
	// failed runs. (Issue #3001)
	TerminalFailureKind string
	// StageOutputTails maps stage name → last 200 lines of subagent output
	// captured at terminal failure. Populated on the matching V2StageDetail.
	// (Issue #3001)
	StageOutputTails map[string]string
	// StageFailureCategories maps stage name → failure_category string applied
	// to the matching V2StageDetail. Used by adaptive stall-recovery (Issue
	// #3005) to mark second-stall stages as `stall-killed-after-retry`.
	StageFailureCategories map[string]string
	// DefaultAdapter is the run-level adapter recorded on per-stage tokens
	// when RuntimeState.StageAdapters does not carry a value for the stage
	// (Issue #3224). The scheduler passes the active global adapter here;
	// once the per-stage adapter resolver lands (#3221), every stage records
	// its own adapter via RecordStageAdapter and this fallback stops being
	// read. Empty string means "unknown" — leaves Adapter absent on the wire.
	DefaultAdapter string
	// OutcomeType is a first-class, needs-human run outcome (e.g. "blocked" for
	// a pr-merge blocked by a required-check/branch-ruleset config no retry can
	// clear). Distinct from Outcome (complete|failed): it refines a FAILED run
	// so the dashboard shows "blocked" instead of a generic failure. Empty for
	// ordinary runs — omitempty drops it on the wire. Callers derive it with
	// orchestrator.OutcomeTypeForTerminalFailure(errMsg).
	OutcomeType string
}

// dirCoordinator serializes and de-duplicates run-record writes for a single
// history directory. Multiple HistoryWriter instances (and multiple terminal
// writers — the interactive notifyComplete funnel, the Go scheduler, the
// crash-recovery synthesizer) target the same on-disk history within one
// process; without cross-instance coordination their append + index
// read-modify-write cycles interleave and (a) append several records for one
// run and (b) tear index.json (Issue #313).
//
// mu makes append+updateIndex a single critical section (atomic per run-end),
// and seen is the idempotency ledger keyed by run: the first full-fidelity
// write for a key wins, and a later write is dropped unless it carries strictly
// more stages (an upgrade), so a degraded skeleton can never bury or duplicate
// the authoritative record.
type dirCoordinator struct {
	mu     sync.Mutex
	seeded bool           // seen lazily hydrated from the on-disk index?
	seen   map[string]int // run key -> richest stage count written for that run
}

var (
	dirCoordinatorsMu sync.Mutex
	dirCoordinators   = map[string]*dirCoordinator{}
)

// coordinatorFor returns the process-wide coordinator for a history directory,
// creating it on first use. Keyed by the resolved history dir so every
// HistoryWriter pointing at the same path shares one lock and one ledger.
func coordinatorFor(dir string) *dirCoordinator {
	dirCoordinatorsMu.Lock()
	defer dirCoordinatorsMu.Unlock()
	c, ok := dirCoordinators[dir]
	if !ok {
		c = &dirCoordinator{seen: map[string]int{}}
		dirCoordinators[dir] = c
	}
	return c
}

// runRecordKey is the idempotency key for a run record: the stable run_id when
// present (the dogfood/interactive path threads a UUID v7 through the runtime),
// falling back to issue+started_at for records written before a run_id was
// assigned. Two writers for the SAME run produce the same key; distinct runs
// (including re-runs, which mint a fresh run_id and start_at) never collide.
func runRecordKey(rec V2RunRecord) string {
	if rec.RunID != "" {
		return "run:" + rec.RunID
	}
	return fmt.Sprintf("issue:%d|%s", rec.IssueNumber, rec.StartedAt)
}

// recordRichness measures how much stage-level data a record carries. A late
// finalizer's skeleton (empty stages map) scores 0 and can therefore never
// supersede a real record; any run that actually executed scores >= 1.
func recordRichness(rec V2RunRecord) int {
	return len(rec.Stages)
}

// HistoryWriter appends execution records to JSONL files.
type HistoryWriter struct {
	dir string
}

// NewHistoryWriter creates a history writer for the given workspace root.
func NewHistoryWriter(workspaceRoot string) *HistoryWriter {
	return &HistoryWriter{
		dir: filepath.Join(workspaceRoot, ".nightgauge", "pipeline", "history"),
	}
}

// WriteRecord appends a pre-built V2RunRecord to today's daily JSONL file and
// updates the index. Used by the orchestrator-crash recovery synthesizer
// (Issue #3001) which constructs the record outside the normal RuntimeState
// flow. Atomic-per-line on POSIX (O_APPEND).
func (hw *HistoryWriter) WriteRecord(record V2RunRecord) error {
	now := time.Now()
	if record.RecordedAt != "" {
		if t, parseErr := time.Parse(time.RFC3339, record.RecordedAt); parseErr == nil {
			// Convert to local time to ensure filename matches local date
			// (consistent with WriteV2 behavior where filename is based on local time.Now())
			now = t.Local()
		}
	}
	return hw.appendAndIndex(record, now)
}

// WriteV2 writes a V2-format run record to the daily YYYY-MM-DD.jsonl file
// and updates the index.json. This is the primary write path for the Go scheduler.
func (hw *HistoryWriter) WriteV2(snap *RuntimeState, success bool, errMsg string, input V2RunInput) error {
	now := time.Now()
	record := hw.BuildV2Record(snap, success, errMsg, input, now)
	return hw.WriteV2Record(record, now)
}

// WriteV2Record writes an already-built V2 run record to the daily
// YYYY-MM-DD.jsonl file and updates the index.json. Callers that also need the
// record object — e.g. the interactive terminal funnel, which pushes the same
// record to the platform telemetry sink — build it once via BuildV2Record and
// pass it here, rather than calling WriteV2 (which builds and writes in one
// step and discards the record). `now` dates the daily file.
func (hw *HistoryWriter) WriteV2Record(record V2RunRecord, now time.Time) error {
	return hw.appendAndIndex(record, now)
}

// appendAndIndex is the single serialized, idempotent run-record write path.
// It holds the per-directory coordinator lock across BOTH the JSONL append and
// the index read-modify-write, so concurrent terminal writers can neither
// interleave (torn index.json) nor emit duplicate/degraded records for one run
// (Issue #313). Idempotency: the first write for a run key wins; a later write
// is dropped unless it carries strictly more stages than what was already
// recorded, in which case it is appended as an upgrade and the index entry for
// that key is replaced. A skeleton (empty stages) therefore never appends or
// overwrites once any real record exists.
func (hw *HistoryWriter) appendAndIndex(record V2RunRecord, now time.Time) error {
	c := coordinatorFor(hw.dir)
	c.mu.Lock()
	defer c.mu.Unlock()

	hw.seedSeenLocked(c)

	key := runRecordKey(record)
	rich := recordRichness(record)
	if prev, ok := c.seen[key]; ok && rich <= prev {
		// Duplicate (equal richness) or degraded skeleton (fewer stages): the
		// authoritative record for this run already exists. Drop silently — the
		// JSONL append-only log stays one-line-per-run and the index is untouched.
		return nil
	}

	filename := now.Format("2006-01-02") + ".jsonl"
	filePath := filepath.Join(hw.dir, filename)
	if err := history.AppendJSONL(filePath, record); err != nil {
		return fmt.Errorf("write history entry: %w", err)
	}

	// Update index.json (non-critical — failure logged but not returned). Runs
	// under the coordinator lock so the read-modify-write is atomic.
	if indexErr := hw.updateIndexLocked(record); indexErr != nil {
		fmt.Fprintf(os.Stderr, "[history] index update failed: %v\n", indexErr)
	}

	c.seen[key] = rich
	return nil
}

// seedSeenLocked hydrates the coordinator's idempotency ledger from the on-disk
// index on first use, so a writer started in a fresh process (e.g. the
// crash-recovery synthesizer after a restart) still recognizes runs already
// recorded by a previous process and does not re-append them. The index
// entry's StageCount is a lower bound on richness — sufficient to drop exact
// re-emissions while still allowing a genuinely richer record to upgrade.
// Caller must hold c.mu.
func (hw *HistoryWriter) seedSeenLocked(c *dirCoordinator) {
	if c.seeded {
		return
	}
	c.seeded = true
	idx, _ := hw.readIndex() // best-effort: absent/corrupt index seeds nothing
	for _, e := range idx.Entries {
		key := indexEntryKey(e)
		if e.StageCount > c.seen[key] {
			c.seen[key] = e.StageCount
		}
	}
}

// computeAccumulatedTokens sums token metrics across all completed stages.
// Using per-stage data as the source of truth ensures interim records (written
// after a partial pipeline) show correct accumulated totals instead of reading
// from global accumulators that may not yet reflect all completed stages.
func computeAccumulatedTokens(stages []StageResult) (input, output, cacheRead int, costUSD float64) {
	for _, s := range stages {
		input += s.InputTokens // combined: actual input + cache read
		output += s.OutputTokens
		cacheRead += s.CacheRead
		costUSD += s.CostUSD
	}
	return
}

// BuildV2Record constructs a V2RunRecord from a RuntimeState snapshot.
func (hw *HistoryWriter) BuildV2Record(snap *RuntimeState, success bool, errMsg string, input V2RunInput, now time.Time) V2RunRecord {
	outcome := "complete"
	if !success {
		outcome = "failed"
	}

	startedAt := now.Format(time.RFC3339)
	durationMs := int64(0)
	if !snap.StartedAt.IsZero() {
		startedAt = snap.StartedAt.Format(time.RFC3339)
		durationMs = now.Sub(snap.StartedAt).Milliseconds()
	}

	branch := input.Branch
	if branch == "" {
		branch = fmt.Sprintf("feat/%d", snap.IssueNumber)
	}

	baseBranch := input.BaseBranch
	if baseBranch == "" {
		baseBranch = "main"
	}

	// Build per-stage details and token records
	stages := make(map[string]V2StageDetail)
	perStageTokens := make(map[string]V2StageTokens)

	for _, sr := range snap.CompletedStages {
		stageName := string(sr.Stage)
		stageStarted := sr.StartedAt.Format(time.RFC3339)
		stageCompleted := sr.StartedAt.Add(sr.Duration).Format(time.RFC3339)

		detail := V2StageDetail{
			Status:          "complete",
			StartedAt:       stageStarted,
			CompletedAt:     stageCompleted,
			DurationMs:      sr.Duration.Milliseconds(),
			PerformanceMode: snap.StageModes[stageName],
			ExecutionPath:   snap.StageExecutionPaths[stageName],
			PuntReason:      snap.StagePuntReasons[stageName],
		}

		// Attribute the stage to the model that ACTUALLY ran it (#42) — after
		// escalation overrides, model-unavailable downgrades, and CLI-internal
		// refusal fallbacks (#91). Source distinguishes a plain scheduler
		// resolution from a run where model changes occurred (escalation,
		// fallback, or the CLI's silent refusal swap), so consumers can flag
		// substituted stages without diffing against the predicted model.
		if m := snap.StageModels[stageName]; m != "" {
			source := "scheduler"
			for _, fb := range snap.ModelRefusalFallbacks {
				if fb.Stage == stageName {
					source = "cli-refusal-fallback"
					break
				}
			}
			if source == "scheduler" {
				for _, esc := range snap.EscalationHistory {
					if string(esc.Stage) == stageName {
						source = esc.Reason
						break
					}
				}
			}
			detail.ModelSelection = &V2ModelSelect{Model: m, Source: source}
		}

		// Per-stage tries-until-green: set only when the stage looped (Ralph
		// iterations > 1). Absent/0 means the clean default of a single attempt,
		// keeping ordinary records lean. Issue #4172.
		if iters := snap.RalphIterations[stageName]; iters > 1 {
			detail.AttemptsUntilSuccess = iters
		}

		// Check for stage error
		if stageErr, ok := snap.StageErrors[stageName]; ok && stageErr != "" {
			detail.Status = "failed"
			detail.Error = stageErr
		}

		stages[stageName] = detail

		// Compute per-stage cache hit rate: cache_read / (input + cache_read)
		// sr.InputTokens is the combined value (actual input + cache_read).
		var cacheHitRate *float64
		if sr.InputTokens > 0 {
			rate := float64(sr.CacheRead) / float64(sr.InputTokens)
			cacheHitRate = &rate
		}

		// Issue #3224: prefer per-stage adapter recorded by the resolver
		// (#3221), falling back to the run-level default when absent. Empty
		// string is left as-is so omitempty drops the key on the wire.
		stageAdapter := snap.StageAdapters[stageName]
		if stageAdapter == "" {
			stageAdapter = input.DefaultAdapter
		}

		perStageTokens[stageName] = V2StageTokens{
			Input:         sr.InputTokens - sr.CacheRead, // actual non-cached input tokens
			Output:        sr.OutputTokens,
			CacheRead:     sr.CacheRead,
			CacheCreation: 0, // not tracked per stage — only available at run level via execution stream
			CostUSD:       sr.CostUSD,
			CacheHitRate:  cacheHitRate,
			Adapter:       stageAdapter,
		}
	}

	for _, skipped := range snap.SkippedStages {
		stages[skipped] = V2StageDetail{
			Status: "skipped",
		}
	}

	// If there's a global error but no specific stage error, attach to the last stage
	if errMsg != "" && !success {
		if snap.Stage != "" {
			stageName := string(snap.Stage)
			if detail, ok := stages[stageName]; ok {
				detail.Status = "failed"
				detail.Error = errMsg
				stages[stageName] = detail
			} else {
				// Stage failed before CompletedStages got an entry — synthesize
				// a minimal failed detail so the timeline isn't lost.
				stages[stageName] = V2StageDetail{
					Status:          "failed",
					StartedAt:       snap.StageStart.Format(time.RFC3339),
					Error:           errMsg,
					PerformanceMode: snap.StageModes[stageName],
					ExecutionPath:   snap.StageExecutionPaths[stageName],
					PuntReason:      snap.StagePuntReasons[stageName],
				}
			}
		}
	}

	// Attach per-stage output tails (Issue #3001) — captured by the runtime
	// ring buffer at terminal failure. Only populates fields on stages we
	// already know about; missing stages are left absent (no synthesis).
	for stageName, tail := range input.StageOutputTails {
		if tail == "" {
			continue
		}
		if detail, ok := stages[stageName]; ok {
			detail.LastOutputLines = tail
			stages[stageName] = detail
		}
	}

	// Attach per-stage failure_category overrides (Issue #3005). Mirrors the
	// output-tails pattern: only populates fields on existing stages.
	for stageName, category := range input.StageFailureCategories {
		if category == "" {
			continue
		}
		if detail, ok := stages[stageName]; ok {
			detail.FailureCategory = category
			stages[stageName] = detail
		}
	}

	// Attach per-stage gate_results (Issue #3266). Mirrors the output-tails
	// pattern: only populates fields on existing stages.
	for stageName, gateResults := range snap.StageGateResults {
		if len(gateResults) == 0 {
			continue
		}
		if detail, ok := stages[stageName]; ok {
			copied := make([]StageGateResult, len(gateResults))
			copy(copied, gateResults)
			detail.GateResults = copied
			stages[stageName] = detail
		}
	}

	// Attach per-stage anomalies (Issue #3267). Same pattern.
	for stageName, anomalies := range snap.StageAnomalies {
		if len(anomalies) == 0 {
			continue
		}
		if detail, ok := stages[stageName]; ok {
			copied := make([]Anomaly, len(anomalies))
			copy(copied, anomalies)
			detail.Anomalies = copied
			stages[stageName] = detail
		}
	}

	// Attach per-stage recovery attempts (Issue #3268). Same pattern.
	for stageName, attempts := range snap.StageRecoveryAttempts {
		if len(attempts) == 0 {
			continue
		}
		if detail, ok := stages[stageName]; ok {
			copied := make([]RecoveryAttempt, len(attempts))
			copy(copied, attempts)
			detail.RecoveryAttempts = copied
			stages[stageName] = detail
		}
	}

	// Nullable string pointers for size/type
	var sizePtr, typePtr *string
	if input.Size != "" {
		sizePtr = &input.Size
	}
	if input.IssueType != "" {
		typePtr = &input.IssueType
	}

	skipStages := input.SkipStages
	if skipStages == nil {
		skipStages = []string{}
	}

	routingPath := input.RoutingPath
	if routingPath == "" {
		routingPath = "standard"
	}

	// Compute token totals from per-stage data rather than global accumulators.
	// This ensures interim records (written mid-pipeline after N completed stages)
	// always reflect the correct accumulated values for those N stages.
	accInput, accOutput, accCacheRead, accCostUSD := computeAccumulatedTokens(snap.CompletedStages)

	// Bump schema_version to "3" when V3-only fields are populated (Issue #3001).
	schemaVersion := "2"
	if input.TerminalFailureKind != "" || hasOutputTails(stages) {
		schemaVersion = "3"
	}

	rec := V2RunRecord{
		SchemaVersion: schemaVersion,
		RecordType:    "run",
		IssueNumber:   snap.IssueNumber,
		RunID:         snap.RunID,
		Repo:          snap.Repo,
		Title:         input.Title,
		Branch:        branch,
		BaseBranch:    baseBranch,
		ExecutionMode: "automatic",
		StartedAt:     startedAt,
		CompletedAt:   now.Format(time.RFC3339),
		TotalDuration: durationMs,
		Outcome:       outcome,
		// Issue body captured at pickup (#183). Bounded here as a safety net in
		// case a caller (e.g. the IPC path reading a runtime state the extension
		// populated) supplies an unbounded value; the pickup capture already caps
		// it. 8192 matches the platform issueBody .max(8192) telemetry bound.
		Body:   clipHistoryRunes(input.Body, v2RunBodyMax),
		Labels: input.Labels,
		Size:   sizePtr,
		Type:   typePtr,
		Stages: stages,
		Tokens: V2Tokens{
			TotalInput:       accInput,
			TotalOutput:      accOutput,
			TotalCacheRead:   accCacheRead,
			EstimatedCostUSD: accCostUSD,
			PerStage:         perStageTokens,
		},
		Files: V2Files{},
		Routing: V2Routing{
			ComplexityScore: input.ComplexityScore,
			Path:            routingPath,
			SkipStages:      skipStages,
			ChangeClass:     input.ChangeClass,
		},
		IsRecovery:          input.IsRecovery,
		TerminalFailureKind: input.TerminalFailureKind,
		OutcomeType:         input.OutcomeType,
		PerformanceMode:     dominantPerformanceMode(stages),
		RecordedAt:          now.Format(time.RFC3339),
	}
	// Set the canonical tries-until-green only when the run actually retried,
	// escalated, or looped — a clean run omits it (readers default to 1). #4172.
	if a := ComputeAttemptsUntilSuccess(snap.RalphIterations, snap.RetryCount, len(snap.EscalationHistory)); a > 1 {
		rec.AttemptsUntilSuccess = a
	}
	return rec
}

// ComputeAttemptsUntilSuccess returns the canonical "tries until green" count for
// a run (Issue #4172): the first attempt, plus every stage retry, plus every
// model escalation, plus each extra Ralph iteration (iterations beyond the first,
// summed across stages). A clean run with no retries/escalations/Ralph loops
// returns 1. Pure and deterministic — no dependency on clock or state mutation.
func ComputeAttemptsUntilSuccess(ralphIterations map[string]int, retryCount, escalationCount int) int {
	attempts := 1 + retryCount + escalationCount
	for _, iters := range ralphIterations {
		if iters > 1 {
			attempts += iters - 1
		}
	}
	return attempts
}

// hasOutputTails reports whether any stage carries a non-empty
// LastOutputLines field (Issue #3001 — V3 schema marker).
func hasOutputTails(stages map[string]V2StageDetail) bool {
	for _, s := range stages {
		if s.LastOutputLines != "" {
			return true
		}
	}
	return false
}

// dominantPerformanceMode returns the most common non-empty PerformanceMode
// across all stages, used to derive the run-level performance_mode field
// (Issue #3218). Returns empty string when no stage has a mode set.
func dominantPerformanceMode(stages map[string]V2StageDetail) string {
	counts := map[string]int{}
	for _, s := range stages {
		if s.PerformanceMode != "" {
			counts[s.PerformanceMode]++
		}
	}
	best := ""
	bestCount := 0
	// Deterministic winner order: maximum > elevated > efficiency
	for _, mode := range []string{"maximum", "elevated", "efficiency"} {
		if counts[mode] > bestCount {
			best = mode
			bestCount = counts[mode]
		}
	}
	return best
}

// indexEntryKey mirrors runRecordKey for an already-projected index entry, so
// the index can be de-duplicated by run identity. The index carries run_id
// (additive field) on records written since Issue #313; older entries fall back
// to issue+started_at.
func indexEntryKey(e V2IndexEntry) string {
	if e.RunID != "" {
		return "run:" + e.RunID
	}
	return fmt.Sprintf("issue:%d|%s", e.IssueNumber, e.StartedAt)
}

// buildIndexEntry projects a run record onto its index entry.
func buildIndexEntry(record V2RunRecord) V2IndexEntry {
	stageCount := 0
	for _, stage := range record.Stages {
		if stage.Status == "complete" || stage.Status == "skipped" {
			stageCount++
		}
	}
	return V2IndexEntry{
		IssueNumber:            record.IssueNumber,
		RunID:                  record.RunID,
		Title:                  record.Title,
		Outcome:                record.Outcome,
		OutcomeType:            record.OutcomeType,
		IsRecovery:             record.IsRecovery,
		PerformanceMode:        record.PerformanceMode,
		CostUSD:                record.Tokens.EstimatedCostUSD,
		TotalInputTokens:       record.Tokens.TotalInput,
		TotalOutputTokens:      record.Tokens.TotalOutput,
		TotalCacheReadTokens:   record.Tokens.TotalCacheRead,
		TotalCacheCreateTokens: record.Tokens.TotalCacheCreation,
		DurationMs:             record.TotalDuration,
		StageCount:             stageCount,
		StartedAt:              record.StartedAt,
		RecordedAt:             record.RecordedAt,
		Labels:                 record.Labels,
		Size:                   record.Size,
		Type:                   record.Type,
		Branch:                 record.Branch,
	}
}

// readIndex loads index.json. It returns a fresh empty index when the file is
// absent, and (false) when the file exists but is unparseable — signaling the
// caller to rebuild from the JSONL source of truth rather than silently
// discarding it.
func (hw *HistoryWriter) readIndex() (V2Index, bool) {
	fresh := V2Index{SchemaVersion: "1", Entries: []V2IndexEntry{}}
	data, err := os.ReadFile(filepath.Join(hw.dir, "index.json"))
	if err != nil {
		return fresh, os.IsNotExist(err) // missing == clean start; other errors == "rebuild"
	}
	var existing V2Index
	if json.Unmarshal(data, &existing) == nil && existing.SchemaVersion != "" {
		if existing.Entries == nil {
			existing.Entries = []V2IndexEntry{}
		}
		return existing, true
	}
	return fresh, false
}

// updateIndexLocked de-duplicates the index by run key, prepends the new
// entry (most-recent-first), and writes back atomically (temp file + rename).
// The caller MUST hold the directory coordinator lock so the read-modify-write
// cannot interleave with a concurrent completion (Issue #313 — torn index.json).
//
// When the existing index is present but unparseable, the entries are REBUILT
// from the daily JSONL files (the append-only source of truth) instead of
// silently starting fresh — automating the rebuild an operator previously did
// by hand.
func (hw *HistoryWriter) updateIndexLocked(record V2RunRecord) error {
	indexPath := filepath.Join(hw.dir, "index.json")

	idx, ok := hw.readIndex()
	if !ok {
		// Existing index is corrupt: reconstruct entries from the JSONL records.
		idx = V2Index{SchemaVersion: "1", Entries: hw.rebuildIndexEntriesFromJSONL()}
	}

	entry := buildIndexEntry(record)
	key := indexEntryKey(entry)

	// Drop any prior entry for this run (an upgrade replacing a leaner record,
	// or a re-open of the JSONL that already contained it) so the index holds
	// exactly one entry per run.
	deduped := idx.Entries[:0]
	for _, e := range idx.Entries {
		if indexEntryKey(e) == key {
			continue
		}
		deduped = append(deduped, e)
	}
	idx.Entries = append([]V2IndexEntry{entry}, deduped...)
	idx.TotalRuns = len(idx.Entries)
	idx.UpdatedAt = time.Now().Format(time.RFC3339)

	return writeIndexAtomic(indexPath, idx)
}

// writeIndexAtomic marshals and writes the index via a temp file + rename so a
// reader never observes a partially written index.json.
func writeIndexAtomic(indexPath string, idx V2Index) error {
	outData, err := json.MarshalIndent(idx, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal index: %w", err)
	}
	tmpPath := indexPath + ".tmp"
	if err := os.WriteFile(tmpPath, outData, 0644); err != nil {
		return fmt.Errorf("write tmp index: %w", err)
	}
	if err := os.Rename(tmpPath, indexPath); err != nil {
		os.Remove(tmpPath)
		return fmt.Errorf("rename index: %w", err)
	}
	return nil
}

// rebuildIndexEntriesFromJSONL reconstructs the index entry list from every
// daily JSONL file on disk — the append-only records are the source of truth.
// Records are de-duplicated by run key (richest wins) and returned
// most-recent-first, matching the live prepend ordering.
func (hw *HistoryWriter) rebuildIndexEntriesFromJSONL() []V2IndexEntry {
	records := dedupeRichestByKey(hw.readAllDailyRecords())
	// readAllDailyRecords returns oldest-first; reverse into most-recent-first.
	entries := make([]V2IndexEntry, 0, len(records))
	for i := len(records) - 1; i >= 0; i-- {
		entries = append(entries, buildIndexEntry(records[i]))
	}
	return entries
}

// Write appends a legacy HistoryEntry to executions.jsonl.
// Retained for backward compatibility; new code should use WriteV2.
func (hw *HistoryWriter) Write(rs *RuntimeState, success bool, errMsg string) error {
	if err := os.MkdirAll(hw.dir, 0755); err != nil {
		return fmt.Errorf("create history dir: %w", err)
	}

	snap := rs.Snapshot()
	entry := HistoryEntry{
		Timestamp:     time.Now(),
		Repo:          snap.Repo,
		IssueNumber:   snap.IssueNumber,
		Duration:      snap.TotalDuration(),
		InputTokens:   snap.InputTokens,
		OutputTokens:  snap.OutputTokens,
		TotalCostUSD:  snap.TotalCostUSD,
		Stages:        snap.CompletedStages,
		SkippedStages: snap.SkippedStages,
		Success:       success,
		Error:         errMsg,
	}

	data, err := json.Marshal(entry)
	if err != nil {
		return fmt.Errorf("marshal history entry: %w", err)
	}

	filename := filepath.Join(hw.dir, "executions.jsonl")
	f, err := os.OpenFile(filename, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644)
	if err != nil {
		return fmt.Errorf("open history file: %w", err)
	}
	defer f.Close()

	if _, err := f.Write(append(data, '\n')); err != nil {
		return fmt.Errorf("write history entry: %w", err)
	}

	return nil
}

// ReadRecentV2 reads the last N V2RunRecords from daily YYYY-MM-DD.jsonl files.
// It reads up to days most recent daily files (defaults to 7 when days <= 0).
// Returns records in chronological order (oldest first).
// Skips malformed JSON lines.
func (hw *HistoryWriter) ReadRecentV2(n int, days int) ([]V2RunRecord, error) {
	if days <= 0 {
		days = 7
	}

	entries, err := os.ReadDir(hw.dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("read history dir: %w", err)
	}

	// Collect daily JSONL filenames (YYYY-MM-DD.jsonl) sorted descending.
	var dailyFiles []string
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		name := e.Name()
		if len(name) == len("2006-01-02.jsonl") && strings.HasSuffix(name, ".jsonl") {
			// Validate it looks like a date file (not executions.jsonl etc.)
			base := strings.TrimSuffix(name, ".jsonl")
			if len(base) == 10 && base[4] == '-' && base[7] == '-' {
				dailyFiles = append(dailyFiles, name)
			}
		}
	}

	// Sort descending (most recent first) — lexicographic works for YYYY-MM-DD.
	for i := 0; i < len(dailyFiles); i++ {
		for j := i + 1; j < len(dailyFiles); j++ {
			if dailyFiles[i] < dailyFiles[j] {
				dailyFiles[i], dailyFiles[j] = dailyFiles[j], dailyFiles[i]
			}
		}
	}

	// Read up to `days` files.
	if len(dailyFiles) > days {
		dailyFiles = dailyFiles[:days]
	}

	// Reverse to ascending order so we read oldest-first files, preserving
	// chronological order within the final slice.
	for i, j := 0, len(dailyFiles)-1; i < j; i, j = i+1, j-1 {
		dailyFiles[i], dailyFiles[j] = dailyFiles[j], dailyFiles[i]
	}

	var records []V2RunRecord
	for _, fname := range dailyFiles {
		filePath := filepath.Join(hw.dir, fname)
		data, err := os.ReadFile(filePath)
		if err != nil {
			continue // Skip unreadable files
		}
		for _, line := range splitLines(data) {
			if len(line) == 0 {
				continue
			}
			var rec V2RunRecord
			if err := json.Unmarshal(line, &rec); err != nil {
				continue // Skip malformed lines
			}
			records = append(records, rec)
		}
	}

	// Collapse any duplicate records for a run to the single richest one
	// (Issue #313), so a consumer never sees a degraded skeleton buried under —
	// or shadowing — the authoritative record, even on history files that were
	// written before the single-flight writer landed.
	records = dedupeRichestByKey(records)

	// Trim to last N records if n > 0.
	if n > 0 && len(records) > n {
		records = records[len(records)-n:]
	}
	return records, nil
}

// readAllDailyRecords reads every daily YYYY-MM-DD.jsonl file in the history
// directory (no day window), oldest-first, skipping malformed lines. Used to
// rebuild the index from the append-only source of truth (Issue #313).
func (hw *HistoryWriter) readAllDailyRecords() []V2RunRecord {
	entries, err := os.ReadDir(hw.dir)
	if err != nil {
		return nil
	}
	var dailyFiles []string
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		name := e.Name()
		if len(name) == len("2006-01-02.jsonl") && strings.HasSuffix(name, ".jsonl") {
			base := strings.TrimSuffix(name, ".jsonl")
			if len(base) == 10 && base[4] == '-' && base[7] == '-' {
				dailyFiles = append(dailyFiles, name)
			}
		}
	}
	// Ascending (oldest-first) — lexicographic works for YYYY-MM-DD.
	for i := 0; i < len(dailyFiles); i++ {
		for j := i + 1; j < len(dailyFiles); j++ {
			if dailyFiles[i] > dailyFiles[j] {
				dailyFiles[i], dailyFiles[j] = dailyFiles[j], dailyFiles[i]
			}
		}
	}
	var records []V2RunRecord
	for _, fname := range dailyFiles {
		data, err := os.ReadFile(filepath.Join(hw.dir, fname))
		if err != nil {
			continue
		}
		for _, line := range splitLines(data) {
			if len(line) == 0 {
				continue
			}
			var rec V2RunRecord
			if err := json.Unmarshal(line, &rec); err != nil {
				continue
			}
			records = append(records, rec)
		}
	}
	return records
}

// dedupeRichestByKey collapses records that share a run key to a single record,
// keeping the richest (most stages); on a tie the later record wins (freshest
// among equally complete). Input order is otherwise preserved by keeping each
// run at the position of its first occurrence (Issue #313).
func dedupeRichestByKey(records []V2RunRecord) []V2RunRecord {
	if len(records) < 2 {
		return records
	}
	pos := make(map[string]int, len(records)) // key -> index in out
	out := make([]V2RunRecord, 0, len(records))
	for _, rec := range records {
		key := runRecordKey(rec)
		if i, ok := pos[key]; ok {
			if recordRichness(rec) >= recordRichness(out[i]) {
				out[i] = rec // upgrade in place (>= keeps the freshest on ties)
			}
			continue
		}
		pos[key] = len(out)
		out = append(out, rec)
	}
	return out
}

// ReadRecent reads the last N legacy history entries from executions.jsonl.
func (hw *HistoryWriter) ReadRecent(n int) ([]HistoryEntry, error) {
	filename := filepath.Join(hw.dir, "executions.jsonl")
	data, err := os.ReadFile(filename)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("read history: %w", err)
	}

	var entries []HistoryEntry
	for _, line := range splitLines(data) {
		if len(line) == 0 {
			continue
		}
		var entry HistoryEntry
		if err := json.Unmarshal(line, &entry); err != nil {
			continue // Skip malformed entries
		}
		entries = append(entries, entry)
	}

	if n > 0 && len(entries) > n {
		entries = entries[len(entries)-n:]
	}
	return entries, nil
}

func splitLines(data []byte) [][]byte {
	var lines [][]byte
	start := 0
	for i := 0; i < len(data); i++ {
		if data[i] == '\n' {
			lines = append(lines, data[start:i])
			start = i + 1
		}
	}
	if start < len(data) {
		lines = append(lines, data[start:])
	}
	return lines
}

// ExtractTypeFromLabels extracts the issue type from labels.
func ExtractTypeFromLabels(labels []string) string {
	for _, l := range labels {
		lower := strings.ToLower(l)
		if lower == "bug" || lower == "type:bug" {
			return "bug"
		}
		if lower == "enhancement" || lower == "type:feature" || lower == "feature" {
			return "feature"
		}
		if strings.HasPrefix(lower, "type:") {
			return strings.TrimPrefix(lower, "type:")
		}
	}
	return ""
}
