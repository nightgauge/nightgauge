package main

import (
	"context"
	"errors"
	"fmt"
	"log"
	"strings"
	"sync"
	"time"

	"github.com/nightgauge/nightgauge/internal/config"
	gh "github.com/nightgauge/nightgauge/internal/github"
	"github.com/nightgauge/nightgauge/internal/headlesstrigger"
	"github.com/nightgauge/nightgauge/internal/ipc"
	"github.com/nightgauge/nightgauge/internal/orchestrator"
	"github.com/nightgauge/nightgauge/internal/platform"
	"github.com/nightgauge/nightgauge/internal/runstate"
)

var _ headlessTriggerHost = (*ipc.Server)(nil)

// headlessTriggerHost is what a headless daemon needs from its IPC server to
// decide a trigger it executes itself (#3586): whether an extension is
// attached, whether the issue already has a live or queued run, and the
// remote-pin validation. *ipc.Server implements it.
type headlessTriggerHost interface {
	ExecutorAttached() bool
	TriggerIssueBusy(repo string, issueNumber int) bool
	ValidateTriggerPin(repo, adapter, model string) string
	AddIssueBusyCheck(check func(repo string, issueNumber int) bool)
	// TriggerIssueRunning is TriggerIssueBusy without the registered busy
	// checks, so a trigger that waited for a slot does not see itself (#3673).
	TriggerIssueRunning(repo string, issueNumber int) bool
}

// newHeadlessTriggerRunner builds the runner a headless daemon executes an
// accepted trigger on. A variable so a test can run without a real adapter.
var newHeadlessTriggerRunner = func(workspaceRoot string) headlesstrigger.Runner {
	return headlessTriggerRunner{workspaceRoot: workspaceRoot}
}

// headlessTriggerRelay wraps relay for a daemon that may have no extension
// attached (#3586). A `trigger` that arrives while no extension is attached
// is executed here by triggers and never emitted as EventAgentCommand. Every
// other command, and a trigger while an extension is attached, goes through
// relay unchanged.
func headlessTriggerRelay(host headlessTriggerHost, triggers platform.AgentCommandHandler, relay platform.AgentCommandRelay) platform.AgentCommandRelay {
	return func(agentID string, cmd platform.PendingCommand) {
		if cmd.Type == headlesstrigger.TriggerCommandType && !host.ExecutorAttached() {
			log.Printf("[nightgauge] trigger %s for agent %s: no extension attached; the daemon executes it", cmd.ID, agentID)
			triggers(context.Background(), agentID, cmd)
			return
		}
		relay(agentID, cmd)
	}
}

// newHeadlessTriggers builds the handler that executes a headless trigger.
// Its runs share the daemon's headless trigger slots for workspaceRoot, so at
// most the workspace max-concurrent, held to throttle, run at once (#3673).
func newHeadlessTriggers(ctx context.Context, platformClient *platform.Client, host headlessTriggerHost,
	workspaceRoot string, served func() (platform.WorkspaceDeclaration, error),
	throttle *orchestrator.DispatchThrottle) *headlesstrigger.Handler {
	runner := boundedHeadlessTriggerRunner{
		inner: newHeadlessTriggerRunner(workspaceRoot),
		slots: daemonHeadlessTriggerSlots.forWorkspace(workspaceRoot, throttle),
		busy:  host.TriggerIssueRunning,
	}
	h := headlesstrigger.New(ctx, headlesstrigger.Config{
		Acks:    platform.NewCommandService(platformClient),
		Reports: platformClient,
		Runner:  runner,
		Served: func() ([]platform.AgentRepo, error) {
			decl, err := served()
			return decl.Repos, err
		},
		Busy:        host.TriggerIssueBusy,
		ValidatePin: host.ValidateTriggerPin,
	})
	// A headless run never enters the run registry; make it visible to the
	// daemon's busy checks while it runs.
	host.AddIssueBusyCheck(h.Running)
	return h
}

// errHeadlessTriggerDeduped is returned for a trigger whose issue started
// running elsewhere while it waited for a slot; its run is never started.
var errHeadlessTriggerDeduped = errors.New("already-running: the issue started running while this trigger waited for a slot")

// headlessTriggerSlots bounds the headless trigger runs one daemon executes at
// once (#3673). The ceiling is read at every acquire and wake, so a config
// edit or a throttle change reaches runs already waiting.
type headlessTriggerSlots struct {
	ceiling  func() int
	throttle *orchestrator.DispatchThrottle

	mu      sync.Mutex
	running int
	freed   chan struct{} // closed and replaced at each release
}

