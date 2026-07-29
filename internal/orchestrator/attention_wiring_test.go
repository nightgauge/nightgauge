package orchestrator

import (
	"fmt"
	"testing"
	"time"

	"github.com/nightgauge/nightgauge/internal/attention"
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
