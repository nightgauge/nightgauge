// Package orchestrator implements the board-driven scheduling algorithm
// with cross-repo coordination, dependency ordering, and merge serialization.
package orchestrator

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/nightgauge/nightgauge/internal/attention"
	"github.com/nightgauge/nightgauge/internal/config"
	"github.com/nightgauge/nightgauge/internal/dockercompose"
	"github.com/nightgauge/nightgauge/internal/execution"
	"github.com/nightgauge/nightgauge/internal/execution/adapters"
	stagecontext "github.com/nightgauge/nightgauge/internal/execution/context"
	"github.com/nightgauge/nightgauge/internal/git"
	gh "github.com/nightgauge/nightgauge/internal/github"
	changeClassifier "github.com/nightgauge/nightgauge/internal/intelligence/changeClassifier"
	"github.com/nightgauge/nightgauge/internal/intelligence/complexity"
	"github.com/nightgauge/nightgauge/internal/intelligence/learning"
	"github.com/nightgauge/nightgauge/internal/intelligence/routing"
	"github.com/nightgauge/nightgauge/internal/intelligence/survival"
	"github.com/nightgauge/nightgauge/internal/intelligence/tokens"
	"github.com/nightgauge/nightgauge/internal/orchestrator/gates"
	"github.com/nightgauge/nightgauge/internal/orchestrator/recovery"
	pmstages "github.com/nightgauge/nightgauge/internal/orchestrator/stages"
	"github.com/nightgauge/nightgauge/internal/platform"
	"github.com/nightgauge/nightgauge/internal/runstate"
	"github.com/nightgauge/nightgauge/internal/state"
	"github.com/nightgauge/nightgauge/internal/trace"
	"github.com/nightgauge/nightgauge/pkg/types"
)

// StageRunner abstracts skill execution for pipeline stages.
// Two implementations: ExecutionManagerRunner (auto mode) and IpcStageRunner (VSCode mode).
type StageRunner interface {
	RunStage(ctx context.Context, params StageRunParams) (*StageRunResult, error)
}

// telemetryService is the interface for platform telemetry operations used by the scheduler.
// *platform.TelemetryService satisfies this interface.
type telemetryService interface {
	EmitPipelineEvent(ctx context.Context, event platform.PipelineEvent)
	PushPipelineRun(ctx context.Context, record state.V2RunRecord)
	// SyncQueue mirrors the current queue snapshot to the platform so the web
	// dashboard shows live queued/working items. Fire-and-forget.
	SyncQueue(ctx context.Context, items []platform.QueueSyncItem)
}

// StageRunParams is the cross-mode stage execution request.
type StageRunParams struct {
	Stage             state.PipelineStage
	IssueNumber       int
	Repo              string
	Model             string
	MaxTokens         int
	Timeout           time.Duration
	SkillPath         string
	ContextFile       string
	OutputFile        string
	TargetRepo        string
	WorktreePath      string // Absolute path for Claude CLI working directory (IPC mode only)
	Runtime           *state.RuntimeState
	AllowedTools      []string
	Prompt            string
	PhaseEventFn      func(stage, name string, index, total int)
	SkillContent      string // Resolved skill body from platform; empty = use local file
	SkillFallbackUsed bool   // True when platform resolution failed and community skill is used
	RetroFindings     string // Prior failure findings injected on escalated retry; empty = first attempt
	IsEscalatedRetry  bool   // True when this is an escalated retry with a better model
}

// StageRunResult is the cross-mode stage execution result.
type StageRunResult struct {
	ExitCode           int
	InputTokens        int
	OutputTokens       int
	CacheReadTokens    int     // Cache read input tokens (billed at lower rate)
	CostUsd            float64 // Actual cost from Claude CLI (total_cost_usd); 0 = use calculated fallback
	FeedbackFile       string  // path to feedback context file, if written by stage
	EscalationRecorded bool    // True when the runner evaluated and recorded escalation (IPC mode)
	// FallbackRecorded is the model-unavailable sibling of EscalationRecorded
	// (#42): the runner classified the failure as an API model rejection and
	// recorded a sticky tier DOWNGRADE on the RetryEngine. The scheduler
	// retries the stage (the model resolution picks up the substitution) and
	// fans out the user-facing notification from FallbackFromModel/ToModel.
	FallbackRecorded  bool
	FallbackFromModel string
	FallbackToModel   string
	// ── #91 served-model attribution ────────────────────────────────────
	// ServedModel is the model that ACTUALLY served the stage per the CLI
	// stream (last observed). Empty when the stream carried no model info.
	// The claude CLI silently retries safety-refused turns on a fallback
	// model (its internal model_refusal_fallback event) and still exits 0,
	// so the requested model is not guaranteed to be the serving one. Cost,
	// exit-record, telemetry, and history attribution use this when set.
	// Distinct from FallbackFromModel/ToModel above, which record OUR #42
	// retry-engine downgrade, not the CLI's internal swap — this field must
	// never feed routing, sticky downgrades, or retries.
	// See docs/spikes/fable-5-behavior-porting.md §8.3.
	ServedModel string
	// RefusalFallback* echo the CLI's model_refusal_fallback event when one
	// was observed (#91). Attribution + notification only.
	RefusalFallbackFrom     string
	RefusalFallbackTo       string
	RefusalFallbackCategory string
	// ErrorText is the human-readable error reason for a non-zero exit, propagated
	// from the executor (IPC: from skillRunner stall-kill / cost-cap markers).
	// Issue #3207 — without this, IPC stall-kill failures arrived at the scheduler
	// with err==nil and ClassifyTerminalKind never matched, so the daily JSONL
	// either dropped the record or mis-classified it as subagent_crash.
	ErrorText string
	// LastOutputLines is the trailing stderr/stdout snippet (≤200 lines, ≤200KB)
	// captured by the executor at terminal failure — populated on the matching
	// V3 record's StageDetail.last_output_lines so retros have evidence.
	LastOutputLines string

	// ── #3605 stage-exit diagnostic record fields ─────────────────────
	// Forwarded verbatim from StageResultParams (IPC mode) for persistence
	// by internal/diagnostics.WriteStageExitRecord at stage end. All
	// optional — absent values yield a terser exit record but never a
	// missing one. See docs/STAGE_EXIT_DIAGNOSTIC.md.

	// SessionID is the claude CLI conversation id captured by TS.
	SessionID string
	// Signal is the POSIX signal name delivered to the subprocess.
	Signal string
	// SignalSource names the in-binary code path that delivered Signal.
	SignalSource string
	// ElapsedMs is total wall time from stage start to exit (ms).
	ElapsedMs int64
	// IdleMsAtExit is ms since the last subprocess output chunk at exit.
	IdleMsAtExit int64
	// CacheCreationTokens is the cache-creation token count for the stage.
	CacheCreationTokens int
	// LastBashCommand is the most recent Bash tool_use input.
	LastBashCommand string
	// LastBashExit is the exit code of the matching Bash tool_result.
	LastBashExit *int
	// StopHookErrored is true when the stream included a stop-hook-error.
	StopHookErrored bool
	// StderrTail is the last 4 KB of stderr from the SkillRunner ring buffer.
	StderrTail string

	// ── #3666 follow-up: budget-kill + shipped-partially via IPC ────────
	// BudgetExceeded is true when the BudgetEnforcer killed this stage.
	// Set independently of (err != nil) so the scheduler can take the
	// budget-aware branch without parsing the error text.
	BudgetExceeded bool
	// ShippedPartially is true when BudgetExceeded fired but the stage's
	// work product shipped (e.g. pr-create killed AFTER opening the PR).
	// Scheduler advances to next stage rather than retrying. See #3666.
	ShippedPartially bool
	// ShippedPRNumber is the PR the killed stage produced (0 when
	// ShippedPartially is false). Logged for operator visibility.
	ShippedPRNumber int
}

// LicenseChecker is the preflight hook for license validation.
// Returns nil result with nil error when running community tier (allow).
type LicenseChecker interface {
	CheckLicense(ctx context.Context, issueNumber int) (*LicenseCheckResult, error)
}

// LicenseCheckResult is the preflight license check outcome.
type LicenseCheckResult struct {
	Allowed    bool   `json:"allowed"`
	Tier       string `json:"tier"`
	Reason     string `json:"reason,omitempty"`
	ActionURL  string `json:"actionUrl,omitempty"`
	CacheUntil string `json:"cacheUntil,omitempty"` // ISO 8601 — re-validate when now > this
	// Status is one of "active"/"expired"/"revoked"/"suspended", or "" when
	// unknown. A CONFIRMED "revoked"/"suspended" status re-validated mid-run
	// halts the pipeline (see Scheduler.revalidateLicense) rather than merely
	// flagging-and-continuing like a passive cache-expiry. Issue #4156.
	Status string `json:"status,omitempty"`
}

// IdentityChecker is the preflight hook for per-repo GitHub identity assertion
// (#4068). Before dispatching any stage for a target repo, the scheduler asserts
// the resolved identity is the one configured for that repo's owner AND has push
// (and, when a required-review ruleset gates the base branch, admin/bypass) — so
// a read-only or wrong-user identity is rejected at preflight rather than
// surfacing later as a silent un-mergeable PR.
//
// CheckIdentity returns (allowed, reason). reason is the specific blocker when
// allowed=false (surfaced via SetStageError). Implementations MUST treat "no
// github_user configured for the repo's owner" as allowed (skip) so
// single-identity repos and CLI mode are unaffected. A nil checker on the
// scheduler disables the gate entirely.
type IdentityChecker interface {
	CheckIdentity(ctx context.Context, owner, repo string, issueNumber int) (bool, string)
}

// ExecutionManagerRunner wraps the existing execution.Manager as a StageRunner.
type ExecutionManagerRunner struct {
	execMgr *execution.Manager
}

// RunStage implements StageRunner by delegating to execution.Manager.
func (r *ExecutionManagerRunner) RunStage(ctx context.Context, params StageRunParams) (*StageRunResult, error) {
	opts := execution.StageOptions{
		Repo:         params.Repo,
		IssueNumber:  params.IssueNumber,
		Stage:        string(params.Stage),
		SkillPath:    params.SkillPath,
		ContextFile:  params.ContextFile,
		OutputFile:   params.OutputFile,
		Model:        params.Model,
		MaxTokens:    params.MaxTokens,
		Timeout:      params.Timeout,
		Runtime:      params.Runtime,
		AllowedTools: params.AllowedTools,
		Prompt:       params.Prompt,
		TargetRepo:   params.TargetRepo,
		PhaseEventFn: params.PhaseEventFn,
	}

	result, err := r.execMgr.RunStage(ctx, opts)
	if err != nil {
		exitCode := 0
		if result != nil {
			exitCode = result.ExitCode
		}
		return &StageRunResult{ExitCode: exitCode}, err
	}

	return &StageRunResult{
		ExitCode:     result.ExitCode,
		InputTokens:  result.InputTokens,
		OutputTokens: result.OutputTokens,
		// #91 served-model attribution, tracked by the execution manager's
		// stream reader.
		ServedModel:             result.ServedModel,
		RefusalFallbackFrom:     result.RefusalFallbackFrom,
		RefusalFallbackTo:       result.RefusalFallbackTo,
		RefusalFallbackCategory: result.RefusalFallbackCategory,
	}, nil
}

// issueGetter abstracts issue operations used by the scheduler for testability.
type issueGetter interface {
	GetIssue(ctx context.Context, owner, repo string, number int) (*types.Issue, error)
	GetIssuesByNumbers(ctx context.Context, owner, repo string, numbers []int) (map[int]*types.Issue, error)
	GetEpicProgress(ctx context.Context, epicNodeID string) (*types.EpicProgress, error)
	GetEpicProgressByNumber(ctx context.Context, owner, repo string, number int) (*types.EpicProgress, error)
	CloseIssue(ctx context.Context, issueID string) error
	RemoveBlockedBy(ctx context.Context, blockedID, blockerID string) error
}

// Scheduler reads the project board and dispatches pipeline executions.
type Scheduler struct {
	client        *gh.Client
	boardSvc      *gh.BoardService
	issueSvc      issueGetter
	epicSvc       *gh.EpicService
	execMgr       *execution.Manager
	stateSvc      *state.BoardStateService
	owner         string
	projectNumber int
	workspaceRoot string

	// attention is the shared Action Center DecisionRequest store (ADR 015),
	// injected by NewAutonomousScheduler via SetAttention so the run-scoped
	// producers (budget ceiling, branch-protection block, definitive auth
	// failure) raise through the same single writer as the fleet-scoped ones.
	// nil in CLI/auto mode (no daemon) — raiseAttention is nil-safe.
	attention *attention.Store

	// clientResolver, when set, resolves a GitHub client scoped to a specific
	// (owner, repo) using that repo's configured token/identity. The default
	// issueSvc is built from a single startup client tied to the primary repo's
	// config, which is wrong for cross-repo work: a sub/epic in another repo may
	// require a different configured github_user (and thus a different token).
	// EnqueueEpic resolves a per-repo issueGetter through this when available so
	// private cross-repo epics use the correct configured identity (#3700).
	// nil → fall back to the default issueSvc.
	clientResolver func(ctx context.Context, owner, repo string) (*gh.Client, error)

	// repoPathResolver, when set, maps an "owner/repo" slug to that repo's
	// filesystem root. A run's on-disk state — trace, runtime-{N}.json,
	// stage-context, exit-records, worktrees — must all root at the run's
	// TARGET repo in a multi-repo workspace, not the scheduler's single launch
	// root, or the state is split across two repos (#229; mirrors the IPC
	// server's pipelineStateDir fix for #215/#218). The IPC server wires this
	// from its ClientResolver.RepoPath; CLI/auto mode leaves it nil and every
	// root falls back to the execution manager's workspace root — purely
	// additive, single-repo behavior unchanged.
	repoPathResolver func(repo string) string

	// repoRootsResolver, when set, enumerates every registered repo filesystem
	// root the scheduler can dispatch to. Crash recovery reads the
	// current-run.json sidecar, but since #229 the sidecar is written at the
	// run's TARGET repo root (via runRoot) — so a cross-repo run that crashes
	// leaves its sidecar outside the launch root. Scanning only the launch root
	// would miss it, leaving the run orphaned with no synthesized terminal
	// record. Mirrors the IPC server's pipelineStateScanRoots fix (#218); the
	// IPC server wires this from ClientResolver.RegisteredPaths, and CLI/auto
	// mode leaves it nil — only the launch root is scanned, single-repo behavior
	// unchanged (#239).
	repoRootsResolver func() []string

	// StageRunner abstracts skill execution (auto mode vs IPC mode)
	stageRunner StageRunner

	// LicenseChecker validates license before pipeline stages (nil = community tier)
	licenseChecker LicenseChecker

	// identityChecker asserts the resolved per-repo GitHub identity has push
	// (and admin/bypass when needed) before dispatch (#4068). nil disables the
	// gate — single-identity repos and CLI mode are unaffected.
	identityChecker IdentityChecker

	// SkillService resolves optimized skill content from the platform (nil = use local files)
	skillService *platform.SkillService

	// Orchestration engines
	retryEngine  *RetryEngine
	budgetEngine *BudgetEnforcer
	ralphEngine  *RalphLoopController

	// stageGates is the post-condition verification registry (Issue #3266).
	// Defaults to gates.Default(); injectable for tests. Stages without an
	// entry skip Verify entirely.
	stageGates map[state.PipelineStage]gates.StageGate

	// Concurrency limits
	maxPerRepo               int
	repoConcurrencyOverrides map[string]int
	repoRunning              map[string]int
	mu                       sync.Mutex
	scalingConfig            *ScalingConfig // Dynamic agent scaling (nil = use defaults)

	// Budget-aware retry tracking (Issue #2338 — max 1 budget retry per stage per run)
	budgetRetries map[string]int

	// Merge serialization
	mergeLocks map[string]*sync.Mutex

	// prMergeRunner is the deterministic-first hook for the pr-merge stage
	// (Issue #3264). When non-nil, the scheduler invokes it before the LLM
	// skill path; on `merged` the skill is skipped, on `punt` the skill runs
	// as it does today. nil disables the hook (every pr-merge runs LLM).
	// Tests inject deterministic fakes via WithPRMergeRunner.
	prMergeRunner pmstages.PRMergeRunner

	// prCreateRunner is the deterministic-first hook for the pr-create stage
	// (Issue #3265). Mirrors prMergeRunner: on `created` the skill is skipped,
	// on `punt` the skill runs as today. nil disables the hook. Tests inject
	// deterministic fakes via WithPRCreateRunner.
	prCreateRunner pmstages.PRCreateRunner

	// recoveryRegistry is the FailureRecovery registry consulted on stage
	// failure (Issue #3268). When non-nil, the scheduler invokes
	// TryRecover after stall-rewind doesn't apply and before model
	// escalation; on Recovered=true the stage advances. nil disables the
	// framework (every stage failure follows the legacy retry/escalation
	// path). Tests inject deterministic fakes via WithRecoveryRegistry.
	recoveryRegistry *recovery.Registry

	// Outcome recorder for the pipeline learning system
	recorder *learning.Recorder

	// telemetrySvc pushes completed run records to the platform (optional).
	telemetrySvc telemetryService
	// telemetryEnabled is the resolved config gate for platform telemetry.
	telemetryEnabled bool

	// Queue — authoritative, file-backed
	queue []QueueItem

	// OnFailureStatus: "ready" (default), "backlog", or "unchanged"
	onFailureStatus string

	// excludeLabels lists human-only labels (autonomous.exclude_labels,
	// default ["owner-action"]) that EnqueueEpic refuses to enqueue as
	// sub-issues, and that queueAddCmd checks before a direct `queue add`.
	// Issue #317.
	excludeLabels []string

	// adapterExplicit mirrors SchedulerConfig.AdapterExplicit (#54).
	adapterExplicit string
	// runDefaultAdapter is the adapter the run started with — stages without
	// a stage_adapters entry revert to it after a per-stage switch (#54).
	runDefaultAdapter adapters.SkillRunner

	// Callbacks
	onStageStart       func(repo string, issue int, stage string, title string)
	onStageComplete    func(repo string, issue int, stage string, err error, inputTokens, outputTokens, cacheReadTokens int, costUsd float64, model string)
	onEpicComplete     func(repo string, epicNumber int)
	onPipelineComplete func(repo string, issue int, runtime *state.RuntimeState, success bool)
	onQueueChanged     func(QueueState)
	onStateChanged     func(repo string, issue int, runtime *state.RuntimeState)
	onModelFallback    func(repo string, issue int, stage, fromModel, toModel, reason string)
	onPhaseDetected    func(repo string, issue int, stage, name string, index, total int)
	onScalingDecision  func(epicNumber int, decision ScalingDecision)

	// activeStages tracks the cancel function for each currently-running
	// per-issue stage context (Issue #3296). When the TS-side stall watchdog
	// observes ≥ N consecutive *connectivity* failures (DNS/ECONNREFUSED) and
	// fires pipeline.cancelActiveForNetworkOutage, the scheduler walks this
	// map and cancels each ctx with cause ErrNetworkUnavailable so the LLM
	// subprocess exits immediately instead of burning tokens until Anthropic's
	// stream-idle-timeout fires (the failure mode behind #3216 / $20.87 lost).
	activeStages   map[int]context.CancelCauseFunc
	activeStagesMu sync.Mutex

	// activeRuntimes tracks the live RuntimeState for each currently-running
	// pipeline keyed by issue number. Used by IPC mode (IpcStageRunner) so
	// the IPC server can update PhaseHistory on the scheduler's runtime when
	// TypeScript reports phase markers via pipeline.notifyPhaseTransition.
	// Without this, IPC-mode runs have an empty PhaseHistory in every
	// pipeline.stateChanged snapshot, which means the tree view loses phase
	// counts ("17/17 phases") on already-completed stages whenever the
	// extension reloads mid-pipeline.
	activeRuntimes   map[int]*state.RuntimeState
	activeRuntimesMu sync.Mutex

	// runningSiblingsFn, when non-nil, returns the set of `owner/repo#number`
	// keys for in-flight pipelines other than (repo, issueNumber). Used by
	// the stage-exit diagnostic writer (#3605) so each daily record carries
	// the cross-pipeline forensic context (which sibling pipelines were
	// live at the moment this stage exited). nil disables sibling capture —
	// the record still writes, with an empty siblings list.
	runningSiblingsFn func(repo string, issueNumber int) []string

	// rateLimitRemainingFn, when non-nil, returns the GitHub GraphQL bucket
	// reading at call time. Used by the stage-exit diagnostic writer (#3605)
	// to correlate near-empty buckets with the stage failures they likely
	// caused. Return -1 when unavailable. nil disables the field — the
	// record still writes, with RateLimitRemainingAtExit omitted.
	rateLimitRemainingFn func() int
}

// ErrNetworkUnavailable is the cancel cause used when extended GitHub
// connectivity loss aborts an active LLM stage. Failure handling treats this
// kind specially: the failure is environmental, not model-related, so the
// pipeline skips auto-retro and calibration update, preserves the worktree,
// and resets board status to "Ready" for re-pickup once connectivity returns.
// See docs/FAILURE_TAXONOMY.md (terminal_failure_kind="network_unavailable").
var ErrNetworkUnavailable = errors.New("network unavailable: extended GitHub connectivity loss")

// QueueEntry represents an issue queued for pipeline execution (legacy alias).
type QueueEntry struct {
	Repo        string `json:"repo"`
	IssueNumber int    `json:"issueNumber"`
	Priority    int    `json:"priority"`
	// RemoteRunID is the run_id from the platform command payload, when set
	// by a remote-triggered pipeline.run command (#3557).
	RemoteRunID string `json:"remoteRunId,omitempty"`
}

// QueueItem represents a queued issue with full metadata (authoritative).
type QueueItem struct {
	Repo        string `json:"repo"`
	IssueNumber int    `json:"issueNumber"`
	Title       string `json:"title"`
	Priority    int    `json:"priority"`
	// Status is one of pending|ready|processing|completed|failed|paused.
	// "paused" (Issue #3001) means the item was waiting behind a pipeline that
	// hit a terminal failure; PausedReason carries the cause. Items only resume
	// via explicit operator action — never auto-resume when failure_mode=halt.
	Status     string             `json:"status"`
	Labels     []string           `json:"labels,omitempty"`
	BlockedBy  []QueueBlockingRef `json:"blockedBy,omitempty"`
	EpicOrder  *int               `json:"epicOrder,omitempty"`
	IsBatch    bool               `json:"isBatch,omitempty"`
	EpicNumber *int               `json:"epicNumber,omitempty"`
	AddedAt    time.Time          `json:"addedAt"`
	Position   int                `json:"position"` // 1-indexed
	// PausedReason is set when Status == "paused" (Issue #3001). Discriminated
	// by Kind so future paused reasons (manual hold, license check) can be
	// added without re-shaping callers.
	PausedReason *QueuePausedReason `json:"pausedReason,omitempty"`
	// RemoteRunID is the run_id from the platform command payload for
	// remote-triggered runs. Preferred over the locally-generated runstate
	// UUID when set (#3557).
	RemoteRunID string `json:"remoteRunId,omitempty"`
}

// QueuePausedReason explains why a queue item is paused.
//
// Kind values:
//   - "upstream_failure" — pipeline run for an earlier item hit a terminal
//     failure; FailedRunID points to the failed RunRecord. (Issue #3001)
//   - "baseline_ci_red" — issue acceptance criteria require a CI baseline
//     that is currently red on `main`. The Workflow/Job/FailedRuns/
//     LookbackRuns fields carry the gate's evidence so a daily promote
//     sweep can re-evaluate without re-parsing the issue body. (Issue #3004)
//   - "blocked_dependency" — issue has an OPEN native `blockedBy` dependency
//     (blocker's PR not merged). The BlockingIssues field names the open
//     blockers so the deps-gate promote sweep (and the autonomous cascade)
//     can resume the item once they all close. A controlled hold, not a
//     failure. (Issue #231)
//
// FailedRunID is empty for kinds that are not associated with a specific
// failed RunRecord (e.g. baseline_ci_red, blocked_dependency).
type QueuePausedReason struct {
	Kind        string `json:"kind"`
	FailedRunID string `json:"failed_run_id,omitempty"`
	Summary     string `json:"summary,omitempty"`

	// Workflow / Job / FailedRuns / LookbackRuns are populated when
	// Kind == "baseline_ci_red". Empty/zero for other kinds.
	Workflow     string `json:"workflow,omitempty"`
	Job          string `json:"job,omitempty"`
	FailedRuns   int    `json:"failed_runs,omitempty"`
	LookbackRuns int    `json:"lookback_runs,omitempty"`

	// BlockingIssues names the open blockers when Kind == "blocked_dependency".
	// Empty for other kinds. (Issue #231)
	BlockingIssues []QueueBlockingRef `json:"blocking_issues,omitempty"`
}

// QueueBlockingRef is a reference to a blocking issue within a queue item.
type QueueBlockingRef struct {
	Number int    `json:"number"`
	Title  string `json:"title"`
	State  string `json:"state"`
}

// QueueState is the persistent queue state.
type QueueState struct {
	SchemaVersion string      `json:"schema_version"`
	Status        string      `json:"status"` // idle|waiting|processing|paused
	Items         []QueueItem `json:"items"`
	UpdatedAt     time.Time   `json:"updated_at"`
}

const queueStateFile = ".nightgauge/pipeline/queue-state.json"

// queueSchemaVersion is the persisted queue schema version.
//
// 2.0 → 2.1 (Issue #3001): added per-item "paused" status and structured
// PausedReason. Additive — readers default missing fields to undefined and
// treat unknown statuses as "pending".
//
// 2.1 → 2.2 (Issue #3004): added "baseline_ci_red" PausedReason kind and the
// Workflow/Job/FailedRuns/LookbackRuns fields on QueuePausedReason. Additive
// — readers that don't recognize the kind treat it the same as any other
// paused item (no auto-resume).
//
// 2.2 → 2.3 (Issue #231): added "blocked_dependency" PausedReason kind and the
// BlockingIssues field on QueuePausedReason. Additive — 2.2 readers ignore the
// unknown kind (it parses as a generic paused item) and the BlockingIssues
// field is omitempty, so older records remain valid without a migration.
const queueSchemaVersion = "2.3"

// currentRunSidecarFile is the path (relative to workspaceRoot) where the
// scheduler records the in-flight run at stage start. The file is removed on
// clean pipeline completion. A stale sidecar at scheduler startup means the
// orchestrator process crashed mid-stage; the loadQueue path synthesizes a
// terminal-failure RunRecord and pauses the queue. (Issue #3001)
const currentRunSidecarFile = ".nightgauge/pipeline/current-run.json"

// SchedulerConfig holds configuration for the scheduler.
type SchedulerConfig struct {
	Owner         string
	OwnerType     gh.OwnerType
	ProjectNumber int
	// MaxPerRepo is the default per-repository concurrency cap (concurrency.
	// per_repo_max). 0 → 1 (serialize per repo).
	MaxPerRepo int
	// RepoConcurrencyOverrides overrides MaxPerRepo for specific repos
	// (concurrency.repository_overrides), keyed by short name or "owner/repo".
	RepoConcurrencyOverrides map[string]int
	WorkspaceRoot            string
	Adapter                  adapters.SkillRunner
	// AdapterExplicit is the raw per-invocation adapter override (--adapter
	// flag or NIGHTGAUGE_ADAPTER env), "" when the adapter came from config
	// or the default. When set, per-stage pipeline.stage_adapters overrides
	// are skipped — the invocation pinned the adapter for the whole run (#54).
	AdapterExplicit string
	// OnFailureStatus controls where issues move on the project board when a
	// pipeline run fails. Valid values: "ready" (default), "backlog", "unchanged".
	OnFailureStatus string
	// ExcludeLabels lists human-only labels (autonomous.exclude_labels) that
	// EnqueueEpic must never enqueue as sub-issues. Empty falls back to
	// defaultExcludeLabels (["owner-action"]). Issue #317.
	ExcludeLabels []string
}

// NewScheduler creates a board-driven scheduler.
func NewScheduler(client *gh.Client, cfg SchedulerConfig) *Scheduler {
	maxPerRepo := cfg.MaxPerRepo
	if maxPerRepo <= 0 {
		maxPerRepo = 1
	}

	execMgr := execution.NewManager(cfg.WorkspaceRoot, cfg.Adapter)

	onFailureStatus := cfg.OnFailureStatus
	if onFailureStatus == "" {
		onFailureStatus = "ready"
	}

	excludeLabels := resolvedExcludeLabels(cfg.ExcludeLabels)

	s := &Scheduler{
		client:                   client,
		boardSvc:                 gh.NewBoardService(client, cfg.Owner, cfg.ProjectNumber, cfg.OwnerType),
		issueSvc:                 gh.NewIssueService(client),
		epicSvc:                  gh.NewEpicService(client),
		execMgr:                  execMgr,
		stateSvc:                 state.NewBoardStateService(client, cfg.Owner, cfg.ProjectNumber, cfg.OwnerType),
		owner:                    cfg.Owner,
		projectNumber:            cfg.ProjectNumber,
		workspaceRoot:            cfg.WorkspaceRoot,
		stageRunner:              &ExecutionManagerRunner{execMgr: execMgr},
		retryEngine:              NewRetryEngine(retryConfigForWorkspace(cfg.WorkspaceRoot)),
		budgetEngine:             NewBudgetEnforcer(DefaultBudgetConfig()),
		ralphEngine:              NewRalphLoopController(DefaultRalphConfig()),
		stageGates:               defaultStageGatesFromEnv(),
		maxPerRepo:               maxPerRepo,
		repoConcurrencyOverrides: cfg.RepoConcurrencyOverrides,
		repoRunning:              make(map[string]int),
		budgetRetries:            make(map[string]int),
		mergeLocks:               make(map[string]*sync.Mutex),
		recorder:                 learning.NewRecorder(cfg.WorkspaceRoot),
		onFailureStatus:          onFailureStatus,
		excludeLabels:            excludeLabels,
		adapterExplicit:          cfg.AdapterExplicit,
		runDefaultAdapter:        cfg.Adapter,
		prMergeRunner:            pmstages.NewDeterministicRunner(),
		prCreateRunner:           NewDefaultPRCreateRunner(client),
	}
	// Wire FailureRecovery registry (Issue #3268). Reuses the same runners
	// as the deterministic-first hooks so a recovery and a deterministic
	// merge use a single source of truth.
	s.recoveryRegistry = recovery.Default(cfg.WorkspaceRoot, s.prMergeRunner, s.prCreateRunner)
	s.loadQueue()
	return s
}

// applyStageAdapter re-points the execution manager at the adapter the
// canonical config chain resolves for this stage (#54):
// NIGHTGAUGE_PIPELINE_STAGE_ADAPTER_<STAGE> env → pipeline.stage_adapters.
// <stage> → ui.core.adapter → the run's initial adapter. Called only on the
// Go-direct path (IPC mode has no Go-side adapter; the VSCode resolver owns
// per-stage selection there) and only when the invocation did not pin the
// adapter explicitly. An unresolvable adapter name is a stage failure with
// remediation, never a silent fallback.
func (s *Scheduler) applyStageAdapter(stage, workspaceRoot string) error {
	cfg, err := config.Load(workspaceRoot)
	if err != nil {
		cfg = nil // no readable config — resolution falls through to the run default
	}
	res := config.ResolveStageAdapter(cfg, stage, os.Getenv)
	target := res.Adapter
	if target == "" || res.Source == "adapter-env" {
		// Nothing stage-specific resolved (adapter-env is the invocation
		// override, already active) — restore the run default in case an
		// earlier stage switched away from it.
		if s.runDefaultAdapter != nil && s.execMgr.AdapterName() != s.runDefaultAdapter.Name() {
			s.execMgr.SetAdapter(s.runDefaultAdapter)
			log.Printf("stage %s: adapter restored to run default %q", stage, s.runDefaultAdapter.Name())
		}
		return nil
	}
	registry := adapters.NewRegistry()
	runner, gerr := registry.Get(target)
	if gerr != nil {
		return fmt.Errorf("stage %q adapter %q (from %s) is not a known adapter: %w — fix pipeline.stage_adapters.%s / ui.core.adapter or the stage env override", stage, target, res.Source, gerr, stage)
	}
	if s.execMgr.AdapterName() != runner.Name() {
		s.execMgr.SetAdapter(runner)
		log.Printf("stage %s: adapter %q (source=%s)", stage, runner.Name(), res.Source)
	}
	return nil
}