func newHeadlessTriggerSlots(ceiling func() int, throttle *orchestrator.DispatchThrottle) *headlessTriggerSlots {
	return &headlessTriggerSlots{ceiling: ceiling, throttle: throttle, freed: make(chan struct{})}
}

// limit is the configured ceiling (at least 1) held to the throttle in force;
// a throttle of 0 holds every run.
func (s *headlessTriggerSlots) limit() int {
	configured := s.ceiling()
	if configured < 1 {
		configured = 1
	}
	return s.throttle.Ceiling(configured)
}

// acquire waits for a slot, or for ctx. waiting is called once, with the
// running count and limit, when the run has to wait.
func (s *headlessTriggerSlots) acquire(ctx context.Context, waiting func(running, limit int)) (func(), error) {
	notified := false
	for {
		var changed <-chan struct{}
		if s.throttle != nil {
			changed = s.throttle.Changed()
		}
		limit := s.limit()
		s.mu.Lock()
		if s.running < limit {
			s.running++
			s.mu.Unlock()
			var once sync.Once
			return func() { once.Do(s.release) }, nil
		}
		freed, running := s.freed, s.running
		s.mu.Unlock()
		if !notified && waiting != nil {
			waiting(running, limit)
			notified = true
		}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-freed:
		case <-changed:
		}
	}
}

func (s *headlessTriggerSlots) release() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.running--
	close(s.freed)
	s.freed = make(chan struct{})
}

// headlessTriggerSlotPools holds one slot pool per workspace, shared by the
// handlers of every platform agent serving it.
type headlessTriggerSlotPools struct {
	mu    sync.Mutex
	pools map[string]*headlessTriggerSlots
}

var daemonHeadlessTriggerSlots headlessTriggerSlotPools

func (p *headlessTriggerSlotPools) forWorkspace(workspaceRoot string, throttle *orchestrator.DispatchThrottle) *headlessTriggerSlots {
	p.mu.Lock()
	defer p.mu.Unlock()
	if s, ok := p.pools[workspaceRoot]; ok {
		return s
	}
	if p.pools == nil {
		p.pools = map[string]*headlessTriggerSlots{}
	}
	s := newHeadlessTriggerSlots(func() int {
		cfg, err := config.Load(workspaceRoot)
		if err != nil {
			return 1
		}
		return config.ResolvedMaxConcurrent(cfg)
	}, throttle)
	p.pools[workspaceRoot] = s
	return s
}

// boundedHeadlessTriggerRunner runs inner only once it holds a slot, and not
// at all when the issue started running elsewhere while it waited.
type boundedHeadlessTriggerRunner struct {
	inner headlesstrigger.Runner
	slots *headlessTriggerSlots
	busy  func(repo string, issueNumber int) bool
}

func (r boundedHeadlessTriggerRunner) Run(ctx context.Context, req headlesstrigger.Request) (headlesstrigger.Result, error) {
	waited := false
	release, err := r.slots.acquire(ctx, func(running, limit int) {
		waited = true
		log.Printf("[nightgauge] headless trigger %s#%d: waiting for a headless trigger slot (%d/%d running)",
			req.Repo, req.IssueNumber, running, limit)
	})
	if err != nil {
		return headlesstrigger.Result{}, err
	}
	defer release()
	if waited && r.busy != nil && r.busy(req.Repo, req.IssueNumber) {
		log.Printf("[nightgauge] headless trigger %s#%d: %v", req.Repo, req.IssueNumber, errHeadlessTriggerDeduped)
		return headlesstrigger.Result{}, errHeadlessTriggerDeduped
	}
	return r.inner.Run(ctx, req)
}

// headlessTriggerRunner runs one accepted trigger on its own scheduler, built
// the way `run` builds one: with the adapter resolveRunAdapter picks and an
// ephemeral queue, so serve's scheduler and its queue file are untouched.
type headlessTriggerRunner struct{ workspaceRoot string }

