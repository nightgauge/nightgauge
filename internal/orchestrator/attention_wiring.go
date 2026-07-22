package orchestrator

// Action Center producer wiring (ADR 015 §F). Each producer replaces a
// dead-end that is today silent or one-way: instead of only logging or firing a
// one-way Discord embed, the trigger site calls a `raise*` builder here, which
// routes through the single authoritative store (as.attention) with a stable
// idempotency_key, a declared default_action/expires_at, and options bound to
// registry verbs. Re-detecting the same condition UPDATES the open request in
// place (dedup), so calling from a per-cycle loop never spawns duplicates.
//
// All raise paths are fail-open and nil-safe: an attention-write failure or an
// unconfigured store must never break the scheduler.

import (
	"context"
	"fmt"
	"log"
	"strings"
	"time"

	"github.com/nightgauge/nightgauge/internal/attention"
	pmstages "github.com/nightgauge/nightgauge/internal/orchestrator/stages"
	"github.com/nightgauge/nightgauge/internal/trace"
)

// isBranchProtectionPunt reports whether a pr-merge punt reason is a
// branch-protection / required-check / review block that no LLM retry can clear
// — the class that warrants a human-needed Action Center card (ADR 015 §F #6).
// Reasons are prefixed (e.g. "review-not-approved: …"), so match by prefix.
func isBranchProtectionPunt(reason string) bool {
	for _, p := range []string{
		pmstages.ReasonReviewMissing,
		pmstages.ReasonFailedChecks,
		pmstages.ReasonDirtyState,
		pmstages.ReasonNotMergeable,
	} {
		if strings.HasPrefix(reason, p) {
			return true
		}
	}
	return false
}

// nowRFC3339Nano returns an RFC3339Nano UTC timestamp `d` from now.
func expiryFromNow(d time.Duration) string {
	return time.Now().UTC().Add(d).Format(time.RFC3339Nano)
}

// Attention returns the DecisionRequest store (nil when unconfigured).
func (as *AutonomousScheduler) Attention() *attention.Store {
	if as == nil {
		return nil
	}
	return as.attention
}

// SetAttention injects the shared store into the inner Scheduler so run-scoped
// producers raise through the same single writer as the fleet-scoped ones.
func (s *Scheduler) SetAttention(store *attention.Store) {
	if s == nil {
		return
	}
	s.attention = store
}

// raiseAttention is the nil-safe, fail-open producer entry point on the
// autonomous scheduler. It fills a fresh id and forwards to the store.
func (as *AutonomousScheduler) raiseAttention(req attention.DecisionRequest) {
	if as == nil {
		return
	}
	raiseThrough(as.attention, req)
}

// raiseAttention is the run-scoped producer entry point on the Scheduler.
func (s *Scheduler) raiseAttention(req attention.DecisionRequest) {
	if s == nil {
		return
	}
	raiseThrough(s.attention, req)
}

func raiseThrough(store *attention.Store, req attention.DecisionRequest) {
	if store == nil {
		return
	}
	if req.ID == "" {
		id, err := attention.NewID()
		if err != nil {
			log.Printf("attention: id generation failed (fail-open): %v", err)
			return
		}
		req.ID = id
	}
	if _, err := store.Raise(req); err != nil {
		log.Printf("attention: raise %q failed (fail-open): %v", req.IdempotencyKey, err)
	}
}

