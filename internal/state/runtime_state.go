package state

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/nightgauge/nightgauge/internal/intelligence/tokens"
)

// LicenseSnapshot records the license validation result captured at pipeline start.
// Used by the scheduler to detect mid-pipeline expiry without re-validating.
type LicenseSnapshot struct {
	Tier       string    `json:"tier"`
	Allowed    bool      `json:"allowed"`
	CacheUntil time.Time `json:"cacheUntil"` // zero = no expiry (community tier)
	// Status is one of "active"/"expired"/"revoked"/"suspended", or "" when
	// unknown. Refreshed on every re-validation (#4156) so the most recent
	// confirmed status is visible on the runtime snapshot for diagnostics.
	Status string `json:"status,omitempty"`
}

// RuntimeState holds in-memory-only state for a single pipeline execution.
// This data is NOT persisted — if the process dies, these metrics are lost (acceptable).
// On completion, metrics are written to the execution history JSONL.
type RuntimeState struct {
	mu sync.Mutex

	// Execution identity
	Repo        string `json:"repo"`
	IssueNumber int    `json:"issueNumber"`
	ItemID      string `json:"itemId"`
	Title       string `json:"title,omitempty"`
	// Body is the GitHub issue body captured at pickup (#183), bounded to a
	// sensible excerpt at capture time. Threaded onto the V2 run record and the
	// telemetry wire so the dashboard run-detail page can show what the run is
	// doing without leaving the dashboard. Empty when no issue body was resolved.
	Body   string `json:"body,omitempty"`
	Branch string `json:"branch,omitempty"`
	RunID       string `json:"runId,omitempty"` // UUID v7 from runstate, threaded into all PipelineEvent emissions (#3557)

	// Current stage
	Stage      PipelineStage `json:"stage"`
	StartedAt  time.Time     `json:"startedAt"`
	StageStart time.Time     `json:"stageStart"`

	// Process tracking
	PID         int    `json:"pid,omitempty"`
	WorktreeDir string `json:"worktreeDir,omitempty"`

	// AuthoritativeChangeClass is the post-dev change classification captured
	// DURING the run (while the worktree + diff still exist), so the run record
	// gets the real class even after the worktree is archived (#4129). Empty
	// until a content-producing stage has run.
	AuthoritativeChangeClass string `json:"authoritativeChangeClass,omitempty"`

	// Token/cost metrics (accumulated across stages)
	InputTokens  int     `json:"inputTokens"`
	OutputTokens int     `json:"outputTokens"`
	TotalCostUSD float64 `json:"totalCostUsd"`

	// Stage history
	CompletedStages []StageResult `json:"completedStages"`
	SkippedStages   []string      `json:"skippedStages"`

	// Phase tracking
	PhaseHistory []PhaseRecord     `json:"phaseHistory"`
	StageErrors  map[string]string `json:"stageErrors"` // stage → error message

	// Pause state (persisted to runtime-{N}.json for reload recovery)
	Paused bool `json:"paused,omitempty"`

	// Orchestration tracking (populated by Go scheduler engines)
	RetryCount        int                `json:"retryCount,omitempty"`
	EscalationHistory []EscalationRecord `json:"escalationHistory,omitempty"`
	RalphIterations   map[string]int     `json:"ralphIterations,omitempty"` // stage → iteration count

	// Quality gate results (populated after feature-validate)
	GateResults []GateResult `json:"gateResults,omitempty"`

	// PR URL (populated after pr-create)
	PrUrl string `json:"prUrl,omitempty"`

	// MergedCommitSha + MergedAt are the post-merge ground-truth breadcrumb
	// (#4133): the merge commit on the base branch and GitHub's ISO-8601 merge
	// timestamp, captured by the post-merge hook. Empty until pr-merge completes
	// (and stay empty when the breadcrumb fetch fails — non-blocking).
	MergedCommitSha string `json:"mergedCommitSha,omitempty"`
	MergedAt        string `json:"mergedAt,omitempty"`

	// License tracking — in-memory only, never persisted to disk
	License              *LicenseSnapshot `json:"license,omitempty"`
	LicenseExpiredMidRun bool             `json:"licenseExpiredMidRun,omitempty"`

	// StageOutputTails captures the last lines of subagent stdout/stderr per
	// stage, bounded to ~200 lines × ≤1KB/line via the runtime ring buffer
	// (Issue #3001). Populated by StageRunner implementations (IPC + auto)
	// when they have access to the streamed output. On terminal failure, the
	// tail for the failed stage is copied into the V3 RunRecord so operators
	// can diagnose without re-running.
	StageOutputTails map[string]string `json:"stageOutputTails,omitempty"`

	// StageModes captures the performance mode resolved at each stage's start
	// (Issue #3215). Keys are stage names; values are one of
	// "efficiency" | "elevated" | "maximum". The map is keyed by stage rather
	// than appended to StageResult so the mode survives stage failures, stalls,
	// and crashes — BuildV2Record reads from this map regardless of how the
	// stage terminated.
	StageModes map[string]string `json:"stageModes,omitempty"`

	// StageAdapters captures the adapter resolved at each stage's start
	// (Issue #3224). Keys are stage names; values are the adapter id (one of
	// "claude" | "codex" | "gemini" | "gemini-sdk" | "lm-studio" | "ollama" |
	// "copilot"). Mirrors StageModes — keyed by stage rather than appended to
	// StageResult so the value survives stage failures, stalls, and crashes.
	// BuildV2Record reads from this map and falls back to V2RunInput's
	// DefaultAdapter when a stage has no entry.
	StageAdapters map[string]string `json:"stageAdapters,omitempty"`

	// StageModels captures the model that ACTUALLY executed each stage
	// (Issue #42) — after escalation overrides and model-unavailable tier
	// downgrades, which can differ from the run-level predicted model.
	// Mirrors StageModes/StageAdapters: keyed by stage so the value survives
	// stage failures, stalls, and crashes. BuildV2Record projects this map
	// onto V2StageDetail.ModelSelection so outcome records and cost telemetry
	// attribute each stage to the model that ran it.
	StageModels map[string]string `json:"stageModels,omitempty"`

	// ModelRefusalFallbacks is the append-only record of CLI-internal model
	// swaps observed in the stage stream (#91): on a safety refusal the
	// claude CLI silently retries the turn on a fallback model and the
	// session still exits 0. Attribution only — the scheduler re-records
	// StageModels with the served model and BuildV2Record marks the stage's
	// ModelSelection source as "cli-refusal-fallback"; routing and retry
	// never key off this. See docs/spikes/fable-5-behavior-porting.md §8.3.
	ModelRefusalFallbacks []ModelRefusalFallback `json:"modelRefusalFallbacks,omitempty"`

	// StageExecutionPaths captures the execution path resolved at each stage
	// (Issue #3264). Keys are stage names; values are one of "deterministic" |
	// "llm". Recorded by the scheduler when a deterministic-first hook fires
	// — currently only pr-merge; future stages (pr-create has been suggested
	// in epic #3261) can populate this map without schema growth.
	// BuildV2Record reads from this map onto V2StageDetail.ExecutionPath.
	StageExecutionPaths map[string]string `json:"stageExecutionPaths,omitempty"`

	// StagePuntReasons captures the machine-readable reason a deterministic-first
	// hook declined and fell through to the LLM path (Issue #297). Keys are stage
	// names; values are the runner's punt reason code (e.g. "missing-dev-context",
	// "dirty-merge-state: BLOCKED"). Only set when ExecutionPath is "llm" AND the
	// deterministic path actually ran and punted — a stage that has no
	// deterministic-first hook records neither field. BuildV2Record reads from
	// this map onto V2StageDetail.PuntReason so a run's history JSONL answers WHY
	// the deterministic path was not taken, which pre-#297 required forensic
	// archaeology across session logs.
	StagePuntReasons map[string]string `json:"stagePuntReasons,omitempty"`

	// StageGateResults captures the post-condition gate outcomes recorded by
	// the stage-gate framework (Issue #3266). Keys are stage names; values
	// are slices because a future stage could register multiple gates. The
	// scheduler appends to this map immediately after a gate runs; the V2
	// writer projects this map onto V2StageDetail.GateResults per stage.
	StageGateResults map[string][]StageGateResult `json:"stageGateResults,omitempty"`

	// StageAnomalies captures per-stage anomaly records (Issue #3267) — e.g.,
	// the atomic-eligible-stage LLM-overrun detector. Additive: scheduler
	// appends entries via AppendStageAnomaly; BuildV2Record projects the map
	// onto V2StageDetail.Anomalies. Older state files omit this map; readers
	// default to nil/empty.
	StageAnomalies map[string][]Anomaly `json:"stageAnomalies,omitempty"`

	// StageRecoveryAttempts captures FailureRecovery registry outcomes per
	// stage (Issue #3268). Additive: scheduler appends entries via
	// AppendRecoveryAttempt; BuildV2Record projects the map onto
	// V2StageDetail.RecoveryAttempts. Older state files omit this map; readers
	// default to nil/empty.
	StageRecoveryAttempts map[string][]RecoveryAttempt `json:"stageRecoveryAttempts,omitempty"`
}

