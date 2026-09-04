package ipc

// Action Center IPC surface (ADR 015 §E). The extension binds to the local
// DecisionRequest store through three methods — attention.list, attention.resolve,
// attention.acknowledge — plus the `attention.event` push (wired in
// SetAutonomousScheduler). resolve is the sole mutation and always terminates at
// the single Go writer, which re-validates the option against the persisted
// request AND the verb registry before executing (defense in depth, §J).

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"os"
	"strings"

	"github.com/nightgauge/nightgauge/internal/attention"
	"github.com/nightgauge/nightgauge/internal/attention/sweep"
	"github.com/nightgauge/nightgauge/internal/config"
	gh "github.com/nightgauge/nightgauge/internal/github"
	"github.com/nightgauge/nightgauge/internal/orchestrator"
	"github.com/nightgauge/nightgauge/internal/platform"
	"github.com/nightgauge/nightgauge/internal/workspacemanifest"
)

// AttentionListResult is the attention.list response — the materialized read
// model, ordered most-severe-then-newest.
type AttentionListResult struct {
	Requests []attention.DecisionRequest `json:"requests"`
}

// AttentionResolveResult is the attention.resolve response. Ok is always true
// when the method returns without error — a verb failure now short-circuits
// Resolve into a returned error before this handler ever builds a result, so
// there is no longer a way to reach this struct with a failed verb.
type AttentionResolveResult struct {
	Ok              bool `json:"ok"`
	AlreadyResolved bool `json:"alreadyResolved"`
}

// AttentionAcknowledgeResult is the attention.acknowledge response.
type AttentionAcknowledgeResult struct {
	Ok bool `json:"ok"`
}

// Surfaces a resolve can arrive from, used as the fallback actor label when the
// payload does not name one. The label must name the surface that ACTUALLY
// acted: "vscode" on a relayed platform resolve would be a false attribution,
// dishonest in exactly the way `$USER` was (#1418).
const (
	actorSurfaceVSCode   = "vscode"
	actorSurfacePlatform = "platform"
)

// ipcAttentionActor names who resolved a card when the payload does not
// (#1405), labelled by the surface the request arrived from (#1418).
//
// The store refuses an empty actor because the card contract requires one and
// it cannot know who the operator is. This layer does know the SHAPE of the
// caller, so it supplies that rather than letting an operator's click fail.
//
// EVERY Store.Resolve / Store.Acknowledge CALLER MUST GO THROUGH A FALLBACK
// LIKE THIS ONE, and #1418 exists because three of the four did.
// ApplyRelayedResolve passed its actor raw while its two siblings in this same
// file did not — and that one is the worst of the four to miss: a CLI resolve
// with an empty actor fails loudly, whereas a relayed one has its error acked
// as consumed and never retried (#1421), so the dashboard reports success and
// the card stays open. Loud and silent versions of one defect.
func ipcAttentionActor(actor, surface string) string {
	if strings.TrimSpace(actor) != "" {
		return actor
	}
	return surface
}

// attentionStore returns the shared DecisionRequest store, or nil when no
// autonomous scheduler is attached (the store lives on it).

func (s *Server) attentionStore() *attention.Store {
	if s.autonomousScheduler == nil {
		return nil
	}
	return s.autonomousScheduler.Attention()
}

// handleAttentionList returns open (and optionally terminal) requests, ordered
// most-severe-then-newest.
func (s *Server) handleAttentionList(_ context.Context, raw json.RawMessage) (interface{}, error) {
	var p AttentionListParams
	if len(raw) > 0 {
		if err := json.Unmarshal(raw, &p); err != nil {
			return nil, fmt.Errorf("attention.list: parse params: %w", err)
		}
	}
	store := s.attentionStore()
	if store == nil {
		return AttentionListResult{Requests: []attention.DecisionRequest{}}, nil
	}
	reqs, err := store.List(attention.ListFilter{IncludeTerminal: p.IncludeTerminal, Repo: p.Repo})
	if err != nil {
		return nil, fmt.Errorf("attention.list: %w", err)
	}
	if reqs == nil {
		reqs = []attention.DecisionRequest{}
	}
	return AttentionListResult{Requests: reqs}, nil
}