// auditAttentionTransition is the store TransitionListener that writes the
// ADR-013 decision_request trace leg for terminal transitions of a run-scoped
// request (ADR 015 §H). Fleet-scoped requests (no run_id) skip the run-trace
// leg and are audited via the journal alone.
func (as *AutonomousScheduler) auditAttentionTransition(entry attention.JournalEntry, req *attention.DecisionRequest) {
	if req == nil || req.Context.RunID == "" {
		return
	}
	if entry.Action != attention.ActionResolved && entry.Action != attention.ActionExpired {
		return
	}
	root := as.workspaceRoot
	if as.scheduler != nil {
		if r := as.scheduler.runRoot(req.Context.Repo); r != "" {
			root = r
		}
	}
	w := trace.NewWriter(root, req.Context.RunID, req.Context.Repo, req.Context.Issue)
	payload := trace.DecisionRequestPayload{
		ID:         req.ID,
		Kind:       string(req.Kind),
		Producer:   req.Producer,
		Transition: entry.Action,
	}
	if req.Lifecycle.Resolved != nil {
		payload.OptionID = req.Lifecycle.Resolved.OptionID
		payload.Actor = req.Lifecycle.Resolved.Actor
		payload.Note = req.Lifecycle.Resolved.Note
	}
	if req.Lifecycle.Expired != nil {
		payload.Applied = req.Lifecycle.Expired.Applied
	}
	if req.Context.TraceRef != nil {
		payload.OriginatingSeq = req.Context.TraceRef.Seq
	}
	w.Emit(trace.KindDecisionRequest, req.Context.Stage, payload)
}

// noopOption is the explicit "do nothing but resolve" choice (leave / keep-paused
// / wait / halt in the ADR producer table). It binds the registered no-op verb.
func noopOption(id, label string) attention.Option {
	return attention.Option{ID: id, Label: label, Verb: attention.VerbNoop, Style: attention.StyleDefault}
}

// sweepAttentionExpired transitions every open request past its expires_at to
// expired, applying default_action (ADR 015 §C). Piggybacks the scheduler's
// periodic scan alongside stuck-epic detection and the survival sweep. Every E1
// producer's default_action is a no-op / expire_noop, so a NoopExecutor is the
// correct sweep executor — the sweep marks expiry without mutating the fleet.
func (as *AutonomousScheduler) sweepAttentionExpired(ctx context.Context) {
	if as == nil || as.attention == nil {
		return
	}
	if n, err := as.attention.SweepExpired(ctx, attention.NoopExecutor{}); err != nil {
		log.Printf("attention: expiry sweep failed (fail-open): %v", err)
	} else if n > 0 {
		log.Printf("attention: expired %d stale DecisionRequest(s)", n)
	}
}

// --- Producer 1: work exhaustion (fleet-scoped) ------------------------------

// raiseWorkExhaustion surfaces the fleet-idle dead-end: nothing dispatchable
// (remaining==0 && running==0). Fleet-scoped (no run/repo). promotable is the
// count of Backlog candidates the operator could promote.
func (as *AutonomousScheduler) raiseWorkExhaustion(promotable int) {
	as.raiseAttention(attention.DecisionRequest{
		IdempotencyKey: "work-exhaustion:fleet",
		Kind:           attention.KindChoose,
		Severity:       attention.SeverityFYI,
		Title:          fmt.Sprintf("Fleet idle — %d Backlog item(s) promotable", promotable),
		Body:           "No dispatchable work remains. Re-scan for newly-ready work, or leave the fleet idle.",
		Producer:       "work-exhaustion",
		Context:        attention.Context{},
		Options: []attention.Option{
			{ID: "rescan", Label: "Re-scan for work", Verb: attention.VerbAutonomousRescan, Style: attention.StylePrimary},
			noopOption("leave", "Leave idle"),
		},
		DefaultAction: "leave",
		ExpiresAt:     expiryFromNow(24 * time.Hour),
		Steer:         &attention.Steer{Enabled: true, Hint: "Add context for the chosen option"},
	})
}

// --- Producer 2: owner-action handoff (per-issue) ----------------------------

