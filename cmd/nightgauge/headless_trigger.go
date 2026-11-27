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
func newHeadlessTriggers(ctx context.Context, platformClient *platform.Client, host headlessTriggerHost,
	workspaceRoot string, served func() (platform.WorkspaceDeclaration, error)) *headlesstrigger.Handler {
	h := headlesstrigger.New(ctx, headlesstrigger.Config{
		Acks:    platform.NewCommandService(platformClient),
		Reports: platformClient,
		Runner:  newHeadlessTriggerRunner(workspaceRoot),
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
