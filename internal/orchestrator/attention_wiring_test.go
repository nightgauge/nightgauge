package orchestrator

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/nightgauge/nightgauge/internal/attention"
	"github.com/nightgauge/nightgauge/internal/deliverable"
	"github.com/nightgauge/nightgauge/internal/depgraph"
	pmstages "github.com/nightgauge/nightgauge/internal/orchestrator/stages"
)

// newAttentionProducerScheduler builds a scheduler through the real constructor
// so the attention store is wired exactly as in production (rooted at tmpDir,
// with the steer + trace listeners attached).
func newAttentionProducerScheduler(t *testing.T) *AutonomousScheduler {
	t.Helper()
	as := NewAutonomousScheduler(nil, nil, nil, nil, DefaultAutonomousConfig(), t.TempDir())
	if as.Attention() == nil {
		t.Fatal("attention store not wired by NewAutonomousScheduler")
	}
	t.Cleanup(as.drainBackground) // backstop; see newAutonomousForCascadeTest
	return as
}

func openRequests(t *testing.T, as *AutonomousScheduler) []attention.DecisionRequest {
	t.Helper()
	reqs, err := as.Attention().List(attention.ListFilter{})
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	return reqs
}

// newAttentionProducerRunScheduler builds a bare run-scoped Scheduler wired
// with its own attention store — sufficient for the three Scheduler-bound
// producers (raiseBudgetCeilingHit, raiseBranchProtectionBlock,
// raiseAuthFailure), which touch no other Scheduler field.
func newAttentionProducerRunScheduler(t *testing.T) *Scheduler {
	t.Helper()
	s := &Scheduler{}
	s.SetAttention(attention.New(t.TempDir()))
	if s.attention == nil {
		t.Fatal("attention store not wired")
	}
	return s
}

func openRunRequests(t *testing.T, s *Scheduler) []attention.DecisionRequest {
	t.Helper()
	reqs, err := s.attention.List(attention.ListFilter{})
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	return reqs
}

// assertSteerSet asserts every producer's card enables the steer rail with a
// non-empty, reasonably short hint (#363: no producer set Steer, so no card
// ever rendered the steer box despite the dashboard copy promising one).
func assertSteerSet(t *testing.T, r attention.DecisionRequest) {
	t.Helper()
	if r.Steer == nil || !r.Steer.Enabled {
		t.Fatalf("producer %q: Steer.Enabled = false/nil, want true", r.Producer)
	}
	if r.Steer.Hint == "" {
		t.Errorf("producer %q: Steer.Hint is empty, want a tailored hint", r.Producer)
	}
	if len(r.Steer.Hint) > 80 {
		t.Errorf("producer %q: Steer.Hint too long (%d chars): %q", r.Producer, len(r.Steer.Hint), r.Steer.Hint)
	}
}

func TestProducerWorkExhaustionEmitsFleetCard(t *testing.T) {
	as := newAttentionProducerScheduler(t)
	as.raiseWorkExhaustion(3)

	reqs := openRequests(t, as)
	if len(reqs) != 1 {
		t.Fatalf("got %d requests, want 1", len(reqs))
	}
	r := reqs[0]
	if r.Kind != attention.KindChoose || r.Severity != attention.SeverityFYI {
		t.Errorf("kind/severity = %q/%q, want choose/fyi", r.Kind, r.Severity)
	}
	if r.Context.Repo != "" || r.Context.RunID != "" {
		t.Error("work-exhaustion must be fleet-scoped (no repo/run)")
	}
	if r.FindOption("rescan") == nil || r.FindOption("leave") == nil {
		t.Error("expected rescan + leave options")
	}
	if r.DefaultAction != "leave" {
		t.Errorf("default_action = %q, want leave", r.DefaultAction)
	}
	// Every option binds a registered verb (the security boundary).
	for _, o := range r.Options {
		if !attention.IsRegisteredVerb(o.Verb) {
			t.Errorf("option %q binds unregistered verb %q", o.ID, o.Verb)
		}
	}
	assertSteerSet(t, r)
}

// TestIdleBranch_WorkExhaustionUsesLastPromotionEligible (#288) asserts the
// idle branch in runCycle feeds raiseWorkExhaustion the corrected
// LastPromotionEligible count rather than the old (semantically wrong)
// sum-of-rejection-reasons value.
func TestIdleBranch_WorkExhaustionUsesLastPromotionEligible(t *testing.T) {
	as := newAttentionProducerScheduler(t)
	as.state.Status = "running"
	as.state.LastPromotionEligible = 3
	// The old computation summed LastRejectionReasons — populate it with a
	// different total so a regression back to the old value is caught.
	as.state.LastRejectionReasons = map[string]int{"no-priority": 99}
	as.buildGraphFn = func(_ context.Context) (*depgraph.Graph, error) {
		return buildTestGraph(nil, nil), nil
	}

	as.runCycle(context.Background())

	reqs := openRequests(t, as)
	if len(reqs) != 1 {
		t.Fatalf("got %d requests, want 1 (fleet-idle card)", len(reqs))
	}
	r := reqs[0]
	if !strings.Contains(r.Title, "3 Backlog item(s) promotable") {
		t.Errorf("Title = %q, want it to reflect LastPromotionEligible=3, not LastRejectionReasons sum=99", r.Title)
	}
}

func TestProducerOwnerActionHandoffEmitsAndDedups(t *testing.T) {
	as := newAttentionProducerScheduler(t)
	as.raiseOwnerActionHandoff("octocat/acme", 51, "Rotate Cloudflare token", "owner-action")
	// Re-detection on a later cycle must UPDATE in place, not duplicate.
	as.raiseOwnerActionHandoff("octocat/acme", 51, "Rotate Cloudflare token", "owner-action")

	reqs := openRequests(t, as)
	if len(reqs) != 1 {
		t.Fatalf("got %d requests, want 1 (dedup)", len(reqs))
	}
	r := reqs[0]
	if r.Kind != attention.KindHandoff {
		t.Errorf("kind = %q, want handoff", r.Kind)
	}
	if r.Context.Issue != 51 || r.Context.Repo != "octocat/acme" {
		t.Errorf("context = %s#%d, want octocat/acme#51", r.Context.Repo, r.Context.Issue)
	}
	md := r.FindOption("mark-done")
	if md == nil || md.Verb != attention.VerbAutonomousComplete {
		t.Error("mark-done option must bind autonomous.complete")
	}
	if r.DefaultAction != attention.ExpireNoop {
		t.Errorf("default_action = %q, want expire_noop (needs a human)", r.DefaultAction)
	}
	assertSteerSet(t, r)
}

func TestProducerCascadePauseEmitsBlockingFleet(t *testing.T) {
	as := newAttentionProducerScheduler(t)
	as.raiseCascadePause("octocat/acme", 12, "safety:cascading-failures — 3 failures in 10m")

	reqs := openRequests(t, as)
	if len(reqs) != 1 {
		t.Fatalf("got %d requests, want 1", len(reqs))
	}
	r := reqs[0]
	if r.Kind != attention.KindResume || r.Severity != attention.SeverityBlockingFleet {
		t.Errorf("kind/severity = %q/%q, want resume/blocking_fleet", r.Kind, r.Severity)
	}
	resume := r.FindOption("resume")
	if resume == nil || resume.Verb != attention.VerbAutonomousResume {
		t.Error("resume option must bind autonomous.resume")
	}
	assertSteerSet(t, r)
}

func TestProducerBlockedByDeferralEmitsChoose(t *testing.T) {
	as := newAttentionProducerScheduler(t)
	as.raiseBlockedByDeferral("octocat/acme", 77, "Add login flow", "blocked by open dependency #70")

	reqs := openRequests(t, as)
	if len(reqs) != 1 {
		t.Fatalf("got %d requests, want 1", len(reqs))
	}
	r := reqs[0]
	if r.Kind != attention.KindChoose || r.Severity != attention.SeverityBlockingRun {
		t.Errorf("kind/severity = %q/%q, want choose/blocking_run", r.Kind, r.Severity)
	}
	if r.FindOption("requeue") == nil || r.FindOption("leave") == nil {
		t.Error("expected requeue + leave options")
	}
	assertSteerSet(t, r)
}