// StageOutputBufferLineLimit caps each captured tail at this many lines
// (Issue #3001). Combined with a per-line byte budget elsewhere this keeps
// each per-stage tail bounded to ~200KB.
const StageOutputBufferLineLimit = 200

// StageOutputBufferByteCap is the absolute upper bound applied to any single
// captured tail to defend against pathological log lines. (Issue #3001)
const StageOutputBufferByteCap = 200 * 1024 // 200KB

// PhaseRecord records the lifecycle of a single phase within a stage.
type PhaseRecord struct {
	Stage       PipelineStage `json:"stage"`
	Name        string        `json:"name"`
	Index       int           `json:"index"`
	Total       int           `json:"total"`
	Status      string        `json:"status"` // "running" | "complete" | "skipped"
	StartedAt   time.Time     `json:"startedAt"`
	CompletedAt *time.Time    `json:"completedAt,omitempty"`
}

// EscalationRecord records a model escalation event during pipeline execution.
type EscalationRecord struct {
	Stage     PipelineStage `json:"stage"`
	FromModel string        `json:"fromModel"`
	ToModel   string        `json:"toModel"`
	Reason    string        `json:"reason"`
	At        time.Time     `json:"at"`
}

// StageResult records the outcome of a completed stage.
type StageResult struct {
	Stage        PipelineStage `json:"stage"`
	StartedAt    time.Time     `json:"startedAt"`
	Duration     time.Duration `json:"duration"`
	ExitCode     int           `json:"exitCode"`
	InputTokens  int           `json:"inputTokens"` // combined: actual input + cache read
	OutputTokens int           `json:"outputTokens"`
	CacheRead    int           `json:"cacheRead"` // cache read tokens (subset of InputTokens)
	CostUSD      float64       `json:"costUsd"`
}

