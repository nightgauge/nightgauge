package state

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/nightgauge/nightgauge/internal/diagnostics"
	"github.com/nightgauge/nightgauge/internal/intelligence/tokens"
	"github.com/nightgauge/nightgauge/internal/runstate"
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
	// RunID is the run identity (canonical lowercase UUIDv7, runstate.NewRunID).
	// IMMUTABLE AFTER CONSTRUCTION — set only by NewRuntimeState, never by a
	// setter (ADR-017 Decision 1). An immutable field written once before the
	// value is shared with any goroutine needs no lock, which is what dissolves
	// the two-locks-one-comparator hazard (F20).
	//
	// The tag carries NO omitempty (ADR-017 step 1): a new-scheme file always
	// carries the key, so a legacy file (key absent) stays distinguishable from
	// a corrupt one (key present but empty).
	RunID string `json:"runId"`

	// Terminal / TerminalAt / TerminalOutcome are the DURABLE half of the
	// terminal latch (ADR-017 Decision 5). The registry's runEntry.terminal is
	// admission control for new resolutions and dies with the process; these
	// travel with the object, are marshalled into every byte the run writes,
	// and are what a fresh process, the gate CLI and the reconciler read.
	// Written under rs.mu by markTerminalLocked (claim step 1c) — never cleared.
	Terminal        bool       `json:"terminal,omitempty"`
	TerminalAt      *time.Time `json:"terminalAt,omitempty"`
	TerminalOutcome string     `json:"terminalOutcome,omitempty"`

	// Abandoned / AbandonedAt record that the force-clear funnel gave up on the
	// DISPATCH (ADR-017 7.1). The run itself may still be alive and streaming,
	// which is why abandonment is not terminality and why there is deliberately
	// no ClearAbandoned: no transition, phase or progress call resets it.
	// Written under rs.mu by markAbandonedLocked.
	Abandoned       bool       `json:"abandoned,omitempty"`
	AbandonedAt     *time.Time `json:"abandonedAt,omitempty"`
	AbandonedReason string     `json:"abandonedReason,omitempty"`

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
	PhaseHistory []PhaseRecord `json:"phaseHistory"`
	// StageErrors is CURRENT-ATTEMPT state, not a run-long failure log (#407).
	// CONTRACT: a stage has an entry here ⇔ that stage's MOST RECENT attempt
	// failed, OR the pipeline refused before that stage could attempt anything.
	// The refusal writers are real and record entries for stages that never
	// dispatched at all — internal/orchestrator/scheduler.go's pipeline-start
	// preflights, budget-ceiling terminate, branch-fork preflight and mid-run
	// license halt — so "an entry means an attempt happened" would be false.
	// (Gate refusals such as the pr-merge merge verification are the ordinary
	// case: the stage ran and the gate is what failed it.)
	//
	// SetStageError writes on failure; completeStageInternal deletes when — and
	// only when — a completion for the stage both SUCCEEDS (exit 0) and is
	// actually booked (not suppressed by the #230 idempotency guard). A stage
	// that failed and then succeeded on retry therefore has NO entry, and both
	// TS appliers may keep applying stageErrors after completedStages — with
	// this contract, a stage in both maps is the legitimate backtrack case
	// (completed earlier, re-run later, failed) and "most recent attempt
	// failed" is what must win.
	//
	// Rehydration deliberately preserves whatever was on disk: a crash snapshot
	// for a stage that later completes successfully is cleared by that
	// completion, and a terminal failure's stage never completes, so its entry
	// survives.
	StageErrors map[string]string `json:"stageErrors"` // stage → most recent attempt's error message

	// Pause state (persisted to runtime-{issue}-{runId}.json for reload recovery)
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

	// ToolCalls accumulates the bounded all-tools call log forwarded from
	// each stage's StageRunResult (Issue #144), in stage-completion order.
	// Not persisted with the rest of RuntimeState — copied onto the V2
	// history record's ToolCalls field at run completion via Snapshot().
	ToolCalls []diagnostics.ToolCallRecord `json:"toolCalls,omitempty"`

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

	// TerminatingStageTokens captures the ground-truth token/cost data for a
	// stage that failed before reaching CompleteStage/CompleteStageWithCost
	// (Issue #146). Keyed by stage name, set from the same
	// inputTokens/outputTokens/cacheReadTokens/actualCostUsd locals already
	// passed to writeStageExitRecord on the failure path. BuildV2Record's
	// failed-stage synthesis branch reads this map to populate the missing
	// tokens.per_stage entry for a stage that is present in `stages` but
	// absent from CompletedStages, mirroring StageOutputTails/ToolCalls
	// (#3001, #144) — data recorded independently of CompletedStages so it
	// survives the stage never completing normally.
	TerminatingStageTokens map[string]StageResult `json:"terminatingStageTokens,omitempty"`

	// sealed is the IN-MEMORY half of the terminal latch (ADR-017 Decision 5,
	// claim step 4). Set by SealAndRemove once the terminal-stamped snapshot has
	// been written and removed; from then on every Persist returns ErrRunSealed
	// WITHOUT writing, so a transition's in-flight Persist can never re-create
	// the snapshot the claim just deleted (F27). Never serialised — the durable
	// equivalent is the Terminal field the seal just wrote. Read by
	// persistLocked only, inside its own rs.mu critical section, so the check
	// and the write cannot be separated by a scheduler.
	sealed bool
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
//
// runID is the run identity (ADR-017 Decision 1). It is a CONSTRUCTOR
// ARGUMENT, not a settable field: RunID is immutable after construction and
// there is no setter. The constructor stores whatever it is given and does NOT
// validate — the refusal lives in Persist, which will not write a state it
// cannot name. That split is deliberate: identity-less construction stays legal
// for the two sites that genuinely have no identity to offer (both of which are
// unpersistable by design, and both of which ADR-017 step 4 deletes), while
// nothing identity-less can ever reach disk.
func NewRuntimeState(repo string, issueNumber int, itemID, runID string) *RuntimeState {
	return &RuntimeState{
		Repo:        repo,
		IssueNumber: issueNumber,
		ItemID:      itemID,
		RunID:       runID,
		StartedAt:   time.Now(),
		StageErrors: make(map[string]string),
	}
}

