package orchestrator

// Action Center producer wiring (ADR 015 §F). Each producer replaces a
// dead-end that is today silent or one-way: instead of only logging or firing a
// one-way Discord embed, the trigger site calls a `raise*` builder here, which
// routes through the single authoritative store (as.attention) with a stable
// idempotency_key, a declared default_action/expires_at, and options bound to
// registry verbs. Re-detecting the same condition UPDATES the record in place
// (dedup), so calling from a per-cycle loop never spawns duplicates.
//
// Three of these producers are STANDING rather than event-shaped (#108): work
// exhaustion, the owner-action handoff, and the stuck-epic watchdog each
// describe a condition the scheduler re-evaluates on every cycle, not a
// transition it observed once. They declare Standing + a Fingerprint so a
// re-observation refreshes silently, and their trigger sites call
// autoResolveAttention with the complete set they just saw, so a condition that
// stopped being true retracts its own card instead of waiting out a TTL.
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
	"github.com/nightgauge/nightgauge/internal/deliverable"
	pmstages "github.com/nightgauge/nightgauge/internal/orchestrator/stages"
	"github.com/nightgauge/nightgauge/internal/trace"
)

// Producer ids for the three STANDING run-loop producers (#108). A standing
// producer is re-evaluated on every scheduler cycle, so its name is needed
// twice — once to stamp the card, once to retract the cards whose condition
// stopped being observed — and a literal in two places is a name waiting to
// drift.
const (
	producerWorkExhaustion      = "work-exhaustion"
	producerOwnerActionHandoff  = "owner-action-handoff"
	producerStuckEpic           = "watchdog-stuck-epic"
	producerArchitectureApprove = "architecture-approval"
	producerTerminalFailure     = "terminal-failure"
)

// keyWorkExhaustion is the fleet-idle condition's sticky identity. There is one
// fleet, so there is one key.
const keyWorkExhaustion = producerWorkExhaustion + ":fleet"

// keyOwnerActionHandoff / keyStuckEpic build the sticky (producer,
// idempotency_key) identity for a per-issue standing condition.
//
// The stuck-epic key prefix is `stuck-epic`, not the producer name — it shipped
// that way and an idempotency_key is durable identity, so "correcting" it would
// orphan every live card and re-raise each one under a new id.
func keyOwnerActionHandoff(repo string, issue int) string {
	return fmt.Sprintf("%s:%s#%d", producerOwnerActionHandoff, repo, issue)
}

func keyStuckEpic(repo string, epic int) string {
	return fmt.Sprintf("stuck-epic:%s#%d", repo, epic)
}

func keyArchitectureApproval(repo string, issue int) string {
	return fmt.Sprintf("%s:%s#%d", producerArchitectureApprove, repo, issue)
}

// keyTerminalFailure builds the sticky (producer, idempotency_key) identity
// for the per-issue terminal-failure halt card (#148).
func keyTerminalFailure(repo string, issue int) string {
	return fmt.Sprintf("%s:%s#%d", producerTerminalFailure, repo, issue)
}

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

// standingExpiry is the expiry every STANDING producer declares (#108).
//
// For an event, expires_at answers "how long is this worth showing?" — it is
// the only thing that ever removes the card. For a standing condition it
// answers a different question: the card is removed when the condition clears,
// so expiry is purely the safety net for a producer that stopped being
// evaluated at all. The short event TTLs fired routinely on a perfectly healthy
// producer — the stuck-epic watchdog only runs on idle cycles, so its 30-minute
// window lapsed between observations — which is how one standing condition
// became a card per window. attention.StandingExpiry is the declared window for
// exactly this role.
func standingExpiry() string { return expiryFromNow(attention.StandingExpiry) }

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