// NewRuntimeState creates a new runtime state for a pipeline execution.
func NewRuntimeState(repo string, issueNumber int, itemID string) *RuntimeState {
	return &RuntimeState{
		Repo:        repo,
		IssueNumber: issueNumber,
		ItemID:      itemID,
		StartedAt:   time.Now(),
		StageErrors: make(map[string]string),
	}
}

// BeginStage marks the start of a new pipeline stage.
func (rs *RuntimeState) BeginStage(stage PipelineStage) {
	rs.mu.Lock()
	defer rs.mu.Unlock()
	rs.Stage = stage
	rs.StageStart = time.Now()
}

// CompleteStage records the completion of the current stage.
// model is the AI model used (e.g., "claude-sonnet-4-6"). If empty, a default
// cost rate is applied. Cost is calculated from token counts and model rates.
func (rs *RuntimeState) CompleteStage(exitCode, inputTokens, outputTokens int, model string) {
	cost := tokens.CalculateCost(model, inputTokens, outputTokens)
	rs.completeStageInternal(exitCode, inputTokens, outputTokens, 0, cost)
}

// CompleteStageWithCost records stage completion using the actual cost from
// Claude CLI (total_cost_usd) instead of recalculating from token counts.
// This is more accurate because it accounts for cache_read tokens at their
// lower per-token rate.
func (rs *RuntimeState) CompleteStageWithCost(exitCode, inputTokens, outputTokens, cacheReadTokens int, actualCostUsd float64) {
	rs.completeStageInternal(exitCode, inputTokens+cacheReadTokens, outputTokens, cacheReadTokens, actualCostUsd)
}

func (rs *RuntimeState) completeStageInternal(exitCode, inputTokens, outputTokens, cacheReadTokens int, cost float64) {
	rs.mu.Lock()
	defer rs.mu.Unlock()

	// Idempotency guard (#230): if this exact stage occurrence was already
	// completed — same Stage AND the same BeginStage-stamped StageStart — skip
	// it so a residual double-complete yields exactly one completedStages entry
	// and never double-counts tokens/cost. A legitimate retry re-runs
	// BeginStage, which advances StageStart, so its completion carries a
	// distinct StartedAt and still appends.
	if n := len(rs.CompletedStages); n > 0 {
		last := rs.CompletedStages[n-1]
		if last.Stage == rs.Stage && last.StartedAt.Equal(rs.StageStart) {
			return
		}
	}

	result := StageResult{
		Stage:        rs.Stage,
		StartedAt:    rs.StageStart,
		Duration:     time.Since(rs.StageStart),
		ExitCode:     exitCode,
		InputTokens:  inputTokens,
		OutputTokens: outputTokens,
		CacheRead:    cacheReadTokens,
		CostUSD:      cost,
	}
	rs.CompletedStages = append(rs.CompletedStages, result)
	rs.InputTokens += inputTokens
	rs.OutputTokens += outputTokens
	rs.TotalCostUSD += cost
}

// LastStageDurationMs returns the wall-clock duration of the most recently
// completed stage in milliseconds, or 0 when no stage has completed yet.
// Mutex-safe — used by the IPC telemetry emitter to populate the platform's
// `stage_completed` durationMs field.
func (rs *RuntimeState) LastStageDurationMs() int {
	rs.mu.Lock()
	defer rs.mu.Unlock()
	if len(rs.CompletedStages) == 0 {
		return 0
	}
	d := rs.CompletedStages[len(rs.CompletedStages)-1].Duration.Milliseconds()
	if d < 0 {
		return 0
	}
	return int(d)
}

// SkipStage records a skipped stage.
func (rs *RuntimeState) SkipStage(stage PipelineStage) {
	rs.mu.Lock()
	defer rs.mu.Unlock()
	rs.SkippedStages = append(rs.SkippedStages, string(stage))
}

// IsStageSkipped reports whether the named stage was skipped on this run (its
// output context was never written). Used by skip-aware prerequisite resolution
// so a fast-tracked run (e.g. docs-only skips feature-planning + feature-validate)
// consumes the nearest upstream stage that actually ran instead of failing on a
// missing context file.
func (rs *RuntimeState) IsStageSkipped(stage PipelineStage) bool {
	rs.mu.Lock()
	defer rs.mu.Unlock()
	for _, s := range rs.SkippedStages {
		if s == string(stage) {
			return true
		}
	}
	return false
}

// SetProcess records the child process PID and worktree path.
// SetAuthoritativeChangeClass records the post-dev change classification on the
// runtime so it survives worktree archival and is read back at record time.
func (rs *RuntimeState) SetAuthoritativeChangeClass(class string) {
	rs.mu.Lock()
	defer rs.mu.Unlock()
	rs.AuthoritativeChangeClass = class
}

func (rs *RuntimeState) SetProcess(pid int, worktreeDir string) {
	rs.mu.Lock()
	defer rs.mu.Unlock()
	rs.PID = pid
	rs.WorktreeDir = worktreeDir
}