// retryConfigForWorkspace returns the default retry config with the
// conflict-recovery re-dispatch bound resolved from the workspace config
// (pipeline.recovery.conflict_recovery.max_dev_redispatch / env override), so the
// RetryEngine's per-edge conflict limit matches the conflict-recovery action's
// configured bound (#4072).
func retryConfigForWorkspace(workspaceRoot string) RetryConfig {
	cfg := DefaultRetryConfig()
	cfg.MaxConflictRedispatch = recovery.GetConflictMaxDevRedispatch(workspaceRoot)
	return cfg
}

// formatConflictExhaustion builds the terminal failure reason for an exhausted
// conflict-recovery loop, naming the conflicting files carried in the recovery
// action's evidence (entries prefixed "conflicting_file="). Used when the
// in-memory edge bound terminates the loop on the skill-crash path, so the
// persisted terminal state names the files just like the normal-path escalation
// (#4072 review).
func formatConflictExhaustion(evidence []string) string {
	var files []string
	for _, e := range evidence {
		if f := strings.TrimPrefix(e, "conflicting_file="); f != e {
			files = append(files, f)
		}
	}
	if len(files) == 0 {
		return "conflict recovery exhausted: max feature-dev re-dispatches did not resolve the rebase conflict"
	}
	return fmt.Sprintf("conflict recovery exhausted: max feature-dev re-dispatches did not resolve the rebase conflict in %s",
		strings.Join(files, ", "))
}

// WithClientResolver injects a per-repo GitHub client resolver so cross-repo
// operations (e.g. EnqueueEpic) authenticate with the identity configured for
// the target repo rather than the scheduler's single startup client. The IPC
// server wires its ClientResolver here; CLI/auto mode leaves it nil and uses
// the default issueSvc. See #3700.
func (s *Scheduler) WithClientResolver(fn func(ctx context.Context, owner, repo string) (*gh.Client, error)) {
	s.clientResolver = fn
}

// WithRepoPathResolver injects a resolver mapping an "owner/repo" slug to that
// repo's filesystem root so a run's trace, runtime-{N}.json, stage-context,
// exit-records, and worktrees all root at the run's TARGET repo — not the
// scheduler's launch root — in a multi-repo workspace (#229). The resolver is
// also forwarded to the execution manager so worktree resolution stays
// consistent with run state. The IPC server wires this from its
// ClientResolver.RepoPath; CLI/auto mode leaves it nil and every root resolves
// to the execution manager's workspace root (additive; single-repo behavior
// unchanged).
func (s *Scheduler) WithRepoPathResolver(fn func(repo string) string) {
	s.repoPathResolver = fn
	if s.execMgr != nil {
		s.execMgr.SetRepoPathResolver(fn)
	}
}

// WithRepoRootsResolver injects an enumerator of every registered repo
// filesystem root so orchestrator-crash recovery can scan each repo's
// current-run.json sidecar, not just the launch root's. Because #229 roots a
// run's sidecar at its TARGET repo (via runRoot), a cross-repo run that crashes
// leaves its sidecar under a non-launch repo root; scanning only the launch
// root would never reconcile it. The IPC server wires this from its
// ClientResolver.RegisteredPaths; CLI/auto mode leaves it nil and only the
// launch root is scanned (additive; single-repo behavior unchanged, #239).
func (s *Scheduler) WithRepoRootsResolver(fn func() []string) {
	s.repoRootsResolver = fn
}

// runRoot resolves the filesystem root a run's on-disk state belongs in — the
// run's target repo in a multi-repo workspace so trace, runtime-{N}.json,
// stage-context, and exit-records never split across repos (mirrors
// Server.pipelineStateDir, #215). Falls back to the execution manager's
// workspace root when no resolver is set or the repo is unregistered, keeping
// single-repo / CLI / auto behavior byte-identical (#229).
func (s *Scheduler) runRoot(repo string) string {
	if s.repoPathResolver != nil {
		if root := s.repoPathResolver(repo); root != "" {
			return root
		}
	}
	return s.execMgr.WorkspaceRoot()
}

// issueServiceFor returns an issueGetter scoped to (owner, repo). When a
// clientResolver is wired it resolves a repo-specific client (correct token /
// configured github_user); otherwise it returns the default issueSvc. Resolver
// errors are non-fatal — we log and fall back so a transient resolver failure
// never blocks enqueue outright.
func (s *Scheduler) issueServiceFor(ctx context.Context, owner, repo string) issueGetter {
	if s.clientResolver == nil {
		return s.issueSvc
	}
	client, err := s.clientResolver(ctx, owner, repo)
	if err != nil || client == nil {
		log.Printf("WARN: client resolver failed for %s/%s (%v) — using default client", owner, repo, err)
		return s.issueSvc
	}
	return gh.NewIssueService(client)
}

// WithPRMergeRunner overrides the deterministic-first runner for the pr-merge
// stage (Issue #3264). Used by tests to inject a fake; production code should
// rely on the constructor's default.
func (s *Scheduler) WithPRMergeRunner(r pmstages.PRMergeRunner) {
	s.prMergeRunner = r
}

// WithPRCreateRunner overrides the deterministic-first runner for the
// pr-create stage (Issue #3265). Used by tests to inject a fake; production
// relies on the constructor's default.
func (s *Scheduler) WithPRCreateRunner(r pmstages.PRCreateRunner) {
	s.prCreateRunner = r
}

// WithRecoveryRegistry overrides the FailureRecovery registry (Issue #3268).
// Used by tests to inject a controlled set of actions and a small per-run
// cap. Pass nil to disable recovery entirely.
func (s *Scheduler) WithRecoveryRegistry(r *recovery.Registry) {
	s.recoveryRegistry = r
}

// registerActiveStage stores the cancel function for a per-issue stage context.
// Used by CancelAllForNetworkOutage to abort live LLM subprocesses when the
// TS-side watchdog detects an extended connectivity outage (#3296).
func (s *Scheduler) registerActiveStage(issueNumber int, cancel context.CancelCauseFunc) {
	if issueNumber <= 0 || cancel == nil {
		return
	}
	s.activeStagesMu.Lock()
	defer s.activeStagesMu.Unlock()
	if s.activeStages == nil {
		s.activeStages = make(map[int]context.CancelCauseFunc)
	}
	s.activeStages[issueNumber] = cancel
}

// unregisterActiveStage removes the cancel function for a per-issue stage.
// Called via defer at the end of each stage's execution.
func (s *Scheduler) unregisterActiveStage(issueNumber int) {
	s.activeStagesMu.Lock()
	defer s.activeStagesMu.Unlock()
	delete(s.activeStages, issueNumber)
}

// registerRuntime stores the live RuntimeState for an active pipeline. Used
// in IPC mode so the IPC server can record phase transitions onto the
// scheduler's runtime via RecordPhaseStart / RecordPhaseComplete.
func (s *Scheduler) registerRuntime(issueNumber int, rt *state.RuntimeState) {
	if issueNumber <= 0 || rt == nil {
		return
	}
	s.activeRuntimesMu.Lock()
	defer s.activeRuntimesMu.Unlock()
	if s.activeRuntimes == nil {
		s.activeRuntimes = make(map[int]*state.RuntimeState)
	}
	s.activeRuntimes[issueNumber] = rt
}

// unregisterRuntime removes the runtime for an issue. Called via defer at the
// end of runPipeline so a completed run can be GC'd.
func (s *Scheduler) unregisterRuntime(issueNumber int) {
	s.activeRuntimesMu.Lock()
	defer s.activeRuntimesMu.Unlock()
	delete(s.activeRuntimes, issueNumber)
}

// getActiveRuntime returns the live RuntimeState for an issue, or nil if no
// pipeline is currently running for that issue. Used by RecordPhase* and
// available to the IPC layer for additional bookkeeping.
func (s *Scheduler) getActiveRuntime(issueNumber int) *state.RuntimeState {
	s.activeRuntimesMu.Lock()
	defer s.activeRuntimesMu.Unlock()
	return s.activeRuntimes[issueNumber]
}

// RecordPhaseStart records a phase:start transition on the scheduler's
// runtime for the given issue. Safe no-op when no runtime is registered
// (e.g., HeadlessOrchestrator path — that path uses ipc.Server.activeRuntimes
// instead). Mirrors the BeginPhase call made by the auto/CLI-mode
// phaseEventFn at the top of runPipeline's stage loop, so PhaseHistory is
// populated regardless of which StageRunner executes the stage.
func (s *Scheduler) RecordPhaseStart(issueNumber int, stage, name string, index, total int) {
	if rt := s.getActiveRuntime(issueNumber); rt != nil {
		rt.BeginPhase(state.PipelineStage(stage), name, index, total)
	}
}

// RecordPhaseComplete records a phase:complete transition on the scheduler's
// runtime for the given issue. Safe no-op when no runtime is registered.
func (s *Scheduler) RecordPhaseComplete(issueNumber int, stage, name string) {
	if rt := s.getActiveRuntime(issueNumber); rt != nil {
		rt.CompletePhase(state.PipelineStage(stage), name)
	}
}

// CancelAllForNetworkOutage cancels every actively-running stage context with
// cause ErrNetworkUnavailable. Returns the issue numbers whose stages were
// signalled. Safe to call when no stages are active (returns nil).
//
// Invoked by the IPC handler `pipeline.cancelActiveForNetworkOutage` after
// the TS-side stall watchdog observes the threshold of consecutive
// connectivity failures. Each cancelled stage's failure handler classifies
// the run with terminal_failure_kind="network_unavailable" and skips
// auto-retro / calibration update so the noisy bookkeeping for an
// environmental failure doesn't pollute learning data.
func (s *Scheduler) CancelAllForNetworkOutage() []int {
	s.activeStagesMu.Lock()
	defer s.activeStagesMu.Unlock()
	if len(s.activeStages) == 0 {
		return nil
	}
	cancelled := make([]int, 0, len(s.activeStages))
	for n, cancel := range s.activeStages {
		cancel(ErrNetworkUnavailable)
		cancelled = append(cancelled, n)
	}
	sort.Ints(cancelled)
	log.Printf("CancelAllForNetworkOutage: cancelled %d active stage(s): %v", len(cancelled), cancelled)
	return cancelled
}

// WithStageRunner sets a custom StageRunner implementation.
// Used by IPC server to route stage execution through TypeScript SkillRunner.
func (s *Scheduler) WithStageRunner(runner StageRunner) {
	s.stageRunner = runner
}

// WithStageGates overrides the post-condition gate registry. Used by tests to
// inject a stub gate that returns passed=false on demand. nil restores the
// default registry (gates.Default()).
func (s *Scheduler) WithStageGates(reg map[state.PipelineStage]gates.StageGate) {
	if reg == nil {
		s.stageGates = gates.Default()
		return
	}
	s.stageGates = reg
}

// defaultStageGatesFromEnv builds the stage-gate registry, honouring the
// NIGHTGAUGE_DISABLE_GATES env var (comma-separated stage names) so
// integration tests that cannot satisfy a gate's external dependencies
// (e.g., `gh pr view` in pr-merge) can selectively disable them. Empty
// env var = full default registry.
func defaultStageGatesFromEnv() map[state.PipelineStage]gates.StageGate {
	reg := gates.Default()
	disabled := os.Getenv("NIGHTGAUGE_DISABLE_GATES")
	if disabled == "" {
		return reg
	}
	for _, name := range strings.Split(disabled, ",") {
		name = strings.TrimSpace(name)
		if name == "" {
			continue
		}
		delete(reg, state.PipelineStage(name))
	}
	return reg
}

// RetryEngine returns the retry engine for use by external runners.
// Used by IpcStageRunner to evaluate model escalation on stage failures.
func (s *Scheduler) RetryEngine() *RetryEngine {
	return s.retryEngine
}

// ExecMgr returns the execution manager for direct skill invocation.
// Used by AutonomousScheduler for refinement (non-pipeline skill runs).
func (s *Scheduler) ExecMgr() *execution.Manager {
	return s.execMgr
}

// WithLicenseChecker sets the license validation hook for pipeline preflight.
// Used by IPC server to route license checks through TypeScript PlatformApiClient.
func (s *Scheduler) WithLicenseChecker(lc LicenseChecker) {
	s.licenseChecker = lc
}

// WithIdentityChecker sets the per-repo identity assertion hook for pipeline
// preflight (#4068). nil disables the gate. Production wires a checker that
// resolves the configured github_user for the target owner and verifies push
// (and admin/bypass) via the GitHub collaborator-permission endpoint; tests
// inject a fake to exercise the dispatch gate deterministically.
func (s *Scheduler) WithIdentityChecker(ic IdentityChecker) {
	s.identityChecker = ic
}

// WithSkillService sets the platform skill resolution service.
// When set, paid-tier pipeline runs resolve skills from the platform instead of local files.
func (s *Scheduler) WithSkillService(svc *platform.SkillService) {
	s.skillService = svc
}

// WithTelemetryService enables platform telemetry for run records.
// When telemetryEnabled is true, recordOutcome() calls PushPipelineRun after each run.
func (s *Scheduler) WithTelemetryService(svc telemetryService, enabled bool) {
	s.telemetrySvc = svc
	s.telemetryEnabled = enabled
}

// preflightLicense checks the user's license before pipeline execution.
// Returns (allowed, tier) where tier is the license tier string.
// When no licenseChecker is configured (CLI mode), returns (true, "community").
func (s *Scheduler) preflightLicense(ctx context.Context, item types.BoardItem, runtime *state.RuntimeState) (bool, string) {
	if s.licenseChecker == nil {
		return true, "community" // No checker = CLI mode = community tier (allow)
	}
	preflightCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	result, err := s.licenseChecker.CheckLicense(preflightCtx, item.Number)
	if err != nil {
		log.Printf("#%d: license preflight error (degrading to community): %v", item.Number, err)
		return true, "community" // Fail-open for errors
	}
	if !result.Allowed {
		log.Printf("#%d: license preflight blocked: %s", item.Number, result.Reason)
		runtime.SetStageError("pipeline-start", result.Reason)
		s.emitStateChanged(item.Repo, item.Number, runtime)
		return false, ""
	}

	// Store license snapshot for mid-pipeline expiry detection.
	runtime.SetLicenseSnapshot(result.Tier, result.Allowed, result.Status, parseLicenseCacheUntil(item.Number, result.CacheUntil))

	log.Printf("#%d: license preflight passed: tier=%s status=%s", item.Number, result.Tier, result.Status)
	return true, result.Tier
}

// parseLicenseCacheUntil parses a LicenseCheckResult.CacheUntil (ISO 8601)
// into a time.Time. Returns the zero value (no expiry — community tier, or a
// malformed value) on any parse failure, logging the malformed case so a
// platform-side format regression is visible.
func parseLicenseCacheUntil(issueNumber int, cacheUntil string) time.Time {
	if cacheUntil == "" {
		return time.Time{}
	}
	t, err := time.Parse(time.RFC3339, cacheUntil)
	if err != nil {
		log.Printf("#%d: license: ignoring malformed cacheUntil %q: %v", issueNumber, cacheUntil, err)
		return time.Time{}
	}
	return t
}

// revalidateLicense re-checks the license mid-pipeline (#4156). Unlike
// preflightLicense (which blocks the whole run on any invalid result),
// revalidateLicense only halts progression for a CONFIRMED revoked/suspended
// status — the one signal IpcLicenseChecker.CheckLicense guarantees is not a
// transient/offline degradation (see internal/ipc/license_checker.go). Every
// other outcome (fail-open community, active, expired, a checker error) is
// treated as "still allowed" so a flaky connection can never falsely block a
// run that started with a valid license.
//
// Returns (stillAllowed, status). status is the confirmed status when
// stillAllowed is false, for logging/error-message purposes.
func (s *Scheduler) revalidateLicense(ctx context.Context, item types.BoardItem, runtime *state.RuntimeState) (bool, string) {
	if s.licenseChecker == nil {
		return true, ""
	}
	revalCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	result, err := s.licenseChecker.CheckLicense(revalCtx, item.Number)
	if err != nil {
		// The IpcLicenseChecker implementation never returns a non-nil error
		// from ctx.Done() (it resolves to a result) — this branch only fires
		// for a genuinely unexpected checker failure. Treat as transient.
		log.Printf("#%d: license re-validation error (not blocking, transient): %v", item.Number, err)
		return true, ""
	}
	if !result.Allowed && (result.Status == "revoked" || result.Status == "suspended") {
		return false, result.Status
	}

	// Refresh the snapshot so the next staleness check reuses the new
	// cacheUntil window instead of re-validating on every stage.
	tier := result.Tier
	if tier == "" {
		tier = "community"
	}
	runtime.SetLicenseSnapshot(tier, result.Allowed, result.Status, parseLicenseCacheUntil(item.Number, result.CacheUntil))
	return true, result.Status
}

// preflightIdentity asserts the resolved per-repo GitHub identity can actually
// mutate the target repo BEFORE any stage runs (#4068, epic #4067 item 1).
// Mirrors preflightLicense: a bounded check that returns (allowed, reason) and
// short-circuits the run on failure. On a blocked identity it records the
// specific reason via SetStageError so the failure surfaces as a pipeline-failed
// outcome (and flows to the epic-flag path) rather than a silent un-mergeable PR.
//
// Skippable by design: when no identityChecker is wired (CLI mode / single-repo)
// or the checker reports the repo's owner has no configured github_user, the
// gate allows the run so single-identity workspaces are unaffected.
func (s *Scheduler) preflightIdentity(ctx context.Context, item types.BoardItem, runtime *state.RuntimeState) (bool, string) {
	if s.identityChecker == nil {
		return true, "" // No checker = gate disabled (CLI / single-identity).
	}
	owner, repo := splitOwnerRepo(item.Repo)
	if owner == "" || repo == "" {
		// Can't assert against an unqualified repo; don't block on a parsing gap.
		log.Printf("#%d: identity preflight skipped — repo %q is not owner/name", item.Number, item.Repo)
		return true, ""
	}
	preflightCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	allowed, reason := s.identityChecker.CheckIdentity(preflightCtx, owner, repo, item.Number)
	if allowed {
		log.Printf("#%d: identity preflight passed for %s/%s", item.Number, owner, repo)
		return true, ""
	}
	log.Printf("#%d: identity preflight blocked: %s", item.Number, reason)
	runtime.SetStageError("pipeline-start", "identity preflight: "+reason)
	s.emitStateChanged(item.Repo, item.Number, runtime)
	// Action Center definitive-auth-failure producer (ADR 015 §F #7): the
	// fail-closed identity preflight blocked dispatch. Surface a provide_input
	// card so the operator can re-authenticate and retry.
	s.raiseAuthFailure(item.Repo, item.Number, runtime.RunID, reason)
	return false, reason
}

// PickNext selects the highest-priority unblocked ready issue to execute.
func (s *Scheduler) PickNext(ctx context.Context) (*types.BoardItem, error) {
	items, err := s.boardSvc.ListItems(ctx, "Ready")
	if err != nil {
		return nil, fmt.Errorf("fetch ready items: %w", err)
	}

	// Build parent epic map for transitive blocking checks.
	// If an epic is blocked by another epic (cross-epic dependency),
	// all of its sub-issues are transitively blocked even if they
	// don't have direct blockedBy entries.
	subIssueToEpicIdx := make(map[int]int) // sub-issue number → index in items
	for i, item := range items {
		if item.IsEpic {
			for _, si := range item.SubIssues {
				subIssueToEpicIdx[si.Number] = i
			}
		}
	}

	// Filter out blocked items and items in repos at capacity
	var candidates []types.BoardItem
	for _, item := range items {
		if item.IsPR {
			continue
		}

		// Check blocking relationships
		blocked, err := s.isBlocked(ctx, item)
		if err != nil {
			log.Printf("warn: failed to check blocking for #%d: %v", item.Number, err)
			continue
		}
		if blocked {
			continue
		}

		// Check parent epic blocking (cross-epic transitive blocking).
		// If this issue is a sub-issue of an epic that has open blockedBy
		// entries, the sub-issue is transitively blocked.
		if epicIdx, ok := subIssueToEpicIdx[item.Number]; ok {
			epicItem := items[epicIdx]
			epicBlocked := false
			for _, b := range epicItem.BlockedBy {
				if strings.EqualFold(b.State, "OPEN") {
					epicBlocked = true
					break
				}
			}
			if epicBlocked {
				log.Printf("#%d: skipping — parent epic #%d is blocked", item.Number, epicItem.Number)
				continue
			}
		}

		// Check repo concurrency limit
		s.mu.Lock()
		running := s.repoRunning[item.Repo]
		s.mu.Unlock()
		if running >= s.capForRepo(item.Repo) {
			continue
		}

		candidates = append(candidates, item)
	}

	if len(candidates) == 0 {
		return nil, nil // Nothing to run
	}

	// Sort: priority descending, then issue number ascending (oldest first)
	sort.Slice(candidates, func(i, j int) bool {
		pi := priorityRank(candidates[i].Priority)
		pj := priorityRank(candidates[j].Priority)
		if pi != pj {
			return pi < pj // Lower rank = higher priority
		}
		return candidates[i].Number < candidates[j].Number
	})

	return &candidates[0], nil
}

// RunAuto continuously polls the board and dispatches pipelines.
// A backstop sweep ticker fires every sweepMultiplier * pollInterval to close
// any epics whose sub-issues are all done but which the on-merge trigger missed.
func (s *Scheduler) RunAuto(ctx context.Context, pollInterval time.Duration) error {
	log.Printf("Starting auto-scheduler (poll every %s)", pollInterval)

	const sweepMultiplier = 10
	sweepTicker := time.NewTicker(sweepInterval(pollInterval, sweepMultiplier))
	defer sweepTicker.Stop()

	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-sweepTicker.C:
			go s.runEpicBackstopSweep(ctx)
		default:
		}

		item, err := s.PickNext(ctx)
		if err != nil {
			log.Printf("scheduler error: %v", err)
		} else if item != nil {
			log.Printf("dispatching #%d: %s (%s)", item.Number, item.Title, item.Repo)
			go s.dispatchItem(ctx, *item)
		}

		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(pollInterval):
		}
	}
}

// sweepInterval computes the backstop sweep interval from the poll interval and multiplier.
// Extracted for testability.
func sweepInterval(pollInterval time.Duration, multiplier int) time.Duration {
	return pollInterval * time.Duration(multiplier)
}

// runEpicBackstopSweep checks all repos with items on the project board for
// open epics that should be closed. This catches epics that the on-merge
// trigger missed (e.g., due to eventual consistency or a crash).
func (s *Scheduler) runEpicBackstopSweep(ctx context.Context) {
	if s.epicSvc == nil {
		return
	}

	// Collect unique repos from board items (all statuses).
	items, err := s.boardSvc.ListItems(ctx, "")
	if err != nil {
		log.Printf("backstop sweep: failed to list board items: %v", err)
		return
	}

	repos := make(map[string]struct{})
	for _, item := range items {
		if item.Repo != "" {
			repos[item.Repo] = struct{}{}
		}
	}

	for fullRepo := range repos {
		owner, repoName := splitOwnerRepo(fullRepo)
		if owner == "" || repoName == "" {
			continue
		}

		result, err := s.epicSvc.AutoClose(ctx, owner, repoName, s.projectNumber)
		if err != nil {
			log.Printf("backstop sweep %s: error: %v", fullRepo, err)
			continue
		}
		if result.Closed == 0 {
			continue
		}

		log.Printf("backstop sweep %s: closed %d stalled epic(s)", fullRepo, result.Closed)
		if s.onEpicComplete != nil {
			for _, item := range result.Summary {
				if item.Status == "closed" {
					s.onEpicComplete(fullRepo, item.EpicNumber)
				}
			}
		}
	}
}

// RunQueue processes all queued issues sequentially.
func (s *Scheduler) RunQueue(ctx context.Context) error {
	s.mu.Lock()
	queue := make([]QueueItem, len(s.queue))
	copy(queue, s.queue)
	s.queue = nil
	s.persistQueue()
	s.emitQueueChangedUnlocked()
	s.mu.Unlock()

	for _, entry := range queue {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}

		log.Printf("processing queue: #%d (%s)", entry.IssueNumber, entry.Repo)

		items, err := s.boardSvc.ListItems(ctx, "")
		if err != nil {
			log.Printf("queue: failed to fetch board: %v", err)
			continue
		}

		var item *types.BoardItem
		for _, bi := range items {
			if bi.Number == entry.IssueNumber {
				item = &bi
				break
			}
		}
		if item == nil {
			log.Printf("queue: issue #%d not found on board", entry.IssueNumber)
			continue
		}

		// Check blocking relationships before dispatching (mirrors PickNext behavior)
		blocked, err := s.isBlocked(ctx, *item)
		if err != nil {
			log.Printf("queue: failed to check blocking for #%d: %v", entry.IssueNumber, err)
			continue
		}
		if blocked {
			log.Printf("queue: skipping #%d — has open blockers", entry.IssueNumber)
			continue
		}

		s.runPipeline(ctx, *item)
	}

	return nil
}

// QueueAdd adds issues to the execution queue.
// Accepts QueueEntry (legacy) or QueueItem; QueueEntry is promoted to QueueItem internally.
// Duplicate issue numbers are silently skipped.
func (s *Scheduler) QueueAdd(entries ...QueueEntry) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, e := range entries {
		if s.queueContainsUnlocked(e.IssueNumber) {
			continue
		}
		item := QueueItem{
			Repo:        e.Repo,
			IssueNumber: e.IssueNumber,
			Priority:    e.Priority,
			Status:      "pending",
			AddedAt:     time.Now().UTC(),
			Position:    len(s.queue) + 1,
			RemoteRunID: e.RemoteRunID,
		}
		s.queue = append(s.queue, item)
	}
	s.persistQueue()
	s.emitQueueChangedUnlocked()
}

// QueueAddItem adds rich queue items to the execution queue.
// Duplicate issue numbers are silently skipped.
func (s *Scheduler) QueueAddItem(items ...QueueItem) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for i := range items {
		if s.queueContainsUnlocked(items[i].IssueNumber) {
			continue
		}
		if items[i].Status == "" {
			items[i].Status = "pending"
		}
		if items[i].AddedAt.IsZero() {
			items[i].AddedAt = time.Now().UTC()
		}
		items[i].Position = len(s.queue) + 1
		s.queue = append(s.queue, items[i])
	}
	s.persistQueue()
	s.emitQueueChangedUnlocked()
}

// queueContainsUnlocked returns true if the queue already contains an item with
// the given issue number. Caller must hold s.mu.
func (s *Scheduler) queueContainsUnlocked(issueNumber int) bool {
	for _, existing := range s.queue {
		if existing.IssueNumber == issueNumber {
			return true
		}
	}
	return false
}

// QueuePendingCount returns the number of items in the queue with status "pending".
// Used by the autonomous scheduler to reserve slots for queued items before
// dispatching candidates from the project board (#3532).
func (s *Scheduler) QueuePendingCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	count := 0
	for _, item := range s.queue {
		if item.Status == "pending" {
			count++
		}
	}
	return count
}

// queueItemRemoteRunID returns the RemoteRunID for a queued issue, or "" when not found.
// Used by runPipeline to prefer platform-assigned run IDs over locally-generated ones (#3557).
func (s *Scheduler) queueItemRemoteRunID(issueNumber int) string {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, item := range s.queue {
		if item.IssueNumber == issueNumber {
			return item.RemoteRunID
		}
	}
	return ""
}

// QueueList returns the current queue as legacy entries.
func (s *Scheduler) QueueList() []QueueEntry {
	s.mu.Lock()
	defer s.mu.Unlock()
	result := make([]QueueEntry, len(s.queue))
	for i, item := range s.queue {
		result[i] = QueueEntry{
			Repo:        item.Repo,
			IssueNumber: item.IssueNumber,
			Priority:    item.Priority,
		}
	}
	return result
}

// GetState returns the full queue state with rich metadata.
func (s *Scheduler) GetState() QueueState {
	s.mu.Lock()
	defer s.mu.Unlock()
	items := make([]QueueItem, len(s.queue))
	copy(items, s.queue)
	return QueueState{
		SchemaVersion: queueSchemaVersion,
		Status:        s.queueStatusLocked(),
		Items:         items,
		UpdatedAt:     time.Now().UTC(),
	}
}

// QueueRemove removes an issue from the queue by number.
func (s *Scheduler) QueueRemove(issueNumber int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	filtered := s.queue[:0]
	for _, e := range s.queue {
		if e.IssueNumber != issueNumber {
			filtered = append(filtered, e)
		}
	}
	s.queue = filtered
	s.recalculatePositions()
	s.persistQueue()
	s.emitQueueChangedUnlocked()
}

// PauseDeferred adds a queue item in `paused` status with the given reason,
// or if an item with the issue number already exists, marks it paused.
// Used by the baseline-CI gate (Issue #3004) to defer dispatch when the
// referenced workflow is currently red on `main`.
//
// Idempotent: calling twice with the same issue number updates the
// PausedReason without duplicating the queue entry.
func (s *Scheduler) PauseDeferred(item QueueItem, reason QueuePausedReason) {
	s.mu.Lock()
	defer s.mu.Unlock()
	r := reason
	for i := range s.queue {
		if s.queue[i].IssueNumber == item.IssueNumber {
			s.queue[i].Status = "paused"
			s.queue[i].PausedReason = &r
			s.persistQueue()
			s.emitQueueChangedUnlocked()
			return
		}
	}
	if item.Status == "" {
		item.Status = "paused"
	}
	if item.AddedAt.IsZero() {
		item.AddedAt = time.Now().UTC()
	}
	item.PausedReason = &r
	item.Position = len(s.queue) + 1
	s.queue = append(s.queue, item)
	s.persistQueue()
	s.emitQueueChangedUnlocked()
}

// ListPausedByKind returns a snapshot of queue items paused with the given
// PausedReason.Kind. Used by the daily promote sweep (Issue #3004) to find
// candidates for re-evaluation. Returns an empty slice when none match.
func (s *Scheduler) ListPausedByKind(kind string) []QueueItem {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []QueueItem
	for _, item := range s.queue {
		if item.Status != "paused" || item.PausedReason == nil {
			continue
		}
		if item.PausedReason.Kind != kind {
			continue
		}
		out = append(out, item)
	}
	return out
}

// ResumeByIssueNumber clears the paused status from the queue entry whose
// IssueNumber matches. Returns true when an item was resumed. Used by the
// promote command (Issue #3004) to lift a baseline-CI deferral when the
// last green-threshold runs are all success.
func (s *Scheduler) ResumeByIssueNumber(issueNumber int) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	for i := range s.queue {
		if s.queue[i].IssueNumber != issueNumber {
			continue
		}
		if s.queue[i].Status != "paused" {
			return false
		}
		s.queue[i].Status = "pending"
		s.queue[i].PausedReason = nil
		s.persistQueue()
		s.emitQueueChangedUnlocked()
		return true
	}
	return false
}

