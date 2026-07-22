package ipc

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"runtime/debug"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	platformapi "github.com/nightgauge/nightgauge/api/generated/go/platform"
	"github.com/nightgauge/nightgauge/internal/attention"
	"github.com/nightgauge/nightgauge/internal/config"
	"github.com/nightgauge/nightgauge/internal/execution"
	"github.com/nightgauge/nightgauge/internal/executor"
	"github.com/nightgauge/nightgauge/internal/focus"
	gitops "github.com/nightgauge/nightgauge/internal/git"
	gh "github.com/nightgauge/nightgauge/internal/github"
	"github.com/nightgauge/nightgauge/internal/intelligence/complexity"
	"github.com/nightgauge/nightgauge/internal/intelligence/failure"
	"github.com/nightgauge/nightgauge/internal/intelligence/health"
	"github.com/nightgauge/nightgauge/internal/intelligence/routing"
	"github.com/nightgauge/nightgauge/internal/intelligence/tokens"
	knowledgepkg "github.com/nightgauge/nightgauge/internal/knowledge"
	"github.com/nightgauge/nightgauge/internal/knowledge/metrics"
	"github.com/nightgauge/nightgauge/internal/knowledge/recall"
	"github.com/nightgauge/nightgauge/internal/orchestrator"
	"github.com/nightgauge/nightgauge/internal/platform"
	"github.com/nightgauge/nightgauge/internal/state"
	"github.com/nightgauge/nightgauge/pkg/types"
)

// errSchedulerNotConfigured is returned for every scheduler-backed IPC call when
// the serve daemon never attached a pipeline scheduler. It names the missing
// config and the fix so a multi-repo-workspace operator (no root config.yaml)
// is not left guessing — the bare "scheduler not configured" gave no signal
// that the workspace simply lacked an owner + project.number. See #3860.
const errSchedulerNotConfigured = "scheduler not configured — no workspace-root .nightgauge/config.yaml (owner + project.number) and the workspace manifest did not yield one; run `nightgauge workspace-init` or add a root config.yaml"

// Server handles JSON-over-stdio IPC communication with VSCode.
type Server struct {
	client            *gh.Client
	writer            io.Writer
	mu                sync.Mutex
	methods           map[string]Handler
	execMgr           *execution.Manager
	scheduler         *orchestrator.Scheduler
	platformClient    *platform.Client
	licenseSvc        *platform.LicenseService
	authSvc           *platform.AuthService
	skillSvc          *platform.SkillService
	analyticsSvc      *platform.AnalyticsService
	complianceSvc     *platform.ComplianceService
	auditRetentionSvc *platform.AuditRetentionService
	teamSvc           *platform.TeamService
	billingSvc        *platform.BillingService
	workspaceRoot     string
	commandExecutor   *executor.CommandExecutor

	// activeRuntimes holds RuntimeState for HeadlessOrchestrator-initiated pipelines.
	// Keyed by "repo#issueNumber". Protected by runtimesMu.
	activeRuntimes map[string]*state.RuntimeState
	runtimesMu     sync.Mutex

	// autonomousScheduler is the cross-repo autonomous scheduler (optional).
	autonomousScheduler *orchestrator.AutonomousScheduler

	// ipcRunner and licenseChecker are shared across all concurrent pipeline.runItem
	// and pipeline.run invocations. Creating these per-request caused a TOCTOU race:
	// each call overwrote srv.methods["pipeline.stageResult"], orphaning earlier
	// pipelines' pending channels and causing stage dispatch to time out (#3348).
	ipcRunner      *IpcStageRunner
	licenseChecker *IpcLicenseChecker

	// userClients caches per-user GitHub clients for multi-identity support.
	// Key: GitHub username. Protected by userClientsMu.
	userClients   map[string]*gh.Client
	userClientsMu sync.Mutex

	// suppressGHWarning mirrors github_auth.suppress_gh_warning from config.
	// Passed to NewClientForUser so the user's preference is respected.
	suppressGHWarning bool

	// resolver auto-resolves GitHub clients from (owner, repo) using per-repo config.
	resolver *ClientResolver

	// rateLimitTracker persists GitHub rate-limit state to disk so multiple
	// IPC processes (one per VSCode window) share a single view of quota
	// instead of each burning requests on independent checks.
	rateLimitTracker *gh.SharedRateLimitTracker

	// newUserClientFn is the factory used by clientForUser to construct a
	// GitHub client for a given user. Overridable in tests via WithUserClientFactory
	// so the tracker-wiring path can be exercised without spawning `gh`. Defaults
	// to gh.NewClientForUser.
	newUserClientFn func(user string, suppressWarning bool) (*gh.Client, error)

	// notificationReloader is invoked by the notifications.reloadTokens IPC
	// method to refresh the inbound webhook receiver's signing tokens after
	// the user edits notifiers config in VSCode. Optional — when nil, the
	// reload method returns an error so the TS-side caller can surface it.
	notificationReloader func(*config.Config) error

	// authorizeCommandFn is invoked by the notifications.checkAuthorization IPC
	// method. Returns (allowed, mappedIdentity, reason). Optional — when nil the
	// method returns an error. Stored as a closure so internal/ipc does not
	// import internal/notifications/inbound/auth.
	authorizeCommandFn func(ctx context.Context, mattermostUserID, channelID, commandType, repoSlug string) (allowed bool, mappedIdentity, reason string)

	// forgeRegistry stores per-repo forge instance configuration set via the
	// workspace.configureForgeInstance IPC method. Keyed by "owner/repo".
	// In-memory only; full persistence is tracked in #3361.
	forgeRegistry   map[string]ForgeInstanceConfig
	forgeRegistryMu sync.Mutex
}

// ForgeInstanceConfig captures the forge kind + host bound to a repository.
// Stored in Server.forgeRegistry by the workspace.configureForgeInstance IPC
// method.
type ForgeInstanceConfig struct {
	Kind  string // "github" | "gitlab"
	Host  string
	Token string
}

// ForgeInstanceFor returns the registered forge configuration for an
// (owner, repo) pair plus a found flag. Exported for tests and for callers
// that need to route operations to the configured adapter.
func (s *Server) ForgeInstanceFor(owner, repo string) (ForgeInstanceConfig, bool) {
	s.forgeRegistryMu.Lock()
	defer s.forgeRegistryMu.Unlock()
	cfg, ok := s.forgeRegistry[owner+"/"+repo]
	return cfg, ok
}

// Handler processes an IPC request and returns a result or error.
type Handler func(ctx context.Context, params json.RawMessage) (interface{}, error)

// NewServer creates a new IPC server.
func NewServer(client *gh.Client, opts ...ServerOption) *Server {
	s := &Server{
		client:         client,
		writer:         os.Stdout,
		methods:        make(map[string]Handler),
		userClients:    make(map[string]*gh.Client),
		activeRuntimes: make(map[string]*state.RuntimeState),
		forgeRegistry:  make(map[string]ForgeInstanceConfig),
	}
	for _, opt := range opts {
		opt(s)
	}
	// Initialize the shared rate-limit tracker before constructing dependents
	// so the resolver and the default client both feed it (Issue #3417).
	// Without this ordering, resolver-created clients never refreshed the
	// shared file and the proactive gate never fired.
	if s.rateLimitTracker == nil {
		if path, err := gh.DefaultSharedTrackerPath(); err == nil {
			s.rateLimitTracker = gh.NewSharedRateLimitTracker(path)
		}
	}
	if s.rateLimitTracker != nil && s.client != nil {
		// Attach to the default client. The empty user collapses to "default"
		// in the tracker; the header interceptor then keeps the entry fresh
		// on every successful response. WithRateLimitWait: in-flight + recovery
		// ops (revert-status, move-to-done, promote, board sync) wait out a
		// rate-limit reset rather than hard-failing (#3976). The scheduler's
		// dispatch loop is protected separately — it skips the cycle when
		// GitHub headroom < 200 (> the client floor of 100), so the per-call
		// gate never blocks the depgraph build.
		s.client = s.client.WithRateLimitTracker(s.rateLimitTracker, "").WithRateLimitWait()
	}
	if s.newUserClientFn == nil {
		s.newUserClientFn = gh.NewClientForUser
	}
	s.resolver = NewClientResolverWithTracker(s.client, s.suppressGHWarning, s.rateLimitTracker)
	s.registerMethods()
	return s
}

// ServerOption configures the IPC server.
type ServerOption func(*Server)

// WithExecutionManager attaches an execution manager to the IPC server.
func WithExecutionManager(mgr *execution.Manager) ServerOption {
	return func(s *Server) {
		s.execMgr = mgr
	}
}

// WithScheduler attaches the orchestrator scheduler to the IPC server.
func WithScheduler(sched *orchestrator.Scheduler) ServerOption {
	return func(s *Server) {
		s.scheduler = sched
	}
}

// WithPlatformClient attaches a platform client to the IPC server.
func WithPlatformClient(pc *platform.Client) ServerOption {
	return func(s *Server) {
		s.platformClient = pc
		s.licenseSvc = platform.NewLicenseService(pc)
		s.skillSvc = platform.NewSkillService(pc)
		s.analyticsSvc = platform.NewAnalyticsService(pc)
		s.complianceSvc = platform.NewComplianceService(pc)
		s.auditRetentionSvc = platform.NewAuditRetentionService(pc)
		s.teamSvc = platform.NewTeamService(pc)
		s.billingSvc = platform.NewBillingService(pc)
	}
}

// WithAuthService attaches an auth service to the IPC server.
func WithAuthService(as *platform.AuthService) ServerOption {
	return func(s *Server) {
		s.authSvc = as
	}
}

// SetScheduler attaches a scheduler after construction.
// Used when the scheduler depends on the server (e.g., IpcStageRunner).
// Wires the shared IpcStageRunner, IpcLicenseChecker, all lifecycle callbacks,
// and queue.changed — equivalent to the registerMethods() init block but
// called post-construction (e.g., from serveCmd where the scheduler is built
// after NewServer). See #3348.
func (s *Server) SetScheduler(sched *orchestrator.Scheduler) {
	s.scheduler = sched
	s.initSchedulerCallbacks(sched)
}

// initSchedulerCallbacks wires the shared runner, checker, and lifecycle
// callbacks for a scheduler. Called from both SetScheduler (post-construction)
// and registerMethods() (when scheduler is already set via WithScheduler option).
// Idempotent: subsequent calls overwrite single-slot callbacks, which is safe
// because the scheduler is only fully wired once per server lifetime.
func (s *Server) initSchedulerCallbacks(sched *orchestrator.Scheduler) {
	if s.ipcRunner == nil {
		s.ipcRunner = NewIpcStageRunner(s, sched.RetryEngine())
	}
	if s.licenseChecker == nil {
		s.licenseChecker = NewIpcLicenseChecker(s)
	}
	RegisterStageResultHandler(s, s.ipcRunner)
	RegisterLicenseResultHandler(s, s.licenseChecker)
	sched.WithStageRunner(s.ipcRunner)
	sched.WithLicenseChecker(s.licenseChecker)
	// Root each run's on-disk state (trace, runtime-{N}.json, stage-context,
	// exit-records, worktrees) at the run's target repo, reusing the same
	// ClientResolver registry that pipelineStateDir/RegisterRepo populate at
	// startup. Unregistered/empty repos fall back to workspaceRoot inside the
	// scheduler (#229).
	sched.WithRepoPathResolver(func(repo string) string {
		return s.resolver.RepoPath(repo)
	})
	// Let orchestrator-crash recovery scan every registered repo's
	// current-run.json sidecar, not just the launch root's. Because the sidecar
	// is rooted at the run's target repo above (#229), a cross-repo run that
	// crashes mid-stage persists its sidecar outside the launch root; enumerating
	// the registered paths (same registry pipelineStateScanRoots uses) is what
	// lets recovery reconcile it into a terminal record (#239).
	sched.WithRepoRootsResolver(func() []string {
		return s.resolver.RegisteredPaths()
	})

	sched.OnQueueChanged(func(state orchestrator.QueueState) {
		s.Emit("queue.changed", state)
	})
	sched.OnStageStart(func(cbRepo string, issue int, stage string, title string) {
		s.Emit("stage.start", map[string]interface{}{
			"repo":        cbRepo,
			"issueNumber": issue,
			"stage":       stage,
			"title":       title,
		})
	})
	sched.OnStageComplete(func(cbRepo string, issue int, stage string, stageErr error, inputTokens, outputTokens, cacheReadTokens int, costUsd float64, model string) {
		errStr := ""
		if stageErr != nil {
			errStr = stageErr.Error()
		}
		s.Emit("stage.complete", map[string]interface{}{
			"repo":            cbRepo,
			"issueNumber":     issue,
			"stage":           stage,
			"error":           errStr,
			"inputTokens":     inputTokens,
			"outputTokens":    outputTokens,
			"cacheReadTokens": cacheReadTokens,
			"costUsd":         costUsd,
			"model":           model,
		})
	})
	sched.OnPipelineComplete(func(cbRepo string, issue int, runtime *state.RuntimeState, ok bool) {
		snap := runtime
		perStage := make([]map[string]interface{}, len(snap.CompletedStages))
		for i, sr := range snap.CompletedStages {
			perStage[i] = map[string]interface{}{
				"stage":        string(sr.Stage),
				"inputTokens":  sr.InputTokens,
				"outputTokens": sr.OutputTokens,
				"cacheRead":    sr.CacheRead,
				"costUsd":      sr.CostUSD,
			}
		}
		now := time.Now()
		durationMs := int64(0)
		startedAt := now.Format(time.RFC3339)
		if !snap.StartedAt.IsZero() {
			startedAt = snap.StartedAt.Format(time.RFC3339)
			durationMs = now.Sub(snap.StartedAt).Milliseconds()
		}
		s.Emit("pipeline.complete", map[string]interface{}{
			"executionId":       fmt.Sprintf("%s#%d", cbRepo, issue),
			"issueNumber":       issue,
			"success":           ok,
			"totalInputTokens":  snap.InputTokens,
			"totalOutputTokens": snap.OutputTokens,
			"totalCostUSD":      snap.TotalCostUSD,
			"startedAt":         startedAt,
			"durationMs":        durationMs,
			"perStage":          perStage,
		})
		s.Emit("pipeline.historyRecorded", map[string]interface{}{
			"issueNumber": issue,
			"success":     ok,
		})
		if snap.LicenseExpiredMidRun {
			s.Emit("pipeline.licenseExpired", map[string]interface{}{
				"issueNumber": issue,
			})
		}
		owner, _ := splitOwnerRepo(cbRepo)
		s.Emit("tree.in-progress.update", TreeUpdateEvent{
			Owner: owner,
		})
	})
	sched.OnStateChanged(func(cbRepo string, issue int, runtime *state.RuntimeState) {
		snap := runtime
		s.Emit("pipeline.stateChanged", map[string]interface{}{
			"repo":        cbRepo,
			"issueNumber": issue,
			"state":       snap,
		})
	})
	sched.OnModelFallback(func(cbRepo string, issue int, stage, fromModel, toModel, reason string) {
		// Model rejected by the API → sticky tier downgrade (#42). The
		// extension surfaces this as a VSCode notification and Discord embed
		// naming the original model, the rejection reason, and the substitute.
		s.Emit("pipeline.modelFallback", map[string]interface{}{
			"repo":        cbRepo,
			"issueNumber": issue,
			"stage":       stage,
			"fromModel":   fromModel,
			"toModel":     toModel,
			"reason":      reason,
		})
	})
	sched.OnPhaseDetected(func(cbRepo string, issue int, pStage, pName string, pIndex, pTotal int) {
		s.Emit("phase.start", map[string]interface{}{
			"repo":        cbRepo,
			"issueNumber": issue,
			"stage":       pStage,
			"name":        pName,
			"index":       pIndex,
			"total":       pTotal,
		})
	})
}

// WithWorkspaceRoot sets the workspace root for git operations.
func WithWorkspaceRoot(root string) ServerOption {
	return func(s *Server) {
		s.workspaceRoot = root
	}
}

// WithCommandExecutor attaches a CommandExecutor to the IPC server.
// The polling loop retrieves it via CommandExecutor() to dispatch polled commands.
func WithCommandExecutor(e *executor.CommandExecutor) ServerOption {
	return func(s *Server) {
		s.commandExecutor = e
	}
}

// WithRateLimitTracker injects a SharedRateLimitTracker (primarily for tests
// that need a non-default path or a stubbed tracker).
func WithRateLimitTracker(t *gh.SharedRateLimitTracker) ServerOption {
	return func(s *Server) {
		s.rateLimitTracker = t
	}
}

// WithUserClientFactory overrides the constructor used by clientForUser. Test
// hook for verifying tracker wiring without spawning `gh` (Issue #3417). Pass
// a function that returns a synthesized *gh.Client for the requested user.
func WithUserClientFactory(fn func(user string, suppressWarning bool) (*gh.Client, error)) ServerOption {
	return func(s *Server) {
		s.newUserClientFn = fn
	}
}

// WithNotificationReloader registers a callback invoked by the
// notifications.reloadTokens IPC method. The callback receives a freshly
// reloaded *config.Config and is expected to refresh any in-memory
// signing-token state (typically by calling TokenStore.Reload).
//
// This indirection keeps internal/ipc free of any
// internal/notifications/inbound import — the callback is a closure
// constructed in cmd/, where both packages are already in scope.
func WithNotificationReloader(fn func(*config.Config) error) ServerOption {
	return func(s *Server) {
		s.notificationReloader = fn
	}
}

// WithCommandAuthorizer registers the closure invoked by notifications.checkAuthorization.
// The closure receives (mattermostUserID, channelID, commandType, repoSlug) and returns
// (allowed, mappedIdentity, reason). Keeping it as a closure avoids an import of
// internal/notifications/inbound/auth inside internal/ipc.
func WithCommandAuthorizer(fn func(ctx context.Context, mattermostUserID, channelID, commandType, repoSlug string) (bool, string, string)) ServerOption {
	return func(s *Server) {
		s.authorizeCommandFn = fn
	}
}

// WithSuppressGHWarning sets the gh CLI deprecation warning suppression flag.
// Pass cfg.SuppressGHWarning() here when constructing the server from a loaded config.
func WithSuppressGHWarning(suppress bool) ServerOption {
	return func(s *Server) {
		s.suppressGHWarning = suppress
	}
}

// WithAutonomousScheduler attaches the cross-repo autonomous scheduler (option).
func WithAutonomousScheduler(as *orchestrator.AutonomousScheduler) ServerOption {
	return func(s *Server) {
		s.autonomousScheduler = as
	}
}

// SetAutonomousScheduler attaches the cross-repo autonomous scheduler after construction.
// Used by the serve command when the autonomous config is present but the scheduler
// must be created after the IPC server (to share the underlying Scheduler).
func (s *Server) SetAutonomousScheduler(as *orchestrator.AutonomousScheduler) {
	s.autonomousScheduler = as

	// Wire dispatch callback: when the autonomous scheduler wants to run an
	// issue, emit an IPC event so the TypeScript extension can route it through
	// HeadlessOrchestrator (same path as clicking "Pick Up Issue").
	as.OnDispatch(func(owner, repo string, issueNumber int, title string) {
		s.Emit("autonomous.dispatch", map[string]interface{}{
			"owner":       owner,
			"repo":        repo,
			"issueNumber": issueNumber,
			"title":       title,
		})
	})

	// Wire status-change callback: every Status transition (Pause, Resume,
	// safety_tripped, complete, init) emits `autonomous.statusChanged` so the
	// VSCode extension's status-bar badge stays in sync without polling
	// (Issue #3251). Without this, Go-side transitions (e.g. safety trip,
	// haltQueueOnSlotFailure) leave the badge stuck on "running".
	as.OnStatusChange(func(snap orchestrator.AutonomousStatusChange) {
		s.Emit("autonomous.statusChanged", map[string]interface{}{
			"status":           snap.Status,
			"pauseReason":      snap.PauseReason,
			"pauseTriggeredBy": snap.PauseTriggeredBy,
			"runningCount":     snap.RunningCount,
			"remaining":        snap.Remaining,
		})
	})

	// Wire stage-exit diagnostic provider fns onto the inner scheduler so
	// every stage-exit record carries the autonomous scheduler's full
	// cross-repo sibling list and live rate-limit reading. Without this,
	// the scheduler falls back to its single-process activeStages map
	// (no repo info) and only its own ghClient's tracker. Issue #3605.
	if s.scheduler != nil {
		s.scheduler.SetRunningSiblingsFn(as.RunningSiblings)
		s.scheduler.SetRateLimitRemainingFn(as.RateLimitRemaining)
	}

	// Wire the Action Center surface push (ADR 015 §E): every DecisionRequest
	// lifecycle transition (created/updated/acknowledged/resolved/expired) emits
	// `attention.event` so the VSCode Attention view updates live without
	// polling — the same Go→TS event channel autonomous.statusChanged rides.
	if store := as.Attention(); store != nil {
		store.Subscribe(func(entry attention.JournalEntry, req *attention.DecisionRequest) {
			s.Emit("attention.event", map[string]interface{}{
				"action":  entry.Action,
				"request": req,
			})
		})
	}
}

// CommandExecutor returns the CommandExecutor attached to this server.
// The polling loop (#2163) calls this to dispatch each PendingCommand returned
// by CommandService.PollCommands().
func (s *Server) CommandExecutor() *executor.CommandExecutor {
	return s.commandExecutor
}

// clientForUser returns a GitHub client authenticated as the given user.
// Returns the default server client when githubUser is empty. Per-user
// clients are cached for the lifetime of the server.
func (s *Server) clientForUser(githubUser string) (*gh.Client, error) {
	if githubUser == "" {
		return s.client, nil
	}
	s.userClientsMu.Lock()
	defer s.userClientsMu.Unlock()
	if c, ok := s.userClients[githubUser]; ok {
		return c, nil
	}
	c, err := s.newUserClientFn(githubUser, s.suppressGHWarning)
	if err != nil {
		return nil, fmt.Errorf("resolve client for user %s: %w", githubUser, err)
	}
	// Wire the shared rate-limit tracker (Issue #3417). Per-user clients
	// otherwise would not feed the shared file and the proactive gate would
	// be dead code for any IPC call routed through clientForUser.
	if s.rateLimitTracker != nil {
		// WithRateLimitWait: see the default-client wiring above (#3976) —
		// in-flight per-user ops wait out a rate-limit reset instead of
		// hard-failing.
		c = c.WithRateLimitTracker(s.rateLimitTracker, githubUser).WithRateLimitWait()
	}
	s.userClients[githubUser] = c
	log.Printf("IPC: created GitHub client for user %s", githubUser)
	return c, nil
}

// resolveClientForRequest tries clientForUser first (explicit identity),
// then falls back to the per-repo resolver (auto identity).
func (s *Server) resolveClientForRequest(ctx context.Context, githubUser, owner, repo string) (*gh.Client, error) {
	if githubUser != "" {
		return s.clientForUser(githubUser)
	}
	return s.resolver.Resolve(ctx, owner, repo)
}