// raiseOwnerActionHandoff surfaces a human-only (owner-action) issue the fleet
// skipped silently. handoff kind: it needs a human, so the default is
// expire_noop (no auto-mutation).
func (as *AutonomousScheduler) raiseOwnerActionHandoff(repo string, issue int, title, label string) {
	owner, name := splitRepo(repo)
	as.raiseAttention(attention.DecisionRequest{
		IdempotencyKey: fmt.Sprintf("owner-action-handoff:%s#%d", repo, issue),
		Kind:           attention.KindHandoff,
		Severity:       attention.SeverityBlockingRun,
		Title:          fmt.Sprintf("Owner-action needed: %s (#%d)", title, issue),
		Body:           fmt.Sprintf("Issue #%d carries the human-only label %q; no pipeline retry can clear it. Complete the checklist, then mark done to requeue dependents.", issue, label),
		Producer:       "owner-action-handoff",
		Context:        attention.Context{Repo: repo, Issue: issue, Blocker: "human-only label: " + label},
		Options: []attention.Option{
			{ID: "mark-done", Label: "Mark done & requeue dependents", Verb: attention.VerbAutonomousComplete,
				Args: map[string]any{"owner": owner, "repo": name, "issueNumber": issue, "then": "issue.close"}, Style: attention.StylePrimary},
			noopOption("snooze", "Snooze"),
		},
		DefaultAction: attention.ExpireNoop,
		ExpiresAt:     expiryFromNow(7 * 24 * time.Hour),
		Steer:         &attention.Steer{Enabled: true, Hint: "Optional note — recorded in the decision audit and visible to dependent work"},
	})
}

// --- Producer 3: cascade pause (fleet-scoped) --------------------------------

// raiseCascadePause surfaces the cascading-failure circuit-breaker trip. The
// fleet is stopped → blocking_fleet.
func (as *AutonomousScheduler) raiseCascadePause(repo string, issue int, reason string) {
	as.raiseAttention(attention.DecisionRequest{
		IdempotencyKey: "cascade-pause:fleet",
		Kind:           attention.KindResume,
		Severity:       attention.SeverityBlockingFleet,
		Title:          "Fleet stopped — cascade circuit breaker tripped",
		Body:           "Multiple pipeline failures in a short window tripped the safety breaker. Resume the fleet, or keep it paused for manual triage.",
		Producer:       "cascade-breaker",
		Context:        attention.Context{Repo: repo, Issue: issue, Blocker: reason},
		Options: []attention.Option{
			{ID: "resume", Label: "Resume fleet", Verb: attention.VerbAutonomousResume, Style: attention.StylePrimary},
			noopOption("keep-paused", "Keep paused"),
		},
		DefaultAction: "keep-paused",
		ExpiresAt:     expiryFromNow(30 * 24 * time.Hour), // effectively "none" — a bounded safety net (ADR §C)
		Steer:         &attention.Steer{Enabled: true, Hint: "Anything the resumed run should know"},
	})
}

// --- Producer 5: blockedBy deferral (per-issue) ------------------------------

// raiseBlockedByDeferral surfaces a run deferred because a blockedBy dependency
// is still open. choose kind.
func (as *AutonomousScheduler) raiseBlockedByDeferral(repo string, issue int, title, detail string) {
	owner, name := splitRepo(repo)
	as.raiseAttention(attention.DecisionRequest{
		IdempotencyKey: fmt.Sprintf("blocked-by-deferral:%s#%d", repo, issue),
		Kind:           attention.KindChoose,
		Severity:       attention.SeverityBlockingRun,
		Title:          fmt.Sprintf("Deferred: #%d blocked by an open dependency", issue),
		Body:           fmt.Sprintf("%s\nRemove a stale blockedBy edge, requeue, or leave deferred.", detail),
		Producer:       "blocked-by-deferral",
		Context:        attention.Context{Repo: repo, Issue: issue, Blocker: detail},
		Options: []attention.Option{
			{ID: "requeue", Label: "Requeue now", Verb: attention.VerbQueueAdd,
				Args: map[string]any{"owner": owner, "repo": name, "issueNumber": issue, "title": title}, Style: attention.StyleDefault},
			noopOption("leave", "Leave deferred"),
		},
		DefaultAction: "leave",
		ExpiresAt:     expiryFromNow(72 * time.Hour),
		Steer:         &attention.Steer{Enabled: true, Hint: "Note why to requeue now, or why it should stay deferred"},
	})
}