// QueueClear empties the queue.
func (s *Scheduler) QueueClear() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.queue = nil
	s.persistQueue()
	s.emitQueueChangedUnlocked()
}

// DequeueIndependent removes and returns up to maxSlots items that have no
// unresolved blockers among runningIssues or items ahead in the queue.
// capForRepo returns the per-repository concurrency cap: an explicit
// repository_overrides entry (by "owner/repo" then short name), else
// maxPerRepo (concurrency.per_repo_max default).
func (s *Scheduler) capForRepo(repo string) int {
	if s.repoConcurrencyOverrides != nil {
		if v, ok := s.repoConcurrencyOverrides[repo]; ok && v > 0 {
			return v
		}
		short := repo
		if i := strings.LastIndex(repo, "/"); i >= 0 {
			short = repo[i+1:]
		}
		if v, ok := s.repoConcurrencyOverrides[short]; ok && v > 0 {
			return v
		}
	}
	if s.maxPerRepo > 0 {
		return s.maxPerRepo
	}
	return 1
}

func (s *Scheduler) DequeueIndependent(ctx context.Context, maxSlots int, running []RunningItem) []QueueItem {
	// Refresh blocker states from GitHub before acquiring the lock.
	// This ensures we don't skip items whose blockers have been closed
	// since the queue was last persisted.
	s.refreshBlockerStates(ctx)

	s.mu.Lock()
	defer s.mu.Unlock()

	// Numbers already in-flight (for the blockedBy guard) and per-repo
	// in-flight counts (for the per-repo cap). Seeded from the caller's
	// running set; both grow as we dequeue this call.
	dequeuedNums := make(map[int]bool)
	repoInFlight := make(map[string]int)
	for _, r := range running {
		dequeuedNums[r.Number] = true
		repoInFlight[r.Repo]++
	}

	allQueueNums := make(map[int]bool)
	for _, item := range s.queue {
		allQueueNums[item.IssueNumber] = true
	}

	var dequeued []QueueItem
	var toRemoveIdx []int

	for i, item := range s.queue {
		if len(dequeued) >= maxSlots {
			break
		}

		// Paused guard (Issue #3001): items paused after a terminal failure
		// are skipped over until the operator explicitly resumes them via the
		// dashboard Retry / Skip / Discard actions or ResumePausedItems().
		if item.Status == "paused" {
			continue
		}

		// blockedBy guard: skip if blocked by any OPEN issue that is running, dequeued, or still in queue
		if len(item.BlockedBy) > 0 {
			blocked := false
			for _, b := range item.BlockedBy {
				if strings.EqualFold(b.State, "OPEN") &&
					(dequeuedNums[b.Number] || allQueueNums[b.Number]) {
					blocked = true
					break
				}
			}
			if blocked {
				continue
			}
		}

		// Per-repo concurrency cap: never dispatch more than capForRepo(repo)
		// issues from a single repository at once (concurrency.per_repo_max /
		// repository_overrides). The workspace ceiling (maxSlots) still bounds
		// the total across all repos. This is the guard the IPC dispatch path
		// previously lacked — two same-repo issues could run under the global
		// cap alone. An empty repo is "unknown" and not grouped (production
		// queue items always carry a repo).
		if item.Repo != "" && repoInFlight[item.Repo] >= s.capForRepo(item.Repo) {
			continue
		}

		// NOTE: epicOrder guard was removed here. It blocked items when ANY
		// sibling with lower epicOrder was still queued, regardless of actual
		// dependencies. For non-linear dependency graphs (e.g., #2058 depends
		// only on #2053 but has epicOrder=5), this caused the pipeline to stall
		// after the first sub-issue completed. The blockedBy guard above already
		// handles real intra-epic ordering via GitHub's blockedBy relationships.

		dequeued = append(dequeued, item)
		dequeuedNums[item.IssueNumber] = true
		repoInFlight[item.Repo]++
		toRemoveIdx = append(toRemoveIdx, i)
	}

	// Remove dequeued items from queue (reverse index order)
	sort.Sort(sort.Reverse(sort.IntSlice(toRemoveIdx)))
	for _, idx := range toRemoveIdx {
		s.queue = append(s.queue[:idx], s.queue[idx+1:]...)
	}

	if len(dequeued) > 0 {
		s.recalculatePositions()
		s.persistQueue()
		s.emitQueueChangedUnlocked()
	}

	return dequeued
}

// ExcludeLabels returns the resolved set of human-only labels (#317) this
// scheduler refuses to enqueue — the same list EnqueueEpic filters sub-issues
// against. Exported so callers with their own single-issue enqueue path
// (e.g. the `queue add` CLI command) can apply the identical check before
// calling QueueAdd directly.
func (s *Scheduler) ExcludeLabels() []string {
	return s.excludeLabels
}