// ResolveGitHubClient returns a GitHub client scoped to (owner, repo) using that
// repo's configured token/identity. Exported so the orchestrator scheduler can
// authenticate cross-repo operations with the correct configured github_user
// instead of the scheduler's single startup client (#3700).
func (s *Server) ResolveGitHubClient(ctx context.Context, owner, repo string) (*gh.Client, error) {
	return s.resolver.Resolve(ctx, owner, repo)
}

// RegisterRepo maps (owner, repo) to a filesystem path so the per-repo client
// resolver can load that repo's .nightgauge/config.yaml and resolve its
// configured identity. Without a registration the resolver falls back to the
// default startup client — which is the primary repo's identity and cannot see
// private sibling repos. Call at startup for every workspace repo (#3700).
func (s *Server) RegisterRepo(owner, repo, path string) {
	s.resolver.RegisterRepo(owner, repo, path)
}

// repoRoot resolves the on-disk root a run's target repo lives in. In a
// multi-repo workspace a run's persisted state (history RunRecords, stage
// exit-records, runtime snapshot) must land in its target repo — the same
// root its stage context files (issue-{N}.json, pr-{N}.json) use — not the
// IPC server's launch root, or the run's state is split across two repos
// (#215/#232). repo is the "owner/name" slug the notify params carry;
// unknown/empty repos fall back to the server's workspaceRoot. Returns ""
// only when neither resolves. The resolver nil-guard keeps hand-rolled test
// fixtures (&Server{} without a resolver) from panicking.
func (s *Server) repoRoot(repo string) string {
	if repo != "" && s.resolver != nil {
		if root := s.resolver.RepoPath(repo); root != "" {
			return root
		}
	}
	return s.workspaceRoot
}

// pipelineStateDir resolves the .nightgauge/pipeline directory a run's
// runtime-{N}.json belongs in, scoped to the run's target repo via repoRoot.
// Returns "" when no root resolves (e.g. an unconfigured server).
func (s *Server) pipelineStateDir(repo string) string {
	root := s.repoRoot(repo)
	if root == "" {
		return ""
	}
	return filepath.Join(root, ".nightgauge", "pipeline")
}

// invalidateOnAuth401 evicts the cached client for (owner, repo) when a
// GitHub API call returns an authentication error.
func (s *Server) invalidateOnAuth401(err error, owner, repo string) {
	if err != nil && (strings.Contains(err.Error(), "401") || strings.Contains(err.Error(), "UNAUTHORIZED")) {
		s.resolver.Invalidate(owner, repo)
	}
}

// Run starts the IPC server, reading from stdin and writing to stdout.
func (s *Server) Run(ctx context.Context) error {
	// Start periodic flush of buffered analytics (runs, events) in the background.
	if s.analyticsSvc != nil {
		s.analyticsSvc.StartAutoFlush(ctx)
	}

	// Close out platform rows orphaned by runs that died with this server's
	// previous incarnation (#44). Server start is extension activation, so
	// this is the "workspace reopened" reconciliation moment.
	s.reconcileOrphanedRuns()

	// Emit ipc.ready event with protocol version so the TypeScript client
	// can validate binary compatibility on startup.
	s.Emit("ipc.ready", map[string]interface{}{
		"protocolVersion": ProtocolVersion,
	})

	scanner := bufio.NewScanner(os.Stdin)
	// Allow up to 10MB per message
	scanner.Buffer(make([]byte, 0, 64*1024), 10*1024*1024)

	for scanner.Scan() {
		line := scanner.Bytes()
		if len(line) == 0 {
			continue
		}

		var req Request
		if err := json.Unmarshal(line, &req); err != nil {
			s.sendError(0, ErrInvalidParams, fmt.Sprintf("invalid JSON: %v", err))
			continue
		}

		go s.handleRequest(ctx, req)
	}

	return scanner.Err()
}

func (s *Server) handleRequest(ctx context.Context, req Request) {
	// Recover from panics in handlers so a single bad request doesn't crash
	// the entire IPC server (and lose all autonomous mode state/logs).
	defer func() {
		if r := recover(); r != nil {
			log.Printf("WARNING: PANIC in IPC handler %q (id=%d): %v", req.Method, req.ID, r)
			log.Printf("Stack trace:\n%s", debug.Stack())
			s.Emit("ipc.panic", map[string]interface{}{
				"context": req.Method,
				"message": fmt.Sprintf("%v", r),
			})
			s.sendError(req.ID, ErrInternal, fmt.Sprintf("internal panic in %s: %v", req.Method, r))
		}
	}()

	handler, ok := s.methods[req.Method]
	if !ok {
		s.sendError(req.ID, ErrMethodNotFound, fmt.Sprintf("unknown method: %s", req.Method))
		return
	}

	result, err := handler(ctx, req.Params)
	if err != nil {
		s.sendError(req.ID, ErrInternal, err.Error())
		return
	}

	s.sendResponse(Response{
		ID:     req.ID,
		Result: result,
	})
}

// reconcilePrMergeGroundTruth applies the #266 ground-truth rule at the
// interactive recording boundary: a run whose PR merged must never be booked as
// failed by a late per-stage kill (progress-runaway / stall / budget) that fired
// at pr-merge AFTER the merge landed on the forge. It returns the outcome that
// should actually be recorded.
//
// It flips a reported failure to success ONLY when all hold:
//   - the run was reported failed (reportedSuccess == false), and
//   - the extension signalled a forge-confirmed merge (prMerged == true), and
//   - the terminal stage is pr-merge.
//
// The pr-merge scope is deliberate: a failure at a LATER stage (e.g.
// pipeline-finish) is a genuine failure even when the PR merged, so it is left
// untouched. A reported success is always returned as-is.
func reconcilePrMergeGroundTruth(reportedSuccess, prMerged bool, terminalStage string) bool {
	if reportedSuccess || !prMerged {
		return reportedSuccess
	}
	if strings.EqualFold(terminalStage, "pr-merge") {
		return true
	}
	return reportedSuccess
}