// phaseStartDedupeWindow bounds BeginPhase's consecutive-duplicate guard.
// A marker sighted more than once for a single emission (command echo,
// tool_result stdout, text narration) arrives within seconds; a legitimate
// re-run of the same phase (stage retry) takes longer and follows other
// records. 60s absorbs the worst observed straggler (a buffered echo flushed
// 43s late in #217) without eating real re-runs.
const phaseStartDedupeWindow = 60 * time.Second

// BeginPhase records the start of a new phase within a stage.
//
// Consecutive duplicate guard (#217): skills emit markers via
// `printf '<!-- phase:start ... -->'`, and the extension may sight the same
// marker more than once in one tool call (command echo vs tool_result
// stdout). Only the immediately preceding record is compared — and only
// while it is still running and recent — so a later legitimate re-emission
// of the phase appends normally. Naive global dedupe would be wrong: stage
// retries re-emit markers for phases that genuinely run again.
func (rs *RuntimeState) BeginPhase(stage PipelineStage, name string, index, total int) {
	rs.mu.Lock()
	defer rs.mu.Unlock()
	if n := len(rs.PhaseHistory); n > 0 {
		last := rs.PhaseHistory[n-1]
		if last.Stage == stage && last.Name == name && last.Index == index &&
			last.Status == "running" && time.Since(last.StartedAt) < phaseStartDedupeWindow {
			return
		}
	}
	rs.PhaseHistory = append(rs.PhaseHistory, PhaseRecord{
		Stage:     stage,
		Name:      name,
		Index:     index,
		Total:     total,
		Status:    "running",
		StartedAt: time.Now(),
	})
}

// CompletePhase marks the last running phase with the given name as complete.
func (rs *RuntimeState) CompletePhase(stage PipelineStage, name string) {
	rs.mu.Lock()
	defer rs.mu.Unlock()
	// Walk backwards to find the most recent running phase matching name+stage.
	for i := len(rs.PhaseHistory) - 1; i >= 0; i-- {
		p := &rs.PhaseHistory[i]
		if p.Stage == stage && p.Name == name && p.Status == "running" {
			now := time.Now()
			p.Status = "complete"
			p.CompletedAt = &now
			return
		}
	}
}

// SetLicenseSnapshot records the license validation result from pipeline
// preflight or a later mid-run re-validation (#4156).
func (rs *RuntimeState) SetLicenseSnapshot(tier string, allowed bool, status string, cacheUntil time.Time) {
	rs.mu.Lock()
	defer rs.mu.Unlock()
	rs.License = &LicenseSnapshot{
		Tier:       tier,
		Allowed:    allowed,
		Status:     status,
		CacheUntil: cacheUntil,
	}
}

// IsLicenseExpired reports whether the license snapshot indicates the license
// has expired. Returns false when no snapshot is set or CacheUntil is zero
// (community tier — no expiry).
func (rs *RuntimeState) IsLicenseExpired() bool {
	rs.mu.Lock()
	defer rs.mu.Unlock()
	if rs.License == nil || rs.License.CacheUntil.IsZero() {
		return false
	}
	return time.Now().After(rs.License.CacheUntil)
}

// SetBranch records the feature branch name (populated after issue-pickup).
func (rs *RuntimeState) SetBranch(branch string) {
	rs.mu.Lock()
	defer rs.mu.Unlock()
	rs.Branch = branch
}

// SetGateResults stores quality gate results (populated after feature-validate).
func (rs *RuntimeState) SetGateResults(results []GateResult) {
	rs.mu.Lock()
	defer rs.mu.Unlock()
	rs.GateResults = results
}

// SetPrUrl records the pull request URL (populated after pr-create).
func (rs *RuntimeState) SetPrUrl(url string) {
	rs.mu.Lock()
	defer rs.mu.Unlock()
	rs.PrUrl = url
}

// SetMergeOutcome records the post-merge ground-truth breadcrumb (#4133): the
// merge commit SHA and ISO-8601 merge timestamp captured by the post-merge
// hook. Empty values are ignored so a non-blocking breadcrumb-fetch failure
// never overwrites a previously captured SHA with "".
func (rs *RuntimeState) SetMergeOutcome(sha, mergedAt string) {
	rs.mu.Lock()
	defer rs.mu.Unlock()
	if sha != "" {
		rs.MergedCommitSha = sha
	}
	if mergedAt != "" {
		rs.MergedAt = mergedAt
	}
}

// SetLicenseExpiredMidRun sets the flag that indicates the license expired
// during a running pipeline. Non-blocking — allows the current run to finish.
func (rs *RuntimeState) SetLicenseExpiredMidRun(expired bool) {
	rs.mu.Lock()
	defer rs.mu.Unlock()
	rs.LicenseExpiredMidRun = expired
}

// HasLicenseExpiredMidRun reports whether the license expired during this run.
func (rs *RuntimeState) HasLicenseExpiredMidRun() bool {
	rs.mu.Lock()
	defer rs.mu.Unlock()
	return rs.LicenseExpiredMidRun
}

// SetPaused sets the paused flag on the runtime state (thread-safe).
func (rs *RuntimeState) SetPaused(paused bool) {
	rs.mu.Lock()
	defer rs.mu.Unlock()
	rs.Paused = paused
}