// handleAttentionResolve applies a resolution and executes the option's verb.
// Validation failures (unknown option / unregistered verb) return a generic
// client error; details are logged internally (§J error hygiene).
func (s *Server) handleAttentionResolve(ctx context.Context, raw json.RawMessage) (interface{}, error) {
	var p AttentionResolveParams
	if err := json.Unmarshal(raw, &p); err != nil {
		return nil, fmt.Errorf("attention.resolve: parse params: %w", err)
	}
	if p.ID == "" || p.OptionID == "" {
		return nil, fmt.Errorf("attention.resolve: id and optionId are required")
	}
	store := s.attentionStore()
	if store == nil {
		return nil, fmt.Errorf("attention.resolve: attention store not configured")
	}
	res, err := store.Resolve(ctx, p.ID, p.OptionID, ipcAttentionActor(p.Actor, actorSurfaceVSCode), p.SteerText, p.Note, s)
	if err != nil {
		log.Printf("attention.resolve: rejected id=%s option=%s: %v", p.ID, p.OptionID, err)
		return nil, fmt.Errorf("attention.resolve: could not resolve request")
	}
	if res.SteerErr != nil {
		log.Printf("attention.resolve: steer write failed id=%s (non-fatal): %v", p.ID, res.SteerErr)
	}
	return AttentionResolveResult{Ok: true, AlreadyResolved: res.AlreadyResolved}, nil
}

// ApplyRelayedResolve applies a platform-relayed dashboard resolution through the
// single authoritative writer, executing the option's bound verb (this server is
// the verb executor). It satisfies platform.AttentionResolver so the attention
// command consumer applies a dashboard resolve with the SAME CAS + verb +
// attention.event path as a local IPC resolve — the store's existing listener
// fan-out fires the attention.event push, so no second event emitter is added
// (ADR 015 §D/§E, #330). Option re-validation happens inside store.Resolve
// (ValidateOption against the persisted request AND the verb registry — §J
// defense in depth); an unknown option / unregistered verb returns an error and
// the request is left untouched.
//
// A command naming a card this store does not hold is classified separately and
// returned as platform.ErrRelayedRequestNotHere — it is a misrouted command, not
// a rejected one, and this daemon never resolves it on the owner's behalf
// (#1421, ADR-019).
func (s *Server) ApplyRelayedResolve(ctx context.Context, requestID, optionID, actor, steerText string) (platform.AttentionResolveOutcome, error) {
	store := s.attentionStore()
	if store == nil {
		return platform.AttentionResolveOutcome{}, fmt.Errorf("attention store not configured")
	}
	res, err := store.Resolve(ctx, requestID, optionID, ipcAttentionActor(actor, actorSurfacePlatform), steerText, "", s)
	if err != nil {
		if errors.Is(err, attention.ErrRequestNotFound) {
			// #1421: the command was addressed correctly and delivered to the
			// wrong daemon. The platform upserts an agent row per MACHINE, so
			// every workspace daemon on this machine shares one agent id and
			// one command channel; the store it resolves against is per
			// WORKSPACE. Whichever daemon holds the stream consumes the frame,
			// and if the card was raised elsewhere this one cannot apply it.
			//
			// This daemon deliberately does NOT reach into the owning
			// workspace's store: the verb executor is this server — its
			// scheduler, its repo resolver, its steer root — so a cross-root
			// write would persist the resolution under one workspace and run
			// the verb against another. Write containment
			// (docs/MULTI_REPO_WORKSPACE.md) forbids it for that reason, and
			// the store has no cross-process lock to make it safe even if it
			// did not.
			//
			// So the honest move is to say precisely what happened, naming
			// this daemon's root — without it, an operator reading the log
			// cannot tell which of several daemons answered.
			log.Printf("attention_resolve: request %s is not in this daemon's attention store "+
				"(workspace root %s, store dir %s) — the card was raised under a different workspace "+
				"on this machine; routing it needs a platform-side change (#1421, ADR-019)",
				requestID, s.workspaceRoot, store.Dir())
			return platform.AttentionResolveOutcome{NotInThisWorkspace: true},
				fmt.Errorf("%w: %w", platform.ErrRelayedRequestNotHere, err)
		}
		return platform.AttentionResolveOutcome{}, err
	}
	return platform.AttentionResolveOutcome{
		Applied:         !res.AlreadyResolved,
		AlreadyResolved: res.AlreadyResolved,
	}, nil
}

