package orchestrator

import (
	"testing"
	"time"
)

// Tests for the #4002 transient-network terminal kinds. A seconds-long local
// network/DNS blip produced two "failures" in the originating incident
// (acmeapp 2026-06-10): an Anthropic transport drop mid-feature-dev and a
// gh-auth connectivity failure at pipeline-start. Both must route through the
// environmental recovery paths — retry without pausing the queue, paging the
// operator, or counting toward the lifetime-failure cap.

// TestOnPipelineComplete_ApiConnectionLost_TransientNoPause verifies the
// Anthropic transport-drop kind routes exactly like api_overloaded: short
// (~5m) per-issue backoff, NO lifetime/per-session cap increment, NO pause,
// NO global cooldown.
func TestOnPipelineComplete_ApiConnectionLost_TransientNoPause(t *testing.T) {
	as := &AutonomousScheduler{
		config: AutonomousConfig{MaxConcurrent: 3},
		state: &AutonomousState{
			Status: "running",
			Running: []RunningItem{
				{Repo: "nightgauge/acmeapp-infra", Number: 78, Title: "Transport drop case"},
			},
			LifetimeIssueFailures: map[string]int{},
		},
		rescanCh:             make(chan struct{}, 1),
		perIssueFailureCount: map[string]int{},
		retryBackoff:         map[string]time.Time{},
	}

	before := time.Now()
	as.onPipelineComplete("nightgauge/acmeapp-infra", 78, false, false,
		TerminalKindApiConnectionLost, "API Error: The socket connection was closed unexpectedly")
	after := time.Now()

	key := "nightgauge/acmeapp-infra#78"

	if got := as.state.LifetimeIssueFailures[key]; got != 0 {
		t.Errorf("LifetimeIssueFailures[%q] = %d after api-connection-lost, want 0 (transient)", key, got)
	}
	if got := as.perIssueFailureCount[key]; got != 0 {
		t.Errorf("perIssueFailureCount[%q] = %d after api-connection-lost, want 0", key, got)
	}
	if as.state.Status == "paused" {
		t.Errorf("autonomous paused after api-connection-lost; want still running (a network blip must not halt the queue)")
	}
	if as.state.QuotaCooldownUntil != "" {
		t.Errorf("QuotaCooldownUntil = %q after api-connection-lost, want empty (per-issue backoff only)",
			as.state.QuotaCooldownUntil)
	}
	retryAt, ok := as.retryBackoff[key]
	if !ok {
		t.Fatalf("expected retryBackoff[%q] to be set after api-connection-lost", key)
	}
	wait := retryAt.Sub(before)
	if wait < 3*time.Minute || wait > 8*time.Minute {
		t.Errorf("backoff = %v, want ~5min (allowed 3m–8m)", wait)
	}
	if !retryAt.After(after) {
		t.Errorf("retryAt %v is not after call return %v", retryAt, after)
	}
	if len(as.state.Running) != 0 {
		t.Errorf("expected 0 running after api-connection-lost, got %d", len(as.state.Running))
	}
	if len(as.state.Failed) != 1 || as.state.Failed[0].Number != 78 {
		t.Fatalf("expected 1 failed entry for #78, got %+v", as.state.Failed)
	}
}