// --- Producer 8: watchdog / stuck-epic (per-epic) ----------------------------

// raiseStuckEpic surfaces an epic the watchdog flagged as stalled (open with
// open sub-issues, zero eligible work, no running pipeline).
func (as *AutonomousScheduler) raiseStuckEpic(repo string, epic int, title, summary string) {
	owner, name := splitRepo(repo)
	as.raiseAttention(attention.DecisionRequest{
		IdempotencyKey: fmt.Sprintf("stuck-epic:%s#%d", repo, epic),
		Kind:           attention.KindChoose,
		Severity:       attention.SeverityFYI,
		Title:          fmt.Sprintf("Epic stalled: %s (#%d)", title, epic),
		Body:           summary,
		Producer:       "watchdog-stuck-epic",
		Context:        attention.Context{Repo: repo, Issue: epic, Blocker: summary},
		Options: []attention.Option{
			{ID: "escalate", Label: "Escalate model & retry", Verb: attention.VerbRunRetryWithEscalation,
				Args: map[string]any{"issueNumber": epic, "tier": "opus"}, Style: attention.StylePrimary},
			{ID: "requeue", Label: "Requeue epic", Verb: attention.VerbQueueAdd,
				Args: map[string]any{"owner": owner, "repo": name, "issueNumber": epic, "title": title}, Style: attention.StyleDefault},
			noopOption("wait", "Wait"),
		},
		DefaultAction: "wait",
		ExpiresAt:     expiryFromNow(30 * time.Minute),
		Steer:         &attention.Steer{Enabled: true, Hint: "Tell the pipeline what to do differently on retry"},
	})
}

// --- Producer 4: budget ceiling hit (run-scoped, Scheduler) ------------------

// raiseBudgetCeilingHit surfaces a run terminated by the pipeline budget
// ceiling. approve kind. raise-to option carries the proposed higher ceiling.
func (s *Scheduler) raiseBudgetCeilingHit(repo string, issue int, runID string, costUSD, proposedCeilingUSD float64) {
	owner, name := splitRepo(repo)
	s.raiseAttention(attention.DecisionRequest{
		IdempotencyKey: fmt.Sprintf("budget-ceiling:%s#%d", repo, issue),
		Kind:           attention.KindApprove,
		Severity:       attention.SeverityBlockingRun,
		Title:          fmt.Sprintf("Budget ceiling hit — $%.2f spent on #%d", costUSD, issue),
		Body:           fmt.Sprintf("Run #%d hit the pipeline budget ceiling. Raise the ceiling to $%.2f and retry, or halt.", issue, proposedCeilingUSD),
		Producer:       "budget-ceiling",
		Context:        attention.Context{Repo: repo, Issue: issue, RunID: runID, CostSoFarUSD: costUSD, Blocker: "pipeline budget ceiling exceeded", TraceRef: runTraceRef(runID)},
		Options: []attention.Option{
			{ID: "raise", Label: fmt.Sprintf("Raise to $%.2f & retry", proposedCeilingUSD), Verb: attention.VerbBudgetRaiseCeiling,
				Args: map[string]any{"owner": owner, "repo": name, "issueNumber": issue, "ceilingUsd": proposedCeilingUSD, "title": ""}, Style: attention.StylePrimary},
			noopOption("halt", "Halt run"),
		},
		DefaultAction: "halt",
		ExpiresAt:     expiryFromNow(1 * time.Hour),
		Steer:         &attention.Steer{Enabled: true, Hint: "Add context for raising the ceiling, or for halting"},
	})
}

// --- Producer 6: branch-protection block (run-scoped, Scheduler) -------------