func (s *Server) registerMethods() {
	// --- Workspace methods ---

	//ipc:method workspaceSetRoot params:WorkspaceSetRootParams result:WorkspaceSetRootResult
	s.methods["workspace.setRoot"] = func(_ context.Context, raw json.RawMessage) (interface{}, error) {
		var p WorkspaceSetRootParams
		if err := json.Unmarshal(raw, &p); err != nil {
			return nil, fmt.Errorf("parse params: %w", err)
		}
		if p.Root == "" {
			return nil, fmt.Errorf("root must not be empty")
		}
		s.workspaceRoot = p.Root
		// A multi-repo workspace switch exposes a different .nightgauge/pipeline
		// dir — close out any runs orphaned there too (#44). Idempotent: each
		// reconciled snapshot is removed after its terminal event is emitted.
		s.reconcileOrphanedRuns()
		return &WorkspaceSetRootResult{OK: true}, nil
	}

	//ipc:method workspaceRegisterRepo params:WorkspaceRegisterRepoParams result:WorkspaceRegisterRepoResult
	s.methods["workspace.registerRepo"] = func(_ context.Context, raw json.RawMessage) (interface{}, error) {
		var p WorkspaceRegisterRepoParams
		if err := json.Unmarshal(raw, &p); err != nil {
			return nil, fmt.Errorf("parse params: %w", err)
		}
		if p.Owner == "" || p.Repo == "" || p.Path == "" {
			return nil, fmt.Errorf("owner, repo, and path are all required")
		}
		s.resolver.RegisterRepo(p.Owner, p.Repo, p.Path)
		return &WorkspaceRegisterRepoResult{OK: true}, nil
	}

	//ipc:method workspaceConfigureForgeInstance params:ConfigureForgeInstanceParams result:ConfigureForgeInstanceResult
	s.methods["workspace.configureForgeInstance"] = func(_ context.Context, raw json.RawMessage) (interface{}, error) {
		var p ConfigureForgeInstanceParams
		if err := json.Unmarshal(raw, &p); err != nil {
			return nil, fmt.Errorf("parse params: %w", err)
		}
		if p.Owner == "" || p.Repo == "" {
			return nil, fmt.Errorf("owner and repo are required")
		}
		switch p.Kind {
		case "github", "gitlab":
			// ok
		default:
			return nil, fmt.Errorf("kind must be \"github\" or \"gitlab\", got %q", p.Kind)
		}
		s.forgeRegistryMu.Lock()
		s.forgeRegistry[p.Owner+"/"+p.Repo] = ForgeInstanceConfig{
			Kind:  p.Kind,
			Host:  p.Host,
			Token: p.Token,
		}
		s.forgeRegistryMu.Unlock()
		return &ConfigureForgeInstanceResult{OK: true, Kind: p.Kind}, nil
	}

	// --- Config methods ---

	//ipc:method configGetProjectConfig params:ConfigGetProjectParams result:ConfigGetProjectResult
	s.methods["config.getProjectConfig"] = func(_ context.Context, raw json.RawMessage) (interface{}, error) {
		var p ConfigGetProjectParams
		if len(raw) > 0 {
			_ = json.Unmarshal(raw, &p)
		}
		root := p.Root
		if root == "" {
			root = s.workspaceRoot
		}
		if root == "" {
			return nil, fmt.Errorf("no workspace root configured")
		}
		cfg, err := config.Load(root)
		if err != nil {
			return nil, fmt.Errorf("load config: %w", err)
		}
		return &ConfigGetProjectResult{
			Owner:         cfg.Owner,
			ProjectNumber: cfg.ProjectNumber,
			DefaultRepo:   cfg.DefaultRepo,
			OwnerType:     cfg.OwnerType,
		}, nil
	}

	//ipc:method configGetHealthThresholds params:none result:ConfigGetHealthThresholdsResult
	s.methods["config.getHealthThresholds"] = func(_ context.Context, _ json.RawMessage) (interface{}, error) {
		root := s.workspaceRoot
		if root == "" {
			return nil, fmt.Errorf("no workspace root configured")
		}
		cfg, err := config.Load(root)
		if err != nil {
			return nil, fmt.Errorf("load config: %w", err)
		}
		// Apply defaults matching HealthActionService TypeScript defaults
		warningThreshold := 70.0
		criticalThreshold := 50.0
		emergencyThreshold := 30.0
		actionsEnabled := true
		policiesEnabled := true
		feedbackLoopEnabled := true
		if cfg.FeedbackLoop != nil {
			fl := cfg.FeedbackLoop
			if fl.WarningThreshold != 0 {
				warningThreshold = fl.WarningThreshold
			}
			if fl.CriticalThreshold != 0 {
				criticalThreshold = fl.CriticalThreshold
			}
			if fl.EmergencyThreshold != 0 {
				emergencyThreshold = fl.EmergencyThreshold
			}
			if fl.ActionsEnabled != nil {
				actionsEnabled = *fl.ActionsEnabled
			}
			if fl.PoliciesEnabled != nil {
				policiesEnabled = *fl.PoliciesEnabled
			}
			if fl.AutoRetroactive != nil {
				feedbackLoopEnabled = *fl.AutoRetroactive
			}
		}
		return &ConfigGetHealthThresholdsResult{
			WarningThreshold:    warningThreshold,
			CriticalThreshold:   criticalThreshold,
			EmergencyThreshold:  emergencyThreshold,
			ActionsEnabled:      actionsEnabled,
			PoliciesEnabled:     policiesEnabled,
			FeedbackLoopEnabled: feedbackLoopEnabled,
		}, nil
	}

	//ipc:method configTierAudit params:ConfigTierAuditParams result:ConfigTierAuditResult
	s.methods["config.tierAudit"] = func(_ context.Context, raw json.RawMessage) (interface{}, error) {
		var p ConfigTierAuditParams
		if len(raw) > 0 {
			_ = json.Unmarshal(raw, &p)
		}
		root := p.Root
		if root == "" {
			root = s.workspaceRoot
		}
		if root == "" {
			return nil, fmt.Errorf("no workspace root configured")
		}
		entries, err := config.BuildAuditReport(root)
		if err != nil {
			return nil, fmt.Errorf("tier audit: %w", err)
		}
		hasDrift := false
		for _, e := range entries {
			if strings.HasPrefix(e.Status, "DRIFT") {
				hasDrift = true
				break
			}
		}
		return &ConfigTierAuditResult{Entries: entries, HasDrift: hasDrift}, nil
	}

	// --- Notifications methods ---

	//ipc:method notificationsReloadTokens params:none result:NotificationsReloadTokensResult
	s.methods["notifications.reloadTokens"] = func(_ context.Context, _ json.RawMessage) (interface{}, error) {
		if s.notificationReloader == nil {
			return nil, fmt.Errorf("notifications.reloadTokens: receiver not enabled")
		}
		root := s.workspaceRoot
		if root == "" {
			return nil, fmt.Errorf("no workspace root configured")
		}
		cfg, err := config.Load(root)
		if err != nil {
			return nil, fmt.Errorf("load config: %w", err)
		}
		if err := s.notificationReloader(cfg); err != nil {
			return nil, fmt.Errorf("reload tokens: %w", err)
		}
		return &NotificationsReloadTokensResult{OK: true}, nil
	}

	//ipc:method notificationsCheckAuthorization params:CheckAuthorizationParams result:CheckAuthorizationResult
	s.methods["notifications.checkAuthorization"] = func(ctx context.Context, params json.RawMessage) (interface{}, error) {
		if s.authorizeCommandFn == nil {
			return nil, fmt.Errorf("notifications.checkAuthorization: authorization not configured")
		}
		var p CheckAuthorizationParams
		if err := json.Unmarshal(params, &p); err != nil {
			return nil, fmt.Errorf("invalid params: %w", err)
		}
		allowed, mappedIdentity, reason := s.authorizeCommandFn(ctx, p.MattermostUserID, p.ChannelID, p.CommandType, p.RepoSlug)
		return &CheckAuthorizationResult{
			Allowed:        allowed,
			MappedIdentity: mappedIdentity,
			Reason:         reason,
		}, nil
	}

	//ipc:method boardList params:BoardListParams result:BoardItem[] nullable
	s.methods["board.list"] = func(ctx context.Context, params json.RawMessage) (interface{}, error) {
		var p BoardListParams
		if err := json.Unmarshal(params, &p); err != nil {
			return nil, fmt.Errorf("invalid params: %w", err)
		}
		c, err := s.clientForUser(p.GitHubUser)
		if err != nil {
			return nil, err
		}
		svc := gh.NewBoardService(c, p.Owner, p.ProjectNumber, gh.ParseOwnerType(p.OwnerType))
		return svc.ListItems(ctx, p.Status)
	}

	//ipc:method boardCounts params:BoardCountsParams result:StatusCounts
	s.methods["board.counts"] = func(ctx context.Context, params json.RawMessage) (interface{}, error) {
		var p BoardCountsParams
		if err := json.Unmarshal(params, &p); err != nil {
			return nil, fmt.Errorf("invalid params: %w", err)
		}
		c, err := s.clientForUser(p.GitHubUser)
		if err != nil {
			return nil, err
		}
		svc := gh.NewBoardService(c, p.Owner, p.ProjectNumber, gh.ParseOwnerType(p.OwnerType))
		return svc.CountsByStatus(ctx)
	}

	//ipc:method githubRateLimit params:GitHubRateLimitParams result:RateLimitInfo
	s.methods["github.rateLimit"] = func(ctx context.Context, params json.RawMessage) (interface{}, error) {
		var p GitHubRateLimitParams
		if err := json.Unmarshal(params, &p); err != nil {
			return nil, fmt.Errorf("invalid params: %w", err)
		}
		// Serve from the shared tracker when a recent reading exists. This is
		// the core of the multi-workspace fix: N VSCode windows opening would
		// previously issue N GraphQL rateLimit queries per minute. With the
		// tracker, at most one query fires per SharedTrackerMinCheckIntervalSecs
		// regardless of how many windows are open.
		if s.rateLimitTracker != nil {
			if entry, fresh, err := s.rateLimitTracker.Get(p.GitHubUser); err == nil && fresh && entry != nil {
				return &gh.RateLimitInfo{
					Remaining: entry.Remaining,
					Limit:     entry.Limit,
					ResetAt:   entry.ResetAt,
				}, nil
			}
		}
		c, err := s.clientForUser(p.GitHubUser)
		if err != nil {
			return nil, err
		}
		info, err := c.GetRateLimit(ctx)
		if err != nil {
			return nil, err
		}
		if s.rateLimitTracker != nil {
			// Persist is best-effort — if the tracker file is unwritable we
			// still return fresh data to the caller rather than failing.
			_ = s.rateLimitTracker.Set(p.GitHubUser, info)
		}
		return info, nil
	}

	//ipc:method workflowQuotaState params:WorkflowQuotaStateParams result:WorkflowQuotaStateResult
	// #3909 — bridges the Go-side ratelimit/cooldown quota state to the TS SDK
	// so the WorkflowExecutor (#3908) can gate a large fan-out against remaining
	// quota. Deterministic and fast: served entirely from already-persisted
	// state (the shared rate-limit tracker + the autonomous dispatch cooldown),
	// so it issues NO live GraphQL probe and consumes zero quota. The gate
	// decision (`exhausted` + `bucket`) is computed HERE so the quota logic stays
	// single-sourced in Go and is never duplicated in TypeScript.
	s.methods["workflow.quotaState"] = func(_ context.Context, params json.RawMessage) (interface{}, error) {
		var p WorkflowQuotaStateParams
		if len(params) > 0 {
			if err := json.Unmarshal(params, &p); err != nil {
				return nil, fmt.Errorf("invalid params: %w", err)
			}
		}

		result := WorkflowQuotaStateResult{Remaining: -1, Limit: -1}

		// GitHub bucket: read the shared tracker without a live probe so the call
		// is deterministic. A missing/stale entry leaves the -1 sentinels — the
		// caller treats "no reading" as "not exhausted on this signal".
		var haveBucket bool
		if s.rateLimitTracker != nil {
			if entry, _, err := s.rateLimitTracker.Get(p.GitHubUser); err == nil && entry != nil {
				result.Remaining = entry.Remaining
				result.Limit = entry.Limit
				result.ResetsAt = entry.ResetAt
				haveBucket = true
			}
		}

		// Global dispatch cooldown: covers both the Anthropic 5-hour bucket and
		// the GitHub-quota suspension via the single QuotaCooldownUntil field.
		var cooldownActive bool
		if s.autonomousScheduler != nil {
			until, reason, active := s.autonomousScheduler.QuotaCooldownSnapshot()
			result.CooldownUntil = until
			result.CooldownReason = reason
			cooldownActive = active
		}

		// Derive the single gate signal. An active dispatch cooldown is the
		// strongest signal (a quota was proven exhausted), so it wins the bucket
		// attribution; a depleted GitHub tracker bucket is the fallback signal.
		switch {
		case cooldownActive:
			result.Exhausted = true
			result.Bucket = quotaCooldownBucket(result.CooldownReason)
		case haveBucket && result.Remaining <= 0:
			result.Exhausted = true
			result.Bucket = "github-rest"
		}

		return result, nil
	}

	//ipc:method forgeList params:none result:ForgeListResult
	s.methods["forge.list"] = func(_ context.Context, _ json.RawMessage) (interface{}, error) {
		root := s.workspaceRoot
		if root == "" {
			return &ForgeListResult{Forges: []ForgeListEntry{}}, nil
		}
		cfg, err := config.Load(root)
		if err != nil {
			return nil, fmt.Errorf("load config: %w", err)
		}
		entries := make([]ForgeListEntry, 0, len(cfg.Forges))
		for id, entry := range cfg.Forges {
			if entry == nil {
				continue
			}
			entries = append(entries, ForgeListEntry{
				ID:         id,
				Kind:       entry.Kind,
				BaseURL:    entry.BaseURL,
				AuthMethod: entry.AuthMethod,
				CABundle:   entry.CABundle,
			})
		}
		return &ForgeListResult{Forges: entries}, nil
	}

	//ipc:method forgeConnectionTest params:ForgeConnectionTestParams result:ForgeConnectionTestResult
	s.methods["forge.connectionTest"] = func(ctx context.Context, raw json.RawMessage) (interface{}, error) {
		var p ForgeConnectionTestParams
		if err := json.Unmarshal(raw, &p); err != nil {
			return nil, fmt.Errorf("parse params: %w", err)
		}
		if p.InstanceID == "" {
			return nil, fmt.Errorf("instance_id is required")
		}
		root := s.workspaceRoot
		if root == "" {
			return nil, fmt.Errorf("no workspace root configured")
		}
		cfg, err := config.Load(root)
		if err != nil {
			return nil, fmt.Errorf("load config: %w", err)
		}
		entry, ok := cfg.Forges[p.InstanceID]
		if !ok || entry == nil {
			return nil, fmt.Errorf("forge instance %q not found in config", p.InstanceID)
		}

		// Resolve credential: prefer param token, then env var from config
		token := p.Token
		if token == "" && entry.TokenEnv != "" {
			token = os.Getenv(entry.TokenEnv)
		}

		start := time.Now()
		var testErr error
		var version string
		var scopes []string

		switch entry.Kind {
		case "gitlab":
			baseURL := entry.BaseURL
			if baseURL == "" {
				baseURL = "https://gitlab.com"
			}
			httpClient := &http.Client{Timeout: 10 * time.Second}
			req, reqErr := http.NewRequestWithContext(ctx, http.MethodGet, strings.TrimRight(baseURL, "/")+"/api/v4/version", nil)
			if reqErr != nil {
				testErr = reqErr
			} else {
				if token != "" {
					req.Header.Set("PRIVATE-TOKEN", token)
				}
				resp, doErr := httpClient.Do(req)
				if doErr != nil {
					testErr = doErr
				} else {
					resp.Body.Close()
					if resp.StatusCode == http.StatusOK {
						version = resp.Header.Get("X-Gitlab-Meta")
					} else if resp.StatusCode == http.StatusUnauthorized {
						testErr = fmt.Errorf("authentication failed (HTTP 401)")
					} else {
						testErr = fmt.Errorf("forge returned HTTP %d", resp.StatusCode)
					}
				}
			}
		case "github", "":
			baseURL := entry.BaseURL
			if baseURL == "" {
				baseURL = "https://api.github.com"
			}
			httpClient := &http.Client{Timeout: 10 * time.Second}
			req, reqErr := http.NewRequestWithContext(ctx, http.MethodGet, strings.TrimRight(baseURL, "/")+"/user", nil)
			if reqErr != nil {
				testErr = reqErr
			} else {
				req.Header.Set("Accept", "application/vnd.github+json")
				if token != "" {
					req.Header.Set("Authorization", "Bearer "+token)
				}
				resp, doErr := httpClient.Do(req)
				if doErr != nil {
					testErr = doErr
				} else {
					scopeHeader := resp.Header.Get("X-OAuth-Scopes")
					resp.Body.Close()
					if resp.StatusCode == http.StatusOK {
						version = resp.Header.Get("X-GitHub-Media-Type")
						if scopeHeader != "" {
							for _, s := range strings.Split(scopeHeader, ",") {
								scopes = append(scopes, strings.TrimSpace(s))
							}
						}
					} else if resp.StatusCode == http.StatusUnauthorized {
						testErr = fmt.Errorf("authentication failed (HTTP 401)")
					} else {
						testErr = fmt.Errorf("forge returned HTTP %d", resp.StatusCode)
					}
				}
			}
		default:
			testErr = fmt.Errorf("unsupported forge kind %q", entry.Kind)
		}

		latencyMs := time.Since(start).Milliseconds()
		if testErr != nil {
			return &ForgeConnectionTestResult{
				OK:        false,
				LatencyMs: latencyMs,
				Error:     testErr.Error(),
			}, nil
		}
		return &ForgeConnectionTestResult{
			OK:        true,
			LatencyMs: latencyMs,
			Version:   version,
			Scopes:    scopes,
		}, nil
	}

	//ipc:method githubAuthCheck params:GitHubAuthCheckParams result:GitHubAuthCheckResult
	s.methods["github.authCheck"] = func(ctx context.Context, params json.RawMessage) (interface{}, error) {
		var p GitHubAuthCheckParams
		if err := json.Unmarshal(params, &p); err != nil {
			return nil, fmt.Errorf("invalid params: %w", err)
		}
		c, err := s.clientForUser(p.GitHubUser)
		if err != nil {
			return &GitHubAuthCheckResult{Valid: false, Error: err.Error()}, nil
		}
		info, err := c.CheckTokenScopes(ctx)
		if err != nil {
			return &GitHubAuthCheckResult{Valid: false, Error: err.Error()}, nil
		}
		return &GitHubAuthCheckResult{
			Valid:          info.Valid,
			Login:          info.Login,
			Scopes:         info.Scopes,
			MissingScopes:  info.MissingScopes,
			OrgMemberships: info.OrgMemberships,
			Resolution:     info.Resolution,
		}, nil
	}

	//ipc:method issueView params:IssueViewParams result:IssueDetail
	s.methods["issue.view"] = func(ctx context.Context, params json.RawMessage) (interface{}, error) {
		var p IssueViewParams
		if err := json.Unmarshal(params, &p); err != nil {
			return nil, fmt.Errorf("invalid params: %w", err)
		}
		c, err := s.resolveClientForRequest(ctx, p.GitHubUser, p.Owner, p.Repo)
		if err != nil {
			return nil, err
		}
		return gh.NewIssueService(c).GetIssue(ctx, p.Owner, p.Repo, p.Number)
	}

	//ipc:method pipelineCancelActiveForNetworkOutage params:none result:CancelActiveForNetworkOutageResult
	s.methods["pipeline.cancelActiveForNetworkOutage"] = func(_ context.Context, _ json.RawMessage) (interface{}, error) {
		if s.scheduler == nil {
			return CancelActiveForNetworkOutageResult{CancelledIssues: nil}, nil
		}
		cancelled := s.scheduler.CancelAllForNetworkOutage()
		return CancelActiveForNetworkOutageResult{CancelledIssues: cancelled}, nil
	}

	//ipc:method issueViewMany params:IssueViewManyParams result:IssueDetail[]
	s.methods["issue.viewMany"] = func(ctx context.Context, params json.RawMessage) (interface{}, error) {
		var p IssueViewManyParams
		if err := json.Unmarshal(params, &p); err != nil {
			return nil, fmt.Errorf("invalid params: %w", err)
		}
		c, err := s.resolveClientForRequest(ctx, p.GitHubUser, p.Owner, p.Repo)
		if err != nil {
			return nil, err
		}
		issues, err := gh.NewIssueService(c).GetIssuesByNumbers(ctx, p.Owner, p.Repo, p.Numbers)
		if err != nil {
			return nil, err
		}
		// Return as a slice in input order; numbers absent from the response
		// are skipped (deleted/inaccessible — same semantics as GetIssuesByNumbers).
		out := make([]*types.Issue, 0, len(p.Numbers))
		for _, n := range p.Numbers {
			if iss, ok := issues[n]; ok {
				out = append(out, iss)
			}
		}
		return out, nil
	}

	//ipc:method issueList params:IssueListParams result:IssueDetail[] skip
	s.methods["issue.list"] = func(ctx context.Context, params json.RawMessage) (interface{}, error) {
		var p IssueListParams
		if err := json.Unmarshal(params, &p); err != nil {
			return nil, fmt.Errorf("invalid params: %w", err)
		}
		c, err := s.resolveClientForRequest(ctx, p.GitHubUser, p.Owner, p.Repo)
		if err != nil {
			return nil, err
		}
		svc := gh.NewIssueService(c)
		if p.Epic > 0 {
			return svc.GetEpicProgressByNumber(ctx, p.Owner, p.Repo, p.Epic)
		}
		return svc.ListIssues(ctx, p.Owner, p.Repo, p.Labels)
	}

	//ipc:method prView params:PRViewParams result:unknown
	s.methods["pr.view"] = func(ctx context.Context, params json.RawMessage) (interface{}, error) {
		var p PRViewParams
		if err := json.Unmarshal(params, &p); err != nil {
			return nil, fmt.Errorf("invalid params: %w", err)
		}
		c, err := s.resolveClientForRequest(ctx, p.GitHubUser, p.Owner, p.Repo)
		if err != nil {
			return nil, err
		}
		return gh.NewPRService(c).GetPR(ctx, p.Owner, p.Repo, p.Number)
	}

	//ipc:method epicProgress params:EpicProgressParams result:EpicProgress
	s.methods["epic.progress"] = func(ctx context.Context, params json.RawMessage) (interface{}, error) {
		var p EpicProgressParams
		if err := json.Unmarshal(params, &p); err != nil {
			return nil, fmt.Errorf("invalid params: %w", err)
		}
		c, err := s.resolveClientForRequest(ctx, p.GitHubUser, p.Owner, p.Repo)
		if err != nil {
			return nil, err
		}
		return gh.NewIssueService(c).GetEpicProgressByNumber(ctx, p.Owner, p.Repo, p.Number)
	}

	//ipc:method pipelineStatus params:PipelineStatusParams result:PipelineStatus
	s.methods["pipeline.status"] = func(ctx context.Context, params json.RawMessage) (interface{}, error) {
		var p PipelineStatusParams
		if err := json.Unmarshal(params, &p); err != nil {
			return nil, fmt.Errorf("invalid params: %w", err)
		}
		c, err := s.clientForUser(p.GitHubUser)
		if err != nil {
			return nil, err
		}
		stateSvc := state.NewBoardStateService(c, p.Owner, p.ProjectNumber, gh.ParseOwnerType(p.OwnerType))
		stage, err := stateSvc.GetPipelineStage(ctx, p.ItemID)
		if err != nil {
			return nil, err
		}
		return map[string]string{"stage": string(stage)}, nil
	}

	//ipc:method executionList params:none result:ExecutionInfo[]
	s.methods["execution.list"] = func(_ context.Context, _ json.RawMessage) (interface{}, error) {
		if s.execMgr == nil {
			return []interface{}{}, nil
		}
		return s.execMgr.ListRunning(), nil
	}

	// --- Intelligence methods ---

	complexityEstimator := complexity.NewEstimator()
	modelRouter := routing.NewRouter(s.platformClient, s.workspaceRoot)
	failureClassifier := failure.NewClassifier()
	//ipc:method intelligenceComplexity params:ComplexityEstimateParams result:ComplexityResult skip
	s.methods["intelligence.complexity"] = func(_ context.Context, params json.RawMessage) (interface{}, error) {
		var p ComplexityEstimateParams
		if err := json.Unmarshal(params, &p); err != nil {
			return nil, fmt.Errorf("invalid params: %w", err)
		}
		return complexityEstimator.Estimate(complexity.Input{
			Title:             p.Title,
			Body:              p.Body,
			Labels:            p.Labels,
			FileCountEstimate: p.FileCountEstimate,
			SubIssueCount:     p.SubIssueCount,
		}), nil
	}

	//ipc:method intelligenceRoute params:ModelRouteParams result:ModelRouteResult
	s.methods["intelligence.route"] = func(ctx context.Context, params json.RawMessage) (interface{}, error) {
		var p ModelRouteParams
		if err := json.Unmarshal(params, &p); err != nil {
			return nil, fmt.Errorf("invalid params: %w", err)
		}
		return modelRouter.Route(ctx, p.Stage, complexity.Score{Value: p.ComplexityScore}), nil
	}

	//ipc:method intelligenceClassify params:FailureClassifyParams result:FailureClassification
	s.methods["intelligence.classify"] = func(_ context.Context, params json.RawMessage) (interface{}, error) {
		var p FailureClassifyParams
		if err := json.Unmarshal(params, &p); err != nil {
			return nil, fmt.Errorf("invalid params: %w", err)
		}
		return failureClassifier.Classify(p.Stage, p.ExitCode, p.Stderr), nil
	}

	//ipc:method intelligenceCost params:CostEstimateParams result:CostEstimate
	s.methods["intelligence.cost"] = func(_ context.Context, params json.RawMessage) (interface{}, error) {
		var p CostEstimateParams
		if err := json.Unmarshal(params, &p); err != nil {
			return nil, fmt.Errorf("invalid params: %w", err)
		}
		return tokens.EstimateCost(p.Stages, p.ComplexityScore), nil
	}

	// --- Platform methods ---

	//ipc:method platformStatus params:none result:PlatformStatus
	s.methods["platform.status"] = func(_ context.Context, _ json.RawMessage) (interface{}, error) {
		if s.platformClient == nil {
			return map[string]interface{}{
				"mode":    string(platform.ModeOffline),
				"message": "platform client not configured",
			}, nil
		}
		result := map[string]interface{}{
			"mode": string(s.platformClient.Mode()),
		}
		if s.licenseSvc != nil {
			result["tier"] = s.licenseSvc.CurrentTier()
		}
		return result, nil
	}

	//ipc:method platformLicense params:none result:LicenseInfo
	s.methods["platform.license"] = func(ctx context.Context, _ json.RawMessage) (interface{}, error) {
		if s.licenseSvc == nil {
			return platform.CommunityLicenseInfo(), nil
		}
		info, err := s.licenseSvc.Validate(ctx)
		if err != nil {
			return nil, err
		}
		return info, nil
	}

	//ipc:method platformResolveSkill params:PlatformResolveSkillParams result:CachedSkill
	s.methods["platform.resolveSkill"] = func(ctx context.Context, params json.RawMessage) (interface{}, error) {
		if s.skillSvc == nil {
			return nil, fmt.Errorf("platform client not configured")
		}
		var p PlatformResolveSkillParams
		if err := json.Unmarshal(params, &p); err != nil {
			return nil, fmt.Errorf("invalid params: %w", err)
		}
		return s.skillSvc.Resolve(ctx, p.SkillID, &platform.SkillResolveOptions{
			ComplexityScore: p.ComplexityScore,
			IssueType:       p.IssueType,
			SizeLabel:       p.SizeLabel,
		})
	}

	//ipc:method platformValidateLicense params:PlatformValidateLicenseParams result:LicenseInfo
	s.methods["platform.validateLicense"] = func(ctx context.Context, params json.RawMessage) (interface{}, error) {
		if s.licenseSvc == nil {
			return nil, fmt.Errorf("platform client not configured")
		}
		var p PlatformValidateLicenseParams
		if err := json.Unmarshal(params, &p); err != nil {
			return nil, fmt.Errorf("invalid params: %w", err)
		}
		// Default path: validate the session key set at Client construction
		// (cached). When the caller passes a DIFFERENT non-empty key — the
		// "Activate License" flow verifying a key before it's persisted — validate
		// that arbitrary key directly, bypassing the session cache so the result
		// reflects the entered key, not the current session license.
		if p.LicenseKey != "" && p.LicenseKey != s.licenseSvc.ConfiguredKey() {
			return s.licenseSvc.ValidateKey(ctx, p.LicenseKey)
		}
		return s.licenseSvc.Validate(ctx)
	}

	//ipc:method platformStartTrial params:PlatformStartTrialParams result:TrialResult
	s.methods["platform.startTrial"] = func(ctx context.Context, params json.RawMessage) (interface{}, error) {
		if s.licenseSvc == nil {
			return nil, fmt.Errorf("platform client not configured")
		}
		var p PlatformStartTrialParams
		if err := json.Unmarshal(params, &p); err != nil {
			return nil, fmt.Errorf("invalid params: %w", err)
		}
		// The device-flow JWT is applied as a per-call bearer inside StartTrial and
		// is never logged here. A typed *platform.TrialError (NOT_ELIGIBLE / 401 /
		// transport) propagates to the TS command for a precise message.
		return s.licenseSvc.StartTrial(ctx, p.AccessToken)
	}

	//ipc:method platformSubmitAnalytics params:PlatformSubmitAnalyticsParams result:StatusOK
	s.methods["platform.submitAnalytics"] = func(ctx context.Context, params json.RawMessage) (interface{}, error) {
		var p PlatformSubmitAnalyticsParams
		if err := json.Unmarshal(params, &p); err != nil {
			return nil, fmt.Errorf("invalid params: %w", err)
		}
		if s.analyticsSvc != nil {
			// Fire-and-forget: buffer locally, return immediately
			s.analyticsSvc.Ingest(ctx, "", 0, []platform.AnalyticsEvent{{
				Type:      p.EventType,
				Timestamp: time.Now(),
				Data:      p.Payload,
			}})
		}
		return map[string]string{"status": "ok"}, nil
	}

	//ipc:method platformGetUsageSummary params:none result:UsageSummaryResult
	s.methods["platform.getUsageSummary"] = func(ctx context.Context, _ json.RawMessage) (interface{}, error) {
		if s.analyticsSvc == nil {
			return nil, fmt.Errorf("platform client not configured")
		}
		return s.analyticsSvc.GetUsageSummary(ctx)
	}

	//ipc:method platformGetCostAnalytics params:PlatformCostAnalyticsParams result:CostAnalyticsResult
	s.methods["platform.getCostAnalytics"] = func(ctx context.Context, params json.RawMessage) (interface{}, error) {
		if s.analyticsSvc == nil {
			return nil, fmt.Errorf("analytics service unavailable")
		}
		var p PlatformCostAnalyticsParams
		_ = json.Unmarshal(params, &p)
		return s.analyticsSvc.GetCostAnalytics(ctx, p.StartDate, p.EndDate)
	}

	//ipc:method platformGetAnalyticsHealth params:none result:AnalyticsHealthResult
	s.methods["platform.getAnalyticsHealth"] = func(ctx context.Context, _ json.RawMessage) (interface{}, error) {
		if s.analyticsSvc == nil {
			return nil, fmt.Errorf("analytics service unavailable")
		}
		return s.analyticsSvc.GetAnalyticsHealth(ctx)
	}

	//ipc:method platformGetAnalyticsRuns params:PlatformAnalyticsRunsParams result:AnalyticsRunsResult
	s.methods["platform.getAnalyticsRuns"] = func(ctx context.Context, params json.RawMessage) (interface{}, error) {
		if s.analyticsSvc == nil {
			return nil, fmt.Errorf("analytics service unavailable")
		}
		var p PlatformAnalyticsRunsParams
		_ = json.Unmarshal(params, &p)
		return s.analyticsSvc.GetAnalyticsRuns(ctx, p.StartDate, p.EndDate, p.Cursor, p.Outcome, p.Branch, p.Limit)
	}

	//ipc:method platformGetAnalyticsTrends params:PlatformGetAnalyticsTrendsParams result:AnalyticsTrendsResult
	s.methods["platform.getAnalyticsTrends"] = func(ctx context.Context, params json.RawMessage) (interface{}, error) {
		if s.analyticsSvc == nil {
			return nil, fmt.Errorf("analytics service unavailable")
		}
		var p PlatformGetAnalyticsTrendsParams
		_ = json.Unmarshal(params, &p)
		if p.Period == "" {
			p.Period = "30d"
		}
		return s.analyticsSvc.GetAnalyticsTrends(ctx, p.Period)
	}

	//ipc:method platformAuditGenerateReport params:PlatformAuditGenerateReportParams result:ComplianceReportResult
	s.methods["platform.auditGenerateReport"] = func(ctx context.Context, params json.RawMessage) (interface{}, error) {
		if s.complianceSvc == nil {
			return nil, fmt.Errorf("compliance service unavailable")
		}
		var p PlatformAuditGenerateReportParams
		if err := json.Unmarshal(params, &p); err != nil {
			return nil, fmt.Errorf("invalid params: %w", err)
		}
		return s.complianceSvc.GenerateReport(ctx, p.ReportType, p.StartDate, p.EndDate, p.Format)
	}

	//ipc:method platformAuditListReports params:PlatformAuditListReportsParams result:ComplianceReportsPage
	s.methods["platform.auditListReports"] = func(ctx context.Context, params json.RawMessage) (interface{}, error) {
		if s.complianceSvc == nil {
			return nil, fmt.Errorf("compliance service unavailable")
		}
		var p PlatformAuditListReportsParams
		if err := json.Unmarshal(params, &p); err != nil {
			return nil, fmt.Errorf("invalid params: %w", err)
		}
		return s.complianceSvc.ListReports(ctx, p.Cursor, p.Limit)
	}

	//ipc:method platformAuditGetReport params:PlatformAuditGetReportParams result:ComplianceReportDetail
	s.methods["platform.auditGetReport"] = func(ctx context.Context, params json.RawMessage) (interface{}, error) {
		if s.complianceSvc == nil {
			return nil, fmt.Errorf("compliance service unavailable")
		}
		var p PlatformAuditGetReportParams
		if err := json.Unmarshal(params, &p); err != nil {
			return nil, fmt.Errorf("invalid params: %w", err)
		}
		return s.complianceSvc.GetReport(ctx, p.ReportID)
	}

	//ipc:method auditGetRetentionConfig params:AuditGetRetentionConfigParams result:RetentionConfig
	s.methods["audit.getRetentionConfig"] = func(ctx context.Context, params json.RawMessage) (interface{}, error) {
		if s.auditRetentionSvc == nil {
			return nil, fmt.Errorf("audit retention service unavailable")
		}
		return s.auditRetentionSvc.GetRetentionConfig(ctx)
	}

	//ipc:method auditUpdateRetentionConfig params:AuditUpdateRetentionConfigParams result:RetentionConfig
	s.methods["audit.updateRetentionConfig"] = func(ctx context.Context, params json.RawMessage) (interface{}, error) {
		if s.auditRetentionSvc == nil {
			return nil, fmt.Errorf("audit retention service unavailable")
		}
		var p AuditUpdateRetentionConfigParams
		if err := json.Unmarshal(params, &p); err != nil {
			return nil, fmt.Errorf("invalid params: %w", err)
		}
		if p.RetentionDays < 1 || p.RetentionDays > 3650 {
			return nil, fmt.Errorf("retentionDays must be between 1 and 3650")
		}
		return s.auditRetentionSvc.UpdateRetentionConfig(ctx, p.RetentionDays)
	}

	//ipc:method auditVerifyIntegrity params:AuditVerifyIntegrityParams result:IntegrityResult
	s.methods["audit.verifyIntegrity"] = func(ctx context.Context, params json.RawMessage) (interface{}, error) {
		if s.auditRetentionSvc == nil {
			return nil, fmt.Errorf("audit retention service unavailable")
		}
		var p AuditVerifyIntegrityParams
		if err := json.Unmarshal(params, &p); err != nil {
			return nil, fmt.Errorf("invalid params: %w", err)
		}
		if p.WindowDays != 30 && p.WindowDays != 90 && p.WindowDays != 365 {
			return nil, fmt.Errorf("windowDays must be 30, 90, or 365")
		}
		return s.auditRetentionSvc.VerifyIntegrity(ctx, p.WindowDays)
	}

	//ipc:method platformSyncTelemetry params:PlatformSyncTelemetryParams result:PlatformSyncTelemetryResult
	s.methods["platform.syncTelemetry"] = func(ctx context.Context, params json.RawMessage) (interface{}, error) {
		var p PlatformSyncTelemetryParams
		if err := json.Unmarshal(params, &p); err != nil {
			return nil, fmt.Errorf("invalid params: %w", err)
		}
		if s.analyticsSvc == nil {
			return nil, fmt.Errorf("platform client not configured")
		}
		if s.workspaceRoot == "" {
			return nil, fmt.Errorf("workspace root not set")
		}

		limit := p.Limit
		if limit <= 0 {
			limit = 50
		}
		daysBack := p.DaysBack
		if daysBack <= 0 {
			daysBack = 7
		}

		hw := state.NewHistoryWriter(s.workspaceRoot)
		records, err := hw.ReadRecentV2(limit, daysBack)
		if err != nil {
			return nil, fmt.Errorf("read history: %w", err)
		}

		// The repo param is forwarded to SyncTelemetry and applied to all
		// records as the ExecutionHistoryRunRecord.Repo value.

		syncCtx, cancel := context.WithTimeout(ctx, 2*time.Minute)
		defer cancel()

		res := s.analyticsSvc.SyncTelemetry(syncCtx, records, p.Repo)
		return PlatformSyncTelemetryResult{
			Synced: res.Synced,
			Failed: res.Failed,
			Errors: res.Errors,
		}, nil
	}

	//ipc:method platformGetTeamMembers params:none result:TeamMemberResult[] nullable
	s.methods["platform.getTeamMembers"] = func(ctx context.Context, _ json.RawMessage) (interface{}, error) {
		if s.teamSvc == nil {
			return nil, fmt.Errorf("platform client not configured")
		}
		return s.teamSvc.GetMembers(ctx)
	}

	//ipc:method platformCreatePortalSession params:none result:PortalSessionResult
	s.methods["platform.createPortalSession"] = func(ctx context.Context, _ json.RawMessage) (interface{}, error) {
		if s.billingSvc == nil {
			return nil, fmt.Errorf("platform client not configured")
		}
		return s.billingSvc.CreatePortalSession(ctx)
	}

	//ipc:method platformHealthCheck params:none result:HealthResponse
	s.methods["platform.healthCheck"] = func(ctx context.Context, _ json.RawMessage) (interface{}, error) {
		if s.platformClient == nil {
			return map[string]interface{}{"status": "offline", "mode": "offline"}, nil
		}
		resp, err := s.platformClient.API().GetHealthWithResponse(ctx)
		if err != nil {
			return nil, fmt.Errorf("health check failed: %w", err)
		}
		if resp.JSON200 == nil {
			return nil, fmt.Errorf("unexpected health response: %d", resp.StatusCode())
		}
		return resp.JSON200, nil
	}

	//ipc:method platformAuthDeviceCode params:none result:{device_code:string;expires_in:number;interval:number;user_code:string;verification_uri:string}
	s.methods["platform.authDeviceCode"] = func(ctx context.Context, _ json.RawMessage) (interface{}, error) {
		if s.platformClient == nil {
			return nil, fmt.Errorf("platform client not configured")
		}
		resp, err := s.platformClient.API().AuthDeviceCodeWithResponse(ctx)
		if err != nil {
			return nil, fmt.Errorf("authDeviceCode: %w", err)
		}
		if resp.JSON200 == nil {
			return nil, fmt.Errorf("authDeviceCode: unexpected status %d", resp.StatusCode())
		}
		return resp.JSON200, nil
	}

	//ipc:method platformAuthDeviceToken params:PlatformAuthDeviceTokenParams result:unknown
	s.methods["platform.authDeviceToken"] = func(ctx context.Context, params json.RawMessage) (interface{}, error) {
		if s.platformClient == nil {
			return nil, fmt.Errorf("platform client not configured")
		}
		var p PlatformAuthDeviceTokenParams
		if err := json.Unmarshal(params, &p); err != nil {
			return nil, fmt.Errorf("invalid params: %w", err)
		}
		if p.DeviceCode == "" {
			return nil, fmt.Errorf("deviceCode is required")
		}
		resp, err := s.platformClient.API().AuthDeviceTokenWithResponse(ctx, platformapi.AuthDeviceTokenJSONRequestBody{
			DeviceCode: p.DeviceCode,
		})
		if err != nil {
			return nil, fmt.Errorf("authDeviceToken: %w", err)
		}
		// JSON200 is a union type (AuthTokenResponse | AuthPendingResponse);
		// pass through the raw response body for the caller to discriminate.
		if resp.StatusCode() != 200 {
			return nil, fmt.Errorf("authDeviceToken: unexpected status %d", resp.StatusCode())
		}
		var result interface{}
		if err := json.Unmarshal(resp.Body, &result); err != nil {
			return nil, fmt.Errorf("authDeviceToken: decode body: %w", err)
		}
		return result, nil
	}

	//ipc:method platformAuthGithub params:PlatformAuthGithubParams result:{access_token:string;expires_in:number;refresh_token:string;status:string;token_type:string}
	s.methods["platform.authGithub"] = func(ctx context.Context, params json.RawMessage) (interface{}, error) {
		if s.platformClient == nil {
			return nil, fmt.Errorf("platform client not configured")
		}
		var p PlatformAuthGithubParams
		if err := json.Unmarshal(params, &p); err != nil {
			return nil, fmt.Errorf("invalid params: %w", err)
		}
		if p.GithubAccessToken == "" {
			return nil, fmt.Errorf("githubAccessToken is required")
		}
		resp, err := s.platformClient.API().AuthGithubWithResponse(ctx, platformapi.AuthGithubJSONRequestBody{
			GithubAccessToken: p.GithubAccessToken,
		})
		if err != nil {
			return nil, fmt.Errorf("authGithub: %w", err)
		}
		if resp.JSON200 == nil {
			return nil, fmt.Errorf("authGithub: unexpected status %d", resp.StatusCode())
		}
		return resp.JSON200, nil
	}

	//ipc:method platformAuthRefresh params:PlatformAuthRefreshParams result:{access_token:string;expires_in:number;refresh_token:string;status:string;token_type:string}
	s.methods["platform.authRefresh"] = func(ctx context.Context, params json.RawMessage) (interface{}, error) {
		if s.platformClient == nil {
			return nil, fmt.Errorf("platform client not configured")
		}
		var p PlatformAuthRefreshParams
		if err := json.Unmarshal(params, &p); err != nil {
			return nil, fmt.Errorf("invalid params: %w", err)
		}
		if p.RefreshToken == "" {
			return nil, fmt.Errorf("refreshToken is required")
		}
		resp, err := s.platformClient.API().AuthRefreshWithResponse(ctx, platformapi.AuthRefreshJSONRequestBody{
			RefreshToken: p.RefreshToken,
		})
		if err != nil {
			return nil, fmt.Errorf("authRefresh: %w", err)
		}
		if resp.JSON200 == nil {
			return nil, fmt.Errorf("authRefresh: unexpected status %d", resp.StatusCode())
		}
		return resp.JSON200, nil
	}

	//ipc:method platformAuthSignout params:PlatformAuthSignoutParams result:{message:string;status:string}
	s.methods["platform.authSignout"] = func(ctx context.Context, params json.RawMessage) (interface{}, error) {
		if s.platformClient == nil {
			return nil, fmt.Errorf("platform client not configured")
		}
		var p PlatformAuthSignoutParams
		if err := json.Unmarshal(params, &p); err != nil {
			return nil, fmt.Errorf("invalid params: %w", err)
		}
		if p.RefreshToken == "" {
			return nil, fmt.Errorf("refreshToken is required")
		}
		resp, err := s.platformClient.API().AuthSignoutWithResponse(ctx, platformapi.AuthSignoutJSONRequestBody{
			RefreshToken: p.RefreshToken,
		})
		if err != nil {
			return nil, fmt.Errorf("authSignout: %w", err)
		}
		if resp.JSON200 == nil {
			return nil, fmt.Errorf("authSignout: unexpected status %d", resp.StatusCode())
		}
		return resp.JSON200, nil
	}

	// --- Auth methods ---

	//ipc:method authExchangeGitHub params:AuthExchangeGitHubParams result:AuthTokenResponse
	s.methods["auth.exchangeGitHub"] = func(ctx context.Context, params json.RawMessage) (interface{}, error) {
		if s.authSvc == nil {
			return nil, fmt.Errorf("platform client not configured")
		}
		var p AuthExchangeGitHubParams
		if err := json.Unmarshal(params, &p); err != nil {
			return nil, fmt.Errorf("invalid params: %w", err)
		}
		return s.authSvc.ExchangeGitHubToken(ctx, p.GithubToken)
	}

	//ipc:method authDeviceFlowStart params:none result:AuthDeviceCodeResult
	s.methods["auth.deviceFlowStart"] = func(ctx context.Context, _ json.RawMessage) (interface{}, error) {
		if s.authSvc == nil {
			return nil, fmt.Errorf("platform client not configured")
		}
		return s.authSvc.StartDeviceFlow(ctx)
	}

	//ipc:method authDeviceFlowPoll params:AuthDeviceFlowPollParams result:AuthDeviceFlowPollResult
	s.methods["auth.deviceFlowPoll"] = func(ctx context.Context, params json.RawMessage) (interface{}, error) {
		if s.authSvc == nil {
			return nil, fmt.Errorf("platform client not configured")
		}
		var p AuthDeviceFlowPollParams
		if err := json.Unmarshal(params, &p); err != nil {
			return nil, fmt.Errorf("invalid params: %w", err)
		}
		tokenResp, pendingResp, err := s.authSvc.PollDeviceToken(ctx, p.DeviceCode)
		if err != nil {
			return nil, err
		}
		if tokenResp != nil {
			return map[string]interface{}{
				"status":        string(tokenResp.Status),
				"access_token":  tokenResp.AccessToken,
				"refresh_token": tokenResp.RefreshToken,
				"expires_in":    tokenResp.ExpiresIn,
				"token_type":    string(tokenResp.TokenType),
			}, nil
		}
		return map[string]interface{}{
			"status": string(pendingResp.Status),
		}, nil
	}

	//ipc:method authRefresh params:AuthRefreshParams result:AuthTokenResponse
	s.methods["auth.refresh"] = func(ctx context.Context, params json.RawMessage) (interface{}, error) {
		if s.authSvc == nil {
			return nil, fmt.Errorf("platform client not configured")
		}
		var p AuthRefreshParams
		if err := json.Unmarshal(params, &p); err != nil {
			return nil, fmt.Errorf("invalid params: %w", err)
		}
		return s.authSvc.RefreshToken(ctx, p.RefreshToken)
	}

	// --- Board mutations ---

	//ipc:method boardUpdateStatus params:BoardUpdateStatusParams result:void
	s.methods["board.updateStatus"] = func(ctx context.Context, params json.RawMessage) (interface{}, error) {
		var p BoardUpdateStatusParams
		if err := json.Unmarshal(params, &p); err != nil {
			return nil, fmt.Errorf("invalid params: %w", err)
		}
		c, err := s.clientForUser(p.GitHubUser)
		if err != nil {
			return nil, err
		}
		stateSvc := state.NewBoardStateService(c, p.Owner, p.ProjectNumber, gh.ParseOwnerType(p.OwnerType))
		if err := stateSvc.UpdateStatus(ctx, p.ItemID, p.Status); err != nil {
			return nil, err
		}
		// Emit tree update event so TypeScript tree providers react immediately
		if tabId := statusToTabId(p.Status); tabId != "" {
			s.Emit("tree."+tabId+".update", TreeUpdateEvent{
				Owner:         p.Owner,
				ProjectNumber: p.ProjectNumber,
				ChangedItemID: p.ItemID,
				NewStatus:     p.Status,
			})
		}
		return map[string]string{"status": "ok"}, nil
	}

	// --- Issue mutations ---

	//ipc:method issueCreateSubIssue params:IssueCreateSubIssueParams result:IssueDetail
	s.methods["issue.createSubIssue"] = func(ctx context.Context, params json.RawMessage) (interface{}, error) {
		var p IssueCreateSubIssueParams
		if err := json.Unmarshal(params, &p); err != nil {
			return nil, fmt.Errorf("invalid params: %w", err)
		}
		c, err := s.resolveClientForRequest(ctx, p.GitHubUser, p.Owner, p.Repo)
		if err != nil {
			return nil, err
		}
		svc := gh.NewIssueService(c)
		// Get epic to find its node ID
		epic, err := svc.GetIssue(ctx, p.Owner, p.Repo, p.EpicNumber)
		if err != nil {
			return nil, fmt.Errorf("fetch epic #%d: %w", p.EpicNumber, err)
		}
		// Get repo ID for creating the issue
		repoID, err := c.GetRepositoryID(ctx, p.Owner, p.Repo)
		if err != nil {
			return nil, fmt.Errorf("get repo ID: %w", err)
		}
		// Create issue
		created, err := svc.CreateIssue(ctx, repoID, p.Title, p.Body, nil)
		if err != nil {
			return nil, fmt.Errorf("create issue: %w", err)
		}
		// Link as sub-issue
		if err := svc.AddSubIssue(ctx, epic.NodeID, created.NodeID); err != nil {
			return nil, fmt.Errorf("link sub-issue: %w", err)
		}
		return created, nil
	}

	//ipc:method issueLinkSubIssue params:IssueLinkSubIssueParams result:void
	s.methods["issue.linkSubIssue"] = func(ctx context.Context, params json.RawMessage) (interface{}, error) {
		var p IssueLinkSubIssueParams
		if err := json.Unmarshal(params, &p); err != nil {
			return nil, fmt.Errorf("invalid params: %w", err)
		}
		c, err := s.resolveClientForRequest(ctx, p.GitHubUser, p.Owner, p.Repo)
		if err != nil {
			return nil, err
		}
		svc := gh.NewIssueService(c)
		epic, err := svc.GetIssue(ctx, p.Owner, p.Repo, p.EpicNumber)
		if err != nil {
			return nil, fmt.Errorf("fetch epic: %w", err)
		}
		child, err := svc.GetIssue(ctx, p.Owner, p.Repo, p.IssueNumber)
		if err != nil {
			return nil, fmt.Errorf("fetch child issue: %w", err)
		}
		if err := svc.AddSubIssue(ctx, epic.NodeID, child.NodeID); err != nil {
			return nil, err
		}
		return map[string]string{"status": "ok"}, nil
	}

	// --- Epic completion ---

	//ipc:method epicCheckCompletion params:EpicCheckCompletionParams result:{complete:boolean;total:number;closed:number}
	s.methods["epic.checkCompletion"] = func(ctx context.Context, params json.RawMessage) (interface{}, error) {
		var p EpicCheckCompletionParams
		if err := json.Unmarshal(params, &p); err != nil {
			return nil, fmt.Errorf("invalid params: %w", err)
		}
		c, err := s.resolveClientForRequest(ctx, p.GitHubUser, p.Owner, p.Repo)
		if err != nil {
			return nil, err
		}
		progress, err := gh.NewIssueService(c).GetEpicProgressByNumber(ctx, p.Owner, p.Repo, p.Number)
		if err != nil {
			return nil, err
		}
		return map[string]interface{}{
			"complete": progress.Open == 0 && progress.Total > 0,
			"total":    progress.Total,
			"closed":   progress.Closed,
		}, nil
	}

	//ipc:method epicTransitionStatus params:EpicTransitionStatusParams result:{epicNumber:number;newStatus:string;epicSynced:boolean;subIssueTotal:number;subIssueMoved:number}
	s.methods["epic.transitionStatus"] = func(ctx context.Context, params json.RawMessage) (interface{}, error) {
		var p EpicTransitionStatusParams
		if err := json.Unmarshal(params, &p); err != nil {
			return nil, fmt.Errorf("invalid params: %w", err)
		}
		c, err := s.resolveClientForRequest(ctx, p.GitHubUser, p.Owner, p.Repo)
		if err != nil {
			return nil, err
		}
		epicSvc := gh.NewEpicService(c)
		result, err := epicSvc.TransitionStatus(ctx, p.Owner, p.Repo, p.EpicNumber, p.ProjectNumber, p.NewStatus)
		if err != nil {
			return nil, err
		}
		// Emit tree update so all status tabs refresh
		s.Emit("tree.ready.update", TreeUpdateEvent{Owner: p.Owner, ProjectNumber: p.ProjectNumber, NewStatus: p.NewStatus})
		s.Emit("tree.backlog.update", TreeUpdateEvent{Owner: p.Owner, ProjectNumber: p.ProjectNumber, NewStatus: p.NewStatus})
		s.Emit("tree.in-progress.update", TreeUpdateEvent{Owner: p.Owner, ProjectNumber: p.ProjectNumber, NewStatus: p.NewStatus})
		s.Emit("tree.in-review.update", TreeUpdateEvent{Owner: p.Owner, ProjectNumber: p.ProjectNumber, NewStatus: p.NewStatus})
		return result, nil
	}

	// --- Branch cleanup ---

	s.methods["branch.cleanup"] = func(ctx context.Context, params json.RawMessage) (interface{}, error) {
		var p struct {
			Branch  string `json:"branch"`
			WorkDir string `json:"workDir,omitempty"`
		}
		if err := json.Unmarshal(params, &p); err != nil {
			return nil, fmt.Errorf("invalid params: %w", err)
		}
		if p.Branch == "" {
			return nil, fmt.Errorf("branch is required")
		}
		gitSvc, err := s.gitService(p.WorkDir)
		if err != nil {
			return nil, err
		}
		if err := gitSvc.BranchCleanup(p.Branch); err != nil {
			return map[string]interface{}{"success": false, "error": err.Error()}, nil
		}
		return map[string]interface{}{"success": true, "branch": p.Branch}, nil
	}

	s.methods["epic.createPR"] = func(ctx context.Context, params json.RawMessage) (interface{}, error) {
		var p struct {
			Owner      string `json:"owner"`
			Repo       string `json:"repo"`
			EpicNumber int    `json:"epicNumber"`
			BaseBranch string `json:"baseBranch,omitempty"`
			GitHubUser string `json:"githubUser,omitempty"`
		}
		if err := json.Unmarshal(params, &p); err != nil {
			return nil, fmt.Errorf("invalid params: %w", err)
		}
		if p.BaseBranch == "" {
			p.BaseBranch = "main"
		}
		c, err := s.resolveClientForRequest(ctx, p.GitHubUser, p.Owner, p.Repo)
		if err != nil {
			return nil, err
		}

		// Find epic branch
		gitSvc, err := s.gitService("")
		if err != nil {
			return nil, err
		}
		epicBranch, err := gitSvc.FindEpicBranch(p.EpicNumber)
		if err != nil {
			return nil, err
		}

		// Get epic title
		epicIssue, err := gh.NewIssueService(c).GetIssue(ctx, p.Owner, p.Repo, p.EpicNumber)
		if err != nil {
			return nil, fmt.Errorf("fetch epic: %w", err)
		}

		prSvc := gh.NewPRService(c)
		result, err := prSvc.CreateEpicPR(ctx, p.Owner, p.Repo, p.EpicNumber, epicIssue.Title, epicBranch, p.BaseBranch)
		if err != nil {
			return nil, err
		}
		return result, nil
	}

	s.methods["epic.mergePR"] = func(ctx context.Context, params json.RawMessage) (interface{}, error) {
		var p struct {
			Owner      string `json:"owner"`
			Repo       string `json:"repo"`
			EpicNumber int    `json:"epicNumber"`
			PRNodeID   string `json:"prNodeId"`
			EpicBranch string `json:"epicBranch"`
			GitHubUser string `json:"githubUser,omitempty"`
		}
		if err := json.Unmarshal(params, &p); err != nil {
			return nil, fmt.Errorf("invalid params: %w", err)
		}
		c, err := s.resolveClientForRequest(ctx, p.GitHubUser, p.Owner, p.Repo)
		if err != nil {
			return nil, err
		}

		prSvc := gh.NewPRService(c)
		if err := prSvc.MergeEpicPR(ctx, p.Owner, p.Repo, p.PRNodeID, p.EpicBranch); err != nil {
			return nil, err
		}

		// Cleanup local branch + remote tracking refs
		gitSvc, err := s.gitService("")
		if err == nil {
			_ = gitSvc.BranchCleanup(p.EpicBranch)
		}

		return map[string]interface{}{
			"success":    true,
			"epicNumber": p.EpicNumber,
			"action":     "merged",
		}, nil
	}

	// --- Pipeline execution ---

	//ipc:method pipelineRun params:PipelineRunParams result:RunPipelineResult skip
	s.methods["pipeline.run"] = func(ctx context.Context, params json.RawMessage) (interface{}, error) {
		var p PipelineRunParams
		if err := json.Unmarshal(params, &p); err != nil {
			return nil, fmt.Errorf("invalid params: %w", err)
		}
		if s.scheduler == nil {
			return nil, errors.New(errSchedulerNotConfigured)
		}
		// Queue the issue for execution and run it
		parts := []string{p.Owner, p.Repo}
		repo := strings.Join(parts, "/")

		// Stage lifecycle callbacks (OnStageStart, OnStageComplete, OnPipelineComplete,
		// OnStateChanged, OnPhaseDetected) are registered once at server init in
		// registerMethods() to avoid overwriting them on concurrent calls (#3348).

		// OnEpicComplete: deterministic epic PR creation, merge, and branch cleanup.
		// Runs only on successful pipeline completion (all sub-issues closed).
		prSvc := gh.NewPRService(s.client)
		s.scheduler.OnEpicComplete(func(cbRepo string, epicNumber int) {
			ctx := context.Background()
			owner, repo := splitOwnerRepo(cbRepo)
			if owner == "" || repo == "" {
				log.Printf("epic #%d: invalid repo format %q", epicNumber, cbRepo)
				return
			}

			// 1. Find the epic branch on remote
			gitSvc, err := s.gitService("")
			if err != nil {
				log.Printf("epic #%d: git service: %v", epicNumber, err)
				return
			}
			epicBranch, err := gitSvc.FindEpicBranch(epicNumber)
			if err != nil {
				log.Printf("epic #%d: no epic branch found, skipping PR creation: %v", epicNumber, err)
				return
			}

			// 2. Get epic title for PR
			issueSvc := gh.NewIssueService(s.client)
			epicIssue, err := issueSvc.GetIssue(ctx, owner, repo, epicNumber)
			if err != nil {
				log.Printf("epic #%d: failed to fetch issue: %v", epicNumber, err)
				return
			}

			// 3. Create epic PR (epic branch → main)
			baseBranch := "main"
			result, err := prSvc.CreateEpicPR(ctx, owner, repo, epicNumber, epicIssue.Title, epicBranch, baseBranch)
			if err != nil {
				log.Printf("epic #%d: failed to create epic PR: %v", epicNumber, err)
				s.Emit("epic.prFailed", map[string]interface{}{
					"repo":       cbRepo,
					"epicNumber": epicNumber,
					"error":      err.Error(),
				})
				return
			}

			log.Printf("epic #%d: PR %s (%s)", epicNumber, result.PRURL, result.Action)

			if result.Action == "already_merged" {
				// PR was already merged — just cleanup branches
				log.Printf("epic #%d: already merged, cleaning up branches", epicNumber)
				_ = gitSvc.BranchCleanup(epicBranch)
				s.Emit("epic.completed", map[string]interface{}{
					"repo":       cbRepo,
					"epicNumber": epicNumber,
					"action":     "already_merged",
					"prUrl":      result.PRURL,
				})
				return
			}

			// 4. Merge the epic PR (MERGE strategy to preserve commit history)
			prNodeID := result.PRNodeID
			if prNodeID == "" {
				log.Printf("epic #%d: no PR node ID, cannot auto-merge", epicNumber)
				s.Emit("epic.prCreated", map[string]interface{}{
					"repo":       cbRepo,
					"epicNumber": epicNumber,
					"prUrl":      result.PRURL,
					"prNumber":   result.PRNumber,
					"action":     "created_manual_merge_required",
				})
				return
			}

			if err := prSvc.MergeEpicPR(ctx, owner, repo, prNodeID, epicBranch); err != nil {
				log.Printf("epic #%d: failed to merge epic PR: %v", epicNumber, err)
				s.Emit("epic.mergeFailed", map[string]interface{}{
					"repo":       cbRepo,
					"epicNumber": epicNumber,
					"prUrl":      result.PRURL,
					"error":      err.Error(),
				})
				return
			}

			// 5. Cleanup: delete epic branch locally + remote tracking refs
			if err := gitSvc.BranchCleanup(epicBranch); err != nil {
				log.Printf("epic #%d: branch cleanup warning: %v", epicNumber, err)
			}

			log.Printf("epic #%d: completed — PR merged, branches cleaned", epicNumber)
			s.Emit("epic.completed", map[string]interface{}{
				"repo":       cbRepo,
				"epicNumber": epicNumber,
				"action":     "merged",
				"prUrl":      result.PRURL,
				"prNumber":   result.PRNumber,
			})
		})

		// Update autonomous stall escalation mode on the shared runner (#3348).
		// The shared ipcRunner was created once at server init; AutonomousMode
		// must still reflect the current autonomous scheduler state.
		if s.ipcRunner != nil {
			autonomousActive := s.autonomousScheduler != nil && s.autonomousScheduler.IsRunning()
			if autonomousActive {
				if cfg, err := config.Load(s.workspaceRoot); err == nil && cfg.Autonomous.IsStallEscalationEnabled() {
					s.ipcRunner.AutonomousMode = true
					log.Printf("autonomous: stall escalation enabled (pause timeout: %s)", cfg.Autonomous.ResolvedStallPauseTimeout())
				}
			} else {
				s.ipcRunner.AutonomousMode = false
			}
		}

		s.scheduler.QueueAdd(orchestrator.QueueEntry{
			Repo:        repo,
			IssueNumber: p.IssueNumber,
			Priority:    0,
		})
		go func() {
			if err := s.scheduler.RunQueue(ctx); err != nil {
				s.Emit("pipeline.error", map[string]interface{}{
					"issueNumber": p.IssueNumber,
					"error":       err.Error(),
				})
			}
		}()
		return map[string]interface{}{
			"executionId": fmt.Sprintf("%s#%d", repo, p.IssueNumber),
			"issueNumber": p.IssueNumber,
			"status":      "queued",
		}, nil
	}

	// pipeline.runItem — direct pipeline dispatch bypassing board lookup.
	// Accepts a BoardItem directly (owner, repo, issueNumber, title, id)
	// and calls RunPipelineForItem. Used by E2E tests and direct-dispatch.
	//ipc:method pipelineRunItem params:PipelineRunItemParams result:RunPipelineResult skip
	s.methods["pipeline.runItem"] = func(ctx context.Context, params json.RawMessage) (interface{}, error) {
		var p struct {
			Owner       string `json:"owner"`
			Repo        string `json:"repo"`
			IssueNumber int    `json:"issueNumber"`
			Title       string `json:"title"`
			ID          string `json:"id"`
		}
		if err := json.Unmarshal(params, &p); err != nil {
			return nil, fmt.Errorf("invalid params: %w", err)
		}
		if s.scheduler == nil {
			return nil, errors.New(errSchedulerNotConfigured)
		}

		item := types.BoardItem{
			ID:     p.ID,
			Number: p.IssueNumber,
			Title:  p.Title,
			Repo:   p.Owner + "/" + p.Repo,
		}

		// Stage lifecycle callbacks and IpcStageRunner/IpcLicenseChecker are
		// registered once at server init in registerMethods() to prevent the
		// TOCTOU race where concurrent calls overwrote srv.methods["pipeline.stageResult"]
		// and orphaned earlier pipelines' pending channels (#3348).

		// Update autonomous stall escalation mode on the shared runner.
		if s.ipcRunner != nil {
			autonomousActive := s.autonomousScheduler != nil && s.autonomousScheduler.IsRunning()
			if autonomousActive {
				if cfg, err := config.Load(s.workspaceRoot); err == nil && cfg.Autonomous.IsStallEscalationEnabled() {
					s.ipcRunner.AutonomousMode = true
					log.Printf("autonomous: stall escalation enabled (pause timeout: %s)", cfg.Autonomous.ResolvedStallPauseTimeout())
				}
			} else {
				s.ipcRunner.AutonomousMode = false
			}
		}

		go s.scheduler.RunPipelineForItem(ctx, item)
		return map[string]interface{}{
			"executionId": fmt.Sprintf("%s#%d", item.Repo, item.Number),
			"issueNumber": p.IssueNumber,
			"status":      "queued",
		}, nil
	}

	//ipc:method pipelineStop params:PipelineStopParams result:void
	s.methods["pipeline.stop"] = func(_ context.Context, params json.RawMessage) (interface{}, error) {
		var p PipelineStopParams
		if err := json.Unmarshal(params, &p); err != nil {
			return nil, fmt.Errorf("invalid params: %w", err)
		}
		if s.execMgr == nil {
			return nil, fmt.Errorf("execution manager not configured")
		}
		s.execMgr.Stop(p.ExecutionID)
		return map[string]string{"status": "ok"}, nil
	}

	//ipc:method pipelinePause params:PipelinePauseParams result:void
	s.methods["pipeline.pause"] = func(_ context.Context, params json.RawMessage) (interface{}, error) {
		var p PipelinePauseParams
		if err := json.Unmarshal(params, &p); err != nil {
			return nil, fmt.Errorf("invalid params: %w", err)
		}
		if s.execMgr == nil {
			return nil, fmt.Errorf("execution manager not configured")
		}
		s.execMgr.Pause(p.ExecutionID)
		return map[string]string{"status": "ok"}, nil
	}

	//ipc:method pipelineResume params:PipelineResumeParams result:void
	s.methods["pipeline.resume"] = func(_ context.Context, params json.RawMessage) (interface{}, error) {
		var p PipelineResumeParams
		if err := json.Unmarshal(params, &p); err != nil {
			return nil, fmt.Errorf("invalid params: %w", err)
		}
		if s.execMgr == nil {
			return nil, fmt.Errorf("execution manager not configured")
		}
		s.execMgr.Resume(p.ExecutionID)
		return map[string]string{"status": "ok"}, nil
	}

	//ipc:method pipelineSetPaused params:PipelineSetPausedParams result:void
	s.methods["pipeline.setPaused"] = func(_ context.Context, params json.RawMessage) (interface{}, error) {
		var p PipelineSetPausedParams
		if err := json.Unmarshal(params, &p); err != nil {
			return nil, fmt.Errorf("invalid params: %w", err)
		}

		runtimeKey := fmt.Sprintf("%d", p.IssueNumber)
		s.runtimesMu.Lock()
		rt, ok := s.activeRuntimes[runtimeKey]
		if !ok {
			rt = state.NewRuntimeState("", p.IssueNumber, "")
			s.activeRuntimes[runtimeKey] = rt
		}
		s.runtimesMu.Unlock()

		rt.SetPaused(p.Paused)

		snap := rt.Snapshot()

		// Persist to disk so reload can restore it — into the run's target
		// repo (snap.Repo is seeded by the run's first stage transition).
		//
		// #307: only persist when the run's repo is known. Previously an
		// unidentified runtime (snap.Repo == "") fell back to the shared server
		// root (s.workspaceRoot) via pipelineStateDir(""), so in a multi-repo
		// workspace a pause stub landed in whichever repo the IPC server was
		// launched from — cross-contaminating a repo that never ran the issue.
		// An identity-less runtime has no correct home; skip the disk write and
		// let a later, repo-carrying transition persist it to the right repo.
		if snap.Repo != "" {
			if stateDir := s.pipelineStateDir(snap.Repo); stateDir != "" {
				if err := rt.Persist(stateDir); err != nil {
					return nil, fmt.Errorf("persist pause state: %w", err)
				}
			}
		}

		// Emit stateChanged so UI updates
		s.Emit("pipeline.stateChanged", map[string]interface{}{
			"repo":        snap.Repo,
			"issueNumber": p.IssueNumber,
			"state":       snap,
		})

		return map[string]string{"status": "ok"}, nil
	}

	// Persists a stage-exit diagnostic record from the TS dispatch path.
	//
	// Background (#3619 retro of #3340): the Go-scheduler write at
	// internal/orchestrator/scheduler.go:2487 (PR #3608) only fires when a
	// stage exits through `scheduler.runPipeline()`. The user's autonomous
	// workflow uses `headlessOrchestrator.runPipeline()` (TS-side legacy
	// path) which never round-trips Go's scheduler, so no record was
	// written for IPC-mode failures. This IPC method is the parallel write
	// path: TS calls it after each stage exit, the on-disk format is
	// identical, and `nightgauge exit-records tail` reads a unified
	// stream regardless of which dispatch path produced the record.
	//
	// Best-effort: a write failure returns an error but never blocks the
	// pipeline. TS treats this call as fire-and-forget — a missing record
	// is annoying but never fatal to the run.
	//
	// Registration uses an inline `s.methods[...]` line so the IPC
	// codegen scanner (cmd/ipc-codegen/main.go) can pair the annotation
	// with the method key. The handler body delegates to the helper for
	// testability.
	//ipc:method diagnosticsRecordStageExit params:RecordStageExitParams result:RecordStageExitResult
	s.methods["diagnostics.recordStageExit"] = makeDiagnosticsRecordStageExitHandler(s)

	//ipc:method pipelineGetState params:PipelineGetStateParams result:unknown skip
	s.methods["pipeline.getState"] = func(ctx context.Context, params json.RawMessage) (interface{}, error) {
		var p PipelineGetStateParams
		if err := json.Unmarshal(params, &p); err != nil {
			return nil, fmt.Errorf("invalid params: %w", err)
		}
		// Try live execution state first (scheduler path — key is "owner/repo#N")
		if s.execMgr != nil {
			key := fmt.Sprintf("%s/%s#%d", p.Owner, p.Repo, p.IssueNumber)
			if st := s.execMgr.GetState(key); st != nil {
				return st, nil
			}
		}
		// Try activeRuntimes (HeadlessOrchestrator path — keyed by issueNumber)
		runtimeKey := fmt.Sprintf("%d", p.IssueNumber)
		s.runtimesMu.Lock()
		if rt, ok := s.activeRuntimes[runtimeKey]; ok {
			snap := rt.Snapshot()
			s.runtimesMu.Unlock()
			return snap, nil
		}
		s.runtimesMu.Unlock()
		// Fall back to persisted file — scoped to the target repo's state
		// dir, where notifyStageTransition/setPaused persist it (#215).
		repoSlug := ""
		if p.Owner != "" && p.Repo != "" {
			repoSlug = p.Owner + "/" + p.Repo
		}
		if stateDir := s.pipelineStateDir(repoSlug); stateDir != "" {
			persisted, err := state.LoadPersistedState(stateDir, p.IssueNumber)
			if err == nil {
				return persisted, nil
			}
		}
		return nil, nil
	}

	// --- Pipeline state notifications (HeadlessOrchestrator path) ---

	//ipc:method pipelineNotifyStageTransition params:PipelineNotifyStageTransitionParams result:void skip
	s.methods["pipeline.notifyStageTransition"] = func(_ context.Context, params json.RawMessage) (interface{}, error) {
		var p PipelineNotifyStageTransitionParams
		if err := json.Unmarshal(params, &p); err != nil {
			return nil, fmt.Errorf("invalid params: %w", err)
		}
		runtimeKey := fmt.Sprintf("%d", p.IssueNumber)

		s.runtimesMu.Lock()
		rt, ok := s.activeRuntimes[runtimeKey]
		if !ok {
			rt = state.NewRuntimeState(p.Repo, p.IssueNumber, "")
			// Generate a stable run UUID for the extension/HeadlessOrchestrator
			// path (the Go-scheduler path threads RunID from runstate instead).
			// This is the runId the platform requires to materialise a live
			// pipeline_runs row from stage_started (#1047). Keyed by issue, so it
			// stays stable across every stage of the run.
			rt.RunID = uuid.NewString()
			s.activeRuntimes[runtimeKey] = rt
		}
		// Propagate title/branch from the transition params so that stateChanged
		// events carry the real GitHub issue title instead of an empty string.
		if p.Title != "" && rt.Title == "" {
			rt.Title = p.Title
		}
		if p.Branch != "" {
			rt.Branch = p.Branch
		}
		// Seed the run's repo from the first transition that carries it (the
		// TypeScript orchestrator sets it via PipelineStateService.setRunRepo).
		// Required for the platform's run-creation context (owner/name format).
		if p.Repo != "" && rt.Repo == "" {
			rt.Repo = p.Repo
		}
		runID := rt.RunID
		repo := rt.Repo
		s.runtimesMu.Unlock()

		stage := state.PipelineStage(p.Stage)

		// Attribute this stage to the served model + adapter the extension
		// reports (#268). RecordStageModel/RecordStageAdapter are latest-wins
		// per stage and ignore empty strings, so recording on every transition
		// lets the authoritative "complete" servedModel win over an earlier
		// requested model, and a "skipped"/bookend transition (no model) is a
		// no-op. BuildV2Record (run at notifyComplete) projects StageModels onto
		// V2StageDetail.ModelSelection and StageAdapters onto V2StageTokens.Adapter
		// — without this the VSCode-orchestrated path never populated either, so
		// per-stage model attribution was null (cost_events.model_id = 'unknown')
		// and the adapter never reached the wire (empty Adapter Mix donut).
		rt.RecordStageModel(stage, p.Model)
		rt.RecordStageAdapter(stage, p.Adapter)

		switch p.Status {
		case "initialized":
			// Pipeline initialized — runtime already created above
		case "running":
			rt.BeginStage(stage)
		case "model-resolved":
			// Up-front model attribution (#367): the extension records the
			// resolved model BEFORE the stage runs so a stage killed before
			// completion still attributes its true model, not 'unknown'. The
			// record already happened above (RecordStageModel); this case is an
			// intentional no-op. Do NOT call BeginStage here — that would reset
			// StageStart (the stage clock) before "running" arrives.
		case "complete":
			// Thread the per-stage usage the extension accumulated (#227) instead
			// of the old hardcoded rt.CompleteStage(0, 0, 0, ""). Mirror the
			// scheduler path (scheduler.go): prefer the CLI-authoritative
			// total_cost_usd when present, otherwise fall back to token-derived
			// cost. exitCode stays 0 — the notify path has no subprocess exit code.
			if p.CostUsd > 0 {
				rt.CompleteStageWithCost(0, p.InputTokens, p.OutputTokens, p.CacheReadTokens, p.CostUsd)
			} else {
				rt.CompleteStage(0, p.InputTokens, p.OutputTokens, p.Model)
			}
			// NOTE: Do NOT delete the runtime here on IsComplete().
			// The HeadlessOrchestrator path has 8 stages (6 pipeline stages
			// plus pipeline-start and pipeline-finish bookends), but
			// IsComplete() triggers at 6. Deleting here causes pr-merge and
			// pipeline-finish to create a NEW runtime with empty history,
			// which wipes completed-stage data from the stateChanged event
			// and breaks Discord embed status display.
			// Cleanup happens naturally: the next "initialized" call for
			// the same issue replaces the runtime, and process exit drops all.
		case "failed":
			rt.SetStageError(stage, p.Error)
			// NOTE: Do NOT delete the runtime here (#232). notifyComplete is the
			// interactive terminal funnel and fires right after this with
			// Success=false; it needs the runtime still present to build the
			// authoritative FAILED RunRecord. Deleting here stranded failed runs
			// with no history entry. Cleanup still happens in notifyComplete, and
			// the next "initialized" for the same issue replaces the runtime —
			// mirroring the deferred cleanup the "complete" case already relies on.
			// The on-disk runtime-{N}.json IS still removed below (terminal snapshot).
		case "skipped":
			rt.SkipStage(stage)
		case "deferred":
			rt.SkipStage(stage) // treat deferred as skipped in Go state
		}

		// Persist the runtime snapshot (carrying RunID) so a crash between here
		// and pipeline.notifyComplete leaves the run's platform UUID on disk for
		// orphan reconciliation at next activation (#44). On "failed" the run is
		// terminal — remove the snapshot instead so reconcile never re-emits.
		// Best-effort: persistence failures must never block the pipeline.
		//
		// #307: gate on a known repo. The first "initialized" transition of a
		// concurrent HeadlessOrchestrator slot arrives before setRunRepo seeds
		// the slug (the TS orchestrator resolves it asynchronously), so repo is
		// "" and pipelineStateDir("") resolves the shared server root
		// (s.workspaceRoot). Persisting there stranded an empty repo/stage stub
		// in the launch repo — a repo that never ran the issue — which the
		// startup restore then tried to resurrect. Wait for a repo-carrying
		// transition; the run's own repo dir is the only correct home.
		if repo != "" {
			if stateDir := s.pipelineStateDir(repo); stateDir != "" {
				if p.Status == "failed" {
					_ = os.Remove(filepath.Join(stateDir, fmt.Sprintf("runtime-%d.json", p.IssueNumber)))
				} else if err := rt.Persist(stateDir); err != nil {
					log.Printf("notifyStageTransition: persist runtime snapshot failed (non-fatal): %v", err)
				}
			}
		}

		// Emit stateChanged event
		snap := rt.Snapshot()
		s.Emit("pipeline.stateChanged", map[string]interface{}{
			"repo":        p.Repo,
			"issueNumber": p.IssueNumber,
			"state":       snap,
		})

		// Emit real-time platform telemetry for the live Pipelines view. The
		// extension/HeadlessOrchestrator path does not run through the Go
		// scheduler, so without this the platform never sees the run (the
		// "No pipeline runs yet" symptom). Reuses the same proven AnalyticsService
		// emitter + license the Go scheduler uses. Fire-and-forget.
		// Thread the authoritative per-stage tokens/cost (#227) into the platform
		// stage_completed event too (#233), so it carries the real totals that
		// reconcile the live stage_progress estimate. Zero for non-complete
		// transitions (buildStageTelemetryEvent only writes them on "complete").
		s.emitStageTelemetry(runID, repo, p.IssueNumber, p.Stage, p.Status, p.Error, p.InputTokens, p.OutputTokens, p.CacheReadTokens, p.CostUsd, rt)

		return map[string]string{"status": "ok"}, nil
	}

	//ipc:method pipelineNotifyStageProgress params:PipelineNotifyStageProgressParams result:void skip
	s.methods["pipeline.notifyStageProgress"] = func(_ context.Context, params json.RawMessage) (interface{}, error) {
		var p PipelineNotifyStageProgressParams
		if err := json.Unmarshal(params, &p); err != nil {
			return nil, fmt.Errorf("invalid params: %w", err)
		}

		// Resolve the run's stable UUID + repo from the active runtime, keyed by
		// issue number (same lookup as notifyStageTransition). Progress is
		// best-effort and IN-FLIGHT ONLY: do NOT create a runtime when absent and
		// do NOT mutate CompletedStages — the terminal "complete" transition owns
		// the authoritative per-stage totals; this only streams a live estimate.
		runtimeKey := fmt.Sprintf("%d", p.IssueNumber)
		s.runtimesMu.Lock()
		rt, ok := s.activeRuntimes[runtimeKey]
		var runID, repo string
		if ok {
			runID = rt.RunID
			repo = rt.Repo
		}
		s.runtimesMu.Unlock()
		if repo == "" {
			repo = p.Repo
		}

		// Emit the live in-stage token/cost estimate as a stage_progress event.
		// Skipped internally (no runID / bookend stage) rather than erroring.
		s.emitStageProgressTelemetry(runID, repo, p.IssueNumber, p.Stage, p.InputTokens, p.OutputTokens, p.CacheReadTokens, p.CostUsd)

		return map[string]string{"status": "ok"}, nil
	}

	//ipc:method pipelineNotifyComplete params:PipelineNotifyCompleteParams result:void skip
	s.methods["pipeline.notifyComplete"] = func(_ context.Context, params json.RawMessage) (interface{}, error) {
		var p PipelineNotifyCompleteParams
		if err := json.Unmarshal(params, &p); err != nil {
			return nil, fmt.Errorf("invalid params: %w", err)
		}

		// Resolve the run's stable UUID from the active runtime so the platform
		// can transition the live row from 'running' to 'complete'/'failed'.
		runtimeKey := fmt.Sprintf("%d", p.IssueNumber)
		s.runtimesMu.Lock()
		rt, ok := s.activeRuntimes[runtimeKey]
		var runID string
		if ok {
			runID = rt.RunID
		}
		s.runtimesMu.Unlock()

		// #266 ground-truth reconciliation. A run whose PR merged must never be
		// booked as failed by a late per-stage kill (progress-runaway / stall /
		// budget) that fired at pr-merge AFTER the merge already landed — that
		// misattribution recorded bowlsheet #261 (a merged run) as
		// failed/stall_kill. Resolve the terminal stage from the runtime
		// snapshot and, when the extension signalled a forge-confirmed merge,
		// flip the reported failure to success BEFORE both telemetry and the
		// RunRecord so every surface reflects the merge, not a phantom kill.
		// #309: replay the TS orchestrator's per-stage execution-path decisions
		// onto the runtime BEFORE snapshotting, so BuildV2Record stamps
		// execution_path / punt_reason on the authoritative history stage records.
		// The dogfood path (HeadlessOrchestrator) runs the deterministic-first
		// pr-create/pr-merge (and issue-pickup) hooks in TypeScript, so the Go
		// runtime never saw those decisions — its stageExecutionPaths map lived
		// only in the extension process. RecordExecutionPath / RecordStagePuntReason
		// ignore empty values, so an absent map is a no-op.
		if ok {
			for stg, path := range p.StageExecutionPaths {
				rt.RecordExecutionPath(state.PipelineStage(stg), path)
			}
			for stg, reason := range p.StagePuntReasons {
				rt.RecordStagePuntReason(state.PipelineStage(stg), reason)
			}
		}

		var snap *state.RuntimeState
		if ok {
			snap = rt.Snapshot()
			if effective := reconcilePrMergeGroundTruth(p.Success, p.PrMerged, string(snap.Stage)); effective != p.Success {
				log.Printf(
					"notifyComplete: #%d reported failed at pr-merge but the PR is MERGED — recording complete (ground truth, #266)",
					p.IssueNumber,
				)
				p.Success = effective
			}
		}

		s.emitPipelineDoneTelemetry(runID, p)

		// Write the authoritative interactive RunRecord (#232). notifyComplete
		// is the interactive-only terminal funnel — the Go scheduler path emits
		// its own RunRecord via OnPipelineComplete, so this cannot collide with
		// it — and it is the sole writer for the extension/HeadlessOrchestrator
		// path, for BOTH success and failure. It lands in the run's TARGET repo
		// (#215) so history isn't split across repos, and must run BEFORE the
		// runtime delete below so the snapshot is still available. Best-effort:
		// a write failure is logged but never blocks the pipeline.
		if ok {
			if root := s.repoRoot(p.Repo); root != "" {
				errMsg := ""
				if !p.Success {
					errMsg = snap.StageErrors[string(snap.Stage)]
					if errMsg == "" {
						for _, v := range snap.StageErrors {
							if v != "" {
								errMsg = v
								break
							}
						}
					}
					if errMsg == "" {
						errMsg = "pipeline failed"
					}
				}
				input := state.V2RunInput{
					Title: snap.Title,
					// Issue body captured at pickup (#183). Empty unless the
					// runtime state carried a body (autonomous path); flows to
					// the telemetry wire's issueBody when present.
					Body:        snap.Body,
					Branch:      snap.Branch,
					BaseBranch:  "main",
					RoutingPath: "standard",
				}
				// A blocked-dependency deferral (#305) is a NON-FAILURE even
				// though p.Success is false — skip the terminal-kind
				// classification entirely so the record is not stamped as a
				// failure. Its outcome fields are overridden below after the
				// record is built.
				if !p.Success && !p.Deferred {
					// Mirror the scheduler's failure records: classify the
					// terminal kind (which bumps schema_version to "3"), and
					// fall back to the most generic kind when the error text is
					// unclassifiable so the record still distinguishes "failed"
					// from "complete" in dashboards that group by terminal kind.
					kind := orchestrator.ClassifyTerminalKind(errMsg)
					if kind == "" {
						kind = orchestrator.TerminalKindSubagentCrash
					}
					input.TerminalFailureKind = kind
					// Refine into a first-class outcome_type when the failure is a
					// needs-human repo-config block (pr-merge blocked by a required
					// check no retry can clear) so the dashboard shows "blocked",
					// not a generic failure. Empty for ordinary failures.
					input.OutcomeType = orchestrator.OutcomeTypeForTerminalFailure(errMsg)
				}
				// TODO(#226 follow-up): hydrate Labels/Size/Type from the target
				// repo's issue-{N}.json (they are omitempty, so absent for now).
				hw := state.NewHistoryWriter(root)
				now := time.Now()
				record := hw.BuildV2Record(snap, p.Success, errMsg, input, now)

				// #305: book a blocked-dependency deferral as a first-class
				// NON-FAILURE. BuildV2Record maps p.Success==false to
				// outcome="failed"; override the three run-level fields so the
				// record — and every surface that reads it (local JSONL, the
				// platform push below via V2RunRecordToExecutionHistoryRunRecord,
				// which accepts "cancelled" as a telemetry outcome) — reflects a
				// non-failure deferral: outcome "cancelled" (closest non-failure
				// value the complete|failed|cancelled enum accepts), NO terminal
				// failure kind, and outcome_type "deferred".
				if p.Deferred {
					record.Outcome = "cancelled"
					record.TerminalFailureKind = ""
					record.OutcomeType = orchestrator.OutcomeTypeDeferred
				}
				if err := hw.WriteV2Record(record, now); err != nil {
					log.Printf("notifyComplete: write RunRecord failed (non-fatal): %v", err)
				}

				// Push the completed-run record to the platform telemetry sink
				// (POST /v1/telemetry/pipeline-run), the interactive mirror of the
				// autonomous scheduler's recordOutcome → PushPipelineRun. Without
				// this, interactive runs only wrote local JSONL + real-time stage
				// events, so the platform's usage_events / cost_events /
				// stage.snapshot rows (and pipeline_runs.cost) — the analytics
				// surface the dashboard's "Tokens today" and cost widgets read —
				// were never produced for extension-driven runs. Delegating this
				// to the extension's TelemetryUploaderService alone was unreliable
				// (consent/credential gating + a single-workspace-root JSONL scan
				// that misses target-repo runs in a multi-repo workspace). The
				// platform ingest is idempotent per (account, issue, started_at),
				// so this server-side push is safe alongside that best-effort
				// uploader. Fire-and-forget: PushPipelineRun buffers + retries
				// internally and never blocks the pipeline.
				if s.analyticsSvc != nil {
					repoForPush := record.Repo
					if repoForPush == "" {
						repoForPush = p.Repo
					}
					runRecord, mapErr := platform.V2RunRecordToExecutionHistoryRunRecord(
						record, platform.ExecutionHistoryMapperInput{Repo: repoForPush},
					)
					if mapErr != nil {
						log.Printf("notifyComplete: map RunRecord for platform push failed (non-fatal): %v", mapErr)
					} else {
						s.analyticsSvc.PushPipelineRun(context.Background(), runRecord)
					}
				}
			}
		}

		// Drop the runtime now the run is terminal so a subsequent run of the
		// same issue starts with a fresh UUID rather than reusing this one.
		s.runtimesMu.Lock()
		delete(s.activeRuntimes, runtimeKey)
		s.runtimesMu.Unlock()

		// The run reached its terminal event — remove the crash-recovery
		// snapshot so orphan reconciliation (#44) never re-terminates it.
		// Resolved per-repo: the snapshot lives in the run's target repo (#215).
		if stateDir := s.pipelineStateDir(p.Repo); stateDir != "" {
			_ = os.Remove(filepath.Join(stateDir, fmt.Sprintf("runtime-%d.json", p.IssueNumber)))
		}

		return map[string]string{"status": "ok"}, nil
	}

	//ipc:method pipelineNotifyPhaseTransition params:PipelineNotifyPhaseTransitionParams result:void skip
	s.methods["pipeline.notifyPhaseTransition"] = func(_ context.Context, params json.RawMessage) (interface{}, error) {
		var p PipelineNotifyPhaseTransitionParams
		if err := json.Unmarshal(params, &p); err != nil {
			return nil, fmt.Errorf("invalid params: %w", err)
		}

		// Look up the runtime for phase recording. Two runtime registries
		// exist: activeRuntimes (HeadlessOrchestrator path) and the
		// scheduler's per-issue registry (Go-scheduler / IPC mode). Phase
		// markers must be recorded in whichever runtime actually drives the
		// current run so the snapshot embedded in pipeline.stateChanged
		// carries PhaseHistory — otherwise the tree view loses phase counts
		// on already-completed stages whenever the extension reloads
		// mid-pipeline.
		runtimeKey := fmt.Sprintf("%d", p.IssueNumber)
		s.runtimesMu.Lock()
		rt, hasRuntime := s.activeRuntimes[runtimeKey]
		s.runtimesMu.Unlock()

		stage := state.PipelineStage(p.Stage)
		switch p.EventType {
		case "start":
			if hasRuntime {
				rt.BeginPhase(stage, p.Name, p.Index, p.Total)
			}
			if s.scheduler != nil {
				s.scheduler.RecordPhaseStart(p.IssueNumber, p.Stage, p.Name, p.Index, p.Total)
			}
			s.Emit("phase.start", map[string]interface{}{
				"repo":        p.Repo,
				"issueNumber": p.IssueNumber,
				"stage":       p.Stage,
				"name":        p.Name,
				"index":       p.Index,
				"total":       p.Total,
			})
		case "complete":
			if hasRuntime {
				rt.CompletePhase(stage, p.Name)
			}
			if s.scheduler != nil {
				s.scheduler.RecordPhaseComplete(p.IssueNumber, p.Stage, p.Name)
			}
			s.Emit("phase.complete", map[string]interface{}{
				"repo":        p.Repo,
				"issueNumber": p.IssueNumber,
				"stage":       p.Stage,
				"name":        p.Name,
				"index":       p.Index,
				"total":       p.Total,
			})
		}

		return map[string]string{"status": "ok"}, nil
	}

	// --- Wave orchestration methods ---

	//ipc:method waveStatus params:WaveStatusParams result:WaveStatusResult skip
	s.methods["wave.status"] = func(_ context.Context, params json.RawMessage) (interface{}, error) {
		var p struct {
			EpicNumber int `json:"epicNumber"`
		}
		if err := json.Unmarshal(params, &p); err != nil {
			return nil, fmt.Errorf("invalid params: %w", err)
		}
		if s.workspaceRoot == "" {
			return nil, fmt.Errorf("no workspace root configured")
		}
		// Read persisted wave status from disk
		statusPath := filepath.Join(s.workspaceRoot, ".nightgauge", "pipeline",
			fmt.Sprintf("wave-status-%d.json", p.EpicNumber))
		data, err := os.ReadFile(statusPath)
		if err != nil {
			if os.IsNotExist(err) {
				// Try wave plan (orchestration may still be running)
				planPath := filepath.Join(s.workspaceRoot, ".nightgauge", "pipeline",
					fmt.Sprintf("wave-plan-%d.json", p.EpicNumber))
				planData, planErr := os.ReadFile(planPath)
				if planErr != nil {
					return nil, fmt.Errorf("no wave data for epic #%d", p.EpicNumber)
				}
				var plan json.RawMessage
				if err := json.Unmarshal(planData, &plan); err != nil {
					return nil, fmt.Errorf("parse wave plan: %w", err)
				}
				return map[string]interface{}{
					"status": "running",
					"plan":   plan,
				}, nil
			}
			return nil, fmt.Errorf("read wave status: %w", err)
		}
		var status json.RawMessage
		if err := json.Unmarshal(data, &status); err != nil {
			return nil, fmt.Errorf("parse wave status: %w", err)
		}
		return status, nil
	}

	// --- Epic Context methods (Issue #2404) ---

	//ipc:method epicReadContext params:EpicContextParams result:EpicContextResult
	s.methods["epic.readContext"] = func(_ context.Context, params json.RawMessage) (interface{}, error) {
		var p struct {
			EpicNumber int `json:"epicNumber"`
		}
		if err := json.Unmarshal(params, &p); err != nil {
			return nil, fmt.Errorf("invalid params: %w", err)
		}
		if s.workspaceRoot == "" {
			return nil, fmt.Errorf("no workspace root configured")
		}
		ctxPath := filepath.Join(s.workspaceRoot, ".nightgauge", "pipeline",
			fmt.Sprintf("epic-context-%d.json", p.EpicNumber))
		data, err := os.ReadFile(ctxPath)
		if err != nil {
			if os.IsNotExist(err) {
				return nil, nil // No context yet — first sub-issue
			}
			return nil, fmt.Errorf("read epic context: %w", err)
		}
		var ctx json.RawMessage
		if err := json.Unmarshal(data, &ctx); err != nil {
			return nil, fmt.Errorf("parse epic context: %w", err)
		}
		return ctx, nil
	}

	//ipc:method epicAppendContext params:EpicAppendContextParams result:void
	s.methods["epic.appendContext"] = func(_ context.Context, params json.RawMessage) (interface{}, error) {
		var p struct {
			EpicNumber  int `json:"epicNumber"`
			IssueNumber int `json:"issueNumber"`
			Findings    struct {
				FilesTouched []string `json:"files_touched"`
				Decisions    []string `json:"decisions"`
				Discoveries  []string `json:"discoveries"`
				Patterns     []string `json:"patterns"`
				RecordedAt   string   `json:"recorded_at"`
			} `json:"findings"`
		}
		if err := json.Unmarshal(params, &p); err != nil {
			return nil, fmt.Errorf("invalid params: %w", err)
		}
		if s.workspaceRoot == "" {
			return nil, fmt.Errorf("no workspace root configured")
		}

		dir := filepath.Join(s.workspaceRoot, ".nightgauge", "pipeline")
		if err := os.MkdirAll(dir, 0755); err != nil {
			return nil, fmt.Errorf("create pipeline dir: %w", err)
		}

		ctxPath := filepath.Join(dir, fmt.Sprintf("epic-context-%d.json", p.EpicNumber))

		// Read existing context or create new one
		type subIssueFindings struct {
			FilesTouched []string `json:"files_touched"`
			Decisions    []string `json:"decisions"`
			Discoveries  []string `json:"discoveries"`
			Patterns     []string `json:"patterns"`
			RecordedAt   string   `json:"recorded_at"`
		}
		type sharedResearch struct {
			CodebaseNotes     []string `json:"codebase_notes"`
			ArchitectureNotes []string `json:"architecture_notes"`
			RelevantFiles     []string `json:"relevant_files"`
		}
		type epicCtx struct {
			SchemaVersion    string                       `json:"schema_version"`
			EpicNumber       int                          `json:"epic_number"`
			LastUpdated      string                       `json:"last_updated"`
			SubIssueFindings map[string]*subIssueFindings `json:"sub_issue_findings"`
			SharedResearch   sharedResearch               `json:"shared_research"`
		}

		var ec epicCtx
		data, err := os.ReadFile(ctxPath)
		if err != nil {
			// Initialize fresh
			ec = epicCtx{
				SchemaVersion:    "1.0",
				EpicNumber:       p.EpicNumber,
				SubIssueFindings: make(map[string]*subIssueFindings),
				SharedResearch: sharedResearch{
					CodebaseNotes:     []string{},
					ArchitectureNotes: []string{},
					RelevantFiles:     []string{},
				},
			}
		} else {
			if err := json.Unmarshal(data, &ec); err != nil {
				return nil, fmt.Errorf("parse existing epic context: %w", err)
			}
		}

		// Append findings
		ec.LastUpdated = p.Findings.RecordedAt
		if ec.LastUpdated == "" {
			ec.LastUpdated = time.Now().UTC().Format(time.RFC3339)
		}
		ec.SubIssueFindings[fmt.Sprintf("%d", p.IssueNumber)] = &subIssueFindings{
			FilesTouched: p.Findings.FilesTouched,
			Decisions:    p.Findings.Decisions,
			Discoveries:  p.Findings.Discoveries,
			Patterns:     p.Findings.Patterns,
			RecordedAt:   ec.LastUpdated,
		}

		// Merge relevant files (deduplicate)
		if len(p.Findings.FilesTouched) > 0 {
			seen := make(map[string]bool)
			for _, f := range ec.SharedResearch.RelevantFiles {
				seen[f] = true
			}
			for _, f := range p.Findings.FilesTouched {
				if !seen[f] {
					seen[f] = true
					ec.SharedResearch.RelevantFiles = append(ec.SharedResearch.RelevantFiles, f)
				}
			}
		}

		out, err := json.MarshalIndent(ec, "", "  ")
		if err != nil {
			return nil, fmt.Errorf("marshal epic context: %w", err)
		}
		tmpPath := ctxPath + ".tmp"
		if err := os.WriteFile(tmpPath, out, 0644); err != nil {
			return nil, fmt.Errorf("write temp file: %w", err)
		}
		if err := os.Rename(tmpPath, ctxPath); err != nil {
			os.Remove(tmpPath)
			return nil, fmt.Errorf("rename temp file: %w", err)
		}
		return map[string]string{"status": "ok"}, nil
	}

	// --- Queue methods ---

	//ipc:method queueAdd params:QueueAddParams result:void
	s.methods["queue.add"] = func(_ context.Context, params json.RawMessage) (interface{}, error) {
		var p QueueAddParams
		if err := json.Unmarshal(params, &p); err != nil {
			return nil, fmt.Errorf("invalid params: %w", err)
		}
		if s.scheduler == nil {
			return nil, errors.New(errSchedulerNotConfigured)
		}
		repo := fmt.Sprintf("%s/%s", p.Owner, p.Repo)
		s.scheduler.QueueAddItem(orchestrator.QueueItem{
			Repo:        repo,
			IssueNumber: p.IssueNumber,
			Title:       p.Title,
			Labels:      p.Labels,
			// Adopt the platform-assigned run_id (dashboard-trigger ack) when
			// present so the scheduler's runtime.RunID matches the command's
			// ack runId — keeping the dashboard's run deep-link resolvable (#4120).
			RemoteRunID: p.RemoteRunID,
		})
		return map[string]string{"status": "ok"}, nil
	}

	//ipc:method queueList params:none result:IpcQueueState
	s.methods["queue.list"] = func(_ context.Context, _ json.RawMessage) (interface{}, error) {
		if s.scheduler == nil {
			return orchestrator.QueueState{
				SchemaVersion: "2.0",
				Status:        "idle",
				Items:         []orchestrator.QueueItem{},
			}, nil
		}
		return s.scheduler.GetState(), nil
	}

	//ipc:method queueRemove params:QueueRemoveParams result:void
	s.methods["queue.remove"] = func(_ context.Context, params json.RawMessage) (interface{}, error) {
		var p QueueRemoveParams
		if err := json.Unmarshal(params, &p); err != nil {
			return nil, fmt.Errorf("invalid params: %w", err)
		}
		if s.scheduler == nil {
			return nil, errors.New(errSchedulerNotConfigured)
		}
		s.scheduler.QueueRemove(p.IssueNumber)
		return map[string]string{"status": "ok"}, nil
	}

	//ipc:method queueClear params:none result:void
	s.methods["queue.clear"] = func(_ context.Context, _ json.RawMessage) (interface{}, error) {
		if s.scheduler == nil {
			return nil, errors.New(errSchedulerNotConfigured)
		}
		s.scheduler.QueueClear()
		return map[string]string{"status": "ok"}, nil
	}

	//ipc:method queueDequeueIndependent params:QueueDequeueIndependentParams result:IpcQueueItem[]
	s.methods["queue.dequeueIndependent"] = func(ctx context.Context, params json.RawMessage) (interface{}, error) {
		var p QueueDequeueIndependentParams
		if err := json.Unmarshal(params, &p); err != nil {
			return nil, fmt.Errorf("invalid params: %w", err)
		}
		if s.scheduler == nil {
			return nil, errors.New(errSchedulerNotConfigured)
		}
		running := make([]orchestrator.RunningItem, 0, len(p.RunningItems))
		for _, r := range p.RunningItems {
			running = append(running, orchestrator.RunningItem{Repo: r.Repo, Number: r.Number})
		}
		items := s.scheduler.DequeueIndependent(ctx, p.MaxSlots, running)
		if items == nil {
			items = []orchestrator.QueueItem{}
		}
		return items, nil
	}

	//ipc:method queueEnqueueEpic params:QueueEnqueueEpicParams result:void
	s.methods["queue.enqueueEpic"] = func(ctx context.Context, params json.RawMessage) (interface{}, error) {
		var p QueueEnqueueEpicParams
		if err := json.Unmarshal(params, &p); err != nil {
			return nil, fmt.Errorf("invalid params: %w", err)
		}
		if s.scheduler == nil {
			return nil, errors.New(errSchedulerNotConfigured)
		}
		if err := s.scheduler.EnqueueEpic(ctx, p.Owner, p.Repo, p.EpicNumber, p.Title, p.Labels, p.EligibleSubIssues); err != nil {
			return nil, err
		}
		return map[string]string{"status": "ok"}, nil
	}

	// --- Health analysis ---

	healthAnalyzer := health.NewAnalyzer()

	//ipc:method healthAnalyze params:HealthAnalysisParams result:HealthAnalysis
	s.methods["intelligence.health"] = func(_ context.Context, params json.RawMessage) (interface{}, error) {
		var p HealthAnalysisParams
		if err := json.Unmarshal(params, &p); err != nil {
			return nil, fmt.Errorf("invalid params: %w", err)
		}
		// TODO: Load run data from workspace; for now return empty analysis
		report := healthAnalyzer.Analyze(nil)
		return report, nil
	}

	// --- Issue create/close ---

	//ipc:method issueCreate params:IssueCreateParams result:IssueDetail
	s.methods["issue.create"] = func(ctx context.Context, params json.RawMessage) (interface{}, error) {
		var p IssueCreateParams
		if err := json.Unmarshal(params, &p); err != nil {
			return nil, fmt.Errorf("invalid params: %w", err)
		}
		c, err := s.resolveClientForRequest(ctx, p.GitHubUser, p.Owner, p.Repo)
		if err != nil {
			return nil, err
		}
		repoID, err := c.GetRepositoryID(ctx, p.Owner, p.Repo)
		if err != nil {
			return nil, fmt.Errorf("get repo ID: %w", err)
		}
		return gh.NewIssueService(c).CreateIssue(ctx, repoID, p.Title, p.Body, p.Labels)
	}

	//ipc:method issueClose params:IssueCloseParams result:void
	s.methods["issue.close"] = func(ctx context.Context, params json.RawMessage) (interface{}, error) {
		var p IssueCloseParams
		if err := json.Unmarshal(params, &p); err != nil {
			return nil, fmt.Errorf("invalid params: %w", err)
		}
		c, err := s.resolveClientForRequest(ctx, p.GitHubUser, p.Owner, p.Repo)
		if err != nil {
			return nil, err
		}
		svc := gh.NewIssueService(c)
		issue, err := svc.GetIssue(ctx, p.Owner, p.Repo, p.Number)
		if err != nil {
			return nil, fmt.Errorf("fetch issue #%d: %w", p.Number, err)
		}
		if err := svc.CloseIssue(ctx, issue.NodeID); err != nil {
			return nil, err
		}
		s.Emit("tree.ready.update", TreeUpdateEvent{
			Owner: p.Owner,
		})
		return map[string]string{"status": "ok"}, nil
	}

	//ipc:method issueReopen params:IssueReopenParams result:void
	s.methods["issue.reopen"] = func(ctx context.Context, params json.RawMessage) (interface{}, error) {
		var p IssueReopenParams
		if err := json.Unmarshal(params, &p); err != nil {
			return nil, fmt.Errorf("invalid params: %w", err)
		}
		c, err := s.resolveClientForRequest(ctx, p.GitHubUser, p.Owner, p.Repo)
		if err != nil {
			return nil, err
		}
		svc := gh.NewIssueService(c)
		issue, err := svc.GetIssue(ctx, p.Owner, p.Repo, p.Number)
		if err != nil {
			return nil, fmt.Errorf("fetch issue #%d: %w", p.Number, err)
		}
		if err := svc.ReopenIssue(ctx, issue.NodeID); err != nil {
			return nil, err
		}
		s.Emit("tree.ready.update", TreeUpdateEvent{
			Owner: p.Owner,
		})
		return map[string]string{"status": "ok"}, nil
	}

	// --- PR list/create ---

	//ipc:method prList params:PRListParams result:PullRequestDetail[] skip
	s.methods["pr.list"] = func(ctx context.Context, params json.RawMessage) (interface{}, error) {
		var p PRListParams
		if err := json.Unmarshal(params, &p); err != nil {
			return nil, fmt.Errorf("invalid params: %w", err)
		}
		c, err := s.resolveClientForRequest(ctx, p.GitHubUser, p.Owner, p.Repo)
		if err != nil {
			return nil, err
		}
		return gh.NewPRService(c).ListPRs(ctx, p.Owner, p.Repo, p.State, p.HeadRef)
	}

	//ipc:method prCreate params:PRCreateParams result:PullRequestDetail
	s.methods["pr.create"] = func(ctx context.Context, params json.RawMessage) (interface{}, error) {
		var p PRCreateParams
		if err := json.Unmarshal(params, &p); err != nil {
			return nil, fmt.Errorf("invalid params: %w", err)
		}
		c, err := s.resolveClientForRequest(ctx, p.GitHubUser, p.Owner, p.Repo)
		if err != nil {
			return nil, err
		}
		repoID, err := c.GetRepositoryID(ctx, p.Owner, p.Repo)
		if err != nil {
			return nil, fmt.Errorf("get repo ID: %w", err)
		}
		return gh.NewPRService(c).CreatePR(ctx, repoID, p.Title, p.Body, p.HeadRef, p.BaseRef)
	}

	//ipc:method prMerge params:PRMergeParams result:void
	s.methods["pr.merge"] = func(ctx context.Context, params json.RawMessage) (interface{}, error) {
		var p PRMergeParams
		if err := json.Unmarshal(params, &p); err != nil {
			return nil, fmt.Errorf("invalid params: %w", err)
		}
		c, err := s.resolveClientForRequest(ctx, p.GitHubUser, p.Owner, p.Repo)
		if err != nil {
			return nil, err
		}
		strategy := p.Strategy
		if strategy == "" {
			strategy = "SQUASH"
		}
		prSvc := gh.NewPRService(c)
		if _, err := prSvc.MergePRWithStrategy(ctx, p.PRNodeID, strategy); err != nil {
			return nil, err
		}
		return map[string]interface{}{"success": true}, nil
	}

	// --- Project field operations ---

	//ipc:method projectSyncStatus params:ProjectSyncStatusParams result:void
	s.methods["project.syncStatus"] = func(ctx context.Context, params json.RawMessage) (interface{}, error) {
		var p ProjectSyncStatusParams
		if err := json.Unmarshal(params, &p); err != nil {
			return nil, fmt.Errorf("invalid params: %w", err)
		}
		c, err := s.resolveClientForRequest(ctx, p.GitHubUser, p.Owner, p.Repo)
		if err != nil {
			return nil, err
		}
		projSvc := gh.NewProjectService(c, p.Owner, p.ProjectNumber, gh.ParseOwnerType(p.OwnerType))
		if err := projSvc.SyncStatus(ctx, p.Owner, p.Repo, p.IssueNumber, p.Status); err != nil {
			return nil, err
		}
		return map[string]string{"status": "ok"}, nil
	}

	//ipc:method projectSyncIteration params:ProjectSyncIterationParams result:void
	s.methods["project.syncIteration"] = func(ctx context.Context, params json.RawMessage) (interface{}, error) {
		var p ProjectSyncIterationParams
		if err := json.Unmarshal(params, &p); err != nil {
			return nil, fmt.Errorf("invalid params: %w", err)
		}
		c, err := s.resolveClientForRequest(ctx, p.GitHubUser, p.Owner, p.Repo)
		if err != nil {
			return nil, err
		}
		projSvc := gh.NewProjectService(c, p.Owner, p.ProjectNumber, gh.ParseOwnerType(p.OwnerType))
		if err := projSvc.SyncIteration(ctx, p.Owner, p.Repo, p.IssueNumber, p.Iteration); err != nil {
			return nil, err
		}
		return map[string]string{"status": "ok"}, nil
	}

	//ipc:method projectSetHours params:ProjectSetHoursParams result:void
	s.methods["project.setHours"] = func(ctx context.Context, params json.RawMessage) (interface{}, error) {
		var p ProjectSetHoursParams
		if err := json.Unmarshal(params, &p); err != nil {
			return nil, fmt.Errorf("invalid params: %w", err)
		}
		c, err := s.resolveClientForRequest(ctx, p.GitHubUser, p.Owner, p.Repo)
		if err != nil {
			return nil, err
		}
		projSvc := gh.NewProjectService(c, p.Owner, p.ProjectNumber, gh.ParseOwnerType(p.OwnerType))
		if err := projSvc.SetHours(ctx, p.Owner, p.Repo, p.IssueNumber, p.Hours); err != nil {
			return nil, err
		}
		return map[string]string{"status": "ok"}, nil
	}

	//ipc:method projectAddItem params:ProjectAddItemParams result:{itemId:string}
	s.methods["project.addItem"] = func(ctx context.Context, params json.RawMessage) (interface{}, error) {
		var p ProjectAddItemParams
		if err := json.Unmarshal(params, &p); err != nil {
			return nil, fmt.Errorf("invalid params: %w", err)
		}
		c, err := s.resolveClientForRequest(ctx, p.GitHubUser, p.Owner, p.Repo)
		if err != nil {
			return nil, err
		}
		projSvc := gh.NewProjectService(c, p.Owner, p.ProjectNumber, gh.ParseOwnerType(p.OwnerType))
		itemID, err := projSvc.AddIssueByNumber(ctx, p.Owner, p.Repo, p.IssueNumber)
		if err != nil {
			return nil, err
		}
		return map[string]string{"itemId": itemID}, nil
	}

	// --- Git operations ---

	//ipc:method gitCurrentBranch params:GitCurrentBranchParams result:string unwrap:branch
	s.methods["git.currentBranch"] = func(_ context.Context, params json.RawMessage) (interface{}, error) {
		var p GitCurrentBranchParams
		if params != nil {
			json.Unmarshal(params, &p)
		}
		svc, err := s.gitService(p.WorkDir)
		if err != nil {
			return nil, err
		}
		branch, err := svc.CurrentBranch()
		if err != nil {
			return nil, err
		}
		return map[string]string{"branch": branch}, nil
	}

	//ipc:method gitRoot params:GitCurrentBranchParams result:string unwrap:root
	s.methods["git.root"] = func(_ context.Context, params json.RawMessage) (interface{}, error) {
		var p GitCurrentBranchParams // reuse — only needs WorkDir
		if params != nil {
			json.Unmarshal(params, &p)
		}
		svc, err := s.gitService(p.WorkDir)
		if err != nil {
			return nil, err
		}
		root, err := svc.Root()
		if err != nil {
			return nil, err
		}
		return map[string]string{"root": root}, nil
	}

	//ipc:method gitCheckout params:GitCheckoutParams result:void
	s.methods["git.checkout"] = func(_ context.Context, params json.RawMessage) (interface{}, error) {
		var p GitCheckoutParams
		if err := json.Unmarshal(params, &p); err != nil {
			return nil, fmt.Errorf("invalid params: %w", err)
		}
		svc, err := s.gitService(p.WorkDir)
		if err != nil {
			return nil, err
		}
		if err := svc.Checkout(p.Branch); err != nil {
			return nil, err
		}
		return map[string]string{"status": "ok"}, nil
	}

	//ipc:method gitBranchCreate params:GitBranchCreateParams result:void
	s.methods["git.branchCreate"] = func(_ context.Context, params json.RawMessage) (interface{}, error) {
		var p GitBranchCreateParams
		if err := json.Unmarshal(params, &p); err != nil {
			return nil, fmt.Errorf("invalid params: %w", err)
		}
		svc, err := s.gitService(p.WorkDir)
		if err != nil {
			return nil, err
		}
		if err := svc.BranchCreate(p.Name); err != nil {
			return nil, err
		}
		return map[string]string{"status": "ok"}, nil
	}

	//ipc:method gitBranchDelete params:GitBranchDeleteParams result:void
	s.methods["git.branchDelete"] = func(_ context.Context, params json.RawMessage) (interface{}, error) {
		var p GitBranchDeleteParams
		if err := json.Unmarshal(params, &p); err != nil {
			return nil, fmt.Errorf("invalid params: %w", err)
		}
		svc, err := s.gitService(p.WorkDir)
		if err != nil {
			return nil, err
		}
		if err := svc.BranchDelete(p.Name); err != nil {
			return nil, err
		}
		return map[string]string{"status": "ok"}, nil
	}

	//ipc:method gitBranchCleanup params:GitBranchCleanupParams result:void
	s.methods["git.branchCleanup"] = func(_ context.Context, params json.RawMessage) (interface{}, error) {
		var p GitBranchCleanupParams
		if err := json.Unmarshal(params, &p); err != nil {
			return nil, fmt.Errorf("invalid params: %w", err)
		}
		svc, err := s.gitService(p.WorkDir)
		if err != nil {
			return nil, err
		}
		if err := svc.BranchCleanup(p.Name); err != nil {
			return nil, err
		}
		return map[string]string{"status": "ok"}, nil
	}

	//ipc:method gitCleanupMergedBranches params:GitCleanupMergedBranchesParams result:GitCleanupMergedBranchesResult
	s.methods["git.cleanupMergedBranches"] = func(_ context.Context, params json.RawMessage) (interface{}, error) {
		var p GitCleanupMergedBranchesParams
		if params != nil {
			json.Unmarshal(params, &p)
		}
		if s.execMgr == nil {
			return nil, fmt.Errorf("execution manager not initialized")
		}
		deleted, err := s.execMgr.CleanupMergedBranches()
		if err != nil {
			return nil, err
		}
		return GitCleanupMergedBranchesResult{
			Deleted: deleted,
			Count:   len(deleted),
		}, nil
	}

	//ipc:method gitListRemoteBranches params:GitListRemoteBranchesParams result:string[] unwrap:branches nullable
	s.methods["git.listRemoteBranches"] = func(_ context.Context, params json.RawMessage) (interface{}, error) {
		var p GitListRemoteBranchesParams
		if params != nil {
			json.Unmarshal(params, &p)
		}
		svc, err := s.gitService(p.WorkDir)
		if err != nil {
			return nil, err
		}
		branches, err := svc.ListRemoteBranches()
		if err != nil {
			return nil, err
		}
		return map[string]interface{}{"branches": branches}, nil
	}

	//ipc:method gitStatus params:GitStatusParams result:GitStatusResult
	s.methods["git.status"] = func(_ context.Context, params json.RawMessage) (interface{}, error) {
		var p GitStatusParams
		if params != nil {
			json.Unmarshal(params, &p)
		}
		svc, err := s.gitService(p.WorkDir)
		if err != nil {
			return nil, err
		}
		return svc.Status()
	}

	//ipc:method gitCommit params:GitCommitParams result:{hash:string}
	s.methods["git.commit"] = func(_ context.Context, params json.RawMessage) (interface{}, error) {
		var p GitCommitParams
		if err := json.Unmarshal(params, &p); err != nil {
			return nil, fmt.Errorf("invalid params: %w", err)
		}
		svc, err := s.gitService(p.WorkDir)
		if err != nil {
			return nil, err
		}
		hash, err := svc.Commit(p.Message)
		if err != nil {
			return nil, err
		}
		return map[string]string{"hash": hash}, nil
	}

	//ipc:method gitLog params:GitLogParams result:GitLogEntry[]
	s.methods["git.log"] = func(_ context.Context, params json.RawMessage) (interface{}, error) {
		var p GitLogParams
		if params != nil {
			json.Unmarshal(params, &p)
		}
		svc, err := s.gitService(p.WorkDir)
		if err != nil {
			return nil, err
		}
		return svc.Log(p.Limit)
	}

	//ipc:method gitDiff params:GitDiffParams result:string unwrap:diff
	s.methods["git.diff"] = func(_ context.Context, params json.RawMessage) (interface{}, error) {
		var p GitDiffParams
		if params != nil {
			json.Unmarshal(params, &p)
		}
		svc, err := s.gitService(p.WorkDir)
		if err != nil {
			return nil, err
		}
		diff, err := svc.Diff()
		if err != nil {
			return nil, err
		}
		return map[string]string{"diff": diff}, nil
	}

	//ipc:method gitFetch params:GitFetchParams result:void
	s.methods["git.fetch"] = func(_ context.Context, params json.RawMessage) (interface{}, error) {
		var p GitFetchParams
		if params != nil {
			json.Unmarshal(params, &p)
		}
		svc, err := s.gitService(p.WorkDir)
		if err != nil {
			return nil, err
		}
		if err := svc.Fetch(p.Prune); err != nil {
			return nil, err
		}
		return map[string]string{"status": "ok"}, nil
	}

	//ipc:method gitPush params:GitPushParams result:void
	s.methods["git.push"] = func(_ context.Context, params json.RawMessage) (interface{}, error) {
		var p GitPushParams
		if params != nil {
			json.Unmarshal(params, &p)
		}
		svc, err := s.gitService(p.WorkDir)
		if err != nil {
			return nil, err
		}
		if err := svc.Push(); err != nil {
			return nil, err
		}
		return map[string]string{"status": "ok"}, nil
	}

	//ipc:method gitAbortPipeline params:GitAbortPipelineParams result:void
	s.methods["git.abortPipeline"] = func(_ context.Context, params json.RawMessage) (interface{}, error) {
		var p GitAbortPipelineParams
		if err := json.Unmarshal(params, &p); err != nil {
			return nil, fmt.Errorf("invalid params: %w", err)
		}
		svc, err := s.gitService(p.WorkDir)
		if err != nil {
			return nil, err
		}
		if err := svc.AbortPipeline(p.FeatureBranch); err != nil {
			return nil, err
		}
		return map[string]string{"status": "ok"}, nil
	}

	//ipc:method gitResetPipeline params:GitResetPipelineParams result:void
	s.methods["git.resetPipeline"] = func(_ context.Context, params json.RawMessage) (interface{}, error) {
		var p GitResetPipelineParams
		if params != nil {
			json.Unmarshal(params, &p)
		}
		svc, err := s.gitService(p.WorkDir)
		if err != nil {
			return nil, err
		}
		if err := svc.ResetPipeline(); err != nil {
			return nil, err
		}
		return map[string]string{"status": "ok"}, nil
	}

	// --- Remote command methods ---

	//ipc:method remoteGetCommandHistory params:RemoteGetCommandHistoryParams result:RemoteGetCommandHistoryResult
	s.methods["remote.getCommandHistory"] = func(_ context.Context, _ json.RawMessage) (interface{}, error) {
		if s.commandExecutor == nil {
			return RemoteGetCommandHistoryResult{Commands: []RemoteCommandHistoryEntry{}}, nil
		}
		entries := s.commandExecutor.GetCommandHistory()
		result := RemoteGetCommandHistoryResult{
			Commands: make([]RemoteCommandHistoryEntry, len(entries)),
		}
		for i, e := range entries {
			entry := RemoteCommandHistoryEntry{
				ID:         e.ID,
				Type:       e.Type,
				Status:     e.Status,
				ReceivedAt: e.ReceivedAt.UTC().Format(time.RFC3339),
				DurationMs: e.DurationMs,
				Error:      e.Error,
			}
			if e.CompletedAt != nil {
				s := e.CompletedAt.UTC().Format(time.RFC3339)
				entry.CompletedAt = &s
			}
			result.Commands[i] = entry
		}
		return result, nil
	}

	//ipc:method remoteGetPollingStatus params:RemoteGetPollingStatusParams result:RemotePollingStatus
	s.methods["remote.getPollingStatus"] = func(_ context.Context, _ json.RawMessage) (interface{}, error) {
		if s.commandExecutor == nil {
			return RemotePollingStatus{Active: false}, nil
		}
		ps := s.commandExecutor.GetPollingStatus()
		result := RemotePollingStatus{
			Active:       ps.Active,
			PendingCount: ps.PendingCount,
			ErrorCount:   ps.ErrorCount,
		}
		if ps.LastPolledAt != nil {
			s := ps.LastPolledAt.UTC().Format(time.RFC3339)
			result.LastPolledAt = &s
		}
		return result, nil
	}

	// --- autonomous scheduler methods ---

	//ipc:method autonomousStart params:AutonomousStartParams result:AutonomousStatusResult
	s.methods["autonomous.start"] = func(ctx context.Context, params json.RawMessage) (interface{}, error) {
		if s.autonomousScheduler == nil {
			return nil, fmt.Errorf("autonomous scheduler not configured")
		}
		// Parse workspace repos filter from params (if provided).
		var p AutonomousStartParams
		if len(params) > 0 {
			_ = json.Unmarshal(params, &p) // best-effort; empty = no filter
		}
		if allowlist := s.resolveAutonomousAllowlist(p.WorkspaceRepos); len(allowlist) > 0 {
			s.autonomousScheduler.FilterRepos(allowlist)
		}
		if s.autonomousScheduler.IsRunning() {
			// Goroutine is alive but may be blocked by a paused or safety_tripped
			// state. Resume() transitions it back to running and triggers an
			// immediate re-scan. If already running, this is a safe no-op for
			// paused/tripped — it only transitions away from those states.
			s.autonomousScheduler.Resume()
		} else {
			// No goroutine running (fresh server start or prior completion).
			// If persisted state shows paused/tripped, reset safety rails first
			// so the new goroutine doesn't immediately re-trip on first cycle.
			s.autonomousScheduler.Resume() // no-op if status is stopped/complete
			go func() {
				if err := s.autonomousScheduler.Run(ctx); err != nil {
					log.Printf("autonomous scheduler exited: %v", err)
				}
			}()
		}
		// Brief delay to let the scheduler start and update its status
		time.Sleep(50 * time.Millisecond)
		return s.autonomousScheduler.Status(), nil
	}

	//ipc:method autonomousPause params:AutonomousPauseParams result:AutonomousStatusResult
	s.methods["autonomous.pause"] = func(_ context.Context, params json.RawMessage) (interface{}, error) {
		if s.autonomousScheduler == nil {
			return nil, fmt.Errorf("autonomous scheduler not configured")
		}
		// Best-effort param decode — TS callers pass {reason, triggeredBy}
		// for #3251; legacy callers pass nothing and the strings stay empty.
		var p AutonomousPauseParams
		if len(params) > 0 {
			_ = json.Unmarshal(params, &p)
		}
		reason := p.Reason
		triggeredBy := p.TriggeredBy
		if triggeredBy == "" {
			triggeredBy = "unknown"
		}
		if reason == "" {
			reason = "no reason provided"
		}
		s.autonomousScheduler.Pause(reason, triggeredBy)
		return s.autonomousScheduler.Status(), nil
	}

	//ipc:method autonomousResume params:AutonomousResumeParams result:AutonomousStatusResult
	// #3303 — Resume must also start the dispatch goroutine when the scheduler
	// goroutine isn't alive. Previously, after a backend restart the persisted
	// status was preserved as "safety_tripped" but no goroutine existed; calling
	// Resume() flipped status → "running" while leaving runCycle dormant. The
	// status bar's Resume action then silently produced a stuck "running but
	// not dispatching" state. Mirror autonomous.start: kick off Run() when the
	// scheduler isn't alive so Resume reliably leaves the system dispatching.
	s.methods["autonomous.resume"] = func(ctx context.Context, params json.RawMessage) (interface{}, error) {
		if s.autonomousScheduler == nil {
			return nil, fmt.Errorf("autonomous scheduler not configured")
		}
		// Apply workspace repo filter on resume (same as start) so the
		// scheduler doesn't scan sibling repos outside this workspace.
		var p AutonomousResumeParams
		if len(params) > 0 {
			_ = json.Unmarshal(params, &p) // best-effort; empty = no filter
		}
		if allowlist := s.resolveAutonomousAllowlist(p.WorkspaceRepos); len(allowlist) > 0 {
			s.autonomousScheduler.FilterRepos(allowlist)
		}
		if s.autonomousScheduler.IsRunning() {
			s.autonomousScheduler.Resume()
		} else {
			s.autonomousScheduler.Resume()
			go func() {
				if err := s.autonomousScheduler.Run(ctx); err != nil {
					log.Printf("autonomous scheduler exited: %v", err)
				}
			}()
		}
		// Brief delay to let the scheduler start and update its status,
		// matching autonomous.start's behavior.
		time.Sleep(50 * time.Millisecond)
		return s.autonomousScheduler.Status(), nil
	}

	//ipc:method autonomousStop params:none result:AutonomousStatusResult
	s.methods["autonomous.stop"] = func(_ context.Context, _ json.RawMessage) (interface{}, error) {
		if s.autonomousScheduler == nil {
			return nil, fmt.Errorf("autonomous scheduler not configured")
		}
		s.autonomousScheduler.Stop()
		// Brief delay to let the scheduler process the stop signal
		time.Sleep(50 * time.Millisecond)
		return s.autonomousScheduler.Status(), nil
	}

	//ipc:method autonomousComplete params:AutonomousCompleteParams result:AutonomousStatusResult
	s.methods["autonomous.complete"] = func(_ context.Context, params json.RawMessage) (interface{}, error) {
		if s.autonomousScheduler == nil {
			return nil, fmt.Errorf("autonomous scheduler not configured")
		}
		var p AutonomousCompleteParams
		if err := json.Unmarshal(params, &p); err != nil {
			return nil, fmt.Errorf("invalid params: %w", err)
		}
		repo := p.Owner + "/" + p.Repo
		s.autonomousScheduler.NotifyComplete(repo, p.IssueNumber, p.Success, p.ConflictRestart, p.TerminalFailureKind, p.FailureDetail)
		return s.autonomousScheduler.Status(), nil
	}

	//ipc:method autonomousStatus params:none result:AutonomousStatusResult
	s.methods["autonomous.status"] = func(_ context.Context, _ json.RawMessage) (interface{}, error) {
		if s.autonomousScheduler == nil {
			return nil, fmt.Errorf("autonomous scheduler not configured")
		}
		return s.autonomousScheduler.Status(), nil
	}

	//ipc:method autonomousStuckEpics params:none result:StuckEpicsResult
	// Returns epics flagged as stalled on the most recent idle scan (#4073):
	// open with open sub-issues, zero eligible work, no running pipeline, and no
	// sub-issue actively recovering. The VSCode extension surfaces these so a
	// silently-stalled epic never looks "done".
	s.methods["autonomous.stuckEpics"] = func(_ context.Context, _ json.RawMessage) (interface{}, error) {
		if s.autonomousScheduler == nil {
			return nil, fmt.Errorf("autonomous scheduler not configured")
		}
		return map[string]interface{}{"stuckEpics": s.autonomousScheduler.StuckEpicsSnapshot()}, nil
	}

	//ipc:method autonomousRescan params:none result:AutonomousStatusResult
	// #3023 phase 1 — wakes the scheduler loop immediately, bypassing the
	// polling timer. The VSCode extension calls this after local actions
	// (promote / queue add / drag-to-Ready) so the user sees instant
	// dispatch instead of waiting for the next poll. No-op when no
	// scheduler is running.
	s.methods["autonomous.rescan"] = func(_ context.Context, _ json.RawMessage) (interface{}, error) {
		if s.autonomousScheduler == nil {
			return nil, fmt.Errorf("autonomous scheduler not configured")
		}
		s.autonomousScheduler.TriggerRescan()
		return s.autonomousScheduler.Status(), nil
	}

	//ipc:method autonomousUpdateAllowlist params:AutonomousUpdateAllowlistParams result:AutonomousStatusResult
	// #3429 — Live-applies a new repo allowlist to the running scheduler
	// without restarting it. Replaces the previous "Restart Autonomous?"
	// modal flow in the Repositories tree checkbox handler — toggling a
	// repo now updates the active scan set on the next dispatch tick with
	// no user-visible interruption.
	//
	// Same allowlist resolution as autonomous.start / autonomous.resume:
	// WorkspaceRepos is intersected with the user's
	// autonomous.enabled_repos config tier so team-tier YAML still wins
	// when set. FilterRepos is safe to call regardless of scheduler state
	// (running, paused, safety_tripped, or stopped) — see
	// TestFilterRepos_Widening for the widening/narrowing contract.
	//
	// Returns the current scheduler Status so the caller can confirm.
	s.methods["autonomous.updateAllowlist"] = func(_ context.Context, params json.RawMessage) (interface{}, error) {
		if s.autonomousScheduler == nil {
			return nil, fmt.Errorf("autonomous scheduler not configured")
		}
		var p AutonomousUpdateAllowlistParams
		if len(params) > 0 {
			_ = json.Unmarshal(params, &p) // best-effort; empty = no filter
		}
		if allowlist := s.resolveAutonomousAllowlist(p.WorkspaceRepos); len(allowlist) > 0 {
			s.autonomousScheduler.FilterRepos(allowlist)
		}
		return s.autonomousScheduler.Status(), nil
	}

	//ipc:method autonomousClearIssueFailures params:AutonomousClearIssueFailuresParams result:AutonomousClearIssueFailuresResult
	// #3020 — clears the per-issue lifetime failure counter so a chronically-
	// failing issue can be retried after manual triage. Pass empty key to clear
	// all issues. Returns the number of cleared entries.
	s.methods["autonomous.clearIssueFailures"] = func(_ context.Context, params json.RawMessage) (interface{}, error) {
		if s.autonomousScheduler == nil {
			return nil, fmt.Errorf("autonomous scheduler not configured")
		}
		var p AutonomousClearIssueFailuresParams
		if len(params) > 0 {
			_ = json.Unmarshal(params, &p) // best-effort; empty key = clear all
		}
		cleared := s.autonomousScheduler.ClearIssueFailures(p.Key)
		return AutonomousClearIssueFailuresResult{Cleared: cleared}, nil
	}

	//ipc:method autonomousClearQuotaCooldown params:AutonomousClearQuotaCooldownParams result:AutonomousClearQuotaCooldownResult
	// #3446 — clears the global Anthropic-quota cooldown (#3431) so the next
	// runCycle dispatches immediately rather than waiting out the recorded
	// deadline. Manual escape hatch for stale cooldowns, false-positive
	// resetsAt hints, or "I know the quota recovered, just start" overrides.
	// Returns cleared=false when no cooldown was active.
	s.methods["autonomous.clearQuotaCooldown"] = func(_ context.Context, _ json.RawMessage) (interface{}, error) {
		if s.autonomousScheduler == nil {
			return nil, fmt.Errorf("autonomous scheduler not configured")
		}
		cleared, previous := s.autonomousScheduler.ClearQuotaCooldown()
		return AutonomousClearQuotaCooldownResult{
			Cleared:       cleared,
			PreviousUntil: previous,
		}, nil
	}

	// --- Action Center (DecisionRequest store, ADR 015) ---

	//ipc:method attentionList params:AttentionListParams result:AttentionListResult
	s.methods["attention.list"] = s.handleAttentionList

	//ipc:method attentionResolve params:AttentionResolveParams result:AttentionResolveResult
	s.methods["attention.resolve"] = s.handleAttentionResolve

	//ipc:method attentionAcknowledge params:AttentionAcknowledgeParams result:AttentionAcknowledgeResult
	s.methods["attention.acknowledge"] = s.handleAttentionAcknowledge

	//ipc:method issueRemoveBlockedBy params:IssueRemoveBlockedByParams result:void
	s.methods["issue.removeBlockedBy"] = s.handleIssueRemoveBlockedBy

	// --- pipeline config methods ---

	//ipc:method pipelineSetMaxConcurrent params:PipelineSetMaxConcurrentParams result:PipelineMaxConcurrentResult
	// Single source of truth for "max concurrent slots" — applied to both
	// the TS-side ConcurrentPipelineManager (via the response value) AND the
	// Go-side autonomous scheduler (via SetMaxConcurrent). The previous design
	// had two independent settings (`pipeline.max_concurrent` and
	// `autonomous.max_concurrent`), one of which could only be changed by
	// restarting autonomous mode. They are now unified.
	s.methods["pipeline.setMaxConcurrent"] = func(_ context.Context, params json.RawMessage) (interface{}, error) {
		var p struct {
			MaxConcurrent int  `json:"maxConcurrent"`
			Persist       bool `json:"persist"`
		}
		if err := json.Unmarshal(params, &p); err != nil {
			return nil, fmt.Errorf("invalid params: %w", err)
		}
		if p.MaxConcurrent < 1 || p.MaxConcurrent > 10 {
			return nil, fmt.Errorf("maxConcurrent must be between 1 and 10, got %d", p.MaxConcurrent)
		}
		// Push to the autonomous scheduler so the change applies live without
		// requiring the user to stop/start autonomous mode.
		var autonomousPrev, autonomousCur int
		if s.autonomousScheduler != nil {
			autonomousPrev, autonomousCur = s.autonomousScheduler.SetMaxConcurrent(p.MaxConcurrent)
		}
		// Persist to config.yaml if requested
		if p.Persist {
			if err := s.persistMaxConcurrent(p.MaxConcurrent); err != nil {
				log.Printf("WARN: failed to persist maxConcurrent: %v", err)
			}
		}
		// Return the value — the TypeScript side reads the IPC response
		// and calls ConcurrentPipelineManager.setMaxConcurrentSlots()
		return map[string]interface{}{
			"maxConcurrent":      p.MaxConcurrent,
			"persisted":          p.Persist,
			"autonomousPrevious": autonomousPrev,
			"autonomousCurrent":  autonomousCur,
		}, nil
	}

	//ipc:method pipelineGetMaxConcurrent params:PipelineGetMaxConcurrentParams result:PipelineMaxConcurrentResult
	s.methods["pipeline.getMaxConcurrent"] = func(_ context.Context, _ json.RawMessage) (interface{}, error) {
		cfg, err := config.Load(s.workspaceRoot)
		if err != nil || cfg == nil {
			return map[string]interface{}{"maxConcurrent": config.DefaultPipelineMaxConcurrent}, nil
		}
		return map[string]interface{}{"maxConcurrent": config.ResolvedMaxConcurrent(cfg)}, nil
	}

	// --- focus lens methods ---

	//ipc:method focusSet params:FocusSetParams result:FocusShowResult
	s.methods["focus.set"] = func(_ context.Context, params json.RawMessage) (interface{}, error) {
		var p struct {
			Lens string `json:"lens"`
		}
		if err := json.Unmarshal(params, &p); err != nil {
			return nil, fmt.Errorf("invalid params: %w", err)
		}
		m := focus.NewManager(s.workspaceRoot)
		st, err := m.Set(p.Lens, "ipc")
		if err != nil {
			return nil, err
		}
		lens := m.ResolveLens(st.ActiveLens, st)
		return map[string]interface{}{
			"activeLens":  st.ActiveLens,
			"description": lens.Description,
			"setAt":       st.SetAt,
			"setBy":       st.SetBy,
			"boosts":      lens.ScoringBoosts,
			"keywords":    lens.Keywords,
		}, nil
	}

	//ipc:method focusShow params:none result:FocusShowResult
	s.methods["focus.show"] = func(_ context.Context, _ json.RawMessage) (interface{}, error) {
		m := focus.NewManager(s.workspaceRoot)
		st, lens, err := m.Show()
		if err != nil {
			return nil, err
		}
		return map[string]interface{}{
			"activeLens":  st.ActiveLens,
			"description": lens.Description,
			"setAt":       st.SetAt,
			"setBy":       st.SetBy,
			"boosts":      lens.ScoringBoosts,
			"keywords":    lens.Keywords,
		}, nil
	}

	//ipc:method focusClear params:none result:FocusShowResult
	s.methods["focus.clear"] = func(_ context.Context, _ json.RawMessage) (interface{}, error) {
		m := focus.NewManager(s.workspaceRoot)
		st, err := m.Clear("ipc")
		if err != nil {
			return nil, err
		}
		lens := m.ResolveLens(st.ActiveLens, st)
		return map[string]interface{}{
			"activeLens":  st.ActiveLens,
			"description": lens.Description,
			"setAt":       st.SetAt,
			"setBy":       st.SetBy,
			"boosts":      lens.ScoringBoosts,
			"keywords":    lens.Keywords,
		}, nil
	}

	//ipc:method knowledgeMetrics params:KnowledgeMetricsParams result:KnowledgeMetricsResult
	s.methods["knowledge.metrics"] = func(_ context.Context, raw json.RawMessage) (interface{}, error) {
		var p KnowledgeMetricsParams
		if len(raw) > 0 {
			_ = json.Unmarshal(raw, &p)
		}
		windowDays := p.WindowDays
		if windowDays <= 0 {
			windowDays = 7
		}
		staleDays := p.StaleDays
		if staleDays < 0 {
			staleDays = 30
		}
		root := s.workspaceRoot
		if root == "" {
			return nil, fmt.Errorf("no workspace root configured")
		}
		result, err := metrics.Aggregate(root, windowDays, staleDays)
		if err != nil {
			return nil, fmt.Errorf("aggregate knowledge metrics: %w", err)
		}
		// Overlay disabled status when telemetry is opted out — UI uses this
		// to render an actionable empty state pointing at config.yaml.
		if cfg, cerr := config.Load(root); cerr == nil && cfg != nil && cfg.Knowledge != nil {
			if !cfg.Knowledge.IsTelemetryEnabled() {
				result.Status = metrics.StatusDisabled
			}
		}
		return result, nil
	}

	//ipc:method knowledgeSearch params:KnowledgeSearchParams result:KnowledgeSearchResult
	s.methods["knowledge.search"] = func(_ context.Context, raw json.RawMessage) (interface{}, error) {
		var p KnowledgeSearchParams
		if len(raw) > 0 {
			if err := json.Unmarshal(raw, &p); err != nil {
				return nil, fmt.Errorf("parse knowledge.search params: %w", err)
			}
		}
		if strings.TrimSpace(p.Query) == "" {
			return KnowledgeSearchResult{Hits: []KnowledgeRecallHit{}, TotalHits: 0}, nil
		}
		root := s.workspaceRoot
		if root == "" {
			return nil, fmt.Errorf("no workspace root configured")
		}
		limit := p.Limit
		if limit <= 0 {
			limit = 10
		}
		scopes := p.Scope
		if len(scopes) == 0 {
			scopes = []string{"local", "cross-repo", "workspace"}
		}

		var knowledgeCfg *config.KnowledgeConfig
		if cfg, _ := config.Load(root); cfg != nil {
			knowledgeCfg = cfg.Knowledge
		}
		if knowledgeCfg == nil {
			knowledgeCfg = &config.KnowledgeConfig{}
		}

		idx, err := recall.BuildIndex(root, scopes, knowledgeCfg)
		if err != nil {
			return nil, fmt.Errorf("build recall index: %w", err)
		}
		res, err := recall.Query(idx, p.Query, limit, scopes)
		if err != nil {
			return nil, fmt.Errorf("recall query: %w", err)
		}
		hits := convertRecallHits(res.Hits, p.Tags)
		return KnowledgeSearchResult{Hits: hits, TotalHits: res.TotalHits}, nil
	}

	//ipc:method knowledgeBacklinks params:KnowledgeBacklinksParams result:KnowledgeBacklinksResult
	s.methods["knowledge.backlinks"] = func(_ context.Context, raw json.RawMessage) (interface{}, error) {
		var p KnowledgeBacklinksParams
		if len(raw) > 0 {
			if err := json.Unmarshal(raw, &p); err != nil {
				return nil, fmt.Errorf("parse knowledge.backlinks params: %w", err)
			}
		}
		root := s.workspaceRoot
		if root == "" {
			return nil, fmt.Errorf("no workspace root configured")
		}
		idx, err := knowledgepkg.LoadMetadataIndex(root)
		if err != nil {
			return KnowledgeBacklinksResult{Backlinks: []string{}}, nil
		}
		links := knowledgepkg.BacklinksFor(idx, p.Path)
		if links == nil {
			links = []string{}
		}
		return KnowledgeBacklinksResult{Backlinks: links}, nil
	}

	//ipc:method knowledgeRelatedToIssue params:KnowledgeRelatedToIssueParams result:KnowledgeRelatedToIssueResult
	s.methods["knowledge.relatedToIssue"] = func(_ context.Context, raw json.RawMessage) (interface{}, error) {
		var p KnowledgeRelatedToIssueParams
		if len(raw) > 0 {
			if err := json.Unmarshal(raw, &p); err != nil {
				return nil, fmt.Errorf("parse knowledge.relatedToIssue params: %w", err)
			}
		}
		if p.IssueNumber <= 0 {
			return nil, fmt.Errorf("issueNumber is required")
		}
		root := s.workspaceRoot
		if root == "" {
			return nil, fmt.Errorf("no workspace root configured")
		}
		limit := p.Limit
		if limit <= 0 {
			limit = 10
		}

		var knowledgeCfg *config.KnowledgeConfig
		if cfg, _ := config.Load(root); cfg != nil {
			knowledgeCfg = cfg.Knowledge
		}
		if knowledgeCfg == nil {
			knowledgeCfg = &config.KnowledgeConfig{}
		}

		scopes := []string{"local", "cross-repo", "workspace"}
		idx, err := recall.BuildIndex(root, scopes, knowledgeCfg)
		if err != nil {
			return nil, fmt.Errorf("build recall index: %w", err)
		}
		// Query by issue number — the BM25 tokenizer will index the digits.
		// The issue's own KB files are filtered out below so the result is
		// limited to *related* decisions, not the issue's own PRD/decisions.
		query := fmt.Sprintf("issue %d", p.IssueNumber)
		res, err := recall.Query(idx, query, limit*2, scopes)
		if err != nil {
			return nil, fmt.Errorf("recall query: %w", err)
		}
		issuePrefix := fmt.Sprintf("%d-", p.IssueNumber)
		filtered := make([]KnowledgeRecallHit, 0, len(res.Hits))
		for _, h := range res.Hits {
			// Skip hits that originate from the issue's own KB directory.
			if strings.Contains(h.Path, issuePrefix) && h.IssueNumber == p.IssueNumber {
				continue
			}
			filtered = append(filtered, KnowledgeRecallHit{
				Rank:        len(filtered) + 1,
				Score:       h.Score,
				Path:        h.Path,
				Kind:        h.Kind,
				IssueNumber: h.IssueNumber,
				Tags:        h.Tags,
				Snippet:     h.Snippet,
				Graduated:   h.Graduated,
			})
			if len(filtered) >= limit {
				break
			}
		}
		return KnowledgeRelatedToIssueResult{Hits: filtered}, nil
	}

	//ipc:method focusList params:none result:FocusListResult
	s.methods["focus.list"] = func(_ context.Context, _ json.RawMessage) (interface{}, error) {
		m := focus.NewManager(s.workspaceRoot)
		st, _ := m.Load()
		var lenses []map[string]interface{}
		for _, l := range m.AllLenses() {
			lenses = append(lenses, map[string]interface{}{
				"name":        l.Name,
				"description": l.Description,
				"boosts":      l.ScoringBoosts,
				"keywords":    l.Keywords,
				"builtin":     l.Builtin,
				"active":      l.Name == st.ActiveLens,
			})
		}
		return map[string]interface{}{
			"activeLens": st.ActiveLens,
			"lenses":     lenses,
		}, nil
	}

	// When scheduler is already available (e.g., passed via WithScheduler option),
	// initialize the shared runner, checker, and lifecycle callbacks immediately.
	// When scheduler is attached post-construction via SetScheduler (e.g., serveCmd),
	// SetScheduler calls initSchedulerCallbacks at that point instead.
	if s.scheduler != nil {
		s.initSchedulerCallbacks(s.scheduler)
	}

	// --- Agent methods ---

	//ipc:method agentAcknowledgeCommand params:AgentAcknowledgeCommandParams result:AgentAcknowledgeCommandResult
	s.methods["agent.acknowledgeCommand"] = s.handleAgentAcknowledgeCommand
}