// handleAttentionAcknowledge marks a request seen without resolving it.
func (s *Server) handleAttentionAcknowledge(_ context.Context, raw json.RawMessage) (interface{}, error) {
	var p AttentionAcknowledgeParams
	if err := json.Unmarshal(raw, &p); err != nil {
		return nil, fmt.Errorf("attention.acknowledge: parse params: %w", err)
	}
	if p.ID == "" {
		return nil, fmt.Errorf("attention.acknowledge: id is required")
	}
	store := s.attentionStore()
	if store == nil {
		return nil, fmt.Errorf("attention.acknowledge: attention store not configured")
	}
	if _, err := store.Acknowledge(p.ID, ipcAttentionActor(p.Actor, actorSurfaceVSCode)); err != nil {
		return nil, fmt.Errorf("attention.acknowledge: %w", err)
	}
	return AttentionAcknowledgeResult{Ok: true}, nil
}

// handleIssueRemoveBlockedBy is the thin IPC wrapper the ADR calls for (§B) —
// a pure re-export of the existing internal RemoveBlockedByNumber call, not new
// mutation logic. No IPC method existed for it before E1.
func (s *Server) handleIssueRemoveBlockedBy(ctx context.Context, raw json.RawMessage) (interface{}, error) {
	var p IssueRemoveBlockedByParams
	if err := json.Unmarshal(raw, &p); err != nil {
		return nil, fmt.Errorf("issue.removeBlockedBy: parse params: %w", err)
	}
	if p.Owner == "" || p.Repo == "" || p.BlockedNumber == 0 || p.BlockerNumber == 0 {
		return nil, fmt.Errorf("issue.removeBlockedBy: owner, repo, blockedNumber, blockerNumber are required")
	}
	c, err := s.resolveClientForRequest(ctx, p.GitHubUser, p.Owner, p.Repo)
	if err != nil {
		return nil, err
	}
	projSvc := s.boardServicesFor(c, p.Owner, 0, gh.OwnerTypeUser).Project
	if err := projSvc.RemoveBlockedByNumber(ctx, p.Owner, p.Repo, p.BlockedNumber, p.BlockerNumber); err != nil {
		return nil, err
	}
	return map[string]string{"status": "ok"}, nil
}