// EnqueueEpic fetches sub-issues from GitHub and enqueues them with epicOrder.
//
// When eligibleSubIssues is non-empty, only sub-issues whose number is in the
// whitelist are enqueued. This is the drag-to-queue path where TypeScript has
// already filtered out Backlog/in-review sub-issues and ones with an open PR.
// Pass nil or an empty slice for the unfiltered autonomous path (the set of
// open sub-issues that isn't CLOSED is enqueued as before).
// @see Issue #2992 — epic drag filter.
func (s *Scheduler) EnqueueEpic(ctx context.Context, owner, repo string, epicNumber int, title string, labels []string, eligibleSubIssues []int) error {
	fullRepo := owner + "/" + repo
	log.Printf("EnqueueEpic: fetching epic #%d from %s", epicNumber, fullRepo)
	// Resolve a client scoped to the epic's repo so private cross-repo epics use
	// that repo's configured identity instead of the scheduler's startup client
	// (which is tied to the primary repo's config). See #3700.
	issueSvc := s.issueServiceFor(ctx, owner, repo)
	issue, err := issueSvc.GetIssue(ctx, owner, repo, epicNumber)
	if err != nil {
		return fmt.Errorf("get epic #%d: %w", epicNumber, err)
	}
	log.Printf("EnqueueEpic: epic #%d has %d sub-issues, title=%q", epicNumber, len(issue.SubIssues), issue.Title)

	// Build the eligible-sub-issue set when a whitelist was supplied.
	var eligibleSet map[int]struct{}
	if len(eligibleSubIssues) > 0 {
		eligibleSet = make(map[int]struct{}, len(eligibleSubIssues))
		for _, n := range eligibleSubIssues {
			eligibleSet[n] = struct{}{}
		}
		log.Printf("EnqueueEpic: filter active — eligible=%d, total=%d", len(eligibleSet), len(issue.SubIssues))
	}

	// Fetch per-sub-issue blockedBy relationships before taking the lock.
	// The epic query only returns lightweight SubIssueRef (no blocking data),
	// so we call GetIssue for each sub-issue to get its own blockedBy/blocking.
	subIssueBlockedBy := make(map[int][]types.BlockingRef, len(issue.SubIssues))
	for _, si := range issue.SubIssues {
		if strings.EqualFold(si.State, "CLOSED") {
			continue
		}
		// Determine owner/repo for this sub-issue (may differ in cross-repo epics)
		siOwner, siRepo := owner, repo
		if si.Repo != "" && si.Repo != fullRepo {
			parts := strings.SplitN(si.Repo, "/", 2)
			if len(parts) == 2 {
				siOwner, siRepo = parts[0], parts[1]
			}
		}
		// Resolve per sub-issue repo — a cross-repo sub-issue may need a
		// different configured identity than the epic's repo (#3700). The
		// resolver caches clients, so same-repo sub-issues reuse one client.
		siIssue, err := s.issueServiceFor(ctx, siOwner, siRepo).GetIssue(ctx, siOwner, siRepo, si.Number)
		if err != nil {
			log.Printf("WARN: failed to fetch blockedBy for sub-issue #%d: %v", si.Number, err)
			continue
		}
		subIssueBlockedBy[si.Number] = siIssue.BlockedBy
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	epicOrder := 0
	for _, si := range issue.SubIssues {
		// Skip closed sub-issues — no point queuing work that's already done.
		if strings.EqualFold(si.State, "CLOSED") {
			continue
		}
		// Apply the caller-provided whitelist (drag path). A nil/empty set
		// means no filter — autonomous path keeps its existing behaviour.
		if eligibleSet != nil {
			if _, ok := eligibleSet[si.Number]; !ok {
				log.Printf("EnqueueEpic: skipping sub-issue #%d — not in eligible set", si.Number)
				continue
			}
		}
		// Skip sub-issues carrying a human-only label (autonomous.exclude_labels,
		// default ["owner-action"]) — mirrors the autonomous candidate loop's
		// exclusion (autonomous.go) so epic expansion can't route a human-only
		// sub-issue into the pipeline either. Issue #317.
		if label, excluded := excludedLabelMatch(si.Labels, s.excludeLabels); excluded {
			log.Printf("EnqueueEpic: skipping sub-issue #%d — carries human-only label %q (autonomous.exclude_labels)", si.Number, label)
			continue
		}
		// Use the sub-issue's own repo if it differs from the epic's repo
		// (cross-repo epics). Without this, all sub-issues are routed to the
		// epic's repo, causing pipeline runs in the wrong repository.
		subIssueRepo := fullRepo
		if si.Repo != "" {
			subIssueRepo = si.Repo
		}
		order := epicOrder
		item := QueueItem{
			Repo:        subIssueRepo,
			IssueNumber: si.Number,
			Title:       si.Title,
			Status:      "pending",
			Labels:      labels,
			EpicOrder:   &order,
			EpicNumber:  &epicNumber,
			IsBatch:     true,
			AddedAt:     time.Now().UTC(),
		}
		// Epic-level blockers apply to all sub-issues
		for _, b := range issue.BlockedBy {
			item.BlockedBy = append(item.BlockedBy, QueueBlockingRef{
				Number: b.Number,
				Title:  b.Title,
				State:  b.State,
			})
		}
		// Sub-issue-level blockers (e.g., #1335 blockedBy #1336 within the epic)
		for _, b := range subIssueBlockedBy[si.Number] {
			item.BlockedBy = append(item.BlockedBy, QueueBlockingRef{
				Number: b.Number,
				Title:  b.Title,
				State:  b.State,
			})
		}
		// Skip if already in queue (e.g., re-enqueued individually after a
		// prior failure). Without this, the same issue can be dequeued into
		// multiple concurrent slots — causing duplicate runs.
		if s.queueContainsUnlocked(si.Number) {
			log.Printf("EnqueueEpic: skipping sub-issue #%d — already in queue", si.Number)
			epicOrder++
			continue
		}
		item.Position = len(s.queue) + 1
		s.queue = append(s.queue, item)
		epicOrder++
	}

	log.Printf("EnqueueEpic: added %d open sub-issues to queue (total queue: %d)", epicOrder, len(s.queue))
	s.persistQueue()
	s.emitQueueChangedUnlocked()
	return nil
}

// OnQueueChanged sets a callback for queue state changes.
func (s *Scheduler) OnQueueChanged(fn func(QueueState)) {
	s.onQueueChanged = fn
}

// persistQueue writes the current queue to disk atomically.
// Must be called with s.mu held.
func (s *Scheduler) persistQueue() {
	if s.workspaceRoot == "" {
		return
	}
	st := QueueState{
		SchemaVersion: queueSchemaVersion,
		Status:        s.queueStatusLocked(),
		Items:         s.queue,
		UpdatedAt:     time.Now().UTC(),
	}
	data, err := json.MarshalIndent(st, "", "  ")
	if err != nil {
		log.Printf("queue: failed to marshal state: %v", err)
		return
	}
	p := filepath.Join(s.workspaceRoot, queueStateFile)
	dir := filepath.Dir(p)
	if err := os.MkdirAll(dir, 0755); err != nil {
		log.Printf("queue: failed to create dir: %v", err)
		return
	}
	tmp := p + ".tmp"
	if err := os.WriteFile(tmp, data, 0644); err != nil {
		log.Printf("queue: failed to write queue state: %v", err)
		return
	}
	if err := os.Rename(tmp, p); err != nil {
		log.Printf("queue: failed to rename temp file: %v", err)
	}

	// Mirror the snapshot to the platform so the web dashboard shows live
	// queued/working items. Fire-and-forget — never blocks the scheduler.
	s.syncQueueToCloudLocked()
}

// syncQueueToCloudLocked pushes the current queue snapshot to the platform.
// Must be called with s.mu held (it reads s.queue). No-op when telemetry is
// disabled or unconfigured. The telemetry service stamps the machine id and
// origin and pushes in a goroutine, so this returns immediately.
func (s *Scheduler) syncQueueToCloudLocked() {
	if s.telemetrySvc == nil || !s.telemetryEnabled {
		return
	}
	items := make([]platform.QueueSyncItem, 0, len(s.queue))
	for _, it := range s.queue {
		status := queueStatusToPlatform(it.Status)
		if status == "" {
			continue // terminal/unsyncable — keep it out of the live snapshot
		}
		items = append(items, platform.QueueSyncItem{
			IssueNumber:  it.IssueNumber,
			Position:     it.Position,
			Priority:     queuePriorityFromLabels(it.Labels),
			Status:       status,
			RepoFullName: it.Repo,
			Title:        it.Title,
		})
	}
	s.telemetrySvc.SyncQueue(context.Background(), items)
}

// queueStatusToPlatform maps a local queue item status to the platform's queue
// status enum (pending|processing). Returns "" for terminal/unsyncable states
// (completed, failed, unknown) so finished items don't linger in the live cloud
// snapshot. "paused" maps to pending — it is still waiting in the queue.
func queueStatusToPlatform(status string) string {
	switch status {
	case "processing":
		return "processing"
	case "pending", "ready", "paused":
		return "pending"
	default:
		return ""
	}
}

// queuePriorityFromLabels derives the platform priority enum from issue labels
// (priority:critical|high|medium|low), mirroring the board's priorityFromLabels.
// Defaults to "medium" when no priority label is present.
func queuePriorityFromLabels(labels []string) string {
	for _, l := range labels {
		switch l {
		case "priority:critical":
			return "critical"
		case "priority:high":
			return "high"
		case "priority:medium":
			return "medium"
		case "priority:low":
			return "low"
		}
	}
	return "medium"
}

// loadQueue reads queue state from disk on startup.
//
// On startup it also runs the orchestrator-crash recovery synthesizer (Issue
// #3001): if a current-run.json sidecar is present, the previous orchestrator
// process died mid-stage. The synthesizer writes a terminal-failure RunRecord
// (failure_category: orchestrator_crash) to the daily JSONL and pauses any
// remaining queued items so the operator can investigate.
func (s *Scheduler) loadQueue() {
	if s.workspaceRoot == "" {
		return
	}
	p := filepath.Join(s.workspaceRoot, queueStateFile)
	data, err := os.ReadFile(p)
	switch {
	case os.IsNotExist(err):
		// Queue file missing — proceed to recovery scan with empty queue.
	case err != nil:
		log.Printf("queue: failed to read queue state: %v", err)
		return
	default:
		var st QueueState
		if err := json.Unmarshal(data, &st); err != nil {
			log.Printf("queue: failed to parse queue state: %v", err)
			return
		}
		s.mu.Lock()
		s.queue = st.Items
		s.mu.Unlock()
		log.Printf("queue: loaded %d items from disk (schema %s)", len(st.Items), st.SchemaVersion)
	}

	s.recoverOrchestratorCrash()
	s.reconcileOrphanedComposeProjects()
}

// reconcileOrphanedComposeProjects tears down per-issue docker compose
// stacks (`issue-NNN`) whose worktree no longer exists. Runs once at
// scheduler startup so a previous crash that bypassed CleanupWorktree
// cannot leave stale containers, volumes, networks, or images squatting
// host ports across pipeline runs. Soft-fail: errors are logged and
// teardown continues for remaining projects. See Issue #3050.
func (s *Scheduler) reconcileOrphanedComposeProjects() {
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	projects, err := dockercompose.ListIssueProjects(ctx)
	if err != nil {
		log.Printf("compose-reconcile: list compose projects failed: %v", err)
		return
	}
	if len(projects) == 0 {
		return
	}
	active := s.activeWorktreeIssues()
	for _, p := range projects {
		if active[p.IssueNumber] {
			continue
		}
		log.Printf("compose-reconcile: tearing down orphaned compose project %s (no matching worktree)", p.Name)
		if _, err := dockercompose.TeardownProject(ctx, p.Name, dockercompose.TeardownOptions{
			RemoveImages: true,
		}); err != nil {
			log.Printf("compose-reconcile: teardown of %s failed: %v", p.Name, err)
		}
	}
}

// activeWorktreeIssues parses `git worktree list --porcelain` from the
// scheduler's workspace root and returns the set of issue numbers held by
// an active worktree. Returns an empty map when git is unavailable or the
// workspace root is unset.
func (s *Scheduler) activeWorktreeIssues() map[int]bool {
	out := map[int]bool{}
	if s.workspaceRoot == "" {
		return out
	}
	cmd := exec.Command("git", "worktree", "list", "--porcelain")
	cmd.Dir = s.workspaceRoot
	data, err := cmd.Output()
	if err != nil {
		return out
	}
	for _, line := range strings.Split(strings.TrimSpace(string(data)), "\n") {
		if !strings.HasPrefix(line, "worktree ") {
			continue
		}
		path := strings.TrimSpace(strings.TrimPrefix(line, "worktree "))
		base := filepath.Base(path)
		idx := strings.LastIndex(base, "issue-")
		if idx < 0 {
			continue
		}
		tail := base[idx+len("issue-"):]
		if tail == "" {
			continue
		}
		var n int
		if _, err := fmt.Sscanf(tail, "%d", &n); err == nil && n > 0 {
			out[n] = true
		}
	}
	return out
}

// crashScanRoots returns every filesystem root whose current-run.json sidecar
// orchestrator-crash recovery must inspect: the scheduler's launch root plus
// every repo registered with the roots resolver. Since #229 a run's sidecar is
// written at its TARGET repo root (via runRoot), so a cross-repo run that
// crashes leaves its sidecar outside the launch root — scanning only the launch
// root would miss it (mirrors the IPC pipelineStateScanRoots fix, #218).
// Deduplicated: the primary repo is typically both the launch root and a
// registered path, and each run's sidecar lives under exactly one root, so
// there is no duplicate reconciliation. In CLI/auto mode the resolver is nil
// and only the launch root is returned (#239).
func (s *Scheduler) crashScanRoots() []string {
	seen := make(map[string]bool)
	var roots []string
	add := func(root string) {
		if root == "" || seen[root] {
			return
		}
		seen[root] = true
		roots = append(roots, root)
	}
	add(s.workspaceRoot)
	if s.repoRootsResolver != nil {
		for _, p := range s.repoRootsResolver() {
			add(p)
		}
	}
	return roots
}

// recoverOrchestratorCrash synthesizes a terminal-failure RunRecord from any
// stale current-run.json sidecar across every registered repo root and pauses
// the queue. Safe to call when no sidecar exists. (Issue #3001 ADR-003, #239)
func (s *Scheduler) recoverOrchestratorCrash() {
	now := time.Now().UTC()
	for _, root := range s.crashScanRoots() {
		s.recoverOrchestratorCrashAt(root, now)
	}
}

// recoverOrchestratorCrashAt reconciles a single repo root's sidecar. The
// synthesized crash record is written to the SAME root the sidecar lives under
// — the run's target repo in a multi-repo workspace — so the daily JSONL stays
// with the rest of that run's on-disk state (#229), matching where a normal run
// records history via runRoot.
//
// Guard: only synthesizes when the sidecar's StartedAt is in the past (defense
// against clock skew or stale workspace moves).
func (s *Scheduler) recoverOrchestratorCrashAt(root string, now time.Time) {
	sc, err := readCurrentRunSidecar(root)
	if err != nil {
		log.Printf("recovery: failed to read current-run sidecar at %s: %v", root, err)
		return
	}
	if sc == nil {
		return
	}
	if !sc.StartedAt.Before(now) {
		log.Printf("recovery: sidecar StartedAt %s is in the future — skipping (likely clock skew)",
			sc.StartedAt)
		removeCurrentRunSidecar(root)
		return
	}
	rec := SynthesizeOrchestratorCrashRecord(*sc, now)
	hw := state.NewHistoryWriter(root)
	if writeErr := hw.WriteRecord(rec); writeErr != nil {
		log.Printf("recovery: failed to write synthesized crash record for #%d: %v",
			sc.IssueNumber, writeErr)
	} else {
		log.Printf("recovery: synthesized terminal-failure RunRecord for #%d (orchestrator_crash, stage=%s) from %s",
			sc.IssueNumber, sc.Stage, root)
	}

	// Pause downstream queued items so they don't dispatch before the
	// operator decides what to do with the crashed run.
	reason := QueuePausedReason{
		Kind:        "upstream_failure",
		FailedRunID: FailedRunID(sc.IssueNumber, sc.StartedAt),
		Summary:     fmt.Sprintf("orchestrator crash mid-stage %s", sc.Stage),
	}
	s.mu.Lock()
	paused := s.pauseQueuedItemsUnlocked(reason)
	if paused > 0 {
		s.persistQueue()
	}
	s.mu.Unlock()
	if paused > 0 {
		log.Printf("recovery: paused %d queued item(s) after orchestrator crash (run_id=%s)",
			paused, reason.FailedRunID)
	}

	removeCurrentRunSidecar(root)
}

// queueStatusLocked returns the queue status. Must be called with s.mu held.
//
// Per ADR-005 (Issue #3001), the top-level "paused" status is *derived* from
// per-item state — true iff any item carries Status="paused". This keeps the
// two paused semantics (queue-level vs item-level) reconciled.
func (s *Scheduler) queueStatusLocked() string {
	if len(s.queue) == 0 {
		return "idle"
	}
	for _, item := range s.queue {
		if item.Status == "paused" {
			return "paused"
		}
	}
	return "waiting"
}

// pauseQueuedItemsUnlocked marks every pending/ready item in the queue as
// paused with the given reason. Used by terminal-failure handling when
// pipeline.failure_mode == "halt". Items already in a terminal state
// (completed, failed) are left as-is. Caller must hold s.mu.
//
// @see Issue #3001
func (s *Scheduler) pauseQueuedItemsUnlocked(reason QueuePausedReason) int {
	count := 0
	for i := range s.queue {
		st := s.queue[i].Status
		if st == "pending" || st == "ready" || st == "" {
			s.queue[i].Status = "paused"
			r := reason // copy so each item owns its struct
			s.queue[i].PausedReason = &r
			count++
		}
	}
	return count
}

// ResumePausedItems clears the paused status from every item with the given
// FailedRunID, restoring them to "pending" so the dispatcher picks them up
// again. Returns the number of items resumed. Used by the operator-driven
// "Skip and continue" / "Discard failed run" actions. (Issue #3001)
func (s *Scheduler) ResumePausedItems(failedRunID string) int {
	s.mu.Lock()
	defer s.mu.Unlock()
	count := 0
	for i := range s.queue {
		if s.queue[i].Status != "paused" {
			continue
		}
		if s.queue[i].PausedReason == nil {
			continue
		}
		if failedRunID != "" && s.queue[i].PausedReason.FailedRunID != failedRunID {
			continue
		}
		s.queue[i].Status = "pending"
		s.queue[i].PausedReason = nil
		count++
	}
	if count > 0 {
		s.persistQueue()
		s.emitQueueChangedUnlocked()
	}
	return count
}

// emitQueueChangedUnlocked fires the queue change callback. Must be called with s.mu held.
// The callback receives a snapshot copy of the state.
func (s *Scheduler) emitQueueChangedUnlocked() {
	if s.onQueueChanged == nil {
		return
	}
	items := make([]QueueItem, len(s.queue))
	copy(items, s.queue)
	st := QueueState{
		SchemaVersion: queueSchemaVersion,
		Status:        s.queueStatusLocked(),
		Items:         items,
		UpdatedAt:     time.Now().UTC(),
	}
	// Fire callback outside critical path — caller holds lock
	go s.onQueueChanged(st)
}

// recalculatePositions renumbers queue items 1..N. Must be called with s.mu held.
func (s *Scheduler) recalculatePositions() {
	for i := range s.queue {
		s.queue[i].Position = i + 1
	}
}

// containsInt checks if a slice contains a specific integer.
func containsInt(slice []int, val int) bool {
	for _, v := range slice {
		if v == val {
			return true
		}
	}
	return false
}

// OnStageStart sets a callback for when a stage begins.
func (s *Scheduler) OnStageStart(fn func(repo string, issue int, stage string, title string)) {
	s.onStageStart = fn
}

// OnStageComplete sets a callback for when a stage completes.
func (s *Scheduler) OnStageComplete(fn func(repo string, issue int, stage string, err error, inputTokens, outputTokens, cacheReadTokens int, costUsd float64, model string)) {
	s.onStageComplete = fn
}

// OnEpicComplete sets a callback for when an epic auto-closes.
func (s *Scheduler) OnEpicComplete(fn func(repo string, epicNumber int)) {
	s.onEpicComplete = fn
}

// OnPipelineComplete sets a callback invoked when a pipeline finishes (success or failure).
// The callback receives a snapshot of RuntimeState with accumulated token metrics.
func (s *Scheduler) OnPipelineComplete(fn func(repo string, issue int, runtime *state.RuntimeState, success bool)) {
	s.onPipelineComplete = fn
}

// OnStateChanged sets a callback invoked on every runtime state mutation.
func (s *Scheduler) OnStateChanged(fn func(repo string, issue int, runtime *state.RuntimeState)) {
	s.onStateChanged = fn
}

// OnModelFallback sets a callback invoked when a stage's model is rejected by
// the API and the run falls back to a weaker tier (#42). The IPC server wires
// this to the `pipeline.modelFallback` event so the extension surfaces a
// VSCode notification and Discord embed naming the original model, the
// rejection reason, and the substituted model.
func (s *Scheduler) OnModelFallback(fn func(repo string, issue int, stage, fromModel, toModel, reason string)) {
	s.onModelFallback = fn
}

// fireModelFallback fans a model-unavailable substitution out to the
// registered callback (best-effort — nil callback is a no-op).
func (s *Scheduler) fireModelFallback(repo string, issue int, stage state.PipelineStage, fromModel, toModel, reason string) {
	if s.onModelFallback != nil {
		s.onModelFallback(repo, issue, string(stage), fromModel, toModel, reason)
	}
}

// OnPhaseDetected sets a callback invoked when a phase marker is detected in skill output.
func (s *Scheduler) OnPhaseDetected(fn func(repo string, issue int, stage, name string, index, total int)) {
	s.onPhaseDetected = fn
}

// OnScalingDecision sets a callback invoked when the wave orchestrator makes a
// dynamic concurrency scaling decision for a wave. The UI can display this to
// show why concurrency was adjusted (config ceiling, budget constraint, etc.).
func (s *Scheduler) OnScalingDecision(fn func(epicNumber int, decision ScalingDecision)) {
	s.onScalingDecision = fn
}

// WithScalingConfig sets the agent teams scaling configuration from config.yaml.
func (s *Scheduler) WithScalingConfig(cfg *ScalingConfig) {
	s.scalingConfig = cfg
}

// SetRunningSiblingsFn injects the lookup used by the stage-exit diagnostic
// writer (#3605) to enumerate sibling pipelines at exit. nil is allowed.
// The autonomous scheduler typically wires this to its RunningSiblings.
func (s *Scheduler) SetRunningSiblingsFn(fn func(repo string, issueNumber int) []string) {
	s.runningSiblingsFn = fn
}

// SetRateLimitRemainingFn injects the lookup used by the stage-exit diagnostic
// writer (#3605) to snapshot the GitHub GraphQL bucket at exit. The function
// returns -1 when the reading is unavailable; nil disables the field.
func (s *Scheduler) SetRateLimitRemainingFn(fn func() int) {
	s.rateLimitRemainingFn = fn
}

// emitStateChanged fires the onStateChanged callback with a snapshot of the runtime state.
func (s *Scheduler) emitStateChanged(repo string, issue int, runtime *state.RuntimeState) {
	if s.onStateChanged != nil {
		s.onStateChanged(repo, issue, runtime.Snapshot())
	}
}

// stagePrerequisites maps each stage to its input context prerequisite.
// Matches the TypeScript STAGE_INPUT_PREREQUISITES in HeadlessOrchestrator.ts.
var stagePrerequisites = map[state.PipelineStage]struct {
	Stage       state.PipelineStage
	ContextType string
}{
	state.StageFeaturePlanning: {state.StageIssuePickup, "issue"},
	state.StageFeatureDev:      {state.StageFeaturePlanning, "planning"},
	state.StageFeatureValidate: {state.StageFeatureDev, "dev"},
	state.StagePRCreate:        {state.StageFeatureValidate, "validate"},
	state.StagePRMerge:         {state.StagePRCreate, "pr"},
	// spike-materialize runs after pr-merge for type:spike issues. pr-merge
	// is terminal (no output context), so the prerequisite is pr-create's
	// "pr" context — same as pr-merge itself.
	state.StageSpikeMaterialize: {state.StagePRCreate, "pr"},
}

// stageOutputContextType maps stages to their output context file prefix.
var stageOutputContextType = map[state.PipelineStage]string{
	state.StageIssuePickup:     "issue",
	state.StageFeaturePlanning: "planning",
	state.StageFeatureDev:      "dev",
	state.StageFeatureValidate: "validate",
	state.StagePRCreate:        "pr",
}

// validateStageOutput verifies that the given stage wrote its expected
// output context file. Returns nil when the file exists or when the stage
// is terminal (no output expected, e.g. pr-merge). Returns an error
// describing the missing file otherwise.
//
// A skill exiting 0 does not guarantee it produced its output context —
// silent exits, malformed runs, or early aborts all leave the file absent.
// Without this check the missing file is later discovered by the next
// stage's prerequisite validation, which (a) blames the wrong stage and
// (b) loses the model-escalation opportunity that a stage failure offers.
//
// @see Issue #2870
func validateStageOutput(stage state.PipelineStage, workspaceRoot string, issueNumber int) error {
	ctxType, ok := stageOutputContextType[stage]
	if !ok {
		return nil // terminal stage — no output context expected
	}
	outputFile := stagecontext.ContextPath(workspaceRoot, issueNumber, ctxType)
	if _, statErr := os.Stat(outputFile); os.IsNotExist(statErr) {
		return fmt.Errorf("stage %s exited 0 but did not write expected output context: %s", stage, outputFile)
	}
	return nil
}

// hasUncommittedWork returns true when the worktree has staged, unstaged, or
// untracked files — indicating work that was done but never committed. Uses a
// git subprocess (not go-git) for reliability in worktree subdirectories,
// consistent with the recovery-action shell-out pattern. Issue #3542.
func hasUncommittedWork(worktreePath string) bool {
	if worktreePath == "" {
		return false
	}
	out, err := exec.Command("git", "-C", worktreePath, "status", "--porcelain").Output()
	if err != nil {
		return false
	}
	return len(strings.TrimSpace(string(out))) > 0
}

// recoverUncommittedWork stages all changes, creates a recovery commit, and
// pushes it to origin. Best-effort and non-fatal: a failed push logs a warning
// but the local recovery commit is still preserved on the worktree. Returns an
// error only when staging or committing fails (the work is then still on disk
// for manual recovery). Issue #3542.
func recoverUncommittedWork(worktreePath string, issueNumber int, stage string) error {
	if worktreePath == "" {
		return fmt.Errorf("worktreePath is empty")
	}
	if err := exec.Command("git", "-C", worktreePath, "add", "-A").Run(); err != nil {
		return fmt.Errorf("git add: %w", err)
	}
	msg := fmt.Sprintf("feat(#%d): [auto-recovery] %s work recovered after stop-hook failure", issueNumber, stage)
	if err := exec.Command("git", "-C", worktreePath, "commit", "-m", msg).Run(); err != nil {
		return fmt.Errorf("git commit: %w", err)
	}
	if err := exec.Command("git", "-C", worktreePath, "push", "origin", "HEAD").Run(); err != nil {
		log.Printf("#%d: recovery commit push failed (non-fatal): %v", issueNumber, err)
	}
	return nil
}

// loadWorktreePath resolves the worktree directory for an issue's pipeline run.
// Prefers the durable run-state.json record (worktree_path); falls back to the
// workspace root, which is what the Go scheduler passes to RunStage as
// StageRunParams.WorktreePath. Returns "" only when neither is available.
// Issue #3542.
func loadWorktreePath(workspaceRoot string, issueNumber int) string {
	baseDir := filepath.Join(workspaceRoot, ".nightgauge", "pipeline")
	if rs, err := runstate.Load(baseDir); err == nil && rs != nil &&
		rs.IssueNumber == issueNumber && rs.WorktreePath != nil && *rs.WorktreePath != "" {
		return *rs.WorktreePath
	}
	return workspaceRoot
}

// getPipelineBudgetCeilingUSD resolves pipeline.token_budget_ceiling.ceiling_usd
// through the tier-merged config (machine → project → local via config.Load),
// mirroring the TypeScript-side getPipelineCeilingConfig resolution so the Go
// scheduler's budget-aware model escalation (Issue #3542) uses the same ceiling
// the TS ceiling enforcement does. The env override wins over all file tiers.
// Returns the maintainer-set default of $75 when the key is absent.
func getPipelineBudgetCeilingUSD(workspaceRoot string) float64 {
	const defaultCeilingUSD = 75.0
	// A runtime override raised via the Action Center `budget.raiseCeiling` verb
	// (ADR 015 §B) wins when higher than the configured/default ceiling — this is
	// the "runtime ceiling override honored before the budget_ceiling_hit
	// terminal" the ADR names. Env override still takes precedence below.
	base := defaultCeilingUSD
	if v := os.Getenv("NIGHTGAUGE_PIPELINE_TOKEN_BUDGET_CEILING_CEILING_USD"); v != "" {
		if n, err := strconv.ParseFloat(v, 64); err == nil && n > 0 {
			return maxFloat64(n, readBudgetCeilingOverrideUSD(workspaceRoot))
		}
	}
	if workspaceRoot == "" {
		return base
	}
	if cfg, err := config.Load(workspaceRoot); err == nil && cfg != nil && cfg.Pipeline != nil && cfg.Pipeline.TokenBudgetCeiling != nil {
		if n := cfg.Pipeline.TokenBudgetCeiling.CeilingUSD; n > 0 {
			base = n
		}
	}
	return maxFloat64(base, readBudgetCeilingOverrideUSD(workspaceRoot))
}

func maxFloat64(a, b float64) float64 {
	if a > b {
		return a
	}
	return b
}

// runPipeline executes the full 6-stage pipeline for a board item.
//
// The loop integrates retry, budget, and RALPH engines:
// 1. Check budget ceiling before each stage
// 2. Execute stage via StageRunner (auto mode or IPC mode)
// 3. Record tokens with BudgetEnforcer
// 4. Evaluate model escalation signals (same-stage retry with better model)
// 5. Evaluate backtrack signals (rewind to earlier stage)
// 6. For feature-validate: run RALPH loop for self-healing
func (s *Scheduler) runPipeline(ctx context.Context, item types.BoardItem) {
	// Track repo concurrency
	s.mu.Lock()
	s.repoRunning[item.Repo]++
	s.mu.Unlock()
	defer func() {
		s.mu.Lock()
		s.repoRunning[item.Repo]--
		s.mu.Unlock()
	}()

	runtime := state.NewRuntimeState(item.Repo, item.Number, item.ID)
	runtime.Title = item.Title
	// Capture the issue body at pickup (#183) so the run record + telemetry can
	// show the issue context (title, labels, body) on the dashboard run-detail
	// page without leaving the dashboard. Title/labels are already on the board
	// item; the body is fetched here (best-effort — a fetch failure leaves it
	// empty and the run proceeds). Bounded to a sensible excerpt at capture.
	runtime.Body = s.captureIssueBody(ctx, item)
	// Root this run's on-disk state (trace, runtime-{N}.json, stage-context,
	// exit-records, worktrees) at the run's TARGET repo, not the scheduler's
	// launch root, so multi-repo state is never split (#229). Falls back to the
	// execution manager's workspace root in single-repo / CLI / auto mode.
	workspaceRoot := s.runRoot(item.Repo)

	// Load run_id for telemetry correlation (#3557). Prefer the RemoteRunID
	// from the platform command payload (for remote-triggered runs); fall back
	// to the locally-generated UUID v7 from runstate.
	{
		remoteRunID := s.queueItemRemoteRunID(item.Number)
		if remoteRunID != "" {
			runtime.RunID = remoteRunID
		} else {
			baseDir := filepath.Join(workspaceRoot, ".nightgauge", "pipeline")
			if rs, err := runstate.Load(baseDir); err == nil && rs != nil && rs.RunID != "" {
				runtime.RunID = rs.RunID
			}
		}
		// Lifecycle trace fallback (#179 / ADR 013): when neither a remote id
		// nor run-state.json resolved a run_id, generate one here so the run
		// is still traced AND joined — exit records, telemetry, and the V3
		// record all read runtime.RunID, so stamping it threads one key
		// through every store.
		if runtime.RunID == "" {
			if id, idErr := runstate.NewRunID(); idErr == nil {
				runtime.RunID = id
			}
		}
	}

	// Per-run lifecycle decision trace writer (#179 / ADR 013). Nil-safe and
	// fail-open: emit calls below never block or fail the pipeline.
	tracer := trace.NewWriter(workspaceRoot, runtime.RunID, item.Repo, item.Number)

	// Register the runtime so IPC-mode phase transitions (which arrive via
	// the IPC server's pipeline.notifyPhaseTransition handler) can update
	// PhaseHistory on this runtime. Without registration the IPC path leaves
	// PhaseHistory empty for the entire run, and any extension reload
	// mid-pipeline loses phase counts on already-completed stages.
	s.registerRuntime(item.Number, runtime)
	defer s.unregisterRuntime(item.Number)

	// Reset orchestration engines for this pipeline run
	s.retryEngine.Reset()
	s.budgetEngine.Reset()
	s.ralphEngine.Reset()

	// Issue #3217: push the active performance mode into the BudgetEnforcer
	// so its decisions / log lines carry mode context and the maximum-mode
	// observe-only path can fire. `disableCeiling` mirrors
	// `MODE_PROFILES.maximum.pipeline.disableBudgetCeiling` from the TS side
	// — deliberately duplicated to avoid threading TS config into Go.
	pipelineMode := string(routing.ResolvePerformanceMode(workspaceRoot))
	disableBudgetCeiling := pipelineMode == string(routing.ModeMaximum)
	s.budgetEngine.SetPerformanceMode(pipelineMode, disableBudgetCeiling)

	// Load issue context to extract complexity and predicted model for outcome recording.
	// Non-fatal: missing or malformed context results in zero values.
	complexityScore, issueRoutingPath, predictedModel := loadIssueContext(workspaceRoot, item.Number)

	// Per-stage model_routing.minimum_model floors (#366), loaded once for the
	// run. Applied at dispatch below so an autonomous run honors a configured
	// minimum tier — parity with the TS SkillRunner's enforceMinimumModel.
	modelFloors := configModelFloors(workspaceRoot)

	// run.retryWithEscalation (ADR 015 §B): a resolution of the watchdog
	// DecisionRequest wrote a consume-once forced model tier for this issue.
	// Apply it as the predicted model for the whole run and clear it so the
	// escalation applies to this retry only.
	if forcedTier, ok := ConsumeEscalationOverride(workspaceRoot, item.Number); ok {
		log.Printf("#%d: run.retryWithEscalation applied — forcing model tier %q for this run", item.Number, forcedTier)
		predictedModel = forcedTier
	}

	// Trace the scheduler's model-routing decision for the dev stage (#179).
	// Recomputed with the stateless router so the persisted decision carries
	// the full rationale and rejected alternatives, not just the cached model
	// name from issue context (whose model_selection fields were observed
	// empty in real local data).
	{
		rec := routing.NewRouter(nil, workspaceRoot).Route(ctx, "feature-dev", complexity.Score{Value: complexityScore})
		tracer.Emit(trace.KindModelRouting, "", trace.ModelRoutingPayload{
			ForStage:         "feature-dev",
			Model:            rec.Model,
			Reasoning:        rec.Reasoning,
			EstimatedCostUSD: rec.EstimatedCost,
			Alternatives:     traceAlternatives(rec.Alternatives),
			Trigger:          "scheduler_pickup",
		})
	}

	// Re-route if performance-mode.yaml has been updated since the issue context was written.
	// Non-fatal: routing failure falls back to the cached model.
	if s.shouldReRoute(workspaceRoot, item.Number) {
		if rec, rerouteErr := s.reRouteContext(ctx, workspaceRoot, item.Number, predictedModel); rerouteErr != nil {
			log.Printf("#%d: re-routing failed: %v — using cached model %s", item.Number, rerouteErr, predictedModel)
		} else {
			predictedModel = rec.Model
			tracer.Emit(trace.KindModelRouting, "", trace.ModelRoutingPayload{
				ForStage:         "feature-dev",
				Model:            rec.Model,
				Reasoning:        rec.Reasoning,
				EstimatedCostUSD: rec.EstimatedCost,
				Alternatives:     traceAlternatives(rec.Alternatives),
				Trigger:          "performance_mode_reroute",
			})
		}
	}

	// Emit pipeline.complete callback on exit (success or failure).
	// success is set to true after all stages complete.
	pipelineSuccess := false
	// terminalFailureKind names what aborted the run when pipelineSuccess=false
	// (Issue #3001). Set by the failure-handling code paths below; "" means
	// "no terminal-kind reason was identified" — recordV2History writes a V2
	// record in that case.
	var terminalFailureKind string
	// stallRetryCount tracks the number of adaptive stall-recovery rewinds
	// already taken in this run (Issue #3005). At most 1 — the second
	// stall-kill is terminal regardless of which stage stalls.
	stallRetryCount := 0
	// recoveryAttemptsThisRun bounds the FailureRecovery registry's per-run
	// budget (Issue #3268). Each matched action — whether it actually
	// recovered or declined — counts toward the cap. Reset implicitly at
	// pipeline start; never persisted across runs.
	recoveryAttemptsThisRun := 0
	// stageFailureCategories carries per-stage failure_category overrides
	// applied to the V3 record. Used by adaptive stall-recovery (Issue #3005)
	// to mark second-stall stages as `stall-killed-after-retry`.
	stageFailureCategories := make(map[string]string)
	defer func() {
		// Issue #3542: before notifying the autonomous scheduler or reverting
		// the board, check for uncommitted work in the worktree. The #3365
		// incident lost $61.51 of complete work because the failure cleanup
		// path found no commits and reverted the board to Ready. Recover the
		// work into a commit and reclassify the terminal kind so it is NOT
		// counted as an agent failure — no LifetimeIssueFailures increment, no
		// board revert. Runs before onPipelineComplete so the autonomous
		// callback (which re-derives the kind via ClassifyTerminalKind on the
		// stage error text) sees the recoverable kind.
		if !pipelineSuccess && terminalFailureKind == "" {
			preSnap := runtime.Snapshot()
			worktreePath := loadWorktreePath(workspaceRoot, item.Number)
			if worktreePath != "" && hasUncommittedWork(worktreePath) {
				log.Printf("#%d: failure cleanup: uncommitted work detected in worktree — attempting recovery",
					item.Number)
				if recErr := recoverUncommittedWork(worktreePath, item.Number, string(preSnap.Stage)); recErr != nil {
					log.Printf("#%d: uncommitted work recovery failed: %v — worktree preserved at %s",
						item.Number, recErr, worktreePath)
				} else {
					log.Printf("#%d: uncommitted work recovered — setting terminal_failure_kind=%s",
						item.Number, TerminalKindWorktreeUncommitted)
					terminalFailureKind = TerminalKindWorktreeUncommitted
					// Overwrite the failed stage's error text with the recovery
					// marker so the autonomous onPipelineComplete wrapper — which
					// re-derives the terminal kind via ClassifyTerminalKind — sees
					// worktree_uncommitted and skips the lifetime-failure increment.
					if preSnap.Stage != "" {
						runtime.SetStageError(preSnap.Stage,
							fmt.Sprintf("worktree_uncommitted: work auto-recovered after %s failure", preSnap.Stage))
					}
				}
			}
		}

		snap := runtime.Snapshot()

		// Terminal trace event (#179): the run's outcome with the terminal
		// failure kind, closing the per-run decision trace.
		tracer.Emit(trace.KindOutcome, "", trace.OutcomePayload{
			Success:             pipelineSuccess,
			TerminalFailureKind: terminalFailureKind,
			TotalCostUSD:        snap.TotalCostUSD,
		})

		if s.onPipelineComplete != nil {
			s.onPipelineComplete(item.Repo, item.Number, snap, pipelineSuccess)
		}
		if s.telemetrySvc != nil && s.telemetryEnabled {
			// Total run duration + outcome so the platform transitions the live
			// row from 'running' to complete/failed (#1047). Copy the bool so the
			// pointer doesn't alias the loop/closure variable.
			doneSuccess := pipelineSuccess
			totalDurationMs := 0
			if !snap.StartedAt.IsZero() {
				totalDurationMs = int(time.Since(snap.StartedAt).Milliseconds())
			}
			s.telemetrySvc.EmitPipelineEvent(context.Background(), platform.PipelineEvent{
				RunID:           snap.RunID,
				IssueNumber:     item.Number,
				EventType:       "pipeline_done",
				Stage:           "",
				Timestamp:       time.Now(),
				TotalDurationMs: totalDurationMs,
				Success:         &doneSuccess,
				Metadata: map[string]interface{}{
					"success":             pipelineSuccess,
					"total_input_tokens":  snap.InputTokens,
					"total_output_tokens": snap.OutputTokens,
				},
				SchemaVersion: "1",
			})
		}
		// Failure-preservation classification (Issue #3001): when the pipeline
		// failed and no caller already classified the kind, derive it from the
		// stage error so the V3 record's terminal_failure_kind is populated.
		// We classify BEFORE recordOutcome so network-unavailable runs can
		// skip the calibration update entirely (Issue #3296).
		if !pipelineSuccess && terminalFailureKind == "" && snap.Stage != "" {
			if errMsg, ok := snap.StageErrors[string(snap.Stage)]; ok {
				terminalFailureKind = ClassifyTerminalKind(errMsg)
				if terminalFailureKind == "" {
					// Unclassifiable — fall back to the most generic kind so
					// the record still distinguishes "failed" from "complete"
					// in dashboards that group by terminal kind.
					terminalFailureKind = TerminalKindSubagentCrash
				}
			}
		}

		// Skip calibration / outcome recording for network-unavailable failures
		// (Issue #3296). The cost / duration / token data from a half-completed
		// network-killed run is environmental noise, not signal about model or
		// stage performance — feeding it to the learning recorder skews future
		// model/size predictions for no benefit.
		var outcomePrediction *state.OutcomePrediction
		if terminalFailureKind != TerminalKindNetworkUnavailable {
			outcomePrediction = s.recordOutcome(item, snap, pipelineSuccess, complexityScore, predictedModel)
		} else {
			log.Printf("#%d: skipping outcome recording (terminal_failure_kind=%s — environmental, not model)",
				item.Number, TerminalKindNetworkUnavailable)
		}

		// Write V2/V3-format execution history to daily JSONL (dashboard reads
		// these) and push the same record to platform telemetry (#261).
		s.recordV2History(item, snap, pipelineSuccess, workspaceRoot, complexityScore, issueRoutingPath, terminalFailureKind, stageFailureCategories, outcomePrediction)

		// Pause downstream queued items on terminal failure when the operator
		// has not opted into continue-queue / auto-resume (Issue #3001 ADR-004).
		// Always remove the in-flight sidecar — its purpose is detecting an
		// orchestrator crash, not preserving a normally-handled failure.
		if !pipelineSuccess {
			mode := GetPipelineFailureMode(workspaceRoot)
			if mode == FailureModeHalt {
				reason := QueuePausedReason{
					Kind:        "upstream_failure",
					FailedRunID: FailedRunID(item.Number, snap.StartedAt),
					Summary:     fmt.Sprintf("stage %s: %s", snap.Stage, terminalFailureKind),
				}
				s.mu.Lock()
				paused := s.pauseQueuedItemsUnlocked(reason)
				if paused > 0 {
					s.persistQueue()
					s.emitQueueChangedUnlocked()
				}
				s.mu.Unlock()
				if paused > 0 {
					log.Printf("#%d: failure_mode=halt — paused %d downstream queued item(s) (run_id=%s)",
						item.Number, paused, reason.FailedRunID)
				}
			} else {
				log.Printf("#%d: failure_mode=%s — leaving queue running after terminal failure",
					item.Number, mode)
			}
		}
		removeCurrentRunSidecar(workspaceRoot)

		// Clean up feature branch after pipeline completes (success or failure).
		// Only deletes if the branch exists; protected branches are never deleted.
		if s.execMgr != nil {
			if branchName := loadFeatureBranch(workspaceRoot, item.Number); branchName != "" {
				if err := s.execMgr.CleanupBranch(branchName); err != nil {
					log.Printf("#%d: branch cleanup failed for %s: %v", item.Number, branchName, err)
				} else {
					log.Printf("#%d: cleaned up feature branch %s", item.Number, branchName)
				}
			}
		}

		// Revert board status on failure so the autonomous scheduler can re-dispatch.
		// Skips revert if issue is already "In Review" (PR was opened before failure)
		// or if configured as "unchanged" (legacy behavior).
		//
		// Issue #3542: also skip the scheduler-side revert for the two
		// recoverable terminal kinds. worktree_uncommitted means the work was
		// preserved into a recovery commit; budget_ceiling_hit means the cost
		// was real spend, not a code defect. Leaving the issue "In Progress"
		// lets the pipeline (or operator) re-run the next stage. In autonomous
		// mode, revertFailedIssueStatus still resets it to Ready for
		// re-dispatch — but without a LifetimeIssueFailures increment.
		skipBoardRevert := terminalFailureKind == TerminalKindWorktreeUncommitted ||
			terminalFailureKind == TerminalKindBudgetCeiling
		if !pipelineSuccess && !skipBoardRevert && s.stateSvc != nil && s.onFailureStatus != "unchanged" {
			var targetStatus state.BoardStatus
			switch s.onFailureStatus {
			case "backlog":
				targetStatus = state.StatusBacklog
			default: // "ready"
				targetStatus = state.StatusReady
			}
			moved, err := s.stateSvc.FailPipeline(context.Background(), item.ID, targetStatus)
			if err != nil {
				log.Printf("#%d: failed to revert board status after pipeline failure: %v", item.Number, err)
			} else if moved {
				log.Printf("#%d: pipeline failed — moved issue back to %s on project board", item.Number, targetStatus)
			} else {
				log.Printf("#%d: pipeline failed — issue is In Review, leaving board status unchanged", item.Number)
			}
		}
	}()

	// Set board status to In Progress (non-fatal: board sync failure should not abort pipeline)
	if s.stateSvc != nil {
		if err := s.stateSvc.StartPipeline(ctx, item.ID, state.StageIssuePickup); err != nil {
			log.Printf("#%d: board sync unavailable, continuing: %v", item.Number, err)
		}
	}

	// License preflight check (before any stage)
	allowed, tier := s.preflightLicense(ctx, item, runtime)
	if !allowed {
		return // Pipeline blocked by license check
	}

	// Identity preflight check (#4068): assert the resolved per-repo identity
	// has push (and admin/bypass when a review ruleset gates the branch) on the
	// target repo BEFORE any stage runs. Fail fast with a surfaced reason rather
	// than producing an un-mergeable PR as the wrong (read-only) user.
	if ok, _ := s.preflightIdentity(ctx, item, runtime); !ok {
		return // Pipeline blocked by identity check
	}

	// Issue #3542: resolve the USD budget ceiling once per run — it is config,
	// not runtime state, so re-reading it inside the stall-kill retry path
	// would just re-parse the same file.
	pipelineBudgetCeilingUSD := getPipelineBudgetCeilingUSD(workspaceRoot)

	stages := []state.PipelineStage{
		state.StageIssuePickup,
		state.StageFeaturePlanning,
		state.StageFeatureDev,
		state.StageFeatureValidate,
		state.StagePRCreate,
		state.StagePRMerge,
	}

	// Dependabot issues skip feature-planning and feature-dev — dependency updates
	// are mechanical changes that don't benefit from AI planning or implementation.
	// Route: issue-pickup → feature-validate → pr-create → pr-merge.
	if gh.IsDependabotIssue(item.Labels) {
		log.Printf("#%d: Dependabot issue detected — skipping feature-planning and feature-dev stages",
			item.Number)
		runtime.SkipStage(state.StageFeaturePlanning)
		runtime.SkipStage(state.StageFeatureDev)
		for _, skipped := range []state.PipelineStage{state.StageFeaturePlanning, state.StageFeatureDev} {
			tracer.Emit(trace.KindStageSkip, string(skipped), trace.StageSkipPayload{
				Source: "dependabot",
				Reason: "dependency updates are mechanical — planning and dev stages add no value",
			})
		}
		stages = []state.PipelineStage{
			state.StageIssuePickup,
			state.StageFeatureValidate,
			state.StagePRCreate,
			state.StagePRMerge,
		}
	}

	// Spike issues append a spike-materialize stage after pr-merge that creates
	// follow-up issues from the artifact's YAML recommendations block. See
	// docs/SPIKE_CONTRACT.md and #3054.
	if gh.IsSpikeIssue(item.Labels) {
		log.Printf("#%d: spike issue detected — appending spike-materialize stage after pr-merge",
			item.Number)
		stages = append(stages, state.StageSpikeMaterialize)
	}

	// Routing fast-track (#4126): honor the deterministic routing Decision and
	// skip the stages it marks skippable, on top of any Dependabot/spike
	// adjustments above. deriveRoutingDecision re-derives from the item's
	// labels/board fields + the repo's routing config (force_full_pipeline +
	// change_rules, #4125) rather than trusting the AI-authored skip_stages in
	// issue-{N}.json — so the risk_high floor, force_full_pipeline override, and
	// config-driven change_rules all flow through deterministically. Skipped
	// stages are marked via runtime.SkipStage so they still count toward success
	// (completed + skipped == STAGE_ORDER). Only feature-planning and
	// feature-validate are skippable here; feature-dev/pr-create/pr-merge always
	// run so every pipeline still produces and merges a PR.
	routingDecision := deriveRoutingDecision(workspaceRoot, item)
	// Trace the deterministic change-class / fast-track resolution with the
	// matched rule and full rationale (#179), regardless of whether it skips
	// anything — "no fast-track" is a decision too.
	tracer.Emit(trace.KindChangeClass, "", trace.ChangeClassPayload{
		SuggestedRoute:    routingDecision.SuggestedRoute,
		MatchedChangeRule: routingDecision.MatchedChangeRule,
		SkipStages:        routingDecision.SkipStages,
		Rationale:         routingDecision.Rationale,
		RiskHigh:          routingDecision.RiskHigh,
		RiskReasons:       routingDecision.RiskReasons,
		ChangeType:        routingDecision.ChangeType,
		ComplexityScore:   routingDecision.ComplexityScore,
	})
	if skips := schedulerSkippableStages(routingDecision.SkipStages); len(skips) > 0 {
		kept := make([]state.PipelineStage, 0, len(stages))
		for _, st := range stages {
			if skips[st] {
				runtime.SkipStage(st)
				tracer.Emit(trace.KindStageSkip, string(st), trace.StageSkipPayload{
					Source:            "routing",
					Reason:            fmt.Sprintf("route %q skips %s", routingDecision.SuggestedRoute, st),
					MatchedChangeRule: routingDecision.MatchedChangeRule,
				})
				log.Printf("#%d: routing %q (rule=%q) — skipping %s stage",
					item.Number, routingDecision.SuggestedRoute, routingDecision.MatchedChangeRule, st)
				continue
			}
			kept = append(kept, st)
		}
		stages = kept
	}

	// The per-repo merge lock serializes pr-merge across parallel pipelines for
	// the same repo. It is held ONLY while the pr-merge stage runs — not across
	// the whole pipeline — because a stage can rewind out of pr-merge (e.g.
	// conflict-recovery rewinds pr-merge → feature-dev, #4072) and later re-enter
	// it. A function-scoped `defer Unlock()` would (a) self-deadlock on the
	// non-reentrant mutex when pr-merge is re-entered within one run, and (b)
	// block other issues' merges while this one redoes feature-dev. heldMergeLock
	// tracks the single lock this goroutine holds; releaseMergeLock drops it.
	var heldMergeLock *sync.Mutex
	releaseMergeLock := func() {
		if heldMergeLock != nil {
			heldMergeLock.Unlock()
			heldMergeLock = nil
		}
	}
	defer releaseMergeLock() // safety net: release on any return path

	// Carries the file-named terminal reason captured when the conflict-recovery
	// bound is exhausted, applied at the terminal SetStageError so the failure
	// names the conflicting files even on the skill-crash path where the on-disk
	// escalation never fired. Hoisted OUT of the loop so a model-escalation
	// `continue` (which re-runs the SAME stage) does not discard it;
	// conflictExhaustionStage scopes it to its stage so it can never bleed into a
	// later stage's terminal failure (#4072 review).
	conflictExhaustionReason := ""
	conflictExhaustionStage := state.PipelineStage("")

	stageIdx := 0
	for stageIdx < len(stages) {
		stage := stages[stageIdx]

		// Drop a conflict-exhaustion reason captured for a different stage (a
		// model-escalation retry stays on the same stage and must keep it).
		if conflictExhaustionReason != "" && stage != conflictExhaustionStage {
			conflictExhaustionReason = ""
		}

		select {
		case <-ctx.Done():
			return
		default:
		}

		// For merge stage, acquire the per-repo lock; for any other stage, drop a
		// lock still held from a prior pr-merge iteration that rewound away.
		if stage == state.StagePRMerge {
			if heldMergeLock == nil {
				lock := s.getMergeLock(item.Repo)
				lock.Lock()
				heldMergeLock = lock
			}
		} else {
			releaseMergeLock()
		}

		// Check pipeline budget before running stage
		budgetDecision := s.budgetEngine.CheckPipelineBudget()
		if budgetDecision.ShouldTerminate {
			log.Printf("#%d: pipeline budget exceeded (%d tokens > %d ceiling, $%.4f accumulated, mode=%s) — aborting",
				item.Number, budgetDecision.UsedTokens, budgetDecision.CeilingTokens, runtime.TotalCostUSD, budgetDecision.PerformanceMode)
			runtime.SetStageError(stage, budgetDecision.Reason)
			s.emitStateChanged(item.Repo, item.Number, runtime)
			// Issue #3001: record the terminal kind so the V3 record names what
			// stopped the run, not just "failed".
			terminalFailureKind = TerminalKindBudgetExceeded
			// Action Center budget-ceiling producer (ADR 015 §F #4): surface an
			// approve card offering budget.raiseCeiling (a runtime override that
			// getPipelineBudgetCeilingUSD honors) + retry, or halt. Propose a 50%
			// raise above the current ceiling, floored above the current spend.
			proposed := getPipelineBudgetCeilingUSD(workspaceRoot) * 1.5
			if proposed <= runtime.TotalCostUSD {
				proposed = runtime.TotalCostUSD * 1.5
			}
			s.raiseBudgetCeilingHit(item.Repo, item.Number, runtime.RunID, runtime.TotalCostUSD, proposed)
			return
		}
		if budgetDecision.ShouldWarn {
			log.Printf("#%d: pipeline budget warning: %s (mode=%s)", item.Number, budgetDecision.Reason, budgetDecision.PerformanceMode)
		}

		// Validate context prerequisite (previous stage output exists). Resolve
		// the prerequisite skip-aware (#4126): when the immediate prior stage was
		// skipped (docs-only skips planning + validate) its context was never
		// written, so consume the nearest upstream stage that actually ran. Check
		// in the worktree where the stage executed, not the main root.
		if ctxType, ok := effectivePrereqContextType(stage, runtime); ok {
			ctxPath := stagecontext.ContextPath(stageWorkspace(runtime, workspaceRoot), item.Number, ctxType)
			if _, err := os.Stat(ctxPath); os.IsNotExist(err) {
				log.Printf("#%d: stage %s prerequisite missing: %s context (resolved skip-aware)",
					item.Number, stage, ctxType)
				return // Pipeline failed — missing prerequisite
			}
		}

		// Find and read SKILL.md
		skillPath, err := execution.FindSkillFile(workspaceRoot, stage)
		if err != nil {
			log.Printf("#%d: %v", item.Number, err)
			return
		}

		skillData, err := execution.ReadSkillFile(skillPath)
		if err != nil {
			log.Printf("#%d: failed to read skill: %v", item.Number, err)
			return
		}

		// Platform skill resolution for paid tiers
		var resolvedSkillContent string
		var skillFallbackUsed bool
		if s.skillService != nil && tier != "" && tier != "community" {
			opts := &platform.SkillResolveOptions{
				ComplexityScore: complexityScore,
			}
			resolved, resolveErr := s.skillService.Resolve(ctx, string(stage), opts)
			if resolveErr == nil {
				resolvedSkillContent = resolved.Content
				log.Printf("#%d: stage %s — using platform skill (tier=%s, variant=%s)",
					item.Number, stage, tier, resolved.Variant)
			} else {
				skillFallbackUsed = true
				log.Printf("#%d: stage %s — platform skill resolve failed, using community skill: %v",
					item.Number, stage, resolveErr)
			}
		}

		// Build prompt for stdin delivery. The absolute skill dir rewrites
		// skill-relative read directives so they resolve from cross-repo
		// worktrees (#196).
		prompt := execution.BuildPrompt(stage, skillData.Content, item.Number, filepath.Dir(skillData.Path))

		// Epic project-memory forward injection (#4096): for a sub-issue that
		// belongs to an epic, append the bounded, semi-trusted context that
		// completed sibling sub-issues accumulated — on the two stages where
		// codebase context helps. Returns "" (no-op) for non-epic work or when
		// nothing has accumulated yet, keeping those prompts byte-identical.
		if item.ParentNumber > 0 &&
			(stage == state.StageFeaturePlanning || stage == state.StageFeatureDev) {
			if section := renderEpicContextForPrompt(s.workspaceRoot, item.ParentNumber); section != "" {
				prompt += section
			}
		}

		// Determine context file paths (skip-aware input; worktree-rooted).
		ws := stageWorkspace(runtime, workspaceRoot)
		var contextFile string
		if ctxType, ok := effectivePrereqContextType(stage, runtime); ok {
			contextFile = stagecontext.ContextPath(ws, item.Number, ctxType)
		}
		var outputFile string
		if ctxType, ok := stageOutputContextType[stage]; ok {
			outputFile = stagecontext.ContextPath(ws, item.Number, ctxType)
		}

		// Resolve model — use escalation override if set, otherwise use predicted model
		model := predictedModel
		if override := s.retryEngine.CurrentModel(string(stage)); override != "" {
			model = override
		}
		// Apply the per-stage model_routing.minimum_model floor (#366) so an
		// autonomous run honors a configured minimum tier — parity with the TS
		// SkillRunner's enforceMinimumModel. Placed before ApplyDowngrades so a
		// model-unavailable downgrade (#42) stays the final safety net (a floor
		// must never force a run back onto a tier the API just rejected), and
		// before RecordStageModel so attribution reflects the floored tier.
		if floor := stageModelFloor(modelFloors, string(stage)); floor != "" {
			if raised := enforceMinimumModel(model, floor); raised != model {
				log.Printf("#%d: stage %s — model_routing.minimum_model floor %q raised %s → %s",
					item.Number, stage, floor, model, raised)
				model = raised
			}
		}
		// Reroute through any sticky model-unavailable downgrades (#42): once
		// the API rejected a tier this run, every later stage resolving to it
		// runs on the substituted tier instead of re-failing identically.
		model = s.retryEngine.ApplyDowngrades(model)

		// For pr-create, escalate from haiku to sonnet when the diff is large.
		// Large diffs cause haiku to stall before producing a complete PR.
		if stage == state.StagePRCreate && isHaikuModel(model) {
			threshold := getLargeDiffThreshold(workspaceRoot)
			if threshold > 0 {
				if diffLines := getDiffLineCount(workspaceRoot); diffLines > threshold {
					log.Printf("#%d: pr-create diff is %d lines (threshold %d) — escalating to sonnet",
						item.Number, diffLines, threshold)
					model = routing.ModelSonnet
				}
			}
		}

		// For feature-validate, disable haiku auto-routing unless the dev-stage
		// build verification already passed. Haiku is too lightweight to reliably
		// run real build/test commands without shortcutting them (Issue #3041).
		if stage == state.StageFeatureValidate && isHaikuModel(model) {
			if !devContextBuildPassed(workspaceRoot, item.Number) {
				log.Printf("#%d: feature-validate: dev build_verification not passed — disabling haiku, escalating to sonnet",
					item.Number)
				model = routing.ModelSonnet
			}
		}

		// pr-merge's LLM path only runs when the deterministic runner punted —
		// exclusively the judgment-heavy instances (blocked merge state,
		// failing checks, dirty state). Issue size does not predict punt
		// difficulty, so haiku is never the right tier here regardless of what
		// config/calibration resolved (#197 — bowlsheet#233's haiku pr-merge
		// improvised an admin bypass). Floor: sonnet.
		if stage == state.StagePRMerge && isHaikuModel(model) {
			log.Printf("#%d: pr-merge LLM path runs only on deterministic punts — flooring haiku to sonnet (#197)",
				item.Number)
			model = routing.ModelSonnet
		}

		// Behavioral preamble for the Haiku tier (#77 → #106): measured
		// +7.9 composite / +11.1pp pass rate on Haiku, ≈0 on Sonnet/Opus
		// (measured skip — Haiku only). Applied after ALL escalations so a
		// stage that just escalated off Haiku gets the unmodified prompt.
		prompt = execution.WithBehavioralPreamble(prompt, model)

		runtime.BeginStage(stage)
		// Resolve the perf mode once: reused for the per-stage history record
		// (RecordStageMode) and the stage_started telemetry event (mapped to the
		// dashboard's vocabulary, omitted when unresolvable — e.g. 'frontier').
		stagePerfMode := routing.ResolvePerformanceMode(workspaceRoot)
		runtime.RecordStageMode(stage, string(stagePerfMode))
		// Attribute the stage to the model that actually dispatches (#42) —
		// after escalation overrides, sticky downgrades, and the pr-create /
		// feature-validate adjustments above.
		runtime.RecordStageModel(stage, model)
		s.emitStateChanged(item.Repo, item.Number, runtime)

		// Crash-recovery sidecar (Issue #3001): record the in-flight run at
		// stage-start. Removed on clean completion (success and failure paths
		// both call removeCurrentRunSidecar). A stale sidecar at scheduler
		// startup signals an orchestrator process crash → the synthesizer in
		// loadQueue writes a terminal-failure RunRecord and pauses the queue.
		if sidecarErr := writeCurrentRunSidecar(workspaceRoot, CurrentRunSidecar{
			IssueNumber: item.Number,
			Repo:        item.Repo,
			ItemID:      item.ID,
			Title:       item.Title,
			StartedAt:   runtime.StartedAt,
			Stage:       string(stage),
			StageStart:  time.Now().UTC(),
			PID:         os.Getpid(),
		}); sidecarErr != nil {
			log.Printf("#%d: failed to write current-run sidecar: %v", item.Number, sidecarErr)
		}

		// Check license expiry / re-validate before each stage (#4156).
		//
		// IsLicenseExpired() is true once now() has passed the cached
		// snapshot's CacheUntil (the ~5-minute TTL from the original
		// preflight/last re-validation) — reused here as the re-validation
		// cadence so this doesn't introduce a second timer concept.
		//
		// Two independent things happen on staleness:
		//  1. Passive notice (pre-existing): flag once so onPipelineComplete
		//     emits pipeline.licenseExpired after the run finishes.
		//  2. Active re-validation: actually re-check with the server. A
		//     CONFIRMED revoked/suspended result HALTS the run immediately —
		//     continuing to execute under a definitively invalid license
		//     defeats license enforcement. Any other outcome (including a
		//     transient unreachable-server timeout) does NOT block — see
		//     revalidateLicense / IpcLicenseChecker for the fail-open/closed
		//     split — so flaky connectivity never falsely blocks a run that
		//     started with a valid license.
		if runtime.IsLicenseExpired() {
			if !runtime.HasLicenseExpiredMidRun() {
				log.Printf("#%d: license expired mid-pipeline at stage %s — run continues, notify on completion",
					item.Number, stage)
				runtime.SetLicenseExpiredMidRun(true)
			}
			if stillAllowed, confirmedStatus := s.revalidateLicense(ctx, item, runtime); !stillAllowed {
				log.Printf("#%d: license re-validation confirmed %s mid-pipeline at stage %s — halting run",
					item.Number, confirmedStatus, stage)
				runtime.SetStageError(stage, fmt.Sprintf("license %s — execution halted", confirmedStatus))
				s.emitStateChanged(item.Repo, item.Number, runtime)
				return
			}
		}

		// Trace the stage boundary with its dispatch context (#179).
		tracer.Emit(trace.KindStageStart, string(stage), trace.StageStartPayload{
			Model:           model,
			PerformanceMode: string(stagePerfMode),
			EscalatedRetry:  s.retryEngine.CurrentModel(string(stage)) != "",
		})

		if s.onStageStart != nil {
			s.onStageStart(item.Repo, item.Number, string(stage), item.Title)
		}
		if s.telemetrySvc != nil && s.telemetryEnabled {
			s.telemetrySvc.EmitPipelineEvent(ctx, platform.PipelineEvent{
				RunID:       runtime.RunID,
				IssueNumber: item.Number,
				EventType:   "stage_started",
				Stage:       string(stage),
				Timestamp:   time.Now(),
				// Run-creation context so the platform materialises a live
				// status='running' row on the first stage (#1047).
				Repo:   item.Repo,
				Origin: "local_cli",
				// Branch + perf mode enrich the running row's branch column and
				// mode badge. Branch is empty on issue-pickup (resolved only after
				// it completes); later stages carry it and the platform enriches
				// the row. Mode is mapped to the dashboard vocabulary and omitted
				// when unresolvable ('frontier' → "").
				Branch:        runtime.Branch,
				Mode:          routing.DashboardPerformanceMode(stagePerfMode),
				Metadata:      map[string]interface{}{"model": model},
				SchemaVersion: "1",
			})
		}

		// Update board with current stage
		if s.stateSvc != nil {
			_ = s.stateSvc.SetPipelineStage(ctx, item.ID, stage)
		}

		log.Printf("#%d: stage %s — model=%s, tools=%d, prompt=%d chars",
			item.Number, stage, model, len(skillData.AllowedTools), len(prompt))

		// Build phase event callback for this stage — always record in runtime
		phaseEventFn := func(pStage, pName string, pIndex, pTotal int) {
			runtime.BeginPhase(stage, pName, pIndex, pTotal)
			if s.onPhaseDetected != nil {
				s.onPhaseDetected(item.Repo, item.Number, pStage, pName, pIndex, pTotal)
			}
		}

		// Check if this is an escalated retry — load retro findings if so
		isEscalated := s.retryEngine.CurrentModel(string(stage)) != ""
		retroFindings := ""
		if isEscalated {
			retroFindings = loadLatestRetro(workspaceRoot, item.Number, string(stage))
			if retroFindings != "" {
				log.Printf("#%d: stage %s escalated retry — injecting retro findings (%d chars)",
					item.Number, stage, len(retroFindings))
			}
		}

		// Run the stage via StageRunner interface
		stageParams := StageRunParams{
			Stage:       stage,
			IssueNumber: item.Number,
			Repo:        item.Repo,
			Model:       model,
			// Stage-aware + model-aware last-resort context deadline (#73).
			// Replaces a blind 30-min literal that killed frontier-mode Fable
			// stages before their own progress-gated hard cap could apply.
			Timeout:           routing.ResolveStageTimeout(string(stage), model),
			SkillPath:         skillPath,
			ContextFile:       contextFile,
			OutputFile:        outputFile,
			TargetRepo:        item.Repo,
			WorktreePath:      workspaceRoot, // Working directory for Claude CLI (IPC mode)
			Runtime:           runtime,
			AllowedTools:      skillData.AllowedTools,
			Prompt:            prompt,
			PhaseEventFn:      phaseEventFn,
			SkillContent:      resolvedSkillContent, // Platform-resolved; empty = TypeScript uses local file
			SkillFallbackUsed: skillFallbackUsed,    // True when platform failed for paid tier
			RetroFindings:     retroFindings,
			IsEscalatedRetry:  isEscalated,
		}

		// stageStartedAt anchors the diagnostic record's ElapsedMs fallback
		// when the TS SkillRunner doesn't forward its own ElapsedMs (e.g.
		// deterministic-merge fast path or pre-TS-update builds). Issue #3605.
		stageStartedAt := time.Now()

		// Deterministic-first hook for pr-merge (Issue #3264). Pre-flight via
		// `gh pr view`; if the PR is already MERGED or clean+mergeable+CI-green,
		// merge directly (zero LLM tokens) and skip the skill. On punt, fall
		// through to the existing skill path with execution_path="llm". The
		// post-stage verifyPRMerged gate runs regardless of which path produced
		// the result. See docs/PR_MERGE_STAGE.md.
		var result *StageRunResult
		var stageRunErr error

		// Per-stage adapter resolution (#54): only meaningful on the
		// Go-direct path (execMgr holds an adapter) and when the invocation
		// did not pin one via --adapter / NIGHTGAUGE_ADAPTER.
		var adapterResolveErr error
		if s.adapterExplicit == "" && s.execMgr != nil && s.execMgr.HasAdapter() {
			adapterResolveErr = s.applyStageAdapter(string(stage), workspaceRoot)
		}

		deterministicMerged, detMergePRState, mergeRateLimited := s.tryDeterministicPRMerge(ctx, stage, runtime, item, workspaceRoot)
		deterministicCreated := false
		createRateLimited := false
		if !deterministicMerged && !mergeRateLimited {
			deterministicCreated, createRateLimited = s.tryDeterministicPRCreate(ctx, stage, runtime, item, workspaceRoot)
		}
		// prStageRateLimited is true when the deterministic pr-merge/pr-create
		// path declined because GitHub is rate-limited. The LLM path is skipped
		// (it would re-shell `gh` into the same exhausted bucket); the failure
		// block below short-circuits this to the environmental recovery path
		// (#3896) via a github-quota-low marker. Issue #3976.
		prStageRateLimited := mergeRateLimited || createRateLimited
		switch {
		case deterministicMerged || deterministicCreated:
			result = &StageRunResult{ExitCode: 0}
		case adapterResolveErr != nil:
			result = &StageRunResult{ExitCode: 1}
			stageRunErr = adapterResolveErr
		case prStageRateLimited:
			result = &StageRunResult{ExitCode: 1}
			stageRunErr = fmt.Errorf("github-quota-low: %s deterministic path rate-limited; deferring until GitHub bucket reset (LLM fallback skipped to avoid quota/token burn) [#3976]", stage)
		default:
			// Wrap the stage context so CancelAllForNetworkOutage can abort
			// this LLM subprocess directly when the TS watchdog detects an
			// extended connectivity outage (Issue #3296).
			stageCtx, cancelStage := context.WithCancelCause(ctx)
			s.registerActiveStage(item.Number, cancelStage)
			result, stageRunErr = s.stageRunner.RunStage(stageCtx, stageParams)
			s.unregisterActiveStage(item.Number)
			// If the cancellation cause was ErrNetworkUnavailable, surface a
			// typed error to the failure handler so it can classify the
			// terminal kind correctly (skip retro/calibration, reset to Ready).
			if cause := context.Cause(stageCtx); errors.Is(cause, ErrNetworkUnavailable) {
				stageRunErr = ErrNetworkUnavailable
			}
			cancelStage(nil) // release ctx resources
		}
		err = stageRunErr

		exitCode := 0
		inputTokens, outputTokens, cacheReadTokens := 0, 0, 0
		var actualCostUsd float64
		if result != nil {
			exitCode = result.ExitCode
			inputTokens = result.InputTokens
			outputTokens = result.OutputTokens
			cacheReadTokens = result.CacheReadTokens
			actualCostUsd = result.CostUsd
			// Capture last_output_lines so the V3 record's StageDetail carries
			// the trailing stderr/stdout snippet when this stage fails terminally.
			// Issue #3207 — IPC-mode stall-kill / cost-cap kills now propagate
			// the executor-captured tail through StageRunResult instead of being
			// silently discarded.
			if result.LastOutputLines != "" {
				runtime.RecordStageOutputTail(stage, result.LastOutputLines)
			}
		}

		// #91 served-model attribution: the claude CLI can silently retry a
		// safety-refused turn on a fallback model (model_refusal_fallback)
		// and still exit 0, so the model that served the stage is not
		// guaranteed to be the one requested. Cost, exit-record, telemetry,
		// and history sinks below use servedModel; routing, escalation, and
		// retry decisions stay on the requested `model`.
		// See docs/spikes/fable-5-behavior-porting.md §8.3.
		servedModel := model
		if result != nil && result.ServedModel != "" {
			servedModel = result.ServedModel
		}
		if result != nil && result.RefusalFallbackTo != "" {
			runtime.RecordModelRefusalFallback(stage, result.RefusalFallbackFrom,
				result.RefusalFallbackTo, result.RefusalFallbackCategory)
			log.Printf("#%d: stage %s — claude CLI model_refusal_fallback: %s → %s (category %q); attributing served model (#91)",
				item.Number, stage, result.RefusalFallbackFrom, result.RefusalFallbackTo, result.RefusalFallbackCategory)
			if s.telemetrySvc != nil && s.telemetryEnabled {
				s.telemetrySvc.EmitPipelineEvent(ctx, platform.PipelineEvent{
					RunID:       runtime.RunID,
					IssueNumber: item.Number,
					EventType:   "model_refusal_fallback",
					Stage:       string(stage),
					Timestamp:   time.Now(),
					Metadata: map[string]interface{}{
						"original_model":       result.RefusalFallbackFrom,
						"fallback_model":       result.RefusalFallbackTo,
						"api_refusal_category": result.RefusalFallbackCategory,
					},
					SchemaVersion: "1",
				})
			}
		}
		if servedModel != model {
			// Re-record so the V2 per-stage ModelSelection carries the
			// serving model — the dispatch-time RecordStageModel only knew
			// the request.
			runtime.RecordStageModel(stage, servedModel)
		}

		// Record tokens with budget enforcer
		s.budgetEngine.RecordStageTokens(string(stage), inputTokens, outputTokens)

		// Use actual cost from Claude CLI when available; fall back to calculated cost
		if actualCostUsd > 0 {
			runtime.CompleteStageWithCost(exitCode, inputTokens, outputTokens, cacheReadTokens, actualCostUsd)
		} else {
			runtime.CompleteStage(exitCode, inputTokens, outputTokens, servedModel)
		}
		s.emitStateChanged(item.Repo, item.Number, runtime)

		// Populate metadata after specific stages for Discord/UI enrichment
		switch stage {
		case state.StageIssuePickup:
			if b := loadFeatureBranch(workspaceRoot, item.Number); b != "" {
				runtime.SetBranch(b)
			}
			// Auto-create epic branch if this is a sub-issue and config allows
			if item.ParentNumber != 0 {
				s.ensureEpicBranchForItem(ctx, workspaceRoot, item)
			}
		case state.StageFeatureValidate:
			if gr := loadGateResults(workspaceRoot, item.Number); len(gr) > 0 {
				runtime.SetGateResults(gr)
				// Trace the quality gates (build/lint/test) the validate
				// stage ran (#179). Result vocabulary is "pass" | "catch".
				for _, q := range gr {
					tracer.Emit(trace.KindGateResult, string(stage), trace.GateResultPayload{
						GateName:   q.GateName,
						Source:     "quality_gate",
						Passed:     q.Result == "pass",
						Reason:     q.ErrorSummary,
						DurationMs: q.DurationMs,
					})
				}
			}
		case state.StagePRCreate:
			if u := loadPrUrl(stageWorkspace(runtime, workspaceRoot), item.Number); u != "" {
				runtime.SetPrUrl(u)
				s.emitStateChanged(item.Repo, item.Number, runtime)
			}
		}

		// Stage post-condition gate (Issue #3266). Runs only when the skill
		// reported success (err == nil && exitCode == 0); a failed skill is
		// already on the failure path. A failed gate synthesizes an error
		// that maps onto the existing stage-failure branch below — the
		// retry/backtrack engine handles it like any other stage failure.
		// gateRes is hoisted out of the success block so the failure branch
		// (Issue #3268 FailureRecovery registry) can read the gate's Kind
		// and Reason when constructing a StageFailure.
		var gateRes gates.GateResult
		var gateRan bool
		if err == nil && exitCode == 0 {
			if gate, ok := s.stageGates[stage]; ok && gate != nil {
				// Trivial-change gate relaxation (#4128): for the PR gates, opt-in
				// config can relax the retry/sleep overhead when the AUTHORITATIVE
				// post-dev diff classifies as a configured trivial class. Drift-safe:
				// the classification runs on the real changed files.
				gateCtx := ctx
				if stage == state.StagePRCreate || stage == state.StagePRMerge {
					gateCtx = s.gateRelaxContext(ctx, stage, workspaceRoot, item, runtime)
				}
				// Gates inspect the stage's output (context files), which live in
				// the worktree on isolated runs — check there, not the main root.
				gateRes = gate.Verify(gateCtx, item.Number, stageWorkspace(runtime, workspaceRoot))
				gateRan = true
				runtime.AppendStageGateResult(stage, gateRes.ToStageGateResult())
				tracer.Emit(trace.KindGateResult, string(stage), trace.GateResultPayload{
					GateName:   gateRes.GateName,
					Source:     "stage_gate",
					Passed:     gateRes.Passed,
					ResultKind: string(gateRes.Kind),
					Reason:     gateRes.Reason,
					Evidence:   gateRes.Evidence,
					DurationMs: gateRes.DurationMs,
					Trigger:    "post_stage",
				})

				// Capture the authoritative change_class once feature-dev has
				// produced the diff (#4129) — DURING the run, while the worktree
				// still exists, so the run record gets the real class even after
				// the worktree is archived. Best-effort; empty stays empty.
				if gateRes.Passed && runtime.AuthoritativeChangeClass == "" {
					if cc := authoritativeChangeClass(stageWorkspace(runtime, workspaceRoot)); cc != "" {
						runtime.SetAuthoritativeChangeClass(cc)
					}
				}
				if !gateRes.Passed {
					log.Printf("#%d: stage %s post-condition gate FAILED: %s",
						item.Number, stage, gateRes.Reason)
					// KindNoOp on a clean exit is the "ended a turn on a
					// promise" failure (#74): the skill exited 0 but produced
					// no state change. Stamp a distinct marker so
					// ClassifyTerminalKind records premature_turn_end instead
					// of a generic gate failure (pr-merge's no-op still
					// classifies as pr_merge_unmerged — its matcher runs
					// first and the gate reason phrasing is preserved here).
					if gateRes.Kind == gates.KindNoOp {
						err = fmt.Errorf("premature turn end: stage exited 0 with no state change (gate no-op): %s", gateRes.Reason)
					} else {
						err = fmt.Errorf("stage gate failed: %s", gateRes.Reason)
					}
					exitCode = 2
				} else {
					log.Printf("#%d: stage %s post-condition gate passed (%s)",
						item.Number, stage, gateRes.Reason)
				}

				// Anomaly detection (Issue #3267). Atomic-eligible stages that
				// run via the LLM path AND whose gate still passed AND whose
				// stage cost crossed the floor get an anomaly record persisted
				// on V2StageDetail.Anomalies. Non-blocking: a successful run
				// is not turned into a failure, only flagged.
				if gates.IsAtomicEligible(stage) {
					anomalyCost := actualCostUsd
					if anomalyCost == 0 {
						anomalyCost = tokens.CalculateCost(servedModel, inputTokens, outputTokens)
					}
					anomalyFloor := getAnomalyFloorUSD(workspaceRoot)
					executionPath := runtime.StageExecutionPath(stage)
					if anomaly := gates.DetectAtomicLLMOverrun(stage, executionPath, anomalyCost, gateRes.Passed, anomalyFloor); anomaly != nil {
						runtime.AppendStageAnomaly(stage, anomaly.ToState())
						log.Printf("#%d: Anomaly: LLM run on atomic-eligible stage stage=%s cost=$%.4f predicate=%q",
							item.Number, stage, anomaly.StageCostUSD, anomaly.DeterministicPredicate)
						if s.telemetrySvc != nil && s.telemetryEnabled {
							s.telemetrySvc.EmitPipelineEvent(ctx, platform.PipelineEvent{
								RunID:       runtime.RunID,
								IssueNumber: item.Number,
								EventType:   "pipeline.anomaly",
								Stage:       string(stage),
								Timestamp:   time.Now(),
								Metadata: map[string]interface{}{
									"anomaly_kind":            string(anomaly.Kind),
									"execution_path":          anomaly.ExecutionPath,
									"stage_cost_usd":          anomaly.StageCostUSD,
									"deterministic_predicate": anomaly.DeterministicPredicate,
									"floor_usd":               anomalyFloor,
								},
								SchemaVersion: "1",
							})
						}
					}
				}
			}
		}

		// #3835 WS1: reconcile false-alarm failures on terminal stages. A
		// terminal stage (pr-create, pr-merge) can complete the real work —
		// create or merge the PR — and then exit non-zero on a SECONDARY error
		// (API 429, a post-merge step glitch, an interactive AskUserQuestion
		// fallback, the ruleset-precheck false positive). Previously that paged
		// the operator and paused autonomous on work that actually landed
		// (#3806 recorded a pr-merge failure 12s AFTER its PR merged). The
		// post-condition gate is the source of truth for "did the work land"
		// (#3266) but only runs on a clean exit above — so re-run it here when
		// the skill reported failure. If it passes, the work is done: clear the
		// error instead of failing, paging, and retrying completed work.
		if (err != nil || exitCode != 0) && !gateRan && isTerminalStage(stage) {
			if gate, ok := s.stageGates[stage]; ok && gate != nil {
				recon := gate.Verify(ctx, item.Number, stageWorkspace(runtime, workspaceRoot))
				gateRan = true
				gateRes = recon
				runtime.AppendStageGateResult(stage, recon.ToStageGateResult())
				tracer.Emit(trace.KindGateResult, string(stage), trace.GateResultPayload{
					GateName:   recon.GateName,
					Source:     "stage_gate",
					Passed:     recon.Passed,
					ResultKind: string(recon.Kind),
					Reason:     recon.Reason,
					Evidence:   recon.Evidence,
					DurationMs: recon.DurationMs,
					Trigger:    "terminal_reconcile",
				})
				if recon.Passed {
					log.Printf("#%d: stage %s reported failure (exit=%d) but post-condition gate passed (%s) — reconciling to success (#3835)",
						item.Number, stage, exitCode, recon.Reason)
					err = nil
					exitCode = 0
					if s.telemetrySvc != nil && s.telemetryEnabled {
						s.telemetrySvc.EmitPipelineEvent(ctx, platform.PipelineEvent{
							RunID:         runtime.RunID,
							IssueNumber:   item.Number,
							EventType:     "pipeline.failure_reconciled",
							Stage:         string(stage),
							Timestamp:     time.Now(),
							Metadata:      map[string]interface{}{"gate_reason": recon.Reason},
							SchemaVersion: "1",
						})
					}
				}
			}
		}

		// #3873 Case 1: non-terminal complement to the #3835 terminal reconcile
		// above. A non-terminal `feature-*` stage can exit non-zero in pre-flight
		// (the ~120ms / zero-token deaths) on an issue whose work ALREADY landed
		// in a prior run — the PR merged, the issue closed. Recording that as a
		// success:false exit-record re-introduces false-failure paging (the TS
		// notifier and health consumers treat it as a real failure). The terminal
		// block can't cover these stages: it calls the PR-context post-condition
		// gate, which feature-* stages have no equivalent for. So run a lightweight
		// forge resolution check here instead — issue CLOSED or branch PR
		// merged/open — and on a positive result clear the failure so the written
		// record is success:true. Guarded on !isTerminalStage so the two blocks are
		// mutually exclusive and the terminal path (#3835 WS1) is untouched. Fails
		// closed: any query error returns false → the failure is preserved.
		if (err != nil || exitCode != 0) && !isTerminalStage(stage) {
			branch := loadFeatureBranch(workspaceRoot, item.Number)
			// Bound the (up to two) sequential gh calls so a slow / rate-limited
			// GitHub never blocks the stage loop indefinitely. 15s matches the TS
			// notifier's execFile timeout and the gate's gh budget. On timeout the
			// helper's exec errors → fails closed (failure preserved).
			reconCtx, cancelRecon := context.WithTimeout(ctx, 15*time.Second)
			reconciled := reconcileIssueResolved(reconCtx, item, branch)
			cancelRecon()
			if reconciled {
				log.Printf("#%d: non-terminal stage %s reported failure (exit=%d, err=%v) but issue is resolved on forge (closed / branch PR landed) — reconciling to success (#3873)",
					item.Number, stage, exitCode, err)
				err = nil
				exitCode = 0
				if s.telemetrySvc != nil && s.telemetryEnabled {
					s.telemetrySvc.EmitPipelineEvent(ctx, platform.PipelineEvent{
						RunID:         runtime.RunID,
						IssueNumber:   item.Number,
						EventType:     "pipeline.failure_reconciled",
						Stage:         string(stage),
						Timestamp:     time.Now(),
						Metadata:      map[string]interface{}{"reason": "non-terminal stage; issue resolved on forge"},
						SchemaVersion: "1",
					})
				}
			}
		}

		// Persist state to disk after each stage completes
		stateDir := filepath.Join(workspaceRoot, ".nightgauge", "pipeline")
		if persistErr := runtime.Persist(stateDir); persistErr != nil {
			log.Printf("#%d: failed to persist state: %v", item.Number, persistErr)
		}

		// Issue #3605: persist per-stage forensic record (success or failure)
		// to .nightgauge/pipeline/exit-records/<UTC-day>.jsonl. Healthy
		// runs anchor what "normal" looks like for ratio-based health analysis;
		// failed runs make the next post-mortem debuggable in 30 seconds
		// instead of an hour. Best-effort — a write failure logs but never
		// blocks pipeline progress. See docs/STAGE_EXIT_DIAGNOSTIC.md.
		prStateAtExit := detMergePRState
		s.writeStageExitRecord(item, stage, runtime, result, exitCode, err,
			actualCostUsd, servedModel, inputTokens, outputTokens, cacheReadTokens,
			stageStartedAt, workspaceRoot, prStateAtExit, predictedSizeLabel(complexityScore))

		// Trace the stage exit summary (#179). Full forensics stay in the
		// exit-records store, joined by run_id (ADR 013 non-duplication rule).
		{
			exitPayload := trace.StageExitPayload{
				Success:   err == nil && exitCode == 0,
				ExitCode:  exitCode,
				ElapsedMs: time.Since(stageStartedAt).Milliseconds(),
				Model:     servedModel,
				CostUSD:   actualCostUsd,
			}
			if !exitPayload.Success && err != nil {
				exitPayload.TerminalKind = ClassifyTerminalKind(err.Error())
			}
			if gateRan {
				exitPayload.GateKind = string(gateRes.Kind)
			}
			tracer.Emit(trace.KindStageExit, string(stage), exitPayload)
		}

		if s.onStageComplete != nil {
			stageCostForCb := actualCostUsd
			if stageCostForCb == 0 {
				stageCostForCb = tokens.CalculateCost(servedModel, inputTokens, outputTokens)
			}
			s.onStageComplete(item.Repo, item.Number, string(stage), err, inputTokens, outputTokens, cacheReadTokens, stageCostForCb, servedModel)
		}

		if err != nil || exitCode != 0 {
			// Network-unavailable abort (Issue #3296): short-circuit before any
			// retry / escalation / stall-recovery logic. The cancellation came
			// from outside (TS watchdog observed sustained connectivity loss);
			// retrying or escalating model would just spend more tokens against
			// the same outage. Mark terminal kind, record stage error, return.
			if errors.Is(err, ErrNetworkUnavailable) {
				terminalFailureKind = TerminalKindNetworkUnavailable
				runtime.SetStageError(stage, err.Error())
				s.emitStateChanged(item.Repo, item.Number, runtime)
				log.Printf("#%d: stage %s aborted by network-outage circuit breaker — no retry, no escalation",
					item.Number, stage)
				if s.telemetrySvc != nil && s.telemetryEnabled {
					s.telemetrySvc.EmitPipelineEvent(ctx, platform.PipelineEvent{
						RunID:       runtime.RunID,
						IssueNumber: item.Number,
						EventType:   "stage_error",
						Stage:       string(stage),
						Timestamp:   time.Now(),
						Metadata: map[string]interface{}{
							"error":            err.Error(),
							"failure_category": "network",
							"abort_source":     "watchdog_connectivity_threshold",
						},
						SchemaVersion: "1",
					})
				}
				return
			}

			// Deterministic PR-stage GitHub rate-limit deferral (Issue #3976).
			// The deterministic pr-merge/pr-create path declined because the
			// GitHub bucket is exhausted, and the LLM path was deliberately
			// skipped. Short-circuit BEFORE retry / model-escalation / recovery —
			// all of which would re-run the stage straight into the same wall —
			// and route to the environmental recovery path: the github-quota-low
			// marker classifies to TerminalKindGitHubQuotaLow, whose
			// onPipelineComplete handler applies a GLOBAL quota cooldown until the
			// bucket resets, reverts the issue to Ready, and does NOT count it
			// toward the lifetime-failure cap (#3896). The #3835 reconcile gate
			// above already cleared err/exitCode if the merge actually landed, so
			// reaching here means the work genuinely did not land.
			if prStageRateLimited {
				terminalFailureKind = TerminalKindGitHubQuotaLow
				runtime.SetStageError(stage, err.Error())
				s.emitStateChanged(item.Repo, item.Number, runtime)
				log.Printf("#%d: stage %s deferred — GitHub rate limit; no LLM fallback, will retry after bucket reset [#3976]",
					item.Number, stage)
				if s.telemetrySvc != nil && s.telemetryEnabled {
					s.telemetrySvc.EmitPipelineEvent(ctx, platform.PipelineEvent{
						RunID:       runtime.RunID,
						IssueNumber: item.Number,
						EventType:   "stage_error",
						Stage:       string(stage),
						Timestamp:   time.Now(),
						Metadata: map[string]interface{}{
							"error":            err.Error(),
							"failure_category": "github_rate_limit",
							"execution_path":   "deterministic",
						},
						SchemaVersion: "1",
					})
				}
				return
			}

			// Budget-aware retry: check if partial work was committed (Issue #2338).
			//
			// PRIMARY signal — in-memory from IPC (#3666 follow-up). The TS
			// HeadlessOrchestrator now stamps BudgetExceeded / ShippedPartially /
			// ShippedPRNumber directly on the StageRunResult so the scheduler
			// reads what TS observed without disk-path coordination. This
			// replaces the budget-overrun-{N}.json file lookup, which silently
			// broke for multi-repo workspaces (TS wrote to the per-issue
			// worktree, Go read from workspaceRoot — they diverged for any
			// non-primary repo, e.g. acme/dashboard#443).
			if result != nil && result.BudgetExceeded {
				if result.ShippedPartially {
					// A budget-killed pr-merge cannot be trusted as "shipped"
					// without a MERGED check — otherwise this fast-advance skips
					// the verifier and reports phantom success (#4070 review).
					if stage == state.StagePRMerge && s.verifyPRMergeForStage(ctx, item, runtime, "budget-shipped") {
						return
					}
					log.Printf("#%d: stage %s budget-killed but shipped (PR #%d) — advancing to next stage (no retry needed)",
						item.Number, stage, result.ShippedPRNumber)
					runtime.CompleteStageWithCost(0, 0, 0, 0, 0)
					err = nil
					exitCode = 0
					terminalFailureKind = ""
					stageIdx++
					continue
				}
				// Non-shipped budget kill — fall through to the disk-file
				// path below, which still handles the #2338 WIP-retry case
				// for stages that did commit partial work without producing
				// a PR (feature-dev, feature-validate, etc.).
			}

			// LEGACY / FALLBACK — disk-file budget-overrun signal (#2338).
			// Kept for backward compatibility with older TS extensions that
			// don't yet stamp BudgetExceeded on the IPC result, and for the
			// WIP-retry path that still uses the WIPBranch field. Resolves
			// via loadWorktreePath so single-repo runs still find the file.
			overrunBase := loadWorktreePath(workspaceRoot, item.Number)
			overrunFile := filepath.Join(overrunBase, ".nightgauge", "pipeline",
				fmt.Sprintf("budget-overrun-%d.json", item.Number))
			if overrun, readErr := ReadBudgetOverrun(overrunFile); readErr == nil {
				stageKey := fmt.Sprintf("%s:%d", string(stage), item.Number)
				if overrun.ShippedPartially {
					// Same MERGED guard as the in-memory path: a budget-shipped
					// pr-merge must still be verified before it counts (#4070).
					if stage == state.StagePRMerge && s.verifyPRMergeForStage(ctx, item, runtime, "budget-shipped") {
						os.Remove(overrunFile)
						return
					}
					log.Printf("#%d: stage %s budget-killed but shipped (PR #%d, %.1fx overrun) — advancing to next stage (no retry needed)",
						item.Number, stage, overrun.ShippedPRNumber, overrun.OverrunRatio)
					os.Remove(overrunFile)
					runtime.CompleteStageWithCost(0, 0, 0, 0, 0)
					err = nil
					exitCode = 0
					terminalFailureKind = ""
					stageIdx++
					continue
				}
				if overrun.WIPCommitted && overrun.OverrunRatio < 3.0 && s.budgetRetries[stageKey] < 1 {
					s.budgetRetries[stageKey]++
					log.Printf("#%d: stage %s budget-killed (%.1fx overrun, WIP on %s) — retrying with partial work",
						item.Number, stage, overrun.OverrunRatio, overrun.WIPBranch)
					os.Remove(overrunFile)
					continue // Retry same stage (stageIdx not incremented)
				}
				log.Printf("#%d: stage %s budget-killed (%.1fx overrun) — no retry (retries=%d, wip=%v)",
					item.Number, stage, overrun.OverrunRatio, s.budgetRetries[stageKey], overrun.WIPCommitted)
				os.Remove(overrunFile)
			}

			// If the runner already evaluated and recorded escalation (IPC mode), retry directly.
			if result != nil && result.EscalationRecorded {
				tracer.Emit(trace.KindComplexityEscalation, string(stage), trace.EscalationPayload{
					Direction: "up",
					FromModel: model,
					ToModel:   s.retryEngine.CurrentModel(string(stage)),
					Reasoning: "stage failed; escalation evaluated and recorded by the IPC runner",
					Trigger:   "runner_recorded",
				})
				log.Printf("#%d: stage %s failed — escalation recorded by runner, retrying",
					item.Number, stage)
				continue
			}

			// If the runner classified an API model rejection and recorded the
			// sticky tier downgrade (IPC mode, #42), notify + retry directly —
			// the model resolution above picks up the substitution.
			if result != nil && result.FallbackRecorded {
				runtime.AppendEscalation(state.EscalationRecord{
					Stage:     stage,
					FromModel: result.FallbackFromModel,
					ToModel:   result.FallbackToModel,
					Reason:    "model_unavailable",
					At:        time.Now(),
				})
				tracer.Emit(trace.KindComplexityEscalation, string(stage), trace.EscalationPayload{
					Direction: "down",
					FromModel: result.FallbackFromModel,
					ToModel:   result.FallbackToModel,
					Reasoning: "model rejected by API; sticky downgrade for the rest of the run",
					Trigger:   "model_unavailable",
				})
				log.Printf("#%d: stage %s — model %s rejected by API; falling back to %s for the rest of the run",
					item.Number, stage, result.FallbackFromModel, result.FallbackToModel)
				s.fireModelFallback(item.Repo, item.Number, stage,
					result.FallbackFromModel, result.FallbackToModel, result.ErrorText)
				continue
			}

			// Adaptive stall-recovery (Issue #3005). Runs BEFORE model
			// escalation: stall-kill is rarely a model-capacity issue, and
			// re-planning is a more accurate response. Cost-cap kills (#3002)
			// are NEVER retried — operator's per-stage cap contract takes
			// precedence. See ADR-004.
			stallErrMsg := ""
			if err != nil {
				stallErrMsg = err.Error()
			}
			isStallKill := ClassifyTerminalKind(stallErrMsg) == TerminalKindStallKill
			isCostCapKill := HasCostCapKillMarker(stallErrMsg)
			if isStallKill && !isCostCapKill {
				// Issue #3542: budget-aware model escalation. When a stall-kill
				// occurs AND the pipeline has already burned >50% of its USD
				// budget ceiling, a same-model stall-retry is likely to burn
				// the rest of the budget the same way. Prefer escalating the
				// model (sonnet → opus) — a stronger model is more likely to
				// finish within the remaining budget than a re-plan retry.
				if pipelineBudgetCeilingUSD > 0 {
					budgetRatio := runtime.TotalCostUSD / pipelineBudgetCeilingUSD
					if budgetRatio > 0.5 {
						escalation := s.retryEngine.EvaluateEscalation(string(stage), model)
						if escalation.ShouldEscalate {
							log.Printf("#%d: stall-kill with >50%% budget consumed ($%.2f/$%.2f) — escalating model to %s",
								item.Number, runtime.TotalCostUSD, pipelineBudgetCeilingUSD, escalation.NewModel)
							s.retryEngine.RecordEscalation(string(stage), escalation.NewModel)
							tracer.Emit(trace.KindComplexityEscalation, string(stage), trace.EscalationPayload{
								Direction: "up",
								FromModel: model,
								ToModel:   escalation.NewModel,
								Reasoning: fmt.Sprintf("stall-kill with >50%% of budget ceiling consumed ($%.2f/$%.2f) — a stronger model is more likely to finish within the remaining budget than a re-plan retry", runtime.TotalCostUSD, pipelineBudgetCeilingUSD),
								Trigger:   "stall_budget",
							})
							continue // retry same stage with the escalated model
						}
					}
				}

				if stallRetryCount == 0 &&
					CanRewindFromStage(stage) &&
					GetAdaptiveStallRecoveryEnabled(workspaceRoot) {

					signal := ClassifyStallSignal(stage, stallErrMsg, workspaceRoot, item.Number)
					if writeErr := WriteSyntheticFeedbackContext(workspaceRoot, item.Number, signal); writeErr != nil {
						log.Printf("#%d: stall-recovery: failed to write feedback context: %v",
							item.Number, writeErr)
					} else {
						feedbackPath := filepath.Join(workspaceRoot, ".nightgauge", "pipeline",
							fmt.Sprintf("feedback-%d.json", item.Number))
						decision, btErr := s.retryEngine.EvaluateBacktrack(feedbackPath)
						if btErr != nil {
							log.Printf("#%d: stall-recovery: failed to evaluate backtrack: %v",
								item.Number, btErr)
						} else if decision.ShouldBacktrack {
							s.retryEngine.RecordBacktrack(string(stage), string(decision.TargetStage), decision.SignalType)
							stallRetryCount++
							tracer.Emit(trace.KindBacktrack, string(stage), trace.BacktrackPayload{
								FromStage:   string(stage),
								TargetStage: string(decision.TargetStage),
								SignalType:  decision.SignalType,
								Rationale:   decision.Rationale,
								Trigger:     "stall_recovery",
							})
							log.Printf("#%d: %s — stall-kill in %s, rewinding to %s (signal=%s)",
								item.Number, StallRetriedOutcome, stage, decision.TargetStage, signal.SignalType)
							if s.telemetrySvc != nil && s.telemetryEnabled {
								s.telemetrySvc.EmitPipelineEvent(ctx, platform.PipelineEvent{
									RunID:       runtime.RunID,
									IssueNumber: item.Number,
									EventType:   "stall_retried",
									Stage:       string(stage),
									Timestamp:   time.Now(),
									Metadata: map[string]interface{}{
										"signal_type":  signal.SignalType,
										"target_stage": string(decision.TargetStage),
										"killed_stage": string(stage),
										"retry_count":  stallRetryCount,
									},
									SchemaVersion: "1",
								})
							}
							for i, st := range stages {
								if st == decision.TargetStage {
									stageIdx = i
									break
								}
							}
							continue // Rewind — re-run from feature-planning
						} else {
							log.Printf("#%d: stall-recovery: backtrack blocked (limit=%v, oscillation=%v) — falling through to terminal",
								item.Number, decision.LimitReached, decision.OscillationBlocked)
						}
					}
				} else if stallRetryCount >= 1 {
					// Second stall-kill in the same run — terminal. Mark the
					// stage detail with the agent-bucket failure_category and
					// set terminal_failure_kind so the V3 record reads
					// correctly. Then fall through to the terminal path
					// (skipping model escalation — escalation on a re-stall
					// after a re-plan retry is double-spend).
					stageFailureCategories[string(stage)] = StallKilledAfterRetryCategory
					terminalFailureKind = TerminalKindStallKill
					log.Printf("#%d: stall-kill after retry — marking stage %s as %s",
						item.Number, stage, StallKilledAfterRetryCategory)
					runtime.SetStageError(stage, fmt.Sprintf("exit %d: %v", exitCode, err))
					s.emitStateChanged(item.Repo, item.Number, runtime)
					if s.telemetrySvc != nil && s.telemetryEnabled {
						s.telemetrySvc.EmitPipelineEvent(ctx, platform.PipelineEvent{
							RunID:       runtime.RunID,
							IssueNumber: item.Number,
							EventType:   "stage_error",
							Stage:       string(stage),
							Timestamp:   time.Now(),
							Metadata: map[string]interface{}{
								"error":            stallErrMsg,
								"exit_code":        exitCode,
								"model":            model,
								"failure_category": StallKilledAfterRetryCategory,
							},
							SchemaVersion: "1",
						})
					}
					return // Pipeline failed terminally on second stall
				}
			}

			// FailureRecovery registry (Issue #3268). Consult after
			// stall-recovery doesn't apply and BEFORE model escalation —
			// most matched cases are deterministically resolvable, so
			// recovering for free is preferable to spending tokens on a
			// stronger model. When the registry returns matched=false the
			// caller falls through to escalation unchanged.
			if s.recoveryRegistry != nil {
				stageErrText := ""
				if err != nil {
					stageErrText = err.Error()
				}
				// Recovery actions read `.nightgauge/pipeline/*-{N}.json` and run git/
				// gh against the tree the stages executed in. On worktree-isolated
				// runs that is the worktree, not the canonical root — so resolve it
				// the same way the deterministic dispatch and the LLM path do (#275).
				// Passing the bare root made pr-{N}.json invisible (PRNumber=0) and
				// pointed every git-op recovery (branch-out-of-date rebase,
				// SkillExitedWithoutCreatingPR re-run) at the main checkout.
				recoveryWS := stageWorkspace(runtime, workspaceRoot)
				failure := recovery.StageFailure{
					Stage:          stage,
					GateName:       "",
					GateKind:       gates.KindOK,
					Reason:         "",
					Evidence:       nil,
					StageError:     stageErrText,
					TerminalKind:   ClassifyTerminalKind(stageErrText),
					PRNumber:       loadPRNumberForRecovery(recoveryWS, item.Number),
					Workspace:      recoveryWS,
					IssueNumber:    item.Number,
					Repo:           item.Repo,
					AttemptOrdinal: recoveryAttemptsThisRun + 1,
				}
				if gateRan {
					failure.GateName = gateRes.GateName
					failure.GateKind = gateRes.Kind
					failure.Reason = gateRes.Reason
					failure.Evidence = gateRes.Evidence
				}
				if result, matched := s.recoveryRegistry.TryRecover(ctx, failure, recoveryAttemptsThisRun); matched {
					// Cap-exempt actions (conflict-recovery) carry their own
					// per-edge bound and must not draw from the global per-run
					// pool, or an unrelated earlier recovery would silently
					// shorten the configured max_dev_redispatch (#4072 review).
					if !s.recoveryRegistry.IsCapExempt(result.Action) {
						recoveryAttemptsThisRun++
					}
					runtime.AppendRecoveryAttempt(stage, recovery.ToStateRecoveryAttempt(result))
					tracer.Emit(trace.KindRecoveryRetry, string(stage), trace.RecoveryRetryPayload{
						Action:         result.Action,
						Recovered:      result.Recovered,
						Reason:         result.Reason,
						Evidence:       result.Evidence,
						FollowUp:       result.FollowUp,
						AttemptOrdinal: failure.AttemptOrdinal,
						DurationMs:     result.DurationMs,
					})
					// Persist the recovery attempt immediately so the
					// runtime-{N}.json snapshot reflects it before the
					// next iteration runs; the success block's persist
					// is skipped on the failure→recovery path.
					if persistErr := runtime.Persist(filepath.Join(workspaceRoot, ".nightgauge", "pipeline")); persistErr != nil {
						log.Printf("#%d: failed to persist state after recovery attempt: %v", item.Number, persistErr)
					}
					if s.telemetrySvc != nil && s.telemetryEnabled {
						s.telemetrySvc.EmitPipelineEvent(ctx, platform.PipelineEvent{
							RunID:       runtime.RunID,
							IssueNumber: item.Number,
							EventType:   "pipeline.recovery_attempt",
							Stage:       string(stage),
							Timestamp:   time.Now(),
							Metadata: map[string]interface{}{
								"action":          result.Action,
								"recovered":       result.Recovered,
								"reason":          result.Reason,
								"follow_up":       result.FollowUp,
								"cost_usd":        result.CostUSD,
								"duration_ms":     result.DurationMs,
								"attempt_ordinal": failure.AttemptOrdinal,
							},
							SchemaVersion: "1",
						})
					}
					if result.Recovered {
						log.Printf("#%d: stage %s self-healed via %s (%s)",
							item.Number, stage, result.Action, result.Reason)
						err = nil
						exitCode = 0
						stageIdx++
						continue
					}
					// A deterministic action may decline to recover the stage
					// in place yet set up a backward rewind by emitting a
					// feedback signal (e.g. conflict-recovery-loop emits
					// CONFLICT_RESOLUTION_NEEDED targeting feature-dev — #4072).
					// When the action signals the stage can resume via the
					// feedback-rewind path, honor the feedback file: rewind to
					// the target stage instead of falling through to terminal.
					// The LLM work happens in the rewound stage, keeping the
					// recovery action itself deterministic.
					if result.FollowUp == recovery.FollowUpStageCanResume {
						// A deterministic action declined to recover the stage in
						// place but set up a backward rewind by emitting a feedback
						// signal (conflict-recovery-loop → CONFLICT_RESOLUTION_NEEDED
						// targeting feature-dev, #4072). Honor the feedback file via
						// the conflict-specific evaluator: that edge is bounded by a
						// PER-EDGE count (MaxConflictRedispatch) instead of the
						// open-ended-ping-pong oscillation block, so the loop
						// re-dispatches feature-dev up to the configured bound and
						// then declines → terminal failure naming the files. Using the
						// conflict-only evaluator (not the generic one) keeps this the
						// SOLE consumer of the conflict signal — the generic post-stage
						// rewind sites skip it, avoiding a feature-dev self-loop (#4072).
						feedbackFile := filepath.Join(workspaceRoot, ".nightgauge", "pipeline",
							fmt.Sprintf("feedback-%d.json", item.Number))
						decision, btErr := s.retryEngine.EvaluateConflictBacktrack(feedbackFile)
						if btErr != nil {
							log.Printf("#%d: recovery %s requested resume but backtrack eval failed: %v",
								item.Number, result.Action, btErr)
						} else if decision.ShouldBacktrack {
							s.retryEngine.RecordBacktrack(string(stage), string(decision.TargetStage), decision.SignalType)
							tracer.Emit(trace.KindBacktrack, string(stage), trace.BacktrackPayload{
								FromStage:   string(stage),
								TargetStage: string(decision.TargetStage),
								SignalType:  decision.SignalType,
								Rationale:   decision.Rationale,
								Trigger:     "conflict_recovery",
							})
							log.Printf("#%d: recovery %s — rewinding %s → %s (signal=%s)",
								item.Number, result.Action, stage, decision.TargetStage, decision.SignalType)
							err = nil
							exitCode = 0
							for i, st := range stages {
								if st == decision.TargetStage {
									stageIdx = i
									break
								}
							}
							continue // Re-run from the target stage
						} else {
							log.Printf("#%d: recovery %s requested resume but backtrack declined (limit=%v, oscillation=%v) — escalating: %v",
								item.Number, result.Action, decision.LimitReached, decision.OscillationBlocked, result.Evidence)
							// Conflict bound exhausted: surface a file-named terminal
							// reason so the skill-crash path (where the on-disk
							// escalation never fired) still names the conflicting files,
							// matching the normal-path Execute escalation (#4072 review).
							if decision.LimitReached {
								conflictExhaustionReason = formatConflictExhaustion(result.Evidence)
								conflictExhaustionStage = stage
							}
						}
					}
					log.Printf("#%d: stage %s recovery action %s declined (%s) — falling through",
						item.Number, stage, result.Action, result.Reason)
				}
			}

			// Model rejected by the API (#42): substitute the next-best tier
			// instead of escalating upward — a stronger model on a plan that
			// already refused this one would be rejected the same way. The
			// substitution is sticky for the rest of the run; the retry below
			// re-dispatches this stage and the model resolution picks it up.
			failText := ""
			if err != nil {
				failText = err.Error()
			}
			modelRejected := ClassifyTerminalKind(failText) == TerminalKindModelUnavailable
			if modelRejected {
				if dg := s.retryEngine.EvaluateDowngrade(model); dg.ShouldDowngrade {
					s.retryEngine.RecordDowngrade(model, dg.NewTier)
					runtime.AppendEscalation(state.EscalationRecord{
						Stage:     stage,
						FromModel: model,
						ToModel:   dg.NewTier,
						Reason:    "model_unavailable",
						At:        time.Now(),
					})
					tracer.Emit(trace.KindComplexityEscalation, string(stage), trace.EscalationPayload{
						Direction: "down",
						FromModel: model,
						ToModel:   dg.NewTier,
						Reasoning: "model rejected by API; substituting next-best tier (sticky for the run) — a stronger model on a plan that refused this one would be rejected the same way",
						Trigger:   "model_unavailable",
					})
					log.Printf("#%d: stage %s — model %s rejected by API; falling back to %s for the rest of the run",
						item.Number, stage, model, dg.NewTier)
					s.fireModelFallback(item.Repo, item.Number, stage, model, dg.NewTier, failText)
					continue // Retry same stage on the substituted tier
				}
				log.Printf("#%d: stage %s — model %s rejected by API and no weaker tier available; giving up",
					item.Number, stage, model)
				terminalFailureKind = TerminalKindModelUnavailable
			}

			// Stage failed — evaluate model escalation before giving up.
			// Skipped on a model rejection: escalation moves UP the ladder,
			// which cannot help when the plan refused the current model.
			if !modelRejected {
				escalation := s.retryEngine.EvaluateEscalation(string(stage), model)
				if escalation.ShouldEscalate {
					log.Printf("#%d: stage %s failed — escalating model to %s",
						item.Number, stage, escalation.NewModel)
					s.retryEngine.RecordEscalation(string(stage), escalation.NewModel)
					tracer.Emit(trace.KindComplexityEscalation, string(stage), trace.EscalationPayload{
						Direction: "up",
						FromModel: model,
						ToModel:   escalation.NewModel,
						Reasoning: "stage failed; retrying on a stronger model",
						Trigger:   "stage_failure",
					})
					// Retry same stage (don't increment stageIdx)
					continue
				}
			}

			terminalReason := fmt.Sprintf("exit %d: %v", exitCode, err)
			if conflictExhaustionReason != "" {
				terminalReason = conflictExhaustionReason
			}
			runtime.SetStageError(stage, terminalReason)
			s.emitStateChanged(item.Repo, item.Number, runtime)
			// Log and telemetry use terminalReason so the file-named conflict
			// exhaustion propagates consistently with the persisted state (it
			// already defaults to "exit N: err", so the non-conflict path is
			// unchanged) (#4072 review).
			log.Printf("#%d: stage %s failed (exit %d): %s", item.Number, stage, exitCode, terminalReason)
			if s.telemetrySvc != nil && s.telemetryEnabled {
				s.telemetrySvc.EmitPipelineEvent(ctx, platform.PipelineEvent{
					RunID:       runtime.RunID,
					IssueNumber: item.Number,
					EventType:   "stage_error",
					Stage:       string(stage),
					Timestamp:   time.Now(),
					Metadata: map[string]interface{}{
						"error":     terminalReason,
						"exit_code": exitCode,
						"model":     model,
					},
					SchemaVersion: "1",
				})
			}
			return // Pipeline failed
		}

		// #2870: A stage exit code 0 doesn't guarantee the skill produced its
		// output context file. Verify the expected output exists; treat a
		// missing file as a stage failure so the actual offender is named
		// (vs blaming the next stage's prerequisite check), telemetry sees
		// the failure, and model escalation gets a chance to recover.
		if outputErr := validateStageOutput(stage, stageWorkspace(runtime, workspaceRoot), item.Number); outputErr != nil {
			runtime.SetStageError(stage, outputErr.Error())
			s.emitStateChanged(item.Repo, item.Number, runtime)
			log.Printf("#%d: %v", item.Number, outputErr)
			if s.telemetrySvc != nil && s.telemetryEnabled {
				s.telemetrySvc.EmitPipelineEvent(ctx, platform.PipelineEvent{
					RunID:       runtime.RunID,
					IssueNumber: item.Number,
					EventType:   "stage_error",
					Stage:       string(stage),
					Timestamp:   time.Now(),
					Metadata: map[string]interface{}{
						"error":     outputErr.Error(),
						"exit_code": 0,
						"model":     model,
						"reason":    "missing_output_context",
					},
					SchemaVersion: "1",
				})
			}
			// Stronger model may produce the missing context — try escalation
			// before giving up, mirroring the regular failure path.
			escalation := s.retryEngine.EvaluateEscalation(string(stage), model)
			if escalation.ShouldEscalate {
				log.Printf("#%d: stage %s missing output — escalating model to %s",
					item.Number, stage, escalation.NewModel)
				s.retryEngine.RecordEscalation(string(stage), escalation.NewModel)
				tracer.Emit(trace.KindComplexityEscalation, string(stage), trace.EscalationPayload{
					Direction: "up",
					FromModel: model,
					ToModel:   escalation.NewModel,
					Reasoning: "stage exited 0 but produced no output context; a stronger model may produce it",
					Trigger:   "missing_output",
				})
				continue // Retry same stage
			}
			// Issue #3001: missing output context is a validation_error.
			terminalFailureKind = TerminalKindValidationError
			return // Pipeline failed
		}

		if s.telemetrySvc != nil && s.telemetryEnabled {
			s.telemetrySvc.EmitPipelineEvent(ctx, platform.PipelineEvent{
				RunID:       runtime.RunID,
				IssueNumber: item.Number,
				EventType:   "stage_completed",
				Stage:       string(stage),
				Timestamp:   time.Now(),
				DurationMs:  int(result.ElapsedMs),
				Metadata: map[string]interface{}{
					"input_tokens":      inputTokens,
					"output_tokens":     outputTokens,
					"cache_read_tokens": cacheReadTokens,
					"cost_usd":          actualCostUsd,
					// Served model (#91): differs from the requested model when
					// the CLI's refusal fallback swapped mid-stage.
					"model": servedModel,
				},
				SchemaVersion: "1",
			})
		}

		// Issue #3542: after a successful feature-dev stage, check whether the
		// Stop hook signaled incomplete tasks (stop-hook-status-{N}.json). In
		// the #3365 incident the stop hook returned OK=false while the agent
		// was finishing up — the agent kept working and the budget ceiling
		// killed it mid-cleanup with uncommitted work. The stage still reports
		// exit 0, so detect the sentinel here and recover any uncommitted work
		// into a commit before continuing to feature-validate.
		if stage == state.StageFeatureDev {
			sentinelPath := filepath.Join(workspaceRoot, ".nightgauge", "pipeline",
				fmt.Sprintf("stop-hook-status-%d.json", item.Number))
			if _, statErr := os.Stat(sentinelPath); statErr == nil {
				// Consume the sentinel before recovering — it is a one-shot
				// signal, and removing it first keeps it out of the recovery
				// commit's `git add -A`.
				_ = os.Remove(sentinelPath)
				worktreePath := loadWorktreePath(workspaceRoot, item.Number)
				if worktreePath != "" && hasUncommittedWork(worktreePath) {
					log.Printf("#%d: stop hook signaled incomplete tasks — recovering uncommitted feature-dev work",
						item.Number)
					if recErr := recoverUncommittedWork(worktreePath, item.Number, string(stage)); recErr != nil {
						log.Printf("#%d: stop-hook fallback commit failed: %v — worktree preserved at %s",
							item.Number, recErr, worktreePath)
					} else {
						log.Printf("#%d: stop-hook fallback commit successful", item.Number)
					}
				}
			}
		}

		// Stage succeeded — check for feedback signals (backtrack evaluation)
		feedbackFile := filepath.Join(workspaceRoot, ".nightgauge", "pipeline",
			fmt.Sprintf("feedback-%d.json", item.Number))
		if _, statErr := os.Stat(feedbackFile); statErr == nil {
			backtrack, btErr := s.retryEngine.EvaluateBacktrack(feedbackFile)
			if btErr != nil {
				log.Printf("#%d: failed to evaluate backtrack: %v", item.Number, btErr)
			} else if backtrack.ShouldBacktrack {
				log.Printf("#%d: backtracking from %s to %s — %s",
					item.Number, stage, backtrack.TargetStage, backtrack.Rationale)
				s.retryEngine.RecordBacktrack(string(stage), string(backtrack.TargetStage), backtrack.SignalType)
				tracer.Emit(trace.KindBacktrack, string(stage), trace.BacktrackPayload{
					FromStage:   string(stage),
					TargetStage: string(backtrack.TargetStage),
					SignalType:  backtrack.SignalType,
					Rationale:   backtrack.Rationale,
					Trigger:     "feedback",
				})

				// Find target stage index and rewind
				for i, st := range stages {
					if st == backtrack.TargetStage {
						stageIdx = i
						break
					}
				}
				continue // Re-run from target stage
			} else if backtrack.OscillationBlocked {
				log.Printf("#%d: backtrack blocked — oscillation detected (%s→%s)",
					item.Number, stage, backtrack.TargetStage)
			} else if backtrack.LimitReached {
				log.Printf("#%d: backtrack blocked — max backtracks exceeded",
					item.Number)
			}
		}

		stageCost := actualCostUsd
		if stageCost == 0 {
			stageCost = tokens.CalculateCost(model, inputTokens, outputTokens)
		}
		// source=llm: All Go-scheduler stages run via LLM in this iteration.
		// Deterministic-first is TypeScript-only (Issue #2614); this field
		// enables future Go-side deterministic-first tracking.
		log.Printf("#%d: stage %s complete — model=%s source=llm, tokens: %d in (%d cached) / %d out, cost: $%.4f",
			item.Number, stage, model, inputTokens, cacheReadTokens, outputTokens, stageCost)

		// Post-stage verification for pr-merge: the skill's exit code is not
		// sufficient evidence that the PR actually merged. Query GitHub and
		// confirm state=MERGED before moving to pipeline-finish (#2843). The
		// helper also owns the post-merge sub-issue close + epic auto-close.
		if stage == state.StagePRMerge {
			if s.verifyPRMergeForStage(ctx, item, runtime, "") {
				return // Pipeline failed — board revert + outcome=failed via deferred path
			}
		}

		stageIdx++
	}

	// Log pipeline cost summary
	snap := runtime.Snapshot()
	log.Printf("#%d: ═══ Pipeline Complete ═══", item.Number)
	for _, sr := range snap.CompletedStages {
		log.Printf("#%d:   %-20s %d in / %d out  $%.4f",
			item.Number, sr.Stage, sr.InputTokens, sr.OutputTokens, sr.CostUSD)
	}
	log.Printf("#%d:   %-20s TOTAL  $%.4f", item.Number, "─────────────────", snap.TotalCostUSD)

	// Pipeline complete — update board
	if s.stateSvc != nil {
		_ = s.stateSvc.CompletePipeline(ctx, item.ID, state.StatusInReview)
	}

	// Mark pipeline as successful before defer fires.
	// Note: parent-epic auto-close already fired immediately after pr-merge
	// verification above, so we do not call checkEpicCompletion again here —
	// double-firing would be a harmless no-op but pollutes logs.
	pipelineSuccess = true
}

// RunPipelineForItem executes the full pipeline for a known BoardItem.
// Unlike RunQueue, this bypasses the project board lookup — the caller
// supplies the item directly. Used by pipeline.runItem IPC method (testing
// and direct-dispatch use cases).
func (s *Scheduler) RunPipelineForItem(ctx context.Context, item types.BoardItem) {
	s.dispatchItem(ctx, item)
}

// dispatchItem routes a board item to either wave orchestration (for epics
// with parallel-eligible sub-issues) or the standard sequential pipeline.
func (s *Scheduler) dispatchItem(ctx context.Context, item types.BoardItem) {
	if item.IsEpic && len(item.SubIssues) > 0 {
		log.Printf("#%d: detected epic with %d sub-issues — attempting wave orchestration",
			item.Number, len(item.SubIssues))
		if s.RunEpicWaves(ctx, item) {
			return // Wave orchestration handled the epic
		}
		// Wave orchestration declined (sequential strategy) — fall back to queue
		log.Printf("#%d: wave orchestration declined — falling back to sequential queue", item.Number)
		ownerPart, repoPart := splitOwnerRepo(item.Repo)
		if err := s.EnqueueEpic(ctx, ownerPart, repoPart, item.Number, item.Title, item.Labels, nil); err != nil {
			log.Printf("#%d: failed to enqueue epic sub-issues: %v", item.Number, err)
		}
		return
	}
	s.runPipeline(ctx, item)
}

// isBlocked checks if any blockedBy issue is still OPEN, using the BlockedBy
// data already populated on the BoardItem by the project-board GraphQL query
// (see internal/github/board.go nodeToItem). This avoids a per-Ready-item
// GetIssue round-trip on every scheduler tick — the dominant source of GitHub
// API consumption in autonomous mode for single-repo workspaces.
//
// It auto-removes circular blockedBy relationships where an issue is blocked
// by its own parent epic (which can never resolve).
func (s *Scheduler) isBlocked(ctx context.Context, item types.BoardItem) (bool, error) {
	for _, blocker := range item.BlockedBy {
		if !strings.EqualFold(blocker.State, "OPEN") {
			continue
		}

		// Detect circular dependency: issue blocked by its own parent epic.
		// This can never resolve (epic waits for sub-issue, sub-issue waits for epic).
		// Auto-remove the relationship and skip this blocker.
		if item.ParentNumber > 0 && blocker.Number == item.ParentNumber {
			log.Printf("AUTO-FIX: removing circular blockedBy — #%d was blocked by its parent epic #%d", item.Number, blocker.Number)
			if s.issueSvc != nil && item.NodeID != "" && blocker.NodeID != "" {
				if removeErr := s.issueSvc.RemoveBlockedBy(ctx, item.NodeID, blocker.NodeID); removeErr != nil {
					log.Printf("WARN: failed to remove circular blockedBy for #%d: %v", item.Number, removeErr)
				}
			}
			continue
		}

		return true, nil
	}
	return false, nil
}

// refreshBlockerStates fetches fresh blocker state from GitHub for all queued
// items that have blockedBy entries. This prevents items from staying stuck
// when their blockers have been closed since the queue was last persisted.
func (s *Scheduler) refreshBlockerStates(ctx context.Context) {
	if s.issueSvc == nil {
		return
	}
	s.mu.Lock()
	// Collect items that need refresh (have OPEN blockers).
	type refreshTarget struct {
		queueIdx   int
		blockerIdx int
		repo       string
		number     int
	}
	var targets []refreshTarget
	for i, item := range s.queue {
		for j, b := range item.BlockedBy {
			if strings.EqualFold(b.State, "OPEN") {
				targets = append(targets, refreshTarget{
					queueIdx:   i,
					blockerIdx: j,
					repo:       item.Repo,
					number:     b.Number,
				})
			}
		}
	}
	s.mu.Unlock()

	if len(targets) == 0 {
		return
	}

	// Group targets by repo so each repo can be served by a single batched
	// GraphQL query (issueSvc.GetIssuesByNumbers) instead of one round-trip
	// per blocker. A queue of 10 items × 2 OPEN blockers in one repo collapses
	// from 20 serial GetIssue calls to 1 aliased GraphQL request.
	type result struct {
		target refreshTarget
		state  string
	}
	byRepo := make(map[string][]int)
	for _, t := range targets {
		byRepo[t.repo] = append(byRepo[t.repo], t.number)
	}

	stateByRepoNumber := make(map[string]map[int]string, len(byRepo))
	for repo, nums := range byRepo {
		owner, name := splitOwnerRepo(repo)
		issues, err := s.issueSvc.GetIssuesByNumbers(ctx, owner, name, nums)
		if err != nil {
			// Per-repo failure is non-fatal: other repos still get refreshed.
			log.Printf("WARN: refreshBlockerStates: batch fetch failed for %s: %v", repo, err)
			continue
		}
		m := make(map[int]string, len(issues))
		for n, iss := range issues {
			m[n] = iss.State
		}
		stateByRepoNumber[repo] = m
	}

	var results []result
	for _, t := range targets {
		repoStates, ok := stateByRepoNumber[t.repo]
		if !ok {
			continue
		}
		state, ok := repoStates[t.number]
		if !ok {
			// Issue not in batch response (deleted/inaccessible). Leave as-is.
			continue
		}
		results = append(results, result{target: t, state: state})
	}

	// Apply updates under lock.
	s.mu.Lock()
	changed := false
	for _, r := range results {
		idx := r.target.queueIdx
		bIdx := r.target.blockerIdx
		if idx < len(s.queue) && bIdx < len(s.queue[idx].BlockedBy) &&
			s.queue[idx].BlockedBy[bIdx].Number == r.target.number {
			if !strings.EqualFold(s.queue[idx].BlockedBy[bIdx].State, r.state) {
				log.Printf("refreshBlockerStates: #%d blocker #%d state %s → %s",
					s.queue[idx].IssueNumber, r.target.number,
					s.queue[idx].BlockedBy[bIdx].State, r.state)
				s.queue[idx].BlockedBy[bIdx].State = r.state
				changed = true
			}
		}
	}
	if changed {
		s.persistQueue()
	}
	s.mu.Unlock()
}

// getMergeLock returns the per-repo merge mutex.
func (s *Scheduler) getMergeLock(repo string) *sync.Mutex {
	s.mu.Lock()
	defer s.mu.Unlock()
	lock, ok := s.mergeLocks[repo]
	if !ok {
		lock = &sync.Mutex{}
		s.mergeLocks[repo] = lock
	}
	return lock
}

func priorityRank(p types.Priority) int {
	switch p {
	case types.PriorityP0:
		return 0
	case types.PriorityP1:
		return 1
	case types.PriorityP2:
		return 2
	case types.PriorityP3:
		return 3
	default:
		return 4 // No priority = lowest
	}
}

func splitOwnerRepo(fullRepo string) (string, string) {
	parts := strings.SplitN(fullRepo, "/", 2)
	if len(parts) == 2 {
		return parts[0], parts[1]
	}
	return "", fullRepo
}

// splitNodeKey parses a graph node key ("owner/repo#number") into its repo
// ("owner/repo") and issue number. ok is false for malformed keys (no '#', or
// a non-numeric suffix).
func splitNodeKey(key string) (repo string, number int, ok bool) {
	idx := strings.LastIndex(key, "#")
	if idx <= 0 || idx == len(key)-1 {
		return "", 0, false
	}
	num, err := strconv.Atoi(key[idx+1:])
	if err != nil {
		return "", 0, false
	}
	return key[:idx], num, true
}

// resolveIssueStatesByKey batch-resolves the current GitHub State ("OPEN" or
// "CLOSED") of the given graph node keys ("owner/repo#number") via
// issueSvc.GetIssuesByNumbers, grouped by repo so any number of keys sharing a
// repo cost exactly ONE GraphQL round trip — the same batching discipline as
// refreshBlockerStates above (a queue of N items spread across R repos
// collapses from N calls to R). Used by the autonomous scheduler's
// candidate-selection dep check to resolve "dangling" dependency edges that
// reference an issue with no node in the graph (#306).
//
// Keys the resolver couldn't confirm — a malformed key, a per-repo fetch
// error, or an issue simply absent from the batch response (deleted /
// inaccessible) — are left OUT of the returned map. Callers must treat
// absence as "still unresolved" and apply their own fail-open/fail-closed
// policy; this helper never guesses a state.
func resolveIssueStatesByKey(ctx context.Context, issueSvc issueGetter, keys []string) map[string]string {
	if issueSvc == nil || len(keys) == 0 {
		return nil
	}

	byRepo := make(map[string][]int)
	for _, key := range keys {
		repo, num, ok := splitNodeKey(key)
		if !ok {
			log.Printf("WARN: resolveIssueStatesByKey: malformed node key %q, skipping", key)
			continue
		}
		byRepo[repo] = append(byRepo[repo], num)
	}

	resolved := make(map[string]string, len(keys))
	for repo, nums := range byRepo {
		owner, name := splitOwnerRepo(repo)
		issues, err := issueSvc.GetIssuesByNumbers(ctx, owner, name, nums)
		if err != nil {
			log.Printf("WARN: resolveIssueStatesByKey: batch fetch failed for %s: %v", repo, err)
			continue
		}
		for n, iss := range issues {
			resolved[fmt.Sprintf("%s#%d", repo, n)] = iss.State
		}
	}
	return resolved
}

// issueBodyCaptureMax bounds the issue body captured at pickup (#183) to a
// sensible excerpt so runtime-{N}.json / the JSONL history stay lean and the
// telemetry wire's issueBody .max(8192) is never exceeded. The platform enforces
// the same ceiling; capping here keeps the on-disk state small too.
const issueBodyCaptureMax = 8192

// captureIssueBody fetches the dispatched issue's body at pickup so the run
// record and telemetry can surface issue context on the dashboard run-detail
// page (#183). Best-effort and non-fatal: a missing client, an unparseable
// repo, or a GetIssue error leaves the body empty and the run proceeds. The
// result is bounded to issueBodyCaptureMax runes.
func (s *Scheduler) captureIssueBody(ctx context.Context, item types.BoardItem) string {
	owner, repo := splitOwnerRepo(item.Repo)
	if owner == "" || repo == "" {
		return ""
	}
	issueSvc := s.issueServiceFor(ctx, owner, repo)
	if issueSvc == nil {
		return ""
	}
	issue, err := issueSvc.GetIssue(ctx, owner, repo, item.Number)
	if err != nil || issue == nil {
		if err != nil {
			log.Printf("#%d: issue-context capture (body): GetIssue failed (non-fatal): %v", item.Number, err)
		}
		return ""
	}
	return clipRunes(issue.Body, issueBodyCaptureMax)
}

// clipRunes truncates s to at most n runes (rune-safe — never splits a
// multi-byte character), returning s unchanged when it already fits.
func clipRunes(s string, n int) string {
	if n <= 0 {
		return ""
	}
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n])
}

