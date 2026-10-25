package orchestrator

import (
	"fmt"
	"testing"
	"time"

	"github.com/nightgauge/nightgauge/internal/terminalkind"
)

// #2551: core#119 ended BLOCKED because the Codex account the issue
// investigated was out of quota. The stage quoted Codex's usage-limit text as
// evidence, the extension's signal ladder read that quote as
// rate_limit_quota_exhausted, and the scheduler applied the GLOBAL one-hour
// cooldown over one issue's verdict. The Anthropic quota was fine throughout.

const issue119Detail = `BLOCKED — PR unmerged: out-of-scope: PLAN_REVISION_NEEDED. Remediation: ` +
	`feature-planning probed codex exec: "You've hit your usage limit. Upgrade to Pro … or try again at ` +
	`Nov 16th, 2026" — external-blocker: the Codex account's usage limit`

func newBlockedQuotaScheduler(t *testing.T) *AutonomousScheduler {
	t.Helper()
	stubReconcileGhUnreachable(t)
	as := newAutonomousForCascadeTest(t, 3, 30*time.Minute)
	as.state.LifetimeIssueFailures = map[string]int{}
	as.perIssueFailureCount = map[string]int{}
	as.retryBackoff = map[string]retryPlan{}
	return as
}

// The table itself still classifies #119's text as a quota failure — the
// `~usage limit` extension exists for raw adapter stderr and cannot see where
// text came from. That is why the guard sits at the consumer.
func TestBlockedQuota_SignalLadderStillReadsQuoteAsQuota(t *testing.T) {
	if got := terminalkind.SignalKind(issue119Detail); got != TerminalKindRateLimitQuotaExhausted {
		t.Fatalf("SignalKind(#119 text) = %q, want %q — if this changed, revisit the consumer guard", got, TerminalKindRateLimitQuotaExhausted)
	}
}

func TestBlockedQuota_BlockedOutcomeAppliesNoGlobalCooldown(t *testing.T) {
	as := newBlockedQuotaScheduler(t)
	addRunning(as, "acme/core", 119, "investigate codex")
	as.NotifyComplete("acme/core", 119, false, false, TerminalKindRateLimitQuotaExhausted, issue119Detail)
	as.drainBackground()

	key := "acme/core#119"
	if as.state.QuotaCooldownUntil != "" {
		t.Errorf("QuotaCooldownUntil = %q, want empty — a BLOCKED verdict must not suspend the fleet", as.state.QuotaCooldownUntil)
	}
	if got := as.state.LifetimeIssueFailures[key]; got != 0 {
		t.Errorf("LifetimeIssueFailures[%q] = %d, want 0", key, got)
	}
	if _, ok := retryDeadline(as, key); ok {
		t.Errorf("retryBackoff[%q] set — a held BLOCKED outcome must not be retried", key)
	}
	if got := failedKind(t, as, "acme/core", 119); got != TerminalKindBlockedOutcomeHold {
		t.Errorf("recorded kind = %q, want %q", got, TerminalKindBlockedOutcomeHold)
	}
	if HoldForTerminalKind(TerminalKindBlockedOutcomeHold) == HoldNone {
		t.Error("the held BLOCKED outcome must not be re-admitted by the graph reconcile unaided")
	}
}

func TestBlockedQuota_NotPipelineActionableQuotingUsageLimit(t *testing.T) {
	as := newBlockedQuotaScheduler(t)
	addRunning(as, "acme/core", 120, "legal review")
	detail := "[not-pipeline-actionable] deliverable needs the operator; codex said: You've hit your usage limit"
	as.NotifyComplete("acme/core", 120, false, false, TerminalKindRateLimitQuotaExhausted, detail)
	as.drainBackground()

	if as.state.QuotaCooldownUntil != "" {
		t.Errorf("QuotaCooldownUntil = %q, want empty", as.state.QuotaCooldownUntil)
	}
	if got := failedKind(t, as, "acme/core", 120); got != TerminalKindNotPipelineActionable {
		t.Errorf("recorded kind = %q, want %q", got, TerminalKindNotPipelineActionable)
	}
}

func TestBlockedQuota_RealQuotaMarkerStillSetsCooldown(t *testing.T) {
	as := newBlockedQuotaScheduler(t)
	addRunning(as, "acme/core", 121, "real quota")
	reset := time.Now().Add(40 * time.Minute).Unix()
	as.NotifyComplete("acme/core", 121, false, false, TerminalKindRateLimitQuotaExhausted,
		fmt.Sprintf("[rate-limit-quota-exhausted] resetsAt=%d", reset))
	as.drainBackground()

	if as.state.QuotaCooldownUntil == "" {
		t.Fatal("a real adapter quota marker must still set the global cooldown")
	}
	got := parseRFC3339OrFail(t, as.state.QuotaCooldownUntil)
	if got.Unix() < reset-1 {
		t.Errorf("cooldown until %v ends before the resetsAt %d", got, reset)
	}
}

func TestBlockedQuota_RawAdapterUsageLimitStillSetsCooldown(t *testing.T) {
	as := newBlockedQuotaScheduler(t)
	addRunning(as, "acme/core", 122, "raw adapter")
	as.NotifyComplete("acme/core", 122, false, false, TerminalKindRateLimitQuotaExhausted,
		"exit 1: You've hit your usage limit · resets 3pm")
	as.drainBackground()

	if as.state.QuotaCooldownUntil == "" {
		t.Fatal("raw adapter usage-limit stderr must still set the global cooldown")
	}
}

func failedKind(t *testing.T, as *AutonomousScheduler, repo string, number int) string {
	t.Helper()
	for _, f := range as.state.Failed {
		if f.Repo == repo && f.Number == number {
			return f.Kind
		}
	}
	t.Fatalf("no failure recorded for %s#%d", repo, number)
	return ""
}
