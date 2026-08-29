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
	"slices"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/nightgauge/nightgauge/internal/attention"
	"github.com/nightgauge/nightgauge/internal/ci"
	"github.com/nightgauge/nightgauge/internal/config"
	"github.com/nightgauge/nightgauge/internal/deliverable"
	"github.com/nightgauge/nightgauge/internal/diagnostics"
	"github.com/nightgauge/nightgauge/internal/dockercompose"
	"github.com/nightgauge/nightgauge/internal/execution"
	"github.com/nightgauge/nightgauge/internal/execution/adapters"
	"github.com/nightgauge/nightgauge/internal/execution/codexprovision"
	stagecontext "github.com/nightgauge/nightgauge/internal/execution/context"
	"github.com/nightgauge/nightgauge/internal/forge"
	"github.com/nightgauge/nightgauge/internal/git"
	gh "github.com/nightgauge/nightgauge/internal/github"
	"github.com/nightgauge/nightgauge/internal/hooks"
	changeClassifier "github.com/nightgauge/nightgauge/internal/intelligence/changeClassifier"
	"github.com/nightgauge/nightgauge/internal/intelligence/complexity"
	"github.com/nightgauge/nightgauge/internal/intelligence/learning"
	"github.com/nightgauge/nightgauge/internal/intelligence/routing"
	"github.com/nightgauge/nightgauge/internal/intelligence/survival"
	"github.com/nightgauge/nightgauge/internal/intelligence/tokens"
	"github.com/nightgauge/nightgauge/internal/models"
	"github.com/nightgauge/nightgauge/internal/orchestrator/gates"
	"github.com/nightgauge/nightgauge/internal/orchestrator/recovery"
	pmstages "github.com/nightgauge/nightgauge/internal/orchestrator/stages"
	"github.com/nightgauge/nightgauge/internal/platform"
	"github.com/nightgauge/nightgauge/internal/reclaim"
	"github.com/nightgauge/nightgauge/internal/runstate"
	"github.com/nightgauge/nightgauge/internal/skillrender"
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
	Stage       state.PipelineStage
	IssueNumber int
	Repo        string
	Model       string
	// Effort/Thinking complete the dispatch envelope next to Model (#581,
	// spike #568 §4.1: "the wire grows effort and thinking alongside model").
	// Resolved by resolveWireEffort/resolveWireThinking on the IPC path,
	// where the scheduler is the only resolver (#340) and the extension
	// executes the wire effort verbatim. On the Go-direct adapter path
	// Thinking stays env/adapter-owned (dispatch_envelope.go) and Effort is
	// resolved only for xai adapters (#606) — RunOptions threads it to the
	// grok CLI, with NIGHTGAUGE_GROK_EFFORT demoted to operator override.
	Effort   string
	Thinking string
	// NOTE (#611): there is deliberately no Adapter field here. On the
	// Go-direct path the runner IS execMgr's adapter, so the value would be a
	// restatement; on the IPC path Go holds no adapter and the extension owns
	// per-stage selection (auto-router + stage-start fallback walk), so any
	// value Go could put here would be a guess at someone else's decision.
	// The IPC consumer keys off the adapter's own first-hand report instead —
	// see DowngradeProviderForServedModel.
	MaxTokens    int
	Timeout      time.Duration
	SkillPath    string
	ContextFile  string
	OutputFile   string
	TargetRepo   string
	WorktreePath string // Absolute path for Claude CLI working directory (IPC mode only)
	// Runtime is NON-OPTIONAL for production dispatches (ADR-017 step 0b).
	// runPipeline constructs the runtime before it can reach a stage, so a nil
	// Runtime here is a programming error, not a supported configuration —
	// only tests construct it nil.
	Runtime *state.RuntimeState
	// RunID is the run identity this stage is dispatched under, populated from
	// Runtime.RunID at the single production construction site. Non-optional
	// for pipeline dispatches: IpcStageRunner.RunStage refuses to emit without
	// it rather than sending an empty id the server's identity-bearing verbs
	// would then hard-reject, silently, one swallowed `.catch` at a time
	// (ADR-017 Decision 10).
	RunID             string
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
	// ServedEffort/ServedThinking are the envelope analogues of ServedModel
	// (#606, mirroring the #91 flow): what the executor's last-mile
	// translation ACTUALLY dispatched — the codex reasoning vocabulary value,
	// the grok effort after normalization into EFFORT_LEVELS, the disable
	// interlock's "off" — never a copy of the requested wire envelope. Empty
	// means honestly-unreported (adapter with no effort axis, no first-hand
	// evidence). Attribution only, same rule as ServedModel: never feeds
	// routing, sticky substitutions, or retries.
	ServedEffort   string
	ServedThinking string
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
	// KillCeiling is the stable name of the LIMIT that terminated the stage
	// and KillCeilingValue is its resolved value plus derivation (#161).
	KillCeiling      string
	KillCeilingValue string
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
	// RecentBash is the tail of the stage's Bash history (oldest first, at
	// most 10), each entry with its own exit code. Superset of
	// LastBashCommand/LastBashExit — its last element is that same command
	// (#156).
	RecentBash []diagnostics.RecentBashEntry
	// StopHookErrored is true when the stream included a stop-hook-error.
	StopHookErrored bool
	// StderrTail is the last 4 KB of stderr from the SkillRunner ring buffer.
	StderrTail string
	// ToolCalls is the stage's bounded all-tools call log, forwarded
	// verbatim from TS via pipeline.stageResult. Superset of RecentBash —
	// covers every tool, not just Bash. (Issue #144)
	ToolCalls []diagnostics.ToolCallRecord

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

// Bounds on the failure reason a CLI stage's stderr may contribute to
// StageRunResult.ErrorText — the shape the TS SkillRunner's inferProcessError
// already produces for the IPC path (last few non-empty stderr lines).
//
// ErrorText is CURATED BY CONTRACT (internal/ipc/pipeline_messages.go): it is
// the human-readable failure REASON, and it is the string ClassifyTerminalKind
// reads. The classifier is an ordered, first-match-wins, case-insensitive
// SUBSTRING ladder; its corpus is single-line reasons of a couple of hundred
// characters. Feeding it a buffer instead of a reason is how a `go test` line
// containing "hard cap" becomes a stall_kill and a lint line quoting
// "cost-cap-exceeded" becomes budget_exceeded. LastOutputLines is the raw
// evidence field and is deliberately never classified.
const (
	stderrReasonMaxLines = 3
	stderrReasonMaxBytes = 2 * 1024
)

// stderrFailureReason extracts the reason a CLI adapter printed on its OWN
// stderr: the last stderrReasonMaxLines non-empty lines, hard-capped at
// stderrReasonMaxBytes. Returns "" when stderr carried nothing.
//
// STDOUT IS DELIBERATELY NOT A SOURCE. For a CLI adapter, stdout is the whole
// streaming-JSON transcript — every assistant turn and every `tool_result`
// (bash output, file contents, web fetches). That is model- and tool-authored
// prose, and routing it into the substring classifier misroutes ordinary
// crashes onto uncapped recovery branches (#533 review). A CLI stage whose
// stderr is silent therefore yields an EMPTY ErrorText and keeps its pre-#533
// subagent_crash routing, which is the honest answer: we do not know why it
// died. The motivating failure needs nothing more — grok writes
// `Error: Couldn't set model …: "unknown model id"` to stderr.
//
// The result is cloned: strings.Join returns its single element unchanged, and
// that element is a slice of the caller's stderr buffer, which would otherwise
// be pinned for the lifetime of the run.
func stderrFailureReason(stderr string) string {
	if len(stderr) > stderrReasonMaxBytes {
		stderr = stderr[len(stderr)-stderrReasonMaxBytes:]
		// Drop the partial first line the byte cut left behind, unless that
		// would empty the reason (one line longer than the whole cap).
		if i := strings.IndexByte(stderr, '\n'); i >= 0 && i+1 < len(stderr) {
			stderr = stderr[i+1:]
		}
	}
	lines := strings.Split(stderr, "\n")
	picked := make([]string, 0, stderrReasonMaxLines)
	for i := len(lines) - 1; i >= 0 && len(picked) < stderrReasonMaxLines; i-- {
		if t := strings.TrimSpace(lines[i]); t != "" {
			picked = append(picked, t)
		}
	}
	if len(picked) == 0 {
		return ""
	}
	slices.Reverse(picked)
	return strings.Clone(strings.Join(picked, "\n"))
}

// cliFailureText derives the two failure-evidence fields from a CLI stage's
// captured output (#533).
//
// The two fields have DIFFERENT contracts and therefore different sources:
//
//	ErrorText       — curated reason, read by ClassifyTerminalKind. Stderr only,
//	                  bounded by stderrFailureReason. Empty when stderr is silent.
//	LastOutputLines — raw forensic evidence, never classified. The combined
//	                  stdout+stderr tail, bounded by the same caps
//	                  RecordStageOutputTail applies (state.TruncateOutputTail).
//
// Each source is bounded before the join so combining never materializes a copy
// of a multi-megabyte buffer, and the join is bounded again.
func cliFailureText(stdout, stderr string) (errorText, lastOutputLines string) {
	combined := state.TruncateOutputTail(stdout)
	if errTail := state.TruncateOutputTail(stderr); errTail != "" {
		if combined != "" && !strings.HasSuffix(combined, "\n") {
			combined += "\n"
		}
		combined += errTail
	}
	return stderrFailureReason(stderr), state.TruncateOutputTail(combined)
}

// stageFailureText is the single text every terminal-failure classification
// input derives from.
//
// err.Error() WINS whenever the executor returned an error, so IPC mode and
// CLI mode's non-exit failures behave exactly as before. Only when err is nil
// — the CLI-mode non-zero-exit shape, where execution.Manager reports the exit
// on the result and the reason on the result's stderr — does the runner's
// carried ErrorText supply the text. Returning "" for both leaves the caller
// on its pre-#533 behavior, which is what a runner with no evidence should do.
func stageFailureText(err error, result *StageRunResult) string {
	if err != nil {
		return err.Error()
	}
	if result != nil {
		return result.ErrorText
	}
	return ""
}

// terminalFailureReason renders the reason persisted on the runtime snapshot,
// which recordV2History re-classifies into the V3 record's
// terminal_failure_kind. Extracted so the classification consequences of the
// #533 carry are testable at the same seam the scheduler uses.
//
// `%v` of a nil error renders the literal "<nil>", which is what every
// CLI-mode failure recorded before #533 — so when there is no Go error but the
// runner carried the process's own reason, print that instead. The "exit N: "
// prefix stays either way: retro tooling and the terminal-kind corpus already
// match against it, and it is what keeps an unrecognized failure classifying as
// subagent_crash rather than falling out of the ladder entirely.
func terminalFailureReason(exitCode int, err error, failText string) string {
	if err == nil && failText != "" {
		return fmt.Sprintf("exit %d: %s", exitCode, failText)
	}
	return fmt.Sprintf("exit %d: %v", exitCode, err)
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
		Effort:       params.Effort,
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
		out := &StageRunResult{}
		if result != nil {
			out.ExitCode = result.ExitCode
			// Carry the evidence even on the error return: err.Error() still
			// wins for classification, but LastOutputLines is what lands on
			// the V3 record's StageDetail for retros regardless of err.
			out.ErrorText, out.LastOutputLines = cliFailureText(result.Stdout, result.Stderr)
		}
		return out, err
	}

	// CLI mode's terminal shape (#533): execution.Manager turns a non-zero exit
	// into result.ExitCode and returns a NIL error, with the reason on
	// result.Stderr. Every classification input in runPipeline used to derive
	// only from that nil err, so ClassifyTerminalKind was handed the literal
	// string "exit 1: <nil>" and answered subagent_crash (#520) for EVERY
	// CLI-mode failure of every adapter — which then routed to an upward model
	// escalation instead of, for a rejected model, the #42 sticky downgrade.
	//
	// Gated on a non-zero exit because that is the ONLY failure predicate CLI
	// mode has: adapters.RunResult carries no Success field (the IPC result
	// does, and IpcStageRunner reads it). Without the gate a successful stage's
	// stderr — deprecation notices, progress chatter — would be published as a
	// failure reason for a run that never failed.
	errorText, lastOutputLines := "", ""
	if result.ExitCode != 0 {
		errorText, lastOutputLines = cliFailureText(result.Stdout, result.Stderr)
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
		ErrorText:               errorText,
		LastOutputLines:         lastOutputLines,
	}, nil
}

// issueGetter abstracts issue operations used by the scheduler for testability.
type issueGetter interface {
	GetIssue(ctx context.Context, owner, repo string, number int) (*types.Issue, error)
	GetIssuesByNumbers(ctx context.Context, owner, repo string, numbers []int) (map[int]*types.Issue, error)
	GetEpicProgress(ctx context.Context, epicNodeID string) (*types.EpicProgress, error)
	GetEpicProgressByNumber(ctx context.Context, owner, repo string, number int) (*types.EpicProgress, error)
	CloseIssue(ctx context.Context, issueID string) error
	RemoveBlockedBy(ctx context.Context, blocked, blocker forge.IssueRef) error
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
	// filesystem root. A run's on-disk state — trace, runtime-{issue}-{runId}.json,
	// stage-context, exit-records, worktrees — must all root at the run's
	// TARGET repo in a multi-repo workspace, not the scheduler's single launch
	// root, or the state is split across two repos (#229; mirrors the IPC
	// server's pipelineStateDir fix for #215/#218). The IPC server wires this
	// from its ClientResolver.RepoPath; CLI/auto mode wires an equivalent built
	// from the workspace repo registry (#882). Nil, or a "" answer for a repo
	// that is not the launch repo, makes resolveRunRoot REFUSE the run rather
	// than root it at the launch repo.
	repoPathResolver func(repo string) string

	// repoRootsResolver, when set, enumerates every registered repo filesystem
	// root the scheduler can dispatch to. Crash recovery reads the
	// current-run.json sidecar, but since #229 the sidecar is written at the
	// run's TARGET repo root (via runRoot) — so a cross-repo run that crashes
	// leaves its sidecar outside the launch root. Scanning only the launch root
	// would miss it, leaving the run orphaned with no synthesized terminal
	// record. Mirrors the IPC server's pipelineStateScanRoots fix (#218); the
	// IPC server wires this from ClientResolver.RegisteredPaths, and CLI/auto
	// mode wires an equivalent from the workspace repo registry (#882). Nil
	// leaves only the launch root scanned (#239).
	repoRootsResolver func() []string

	// launchRepo is the "owner/repo" slug of the repo the scheduler was
	// launched in, resolved from SchedulerConfig.RuntimeConfig at construction.
	// It is the ONLY repo whose runs may legitimately root at the launch root
	// when no repoPathResolver is wired — see resolveRunRoot, which refuses a
	// run for any other repo rather than writing that run's worktree, branch
	// and pipeline state into a real-but-wrong repository (#882). "" when the
	// launch root carries no config that names a repo.
	launchRepo     string
	launchRepoOnce sync.Once

	// composeLister and composeTeardown are seams over the docker compose CLI so
	// reconcileOrphanedComposeProjects' DECISION is testable without a docker
	// daemon. The decision is the whole risk: teardown runs `down -v
	// --remove-orphans`, so a test asserting only "no error" would pass against
	// the #296 bug, which returned success while destroying a live run's
	// volumes. nil → the real dockercompose package.
	composeLister   func(ctx context.Context) ([]dockercompose.Project, error)
	composeTeardown func(ctx context.Context, name string, opts dockercompose.TeardownOptions) (dockercompose.TeardownResult, error)

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

	// recordOutcomes gates the pipeline learning system's corpus write. The
	// Recorder itself is built per run against the run's TARGET repo root, not
	// held here: pinning one Recorder at construction rooted it at the daemon's
	// LAUNCH root, so in a multi-repo workspace a run's outcome landed in a
	// different repo than the run record it was derived from (#215/#232 say
	// both belong in the target repo) — and the extension path, which does root
	// per run, would write to a second file for the same run (#304).
	recordOutcomes bool

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

	// trustedAuthorAssociations mirrors SchedulerConfig.TrustedAuthorAssociations
	// (#270) — used by PickNext's defense-in-depth author-trust filter.
	trustedAuthorAssociations []string

	// adapterExplicit mirrors SchedulerConfig.AdapterExplicit (#54).
	adapterExplicit string
	// runDefaultAdapter is the adapter the run started with — stages without
	// a stage_adapters entry revert to it after a per-stage switch (#54).
	runDefaultAdapter adapters.SkillRunner

	// Callbacks
	onStageStart    func(repo string, issue int, stage string, title string)
	onStageComplete func(repo string, issue int, stage string, err error, inputTokens, outputTokens, cacheReadTokens int, costUsd float64, model string)
	onEpicComplete  func(repo string, epicNumber int)
	// evaluatePostMergeFn performs the post-merge evaluation. A field, not a
	// direct call, for the same reason buildGraphFn is one: checkEpicCompletion
	// otherwise constructs its own GitHub services from a live client, so the
	// only branch that matters here — result.AutoClosed == true — is
	// unreachable in a test. Without this seam, deleting the epic.go call site
	// breaks no test, which is the unpinned-wiring shape #991 exists to fix.
	// Defaulted in NewScheduler.
	evaluatePostMergeFn func(ctx context.Context, issueSvc hooks.IssueFetcher, issueCloser hooks.IssueCloser,
		epicSvc hooks.EpicAutoCloser, prVerifier hooks.PRVerifier, boardSvc hooks.BoardSyncer,
		input hooks.PostMergeInput) hooks.PostMergeResult

	// epicCheckpoint latches the autonomous scheduler's between-epic safety
	// pause when an epic auto-closes.
	//
	// A DEDICATED field rather than a second registration on onEpicComplete,
	// which is a single slot that internal/ipc/server.go re-assigns inside the
	// `pipeline.run` method closure — per REQUEST, not at startup. Chaining
	// onto it would be silently wiped by the next pipeline.run, or born nil,
	// depending on ordering; both failures are invisible (#991). Mirrors the
	// SetAttention shape: one writer, nil-safe on both ends. nil in CLI mode.
	epicCheckpoint     func(epicNumber int)
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
	// pipeline, keyed by RUN ID (ADR-017 follow-up R-5, #379). Used by IPC
	// mode (IpcStageRunner) so the IPC server can update PhaseHistory on the
	// scheduler's runtime when TypeScript reports phase markers via
	// pipeline.notifyPhaseTransition. Without this, IPC-mode runs have an
	// empty PhaseHistory in every pipeline.stateChanged snapshot, which means
	// the tree view loses phase counts ("17/17 phases") on already-completed
	// stages whenever the extension reloads mid-pipeline.
	//
	// It was keyed by issue number until #379, with an identity guard at each
	// write site checking rt.RunID against the caller's. ADR-017 identifies a
	// compensating check of exactly that shape as the signature of a wrong
	// key: the guard existed because the key could not distinguish two runs of
	// one issue. Keying on the identity removes the guard rather than
	// duplicating it, and makes LookupRunByID a map hit instead of a scan.
	activeRuntimes   map[string]*state.RuntimeState
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
	// TrustedAuthorAssociations overrides the default trusted set
	// (OWNER/MEMBER/COLLABORATOR) used by PickNext's author-trust
	// defense-in-depth filter (#270). Empty → use the built-in default.
	TrustedAuthorAssociations []string
	// RuntimeConfig is the loaded config.Config for this repo, used at
	// startup to cross-check a multi-repo workspace manifest's
	// project_number against the runtime-resolved board (#271). Nil skips
	// the check (e.g. single-repo invocations with no loaded config).
	RuntimeConfig *config.Config
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
		// Default to the real evaluator; tests override to drive the
		// AutoClosed branch without a live GitHub client (#991).
		evaluatePostMergeFn:       hooks.EvaluatePostMerge,
		client:                    client,
		boardSvc:                  gh.NewBoardService(client, cfg.Owner, cfg.ProjectNumber, cfg.OwnerType),
		issueSvc:                  gh.NewIssueService(client),
		epicSvc:                   gh.NewEpicService(client),
		execMgr:                   execMgr,
		stateSvc:                  state.NewBoardStateServiceForClient(client, cfg.Owner, cfg.ProjectNumber, cfg.OwnerType),
		owner:                     cfg.Owner,
		projectNumber:             cfg.ProjectNumber,
		workspaceRoot:             cfg.WorkspaceRoot,
		stageRunner:               &ExecutionManagerRunner{execMgr: execMgr},
		retryEngine:               NewRetryEngine(retryConfigForWorkspace(cfg.WorkspaceRoot)),
		budgetEngine:              NewBudgetEnforcer(DefaultBudgetConfig()),
		ralphEngine:               NewRalphLoopController(DefaultRalphConfig()),
		stageGates:                defaultStageGatesFromEnv(),
		maxPerRepo:                maxPerRepo,
		repoConcurrencyOverrides:  cfg.RepoConcurrencyOverrides,
		repoRunning:               make(map[string]int),
		budgetRetries:             make(map[string]int),
		mergeLocks:                make(map[string]*sync.Mutex),
		recordOutcomes:            true,
		onFailureStatus:           onFailureStatus,
		excludeLabels:             excludeLabels,
		trustedAuthorAssociations: cfg.TrustedAuthorAssociations,
		adapterExplicit:           cfg.AdapterExplicit,
		runDefaultAdapter:         cfg.Adapter,
		prMergeRunner:             pmstages.NewDeterministicRunner(),
		prCreateRunner:            NewDefaultPRCreateRunner(client),
		launchRepo:                launchRepoSlug(cfg.RuntimeConfig),
	}
	// Wire FailureRecovery registry (Issue #3268). Reuses the same runners
	// as the deterministic-first hooks so a recovery and a deterministic
	// merge use a single source of truth.
	s.recoveryRegistry = recovery.Default(cfg.WorkspaceRoot, s.prMergeRunner, s.prCreateRunner)
	s.loadQueue()
	warnProjectMappingMismatch(cfg.RuntimeConfig, cfg.WorkspaceRoot)
	return s
}