// loadIssueContext reads the issue context JSON and extracts complexity score,
// routing path, and predicted model. Returns zero values if the file is missing
// or malformed.
func loadIssueContext(workspaceRoot string, issueNumber int) (complexityScore int, routingPath string, predictedModel string) {
	path := filepath.Join(workspaceRoot, ".nightgauge", "pipeline",
		fmt.Sprintf("issue-%d.json", issueNumber))
	data, err := os.ReadFile(path)
	if err != nil {
		return 0, "", ""
	}
	var ctx struct {
		Routing struct {
			ComplexityScore      int    `json:"complexity_score"`
			Path                 string `json:"path"`
			PickupRecommendation struct {
				DevModel string `json:"dev_model"`
			} `json:"pickup_recommendation"`
		} `json:"routing"`
	}
	if err := json.Unmarshal(data, &ctx); err != nil {
		return 0, "", ""
	}
	model := ctx.Routing.PickupRecommendation.DevModel
	if model == "" {
		model = "sonnet" // Default: sonnet is the general-purpose model
	}
	return ctx.Routing.ComplexityScore, ctx.Routing.Path, model
}

// deriveRoutingDecision computes the authoritative routing Decision for a queued
// item deterministically (#4126). Unlike the AI-authored skip_stages persisted
// in issue-{N}.json, this is a pure function of the item's labels/board fields
// plus the repo's routing config — so the risk_high floor, force_full_pipeline
// override, and config-driven change_rules (#4125) all flow through reliably.
// A missing/unreadable config leaves routing.DefaultChangeRules() in force.
// traceAlternatives converts router alternatives to the trace payload shape
// (#179). Returns nil for an empty slice so omitempty keeps the event terse.
func traceAlternatives(alts []routing.Alternative) []trace.RoutingAlternative {
	if len(alts) == 0 {
		return nil
	}
	out := make([]trace.RoutingAlternative, len(alts))
	for i, a := range alts {
		out[i] = trace.RoutingAlternative{Model: a.Model, TradeOff: a.TradeOff}
	}
	return out
}

