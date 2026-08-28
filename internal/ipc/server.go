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
	"sync/atomic"
	"time"

	platformapi "github.com/nightgauge/nightgauge/api/generated/go/platform"
	"github.com/nightgauge/nightgauge/internal/attention"
	"github.com/nightgauge/nightgauge/internal/config"
	"github.com/nightgauge/nightgauge/internal/execution"
	"github.com/nightgauge/nightgauge/internal/executor"
	"github.com/nightgauge/nightgauge/internal/focus"
	"github.com/nightgauge/nightgauge/internal/forge"
	"github.com/nightgauge/nightgauge/internal/forge/boardcache"
	gitops "github.com/nightgauge/nightgauge/internal/git"
	gh "github.com/nightgauge/nightgauge/internal/github"
	"github.com/nightgauge/nightgauge/internal/intelligence/complexity"
	"github.com/nightgauge/nightgauge/internal/intelligence/failure"
	"github.com/nightgauge/nightgauge/internal/intelligence/health"
	"github.com/nightgauge/nightgauge/internal/intelligence/learning"
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
	client    *gh.Client
	writer    io.Writer
	mu        sync.Mutex
	methods   map[string]Handler
	execMgr   *execution.Manager
	scheduler *orchestrator.Scheduler

	// platformClient and every service built on it (below) are normally set
	// once, before Run(), by WithPlatformClient — safe to read unguarded
	// after that point since nothing mutates them again.
	//
	// #756 breaks that invariant: a signed-in session alone must be enough to
	// talk to the platform, even when the daemon was spawned with no
	// api_url/api_key/license_key at all (nothing for the eager path to
	// construct from). ensurePlatformClient lazily builds the default client
	// the first time platform.setSessionToken sees a real token — from a
	// request-handling goroutine, since IPC requests are dispatched one
	// goroutine per call (see handleRequest). That write has to be visible to
	// every OTHER handler goroutine reading these same fields, so both the
	// write and every read go through platformClientMu — see
	// setPlatformServicesLocked and the getPlatformClient/getXSvc getters.
	//
	// authSvc is deliberately NOT part of this group: it drives the daemon's
	// own device-code / GitHub sign-in flow, i.e. it is how a session gets
	// created, not something a session's arrival should construct.
	platformClientMu  sync.RWMutex
	platformClient    *platform.Client
	licenseSvc        *platform.LicenseService
	authSvc           *platform.AuthService
	skillSvc          *platform.SkillService
	analyticsSvc      *platform.AnalyticsService
	complianceSvc     *platform.ComplianceService
	auditRetentionSvc *platform.AuditRetentionService
	teamSvc           *platform.TeamService
	billingSvc        *platform.BillingService
	commandExecutor   *executor.CommandExecutor

	// workspaceRoot is the CURRENT root, and it is MUTABLE: workspace.setRoot
	// re-points it on a multi-repo workspace switch, from a handler goroutine,
	// while the deferred reconcile sweep reads it from a timer goroutine (ADR-017
	// 7.3). Every access goes through workspaceRootPath/setWorkspaceRoot — an
	// unlocked read is a data race the moment the sweep stopped being inline.
	//
	// launchRoot is the root this server was CONSTRUCTED with, written once by
	// the first setWorkspaceRoot and never again. The deferred sweep is the
	// reason it exists: it counts candidates at activation and re-scans two
	// minutes later, and a workspace switch inside that window would otherwise
	// silently narrow the scan to the new root — the orphans of the root the
	// process started in would then never be collected by the pass that was
	// deferred FOR them. pipelineStateScanRoots always adds it.
	workspaceRoot   string
	launchRoot      string
	workspaceRootMu sync.RWMutex

	// startupGraceUntil is ladder arm 5 as a deadline in UnixNano, armed by
	// Run and read by every reconcile pass including workspace.setRoot's inline
	// one. Zero means no grace was ever armed.
	startupGraceUntil atomic.Int64

	// reconcileMu serializes reconcile passes. The deferred startup sweep and
	// workspace.setRoot's inline pass walk the same directories; two of them at
	// once would race each other's removals and renames.
	reconcileMu sync.Mutex

	// activeRuntimes holds the live runs the IPC server owns — the
	// extension/HeadlessOrchestrator population. KEYED BY RUN IDENTITY
	// (ADR-017 Decision 1, step 4), not by issue number: two dispatches of one
	// issue are two runs, and an issue-shaped key merged them into one runtime,
	// one snapshot and one set of accumulators. The issue number survives as a
	// DERIVED index (Decision 6, currentRunForIssue) — there is no second map.
	//
	// adopting is the per-id adoption singleflight (Decision 4) and closedRuns
	// the FIFO ring of ids whose terminal claim has run. All three, and every
	// runEntry field, are protected by runtimesMu.
	activeRuntimes map[string]*runEntry
	adopting       map[string]*adoptFlight
	closedRuns     closedRunRing
	runtimesMu     sync.Mutex

	// rejectLogSeen rate-limits the identity-rejection log to one line per
	// (method, runId) per minute — the notifyStageProgress cadence is why.
	// Deliberately NOT under runtimesMu: logging a refusal must never queue
	// behind the registry.
	rejectLogSeen map[string]time.Time
	rejectLogMu   sync.Mutex

	// schedulerRuns is the narrow read surface ADR-017 Decision 11 needs from
	// the Go scheduler's own registry. It is set from the same scheduler
	// WithScheduler attaches; it exists as an interface so the resolution
	// rule's scheduler arm is testable without standing up a full scheduler.
	schedulerRuns schedulerRunRegistry

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

	// forgeClientFn resolves a forge.ForgeClient for an "owner/name" spec.
	// attention.sweep is its only consumer today. Stored as a closure for the
	// same reason as notificationReloader: the router builder lives in cmd/,
	// and internal/ipc must not import it.
	forgeClientFn func(repo string) (forge.ForgeClient, error)

	// lastSweepAt is when this daemon's last sweep that actually evaluated
	// something finished, for the SweepMinGap check (#848). Read and written
	// only while holding sweepMu, so it needs no lock of its own.
	lastSweepAt time.Time

	// boards is the daemon-wide board snapshot cache (#845), shared by every
	// board verb through boardServicesFor. One cache for the process, keyed by
	// (owner, project): in a shared-board workspace several repos resolve to
	// the same board, and that keying is what lets the second consumer reuse
	// the first one's snapshot. Never nil after NewServer; the accessor still
	// tolerates nil so a zero-value Server in a test is usable.
	boards *boardcache.Cache
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

// schedulerRunRegistry is the Go scheduler's half of ADR-017 Decision 11's
// "two registries" boundary, as the IPC server needs to see it: which runs the
// scheduler owns, and the identity-gated write arms for phase markers.
// *orchestrator.Scheduler satisfies it.
type schedulerRunRegistry interface {
	LookupRunByID(runID string) *state.RuntimeState
	IsRunLive(runID string) bool
	RecordPhaseStartForRun(runID string, issueNumber int, stage, name string, index, total int)
	RecordPhaseCompleteForRun(runID string, issueNumber int, stage, name string)
	RunIDForIssue(issueNumber int) string
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
		activeRuntimes: make(map[string]*runEntry),
		adopting:       make(map[string]*adoptFlight),
		forgeRegistry:  make(map[string]ForgeInstanceConfig),
		// Zero TTL takes boardcache.DefaultTTL. Constructed before options run
		// so a test can still override it.
		boards: boardcache.New(0),
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
		// Guarded: a nil *Scheduler stored in an interface field is a NON-nil
		// interface, and every `if s.schedulerRuns != nil` guard downstream
		// would then call through a nil receiver.
		if sched != nil {
			s.schedulerRuns = sched
		}
	}
}

// WithPlatformClient attaches a platform client to the IPC server.
func WithPlatformClient(pc *platform.Client) ServerOption {
	return func(s *Server) {
		s.attachPlatformClient(pc)
	}
}

// setPlatformServicesLocked wires pc and every service built on it onto the
// server, replacing whatever was there before. Callers must hold
// platformClientMu for writing.
func (s *Server) setPlatformServicesLocked(pc *platform.Client) {
	s.platformClient = pc
	s.licenseSvc = platform.NewLicenseService(pc)
	s.skillSvc = platform.NewSkillService(pc)
	s.analyticsSvc = platform.NewAnalyticsService(pc)
	s.complianceSvc = platform.NewComplianceService(pc)
	s.auditRetentionSvc = platform.NewAuditRetentionService(pc)
	s.teamSvc = platform.NewTeamService(pc)
	s.billingSvc = platform.NewBillingService(pc)
}

// attachPlatformClient installs pc (and its dependent services) under
// platformClientMu. Used both by WithPlatformClient (before Run(), where the
// lock is uncontended) and by ensurePlatformClient (from a request goroutine
// during Run()).
func (s *Server) attachPlatformClient(pc *platform.Client) {
	s.platformClientMu.Lock()
	defer s.platformClientMu.Unlock()
	s.setPlatformServicesLocked(pc)
}

// ensurePlatformClient lazily builds the default platform client — and every
// service on top of it — the first time a signed-in session token arrives on
// a daemon that was never given a platform URL, API key, or license key at
// startup (#756). A signed-in session is itself proof a platform exists; the
// session token carries no URL of its own, so this defaults to
// platform.DefaultConfig()'s base URL exactly as the eagerly-configured path
// in cmd/nightgauge/main.go does when api_url is unset.
//
// Double-checked under platformClientMu: two setSessionToken calls racing on
// a cold daemon (e.g. a stale sign-in event replayed alongside a fresh one)
// must not each build their own client. The loser reuses whatever the winner
// built and applies its own token to that one client.
//
// StartHealthPolling is started in its own goroutine, not inline: its first
// check runs synchronously before it returns (see platform.Client), and
// running that here would hold platformClientMu.Lock() — blocking every
// OTHER platform.* request on this daemon — for the length of a real network
// round trip to the platform. The eager path in cmd/nightgauge/main.go can
// afford that cost inline because it runs once at startup, before the IPC
// server accepts any request; this path runs mid-Run(), under contention.
// sessionOnlyPlatformConfig is the config the lazy path builds from when a
// session token arrives and no client exists yet. A signed-in session carries
// no api_url, so the base URL can only come from the default.
//
// It is a named function rather than an inline platform.DefaultConfig() so a
// test can assert what the lazy path resolves to WITHOUT reading Client.base —
// that field has exactly one sanctioned reader (Client.newRequest, #750), and
// re-exposing it through an accessor would put a second URL source back in
// reach of the very code the guard exists to constrain.
func sessionOnlyPlatformConfig() platform.Config {
	return platform.DefaultConfig()
}

func (s *Server) ensurePlatformClient() (*platform.Client, error) {
	s.platformClientMu.Lock()
	defer s.platformClientMu.Unlock()
	if s.platformClient != nil {
		return s.platformClient, nil
	}
	pc, err := platform.NewClient(sessionOnlyPlatformConfig())
	if err != nil {
		return nil, err
	}
	go pc.StartHealthPolling(context.Background())
	s.setPlatformServicesLocked(pc)
	return pc, nil
}

// getPlatformClient returns the current platform client, or nil when none has
// been configured (eagerly at startup) or constructed yet (lazily, on the
// first signed-in session — see ensurePlatformClient). Every handler must
// read the field through this getter rather than s.platformClient directly:
// IPC requests are dispatched one goroutine per call, and ensurePlatformClient
// can write this field from any of them at any time.
func (s *Server) getPlatformClient() *platform.Client {
	s.platformClientMu.RLock()
	defer s.platformClientMu.RUnlock()
	return s.platformClient
}

func (s *Server) getLicenseSvc() *platform.LicenseService {
	s.platformClientMu.RLock()
	defer s.platformClientMu.RUnlock()
	return s.licenseSvc
}

func (s *Server) getSkillSvc() *platform.SkillService {
	s.platformClientMu.RLock()
	defer s.platformClientMu.RUnlock()
	return s.skillSvc
}

func (s *Server) getAnalyticsSvc() *platform.AnalyticsService {
	s.platformClientMu.RLock()
	defer s.platformClientMu.RUnlock()
	return s.analyticsSvc
}

func (s *Server) getComplianceSvc() *platform.ComplianceService {
	s.platformClientMu.RLock()
	defer s.platformClientMu.RUnlock()
	return s.complianceSvc
}

