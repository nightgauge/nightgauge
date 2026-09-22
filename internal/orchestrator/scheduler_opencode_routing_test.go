package orchestrator

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/nightgauge/nightgauge/internal/execution/opencodeplugin"
)

// scheduler_opencode_routing_test.go pins #1643's three regression
// contracts that have no other single home: the cap-hop skip check with an
// opencode candidate in the chain (AC 2), `pipeline.adapter_fallback_chain`
// accepting "opencode" with no new validation code (AC 3), and the
// adapter_auth_failed retryable-infra path specifically for an opencode
// failure detail (AC 6). See cap_recovery_test.go for the cap-recovery
// ladder's own broader coverage and opencode_test.go for BuildCommand's.

// TestOpenCodeCapHopSkipsCandidateConfiguredForTheCappedProvider is AC 2's
// first half: after an anthropic cap, an opencode candidate CONFIGURED WITH
// anthropic/* is the same account that just refused the run and must be
// skipped, even though "opencode" the adapter name maps to no fixed
// provider on its own (models.ProviderForAdapter("opencode") == "other").
// Reverting cap_recovery.go's nextCapProvider fix (dropping the
// CandidateModel-aware ProviderFor call back to the adapter-only
// ProviderForAdapter) must turn this red.
func TestOpenCodeCapHopSkipsCandidateConfiguredForTheCappedProvider(t *testing.T) {
	probed := []string{}
	d := DecideCapRecovery(CapRecoveryInput{
		Kind:          ClassifyTerminalKind(capRejectionMarker),
		DispatchModel: "haiku", // bottom rung: no tier descent to spend first
		ServedModel:   "claude-haiku-4-5-20251001",
		Chain:         []string{"opencode"},
		AdapterUsable: func(a string) (bool, string) {
			probed = append(probed, a)
			return true, ""
		},
		CandidateModel: func(adapter string) string {
			if adapter == "opencode" {
				return "anthropic/claude-3-opus"
			}
			return ""
		},
	})
	if d.Verdict != CapRecoveryCoolDown {
		t.Fatalf("verdict = %q, want cool_down — the only chain candidate is an opencode/anthropic dispatch, the same capped account (%s)", d.Verdict, d.Why)
	}
	if len(probed) != 0 {
		t.Errorf("AdapterUsable was probed for %v; the candidate must be skipped by provider before any probe", probed)
	}
}

// TestOpenCodeCapHopAcceptsCandidateConfiguredForADifferentProvider is AC 2's
// second half: an opencode candidate configured with lmstudio/* (a local
// provider, unaffected by an anthropic account cap) is eligible.
func TestOpenCodeCapHopAcceptsCandidateConfiguredForADifferentProvider(t *testing.T) {
	d := DecideCapRecovery(CapRecoveryInput{
		Kind:          ClassifyTerminalKind(capRejectionMarker),
		DispatchModel: "haiku",
		ServedModel:   "claude-haiku-4-5-20251001",
		Chain:         []string{"opencode"},
		AdapterUsable: alwaysUsable,
		CandidateModel: func(adapter string) string {
			if adapter == "opencode" {
				return "lmstudio/qwen3-coder:30b"
			}
			return ""
		},
	})
	if d.Verdict != CapRecoveryHopProvider {
		t.Fatalf("verdict = %q, want hop_provider — an opencode/lmstudio dispatch is not the capped anthropic account (%s)", d.Verdict, d.Why)
	}
	if d.NextAdapter != "opencode" {
		t.Fatalf("hopped to %q, want opencode", d.NextAdapter)
	}
}

// TestOpenCodeCapHopAcceptsAnOpenAIConfiguredCandidate covers AC 2's other
// named example provider (openai/*, alongside lmstudio/*).
func TestOpenCodeCapHopAcceptsAnOpenAIConfiguredCandidate(t *testing.T) {
	d := DecideCapRecovery(CapRecoveryInput{
		Kind:          ClassifyTerminalKind(capRejectionMarker),
		DispatchModel: "haiku",
		ServedModel:   "claude-haiku-4-5-20251001",
		Chain:         []string{"opencode"},
		AdapterUsable: alwaysUsable,
		CandidateModel: func(adapter string) string {
			if adapter == "opencode" {
				return "openai/gpt-5"
			}
			return ""
		},
	})
	if d.Verdict != CapRecoveryHopProvider {
		t.Fatalf("verdict = %q, want hop_provider — an opencode/openai dispatch is not the capped anthropic account (%s)", d.Verdict, d.Why)
	}
}

// TestOpenCodeCapHopWithNoCandidateModelFallsBackToAdapterOnly pins the
// backward-compatible default: a nil CandidateModel (every call site before
// #1643, and every non-scheduler caller of DecideCapRecovery) leaves the
// skip check reading ProviderFor(candidate, "") — ProviderForAdapter's
// answer for "opencode" ("other"), which never equals a real capped
// provider, so the candidate is never wrongly skipped for lack of a
// resolvable model.
func TestOpenCodeCapHopWithNoCandidateModelFallsBackToAdapterOnly(t *testing.T) {
	d := DecideCapRecovery(CapRecoveryInput{
		Kind:          ClassifyTerminalKind(capRejectionMarker),
		DispatchModel: "haiku",
		ServedModel:   "claude-haiku-4-5-20251001",
		Chain:         []string{"opencode"},
		AdapterUsable: alwaysUsable,
		// CandidateModel intentionally nil.
	})
	if d.Verdict != CapRecoveryHopProvider {
		t.Fatalf("verdict = %q, want hop_provider — with no CandidateModel resolver, opencode's unresolvable provider must never be skipped as the capped one (%s)", d.Verdict, d.Why)
	}
}