// ExecuteVerb implements attention.VerbExecutor for the daemon: it binds each
// registered verb to the trusted primitive the fleet already exposes. The store
// calls this AFTER the resolution is persisted (CAS), so a verb failure is
// audited but never leaves the request half-open.
func (s *Server) ExecuteVerb(ctx context.Context, req *attention.DecisionRequest, opt attention.Option) error {
	repo := req.Context.Repo
	issue := req.Context.Issue
	owner, name := splitOwnerRepo(repo)
	key := fmt.Sprintf("%s#%d", repo, issue)
	actor := ""
	if req.Lifecycle.Resolved != nil {
		actor = req.Lifecycle.Resolved.Actor
	}

	switch opt.Verb {
	case attention.VerbNoop:
		return nil

	case attention.VerbAutonomousResume:
		// Through the shared helper, not a bare Resume(): after a backend
		// restart the fleet comes up latched with no goroutine alive (#405),
		// and the #3303 dead state — status flips to "running", nothing
		// dispatches — is reachable from this card the moment a halt survives
		// a boot. Resuming and ensuring the loop is up is one action.
		return s.resumeAndEnsureRunning(ctx)

	case attention.VerbAutonomousRescan:
		if s.autonomousScheduler == nil {
			return fmt.Errorf("autonomous scheduler not configured")
		}
		s.autonomousScheduler.TriggerRescan()
		return nil

	case attention.VerbAutonomousComplete:
		if s.autonomousScheduler == nil {
			return fmt.Errorf("autonomous scheduler not configured")
		}
		s.autonomousScheduler.NotifyComplete(repo, issue, true, false, "", "")
		if argString(opt.Args, "then") == "issue.close" {
			if err := s.closeIssueBestEffort(ctx, owner, name, issue); err != nil {
				log.Printf("attention: mark-done issue.close failed for %s (non-fatal): %v", key, err)
			}
		}
		return nil

	case attention.VerbAutonomousClearIssueFailures:
		if s.autonomousScheduler == nil {
			return fmt.Errorf("autonomous scheduler not configured")
		}
		k := argString(opt.Args, "key")
		if k == "" {
			k = key
		}
		s.autonomousScheduler.ClearIssueFailures(k)
		return s.applyThenAction(ctx, argString(opt.Args, "then"), repo)

	case attention.VerbQueueAdd:
		if s.scheduler == nil {
			return fmt.Errorf("scheduler not configured")
		}
		s.scheduler.QueueAddItem(orchestrator.QueueItem{Repo: repo, IssueNumber: issue, Title: argString(opt.Args, "title")})
		return nil

	case attention.VerbBudgetRaiseCeiling:
		ceiling := argFloat(opt.Args, "ceilingUsd")
		// THE CARD'S OWN REPO ROOT, not s.workspaceRoot (fixed in #305 review).
		// `s.workspaceRoot` is a mutable pointer to whichever repo owns the
		// focused editor (`workspace.setRoot` ← `resolveActiveRepository`), so
		// in a multi-repo workspace the override landed under whatever the
		// operator happened to be looking at when they clicked — while the run
		// that needs it reads its OWN repo's `.nightgauge/pipeline/`. Same
		// per-repo registry that scopes run state (#215/#307), and the same root
		// the raise resolved its enforced ceiling from, so proposal and
		// persistence cannot disagree about which file is live.
		if err := orchestrator.WriteBudgetCeilingOverride(s.repoRoot(repo), ceiling, actor, "action-center: budget.raiseCeiling"); err != nil {
			return err
		}
		return s.redispatchAfterOverride(ctx, key, repo, issue, argString(opt.Args, "then"))

	case attention.VerbRunRetryWithEscalation:
		tier := argString(opt.Args, "tier")
		if tier == "" {
			tier = "opus"
		}
		if err := orchestrator.WriteEscalationOverride(s.workspaceRootPath(), issue, tier, actor); err != nil {
			return err
		}
		return s.redispatchAfterOverride(ctx, key, repo, issue, argString(opt.Args, "then"))

	case attention.VerbIssueClose:
		return s.closeIssueBestEffort(ctx, owner, name, issue)

	case attention.VerbIssueRemoveBlockedBy:
		blocker := argInt(opt.Args, "blockerNumber")
		if blocker == 0 {
			return fmt.Errorf("issue.removeBlockedBy: blockerNumber required")
		}
		c, err := s.resolveClientForRequest(ctx, "", owner, name)
		if err != nil {
			return err
		}
		return s.boardServicesFor(c, owner, 0, gh.OwnerTypeUser).Project.RemoveBlockedByNumber(ctx, owner, name, issue, blocker)

	case attention.VerbWorkspaceAddRepo:
		// The workspace root, not s.repoRoot(repo): the manifest lives once at
		// the workspace root, and repo is by definition NOT yet a configured
		// repository, so it has no root of its own to resolve.
		root := s.workspaceRootPath()
		return attention.ExecuteAddRepo(ctx, workspaceRepoAdder{root: root}, req, opt, sweep.ConfiguredRepos(root))

	case attention.VerbBlockedFindingClear:
		// The RUN'S OWN REPO ROOT, not s.workspaceRoot — the same per-repo
		// registry that decides where a run's runtime snapshot and its
		// budget-override.json live. The extension writes the finding under
		// getRunRepoRoot(); a daemon that deleted from the focused editor's repo
		// instead would report success and leave the issue deferring forever,
		// which is the multi-repo inertness #305 had to fix on the budget path.
		return attention.ExecuteClearBlockedFinding(ctx, blockedFindingClearer{server: s},
			req, opt, sweep.ConfiguredRepos(s.workspaceRootPath()))

	case attention.VerbIssueApproveArchitecture:
		return s.approveArchitecture(ctx, key, repo, owner, name, issue)

	case attention.VerbProjectSyncStatus:
		// Not producer-emitted in E1; the extension surface (#325) supplies full
		// project config for this path. Kept registry-gated for future use.
		return fmt.Errorf("project.syncStatus resolution is handled by the extension surface, not the daemon executor")

	default:
		return fmt.Errorf("attention: unsupported verb %q", opt.Verb)
	}
}

// Follow-on actions a card option may declare in Args["then"] — the second
// half of a retry-shaped verb, executed after its primary effect lands.
// (autonomous.complete carries its own `then: issue.close`, handled inline in
// that arm because it needs the request's owner/repo/number.)
const (
	thenAutonomousRescan = "autonomous.rescan"
	thenAutonomousResume = "autonomous.resume"
	// thenAutonomousResumeRepo releases only the halted repository named by the
	// request's Context.Repo (#1148). The terminal-failure card's Retry uses it
	// because the halt it reports is scoped to one repository.
	thenAutonomousResumeRepo = "autonomous.resumeRepo"
)