// resolveAutonomousAllowlist computes the final repo allowlist for
// FilterRepos, combining the workspace-derived list (from VS Code) with the
// user-configured autonomous.enabled_repos allowlist (from config.yaml).
//
// Precedence / semantics:
//   - Neither set → returns nil (no-op; scheduler scans all configured repos).
//   - Only workspaceRepos → returns workspaceRepos (current behavior).
//   - Only enabled_repos → returns enabled_repos (CLI-style use).
//   - Both set → intersection (workspaceRepos ∩ enabled_repos). If the
//     intersection is empty we fall back to enabled_repos so the user's
//     explicit intent wins — otherwise toggling on a repo not open in the
//     workspace would silently scan nothing.
//
// Matching is case-insensitive on fully-qualified "owner/repo" names.
// Short names in enabled_repos are expanded using the configured owner.
func (s *Server) resolveAutonomousAllowlist(workspaceRepos []string) []string {
	var enabled []string
	if cfg, err := config.Load(s.workspaceRoot); err == nil && cfg != nil && cfg.Autonomous != nil {
		enabled = cfg.Autonomous.ResolvedEnabledRepos(cfg.Owner)
	}

	if len(enabled) == 0 {
		return workspaceRepos
	}
	if len(workspaceRepos) == 0 {
		return enabled
	}

	allowed := make(map[string]bool, len(workspaceRepos))
	for _, r := range workspaceRepos {
		allowed[strings.ToLower(r)] = true
	}
	var intersection []string
	for _, r := range enabled {
		if allowed[strings.ToLower(r)] {
			intersection = append(intersection, r)
		}
	}
	if len(intersection) == 0 {
		// User's explicit config takes precedence over workspace membership.
		// e.g. user toggled "platform" but hasn't opened that folder in this
		// workspace — still filter the scheduler to platform only.
		return enabled
	}
	return intersection
}