// warnProjectMappingMismatch logs a loud but non-fatal warning when a
// multi-repo workspace manifest's project_number disagrees with the
// runtime-resolved board (#271). Issues land on the board the manifest
// names, but PickNext only ever polls the runtime-resolved board — this
// startup check surfaces the split immediately instead of letting it
// silently strand Ready items. Never blocks scheduler construction; the
// loud, blocking failure mode is `nightgauge doctor`'s job.
func warnProjectMappingMismatch(runtimeCfg *config.Config, workspaceRoot string) {
	if runtimeCfg == nil {
		return
	}
	mismatches, err := config.CheckWorkspaceProjectMapping(runtimeCfg, workspaceRoot)
	if err != nil {
		return // no workspace manifest — nothing to cross-check
	}
	for _, m := range mismatches {
		log.Printf("WARN scheduler: %s", m)
	}
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

// descentProviderForDispatch names the provider a stage's next dispatch will
// EXECUTE on, from the strongest evidence each path actually has (#611). It is
// the one input to both provider-scoped descent decisions — the sticky-effort
// rung the wire carries, and the ladder an API rejection walks — so the write
// key and the read key cannot come from different notions of "who is running
// this".
//
// activeAdapter is Go's own adapter and wins whenever it is set: on the
// Go-direct path applyStageAdapter has already re-pointed execMgr for this
// stage, so it IS the executing adapter — first-hand, and known before the
// stage runs.
//
// On the IPC path Go holds no adapter (SchedulerConfig.Adapter is nil there by
// construction) and MUST NOT infer one. The extension's resolveStageAdapter is
// not config.ResolveStageAdapter with different spelling: it adds a
// ConfigBridge-sourced global rung, the AutoProviderRouter and a hardcoded
// default, omits the NIGHTGAUGE_ADAPTER rung Go has, and then lets
// walkAdapterFallback replace the decision outright at stage start when prereq
// validation fails. A Go-side re-derivation is wrong in both directions —
// blind to an auto-router-selected grok, and confidently wrong (an xai rung on
// a claude dispatch) whenever the walker hops. So the evidence used instead is
// what the ADAPTER ITSELF reported for this stage's previous attempt: the
// concrete id its process was spawned with, after every one of those decisions
// (StageResultParams.ServedModel → RecordStageServedModel).
//
// "" when there is no such evidence — a stage's first IPC dispatch, or an
// adapter whose served id the registry does not know. Every consumer reads ""
// as the historical anthropic inference, so an unknown provider simply gets no
// provider-scoped rung. That is the safe direction: the failure mode of
// guessing is exactly the cross-provider bleed this keying exists to stop.
func descentProviderForDispatch(rt *state.RuntimeState, stage state.PipelineStage, activeAdapter string) string {
	if activeAdapter != "" {
		return DowngradeProviderForAdapter(activeAdapter)
	}
	if rt == nil {
		return ""
	}
	return DowngradeProviderForServedModel(rt.StageServedModel(stage))
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
// repo's filesystem root so a run's trace, runtime-{issue}-{runId}.json, stage-context,
// exit-records, and worktrees all root at the run's TARGET repo — not the
// scheduler's launch root — in a multi-repo workspace (#229). The resolver is
// also forwarded to the execution manager so worktree resolution stays
// consistent with run state. The IPC server wires this from its
// ClientResolver.RepoPath; CLI/auto mode wires an equivalent built from the
// workspace repo registry (Scheduler.WithWorkspaceRepoRegistry, #882).
//
// A wired resolver that returns "" for a repo is a REFUSAL, not a fallback:
// resolveRunRoot fails the run closed rather than rooting it at the launch
// repo. Leaving the resolver nil is only safe for a scheduler that will never
// be handed a repo other than the one it was launched in.
func (s *Scheduler) WithRepoPathResolver(fn func(repo string) string) {
	s.repoPathResolver = fn
	if s.execMgr != nil {
		s.execMgr.SetRepoPathResolver(fn)
	}
}

// WithWorkspaceRepoRegistry builds the repo→path registry a CLI-mode scheduler
// needs and wires it into BOTH resolvers (#882). It is the CLI's equivalent of
// the IPC server's ClientResolver wiring, discovered from the launch root
// instead of handed over at daemon start.
//
// Leaving this unwired is not a neutral omission. Without it resolveRunRoot has
// no registry to consult, so `nightgauge queue add <N> --repo <other/repo>` —
// the documented way to queue cross-repo work — ran entirely inside the LAUNCH
// repo: an empty worktree under launch/.nightgauge/worktrees/, history and
// trace under launch/.nightgauge/pipeline/, and an epic branch cut from the
// LAUNCH repo's default branch and pushed to the LAUNCH repo's remote. The
// target repo received nothing.
//
// A repo the discovery cannot see is NOT silently rooted at the launch repo:
// resolveRunRoot refuses the run instead.
func (s *Scheduler) WithWorkspaceRepoRegistry(launchRoot string) {
	if launchRoot == "" {
		return
	}
	paths := config.WorkspaceRepoPaths(launchRoot)
	if len(paths) == 0 {
		return
	}
	s.WithRepoPathResolver(func(repo string) string { return paths[repo] })

	roots := make([]string, 0, len(paths))
	seen := make(map[string]bool, len(paths))
	for _, root := range paths {
		if seen[root] {
			continue
		}
		seen[root] = true
		roots = append(roots, root)
	}
	s.WithRepoRootsResolver(func() []string { return roots })
}

// WithRepoRootsResolver injects an enumerator of every registered repo
// filesystem root so orchestrator-crash recovery can scan each repo's
// current-run.json sidecar, not just the launch root's. Because #229 roots a
// run's sidecar at its TARGET repo (via runRoot), a cross-repo run that crashes
// leaves its sidecar under a non-launch repo root; scanning only the launch
// root would never reconcile it. The IPC server wires this from its
// ClientResolver.RegisteredPaths; CLI/auto mode wires an equivalent from the
// workspace repo registry (Scheduler.WithWorkspaceRepoRegistry, #882).
// Nil leaves only the launch root scanned (#239).
func (s *Scheduler) WithRepoRootsResolver(fn func() []string) {
	s.repoRootsResolver = fn
}

// launchRepoSlug returns the "owner/repo" the launch root's own config names,
// or "" when no config was loaded or it does not name both halves.
func launchRepoSlug(cfg *config.Config) string {
	if cfg == nil || cfg.Owner == "" || cfg.DefaultRepo == "" {
		return ""
	}
	return cfg.Owner + "/" + cfg.DefaultRepo
}

// repoSlugsMatch reports whether two repo identifiers name the same repository.
//
// Tolerant of a missing owner on EITHER side, because they are not written by
// the same producer: a board item's Repo is normally "owner/name", but the IPC
// pipeline.runItem verb accepts a bare repo name, and a config may name only
// half. Comparing strictly would read a bare "acme" as different from
// "owner/acme" and refuse a perfectly ordinary single-repo run — a false
// refusal is the one failure mode a fail-closed gate cannot afford, because it
// stops work that was never in danger.
func repoSlugsMatch(a, b string) bool {
	if a == "" || b == "" {
		return false
	}
	if strings.EqualFold(a, b) {
		return true
	}
	short := func(v string) string {
		if i := strings.LastIndex(v, "/"); i >= 0 {
			return v[i+1:]
		}
		return v
	}
	// Only fall back to short-name comparison when one side omitted its owner.
	// Two fully-qualified slugs with different owners are different repos.
	if strings.Contains(a, "/") && strings.Contains(b, "/") {
		return false
	}
	return strings.EqualFold(short(a), short(b))
}

// launchRepoIdentity returns the "owner/repo" the LAUNCH ROOT is, or "" when it
// cannot be determined. Config first (the authoritative declaration), then the
// launch root's `origin` remote, which is what makes the identity knowable for
// a checkout whose config names only a project board.
//
// Cached: a run resolves this once per call site and the answer cannot change
// for the life of the process.
func (s *Scheduler) launchRepoIdentity() string {
	s.launchRepoOnce.Do(func() {
		if s.launchRepo != "" {
			return
		}
		s.launchRepo = originRepoSlug(s.execMgr.WorkspaceRoot())
	})
	return s.launchRepo
}

// originRepoSlug reads "owner/repo" from the `origin` remote of the git repo at
// root, or returns "" when the remote is absent or is not a FORGE url.
//
// A local-path remote (`/tmp/fixtures/origin`, `../mirror.git`) yields "" on
// purpose. Its trailing two path segments look exactly like an owner/repo pair
// — a fixture pushing to `.../001/origin` reads as the repo "001/origin" — and
// a WRONG launch identity is worse than none: it turns the fail-closed gate
// into a refusal machine for runs that were never in danger.
func originRepoSlug(root string) string {
	if root == "" {
		return ""
	}
	out, err := exec.Command("git", "-C", root, "config", "--get", "remote.origin.url").Output()
	if err != nil {
		return ""
	}
	url := strings.TrimSuffix(strings.TrimSpace(string(out)), ".git")

	var path string
	switch {
	case strings.HasPrefix(url, "https://"), strings.HasPrefix(url, "http://"), strings.HasPrefix(url, "ssh://"):
		rest := url[strings.Index(url, "://")+3:]
		i := strings.Index(rest, "/")
		if i < 0 {
			return ""
		}
		path = rest[i+1:]
	case !strings.HasPrefix(url, "/") && !strings.HasPrefix(url, ".") &&
		strings.Contains(url, "@") && strings.Contains(url, ":"):
		// scp-style: user@host:owner/repo
		path = url[strings.Index(url, ":")+1:]
	default:
		return "" // local path, file://, or unrecognized — not a forge identity
	}

	parts := strings.Split(strings.Trim(path, "/"), "/")
	if len(parts) < 2 {
		return ""
	}
	owner, name := parts[len(parts)-2], parts[len(parts)-1]
	if owner == "" || name == "" {
		return ""
	}
	return owner + "/" + name
}

// runRoot resolves the filesystem root a run's on-disk state belongs in — the
// run's target repo in a multi-repo workspace so trace, runtime-{issue}-{runId}.json,
// stage-context, and exit-records never split across repos (mirrors
// Server.pipelineStateDir, #215). Returns "" when the target repo's root cannot
// be resolved safely; callers that cannot refuse (best-effort bookkeeping) must
// treat "" as "no root" rather than substituting the launch root (#882).
func (s *Scheduler) runRoot(repo string) string {
	root, err := s.resolveRunRoot(repo)
	if err != nil {
		return ""
	}
	return root
}

// resolveRunRoot resolves the filesystem root for a run targeting repo, and
// FAILS CLOSED whenever it can see that the launch root is not that repo's.
//
// WHY REFUSING BEATS FALLING BACK (#882). The previous behavior returned the
// execution manager's workspace root whenever the resolver was nil or the repo
// was unregistered. That is not a harmless default: in CLI/auto mode — where
// the resolver was always nil — a run queued with `queue add <N> --repo
// <other/repo>` created its worktree, wrote its history and trace, and created
// AND PUSHED its epic branch inside the LAUNCH repo. The launch repo is a real
// repository with a real remote; publishing another repo's work there is worse
// than not running at all.
//
// The arms, in order:
//
//  1. The item names no repo: nothing to mismatch, launch root.
//  2. A wired resolver answered: that is the target repo's root, authoritative.
//  3. The launch root's own identity is KNOWN (its config names owner +
//     defaultRepo, or its `origin` remote does) and it IS the target repo: the
//     launch root is that repo's root by definition. The single-repo case,
//     unchanged — including when the registry simply has no entry for it.
//  4. The launch root's identity is KNOWN and it is NOT the target repo:
//     REFUSE, naming the repo that could not be resolved. This is the whole
//     point of the gate, and it holds whether or not a resolver is wired.
//  5. The launch root's identity is UNKNOWN: there is no evidence of a
//     mismatch to act on — no config names the launch repo and it has no
//     origin remote — so the launch root stands, with a warning. Refusing here
//     would strand every run in a workspace that merely declines to name
//     itself, which is a worse failure than the one being prevented: it stops
//     work that was never in danger.
func (s *Scheduler) resolveRunRoot(repo string) (string, error) {
	launchRoot := s.execMgr.WorkspaceRoot()
	if repo == "" {
		return launchRoot, nil // arm 1
	}
	if s.repoPathResolver != nil {
		if root := s.repoPathResolver(repo); root != "" {
			return root, nil // arm 2
		}
	}
	launch := s.launchRepoIdentity()
	if launch == "" {
		log.Printf("WARN: cannot identify the repository at the launch root %q (no config names it and it has no origin remote) — "+
			"rooting the run for %q there unverified (#882)", launchRoot, repo)
		return launchRoot, nil // arm 5
	}
	if repoSlugsMatch(repo, launch) {
		return launchRoot, nil // arm 3
	}
	return "", fmt.Errorf( // arm 4
		"cannot resolve a filesystem root for repo %q: this workspace's registry has no path for it and the launch root is %q. "+
			"Refusing to run rather than rooting this run's worktree, pipeline state and branches at the launch repo, "+
			"which would write — and push — another repository's work into a real repository that is not its own. "+
			"Add the repo to the workspace (a checkout carrying .nightgauge/config.yaml beside the launch repo, or a "+
			"repositories[] entry in .vscode/nightgauge-workspace.yaml) and re-queue the issue",
		repo, launch)
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

// registerRuntime stores the live RuntimeState for an active pipeline under
// its own run identity. Used in IPC mode so the IPC server can record phase
// transitions onto the scheduler's runtime via RecordPhaseStart /
// RecordPhaseComplete.
//
// A runtime with no identity is not registered. Registering one under the
// empty key would make the map's key meaningless for that entry and let the
// next identity-less run overwrite it — the collision the re-key exists to
// remove, reintroduced through the back door.
func (s *Scheduler) registerRuntime(rt *state.RuntimeState) {
	if rt == nil || rt.RunID == "" {
		return
	}
	s.activeRuntimesMu.Lock()
	defer s.activeRuntimesMu.Unlock()
	if s.activeRuntimes == nil {
		s.activeRuntimes = make(map[string]*state.RuntimeState)
	}
	s.activeRuntimes[rt.RunID] = rt
}

// unregisterRuntime removes one run. Called via defer at the end of
// runPipeline so a completed run can be GC'd.
//
// Keyed by identity, this deletes exactly the run that finished. Keyed by
// issue number it deleted whatever was registered for that issue, which on a
// re-run was not necessarily the same run.
func (s *Scheduler) unregisterRuntime(runID string) {
	if runID == "" {
		return
	}
	s.activeRuntimesMu.Lock()
	defer s.activeRuntimesMu.Unlock()
	delete(s.activeRuntimes, runID)
}

// LookupRunByID returns the scheduler-owned runtime carrying runID, or nil.
//
// Since #379 the registry is keyed by identity, so this is a map hit rather
// than the scan it was under the issue-number key.
//
// It is the step-3 arm of ADR-017 Decision 11's resolution order: a
// run-progress OR administrative call carrying a live scheduler run's id is
// SERVED from this runtime and never adopted into the IPC registry, and a
// terminal call carrying one is still refused `run_wrong_owner` — the
// scheduler books its own terminal record. Takes activeRuntimesMu and must
// never be called while the IPC server holds its own runtimesMu — the two
// registry locks are never held together (Decision 5's lock-discipline table).
func (s *Scheduler) LookupRunByID(runID string) *state.RuntimeState {
	if runID == "" {
		return nil
	}
	s.activeRuntimesMu.Lock()
	defer s.activeRuntimesMu.Unlock()
	return s.activeRuntimes[runID]
}

// IsRunLive reports whether runID names a run this scheduler is currently
// executing. Feeds the reconciler's skip predicate (ADR-017 7.2) — a live
// scheduler run's snapshot must never be reconciled as an orphan.
func (s *Scheduler) IsRunLive(runID string) bool {
	return s.LookupRunByID(runID) != nil
}

// RecordPhaseStartForRun records a phase:start transition on the scheduler's
// runtime for runID.
//
// Before #379 this looked the runtime up by ISSUE NUMBER and then compared
// rt.RunID against the caller's, logging and refusing on a mismatch. That
// comparison was a compensating check for a key that could not tell two runs
// of one issue apart — the shape ADR-017 itself names as the signature of a
// wrong key. With the registry keyed on identity the lookup either finds the
// named run or finds nothing, so there is no second run to mis-address and
// nothing left to compensate for. The guard is deleted, not moved.
//
// Safe no-op when no runtime is registered (the HeadlessOrchestrator path keys
// its runtimes in the IPC server's own registry instead), and equally a no-op
// for an id this scheduler does not own — which is the guard's old job, now
// done by the lookup itself.
func (s *Scheduler) RecordPhaseStartForRun(runID string, _ int, stage, name string, index, total int) {
	rt := s.LookupRunByID(runID)
	if rt == nil {
		return
	}
	rt.BeginPhase(state.PipelineStage(stage), name, index, total)
}

// RecordPhaseCompleteForRun is RecordPhaseStartForRun's complete arm. It
// resolves by identity for the same reason.
func (s *Scheduler) RecordPhaseCompleteForRun(runID string, _ int, stage, name string) {
	rt := s.LookupRunByID(runID)
	if rt == nil {
		return
	}
	rt.CompletePhase(state.PipelineStage(stage), name)
}

// RunIDForIssue returns the identity of the scheduler-owned run for an issue,
// or "" when the scheduler is not running that issue. It is how the IPC
// server's scheduler-sourced emitters stamp a real `runId` on their envelopes
// (ADR-017 Decision 6) instead of fabricating one.
//
// A derived scan rather than a second map, following the IPC registry's own
// "THERE IS NO SECOND MAP" rule: the registry holds at most the concurrency
// limit's worth of entries, and a derived index cannot drift from its source.
//
// Issue number is not an identity, so this can only answer for a single live
// run per issue. When more than one is somehow live it returns the empty
// string rather than picking one, because an arbitrary choice here would be
// stamped onto an event envelope as though it were resolved — exactly the
// confidently-wrong answer the re-key exists to stop.
func (s *Scheduler) RunIDForIssue(issueNumber int) string {
	if issueNumber <= 0 {
		return ""
	}
	s.activeRuntimesMu.Lock()
	defer s.activeRuntimesMu.Unlock()
	found := ""
	for runID, rt := range s.activeRuntimes {
		if rt == nil || rt.IssueNumber != issueNumber {
			continue
		}
		if found != "" {
			log.Printf("scheduler: issue #%d has more than one live run (%q, %q) — refusing to guess which one an envelope means (ADR-017 R-5)",
				issueNumber, found, runID)
			return ""
		}
		found = runID
	}
	return found
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
		// Board-driven autonomous pickup does not dispatch PR-shaped items,
		// INCLUDING Dependabot remediation PRs. #345 re-keyed the fast-track
		// ROUTE below onto the pull request (isDependabotRemediationPR) but
		// deliberately did NOT widen this filter, and the reason is not
		// squeamishness — widening it alone ships a guaranteed-failing loop.
		//
		// Three things are missing, none of them in this file, and the first is
		// fatal on its own:
		//
		//  1. THE PIPELINE'S FIRST STAGE RESOLVES AN ISSUE BY NUMBER. issue-pickup
		//     runs `nightgauge git branch-create --issue <N>`, which calls
		//     IssueService.GetIssue(owner, repo, N) to derive the branch prefix
		//     and slug (cmd/nightgauge/main.go, `branch-create`). GraphQL's
		//     `repository.issue(number:)` returns null for a pull request number,
		//     so the call errors, the skill exits 1, and the stage fails. Nothing
		//     in the pipeline knows how to check out a PR's existing head branch
		//     instead of creating one — that is skills/nightgauge-issue-pickup
		//     work, not scheduler work.
		//
		//  2. NO TRUST ANSWER EXISTS FOR A PR. The author-trust gate immediately
		//     below refuses any item whose author is not affirmatively trusted,
		//     and internal/github/board.go's PullRequest branch populates no
		//     AuthorAssociation and no author login at all (types.BoardItem has
		//     no field for one). A bot-authored remediation PR is therefore
		//     indistinguishable from a stranger's pull request here, and
		//     Dependabot's labels are not a substitute — any account that can
		//     label can forge them.
		//
		//  3. THE EXTENSION CANNOT HAND OVER A PR-SHAPED ITEM EITHER. The
		//     `pipeline.runItem` IPC builds its types.BoardItem from
		//     {owner, repo, issueNumber, title, id}, dropping IsPR and Labels.
		//
		// The authoritative alert→PR join needed for (2) already exists —
		// forge.SecurityService.ListOpenAlerts returns Remediation.PRNumber
		// (#343) — so the trust question has a real answer available; it just
		// cannot be asked usefully until (1) exists. Until then this filter stays
		// closed, because a scheduler that picks up a PR it cannot drive books a
		// failure every scan instead of doing nothing.
		if item.IsPR {
			continue
		}

		// Author-trust gate, defense-in-depth (#270): even if an item reached
		// Ready by some other route (board auto-add, a mis-set status, a
		// future code path), the final dispatch gate must still refuse an
		// untrusted author. Deliberately redundant with the refinement and
		// isTriagedAndUnblocked gates upstream.
		if !isTrustedAuthor(item.AuthorAssociation, s.trustedAuthorAssociations) {
			log.Printf("#%d: skipping — untrusted author (author_association=%q)", item.Number, item.AuthorAssociation)
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
//
// The returned QueueRunSummary accounts for EVERY entry the pass consumed, and
// the error is non-nil only when the pass itself could not proceed (a cancelled
// context). A per-issue terminal failure does not abort the batch — that part
// is deliberate — but it is recorded in the summary so the caller can report it
// and, in `queue run`'s case, exit non-zero (#875). Before this, the failure was
// swallowed with no return value carrying it: the whole visible output of a run
// where nothing succeeded was "Processing 1 queued issues..." and exit 0.
//
// The summary is a RETURN VALUE rather than a log line on purpose. RunQueue has
// three callers on two very different surfaces — the CLI, the IPC server's
// pipeline.run verb, and the autonomous scheduler's per-candidate spawn — and
// only the CLI has a stdout an operator is reading. Printing here would put the
// summary on a daemon's log for the other two and still leave the CLI with no
// way to derive an exit status from it.
func (s *Scheduler) RunQueue(ctx context.Context) (QueueRunSummary, error) {
	s.mu.Lock()
	queue := make([]QueueItem, len(s.queue))
	copy(queue, s.queue)
	s.queue = nil
	s.persistQueue()
	s.emitQueueChangedUnlocked()
	s.mu.Unlock()

	summary := QueueRunSummary{}
	record := func(entry QueueItem, kind QueueOutcomeKind, terminalKind, detail string) {
		summary.Outcomes = append(summary.Outcomes, QueueOutcome{
			Repo:         entry.Repo,
			IssueNumber:  entry.IssueNumber,
			Kind:         kind,
			TerminalKind: terminalKind,
			Detail:       detail,
		})
	}

	for _, entry := range queue {
		select {
		case <-ctx.Done():
			return summary, ctx.Err()
		default:
		}

		log.Printf("processing queue: #%d (%s)", entry.IssueNumber, entry.Repo)

		// One board ITEM, not the whole board (#908). This used to page the
		// entire project at first:100 — 17 points per page, 34 for this repo's
		// board — and then discard 99% of it to find one row, once per queue
		// entry. GetItem asks the same question with a server-side
		// `owner/repo#N` filter in a single unpaginated page.
		//
		// The read stays INSIDE the loop on purpose: runPipeline executes a
		// whole pipeline between iterations, so board state genuinely moves and
		// hoisting it out would serve stale statuses. The waste was never the
		// per-entry read, it was reading a whole board to answer a one-row
		// question.
		// The old scan matched on issue NUMBER ALONE, ignoring the repo
		// entirely — on a shared board two repos with the same issue number
		// collided and the first row won. GetItem filters on owner/repo#N, so
		// the identity has to be resolved properly rather than guessed.
		entryRepo := entry.Repo
		if entryRepo == "" {
			// An entry that names no repo at all is only dispatchable against
			// the launch repo, which is what resolveRunRoot would root it at.
			entryRepo = s.launchRepo
		}
		owner, repoName := splitOwnerRepo(entryRepo)
		if owner == "" {
			// A bare slug carries no owner; the scheduler's configured one is
			// the only other answer available, and it is the owner the board
			// itself is bound to.
			owner = s.owner
		}
		item, err := s.boardSvc.GetItem(ctx, owner, repoName, entry.IssueNumber)
		if err != nil {
			// "Not on the board" and "I could not look" must never collapse
			// into each other. Absence makes the caller skip the entry, which
			// is the destructive answer — a transient forge failure reported as
			// absence silently drops queued work.
			if errors.Is(err, forge.ErrNotFound) {
				log.Printf("queue: issue #%d not found on board", entry.IssueNumber)
				record(entry, QueueOutcomeNotDispatched, "", "not found on project board")
				continue
			}
			log.Printf("queue: failed to fetch board: %v", err)
			record(entry, QueueOutcomeNotDispatched, "", fmt.Sprintf("board fetch failed: %v", err))
			continue
		}
		if item == nil {
			log.Printf("queue: issue #%d not found on board", entry.IssueNumber)
			record(entry, QueueOutcomeNotDispatched, "", "not found on project board")
			continue
		}

		// Check blocking relationships before dispatching (mirrors PickNext behavior)
		blocked, err := s.isBlocked(ctx, *item)
		if err != nil {
			log.Printf("queue: failed to check blocking for #%d: %v", entry.IssueNumber, err)
			record(entry, QueueOutcomeNotDispatched, "", fmt.Sprintf("blocking check failed: %v", err))
			continue
		}
		if blocked {
			log.Printf("queue: skipping #%d — has open blockers", entry.IssueNumber)
			record(entry, QueueOutcomeBlocked, "", "has open blockers")
			continue
		}

		ok, terminalKind := s.runPipeline(ctx, *item)
		if ok {
			record(entry, QueueOutcomeCompleted, "", "")
		} else {
			record(entry, QueueOutcomeFailed, terminalKind, "")
		}
	}

	return summary, nil
}

// QueueAdd adds issues to the execution queue.
// Accepts QueueEntry (legacy) or QueueItem; QueueEntry is promoted to QueueItem internally.
// Duplicate repository+issue identities are silently skipped.
func (s *Scheduler) QueueAdd(entries ...QueueEntry) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, e := range entries {
		if s.queueContainsUnlocked(e.Repo, e.IssueNumber) {
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
// Duplicate repository+issue identities are silently skipped.
func (s *Scheduler) QueueAddItem(items ...QueueItem) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for i := range items {
		if s.queueContainsUnlocked(items[i].Repo, items[i].IssueNumber) {
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

// queueContainsUnlocked returns true if the queue already contains the exact
// repository+issue identity. Issue numbers are only unique within a repository.
func (s *Scheduler) queueContainsUnlocked(repo string, issueNumber int) bool {
	for _, existing := range s.queue {
		if existing.Repo == repo && existing.IssueNumber == issueNumber {
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
func (s *Scheduler) queueItemRemoteRunID(repo string, issueNumber int) string {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, item := range s.queue {
		if item.Repo == repo && item.IssueNumber == issueNumber {
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

		// Processing guard (Issue #232): items already dispatched are kept in
		// s.queue (see below — they are no longer spliced out) so their
		// in-flight state is visible to queueStatusLocked() and cloud sync.
		// Skip them here so a later DequeueIndependent call doesn't
		// re-dispatch a run that is already executing.
		if item.Status == "processing" {
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

		item.Status = "processing"
		dequeued = append(dequeued, item)
		dequeuedNums[item.IssueNumber] = true
		repoInFlight[item.Repo]++
		toRemoveIdx = append(toRemoveIdx, i)
	}

	// Mark dequeued items as "processing" in place instead of splicing them
	// out (Issue #232). This keeps in-flight items visible in s.queue so
	// queueStatusLocked() and cloud sync can report "processing" instead of
	// "idle" while a fleet is running. completeQueueItemLocked removes them
	// once the pipeline reaches a terminal state.
	for _, idx := range toRemoveIdx {
		s.queue[idx].Status = "processing"
	}

	if len(dequeued) > 0 {
		s.recalculatePositions()
		s.persistQueue()
		s.emitQueueChangedUnlocked()
	}

	return dequeued
}

// completeQueueItemLocked removes the matching "processing" queue item
// (repo + issue number) once its pipeline run reaches a terminal state
// (Issue #232). No-op when no such item exists — runs dispatched via the
// autonomous board-scan path (RunPipelineForItem -> dispatchItem ->
// runPipeline) never touch s.queue, so this only matters for items that came
// through DequeueIndependent. Caller must hold s.mu.
func (s *Scheduler) completeQueueItemLocked(repo string, issueNumber int) {
	for i, item := range s.queue {
		if item.Status != "processing" || item.IssueNumber != issueNumber || item.Repo != repo {
			continue
		}
		s.queue = append(s.queue[:i], s.queue[i+1:]...)
		s.recalculatePositions()
		s.persistQueue()
		s.emitQueueChangedUnlocked()
		return
	}
}

// CompleteQueueItem is the exported, lock-acquiring wrapper around
// completeQueueItemLocked. Called from runPipeline's terminal defer on every
// exit path (success, failure, cancellation) so a dequeued item never lingers
// in the queue as "processing" after its run ends. (Issue #232)
func (s *Scheduler) CompleteQueueItem(repo string, issueNumber int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.completeQueueItemLocked(repo, issueNumber)
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
		if s.queueContainsUnlocked(subIssueRepo, si.Number) {
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
	// CONSTRUCTION REMOVES NO WORKTREE, CONTAINER, VOLUME OR BRANCH — and it
	// never touches a LIVE run's state (#403, then #410 for the half #403
	// missed). loadQueue runs from NewScheduler, and getQueueScheduler builds a
	// Scheduler for `queue add|list|run|remove|clear` and the
	// deps-gate/baseline-gate promote commands, so anything destructive here runs
	// as a side effect of being constructed, on behalf of a process that can see
	// no other process's in-flight runs.
	//
	// #403 removed the merged-worktree sweep from this line and left
	// reconcileOrphanedComposeProjects sitting right beside it, which made
	// `queue list` — a printf loop — run `docker compose down -v
	// --remove-orphans` plus image removal against every `issue-NNN` stack whose
	// worktree it could not see. Its protection was the same
	// activeWorktreeIssues scan the worktree sweep had, and had exactly the same
	// blind spot: a live run whose worktree is registered in a root this process
	// does not scan reads as orphaned, and `down -v` destroys named volumes
	// nothing recovers.
	//
	// Both reconciles now have ONE production caller each, the autonomous
	// reconcile cycle, because that is the only process holding an authoritative
	// in-flight set (as.state.Running) for the runs whose state it is about to
	// destroy. See runMergedWorktreeSweep and
	// (*AutonomousScheduler).sweepOrphanedComposeProjects.
	//
	// WHAT CONSTRUCTION STILL WRITES, named rather than implied by a banner:
	// recoverOrchestratorCrashAt synthesizes a terminal-failure RunRecord into
	// the daily JSONL, pauses and persists queue-state.json, and unlinks
	// `.nightgauge/pipeline/current-run.json`. That is bookkeeping about a run
	// whose process is GONE — the gate is runstate.ProcessAlive on the pid the
	// sidecar carries, so a live run is left entirely alone: no record, no pause,
	// no unlink. Writing a crash record for a dead orchestrator is not the same
	// act as deleting live state, and the sidecar is the TypeScript side's index
	// into a run, so removing one belonging to a live run would have been the
	// same class of construction side effect #403/#410 exist to end.
}

// reconcileOrphanedComposeProjects tears down per-issue docker compose stacks
// (`issue-NNN`) whose run is gone, so a crash that bypassed CleanupWorktree
// cannot leave containers, volumes, networks, or images squatting host ports
// across pipeline runs. Soft-fail: errors are logged and teardown continues for
// the remaining projects. See Issue #3050.
//
// inFlight and determined are PARAMETERS, not something this function resolves
// (#410). It used to ride NewScheduler → loadQueue and compute its own
// protection from s.activeWorktreeIssues(), which is a worktree scan of the
// roots THIS process knows about — so `queue list` ran `docker compose down -v`
// against a fleet it could not see. The caller must now state whose in-flight
// set this is; the one production caller is
// (*AutonomousScheduler).sweepOrphanedComposeProjects, which unions
// as.state.Running (authoritative for the runs it dispatched) with the
// active-worktree scan.
//
// determined=false means the caller could not READ one of its in-flight
// sources. Every project would then be torn down with `down -v
// --remove-orphans` on the strength of a set nobody obtained. Doing nothing
// leaves stale containers squatting ports, which a later cycle fixes; guessing
// destroys a live run's named volumes, which nothing recovers.
//
// ctx is the CALLER's context, not a fresh Background (#410). The pass now runs
// inside the autonomous cycle, so an orchestrator shutdown must cancel it rather
// than wait on docker; the 60s budget is derived from the cycle context and
// remains a soft cap on the whole fan-out.
func (s *Scheduler) reconcileOrphanedComposeProjects(ctx context.Context, inFlight map[int]bool, determined bool) {
	ctx, cancel := context.WithTimeout(ctx, 60*time.Second)
	defer cancel()

	lister := s.composeLister
	if lister == nil {
		lister = dockercompose.ListIssueProjects
	}
	teardown := s.composeTeardown
	if teardown == nil {
		teardown = dockercompose.TeardownProject
	}

	projects, err := lister(ctx)
	if err != nil {
		log.Printf("compose-reconcile: list compose projects failed: %v", err)
		return
	}
	if len(projects) == 0 {
		return
	}
	if !determined {
		log.Printf("compose-reconcile: WARN the in-flight set is undetermined (the caller could not read one of its sources) — skipping teardown of %d compose project(s) rather than risk destroying a live run's volumes", len(projects))
		return
	}
	for _, p := range projects {
		if inFlight[p.IssueNumber] {
			continue
		}
		// "no run in flight", not "no matching worktree" (#410): the protecting
		// set is now a union, and a worktree is only half of it. A log naming a
		// check that is no longer the one performed is the same defect as the
		// sweep's "content already on <base>" line for a door that compared
		// nothing.
		log.Printf("compose-reconcile: tearing down orphaned compose project %s (no run in flight for #%d)",
			p.Name, p.IssueNumber)
		if _, err := teardown(ctx, p.Name, dockercompose.TeardownOptions{
			RemoveImages: true,
		}); err != nil {
			log.Printf("compose-reconcile: teardown of %s failed: %v", p.Name, err)
		}
	}
}

// activeWorktreeIssuesFor returns the issue numbers held by an active worktree
// across a CALLER-SUPPLIED root set, plus whether that answer is DETERMINED. The
// scan itself lives in execution.ActiveWorktreeIssues — the one implementation
// shared with `doctor` and `cleanup` (#323); the scheduler's only distinct
// contribution is knowing which roots to scan.
//
// determined=false means "I could not find out", which is not the same as "no
// worktrees exist", and the difference decides whether a destructive caller may
// act. See execution.ActiveWorktreeIssues for why (#296).
//
// The roots are a PARAMETER and there is deliberately no self-resolving variant:
// a destructive caller resolves repoScanRoots() ONCE and passes the same slice
// here and to the pass it protects, so the `determined` bit describes exactly the
// roots that are then acted on. Resolving twice is not equivalent — the resolver
// is a live callback over workspace registration, so a repo added or dropped
// between the two calls yields a verdict about a root set that was never swept.
func (s *Scheduler) activeWorktreeIssuesFor(roots []string) (map[int]bool, bool) {
	return execution.ActiveWorktreeIssues(roots)
}

// repoScanRoots returns every filesystem root a workspace-wide reconcile must
// inspect: the scheduler's launch root plus every repo registered with the
// roots resolver. Since #229 a run's on-disk state (current-run.json sidecar,
// worktrees) is rooted at its TARGET repo, so a cross-repo run leaves state
// outside the launch root — scanning only the launch root would miss it
// (mirrors the IPC pipelineStateScanRoots fix, #218). Deduplicated: the primary
// repo is typically both the launch root and a registered path, and each run's
// state lives under exactly one root, so there is no duplicate reconciliation.
// A nil resolver returns only the launch root (#239); CLI/auto mode wires one
// from the workspace repo registry (#882).
func (s *Scheduler) repoScanRoots() []string {
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
	for _, root := range s.repoScanRoots() {
		s.recoverOrchestratorCrashAt(root, now)
	}
}

// recoverOrchestratorCrashAt reconciles a single repo root's sidecar. The
// synthesized crash record is written to the SAME root the sidecar lives under
// — the run's target repo in a multi-repo workspace — so the daily JSONL stays
// with the rest of that run's on-disk state (#229), matching where a normal run
// records history via runRoot.
//
// Guards, in order:
//
//  1. LIVENESS. A sidecar is not evidence of a crash — it is evidence of a RUN,
//     and it is present for the entire life of a healthy one. The recorded pid is
//     the orchestrator's own (writeCurrentRunSidecar stamps os.Getpid() at stage
//     start), so `runstate.ProcessAlive` distinguishes the two cases with the
//     tree's one liveness probe (#341). Without this check, constructing a
//     Scheduler in a second terminal — `nightgauge queue list`, a printf loop —
//     destroyed the live run's sidecar (the TypeScript side's INDEX into that
//     run), invented a terminal-failure record for a run that was still
//     executing, and paused the queue. That is the construction side effect
//     #403/#410 exist to end, one noun outside the banner's list (#410).
//  2. Clock sanity: only synthesizes when the sidecar's StartedAt is in the past
//     (defense against clock skew or stale workspace moves).
func (s *Scheduler) recoverOrchestratorCrashAt(root string, now time.Time) {
	sc, err := readCurrentRunSidecar(root)
	if err != nil {
		log.Printf("recovery: failed to read current-run sidecar at %s: %v", root, err)
		return
	}
	if sc == nil {
		return
	}
	if runstate.ProcessAlive(sc.PID) {
		log.Printf("recovery: current-run sidecar at %s names LIVE pid %d (#%d, stage=%s, run_id=%s) — that run is still executing; no crash record, no queue pause, no sidecar removal",
			root, sc.PID, sc.IssueNumber, sc.Stage, sc.RunID)
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
	// pipeline.logs.history_retention_days drives the prune pass
	// appendAndIndex runs on every write below (#674).
	if cfg, cfgErr := config.Load(root); cfgErr == nil && cfg != nil {
		hw.SetRetentionDays(cfg.Pipeline.ResolveHistoryRetentionDays())
	}
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
	// Precedence (Issue #232): processing > paused > waiting > idle. A
	// dispatched item stays in s.queue with Status == "processing" instead
	// of being spliced out, so its presence must outrank "paused"/"waiting"
	// — otherwise a running fleet still reads as idle/waiting on disk, in
	// the VSCode tree, and via the cloud sync mirror.
	for _, item := range s.queue {
		if item.Status == "processing" {
			return "processing"
		}
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

// recalculatePositions renumbers queue items 1..N over the items actually
// waiting. "processing" items are skipped (Issue #232) so an in-flight item
// doesn't occupy a position slot among the waiting items; its
// Position is left at whatever it last held.
func (s *Scheduler) recalculatePositions() {
	pos := 1
	for i := range s.queue {
		if s.queue[i].Status == "processing" {
			continue
		}
		s.queue[i].Position = pos
		pos++
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
//
// SINGLE SLOT: a second call replaces the first. internal/ipc/server.go
// registers one per `pipeline.run` request, so anything that needs to observe
// epic completion durably must NOT register here — see SetEpicCheckpointFn for
// the shape that survives (#991).
func (s *Scheduler) OnEpicComplete(fn func(repo string, epicNumber int)) {
	s.onEpicComplete = fn
}

// SetEpicCheckpointFn injects the autonomous scheduler's epic-checkpoint
// recorder, so the fleet-scoped SafetyRails pause fires from the one place an
// epic actually closes.
//
// Nil-receiver guard is required: NewAutonomousScheduler is called with a nil
// *Scheduler throughout the test suite.
func (s *Scheduler) SetEpicCheckpointFn(fn func(epicNumber int)) {
	if s == nil {
		return
	}
	s.epicCheckpoint = fn
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
	if stage == state.StageFeaturePlanning {
		spliceACReconcile(workspaceRoot, issueNumber, outputFile)
	}
	return nil
}

// spliceACReconcile overwrites planning-{N}.json's `ac_reconcile` field with the
// verbatim contents of ac-reconcile-{N}.json (#1011).
//
// This is the Go-path twin of ContextAssembler.spliceACReconcile. Both exist
// because the two dispatch paths share no post-stage step: the extension
// validates the planning context against a Zod schema and can splice there, and
// this path has no schema in the loop at all — validateStageOutput otherwise
// only stats the file and never opens it. Fixing one and not the other would
// leave `nightgauge run <issue>` producing exactly the truncated handoff the
// extension no longer produces, which is the dual-path drift class this
// repository keeps finding.
//
// Best-effort by design: a missing or malformed report leaves the field alone
// rather than failing a stage that succeeded. The stage is instructed not to
// write the field, so "left alone" means the skeleton's null.
func spliceACReconcile(workspaceRoot string, issueNumber int, planningFile string) {
	reportPath := stagecontext.ContextPath(workspaceRoot, issueNumber, "ac-reconcile")
	reportRaw, err := os.ReadFile(reportPath)
	if err != nil {
		return
	}
	var report map[string]any
	if err := json.Unmarshal(reportRaw, &report); err != nil {
		log.Printf("#%d: ac_reconcile report is not valid JSON, leaving planning context alone: %v", issueNumber, err)
		return
	}

	planningRaw, err := os.ReadFile(planningFile)
	if err != nil {
		return
	}
	var planning map[string]any
	if err := json.Unmarshal(planningRaw, &planning); err != nil {
		// The extension path reports this through schema validation; here there
		// is no validator, so say it rather than swallowing it.
		log.Printf("#%d: planning context is not valid JSON, cannot splice ac_reconcile: %v", issueNumber, err)
		return
	}

	planning["ac_reconcile"] = report
	merged, err := json.MarshalIndent(planning, "", "  ")
	if err != nil {
		return
	}
	if err := os.WriteFile(planningFile, merged, 0o644); err != nil {
		log.Printf("#%d: could not persist the spliced planning context: %v", issueNumber, err)
		return
	}
	if acs, ok := report["acceptance_criteria"].([]any); ok {
		log.Printf("#%d: spliced ac_reconcile (%d criteria, aggregate=%v)", issueNumber, len(acs), report["aggregate_status"])
	}
}

// hasUncommittedWork returns true when the worktree has staged, unstaged, or
// untracked DELIVERABLE files — indicating work that was done but never
// committed. Uses a git subprocess (not go-git) for reliability in worktree
// subdirectories, consistent with the recovery-action shell-out pattern.
// Issue #3542.
//
// Bookkeeping directories are excluded (#202). Every run writes
// `.nightgauge/pipeline/*.json` and most write `.nightgauge/attention/*.json`;
// this repo gitignores the former but not the latter, and a consumer repo may
// ignore neither. Counting them made the pipeline's own exhaust answer "was
// work lost?" — which has two costs, both silent. The recovery commit swept
// pipeline state into the user's branch via `git add -A`, and, worse, ANY
// failure with an unset terminal kind got reclassified as
// worktree_uncommitted: a kind that means "recovered, not a failure" and so
// skips the LifetimeIssueFailures increment and the board revert. A real
// defect laundered into a non-event by a JSON file the pipeline wrote itself.
// #332 widened the scope from "deliverable paths only" to "everything that is
// not the pipeline's own UNTRACKED exhaust". #202's mechanism is untouched by
// that: the state files it named — the run's own dev-{N}.json, attention cards,
// containment records — are written fresh by each run and are never committed,
// so they stay untracked and stay excluded. What the widening admits is a
// change to a bookkeeping file someone TRACKED, which is a decision rather than
// exhaust, and which the pre-#332 scope made invisible to the rescue that
// exists to preserve it.
func hasUncommittedWork(worktreePath string) bool {
	if worktreePath == "" {
		return false
	}
	out, err := exec.Command("git", "-C", worktreePath, "status", "--porcelain", "--untracked-files=all").Output()
	if err != nil {
		return false
	}
	for _, path := range reclaim.ClassifyStatus(string(out)).Blocking {
		if path == "AGENTS.md" && onlyManagedAgentsChange(worktreePath) {
			continue
		}
		return true
	}
	return false
}

// untrackedExhaust lists the pipeline's own untracked bookkeeping in a
// worktree — the files a recovery commit must never publish. Returns nil when
// git cannot answer, and the caller then stages nothing extra: failing toward
// "nothing is exhaust" would publish it, so the read happens before any
// mutation and an unreadable tree is treated as having none to exclude only
// because `git add -A` will likewise have staged nothing readable.
func untrackedExhaust(worktreePath string) []string {
	out, err := exec.Command("git", "-C", worktreePath, "status", "--porcelain", "--untracked-files=all").Output()
	if err != nil {
		log.Printf("recovery: cannot read %s to separate exhaust from work: %v", worktreePath, err)
		return nil
	}
	return reclaim.ClassifyStatus(string(out)).Exhaust
}

// hasUnmergedIndex reports whether the worktree's index carries conflict stages
// — a merge or rebase stopped at a conflict. `ls-files -u -z` prints nothing at
// all when the index is clean, so an empty result is an unambiguous "no". A git
// error answers false: this guards a rescue, and a repo git cannot read has no
// rescue to guard.
func hasUnmergedIndex(worktreePath string) bool {
	out, err := exec.Command("git", "-C", worktreePath, "ls-files", "-u", "-z").Output()
	if err != nil {
		return false
	}
	return len(out) > 0
}

// stagedDeletionDominance reports how much of the CURRENTLY STAGED change is
// the deletion of tracked files, after git's own rename detection has run.
//
// Rename detection is the load-bearing part and the reason this reads the index
// rather than the pre-`add` porcelain. A filesystem rename appears before
// staging as `" D old"` plus `"?? new"` — indistinguishable, by column alone,
// from the mid-transformation tree this guard exists to catch. `git add -A`
// resolves it to a single `R`, so counting `D` entries here counts only
// deletions that are NOT half of a rename. A column-blind check on the
// pre-staging porcelain would fire on every refactor that moves a file.
//
// Returns (deletions, total entries, a sample path). A git error answers
// (0, 0, "") — this guards a rescue, and a tree git cannot read gets no guard
// rather than a fabricated verdict.
func stagedDeletionDominance(worktreePath string) (int, int, string) {
	out, err := exec.Command("git", "-C", worktreePath, "diff", "--cached", "--find-renames", "--name-status", "-z").Output()
	if err != nil {
		return 0, 0, ""
	}
	fields := strings.Split(string(out), "\x00")
	deletions, total, sample := 0, 0, ""
	for i := 0; i < len(fields); i++ {
		status := fields[i]
		if status == "" {
			continue
		}
		// -z emits status and path as separate NUL-terminated fields; a rename
		// or copy emits status, source, destination — consume the extra path so
		// a rename is never miscounted as two entries.
		paths := 1
		if status[0] == 'R' || status[0] == 'C' {
			paths = 2
		}
		var first string
		if i+1 < len(fields) {
			first = fields[i+1]
		}
		i += paths
		total++
		if status[0] == 'D' {
			deletions++
			if sample == "" {
				sample = first
			}
		}
	}
	return deletions, total, sample
}

func onlyManagedAgentsChange(worktreePath string) bool {
	working, err := os.ReadFile(filepath.Join(worktreePath, "AGENTS.md"))
	if err != nil {
		return false
	}
	committed, err := exec.Command("git", "-C", worktreePath, "show", "HEAD:AGENTS.md").Output()
	if err != nil {
		committed = nil // untracked file: only a pure managed block is ignorable
	}
	return codexprovision.IsOnlyManagedSteeringChange(string(committed), string(working))
}

// RecoverUncommittedWork stages all changes, creates a recovery commit, and
// pushes it to origin. Best-effort and non-fatal: a failed push logs a warning
// but the local recovery commit is still preserved on the worktree. Returns an
// error only when staging or committing fails (the work is then still on disk
// for manual recovery). Issue #3542.
//
// Exported for `nightgauge worktree recover` (#223). Until then this lived only
// on the Go scheduler's failure path, so a run driven by the extension's
// HeadlessOrchestrator — which is most of them — had no recovery at all. #221
// ended with a finished implementation sitting uncommitted in a worktree
// precisely because nothing on that path could reach this function.
func RecoverUncommittedWork(worktreePath string, issueNumber int, stage string) error {
	if worktreePath == "" {
		return fmt.Errorf("worktreePath is empty")
	}
	// An unmerged index is not uncommitted work and this rescue cannot handle it
	// (#301). `git status --porcelain` reports a conflicted path as `UU`, which
	// reclaim.ClassifyStatus correctly calls Blocking, so hasUncommittedWork says
	// "work to save" and lands here — but `git add -A` COLLAPSES the :2:/:3:
	// index stages (verified: `git show :2:<path>` afterwards is
	// "in the index, but not at stage 2"), and the commit then writes files full
	// of conflict markers onto whatever HEAD a rebase happens to have detached
	// to. The caller treats a nil return as "recovered", which reclassifies a
	// real failure as worktree_uncommitted — a kind that skips the
	// lifetime-failure increment and the board revert. Refuse instead: the caller
	// logs the reason and preserves the worktree.
	if hasUnmergedIndex(worktreePath) {
		return fmt.Errorf("worktree has an unmerged index (a merge or rebase is stopped at a conflict) — refusing to stage it, which would collapse the conflict stages and commit conflict markers")
	}
	// Read the tree BEFORE staging: `git add -A` collapses the distinction
	// this rescue turns on. A staged deletion and an untracked scaffold look
	// alike in the index, and only one of them is work.
	exhaust := untrackedExhaust(worktreePath)

	if err := exec.Command("git", "-C", worktreePath, "add", "-A").Run(); err != nil {
		return fmt.Errorf("git add: %w", err)
	}
	// Unstage the pipeline's own exhaust (#202). `git add -A` alone swept the
	// run's `.nightgauge/` state into the recovery commit and pushed it to the
	// issue branch, so a rescue meant to preserve the user's work also
	// published pipeline exhaust into their PR.
	//
	// Only the UNTRACKED bookkeeping is unstaged, never the whole
	// `.nightgauge`/`.claude` tree (#332). Resetting the tree wholesale
	// destroyed the very thing being rescued when a stage's deliverable WAS
	// bookkeeping: `.worktrees/issue-701` held 209 staged deletions under
	// `.nightgauge/pipeline/assessments/`, and `git reset -- .nightgauge`
	// restores every one of them from HEAD — the rescue erased the whole
	// deliverable and reported success. #237/#248 taught the dev gate that a
	// bookkeeping-only deliverable is real work; this path never learned it.
	//
	// Unstaged afterwards rather than excluded via `add`'s pathspec: naming a
	// gitignored path in an exclude pathspec makes `git add` exit 1 ("paths
	// are ignored by one of your .gitignore files"), which would turn this
	// rescue into a hard failure in every repo that DOES ignore `.nightgauge`
	// — the #3365 case this function exists for. `git reset` exits 0 whether
	// the path is ignored, untracked, or absent.
	if len(exhaust) > 0 {
		resetArgs := append([]string{"-C", worktreePath, "reset", "-q", "--"}, exhaust...)
		if err := exec.Command("git", resetArgs...).Run(); err != nil {
			log.Printf("#%d: unstaging pipeline exhaust before recovery commit failed (non-fatal): %v", issueNumber, err)
		}
	}
	// Refuse to publish a tree that is mostly the deletion of tracked files
	// (#1053). A stage that removes generated output intending to regenerate it,
	// and dies before regenerating, leaves exactly this shape: the sources still
	// declare the artifacts, the artifacts are gone, and the tree cannot build.
	// Observed on a real run — 89 generated files deleted against 10 edited, and
	// the surviving sources still carried `part 'router.g.dart';` for a file the
	// commit removed.
	//
	// This REFUSES rather than editing the commit's contents, deliberately, and
	// mirrors the unmerged-index guard above. Withholding the deletions instead
	// would commit a tree holding both halves of every rename and would repeat
	// the #332/#701 lesson in a new column: a stage whose deliverable IS
	// deletion would have that deliverable silently dropped and reported as
	// rescued. Refusing loses nothing — the files are still in HEAD, the
	// deletions are still on disk, and the caller preserves the worktree — while
	// keeping an incoherent tree off the branch and out of `origin`.
	//
	// Renames are already excluded: the count runs after `git add -A` has let
	// git resolve `D`+`??` pairs into a single `R`.
	//
	// A PURELY deletional change is explicitly allowed through. That is the
	// #332/#701 shape — a stage whose deliverable IS removal, such as the 209
	// staged deletions under `.nightgauge/pipeline/assessments/` that an earlier
	// category-blind guard destroyed — and `TestWorktreeRecover_RescuesBookkeeping
	// OnlyDeliverable` pins it. Deliberate removal arrives as removals and
	// nothing else; the mid-transformation tree is the one that deletes most of
	// itself AND still carries the edits that triggered the deletion. Requiring
	// a non-deletion entry is what separates the two, and it is the difference
	// between the observed #1053 tree (89 deletions alongside 10 edits) and a
	// legitimate deletion deliverable.
	if deletions, total, sample := stagedDeletionDominance(worktreePath); deletions > 0 && deletions < total && deletions*2 > total {
		// Restore the index so the worktree is left exactly as it was found.
		// The work stays on disk for the next attempt or for a human.
		if err := exec.Command("git", "-C", worktreePath, "reset", "-q").Run(); err != nil {
			log.Printf("#%d: restoring the index after refusing the recovery commit failed (non-fatal): %v", issueNumber, err)
		}
		return fmt.Errorf(
			"staged change is %d deletion(s) of tracked files out of %d entries (e.g. %q) — refusing to publish a tree that is most likely mid-transformation; the work is preserved in the worktree",
			deletions, total, sample)
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

// schedulerTerminalOutcome derives the terminal outcome string the Go
// scheduler latches into RuntimeState.TerminalOutcome (#440).
//
// IT IS DELIBERATELY THE SAME THREE STRINGS the extension path's outcomeFor
// closure produces at internal/ipc/server.go's pipeline.notifyComplete —
// "complete", "cancelled", "failed". The marker is read by the orphan
// reconciler and by every consumer of a sealed snapshot, none of which know
// which dispatch path produced the run; a fourth spelling here would be a
// path-dependent vocabulary in a field whose whole purpose is to be
// path-independent. That is the same one-corpus-one-meaning rule the outcome
// recorder states for `success` and `costUsd`.
//
// A DEFERRAL IS "cancelled", NOT "failed". blocked_dependency means no AI
// stage ran and nothing was attempted — the scheduler already excludes it from
// outcome recording for exactly this reason, and the extension path maps its
// Deferred flag to "cancelled" rather than to a failure.
func schedulerTerminalOutcome(success bool, terminalFailureKind string) string {
	switch {
	case success:
		return "complete"
	case terminalFailureKind == TerminalKindBlockedDependency:
		return "cancelled"
	default:
		return "failed"
	}
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

// refusePreDispatch books a stage the pipeline declines to DISPATCH — the
// prerequisite gate and the skill-render failure — and returns the terminal
// failure kind the caller must assign to terminalFailureKind before it
// returns. Issue #620.
//
// Three things have to happen together at such a site, and each is easy to get
// right in isolation while breaking one of the others, which is why they live
// in one function rather than open-coded twice.
//
//  1. BeginStage(stage) BEFORE SetStageError(stage, reason). Nothing is
//     dispatched, but StageErrors is keyed by stage and every reader that asks
//     "what went wrong" indexes it with the CURRENT stage
//     (snap.StageErrors[string(snap.Stage)]): IPC terminal-notify,
//     scheduler_exit_record.go's fallback, the outcome-recording site,
//     autonomous reporting. Without BeginStage the key and snap.Stage name
//     different stages and all of them read "" — the #444 invariant.
//
//  2. The #3542 uncommitted-work rescue runs HERE, not in the terminal defer.
//     A refusal does not imply a clean tree: the stage BEFORE the refused one
//     may have left a complete implementation uncommitted, because feature-dev
//     deliberately does not commit (AGENTS.md #1608) — it verifies and hands
//     off to feature-validate. Booking a non-empty kind is exactly the
//     condition the defer's rescue is gated on (`terminalFailureKind == ""`),
//     so a site that sets a kind and returns has SWITCHED THE RESCUE OFF for
//     that run. Left that way, a feature-validate SKILL.md that will not
//     compose strands the whole implementation in the worktree AND (because
//     validation_error is not in the defer's skipBoardRevert set) reverts the
//     board to Ready, so the re-dispatch regenerates it in a fresh worktree:
//     the composite loss shape the #3365 incident and the #3542 recovery exist
//     to prevent.
//
//  3. The refusal reason survives the rescue. The defer's copy REPLACES the
//     stage error with a bare "worktree_uncommitted: work auto-recovered after
//     <stage> failure", which would erase the one thing #620 adds — the
//     operator-actionable cause. Here the marker is PREFIXED instead. The
//     composed text still classifies worktree_uncommitted, because
//     internal/terminalkind/table.json orders the worktree-uncommitted rule
//     ahead of the validation-error rule, so the marker wins wherever the kind
//     is re-derived from prose (autonomous.go's onPipelineComplete wrapper,
//     NotifyComplete's defense-in-depth re-classify) — and it still names the
//     refusal that ended the run.
//
// The kind is validation_error when there was nothing to rescue, or when the
// rescue itself refused (an unmerged index) or failed: "missing prerequisite"
// is already a rule literal in the terminal-kind table whose corpus row names
// this exact producer, and its rationale — a pipeline-plumbing fault, not an
// implementation failure — applies verbatim to a skill that will not compose.
// It is set explicitly rather than left to the defer's fallback, which would
// stamp subagent_crash on a run where no subagent ever started.
//
// The stage-skip trace event carries the refusal reason verbatim, never the
// recovery-prefixed form: the trace answers "why was this stage skipped", and
// that answer must stay stable and greppable whether or not a rescue happened.
func (s *Scheduler) refusePreDispatch(
	item types.BoardItem,
	runtime *state.RuntimeState,
	workspaceRoot string,
	stage state.PipelineStage,
	tracer *trace.Writer,
	source string,
	reason string,
) (kind string, workRecovered bool) {
	log.Printf("#%d: %s", item.Number, reason)
	runtime.BeginStage(stage)
	runtime.SetStageError(stage, reason)

	kind = TerminalKindValidationError
	// loadWorktreePath, not stageWorkspace: the defer's rescue resolves the
	// tree this way, and the two must never disagree about which tree gets
	// rescued.
	if worktreePath := loadWorktreePath(workspaceRoot, item.Number); worktreePath != "" && hasUncommittedWork(worktreePath) {
		log.Printf("#%d: stage %s refused before dispatch with uncommitted work in the worktree — attempting recovery",
			item.Number, stage)
		if recErr := RecoverUncommittedWork(worktreePath, item.Number, string(stage)); recErr != nil {
			log.Printf("#%d: uncommitted work recovery failed: %v — worktree preserved at %s",
				item.Number, recErr, worktreePath)
		} else {
			// THE RESCUE IS NOT THE CAUSE (#875). Pre-fix this line read
			// `kind = TerminalKindWorktreeUncommitted`, and a run that could
			// never have proceeded — no SKILL.md to dispatch — was filed under
			// the hygiene condition the rescue happened to find. In the observed
			// run the "uncommitted work" was two pipeline-owned bookkeeping
			// files; it was not why the stage was refused and no operator acting
			// on it would have got anywhere. The kind is what
			// docs/OUTCOME_RECORDING.md and the retro path consume, so booking
			// the downstream condition there is corpus poisoning, not a
			// mislabel. The refusal reason came first and it stays the kind.
			log.Printf("#%d: uncommitted work recovered — retaining terminal_failure_kind=%s (first cause), work-recovered recorded as context",
				item.Number, kind)
			workRecovered = true
			// Reason FIRST, marker second. The marker is retained because it is
			// load-bearing in a way the kind is not: the autonomous path
			// re-derives recoverability by classifying THIS TEXT
			// (autonomous.go's onPipelineComplete wrapper, NotifyComplete's
			// defense-in-depth re-classify), and worktree_uncommitted is what
			// tells it the work survived — no LifetimeIssueFailures increment,
			// fixed backoff. The two answers are to different questions: the
			// kind answers "why did this run fail", the marker answers "did the
			// work survive", and after this fix each is recorded by whichever
			// mechanism actually knows.
			runtime.SetStageError(stage, fmt.Sprintf("%s — %s: work auto-recovered after %s failure",
				reason, TerminalKindWorktreeUncommitted, stage))
		}
	}

	s.emitStateChanged(item.Repo, item.Number, runtime)
	tracer.Emit(trace.KindStageSkip, string(stage), trace.StageSkipPayload{
		Source: source,
		Reason: reason,
	})
	return kind, workRecovered
}

// PipelineBudgetCeilingUSD resolves pipeline.token_budget_ceiling.ceiling_usd
// through the tier-merged config (machine → project → local via config.Load),
// mirroring the TypeScript-side getPipelineCeilingConfig resolution so the Go
// scheduler's budget-aware model escalation (Issue #3542) uses the same ceiling
// the TS ceiling enforcement does. The env override wins over all file tiers.
// Returns the maintainer-set default of $75 when the key is absent.
//
// EXPORTED (#305) because the ENFORCED ceiling must never cross a wire. The IPC
// `attention.raise` verb derives it here, in-process, exactly as the scheduler
// does — a raise that accepted the ceiling as a parameter would let any caller
// with socket access decide the number a `budget.raiseCeiling` option persists
// to the workspace on one operator click. Deriving it here also removes the
// second implementation: before #305 the extension resolved its own ceiling in
// TypeScript and sent the result back, so "the enforced ceiling" had two
// readers that could disagree with nothing failing.
func PipelineBudgetCeilingUSD(workspaceRoot string) float64 {
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

// newRunID is runstate.NewRunID behind a package var so the run-identity
// preflight's failure path is reachable from a test. NewRunID fails only on a
// clock fault or a CSPRNG read error, neither of which a test can provoke — and
// the whole point of that preflight is WHERE it fails from (below the terminal
// defer, so the run is still booked), which is exactly the kind of claim that
// rots silently if nothing exercises it. Production never reassigns this.
var newRunID = runstate.NewRunID

// runPipeline executes the full 6-stage pipeline for a board item.
//
// The loop integrates retry, budget, and RALPH engines:
// 1. Check budget ceiling before each stage
// 2. Execute stage via StageRunner (auto mode or IPC mode)
// 3. Record tokens with BudgetEnforcer
// 4. Evaluate model escalation signals (same-stage retry with better model)
// 5. Evaluate backtrack signals (rewind to earlier stage)
// 6. For feature-validate: run RALPH loop for self-healing
//
// PATH WARNING (#257): the VSCode extension NEVER enters this function.
// Extension-mode runs — the mode this product is primarily operated in — go
// queue.dequeueIndependent over IPC → ConcurrentPipelineManager.fillSlots →
// HeadlessOrchestrator, and their terminal bookkeeping runs in
// ConcurrentPipelineManager.runSlotPipeline's finally block plus the IPC
// pipeline.notifyComplete handler. A behavior added ONLY here is invisible in
// that mode with no error, no failed test, and no log line (#210, #254).
// Before adding a terminal-path behavior below, answer: which of the two
// paths reaches this, and is the other intentionally excluded? Then record it
// in internal/orchestrator/testdata/terminal_behaviors.json — the parity
// tests (terminal_parity_test.go and the TS twin) fail until you do.
// The return pair is the run's own terminal accounting: `success` mirrors the
// pipeline.complete callback's flag and `terminalKind` the terminal_failure_kind
// written to the run record. RunQueue needs both to build its end-of-run summary
// and its exit status (#875); every other caller may ignore them.
func (s *Scheduler) runPipeline(ctx context.Context, item types.BoardItem) (success bool, terminalKind string) {
	// Track repo concurrency
	s.mu.Lock()
	s.repoRunning[item.Repo]++
	s.mu.Unlock()
	defer func() {
		s.mu.Lock()
		s.repoRunning[item.Repo]--
		s.mu.Unlock()
	}()

	// Root this run's on-disk state (trace, runtime-{issue}-{runId}.json,
	// stage-context, exit-records, worktrees) at the run's TARGET repo, not the
	// scheduler's launch root, so multi-repo state is never split (#229).
	// Resolved BEFORE the runtime exists because the run_id resolution below
	// reads run-state.json underneath it, and RunID is now a constructor
	// argument (ADR-017 Decision 1 — immutable, no setter).
	//
	// FAILS CLOSED (#882). When the target repo's root cannot be resolved, the
	// error is carried to the repo-root preflight below the terminal defer —
	// same shape as runIDMintErr, and for the same reason: a bare return here
	// sits ABOVE the terminal defer, so it would leak the concurrency slot and
	// leave the board In Progress. The launch root is used ONLY as the
	// bookkeeping root for the refusal record itself (the daemon's own
	// workspace, where the operator looks); the run is refused before any
	// worktree, branch, push or stage dispatch can reach the wrong repository.
	workspaceRoot, runRootErr := s.resolveRunRoot(item.Repo)
	if runRootErr != nil {
		log.Printf("#%d: cannot resolve a run root for %s: %v — the run will be refused at preflight (#882)",
			item.Number, item.Repo, runRootErr)
		workspaceRoot = s.execMgr.WorkspaceRoot()
	}

	// Resolve run_id for telemetry correlation (#3557). Prefer the RemoteRunID
	// from the platform command payload (for remote-triggered runs); fall back
	// to run-state.json; then mint locally.
	//
	// This block RUNS BEFORE THE CONSTRUCTOR (ADR-017 step 1): the identity is a
	// constructor fact, so it cannot be stamped on afterwards.
	//
	// EVERY BORROWED VALUE IS VALIDATED BEFORE IT IS ACCEPTED (ADR-017
	// Decision 1). `remoteRunId` arrives on the local IPC socket, which ADR-015
	// documents as UNAUTHENTICATED, and run-state.json is a file on disk; under
	// step 1 the identity is interpolated into the snapshot FILENAME, so an
	// unvalidated value is an arbitrary-path write (`../`) and a merely
	// non-canonical one (a UUIDv4, a `run_01H…` ULID) writes a file the
	// discovery regex cannot match — invisible to orphan reconciliation, the
	// gate seam, getState, the wave orchestrator and the extension's gate map,
	// silently. A present-but-invalid value is therefore IGNORED and the run
	// mints its own identity LOUDLY, which is strictly better than a run whose
	// every snapshot is a phantom. The consequence is named: a platform id that
	// is not a canonical UUIDv7 loses platform correlation until ADR-017
	// Decision 2 threads `remoteRunId` as its own correlation attribute rather
	// than as the identity. (The state layer refuses the same predicate at the
	// persist sink, so this guard is the loud half, not the only half.)
	//
	// runIDMintErr carries a mint failure forward to the run-identity preflight
	// below the terminal defer (ADR-017 step 0b) — see below.
	var runID string
	var runIDMintErr error
	{
		remoteRunID := s.queueItemRemoteRunID(item.Repo, item.Number)
		if remoteRunID != "" {
			if runstate.IsIdentity(remoteRunID) {
				runID = remoteRunID
			} else {
				log.Printf("#%d: ignoring non-identity run id %q from remote — minting locally (ADR-017 Decision 1)",
					item.Number, remoteRunID)
			}
		}
		if runID == "" {
			baseDir := filepath.Join(workspaceRoot, ".nightgauge", "pipeline")
			if rs, err := runstate.Load(baseDir); err == nil && rs != nil && rs.RunID != "" {
				if runstate.IsIdentity(rs.RunID) {
					runID = rs.RunID
				} else {
					log.Printf("#%d: ignoring non-identity run id %q from run-state.json — minting locally (ADR-017 Decision 1)",
						item.Number, rs.RunID)
				}
			}
		}
		// Lifecycle trace fallback (#179 / ADR 013): when neither a remote id
		// nor run-state.json resolved a run_id, generate one here so the run
		// is still traced AND joined — exit records, telemetry, and the V3
		// record all read runtime.RunID, so stamping it threads one key
		// through every store.
		//
		// A mint failure is fatal for the run (ADR-017 step 0b) — but it is NOT
		// aborted here. This point is ABOVE the terminal defer, so a bare return
		// would exit outside the funnel every other pre-dispatch fatal exits
		// through: the autonomous scheduler's Running entry would never be
		// cleared (leaking a MaxConcurrent slot and pinning the issue), the
		// board would stay In Progress, and there would be no pipeline_done, no
		// history record, no outcome. So record the error, construct with an
		// empty identity, and let the run-identity preflight below the terminal
		// defer — beside the license and identity preflights — refuse the run
		// and book the failure. Nothing between here and there reads RunID in a
		// way that misbehaves on empty: trace.NewWriter returns a nil (no-op)
		// writer for an empty id, registerRuntime keys on the issue number, and
		// Persist REFUSES an identity-less runtime outright (ADR-017 Decision 1)
		// rather than writing a nameless file.
		if runID == "" {
			id, idErr := newRunID()
			if idErr != nil {
				runIDMintErr = idErr
				log.Printf("#%d: cannot mint a run identity: %v — the run will be refused at preflight (ADR-017)",
					item.Number, idErr)
			} else {
				runID = id
			}
		}
	}

	runtime := state.NewRuntimeState(item.Repo, item.Number, item.ID, runID)
	runtime.Title = item.Title
	// Capture the issue body at pickup (#183) so the run record + telemetry can
	// show the issue context (title, labels, body) on the dashboard run-detail
	// page without leaving the dashboard. Title/labels are already on the board
	// item; the body is fetched here (best-effort — a fetch failure leaves it
	// empty and the run proceeds). Bounded to a sensible excerpt at capture.
	runtime.Body = s.captureIssueBody(ctx, item)

	// Per-run lifecycle decision trace writer (#179 / ADR 013). Nil-safe and
	// fail-open: emit calls below never block or fail the pipeline.
	tracer := trace.NewWriter(workspaceRoot, runtime.RunID, item.Repo, item.Number)

	// Register the runtime so IPC-mode phase transitions (which arrive via
	// the IPC server's pipeline.notifyPhaseTransition handler) can update
	// PhaseHistory on this runtime. Without registration the IPC path leaves
	// PhaseHistory empty for the entire run, and any extension reload
	// mid-pipeline loses phase counts on already-completed stages.
	s.registerRuntime(runtime)
	defer s.unregisterRuntime(runtime.RunID)

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
	// At PICKUP the worktree does not exist yet and issue-pickup has not written
	// the context, so on a first run this is definitionally empty. It is read
	// here anyway because the routing decisions below want whatever is already
	// known; the value that reaches the CORPUS is re-resolved at recording time
	// (#994), which is the only point at which the file is guaranteed to exist.
	complexityScore, issueRoutingPath, predictedModel := loadIssueContext(
		workspaceRoot, loadWorktreePath(workspaceRoot, item.Number), item.Repo, item.Number)

	// Job-class attribution at pickup (#606): the labels are already read
	// here, and the conservative type-label mapping is the same one the TS
	// resolver applies (jobClassForIssue) — so eval-advice consumption keys
	// on exact job-class entries on BOTH paths, closing the (model, *, *)
	// backoff asymmetry (the dual-path family #340 removed). "" when no type
	// label directly names an eval job class: no advice, the axis query
	// alone decides.
	issueJobClass := routing.JobClassForLabels(item.Labels)

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
		// A forced escalation is a run-wide FLOOR, not merely a prediction
		// (#340). The prediction alone stopped being sufficient once
		// stageBaseModel gained the lightweight-stage defaults, which sit
		// ABOVE the prediction: an operator escalating a stalled pr-create
		// would have watched the retry re-run it on haiku.
		modelFloors = raiseStageFloors(modelFloors, forcedTier)
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
	rerouteWorktree := loadWorktreePath(workspaceRoot, item.Number)
	if s.shouldReRoute(workspaceRoot, rerouteWorktree, item.Repo, item.Number) {
		if rec, rerouteErr := s.reRouteContext(ctx, workspaceRoot, rerouteWorktree, item.Repo, item.Number, predictedModel); rerouteErr != nil {
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
	// workRecovered is set by either #3542 rescue site when uncommitted work
	// was preserved into a recovery commit. Separate from terminalFailureKind
	// on purpose (#875): "work survived" and "why the run failed" are different
	// facts, and encoding the first as the second is what made a run that could
	// not compose its SKILL.md file its post-mortem under worktree_uncommitted.
	// The consumers below that mean "work survived" read THIS.
	workRecovered := false
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
	// Publish the run's outcome to the caller. Registered BEFORE the terminal
	// defer below so it runs AFTER it (defers are LIFO) and therefore observes
	// the final values that defer settles — the #3542 rescue's reclassification
	// included. Deliberately outside the content-pinned fence: it reads the
	// terminal state, it does not participate in producing it.
	defer func() {
		success = pipelineSuccess
		terminalKind = terminalFailureKind
	}()
	// terminal-parity:begin runPipeline-terminal-defer (#257 — this region is
	// content-pinned by testdata/terminal_behaviors.json; any edit fails
	// terminal_parity_test.go until the manifest is updated, which is the
	// moment to check the extension path for the same behavior)
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
				if recErr := RecoverUncommittedWork(worktreePath, item.Number, string(preSnap.Stage)); recErr != nil {
					log.Printf("#%d: uncommitted work recovery failed: %v — worktree preserved at %s",
						item.Number, recErr, worktreePath)
				} else {
					log.Printf("#%d: uncommitted work recovered — setting terminal_failure_kind=%s",
						item.Number, TerminalKindWorktreeUncommitted)
					terminalFailureKind = TerminalKindWorktreeUncommitted
					workRecovered = true
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

		// Remove the item from the queue on every exit path — success,
		// failure, cancellation. No-op when the run wasn't dispatched via
		// DequeueIndependent (e.g. the autonomous board-scan path). (Issue #232)
		s.CompleteQueueItem(item.Repo, item.Number)

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

		// TERMINAL LATCH — the durable half (ADR-017 Decision 5 step 1c, #440).
		//
		// The latch and the seal were EXTENSION-PATH-ONLY until this line. The
		// IPC funnel latches in ClaimTerminal and seals in SealAndRemove; this
		// path had neither, so a finished or crashed Go-scheduler run left a
		// NON-TERMINAL snapshot on disk indefinitely and only the orphan
		// reconciler's 14-day cap ever collected it. Every reader of the
		// canonical snapshots then had to special-case this path — #410's
		// ActiveIssuesFromSnapshots reader is liveness-bounded rather than
		// terminality-based for exactly this reason — and ADR-017 §7.4's
		// disposition reasoning, which assumes terminality is meaningful, was
		// simply not true here. The two dispatch paths silently disagreed
		// about what a snapshot on disk means; they no longer do.
		//
		// LATCHED HERE, at outcome determination, because this is the first
		// point where terminalFailureKind is final: the classification block
		// above is what turns a bare `pipelineSuccess == false` into the kind
		// the outcome string is derived from. Latching earlier would stamp an
		// outcome the run had not finished deciding.
		//
		// The latch does NOT freeze the runtime — markTerminalLocked sets the
		// durable marker and nothing else. The bookkeeping below (outcome
		// recording, the V3 record, cleanup) still mutates and still persists;
		// what changes is that every Persist from here on marshals
		// `terminal: true`, so even a snapshot that lands between this line and
		// the seal is one adoption refuses and the reconciler removes without
		// emitting. Writes stop at the seal, not here.
		runtime.MarkTerminal(schedulerTerminalOutcome(pipelineSuccess, terminalFailureKind))

		// Two terminal kinds record NOTHING, and the extension path skips the
		// same two (internal/ipc/outcome_record.go) — one corpus, one meaning
		// per field, or `success` and `costUsd` carry two meanings across the
		// file's two writers with no discriminator to tell them apart.
		//
		//   network_unavailable (#3296) — the cost / duration / token data from
		//   a half-completed network-killed run is environmental noise, not
		//   signal about model or stage performance.
		//
		//   blocked_dependency (#305) — a deferral is not a failure and did no
		//   work: no AI stage ran, so the row would book success:false at ~$0.
		//   Left in, five such deferrals in the recent half of a 20-run corpus
		//   flip the cost-optimization loop to `closing` ("cost per run
		//   decreasing") and the reliability loop to `degrading` ("failure rate
		//   increasing") — credit for savings and blame for failures from runs
		//   that never executed a stage. Both verdict strings feed Phase 4
		//   proposal generation in the continuous-improvement skill.
		var outcomePrediction *state.OutcomePrediction
		switch terminalFailureKind {
		case TerminalKindNetworkUnavailable:
			log.Printf("#%d: skipping outcome recording (terminal_failure_kind=%s — environmental, not model)",
				item.Number, TerminalKindNetworkUnavailable)
		case TerminalKindBlockedDependency:
			log.Printf("#%d: skipping outcome recording (terminal_failure_kind=%s — deferral, no work done)",
				item.Number, TerminalKindBlockedDependency)
		default:
			// recordOutcome re-resolves the prediction from the run's worktree
			// before writing the row (#994) — the values here were read at
			// pickup, before the file existed.
			outcomePrediction = s.recordOutcome(item, snap, pipelineSuccess, complexityScore, predictedModel, workspaceRoot)
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

		// Clean up the feature branch after the pipeline completes.
		//
		// A shipped run's branch is spent: the PR merged, so origin's copy and
		// the local ref both go. A FAILED run is the #163 case and is handled
		// deliberately, because the pre-fix behaviour here was to delete origin's
		// copy unconditionally — which is wrong in both directions. It destroys
		// the remote branch of an open PR that is holding the work, and (because
		// the branch name was only ever looked up in the main workspace root, so
		// every worktree-isolated run resolved "") it in practice deleted nothing
		// at all, leaving a killed stage's pushed commit orphaned on origin to
		// fork the branch on the next attempt.
		//
		// So: PR present → keep origin's copy, drop only the local ref. No PR →
		// ReclaimOrphanedRemoteBranch, which drops origin's copy ONLY when its
		// head is contained in this run's local history (proof the pipeline
		// pushed it) and leaves anyone else's commit standing for the next
		// attempt's fork pre-flight to report.
		if branchName := resolveFeatureBranch(runtime, workspaceRoot, item.Number); branchName != "" && s.execMgr != nil {
			switch {
			case pipelineSuccess:
				// Content-diff gate before any local branch delete (#106): even
				// on the success path, never hand CleanupBranch's unconditional
				// `git branch -D` a branch that isn't actually merged into the
				// default branch — reuses the same squash-merge-safe check the
				// worktree reclamation sweep relies on.
				if err := s.execMgr.CleanupBranchAndRemoteIfMerged(item.Repo, branchName); err != nil {
					log.Printf("#%d: branch cleanup failed for %s: %v", item.Number, branchName, err)
				} else {
					log.Printf("#%d: cleaned up feature branch %s", item.Number, branchName)
				}
			case loadPrUrl(stageWorkspace(runtime, workspaceRoot), item.Number) != "":
				log.Printf("#%d: run failed but PR exists — keeping origin/%s (the PR holds the work), dropping local ref only",
					item.Number, branchName)
				_ = s.execMgr.CleanupLocalBranch(item.Repo, branchName)
			default:
				// Reclaim from the repo root, not the worktree: linked worktrees
				// share the ref store and object database, so the branch tip is
				// resolvable from either — but the worktree may already have been
				// pruned by the time this runs, and the repo root has not.
				res := ReclaimOrphanedRemoteBranch(context.Background(), workspaceRoot, branchName)
				if res.Deleted {
					log.Printf("#%d: orphaned-push reclamation — %s", item.Number, res.Reason)
				} else {
					log.Printf("#%d: orphaned-push reclamation declined — %s", item.Number, res.Reason)
				}
				_ = s.execMgr.CleanupLocalBranch(item.Repo, branchName)
			}
		}

		// Remove the worktree for every terminal outcome — merged, failed,
		// abandoned/discarded — not only on success (#106). CleanupWorktree
		// itself preserves a worktree with uncommitted tracked changes (logs
		// SkipDirty and returns without removing), so a failed run a developer
		// still needs to inspect is never silently destroyed here.
		if s.execMgr != nil {
			if err := s.execMgr.CleanupWorktree(item.Repo, item.Number); err != nil {
				log.Printf("#%d: worktree cleanup failed: %v", item.Number, err)
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
		//
		// Issue #163 adds a third, for the opposite reason: branch_forked is NOT
		// recoverable by re-running. Reverting the board to Ready re-dispatches
		// the issue straight back into the same non-fast-forward rejection, which
		// is the loop that burned a full pipeline per cycle. The issue stays put
		// and its Action Center card is the way back in.
		// commit_orphaned (#266) joins branch_forked for the same reason:
		// reverting to Ready re-dispatches into a fresh worktree that redoes the
		// work, while the commit the pipeline actually produced sits preserved
		// (by the CleanupWorktree/CleanupLocalBranch ahead-of-base guard) on a
		// branch nobody re-runs against automatically. The way back in is the
		// Action Center card, not an automatic retry.
		// workRecovered, not the kind (#875). The revert is harmful whenever a
		// recovery commit exists — re-dispatch regenerates the work in a fresh
		// worktree while the preserved commit sits on a branch nobody re-runs —
		// and that is true regardless of what NAME the run's failure ended up
		// with. Keying it on terminalFailureKind meant the protection could only
		// be kept by also renaming the failure after the rescue. Strictly wider
		// than the previous condition: every kind listed below still skips.
		skipBoardRevert := workRecovered ||
			terminalFailureKind == TerminalKindWorktreeUncommitted ||
			terminalFailureKind == TerminalKindBudgetCeiling ||
			terminalFailureKind == TerminalKindBranchForked ||
			terminalFailureKind == TerminalKindCommitOrphaned
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

		// SEAL AND REMOVE — claim step 4 (ADR-017 Decision 5, #440), the second
		// half of the parity this path was missing. LAST in the defer, after
		// every piece of terminal bookkeeping above has run: the record, the
		// outcome, the branch and worktree cleanup and the board revert all
		// still need a runtime they can persist, and the seal is the operation
		// that ends writing. Sealing earlier would turn each of those persists
		// into an ErrRunSealed no-op.
		//
		// THE PATH IS THE IDENTITY. SnapshotFilename composes this run's own
		// runId, so the remove cannot take a successor's file even in
		// principle — the property that makes this safe to do on a path where
		// a re-dispatch of the same issue may already be running.
		//
		// Rooted at workspaceRoot, which in runPipeline is s.runRoot(item.Repo)
		// — the run's OWN repo root, not the scheduler's launch root. That is
		// the directory every other persist on this path writes to, and since
		// #410 it is also the in-flight source ActiveIssuesFromSnapshots scans.
		// Sealing anywhere else would leave the real snapshot behind and remove
		// nothing.
		//
		// Best-effort with three distinct outcomes, kept distinct in the log
		// because they leave three different things on disk (the extension
		// path's seal at internal/ipc/server.go makes the same three-way
		// distinction, and for the same reason).
		if stateDir := filepath.Join(workspaceRoot, ".nightgauge", "pipeline"); workspaceRoot != "" {
			if err := runtime.SealAndRemove(stateDir); err != nil {
				switch {
				case errors.Is(err, state.ErrNotRunOwner):
					// NOT A FAILURE — the correct refusal (#557). The snapshot
					// belongs to a live foreign process; sealing would remove a
					// file whose owner re-creates it at its next persist,
					// resurrecting the run.
					log.Printf("#%d: DECLINED to seal run %s — its snapshot belongs to a live owner process, not to this one: %v",
						item.Number, snap.RunID, err)
				case errors.Is(err, state.ErrSealWriteFailed):
					// The terminal marker never reached disk, but the seal is
					// latched and the stale NON-TERMINAL snapshot was removed
					// rather than left for a restart to rehydrate.
					log.Printf("#%d: seal for run %s could NOT WRITE the terminal marker; the stale snapshot was removed instead (non-fatal): %v",
						item.Number, snap.RunID, err)
				case errors.Is(err, state.ErrNoRunIdentity):
					// A run refused at the identity preflight never persisted a
					// snapshot under a composed name, so there is nothing to
					// seal. Expected, not a defect.
					log.Printf("#%d: no run identity — nothing to seal (the run was refused before it persisted): %v",
						item.Number, err)
				default:
					// The file on disk DOES carry `terminal: true`; only its
					// removal failed. Adoption refuses that snapshot and the
					// reconciler removes it without emitting.
					log.Printf("#%d: seal-and-remove for run %s failed AFTER the snapshot was terminal-marked (non-fatal): %v",
						item.Number, snap.RunID, err)
				}
			}
		}
	}()
	// terminal-parity:end runPipeline-terminal-defer

	// Set board status to In Progress (non-fatal: board sync failure should not abort pipeline)
	if s.stateSvc != nil {
		if err := s.stateSvc.StartPipeline(ctx, item.ID, state.StageIssuePickup); err != nil {
			log.Printf("#%d: board sync unavailable, continuing: %v", item.Number, err)
		}
	}

	// Repo-root preflight (#882): refuse a run whose TARGET repo has no
	// resolvable filesystem root. Everything downstream — the worktree, the
	// epic base branch (created from a default branch and PUSHED to a remote),
	// the trace, the history record — is rooted at the value resolved above. If
	// that value is not the target repo's root it is some OTHER real
	// repository's root, and the run publishes work into a project it has
	// nothing to do with. Refusing books the failure through the terminal
	// funnel exactly as the run-identity, license and identity preflights do.
	if runRootErr != nil {
		reason := "repo root preflight: " + runRootErr.Error()
		log.Printf("#%d: %s", item.Number, reason)
		runtime.SetStageError("pipeline-start", reason)
		s.emitStateChanged(item.Repo, item.Number, runtime)
		return // Pipeline blocked by repo-root check
	}

	// Run-identity preflight (ADR-017 step 0b): refuse a run whose identity is
	// not a canonical run identity. Every store this run would write — exit
	// records, telemetry, the V3 record — is keyed by run_id, and every stage
	// would dispatch without one. The mint above records its failure instead of
	// returning, because a return there sits above the terminal defer; refusing
	// here books the failure exactly as a license or identity block is booked.
	// First of the three preflights because it is the only one that costs
	// nothing to check.
	//
	// THE PREDICATE IS runstate.IsIdentity, NOT `== ""` (ADR-017 Decision 1).
	// The resolution above already ignores a non-identity from either borrowed
	// source, so this branch should be unreachable for a present-but-invalid
	// id — which is exactly why it must exist: if any future path ever seeds
	// one, the run fails LOUDLY here, through the terminal funnel (booked,
	// board reset, concurrency slot released) instead of running to completion
	// while every single Persist is silently refused at the state sink.
	if !runstate.IsIdentity(runtime.RunID) {
		reason := "run identity preflight: no run_id resolved for this run"
		if runtime.RunID != "" {
			reason = fmt.Sprintf("run identity preflight: run_id %q is not a canonical run identity", runtime.RunID)
		}
		if runIDMintErr != nil {
			reason = fmt.Sprintf("run identity preflight: cannot mint a run_id: %v", runIDMintErr)
		}
		log.Printf("#%d: %s", item.Number, reason)
		runtime.SetStageError("pipeline-start", reason)
		s.emitStateChanged(item.Repo, item.Number, runtime)
		return // Pipeline blocked by run-identity check
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
	pipelineBudgetCeilingUSD := PipelineBudgetCeilingUSD(workspaceRoot)

	stages := []state.PipelineStage{
		state.StageIssuePickup,
		state.StageFeaturePlanning,
		state.StageFeatureDev,
		state.StageFeatureValidate,
		state.StagePRCreate,
		state.StagePRMerge,
	}

	// A Dependabot REMEDIATION PULL REQUEST skips feature-planning and
	// feature-dev. The decision, the skip bookkeeping and the trace emission are
	// all inside applyDependabotFastTrack rather than inlined here, so a test
	// that calls it observes the same code the run does — see the seam note on
	// the function.
	stages = applyDependabotFastTrack(item, stages, runtime, tracer)

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

	// Records WHICH #3873 non-terminal reconcile arm (if any) ended this run, so
	// the completion block below can pick the run's terminal board status per-arm
	// (#398). Hoisted OUT of the loop for exactly that reason: the per-iteration
	// reconciledNonTerminal flag dies with the iteration that breaks the loop,
	// and the completion block runs after it. reconcileNone — the zero value —
	// is every normal completion.
	reconciledArm := reconcileNone

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
			// Enter the stage before failing it (mirrors the branch-fork
			// preflight below). Nothing is dispatched — the point is that no
			// tokens are spent — but every snap.Stage-keyed reader (IPC
			// terminal-notify, the exit-record fallback, outcome recording,
			// autonomous reporting) derives "what went wrong" from the
			// CURRENT stage: without this, StageErrors keys the budget
			// refusal under the refused stage while snap.Stage still names
			// the PREVIOUS one, and every one of those readers misses it.
			runtime.BeginStage(stage)
			runtime.SetStageError(stage, budgetDecision.Reason)
			s.emitStateChanged(item.Repo, item.Number, runtime)
			// Issue #3001: record the terminal kind so the V3 record names what
			// stopped the run, not just "failed".
			terminalFailureKind = TerminalKindBudgetExceeded
			// Action Center budget-ceiling producer (ADR 015 §F #4): surface an
			// approve card offering budget.raiseCeiling (a runtime override that
			// PipelineBudgetCeilingUSD honors) + retry, or halt. Propose a 50%
			// raise above the current ceiling, floored above the current spend.
			proposed := ProposedCeilingUSD(PipelineBudgetCeilingUSD(workspaceRoot), runtime.TotalCostUSD)
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
		prereqCtxType, prereqCtxOK := effectivePrereqContextType(stage, runtime)
		if prereqCtxOK {
			ctxPath := stagecontext.ContextPath(stageWorkspace(runtime, workspaceRoot), item.Number, prereqCtxType)
			if _, err := os.Stat(ctxPath); os.IsNotExist(err) {
				// "missing prerequisite" is load-bearing prose, not decoration
				// (#620): internal/terminalkind/table.json already routes it to
				// validation_error, and the corpus row that pins it names THIS
				// producer ("the stage pre-condition check that refuses to run a
				// stage whose input context is absent"). Keeping the phrase means
				// the explicit kind set below and every independent
				// re-derivation from the text — autonomous.go's
				// onPipelineComplete wrapper, NotifyComplete's defense-in-depth
				// re-classify — land on the same answer instead of disagreeing.
				reason := fmt.Sprintf(
					"missing prerequisite: stage %s needs the %s context (resolved skip-aware) and %s does not exist",
					stage, prereqCtxType, ctxPath)
				// Pre-#620 this was `log.Printf(...); return` — no BeginStage,
				// no SetStageError, no kind — so the terminal defer's
				// ClassifyTerminalKind("") produced "" and the run was booked
				// terminal with an empty cause. refusePreDispatch books all
				// three, and rescues any uncommitted work first: booking a kind
				// is what disables the defer's own #3542 rescue, so the rescue
				// has to happen on this side of the return.
				terminalFailureKind, workRecovered = s.refusePreDispatch(item, runtime, workspaceRoot, stage, tracer,
					"prerequisite-preflight", reason)
				return // Pipeline failed — missing prerequisite
			}
		}

		// Branch-fork pre-flight (#163). One `git ls-remote` at the stage
		// boundary, before a single token is spent. If the remote branch head is
		// not reachable from the local tip, the branch has forked and every push
		// this run makes is already doomed — pre-fix that was discovered at push
		// time, i.e. after a full pipeline had regenerated the whole
		// implementation (~$25) to reach a guaranteed rejection, once per retry
		// cycle. See branchForkPreflightApplies for which stages it gates, and
		// CheckBranchFork for why every non-answer (offline, no origin) is
		// fail-open — an unreachable remote must never manufacture a block.
		if branchForkPreflightApplies(stage) {
			if branch := resolveFeatureBranch(runtime, workspaceRoot, item.Number); branch != "" {
				fork := CheckBranchFork(ctx, stageWorkspace(runtime, workspaceRoot), branch)
				if fork.Forked() {
					reason := fmt.Sprintf("[branch-forked] %s", fork.Detail)
					log.Printf("#%d: stage %s blocked before dispatch — %s", item.Number, stage, reason)
					// Enter the stage before failing it. Nothing is dispatched
					// (the point is that no tokens are spent), but the run record
					// keys its failure detail off the CURRENT stage: without this
					// the record would name the terminal kind and drop the
					// sentence carrying the two SHAs — which is the only part a
					// post-mortem can act on.
					runtime.BeginStage(stage)
					runtime.SetStageError(stage, reason)
					terminalFailureKind = TerminalKindBranchForked
					s.emitStateChanged(item.Repo, item.Number, runtime)
					tracer.Emit(trace.KindStageSkip, string(stage), trace.StageSkipPayload{
						Source: "branch-fork-preflight",
						Reason: fork.Detail,
					})
					// Action Center branch-fork producer (#163): no retry clears a
					// fork, so surface it as an unblock card rather than letting the
					// run recycle into the same rejection.
					s.raiseBranchForked(item.Repo, item.Number, runtime.RunID, branch, fork)
					return
				}
			}
		}

		// Per-stage adapter resolution (#54): only meaningful on the
		// Go-direct path (execMgr holds an adapter) and when the invocation
		// did not pin one via --adapter / NIGHTGAUGE_ADAPTER.
		//
		// Hoisted above the render (#79). Overlay keys are provider-scoped for
		// tier bands — Resolve matches a concrete id across providers, but
		// "sonnet" only inside one — so rendering before this ran keyed the
		// cascade off the PREVIOUS stage's adapter in a mixed-adapter pipeline.
		// The error is still consumed by the dispatch switch below, unchanged;
		// only the re-point moves, and it touches nothing but execMgr's adapter.
		var adapterResolveErr error
		if s.adapterExplicit == "" && s.execMgr != nil && s.execMgr.HasAdapter() {
			adapterResolveErr = s.applyStageAdapter(string(stage), workspaceRoot)
		}

		// Resolve the dispatch model BEFORE composing the skill (#79). Overlays
		// must key off the model that actually executes, not a configured tier
		// alias — so every escalation, floor, and downgrade below has to have
		// been applied by the time Render runs. The behavioral preamble still
		// applies after the prompt is assembled; only the resolution moves.
		model := s.resolveDispatchModel(stage, item.Number, workspaceRoot, predictedModel, modelFloors, issueJobClass)

		// Compose SKILL.md through the one renderer (#78), overlay-aware (#79).
		// With no overlay files present this is byte-identical to a base-only
		// render: an unmatched key contributes nothing (ADR 016 §2).
		adapterName := ""
		if s.execMgr != nil && s.execMgr.HasAdapter() {
			adapterName = s.execMgr.AdapterName()
		}
		// descentProvider names the provider this dispatch will execute on
		// (#611) — execMgr's adapter on the Go-direct path, and on the IPC
		// path what the adapter itself reported for this stage's previous
		// attempt. Both provider-scoped descent decisions below (the sticky
		// effort rung on the wire, and the ladder an API rejection walks) read
		// this ONE value, so a descent cannot be written under one provider
		// and read under another. See descentProviderForDispatch for why the
		// IPC arm is evidence rather than a re-derivation of the extension's
		// adapter resolution.
		descentProvider := descentProviderForDispatch(runtime, stage, adapterName)
		skillData, err := skillrender.Render(skillrender.Options{
			Stage:       string(stage),
			Model:       model,
			Adapter:     adapterName,
			SkillsRoots: skillrender.DefaultRoots(workspaceRoot),
			Warn:        func(msg string) { log.Printf("#%d: %s", item.Number, msg) },
		})
		if err != nil {
			// A render failure means the stage has no prompt to dispatch: the
			// SKILL.md could not be located, read, or composed. Distinct from
			// the prerequisite refusal above on purpose (#620) — that one says
			// "the previous stage's handoff is missing" (look upstream), this
			// one says "this stage's skill did not compose" (look at the skills
			// tree / overlays / adapter). A shared generic string would put one
			// message on two different operator actions, which is the failure
			// mode this fix exists to remove, so the two never share prose.
			reason := fmt.Sprintf("skill render failed: stage %s could not compose its SKILL.md (model=%q, adapter=%q): %v",
				stage, model, adapterName, err)
			// Same three-part booking as the prerequisite refusal above, and
			// the same reason for doing the #3542 rescue on this side of the
			// return — with a sharper worked example. This site is reachable at
			// feature-validate, i.e. immediately after feature-dev, which does
			// not commit (AGENTS.md #1608). A broken overlay, a missing skills
			// root or an adapter mismatch there refuses the stage with the
			// entire implementation still uncommitted on disk. A dedicated
			// skill-render terminal kind would be truer than validation_error,
			// but it is a cross-surface change (terminalkind table + TS schema
			// + generated SDK mirror + FAILURE_TAXONOMY) and belongs in its own
			// issue.
			terminalFailureKind, workRecovered = s.refusePreDispatch(item, runtime, workspaceRoot, stage, tracer,
				"skill-render", reason)
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

		// Determine context file paths (skip-aware input; worktree-rooted).
		// Reuses prereqCtxType/prereqCtxOK computed above (#49) — the same
		// effectivePrereqContextType result feeds both the prerequisite gate
		// and the prompt's Invocation Context block.
		ws := stageWorkspace(runtime, workspaceRoot)
		var contextFile string
		if prereqCtxOK {
			contextFile = stagecontext.ContextPath(ws, item.Number, prereqCtxType)
		}

		// Build prompt for stdin delivery. The absolute skill dir rewrites
		// skill-relative read directives so they resolve from cross-repo
		// worktrees (#196). effectiveContextType/contextFile (#49) surface the
		// skip-aware prerequisite resolution to the skill body so fast-tracked
		// feature-dev knows to read issue-{N}.json instead of planning-{N}.json.
		effectiveContextType := ""
		if prereqCtxOK {
			effectiveContextType = string(prereqCtxType)
		}
		prompt := execution.BuildPrompt(stage, skillData.Content, item.Number, filepath.Dir(skillData.SkillPath), effectiveContextType, contextFile)

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

		// Determine output context file path (worktree-rooted). contextFile
		// (input) was already resolved above alongside effectiveContextType.
		var outputFile string
		if ctxType, ok := stageOutputContextType[stage]; ok {
			outputFile = stagecontext.ContextPath(ws, item.Number, ctxType)
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
		// Dispatch envelope (#580 → #581 → #606): effort/thinking/mode
		// alongside the model above. On the IPC path (no Go-side adapter) the
		// scheduler RESOLVES effort and thinking onto the wire — the selection
		// query's rung plus the Go-owned effort chain (dispatch_envelope.go) —
		// and the record carries the wire value the extension executes
		// verbatim, the same epistemic status as the Model field. On the
		// Go-direct adapter path the thinking axis stays env/adapter-owned
		// (the #580 observation functions answer, honestly absent when Go has
		// no direct evidence) — but the EFFORT axis now reaches the grok
		// adapters through RunOptions (#606): xai is where the effort rung IS
		// the downgrade ladder (#532), so the envelope must actually dispatch.
		wireEffort, wireThinking := "", ""
		grokEnvEffortSet := false
		if adapterName == "" {
			wireEffort = resolveWireEffort(workspaceRoot, stage)
			wireThinking = resolveWireThinking(model, stagePerfMode)
		} else if models.ProviderForAdapter(adapterName) == "xai" {
			// The operator's NIGHTGAUGE_GROK_EFFORT override owns the
			// dispatch when set (in ANY grok vocabulary — dispatchGrokEffort
			// in the adapter applies the same precedence): resolve no wire
			// effort then, so the envelope neither competes with the
			// override nor mis-attributes a value that never dispatched.
			grokEnvEffortSet = strings.TrimSpace(os.Getenv("NIGHTGAUGE_GROK_EFFORT")) != ""
			if !grokEnvEffortSet {
				wireEffort = resolveWireEffort(workspaceRoot, stage)
			}
		}
		// Sticky effort substitution (#606): a same-model effort descent
		// recorded by the RetryEngine overrides the resolved wire effort,
		// LAST — after the mode's effort clamps — for the #42 reason: a floor
		// re-raising what an API rejection just lowered would re-fail
		// identically. `model` has already been rerouted by ApplyDowngrades,
		// so the substituted tier and this lookup key always agree. Never
		// over an operator env override, which outranks the pipeline.
		//
		// Keyed by the EXECUTING dispatch's provider as well as the tier
		// (#611): the substitution is a rung on one provider's ladder, and in
		// a mixed-adapter run a tier-only lookup handed an xai descent's
		// effort to the next stage that resolved to that tier on any other
		// adapter — a legal EFFORT_LEVELS value with the wrong provenance.
		// descentProvider is "" whenever nobody can name the executing
		// provider first-hand, and an unnamed dispatch deliberately reads no
		// rung: substituting one there would re-create the same bleed from a
		// guess instead of from a missing key.
		if !grokEnvEffortSet {
			if sticky := s.retryEngine.StickyEffort(descentProvider, model); sticky != "" {
				wireEffort = sticky
			}
		}
		// Attribution: the operator env override (resolveDispatchEffort, xai
		// only, EFFORT_LEVELS values — a grok-native rung honestly attributes
		// nothing, per the pinned #580 filter) wins exactly as it wins at the
		// adapter boundary; the wire effort answers otherwise.
		effortAttr := resolveDispatchEffort(adapterName)
		if effortAttr == "" && !grokEnvEffortSet {
			effortAttr = wireEffort
		}
		thinkingAttr := wireThinking
		if thinkingAttr == "" {
			thinkingAttr = resolveDispatchThinking(adapterName, model)
		}
		runtime.RecordStageModelSelectionMode(stage, resolveDispatchSelectionMode(workspaceRoot))
		runtime.RecordStageEffort(stage, effortAttr)
		runtime.RecordStageThinking(stage, thinkingAttr)

		// Clear the stage-child pid BEFORE the stage-start persist below (#534).
		//
		// At this instant runtime.PID still holds the PREVIOUS stage's EXITED
		// child: SetProcess (internal/execution/manager.go) is the only writer on
		// the scheduler path, it runs after cmd.Start(), and the scheduler then
		// blocks until that stage exits. Persisting without clearing would write
		// a snapshot with a correct stage and a fresh mtime around a dead pid —
		// asserting it more confidently than the old stage-boundary-only write
		// did, and handing the liveness ladder's arm 3 a pid that a recycled
		// process makes read as live.
		//
		// Zero means "no child is executing this run right now", which is exactly
		// true here. It mirrors the discipline the extension path already applies
		// on a stage's terminal transition (see SetStageChild in
		// internal/state/runtime_state.go). SetStageChild, not SetProcess: this
		// must not disturb WorktreeDir.
		runtime.SetStageChild(0)

		// Persist the runtime snapshot at stage START (#534).
		//
		// runtime-{issue}-{runId}.json is what the extension mirrors a
		// scheduler-owned run from (CliPipelineReconciliationService composes the
		// filename from the sidecar's identity, PipelineStateService turns the
		// snapshot into stage statuses). Written only on stage COMPLETION, it had
		// no file at all during the first stage — the run was absent from the
		// Pipeline tree until issue-pickup finished — and thereafter named the
		// stage that had just completed, which applyRuntimeSnapshot correctly
		// skips, so the live stage showed pending for the whole run.
		//
		// workspaceRoot is runPipeline's LOCAL value (s.runRoot(item.Repo), the
		// run's TARGET repo per #229), matching the stage-completion persist
		// below — NOT s.workspaceRoot, which would split a cross-repo run's state
		// across two repos on every stage. Pinned by
		// TestRunPipeline_StageStartSnapshotLandsInTheRunsTargetRepo, which runs a
		// cross-repo dispatch where the two roots differ; without it this line's
		// correctness rested on this comment. Every runtime-snapshot persist in
		// this file now roots the same way: the post-merge breadcrumb in
		// verifyPRMergeForStage was the last launch-rooted one, and #441 moved it
		// onto s.runRoot(item.Repo) too (pinned by
		// TestVerifyPRMergeForStage_BreadcrumbLandsInTheRunsTargetRepo).
		//
		// BEFORE emitStateChanged below, not after: that callback ships
		// runtime.Snapshot() over the wire, and emitting first would describe this
		// stage with the previous stage's dead pid while the file for the same
		// stage carries 0. Nothing reads the wire snapshot's pid today; the order
		// is what keeps it from mattering if something ever does.
		//
		// Best-effort, exactly like the completion persist: log and continue.
		//
		// The terminal latch does NOT cover this write, and saying it did would be
		// a false invariant: nothing seals a scheduler-owned runtime. SealAndRemove
		// has one in-tree caller — notifyComplete (internal/ipc/server.go) — and
		// resolveRun refuses terminal verbs against a scheduler-owned run
		// (run_wrong_owner), so rs.sealed is false for this run's entire life and
		// the guard never fires here. What makes the extra write safe is narrower
		// and true: it re-creates exactly the file the stage-completion persist
		// already re-creates, from the same sole writer, into the same directory.
		// RESIDUAL, pre-existing and not closed by this change: another process
		// can adopt this run's non-terminal snapshot (loadRunSnapshot in
		// internal/ipc/run_registry.go), seal the FILE from there, and this
		// scheduler's own unsealed runtime re-creates it at the next stage start.
		if persistErr := runtime.Persist(filepath.Join(workspaceRoot, ".nightgauge", "pipeline")); persistErr != nil {
			log.Printf("#%d: failed to persist state at %s start: %v", item.Number, stage, persistErr)
		}

		s.emitStateChanged(item.Repo, item.Number, runtime)

		// Crash-recovery sidecar (Issue #3001): record the in-flight run at
		// stage-start. Removed on clean completion (success and failure paths
		// both call removeCurrentRunSidecar). A stale sidecar at scheduler
		// startup signals an orchestrator process crash → the synthesizer in
		// loadQueue writes a terminal-failure RunRecord and pauses the queue.
		if sidecarErr := writeCurrentRunSidecar(workspaceRoot, CurrentRunSidecar{
			IssueNumber: item.Number,
			Repo:        item.Repo,
			// The identity the extension's CLI-run reconciler needs to compose
			// runtime-{issue}-{runId}.json (ADR-017 Decision 8).
			RunID:      runtime.RunID,
			ItemID:     item.ID,
			Title:      item.Title,
			StartedAt:  runtime.StartedAt,
			Stage:      string(stage),
			StageStart: time.Now().UTC(),
			PID:        os.Getpid(),
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
			// The effort/thinking halves of the dispatch envelope (#581).
			// Non-empty only on the IPC path, where the extension executes
			// the wire effort verbatim; see the wire-envelope block above.
			Effort:   wireEffort,
			Thinking: wireThinking,
			// Stage-aware + model-aware last-resort context deadline (#73).
			// Replaces a blind 30-min literal that killed frontier-mode Fable
			// stages before their own progress-gated hard cap could apply.
			Timeout:      routing.ResolveStageTimeout(string(stage), model),
			SkillPath:    skillData.SkillPath,
			ContextFile:  contextFile,
			OutputFile:   outputFile,
			TargetRepo:   item.Repo,
			WorktreePath: workspaceRoot, // Working directory for Claude CLI (IPC mode)
			Runtime:      runtime,
			// The run identity this dispatch is booked under (ADR-017 step 0b).
			// Guaranteed non-empty: the run-identity preflight above refuses the
			// run rather than letting it reach a stage without one, so the
			// dispatch-boundary assertion in IpcStageRunner.RunStage is a
			// backstop, not a live branch.
			RunID: runtime.RunID,
			// Every stage the scheduler dispatches runs non-interactively, so
			// strip the tools that cannot work there (#79 moved this out of the
			// composer, which serves interactive callers too).
			AllowedTools:      skillrender.FilterHeadlessTools(skillData.AllowedTools),
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

		// adapterResolveErr was produced by the hoisted per-stage adapter
		// resolution above (#79) — the render needs the adapter, so the
		// re-point runs there and only its outcome is consumed here.
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
		inputTokens, outputTokens, cacheReadTokens, cacheCreationTokens := 0, 0, 0, 0
		var actualCostUsd float64
		if result != nil {
			exitCode = result.ExitCode
			inputTokens = result.InputTokens
			outputTokens = result.OutputTokens
			cacheReadTokens = result.CacheReadTokens
			cacheCreationTokens = result.CacheCreationTokens
			actualCostUsd = result.CostUsd
			// Capture last_output_lines so the V3 record's StageDetail carries
			// the trailing stderr/stdout snippet when this stage fails terminally.
			// Issue #3207 — IPC-mode stall-kill / cost-cap kills now propagate
			// the executor-captured tail through StageRunResult instead of being
			// silently discarded.
			if result.LastOutputLines != "" {
				runtime.RecordStageOutputTail(stage, result.LastOutputLines)
			}
			// Accumulate this stage's all-tools call log onto the run-level
			// ToolCalls slice so recordV2History can attach it to the
			// authoritative V2RunRecord (Issue #144).
			if len(result.ToolCalls) > 0 {
				runtime.RecordToolCalls(stage, result.ToolCalls)
			}
		}

		// #91 served-model attribution: the model that served the stage is not
		// guaranteed to be the one requested — the claude CLI can silently
		// retry a safety-refused turn on a fallback model
		// (model_refusal_fallback) and still exit 0, and a non-Claude adapter
		// translates the tier band into a concrete id before spawning. Cost,
		// exit-record, telemetry, and history sinks below use servedModel;
		// routing, escalation, and retry decisions stay on the requested
		// `model`.
		//
		// The comparison is sound only because `model` is what the executor
		// was actually asked to run (#340). While the IPC path re-resolved the
		// model in TypeScript, the executor measured divergence against its own
		// private resolution — so a Go-resolves-Y / TS-resolves-X /
		// CLI-serves-X stage reported no servedModel at all and this
		// correction could never fire.
		// See docs/spikes/fable-5-behavior-porting.md §8.3.
		servedModel := model
		if result != nil && result.ServedModel != "" {
			servedModel = result.ServedModel
		}
		// #580: record the CLI stream's raw ServedModel verbatim (possibly
		// empty), independent of the servedModel fallback above — this is the
		// honestly-unreported-when-empty concrete id, not the
		// request-or-served value servedModel computes.
		if result != nil {
			runtime.RecordStageServedModel(stage, result.ServedModel)
			// #606 served-envelope attribution, mirroring the servedModel flow
			// exactly: the raw executor report lands on the served_* fields,
			// and the requested-value fields are re-recorded onto the served
			// value below only when the two diverge — requested vs served stay
			// epistemically distinct end to end.
			runtime.RecordStageServedEffort(stage, result.ServedEffort)
			runtime.RecordStageServedThinking(stage, result.ServedThinking)
			if result.ServedEffort != "" && result.ServedEffort != effortAttr {
				runtime.RecordStageEffort(stage, result.ServedEffort)
			}
			if result.ServedThinking != "" && result.ServedThinking != thinkingAttr {
				runtime.RecordStageThinking(stage, result.ServedThinking)
			}
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
			runtime.CompleteStage(exitCode, tokens.TokenCounts{
				Input: inputTokens, Output: outputTokens, CacheRead: cacheReadTokens,
				// Unsplit cache-creation count booked as 5m per the
				// CalculateCost convention. Per-stage 5m/1h split is #390.
				CacheCreation5m: cacheCreationTokens,
			}, servedModel, adapterName)
		}
		s.emitStateChanged(item.Repo, item.Number, runtime)

		// Populate metadata after specific stages for Discord/UI enrichment
		switch stage {
		case state.StageIssuePickup:
			// Resolve worktree-first (#163): on a worktree-isolated run
			// issue-{N}.json is written inside the worktree, so the main-root
			// lookup this used to do returned "" and the run carried no branch at
			// all — which is what left the post-run cleanup and the fork
			// pre-flight with nothing to act on.
			if b := resolveFeatureBranch(runtime, workspaceRoot, item.Number); b != "" {
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

				// Unexercised deliverable (#152). The validate gate has just
				// re-derived the verdict and, if the run built a suite nothing
				// executed, rewritten the artifact. Read it back here rather
				// than plumbing a second return value through GateResult: the
				// artifact is the thing pr-create and every later consumer
				// read, so a card raised from anything else could disagree
				// with the PR body describing the same run.
				if stage == state.StageFeatureValidate && gateRes.Passed {
					if doc, derr := deliverable.ReadValidateContext(
						stageWorkspace(runtime, workspaceRoot), item.Number); derr == nil {
						if f := deliverable.FindingFromArtifact(doc); f.Detected() {
							log.Printf("#%d: %s", item.Number, f.Summary())
							s.raiseUnverifiedDeliverable(item.Repo, item.Number, runtime.RunID, f)
							for _, t := range f.Tiers {
								s.raiseUnverifiedDeliverableStreak(item.Repo, t, item.Number, runtime.RunID, f.TierReasons[t])
							}
						}
						// A tier that DID run this issue resets its streak,
						// independent of whether any tier was idle (#177) — the
						// reset is "this tier executed", not "nothing was
						// detected".
						exec := deliverable.ExecutionFromArtifact(doc)
						if exec.Unit {
							s.resolveUnverifiedDeliverableStreak(item.Repo, deliverable.TierUnit)
						}
						if exec.Integration {
							s.resolveUnverifiedDeliverableStreak(item.Repo, deliverable.TierIntegration)
						}
						if exec.E2E {
							s.resolveUnverifiedDeliverableStreak(item.Repo, deliverable.TierE2E)
						}
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
						// The IPC-delivered cache-creation count is unsplit; booked as
						// 5m per the CalculateCost convention. Per-stage 5m/1h split
						// is #390. Adapter-aware (#585): prices at the serving
						// provider's rates, not an anthropic default.
						anomalyCost, _ = tokens.CalculateCostForAdapter(adapterName, servedModel, tokens.TokenCounts{
							Input: inputTokens, Output: outputTokens, CacheRead: cacheReadTokens,
							CacheCreation5m: cacheCreationTokens,
						})
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
		//
		// reconciledNonTerminal records that THIS stage's failure was cleared by
		// the reconcile below (#299). Two things key off it, both at the end of
		// this iteration: the #2870 output-context check is skipped for the
		// reconciled stage, and the run then finishes rather than advancing.
		// Scoped to one loop iteration on purpose: it must never leak to the
		// next stage.
		//
		// reconciledArm carries the same fact PAST the loop (the completion block
		// needs it, #398), so it is declared above the loop — and cleared here,
		// each iteration, so it can only ever describe how the run ENDED. Today a
		// fired arm always breaks the loop in the same iteration, so the clear is
		// a no-op; it exists so a future `continue` inserted between the reconcile
		// and the break cannot leave a stale Done on a run that went on to finish
		// normally.
		reconciledNonTerminal := false
		reconciledArm = reconcileNone
		if (err != nil || exitCode != 0) && !isTerminalStage(stage) {
			// Resolve worktree-first (#299). The bare workspace-root lookup this
			// used to do answers "" on every worktree-isolated run, because
			// issue-{N}.json is written INSIDE the worktree — and with an empty
			// branch reconcileIssueResolved silently drops the branch-PR half of
			// the check, so a stage that died AFTER its PR merged was recorded
			// success:false and paged.
			branch := resolveFeatureBranch(runtime, workspaceRoot, item.Number)
			// The run's two IDENTITY facts, which are what let the PR probe tell
			// THIS run's open PR from a prior run's (#398): the PR number this
			// run's pr-create recorded in pr-{N}.json, and whether pr-create ran
			// at all. Both are content-free on purpose — a head-SHA or ancestry
			// comparison misclassifies in BOTH directions here (see
			// prOpenPROwnedByRun), because the rewind re-dispatch commits on the
			// branch and issue-pickup resets it to the pushed tip. Read from the
			// checkout the stages actually executed in, the same way the recovery
			// registry reads it (#275). A missing record answers 0, which the
			// probe's fail-closed rule handles.
			recordedPRNumber := loadPRNumberForRecovery(stageWorkspace(runtime, workspaceRoot), item.Number)
			runReachedPRCreate := hasReachedPRCreate(runtime)
			// issue-pickup is exempt: it is the stage that CREATES the branch
			// (SetBranch fires immediately after it, in the post-stage metadata
			// switch), so on a failed pickup no branch can exist by construction
			// and the line would fire on every one of them. The log must mean "a
			// branch should exist but nothing could name it" (#299).
			if branch == "" && stage != state.StageIssuePickup {
				// Nothing could name the branch, so only the issue-closed half of
				// the check can run. Pre-#299 that degradation was silent, which
				// is precisely why the worktree blindness survived: a check that
				// cannot name its subject must say so rather than quietly
				// answering "no". Deliberately does NOT claim which sources were
				// consulted — a runtime that died before the execution manager
				// stamped its worktree never had one to consult.
				log.Printf("#%d: non-terminal stage %s failed but no feature branch could be determined "+
					"from any source — skipping the PR-landed reconcile check; only the issue-closed "+
					"check runs (#299)",
					item.Number, stage)
			}
			// Bound the (up to two) sequential gh calls so a slow / rate-limited
			// GitHub never blocks the stage loop indefinitely. 15s matches the TS
			// notifier's execFile timeout and the gate's gh budget. On timeout the
			// helper's exec errors → fails closed (failure preserved).
			reconCtx, cancelRecon := context.WithTimeout(ctx, 15*time.Second)
			arm := reconcileIssueResolved(reconCtx, item, branch, recordedPRNumber, runReachedPRCreate)
			cancelRecon()
			if arm.reconciled() {
				// Name the evidence, not the category. The pre-#398 line said
				// "closed / branch PR landed" for every arm, so a reconcile driven
				// by an open PR read in the log exactly like one driven by a merge.
				log.Printf("#%d: non-terminal stage %s reported failure (exit=%d, err=%v) but %s — reconciling to success (#3873)",
					item.Number, stage, exitCode, err, arm.evidence())
				err = nil
				exitCode = 0
				reconciledNonTerminal = true
				reconciledArm = arm
				if s.telemetrySvc != nil && s.telemetryEnabled {
					s.telemetrySvc.EmitPipelineEvent(ctx, platform.PipelineEvent{
						RunID:       runtime.RunID,
						IssueNumber: item.Number,
						EventType:   "pipeline.failure_reconciled",
						Stage:       string(stage),
						Timestamp:   time.Now(),
						Metadata: map[string]interface{}{
							"reason": "non-terminal stage; issue resolved on forge",
							"arm":    arm.String(),
						},
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
		// Record the terminating stage's ground-truth token/cost data on the
		// failure path (Issue #146) — so BuildV2Record's failed-stage synthesis
		// branch can populate tokens.per_stage even when this stage never
		// reaches CompleteStage/CompleteStageWithCost above (e.g. a terminal
		// kill before the run loop returns here). Success already flows
		// through CompletedStages normally; recording it unconditionally would
		// let a stale synthesized entry mask a real per-stage bug there.
		if exitCode != 0 || err != nil {
			runtime.RecordTerminatingStageTokens(stage, inputTokens, outputTokens, cacheReadTokens, actualCostUsd)
		}

		prStateAtExit := detMergePRState
		s.writeStageExitRecord(item, stage, runtime, result, exitCode, err,
			actualCostUsd, servedModel, inputTokens, outputTokens, cacheReadTokens,
			stageStartedAt, workspaceRoot, prStateAtExit, SizeBucketForScore(complexityScore))

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
			// stageFailureText, not err.Error(): a CLI-mode failure arrives with
			// err == nil and the reason on the result, so gating on `err != nil`
			// left every CLI stage's trace exit with no terminal kind at all
			// (#533).
			if !exitPayload.Success {
				if failText := stageFailureText(err, result); failText != "" {
					exitPayload.TerminalKind = ResolveTerminalKind(gateRan, gateRes.TerminalKind, failText)
				}
			}
			if gateRan {
				exitPayload.GateKind = string(gateRes.Kind)
			}
			tracer.Emit(trace.KindStageExit, string(stage), exitPayload)
		}

		if s.onStageComplete != nil {
			stageCostForCb := actualCostUsd
			if stageCostForCb == 0 {
				// The IPC-delivered cache-creation count is unsplit; booked as 5m
				// per the CalculateCost convention. Per-stage 5m/1h split is #390.
				// Adapter-aware (#585): prices at the serving provider's rates,
				// not an anthropic default.
				stageCostForCb, _ = tokens.CalculateCostForAdapter(adapterName, servedModel, tokens.TokenCounts{
					Input: inputTokens, Output: outputTokens, CacheRead: cacheReadTokens,
					CacheCreation5m: cacheCreationTokens,
				})
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
					Reason:    state.EscalationReasonModelUnavailable,
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
			stallErrMsg := stageFailureText(err, result)
			isStallKill := ResolveTerminalKind(gateRan, gateRes.TerminalKind, stallErrMsg) == TerminalKindStallKill
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
					// stallErrMsg, not `%v` of err: in CLI mode err is nil, so
					// the old form persisted the literal "exit 1: <nil>" — the
					// exact string #533 exists to remove — on the one path this
					// change made reachable for CLI mode at all (pre-#533
					// stallErrMsg was always "", so isStallKill was false and
					// this branch never ran). It is also the text the sibling
					// telemetry call below already reports.
					runtime.SetStageError(stage, terminalFailureReason(exitCode, err, stallErrMsg))
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
				// stageFailureText, not err.Error(): CLI mode's failures arrive
				// with err == nil, so every recovery matcher saw an empty
				// StageError and TerminalKind="" — the auto-triage registry was
				// structurally blind on the whole CLI path (#533). Safe to widen
				// only because ErrorText is now the adapter's own stderr reason
				// and never the model-authored stdout transcript, which the
				// matchers would otherwise pattern-match against.
				stageErrText := stageFailureText(err, result)
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
					TerminalKind:   ResolveTerminalKind(gateRan, gateRes.TerminalKind, stageErrText),
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
					// runtime-{issue}-{runId}.json snapshot reflects it before the
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
					if result.FollowUp == recovery.FollowUpStageCanResume && result.BacktrackTargetStage != "" {
						// A deterministic action set BacktrackTargetStage directly on
						// the result (abandoned-commit-recoverable, #191) — resume at
						// that stage without going through the feedback-file signal
						// mechanism conflict-recovery-loop uses. This is a one-shot
						// rewind: the target stage's Matches() predicate excludes
						// itself, so the action cannot re-fire and loop, unlike the
						// conflict-recovery edge below which is a deliberately-repeated
						// bounded loop.
						targetStage := state.PipelineStage(result.BacktrackTargetStage)
						s.retryEngine.RecordBacktrack(string(stage), result.BacktrackTargetStage, result.Action)
						tracer.Emit(trace.KindBacktrack, string(stage), trace.BacktrackPayload{
							FromStage:   string(stage),
							TargetStage: result.BacktrackTargetStage,
							SignalType:  result.Action,
							Rationale:   result.Reason,
							Trigger:     "recovery_resume",
						})
						log.Printf("#%d: recovery %s — resuming %s → %s",
							item.Number, result.Action, stage, result.BacktrackTargetStage)
						err = nil
						exitCode = 0
						for i, st := range stages {
							if st == targetStage {
								stageIdx = i
								break
							}
						}
						continue // Re-run from the target stage
					}
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
					// abandoned-commit-recoverable (#191) matched AND found the
					// branch ahead-of-base with a clean tree — but could
					// neither self-heal via the deterministic runner nor set
					// up a backtrack (e.g. no live workspace to resume into).
					// Stamp the richer kind so triage sees "work exists on the
					// branch" instead of a generic crash. Guarded on the
					// ahead_of_base evidence marker so a bare Matches() hit
					// with nothing actually ahead of base (declined with no
					// evidence) doesn't misreport as this kind.
					if result.Action == "abandoned-commit-recoverable" && slices.Contains(result.Evidence, "ahead_of_base=true") {
						terminalFailureKind = TerminalKindAbandonedCommit
					}
				}
			}

			// Model rejected by the API (#42): substitute the next-best tier
			// instead of escalating upward — a stronger model on a plan that
			// already refused this one would be rejected the same way. The
			// substitution is sticky for the rest of the run; the retry below
			// re-dispatches this stage and the model resolution picks it up.
			failText := stageFailureText(err, result)
			resolvedFailureKind := ResolveTerminalKind(gateRan, gateRes.TerminalKind, failText)
			modelRejected := resolvedFailureKind == TerminalKindModelUnavailable
			// Auth-shaped failure (#591): the adapter CLI itself is not logged
			// in, or the pipeline-start auth gate refused to launch. No
			// downgrade/substitution path exists for this kind (unlike
			// modelRejected below) — see the escalation gate further down for
			// why it is excluded from escalation too.
			authFailed := resolvedFailureKind == TerminalKindAdapterAuthFailed
			if authFailed {
				terminalFailureKind = TerminalKindAdapterAuthFailed
			}
			if modelRejected {
				// The wire model is a band, and a band cannot name its
				// provider (#340) — so the ladder this rejection walks is
				// keyed on the provider the dispatch actually executed on
				// (#611). This arm is the Go-direct path only: an IPC
				// rejection is evaluated by IpcStageRunner and returns with
				// FallbackRecorded set, which `continue`s well above here. So
				// descentProvider is execMgr's adapter, and an xai dispatch
				// walks the xai ladder (grok-4.6's effort rungs) instead of
				// the anthropic one (#606, the #532 runtime resolution). It is
				// the SAME value the sticky-effort lookup above reads, so a
				// descent cannot be recorded under one provider and looked up
				// under another.
				if dg := s.retryEngine.EvaluateDowngradeForProvider(model, descentProvider); dg.ShouldDowngrade {
					s.retryEngine.RecordDowngrade(model, dg)
					runtime.AppendEscalation(state.EscalationRecord{
						Stage:     stage,
						FromModel: model,
						ToModel:   dg.NewTier,
						Reason:    state.EscalationReasonModelUnavailable,
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
			// Skipped on an auth-shaped failure for the same reason (#591):
			// escalation is for capability-shaped failures, and a stronger
			// model cannot fix a CLI that isn't logged in.
			//
			// #878 generalizes that last one past the adapter's own login.
			// authFailed above is keyed on TerminalKindAdapterAuthFailed — the
			// pipeline-start auth gate and the adapter CLI's "not signed in".
			// A CREDENTIAL failure inside the stage's own work (the observed
			// case: `git push` refused with `invalid auth method`) carries none
			// of those markers, so it read as a plain stage failure and bought
			// a second full dispatch at a higher tier. The category classifier
			// answers the general question — is this a permission failure? —
			// over everything we know about the failure, the captured output
			// tail included, because the cause is usually several lines above
			// whatever symptom ended the stage.
			catBlocked, catReason := EscalationBlockedByCategory(failText, runtime.StageOutputTail(stage))
			if catBlocked {
				log.Printf("#%d: stage %s failed — NOT escalating model: %s (no model can supply a missing credential)",
					item.Number, stage, catReason)
			}
			if !modelRejected && !authFailed && !catBlocked {
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

			terminalReason := terminalFailureReason(exitCode, err, failText)
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

		// The stage did NOT fail — drop any tail a superseded attempt left
		// behind (#533). Reaching here means one of three things, and all three
		// converge on this line: the stage exited clean, the #3835 terminal
		// reconcile cleared the failure, or the #3873 non-terminal reconcile
		// did. Without this, a stage that failed once and then succeeded on a
		// retry / recovery / reconcile is written to the V3 record as
		// `complete` while StageOutputTails still holds the FAILED attempt's
		// crash tail — a successful stage carrying someone else's evidence.
		//
		// Captured first (#878): the tail is the ONLY place a failure the stage
		// logged but did not exit on survives, and the post-condition check
		// immediately below reports a symptom whose cause lives there. Clearing
		// it before that check ran is what left the observed run attributing a
		// credential-less `git push` to "issue context file missing".
		outputTailBeforeClear := runtime.StageOutputTail(stage)
		runtime.ClearStageOutputTail(stage)

		// #2870: A stage exit code 0 doesn't guarantee the skill produced its
		// output context file. Verify the expected output exists; treat a
		// missing file as a stage failure so the actual offender is named
		// (vs blaming the next stage's prerequisite check), telemetry sees
		// the failure, and model escalation gets a chance to recover.
		//
		// Skipped for a stage whose failure the #3873 reconcile just cleared
		// (#299). That reconcile's premise is that the work ALREADY landed on
		// the forge, so there is no output left for this stage to have written —
		// the exit code is 0 only because the failure was reconciled away, not
		// because a skill ran and produced anything. Validating it here finds
		// the missing context and re-manufactures the very failure the reconcile
		// removed, terminating the run validation_error: the false-failure
		// signal survives with a different label. Deliberately scoped to that
		// one stage; every other stage in the run still gets the #2870 check.
		if outputErr := validateStageOutput(stage, stageWorkspace(runtime, workspaceRoot), item.Number); outputErr != nil && !reconciledNonTerminal {
			// FIRST CAUSE FIRST (#878, same defect class as #875). This check is
			// a post-condition: it can only ever report that the output is
			// absent, never why. When the stage's own output already named a
			// cause — a push that could not authenticate, a command that could
			// not reach the forge — that cause is the run's terminal reason and
			// the missing context is its consequence, not a second, competing
			// explanation. The observed run recorded "issue context file
			// missing" for a `git push` that had failed with `invalid auth
			// method` immediately above, sending whoever read the record at the
			// wrong problem.
			//
			// The symptom is RETAINED as trailing context rather than dropped:
			// "which post-condition tripped" is still the fastest way to see
			// where in the stage the run died.
			stageFailReason := outputErr.Error()
			if firstCause := firstCauseFromOutputTail(outputTailBeforeClear); firstCause != "" {
				stageFailReason = firstCause + " — then " + outputErr.Error()
				log.Printf("#%d: stage %s post-condition failed, but its first cause is upstream: %s",
					item.Number, stage, firstCause)
			}
			runtime.SetStageError(stage, stageFailReason)
			s.emitStateChanged(item.Repo, item.Number, runtime)
			log.Printf("#%d: %s", item.Number, stageFailReason)
			if s.telemetrySvc != nil && s.telemetryEnabled {
				s.telemetrySvc.EmitPipelineEvent(ctx, platform.PipelineEvent{
					RunID:       runtime.RunID,
					IssueNumber: item.Number,
					EventType:   "stage_error",
					Stage:       string(stage),
					Timestamp:   time.Now(),
					Metadata: map[string]interface{}{
						"error":     stageFailReason,
						"exit_code": 0,
						"model":     model,
						"reason":    "missing_output_context",
					},
					SchemaVersion: "1",
				})
			}
			// Stronger model may produce the missing context — try escalation
			// before giving up, mirroring the regular failure path. UNLESS the
			// stage's own output already named a permission failure (#878):
			// this site is a POST-CONDITION check, so it reports the symptom
			// ("no output context") for a cause that happened earlier and is
			// sitting in the tail. The observed run's cause was a
			// credential-less `git push`; escalating re-sent the whole prompt
			// at a higher tier to reach the identical failure.
			catBlocked, catReason := EscalationBlockedByCategory(outputTailBeforeClear, outputErr.Error())
			if catBlocked {
				log.Printf("#%d: stage %s missing output — NOT escalating model: %s (no model can supply a missing credential)",
					item.Number, stage, catReason)
			}
			escalation := s.retryEngine.EvaluateEscalation(string(stage), model)
			if !catBlocked && escalation.ShouldEscalate {
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
					if recErr := RecoverUncommittedWork(worktreePath, item.Number, string(stage)); recErr != nil {
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
			// The IPC-delivered cache-creation count is unsplit; booked as 5m per
			// the CalculateCost convention. Per-stage 5m/1h split is #390.
			// Adapter-aware (#585): prices at the serving provider's rates, not
			// an anthropic default.
			stageCost, _ = tokens.CalculateCostForAdapter(adapterName, model, tokens.TokenCounts{
				Input: inputTokens, Output: outputTokens, CacheRead: cacheReadTokens,
				CacheCreation5m: cacheCreationTokens,
			})
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

		// #299: a stage whose failure the #3873 reconcile cleared ends the run
		// here, and falling through to the completion block below is what makes
		// the written record a success. The reconcile's premise is that the
		// work ALREADY landed on the forge (PR merged or in review, or the issue
		// closed), so there is nothing left for the remaining stages to produce.
		// Continuing instead re-manufactures the failure that was just cleared:
		// the pre-flight death wrote no output context, so the NEXT stage's
		// prerequisite check fails on exactly that missing file and the run is
		// recorded failed anyway — a different terminal kind carrying the same
		// false-failure signal. Clearing err/exitCode without stopping only
		// moves where the phantom failure surfaces.
		if reconciledNonTerminal {
			log.Printf("#%d: stage %s was reconciled against the forge (#3873) — the issue's work has already "+
				"landed, so the run finishes here instead of re-running the remaining stages (#299)",
				item.Number, stage)
			break
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

	// Pipeline complete — update board. The terminal status is per-arm (#398):
	// only a run the #3873 reconcile ended against a CLOSED issue is Done, since
	// Done means "the issue is closed" everywhere else in the system. Every other
	// ending — including the MERGED-PR arm, which by construction runs only after
	// the issue answered NOT-closed — stays In Review, the pipeline's
	// long-standing terminal status for a run that leaves work on the forge.
	//
	// Logged unconditionally, before the nil check: the board write is optional
	// (headless / test runs have no board service) but the status the run
	// RESOLVED is the decision, and it must be visible either way. The write's
	// failure is not the decision, so it gets its own line rather than editing
	// this one.
	completionStatus := completionBoardStatus(reconciledArm)
	log.Printf("#%d: pipeline complete — resolved terminal board status %s (arm %s: %s)",
		item.Number, completionStatus, reconciledArm, reconciledArm.completionReason())
	if s.stateSvc != nil {
		// Never discard this error. The board is what an operator and every
		// dashboard read; a run that resolved Done/In Review and then failed to
		// write it looks identical to one that was never asked to.
		if completeErr := s.stateSvc.CompletePipeline(ctx, item.ID, completionStatus); completeErr != nil {
			log.Printf("#%d: board status %s NOT written: %v", item.Number, completionStatus, completeErr)
		}
	}

	// Mark pipeline as successful before defer fires.
	// Note: parent-epic auto-close already fired immediately after pr-merge
	// verification above, so we do not call checkEpicCompletion again here —
	// double-firing would be a harmless no-op but pollutes logs.
	pipelineSuccess = true
	// The named returns are settled by the publish defer above, which runs
	// after every other terminal defer.
	return
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
			// This branch never actually removed anything before #956. It was
			// guarded on item.NodeID, and BoardItem.NodeID is not populated on
			// the GitHub path -- nodeToItem sets only item.ID, and the board
			// query does not select the issue's own node id. So the log line
			// below printed on every scan and the mutation never ran (#955).
			//
			// The ref-based signature is what fixes it: the numbers and
			// repositories are all present on a board item, so no node ID is
			// needed. The log now follows the call and reports what happened,
			// because a line that announces a repair before attempting it is
			// how the original defect stayed invisible.
			if s.issueSvc != nil {
				itemOwner, itemRepo := s.linkRepoFor(item.Repo)
				blockerOwner, blockerRepo := s.linkRepoFor(blocker.Repo)
				if blockerOwner == "" || blockerRepo == "" {
					blockerOwner, blockerRepo = itemOwner, itemRepo
				}
				if removeErr := s.issueSvc.RemoveBlockedBy(ctx,
					forge.IssueRef{Owner: itemOwner, Repo: itemRepo, Number: item.Number},
					forge.IssueRef{Owner: blockerOwner, Repo: blockerRepo, Number: blocker.Number},
				); removeErr != nil {
					log.Printf("WARN: failed to remove circular blockedBy for #%d (blocked by parent epic #%d): %v",
						item.Number, blocker.Number, removeErr)
				} else {
					log.Printf("AUTO-FIX: removed circular blockedBy — #%d was blocked by its parent epic #%d",
						item.Number, blocker.Number)
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

// linkRepoFor resolves a BoardItem's "owner/repo" string into its parts,
// falling back to the scheduler's own owner when the item does not carry one.
// A board shared across repositories yields items whose Repo is set; a
// single-repo board may leave it empty, and an empty owner or repo would build
// a REST path like "/repos/owner//issues/12" -- a 404 that reads as "the issue
// is absent" rather than as the malformed path it is.
func (s *Scheduler) linkRepoFor(itemRepo string) (string, string) {
	owner, repo := splitOwnerRepo(itemRepo)
	if owner == "" || repo == "" {
		return s.owner, repo
	}
	return owner, repo
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
// sensible excerpt so runtime-{issue}-{runId}.json / the JSONL history stay lean and the
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
//
// predictedModel is the router's recommendation VERBATIM and is EMPTY when the
// context names none. It used to default to "sonnet" HERE, which put a
// fabricated prediction into the learning corpus on every unrouted run — and
// the corpus's readers cannot tell a fabricated prediction from a measured one,
// so it was counted as a routing measurement of nothing (#304).
//
// The default was not deleted, it MOVED: resolveDispatchModel applies
// defaultDispatchModel to the value it dispatches on, so the minimum-model
// floor, the sticky model-unavailable downgrade, per-stage model attribution
// and cost accounting all behave exactly as they did when the default lived
// here. What changed is only that the recording path sees the raw value. Do not
// reintroduce a default in this function: one caller wants "what did the router
// say" and the other wants "what will we run", and they are not the same
// question.
// SEARCHES EVERY WORKTREE LAYOUT, NOT JUST THE ROOT (#994). This used to read
// `<workspaceRoot>/.nightgauge/pipeline/issue-{N}.json` and nothing else, while
// the issue-pickup stage writes that file into the run's WORKTREE — so on the
// autonomous path it found nothing, every time, and every one of the corpus's
// rows carried complexityScore 0 and predictedModel "". The comment in
// recordOutcome had described this outcome exactly, as a known consequence,
// without it being read as a bug.
//
// repo and worktreeDir may be "" — the candidate list degrades to the old
// single-root behaviour, which is still correct for a run with no worktree.
func loadIssueContext(workspaceRoot, worktreeDir, repo string, issueNumber int) (complexityScore int, routingPath string, predictedModel string) {
	var data []byte
	var err error
	for _, path := range execution.IssueContextCandidates(workspaceRoot, worktreeDir, repo, issueNumber) {
		data, err = os.ReadFile(path)
		if err == nil {
			break
		}
	}
	if err != nil || len(data) == 0 {
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
	return ctx.Routing.ComplexityScore, ctx.Routing.Path,
		ctx.Routing.PickupRecommendation.DevModel
}

// dependabotFastTrackReason is the rationale recorded on every stage the
// fast-track removes. Named rather than inlined so the trace payload and the
// test assert the same string.
const dependabotFastTrackReason = "dependency updates are mechanical — planning and dev stages add no value"

// applyDependabotFastTrack is THE Dependabot routing seam: the predicate, the
// skip bookkeeping, the trace emission and the resulting stage order, in one
// function, because runPipeline calls exactly this and nothing else.
//
// The one-function shape is deliberate and is the fix for a specific review
// finding. When the predicate lived at the call site and only the pure stage
// helpers were extracted, a test could assert everything the helpers computed
// while the call site was free to consult a different predicate, drop
// runtime.SkipStage, or drop the tracer emission — all three mutations left the
// whole orchestrator package green. Splitting a decision from the bookkeeping
// that makes it count leaves the bookkeeping unobserved, so they are not split.
//
// rt.SkipStage is not optional decoration. A run reports success on
// `completed + skipped == STAGE_ORDER`, so a stage that is removed from the
// order without being marked skipped makes a COMPLETED fast-tracked run report
// FAILURE. The trace emission is the routing rationale (#179): it is how an
// operator reading a run's trace learns why planning and dev never ran.
func applyDependabotFastTrack(
	item types.BoardItem,
	full []state.PipelineStage,
	rt *state.RuntimeState,
	tracer *trace.Writer,
) []state.PipelineStage {
	if !isDependabotRemediationPR(item) {
		return full
	}
	kept, skipped := dependabotFastTrackRoute(full)
	log.Printf("#%d: Dependabot remediation PR detected — skipping %d stages (%s)",
		item.Number, len(skipped), joinStages(skipped))
	for _, st := range skipped {
		rt.SkipStage(st)
		tracer.Emit(trace.KindStageSkip, string(st), trace.StageSkipPayload{
			Source: "dependabot",
			Reason: dependabotFastTrackReason,
		})
	}
	return kept
}

// isDependabotRemediationPR reports whether a board item is the artifact
// Dependabot ACTUALLY produces: an open PULL REQUEST carrying Dependabot's own
// labels.
//
// The `item.IsPR` half is the entire point of #345 and is not a redundant
// belt-and-braces check. Dependabot does not open issues — it opens pull
// requests — so keying the fast-track on labels alone routed on a board item
// that only ever exists when a human hand-creates and hand-labels one. Both
// halves are required and neither is sufficient: a label-only test fires for a
// hand-made issue and never for the real artifact, and an IsPR-only test would
// fast-track every pull request on the board past planning and dev.
//
// This predicate decides a ROUTE, not a TRUST answer. The worst outcome of a
// wrong answer here is a pull request that runs the full pipeline, or one that
// skips planning and dev when it should not have. Whether a PR-shaped board
// item may be DISPATCHED at all is a separate, security-bearing question
// answered upstream in PickNext — see the note there.
func isDependabotRemediationPR(item types.BoardItem) bool {
	return item.IsPR && gh.IsDependabotIssue(item.Labels)
}

// dependabotFastTrackSkipSet is the EXACT set of stages the Dependabot
// fast-track removes from a run, and it is deliberately just these two.
//
// feature-validate, pr-create and pr-merge are absent because a dependency bump
// still has to compile, still has to pass CI, and still has to go through the
// same merge gate as everything else. Adding a stage here weakens the merge
// gate; #345's whole premise is that landing a bump LATE beats landing a broken
// one.
func dependabotFastTrackSkipSet() []state.PipelineStage {
	return []state.PipelineStage{state.StageFeaturePlanning, state.StageFeatureDev}
}

// dependabotFastTrackRoute partitions full into the stages a fast-tracked run
// RUNS and the stages it SKIPS, preserving order in both halves.
//
// It walks `full` once and routes each member to exactly one side, rather than
// filtering for `kept` and returning the skip set literal for `skipped`. The
// difference matters: the run reports success on
// `completed + skipped == STAGE_ORDER`, so `skipped` must name stages that were
// actually in the order — a literal would over-report a skip-set member that
// the order does not contain, and a stage in neither half makes a successful
// fast-tracked run report failure. Partitioning by construction makes both
// errors unrepresentable.
func dependabotFastTrackRoute(full []state.PipelineStage) (kept, skipped []state.PipelineStage) {
	skip := make(map[state.PipelineStage]bool, 2)
	for _, st := range dependabotFastTrackSkipSet() {
		skip[st] = true
	}
	kept = make([]state.PipelineStage, 0, len(full))
	for _, st := range full {
		if skip[st] {
			skipped = append(skipped, st)
			continue
		}
		kept = append(kept, st)
	}
	return kept, skipped
}

// joinStages renders a stage list for a log line.
func joinStages(stages []state.PipelineStage) string {
	names := make([]string, len(stages))
	for i, st := range stages {
		names[i] = string(st)
	}
	return strings.Join(names, ", ")
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
//
// Delegates to internal/ci so the unexercised-deliverable check (#152) and this
// gate resolve the same base. They used to be two hand-written copies of the
// same git invocation, which is how they would have drifted.
func changedFilesAgainstBase(workspaceRoot string) []string {
	return ci.ChangedFilesAgainstDefaultBase(workspaceRoot)
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

// branchForkPreflightApplies reports whether the branch-fork pre-flight (#163)
// gates a stage.
//
// It gates the stages whose work is only useful if the local tip can eventually
// be pushed — planning, dev, validate, pr-create — which is where the entire
// wasted spend lives. It deliberately does NOT gate:
//
//   - issue-pickup, which is the stage that creates the branch. There is
//     nothing to compare yet.
//   - pr-merge, which merges server-side and does not depend on the local tip.
//     A remote branch that legitimately moved ahead (GitHub's "Update branch",
//     a merge queue) would otherwise block a merge that was never at risk; the
//     branch-out-of-date recovery already owns that case.
//   - spike-materialize, which reads a merged artifact and pushes nothing.
func branchForkPreflightApplies(stage state.PipelineStage) bool {
	switch stage {
	case state.StageFeaturePlanning,
		state.StageFeatureDev,
		state.StageFeatureValidate,
		state.StagePRCreate:
		return true
	}
	return false
}

// resolveFeatureBranch resolves the run's feature branch from the most
// authoritative source that has it: the live runtime (populated at issue-pickup
// via SetBranch), then the issue context in the worktree the stages actually
// executed in, then the main workspace root.
//
// Pre-#163 the post-run branch cleanup consulted only the last of the three. On
// a worktree-isolated run issue-{N}.json is written INSIDE the worktree, so the
// lookup returned "" and the cleanup silently no-opped — which is how a killed
// stage's pushed commit survived to fork the branch on the next attempt. A
// cleanup that cannot name its branch does nothing and says nothing, so the
// gap was invisible until the fork it caused was diagnosed two attempts later.
//
// #299 finished the conversion: this is now the ONLY caller of
// loadFeatureBranch. Until #299, two sites read the bare workspace root — the
// #3873 non-terminal reconcile (which then skipped the branch-PR probe and
// paged on an issue whose PR had merged) and recordV2History (which persisted
// no branch on every worktree-isolated run). Call this, never loadFeatureBranch
// directly: the bare lookup is correct only for in-place runs, and nothing at a
// call site distinguishes the two.
func resolveFeatureBranch(runtime *state.RuntimeState, workspaceRoot string, issueNumber int) string {
	if runtime != nil {
		if b := runtime.FeatureBranch(); b != "" {
			return b
		}
	}
	if ws := stageWorkspace(runtime, workspaceRoot); ws != workspaceRoot {
		if b := loadFeatureBranch(ws, issueNumber); b != "" {
			return b
		}
	}
	return loadFeatureBranch(workspaceRoot, issueNumber)
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
// resolveIssueContextPath returns the first candidate issue-context path that
// EXISTS, or "" when none does.
//
// Separate from loadIssueContext because two callers need the PATH rather than
// the contents: shouldReRoute stats it, and reRouteContext rewrites it. Both
// used to hardcode `<workspaceRoot>/.nightgauge/pipeline/issue-{N}.json`, which
// made shouldReRoute return false on exactly the worktree-isolated runs whose
// prediction it was supposed to repair — and would have made reRouteContext
// write a SECOND context file at the root, diverging from the one the stages
// actually read (#994).
func resolveIssueContextPath(workspaceRoot, worktreeDir, repo string, issueNumber int) string {
	for _, path := range execution.IssueContextCandidates(workspaceRoot, worktreeDir, repo, issueNumber) {
		if _, err := os.Stat(path); err == nil {
			return path
		}
	}
	return ""
}

func (s *Scheduler) shouldReRoute(workspaceRoot, worktreeDir, repo string, issueNumber int) bool {
	perfModePath := filepath.Join(workspaceRoot, ".nightgauge", "performance-mode.yaml")
	perfModeInfo, err := os.Stat(perfModePath)
	if err != nil {
		return false
	}
	contextPath := resolveIssueContextPath(workspaceRoot, worktreeDir, repo, issueNumber)
	if contextPath == "" {
		return false
	}
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
func (s *Scheduler) reRouteContext(ctx context.Context, workspaceRoot, worktreeDir, repo string, issueNumber int, oldModel string) (routing.Recommendation, error) {
	// Rewrite the file the stages actually read. Writing to the workspace root
	// unconditionally would leave the real context — in the worktree —
	// untouched while creating a decoy beside it (#994).
	contextPath := resolveIssueContextPath(workspaceRoot, worktreeDir, repo, issueNumber)
	if contextPath == "" {
		contextPath = filepath.Join(workspaceRoot, ".nightgauge", "pipeline",
			fmt.Sprintf("issue-%d.json", issueNumber))
	}

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

// recordOutcome builds a learning.Outcome from the pipeline result, records it
// under the run's TARGET repo root, and returns the predicted-vs-actual routing
// decisions for the run record.
//
// runRoot is the same root recordV2History writes the run record to (#215/#232:
// a run's persisted state lands in its target repo, never the daemon's launch
// root). The corpus follows the record — one run, one repo, one pair of files —
// so `nightgauge intelligence loop-verdicts --workdir <repo>` and `nightgauge
// learn tune --workdir <repo>` see the outcomes for exactly the history they
// are reading beside.
// resolveOutcomePrediction returns the complexity score and predicted model to
// record, preferring a freshly-read issue context over the caller's pickup-time
// values.
//
// Separate and named so it can be driven in a test. Inlined at the call site it
// was unfalsifiable — the first version of this fix was inlined, and deleting
// it left the entire suite green (#994).
//
// Each half is upgraded independently: a context that carries a model but no
// score must not discard a score the caller already had, and vice versa.
func (s *Scheduler) resolveOutcomePrediction(runRoot, worktreeDir, repo string, issueNumber, pickupScore int, pickupModel string) (int, string) {
	score, model := pickupScore, pickupModel
	freshScore, _, freshModel := loadIssueContext(runRoot, worktreeDir, repo, issueNumber)
	if freshScore > 0 {
		score = freshScore
	}
	if freshModel != "" {
		model = freshModel
	}
	return score, model
}

func (s *Scheduler) recordOutcome(item types.BoardItem, snap *state.RuntimeState, success bool, complexityScore int, predictedModel, runRoot string) *state.OutcomePrediction {
	if !s.recordOutcomes || runRoot == "" {
		return nil
	}
	var failedStage string
	if !success {
		failedStage = string(snap.Stage)
	}

	// RE-RESOLVE THE PREDICTION HERE, not at pickup (#994).
	//
	// The caller's values were read before the stage loop — therefore before
	// issue-pickup wrote the context, and before the worktree it writes into
	// existed. That is why every row in the corpus carried complexityScore 0
	// and predictedModel "" for the life of the product, while the real
	// issue-{N}.json files in the field carry both.
	//
	// This is the recording boundary, and it is the first moment at which the
	// file is guaranteed to exist and its location is known. Only ever an
	// upgrade: a re-read that finds nothing keeps whatever the caller had, so a
	// run whose context genuinely never appeared is no worse off.
	complexityScore, predictedModel = s.resolveOutcomePrediction(
		runRoot, snap.WorktreeDir, item.Repo, item.Number, complexityScore, predictedModel)
	// Predicted vs actual, in the vocabulary the corpus's OTHER writer uses —
	// see internal/orchestrator/outcome_semantics.go for the three rules both
	// paths obey. The helpers are shared precisely so they cannot drift: this
	// writer used to record actualModel := predictedModel (a copy — a
	// guaranteed match on every row, in raw model-id vocabulary) against an IPC
	// writer that recorded a normalized measurement, so one corpus field
	// carried two meanings with no discriminator (#304 round 2).
	//
	// ActualModel is what the IMPLEMENTATION stage actually served, because
	// PredictedModel is the router's recommendation FOR that stage. Empty when
	// it never ran — an absent value is excluded from the accuracy denominator,
	// a copied one is counted as a measurement of nothing.
	//
	// ActualSize is deliberately unset: no lines-changed measurement exists at
	// this boundary, and the size:* label it used to be derived from is one of
	// the same pre-run inputs the prediction came from.
	outcome := learning.Outcome{
		IssueNumber:     item.Number,
		Repo:            item.Repo,
		PredictedSize:   OutcomePredictedSize(string(item.Size), item.Labels, complexityScore),
		PredictedModel:  OutcomeModelBand(predictedModel),
		ActualModel:     OutcomeActualBand(OutcomeServedDevModel(snap), predictedModel),
		Success:         success,
		DurationMs:      snap.TotalDuration().Milliseconds(),
		InputTokens:     snap.InputTokens,
		OutputTokens:    snap.OutputTokens,
		CostUSD:         snap.TotalCostUSD,
		ComplexityScore: complexityScore,
		FailedStage:     failedStage,
		CompletedAt:     time.Now(),
	}
	// Loud on every unattributed field, matching the extension writer
	// (internal/ipc/server.go). An empty value is recoverable — every consumer
	// excludes a pair with an empty half rather than booking it as a miss — but
	// only if somebody knows it happened: the pre-#304 corpus was 100%
	// model-less for the life of the product and nobody noticed. On this
	// machine's canonical roots the autonomous writer is the blind one:
	// loadIssueContext reads <runRoot>/.nightgauge/pipeline/issue-{N}.json and
	// stages write that file into the run's WORKTREE, so an autonomous row can
	// carry score 0 and no prediction at all.
	//
	// WHICH cause fired is the whole point, and there are three (see
	// OutcomePredictedModelDiagnostic): absent, or present-but-unregistered on
	// either half. The sentences live in outcome_semantics.go beside the rule
	// that produces the empty band, so this writer and the extension's cannot
	// name different causes for one corpus field.
	if outcome.PredictedModel == "" {
		log.Printf("#%d: %s", item.Number,
			OutcomePredictedModelDiagnostic(item.Number, predictedModel))
	}
	if outcome.ActualModel == "" {
		log.Printf("#%d: %s", item.Number,
			OutcomeActualModelDiagnostic(OutcomeServedDevModel(snap)))
	}
	if complexityScore <= 0 {
		log.Printf("#%d: learning outcome has no routing.complexity_score — no issue context reached this handler, so the run records no size prediction at all (#304)",
			item.Number)
	}
	if err := learning.NewRecorder(runRoot).Record(outcome); err != nil {
		log.Printf("#%d: failed to record outcome: %v", item.Number, err)
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
	// pipeline.logs.history_retention_days drives the prune pass
	// appendAndIndex runs on every write below (#674).
	if cfg, cfgErr := config.Load(workspaceRoot); cfgErr == nil && cfg != nil {
		hw.SetRetentionDays(cfg.Pipeline.ResolveHistoryRetentionDays())
	}
	// Resolve worktree-first (#299): snap carries the run's WorktreeDir, so this
	// consults the live runtime and the worktree the stages ran in before the
	// workspace root. The bare root lookup answered "" for every
	// worktree-isolated run, and BuildV2Record then substituted a synthetic
	// `feat/{N}` — a value no reader could tell apart from a real branch. #397
	// deleted that substitution; BuildV2Record now records what resolved, or
	// nothing.
	branch := resolveFeatureBranch(snap, workspaceRoot, item.Number)
	if branch == "" {
		// The record will carry `"branch": ""` — key present, value empty, which
		// is the record's own way of saying "undetermined" (#397). Announcing it
		// here is still worth a line: this is the only place that knows WHICH run
		// failed to resolve, and a run that reaches history without a branch is a
		// resolution gap worth seeing in the log rather than only on disk.
		log.Printf("#%d: no feature branch could be determined from any source — the history record "+
			"will carry an EMPTY branch, which is how a record says \"undetermined\"; nothing is "+
			"fabricated in its place (#299, #397)", item.Number)
	}

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
		ToolCalls:              snap.ToolCalls,
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

// SizeBucketForScore maps a complexity score to the learning corpus's size
// vocabulary (small|medium|large). This is the ONLY vocabulary
// learning.Outcome.PredictedSize / ActualSize are ever written in — the
// calibration loop compares the two for equality, so a second vocabulary in
// the same field can only ever report 0% accuracy (#304).
//
// It is a pure bucketing of whatever score it is handed, INCLUDING 0 (→
// "small") — so corpus writers must not call it directly. They call
// OutcomePredictedSize, which decides whether the run had a size to predict at
// all before bucketing; diagnostic callers (the stage-exit record) may bucket
// unconditionally because nothing compares that value to a measurement.
func SizeBucketForScore(score int) string {
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
//
// #582 keep-with-reason (band-retirement sweep, PR #607): the substring match
// survives deliberately. The input is the DISPATCH model — bands plus claude-*
// ids on this path — and the substring covers every Haiku spelling that
// vocabulary has ever used ("haiku", "claude-haiku-4-5", the older dated
// "claude-3-5-haiku-*" forms an operator env override can still pin), which
// `models.ClaudeIDTier`'s prefix classifier does not. A single-band substring
// is not a closed-set enumeration (gate-clean by design); rewriting it to a
// registry resolution would change behavior for the legacy-id population,
// outside this sweep's byte-identical boundary.
func isHaikuModel(model string) bool {
	return strings.Contains(model, "haiku")
}

// defaultDispatchModel is the tier an autonomous stage dispatches on when the
// router recommended no model for it — the general-purpose default, spelled as
// a registry BAND so the floor, the downgrade ladder and the escalation ladder
// all recognize it. Applied in exactly one place (resolveDispatchModel), and
// never on the corpus-recording path: what the pipeline chose in the absence of
// a recommendation is a dispatch decision, not a prediction to score (#304).
//
// ONE rule for every unresolved model, deliberately. Before #304 the default
// was applied inside loadIssueContext, so it covered a context file that named
// no dev_model but NOT a context file that was missing or unparseable — that
// rarer population reached dispatch as "" and silently lost the floor, the
// sticky downgrade, per-stage attribution and cost accounting. Both populations
// now resolve here. That is a deliberate behavior change for the missing-file
// case; a stage the operator floored to a premium tier should not run the
// provider default, with no log line, because a context file failed to parse.
const defaultDispatchModel = tierSonnet

// tierSonnet is the sonnet registry BAND, spelled out rather than reached for
// as routing.ModelSonnet (#340). The three stage-specific haiku exclusions
// below all escalate to sonnet, and until #340 they wrote the concrete id — so
// resolveDispatchModel emitted two vocabularies on one field. Both this
// package's ladders and the extension's band-keyed lookups fail SILENTLY on a
// concrete id, which is why the ONE vocabulary is spelled the same way
// everywhere it is produced.
const tierSonnet = "sonnet"

// resolveDispatchModel returns the model a stage will actually dispatch on,
// after every override the run can apply: the per-stage base routing, then
// escalation, the configured minimum floor, sticky model-unavailable
// downgrades, and the three stage-specific haiku exclusions.
//
// Extracted from the dispatch path in #79 so the result is available BEFORE
// the skill is composed. Overlay keys are derived from this value (ADR 016 §2)
// — resolving it after the render would have keyed the cascade off the
// pre-escalation tier, so a stage escalated from haiku to sonnet would have run
// sonnet against haiku's overlays. The ordering INSIDE this function is
// load-bearing and unchanged from the inline version: the floor lands before
// ApplyDowngrades so a model-unavailable downgrade stays the final safety net
// (a floor must never force a run back onto a tier the API just rejected).
//
// This is the ONLY router on both dispatch paths since #340. Everything the
// TypeScript resolveModel chain used to contribute on the IPC path — the
// performance-mode pin AND envelope, pipeline.stage_models and its env
// overrides, model_routing.mode, the lightweight-stage defaults — is applied by
// stageBaseModel (dispatch_routing.go) before the first line below. The mode's
// ceiling is applied again after the raising mechanisms, where this function
// (not stageBaseModel) is the one that knows what they produced.
//
// The RETURN VALUE is always a registry band when the registry recognizes it
// (normalizeDispatchTier, last line). The wire, both executors and every ladder
// in this package speak that one vocabulary; a concrete id reaching a band-keyed
// consumer fails silently rather than loudly.
func (s *Scheduler) resolveDispatchModel(
	stage state.PipelineStage,
	issueNumber int,
	workspaceRoot string,
	predictedModel string,
	modelFloors map[string]string,
	jobClass string,
) string {
	// Per-stage base routing. An unrouted run still ends on the general-purpose
	// default tier, and that default lives in stageBaseModel's last branch and
	// nowhere else (#304): it used to live in loadIssueContext, where the same
	// value also reached the learning corpus and was recorded as a routing
	// prediction the router never made. Deleting it there was right; deleting
	// it outright was not, because four mechanisms below key on tier
	// RECOGNITION and silently no-op on "" — the model_routing.minimum_model
	// floor (#366) returns the selection untouched, the sticky
	// model-unavailable downgrade (#42) reports model_not_in_registry,
	// RecordStageModel drops the empty value so the run record carries no
	// per-stage attribution, and tokens.CalculateCost("") returns a
	// truthful-looking $0.
	mode := routing.ResolvePerformanceMode(workspaceRoot)
	baseModel, explicitBase := stageBaseModel(workspaceRoot, mode, stage, predictedModel, jobClass)
	model := baseModel
	// Escalation override if set, otherwise the base.
	if override := s.retryEngine.CurrentModel(string(stage)); override != "" {
		model = override
	}
	// Per-stage model_routing.minimum_model floor (#366) so an autonomous run
	// honors a configured minimum tier — parity with the TS SkillRunner's
	// enforceMinimumModel. Before RecordStageModel too, so attribution
	// reflects the floored tier.
	if floor := stageModelFloor(modelFloors, string(stage)); floor != "" {
		if raised := enforceMinimumModel(model, floor); raised != model {
			log.Printf("#%d: stage %s — model_routing.minimum_model floor %q raised %s → %s",
				issueNumber, stage, floor, model, raised)
			model = raised
		}
	}
	// The performance mode's CEILING binds everything the pipeline chose (#340,
	// #19). Escalation and the minimum_model floor both only RAISE, and TS
	// re-clamps its own enforceMinimumModel result to the envelope ceiling for
	// the same reason: a cost-capping mode that any later mechanism can raise
	// out of caps nothing. So Efficiency tops out at sonnet even after a failed
	// stage escalates, and an operator forcing a tier through
	// run.retryWithEscalation gets it within the band they selected.
	//
	// The clamp is applied to what the RAISING MECHANISMS PRODUCED, never
	// skipped on the provenance of the base they raised. Gating it on
	// `explicitBase` was the round-2 defect: under `model_routing.mode: manual`
	// — which every recommended docs/CONFIGURATION.md profile sets, and where
	// stageConfiguredModel answers for EVERY stage from defaultStageModels —
	// the flag was true on every stage, so the ceiling never bound escalation,
	// the floor or the forced tier for the operators most likely to have chosen
	// a cost-capping mode.
	//
	// Two rules, and only these:
	//   - An explicit per-stage model (the env override, pipeline.stage_models,
	//     the manual-mode table) is the operator overriding the MODE for that
	//     stage. resolveModel Step 1 returns it unclamped; so does this. That
	//     exemption covers the operator's OWN value — it is the strongest tier
	//     this stage can end on when nothing raised it, and a floor may never
	//     LOWER a dispatch below it (forcing a tier must never downgrade).
	//   - The floor half of the envelope is not applied here. It would re-raise
	//     a tier the sticky #42 downgrade just lowered — the ONE thing the
	//     ordering below exists to prevent.
	//
	// The envelope is the stage's ROUTED-TIER envelope, not the raw mode band:
	// a `fable` ceiling belongs only to the heavy reasoning stages, so a
	// run-wide floor (or a forced tier) cannot put feature-validate or the
	// plumbing on Fable under `frontier` — the exact behavior #19 deleted for
	// having "empirically failed validation in dogfooding". stageBaseModel
	// clamps its own branches against the same narrowed envelope.
	envelope := routing.RoutedTierEnvelope(mode, string(stage))
	if capped := routing.ClampToCeiling(model, envelope); capped != model {
		if explicitBase && routing.TierRank(capped) < routing.TierRank(baseModel) {
			// The ceiling landed below the operator's own per-stage model.
			// Keep theirs: the raise is discarded, not the override.
			capped = baseModel
		}
		if capped != model {
			log.Printf("#%d: stage %s — performance mode %s caps %s → %s",
				issueNumber, stage, mode, model, capped)
			model = capped
		}
	}
	// Reroute through any sticky model-unavailable downgrades (#42): once the
	// API rejected a tier this run, every later stage resolving to it runs on
	// the substituted tier instead of re-failing identically.
	model = s.retryEngine.ApplyDowngrades(model)

	// For pr-create, escalate from haiku to sonnet when the diff is large.
	// Large diffs cause haiku to stall before producing a complete PR.
	//
	// Only over a base the PIPELINE chose. In `resolveModel` this rule is
	// structural rather than stated: the escalation lives inside Step 1.5, the
	// lightweight-stage branch, which Step 1 returns before ever reaching
	// whenever `getStageModel` answers. Evaluating it here on the resolved
	// model regardless of provenance made it fire over an explicit
	// `pipeline.stage_models` entry, a `NIGHTGAUGE_PIPELINE_STAGE_MODEL_*`
	// override, and the whole `model_routing.mode: manual` table — where
	// defaultStageModels[pr-create] is haiku and all three recommended
	// docs/CONFIGURATION.md profiles live. One workspace and one 900-line diff
	// then ran pr-create on sonnet autonomously and haiku from the extension.
	//
	// So: explicit operator configuration wins, on both resolvers. An operator
	// who names haiku for pr-create gets haiku; the escalation exists to keep
	// the PIPELINE's own cheap default from stalling, not to overrule a choice.
	if stage == state.StagePRCreate && !explicitBase && isHaikuModel(model) {
		threshold := getLargeDiffThreshold(workspaceRoot)
		if threshold > 0 {
			if diffLines := getDiffLineCount(workspaceRoot); diffLines > threshold {
				log.Printf("#%d: pr-create diff is %d lines (threshold %d) — escalating to sonnet",
					issueNumber, diffLines, threshold)
				model = tierSonnet
			}
		}
	}

	// For feature-validate, disable haiku auto-routing unless the dev-stage
	// build verification already passed. Haiku is too lightweight to reliably
	// run real build/test commands without shortcutting them (Issue #3041).
	if stage == state.StageFeatureValidate && isHaikuModel(model) {
		if !devContextBuildPassed(workspaceRoot, issueNumber) {
			log.Printf("#%d: feature-validate: dev build_verification not passed — disabling haiku, escalating to sonnet",
				issueNumber)
			model = tierSonnet
		}
	}

	// pr-merge's LLM path only runs when the deterministic runner punted —
	// exclusively the judgment-heavy instances (blocked merge state, failing
	// checks, dirty state). Issue size does not predict punt difficulty, so
	// haiku is never the right tier here regardless of what config/calibration
	// resolved (#197 — a haiku pr-merge improvised an admin
	// bypass). Floor: sonnet.
	if stage == state.StagePRMerge && isHaikuModel(model) {
		log.Printf("#%d: pr-merge LLM path runs only on deterministic punts — flooring haiku to sonnet (#197)",
			issueNumber)
		model = tierSonnet
	}

	return normalizeDispatchTier(model)
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

// cleanupMergedRemoteBranch deletes a just-merged PR's remote head branch
// after the deterministic pr-merge path (Issue #589). The deterministic
// runner's Merge call no longer passes `gh pr merge --delete-branch` — that
// flag shells `git checkout <base>` locally, which fails from a linked
// worktree because the primary checkout already holds it (the punt this
// issue fixes). Cleanup moves here instead, through the common-dir-aware git
// service's BranchDeleteRemote: a push-based ref deletion that needs no
// checkout at all, so it works unchanged from a linked worktree.
//
// Best-effort and non-blocking, matching every other post-merge branch
// cleanup in this codebase (e.g. epic.go's BranchCleanup calls): the merge
// itself is already confirmed by the time this runs, and a repo with
// delete_branch_on_merge enabled (the onboarding-recommended default, see
// `nightgauge repo enable-delete-branch`) will usually have deleted the
// branch server-side already, so "already gone" is an expected, ignorable
// outcome here — never a reason to fail a stage whose merge already landed.
func (s *Scheduler) cleanupMergedRemoteBranch(issueNumber int, workdir, headRefName string) {
	if headRefName == "" {
		return
	}
	gitSvc, err := git.NewService(workdir)
	if err != nil {
		log.Printf("#%d: pr-merge deterministic path: remote branch cleanup for %s skipped — git service unavailable: %v",
			issueNumber, headRefName, err)
		return
	}
	if err := gitSvc.BranchDeleteRemote(headRefName); err != nil {
		log.Printf("#%d: pr-merge deterministic path: remote branch cleanup for %s failed (likely already deleted server-side): %v",
			issueNumber, headRefName, err)
		return
	}
	log.Printf("#%d: pr-merge deterministic path: deleted remote branch %s after merge", issueNumber, headRefName)
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
	//
	// ROOTED AT THE RUN'S TARGET REPO (#441, over #229/#410), the same
	// s.runRoot(item.Repo) that runPipeline binds to its local `workspaceRoot`
	// and that every other Persist in this file writes through — this is a
	// runtime-{issue}-{runId}.json write, so it belongs beside the run's other
	// snapshots and nowhere else. It used to go to s.workspaceRoot, the
	// scheduler's LAUNCH root, which filed the breadcrumb under the wrong repo
	// identity on a cross-repo run: since #410 each repo root's own
	// .nightgauge/pipeline/ IS that root's in-flight source for
	// state.ActiveIssuesFromSnapshots, so the launch repo's scan saw a phantom
	// issue while the target repo's scan missed a real one. Contrast the
	// survival record below, which is deliberately launch-rooted.
	//
	// The non-empty guard is on the RESOLVED run root, not on s.workspaceRoot:
	// runRoot falls back to the execution manager's root for an unknown or empty
	// repo, and an empty root would make filepath.Join yield the cwd-relative
	// ".nightgauge/pipeline" — writing a run's state into whatever directory the
	// process happens to be in. Skip the breadcrumb instead.
	if pmResult.MergedCommitSha != "" || pmResult.MergedAt != "" {
		runtime.SetMergeOutcome(pmResult.MergedCommitSha, pmResult.MergedAt)
		if runRoot := s.runRoot(item.Repo); runRoot != "" {
			if persistErr := runtime.Persist(filepath.Join(runRoot, ".nightgauge", "pipeline")); persistErr != nil {
				log.Printf("#%d: warning: failed to persist merge breadcrumb: %v", item.Number, persistErr)
			}
		}
	}

	// (#4151) Seed a pending post-merge survival record for eligible single-issue
	// merges. Best-effort and strictly non-blocking — the merge has already
	// landed; a store failure is logged, never fatal. The reconcile sweep later
	// finalizes the record (survived / reverted / broke / unobserved).
	//
	// LAUNCH-ROOTED ON PURPOSE — this is NOT the breadcrumb's bug repeated (#441
	// adjudication). The survival journal is a single append-only file at
	// <root>/survival.StoreRelPath whose records are self-describing: each one
	// carries its own Repo + Number (survival.NewPending(item.Repo, …)) and the
	// detector resolves the repo to query from rec.Repo, never from the store's
	// root. Both readers are launch-root global and there is no per-repo scan to
	// pair with — sweepSurvivalRecords does survival.NewStore(as.workspaceRoot)
	// and feeds gh.NewOutcomeService(as.workspaceRoot) — so run-rooting only the
	// WRITER would make a cross-repo run's record unreadable by the sweep and it
	// would age out to "unobserved". The breadcrumb is the opposite shape: a
	// per-run snapshot file whose reader scans one repo root at a time and
	// infers the owning repo from WHERE the file is.
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
		s.cleanupMergedRemoteBranch(item.Number, stageWS, detResult.HeadRefName)
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
	if IsBranchProtectionPunt(detResult.Reason) {
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
	// `missing-dev-context` on EVERY worktree-mode run (the dogfood workspace was 0-for-N),
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