// SetStageError records an error message for a stage.
func (rs *RuntimeState) SetStageError(stage PipelineStage, errMsg string) {
	rs.mu.Lock()
	defer rs.mu.Unlock()
	rs.StageErrors[string(stage)] = errMsg
}

// RecordStageOutputTail stores the last lines of a stage's subagent output for
// later inclusion in a terminal-failure RunRecord (Issue #3001). Trims to the
// last StageOutputBufferLineLimit lines and StageOutputBufferByteCap bytes so a
// single pathological log line cannot blow up memory or the JSONL file.
func (rs *RuntimeState) RecordStageOutputTail(stage PipelineStage, raw string) {
	if raw == "" {
		return
	}
	tail := truncateOutputTail(raw)
	rs.mu.Lock()
	defer rs.mu.Unlock()
	if rs.StageOutputTails == nil {
		rs.StageOutputTails = make(map[string]string)
	}
	rs.StageOutputTails[string(stage)] = tail
}

// RecordStageMode records the performance mode active at the start of a stage
// (Issue #3215). Called by the scheduler immediately after BeginStage so the
// captured value reflects mode resolution at stage entry — subsequent mid-run
// toggles do not retroactively change it. Empty mode strings are ignored to
// keep the JSONL output free of "" stubs.
func (rs *RuntimeState) RecordStageMode(stage PipelineStage, mode string) {
	if mode == "" {
		return
	}
	rs.mu.Lock()
	defer rs.mu.Unlock()
	if rs.StageModes == nil {
		rs.StageModes = make(map[string]string)
	}
	rs.StageModes[string(stage)] = mode
}

// StageMode returns the recorded mode for a stage, or "" when absent.
func (rs *RuntimeState) StageMode(stage PipelineStage) string {
	rs.mu.Lock()
	defer rs.mu.Unlock()
	if rs.StageModes == nil {
		return ""
	}
	return rs.StageModes[string(stage)]
}

// RecordStageAdapter records the adapter that executed a stage (Issue #3224).
// Mirrors RecordStageMode: called by the scheduler at stage start with the
// resolved adapter id. Empty adapter strings are ignored to keep the JSONL
// output free of "" stubs and to preserve the omitempty contract on the wire.
func (rs *RuntimeState) RecordStageAdapter(stage PipelineStage, adapter string) {
	if adapter == "" {
		return
	}
	rs.mu.Lock()
	defer rs.mu.Unlock()
	if rs.StageAdapters == nil {
		rs.StageAdapters = make(map[string]string)
	}
	rs.StageAdapters[string(stage)] = adapter
}

// StageAdapter returns the recorded adapter for a stage, or "" when absent.
func (rs *RuntimeState) StageAdapter(stage PipelineStage) string {
	rs.mu.Lock()
	defer rs.mu.Unlock()
	if rs.StageAdapters == nil {
		return ""
	}
	return rs.StageAdapters[string(stage)]
}

// ModelRefusalFallback records one CLI-internal model swap observed in a
// stage's stream (#91): the claude CLI's system/model_refusal_fallback event.
type ModelRefusalFallback struct {
	Stage           string `json:"stage"`
	OriginalModel   string `json:"original_model"`
	FallbackModel   string `json:"fallback_model"`
	RefusalCategory string `json:"refusal_category,omitempty"`
}

// RecordModelRefusalFallback appends a CLI refusal fallback observed during a
// stage (#91). FallbackModel is required; empty appends are ignored.
func (rs *RuntimeState) RecordModelRefusalFallback(stage PipelineStage, original, fallback, category string) {
	if fallback == "" {
		return
	}
	rs.mu.Lock()
	defer rs.mu.Unlock()
	rs.ModelRefusalFallbacks = append(rs.ModelRefusalFallbacks, ModelRefusalFallback{
		Stage:           string(stage),
		OriginalModel:   original,
		FallbackModel:   fallback,
		RefusalCategory: category,
	})
}

// LastRefusalServedModel returns the fallback model of the most recent CLI
// refusal fallback, or "" when none was observed. Run-level consumers (the
// learning outcome's ActualModel) use this as the served model when the CLI
// swapped mid-run (#91).
func (rs *RuntimeState) LastRefusalServedModel() string {
	rs.mu.Lock()
	defer rs.mu.Unlock()
	if len(rs.ModelRefusalFallbacks) == 0 {
		return ""
	}
	return rs.ModelRefusalFallbacks[len(rs.ModelRefusalFallbacks)-1].FallbackModel
}

// RecordStageModel records the model that actually executes a stage (Issue
// #42). Mirrors RecordStageAdapter: called by the scheduler at stage dispatch
// with the fully-resolved model (after escalation overrides and
// model-unavailable downgrades). Empty model strings are ignored to preserve
// the omitempty contract on the wire.
func (rs *RuntimeState) RecordStageModel(stage PipelineStage, model string) {
	if model == "" {
		return
	}
	rs.mu.Lock()
	defer rs.mu.Unlock()
	if rs.StageModels == nil {
		rs.StageModels = make(map[string]string)
	}
	rs.StageModels[string(stage)] = model
}

// StageModel returns the recorded model for a stage, or "" when absent.
func (rs *RuntimeState) StageModel(stage PipelineStage) string {
	rs.mu.Lock()
	defer rs.mu.Unlock()
	if rs.StageModels == nil {
		return ""
	}
	return rs.StageModels[string(stage)]
}