// MarkTerminal latches the durable half of the terminal latch (ADR-017
// Decision 5, claim step 1c): the run has reached its terminal event and will
// accept no further mutations. outcome is the terminal outcome string recorded
// alongside the marker; it is advisory and may be empty.
//
// The latch is one-way. There is no ClearTerminal, and Persist keeps writing
// until SealAndRemove sets the in-memory `sealed` flag — a Persist that lands
// between MarkTerminal and the seal marshals `terminal: true`, so even the
// snapshot it writes is one that adoption refuses and the reconciler removes
// without emitting. A resurrection that cannot rehydrate is not a resurrection.
func (rs *RuntimeState) MarkTerminal(outcome string) {
	rs.mu.Lock()
	defer rs.mu.Unlock()
	rs.markTerminalLocked(outcome)
}

// ClaimTerminal is claim steps 1b–1d of ADR-017 Decision 5 as ONE critical
// section under rs.mu, and it exists because rs.mu is unexported: the claim
// lives in internal/ipc, which cannot hold this mutex itself, so the three
// mutations the sequence declares to be atomic have to be composed here or they
// are not atomic at all.
//
//	1b. replay the dispatcher's terminal payload (#309) — these are the LAST
//	    mutations the run will ever accept, and they run INSIDE the claim rather
//	    than before it: latching first would refuse them (silently dropping
//	    execution_path / punt_reason from every extension-path history record),
//	    and running them before the claim but outside the lock reintroduces the
//	    unlocked resolve-then-mutate window Decision 5 exists to delete;
//	1c. latch the DURABLE half of the terminal latch (the registry's
//	    runEntry.terminal is the caller's to set, in the same runtimesMu hold);
//	1d. snapshot, so the seconds of unlocked work that follow run against a copy
//	    and never against the live pointer.
//
// The caller holds runtimesMu across this call — lock order runtimesMu → rs.mu,
// never the reverse. This is the one exported RuntimeState method the claim
// invokes under runtimesMu, and it is sound for the reason C16's rule is really
// about: it ACQUIRES rs.mu once from a caller that holds no rs.mu, so there is
// no re-entry and no cycle.
//
// Both maps are keyed by stage name and may be nil; the …Locked recorders ignore
// empty values, so an absent map is a no-op.
//
// outcomeFor derives the terminal outcome string from the run's terminating
// stage. It is a callback rather than a plain string because the caller's
// outcome depends on that stage (#266's pr-merge ground truth) and reading the
// stage before the claim would put a resolve-then-mutate window back exactly
// where Decision 5 removed one. It must be pure and must not touch this
// RuntimeState — it runs inside rs.mu. nil records an empty outcome.
func (rs *RuntimeState) ClaimTerminal(executionPaths, puntReasons map[string]string, outcomeFor func(stage PipelineStage) string) *RuntimeState {
	rs.mu.Lock()
	defer rs.mu.Unlock()
	for stg, path := range executionPaths {
		rs.recordExecutionPathLocked(PipelineStage(stg), path)
	}
	for stg, reason := range puntReasons {
		rs.recordStagePuntReasonLocked(PipelineStage(stg), reason)
	}
	outcome := ""
	if outcomeFor != nil {
		outcome = outcomeFor(rs.Stage)
	}
	rs.markTerminalLocked(outcome)
	return rs.snapshotLocked()
}