// autoResolveAttention retracts every open standing card from producer whose
// idempotency_key is absent from observed — the condition stopped being true.
// Callers must have just evaluated the producer's whole condition set
// successfully; an empty observed slice asserts that nothing holds, and is
// never a way to say "I could not look". Fail-open like every raise path.
func (as *AutonomousScheduler) autoResolveAttention(producer string, observed []string) {
	if as == nil || as.attention == nil {
		return
	}
	n, err := as.attention.AutoResolveUnobserved(producer, observed)
	if err != nil {
		log.Printf("attention: auto-resolve %q failed (fail-open): %v", producer, err)
		return
	}
	if n > 0 {
		log.Printf("attention: retracted %d %q card(s) — condition no longer observed", n, producer)
	}
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
	switch entry.Action {
	case attention.ActionResolved, attention.ActionExpired, attention.ActionAutoResolved:
	default:
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
	if req.Lifecycle.AutoResolved != nil {
		// A retraction has no actor and no option: the note carries why the
		// system withdrew the card, so the audit can tell it apart from a
		// decision someone made.
		payload.Note = req.Lifecycle.AutoResolved.Reason
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
//
// Standing (#108): "the fleet has nothing to do" is a condition, not an event —
// every idle cycle re-observes it. retractWorkExhaustion clears the card the
// moment work reappears.
func (as *AutonomousScheduler) raiseWorkExhaustion(promotable int) {
	as.raiseAttention(attention.DecisionRequest{
		IdempotencyKey: keyWorkExhaustion,
		Kind:           attention.KindChoose,
		Severity:       attention.SeverityFYI,
		Title:          fmt.Sprintf("Fleet idle — %d Backlog item(s) promotable", promotable),
		Body:           "No dispatchable work remains. Re-scan for newly-ready work, or leave the fleet idle.",
		Producer:       producerWorkExhaustion,
		Context:        attention.Context{},
		Standing:       true,
		// The condition is binary: nothing is dispatchable. The promotable
		// tally belongs in the title, not the fingerprint — it moves as backoff
		// windows elapse, and a fingerprint that moves on its own re-alerts
		// every cycle (docs/ATTENTION_PRODUCERS.md invariant 2). The card
		// clears when the fleet has work again, which is the only transition
		// there is.
		Fingerprint: "fleet:idle",
		Options: []attention.Option{
			{ID: "rescan", Label: "Re-scan for work", Verb: attention.VerbAutonomousRescan, Style: attention.StylePrimary},
			noopOption("leave", "Leave idle"),
		},
		DefaultAction: "leave",
		ExpiresAt:     standingExpiry(),
		Steer:         &attention.Steer{Enabled: true, Hint: "Add context for the chosen option"},
	})
}

// retractWorkExhaustion auto-resolves the fleet-idle card once anything is
// dispatchable again. Called from the same cycle branch that would have raised
// it, so "not idle" is always a fresh observation.
func (as *AutonomousScheduler) retractWorkExhaustion() {
	as.autoResolveAttention(producerWorkExhaustion, nil)
}

// --- Producer 2: owner-action handoff (per-issue) ----------------------------

// raiseOwnerActionHandoff surfaces a human-only (owner-action) issue the fleet
// skipped silently. handoff kind: it needs a human, so the default is
// expire_noop (no auto-mutation).
//
// Standing (#108): the issue carries the label until someone removes it or
// closes the issue, and every prioritize pass re-observes that. The scan that
// stops seeing it retracts the card.
func (as *AutonomousScheduler) raiseOwnerActionHandoff(repo string, issue int, title, label string) {
	owner, name := splitRepo(repo)
	as.raiseAttention(attention.DecisionRequest{
		IdempotencyKey: keyOwnerActionHandoff(repo, issue),
		Kind:           attention.KindHandoff,
		Severity:       attention.SeverityBlockingRun,
		Title:          fmt.Sprintf("Owner-action needed: %s (#%d)", title, issue),
		Body:           fmt.Sprintf("Issue #%d carries the human-only label %q; no pipeline retry can clear it. Complete the checklist, then mark done to requeue dependents.", issue, label),
		Producer:       producerOwnerActionHandoff,
		Context:        attention.Context{Repo: repo, Issue: issue, Blocker: "human-only label: " + label},
		Standing:       true,
		// WHICH human-only label is holding the issue. A retitled issue is the
		// same handoff and refreshes silently; a different exclude label is a
		// different ask and re-alerts.
		Fingerprint: "label:" + strings.ToLower(label),
		Options: []attention.Option{
			{ID: "mark-done", Label: "Mark done & requeue dependents", Verb: attention.VerbAutonomousComplete,
				Args: map[string]any{"owner": owner, "repo": name, "issueNumber": issue, "then": "issue.close"}, Style: attention.StylePrimary},
			noopOption("snooze", "Snooze"),
		},
		DefaultAction: attention.ExpireNoop,
		ExpiresAt:     standingExpiry(),
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

// --- Producer 7b: architecture-approval gate (per-issue) ---------------------

// raiseArchitectureApproval surfaces an issue the architecture-approval gate
// (#4098/#4222) halted before feature-dev because a high-impact decision needs
// human sign-off.
//
// Standing: the condition holds until a human approves — the gate re-raises it
// on every dispatch. The idempotency key is per-issue and carries no timestamp,
// cost, or attempt counter, so repeated halts collapse onto one card instead of
// stacking. That is what makes it safe to raise from the failure path.
//
// Before #180 this had no card at all: the halt was recorded as a crash and the
// only human-visible affordance was a transient VSCode toast, which is gone the
// moment it is dismissed or missed. A gate whose entire purpose is a human
// decision has to survive until the human makes it.
func (as *AutonomousScheduler) raiseArchitectureApproval(repo string, issue int, title, detail string) {
	owner, name := splitRepo(repo)
	as.raiseAttention(attention.DecisionRequest{
		IdempotencyKey: keyArchitectureApproval(repo, issue),
		Kind:           attention.KindApprove,
		Severity:       attention.SeverityBlockingRun,
		Title:          fmt.Sprintf("Architecture approval required — #%d", issue),
		Body: fmt.Sprintf("%s\n\nApproving applies the architecture-approval label and requeues the issue; "+
			"the next run passes the gate and proceeds to feature-dev. "+
			"Leaving it parks the issue in In review — nothing re-dispatches it and the rest of the queue keeps flowing.",
			detail),
		Producer: producerArchitectureApprove,
		Standing: true,
		// The condition is binary — the issue is either approved or it is not —
		// so the fingerprint is constant. That is deliberate: it alerts once and
		// stays muted until a human resolves it, instead of re-alerting on every
		// dispatch that re-hits the gate.
		Fingerprint: "awaiting:architecture-approval",
		Context:     attention.Context{Repo: repo, Issue: issue},
		Options: []attention.Option{
			{ID: "approve", Label: "Approve & re-queue", Verb: attention.VerbIssueApproveArchitecture,
				Args:  map[string]any{"owner": owner, "repo": name, "issueNumber": issue, "title": title},
				Style: attention.StyleDefault},
			noopOption("leave", "Leave for review"),
		},
		DefaultAction: "leave",
		ExpiresAt:     standingExpiry(),
		Steer:         &attention.Steer{Enabled: true, Hint: "Note why this architecture is approved, or what needs to change first"},
	})
}

// retractArchitectureApproval clears one issue's approval card once the gate is
// demonstrably satisfied — the issue ran to completion, which it cannot do while
// the gate is still blocking it.
//
// This covers approval granted OUT of band: a human who adds the label with gh
// (or writes the approval file) never touches the card, and without this it
// would sit in the inbox indefinitely — the exact "notification that never
// clears" failure this producer is supposed to avoid, not reproduce.
//
// AutoResolveUnobserved retracts whatever is NOT in the observed set, so the
// observed set is every other open card of this producer. Listing is a local
// store read, not a forge call, so this costs no API budget.
func (as *AutonomousScheduler) retractArchitectureApproval(repo string, issue int) {
	if as == nil || as.attention == nil {
		return
	}
	target := keyArchitectureApproval(repo, issue)
	reqs, err := as.attention.List(attention.ListFilter{})
	if err != nil {
		log.Printf("attention: retract %q list failed (fail-open): %v", producerArchitectureApprove, err)
		return
	}
	observed := make([]string, 0, len(reqs))
	for _, r := range reqs {
		if r.Producer != producerArchitectureApprove || r.IdempotencyKey == target {
			continue
		}
		observed = append(observed, r.IdempotencyKey)
	}
	as.autoResolveAttention(producerArchitectureApprove, observed)
}

// --- Producer 9: terminal failure halt (per-issue, fleet-blocking) ----------

// RaiseTerminalFailure surfaces the terminal stage failure that caused
// haltQueueOnSlotFailure to pause the whole fleet (#148).
//
// Before this, the pause recorded only PauseReason/PauseTriggeredBy on
// AutonomousState — no card at all. One idle-scan cycle later
// raiseWorkExhaustion fired a misleading "Fleet idle — N promotable" over the
// top of it: a halted queue satisfies remaining==0 && running==0 exactly like
// an empty one, so the operator's only card actively recommended the wrong
// action.
//
// blocking_fleet, not blocking_run: haltQueueOnSlotFailure stops the whole
// queue, not just this one issue. Exported so the IPC autonomous.pause
// handler (internal/ipc/server.go) can raise it right after Pause() succeeds,
// carrying whatever structured fields ConcurrentPipelineManager.
// haltQueueOnSlotFailure had in scope at the call site.
//
// stage/terminalKind/costUSD degrade gracefully when empty/zero — the
// terminating-stage cost plumbing (#146) has not landed on main yet, so the
// card must not block on it.
func (as *AutonomousScheduler) RaiseTerminalFailure(repo string, issue int, stage, terminalKind string, costUSD float64) {
	if as == nil {
		return
	}
	if stage == "" {
		stage = "unknown"
	}
	if terminalKind == "" {
		terminalKind = "unknown"
	}

	key := fmt.Sprintf("%s#%d", repo, issue)
	as.mu.Lock()
	lifetimeFails := 0
	if as.state != nil && as.state.LifetimeIssueFailures != nil {
		lifetimeFails = as.state.LifetimeIssueFailures[key]
	}
	as.mu.Unlock()
	attemptsRemaining := MaxLifetimeFailuresPerIssue - lifetimeFails
	if attemptsRemaining < 0 {
		attemptsRemaining = 0
	}

	var b strings.Builder
	b.WriteString(fmt.Sprintf("Issue #%d failed terminally at %s (%s) and halted the whole fleet — nothing else dispatches until this is resolved.\n\n", issue, stage, terminalKind))
	if terminalKind == TerminalKindAbandonedCommit {
		b.WriteString("A recovery attempt already found committed, unmerged work on this branch (clean tree, ahead of base) but could not resume automatically. The commit is still there — retry will resume at pr-create rather than re-deriving the work from scratch.\n\n")
	}
	if costUSD > 0 {
		b.WriteString(fmt.Sprintf("Cost so far: $%.2f.\n\n", costUSD))
	}
	if attemptsRemaining > 0 {
		b.WriteString(fmt.Sprintf("%d attempt(s) remain before the lifetime failure cap (%d) permanently blocks this issue.\n\n", attemptsRemaining, MaxLifetimeFailuresPerIssue))
	} else {
		b.WriteString(fmt.Sprintf("The lifetime failure cap (%d) has been reached — no further automatic retries will be accepted for this issue.\n\n", MaxLifetimeFailuresPerIssue))
	}
	b.WriteString("Retry clears the failure cooldown and re-runs the whole pipeline from issue-pickup — it re-derives work already committed to the branch, it does not resume mid-pipeline. Park leaves the fleet paused for manual triage.")

	options := []attention.Option{
		{ID: "retry", Label: "Retry", Verb: attention.VerbAutonomousClearIssueFailures,
			Args: map[string]any{"key": key, "then": "autonomous.resume"}, Style: attention.StylePrimary},
	}
	if attemptsRemaining > 0 {
		options = append(options, attention.Option{
			ID: "retry-escalate", Label: "Retry with escalation", Verb: attention.VerbRunRetryWithEscalation,
			Args: map[string]any{"key": key, "issueNumber": issue, "then": "autonomous.resume"}, Style: attention.StyleDefault,
		})
	}
	options = append(options, noopOption("park", "Park — leave paused for manual triage"))

	as.raiseAttention(attention.DecisionRequest{
		IdempotencyKey: keyTerminalFailure(repo, issue),
		Kind:           attention.KindUnblock,
		Severity:       attention.SeverityBlockingFleet,
		Title:          fmt.Sprintf("Fleet halted — #%d failed at %s", issue, stage),
		Body:           b.String(),
		Producer:       producerTerminalFailure,
		Context:        attention.Context{Repo: repo, Issue: issue, Stage: stage, CostSoFarUSD: costUSD, Blocker: terminalKind},
		Standing:       true,
		// Material state: which issue, which stage, which terminal kind. No
		// cost and no timestamp — cost moves as the run's own estimate
		// refines and a moving fingerprint would re-alert every cycle
		// (docs/ATTENTION_PRODUCERS.md invariant 2).
		Fingerprint:   fmt.Sprintf("issue:%s#%d stage:%s kind:%s", repo, issue, stage, terminalKind),
		Options:       options,
		DefaultAction: attention.ExpireNoop,
		ExpiresAt:     standingExpiry(),
		Steer:         &attention.Steer{Enabled: true, Hint: "Note what to fix before retrying"},
	})
}

// reconcileTerminalFailureCards re-evaluates the terminal-failure halt
// condition on the idle-scan cycle (#148). RaiseTerminalFailure fires once,
// from the IPC pause handler, with fields this cycle has no way to
// re-derive — so instead of re-raising every cycle, this keeps every open
// terminal-failure card alive for as long as the fleet is still paused for
// that reason, and retracts all of them the instant it is not (Resume()
// cleared the pause, or a human resumed some other way). Mirrors the
// standing-producer auto-resolve contract (docs/ATTENTION_PRODUCERS.md)
// without needing AutonomousState to carry the raise-time fields again.
//
// Fail-open: a list error leaves existing cards untouched rather than
// retracting them — invariant 1, "I could not look" is never "nothing is
// wrong".
func (as *AutonomousScheduler) reconcileTerminalFailureCards() {
	if as == nil || as.attention == nil {
		return
	}
	as.mu.Lock()
	stillHalted := as.state != nil && as.state.Status == "paused" && as.state.PauseTriggeredBy == "haltQueueOnSlotFailure"
	as.mu.Unlock()

	if !stillHalted {
		as.autoResolveAttention(producerTerminalFailure, nil)
		return
	}
	reqs, err := as.attention.List(attention.ListFilter{})
	if err != nil {
		log.Printf("attention: list %q failed (fail-open): %v", producerTerminalFailure, err)
		return
	}
	observed := make([]string, 0, len(reqs))
	for _, r := range reqs {
		if r.Producer == producerTerminalFailure {
			observed = append(observed, r.IdempotencyKey)
		}
	}
	as.autoResolveAttention(producerTerminalFailure, observed)
}

// --- Producer 8: watchdog / stuck-epic (per-epic) ----------------------------

// raiseStuckEpic surfaces an epic the watchdog flagged as stalled (open with
// open sub-issues, zero eligible work, no running pipeline).
//
// Standing (#108): a stalled epic stays stalled until someone unblocks it, and
// the watchdog re-detects it on every idle cycle. summary is StuckEpic.Summary()
// — the blocker set sorted by sub-issue number, each with its board status or
// taxonomy failure kind — so it is material state and doubles as the
// fingerprint. It carries no timestamp, elapsed duration, or counter, which is
// what makes that safe; a different set of blockers is a genuine change and
// re-alerts.
func (as *AutonomousScheduler) raiseStuckEpic(repo string, epic int, title, summary string) {
	owner, name := splitRepo(repo)
	as.raiseAttention(attention.DecisionRequest{
		IdempotencyKey: keyStuckEpic(repo, epic),
		Kind:           attention.KindChoose,
		Severity:       attention.SeverityFYI,
		Title:          fmt.Sprintf("Epic stalled: %s (#%d)", title, epic),
		Body:           summary,
		Producer:       producerStuckEpic,
		Context:        attention.Context{Repo: repo, Issue: epic, Blocker: summary},
		Standing:       true,
		Fingerprint:    "stall:" + summary,
		Options: []attention.Option{
			{ID: "escalate", Label: "Escalate model & retry", Verb: attention.VerbRunRetryWithEscalation,
				Args: map[string]any{"issueNumber": epic, "tier": "opus"}, Style: attention.StylePrimary},
			{ID: "requeue", Label: "Requeue epic", Verb: attention.VerbQueueAdd,
				Args: map[string]any{"owner": owner, "repo": name, "issueNumber": epic, "title": title}, Style: attention.StyleDefault},
			noopOption("wait", "Wait"),
		},
		DefaultAction: "wait",
		ExpiresAt:     standingExpiry(),
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
		// PR is what the sweep's human-gate producer dedupes against: the same
		// blocked PR seen from a run and from a repo scan is one fact.
		Context: attention.Context{Repo: repo, Issue: issue, PR: prNumber, RunID: runID, Stage: "pr-merge", Blocker: reason, TraceRef: runTraceRef(runID)},
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

// --- Producer 8: unexercised deliverable (run-scoped, Scheduler) -------------

// raiseUnverifiedDeliverable surfaces a run that built a test suite it never
// executed (#152).
//
// FYI severity, deliberately: nothing is blocked and the PR is still created.
// The failure this closes was never "the pipeline let work through" — it was
// that a run could merge with its own deliverable unexercised and produce no
// signal anywhere an operator looks. The stage said so honestly, in prose,
// inside a JSON artifact nobody reads unless already suspicious.
//
// The card carries the idle tier's own stated reason rather than a synthesised
// remediation command. The pipeline does not know how to run a suite it could
// not wire up, and inventing a command it never verified would be a second
// confident-sounding claim about something that did not happen.
func (s *Scheduler) raiseUnverifiedDeliverable(repo string, issue int, runID string, f deliverable.Finding) {
	var b strings.Builder
	b.WriteString(fmt.Sprintf("Issue #%d validated green and its PR was created, but %s\n\n",
		issue, strings.ToLower(f.Summary())))
	b.WriteString("Never executed:\n")
	for _, p := range f.Paths() {
		b.WriteString(fmt.Sprintf("- %s\n", p))
	}
	for _, t := range f.Tiers {
		if reason := f.TierReasons[t]; reason != "" {
			b.WriteString(fmt.Sprintf("\n%s tier: %s\n", t, reason))
		}
	}
	b.WriteString("\nVerify the suite manually before relying on it. Each run that ships this way makes the next skip easier to justify and the eventual first execution larger.")

	s.raiseAttention(attention.DecisionRequest{
		IdempotencyKey: fmt.Sprintf("unverified-deliverable:%s#%d", repo, issue),
		Kind:           attention.KindApprove,
		Severity:       attention.SeverityFYI,
		Title:          fmt.Sprintf("#%d shipped a test suite that never ran", issue),
		Body:           b.String(),
		Producer:       "unverified-deliverable",
		Context: attention.Context{
			Repo: repo, Issue: issue, RunID: runID, Stage: "feature-validate",
			Blocker: f.Summary(), TraceRef: runTraceRef(runID),
		},
		Options: []attention.Option{
			noopOption("acknowledged", "Acknowledged"),
			noopOption("will-verify", "Will verify manually"),
		},
		DefaultAction: attention.ExpireNoop,
		ExpiresAt:     expiryFromNow(72 * time.Hour),
		Steer:         &attention.Steer{Enabled: true, Hint: "Note how this suite should be wired up"},
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

// --- Producer 8: branch forked from its remote (run-scoped, Scheduler) ------

// raiseBranchForked surfaces a branch whose remote head is not reachable from
// the local tip (#163). unblock kind: no pipeline retry can clear it — the
// remote carries a commit this worktree never saw, so every push is rejected
// as non-fast-forward until a human decides which side survives.
//
// The card names both SHAs. A fork is only actionable if the operator can see
// what diverged; "your branch has forked" without the two commits sends them
// back to the terminal to re-derive the evidence the pipeline already had.
func (s *Scheduler) raiseBranchForked(repo string, issue int, runID, branch string, fork BranchFork) {
	s.raiseAttention(attention.DecisionRequest{
		IdempotencyKey: fmt.Sprintf("branch-forked:%s#%d", repo, issue),
		Kind:           attention.KindUnblock,
		Severity:       attention.SeverityBlockingRun,
		Title:          fmt.Sprintf("Branch %s has forked from origin", branch),
		Body: fmt.Sprintf(
			"#%d cannot push: %s\n\n"+
				"No retry clears this. Decide which side survives — reset the local branch onto origin/%s, "+
				"or delete origin/%s if the remote commit is an orphan from a killed run — then retry.",
			issue, fork.Detail, branch, branch),
		Producer: "branch-fork",
		Context: attention.Context{
			Repo: repo, Issue: issue, RunID: runID,
			Blocker:  fmt.Sprintf("origin/%s=%s local=%s", branch, shortSHA(fork.RemoteSHA), shortSHA(fork.LocalSHA)),
			TraceRef: runTraceRef(runID),
		},
		Options: []attention.Option{
			{ID: "resolved-retry", Label: "Fork resolved — retry", Verb: attention.VerbAutonomousClearIssueFailures,
				Args: map[string]any{"key": fmt.Sprintf("%s#%d", repo, issue), "then": "autonomous.rescan"}, Style: attention.StylePrimary},
			noopOption("wait", "Leave for manual triage"),
		},
		DefaultAction: attention.ExpireNoop,
		ExpiresAt:     expiryFromNow(48 * time.Hour),
		Steer:         &attention.Steer{Enabled: true, Hint: "Say how the fork was resolved before the retry"},
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