// gitService creates a git.Service for the given workDir (or falls back to workspaceRoot).
func (s *Server) gitService(workDir string) (*gitops.Service, error) {
	dir := workDir
	if dir == "" {
		dir = s.workspaceRoot
	}
	if dir == "" {
		return nil, fmt.Errorf("no workspace root configured for git operations")
	}
	return gitops.NewService(dir)
}

// statusToTabId maps a board status string to its corresponding TabId
// used in tree.{tabId}.update event names.
// Must match TabConfig.ts: 'ready' | 'in-progress' | 'in-review' | 'backlog'.
func statusToTabId(status string) string {
	switch strings.ToLower(status) {
	case "ready":
		return "ready"
	case "in progress":
		return "in-progress"
	case "in review":
		return "in-review"
	case "backlog":
		return "backlog"
	default:
		return ""
	}
}

// quotaCooldownBucket attributes an active dispatch cooldown to the upstream
// bucket that triggered it, by inspecting the cooldown reason text written by
// the autonomous scheduler. applyQuotaCooldownLocked phrases the Anthropic
// case as "... Anthropic API quota exhausted ..." while
// applyGitHubQuotaCooldownLocked phrases the GitHub case as "GitHub API quota
// low ...". Falls back to "dispatch-cooldown" when the reason is empty or
// unrecognized. Used by the workflow.quotaState bridge (#3909).
func quotaCooldownBucket(reason string) string {
	lower := strings.ToLower(reason)
	switch {
	case strings.Contains(lower, "github"):
		return "github-quota"
	case strings.Contains(lower, "anthropic"):
		return "anthropic-five-hour"
	default:
		return "dispatch-cooldown"
	}
}

