package orchestrator

import (
	"testing"
	"time"
)

// The GLOBAL cooldown half of #1545, pinned at the site that actually applies
// it. The decision itself is unit-tested in cap_recovery_test.go; these tests
// answer the operator's question instead — did the fleet stop?
//
// The observed harm: one Fable cap on one repo suspended dispatch for EVERY
// repo for an hour, with opus, sonnet, haiku, codex and grok all available.
// The cooldown is not removed by this issue — an account that really is out of
// quota must still stop the fleet, and the second test below is that guarantee.
// What changes is WHEN it is reached: only after the tier ladder and the
// provider walk have both been spent, which is the only evidence the account
// itself is out rather than one model's window.

// TestAnAttributedCapDoesNotSuspendEveryRepo is acceptance criterion 1 and 2 at
// the fleet level. Once the rejection is attributed to the model in flight, the
// run continues on a weaker tier and the terminal kind the scheduler reports is
// model_unavailable — which takes the per-issue backoff branch and writes NO
// global cooldown.
func TestAnAttributedCapDoesNotSuspendEveryRepo(t *testing.T) {
	as := newAutonomousForCascadeTest(t, 3, 30*time.Minute)
	as.state.QuotaCooldownUntil = ""
	as.state.QuotaCooldownReason = ""

	addRunning(as, "acme/dashboard", 96, "fable cap, ladder still had rungs")
	as.onPipelineComplete("acme/dashboard", 96, false, false,
		TerminalKindModelUnavailable, capRejectionMarker)
	as.drainBackground()

	if as.state.QuotaCooldownUntil != "" {
		t.Fatalf("an attributed cap wrote a GLOBAL cooldown (%q, %q) — this is the #1545 harm: "+
			"one repo's model cap must not suspend dispatch for every other repo",
			as.state.QuotaCooldownUntil, as.state.QuotaCooldownReason)
	}
	// It is still a real, recorded failure with its own per-issue backoff — the
	// issue is deferred, not the fleet.
	if _, ok := as.retryBackoff["acme/dashboard#96"]; !ok {
		t.Error("the capped issue must still take its own backoff")
	}
	if got := as.state.LifetimeIssueFailures["acme/dashboard#96"]; got != 0 {
		t.Errorf("lifetime failures = %d, want 0 — an upstream cap is not the issue's fault", got)
	}
}

// TestASurvivingAccountWideRejectionStillSuspendsTheFleet is acceptance
// criterion 3 and the regression guard the issue names explicitly. A rejection
// that outlived the tier ladder and an empty adapter_fallback_chain arrives at
// the autonomous scheduler still carrying rate_limit_quota_exhausted, and the
// global cooldown fires exactly as it always did — now as a last resort rather
// than a first move.
func TestASurvivingAccountWideRejectionStillSuspendsTheFleet(t *testing.T) {
	as := newAutonomousForCascadeTest(t, 3, 30*time.Minute)
	as.state.QuotaCooldownUntil = ""

	addRunning(as, "acme/dashboard", 96, "ladder and chain both exhausted")
	as.onPipelineComplete("acme/dashboard", 96, false, false,
		TerminalKindRateLimitQuotaExhausted, capRejectionMarker)
	as.drainBackground()

	if as.state.QuotaCooldownUntil == "" {
		t.Fatal("a surviving account-wide rejection wrote no global cooldown — " +
			"deferring the cooldown must never mean deleting it")
	}
	// The marker's own resetsAt is what the cooldown is keyed to, so the fleet
	// resumes when the bucket actually resets rather than on a blind floor.
	until, err := time.Parse(time.RFC3339, as.state.QuotaCooldownUntil)
	if err != nil {
		t.Fatalf("cooldown timestamp %q is unparseable: %v", as.state.QuotaCooldownUntil, err)
	}
	if !until.After(time.Now()) {
		t.Fatalf("cooldown %s is not in the future", until)
	}
}