// AppendEscalation records a model-change event (upward escalation or
// model-unavailable downgrade, distinguished by Reason) on the run's
// EscalationHistory. Issue #42 added the first writer for this field — it
// existed in the schema but was never populated, so AttemptsUntilSuccess
// accounting derived from it was always zero.
func (rs *RuntimeState) AppendEscalation(rec EscalationRecord) {
	rs.mu.Lock()
	defer rs.mu.Unlock()
	rs.EscalationHistory = append(rs.EscalationHistory, rec)
}

// RecordExecutionPath records the execution path used for a stage (Issue
// #3264). Mirrors RecordStageMode/RecordStageAdapter: called by the scheduler
// when the deterministic-first pr-merge hook decides whether to run the
// deterministic Go path or fall through to the LLM skill. Empty path strings
// are ignored to keep the JSONL output free of "" stubs and to preserve the
// omitempty contract on the wire — callers always pass "deterministic" or
// "llm" explicitly.
func (rs *RuntimeState) RecordExecutionPath(stage PipelineStage, path string) {
	if path == "" {
		return
	}
	rs.mu.Lock()
	defer rs.mu.Unlock()
	if rs.StageExecutionPaths == nil {
		rs.StageExecutionPaths = make(map[string]string)
	}
	rs.StageExecutionPaths[string(stage)] = path
}

// StageExecutionPath returns the recorded execution path for a stage, or ""
// when absent (Issue #3264).
func (rs *RuntimeState) StageExecutionPath(stage PipelineStage) string {
	rs.mu.Lock()
	defer rs.mu.Unlock()
	if rs.StageExecutionPaths == nil {
		return ""
	}
	return rs.StageExecutionPaths[string(stage)]
}

// RecordStagePuntReason records the machine-readable reason a deterministic-first
// hook declined and fell through to the LLM path (Issue #297). Paired with
// RecordExecutionPath(stage, "llm") so the history record answers both WHICH
// path ran and WHY the deterministic one was skipped. Empty reasons are ignored
// to preserve the omitempty contract — callers pass the runner's reason code.
func (rs *RuntimeState) RecordStagePuntReason(stage PipelineStage, reason string) {
	if reason == "" {
		return
	}
	rs.mu.Lock()
	defer rs.mu.Unlock()
	if rs.StagePuntReasons == nil {
		rs.StagePuntReasons = make(map[string]string)
	}
	rs.StagePuntReasons[string(stage)] = reason
}

// StagePuntReason returns the recorded deterministic-path punt reason for a
// stage, or "" when absent (Issue #297).
func (rs *RuntimeState) StagePuntReason(stage PipelineStage) string {
	rs.mu.Lock()
	defer rs.mu.Unlock()
	if rs.StagePuntReasons == nil {
		return ""
	}
	return rs.StagePuntReasons[string(stage)]
}

// AppendStageGateResult records a stage post-condition gate outcome (Issue
// #3266). Multiple results per stage are supported but the registry only
// runs one gate per stage today.
func (rs *RuntimeState) AppendStageGateResult(stage PipelineStage, result StageGateResult) {
	rs.mu.Lock()
	defer rs.mu.Unlock()
	if rs.StageGateResults == nil {
		rs.StageGateResults = make(map[string][]StageGateResult)
	}
	key := string(stage)
	rs.StageGateResults[key] = append(rs.StageGateResults[key], result)
}

// AppendStageAnomaly records an anomaly observed during stage execution
// (Issue #3267). Multiple anomalies per stage are supported (a stage could
// trip more than one detector in the future).
func (rs *RuntimeState) AppendStageAnomaly(stage PipelineStage, anomaly Anomaly) {
	rs.mu.Lock()
	defer rs.mu.Unlock()
	if rs.StageAnomalies == nil {
		rs.StageAnomalies = make(map[string][]Anomaly)
	}
	key := string(stage)
	rs.StageAnomalies[key] = append(rs.StageAnomalies[key], anomaly)
}

// StageAnomaliesFor returns a copy of the recorded anomalies for a stage.
func (rs *RuntimeState) StageAnomaliesFor(stage PipelineStage) []Anomaly {
	rs.mu.Lock()
	defer rs.mu.Unlock()
	if rs.StageAnomalies == nil {
		return nil
	}
	src := rs.StageAnomalies[string(stage)]
	if len(src) == 0 {
		return nil
	}
	out := make([]Anomaly, len(src))
	copy(out, src)
	return out
}

// AppendRecoveryAttempt records a FailureRecovery registry outcome for a
// stage (Issue #3268). Multiple attempts per stage are supported — the
// registry's per-run cap bounds the total across all stages, not per-stage.
func (rs *RuntimeState) AppendRecoveryAttempt(stage PipelineStage, attempt RecoveryAttempt) {
	rs.mu.Lock()
	defer rs.mu.Unlock()
	if rs.StageRecoveryAttempts == nil {
		rs.StageRecoveryAttempts = make(map[string][]RecoveryAttempt)
	}
	key := string(stage)
	rs.StageRecoveryAttempts[key] = append(rs.StageRecoveryAttempts[key], attempt)
}