// TestProducerArchitectureApprovalEmitsApprove covers the card that replaces
// the transient VSCode toast: before this producer, a gate whose entire purpose
// is a human decision left no durable trace, so missing the toast meant the
// issue sat sidelined with no visible way back in.
func TestProducerArchitectureApprovalEmitsApprove(t *testing.T) {
	as := newAttentionProducerScheduler(t)
	as.raiseArchitectureApproval("octocat/acme", 900, "Voice session gateway",
		"ARCHITECTURE APPROVAL REQUIRED — production-touching change")

	reqs := openRequests(t, as)
	if len(reqs) != 1 {
		t.Fatalf("got %d requests, want 1", len(reqs))
	}
	r := reqs[0]
	if r.Kind != attention.KindApprove || r.Severity != attention.SeverityBlockingRun {
		t.Errorf("kind/severity = %q/%q, want approve/blocking_run", r.Kind, r.Severity)
	}
	approve := r.FindOption("approve")
	if approve == nil || r.FindOption("leave") == nil {
		t.Fatal("expected approve + leave options")
	}
	if approve.Verb != attention.VerbIssueApproveArchitecture {
		t.Errorf("approve verb = %q, want %q", approve.Verb, attention.VerbIssueApproveArchitecture)
	}
	// The label name must NOT ride in on args — the executor resolves it from
	// config so a surface can only ever grant this one gate.
	for _, k := range []string{"label", "labels", "labelName"} {
		if _, ok := approve.Args[k]; ok {
			t.Errorf("approve args carry %q — the label must be resolved daemon-side, not supplied by the surface", k)
		}
	}
	assertSteerSet(t, r)
}

// TestProducerUntrustedAuthorSkipEmitsApprove covers the review_required-
// equivalent card for #270's author-trust gate: without it, an untrusted
// author's issue silently disappears from autonomous consideration instead of
// surfacing to a maintainer who can review and manually promote it.
func TestProducerUntrustedAuthorSkipEmitsApprove(t *testing.T) {
	as := newAttentionProducerScheduler(t)
	as.raiseUntrustedAuthorSkip("octocat", "acme", 900, "Stranger's issue", "NONE", "refinement")

	reqs := openRequests(t, as)
	if len(reqs) != 1 {
		t.Fatalf("got %d requests, want 1", len(reqs))
	}
	r := reqs[0]
	if r.Kind != attention.KindApprove {
		t.Errorf("kind = %q, want approve", r.Kind)
	}
	promote := r.FindOption("promote")
	if promote == nil || r.FindOption("leave") == nil {
		t.Fatal("expected promote + leave options")
	}
	if promote.Verb != attention.VerbProjectSyncStatus {
		t.Errorf("promote verb = %q, want %q", promote.Verb, attention.VerbProjectSyncStatus)
	}
	assertSteerSet(t, r)
}

// TestUntrustedAuthorSkipDedupesAcrossGates verifies that both gate sites
// (refinement candidate selection and Backlog->Ready promotion) observing the
// same issue update a single card rather than spawning duplicates.
func TestUntrustedAuthorSkipDedupesAcrossGates(t *testing.T) {
	as := newAttentionProducerScheduler(t)
	as.raiseUntrustedAuthorSkip("octocat", "acme", 900, "Stranger's issue", "NONE", "refinement")
	as.raiseUntrustedAuthorSkip("octocat", "acme", 900, "Stranger's issue", "NONE", "triage-promotion")

	if got := len(openRequests(t, as)); got != 1 {
		t.Fatalf("got %d requests after both gates observed the same issue, want 1", got)
	}
}

// TestArchitectureApprovalReHaltNeverGrowsTheInbox is the shape that matters in
// production: the gate re-fires on every dispatch attempt, and the operator must
// still end up with exactly one card.
func TestArchitectureApprovalReHaltNeverGrowsTheInbox(t *testing.T) {
	as := newAttentionProducerScheduler(t)
	for i := 0; i < 4; i++ {
		as.raiseArchitectureApproval("octocat/acme", 900, "Voice session gateway",
			"ARCHITECTURE APPROVAL REQUIRED — production-touching change")
	}
	if reqs := openRequests(t, as); len(reqs) != 1 {
		t.Fatalf("got %d requests after 4 re-halts, want 1", len(reqs))
	}
}

// TestArchitectureApprovalRetractsOnlyTheApprovedIssue covers approval granted
// out-of-band (a human adds the label with gh and never touches the card). The
// approved issue's card must clear while every other pending approval survives —
// retracting the whole producer would silently drop decisions nobody made.
func TestArchitectureApprovalRetractsOnlyTheApprovedIssue(t *testing.T) {
	as := newAttentionProducerScheduler(t)
	as.raiseArchitectureApproval("octocat/acme", 900, "Voice gateway", "gate: production-touching")
	as.raiseArchitectureApproval("octocat/acme", 901, "QuestionSet contract", "gate: trade-off signals")
	as.raiseArchitectureApproval("octocat/other", 12, "Storage ADR", "gate: irreversible")
	if got := len(openRequests(t, as)); got != 3 {
		t.Fatalf("setup: got %d cards, want 3", got)
	}

	as.retractArchitectureApproval("octocat/acme", 900)

	reqs := openRequests(t, as)
	if len(reqs) != 2 {
		t.Fatalf("got %d cards after retracting one, want 2", len(reqs))
	}
	for _, r := range reqs {
		if r.IdempotencyKey == keyArchitectureApproval("octocat/acme", 900) {
			t.Error("the approved issue's card survived — it should have been retracted")
		}
	}

	// Retracting the last one empties the producer's inbox.
	as.retractArchitectureApproval("octocat/acme", 901)
	as.retractArchitectureApproval("octocat/other", 12)
	if got := len(openRequests(t, as)); got != 0 {
		t.Errorf("got %d cards after retracting all, want 0", got)
	}
}

func TestProducerStuckEpicOffersEscalationVerb(t *testing.T) {
	as := newAttentionProducerScheduler(t)
	as.raiseStuckEpic("octocat/acme", 100, "Auth epic", "3 open sub-issues, 0 eligible")

	reqs := openRequests(t, as)
	if len(reqs) != 1 {
		t.Fatalf("got %d requests, want 1", len(reqs))
	}
	esc := reqs[0].FindOption("escalate")
	if esc == nil || esc.Verb != attention.VerbRunRetryWithEscalation {
		t.Error("stuck-epic must offer the run.retryWithEscalation verb (ADR producer 8)")
	}
	assertSteerSet(t, reqs[0])
}

// alertingTransitions counts the journal transitions that would interrupt an
// operator, which is the number that must not grow with observation count.
func alertingTransitions(t *testing.T, as *AutonomousScheduler) int {
	t.Helper()
	entries, err := as.Attention().ReadJournal()
	if err != nil {
		t.Fatalf("ReadJournal: %v", err)
	}
	n := 0
	for _, e := range entries {
		if e.ShouldNotify() {
			n++
		}
	}
	return n
}