// applyThenAction executes the follow-on action named by an option's "then"
// argument. Empty means "nothing further".
//
// An UNRECOGNIZED value is an error, never a silent success (#405). The Retry
// option on the fleet-halt card shipped `then: autonomous.resume` against an
// arm that honored only `autonomous.rescan`: the value fell through a bare
// string compare, the verb returned nil, the store had already CAS-resolved
// the request — so the card vanished, the fleet stayed halted, and the one
// surface that could re-raise it was gone. A card option that cannot be
// carried out must fail loudly enough to be audited.
func (s *Server) applyThenAction(ctx context.Context, then, repo string) error {
	switch then {
	case "":
		return nil
	case thenAutonomousRescan:
		if s.autonomousScheduler == nil {
			return fmt.Errorf("autonomous scheduler not configured")
		}
		s.autonomousScheduler.TriggerRescan()
		return nil
	case thenAutonomousResume:
		return s.resumeAndEnsureRunning(ctx)
	case thenAutonomousResumeRepo:
		// #1148: the repo comes from the card's own Context, never from the
		// option args — a card that could name any repo in its `then` would
		// let one repo's Retry release another repo's halt.
		if repo == "" {
			return fmt.Errorf("attention: %q needs a repo on the request context", thenAutonomousResumeRepo)
		}
		return s.resumeRepoAndEnsureRunning(ctx, repo)
	default:
		log.Printf("attention: WARN unknown \"then\" action %q — the option's follow-on step was NOT executed", then)
		return fmt.Errorf("attention: unknown \"then\" action %q", then)
	}
}

// resumeAndEnsureRunning is the one resume primitive behind every operator
// surface that means "go again": the IPC autonomous.resume method, the
// autonomous.resume verb, and any card option whose `then` is a resume.
//
// Resume() alone is not enough. After a backend restart the persisted state
// comes back halted (a safety trip, or since #405 a latched machine halt) with
// no goroutine alive; flipping the status to "running" without starting Run()
// leaves the silent "running but never dispatching" dead state #3303 fixed for
// the IPC method — and the fixup made that state reachable from the card path
// too, because a halt now survives a boot. One helper, so the next surface
// added cannot get half of it.
//
// ctx is the server-lifetime context (IPC handlers and ExecuteVerb both
// receive it), so the spawned loop dies with the daemon and not with the
// request that started it.
func (s *Server) resumeAndEnsureRunning(ctx context.Context) error {
	if s.autonomousScheduler == nil {
		return fmt.Errorf("autonomous scheduler not configured")
	}
	s.autonomousScheduler.Resume()
	// A fleet resume clears every repo-scoped halt too, so the Repositories
	// view's per-row halt badges must be told (#1148 visibility).
	s.emitRepoHaltChanged()
	if !s.autonomousScheduler.IsRunning() {
		go func() {
			if err := s.autonomousScheduler.Run(ctx); err != nil {
				log.Printf("autonomous scheduler exited: %v", err)
			}
		}()
	}
	return nil
}

// resumeRepoAndEnsureRunning is the repo-scoped twin of
// resumeAndEnsureRunning (#1148): release one repository's halt, then make
// sure the dispatch loop is actually up. Both halves are needed for the same
// reason the fleet path needs them — after a backend restart the halt comes
// back from persisted state with no goroutine alive, and clearing it without
// starting Run() is the silent "resumed but never dispatches" state.
func (s *Server) resumeRepoAndEnsureRunning(ctx context.Context, repo string) error {
	if s.autonomousScheduler == nil {
		return fmt.Errorf("autonomous scheduler not configured")
	}
	s.autonomousScheduler.ResumeRepo(repo)
	s.emitRepoHaltChanged()
	if !s.autonomousScheduler.IsRunning() {
		go func() {
			if err := s.autonomousScheduler.Run(ctx); err != nil {
				log.Printf("autonomous scheduler exited: %v", err)
			}
		}()
	}
	return nil
}

