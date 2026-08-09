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
	producerUntrustedAuthor     = "author-trust-gate"
	producerSelfRepoRefusal     = "self-repo-refusal"
)

// producerUnverifiedDeliverableStreak is a fourth standing, run-loop-scoped
// producer (#177), but unlike the four above it is scoped to (repo, tier),
// not to the fleet or one issue — a validate run only ever observes one
// repo's tiers per invocation, never the producer's complete condition set.
// See raiseUnverifiedDeliverableStreak / resolveUnverifiedDeliverableStreak.
const producerUnverifiedDeliverableStreak = "unverified-deliverable-streak"

// keyUnverifiedDeliverableStreak builds the sticky (producer, idempotency_key)
// identity for a per-(repo, tier) consecutive-skip streak.
func keyUnverifiedDeliverableStreak(repo string, tier deliverable.Tier) string {
	return fmt.Sprintf("%s:%s:%s", producerUnverifiedDeliverableStreak, repo, tier)
}

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

// IsBranchProtectionPunt reports whether a pr-merge punt reason is a
// branch-protection / required-check / review block that no LLM retry can clear
// — the class that warrants a human-needed Action Center card (ADR 015 §F #6).
// Reasons are prefixed (e.g. "review-not-approved: …"), so match by prefix.
//
// Exported for the IPC raise verb (#305): the extension path reaches the same
// dead end and must gate on the same predicate over the same reason strings,
// rather than re-deciding "is this branch protection?" from prose.
func IsBranchProtectionPunt(reason string) bool {
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
	if _, _, err := store.Raise(req); err != nil {
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

// --- Producer (unnumbered): architecture-approval gate (per-issue) -----------

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
			"Leaving it parks the issue off the dispatch path (In progress, no PR exists) — nothing re-dispatches it and the rest of the queue keeps flowing.",
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

// keyUntrustedAuthorSkip builds the sticky (producer, idempotency_key)
// identity for an untrusted-author skip card, keyed on repo#issue so
// re-observation from either gate (refinement or triage-promotion) updates
// the same card in place instead of spawning duplicates.
func keyUntrustedAuthorSkip(repo string, issue int) string {
	return fmt.Sprintf("%s:%s#%d", producerUntrustedAuthor, repo, issue)
}

// keySelfRepoRefusal builds the sticky (producer, idempotency_key) identity
// for a self-repo dispatch refusal (#292), keyed on repo#issue so both the
// prioritize-level gate and the enqueueItem defense-in-depth gate update one
// card in place.
func keySelfRepoRefusal(repo string, issue int) string {
	return fmt.Sprintf("%s:%s#%d", producerSelfRepoRefusal, repo, issue)
}

// raiseSelfRepoRefusal surfaces an fyi card when the self-repo guard (#292)
// refuses to dispatch an issue belonging to the repository that built the
// running binary. The refusal must be visible rather than a silent skip: the
// operator needs to know the issue exists and must be worked interactively
// (or the guard explicitly overridden). No repair option is offered — no
// registry verb clears the condition; only working the issue interactively
// or setting autonomous.allow_self_repo does (ADR 015 invariant 3).
func (as *AutonomousScheduler) raiseSelfRepoRefusal(repo string, issue int, title string) {
	as.raiseAttention(attention.DecisionRequest{
		IdempotencyKey: keySelfRepoRefusal(repo, issue),
		Kind:           attention.KindHandoff,
		Severity:       attention.SeverityFYI,
		Title:          fmt.Sprintf("Self-repo issue refused — work #%d interactively", issue),
		Body: fmt.Sprintf("#%d (%q) belongs to %s — the repository that built the RUNNING binary. "+
			"Autonomous refuses to dispatch it: a stage editing this repo can be destroyed by the "+
			"unfixed version of itself (#289 lost a completed implementation exactly this way, and a "+
			"fix to the execution machinery does not take effect until rebuild+reload, so the pipeline "+
			"would keep running the broken version while repeatedly trying to fix it). Work the issue "+
			"in an interactive session; to override deliberately, set autonomous.allow_self_repo: true "+
			"or pass --allow-self-repo.",
			issue, title, repo),
		Producer: producerSelfRepoRefusal,
		Standing: true,
		// Constant fingerprint: the verdict for a given issue cannot change
		// cycle to cycle unless config or the binary's origin changes, so
		// this alerts once and stays muted until resolved or retracted.
		Fingerprint: "refused:self-repo",
		Context:     attention.Context{Repo: repo, Issue: issue, Blocker: "self-repo guard (#292)"},
		Options: []attention.Option{
			noopOption("acknowledge", "Acknowledge (work interactively)"),
		},
		DefaultAction: "acknowledge",
		ExpiresAt:     standingExpiry(),
	})
}

// raiseUntrustedAuthorSkip surfaces a review_required-equivalent card when the
// author-trust gate (#270) skips an issue at refinement candidate selection or
// Backlog→Ready promotion. Without this, an untrusted-author issue silently
// disappears from autonomous consideration instead of being visible to a
// maintainer who can review it and manually promote if the author should be
// trusted. gateSite distinguishes which gate skipped it (e.g. "refinement",
// "triage-promotion") for the card body.
func (as *AutonomousScheduler) raiseUntrustedAuthorSkip(owner, repo string, issue int, title, authorAssociation, gateSite string) {
	fullRepo := fmt.Sprintf("%s/%s", owner, repo)
	assoc := authorAssociation
	if assoc == "" {
		assoc = "(none)"
	}
	as.raiseAttention(attention.DecisionRequest{
		IdempotencyKey: keyUntrustedAuthorSkip(fullRepo, issue),
		Kind:           attention.KindApprove,
		Severity:       attention.SeverityFYI,
		Title:          fmt.Sprintf("Untrusted author skipped at %s — #%d", gateSite, issue),
		Body: fmt.Sprintf("#%d (%q) was skipped by the author-trust gate at %s: author_association=%s is not in the "+
			"trusted set (default OWNER/MEMBER/COLLABORATOR, or autonomous.trusted_author_associations). "+
			"Review the issue; if the author should be trusted, either add their association to "+
			"autonomous.trusted_author_associations or manually promote the issue's board status to Ready.",
			issue, title, gateSite, assoc),
		Producer: producerUntrustedAuthor,
		Standing: true,
		// Constant fingerprint: the gate's verdict for a given issue does not
		// change cycle to cycle unless the author or config changes, so this
		// alerts once and stays muted until a human resolves it.
		Fingerprint: "skipped:untrusted-author",
		Context:     attention.Context{Repo: fullRepo, Issue: issue, Blocker: fmt.Sprintf("author_association=%s", assoc)},
		Options: []attention.Option{
			{ID: "promote", Label: "Promote to Ready", Verb: attention.VerbProjectSyncStatus,
				Args:  map[string]any{"owner": owner, "repo": repo, "issueNumber": issue, "status": "ready"},
				Style: attention.StyleDefault},
			noopOption("leave", "Leave skipped"),
		},
		DefaultAction: "leave",
		ExpiresAt:     standingExpiry(),
		Steer:         &attention.Steer{Enabled: true, Hint: "Note why this author should (or should not) be trusted"},
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

// --- Producer (unnumbered): terminal failure halt (per-issue, fleet-blocking) ---

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
// stage/terminalKind/costUSD degrade gracefully when empty/zero. costUSD is
// the run's total spend including the failing stage: the IPC "failed" stage
// transition books the terminating stage's cost into TotalCostUSD (#293),
// mirroring the Go scheduler path's #146 plumbing, and the extension passes
// that total through.
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
	if terminalKind == TerminalKindCommitOrphaned {
		b.WriteString("A killed stage's commit landed off the expected feature branch (most likely a stray `temp-pre-push-<n>` branch left over from a SIGKILL mid pre-push validation) and self-heal could not check out the feature branch to recover it. The commit itself was NOT deleted — the worktree/branch cleanup guard preserves anything ahead of the base branch. Push the stranded branch and open a PR from it by hand before retrying; a plain Retry re-dispatches into a fresh worktree and re-derives the work from scratch instead of reusing the commit.\n\n")
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
// Fail-open: a list error — or an autonomous state we cannot read at all —
// leaves existing cards untouched rather than retracting them; invariant 1,
// "I could not look" is never "nothing is wrong". Only a state we DID read,
// and which is genuinely no longer halted for this reason, retracts.
func (as *AutonomousScheduler) reconcileTerminalFailureCards() {
	if as == nil || as.attention == nil {
		return
	}
	// Three states, not two (#302). Folding an unreadable state into
	// "not halted" hands it to the retraction branch, which clears every open
	// card while the halt is still in force — invariant 1 violated by the one
	// path that never looked.
	as.mu.Lock()
	readable := as.state != nil
	// One definition of "the fleet is halted on a slot failure", shared with
	// the fleet-idle suppression guard in runCycle (#405) — two copies of the
	// same conjunct is how one state rewrite silently broke both producers at
	// once.
	stillHalted := readable && haltedOnSlotFailure(as.state.Status, as.state.PauseTriggeredBy)
	as.mu.Unlock()

	if !readable {
		log.Printf("attention: WARN %q reconcile skipped (fail-open): autonomous state unavailable — retracting nothing", producerTerminalFailure)
		return
	}
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

// ProposedCeilingUSD is the single rule for "what ceiling should the card
// offer?" — a 50% raise above the enforced ceiling, floored above what the run
// already spent so the offer is never below the bill.
//
// Extracted (#305) because BOTH terminal paths must propose the same number.
// The Go scheduler and the extension's between-stage ceiling check reach the
// same dead end; if each did the arithmetic itself, the two cards would offer
// different ceilings for the same overrun and the parity test would still pass
// on every field that is not a number.
func ProposedCeilingUSD(enforcedCeilingUSD, spentUSD float64) float64 {
	proposed := enforcedCeilingUSD * 1.5
	if proposed <= spentUSD {
		proposed = spentUSD * 1.5
	}
	return proposed
}

// BuildBudgetCeilingHit constructs the budget-ceiling card. Pure: it takes
// scalars and returns the record, so the Go scheduler path and the IPC raise
// verb (#305) produce a byte-identical DecisionRequest from the same inputs
// rather than two hand-aligned builders.
//
// proposedCeilingUSD <= 0 means THE SPEND COULD NOT BE CORROBORATED, and the
// card is built WITHOUT the `budget.raiseCeiling` option (fixed in review).
// Resolving that option persists a workspace-global dollar ceiling override, so
// the offer may only be made when the numbers behind it came from state the
// daemon itself recorded. When they did not, the honest card is the one that
// says a ceiling stop was reported and asks a human to look — never a
// one-click raise whose magnitude nothing corroborated. `costUSD` is likewise
// the RECORDED spend; a builder that had to invent it would be printing a
// caller's assertion as fact on an operator's screen.
func BuildBudgetCeilingHit(repo string, issue int, runID string, costUSD, proposedCeilingUSD float64) attention.DecisionRequest {
	owner, name := splitRepo(repo)

	title := fmt.Sprintf("Budget ceiling hit — $%.2f spent on #%d", costUSD, issue)
	body := fmt.Sprintf("Run #%d hit the pipeline budget ceiling. Raise the ceiling to $%.2f and retry, or halt.", issue, proposedCeilingUSD)
	options := []attention.Option{
		{ID: "raise", Label: fmt.Sprintf("Raise to $%.2f & retry", proposedCeilingUSD), Verb: attention.VerbBudgetRaiseCeiling,
			Args: map[string]any{"owner": owner, "repo": name, "issueNumber": issue, "ceilingUsd": proposedCeilingUSD, "title": ""}, Style: attention.StylePrimary},
		noopOption("halt", "Halt run"),
	}
	if proposedCeilingUSD <= 0 {
		title = fmt.Sprintf("Budget ceiling stop reported for #%d", issue)
		body = fmt.Sprintf(
			"A budget-ceiling stop was reported for #%d, but this daemon has no recorded spend for that run — "+
				"so the raise-and-retry option is deliberately NOT offered here.\n\n"+
				"Resolving that option persists a workspace-wide runtime ceiling override, and the amount would "+
				"have had to come from the report rather than from state this daemon recorded itself. "+
				"Check the run's cost in the pipeline history, then raise the ceiling in config if it should go up.",
			issue)
		options = []attention.Option{
			noopOption("acknowledged", "Acknowledged"),
			noopOption("halt", "Halt run"),
		}
	}

	return attention.DecisionRequest{
		IdempotencyKey: fmt.Sprintf("budget-ceiling:%s#%d", repo, issue),
		Kind:           attention.KindApprove,
		Severity:       attention.SeverityBlockingRun,
		Title:          title,
		Body:           body,
		Producer:       "budget-ceiling",
		Context:        attention.Context{Repo: repo, Issue: issue, RunID: runID, CostSoFarUSD: costUSD, Blocker: "pipeline budget ceiling exceeded", TraceRef: runTraceRef(runID)},
		Options:        options,
		DefaultAction:  "halt",
		ExpiresAt:      expiryFromNow(1 * time.Hour),
		Steer:          &attention.Steer{Enabled: true, Hint: "Add context for raising the ceiling, or for halting"},
	}
}

// raiseBudgetCeilingHit surfaces a run terminated by the pipeline budget
// ceiling. approve kind. raise-to option carries the proposed higher ceiling.
func (s *Scheduler) raiseBudgetCeilingHit(repo string, issue int, runID string, costUSD, proposedCeilingUSD float64) {
	s.raiseAttention(BuildBudgetCeilingHit(repo, issue, runID, costUSD, proposedCeilingUSD))
}

// --- Producer 6: branch-protection block (run-scoped, Scheduler) -------------

// BuildBranchProtectionBlock constructs the branch-protection card. Pure, for
// the same reason as BuildBudgetCeilingHit (#305).
//
// `reason` must be a pr-merge punt reason as produced by
// stages.Decide — NOT prose. Both paths derive it from the same decision
// matrix over the same PR snapshot, so the card's body and the
// IsBranchProtectionPunt gate read identical text no matter which surface
// observed the block.
func BuildBranchProtectionBlock(repo string, issue, prNumber int, runID, reason string) attention.DecisionRequest {
	return attention.DecisionRequest{
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
	}
}

// raiseBranchProtectionBlock surfaces a pr-merge punt caused by branch
// protection / a required check. unblock kind: it needs a human to fix.
func (s *Scheduler) raiseBranchProtectionBlock(repo string, issue, prNumber int, runID, reason string) {
	s.raiseAttention(BuildBranchProtectionBlock(repo, issue, prNumber, runID, reason))
}

// --- Producer 12: abandoned dispatch (run-scoped, extension-only) ------------
//
// TWELVE, not eleven. Number 11 in ADR-015 §F is "Unexercised deliverable"; the
// first cut of this producer labelled itself 11 too, so the same number named
// two different producers depending on which file you read (fixed in review).
// ADR-015 §F carries the authoritative row.
//
// The numbering rule these headers follow (ADR-015 §F, and pinned by
// TestProducerLabelsMatchTheADRNumbering): a number in a header must be the
// ADR's row for THAT producer, and a producer the ADR does not enumerate is
// labelled `(unnumbered)` rather than given a plausible-looking one. Round 3
// declared the invariant and left four headers violating it — 8 named the
// watchdog, the unexercised deliverable AND the branch fork; 9 named both
// `default-branch-health` and the terminal-failure halt.

// ProducerAbandonedDispatch names the force-clear producer (#305/#307).
const ProducerAbandonedDispatch = "abandoned-dispatch"

// AbandonedDispatchSituation names WHICH force-clear the card describes.
//
// One producer, THREE populations, and the first cut printed one fixed body for
// all of them — two of which it was false for (fixed in review). The
// force-clear funnel has two arms and each arm has two booking outcomes, and
// what an operator needs to know differs on every axis: whether a stage ever
// ran, whether a worktree exists to inspect, and whether the dispatch's
// terminal bookkeeping was actually booked by anyone. The situation is decided
// at the call site — `ConcurrentPipelineManager` already holds both facts
// synchronously before it raises — and it selects the body, never the options.
type AbandonedDispatchSituation string

const (
	// AbandonedReservationNeverStarted is the RESERVATION arm: the dispatch
	// wedged inside `startSlotInner` (worktree-manager resolution, a 15s
	// `git ls-remote`, `git worktree add`) and never became a slot. No stage
	// ran, no agent wrote anything, no `notifyStageTransition` ever fired — so
	// there is no uncommitted work to rescue and no recorded state to be stale.
	AbandonedReservationNeverStarted AbandonedDispatchSituation = "reservation-never-started"

	// AbandonedSlotWorktreePreserved is the SLOT arm with the force-clear
	// booking the terminal outcome on the dispatch's behalf: the queue mark and
	// the scheduler seat are released, and the run's per-issue worktree is
	// deliberately left on disk because a killed process may still be writing
	// in it.
	AbandonedSlotWorktreePreserved AbandonedDispatchSituation = "slot-worktree-preserved"

	// AbandonedClaimTakenThenWedged is either arm when the dispatch had already
	// CLAIMED its terminal outcome and then wedged before its callback fired.
	// The force-clear stands down on the claim (booking twice double-charges
	// the cascade breaker and the per-issue lifetime cap), so nobody books it:
	// the queue mark is released but the Go scheduler's running-slot seat is
	// still held. This is the one situation where something IS held and an
	// action IS required, which is exactly what the single fixed body used to
	// deny.
	AbandonedClaimTakenThenWedged AbandonedDispatchSituation = "claim-taken-then-wedged"
)

// AbandonedDispatchSituations returns the closed set, sorted, for validation,
// error text and tests. Derived from one list so a situation added to the
// builder cannot be missing from what the IPC boundary accepts.
func AbandonedDispatchSituations() []string {
	return []string{
		string(AbandonedClaimTakenThenWedged),
		string(AbandonedReservationNeverStarted),
		string(AbandonedSlotWorktreePreserved),
	}
}

// IsAbandonedDispatchSituation reports whether s is one of the three declared
// situations. The IPC verb rejects anything else rather than defaulting: a
// default would silently print the wrong body for an unrecognised arm, which is
// the defect this parameter exists to fix.
func IsAbandonedDispatchSituation(s string) bool {
	for _, v := range AbandonedDispatchSituations() {
		if v == s {
			return true
		}
	}
	return false
}

// BuildAbandonedDispatch constructs the card for a dispatch the extension's
// abort deadline gave up on (ConcurrentPipelineManager.forceClearStuckSlots).
//
// Extension-only BY DESIGN, and the one producer here with no Go counterpart:
// the Go scheduler's terminal defer runs in the same goroutine as its stage
// loop, so nothing else can declare a run abandoned while that defer is still
// owed. There is no Go force-clear funnel to keep in parity — see the
// force-clear-terminal-bookkeeping row in terminal_behaviors.json.
//
// #307 booked the terminal state of a force-cleared dispatch but had no way to
// TELL anyone: its only surfacing was a transient Stop toast and a warn log,
// and its own ledger named this the gap. The card is what survives the toast.
//
// Keyed per (repo, issue), NOT per generation: a wedged slot that force-clears,
// gets re-queued, and wedges again is one condition an operator has to deal
// with once, not a new card per attempt. Store.Raise's open-record dedup is
// what collapses them — an open card for the key is updated in place.
//
// EVENT-SHAPED, NOT STANDING, and the distinction is load-bearing rather than
// cosmetic (fixed in review; the first cut shipped Standing + a constant
// fingerprint "abandoned:force-clear"). Two independent reasons:
//
//  1. The trigger site observes a TRANSITION once. forceClearStuckSlots fires
//     from the abort deadline for one wedged slot; it does not re-answer "is
//     this issue's dispatch abandoned?" on a loop. That is exactly the test
//     docs/ATTENTION_PRODUCERS.md states for event vs standing.
//  2. Standing carries a suppression rule this producer cannot satisfy.
//     ADR-015 §M: a standing raise whose fingerprint equals the latest
//     HUMAN-RESOLVED record for its key returns `suppressed` and writes
//     nothing. A CONSTANT fingerprint can never move, resolved records are
//     never pruned, and no sweep calls AutoResolveUnobserved/AutoResolveKey for
//     this producer — so the first resolution silenced the (repo, issue) key
//     forever. The failure loop was the card's own happy path: the operator
//     clicks Retry → Store.Resolve marks it resolved and re-dispatches → the
//     retried dispatch wedges again (the common case for a wedged worktree) →
//     `suppressed`, and the operator is never told their retry died.
//     TestAbandonedDispatchReRaisesAfterAHumanResolution pins the fix.
//
// Event shape gives the behaviour that was actually wanted at every step:
// repeat force-clears while the card is OPEN update it in place (one card), a
// resolved card's successor is a genuinely new fact and gets a new card, and an
// EXPIRED predecessor is revived under its own id by findExpiredByKey.
//
// INFORMATIONAL / CLEANUP-SAFETY, NOT AN UNBLOCK — re-shaped in review after
// the population was traced. `forceClearStuckSlots` has exactly ONE call site:
// the ABORT_ALL_TIMEOUT_MS branch of `abortAll`, which is reached only from
// `nightgauge.stopPipeline`, `nightgauge.abortPipeline`, and `deactivate()`.
// Before that deadline can fire, `abortAll` has already cleared the queue and
// set `userCancelled` on every slot, and the terminal error booked is literally
// "Cancelled by user". So one hundred percent of this producer's population is
// "the operator pressed Stop and a slot took longer than the deadline to
// settle" — a fact about the operator's own action, not a blocked run.
//
// The first cut shipped Kind=unblock, Severity=blocking_run, and a StylePrimary
// "Retry" that cleared the issue's failure cooldown and re-dispatched. Stopping
// three wedged pipelines therefore produced three blocking_run cards each
// recommending you undo your own Stop — the inbox-destroying pattern ADR-015
// §D/§L exist to prevent, at a severity §I routes to alerting while nothing is
// blocked. The Retry was also close to inert: its verb pair is
// clearIssueFailures + `autonomous.rescan`, and `TriggerRescan` is a
// non-blocking channel poke, so with the autonomous loop stopped (the ordinary
// state after a manual Stop) it cleared a cooldown, poked a channel nobody
// reads, and resolved the card while nothing happened.
//
// What survives is the part that is true and useful for the situation the call
// site observed: which of the three the card describes is the `situation`
// argument, and each body states only what holds for that arm (fixed in
// review — one fixed body was false for two of the three). Both options are
// noops in every situation — this card asks a human to look, and does not
// pretend the pipeline can fix it.
func BuildAbandonedDispatch(repo string, issue int, runID, stage string, situation AbandonedDispatchSituation) attention.DecisionRequest {
	title, body, blocker := abandonedDispatchProse(issue, stage, situation)
	if situation == AbandonedReservationNeverStarted {
		// No stage ever began, so the card must not name one. Context.Stage is
		// the field the surfaces render as "last seen in"; a value here would be
		// an invented waypoint.
		stage = ""
	} else if stage == "" {
		stage = "unknown"
	}

	return attention.DecisionRequest{
		IdempotencyKey: fmt.Sprintf("%s:%s#%d", ProducerAbandonedDispatch, repo, issue),
		// approve + fyi in all three situations, INCLUDING claim-taken-then-
		// wedged. Every card this producer raises follows the operator's own
		// Stop, and ADR-015 §I routes blocking_run to alerting — paging someone
		// about the consequence of the button they just pressed is the pattern
		// §D/§L exist to prevent. The held seat is real, so the BODY names it and
		// names what clears it; the severity stays where the population puts it.
		Kind:     attention.KindApprove,
		Severity: attention.SeverityFYI,
		Title:    title,
		Body:     body,
		Producer: ProducerAbandonedDispatch,
		Context: attention.Context{
			Repo: repo, Issue: issue, RunID: runID, Stage: stage,
			Blocker:  blocker,
			TraceRef: runTraceRef(runID),
		},
		// NO RETRY. Re-dispatching work the operator deliberately cancelled is
		// not a remedy, and offering it as the PRIMARY action told them the
		// opposite of what they had just decided.
		Options: []attention.Option{
			noopOption("acknowledged", "Acknowledged"),
			noopOption("will-inspect", "Will inspect the worktree"),
		},
		DefaultAction: attention.ExpireNoop,
		// The same window branch-protection declares: neither has an
		// auto-retraction path, so expiry is the only thing that clears them.
		ExpiresAt: expiryFromNow(48 * time.Hour),
		Steer:     &attention.Steer{Enabled: true, Hint: "Note what the stopped run left behind, or what to check first"},
	}
}

// abandonedDispatchProse returns the title, body and blocker for one force-clear
// situation. Split out so the three bodies sit side by side and a claim made in
// one is visibly absent from the others.
//
// The shared prologue is deliberately short. Everything after it is
// situation-specific, because the three differ in every fact an operator acts
// on: whether a stage ran, whether there is a worktree to inspect, whether
// anything the daemon recorded can be stale, and whether the dispatch's terminal
// bookkeeping was booked at all.
func abandonedDispatchProse(issue int, stage string, situation AbandonedDispatchSituation) (title, body, blocker string) {
	const prologue = "You stopped the pipeline, and issue #%d's dispatch did not settle before the abort deadline expired. "

	switch situation {
	case AbandonedReservationNeverStarted:
		return fmt.Sprintf("Stop force-cleared #%d before any stage started", issue),
			fmt.Sprintf(prologue+
				"It never became a running slot — it was still inside worktree setup — so NO STAGE RAN, no agent "+
				"wrote anything, and the daemon was never told about the run at all. The extension booked its "+
				"terminal bookkeeping on its behalf: the queue mark and the scheduler seat are released.\n\n"+
				"NOTHING IS BLOCKED and no action is required. One thing is worth knowing: worktree creation may "+
				"have been interrupted part-way. The wedged process removes its own partial tree when it unwinds; "+
				"if it never does, `nightgauge worktree sweep` reclaims it. There is no uncommitted agent work to "+
				"rescue here and no recorded run state to be stale — neither was ever produced.",
				issue),
			"operator Stop: dispatch wedged during worktree setup, before any stage ran (#307 force-clear)"

	case AbandonedClaimTakenThenWedged:
		return fmt.Sprintf("Stop force-cleared #%d%s — its terminal bookkeeping is still owed", issue, atStageClause(stage)),
			fmt.Sprintf(prologue+
				"It had ALREADY CLAIMED its own terminal bookkeeping, so the force-clear stood down rather than "+
				"booking a second one — and then it wedged before its callback fired, so nobody booked it.%s\n\n"+
				"SOMETHING IS STILL HELD, and this is the one case where an action is worth taking:\n\n"+
				"1. The Go scheduler's running-slot seat for #%d was NOT released. Nothing called "+
				"`autonomous.complete` for this dispatch, so `isRunning()` keeps the issue ineligible for "+
				"re-dispatch. It clears if the wedged process finally settles and fires its own callback; "+
				"otherwise it is held until the autonomous scheduler restarts. Restart it if you need #%d "+
				"dispatchable sooner. (The queue mark itself WAS released — that step does not wait on the "+
				"claim.)\n\n"+
				"2. The worktree is PRESERVED on purpose — the stopped process may still have been writing in it, "+
				"so nothing was stashed, committed, or deleted. It may hold uncommitted work. Inspect it before "+
				"re-dispatching: a re-dispatch reuses the same per-issue worktree path.\n\n"+
				"3. The Go-side state for this issue may be stale — the wedged process was killed mid-flight, so "+
				"what the daemon recorded for the run stops wherever it stopped rather than at a real terminal.",
				issue, lastSeenClause(stage), issue, issue),
			"operator Stop: dispatch claimed its terminal outcome then wedged — outcome unbooked (#307 force-clear)"

	default: // AbandonedSlotWorktreePreserved
		return fmt.Sprintf("Stop force-cleared #%d%s — worktree preserved", issue, atStageClause(stage)),
			fmt.Sprintf(prologue+
				"The extension force-cleared it and booked its terminal bookkeeping on its behalf: the queue mark "+
				"and the scheduler seat are released.%s\n\n"+
				"NOTHING IS BLOCKED and no action is required. This card exists because two things are worth "+
				"knowing before you touch that issue again:\n\n"+
				"1. The worktree is PRESERVED on purpose — the stopped process may still have been writing in it, "+
				"so nothing was stashed, committed, or deleted. It may hold uncommitted work. A re-dispatch reuses "+
				"the same per-issue worktree path and re-derives the work from scratch, so inspect it first if the "+
				"run had got anywhere.\n\n"+
				"2. The Go-side state for this issue may be stale — the wedged process was killed mid-flight, so "+
				"what the daemon recorded for the run stops wherever it stopped rather than at a real terminal.",
				issue, lastSeenClause(stage)),
			"operator Stop: dispatch wedged past the abort deadline (#307 force-clear)"
	}
}

// atStageClause renders the title's stage fragment, or nothing when the caller
// could not name a stage. "at unknown" in a title is noise the operator has to
// decode; absence says the same thing without pretending to a waypoint.
func atStageClause(stage string) string {
	if stage == "" {
		return ""
	}
	return " at " + stage
}

// lastSeenClause renders the body's stage sentence, or nothing.
func lastSeenClause(stage string) string {
	if stage == "" {
		return ""
	}
	return " The last stage it was seen in is " + stage + "."
}

// --- Producer 11: unexercised deliverable (run-scoped, Scheduler) ------------

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

// --- Producer (unnumbered): unverified-deliverable streak (standing) ---------

// raiseUnverifiedDeliverableStreak surfaces consecutive occurrences of the
// same idle tier for the same repo (#177). raiseUnverifiedDeliverable already
// cards each occurrence individually; this producer is what makes the
// PATTERN visible — three consecutive skips of the same tier read as three
// unrelated one-off FYI cards today, and the precedent argument ("the
// identical gap shipped last time too") gets stronger every occurrence while
// the signal an operator sees stays flat.
//
// Standing, keyed per (repo, tier): the streak count is held in the store's
// durable counter, incremented here, and rendered onto the card — Store.Raise's
// dedup-by-key folds this into the existing card rather than creating a new
// one, escalating Severity as the count climbs. Escalation is severity-only
// and caps at blocking_run: nothing in a growing streak justifies blocking
// the fleet (#152 deliberately chose not to block; a gate that blocks
// legitimate work gets routed around).
//
// The count deliberately does not live on the card (#243). Reading it back out
// of the prior card's Fingerprint made every path that ends a card's life a
// silent reset — an operator acknowledging it, or ExpiresAt elapsing — so the
// streak could never exceed the blocking_run threshold that prompted the
// acknowledgement. A card may be dismissed; the fact it reports may not.
func (s *Scheduler) raiseUnverifiedDeliverableStreak(repo string, tier deliverable.Tier, issue int, runID, reason string) {
	key := keyUnverifiedDeliverableStreak(repo, tier)
	next := 1
	if s.attention != nil {
		n, err := s.attention.IncrementStreak(key)
		if err != nil {
			log.Printf("attention: increment streak %q failed (fail-open): %v", key, err)
		}
		if n > 0 {
			next = n
		}
	}

	severity := attention.SeverityFYI
	if next >= 3 {
		severity = attention.SeverityBlockingRun
	}

	body := fmt.Sprintf("The %s tier has now shipped unexecuted %d consecutive time(s) for %s — most recently on #%d.\n\n",
		tier, next, repo, issue)
	if reason != "" {
		body += fmt.Sprintf("Latest reason: %s\n\n", reason)
	}
	body += "Each consecutive skip makes the precedent argument for the next one stronger while this card's severity is the only thing tracking that. It clears the moment this tier actually executes for any issue in this repo."

	s.raiseAttention(attention.DecisionRequest{
		IdempotencyKey: key,
		Kind:           attention.KindApprove,
		Severity:       severity,
		Title:          fmt.Sprintf("%s tier unexecuted %d consecutive time(s) — %s", tier, next, repo),
		Body:           body,
		Producer:       producerUnverifiedDeliverableStreak,
		Context: attention.Context{
			Repo: repo, Issue: issue, RunID: runID, Stage: "feature-validate",
			Blocker: reason, TraceRef: runTraceRef(runID),
		},
		Standing: true,
		// The streak count is the material state (docs/ATTENTION_PRODUCERS.md
		// invariant 2): it only advances on a genuinely new occurrence, never
		// on its own with elapsed time. A changed fingerprint re-alerts; an
		// unchanged one (the same run re-processed, which should not happen)
		// refreshes silently.
		Fingerprint: fmt.Sprintf("streak:%d", next),
		Options: []attention.Option{
			noopOption("acknowledged", "Acknowledged"),
			noopOption("will-verify", "Will verify manually"),
		},
		DefaultAction: attention.ExpireNoop,
		ExpiresAt:     standingExpiry(),
		Steer:         &attention.Steer{Enabled: true, Hint: "Note how this tier should be wired up"},
	})
}

// resolveUnverifiedDeliverableStreak resets one (repo, tier) streak the
// moment that tier actually executes for an issue in the repo.
//
// A targeted single-key retract, not autoResolveAttention /
// AutoResolveUnobserved: those assume the caller just observed the
// producer's ENTIRE condition set, which holds for a fleet-wide producer like
// work-exhaustion but not here — one issue's validate run only ever observes
// one repo's tiers, never every repo the producer has open cards for.
func (s *Scheduler) resolveUnverifiedDeliverableStreak(repo string, tier deliverable.Tier) {
	if s == nil || s.attention == nil {
		return
	}
	key := keyUnverifiedDeliverableStreak(repo, tier)

	// Clear the durable count first, and unconditionally. An execution of the
	// tier is the only thing that makes the streak untrue, and it does so
	// whether or not a card is currently open — the operator may have already
	// acknowledged it, or it may have expired. Gating the reset on a
	// successful retract would leave the count standing in exactly those
	// cases (#243).
	if err := s.attention.ResetStreak(key); err != nil {
		log.Printf("attention: reset streak %q failed (fail-open): %v", key, err)
	}

	resolved, err := s.attention.AutoResolveKey(producerUnverifiedDeliverableStreak, key)
	if err != nil {
		log.Printf("attention: auto-resolve %q failed (fail-open): %v", producerUnverifiedDeliverableStreak, err)
		return
	}
	if resolved {
		log.Printf("attention: retracted %q streak card for %s (%s tier executed)", producerUnverifiedDeliverableStreak, repo, tier)
	}
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

// --- Producer (unnumbered): branch forked from its remote (run-scoped) -------

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