// TestRunLoopStandingProducersDeclareStandingAndAFingerprint pins the #108
// migration. These three describe conditions the scheduler re-evaluates every
// cycle, not transitions it observed once; without Standing + a Fingerprint
// every observation is a fresh event and the inbox fills with copies of one
// problem.
func TestRunLoopStandingProducersDeclareStandingAndAFingerprint(t *testing.T) {
	as := newAttentionProducerScheduler(t)
	as.raiseWorkExhaustion(3)
	as.raiseOwnerActionHandoff("octocat/acme", 51, "Rotate Cloudflare token", "owner-action")
	as.raiseStuckEpic("octocat/acme", 100, "Auth epic", "epic #100 stalled: #101 ready but undispatched")

	reqs := openRequests(t, as)
	if len(reqs) != 3 {
		t.Fatalf("got %d requests, want 3", len(reqs))
	}
	for _, r := range reqs {
		if !r.Standing {
			t.Errorf("producer %q: standing = false, want true", r.Producer)
		}
		if r.Fingerprint == "" {
			t.Errorf("producer %q: no fingerprint — a standing card without one re-alerts every cycle", r.Producer)
		}
		// The safety net, not the card's lifetime: a standing card is removed
		// when its condition clears, so expiry may only fire for a producer
		// that stopped being evaluated at all.
		exp, err := time.Parse(time.RFC3339Nano, r.ExpiresAt)
		if err != nil {
			t.Fatalf("producer %q: unparseable expires_at %q: %v", r.Producer, r.ExpiresAt, err)
		}
		if time.Until(exp) < attention.StandingExpiry-time.Hour {
			t.Errorf("producer %q: expires in %s, want the declared standing window", r.Producer, time.Until(exp))
		}
	}
}

// TestReDetectingOneStalledEpicNeverGrowsTheInbox is the producer-level shape
// of the reported failure: the watchdog re-fires on every idle cycle, and the
// operator must end up with one card and one alert.
func TestReDetectingOneStalledEpicNeverGrowsTheInbox(t *testing.T) {
	as := newAttentionProducerScheduler(t)
	for i := 0; i < 6; i++ {
		// The epic gets retitled between cycles. Prose moves on its own; only
		// the blocker set is material, so every re-detection must refresh the
		// card silently rather than alert.
		as.raiseStuckEpic("octocat/acme", 100, fmt.Sprintf("Auth epic (rev %d)", i),
			"epic #100 stalled: #101 ready but undispatched")
	}
	if got := len(openRequests(t, as)); got != 1 {
		t.Fatalf("six detections of one stalled epic produced %d cards, want 1", got)
	}
	if got := alertingTransitions(t, as); got != 1 {
		t.Errorf("six detections alerted %d times, want 1", got)
	}
}

// TestStandingCardsRetractWhenTheirConditionStopsBeingObserved covers the other
// half of standing semantics: the card clears itself, distinguishably from a
// decision someone made.
func TestStandingCardsRetractWhenTheirConditionStopsBeingObserved(t *testing.T) {
	as := newAttentionProducerScheduler(t)
	as.raiseWorkExhaustion(3)
	as.raiseStuckEpic("octocat/acme", 100, "Auth epic", "epic #100 stalled: #101 ready but undispatched")
	as.raiseStuckEpic("octocat/acme", 200, "Billing epic", "epic #200 stalled: #201 ready but undispatched")
	if got := len(openRequests(t, as)); got != 3 {
		t.Fatalf("got %d cards before retraction, want 3", got)
	}

	// The fleet finds work again.
	as.retractWorkExhaustion()
	// A later scan sees only the billing epic still stalled.
	as.autoResolveAttention(producerStuckEpic, []string{keyStuckEpic("octocat/acme", 200)})

	open := openRequests(t, as)
	if len(open) != 1 || open[0].Context.Issue != 200 {
		t.Fatalf("want only the still-stalled epic open, got %+v", open)
	}
	all, err := as.Attention().List(attention.ListFilter{IncludeTerminal: true})
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	retracted := 0
	for _, r := range all {
		if r.Lifecycle.State == attention.StateAutoResolved {
			retracted++
		}
	}
	if retracted != 2 {
		t.Errorf("%d cards auto-resolved, want 2 — a cleared condition is a system withdrawal, not a human decision", retracted)
	}
}

func TestProducerBudgetCeilingHitEmitsApprove(t *testing.T) {
	s := newAttentionProducerRunScheduler(t)
	s.raiseBudgetCeilingHit("octocat/acme", 42, "run-1", 12.5, 25.0)

	reqs := openRunRequests(t, s)
	if len(reqs) != 1 {
		t.Fatalf("got %d requests, want 1", len(reqs))
	}
	r := reqs[0]
	if r.Kind != attention.KindApprove || r.Severity != attention.SeverityBlockingRun {
		t.Errorf("kind/severity = %q/%q, want approve/blocking_run", r.Kind, r.Severity)
	}
	raise := r.FindOption("raise")
	if raise == nil || raise.Verb != attention.VerbBudgetRaiseCeiling {
		t.Error("raise option must bind budget.raiseCeiling")
	}
	assertSteerSet(t, r)
}

func TestProducerBranchProtectionBlockEmitsUnblock(t *testing.T) {
	s := newAttentionProducerRunScheduler(t)
	s.raiseBranchProtectionBlock("octocat/acme", 42, 99, "run-1", "review-not-approved: needs 1 more approval")

	reqs := openRunRequests(t, s)
	if len(reqs) != 1 {
		t.Fatalf("got %d requests, want 1", len(reqs))
	}
	r := reqs[0]
	if r.Kind != attention.KindUnblock || r.Severity != attention.SeverityBlockingRun {
		t.Errorf("kind/severity = %q/%q, want unblock/blocking_run", r.Kind, r.Severity)
	}
	retry := r.FindOption("retry-after-fix")
	if retry == nil || retry.Verb != attention.VerbAutonomousClearIssueFailures {
		t.Error("retry-after-fix option must bind autonomous.clearIssueFailures")
	}
	assertSteerSet(t, r)
}

func TestProducerAuthFailureEmitsProvideInput(t *testing.T) {
	s := newAttentionProducerRunScheduler(t)
	s.raiseAuthFailure("octocat/acme", 42, "run-1", "token expired")

	reqs := openRunRequests(t, s)
	if len(reqs) != 1 {
		t.Fatalf("got %d requests, want 1", len(reqs))
	}
	r := reqs[0]
	if r.Kind != attention.KindProvideInput || r.Severity != attention.SeverityBlockingRun {
		t.Errorf("kind/severity = %q/%q, want provide_input/blocking_run", r.Kind, r.Severity)
	}
	login := r.FindOption("login-and-retry")
	if login == nil || login.Verb != attention.VerbAutonomousClearIssueFailures {
		t.Error("login-and-retry option must bind autonomous.clearIssueFailures")
	}
	assertSteerSet(t, r)
}

// --- Producer (unnumbered): terminal failure halt (#148) ---------------------

func TestProducerTerminalFailureEmitsUnblockBlockingFleet(t *testing.T) {
	as := newAttentionProducerScheduler(t)
	as.RaiseTerminalFailure("octocat/acme", 42, "feature-dev", "validation_error", 3.25)

	reqs := openRequests(t, as)
	if len(reqs) != 1 {
		t.Fatalf("got %d requests, want 1", len(reqs))
	}
	r := reqs[0]
	if r.Kind != attention.KindUnblock || r.Severity != attention.SeverityBlockingFleet {
		t.Errorf("kind/severity = %q/%q, want unblock/blocking_fleet", r.Kind, r.Severity)
	}
	if !r.Standing {
		t.Error("terminal-failure card must be Standing")
	}
	if r.Fingerprint == "" {
		t.Error("standing card must declare a fingerprint")
	}
	retry := r.FindOption("retry")
	if retry == nil || retry.Verb != attention.VerbAutonomousClearIssueFailures {
		t.Error("retry option must bind autonomous.clearIssueFailures")
	}
	escalate := r.FindOption("retry-escalate")
	if escalate == nil || escalate.Verb != attention.VerbRunRetryWithEscalation {
		t.Error("retry-escalate option must bind run.retryWithEscalation when attempts remain")
	}
	park := r.FindOption("park")
	if park == nil || park.Verb != attention.VerbNoop {
		t.Error("park option must bind the noop verb")
	}
	if r.Context.Repo != "octocat/acme" || r.Context.Issue != 42 || r.Context.Stage != "feature-dev" {
		t.Errorf("context = %+v, want repo/issue/stage populated", r.Context)
	}
	if r.Context.CostSoFarUSD != 3.25 {
		t.Errorf("CostSoFarUSD = %v, want 3.25", r.Context.CostSoFarUSD)
	}
	assertSteerSet(t, r)
}