// splitOwnerRepo splits "owner/repo" into (owner, repo).
func splitOwnerRepo(full string) (string, string) {
	parts := strings.SplitN(full, "/", 2)
	if len(parts) != 2 {
		return "", full
	}
	return parts[0], parts[1]
}

// Emit sends an unsolicited event to VSCode.
func (s *Server) Emit(event string, data interface{}) {
	s.sendJSON(Event{Event: event, Data: data})
}

func (s *Server) sendResponse(resp Response) {
	s.sendJSON(resp)
}

func (s *Server) sendError(id int, code int, message string) {
	s.sendJSON(Response{
		ID:    id,
		Error: &RPCError{Code: code, Message: message},
	})
}

// persistMaxConcurrent writes the unified max_concurrent value to
// pipeline.max_concurrent in config.yaml. The previous implementation did a
// naive first-match on any `max_concurrent:` line, which silently updated
// `autonomous.max_concurrent` when it appeared in the file before the
// pipeline block — leaving pipeline at its old value. This routine now
// targets the `pipeline:` block specifically and creates it (or the key) if
// missing.
//
// See Issue #3195.
func (s *Server) persistMaxConcurrent(n int) error {
	yamlPath := filepath.Join(s.workspaceRoot, ".nightgauge", "config.yaml")
	data, err := os.ReadFile(yamlPath)
	if err != nil {
		return fmt.Errorf("read config.yaml: %w", err)
	}

	lines := strings.Split(string(data), "\n")

	pipelineBlockStart := -1
	pipelineBlockEnd := -1
	for i, line := range lines {
		trimmed := strings.TrimSpace(line)
		if pipelineBlockStart < 0 {
			if trimmed == "pipeline:" {
				pipelineBlockStart = i
			}
			continue
		}
		// Block ends at the next non-empty, non-comment, top-level line.
		if trimmed == "" || strings.HasPrefix(trimmed, "#") {
			continue
		}
		if !strings.HasPrefix(line, " ") && !strings.HasPrefix(line, "\t") {
			pipelineBlockEnd = i
			break
		}
	}

	// Replace existing pipeline.max_concurrent if present inside the block.
	if pipelineBlockStart >= 0 {
		end := pipelineBlockEnd
		if end < 0 {
			end = len(lines)
		}
		for i := pipelineBlockStart + 1; i < end; i++ {
			line := lines[i]
			trimmed := strings.TrimSpace(line)
			// Only top-level keys of the pipeline block — children of nested
			// keys (e.g. context_schema_repair.max_attempts) are at deeper
			// indentation and must be skipped.
			indent := line[:len(line)-len(strings.TrimLeft(line, " \t"))]
			if strings.HasPrefix(trimmed, "max_concurrent:") && len(indent) <= 2 {
				lines[i] = fmt.Sprintf("%smax_concurrent: %d", indent, n)
				return os.WriteFile(yamlPath, []byte(strings.Join(lines, "\n")), 0o644)
			}
		}
		// Block exists but no max_concurrent yet — insert at top of block.
		insertion := fmt.Sprintf("  max_concurrent: %d", n)
		out := make([]string, 0, len(lines)+1)
		out = append(out, lines[:pipelineBlockStart+1]...)
		out = append(out, insertion)
		out = append(out, lines[pipelineBlockStart+1:]...)
		return os.WriteFile(yamlPath, []byte(strings.Join(out, "\n")), 0o644)
	}

	// No pipeline block — append one.
	appended := strings.TrimRight(string(data), "\n")
	appended += fmt.Sprintf("\npipeline:\n  max_concurrent: %d\n", n)
	return os.WriteFile(yamlPath, []byte(appended), 0o644)
}