// TestPipelineAdapterFallbackChainAcceptsOpenCode is AC 3: opencode needs no
// new validation code to be accepted in pipeline.adapter_fallback_chain — a
// plain []string with no adapter allow-list — and capFallbackChain (the one
// Go-side reader, #1545) reads it back unfiltered.
func TestPipelineAdapterFallbackChainAcceptsOpenCode(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, ".nightgauge")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	content := "owner: nightgauge\npipeline:\n  adapter_fallback_chain:\n    - claude\n    - opencode\n"
	if err := os.WriteFile(filepath.Join(dir, "config.yaml"), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}

	s := &Scheduler{}
	chain := s.capFallbackChain(root)
	if len(chain) != 2 || chain[0] != "claude" || chain[1] != "opencode" {
		t.Fatalf("capFallbackChain = %v, want [claude opencode]", chain)
	}
}

// TestOpenCodeAdapterAuthFailedTakesTheRetryableInfraPath is AC 6: a
// regression pin, not a new behavior. adapter_auth_failed's routing in
// autonomous.go branches on TerminalKindAdapterAuthFailed alone — never on
// which adapter produced it — so an opencode-sourced auth failure must take
// exactly the same retryable-infra path (no LifetimeIssueFailures increment,
// no cascade feed) TestOnPipelineComplete_AdapterAuthFailed_TransientNoPauseNoCascade
// already pins for the adapter-agnostic case. This test supplies an
// opencode-flavored failure detail so a future adapter-specific branch that
// narrowed the path to claude/codex/grok would turn it red.
func TestOpenCodeAdapterAuthFailedTakesTheRetryableInfraPath(t *testing.T) {
	as := newAutonomousForCascadeTest(t, 3, 30*time.Minute)
	as.state.LifetimeIssueFailures = map[string]int{}
	as.perIssueFailureCount = map[string]int{}
	as.retryBackoff = map[string]retryPlan{}

	repo, num := "acme/dashboard", 1643
	addRunning(as, repo, num, "opencode auth pre-flight")
	as.onPipelineComplete(repo, num, false, false,
		TerminalKindAdapterAuthFailed,
		"[adapter-auth-failed] opencode: credential refusal — ANTHROPIC_API_KEY is not set for an anthropic/ dispatch.", false)
	as.drainBackground()

	if as.state.Status == "safety_tripped" || as.state.Status == "paused" {
		t.Fatalf("scheduler tripped/paused on an opencode adapter_auth_failed; want still running")
	}
	if as.cascadeTracker.IsTripped() {
		t.Error("cascadeTracker tripped on a single opencode adapter_auth_failed; want excluded from cascade")
	}
	key := repo + "#" + "1643"
	if got := as.state.LifetimeIssueFailures[key]; got != 0 {
		t.Errorf("LifetimeIssueFailures[%q] = %d, want 0 (retryable infra)", key, got)
	}
	if _, ok := retryDeadline(as, key); !ok {
		t.Errorf("expected retryBackoff[%q] to be set after opencode adapter_auth_failed", key)
	}
}

// TestLatestOpenCodeSessionID is #1643 AC 5's capture half: the session id
// recorded after a stage's dispatch is the LAST non-empty one the run's
// events file carries, read back through the same opencodeplugin path #1624
// already writes.
func TestLatestOpenCodeSessionID(t *testing.T) {
	dir := t.TempDir()
	outputFile := filepath.Join(dir, "output.json")
	runID := "01a0c032-1221-729e-b2b5-c47938777f61"
	eventsPath, ok := opencodeplugin.EventsPath(outputFile, runID)
	if !ok {
		t.Fatal("EventsPath returned ok=false for a valid absolute outputFile and runID")
	}
	lines := []string{
		`{"v":1,"ts":"2026-09-20T00:00:00Z","kind":"idle","session_id":"ses_first000000000"}`,
		`{"v":1,"ts":"2026-09-20T00:00:05Z","kind":"compaction","session_id":"ses_second00000000"}`,
	}
	if err := os.WriteFile(eventsPath, []byte(strings.Join(lines, "\n")+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if got := latestOpenCodeSessionID(outputFile, runID); got != "ses_second00000000" {
		t.Errorf("latestOpenCodeSessionID = %q, want the last non-empty session_id (ses_second00000000)", got)
	}
}

// TestLatestOpenCodeSessionIDMissingFile is the honest-silence case: no
// events file (every non-opencode dispatch, or a run that never installed
// the plugin) answers "", never an error a caller must guard.
func TestLatestOpenCodeSessionIDMissingFile(t *testing.T) {
	dir := t.TempDir()
	outputFile := filepath.Join(dir, "output.json")
	if got := latestOpenCodeSessionID(outputFile, "01a0c032-1221-729e-b2b5-c47938777f61"); got != "" {
		t.Errorf("latestOpenCodeSessionID = %q, want \"\" for a missing events file", got)
	}
}