// TestProducerTerminalFailureDegradesGracefullyWithoutCost covers #146 not
// having landed yet: an omitted/zero cost must not block the card, and the
// body must not claim a spend that was never observed.
func TestProducerTerminalFailureDegradesGracefullyWithoutCost(t *testing.T) {
	as := newAttentionProducerScheduler(t)
	as.RaiseTerminalFailure("octocat/acme", 43, "pr-merge", "", 0)

	reqs := openRequests(t, as)
	if len(reqs) != 1 {
		t.Fatalf("got %d requests, want 1", len(reqs))
	}
	r := reqs[0]
	if r.Context.CostSoFarUSD != 0 {
		t.Errorf("CostSoFarUSD = %v, want 0", r.Context.CostSoFarUSD)
	}
	if strings.Contains(r.Body, "Cost so far") {
		t.Error("body must not claim a cost figure when none was observed")
	}
	if r.Context.Stage != "pr-merge" {
		t.Errorf("Stage = %q, want pr-merge", r.Context.Stage)
	}
}

// TestProducerTerminalFailureNoEscalationWhenCapReached covers the lifetime
// failure cap: once LifetimeIssueFailures reaches MaxLifetimeFailuresPerIssue,
// offering "retry with escalation" would be dishonest — the next dispatch is
// refused regardless, so only Retry and Park remain.
func TestProducerTerminalFailureNoEscalationWhenCapReached(t *testing.T) {
	as := newAttentionProducerScheduler(t)
	as.mu.Lock()
	as.state.LifetimeIssueFailures = map[string]int{"octocat/acme#44": MaxLifetimeFailuresPerIssue}
	as.mu.Unlock()

	as.RaiseTerminalFailure("octocat/acme", 44, "feature-validate", "gate_failure", 0)

	reqs := openRequests(t, as)
	if len(reqs) != 1 {
		t.Fatalf("got %d requests, want 1", len(reqs))
	}
	r := reqs[0]
	if r.FindOption("retry-escalate") != nil {
		t.Error("retry-escalate must not be offered once the lifetime failure cap is reached")
	}
	if r.FindOption("retry") == nil || r.FindOption("park") == nil {
		t.Error("retry and park must still be offered")
	}
}

// TestTerminalFailureReHaltNeverGrowsTheInbox mirrors
// TestArchitectureApprovalReHaltNeverGrowsTheInbox: repeated raises for the
// same issue collapse onto one card via the idempotency key.
func TestTerminalFailureReHaltNeverGrowsTheInbox(t *testing.T) {
	as := newAttentionProducerScheduler(t)
	for i := 0; i < 3; i++ {
		as.RaiseTerminalFailure("octocat/acme", 45, "feature-dev", "validation_error", 1.0)
	}
	if reqs := openRequests(t, as); len(reqs) != 1 {
		t.Fatalf("got %d requests after 3 re-halts, want 1", len(reqs))
	}
}

// TestReconcileTerminalFailureCardsRetractsOnceResumed exercises the idle-scan
// reconciliation loop directly (#148): the card raised at pause time must
// survive every reconcile call while still halted for that reason, and must
// be gone the instant Resume() clears the pause.
func TestReconcileTerminalFailureCardsRetractsOnceResumed(t *testing.T) {
	as := newAttentionProducerScheduler(t)
	as.state.Status = "running"
	as.Pause("haltQueueOnSlotFailure: issue #46 failed at feature-dev", "haltQueueOnSlotFailure")
	as.RaiseTerminalFailure("octocat/acme", 46, "feature-dev", "validation_error", 2.0)

	// Still halted: repeated reconciles must not retract the card.
	as.reconcileTerminalFailureCards()
	as.reconcileTerminalFailureCards()
	if reqs := openRequests(t, as); len(reqs) != 1 {
		t.Fatalf("card retracted while still halted: got %d requests, want 1", len(reqs))
	}

	// perIssueFailureCount/retryBackoff/etc. are required by Resume()'s reset
	// logic elsewhere in the scheduler; nil maps here are fine since Resume()
	// only ranges over them.
	as.perIssueFailureCount = map[string]int{}
	as.retryBackoff = map[string]retryPlan{}
	as.conflictRestartCount = map[string]int{}
	as.refinementCooldown = map[string]time.Time{}
	as.refinementFailures = map[string]int{}
	as.Resume()
	as.reconcileTerminalFailureCards()

	reqs := openRequests(t, as)
	for _, r := range reqs {
		if r.Producer == producerTerminalFailure {
			t.Errorf("terminal-failure card survived Resume(): %+v", r)
		}
	}
}

// TestReconcileTerminalFailureCardsNilStateRetractsNothing pins the third of
// the three states the reconcile must distinguish (#302, per the #166 rule).
//
// The function's own header states invariant 1 — "I could not look" is never
// "nothing is wrong" — and the List error path honors it. The nil-state path
// did not: `as.state != nil && ...` folds an unreadable state into
// `stillHalted == false`, which is the *legitimately not halted* branch, and
// that branch retracts every open terminal-failure card. The halt is still in
// force; the operator's inbox just goes quiet.
func TestReconcileTerminalFailureCardsNilStateRetractsNothing(t *testing.T) {
	as := newAttentionProducerScheduler(t)
	as.state.Status = "running"
	as.Pause("haltQueueOnSlotFailure: issue #47 failed at feature-validate", "haltQueueOnSlotFailure")
	as.RaiseTerminalFailure("octocat/acme", 47, "feature-validate", "gate_failure", 3.0)

	if reqs := openRequests(t, as); len(reqs) != 1 {
		t.Fatalf("setup: got %d requests, want 1", len(reqs))
	}

	// The state is unreadable — not "resumed". Nothing about the halt changed.
	as.mu.Lock()
	as.state = nil
	as.mu.Unlock()

	out := captureLog(t, func() { as.reconcileTerminalFailureCards() })

	var survived bool
	for _, r := range openRequests(t, as) {
		if r.Producer == producerTerminalFailure {
			survived = true
		}
	}
	if !survived {
		t.Error("nil autonomous state retracted the terminal-failure card — 'I could not look' was treated as 'nothing is wrong'")
	}
	if !strings.Contains(out, "autonomous state unavailable") {
		t.Errorf("the skip is silent — nothing names why the reconcile declined to act; got %q", out)
	}
	// Same volume as the sibling fail-open skips (the zero-roots worktree sweep
	// logs `autonomous: worktree sweep: WARN ...`): an unreadable state that
	// suppresses a whole producer's reconciliation is a warning, not a debug
	// line, and an operator grepping WARN must find it.
	if !strings.Contains(out, "WARN") {
		t.Errorf("a fail-open that suppresses reconciliation is a warning, not a debug line; got %q", out)
	}
}

// TestReconcileTerminalFailureCardsRetractsWhenGenuinelyNotHalted keeps the
// middle state honest: a *readable* state that is no longer halted must still
// retract, or the #302 fix would turn the fail-open into a card that never
// clears.
//
// The un-halt goes through Resume(), the production clearer. Since #405 that
// is the only thing that un-halts a fleet: hand-wiping Status and
// PauseTriggeredBy leaves the latch — and therefore the card — standing, which
// is exactly the property the latch exists to give.
func TestReconcileTerminalFailureCardsRetractsWhenGenuinelyNotHalted(t *testing.T) {
	as := newAttentionProducerScheduler(t)
	as.state.Status = "running"
	as.Pause("haltQueueOnSlotFailure: issue #48 failed at feature-dev", "haltQueueOnSlotFailure")
	as.RaiseTerminalFailure("octocat/acme", 48, "feature-dev", "validation_error", 1.0)

	as.Resume()

	as.reconcileTerminalFailureCards()

	for _, r := range openRequests(t, as) {
		if r.Producer == producerTerminalFailure {
			t.Errorf("card survived a readable, un-halted state: %+v", r)
		}
	}
}

// --- Producer (unnumbered): unverified-deliverable streak (#177) -------------