// TestOnPipelineComplete_GitHubNetworkOutage_ShortGlobalCooldownNoPause
// verifies the pipeline-start connectivity kind: a SHORT (~2m) GLOBAL
// dispatch cooldown (every repo is equally unreachable), matching per-issue
// backoff, NO lifetime-cap increment, NO pause. Writing QuotaCooldownUntil
// also arms the #3444 pause-decline guard against a racing
// haltQueueOnSlotFailure.
func TestOnPipelineComplete_GitHubNetworkOutage_ShortGlobalCooldownNoPause(t *testing.T) {
	as := &AutonomousScheduler{
		config: AutonomousConfig{MaxConcurrent: 3},
		state: &AutonomousState{
			Status: "running",
			Running: []RunningItem{
				{Repo: "nightgauge/acmeapp-infra", Number: 79, Title: "Preflight outage case"},
			},
			LifetimeIssueFailures: map[string]int{},
		},
		rescanCh:             make(chan struct{}, 1),
		perIssueFailureCount: map[string]int{},
		retryBackoff:         map[string]time.Time{},
	}

	before := time.Now()
	as.onPipelineComplete("nightgauge/acmeapp-infra", 79, false, false,
		TerminalKindGitHubNetworkOutage,
		"[github-network-outage] GitHub API unreachable — pipeline deferred before AI stages (transient; retryInSec=120).")

	key := "nightgauge/acmeapp-infra#79"

	if got := as.state.LifetimeIssueFailures[key]; got != 0 {
		t.Errorf("LifetimeIssueFailures[%q] = %d after github-network-outage, want 0 (transient)", key, got)
	}
	if as.state.Status == "paused" {
		t.Errorf("autonomous paused after github-network-outage; want still running")
	}
	// GLOBAL cooldown ~githubNetworkOutageCooldown out (clamp tolerance).
	cooldownUntil := parseRFC3339OrFail(t, as.state.QuotaCooldownUntil)
	cooldownWait := cooldownUntil.Sub(before)
	if cooldownWait < time.Minute || cooldownWait > 5*time.Minute {
		t.Errorf("global cooldown = %v, want ~%v (allowed 1m–5m)", cooldownWait, githubNetworkOutageCooldown)
	}
	// The #3444 guard must now decline a racing haltQueueOnSlotFailure pause.
	as.Pause("haltQueueOnSlotFailure: issue #79 failed at pipeline-start", "haltQueueOnSlotFailure")
	if as.state.Status == "paused" {
		t.Errorf("haltQueueOnSlotFailure pause landed despite active network-outage cooldown; #3444 guard should decline it")
	}
	// Per-issue backoff matches the cooldown window.
	retryAt, ok := as.retryBackoff[key]
	if !ok {
		t.Fatalf("expected retryBackoff[%q] to be set after github-network-outage", key)
	}
	backoffWait := retryAt.Sub(before)
	if backoffWait < time.Minute || backoffWait > 5*time.Minute {
		t.Errorf("backoff = %v, want ~%v (allowed 1m–5m)", backoffWait, githubNetworkOutageCooldown)
	}
	if len(as.state.Failed) != 1 || as.state.Failed[0].Number != 79 {
		t.Fatalf("expected 1 failed entry for #79, got %+v", as.state.Failed)
	}
}

// TestNotifyComplete_EmptyKindSocketCloseDetail_RoutesTransient is the #3439
// defense-in-depth contract for the new transport-drop kind: when the IPC
// caller passes terminalFailureKind="" but failureDetail carries the literal
// CLI message, the Go-side ClassifyTerminalKind fallback must re-classify and
// route to the transient branch (no lifetime increment, no pause).
func TestNotifyComplete_EmptyKindSocketCloseDetail_RoutesTransient(t *testing.T) {
	as := &AutonomousScheduler{
		config: AutonomousConfig{MaxConcurrent: 3},
		state: &AutonomousState{
			Status: "running",
			Running: []RunningItem{
				{Repo: "nightgauge/acmeapp-infra", Number: 78, Title: "Reclassify case"},
			},
			LifetimeIssueFailures: map[string]int{},
		},
		rescanCh:             make(chan struct{}, 1),
		perIssueFailureCount: map[string]int{},
		retryBackoff:         map[string]time.Time{},
	}

	as.NotifyComplete("nightgauge/acmeapp-infra", 78, false, false, "",
		"API Error: The socket connection was closed unexpectedly")

	key := "nightgauge/acmeapp-infra#78"
	if got := as.state.LifetimeIssueFailures[key]; got != 0 {
		t.Errorf("LifetimeIssueFailures[%q] = %d, want 0 (reclassified transient from detail)", key, got)
	}
	if as.state.Status == "paused" {
		t.Errorf("autonomous paused; want still running (reclassified transient)")
	}
	if _, ok := as.retryBackoff[key]; !ok {
		t.Errorf("expected retryBackoff[%q] after reclassification", key)
	}
}
