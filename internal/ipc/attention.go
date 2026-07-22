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
	"fmt"
	"log"

	"github.com/nightgauge/nightgauge/internal/attention"
	gh "github.com/nightgauge/nightgauge/internal/github"
	"github.com/nightgauge/nightgauge/internal/orchestrator"
	"github.com/nightgauge/nightgauge/internal/platform"
)

// AttentionListResult is the attention.list response — the materialized read
// model, ordered most-severe-then-newest.
type AttentionListResult struct {
	Requests []attention.DecisionRequest `json:"requests"`
}

// AttentionResolveResult is the attention.resolve response. Ok is false when the
// verb side-effect failed (the resolution itself still applied, once).
type AttentionResolveResult struct {
	Ok              bool `json:"ok"`
	AlreadyResolved bool `json:"alreadyResolved"`
}

// AttentionAcknowledgeResult is the attention.acknowledge response.
type AttentionAcknowledgeResult struct {
	Ok bool `json:"ok"`
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
	res, err := store.Resolve(ctx, p.ID, p.OptionID, p.Actor, p.SteerText, p.Note, s)
	if err != nil {
		log.Printf("attention.resolve: rejected id=%s option=%s: %v", p.ID, p.OptionID, err)
		return nil, fmt.Errorf("attention.resolve: could not resolve request")
	}
	if res.SteerErr != nil {
		log.Printf("attention.resolve: steer write failed id=%s (non-fatal): %v", p.ID, res.SteerErr)
	}
	if res.VerbErr != nil {
		log.Printf("attention.resolve: verb execution failed id=%s option=%s: %v", p.ID, p.OptionID, res.VerbErr)
	}
	return AttentionResolveResult{Ok: res.VerbErr == nil, AlreadyResolved: res.AlreadyResolved}, nil
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
func (s *Server) ApplyRelayedResolve(ctx context.Context, requestID, optionID, actor, steerText string) (platform.AttentionResolveOutcome, error) {
	store := s.attentionStore()
	if store == nil {
		return platform.AttentionResolveOutcome{}, fmt.Errorf("attention store not configured")
	}
	res, err := store.Resolve(ctx, requestID, optionID, actor, steerText, "", s)
	if err != nil {
		return platform.AttentionResolveOutcome{}, err
	}
	return platform.AttentionResolveOutcome{
		Applied:         !res.AlreadyResolved,
		AlreadyResolved: res.AlreadyResolved,
		VerbErr:         res.VerbErr,
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
	if _, err := store.Acknowledge(p.ID, p.Actor); err != nil {
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
	projSvc := gh.NewProjectService(c, p.Owner, 0, gh.OwnerTypeUser)
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
		if s.autonomousScheduler == nil {
			return fmt.Errorf("autonomous scheduler not configured")
		}
		s.autonomousScheduler.Resume()
		return nil

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
		if argString(opt.Args, "then") == "autonomous.rescan" {
			s.autonomousScheduler.TriggerRescan()
		}
		return nil

	case attention.VerbQueueAdd:
		if s.scheduler == nil {
			return fmt.Errorf("scheduler not configured")
		}
		s.scheduler.QueueAddItem(orchestrator.QueueItem{Repo: repo, IssueNumber: issue, Title: argString(opt.Args, "title")})
		return nil

	case attention.VerbBudgetRaiseCeiling:
		ceiling := argFloat(opt.Args, "ceilingUsd")
		if err := orchestrator.WriteBudgetCeilingOverride(s.workspaceRoot, ceiling, actor, "action-center: budget.raiseCeiling"); err != nil {
			return err
		}
		s.redispatchAfterOverride(key, repo, issue)
		return nil

	case attention.VerbRunRetryWithEscalation:
		tier := argString(opt.Args, "tier")
		if tier == "" {
			tier = "opus"
		}
		if err := orchestrator.WriteEscalationOverride(s.workspaceRoot, issue, tier, actor); err != nil {
			return err
		}
		s.redispatchAfterOverride(key, repo, issue)
		return nil

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
		return gh.NewProjectService(c, owner, 0, gh.OwnerTypeUser).RemoveBlockedByNumber(ctx, owner, name, issue, blocker)

	case attention.VerbProjectSyncStatus:
		// Not producer-emitted in E1; the extension surface (#325) supplies full
		// project config for this path. Kept registry-gated for future use.
		return fmt.Errorf("project.syncStatus resolution is handled by the extension surface, not the daemon executor")

	default:
		return fmt.Errorf("attention: unsupported verb %q", opt.Verb)
	}
}

// redispatchAfterOverride clears the issue failure cooldown, requeues, and wakes
// the scheduler — the common tail of budget.raiseCeiling and
// run.retryWithEscalation so the override actually takes effect on a retry.
func (s *Server) redispatchAfterOverride(key, repo string, issue int) {
	if s.autonomousScheduler != nil {
		s.autonomousScheduler.ClearIssueFailures(key)
	}
	if s.scheduler != nil {
		s.scheduler.QueueAddItem(orchestrator.QueueItem{Repo: repo, IssueNumber: issue})
	}
	if s.autonomousScheduler != nil {
		s.autonomousScheduler.TriggerRescan()
	}
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