func TestUnverifiedDeliverableStreakFirstOccurrenceRaisesFYIStreakOne(t *testing.T) {
	s := newAttentionProducerRunScheduler(t)
	s.raiseUnverifiedDeliverableStreak("octocat/acme", deliverable.TierE2E, 42, "run-1", "no framework detected")

	reqs := openRunRequests(t, s)
	if len(reqs) != 1 {
		t.Fatalf("got %d requests, want 1", len(reqs))
	}
	r := reqs[0]
	if r.Producer != producerUnverifiedDeliverableStreak {
		t.Errorf("producer = %q, want %q", r.Producer, producerUnverifiedDeliverableStreak)
	}
	if !r.Standing {
		t.Error("streak card must be Standing")
	}
	if r.Fingerprint != "streak:1" {
		t.Errorf("fingerprint = %q, want streak:1", r.Fingerprint)
	}
	if r.Severity != attention.SeverityFYI {
		t.Errorf("severity = %q, want fyi at streak 1", r.Severity)
	}
	assertSteerSet(t, r)
}

// TestUnverifiedDeliverableStreakReoccurrenceUpdatesInPlace asserts a second
// consecutive occurrence for the same (repo, tier) updates the existing card
// rather than duplicating it, and escalates the fingerprint to streak:2 while
// staying at FYI severity (escalation only crosses into blocking_run at 3+).
func TestUnverifiedDeliverableStreakReoccurrenceUpdatesInPlace(t *testing.T) {
	s := newAttentionProducerRunScheduler(t)
	s.raiseUnverifiedDeliverableStreak("octocat/acme", deliverable.TierE2E, 42, "run-1", "no framework detected")
	s.raiseUnverifiedDeliverableStreak("octocat/acme", deliverable.TierE2E, 43, "run-2", "no framework detected")

	reqs := openRunRequests(t, s)
	if len(reqs) != 1 {
		t.Fatalf("got %d requests after 2 occurrences, want 1 (in-place update): %+v", len(reqs), reqs)
	}
	r := reqs[0]
	if r.Fingerprint != "streak:2" {
		t.Errorf("fingerprint = %q, want streak:2", r.Fingerprint)
	}
	if r.Severity != attention.SeverityFYI {
		t.Errorf("severity = %q, want fyi at streak 2", r.Severity)
	}
}

// TestUnverifiedDeliverableStreakEscalatesAtThreeOccurrences asserts the
// severity ladder crosses into blocking_run (never blocking_fleet) once the
// streak reaches 3 consecutive occurrences.
func TestUnverifiedDeliverableStreakEscalatesAtThreeOccurrences(t *testing.T) {
	s := newAttentionProducerRunScheduler(t)
	for i, issue := range []int{42, 43, 44} {
		s.raiseUnverifiedDeliverableStreak("octocat/acme", deliverable.TierUnit, issue, fmt.Sprintf("run-%d", i), "reason")
	}

	reqs := openRunRequests(t, s)
	if len(reqs) != 1 {
		t.Fatalf("got %d requests after 3 occurrences, want 1", len(reqs))
	}
	r := reqs[0]
	if r.Fingerprint != "streak:3" {
		t.Errorf("fingerprint = %q, want streak:3", r.Fingerprint)
	}
	if r.Severity != attention.SeverityBlockingRun {
		t.Errorf("severity = %q, want blocking_run at streak 3", r.Severity)
	}
}

// TestUnverifiedDeliverableStreakResetsOnExecution asserts a tier executing
// resets the streak: the next occurrence starts back at streak:1, not resumes
// from where it left off.
func TestUnverifiedDeliverableStreakResetsOnExecution(t *testing.T) {
	s := newAttentionProducerRunScheduler(t)
	s.raiseUnverifiedDeliverableStreak("octocat/acme", deliverable.TierIntegration, 42, "run-1", "reason")
	s.raiseUnverifiedDeliverableStreak("octocat/acme", deliverable.TierIntegration, 43, "run-2", "reason")

	s.resolveUnverifiedDeliverableStreak("octocat/acme", deliverable.TierIntegration)

	if reqs := openRunRequests(t, s); len(reqs) != 0 {
		t.Fatalf("card survived resolveUnverifiedDeliverableStreak: got %d open requests, want 0", len(reqs))
	}

	s.raiseUnverifiedDeliverableStreak("octocat/acme", deliverable.TierIntegration, 44, "run-3", "reason")
	reqs := openRunRequests(t, s)
	if len(reqs) != 1 {
		t.Fatalf("got %d requests after reset+re-raise, want 1", len(reqs))
	}
	if reqs[0].Fingerprint != "streak:1" {
		t.Errorf("fingerprint after reset = %q, want streak:1 (not resumed)", reqs[0].Fingerprint)
	}
}

// TestUnverifiedDeliverableStreakResolveIsScopedToOneTier asserts
// resolveUnverifiedDeliverableStreak only retracts the (repo, tier) key it
// was called with, leaving other open tiers' streak cards for the same repo
// untouched — the exact mistake AutoResolveUnobserved would make here.
func TestUnverifiedDeliverableStreakResolveIsScopedToOneTier(t *testing.T) {
	s := newAttentionProducerRunScheduler(t)
	s.raiseUnverifiedDeliverableStreak("octocat/acme", deliverable.TierUnit, 42, "run-1", "reason")
	s.raiseUnverifiedDeliverableStreak("octocat/acme", deliverable.TierE2E, 42, "run-1", "reason")

	s.resolveUnverifiedDeliverableStreak("octocat/acme", deliverable.TierUnit)

	reqs := openRunRequests(t, s)
	if len(reqs) != 1 {
		t.Fatalf("got %d open requests, want 1 (e2e streak untouched)", len(reqs))
	}
	if reqs[0].IdempotencyKey != keyUnverifiedDeliverableStreak("octocat/acme", deliverable.TierE2E) {
		t.Errorf("surviving card key = %q, want the e2e streak card", reqs[0].IdempotencyKey)
	}
}

// Acknowledging the streak card must not reset the streak (#243).
//
// #242 recovered the count by parsing the Fingerprint of an OPEN card, and
// ListFilter excludes terminal requests by default — so resolving the card
// sent the next occurrence back to streak:1. Because the card escalates to
// blocking_run at 3 and both its options are noop verbs that resolve it, the
// only action that unblocks a run was also the one that erased the evidence,
// capping the count at 3 forever.
func TestUnverifiedDeliverableStreakSurvivesAcknowledgement(t *testing.T) {
	s := newAttentionProducerRunScheduler(t)
	repo, tier := "octocat/acme", deliverable.TierUnit

	for i, issue := range []int{42, 43, 44} {
		s.raiseUnverifiedDeliverableStreak(repo, tier, issue, fmt.Sprintf("run-%d", i), "reason")
	}

	reqs := openRunRequests(t, s)
	if len(reqs) != 1 {
		t.Fatalf("got %d requests, want 1", len(reqs))
	}
	if _, err := s.attention.Resolve(
		context.Background(), reqs[0].ID, "acknowledged", "operator", "", "", nil,
	); err != nil {
		t.Fatalf("Resolve: %v", err)
	}

	// The tier still has not executed, so the fourth skip continues the streak.
	s.raiseUnverifiedDeliverableStreak(repo, tier, 45, "run-3", "reason")

	key := keyUnverifiedDeliverableStreak(repo, tier)

	// Assert the operator-visible observable first: the card must report the
	// continued count, not restart. This is what made the defect self-defeating
	// — at 3 the card blocks a run, and clearing it to proceed sent the next
	// card back to streak:1, capping the count at the threshold forever.
	var card *attention.DecisionRequest
	for _, r := range openRunRequests(t, s) {
		if r.IdempotencyKey == key {
			c := r
			card = &c
			break
		}
	}
	if card == nil {
		t.Fatal("no open streak card after the fourth occurrence")
	}
	if card.Fingerprint != "streak:4" {
		t.Errorf("card fingerprint = %q, want streak:4 (a re-raise after acknowledgement must continue, not restart)", card.Fingerprint)
	}
	if card.Severity != attention.SeverityBlockingRun {
		t.Errorf("severity = %q, want blocking_run to persist past an acknowledgement at count 4", card.Severity)
	}

	if got := s.attention.StreakCount(key); got != 4 {
		t.Errorf("streak after acknowledgement = %d, want 4 (acknowledging a card must not rewrite history)", got)
	}
}