// StageRecoveryAttemptsFor returns a copy of the recorded recovery attempts
// for a stage (Issue #3268).
func (rs *RuntimeState) StageRecoveryAttemptsFor(stage PipelineStage) []RecoveryAttempt {
	rs.mu.Lock()
	defer rs.mu.Unlock()
	if rs.StageRecoveryAttempts == nil {
		return nil
	}
	src := rs.StageRecoveryAttempts[string(stage)]
	if len(src) == 0 {
		return nil
	}
	out := make([]RecoveryAttempt, len(src))
	copy(out, src)
	return out
}

// StageGateResultsFor returns a copy of the recorded gate results for a stage.
func (rs *RuntimeState) StageGateResultsFor(stage PipelineStage) []StageGateResult {
	rs.mu.Lock()
	defer rs.mu.Unlock()
	if rs.StageGateResults == nil {
		return nil
	}
	src := rs.StageGateResults[string(stage)]
	if len(src) == 0 {
		return nil
	}
	out := make([]StageGateResult, len(src))
	copy(out, src)
	return out
}

// StageOutputTail returns the captured tail for a stage, or "" when absent.
func (rs *RuntimeState) StageOutputTail(stage PipelineStage) string {
	rs.mu.Lock()
	defer rs.mu.Unlock()
	if rs.StageOutputTails == nil {
		return ""
	}
	return rs.StageOutputTails[string(stage)]
}

// truncateOutputTail keeps only the last StageOutputBufferLineLimit lines and
// caps the total byte length at StageOutputBufferByteCap. The byte cap wins —
// even a single very long line is sliced from its tail.
func truncateOutputTail(raw string) string {
	if len(raw) > StageOutputBufferByteCap {
		raw = raw[len(raw)-StageOutputBufferByteCap:]
	}
	// Count lines and slice from the back.
	newlineCount := 0
	cut := 0
	for i := len(raw) - 1; i >= 0; i-- {
		if raw[i] == '\n' {
			newlineCount++
			if newlineCount > StageOutputBufferLineLimit {
				cut = i + 1
				break
			}
		}
	}
	if cut > 0 {
		raw = raw[cut:]
	}
	return raw
}

// Persist writes the current state atomically to disk.
// The file is written to {stateDir}/runtime-{issueNumber}.json.
//
// Uses the atomic+fsync write contract from internal/runstate so that a
// reader observes either the prior version or the new version, never partial
// JSON — even on power loss between rename and the next disk flush.
func (rs *RuntimeState) Persist(stateDir string) error {
	rs.mu.Lock()
	snap := rs.snapshotLocked()
	rs.mu.Unlock()

	if err := os.MkdirAll(stateDir, 0755); err != nil {
		return fmt.Errorf("create state dir: %w", err)
	}

	data, err := json.Marshal(snap)
	if err != nil {
		return fmt.Errorf("marshal state: %w", err)
	}

	target := filepath.Join(stateDir, fmt.Sprintf("runtime-%d.json", snap.IssueNumber))
	return AtomicWriteFile(target, data, 0644)
}

// AtomicWriteFile writes data to target using the durable write contract:
// write-temp → fsync(file) → rename → fsync(parent dir). Directory fsync is
// best-effort (no-op on macOS / Windows / certain FUSE mounts).
//
// Exposed so callers across internal/* can share one durability primitive
// without each re-deriving the temp+rename pattern.
func AtomicWriteFile(target string, data []byte, perm os.FileMode) error {
	tmp := target + ".tmp"
	f, err := os.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, perm)
	if err != nil {
		return fmt.Errorf("open tmp: %w", err)
	}
	if _, err := f.Write(data); err != nil {
		f.Close()
		os.Remove(tmp)
		return fmt.Errorf("write tmp: %w", err)
	}
	if err := f.Sync(); err != nil {
		f.Close()
		os.Remove(tmp)
		return fmt.Errorf("fsync tmp: %w", err)
	}
	if err := f.Close(); err != nil {
		os.Remove(tmp)
		return fmt.Errorf("close tmp: %w", err)
	}
	if err := os.Rename(tmp, target); err != nil {
		os.Remove(tmp)
		return fmt.Errorf("rename: %w", err)
	}
	// Best-effort directory fsync — ignored on platforms that disallow it.
	if dir, err := os.Open(filepath.Dir(target)); err == nil {
		_ = dir.Sync()
		_ = dir.Close()
	}
	return nil
}

// LoadPersistedState reads a persisted runtime state from disk.
func LoadPersistedState(stateDir string, issueNumber int) (*RuntimeState, error) {
	path := filepath.Join(stateDir, fmt.Sprintf("runtime-%d.json", issueNumber))
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var rs RuntimeState
	if err := json.Unmarshal(data, &rs); err != nil {
		return nil, fmt.Errorf("unmarshal state: %w", err)
	}
	return &rs, nil
}

// TotalDuration returns the elapsed time since pipeline start.
func (rs *RuntimeState) TotalDuration() time.Duration {
	return time.Since(rs.StartedAt)
}

// IsComplete returns true if all 6 stages are completed or skipped.
func (rs *RuntimeState) IsComplete() bool {
	rs.mu.Lock()
	defer rs.mu.Unlock()
	return len(rs.CompletedStages)+len(rs.SkippedStages) >= 6
}

// Snapshot returns a copy of the runtime state (safe for concurrent reads).
// The returned copy has its own mutex and is safe to use independently.
func (rs *RuntimeState) Snapshot() *RuntimeState {
	rs.mu.Lock()
	defer rs.mu.Unlock()
	return rs.snapshotLocked()
}