func deriveRoutingDecision(workspaceRoot string, item types.BoardItem) routing.Decision {
	in := routing.DeriveInput{
		Title:         item.Title,
		Labels:        item.Labels,
		BoardSize:     string(item.Size),
		BoardPriority: string(item.Priority),
	}
	if cfg, err := config.Load(workspaceRoot); err == nil && cfg != nil && cfg.Routing != nil {
		in.ForceFullPipeline = cfg.Routing.ForceFullPipeline
		in.ChangeRules = cfg.Routing.ChangeRules
	}
	return routing.Derive(in)
}

// gateRelaxContext returns ctx augmented with the gate-relaxation flag (#4128)
// when the repo has opted the named PR gate into trivial-change relaxation AND
// the authoritative post-dev diff classifies into the configured class set. The
// classification runs on the REAL changed files (changedFilesAgainstBase), so a
// mislabeled "docs" change that actually touched source is never relaxed — the
// classifier is the drift-revoke check. A telemetry event records the decision
// (relaxed or not) for the audit trail.
func (s *Scheduler) gateRelaxContext(ctx context.Context, stage state.PipelineStage, workspaceRoot string, item types.BoardItem, runtime *state.RuntimeState) context.Context {
	gateName := string(stage) // "pr-create" | "pr-merge"
	cfg, err := config.Load(workspaceRoot)
	if err != nil || cfg == nil {
		return ctx
	}
	relaxClasses := cfg.Pipeline.RelaxClassesFor(gateName)
	if len(relaxClasses) == 0 {
		return ctx // relaxation is strictly opt-in; default is the full gate
	}

	relaxed, class := gates.RelaxDecision(changedFilesAgainstBase(stageWorkspace(runtime, workspaceRoot)), relaxClasses)
	if s.telemetrySvc != nil && s.telemetryEnabled {
		s.telemetrySvc.EmitPipelineEvent(ctx, platform.PipelineEvent{
			RunID:       runtime.RunID,
			IssueNumber: item.Number,
			EventType:   "gate.relaxation",
			Stage:       gateName,
			Timestamp:   time.Now(),
			Metadata: map[string]interface{}{
				"relaxed":      relaxed,
				"change_class": class,
			},
			SchemaVersion: "1",
		})
	}
	if relaxed {
		log.Printf("#%d: %s gate relaxed for trivial change (class=%s)", item.Number, gateName, class)
		return gates.WithRelaxed(ctx, true)
	}
	return ctx
}