// raiseBranchProtectionBlock surfaces a pr-merge punt caused by branch
// protection / a required check. unblock kind: it needs a human to fix.
func (s *Scheduler) raiseBranchProtectionBlock(repo string, issue, prNumber int, runID, reason string) {
	s.raiseAttention(attention.DecisionRequest{
		IdempotencyKey: fmt.Sprintf("branch-protection:%s#%d", repo, issue),
		Kind:           attention.KindUnblock,
		Severity:       attention.SeverityBlockingRun,
		Title:          fmt.Sprintf("PR #%d blocked by branch protection", prNumber),
		Body:           fmt.Sprintf("pr-merge could not merge PR #%d: %s. Fix the failing check / approval on GitHub, then retry.", prNumber, reason),
		Producer:       "branch-protection",
		Context:        attention.Context{Repo: repo, Issue: issue, RunID: runID, Stage: "pr-merge", Blocker: reason, TraceRef: runTraceRef(runID)},
		Options: []attention.Option{
			{ID: "retry-after-fix", Label: "Retry after fix", Verb: attention.VerbAutonomousClearIssueFailures,
				Args: map[string]any{"key": fmt.Sprintf("%s#%d", repo, issue), "then": "autonomous.rescan"}, Style: attention.StylePrimary},
			noopOption("wait", "Wait — human fixing"),
		},
		DefaultAction: attention.ExpireNoop,
		ExpiresAt:     expiryFromNow(48 * time.Hour),
		Steer:         &attention.Steer{Enabled: true, Hint: "Tell the pipeline what to do differently on retry"},
	})
}

// --- Producer 7: definitive auth failure (run-scoped, Scheduler) -------------

// raiseAuthFailure surfaces a fail-closed identity/permission block. Needs the
// operator to re-authenticate. provide_input kind.
func (s *Scheduler) raiseAuthFailure(repo string, issue int, runID, reason string) {
	s.raiseAttention(attention.DecisionRequest{
		IdempotencyKey: fmt.Sprintf("auth-failure:%s#%d", repo, issue),
		Kind:           attention.KindProvideInput,
		Severity:       attention.SeverityBlockingRun,
		Title:          fmt.Sprintf("Authentication failed for %s", repo),
		Body:           fmt.Sprintf("A definitive auth/permission failure blocked #%d: %s. Re-authenticate the configured identity, then retry.", issue, reason),
		Producer:       "auth-preflight",
		Context:        attention.Context{Repo: repo, Issue: issue, RunID: runID, Blocker: reason, TraceRef: runTraceRef(runID)},
		Options: []attention.Option{
			{ID: "login-and-retry", Label: "Re-authenticated — retry", Verb: attention.VerbAutonomousClearIssueFailures,
				Args: map[string]any{"key": fmt.Sprintf("%s#%d", repo, issue), "then": "autonomous.rescan"}, Style: attention.StylePrimary},
			noopOption("halt", "Halt"),
		},
		DefaultAction: "halt",
		ExpiresAt:     expiryFromNow(12 * time.Hour),
		Steer:         &attention.Steer{Enabled: true, Hint: "Add detail on the auth fix before the retry"},
	})
}

// runTraceRef builds a best-effort ADR-013 trace back-reference for a run-scoped
// request. Producer is the Go binary; seq is unknown at raise time (0) — the
// resolution audit leg re-emits into the same run trace, joined by run_id.
func runTraceRef(runID string) *attention.TraceRef {
	if runID == "" {
		return nil
	}
	return &attention.TraceRef{RunID: runID, Producer: trace.ProducerGo}
}

// splitRepo splits "owner/name" into its parts; returns ("", repo) when there
// is no slash.
func splitRepo(repo string) (owner, name string) {
	for i := 0; i < len(repo); i++ {
		if repo[i] == '/' {
			return repo[:i], repo[i+1:]
		}
	}
	return "", repo
}