// emitRepoHaltChanged tells connected clients that the set of repo-scoped
// halts (#1148) may have changed.
//
// It exists because a repo halt deliberately does NOT move the fleet Status,
// so `autonomous.statusChanged` never fires for one. Without this event the
// Repositories view's per-row warning badge could only appear on the next
// full tree render — which for a workspace sitting idle (exactly the state a
// halt produces) may be a very long time.
//
// The payload carries only the count. A client that cares reads
// `autonomous.status` for the records, so there is one wire shape for halt
// data instead of two that can drift.
func (s *Server) emitRepoHaltChanged() {
	if s == nil || s.autonomousScheduler == nil {
		return
	}
	s.Emit("autonomous.repoHaltChanged", map[string]interface{}{
		"haltedCount": len(s.autonomousScheduler.PausedReposSnapshot()),
	})
}

// redispatchAfterOverride clears the issue failure cooldown, requeues, and
// wakes the scheduler — the common tail of budget.raiseCeiling and
// run.retryWithEscalation so the override actually takes effect on a retry.
//
// `then` carries the option's follow-on action; a rescan is the historical
// default when the option declares none. "Retry with escalation" on the
// fleet-halt card declares `autonomous.resume`, and it must actually resume:
// a rescan on a halted fleet re-runs a cycle that returns at its
// `Status != "running"` guard, so the retry the operator asked for never
// dispatches (#405).
func (s *Server) redispatchAfterOverride(ctx context.Context, key, repo string, issue int, then string) error {
	if s.autonomousScheduler != nil {
		s.autonomousScheduler.ClearIssueFailures(key)
	}
	if s.scheduler != nil {
		s.scheduler.QueueAddItem(orchestrator.QueueItem{Repo: repo, IssueNumber: issue})
	}
	if s.autonomousScheduler == nil {
		return nil
	}
	if then == "" {
		then = thenAutonomousRescan
	}
	return s.applyThenAction(ctx, then, repo)
}

// closeIssueBestEffort closes a GitHub issue via the resolved per-repo client.
func (s *Server) closeIssueBestEffort(ctx context.Context, owner, repo string, number int) error {
	if owner == "" || repo == "" || number == 0 {
		return fmt.Errorf("issue.close: owner/repo/number required")
	}
	c, err := s.resolveClientForRequest(ctx, "", owner, repo)
	if err != nil {
		return err
	}
	svc := gh.NewIssueService(c)
	iss, err := svc.GetIssue(ctx, owner, repo, number)
	if err != nil {
		return fmt.Errorf("fetch issue #%d: %w", number, err)
	}
	return svc.CloseIssue(ctx, iss.NodeID)
}

// approveArchitecture grants the architecture-approval gate for one issue.
//
// The gate (#4098/#4222) reads approval evidence out-of-band — the approval
// label on the issue, or an approval file — precisely so it is exempt from
// human_in_the_loop.auto_accept_stages. This is the label path, executed by the
// single Go writer so the Action Center can offer it as a one-click option
// rather than leaving the operator to run gh by hand.
//
// The label NAME is resolved from config here, never from opt.Args: the verb
// can only ever grant this specific gate, and a surface cannot smuggle an
// arbitrary label onto an arbitrary issue. Label creation is idempotent
// (LabelService.Create returns an existing label untouched), which matters
// because a repo that has never gated before has no such label yet.
//
// After labelling, the issue is requeued through the same tail as the other
// override verbs — the gate is only re-evaluated on the next run, so approving
// without requeuing would leave the issue sidelined in "In review" forever.
func (s *Server) approveArchitecture(ctx context.Context, key, repo, owner, name string, issue int) error {
	if owner == "" || name == "" || issue == 0 {
		return fmt.Errorf("issue.approveArchitecture: owner/repo/issueNumber required")
	}

	label := config.DefaultArchitectureApprovalLabel
	if cfg, err := config.Load(s.workspaceRootPath()); err == nil && cfg != nil {
		if resolved := cfg.Pipeline.ResolveArchitectureApprovalLabel(); resolved != "" {
			label = resolved
		}
	}

	c, err := s.resolveClientForRequest(ctx, "", owner, name)
	if err != nil {
		return err
	}

	lbl, err := gh.NewLabelService(c, owner, name).Create(ctx, label,
		"Human-approved architectural decision — architecture gate passes", "0e8a16")
	if err != nil {
		return fmt.Errorf("resolve approval label %q in %s/%s: %w", label, owner, name, err)
	}

	svc := gh.NewIssueService(c)
	iss, err := svc.GetIssue(ctx, owner, name, issue)
	if err != nil {
		return fmt.Errorf("fetch issue #%d: %w", issue, err)
	}
	if err := svc.AddLabels(ctx, iss.NodeID, []string{lbl.ID}); err != nil {
		return fmt.Errorf("apply %q to #%d: %w", label, issue, err)
	}

	log.Printf("attention: architecture approval granted for %s#%d (label %q) — clearing cooldown and requeuing",
		repo, issue, label)
	return s.redispatchAfterOverride(ctx, key, repo, issue, "")
}