// changedFilesAgainstBase lists files changed on the current branch relative to
// origin/main (falling back to main), name-only. Fail-safe: any error returns
// nil, so RelaxDecision classifies it as Empty and the gate is NOT relaxed —
// the conservative direction.
func changedFilesAgainstBase(workspaceRoot string) []string {
	for _, base := range []string{"origin/main", "main"} {
		cmd := exec.Command("git", "diff", "--name-only", base+"...HEAD")
		cmd.Dir = workspaceRoot
		out, err := cmd.Output()
		if err != nil {
			continue
		}
		var files []string
		for _, line := range strings.Split(string(out), "\n") {
			if f := strings.TrimSpace(line); f != "" {
				files = append(files, f)
			}
		}
		return files
	}
	return nil
}

// effectivePrereqContextType returns the input-context prefix a stage should
// consume, walking back past any SKIPPED prerequisite stages (#4126/#4129). A
// skipped stage wrote no output context, so a fast-tracked run (docs-only skips
// feature-planning + feature-validate) must fall back to the nearest upstream
// stage that actually ran — ultimately issue-pickup's "issue" context. Returns
// ("", false) when the stage has no prerequisite (issue-pickup).
func effectivePrereqContextType(stage state.PipelineStage, runtime *state.RuntimeState) (string, bool) {
	prereq, ok := stagePrerequisites[stage]
	if !ok {
		return "", false
	}
	for runtime != nil && runtime.IsStageSkipped(prereq.Stage) {
		next, ok := stagePrerequisites[prereq.Stage]
		if !ok {
			break // reached the base; consume whatever prereq currently names
		}
		prereq = next
	}
	return prereq.ContextType, true
}

// stageWorkspace returns the directory the pipeline stages actually executed in
// for this run: the isolated worktree when one was created (the `nightgauge
// run` CLI and autonomous worktree mode), else the main workspace root (in-place
// VSCode/headless runs, where WorktreeDir is empty). Post-condition gates and the
// post-dev change classification MUST use this, not workspaceRoot — otherwise on
// a worktree-isolated run they inspect the main tree (which has none of the
// stage's output) and fail: the issue-pickup gate read the main root and failed
// "issue context file missing" although the subagent had written the context in
// the worktree.
func stageWorkspace(runtime *state.RuntimeState, workspaceRoot string) string {
	if runtime != nil && runtime.WorktreeDir != "" {
		return runtime.WorktreeDir
	}
	return workspaceRoot
}

// resolveRecordedChangeClass returns the change_class to record on the run
// record (#4129). It prefers the value captured DURING the run (when the
// worktree + diff still existed); only if that is empty — e.g. an in-place run,
// or a failure before any content stage — does it fall back to classifying the
// current tree, which may be empty if the worktree was already archived.
func resolveRecordedChangeClass(snap *state.RuntimeState, workspaceRoot string) string {
	if snap != nil && snap.AuthoritativeChangeClass != "" {
		return snap.AuthoritativeChangeClass
	}
	return authoritativeChangeClass(stageWorkspace(snap, workspaceRoot))
}

// authoritativeChangeClass classifies the run's REAL post-dev diff (#4129) so
// it can be recorded on the run record's routing.change_class for the
// `cost by-class` reporter. Uses the same authoritative classifier as the gate
// drift-revoke (#4128); fail-safe to "" when the diff can't be computed.
func authoritativeChangeClass(workspaceRoot string) string {
	files := changedFilesAgainstBase(workspaceRoot)
	if len(files) == 0 {
		return ""
	}
	return string(changeClassifier.ClassifyDefault(files))
}

// schedulerSkippableStages maps a routing Decision's skip_stages to the subset
// the scheduler is allowed to skip. Only feature-planning and feature-validate
// are honored — feature-dev/pr-create/pr-merge always run so every pipeline
// still produces and merges a PR, even if a change_rule lists them. The result
// is keyed by state.PipelineStage for direct membership checks in the loop.
func schedulerSkippableStages(skip []string) map[state.PipelineStage]bool {
	out := map[state.PipelineStage]bool{}
	for _, s := range skip {
		switch s {
		case string(state.StageFeaturePlanning):
			out[state.StageFeaturePlanning] = true
		case string(state.StageFeatureValidate):
			out[state.StageFeatureValidate] = true
		}
	}
	return out
}

// loadLatestRetro reads the most recent retro file for an issue and returns
// a summary of findings for injection into escalated retry context.
func loadLatestRetro(workspaceRoot string, issueNumber int, failedStage string) string {
	retroDir := filepath.Join(workspaceRoot, ".nightgauge", "retros")
	entries, err := os.ReadDir(retroDir)
	if err != nil {
		return ""
	}

	// Find the most recent retro for this issue (files are date-prefixed, sorted ascending)
	var latestRetroPath string
	prefix := fmt.Sprintf("_%d_retro.json", issueNumber)
	for _, e := range entries {
		if !e.IsDir() && len(e.Name()) > len(prefix) && e.Name()[len(e.Name())-len(prefix):] == prefix {
			latestRetroPath = filepath.Join(retroDir, e.Name())
		}
	}
	if latestRetroPath == "" {
		return ""
	}

	data, err := os.ReadFile(latestRetroPath)
	if err != nil {
		return ""
	}

	var retro struct {
		IssueNumber int    `json:"issue_number"`
		FailedStage string `json:"failed_stage"`
		Findings    []struct {
			Category       string   `json:"category"`
			Severity       string   `json:"severity"`
			Summary        string   `json:"summary"`
			Evidence       []string `json:"evidence"`
			Recommendation string   `json:"recommendation"`
		} `json:"findings"`
	}
	if err := json.Unmarshal(data, &retro); err != nil {
		return ""
	}

	if len(retro.Findings) == 0 {
		return ""
	}

	// Build a concise summary for injection
	summary := fmt.Sprintf("PRIOR FAILURE (stage: %s):\n", retro.FailedStage)
	for _, f := range retro.Findings {
		summary += fmt.Sprintf("- [%s/%s] %s\n", f.Category, f.Severity, f.Summary)
		if f.Recommendation != "" {
			summary += fmt.Sprintf("  Fix: %s\n", f.Recommendation)
		}
		for _, e := range f.Evidence {
			summary += fmt.Sprintf("  Evidence: %s\n", e)
		}
	}
	return summary
}

// loadFeatureBranch reads the branch name from the issue context JSON.
func loadFeatureBranch(workspaceRoot string, issueNumber int) string {
	path := filepath.Join(workspaceRoot, ".nightgauge", "pipeline",
		fmt.Sprintf("issue-%d.json", issueNumber))
	data, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	var ctx struct {
		Branch string `json:"branch"`
	}
	if err := json.Unmarshal(data, &ctx); err != nil {
		return ""
	}
	return ctx.Branch
}

// shouldReRoute returns true if performance-mode.yaml is strictly newer than
// the issue context file. Non-fatal: missing files return false (no re-route).
func (s *Scheduler) shouldReRoute(workspaceRoot string, issueNumber int) bool {
	perfModePath := filepath.Join(workspaceRoot, ".nightgauge", "performance-mode.yaml")
	perfModeInfo, err := os.Stat(perfModePath)
	if err != nil {
		return false
	}
	contextPath := filepath.Join(workspaceRoot, ".nightgauge", "pipeline",
		fmt.Sprintf("issue-%d.json", issueNumber))
	contextInfo, err := os.Stat(contextPath)
	if err != nil {
		return false
	}
	return perfModeInfo.ModTime().After(contextInfo.ModTime())
}

// reRouteContext re-routes an in-flight issue when performance-mode changed.
// Updates routing.pickup_recommendation.dev_model in the issue context JSON
// using a fresh router call. Writes atomically (temp + rename). Returns the
// full recommendation so the caller can trace the decision with its
// reasoning and rejected alternatives (#179).
func (s *Scheduler) reRouteContext(ctx context.Context, workspaceRoot string, issueNumber int, oldModel string) (routing.Recommendation, error) {
	contextPath := filepath.Join(workspaceRoot, ".nightgauge", "pipeline",
		fmt.Sprintf("issue-%d.json", issueNumber))

	data, err := os.ReadFile(contextPath)
	if err != nil {
		return routing.Recommendation{}, fmt.Errorf("read context: %w", err)
	}

	var raw map[string]interface{}
	if err := json.Unmarshal(data, &raw); err != nil {
		return routing.Recommendation{}, fmt.Errorf("unmarshal context: %w", err)
	}

	// Extract complexity score (preserved — not re-estimated)
	complexityScore := 0
	if routingRaw, ok := raw["routing"].(map[string]interface{}); ok {
		if cs, ok := routingRaw["complexity_score"].(float64); ok {
			complexityScore = int(cs)
		}
	}

	// Get fresh recommendation using the stateless router (reads current perf-mode)
	router := routing.NewRouter(nil, workspaceRoot)
	rec := router.Route(ctx, "feature-dev", complexity.Score{Value: complexityScore})

	// Update only routing fields — complexity and other invariants are unchanged
	if routingRaw, ok := raw["routing"].(map[string]interface{}); ok {
		if pickupRec, ok := routingRaw["pickup_recommendation"].(map[string]interface{}); ok {
			pickupRec["dev_model"] = rec.Model
		} else {
			routingRaw["pickup_recommendation"] = map[string]interface{}{"dev_model": rec.Model}
		}
		routingRaw["rationale"] = rec.Reasoning
	}

	updated, err := json.MarshalIndent(raw, "", "  ")
	if err != nil {
		return routing.Recommendation{}, fmt.Errorf("marshal context: %w", err)
	}

	// Validate JSON before writing
	var check interface{}
	if err := json.Unmarshal(updated, &check); err != nil {
		return routing.Recommendation{}, fmt.Errorf("validate updated context: %w", err)
	}

	// Atomic write: temp file + rename to avoid partial writes
	tmpPath := contextPath + ".tmp"
	if err := os.WriteFile(tmpPath, updated, 0o644); err != nil {
		return routing.Recommendation{}, fmt.Errorf("write temp context: %w", err)
	}
	if err := os.Rename(tmpPath, contextPath); err != nil {
		_ = os.Remove(tmpPath)
		return routing.Recommendation{}, fmt.Errorf("rename context: %w", err)
	}

	if rec.Model != oldModel {
		log.Printf("[router] re-evaluated #%d due to perf-mode change: dev_model=%s→%s",
			issueNumber, oldModel, rec.Model)
	}

	return rec, nil
}

// loadGateResults reads quality gate results for the given issue.
func loadGateResults(workspaceRoot string, issueNumber int) []state.GateResult {
	results, err := state.ReadGateMetricsForIssue(workspaceRoot, issueNumber)
	if err != nil {
		log.Printf("#%d: failed to read gate metrics: %v", issueNumber, err)
		return nil
	}
	return results
}

// loadPrUrl reads the PR URL from the pr-create stage's output context
// (pr-<N>.json — prefix "pr", matching stageOutputContextType[StagePRCreate]
// and loadPRNumberForRecovery, not "pr-create").
func loadPrUrl(workspaceRoot string, issueNumber int) string {
	path := stagecontext.ContextPath(workspaceRoot, issueNumber, "pr")
	data, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	var ctx struct {
		PrUrl string `json:"pr_url"`
	}
	if err := json.Unmarshal(data, &ctx); err != nil {
		return ""
	}
	return ctx.PrUrl
}

// loadPRNumberForRecovery loads pr_number from pr-{N}.json if present so the
// FailureRecovery registry's StageFailure carries enough context for actions
// to dispatch on. Returns 0 when the file is absent or malformed — the
// recovery actions treat 0 as "unknown PR" and decline accordingly.
func loadPRNumberForRecovery(workspaceRoot string, issueNumber int) int {
	path := filepath.Join(workspaceRoot, ".nightgauge", "pipeline",
		fmt.Sprintf("pr-%d.json", issueNumber))
	data, err := os.ReadFile(path)
	if err != nil {
		return 0
	}
	var ctx struct {
		PrNumber int `json:"pr_number"`
	}
	if err := json.Unmarshal(data, &ctx); err != nil {
		return 0
	}
	return ctx.PrNumber
}

// recordOutcome builds a learning.Outcome from the pipeline result, records it,
// and returns the predicted-vs-actual routing decisions for the run record.
func (s *Scheduler) recordOutcome(item types.BoardItem, snap *state.RuntimeState, success bool, complexityScore int, predictedModel string) *state.OutcomePrediction {
	if s.recorder == nil {
		return nil
	}
	var failedStage string
	if !success {
		failedStage = string(snap.Stage)
	}
	// ActualModel: predicted is the proxy — EXCEPT when the CLI's refusal
	// fallback swapped models mid-run (#91), in which case the last served
	// model is recorded so learning/eval data isn't attributed to a model
	// that never produced the output.
	actualModel := predictedModel
	if m := snap.LastRefusalServedModel(); m != "" {
		actualModel = m
	}
	outcome := learning.Outcome{
		IssueNumber:     item.Number,
		Repo:            item.Repo,
		PredictedSize:   predictedSizeLabel(complexityScore),
		PredictedModel:  predictedModel,
		ActualModel:     actualModel,
		Success:         success,
		DurationMs:      snap.TotalDuration().Milliseconds(),
		InputTokens:     snap.InputTokens,
		OutputTokens:    snap.OutputTokens,
		CostUSD:         snap.TotalCostUSD,
		ComplexityScore: complexityScore,
		FailedStage:     failedStage,
		CompletedAt:     time.Now(),
	}
	if s.recorder != nil {
		if err := s.recorder.Record(outcome); err != nil {
			log.Printf("#%d: failed to record outcome: %v", item.Number, err)
		}
	}

	// The telemetry push happens in recordV2History with the exact record
	// written to history — a previous "mirror" builder here drifted and pushed
	// sparse/wrong records to the platform (#261). Return the prediction so
	// the caller can thread it into that single record.
	return &state.OutcomePrediction{
		PredictedSize:  outcome.PredictedSize,
		ActualSize:     outcome.ActualSize,
		PredictedModel: outcome.PredictedModel,
		ActualModel:    outcome.ActualModel,
	}
}

// recordV2History writes a V2/V3-format execution history record to the daily JSONL
// file that the VSCode dashboard reads. When terminalFailureKind is non-empty
// (Issue #3001), the record is bumped to V3 with the kind and per-stage output
// tails populated. Non-fatal: errors are logged but not propagated.
func (s *Scheduler) recordV2History(
	item types.BoardItem,
	snap *state.RuntimeState,
	success bool,
	workspaceRoot string,
	complexityScore int,
	routingPath string,
	terminalFailureKind string,
	stageFailureCategories map[string]string,
	prediction *state.OutcomePrediction,
) {
	hw := state.NewHistoryWriter(workspaceRoot)
	branch := loadFeatureBranch(workspaceRoot, item.Number)

	issueType := state.ExtractTypeFromLabels(item.Labels)

	errMsg := ""
	if !success && snap.Stage != "" {
		if stageErr, ok := snap.StageErrors[string(snap.Stage)]; ok {
			errMsg = stageErr
		}
	}

	if routingPath == "" {
		routingPath = "standard"
	}

	input := state.V2RunInput{
		Title:                  item.Title,
		Body:                   snap.Body,
		Branch:                 branch,
		BaseBranch:             "main",
		Labels:                 item.Labels,
		Size:                   string(item.Size),
		IssueType:              issueType,
		ComplexityScore:        complexityScore,
		RoutingPath:            routingPath,
		SkipStages:             snap.SkippedStages,
		ChangeClass:            resolveRecordedChangeClass(snap, workspaceRoot),
		TerminalFailureKind:    terminalFailureKind,
		StageOutputTails:       snap.StageOutputTails,
		StageFailureCategories: stageFailureCategories,
		OutcomeType:            OutcomeTypeForTerminalFailure(errMsg),
	}

	// Build ONCE, write, and push the SAME record. The telemetry push used to
	// go through a separate "mirror" builder that drifted from this one and
	// shipped sparse/wrong records to the platform (#261) — the record written
	// to history is the single source of truth for both sinks.
	now := time.Now()
	record := hw.BuildV2Record(snap, success, errMsg, input, now)

	// Attach gate results (best-effort — missing file is not an error) and the
	// predicted-vs-actual routing decisions from recordOutcome.
	gateResults, gateErr := state.ReadGateMetricsForIssue(workspaceRoot, item.Number)
	if gateErr != nil {
		log.Printf("#%d: warning: failed to read gate metrics: %v", item.Number, gateErr)
	}
	record.GateResults = gateResults
	record.OutcomePrediction = prediction

	if err := hw.WriteV2Record(record, now); err != nil {
		log.Printf("#%d: failed to write V2 history: %v", item.Number, err)
	} else {
		log.Printf("#%d: V2 execution history recorded (terminal_kind=%q)",
			item.Number, terminalFailureKind)
	}

	if s.telemetrySvc != nil && s.telemetryEnabled {
		s.telemetrySvc.PushPipelineRun(context.Background(), record)
	}
}

// predictedSizeLabel maps a complexity score to a size label.
func predictedSizeLabel(score int) string {
	switch {
	case score <= 3:
		return "small"
	case score <= 6:
		return "medium"
	default:
		return "large"
	}
}

// isHaikuModel checks whether a model string refers to Haiku.
func isHaikuModel(model string) bool {
	return strings.Contains(model, "haiku")
}