// snapshotLocked creates a deep copy while the caller already holds rs.mu.
func (rs *RuntimeState) snapshotLocked() *RuntimeState {
	snap := &RuntimeState{
		Repo:                     rs.Repo,
		IssueNumber:              rs.IssueNumber,
		ItemID:                   rs.ItemID,
		Title:                    rs.Title,
		Body:                     rs.Body,
		Branch:                   rs.Branch,
		RunID:                    rs.RunID,
		Stage:                    rs.Stage,
		StartedAt:                rs.StartedAt,
		StageStart:               rs.StageStart,
		PID:                      rs.PID,
		WorktreeDir:              rs.WorktreeDir,
		AuthoritativeChangeClass: rs.AuthoritativeChangeClass,
		InputTokens:              rs.InputTokens,
		OutputTokens:             rs.OutputTokens,
		TotalCostUSD:             rs.TotalCostUSD,
		Paused:                   rs.Paused,
		LicenseExpiredMidRun:     rs.LicenseExpiredMidRun,
		PrUrl:                    rs.PrUrl,
		MergedCommitSha:          rs.MergedCommitSha,
		MergedAt:                 rs.MergedAt,
	}
	if rs.License != nil {
		licenseCopy := *rs.License
		snap.License = &licenseCopy
	}
	snap.CompletedStages = make([]StageResult, len(rs.CompletedStages))
	copy(snap.CompletedStages, rs.CompletedStages)
	snap.SkippedStages = make([]string, len(rs.SkippedStages))
	copy(snap.SkippedStages, rs.SkippedStages)
	snap.PhaseHistory = make([]PhaseRecord, len(rs.PhaseHistory))
	copy(snap.PhaseHistory, rs.PhaseHistory)
	snap.StageErrors = make(map[string]string, len(rs.StageErrors))
	for k, v := range rs.StageErrors {
		snap.StageErrors[k] = v
	}
	snap.RetryCount = rs.RetryCount
	snap.EscalationHistory = make([]EscalationRecord, len(rs.EscalationHistory))
	copy(snap.EscalationHistory, rs.EscalationHistory)
	if rs.RalphIterations != nil {
		snap.RalphIterations = make(map[string]int, len(rs.RalphIterations))
		for k, v := range rs.RalphIterations {
			snap.RalphIterations[k] = v
		}
	}
	if len(rs.GateResults) > 0 {
		snap.GateResults = make([]GateResult, len(rs.GateResults))
		copy(snap.GateResults, rs.GateResults)
	}
	if len(rs.StageOutputTails) > 0 {
		snap.StageOutputTails = make(map[string]string, len(rs.StageOutputTails))
		for k, v := range rs.StageOutputTails {
			snap.StageOutputTails[k] = v
		}
	}
	if len(rs.StageModes) > 0 {
		snap.StageModes = make(map[string]string, len(rs.StageModes))
		for k, v := range rs.StageModes {
			snap.StageModes[k] = v
		}
	}
	if len(rs.StageModels) > 0 {
		snap.StageModels = make(map[string]string, len(rs.StageModels))
		for k, v := range rs.StageModels {
			snap.StageModels[k] = v
		}
	}
	if len(rs.ModelRefusalFallbacks) > 0 {
		snap.ModelRefusalFallbacks = make([]ModelRefusalFallback, len(rs.ModelRefusalFallbacks))
		copy(snap.ModelRefusalFallbacks, rs.ModelRefusalFallbacks)
	}
	if len(rs.StageAdapters) > 0 {
		snap.StageAdapters = make(map[string]string, len(rs.StageAdapters))
		for k, v := range rs.StageAdapters {
			snap.StageAdapters[k] = v
		}
	}
	if len(rs.StageExecutionPaths) > 0 {
		snap.StageExecutionPaths = make(map[string]string, len(rs.StageExecutionPaths))
		for k, v := range rs.StageExecutionPaths {
			snap.StageExecutionPaths[k] = v
		}
	}
	if len(rs.StagePuntReasons) > 0 {
		snap.StagePuntReasons = make(map[string]string, len(rs.StagePuntReasons))
		for k, v := range rs.StagePuntReasons {
			snap.StagePuntReasons[k] = v
		}
	}
	if len(rs.StageGateResults) > 0 {
		snap.StageGateResults = make(map[string][]StageGateResult, len(rs.StageGateResults))
		for k, v := range rs.StageGateResults {
			copied := make([]StageGateResult, len(v))
			copy(copied, v)
			snap.StageGateResults[k] = copied
		}
	}
	if len(rs.StageAnomalies) > 0 {
		snap.StageAnomalies = make(map[string][]Anomaly, len(rs.StageAnomalies))
		for k, v := range rs.StageAnomalies {
			copied := make([]Anomaly, len(v))
			copy(copied, v)
			snap.StageAnomalies[k] = copied
		}
	}
	if len(rs.StageRecoveryAttempts) > 0 {
		snap.StageRecoveryAttempts = make(map[string][]RecoveryAttempt, len(rs.StageRecoveryAttempts))
		for k, v := range rs.StageRecoveryAttempts {
			copied := make([]RecoveryAttempt, len(v))
			copy(copied, v)
			snap.StageRecoveryAttempts[k] = copied
		}
	}
	return snap
}