func (r headlessTriggerRunner) Run(ctx context.Context, req headlesstrigger.Request) (headlesstrigger.Result, error) {
	cfg, cfgErr := config.Load(r.workspaceRoot)
	if cfgErr != nil {
		return headlesstrigger.Result{}, fmt.Errorf("load config: %w", cfgErr)
	}
	ident := resolveSchedulerIdentity(r.workspaceRoot, cfg)
	if !ident.Resolvable() {
		return headlesstrigger.Result{}, errors.New("no scheduler identity (owner and project number) for this workspace")
	}
	owner, name, _ := strings.Cut(req.Repo, "/")
	client, err := newClientFromConfigFn(cfg, owner, name)
	if err != nil {
		return headlesstrigger.Result{}, fmt.Errorf("github client: %w", err)
	}
	adapter, adapterExplicit, err := resolveRunAdapter("", cfg, cfgErr)
	if err != nil {
		return headlesstrigger.Result{}, err
	}
	onFailureStatus := "ready"
	excludeLabels := config.DefaultExcludeLabels
	if cfg != nil && cfg.Autonomous != nil {
		onFailureStatus = cfg.Autonomous.ResolvedOnFailureStatus()
		excludeLabels = cfg.Autonomous.ResolvedExcludeLabels()
	}
	sched := orchestrator.NewScheduler(client, orchestrator.SchedulerConfig{
		Owner:           ident.Owner,
		OwnerType:       gh.ParseOwnerType(ident.OwnerType),
		ProjectNumber:   ident.ProjectNumber,
		MaxPerRepo:      1,
		WorkspaceRoot:   r.workspaceRoot,
		Adapter:         adapter,
		AdapterExplicit: adapterExplicit,
		OnFailureStatus: onFailureStatus,
		ExcludeLabels:   excludeLabels,
		RuntimeConfig:   cfg,
		EphemeralQueue:  true,
	})
	sched.WithWorkspaceRepoRegistry(r.workspaceRoot)
	wireIdentityChecker(sched, r.workspaceRoot)
	return runHeadlessTrigger(ctx, sched, req)
}

// headlessScheduler is the part of *orchestrator.Scheduler a headless trigger
// run drives.
type headlessScheduler interface {
	QueueAddItem(items ...orchestrator.QueueItem)
	RunQueue(ctx context.Context) (orchestrator.QueueRunSummary, error)
}

// runHeadlessTrigger queues req as the scheduler's one item and runs it. The
// run's id is a fresh runstate id; the ack's run id is only the remote run it
// serves, never its id (#3586, #3605).
func runHeadlessTrigger(ctx context.Context, sched headlessScheduler, req headlesstrigger.Request) (headlesstrigger.Result, error) {
	runID, err := runstate.NewRunID()
	if err != nil {
		return headlesstrigger.Result{}, fmt.Errorf("mint run id: %w", err)
	}
	sched.QueueAddItem(orchestrator.QueueItem{
		Repo:             req.Repo,
		IssueNumber:      req.IssueNumber,
		RunID:            runID,
		RemoteRunID:      req.RemoteRunID,
		Visibility:       req.Visibility,
		RequestedAdapter: req.Adapter,
		RequestedModel:   req.Model,
	})
	summary, err := sched.RunQueue(ctx)
	var res headlesstrigger.Result
	for _, o := range summary.Outcomes {
		if o.IssueNumber != req.IssueNumber || !strings.EqualFold(o.Repo, req.Repo) {
			continue
		}
		res.Completed = o.Kind == orchestrator.QueueOutcomeCompleted
		res.TerminalKind = o.TerminalKind
		if o.Kind == orchestrator.QueueOutcomeNotDispatched || o.Kind == orchestrator.QueueOutcomeBlocked {
			log.Printf("[nightgauge] headless trigger: %s#%d was not run (%s): %s", req.Repo, req.IssueNumber, o.Kind, o.Detail)
		}
	}
	return res, err
}

// daemonHeadlessTriggers are the headless trigger handlers this daemon built,
// one per platform agent. serve waits for them at shutdown, bounded, so a run
// whose context was cancelled at shutdown still reports its terminal status
// to the platform.
var daemonHeadlessTriggers headlessTriggerHandlers

type headlessTriggerHandlers struct {
	mu       sync.Mutex
	handlers []*headlesstrigger.Handler
}

func (h *headlessTriggerHandlers) add(handler *headlesstrigger.Handler) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.handlers = append(h.handlers, handler)
}

func (h *headlessTriggerHandlers) waitFor(d time.Duration) {
	h.mu.Lock()
	handlers := append([]*headlesstrigger.Handler(nil), h.handlers...)
	h.mu.Unlock()
	deadline := time.Now().Add(d)
	for _, handler := range handlers {
		if !handler.WaitFor(time.Until(deadline)) {
			log.Printf("[nightgauge] headless trigger runs still stopping at shutdown after %s", d)
			return
		}
	}
}
