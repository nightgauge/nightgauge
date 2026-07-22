package orchestrator

import (
	"testing"

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