func (s *Server) sendJSON(v interface{}) {
	s.mu.Lock()
	defer s.mu.Unlock()

	data, err := json.Marshal(v)
	if err != nil {
		return
	}
	fmt.Fprintf(s.writer, "%s\n", data)
}

// convertRecallHits maps recall.RecallHit values onto the wire-level
// KnowledgeRecallHit shape and applies an optional client-side tag filter.
// The intent of the tag filter is post-hoc narrowing — BM25 ranking happens
// first, then hits with no overlapping tag are dropped. An empty tag filter
// is a no-op.
func convertRecallHits(hits []recall.RecallHit, tagFilter []string) []KnowledgeRecallHit {
	out := make([]KnowledgeRecallHit, 0, len(hits))
	tagSet := make(map[string]bool, len(tagFilter))
	for _, t := range tagFilter {
		tagSet[strings.ToLower(strings.TrimSpace(t))] = true
	}
	for _, h := range hits {
		if len(tagSet) > 0 {
			matched := false
			for _, ht := range h.Tags {
				if tagSet[strings.ToLower(ht)] {
					matched = true
					break
				}
			}
			if !matched {
				continue
			}
		}
		out = append(out, KnowledgeRecallHit{
			Rank:        h.Rank,
			Score:       h.Score,
			Path:        h.Path,
			Kind:        h.Kind,
			IssueNumber: h.IssueNumber,
			Tags:        h.Tags,
			Snippet:     h.Snippet,
			Graduated:   h.Graduated,
		})
	}
	return out
}