// --- small arg helpers (opt.Args round-trips through JSON: numbers are float64) ---

func argString(m map[string]any, k string) string {
	if v, ok := m[k]; ok {
		if s, ok := v.(string); ok {
			return s
		}
	}
	return ""
}

func argFloat(m map[string]any, k string) float64 {
	if v, ok := m[k]; ok {
		switch n := v.(type) {
		case float64:
			return n
		case int:
			return float64(n)
		}
	}
	return 0
}

func argInt(m map[string]any, k string) int {
	if v, ok := m[k]; ok {
		switch n := v.(type) {
		case float64:
			return int(n)
		case int:
			return n
		}
	}
	return 0
}

// workspaceRepoAdder backs attention.VerbWorkspaceAddRepo with the
// deterministic manifest writer (#703/#706).
//
// It holds only the workspace root: the target repository arrives from the
// card's own Context.Repo through ExecuteAddRepo, never from this struct, so
// there is no field here a surface could influence.
type workspaceRepoAdder struct{ root string }

// AddWorkspaceRepo appends repo to the workspace manifest, resolving its
// project board through the single authoritative repo→project resolver.
//
// Role is "primary" rather than caller-supplied. The verb takes no arguments,
// so there is nothing to derive a role from, and "primary" is what every entry
// a human writes into a real manifest uses; an operator who wants "secondary"
// edits the entry afterwards. Guessing from a card would be judgement, which is
// the one thing a registry verb may not do.
func (a workspaceRepoAdder) AddWorkspaceRepo(_ context.Context, repo string) error {
	m, err := workspacemanifest.Load(workspacemanifest.ManifestPath(a.root))
	if err != nil {
		return err
	}
	entry, err := workspacemanifest.DeriveEntry(m, repo, "primary")
	if err != nil {
		return err
	}
	// The card may name the repo as "owner/name" or bare. Prefer the owner it
	// declared; fall back to the workspace's own owner when it gave none.
	cardOwner := ""
	if i := strings.Index(repo, "/"); i > 0 {
		cardOwner = repo[:i]
	}

	_, err = workspacemanifest.AddRepo(m, entry, func(name string) (int, error) {
		cfg, cerr := config.Load(a.root)
		if cerr != nil || cfg == nil {
			return 0, fmt.Errorf("no config loaded for repo→project resolution: %w", cerr)
		}
		owner := cardOwner
		if owner == "" {
			owner = cfg.Owner
		}
		return config.ResolveRepoProjectNumber(cfg, config.RepoProjectQuery{
			Owner:    owner,
			Repo:     name,
			StartDir: a.root,
		})
	})
	return err
}

// blockedFindingClearer backs attention.VerbBlockedFindingClear by deleting the
// recorded out-of-scope finding for one issue (#1147).
//
// It holds the server rather than a path: the repository arrives from the
// card's own Context.Repo through ExecuteClearBlockedFinding, and only then is
// it resolved to a root via s.repoRoot — so there is no field here a resolving
// surface could influence, and the root is the RUN'S repo rather than whichever
// one owns the focused editor.
type blockedFindingClearer struct{ server *Server }

// ClearBlockedFinding removes the finding file, treating "already gone" as
// success.
//
// The store CAS-resolves the card only after this returns nil, and two surfaces
// can resolve the same card — so reporting a missing file as a failure would
// leave an un-resolvable card in front of an operator whose blocker really is
// cleared. A genuine I/O failure (a permission error, say) still errors, because
// then the hold IS still in place and the card must survive to say so.
func (c blockedFindingClearer) ClearBlockedFinding(_ context.Context, repo string, issue int) error {
	path := orchestrator.BlockedFindingPath(c.server.repoRoot(repo), issue)
	if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("attention: clear blocked finding for %s#%d: %w", repo, issue, err)
	}
	return nil
}