func (s *Server) getAuditRetentionSvc() *platform.AuditRetentionService {
	s.platformClientMu.RLock()
	defer s.platformClientMu.RUnlock()
	return s.auditRetentionSvc
}

func (s *Server) getTeamSvc() *platform.TeamService {
	s.platformClientMu.RLock()
	defer s.platformClientMu.RUnlock()
	return s.teamSvc
}

func (s *Server) getBillingSvc() *platform.BillingService {
	s.platformClientMu.RLock()
	defer s.platformClientMu.RUnlock()
	return s.billingSvc
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
	// THE PRODUCTION ATTACH PATH, and therefore the one that must wire
	// Decision 11's scheduler arm. `nightgauge serve` builds the scheduler
	// AFTER the server (it needs IpcStageRunner) and reaches the server through
	// here; WithScheduler has no production caller at all. Wiring the registry
	// only there left `s.schedulerRuns` nil in every real deployment, so every
	// scheduler-run phase event fell through to ADOPTION — a phantom registry
	// entry that holds a lease, is never terminal-claimed, poisons the derived
	// issue index and swallows the PhaseHistory the scheduler's own runtime
	// should have received.
	//
	// Guarded exactly as WithScheduler is: a nil *Scheduler stored in an
	// interface field is a NON-nil interface, and every `if s.schedulerRuns !=
	// nil` guard downstream would then call through a nil receiver. The guard
	// covers the callback wiring too — `initSchedulerCallbacks` dereferences
	// its argument on the first line.
	if sched == nil {
		return
	}
	s.schedulerRuns = sched
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
	// Root each run's on-disk state (trace, runtime-{issue}-{runId}.json, stage-context,
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
			entry := map[string]interface{}{
				"stage":        string(sr.Stage),
				"inputTokens":  sr.InputTokens,
				"outputTokens": sr.OutputTokens,
				"cacheRead":    sr.CacheRead,
				"costUsd":      sr.CostUSD,
			}
			// The model that ACTUALLY executed the stage (#141). StageResult
			// itself carries no model, but the runtime records the served model
			// per stage — the same source BuildV2Record uses for
			// model_selection — so the event can carry it without inventing one.
			// Absent when the stage ran no model (deterministic/skipped path):
			// omitted rather than emitted empty, so "unknown" stays
			// distinguishable from "no model".
			if m := snap.StageModels[string(sr.Stage)]; m != "" {
				entry["model"] = m
			}
			perStage[i] = entry
		}
		now := time.Now()
		durationMs := int64(0)
		startedAt := now.Format(time.RFC3339)
		if !snap.StartedAt.IsZero() {
			startedAt = snap.StartedAt.Format(time.RFC3339)
			durationMs = now.Sub(snap.StartedAt).Milliseconds()
		}
		// Run identity (#141). `repo` and `runId` were both in scope here and
		// dropped, which left every downstream consumer keying on issueNumber
		// alone — ambiguous the moment two repos in one workspace carry the
		// same issue number — and left the extension unable to correlate this
		// event with the authoritative run record the scheduler writes.
		// runId is the same UUID v7 that record carries, so consumers can join
		// on it instead of guessing by (issue, timestamp).
		s.Emit("pipeline.complete", map[string]interface{}{
			"executionId":       fmt.Sprintf("%s#%d", cbRepo, issue),
			"repo":              cbRepo,
			"runId":             snap.RunID,
			"issueNumber":       issue,
			"success":           ok,
			"totalInputTokens":  snap.InputTokens,
			"totalOutputTokens": snap.OutputTokens,
			"totalCostUSD":      snap.TotalCostUSD,
			"startedAt":         startedAt,
			"durationMs":        durationMs,
			"perStage":          perStage,
		})
		// The run identity travels on the envelope (ADR-017 Decision 6),
		// sourced from the snapshot this callback is about. NOTE: the
		// TypeScript consumer of pipeline.historyRecorded is the GLOBAL
		// dashboard refresh and must stay UNFILTERED — the id is here to
		// correlate, not to route.
		s.Emit("pipeline.historyRecorded", map[string]interface{}{
			"issueNumber": issue,
			"runId":       snap.RunID,
			"success":     ok,
		})
		if snap.LicenseExpiredMidRun {
			// The last envelope in this callback that described a run without
			// naming it. Every sibling above carries the identity off the same
			// snapshot; this one keyed on issueNumber alone, which is ambiguous
			// the moment two dispatches of one issue overlap (ADR-017 Decision
			// 6). Envelope completeness only — the TS consumer is a global
			// warning toast and gains no routing from it.
			s.Emit("pipeline.licenseExpired", map[string]interface{}{
				"issueNumber": issue,
				"runId":       snap.RunID,
			})
		}
		owner, _ := splitOwnerRepo(cbRepo)
		s.Emit("tree.in-progress.update", TreeUpdateEvent{
			Owner: owner,
		})
	})
	sched.OnStateChanged(func(cbRepo string, issue int, runtime *state.RuntimeState) {
		snap := runtime
		// runId from the snapshot the callback carries — the scheduler stamps
		// a real identity on every run, so the ordinary case is a strict match
		// for the extension's run-id routing filter (ADR-017 Decision 6).
		s.Emit("pipeline.stateChanged", map[string]interface{}{
			"repo":        cbRepo,
			"issueNumber": issue,
			"runId":       snap.RunID,
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
		// Resolved from the scheduler's OWN registry, never fabricated: the
		// callback fires from inside runPipeline for this issue, so the
		// registered runtime IS the run this event is about (ADR-017 Decision
		// 6). An empty id (the run already unregistered) falls back to the
		// consumer's issue-number pre-filter rather than being dropped.
		s.Emit("phase.start", map[string]interface{}{
			"repo":        cbRepo,
			"issueNumber": issue,
			"runId":       sched.RunIDForIssue(issue),
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
		s.setWorkspaceRoot(root)
	}
}

// workspaceRootPath returns the IPC launch root. THE ONLY READER OF THE FIELD.
//
// The field is written after construction by workspace.setRoot, so once the
// startup reconcile stopped being inline (ADR-017 7.3) an unlocked read from the
// sweep's goroutine became a data race the detector proves. Guarding one field
// rather than the two goroutines that happen to meet today is what keeps the
// next background reader from re-opening it.
func (s *Server) workspaceRootPath() string {
	s.workspaceRootMu.RLock()
	defer s.workspaceRootMu.RUnlock()
	return s.workspaceRoot
}

// resolvedEstimateAdapter is the execution adapter a cost forecast is priced
// against when the client pinned none (#696). It runs the canonical adapter
// precedence chain (#54) over "feature-dev" — the representative stage the
// estimate's model routing is already based on — and falls back to the Go
// layer's own default adapter rather than to an unstated anthropic assumption.
func (s *Server) resolvedEstimateAdapter() string {
	cfg, err := config.Load(s.workspaceRootPath())
	if err != nil {
		cfg = nil
	}
	if r := config.ResolveStageAdapter(cfg, "feature-dev", os.Getenv); r.Adapter != "" {
		return r.Adapter
	}
	return "claude-headless"
}

// setWorkspaceRoot is THE ONLY WRITER of either field. It takes no other lock,
// so it cannot participate in a lock cycle.
//
// The FIRST non-empty root also becomes the immutable launch root. First rather
// than "the one WithWorkspaceRoot passed", because a server may be constructed
// without one and told its root by the client's opening workspace.setRoot —
// that call is this process's launch root by any honest reading.
func (s *Server) setWorkspaceRoot(root string) {
	s.workspaceRootMu.Lock()
	defer s.workspaceRootMu.Unlock()
	s.workspaceRoot = root
	if s.launchRoot == "" {
		s.launchRoot = root
	}
}

// launchRootPath returns the root this server started in. See launchRoot.
func (s *Server) launchRootPath() string {
	s.workspaceRootMu.RLock()
	defer s.workspaceRootMu.RUnlock()
	return s.launchRoot
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

// WithForgeClientFactory registers the resolver attention.sweep uses to obtain
// a forge client per repo. The closure is built in cmd/, where the router
// builder is already in scope, so internal/ipc keeps no dependency on it.
// Without it, attention.sweep reports Unavailable rather than failing — a
// daemon with no forge configured is a legitimate local-first state.
func WithForgeClientFactory(fn func(repo string) (forge.ForgeClient, error)) ServerOption {
	return func(s *Server) {
		s.forgeClientFn = fn
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
	return s.workspaceRootPath()
}

// pipelineStateDir resolves the .nightgauge/pipeline directory a run's
// runtime-{issue}-{runId}.json belongs in, scoped to the run's target repo via repoRoot.
// Returns "" when no root resolves (e.g. an unconfigured server).
//
// The SLUG→root resolution is this server's own business; the layout is not, so
// the join is state.PipelineStateDir's (#410). A second hand-written copy of the
// path is a second answer to "where does a run's state live?" waiting to
// disagree.
func (s *Server) pipelineStateDir(repo string) string {
	root := s.repoRoot(repo)
	if root == "" {
		return ""
	}
	return state.PipelineStateDir(root)
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
	if s.getAnalyticsSvc() != nil {
		s.getAnalyticsSvc().StartAutoFlush(ctx)
	}

	// Close out platform rows orphaned by runs that died with this server's
	// previous incarnation (#44) — DEFERRED by startupGrace and re-evaluated
	// from scratch at expiry (ADR-017 7.3). Server start is extension
	// activation, but it is ALSO the client's automatic backend restart, under
	// which the extension host and every in-flight run survive; an inline sweep
	// closed those live runs (F26). Nothing about the handshake below waits for
	// it.
	s.startDeferredReconcile(ctx)

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
		s.sendError(req.ID, rpcCodeFor(err), err.Error())
		return
	}

	s.sendResponse(Response{
		ID:     req.ID,
		Result: result,
	})
}

// rpcCodeFor maps a handler error onto its JSON-RPC code. An ADR-017 identity
// refusal gets its own code so a client can tell "this run message was refused"
// from "the server broke"; everything else stays ErrInternal.
func rpcCodeFor(err error) int {
	var rie *runIdentityError
	if errors.As(err, &rie) {
		return ErrRunIdentity
	}
	return ErrInternal
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
		s.setWorkspaceRoot(p.Root)
		// A multi-repo workspace switch exposes a different .nightgauge/pipeline
		// dir — close out any runs orphaned there too (#44). Idempotent: each
		// reconciled snapshot is removed after its terminal event is emitted.
		//
		// STILL INLINE (ADR-017 7.3): a setRoot arrives from a connected, live
		// extension host, so ladder arms 1 and 2 carry real information here.
		// Inside the server's own startup grace arm 5 defers every candidate
		// anyway — so during the window this pass removes terminal snapshots
		// (they carry their own proof, 7.4's first row) and nothing else: claim
		// releases defer on the same arm, and the reaping is skipped whole.
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
			root = s.workspaceRootPath()
		}
		if root == "" {
			return nil, fmt.Errorf("no workspace root configured")
		}
		cfg, err := config.Load(root)
		if err != nil {
			return nil, fmt.Errorf("load config: %w", err)
		}
		projects := make([]ConfigProjectEntry, 0, len(cfg.Projects))
		for _, project := range cfg.Projects {
			projects = append(projects, ConfigProjectEntry{
				Name: project.Name, Number: project.Number,
				SyncFilter: project.SyncFilter, Default: project.Default,
			})
		}
		return &ConfigGetProjectResult{
			Owner:         cfg.Owner,
			ProjectNumber: cfg.ProjectNumber,
			Projects:      projects,
			DefaultRepo:   cfg.DefaultRepo,
			OwnerType:     cfg.OwnerType,
		}, nil
	}

	//ipc:method configGetHealthThresholds params:none result:ConfigGetHealthThresholdsResult
	s.methods["config.getHealthThresholds"] = func(_ context.Context, _ json.RawMessage) (interface{}, error) {
		root := s.workspaceRootPath()
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
			root = s.workspaceRootPath()
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
		root := s.workspaceRootPath()
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
		return s.boardServicesFor(c, p.Owner, p.ProjectNumber, gh.ParseOwnerType(p.OwnerType)).
			Board.ListItems(ctx, p.Status)
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
		return s.boardServicesFor(c, p.Owner, p.ProjectNumber, gh.ParseOwnerType(p.OwnerType)).
			Board.CountsByStatus(ctx)
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
		root := s.workspaceRootPath()
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
		root := s.workspaceRootPath()
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
		stateSvc := s.boardStateFor(c, p.Owner, p.ProjectNumber, gh.ParseOwnerType(p.OwnerType))
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
	modelRouter := routing.NewRouter(s.getPlatformClient(), s.workspaceRootPath())
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
		// The forecast is priced through the serving adapter's provider
		// (#696). A client that did not pin one gets the workspace's own
		// resolved adapter, never a silent anthropic assumption.
		adapter := strings.TrimSpace(p.Adapter)
		if adapter == "" {
			adapter = s.resolvedEstimateAdapter()
		}
		return tokens.EstimateCost(adapter, p.Stages, p.ComplexityScore), nil
	}

	// --- Platform methods ---

	//ipc:method platformStatus params:none result:PlatformStatus
	s.methods["platform.status"] = func(_ context.Context, _ json.RawMessage) (interface{}, error) {
		if s.getPlatformClient() == nil {
			return map[string]interface{}{
				"mode":    string(platform.ModeOffline),
				"message": "platform client not configured",
			}, nil
		}
		result := map[string]interface{}{
			"mode": string(s.getPlatformClient().Mode()),
		}
		if s.getLicenseSvc() != nil {
			result["tier"] = s.getLicenseSvc().CurrentTier()
		}
		return result, nil
	}

	//ipc:method platformLicense params:none result:LicenseInfo
	s.methods["platform.license"] = func(ctx context.Context, _ json.RawMessage) (interface{}, error) {
		if s.getLicenseSvc() == nil {
			return platform.CommunityLicenseInfo(), nil
		}
		info, err := s.getLicenseSvc().Validate(ctx)
		if err != nil {
			return nil, err
		}
		return info, nil
	}

	//ipc:method platformResolveSkill params:PlatformResolveSkillParams result:CachedSkill
	s.methods["platform.resolveSkill"] = func(ctx context.Context, params json.RawMessage) (interface{}, error) {
		if s.getSkillSvc() == nil {
			return nil, fmt.Errorf("platform client not configured")
		}
		var p PlatformResolveSkillParams
		if err := json.Unmarshal(params, &p); err != nil {
			return nil, fmt.Errorf("invalid params: %w", err)
		}
		return s.getSkillSvc().Resolve(ctx, p.SkillID, &platform.SkillResolveOptions{
			ComplexityScore: p.ComplexityScore,
			IssueType:       p.IssueType,
			SizeLabel:       p.SizeLabel,
		})
	}

	//ipc:method platformValidateLicense params:PlatformValidateLicenseParams result:LicenseInfo
	s.methods["platform.validateLicense"] = func(ctx context.Context, params json.RawMessage) (interface{}, error) {
		if s.getLicenseSvc() == nil {
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
		if p.LicenseKey != "" && p.LicenseKey != s.getLicenseSvc().ConfiguredKey() {
			return s.getLicenseSvc().ValidateKey(ctx, p.LicenseKey)
		}
		return s.getLicenseSvc().Validate(ctx)
	}

	//ipc:method platformStartTrial params:PlatformStartTrialParams result:TrialResult
	s.methods["platform.startTrial"] = func(ctx context.Context, params json.RawMessage) (interface{}, error) {
		if s.getLicenseSvc() == nil {
			return nil, fmt.Errorf("platform client not configured")
		}
		var p PlatformStartTrialParams
		if err := json.Unmarshal(params, &p); err != nil {
			return nil, fmt.Errorf("invalid params: %w", err)
		}
		// The device-flow JWT is applied as a per-call bearer inside StartTrial and
		// is never logged here. A typed *platform.TrialError (NOT_ELIGIBLE / 401 /
		// transport) propagates to the TS command for a precise message.
		return s.getLicenseSvc().StartTrial(ctx, p.AccessToken)
	}

	//ipc:method platformSubmitAnalytics params:PlatformSubmitAnalyticsParams result:StatusOK
	s.methods["platform.submitAnalytics"] = func(ctx context.Context, params json.RawMessage) (interface{}, error) {
		var p PlatformSubmitAnalyticsParams
		if err := json.Unmarshal(params, &p); err != nil {
			return nil, fmt.Errorf("invalid params: %w", err)
		}
		if s.getAnalyticsSvc() != nil {
			// Fire-and-forget: buffer locally, return immediately
			s.getAnalyticsSvc().Ingest(ctx, "", 0, []platform.AnalyticsEvent{{
				Type:      p.EventType,
				Timestamp: time.Now(),
				Data:      p.Payload,
			}})
		}
		return map[string]string{"status": "ok"}, nil
	}

	//ipc:method platformGetUsageSummary params:none result:UsageSummaryResult
	s.methods["platform.getUsageSummary"] = func(ctx context.Context, _ json.RawMessage) (interface{}, error) {
		if s.getAnalyticsSvc() == nil {
			return nil, fmt.Errorf("platform client not configured")
		}
		return s.getAnalyticsSvc().GetUsageSummary(ctx)
	}

	//ipc:method platformGetCostAnalytics params:PlatformCostAnalyticsParams result:CostAnalyticsResult
	s.methods["platform.getCostAnalytics"] = func(ctx context.Context, params json.RawMessage) (interface{}, error) {
		if s.getAnalyticsSvc() == nil {
			return nil, fmt.Errorf("analytics service unavailable")
		}
		var p PlatformCostAnalyticsParams
		_ = json.Unmarshal(params, &p)
		return s.getAnalyticsSvc().GetCostAnalytics(ctx, p.StartDate, p.EndDate)
	}

	//ipc:method platformGetAnalyticsHealth params:none result:AnalyticsHealthResult
	s.methods["platform.getAnalyticsHealth"] = func(ctx context.Context, _ json.RawMessage) (interface{}, error) {
		if s.getAnalyticsSvc() == nil {
			return nil, fmt.Errorf("analytics service unavailable")
		}
		return s.getAnalyticsSvc().GetAnalyticsHealth(ctx)
	}

	//ipc:method platformGetAnalyticsRuns params:PlatformAnalyticsRunsParams result:AnalyticsRunsResult
	s.methods["platform.getAnalyticsRuns"] = func(ctx context.Context, params json.RawMessage) (interface{}, error) {
		if s.getAnalyticsSvc() == nil {
			return nil, fmt.Errorf("analytics service unavailable")
		}
		var p PlatformAnalyticsRunsParams
		_ = json.Unmarshal(params, &p)
		return s.getAnalyticsSvc().GetAnalyticsRuns(ctx, p.Cursor, p.Limit)
	}

	//ipc:method platformGetAnalyticsTrends params:PlatformGetAnalyticsTrendsParams result:AnalyticsTrendsResult
	s.methods["platform.getAnalyticsTrends"] = func(ctx context.Context, params json.RawMessage) (interface{}, error) {
		if s.getAnalyticsSvc() == nil {
			return nil, fmt.Errorf("analytics service unavailable")
		}
		var p PlatformGetAnalyticsTrendsParams
		_ = json.Unmarshal(params, &p)
		if p.Period == "" {
			p.Period = "30d"
		}
		return s.getAnalyticsSvc().GetAnalyticsTrends(ctx, p.Period)
	}

	//ipc:method platformAuditGenerateReport params:PlatformAuditGenerateReportParams result:ComplianceReportResult
	s.methods["platform.auditGenerateReport"] = func(ctx context.Context, params json.RawMessage) (interface{}, error) {
		if s.getComplianceSvc() == nil {
			return nil, fmt.Errorf("compliance service unavailable")
		}
		var p PlatformAuditGenerateReportParams
		if err := json.Unmarshal(params, &p); err != nil {
			return nil, fmt.Errorf("invalid params: %w", err)
		}
		return s.getComplianceSvc().GenerateReport(ctx, p.ReportType, p.StartDate, p.EndDate, p.Format)
	}

	//ipc:method platformAuditListReports params:PlatformAuditListReportsParams result:ComplianceReportsResult
	s.methods["platform.auditListReports"] = func(ctx context.Context, _ json.RawMessage) (interface{}, error) {
		if s.getComplianceSvc() == nil {
			return nil, fmt.Errorf("compliance service unavailable")
		}
		return s.getComplianceSvc().ListReports(ctx)
	}

	//ipc:method platformAuditGetReport params:PlatformAuditGetReportParams result:ComplianceReportDetail
	s.methods["platform.auditGetReport"] = func(ctx context.Context, params json.RawMessage) (interface{}, error) {
		if s.getComplianceSvc() == nil {
			return nil, fmt.Errorf("compliance service unavailable")
		}
		var p PlatformAuditGetReportParams
		if err := json.Unmarshal(params, &p); err != nil {
			return nil, fmt.Errorf("invalid params: %w", err)
		}
		return s.getComplianceSvc().GetReport(ctx, p.ReportID)
	}

	//ipc:method platformAuditDownloadReport params:PlatformAuditDownloadReportParams result:ComplianceReportDownload
	s.methods["platform.auditDownloadReport"] = func(ctx context.Context, params json.RawMessage) (interface{}, error) {
		if s.getComplianceSvc() == nil {
			return nil, fmt.Errorf("compliance service unavailable")
		}
		var p PlatformAuditDownloadReportParams
		if err := json.Unmarshal(params, &p); err != nil {
			return nil, fmt.Errorf("invalid params: %w", err)
		}
		return s.getComplianceSvc().DownloadReport(ctx, p.ReportID)
	}

	//ipc:method auditGetRetentionConfig params:AuditGetRetentionConfigParams result:RetentionConfig
	s.methods["audit.getRetentionConfig"] = func(ctx context.Context, params json.RawMessage) (interface{}, error) {
		if s.getAuditRetentionSvc() == nil {
			return nil, fmt.Errorf("audit retention service unavailable")
		}
		return s.getAuditRetentionSvc().GetRetentionConfig(ctx)
	}

	//ipc:method auditUpdateRetentionConfig params:AuditUpdateRetentionConfigParams result:RetentionConfig
	s.methods["audit.updateRetentionConfig"] = func(ctx context.Context, params json.RawMessage) (interface{}, error) {
		if s.getAuditRetentionSvc() == nil {
			return nil, fmt.Errorf("audit retention service unavailable")
		}
		var p AuditUpdateRetentionConfigParams
		if err := json.Unmarshal(params, &p); err != nil {
			return nil, fmt.Errorf("invalid params: %w", err)
		}
		if p.RetentionDays < 1 || p.RetentionDays > 3650 {
			return nil, fmt.Errorf("retentionDays must be between 1 and 3650")
		}
		return s.getAuditRetentionSvc().UpdateRetentionConfig(ctx, p.RetentionDays)
	}

	//ipc:method auditVerifyIntegrity params:AuditVerifyIntegrityParams result:IntegrityResult
	s.methods["audit.verifyIntegrity"] = func(ctx context.Context, params json.RawMessage) (interface{}, error) {
		if s.getAuditRetentionSvc() == nil {
			return nil, fmt.Errorf("audit retention service unavailable")
		}
		var p AuditVerifyIntegrityParams
		if err := json.Unmarshal(params, &p); err != nil {
			return nil, fmt.Errorf("invalid params: %w", err)
		}
		if p.WindowDays != 30 && p.WindowDays != 90 && p.WindowDays != 365 {
			return nil, fmt.Errorf("windowDays must be 30, 90, or 365")
		}
		return s.getAuditRetentionSvc().VerifyIntegrity(ctx, p.WindowDays)
	}

	//ipc:method platformSyncTelemetry params:PlatformSyncTelemetryParams result:PlatformSyncTelemetryResult
	s.methods["platform.syncTelemetry"] = func(ctx context.Context, params json.RawMessage) (interface{}, error) {
		var p PlatformSyncTelemetryParams
		if err := json.Unmarshal(params, &p); err != nil {
			return nil, fmt.Errorf("invalid params: %w", err)
		}
		if s.getAnalyticsSvc() == nil {
			return nil, fmt.Errorf("platform client not configured")
		}
		if s.workspaceRootPath() == "" {
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

		hw := state.NewHistoryWriter(s.workspaceRootPath())
		records, err := hw.ReadRecentV2(limit, daysBack)
		if err != nil {
			return nil, fmt.Errorf("read history: %w", err)
		}

		// The repo param is forwarded to SyncTelemetry and applied to all
		// records as the ExecutionHistoryRunRecord.Repo value.

		syncCtx, cancel := context.WithTimeout(ctx, 2*time.Minute)
		defer cancel()

		res := s.getAnalyticsSvc().SyncTelemetry(syncCtx, records, p.Repo)
		return PlatformSyncTelemetryResult{
			Synced: res.Synced,
			Failed: res.Failed,
			Errors: res.Errors,
		}, nil
	}

	//ipc:method platformGetTeamMembers params:none result:TeamMemberResult[] nullable
	s.methods["platform.getTeamMembers"] = func(ctx context.Context, _ json.RawMessage) (interface{}, error) {
		if s.getTeamSvc() == nil {
			return nil, fmt.Errorf("platform client not configured")
		}
		return s.getTeamSvc().GetMembers(ctx)
	}

	//ipc:method platformCreatePortalSession params:none result:PortalSessionResult
	s.methods["platform.createPortalSession"] = func(ctx context.Context, _ json.RawMessage) (interface{}, error) {
		if s.getBillingSvc() == nil {
			return nil, fmt.Errorf("platform client not configured")
		}
		return s.getBillingSvc().CreatePortalSession(ctx)
	}

	//ipc:method platformHealthCheck params:none result:HealthResponse
	s.methods["platform.healthCheck"] = func(ctx context.Context, _ json.RawMessage) (interface{}, error) {
		if s.getPlatformClient() == nil {
			return map[string]interface{}{"status": "offline", "mode": "offline"}, nil
		}
		resp, err := s.getPlatformClient().API().GetHealthWithResponse(ctx)
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
		if s.getPlatformClient() == nil {
			return nil, fmt.Errorf("platform client not configured")
		}
		resp, err := s.getPlatformClient().API().AuthDeviceCodeWithResponse(ctx)
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
		if s.getPlatformClient() == nil {
			return nil, fmt.Errorf("platform client not configured")
		}
		var p PlatformAuthDeviceTokenParams
		if err := json.Unmarshal(params, &p); err != nil {
			return nil, fmt.Errorf("invalid params: %w", err)
		}
		if p.DeviceCode == "" {
			return nil, fmt.Errorf("deviceCode is required")
		}
		resp, err := s.getPlatformClient().API().AuthDeviceTokenWithResponse(ctx, platformapi.AuthDeviceTokenJSONRequestBody{
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
		if s.getPlatformClient() == nil {
			return nil, fmt.Errorf("platform client not configured")
		}
		var p PlatformAuthGithubParams
		if err := json.Unmarshal(params, &p); err != nil {
			return nil, fmt.Errorf("invalid params: %w", err)
		}
		if p.GithubAccessToken == "" {
			return nil, fmt.Errorf("githubAccessToken is required")
		}
		resp, err := s.getPlatformClient().API().AuthGithubWithResponse(ctx, platformapi.AuthGithubJSONRequestBody{
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
		if s.getPlatformClient() == nil {
			return nil, fmt.Errorf("platform client not configured")
		}
		var p PlatformAuthRefreshParams
		if err := json.Unmarshal(params, &p); err != nil {
			return nil, fmt.Errorf("invalid params: %w", err)
		}
		if p.RefreshToken == "" {
			return nil, fmt.Errorf("refreshToken is required")
		}
		resp, err := s.getPlatformClient().API().AuthRefreshWithResponse(ctx, platformapi.AuthRefreshJSONRequestBody{
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
		if s.getPlatformClient() == nil {
			return nil, fmt.Errorf("platform client not configured")
		}
		var p PlatformAuthSignoutParams
		if err := json.Unmarshal(params, &p); err != nil {
			return nil, fmt.Errorf("invalid params: %w", err)
		}
		if p.RefreshToken == "" {
			return nil, fmt.Errorf("refreshToken is required")
		}
		resp, err := s.getPlatformClient().API().AuthSignoutWithResponse(ctx, platformapi.AuthSignoutJSONRequestBody{
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

	// Hand the daemon the signed-in user's JWT (#742).
	//
	// The extension signs a user in and keeps the access token in
	// SecretStorage, but the daemon is spawned with a *license key* in its
	// environment and resolved its credential once, at construction. A license
	// key identifies an account, not a user, so every user-scoped route
	// (/v1/analytics/{health,trends,cost}, /v1/audit/reports) answered 401 for a
	// signed-in user — the Health, Trends, Cost and Compliance tabs failed by
	// construction.
	//
	// This is a push rather than another spawn-time env var on purpose: access
	// tokens expire, TokenRefreshManager rotates them for the life of the
	// session, and an env var frozen at spawn would work for exactly one token
	// lifetime and then regress silently. An empty token clears the credential,
	// which is what sign-out sends.
	//
	// A signed-in session is itself proof a platform exists (#756): a daemon
	// spawned with no api_url/api_key/license_key at all — the state a
	// brand-new user is in the instant they sign in — otherwise never gets a
	// platform client to receive this push, and every platform.* method
	// (including this one) answers "not configured" forever. So a REAL token
	// (not the empty one sign-out/an unauthenticated startup sync sends)
	// lazily builds the default client here instead of erroring. An empty
	// token on a still-nil client is left alone: there is nothing to clear,
	// and the daemon genuinely has no platform configured, which is exactly
	// the state platform.status and friends must keep reporting accurately
	// (pairs with #748's not_configured classification, which keys off the
	// "not configured" text below — keep it if this message ever changes).
	//ipc:method platformSetSessionToken params:PlatformSetSessionTokenParams result:StatusOK
	s.methods["platform.setSessionToken"] = func(_ context.Context, params json.RawMessage) (interface{}, error) {
		var p PlatformSetSessionTokenParams
		if len(params) > 0 && string(params) != "null" {
			if err := json.Unmarshal(params, &p); err != nil {
				return nil, fmt.Errorf("invalid params: %w", err)
			}
		}
		pc := s.getPlatformClient()
		if pc == nil {
			if strings.TrimSpace(p.Token) == "" {
				return nil, fmt.Errorf("platform client not configured")
			}
			var err error
			pc, err = s.ensurePlatformClient()
			if err != nil {
				return nil, fmt.Errorf("platform client: %w", err)
			}
		}
		pc.SetSessionToken(p.Token)
		return map[string]bool{"ok": true}, nil
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
		stateSvc := s.boardStateFor(c, p.Owner, p.ProjectNumber, gh.ParseOwnerType(p.OwnerType))
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
		// The epic needs no read: the sub-issue endpoint addresses it by
		// number. Only CreateIssue still needs a node ID, and that is the
		// repository's, below.
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
		if err := svc.AddSubIssue(ctx,
			forge.IssueRef{Owner: p.Owner, Repo: p.Repo, Number: p.EpicNumber},
			forge.IssueRef{Owner: p.Owner, Repo: p.Repo, Number: created.Number},
		); err != nil {
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
		// Both GetIssue reads that used to precede this existed only to turn
		// two numbers into two node IDs.
		if err := svc.AddSubIssue(ctx,
			forge.IssueRef{Owner: p.Owner, Repo: p.Repo, Number: p.EpicNumber},
			forge.IssueRef{Owner: p.Owner, Repo: p.Repo, Number: p.IssueNumber},
		); err != nil {
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
				if cfg, err := config.Load(s.workspaceRootPath()); err == nil && cfg.Autonomous.IsStallEscalationEnabled() {
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
			if _, err := s.scheduler.RunQueue(ctx); err != nil {
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
				if cfg, err := config.Load(s.workspaceRootPath()); err == nil && cfg.Autonomous.IsStallEscalationEnabled() {
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

		// THE OPERATOR-WIDE ARM, and it exists for this verb only.
		// MattermostCommandDispatcher's /pause and /resume send
		// pipelineSetPaused(0, …) — a GLOBAL pause naming no run, because there
		// is no run to name. An operator-wide pause is a DIFFERENT TRANSACTION
		// from pausing one run, so it is accepted without an identity and it
		// touches nothing: no registry, no runtime, no disk, no event. Retarget
		// onto a verb that means what it says is tracked in #423.
		//
		// Checked BEFORE the identity checks: an issue number of 0 is the signal
		// that this is not a run message at all.
		if p.IssueNumber == 0 {
			log.Printf("setPaused: operator-wide pause=%v arm (issueNumber 0, no run identity) — no runtime touched, nothing persisted, nothing emitted (ADR-017 step 4; retarget tracked in #423)", p.Paused)
			return map[string]string{"status": "ok"}, nil
		}

		// ADMINISTRATIVE CLASS (Decision 3): a caller asserting something ABOUT
		// a run. It RESOLVES, NEVER INVENTS — a live entry is served and
		// corroborated against repo + issueNumber, a scheduler-owned run is
		// refused run_wrong_owner, a snapshot on disk is adopted through the
		// singleflight with its lease left at zero, and nothing at all is
		// run_not_found. The stub-minting create-on-miss this replaces is F9:
		// it pinned an issue against #44 forever with a runtime nobody ran.
		res, err := s.resolveRun("pipeline.setPaused", verbAdministrative, p.RunID, p.Repo, p.IssueNumber)
		if err != nil {
			return nil, err
		}
		rt := res.rs

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
		//
		// The write goes through the LIVE adopted object, which is the whole
		// point of routing the administrative path through adoption (F33): from
		// the instant the entry is installed, rs.mu serialises this write against
		// every Persist the live run makes, so there is exactly one
		// *RuntimeState for the id to disagree with itself about.
		if snap.Repo != "" {
			if stateDir := s.pipelineStateDir(snap.Repo); stateDir != "" {
				if err := rt.Persist(stateDir); err != nil {
					return nil, fmt.Errorf("persist pause state: %w", err)
				}
			}
		}

		// Emit stateChanged so UI updates. The envelope carries the run identity
		// the server RESOLVED (Decision 6) — never the caller's parameter
		// unresolved, and never a fabricated one.
		s.Emit("pipeline.stateChanged", map[string]interface{}{
			"repo":        snap.Repo,
			"issueNumber": p.IssueNumber,
			"runId":       snap.RunID,
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
		// LOOKUP CLASS (ADR-017 Decision 3): not a run message. It carries no
		// identity requirement, issue-addressed reads stay supported, and
		// "nothing resolved" is an EMPTY RESPONSE rather than an error.
		//
		// Tier 0 — live execution state (scheduler path, key "owner/repo#N").
		// Scheduler-driven runs are not in the IPC issue index and are never
		// "current" there; they continue to resolve here, unchanged.
		if s.execMgr != nil {
			key := fmt.Sprintf("%s/%s#%d", p.Owner, p.Repo, p.IssueNumber)
			if st := s.execMgr.GetState(key); st != nil {
				return st, nil
			}
		}
		repoSlug := ""
		if p.Owner != "" && p.Repo != "" {
			repoSlug = p.Owner + "/" + p.Repo
		}
		// Tier 1 — the DERIVED ISSUE INDEX (Decision 6): the non-abandoned,
		// non-terminal entry for repo#issue with the newest lease. Replaces the
		// old `activeRuntimes[issueNumber]` lookup, which could not exist once
		// the registry keys on the run.
		if current, others := s.currentRunForIssue(repoSlug, p.IssueNumber); current != nil {
			snap := current.rs.Snapshot()
			return newPipelineGetStateResult(snap, others), nil
		}
		// Tier 2 — persisted snapshots for the issue in that repo's dir, where
		// notifyStageTransition/setPaused persist them (#215).
		if stateDir := s.pipelineStateDir(repoSlug); stateDir != "" {
			found, err := state.FindPersistedStatesForIssue(stateDir, p.IssueNumber)
			if err == nil && len(found) > 0 {
				// The standard pick: prefer a non-terminal snapshot, then newest
				// StartedAt (Decision 8). FindPersistedStatesForIssue already
				// sorts newest-first.
				pick := found[0]
				for _, c := range found {
					if !c.Terminal {
						pick = c
						break
					}
				}
				var others []string
				for _, c := range found {
					if c != pick {
						others = append(others, c.RunID)
					}
				}
				return newPipelineGetStateResult(pick, others), nil
			}
		}
		// Issue-addressed reads may now return NOTHING where they previously
		// returned a dead run's snapshot indefinitely (F12). That is an accepted
		// improvement: no answer is better than a confidently wrong one.
		return nil, nil
	}

	// --- Pipeline state notifications (HeadlessOrchestrator path) ---

	//ipc:method pipelineNotifyStageTransition params:PipelineNotifyStageTransitionParams result:void skip
	s.methods["pipeline.notifyStageTransition"] = func(_ context.Context, params json.RawMessage) (interface{}, error) {
		var p PipelineNotifyStageTransitionParams
		if err := json.Unmarshal(params, &p); err != nil {
			return nil, fmt.Errorf("invalid params: %w", err)
		}
		// RUN-PROGRESS CLASS (ADR-017 Decision 3): a caller describing its OWN
		// run. The identity is required, validated before any use, and resolved
		// against the IPC registry, then the scheduler's, then adoption — which
		// REHYDRATES from runtime-{issue}-{runId}.json when one exists, turning
		// the ordinary case (an IPC server restarted mid-run) from lossy into
		// very nearly lossless.
		//
		// The interim server-side mint that stood here is DELETED. Minting on
		// miss is what let a successor's messages land on a zombie's runtime: the
		// identity now comes from the caller, so an adopting zombie re-creates
		// ITS OWN run under ITS OWN key and every write it makes lands on its own
		// record.
		res, err := s.resolveRun("pipeline.notifyStageTransition", verbRunProgress, p.RunID, p.Repo, p.IssueNumber)
		if err != nil {
			return nil, err
		}
		rt := res.rs
		// Unlocked by exemption, not by omission: RunID is a CONSTRUCTOR
		// ARGUMENT with no setter (ADR-017 Decision 1), so no goroutine can
		// write it while this one reads it. Every MUTABLE field of a resolved
		// runtime goes through rs.mu — see the repo read below.
		runID := rt.RunID

		var repo string
		if res.schedulerOwned {
			// A read-through call records this run's PROGRESS onto the
			// scheduler's runtime; it does not re-describe a run the scheduler
			// owns. Repo is a constructor fact on THAT path — the scheduler
			// builds its runtime with the repo already resolved and nothing
			// re-seeds it, because SeedRunContext is only reached through the
			// non-scheduler arm below — so this read needs no lock.
			repo = rt.Repo
		} else {
			// Title/branch/repo are run CONTENT, seeded under the run's OWN
			// mutex — the handler used to write them while holding the
			// REGISTRY's mutex, which is a torn read against any concurrent
			// snapshot or persist (Decision 12).
			repo = rt.SeedRunContext(p.Repo, p.Title, p.Branch)
			// The entry's index key follows the runtime's repo, so the derived
			// issue index (Decision 6) can rank without ever taking rs.mu.
			if res.entry != nil && repo != "" {
				s.runtimesMu.Lock()
				if res.entry.repo == "" {
					res.entry.repo = repo
				}
				s.runtimesMu.Unlock()
			}
		}

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

		// The rest of the #580 model envelope (#888). Everything above this
		// line was recorded on this path; everything below it was recorded
		// ONLY by the Go scheduler, so a complete, successful extension run —
		// six stages, 64M tokens, $17.63 — wrote null into every one of these
		// fields and the routing corpus learned nothing from it.
		//
		// Derived through orchestrator.StageEnvelopeAttribution, the same
		// resolvers the scheduler uses, so the two paths cannot drift apart
		// again by copying. Both recorders ignore empty strings and are
		// latest-wins per stage, so running this on every transition lets the
		// authoritative "complete" values supersede a "model-resolved"
		// estimate, and a bookend transition with no model is a no-op.
		if p.Model != "" {
			thinking, selectionMode := orchestrator.StageEnvelopeAttribution(
				p.Adapter, p.Model, s.workspaceRootPath())
			rt.RecordStageThinking(stage, thinking)
			rt.RecordStageModelSelectionMode(stage, selectionMode)
		}

		// The served envelope, verbatim from the executor's own report and
		// deliberately NOT falling back to the requested values — mirroring
		// scheduler.go, where result.ServedModel is recorded raw alongside the
		// separate request-or-served `servedModel` local. Empty stays empty:
		// "the executor did not say" and "the executor said X" must remain
		// distinguishable, which is the entire point of these fields.
		rt.RecordStageServedModel(stage, p.ServedModel)
		rt.RecordStageServedEffort(stage, p.ServedEffort)
		rt.RecordStageServedThinking(stage, p.ServedThinking)
		// When the executor served something OTHER than what was requested,
		// the requested-value fields are re-recorded onto the served value —
		// again exactly as the scheduler does, so requested-vs-served stay
		// epistemically distinct while the headline field tells the truth
		// about what ran.
		if p.ServedEffort != "" {
			rt.RecordStageEffort(stage, p.ServedEffort)
		}
		if p.ServedThinking != "" {
			rt.RecordStageThinking(stage, p.ServedThinking)
		}

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
			cacheCreation5m, cacheCreation1h := tokens.NormalizeCacheCreation(
				p.CacheCreationTokens,
				p.CacheCreation5mTokens,
				p.CacheCreation1hTokens,
			)
			rt.RecordStageTokenCounts(stage, tokens.TokenCounts{
				CacheRead:       p.CacheReadTokens,
				CacheCreation5m: cacheCreation5m,
				CacheCreation1h: cacheCreation1h,
			})
			if p.CostUsd > 0 {
				rt.CompleteStageWithCost(0, p.InputTokens, p.OutputTokens, p.CacheReadTokens, p.CostUsd, p.CacheCreationTokens)
			} else {
				rt.CompleteStage(0, tokens.TokenCounts{
					Input: p.InputTokens, Output: p.OutputTokens, CacheRead: p.CacheReadTokens,
					CacheCreation5m: cacheCreation5m, CacheCreation1h: cacheCreation1h,
				}, p.Model, p.Adapter)
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
			// Book the failing stage's spend BEFORE recording the error (#293).
			// This branch previously dropped p.CostUsd/p.*Tokens entirely, so
			// TotalCostUSD — and every consumer downstream (stateChanged
			// snapshot → TS estimated_cost_usd → the terminal-failure card) —
			// omitted the failing stage: the card showed $1.52 (spend through
			// the last successful stage) for a $14.84 run, on the exact screen
			// where the operator decides Retry vs recover. Mirrors the Go
			// scheduler path, which books cost unconditionally after a stage
			// returns and records terminating-stage ground truth on failure
			// (#146).
			//
			// #407: an entry in StageErrors means "this stage's MOST RECENT
			// attempt failed". completeStageInternal is the clear site, but it
			// clears only on a SUCCEEDING booking — so the exit-1 booking below
			// cannot retire the error this branch is about to record, and no
			// ordering between the two calls is load-bearing.
			//
			// This branch still writes unconditionally, including for a failure
			// the scheduler is about to retry (retry/escalation is a first-class
			// `continue` in internal/orchestrator/scheduler.go). That is correct
			// under the contract: the retry's own "complete" transition retires
			// the entry. Before #407 nothing ever removed a key, so a stage that
			// failed and then SUCCEEDED sat in CompletedStages and StageErrors at
			// once — and because both TS appliers apply stageErrors after
			// completedStages, it rendered "failed" for the rest of the run and
			// dragged a green run's outcome down to "Complete — 1 stage failed".
			// The applier ordering is deliberately RETAINED: with the contract in
			// place, a stage in both maps is the legitimate backtrack case
			// (completed earlier, re-run later, failed) and the latest attempt
			// must win.
			cacheCreation5m, cacheCreation1h := tokens.NormalizeCacheCreation(
				p.CacheCreationTokens,
				p.CacheCreation5mTokens,
				p.CacheCreation1hTokens,
			)
			rt.RecordStageTokenCounts(stage, tokens.TokenCounts{
				CacheRead:       p.CacheReadTokens,
				CacheCreation5m: cacheCreation5m,
				CacheCreation1h: cacheCreation1h,
			})
			if p.CostUsd > 0 {
				rt.CompleteStageWithCost(1, p.InputTokens, p.OutputTokens, p.CacheReadTokens, p.CostUsd, p.CacheCreationTokens)
			} else if p.InputTokens > 0 || p.OutputTokens > 0 || p.CacheReadTokens > 0 ||
				p.CacheCreationTokens > 0 || p.CacheCreation5mTokens > 0 || p.CacheCreation1hTokens > 0 {
				rt.CompleteStage(1, tokens.TokenCounts{
					Input: p.InputTokens, Output: p.OutputTokens, CacheRead: p.CacheReadTokens,
					CacheCreation5m: cacheCreation5m, CacheCreation1h: cacheCreation1h,
				}, p.Model, p.Adapter)
			}
			rt.RecordTerminatingStageTokens(stage, p.InputTokens, p.OutputTokens, p.CacheReadTokens, p.CostUsd)
			rt.SetStageError(stage, p.Error)
			// NOTE: Do NOT delete the runtime here (#232). notifyComplete is the
			// interactive terminal funnel and fires right after this with
			// Success=false; it needs the runtime still present to build the
			// authoritative FAILED RunRecord. Deleting here stranded failed runs
			// with no history entry. Cleanup still happens in notifyComplete, and
			// the next "initialized" for the same issue replaces the runtime —
			// mirroring the deferred cleanup the "complete" case already relies on.
			// The on-disk runtime-{issue}-{runId}.json IS still removed below (terminal snapshot).
		case "skipped":
			rt.SkipStage(stage)
		case "deferred":
			rt.SkipStage(stage) // treat deferred as skipped in Go state
		}

		// Ladder arm 3's input (ADR-017 7.2): the extension path's stage child.
		// Recorded on EVERY transition, including the zero — `running` is the
		// only status that names a live child, and the stage-terminal transitions
		// send 0 explicitly so a finished child cannot vouch for the run and the
		// PID-reuse window is bounded by one stage. `model-resolved`, `skipped`
		// and `deferred` omit the field, which arrives as the same zero and means
		// the same thing: no child is executing this run right now.
		//
		// NOT on the scheduler-owned arm: that runtime's LIVE pid comes from
		// SetProcess (internal/execution/manager.go), written from the scheduler's
		// own process tree. Writing the extension's pid over it would destroy the
		// scheduler population's arm-3 evidence with a pid from another tree.
		// (The scheduler does call SetStageChild itself — but only with 0, at
		// stage start, so its stage-start snapshot does not republish the previous
		// stage's dead child (#534). That is a clear, not a competing writer.)
		//
		// The value reaches disk through the Persist below — no new persist site.
		if !res.schedulerOwned {
			rt.SetStageChild(p.StagePid)
		}

		// Persist the runtime snapshot (carrying RunID) so a crash between here
		// and pipeline.notifyComplete leaves the run's platform UUID on disk for
		// orphan reconciliation at next activation (#44).
		// Best-effort: persistence failures must never block the pipeline — and
		// after the terminal claim they are EXPECTED: Persist returns ErrRunSealed
		// without writing, which is how an in-flight transition cannot resurrect a
		// snapshot the claim already sealed and removed (F27).
		//
		// THE `failed` SNAPSHOT REMOVAL THAT STOOD HERE IS DELETED (Decision 5).
		// It was a second, redundant terminal path — notifyComplete fires
		// immediately after with Success=false — and it is what let a zombie
		// destroy a live run's crash snapshot (F3). It was also wrong on its own
		// terms: if the host dies between the `failed` transition and
		// notifyComplete, the run never reached a terminal event and DESERVES
		// reconciliation, which the removal prevented. A canonical snapshot now
		// leaves the directory through exactly three doors, and this was not one.
		//
		// #307: gate on a known repo. The first "initialized" transition of a
		// concurrent HeadlessOrchestrator slot arrives before setRunRepo seeds
		// the slug (the TS orchestrator resolves it asynchronously), so repo is
		// "" and pipelineStateDir("") resolves the shared server root
		// (s.workspaceRoot). Persisting there stranded an empty repo/stage stub
		// in the launch repo — a repo that never ran the issue — which the
		// startup restore then tried to resurrect. Wait for a repo-carrying
		// transition; the run's own repo dir is the only correct home.
		//
		// A SCHEDULER-OWNED run is served read-through and never persisted from
		// here: the scheduler owns that snapshot's whole lifecycle, and a second
		// writer aiming at a directory derived from the CALLER's repo param is
		// exactly the split-brain Decision 11 separates the registries to avoid.
		if repo != "" && !res.schedulerOwned {
			if stateDir := s.pipelineStateDir(repo); stateDir != "" {
				if err := rt.Persist(stateDir); err != nil {
					log.Printf("notifyStageTransition: persist runtime snapshot failed (non-fatal): %v", err)
				}
			}
		}

		// Emit stateChanged event. The envelope carries the RESOLVED run identity
		// (Decision 6) so PipelineStateService and PipelineSlotsTracker can route
		// by run rather than by issue number — the filter that closes F19.
		snap := rt.Snapshot()
		s.Emit("pipeline.stateChanged", map[string]interface{}{
			"repo":        p.Repo,
			"issueNumber": p.IssueNumber,
			"runId":       snap.RunID,
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

		// RUN-PROGRESS CLASS (ADR-017 Decision 3), resolved by identity like
		// every other run message. Progress is a live estimate only: it never
		// mutates CompletedStages — the terminal "complete" transition owns the
		// authoritative per-stage totals.
		//
		// It DOES adopt. This is the caller Decision 4 names as the ordinary
		// concurrent adopter (>= 1 call per 5s, arriving alongside the next
		// transition for the same unknown id after a server restart), and the
		// per-id singleflight is what keeps the two from building two runtimes.
		res, err := s.resolveRun("pipeline.notifyStageProgress", verbRunProgress, p.RunID, p.Repo, p.IssueNumber)
		if err != nil {
			return nil, err
		}
		// RunID is a CONSTRUCTOR FACT and immutable — no setter exists — so it
		// is the one field a resolved runtime may be read off without rs.mu.
		// Repo is not: SeedRunContext writes it under rs.mu from the transition
		// handler, which runs in a DIFFERENT GOROUTINE from this one, so the
		// read goes through the locked accessor (ADR-017 Decision 12).
		runID := res.rs.RunID
		repo := res.rs.TargetRepo()
		if repo == "" {
			repo = p.Repo
		}

		// Emit the live in-stage token/cost estimate as a stage_progress event.
		// Skipped internally (no runID / bookend stage) rather than erroring.
		s.emitStageProgressTelemetry(runID, repo, p.IssueNumber, p.Stage, p.InputTokens, p.OutputTokens, p.CacheReadTokens, p.CostUsd)

		return map[string]string{"status": "ok"}, nil
	}

	// pipeline.recordStageGateResult is the single-authoritative-writer route
	// for `nightgauge gate verify --record` (#377, ADR-017 R-1).
	//
	// THE POINT IS TO REMOVE A WRITER. The gate CLI is a DIFFERENT OS PROCESS,
	// so the server's in-memory terminal latch cannot cover its writes. Decision
	// 5 narrowed that cross-process exposure to a rename race — load-or-skip,
	// terminal-refusal, PersistExisting — and named the residual R-1: the file
	// can be sealed and removed between the CLI's load and its write. Routing
	// the result through here makes the IPC server the SINGLE writer of the
	// runtime snapshot whenever it is alive, which is what actually closes the
	// race rather than narrowing it further. The CLI keeps its direct path for
	// when no server is reachable; that path has one writer by definition.
	//
	// Generation is SKIPPED for the TypeScript client on purpose: this verb's
	// only caller is the Go CLI. The extension records gate results through the
	// run's own transitions.
	//
	//ipc:method pipelineRecordStageGateResult params:PipelineRecordStageGateResultParams result:void skip
	s.methods["pipeline.recordStageGateResult"] = func(_ context.Context, params json.RawMessage) (interface{}, error) {
		var p PipelineRecordStageGateResultParams
		if err := json.Unmarshal(params, &p); err != nil {
			return nil, fmt.Errorf("invalid params: %w", err)
		}
		if p.Stage == "" {
			return nil, fmt.Errorf("invalid params: stage is required")
		}

		// RUN-PROGRESS CLASS (Decision 3): the gate CLI is spawned BY the run
		// whose verdict this is, so it describes its OWN run. Adoption is safe
		// for that reason — an adopting caller re-creates its own run under its
		// own key, where every write it makes lands on its own record. The run
		// id is carried explicitly (NIGHTGAUGE_RUN_ID or --run-id), so this
		// resolves EXACTLY rather than by the direct path's newest-non-terminal
		// pick, which mis-attributes under two concurrent dispatches of one
		// issue.
		//
		// A terminal or closed run is refused here — codeRunClosed — which is
		// the same answer the direct path's terminal-refusal gives, except that
		// this one consults the live registry rather than a file that may be
		// mid-seal.
		res, err := s.resolveRun("pipeline.recordStageGateResult", verbRunProgress, p.RunID, p.Repo, p.IssueNumber)
		if err != nil {
			return nil, err
		}
		rt := res.rs
		rt.AppendStageGateResult(state.PipelineStage(p.Stage), p.Result)

		// Persist so the result is durable even if the run never reaches another
		// stage boundary. Persist is latch-aware: after the terminal claim's
		// seal it returns ErrRunSealed WITHOUT writing, so this cannot resurrect
		// a snapshot the claim removed (F27) — the exact failure the CLI's
		// direct write can still produce in its rename-race window, and the
		// reason this route exists.
		//
		// A SCHEDULER-OWNED run is served read-through and NOT persisted from
		// here: the scheduler owns that snapshot's whole lifecycle and persists
		// it at every stage boundary, so the append lands on the live runtime
		// and reaches disk through its owner. A second writer aiming at a
		// directory derived from the CALLER's repo param is exactly the
		// split-brain Decision 11 separates the registries to avoid.
		repo := rt.TargetRepo()
		if repo == "" {
			repo = p.Repo
		}
		if res.schedulerOwned {
			return map[string]string{"status": "ok"}, nil
		}
		if repo == "" {
			// Gated on a known repo for the same reason the transition's persist
			// is (#307): pipelineStateDir("") resolves the shared launch root,
			// and persisting there strands a stub in a repo that never ran the
			// issue.
			log.Printf("recordStageGateResult: #%d run %s carries no repo — the result is on the live runtime but was not persisted (#307)",
				p.IssueNumber, rt.RunID)
			return map[string]string{"status": "ok"}, nil
		}
		if stateDir := s.pipelineStateDir(repo); stateDir != "" {
			if err := rt.Persist(stateDir); err != nil {
				// Non-fatal, and EXPECTED after a terminal claim: the gate's
				// verdict is already returned to its caller by the CLI, and a
				// sealed run refusing a late write is the latch working.
				log.Printf("recordStageGateResult: #%d persist failed (non-fatal): %v", p.IssueNumber, err)
			}
		}

		return map[string]string{"status": "ok"}, nil
	}

	//ipc:method pipelineNotifyComplete params:PipelineNotifyCompleteParams result:void skip
	s.methods["pipeline.notifyComplete"] = func(_ context.Context, params json.RawMessage) (interface{}, error) {
		var p PipelineNotifyCompleteParams
		if err := json.Unmarshal(params, &p); err != nil {
			return nil, fmt.Errorf("invalid params: %w", err)
		}

		// TERMINAL CLASS — THE CLAIM (ADR-017 Decision 5). The sequence below is
		// normative and complete: a mutation that is not in it is refused, and
		// anything added later must be added here or it does not happen.
		//
		// STEP 0 — resolve, holding NEITHER lock. This is where Decision 3's class
		// policy applies: a scheduler-owned id stops here with run_wrong_owner
		// (the scheduler books that run's record itself through
		// OnPipelineComplete, and serving a terminal verb from a registry with no
		// latch, no lease and no compare-and-delete target would write a SECOND
		// authoritative record under one run id — F29), a terminal snapshot yields
		// run_closed, and the singleflight may perform the disk read. The
		// singleflight takes runtimesMu up to three times itself, so containing it
		// in the claim's critical section would deadlock (F36).
		res, err := s.resolveRun("pipeline.notifyComplete", verbTerminal, p.RunID, p.Repo, p.IssueNumber)
		if err != nil {
			return nil, err
		}
		if res.entry == nil {
			// Defence in depth: resolveRun refuses a scheduler-owned run for this
			// class, so an entry-less terminal resolution is unreachable. If it
			// ever happens, refuse rather than claim something with no latch.
			return nil, s.rejectRun("pipeline.notifyComplete", codeRunWrongOwner, p.RunID, p.IssueNumber,
				"terminal resolution produced no claimable registry entry")
		}
		// Unlocked by exemption: RunID is immutable after construction. The
		// claim below takes rs.mu for every field that is not.
		runID := res.entry.rs.RunID

		// #266 ground-truth reconciliation, evaluated INSIDE the claim against the
		// run's own terminating stage. A run whose PR merged must never be booked
		// as failed by a late per-stage kill (progress-runaway / stall / budget)
		// that fired at pr-merge AFTER the merge already landed — that
		// misattribution recorded a merged run as failed/stall_kill. The flip
		// happens BEFORE both telemetry and the RunRecord so every surface
		// reflects the merge, not a phantom kill.
		groundTruthFlipped := false
		outcomeFor := func(stage state.PipelineStage) string {
			if effective := reconcilePrMergeGroundTruth(p.Success, p.PrMerged, string(stage)); effective != p.Success {
				p.Success = effective
				groundTruthFlipped = true
			}
			switch {
			case p.Success:
				return "complete"
			case p.Deferred:
				return "cancelled"
			default:
				return "failed"
			}
		}

		// STEP 1 — the claim: 1a re-check (one retry), 1b the #309 replay, 1c both
		// halves of the latch, 1d the snapshot. Everything after this runs against
		// `snap`, NEVER against the live pointer.
		//
		// 1b is the dispatcher's terminal payload: the TypeScript orchestrator's
		// per-stage execution-path decisions, which the Go runtime never saw
		// because the deterministic-first pr-create/pr-merge (and issue-pickup)
		// hooks run in the extension process. They are the LAST mutations the run
		// will ever accept and they run inside the claim — before the latch would
		// refuse them, and after the point where an unlocked mutation window could
		// exist.
		claimed, snap, err := s.runTerminalClaim("pipeline.notifyComplete", res, runID, p.Repo, p.IssueNumber,
			p.StageExecutionPaths, p.StagePuntReasons, outcomeFor)
		if err != nil {
			return nil, err
		}
		rt := claimed.rs
		if groundTruthFlipped {
			log.Printf(
				"notifyComplete: #%d reported failed at pr-merge but the PR is MERGED — recording complete (ground truth, #266)",
				p.IssueNumber,
			)
		}

		// STEP 2 — the work, UNLOCKED, against the snapshot.
		s.emitPipelineDoneTelemetry(runID, p)

		// Write the authoritative interactive RunRecord (#232). notifyComplete
		// is the interactive-only terminal funnel — the Go scheduler path emits
		// its own RunRecord via OnPipelineComplete, so this cannot collide with
		// it — and it is the sole writer for the extension/HeadlessOrchestrator
		// path, for BOTH success and failure. It lands in the run's TARGET repo
		// (#215) so history isn't split across repos, and must run BEFORE the
		// runtime delete below so the snapshot is still available. Best-effort:
		// a write failure is logged but never blocks the pipeline.
		//
		// THE ROOT COMES FROM THE CLAIMED SNAPSHOT'S OWN `Repo`, for the same
		// reason the seal's directory does (Decision 4 fix #3): a
		// notifyComplete whose repo param disagrees with the run's persisted
		// repo would otherwise file repo A's record under repo B while sealing
		// repo A's snapshot — the run's state split across two repos, which is
		// exactly what #215/#232 settled. p.Repo remains the fallback for a run
		// whose transitions never carried one (it has no snapshot either).
		recordRepo := snap.Repo
		if recordRepo == "" {
			recordRepo = p.Repo
		}
		if root := s.repoRoot(recordRepo); root != "" {
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
				// Prefer the failing stage's gate-sourced structured kind
				// over prose classification of errMsg (Issue #9).
				gateRan := false
				gateTerminalKind := ""
				if gateResults := snap.StageGateResults[string(snap.Stage)]; len(gateResults) > 0 {
					gateRan = true
					gateTerminalKind = gateResults[len(gateResults)-1].TerminalKind
				}
				kind := orchestrator.ResolveTerminalKind(gateRan, gateTerminalKind, errMsg)
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
			// Hydrate Labels/Size/Type from the run's issue-{N}.json (#112).
			// These were left absent on the assumption they were cosmetic
			// display fields. Size is not: it is the join key the VSCode
			// pre-flight estimator matches run history on, so every record
			// written without it was unusable as calibration input and the
			// projection collapsed to the raw static estimate — which ran a
			// median 3.9x under actual across 112 runs before anyone noticed.
			cls := loadIssueClassification(root, snap.WorktreeDir, p.IssueNumber)
			input.Labels = cls.Labels
			input.IssueType = cls.Type
			input.Size = cls.Size
			// The routing PREDICTION the run was picked up under. It sits in
			// the same issue-{N}.json read above and was being dropped: every
			// record this handler wrote carried routing.complexity_score 0,
			// while the scheduler path recorded the real score into the same
			// corpus field, leaving one field with two meanings and no
			// discriminator (#304).
			input.ComplexityScore = cls.ComplexityScore
			if snap.Branch == "" {
				// Mirror the scheduler path: the persisted empty value is the
				// honest "undetermined" state, but the resolution gap must also
				// be visible at the only boundary that knows which run wrote it.
				log.Printf(
					"notifyComplete: #%d: no feature branch could be determined from any source — the history record "+
						"will carry an EMPTY branch, which is how a record says \"undetermined\"; nothing is "+
						"fabricated in its place (#299, #397)",
					p.IssueNumber,
				)
			}
			if cls.ComplexityScore <= 0 {
				log.Printf(
					"notifyComplete: #%d has no routing.complexity_score — no issue context reached this handler, so the run records no routing prediction at all (#304)",
					p.IssueNumber,
				)
			}
			if cls.Size == "" {
				// Loud by design: a silently size-less record is exactly how
				// the calibration path stayed switched off unnoticed (#112).
				log.Printf(
					"notifyComplete: #%d has no size:* label — its run record cannot calibrate the pre-flight cost estimate (#112)",
					p.IssueNumber,
				)
			}

			hw := state.NewHistoryWriter(root)
			// pipeline.logs.history_retention_days drives the prune pass
			// appendAndIndex runs on every write below — the only retention
			// enforcement a headless/CLI-only workspace gets (#674).
			if cfg, cfgErr := config.Load(root); cfgErr == nil && cfg != nil {
				hw.SetRetentionDays(cfg.Pipeline.ResolveHistoryRetentionDays())
			}
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
			// #304: derive the learning/calibration outcome from the SAME
			// record about to be written. Until this, the outcome corpus
			// (.nightgauge/pipeline/history/outcomes.jsonl — the input to the
			// calibration, cost-optimization and reliability loop verdicts and
			// to `nightgauge learn tune`) had exactly ONE writer,
			// scheduler.recordOutcome, reachable only from
			// Scheduler.runPipeline. Extension runs go
			// ConcurrentPipelineManager → HeadlessOrchestrator → this handler
			// and never enter that loop, so in the mode the product is
			// actually operated in NOTHING recorded an outcome and the
			// self-improvement loops steered on autonomous-only evidence.
			// Derived here rather than rebuilt: an independently-built mirror
			// record is exactly what drifted in #261.
			outcome, outcomeVerdict := learningOutcomeFor(record, cls, snap, p.Repo, now)
			if outcomeVerdict == outcomeRecord {
				// Parity with the Go path, where recordOutcome's return value
				// is threaded into recordV2History: the predicted-vs-actual
				// routing fields belong on the run record too. Must be set
				// BEFORE the write and the platform push below, both of which
				// consume `record`.
				record.OutcomePrediction = outcomePredictionFrom(outcome)
			}

			if err := hw.WriteV2Record(record, now); err != nil {
				log.Printf("notifyComplete: write RunRecord failed (non-fatal): %v", err)
			}

			// Append the learning outcome. Best-effort — a corpus write
			// failure is logged and never blocks the pipeline, same
			// discipline as the RunRecord write above.
			//
			// The recorder is rooted at `root` — s.repoRoot(p.Repo), the run's
			// TARGET repo — the SAME root the run record above was written to.
			// It is emphatically NOT s.workspaceRoot: that field is a mutable
			// pointer to the workspace's ACTIVE repo (workspace.setRoot, sent
			// by the extension from resolveActiveRepository — in a multi-repo
			// workspace, whichever repo owns the focused editor), so rooting
			// the corpus there would file repo B's outcome under repo A the
			// moment the operator clicked into a different file. #215/#232
			// already settled where a run's persisted state belongs: with its
			// target repo, or the run's state is split across two repos. The
			// outcome is derived from that record and follows it.
			//
			// `nightgauge intelligence loop-verdicts --workdir X` and
			// `nightgauge learn tune --workdir X` read one root — and they read
			// X's run history from that same root, so a per-repo corpus is
			// exactly what makes their two inputs describe the same runs.
			//
			// Idempotency is inherited, not enforced here: learning.Recorder
			// .Record is a blind append with no dedup. What guarantees at most
			// one corpus row per run is the TERMINAL LATCH plus closedRuns
			// (ADR-017 Decision 5): this handler is only reachable through a
			// claim that won `entry.terminal`, a second notifyComplete for the
			// same identity is refused run_closed before it can resolve, and the
			// scheduler path never calls notifyComplete at all. The `if ok`
			// guard this comment used to name is gone — the missing-runtime
			// accident it relied on was never the real mechanism, and after the
			// re-key a second dispatch of the SAME ISSUE is a different run id
			// with its own record, which is correct rather than a double.
			switch outcomeVerdict {
			case outcomeRecord:
				// Loud by design on EVERY unattributed field. An empty value
				// really is recoverable — learning.Recorder.Calibrate and the
				// calibration loop verdict both count a row toward an
				// accuracy only when BOTH halves of that pair are non-empty,
				// so an absent value is excluded rather than booked as a miss
				// — but only if somebody knows it happened: the pre-#304
				// corpus was 100% model-less and nobody noticed for the life
				// of the product.
				//
				// The sentences come from orchestrator.Outcome*Diagnostic,
				// beside the rule that produces the empty band, because the
				// band has THREE causes and this writer and the scheduler's
				// must not name different ones for one corpus field: absent,
				// or a value the registry has no band for. On a gemini /
				// lm-studio / ollama workspace the implementation stage DOES
				// report a model — so the old single sentence told exactly
				// those operators that the stage never ran (#340).
				if outcome.PredictedModel == "" {
					log.Printf("notifyComplete: #%d %s", p.IssueNumber,
						orchestrator.OutcomePredictedModelDiagnostic(p.IssueNumber, cls.PredictedModel))
				}
				if outcome.ActualModel == "" {
					log.Printf("notifyComplete: #%d %s", p.IssueNumber,
						orchestrator.OutcomeActualModelDiagnostic(rawServedDevModel(record, snap)))
				}
				if err := learning.NewRecorder(root).Record(outcome); err != nil {
					log.Printf("notifyComplete: record learning outcome failed (non-fatal): %v", err)
				}
			case outcomeSkipDeferred, outcomeSkipNetworkUnavailable:
				log.Printf("notifyComplete: #%d skipping learning outcome (%s)",
					p.IssueNumber, outcomeVerdict)
			case outcomeUnset:
				log.Printf("notifyComplete: #%d learning outcome decision was never made — this is a bug",
					p.IssueNumber)
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
			if s.getAnalyticsSvc() != nil {
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
					s.getAnalyticsSvc().PushPipelineRun(context.Background(), runRecord)
				}
			}
		} else {
			// Loud by design: with no resolvable root the run produces
			// NEITHER a history record NOR a learning outcome. Silence here
			// is the shape of #304 — a terminal run that persists nothing
			// and reports success.
			log.Printf(
				"notifyComplete: #%d repo %q resolves to no on-disk root — run record and learning outcome NOT written (#304)",
				p.IssueNumber, recordRepo,
			)
		}

		// STEP 3 — COMPARE-AND-DELETE, under runtimesMu: drop the registry entry
		// only if the entry stored there is the SAME POINTER that was claimed, so
		// a successor installed during the unlocked step-2 window survives (F5).
		// The id is recorded in closedRuns in the same hold, which is what makes a
		// late duplicate notifyComplete a cheap in-memory refusal.
		s.compareAndDeleteRun(claimed, runID)

		// STEP 4 — SEAL AND REMOVE, under rs.mu, as ONE operation holding NO
		// registry lock: write the terminal-stamped snapshot, remove that same
		// path, then latch `sealed` so every later Persist returns ErrRunSealed
		// without writing (F27's in-flight-Persist resurrection).
		//
		// THE DIRECTORY COMES FROM THE CLAIMED SNAPSHOT'S OWN `Repo`, never from
		// p.Repo (Decision 4 fix #3): a notifyComplete whose repo param disagrees
		// with the run's persisted repo could otherwise leave the real file behind
		// while deleting nothing. And THE PATH IS THE IDENTITY — composed from
		// this run's own runId — so the remove cannot take a successor's file even
		// in principle, which the bare-issue delete this replaces could and did
		// (F3).
		//
		// Write-then-remove is idempotent if the reconciler removed the file
		// first: the write re-creates it as terminal and the remove takes it away
		// again, net nothing.
		//
		// Gated on a KNOWN repo, for the same reason the transition's persist is
		// (#307): a run whose transitions never carried a repo has no snapshot
		// anywhere, and pipelineStateDir("") resolves the shared launch root —
		// sealing there would write and remove a file in a repo that never ran
		// the issue.
		if snap.Repo == "" {
			log.Printf("notifyComplete: #%d run %s never carried a repo — it has no snapshot to seal (#307)",
				p.IssueNumber, runID)
		} else if sealDir := s.pipelineStateDir(snap.Repo); sealDir != "" {
			if err := rt.SealAndRemove(sealDir); err != nil {
				// THREE BRANCHES, THREE DIFFERENT THINGS LEFT ON DISK, and the
				// log must not blur them: the earlier "terminal-marked either
				// way" line was true only of the remove failure.
				if errors.Is(err, state.ErrNotRunOwner) {
					// NOT A FAILURE — a refusal, and the correct one (#557).
					// This run was rehydrated from a snapshot another LIVE
					// process is driving (the `nightgauge run` scheduler,
					// observed by this `serve` daemon). Sealing here would
					// remove a file whose owner holds no seal of its own and
					// re-creates it at its next stage-boundary persist,
					// resurrecting the run. Everything else the claim did
					// stands; only the destructive half is declined, and the
					// owner's own bookkeeping disposes of the snapshot.
					log.Printf("notifyComplete: #%d DECLINED to seal run %s — its snapshot belongs to a live owner process, not to this one (#557): %v",
						p.IssueNumber, runID, err)
				} else if errors.Is(err, state.ErrSealWriteFailed) {
					// The terminal marker never reached disk. The seal is
					// latched and the stale NON-TERMINAL snapshot was removed
					// rather than left for a restart to rehydrate, so the worst
					// remaining case is R-4's adopt-empty noise — not a
					// resurrected run.
					log.Printf("notifyComplete: #%d seal for run %s could NOT WRITE the terminal marker; the stale snapshot was removed instead (non-fatal): %v",
						p.IssueNumber, runID, err)
				} else {
					// The file on disk DOES carry `terminal: true`; only its
					// removal failed. Adoption refuses that snapshot and the
					// reconciler removes it without emitting.
					log.Printf("notifyComplete: #%d seal-and-remove for run %s failed AFTER the snapshot was terminal-marked (non-fatal): %v",
						p.IssueNumber, runID, err)
				}
			}
		} else {
			log.Printf("notifyComplete: #%d run %s repo %q resolves to no on-disk root — nothing to seal",
				p.IssueNumber, runID, snap.Repo)
		}

		return map[string]string{"status": "ok"}, nil
	}

	//ipc:method pipelineNotifyPhaseTransition params:PipelineNotifyPhaseTransitionParams result:void skip
	s.methods["pipeline.notifyPhaseTransition"] = func(_ context.Context, params json.RawMessage) (interface{}, error) {
		var p PipelineNotifyPhaseTransitionParams
		if err := json.Unmarshal(params, &p); err != nil {
			return nil, fmt.Errorf("invalid params: %w", err)
		}

		// RUN-PROGRESS CLASS (ADR-017 Decision 3), resolved by identity. Two
		// runtime registries exist — this server's activeRuntimes
		// (extension/HeadlessOrchestrator path) and the scheduler's per-issue
		// registry — and Decision 11 decides which one serves the call: a live
		// scheduler run is served READ-THROUGH onto the scheduler's own runtime
		// and never adopted here, because a second in-memory entry for a run the
		// scheduler already owns would hold a lease, never be terminal-claimed,
		// and become "current" for its repo#issue in the derived index.
		//
		// Phase markers must land in whichever runtime actually drives the run so
		// the snapshot embedded in pipeline.stateChanged carries PhaseHistory —
		// otherwise the tree view loses phase counts on already-completed stages
		// whenever the extension reloads mid-pipeline.
		res, err := s.resolveRun("pipeline.notifyPhaseTransition", verbRunProgress, p.RunID, p.Repo, p.IssueNumber)
		if err != nil {
			return nil, err
		}
		rt := res.rs
		runID := res.runID()

		stage := state.PipelineStage(p.Stage)
		switch p.EventType {
		case "start":
			if res.schedulerOwned {
				// The scheduler's arm carries the IDENTITY GATE (Decision 11): it
				// resolves by issue and no-ops unless the registered runtime's
				// RunID equals this one, so a foreign run can never write into a
				// scheduler run's PhaseHistory.
				s.schedulerRuns.RecordPhaseStartForRun(runID, p.IssueNumber, p.Stage, p.Name, p.Index, p.Total)
			} else {
				rt.BeginPhase(stage, p.Name, p.Index, p.Total)
			}
			s.Emit("phase.start", map[string]interface{}{
				"repo":        p.Repo,
				"issueNumber": p.IssueNumber,
				"runId":       runID,
				"stage":       p.Stage,
				"name":        p.Name,
				"index":       p.Index,
				"total":       p.Total,
			})
		case "complete":
			if res.schedulerOwned {
				s.schedulerRuns.RecordPhaseCompleteForRun(runID, p.IssueNumber, p.Stage, p.Name)
			} else {
				rt.CompletePhase(stage, p.Name)
			}
			s.Emit("phase.complete", map[string]interface{}{
				"repo":        p.Repo,
				"issueNumber": p.IssueNumber,
				"runId":       runID,
				"stage":       p.Stage,
				"name":        p.Name,
				"index":       p.Index,
				"total":       p.Total,
			})
		case "skip":
			// #1026: the extension's skipPhase updated its own state, fired a
			// view event, and sent nothing here — so the GUI knew about a
			// skipped phase and the durable record did not.
			rt.SkipPhase(stage, p.Name, p.Index, p.Total)
			s.Emit("phase.skip", map[string]interface{}{
				"repo":        p.Repo,
				"issueNumber": p.IssueNumber,
				"runId":       runID,
				"stage":       p.Stage,
				"name":        p.Name,
				"index":       p.Index,
				"total":       p.Total,
			})
		case "fail":
			rt.FailPhase(stage, p.Name, p.Index, p.Total)
			s.Emit("phase.fail", map[string]interface{}{
				"repo":        p.Repo,
				"issueNumber": p.IssueNumber,
				"runId":       runID,
				"stage":       p.Stage,
				"name":        p.Name,
				"index":       p.Index,
				"total":       p.Total,
			})
		default:
			// This switch had no default (#1026). An event type the server did
			// not recognise returned {"status":"ok"} having done nothing, so a
			// caller could not tell "recorded" from "silently discarded" — and
			// that is exactly how a whole vocabulary went missing without any
			// surface reporting a problem. Say so rather than answering ok.
			return nil, fmt.Errorf("pipeline.notifyPhaseTransition: unknown eventType %q (want start|complete|skip|fail)", p.EventType)
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
		if s.workspaceRootPath() == "" {
			return nil, fmt.Errorf("no workspace root configured")
		}
		// Read persisted wave status from disk
		statusPath := filepath.Join(s.workspaceRootPath(), ".nightgauge", "pipeline",
			fmt.Sprintf("wave-status-%d.json", p.EpicNumber))
		data, err := os.ReadFile(statusPath)
		if err != nil {
			if os.IsNotExist(err) {
				// Try wave plan (orchestration may still be running)
				planPath := filepath.Join(s.workspaceRootPath(), ".nightgauge", "pipeline",
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
		if s.workspaceRootPath() == "" {
			return nil, fmt.Errorf("no workspace root configured")
		}
		ctxPath := filepath.Join(s.workspaceRootPath(), ".nightgauge", "pipeline",
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
		if s.workspaceRootPath() == "" {
			return nil, fmt.Errorf("no workspace root configured")
		}

		dir := filepath.Join(s.workspaceRootPath(), ".nightgauge", "pipeline")
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

	//ipc:method queueComplete params:QueueCompleteParams result:void
	s.methods["queue.complete"] = func(_ context.Context, params json.RawMessage) (interface{}, error) {
		var p QueueCompleteParams
		if err := json.Unmarshal(params, &p); err != nil {
			return nil, fmt.Errorf("invalid params: %w", err)
		}
		if s.scheduler == nil {
			return nil, errors.New(errSchedulerNotConfigured)
		}
		s.scheduler.CompleteQueueItem(p.Repo, p.IssueNumber)
		return map[string]string{"status": "ok"}, nil
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
		projSvc := s.boardServicesFor(c, p.Owner, p.ProjectNumber, gh.ParseOwnerType(p.OwnerType)).Project
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
		projSvc := s.boardServicesFor(c, p.Owner, p.ProjectNumber, gh.ParseOwnerType(p.OwnerType)).Project
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
		projSvc := s.boardServicesFor(c, p.Owner, p.ProjectNumber, gh.ParseOwnerType(p.OwnerType)).Project
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
		projSvc := s.boardServicesFor(c, p.Owner, p.ProjectNumber, gh.ParseOwnerType(p.OwnerType)).Project
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

	//ipc:method gitComposeBranchName params:GitComposeBranchNameParams result:GitComposeBranchNameResult
	s.methods["git.composeBranchName"] = func(_ context.Context, params json.RawMessage) (interface{}, error) {
		var p GitComposeBranchNameParams
		if err := json.Unmarshal(params, &p); err != nil {
			return nil, fmt.Errorf("invalid params: %w", err)
		}
		if p.IssueNumber <= 0 {
			return nil, fmt.Errorf("issueNumber must be positive, got %d", p.IssueNumber)
		}
		return GitComposeBranchNameResult{
			Name: gitops.ComposeBranchName(p.Labels, p.IssueNumber, p.Title),
		}, nil
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
		// #1013: this used to reach the function through s.execMgr, a field
		// written by exactly one function — WithExecutionManager — which has
		// ZERO callers. The verb therefore answered "execution manager not
		// initialized" on every invocation the extension ever made, and the
		// extension's caller logged and continued, so cleanup was silently
		// skipped on every activation.
		//
		// It also ignored p.WorkDir entirely. Routing through gitService puts
		// this verb on the same rail as every other git.* method and makes the
		// parameter mean something. gitService, not destructiveGitService: the
		// extension calls with no workDir and must resolve the workspace root.
		svc, err := s.gitService(p.WorkDir)
		if err != nil {
			return nil, err
		}
		deleted, err := svc.CleanupMergedBranches()
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
		svc, err := s.destructiveGitService("git.abortPipeline", p.WorkDir)
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
		if err := json.Unmarshal(params, &p); err != nil {
			return nil, fmt.Errorf("invalid params: %w", err)
		}
		svc, err := s.destructiveGitService("git.resetPipeline", p.WorkDir)
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
		// The scheduler may be blocked by a paused or safety_tripped state;
		// resuming transitions it back to running, resets the safety rails so
		// a fresh goroutine doesn't immediately re-trip, and triggers an
		// immediate re-scan. Harmless when already running or stopped.
		//
		// #405 — with ONE exception: a machine-raised halt. The scheduler
		// paused the whole fleet on a terminal stage failure and raised a
		// blocking_fleet card asking a human to decide; Start is "bring the
		// backend back up", not "I triaged that". Leaving the halt in force
		// keeps the card standing, and the returned status (paused, with the
		// pause reason) is how the caller learns the fleet came up halted.
		// Resume — the card's own Retry/Park options, or the explicit Resume
		// action — is what clears it.
		if !s.autonomousScheduler.ResumeUnlessMachineHalted() {
			log.Printf("autonomous: started with a machine-raised halt still in force — no dispatch until an explicit resume")
		}
		if !s.autonomousScheduler.IsRunning() {
			// No goroutine running (fresh server start or prior completion).
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
		// #148: a haltQueueOnSlotFailure pause is a terminal failure that
		// needs a human decision, not just a status flip — raise the proper
		// Action Center card so the operator sees why the fleet stopped
		// instead of a misleading "Fleet idle" card one scan cycle later.
		if triggeredBy == "haltQueueOnSlotFailure" && p.Repo != "" && p.IssueNumber != 0 {
			s.autonomousScheduler.RaiseTerminalFailure(p.Repo, p.IssueNumber, p.Stage, p.TerminalKind, p.CostUsd)
		}
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
		// One resume primitive, shared with the Action Center card options
		// that resume (#405) — see resumeAndEnsureRunning.
		if err := s.resumeAndEnsureRunning(ctx); err != nil {
			return nil, err
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
		cleared, tripped := s.autonomousScheduler.ClearIssueFailures(p.Key)
		return AutonomousClearIssueFailuresResult{Cleared: cleared, CircuitBreakerTripped: tripped}, nil
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

	// The run-scoped producer entry point for the extension operating mode
	// (#305). Closed producer allowlist + typed scalars — the card is built
	// daemon-side from the same orchestrator builders the Go scheduler calls,
	// so a surface can report a CONDITION but never describe a card or the
	// verbs it offers. See internal/ipc/attention_raise.go.
	//ipc:method attentionRaise params:AttentionRaiseParams result:AttentionRaiseResult
	s.methods["attention.raise"] = s.handleAttentionRaise

	//ipc:method attentionMute params:AttentionMuteParams result:AttentionMuteResult
	s.methods["attention.mute"] = s.handleAttentionMute

	//ipc:method attentionUnmute params:AttentionUnmuteParams result:AttentionMuteResult
	s.methods["attention.unmute"] = s.handleAttentionUnmute

	// Repo-scoped evaluation with no run in flight (#89/#93). Deliberately
	// caller-triggered: the sweep is cheap and idempotent, so the extension
	// invokes it on activation, on a view refresh, on its configured interval,
	// and after a run terminates. No timer lives here.
	//ipc:method attentionSweep params:AttentionSweepParams result:AttentionSweepResult
	s.methods["attention.sweep"] = s.handleAttentionSweep

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
		cfg, err := config.Load(s.workspaceRootPath())
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
		m := focus.NewManager(s.workspaceRootPath())
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
		m := focus.NewManager(s.workspaceRootPath())
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
		m := focus.NewManager(s.workspaceRootPath())
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
		root := s.workspaceRootPath()
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
		root := s.workspaceRootPath()
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
		root := s.workspaceRootPath()
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
		root := s.workspaceRootPath()
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
		m := focus.NewManager(s.workspaceRootPath())
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

	// --- Workspace repository management (#705) ---

	//ipc:method workspaceRepoList params:WorkspaceRepoListParams result:WorkspaceRepoListResult
	s.methods["workspace.repoList"] = s.handleWorkspaceRepoList

	//ipc:method workspaceRepoAdd params:WorkspaceRepoAddParams result:WorkspaceRepoAddResult
	s.methods["workspace.repoAdd"] = s.handleWorkspaceRepoAdd

	//ipc:method workspaceRepoRemove params:WorkspaceRepoRemoveParams result:WorkspaceRepoRemoveResult
	s.methods["workspace.repoRemove"] = s.handleWorkspaceRepoRemove
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
	if cfg, err := config.Load(s.workspaceRootPath()); err == nil && cfg != nil && cfg.Autonomous != nil {
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
		dir = s.workspaceRootPath()
	}
	if dir == "" {
		return nil, fmt.Errorf("no workspace root configured for git operations")
	}
	return gitops.NewService(dir)
}

// destructiveGitService resolves the git service for a verb that destroys
// working-tree state, refusing an empty workDir instead of falling back to the
// workspace root (#298).
//
// For a read verb that fallback is a convenience. For `HardReset` + `clean -d`
// it is the #289 blast pattern — a reset escaping its worktree into the main
// checkout — reachable from one malformed frame: the handler dropped its
// unmarshal error, so a renamed or corrupt `workDir` field left the zero value
// and `gitService` helpfully substituted the workspace root. A caller that
// genuinely wants to reset the root has to name it.
func (s *Server) destructiveGitService(method, workDir string) (*gitops.Service, error) {
	if strings.TrimSpace(workDir) == "" {
		return nil, fmt.Errorf("%s requires an explicit workDir: refusing to default to the workspace root "+
			"for a destructive git operation", method)
	}
	return s.gitService(workDir)
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
	yamlPath := filepath.Join(s.workspaceRootPath(), ".nightgauge", "config.yaml")
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