// An expired streak card must not reset the streak either (#243). Occurrences
// spaced further apart than ExpiresAt are exactly the slow-burn case #177 cares
// about, where cost accrues against N files before the first execution.
func TestUnverifiedDeliverableStreakSurvivesCardExpiry(t *testing.T) {
	s := newAttentionProducerRunScheduler(t)
	repo, tier := "octocat/acme", deliverable.TierE2E
	key := keyUnverifiedDeliverableStreak(repo, tier)

	s.raiseUnverifiedDeliverableStreak(repo, tier, 42, "run-0", "reason")
	s.raiseUnverifiedDeliverableStreak(repo, tier, 43, "run-1", "reason")

	// Simulate the card aging out: no open card remains, but the tier has still
	// never executed.
	for _, r := range openRunRequests(t, s) {
		if r.IdempotencyKey == key {
			if _, err := s.attention.Resolve(
				context.Background(), r.ID, "acknowledged", "expiry", "", "", nil,
			); err != nil {
				t.Fatalf("Resolve: %v", err)
			}
		}
	}

	s.raiseUnverifiedDeliverableStreak(repo, tier, 44, "run-2", "reason")

	if got := s.attention.StreakCount(key); got != 3 {
		t.Fatalf("streak after card no longer open = %d, want 3", got)
	}
}

// Executing the tier is the one thing that clears the count — and it must clear
// it even when no card is open to retract, since the operator may already have
// acknowledged it (#243).
func TestUnverifiedDeliverableStreakResetsOnExecutionWithNoOpenCard(t *testing.T) {
	s := newAttentionProducerRunScheduler(t)
	repo, tier := "octocat/acme", deliverable.TierUnit
	key := keyUnverifiedDeliverableStreak(repo, tier)

	s.raiseUnverifiedDeliverableStreak(repo, tier, 42, "run-0", "reason")
	s.raiseUnverifiedDeliverableStreak(repo, tier, 43, "run-1", "reason")

	for _, r := range openRunRequests(t, s) {
		if r.IdempotencyKey == key {
			if _, err := s.attention.Resolve(
				context.Background(), r.ID, "acknowledged", "operator", "", "", nil,
			); err != nil {
				t.Fatalf("Resolve: %v", err)
			}
		}
	}

	s.resolveUnverifiedDeliverableStreak(repo, tier)

	if got := s.attention.StreakCount(key); got != 0 {
		t.Fatalf("streak after execution = %d, want 0", got)
	}
	s.raiseUnverifiedDeliverableStreak(repo, tier, 44, "run-2", "reason")
	if got := s.attention.StreakCount(key); got != 1 {
		t.Fatalf("streak after execution then one skip = %d, want 1", got)
	}
}

// One tier's streak must not disturb another's.
func TestUnverifiedDeliverableStreakCountsAreScopedPerTier(t *testing.T) {
	s := newAttentionProducerRunScheduler(t)
	repo := "octocat/acme"

	s.raiseUnverifiedDeliverableStreak(repo, deliverable.TierUnit, 42, "run-0", "reason")
	s.raiseUnverifiedDeliverableStreak(repo, deliverable.TierUnit, 43, "run-1", "reason")
	s.raiseUnverifiedDeliverableStreak(repo, deliverable.TierE2E, 44, "run-2", "reason")

	s.resolveUnverifiedDeliverableStreak(repo, deliverable.TierUnit)

	if got := s.attention.StreakCount(keyUnverifiedDeliverableStreak(repo, deliverable.TierUnit)); got != 0 {
		t.Errorf("unit streak = %d, want 0 after execution", got)
	}
	if got := s.attention.StreakCount(keyUnverifiedDeliverableStreak(repo, deliverable.TierE2E)); got != 1 {
		t.Errorf("e2e streak = %d, want 1 (untouched by the unit tier executing)", got)
	}
}

// --- #305: the Go call sites ARE the shared builders ------------------------

// buildersIdentity renders the fields that must match across the Go and IPC
// paths, through JSON so a persisted record (whose option Args have been
// through a JSON round-trip) compares equal to an in-memory expectation.
func buildersIdentity(t *testing.T, r attention.DecisionRequest) string {
	t.Helper()
	r.ID = ""
	r.CreatedAt = ""
	r.ExpiresAt = ""
	r.SchemaVersion = attention.SchemaVersion
	r.Lifecycle = attention.Lifecycle{State: attention.StateOpen}
	raw, err := json.Marshal(r)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var norm any
	if err := json.Unmarshal(raw, &norm); err != nil {
		t.Fatalf("normalize: %v", err)
	}
	out, err := json.MarshalIndent(norm, "", "  ")
	if err != nil {
		t.Fatalf("re-marshal: %v", err)
	}
	return string(out)
}

// TestRunScopedProducersDelegateToSharedBuilders is the Go half of the #305
// parity contract. The IPC raise verb builds its card by calling
// orchestrator.BuildBudgetCeilingHit / BuildBranchProtectionBlock; this asserts
// the SCHEDULER call site persists exactly what those builders produce and
// nothing else.
//
// Together the two halves mean the extension path and the Go path cannot drift:
// there is one builder per producer, and both call sites are pinned to it. A
// future edit that "just tweaks the title" at the scheduler call site turns
// this red instead of silently giving the operator two different cards for one
// condition depending on which surface saw it.
func TestRunScopedProducersDelegateToSharedBuilders(t *testing.T) {
	t.Run("budget-ceiling", func(t *testing.T) {
		s := newAttentionProducerRunScheduler(t)
		s.raiseBudgetCeilingHit("octocat/acme", 42, "run-1", 12.5, 25.0)

		reqs := openRunRequests(t, s)
		if len(reqs) != 1 {
			t.Fatalf("got %d requests, want 1", len(reqs))
		}
		want := BuildBudgetCeilingHit("octocat/acme", 42, "run-1", 12.5, 25.0)
		if got, wantJSON := buildersIdentity(t, reqs[0]), buildersIdentity(t, want); got != wantJSON {
			t.Errorf("scheduler card != BuildBudgetCeilingHit\n got: %s\nwant: %s", got, wantJSON)
		}
	})

	t.Run("branch-protection", func(t *testing.T) {
		s := newAttentionProducerRunScheduler(t)
		const reason = "review-not-approved: REVIEW_REQUIRED"
		s.raiseBranchProtectionBlock("octocat/acme", 42, 99, "run-1", reason)

		reqs := openRunRequests(t, s)
		if len(reqs) != 1 {
			t.Fatalf("got %d requests, want 1", len(reqs))
		}
		want := BuildBranchProtectionBlock("octocat/acme", 42, 99, "run-1", reason)
		if got, wantJSON := buildersIdentity(t, reqs[0]), buildersIdentity(t, want); got != wantJSON {
			t.Errorf("scheduler card != BuildBranchProtectionBlock\n got: %s\nwant: %s", got, wantJSON)
		}
	})
}