// markTerminalLocked is MarkTerminal's body for callers that already hold rs.mu
// — the claim sequence's step 1c calls this form, because sync.Mutex is not
// reentrant (C16, F36).
func (rs *RuntimeState) markTerminalLocked(outcome string) {
	if rs.Terminal {
		return
	}
	now := time.Now().UTC()
	rs.Terminal = true
	rs.TerminalAt = &now
	rs.TerminalOutcome = outcome
}

// MarkAbandoned stamps the durable abandonment marker (ADR-017 7.1): the
// force-clear funnel gave up on this DISPATCH. The run may still be alive and
// streaming, so this is emphatically not terminality — the reconciler needs
// TWO independent conditions (abandoned AND outside the liveness window) before
// it removes such a snapshot, and a fresh abandoned run keeps its crash
// snapshot (the F4 correction).
//
// There is no ClearAbandoned by design: no transition, phase or progress call
// resets the flag, so a live abandoned run's next Persist cannot erase it.
func (rs *RuntimeState) MarkAbandoned(at time.Time, reason string) {
	rs.mu.Lock()
	defer rs.mu.Unlock()
	rs.markAbandonedLocked(at, reason)
}

// markAbandonedLocked is MarkAbandoned's body for callers already holding rs.mu.
func (rs *RuntimeState) markAbandonedLocked(at time.Time, reason string) {
	if rs.Abandoned {
		return
	}
	stamp := at.UTC()
	rs.Abandoned = true
	rs.AbandonedAt = &stamp
	rs.AbandonedReason = reason
}

// SetStageChild records the PID of the stage's child process (ADR-017 7.2).
//
// Wired in step 5: pipeline.notifyStageTransition feeds it the wire's stagePid
// on every extension-path transition. Deliberately NOT SetProcess, which also
// writes WorktreeDir and belongs to the scheduler path — this is the extension
// path's one-field setter. A stage's terminal transition sends 0, so a finished
// child cannot vouch for the run and the PID-reuse window is bounded by one
// stage rather than by the whole run.
//
// The value reaches disk through the transition handler's existing Persist — no
// new persist site is introduced, which is what makes the liveness ladder's
// arm 3 cheap.
func (rs *RuntimeState) SetStageChild(pid int) {
	rs.mu.Lock()
	defer rs.mu.Unlock()
	rs.setStageChildLocked(pid)
}

// setStageChildLocked is SetStageChild's body for callers already holding rs.mu.
func (rs *RuntimeState) setStageChildLocked(pid int) {
	rs.PID = pid
}

// StageChildPID reads the recorded pid under rs.mu — ladder arm 3 (ADR-017 7.2)
// asking a LIVE runtime whether its stage child is still there. A lock-safe
// single-field read rather than Snapshot(), for TargetRepo's reason: deep-copying
// every stage record to read one int is a cost that scales with the run.
//
// The reconciler's own arm 3 reads PID off a snapshot it loaded from disk, where
// no mutex applies; this is the registry-side reader.
func (rs *RuntimeState) StageChildPID() int {
	rs.mu.Lock()
	defer rs.mu.Unlock()
	return rs.PID
}

// SeedRunContext fills the run's descriptive fields from a transition that
// carries them and returns the run's resulting Repo.
//
// Latest-wins for Branch; first-wins for Repo and Title, because the run's
// target repo is resolved asynchronously by the dispatcher and the first
// transition to carry it is the authority (#307's persist gate depends on it).
//
// It exists so the IPC transition handler stops writing these fields directly:
// they are run CONTENT, owned by rs.mu (ADR-017 Decision 12), and the handler
// held the REGISTRY's mutex while writing them — a torn read against any
// concurrent snapshotLocked/persistLocked, on fields that end up in every byte
// the run writes.
func (rs *RuntimeState) SeedRunContext(repo, title, branch string) string {
	rs.mu.Lock()
	defer rs.mu.Unlock()
	if title != "" && rs.Title == "" {
		rs.Title = title
	}
	if branch != "" {
		rs.Branch = branch
	}
	if repo != "" && rs.Repo == "" {
		rs.Repo = repo
	}
	return rs.Repo
}

// TargetRepo returns the run's target repo under rs.mu, or "" before a
// repo-carrying transition has seeded one.
//
// It exists because SeedRunContext WRITES Repo under rs.mu (ADR-017 Decision
// 12: repo is run CONTENT), so every reader outside the constructor path must
// take the same mutex — the IPC transition handler and the progress handler run
// in separate goroutines and the race detector proves the unlocked read. A
// lock-safe single-field read rather than Snapshot(): the progress path calls it
// at >= 1 per 5s per run, and deep-copying every stage record to read one string
// is a cost that scales with the run (the same argument FeatureBranch makes).
func (rs *RuntimeState) TargetRepo() string {
	rs.mu.Lock()
	defer rs.mu.Unlock()
	return rs.Repo
}