// devContextBuildPassed reads dev-{N}.json and returns true when
// build_verification.ran=true and build_verification.status="passed".
// Returns false on any read/parse error (safe default: don't allow haiku).
func devContextBuildPassed(workspaceRoot string, issueNumber int) bool {
	p := filepath.Join(workspaceRoot, ".nightgauge", "pipeline",
		fmt.Sprintf("dev-%d.json", issueNumber))
	data, err := os.ReadFile(p)
	if err != nil {
		return false
	}
	var ctx struct {
		BuildVerification struct {
			Ran    bool   `json:"ran"`
			Status string `json:"status"`
		} `json:"build_verification"`
	}
	if err := json.Unmarshal(data, &ctx); err != nil {
		return false
	}
	return ctx.BuildVerification.Ran && ctx.BuildVerification.Status == "passed"
}

// getDiffLineCount returns total lines changed (insertions + deletions) vs main.
// Returns 0 on any error so callers fall through to the default model.
func getDiffLineCount(workspaceRoot string) int {
	cmd := exec.Command("git", "diff", "main", "--shortstat")
	cmd.Dir = workspaceRoot
	out, err := cmd.Output()
	if err != nil {
		return 0
	}
	re := regexp.MustCompile(`(\d+) insertion`)
	insMatch := re.FindStringSubmatch(string(out))
	ins := 0
	if len(insMatch) > 1 {
		ins, _ = strconv.Atoi(insMatch[1])
	}
	re = regexp.MustCompile(`(\d+) deletion`)
	delMatch := re.FindStringSubmatch(string(out))
	del := 0
	if len(delMatch) > 1 {
		del, _ = strconv.Atoi(delMatch[1])
	}
	return ins + del
}

// ensureEpicBranchForItem creates the epic base branch when dispatching the first
// sub-issue of an epic. It is non-blocking: errors are logged and do not abort
// the pipeline. The TypeScript enforceEpicBaseBranch() will fall back to main
// if the branch still does not exist.
func (s *Scheduler) ensureEpicBranchForItem(ctx context.Context, workspaceRoot string, item types.BoardItem) {
	if !getAutoCreateEpicBranch(workspaceRoot) {
		log.Printf("#%d: auto_create_epic_branch disabled — skipping epic branch creation", item.Number)
		return
	}

	gitSvc, err := git.NewService(workspaceRoot)
	if err != nil {
		log.Printf("#%d: epic branch auto-create: git service unavailable: %v", item.Number, err)
		return
	}

	// Prefer ParentTitle from board data; fall back to GitHub API
	epicTitle := item.ParentTitle
	if epicTitle == "" {
		owner, repo := splitOwnerRepo(item.Repo)
		epicIssue, apiErr := s.issueSvc.GetIssue(ctx, owner, repo, item.ParentNumber)
		if apiErr != nil {
			log.Printf("#%d: epic branch auto-create: fetch epic #%d title: %v", item.Number, item.ParentNumber, apiErr)
			return
		}
		epicTitle = epicIssue.Title
	}

	branchName, created, err := gitSvc.EnsureEpicBranch(item.ParentNumber, epicTitle)
	if err != nil {
		log.Printf("#%d: epic branch auto-create: %v", item.Number, err)
		return
	}

	if created {
		log.Printf("#%d: epic branch created: %s", item.Number, branchName)
	} else {
		log.Printf("#%d: epic branch already exists: %s", item.Number, branchName)
	}
}

// getAutoCreateEpicBranch returns whether epic branch auto-creation is enabled.
// Reads from NIGHTGAUGE_PIPELINE_AUTO_CREATE_EPIC_BRANCH env var (default true),
// then falls back to config.yaml pipeline.auto_create_epic_branch.
func getAutoCreateEpicBranch(workspaceRoot string) bool {
	if v := os.Getenv("NIGHTGAUGE_PIPELINE_AUTO_CREATE_EPIC_BRANCH"); v != "" {
		return v != "false" && v != "0"
	}

	configPath := filepath.Join(workspaceRoot, ".nightgauge", "config.yaml")
	data, err := os.ReadFile(configPath)
	if err != nil {
		return true // default: enabled
	}
	inPipeline := false
	for _, line := range strings.Split(string(data), "\n") {
		trimmed := strings.TrimSpace(line)
		if trimmed == "pipeline:" {
			inPipeline = true
			continue
		}
		if inPipeline && trimmed != "" && !strings.HasPrefix(trimmed, "#") && !strings.HasPrefix(line, " ") {
			inPipeline = false
		}
		if inPipeline && strings.HasPrefix(trimmed, "auto_create_epic_branch:") {
			parts := strings.SplitN(trimmed, ":", 2)
			if len(parts) == 2 {
				val := strings.TrimSpace(parts[1])
				return val != "false" && val != "no" && val != "0"
			}
		}
	}
	return true // default: enabled
}

// getLargeDiffThreshold returns the configured lines-changed threshold for
// pr-create model escalation. Reads from NIGHTGAUGE_PIPELINE_LARGE_DIFF_THRESHOLD
// env var, then falls back to config.yaml pipeline.large_diff_threshold, then default 500.
func getLargeDiffThreshold(workspaceRoot string) int {
	const defaultThreshold = 500

	if v := os.Getenv("NIGHTGAUGE_PIPELINE_LARGE_DIFF_THRESHOLD"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n >= 0 {
			return n
		}
	}

	configPath := filepath.Join(workspaceRoot, ".nightgauge", "config.yaml")
	data, err := os.ReadFile(configPath)
	if err != nil {
		return defaultThreshold
	}
	inPipeline := false
	for _, line := range strings.Split(string(data), "\n") {
		trimmed := strings.TrimSpace(line)
		if trimmed == "pipeline:" {
			inPipeline = true
			continue
		}
		if inPipeline && trimmed != "" && !strings.HasPrefix(trimmed, "#") && !strings.HasPrefix(line, " ") {
			inPipeline = false
		}
		if inPipeline && strings.HasPrefix(trimmed, "large_diff_threshold:") {
			parts := strings.SplitN(trimmed, ":", 2)
			if len(parts) == 2 {
				if n, err := strconv.Atoi(strings.TrimSpace(parts[1])); err == nil && n >= 0 {
					return n
				}
			}
		}
	}
	return defaultThreshold
}

// getAnomalyFloorUSD returns the configured cost floor for the
// atomic-LLM-overrun anomaly detector (Issue #3267). Resolution order:
// env var NIGHTGAUGE_PIPELINE_ANOMALY_FLOOR_USD → config.yaml
// pipeline.anomaly_floor_usd → DefaultAnomalyFloorUSD ($0.01).
// isTerminalStage reports whether a stage's success is defined by work landing
// on the forge (an open or merged PR). For these stages the post-condition gate
// is an unambiguous real-world check, so a non-zero skill exit that nonetheless
// satisfies the gate is a false alarm to be reconciled, not a failure (#3835).
func isTerminalStage(stage state.PipelineStage) bool {
	return stage == state.StagePRCreate || stage == state.StagePRMerge
}

func getAnomalyFloorUSD(workspaceRoot string) float64 {
	if v := os.Getenv("NIGHTGAUGE_PIPELINE_ANOMALY_FLOOR_USD"); v != "" {
		if n, err := strconv.ParseFloat(v, 64); err == nil && n > 0 {
			return n
		}
	}

	configPath := filepath.Join(workspaceRoot, ".nightgauge", "config.yaml")
	data, err := os.ReadFile(configPath)
	if err != nil {
		return gates.DefaultAnomalyFloorUSD
	}
	inPipeline := false
	for _, line := range strings.Split(string(data), "\n") {
		trimmed := strings.TrimSpace(line)
		if trimmed == "pipeline:" {
			inPipeline = true
			continue
		}
		if inPipeline && trimmed != "" && !strings.HasPrefix(trimmed, "#") && !strings.HasPrefix(line, " ") {
			inPipeline = false
		}
		if inPipeline && strings.HasPrefix(trimmed, "anomaly_floor_usd:") {
			parts := strings.SplitN(trimmed, ":", 2)
			if len(parts) == 2 {
				if n, err := strconv.ParseFloat(strings.TrimSpace(parts[1]), 64); err == nil && n > 0 {
					return n
				}
			}
		}
	}
	return gates.DefaultAnomalyFloorUSD
}

// verifyPRMerged is the single fail-closed authority on whether the pr-merge
// stage actually landed. It queries GitHub to confirm the PR reached state
// MERGED AND that the linked issue is CLOSED, naming the precise blocker when
// it did not. Called after EVERY pr-merge attempt (deterministic and LLM
// paths) so a skill that returns exit 0 without merging — masked cleanup
// failure, a ruleset/review block treated as non-fatal, CI flipping red
// between the mergeability check and the record write — yields an explicit
// pipeline-failed outcome rather than outcome=complete on an unmerged PR.
// See issues #2843 and #4070.
//
// Blocker classification (when state != MERGED) reuses the deterministic
// runner's reason vocabulary (pmstages.ReasonNotMergeable / ReasonDirtyState
// / ReasonReviewMissing) so telemetry buckets stay consistent across the two
// pr-merge paths and the precise reason flows into stage_error metadata for
// the stuck-epic detector (#4073) to consume.
//
// Returns (true, "") on confirmed merge (with the linked issue CLOSED, or when
// the issue-state check is inconclusive) or when verification isn't applicable
// (no PR URL recorded, or no GitHub client wired — test scheduler path).
// Returns (false, reason) when the PR URL is known but the PR is not MERGED, or
// when it is MERGED but the linked issue is still OPEN.
//
// Inconclusive-on-transient-error policy: a GetPR/GetIssue error (GitHub
// eventual-consistency, transient API failure) must NOT flap the pipeline into
// a hard failure. Such errors are treated as inconclusive — logged, then we
// trust the MERGED signal — matching the existing empty-prURL / nil-client
// tolerance. We only fail closed on an OBSERVED non-MERGED PR or an OBSERVED
// still-OPEN linked issue.
func (s *Scheduler) verifyPRMerged(ctx context.Context, prURL string, issueNumber int) (bool, string) {
	if prURL == "" {
		log.Printf("verifyPRMerged: no PR URL recorded — deferring to pr-merge skill exit code")
		return true, ""
	}
	owner, repoName, number, err := parsePRURL(prURL)
	if err != nil {
		return false, fmt.Sprintf("could not parse PR URL %q: %v", prURL, err)
	}
	if s.client == nil {
		// Tests that skip wiring a GitHub client shouldn't fail pipelines; log
		// and trust the skill's exit code in that case.
		log.Printf("verifyPRMerged: no GitHub client configured, skipping verification for %s/%s#%d", owner, repoName, number)
		return true, ""
	}
	prSvc := gh.NewPRService(s.client)
	pr, err := prSvc.GetPR(ctx, owner, repoName, number)
	if err != nil {
		// Inconclusive: a transient fetch error must not be reported as a
		// non-merge. Log and trust the upstream MERGED signal (#4070).
		log.Printf("verifyPRMerged: fetch PR %s/%s#%d failed (%v) — inconclusive, trusting pr-merge result", owner, repoName, number, err)
		return true, ""
	}
	if pr.State != "MERGED" {
		blocker := classifyMergeBlocker(pr)
		return false, fmt.Sprintf("PR %s/%s#%d not merged — %s", owner, repoName, number, blocker)
	}

	// PR is MERGED — this is the authoritative success signal and the fail-closed
	// guard against phantom success (a non-MERGED PR returned above with a named
	// blocker). The linked issue's closure is OWNED by the post-merge path
	// (checkEpicCompletion → EvaluatePostMerge → CloseIssue), which runs AFTER
	// this verifier. So a still-OPEN linked issue here is NOT a merge failure: it
	// is either GitHub's `Closes #N` auto-close not yet propagated, or a PR whose
	// body lacks the keyword (the explicit CloseIssue will handle it). Surface it
	// as a warning rather than hard-failing a genuinely merged PR — hard-failing
	// here would revert a successful merge to Ready and read as a stall (#4070
	// review: assert-before-close race).
	if issueNumber > 0 {
		issue, issErr := s.issueServiceFor(ctx, owner, repoName).GetIssue(ctx, owner, repoName, issueNumber)
		switch {
		case issErr != nil:
			log.Printf("verifyPRMerged: PR %s/%s#%d is MERGED but GetIssue #%d failed (%v) — trusting MERGED (close owned by post-merge)",
				owner, repoName, number, issueNumber, issErr)
		case issue.State != "CLOSED":
			log.Printf("verifyPRMerged: PR %s/%s#%d is MERGED but linked issue #%d is still %s — the post-merge close will reconcile it",
				owner, repoName, number, issueNumber, issue.State)
		}
	}
	return true, ""
}

// verifyPRMergeForStage runs the post-stage MERGED verification for a pr-merge
// stage and handles a non-MERGED result (record the named blocker as a stage
// error, emit state, fire failure telemetry). It returns true when verification
// FAILED — the caller MUST abort the pipeline (return) — and false when the
// merge is confirmed, in which case it also triggers the post-merge close +
// epic-completion check. reasonPrefix labels how the (unverified) merge was
// reached ("" for the normal success tail, "budget-shipped" for the budget
// fast-advance) so EVERY route to "pr-merge done" passes through one MERGED
// check — no path reports merge success without it (#4070).
func (s *Scheduler) verifyPRMergeForStage(ctx context.Context, item types.BoardItem, runtime *state.RuntimeState, reasonPrefix string) bool {
	snap := runtime.Snapshot()
	merged, reason := s.verifyPRMerged(ctx, snap.PrUrl, item.Number)
	if !merged {
		label := reason
		if reasonPrefix != "" {
			label = reasonPrefix + ": " + reason
		}
		runtime.SetStageError(state.StagePRMerge, fmt.Sprintf("pr-merge verification: %s", label))
		s.emitStateChanged(item.Repo, item.Number, runtime)
		log.Printf("#%d: pr-merge stage produced no verified merge — failing pipeline (%s)", item.Number, label)
		if s.telemetrySvc != nil && s.telemetryEnabled {
			s.telemetrySvc.EmitPipelineEvent(ctx, platform.PipelineEvent{
				RunID:       runtime.RunID,
				IssueNumber: item.Number,
				EventType:   "stage_error",
				Stage:       string(state.StagePRMerge),
				Timestamp:   time.Now(),
				Metadata: map[string]interface{}{
					"error":           "pr-merge verification failed",
					"reason":          label,
					"skill_exit_code": 0,
				},
				SchemaVersion: "1",
			})
		}
		return true
	}
	// Confirmed merged — own the sub-issue close + parent-epic auto-close.
	var mergedPRNumber int
	if _, _, prNum, parseErr := parsePRURL(snap.PrUrl); parseErr == nil {
		mergedPRNumber = prNum
	}
	pmResult := s.checkEpicCompletion(ctx, item, mergedPRNumber)

	// (#4133) Persist the post-merge ground-truth breadcrumb onto runtime state
	// so the run record carries the merge commit SHA + mergedAt. Best-effort:
	// SetMergeOutcome ignores empty values and a persist failure is logged, not
	// fatal — the merge has already happened.
	if pmResult.MergedCommitSha != "" || pmResult.MergedAt != "" {
		runtime.SetMergeOutcome(pmResult.MergedCommitSha, pmResult.MergedAt)
		if s.workspaceRoot != "" {
			if persistErr := runtime.Persist(filepath.Join(s.workspaceRoot, ".nightgauge", "pipeline")); persistErr != nil {
				log.Printf("#%d: warning: failed to persist merge breadcrumb: %v", item.Number, persistErr)
			}
		}
	}

	// (#4151) Seed a pending post-merge survival record for eligible single-issue
	// merges. Best-effort and strictly non-blocking — the merge has already
	// landed; a store failure is logged, never fatal. The reconcile sweep later
	// finalizes the record (survived / reverted / broke / unobserved).
	if pmResult.SurvivalEligible && s.workspaceRoot != "" {
		store := survival.NewStore(s.workspaceRoot)
		rec := survival.NewPending(item.Repo, item.Number, mergedPRNumber, pmResult.MergedCommitSha, pmResult.MergedAt, "")
		if added, appErr := store.Append(rec); appErr != nil {
			log.Printf("#%d: warning: failed to record survival breadcrumb: %v", item.Number, appErr)
		} else if added {
			log.Printf("#%d: recorded pending survival record (merge %s)", item.Number, survivalShortSHA(pmResult.MergedCommitSha))
		}
	}
	return false
}

// survivalShortSHA abbreviates a commit SHA for log lines.
func survivalShortSHA(sha string) string {
	if len(sha) > 7 {
		return sha[:7]
	}
	return sha
}

// classifyMergeBlocker builds a precise, telemetry-stable reason string for a
// PR that did not reach MERGED. It reuses the deterministic runner's reason
// vocabulary so the scheduler verifier and the deterministic Decide() path
// bucket the same blockers identically (#4070).
func classifyMergeBlocker(pr *types.PullRequest) string {
	switch {
	case pr.Mergeable == "CONFLICTING":
		return fmt.Sprintf("%s: %s", pmstages.ReasonNotMergeable, pr.Mergeable)
	case pr.MergeStateStatus == "DIRTY" ||
		pr.MergeStateStatus == "BEHIND" ||
		pr.MergeStateStatus == "BLOCKED" ||
		pr.MergeStateStatus == "UNSTABLE":
		return fmt.Sprintf("%s: %s", pmstages.ReasonDirtyState, pr.MergeStateStatus)
	case pr.ReviewStatus == "REVIEW_REQUIRED" || pr.ReviewStatus == "CHANGES_REQUESTED":
		return fmt.Sprintf("%s: %s", pmstages.ReasonReviewMissing, pr.ReviewStatus)
	default:
		return fmt.Sprintf("unflipped (state=%s)", pr.State)
	}
}

// tryDeterministicPRMerge runs the deterministic-first hook for the pr-merge
// stage (Issue #3264). When the runner reports `merged`, it records
// execution_path="deterministic" on the runtime, emits a telemetry event,
// and returns true so the caller skips s.stageRunner.RunStage. Otherwise it
// records execution_path="llm" and returns false so the LLM path runs as
// today. Always returns false for non-pr-merge stages or when the runner is
// unset.
//
// The third return value (rateLimited) is true when the deterministic runner
// punted SPECIFICALLY because GitHub is rate-limited. The caller must NOT fall
// through to the LLM path in that case — the skill would re-shell `gh pr merge`
// into the same exhausted bucket, a near-certain re-failure that burns
// $5–$25 of tokens and can leave the issue stuck "In review". Instead the
// caller fails the stage with a github-quota-low marker so it routes through
// the environmental recovery path (#3896). Issue #3976.
func (s *Scheduler) tryDeterministicPRMerge(
	ctx context.Context,
	stage state.PipelineStage,
	runtime *state.RuntimeState,
	item types.BoardItem,
	workspaceRoot string,
) (bool, string, bool) {
	if stage != state.StagePRMerge || s.prMergeRunner == nil {
		return false, "", false
	}

	// Read pr-{N}.json (and run `gh` from) the worktree the run's stages executed
	// in, not the canonical root (#275). pr-create writes pr-{N}.json into the
	// worktree's `.nightgauge/pipeline/`; on worktree-isolated runs the canonical
	// root has no such file, so a bare workspaceRoot made the runner punt
	// `missing-pr-context` and fall through to the LLM path every time. Mirrors the
	// pr-create fix and stageWorkspace's documented contract.
	stageWS := stageWorkspace(runtime, workspaceRoot)
	detResult, detErr := s.prMergeRunner.Run(ctx, item.Number, item.Repo, stageWS)
	if detErr == nil && detResult.Reason == pmstages.ReasonRateLimited {
		// Rate-limit punt → defer, do NOT run the LLM path. Leave execution_path
		// unset: neither path produced a result this attempt; the post-cooldown
		// retry records it accurately. Issue #3976.
		log.Printf("#%d: pr-merge deterministic path rate-limited — deferring (no LLM fallback) until GitHub bucket resets [#3976]",
			item.Number)
		return false, detResult.PRState, true
	}
	if detErr == nil && detResult.Path == pmstages.PathMerged {
		runtime.RecordExecutionPath(stage, "deterministic")
		log.Printf("#%d: pr-merge deterministic path: %s (PR #%d, %s, %dms)",
			item.Number, detResult.Path, detResult.PRNumber, detResult.Reason, detResult.DurationMs)
		if s.telemetrySvc != nil && s.telemetryEnabled {
			s.telemetrySvc.EmitPipelineEvent(ctx, platform.PipelineEvent{
				RunID:       runtime.RunID,
				IssueNumber: item.Number,
				EventType:   "stage_deterministic",
				Stage:       string(stage),
				Timestamp:   time.Now(),
				Metadata: map[string]interface{}{
					"path":        string(detResult.Path),
					"pr_number":   detResult.PRNumber,
					"pr_state":    detResult.PRState,
					"reason":      detResult.Reason,
					"duration_ms": detResult.DurationMs,
				},
				SchemaVersion: "1",
			})
		}
		return true, detResult.PRState, false
	}

	runtime.RecordExecutionPath(stage, "llm")
	puntReason := detResult.Reason
	if detErr != nil {
		puntReason = fmt.Sprintf("%s: %v", pmstages.ReasonUnexpected, detErr)
		log.Printf("#%d: pr-merge deterministic path errored — falling through to LLM: %v",
			item.Number, detErr)
	} else {
		log.Printf("#%d: pr-merge deterministic path punted (%s) — falling through to LLM",
			item.Number, detResult.Reason)
	}
	runtime.RecordStagePuntReason(stage, puntReason)
	s.emitStagePunt(ctx, runtime, stage, item.Number, puntReason)
	// Action Center branch-protection producer (ADR 015 §F #6): a punt on a
	// branch-protection / required-check / review block is a human-needed
	// dead-end no LLM retry can clear — surface an unblock card naming the exact
	// blocker. Other punts (CI wait, unexpected) fall through silently.
	if isBranchProtectionPunt(detResult.Reason) {
		s.raiseBranchProtectionBlock(item.Repo, item.Number, detResult.PRNumber, runtime.RunID, detResult.Reason)
	}
	return false, "", false
}

// parsePRURL extracts owner, repo, and number from a GitHub PR URL.
// Accepts both web URLs ("https://github.com/OWNER/REPO/pull/NUMBER") and
// API URLs ("https://api.github.com/repos/OWNER/REPO/pulls/NUMBER").
func parsePRURL(prURL string) (owner, repo string, number int, err error) {
	trimmed := strings.TrimSuffix(strings.TrimSpace(prURL), "/")
	parts := strings.Split(trimmed, "/")
	// Web URL  : [..., OWNER, REPO, "pull",  NUMBER] — number is parts[N-1], repo parts[N-3], owner parts[N-4]
	// API URL  : [..., OWNER, REPO, "pulls", NUMBER] — same offsets, different separator keyword.
	if len(parts) < 4 {
		return "", "", 0, fmt.Errorf("too few path segments")
	}
	last := parts[len(parts)-1]
	n, convErr := strconv.Atoi(last)
	if convErr != nil {
		return "", "", 0, fmt.Errorf("trailing segment %q is not a number", last)
	}
	sep := parts[len(parts)-2]
	if sep != "pull" && sep != "pulls" {
		return "", "", 0, fmt.Errorf("expected pull/pulls segment, got %q", sep)
	}
	if len(parts) < 4 {
		return "", "", 0, fmt.Errorf("missing owner/repo segments")
	}
	return parts[len(parts)-4], parts[len(parts)-3], n, nil
}

// tryDeterministicPRCreate runs the deterministic-first hook for the pr-create
// stage (Issue #3265). When the runner reports `created`, it records
// execution_path="deterministic" on the runtime, emits a telemetry event,
// captures the PR URL on the runtime so verifyPRMerged can re-use it, and
// returns true so the caller skips s.stageRunner.RunStage. Otherwise it
// records execution_path="llm" and returns (false, false) so the LLM path runs
// as today. Always returns (false, false) for non-pr-create stages or when the
// runner is unset.
//
// The second return value (rateLimited) is true when the punt was caused by a
// GitHub rate limit (the runner wraps the in-process client error, so we
// substring-match the rate-limit signal in the reason). As with pr-merge, the
// caller must defer rather than run the LLM path on a rate-limit punt. #3976.
func (s *Scheduler) tryDeterministicPRCreate(
	ctx context.Context,
	stage state.PipelineStage,
	runtime *state.RuntimeState,
	item types.BoardItem,
	workspaceRoot string,
) (bool, bool) {
	if stage != state.StagePRCreate || s.prCreateRunner == nil {
		return false, false
	}

	// Read stage context from the directory the run's stages actually executed
	// in — the isolated worktree when one exists, else workspaceRoot (#275). The
	// deterministic runner projects issue/dev/validate context from
	// `<workdir>/.nightgauge/pipeline/*-{N}.json`, and on worktree-isolated runs
	// (`pipeline.worktree_base` set) those files live ONLY in the worktree, never
	// in the canonical root — they are gitignored per-worktree local state. Passing
	// the bare workspaceRoot made DecideCreate see HasDev=false and punt
	// `missing-dev-context` on EVERY worktree-mode run (bowlsheet was 0-for-N),
	// forcing the expensive LLM fallback. stageWorkspace mirrors what the LLM path
	// (line ~2874) and the post-condition gates already use for the same reason.
	stageWS := stageWorkspace(runtime, workspaceRoot)
	detResult, detErr := s.prCreateRunner.Run(ctx, item.Number, item.Repo, stageWS)
	if detErr == nil && detResult.Path == pmstages.CreatePathPunt && ReasonIndicatesRateLimit(detResult.Reason) {
		// Rate-limit punt → defer, do NOT run the LLM path (#3976). Leave
		// execution_path unset; the post-cooldown retry records it accurately.
		log.Printf("#%d: pr-create deterministic path rate-limited (%s) — deferring (no LLM fallback) until GitHub bucket resets [#3976]",
			item.Number, detResult.Reason)
		return false, true
	}
	if detErr == nil && detResult.Path == pmstages.CreatePathCreated {
		runtime.RecordExecutionPath(stage, "deterministic")
		if detResult.PRURL != "" {
			runtime.SetPrUrl(detResult.PRURL)
		}
		log.Printf("#%d: pr-create deterministic path: %s (PR #%d, %s, %dms)",
			item.Number, detResult.Path, detResult.PRNumber, detResult.Reason, detResult.DurationMs)
		if s.telemetrySvc != nil && s.telemetryEnabled {
			s.telemetrySvc.EmitPipelineEvent(ctx, platform.PipelineEvent{
				RunID:       runtime.RunID,
				IssueNumber: item.Number,
				EventType:   "stage_deterministic",
				Stage:       string(stage),
				Timestamp:   time.Now(),
				Metadata: map[string]interface{}{
					"path":        string(detResult.Path),
					"pr_number":   detResult.PRNumber,
					"pr_url":      detResult.PRURL,
					"reason":      detResult.Reason,
					"duration_ms": detResult.DurationMs,
				},
				SchemaVersion: "1",
			})
		}
		return true, false
	}

	runtime.RecordExecutionPath(stage, "llm")
	puntReason := detResult.Reason
	if detErr != nil {
		puntReason = fmt.Sprintf("%s: %v", pmstages.ReasonUnexpected, detErr)
		log.Printf("#%d: pr-create deterministic path errored — falling through to LLM: %v",
			item.Number, detErr)
	} else {
		log.Printf("#%d: pr-create deterministic path punted (%s) — falling through to LLM",
			item.Number, detResult.Reason)
	}
	runtime.RecordStagePuntReason(stage, puntReason)
	s.emitStagePunt(ctx, runtime, stage, item.Number, puntReason)
	return false, false
}

// emitStagePunt emits the stage_punt telemetry event recording that a
// deterministic-first hook declined and the LLM path ran instead (Issue #297).
// The companion of the stage_deterministic event: together they make the
// execution-path decision observable on BOTH outcomes, so dashboards can
// distinguish "deterministic won" from "punted to LLM because <reason>" without
// scraping session logs. No-op when telemetry is disabled.
func (s *Scheduler) emitStagePunt(ctx context.Context, runtime *state.RuntimeState, stage state.PipelineStage, issueNumber int, reason string) {
	if s.telemetrySvc == nil || !s.telemetryEnabled {
		return
	}
	s.telemetrySvc.EmitPipelineEvent(ctx, platform.PipelineEvent{
		RunID:       runtime.RunID,
		IssueNumber: issueNumber,
		EventType:   "stage_punt",
		Stage:       string(stage),
		Timestamp:   time.Now(),
		Metadata: map[string]interface{}{
			"execution_path": "llm",
			"reason":         reason,
		},
		SchemaVersion: "1",
	})
}

// ReasonIndicatesRateLimit reports whether a deterministic PR-stage punt reason
// carries a GitHub rate-limit signal. pr-merge sets the canonical
// pmstages.ReasonRateLimited (matched exactly by the caller); pr-create surfaces
// it inside a create/push-failed reason wrapping the underlying client error, so
// we substring-match the same signals internal/github keys on. Issue #3976.
// Exported so the `pr-stage` CLI verb (#300) computes the rate_limited flag with
// the exact same rule the scheduler uses to decide defer-vs-LLM-fallthrough.
func ReasonIndicatesRateLimit(reason string) bool {
	r := strings.ToLower(reason)
	return strings.Contains(r, "rate-limited") ||
		strings.Contains(r, "rate limit") ||
		strings.Contains(r, "secondary rate") ||
		strings.Contains(r, "abuse detection") ||
		strings.Contains(r, "too many requests") ||
		strings.Contains(r, "429")
}

// NewDefaultPRCreateRunner builds a production pr-create runner wired with the
// given GitHub client. When `client` is nil (test scheduler path) the runner is
// constructed without GitHub clients, so every Run punts and the LLM path runs
// as today. Exported so the `pr-stage` CLI verb (#300) can construct the SAME
// deterministic runner the scheduler uses — the TS HeadlessOrchestrator invokes
// that verb for its deterministic-first pr-create path instead of maintaining a
// second, divergent create implementation.
func NewDefaultPRCreateRunner(client *gh.Client) pmstages.PRCreateRunner {
	r := pmstages.NewDeterministicPRCreateRunner()
	if client != nil {
		r = r.WithPRCreateClient(&schedulerPRCreateAdapter{client: client, prSvc: gh.NewPRService(client)}).
			WithGitClient(pmstages.NewExecGitClient())
	}
	return r
}

// schedulerPRCreateAdapter bridges internal/github.PRService into the
// stages.prCreateClient interface so the deterministic runner can call
// CreatePR / ListOpenPRsForBranch / GetRepoID without importing
// internal/github (which would create a cycle through pkg/types).
type schedulerPRCreateAdapter struct {
	client *gh.Client
	prSvc  *gh.PRService
}

func (a *schedulerPRCreateAdapter) GetRepoID(ctx context.Context, owner, repo string) (string, error) {
	return a.client.GetRepositoryID(ctx, owner, repo)
}

func (a *schedulerPRCreateAdapter) CreatePR(ctx context.Context, repoID, title, body, head, base string) (*pmstages.CreatedPR, error) {
	pr, err := a.prSvc.CreatePR(ctx, repoID, title, body, head, base)
	if err != nil {
		return nil, err
	}
	return &pmstages.CreatedPR{Number: pr.Number, URL: pr.URL, NodeID: pr.NodeID}, nil
}

func (a *schedulerPRCreateAdapter) ListOpenPRsForBranch(ctx context.Context, owner, repo, head string) ([]pmstages.CreatedPR, error) {
	prs, err := a.prSvc.ListPRs(ctx, owner, repo, "OPEN", head)
	if err != nil {
		return nil, err
	}
	out := make([]pmstages.CreatedPR, 0, len(prs))
	for _, pr := range prs {
		out = append(out, pmstages.CreatedPR{Number: pr.Number, URL: pr.URL, NodeID: pr.NodeID})
	}
	return out, nil
}