// TestProposedCeilingUSDIsOneRule pins the arithmetic BOTH paths use to compute
// the ceiling the card offers. Before #305 it lived inline at the scheduler
// call site, which is why the extension had nothing to reuse and would have had
// to re-derive it in TypeScript — two implementations of one number, with
// nothing that fails when they disagree.
func TestProposedCeilingUSDIsOneRule(t *testing.T) {
	cases := []struct {
		ceiling, spent, want float64
	}{
		// A 50% raise above the enforced ceiling when spend is under it.
		{ceiling: 10, spent: 8, want: 15},
		// A between-stage overrun that the plain raise already clears.
		{ceiling: 10, spent: 12.5, want: 15},
		// Exactly at the proposal is still not ABOVE it, so the floor applies.
		{ceiling: 10, spent: 15, want: 22.5},
		// A run that blew far past its ceiling: offering a ceiling below the
		// bill would be an offer that cannot help.
		{ceiling: 10, spent: 100, want: 150},
	}
	for _, tc := range cases {
		if got := ProposedCeilingUSD(tc.ceiling, tc.spent); got != tc.want {
			t.Errorf("ProposedCeilingUSD(%v, %v) = %v, want %v", tc.ceiling, tc.spent, got, tc.want)
		}
		if got := ProposedCeilingUSD(tc.ceiling, tc.spent); got <= tc.spent {
			t.Errorf("ProposedCeilingUSD(%v, %v) = %v, which is not above the spend", tc.ceiling, tc.spent, got)
		}
	}
}

// TestProducerAbandonedDispatchIsAnInformationalStopCard covers the one
// producer with no Go counterpart (#307's force-clear funnel). Keyed per
// (repo, issue) so a slot that wedges repeatedly collapses onto one card via
// Store.Raise's open-record dedup, instead of one card per force-clear.
//
// INFORMATIONAL, not an unblock, because the whole population is an operator
// Stop: `forceClearStuckSlots` is reached only from `abortAll`'s deadline, and
// `abortAll` only from stopPipeline / abortPipeline / deactivate. A
// blocking_run card recommending a re-dispatch would tell the operator to undo
// their own Stop, at a severity ADR-015 §I routes to alerting.
func TestProducerAbandonedDispatchIsAnInformationalStopCard(t *testing.T) {
	r := BuildAbandonedDispatch("octocat/acme", 42, "run-1", "feature-dev", AbandonedSlotWorktreePreserved)

	if r.Producer != ProducerAbandonedDispatch {
		t.Errorf("producer = %q, want %q", r.Producer, ProducerAbandonedDispatch)
	}
	if r.Kind != attention.KindApprove || r.Severity != attention.SeverityFYI {
		t.Errorf("kind/severity = %q/%q, want approve/fyi — an operator's own Stop blocks nothing",
			r.Kind, r.Severity)
	}
	// EVENT-SHAPED. The force-clear funnel observes a transition once; it does
	// not re-answer "is this dispatch abandoned?" on a loop, which is the test
	// docs/ATTENTION_PRODUCERS.md states. Declaring Standing would also opt this
	// producer into ADR-015 §M suppression — and with no fingerprint that can
	// move and no auto-resolve call site, the first human resolution would
	// silence the (repo, issue) key permanently. See
	// TestAbandonedDispatchReRaisesAfterAHumanResolution in internal/ipc.
	if r.Standing || r.Fingerprint != "" {
		t.Errorf("standing=%v fingerprint=%q: abandoned-dispatch is an EVENT — standing here "+
			"inherits §M suppression it has no way to lapse", r.Standing, r.Fingerprint)
	}
	if want := "abandoned-dispatch:octocat/acme#42"; r.IdempotencyKey != want {
		t.Errorf("idempotency_key = %q, want %q (per-issue, NOT per force-clear generation)", r.IdempotencyKey, want)
	}
	// A second force-clear of the same issue must be the SAME key, or a wedged
	// slot that keeps re-wedging buries the inbox.
	again := BuildAbandonedDispatch("octocat/acme", 42, "run-2", "pr-create", AbandonedSlotWorktreePreserved)
	if again.IdempotencyKey != r.IdempotencyKey {
		t.Errorf("repeat force-clear changed identity: key %q→%q", r.IdempotencyKey, again.IdempotencyKey)
	}
	// NO RETRY, and no primary anything: every option is a noop acknowledgement.
	// Re-dispatching work the operator just cancelled is not a remedy, and the
	// verb pair the first cut used (clearIssueFailures + a non-blocking
	// TriggerRescan poke) did nothing at all with the autonomous loop stopped —
	// the ordinary state after a manual Stop.
	if r.FindOption("retry") != nil {
		t.Error("abandoned-dispatch must not offer `retry`: it re-dispatches deliberately-cancelled work")
	}
	for _, o := range r.Options {
		if o.Verb != attention.VerbNoop {
			t.Errorf("option %q binds %q, want noop", o.ID, o.Verb)
		}
		if o.Style == attention.StylePrimary {
			t.Errorf("option %q is StylePrimary — this card recommends no action", o.ID)
		}
	}
	if r.DefaultAction != attention.ExpireNoop {
		t.Errorf("default_action = %q, want %q — expiry must not silently retry a wedged issue",
			r.DefaultAction, attention.ExpireNoop)
	}
	// The two facts worth an operator's attention survive the re-shape.
	for _, want := range []string{"stopped the pipeline", "worktree is PRESERVED", "may be stale"} {
		if !strings.Contains(r.Body, want) {
			t.Errorf("body does not mention %q", want)
		}
	}
	assertSteerSet(t, r)

	// An unknown stage degrades to a named placeholder rather than an empty
	// gap in the title.
	if noStage := BuildAbandonedDispatch("octocat/acme", 42, "", "", AbandonedSlotWorktreePreserved); noStage.Context.Stage != "unknown" {
		t.Errorf("empty stage = %q, want %q", noStage.Context.Stage, "unknown")
	}
	// No run id is a HANDLED case: the force-clear caller often has none.
	if noRun := BuildAbandonedDispatch("octocat/acme", 42, "", "feature-dev", AbandonedSlotWorktreePreserved); noRun.Context.TraceRef != nil {
		t.Error("an empty runId must produce no trace back-reference, not a synthetic one")
	}
}

// TestAbandonedDispatchBodySaysOnlyWhatIsTrueOfItsSituation is the round-4
// pin for the one-body-three-situations defect.
//
// The force-clear funnel has two arms and two booking outcomes, and round 3
// printed a single fmt.Sprintf for all of them. It promised a preserved
// worktree that "may hold uncommitted work" to a dispatch that wedged before
// any worktree existed, and "NOTHING IS BLOCKED and no action is required" to
// one still holding the Go scheduler's seat. Each body is now asserted to
// CARRY its own facts and to NOT carry the other situations' — the second half
// is what a shared body would break.
func TestAbandonedDispatchBodySaysOnlyWhatIsTrueOfItsSituation(t *testing.T) {
	cases := []struct {
		situation   AbandonedDispatchSituation
		stage       string
		mustSay     []string
		mustNotSay  []string
		wantStage   string
		titleHas    string
		titleHasNot string
	}{
		{
			situation: AbandonedReservationNeverStarted,
			stage:     "", // the reservation arm has no stage to report
			mustSay: []string{
				"NO STAGE RAN",
				"NOTHING IS BLOCKED and no action is required",
				"nightgauge worktree sweep",
			},
			mustNotSay: []string{
				// Both were false here and both were printed.
				"may hold uncommitted work",
				"The last stage it was seen in",
			},
			wantStage:   "",
			titleHas:    "before any stage started",
			titleHasNot: "unknown",
		},
		{
			situation: AbandonedSlotWorktreePreserved,
			stage:     "feature-dev",
			mustSay: []string{
				"worktree is PRESERVED",
				"may hold uncommitted work",
				"NOTHING IS BLOCKED and no action is required",
				"The last stage it was seen in is feature-dev",
			},
			mustNotSay: []string{"SOMETHING IS STILL HELD"},
			wantStage:  "feature-dev",
			titleHas:   "worktree preserved",
		},
		{
			situation: AbandonedClaimTakenThenWedged,
			stage:     "pr-create",
			mustSay: []string{
				"SOMETHING IS STILL HELD",
				"running-slot seat for #42 was NOT released",
				"autonomous scheduler restarts",
				"worktree is PRESERVED",
			},
			mustNotSay: []string{
				// The exact sentence round 3 showed in the one case where an
				// action WAS required.
				"NOTHING IS BLOCKED and no action is required",
			},
			wantStage: "pr-create",
			titleHas:  "terminal bookkeeping is still owed",
		},
	}

	for _, tc := range cases {
		t.Run(string(tc.situation), func(t *testing.T) {
			r := BuildAbandonedDispatch("octocat/acme", 42, "run-1", tc.stage, tc.situation)
			for _, want := range tc.mustSay {
				if !strings.Contains(r.Body, want) {
					t.Errorf("body for %s does not say %q", tc.situation, want)
				}
			}
			for _, never := range tc.mustNotSay {
				if strings.Contains(r.Body, never) {
					t.Errorf("body for %s says %q, which is not true of this situation", tc.situation, never)
				}
			}
			if !strings.Contains(r.Title, tc.titleHas) {
				t.Errorf("title %q does not contain %q", r.Title, tc.titleHas)
			}
			if tc.titleHasNot != "" && strings.Contains(r.Title, tc.titleHasNot) {
				t.Errorf("title %q contains %q", r.Title, tc.titleHasNot)
			}
			if r.Context.Stage != tc.wantStage {
				t.Errorf("context.stage = %q, want %q", r.Context.Stage, tc.wantStage)
			}
			// The situation selects PROSE ONLY. Everything a card can DO must be
			// identical across all three, or a caller-chosen enum would be
			// choosing an operation — the boundary AttentionRaiseParams exists to
			// hold.
			if want := "abandoned-dispatch:octocat/acme#42"; r.IdempotencyKey != want {
				t.Errorf("idempotency_key = %q, want %q — the situation must not fork identity", r.IdempotencyKey, want)
			}
			if r.Kind != attention.KindApprove || r.Severity != attention.SeverityFYI {
				t.Errorf("kind/severity = %q/%q, want approve/fyi in every situation", r.Kind, r.Severity)
			}
			if len(r.Options) != 2 {
				t.Fatalf("options = %d, want 2 in every situation", len(r.Options))
			}
			for _, o := range r.Options {
				if o.Verb != attention.VerbNoop {
					t.Errorf("option %q binds %q, want noop in every situation", o.ID, o.Verb)
				}
			}
			if r.Options[0].ID != "acknowledged" || r.Options[1].ID != "will-inspect" {
				t.Errorf("option ids = %q/%q, want acknowledged/will-inspect — stable ids let one key's "+
					"successive raises share a record", r.Options[0].ID, r.Options[1].ID)
			}
		})
	}
}