// IsTerminal reports whether the durable terminal marker is latched.
func (rs *RuntimeState) IsTerminal() bool {
	rs.mu.Lock()
	defer rs.mu.Unlock()
	return rs.Terminal
}

// IsSealed reports whether SealAndRemove has run on this object. In-memory
// only — a fresh process that loads the same snapshot sees Terminal, not this.
func (rs *RuntimeState) IsSealed() bool {
	rs.mu.Lock()
	defer rs.mu.Unlock()
	return rs.sealed
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
//
// counts carries every billable pool (#358): taking input/output alone here
// while the caller's other cost path prices cache would produce two different
// costs for one stage. Cache-creation feeds PRICING only — it is deliberately
// not stored on StageResult; recording that count is #390's scope.
//
// counts.Input is NON-cached input (what CalculateCost prices at the base
// rate), but StageResult.InputTokens is the COMBINED count with CacheRead as a
// subset — readers subtract (history.go does `InputTokens - CacheRead`) and
// divide by it for the cache-hit rate. So the recorded input adds CacheRead
// back in, exactly as CompleteStageWithCost does.
func (rs *RuntimeState) CompleteStage(exitCode int, counts tokens.TokenCounts, model string) {
	cost := tokens.CalculateCost(model, counts)
	rs.completeStageInternal(exitCode, counts.Input+counts.CacheRead, counts.Output, counts.CacheRead, cost)
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

	// THE CLEAR SITE for the StageErrors contract (#407): an entry means "this
	// stage's MOST RECENT attempt failed" (or that the pipeline refused before
	// the stage could attempt at all), so a SUCCEEDING completion for the stage
	// retires the previous attempt's entry. Without a clear site StageErrors
	// was write-only in production — the only writer was SetStageError and
	// nothing ever removed a key — so a stage that failed and then succeeded on
	// retry sat in BOTH CompletedStages and StageErrors for the rest of the
	// run. Both TS snapshot appliers apply stageErrors AFTER completedStages,
	// so that stage rendered "failed" forever; countFailedStages saw it, and
	// outcomeDisplay downgraded a fully green run to "Complete — 1 stage
	// failed" plus a contradiction warning. history.go's V2 stage detail
	// stamped it "failed" in the durable record too.
	//
	// TWO conditions guard the delete, and both are load-bearing:
	//
	//   exitCode == 0 — a FAILING booking must clear nothing. The Go scheduler
	//   books every stage's exit at the top of its post-run block, then EMITS
	//   pipeline.stateChanged and PERSISTS the runtime to disk, and only after
	//   that reaches the branches that call SetStageError. An unconditional
	//   clear therefore broadcast and wrote to disk a retry whose second
	//   attempt had just failed as "complete, no error" — both TS appliers
	//   render that green and countFailedStages returns 0. With the gate, the
	//   previous attempt's entry survives that window until SetStageError
	//   overwrites it with the new attempt's text.
	//
	//   after the #230 guard — a completion the guard suppresses books nothing,
	//   so it must retire nothing. Clearing ahead of the guard laundered a
	//   stage whose only booked occurrence exited 1 into a "complete" durable
	//   record: BuildV2Record never reads StageResult.ExitCode, it stamps
	//   "complete" for every CompletedStages entry and only a StageErrors entry
	//   flips it to "failed" (internal/state/history.go, "Check for stage
	//   error").
	//
	// No call ORDER between a failure path's booking and its SetStageError is
	// load-bearing any more — a failing booking clears nothing, so neither
	// sequence can lose the error. Do not reintroduce that argument.
	//
	// SETTLED (#407): recovery is NOT a distinct UI state and gets no
	// StageResult field. A stage that failed and then succeeded renders as
	// plain success; the failure-then-recovery history already lives in the
	// retry-engine records, the outcome records, and the stage exit records.
	// Do not reintroduce a "recovered" status here.
	if exitCode == 0 {
		delete(rs.StageErrors, string(rs.Stage))
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

// SetAuthoritativeChangeClass records the post-dev change classification on the
// runtime so it survives worktree archival and is read back at record time.
func (rs *RuntimeState) SetAuthoritativeChangeClass(class string) {
	rs.mu.Lock()
	defer rs.mu.Unlock()
	rs.AuthoritativeChangeClass = class
}

// SetWorktree stamps the run's worktree as soon as it is provisioned — BEFORE
// the child process exists (#399).
//
// The worktree is knowable the moment `git worktree add` returns, but the only
// writer used to be SetProcess, which runs after cmd.Start() succeeds. Every
// error exit in the window between the two (model validation, stdin/stdout/
// stderr pipes, the spawn itself) therefore left WorktreeDir empty on a run
// whose worktree exists on disk, and the scheduler's stageWorkspace fell back
// to the workspace root — so failure-path work (branch resolution, gates,
// cleanup) looked at the wrong tree.
//
// Deliberately NOT SetProcess, which also writes PID (the SetStageChild
// precedent): a run that never spawned must not acquire a process identity as
// a side effect of learning where its worktree is. Nothing may read a
// non-empty WorktreeDir as evidence that a child started — PID is that
// evidence, and it stays 0 here.
//
// Idempotent: RunStage's post-start SetProcess re-stamps the same path.
func (rs *RuntimeState) SetWorktree(worktreeDir string) {
	rs.mu.Lock()
	defer rs.mu.Unlock()
	rs.WorktreeDir = worktreeDir
}

// SetProcess records the child process PID and worktree path.
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

// FeatureBranch returns the recorded feature branch, or "" before issue-pickup
// has resolved one. A lock-safe single-field read: the branch-fork pre-flight
// (#163) needs it once per stage, and taking a full Snapshot (a deep copy of
// every stage record) to read one string is a cost that scales with the run.
func (rs *RuntimeState) FeatureBranch() string {
	rs.mu.Lock()
	defer rs.mu.Unlock()
	return rs.Branch
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

// SetStageError records the error message for a stage's most recent attempt.
//
// One half of the StageErrors contract (#407): this is the only writer, and
// completeStageInternal is the only clear site — and it clears only when a
// completion for the stage both succeeds and is actually booked. An entry
// therefore means "this stage's LATEST attempt failed", or — for the writers
// that refuse before dispatch — "the pipeline refused before this stage could
// attempt anything". It never means "this stage failed at some point in the
// run".
//
// Callers that record a failure the pipeline is about to retry are correct to
// write here unconditionally: the retry's SUCCESSFUL completion retires the
// entry, and a retry that fails again overwrites it here with its own text.
// Callers on a terminal path (pipeline-start preflights, budget-ceiling
// terminate, branch-fork preflight, mid-run license halt, gate refusals,
// exhausted retries) write an entry that no completion will ever clear — and
// the first four write it for a stage that never dispatched at all — which is
// exactly right.
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

// RecordTerminatingStageTokens stores the ground-truth token/cost data for a
// stage on the failure path, so BuildV2Record's synthesis branch can populate
// tokens.per_stage even when the stage never reached CompleteStage (Issue
// #146). A multi-attempt run's most recent record for a stage wins.
func (rs *RuntimeState) RecordTerminatingStageTokens(stage PipelineStage, inputTokens, outputTokens, cacheReadTokens int, actualCostUsd float64) {
	rs.mu.Lock()
	defer rs.mu.Unlock()
	if rs.TerminatingStageTokens == nil {
		rs.TerminatingStageTokens = make(map[string]StageResult)
	}
	rs.TerminatingStageTokens[string(stage)] = StageResult{
		Stage:        stage,
		ExitCode:     -1,
		InputTokens:  inputTokens,
		OutputTokens: outputTokens,
		CacheRead:    cacheReadTokens,
		CostUSD:      actualCostUsd,
	}
}

// RecordToolCalls appends a stage's tool-call log onto the run-level
// accumulator (Issue #144). Entries missing a Stage tag are tagged with the
// given stage so the aggregated ToolCalls slice on the history record
// remains attributable even if the TS side omits it.
func (rs *RuntimeState) RecordToolCalls(stage PipelineStage, calls []diagnostics.ToolCallRecord) {
	if len(calls) == 0 {
		return
	}
	rs.mu.Lock()
	defer rs.mu.Unlock()
	for _, c := range calls {
		if c.Stage == "" {
			c.Stage = string(stage)
		}
		rs.ToolCalls = append(rs.ToolCalls, c)
	}
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
	rs.mu.Lock()
	defer rs.mu.Unlock()
	rs.recordExecutionPathLocked(stage, path)
}

// recordExecutionPathLocked is RecordExecutionPath's body for callers that
// already hold rs.mu — the terminal claim replays the dispatcher's
// StageExecutionPaths payload (#309) inside its critical section, as claim
// step 1b, because those are the last mutations the run will ever accept
// (ADR-017 Decision 5; C16 — sync.Mutex is not reentrant).
func (rs *RuntimeState) recordExecutionPathLocked(stage PipelineStage, path string) {
	if path == "" {
		return
	}
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
	rs.mu.Lock()
	defer rs.mu.Unlock()
	rs.recordStagePuntReasonLocked(stage, reason)
}

// recordStagePuntReasonLocked is RecordStagePuntReason's body for callers that
// already hold rs.mu — claim step 1b's twin to recordExecutionPathLocked
// (ADR-017 Decision 5, C16).
func (rs *RuntimeState) recordStagePuntReasonLocked(stage PipelineStage, reason string) {
	if reason == "" {
		return
	}
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

// AppendStageGateResultToDisk persists a stage post-condition gate result for
// a run driven from outside the in-process scheduler (Issue #210) — the
// `nightgauge gate verify --record` CLI seam the TypeScript
// HeadlessOrchestrator uses. It is the one writer of a run's snapshot that
// lives in a DIFFERENT OS PROCESS, so the IPC server's in-memory terminal latch
// cannot cover it; ADR-017 Decision 5 closes that cross-process half with three
// rules, all implemented here.
//
//  1. LOAD-OR-SKIP. The create-on-miss fallback is deleted. It used to fall back
//     to a fresh RuntimeState and Persist it, which resurrects a snapshot the
//     terminal claim had just removed — from a process with no registry and no
//     closedRuns — producing a second, contradictory pipeline_done at the next
//     server start (F22). With no snapshot for the issue the gate's VERDICT
//     STILL RUNS AND IS RETURNED; only the --record write is skipped, loudly.
//     A skipped gate record is an annoyance; a gate record written into a
//     resurrected or guessed file is corruption.
//  2. IT REFUSES A TERMINAL SNAPSHOT. The durable `terminal` marker is exactly
//     the cross-process latch the in-memory one cannot be.
//  3. IT WRITES THROUGH PersistExisting, NOT Persist, so the read-modify-write
//     cannot re-create a file removed between the load and the write. The
//     residual is a narrow rename race, named in the ADR as R-1.
//
// ADDRESSING IS STILL BY ISSUE, and that is a known interim. The resolution is
// PickPersistedStateForIssue — the standard pick: newest non-terminal, then
// newest overall. Under ADR-017 Decision 8's per-run filenames a re-run issue
// ACCUMULATES snapshots (nothing marks them terminal until step 4), so
// "more than one candidate" is the steady state for any issue dispatched twice,
// not the rare concurrency it looked like when one shared file was overwritten.
// Refusing on it would disable the record for every re-run issue permanently.
//
// Newest-non-terminal is the RIGHT answer here rather than a guess, because of
// who calls this: the gate CLI is spawned BY the run that produced the verdict,
// so that run's snapshot is the newest non-terminal one and any older
// non-terminal snapshot is an orphan of a prior dispatch. The one residual
// mis-attribution is two TRULY CONCURRENT dispatches of a single issue — the
// zombie case — where the newest is not necessarily the caller. That is closed
// by exact addressing (`--run-id`, defaulting to NIGHTGAUGE_RUN_ID from the
// stage environment) in ADR-017 step 7; it is not closed here.
//
// Zero candidates remains a loud SKIP: with no snapshot for the issue the
// gate's VERDICT STILL RUNS AND IS RETURNED, only the --record write is
// dropped.
func AppendStageGateResultToDisk(stateDir string, issueNumber int, stage PipelineStage, result StageGateResult) error {
	rs, err := PickPersistedStateForIssue(stateDir, issueNumber)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return fmt.Errorf(
				"gate record skipped for #%d (verdict still returned, record NOT written): no run snapshot in %s — the run never persisted, or its snapshot was already sealed away",
				issueNumber, stateDir)
		}
		return fmt.Errorf("gate record skipped for #%d (verdict still returned): scan %s: %w", issueNumber, stateDir, err)
	}
	if rs.Terminal {
		return fmt.Errorf(
			"gate record skipped for #%d (verdict still returned, record NOT written): the only snapshot (run %s) is TERMINAL — a gate result cannot be recorded onto a run that already reached its terminal event",
			issueNumber, rs.RunID)
	}
	rs.AppendStageGateResult(stage, result)
	if err := rs.PersistExisting(stateDir); err != nil {
		return fmt.Errorf("gate record skipped for #%d (verdict still returned): %w", issueNumber, err)
	}
	return nil
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

// ErrRunSealed is returned by every write path on a runtime whose terminal
// snapshot has already been written and removed by SealAndRemove (ADR-017
// Decision 5, claim step 4). It is the in-memory half of the terminal latch:
// the transition, phase and progress handlers gain no guard of their own, they
// simply call Persist and get this back.
var ErrRunSealed = errors.New("run is sealed: its terminal snapshot was written and removed")

// ErrNoRunIdentity is returned when a write is attempted on a RuntimeState
// whose RunID is not a canonical run identity (ADR-017 Decision 1). Two shapes
// reach it, and the second is why the predicate is IsIdentity rather than
// `!= ""`:
//
//   - EMPTY — the common case. Such a state has no correct filename and no
//     correct owner; writing `runtime-42-.json` would recreate the
//     shared-namespace collision the run identity exists to kill. This is the
//     same call #307 made when it refused to persist a repo-less runtime.
//   - PRESENT BUT NOT AN IDENTITY — the dangerous case. The RunID is
//     interpolated into the snapshot filename, so a value carrying `/` or `..`
//     is an arbitrary-path write on Persist and an arbitrary-path DELETE on
//     SealAndRemove; and a merely non-canonical value (a UUIDv4, a ULID) writes
//     a file that snapshotFilePattern cannot match, so it is invisible to every
//     discovery path — a silent phantom run. THIS IS THE ONE SINK: every write
//     goes through persistLocked, so validating here closes both halves for
//     every caller, including any future one.
var ErrNoRunIdentity = errors.New("runtime state's run id is not a valid run identity: refusing to persist")

// ErrSealWriteFailed reports that SealAndRemove could not WRITE the
// terminal-stamped snapshot (claim step 4). It exists so the caller's log can
// be honest per branch: on this branch the terminal marker never reached disk
// and the stale snapshot was removed instead, whereas a bare remove failure
// leaves a file that DOES carry `terminal: true`. Saying "the snapshot is
// terminal-marked either way" covers only the second case, and the difference
// is exactly what a reader needs to know about what is left on disk.
//
// The seal is latched on both branches: a run that reached its terminal claim
// never re-opens to further writes, whatever the filesystem did.
var ErrSealWriteFailed = errors.New("seal and remove: the terminal snapshot could not be written")

// Persist writes the current state atomically to {stateDir}/runtime-{issue}-{runId}.json.
//
// It is a Lock/defer-Unlock wrapper over persistLocked so the two forms cannot
// drift (ADR-017 Decision 5's lock-discipline table, C16). rs.mu is held across
// the MARSHAL AND THE WRITE, not just the snapshot: a marshal-under-lock
// followed by an unlocked write lets a stale byte slice land after a removal
// (F27), and it also lets two concurrent Persist calls on one runtime collide
// on AtomicWriteFile's single temp path. Holding the run's own mutex across its
// own file write serialises writers within the process, at a cost of a few
// milliseconds of contention on a mutex whose only other holders are that same
// run's mutations.
//
// Uses the atomic+fsync write contract so that a reader observes either the
// prior version or the new version, never partial JSON — even on power loss
// between rename and the next disk flush.
func (rs *RuntimeState) Persist(stateDir string) error {
	rs.mu.Lock()
	defer rs.mu.Unlock()
	return rs.persistLocked(stateDir)
}

// PersistExisting is Persist restricted to updating a snapshot that is ALREADY
// on disk: if the target file is absent it fails instead of creating it
// (ADR-017 Decision 5). This is what the cross-process gate seam writes
// through, so a read-modify-write cannot re-create a file that a terminal claim
// removed between the load and the write.
func (rs *RuntimeState) PersistExisting(stateDir string) error {
	rs.mu.Lock()
	defer rs.mu.Unlock()
	if rs.sealed {
		return ErrRunSealed
	}
	// Validated BEFORE the identity becomes a path component, not merely
	// non-empty: this method composes a filename and stats it.
	if !runstate.IsIdentity(rs.RunID) {
		return ErrNoRunIdentity
	}
	target := filepath.Join(stateDir, SnapshotFilename(rs.IssueNumber, rs.RunID))
	if _, err := os.Stat(target); err != nil {
		return fmt.Errorf("persist existing %s: %w", filepath.Base(target), err)
	}
	return rs.persistLocked(stateDir)
}

// SealAndRemove is claim step 4 of ADR-017 Decision 5, performed as ONE
// operation under rs.mu and holding NO registry lock: write the
// terminal-stamped snapshot, os.Remove that same path, then latch `sealed` so
// every later Persist returns ErrRunSealed without writing.
//
// THE PATH IS THE IDENTITY. Because the filename carries the run id, this
// cannot take a successor's file even in principle — the strongest available
// form of an identity-checked destructive write, and the direct fix for the
// bare-issue delete that let a zombie destroy a live run's crash snapshot.
//
// Write-then-remove is idempotent if the reconciler removed the file first: the
// write re-creates it as terminal and the remove takes it away again, net
// nothing. It calls persistLocked, NEVER Persist — re-entering the exported
// method from inside its own lock is F36 (sync.Mutex is not reentrant, C16).
func (rs *RuntimeState) SealAndRemove(stateDir string) error {
	rs.mu.Lock()
	defer rs.mu.Unlock()
	// The REMOVE half validated explicitly, ahead of the write. persistLocked
	// refuses the same predicate a line later, so this is belt-and-braces — but
	// this method is the only one that calls os.Remove on a composed path, and
	// an identity that is not an identity is an arbitrary-path DELETE. The
	// refusal must not depend on the ordering of the two statements below.
	if !runstate.IsIdentity(rs.RunID) {
		return fmt.Errorf("seal and remove for #%d: %w", rs.IssueNumber, ErrNoRunIdentity)
	}
	// An already-sealed runtime writes nothing and removes nothing — the same
	// refusal every other write path gives. Stated here rather than left to
	// persistLocked so the write-failure branch below is about genuine write
	// failures only.
	if rs.sealed {
		return ErrRunSealed
	}
	target := filepath.Join(stateDir, SnapshotFilename(rs.IssueNumber, rs.RunID))
	if err := rs.persistLocked(stateDir); err != nil {
		// THE WRITE-FAILURE BRANCH STILL SEALS AND STILL REMOVES (F27).
		// Returning here left the run unsealed AND left the stale NON-terminal
		// snapshot on disk — the one shape adoption happily rehydrates, which
		// is R-4's richer-record overwrite: a restart adopts the dead run, its
		// next call produces a record strictly richer by one stage, and the
		// history layer accepts that as an upgrade over the authoritative one.
		// The authoritative record was already durably written in claim step 2,
		// so removing the stale file costs at worst R-4's adopt-empty noise,
		// which is strictly better than resurrection.
		rs.sealed = true
		if rmErr := os.Remove(target); rmErr != nil && !os.IsNotExist(rmErr) {
			return fmt.Errorf("seal and remove %s: %w (%v) AND the stale non-terminal snapshot could not be removed: %v",
				filepath.Base(target), ErrSealWriteFailed, err, rmErr)
		}
		return fmt.Errorf("seal and remove %s: %w (%v); the stale non-terminal snapshot was REMOVED instead",
			filepath.Base(target), ErrSealWriteFailed, err)
	}
	if err := os.Remove(target); err != nil && !os.IsNotExist(err) {
		// The seal is still latched below: a failed remove leaves a snapshot
		// that carries `terminal: true`, which adoption refuses and the
		// reconciler removes without emitting. Re-opening the run to further
		// writes would be strictly worse.
		rs.sealed = true
		return fmt.Errorf("seal and remove %s: %w", filepath.Base(target), err)
	}
	rs.sealed = true
	return nil
}

// persistLocked is Persist's body for callers that already hold rs.mu —
// SealAndRemove is the one in-tree caller (ADR-017 Decision 5, C16).
//
// It enforces both refusals inside the caller's critical section, so neither
// check can be separated from the write by a scheduler:
//   - a SEALED runtime writes nothing and returns ErrRunSealed;
//   - a runtime whose RunID is not a canonical identity writes nothing and
//     returns ErrNoRunIdentity. THE PREDICATE IS runstate.IsIdentity, NOT
//     `!= ""`: rs.RunID is interpolated into the target path two statements
//     later, so an unvalidated value is an arbitrary-path write. This is the
//     single sink the one composer lives behind — Persist, PersistExisting and
//     SealAndRemove all land here — which is what makes validating at this one
//     line sufficient for the whole state layer.
func (rs *RuntimeState) persistLocked(stateDir string) error {
	if rs.sealed {
		return ErrRunSealed
	}
	if !runstate.IsIdentity(rs.RunID) {
		return fmt.Errorf("persist runtime for #%d: %q: %w", rs.IssueNumber, rs.RunID, ErrNoRunIdentity)
	}

	snap := rs.snapshotLocked()

	if err := os.MkdirAll(stateDir, 0755); err != nil {
		return fmt.Errorf("create state dir: %w", err)
	}

	data, err := json.Marshal(snap)
	if err != nil {
		return fmt.Errorf("marshal state: %w", err)
	}

	target := filepath.Join(stateDir, SnapshotFilename(snap.IssueNumber, snap.RunID))
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
		// The two durable latch halves travel with every copy and therefore
		// with every byte persistLocked writes (ADR-017 Decision 5/12): the
		// snapshot IS what a fresh process, the gate CLI and the reconciler
		// read to learn that this run is over.
		Terminal:        rs.Terminal,
		TerminalOutcome: rs.TerminalOutcome,
		Abandoned:       rs.Abandoned,
		AbandonedReason: rs.AbandonedReason,
	}
	if rs.TerminalAt != nil {
		at := *rs.TerminalAt
		snap.TerminalAt = &at
	}
	if rs.AbandonedAt != nil {
		at := *rs.AbandonedAt
		snap.AbandonedAt = &at
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
	if len(rs.ToolCalls) > 0 {
		snap.ToolCalls = make([]diagnostics.ToolCallRecord, len(rs.ToolCalls))
		copy(snap.ToolCalls, rs.ToolCalls)
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
	if len(rs.TerminatingStageTokens) > 0 {
		snap.TerminatingStageTokens = make(map[string]StageResult, len(rs.TerminatingStageTokens))
		for k, v := range rs.TerminatingStageTokens {
			snap.TerminatingStageTokens[k] = v
		}
	}
	return snap
}