// TestAbandonedDispatchSituationsAreClosed pins the enum the IPC boundary
// validates against to the set the builder actually switches on.
func TestAbandonedDispatchSituationsAreClosed(t *testing.T) {
	declared := AbandonedDispatchSituations()
	if len(declared) != 3 {
		t.Fatalf("AbandonedDispatchSituations() = %v, want the three declared situations", declared)
	}
	for _, s := range declared {
		if !IsAbandonedDispatchSituation(s) {
			t.Errorf("IsAbandonedDispatchSituation(%q) = false for a declared situation", s)
		}
	}
	for _, s := range []string{"", "slot", "reservation", "SLOT-WORKTREE-PRESERVED", "unknown"} {
		if IsAbandonedDispatchSituation(s) {
			t.Errorf("IsAbandonedDispatchSituation(%q) = true — the set must be closed", s)
		}
	}
	// Bodies must be distinct: a situation that fell through to a shared
	// default would pass every "must say" assertion above and still be the
	// defect this parameter exists to fix.
	seen := map[string]string{}
	for _, s := range declared {
		r := BuildAbandonedDispatch("octocat/acme", 7, "", "feature-dev", AbandonedDispatchSituation(s))
		if prior, dup := seen[r.Body]; dup {
			t.Errorf("situations %q and %q produce the identical body", prior, s)
		}
		seen[r.Body] = s
	}
}

// TestIsBranchProtectionPuntMatchesDecideReasons pins the gate to the reason
// strings stages.Decide actually emits — the predicate and its inputs are now
// used from two packages, so a rename on either side must fail here rather than
// silently stop carding branch-protection blocks.
func TestIsBranchProtectionPuntMatchesDecideReasons(t *testing.T) {
	blocking := []pmstages.PRViewSnapshot{
		{State: "OPEN", Mergeable: "MERGEABLE", MergeStateStatus: "CLEAN", ReviewDecision: "REVIEW_REQUIRED"},
		{State: "OPEN", Mergeable: "MERGEABLE", MergeStateStatus: "CLEAN", ReviewDecision: "CHANGES_REQUESTED"},
		{State: "OPEN", Mergeable: "CONFLICTING", MergeStateStatus: "DIRTY"},
		{State: "OPEN", Mergeable: "MERGEABLE", MergeStateStatus: "BLOCKED"},
		{State: "OPEN", Mergeable: "MERGEABLE", MergeStateStatus: "CLEAN",
			StatusCheckRollup: []pmstages.PRStatusCheckRow{{Name: "ci", Conclusion: "FAILURE"}}},
	}
	for _, snap := range blocking {
		d := pmstages.Decide(snap)
		if !d.Punt {
			t.Fatalf("Decide(%+v) did not punt", snap)
		}
		if !IsBranchProtectionPunt(d.Reason) {
			t.Errorf("IsBranchProtectionPunt(%q) = false, want true", d.Reason)
		}
	}

	notBlocking := []pmstages.PRViewSnapshot{
		{State: "MERGED"},
		{State: "CLOSED"},
		{State: "OPEN", Mergeable: "MERGEABLE", MergeStateStatus: "CLEAN", ReviewDecision: "APPROVED"},
	}
	for _, snap := range notBlocking {
		if d := pmstages.Decide(snap); IsBranchProtectionPunt(d.Reason) {
			t.Errorf("IsBranchProtectionPunt(%q) = true for %+v, want false", d.Reason, snap)
		}
	}

	// IsBranchProtectionPunt over bare Decide() is NOT the whole gate, and this
	// is the trap: a PR whose only blocker is a queued required check decides
	// `dirty-merge-state: BLOCKED`, which IS a branch-protection punt by
	// prefix — yet the Go runner never reaches that punt, because
	// DeterministicRunner.Run tests MergeBlockedByPendingCI first and waits.
	// Any surface reusing this predicate must apply the same precondition or it
	// cards in-flight CI as a human-needed block (#297/#305).
	pendingCI := []pmstages.PRViewSnapshot{
		{State: "OPEN", Mergeable: "MERGEABLE", MergeStateStatus: "BLOCKED",
			StatusCheckRollup: []pmstages.PRStatusCheckRow{{Name: "ci", Conclusion: ""}}},
		{State: "OPEN", Mergeable: "MERGEABLE", MergeStateStatus: "UNSTABLE",
			StatusCheckRollup: []pmstages.PRStatusCheckRow{{Name: "ci", Conclusion: "PENDING"}}},
		// #1027: no check run created yet — pr-merge's first snapshot. Same
		// trap, one step earlier.
		{State: "OPEN", Mergeable: "MERGEABLE", MergeStateStatus: "BLOCKED"},
	}
	for _, snap := range pendingCI {
		d := pmstages.Decide(snap)
		if !IsBranchProtectionPunt(d.Reason) {
			t.Errorf("premise changed: Decide(%+v) = %q is no longer a branch-protection punt, "+
				"so the pending-CI precondition below is testing nothing", snap, d.Reason)
		}
		if !pmstages.MergeBlockedByPendingCI(snap) {
			t.Errorf("MergeBlockedByPendingCI(%+v) = false, want true — in-flight CI would be "+
				"carded as branch protection", snap)
		}
	}

	// The wait outcomes the runner punts AFTER the predicate held are not
	// branch protection either: they mean CI never concluded / never started
	// within budget, and no human can "fix the failing check" that implies.
	for _, reason := range []string{pmstages.ReasonCIWaitTimeout, pmstages.ReasonNoChecksCreated} {
		if IsBranchProtectionPunt(reason) {
			t.Errorf("IsBranchProtectionPunt(%q) = true, want false — a CI-wait outcome is not a human-needed block", reason)
		}
	}
}
