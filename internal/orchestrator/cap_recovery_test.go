package orchestrator

import (
	"strings"
	"testing"
)

// The observed failure text from #1545: skillRunner's stamp for a structured
// `rate_limit_event` whose payload carries a bucket name and a reset time and
// NO model at all. It is the exact string the fleet log recorded on
// 2026-09-07, and every test below starts from the kind that classifies from
// it, so the fixture cannot drift away from the defect it pins.
const capRejectionMarker = "[rate-limit-quota-exhausted] idle 2m0s after rate_limit_event " +
	"(status=rejected; seven_day_overage_included bucket; resetsAt=1789185600)"

// alwaysUsable is the injected health probe for tests that want the walk to
// place a provider. The real probe shells the vendor CLI (AdapterUsableForCapHop),
// which is exactly why the decision takes it as an input.
func alwaysUsable(string) (bool, string) { return true, "" }

// neverUsable stands for a chain whose every entry is installed but logged out.
func neverUsable(string) (bool, string) { return false, "not signed in" }

// TestTheObservedMarkerStillClassifiesAccountWide pins the PREMISE of #1545
// rather than the fix: the classifier reads the observed stamp as an
// account-wide exhaustion, because the payload names no model for
// model-unavailable's @mentions_registry_model predicate to find.
//
// This is deliberately NOT changed by this issue. The terminal-kind table keeps
// answering "what did the text say"; the attribution below answers the separate
// question "given what was actually running, what should happen next". If a
// future change makes the table itself answer model_unavailable here, this test
// fails loudly and the whole cap-recovery ladder should be re-derived rather
// than silently double-applied.
func TestTheObservedMarkerStillClassifiesAccountWide(t *testing.T) {
	if got := ClassifyTerminalKind(capRejectionMarker); got != TerminalKindRateLimitQuotaExhausted {
		t.Fatalf("the #1545 marker must still classify account-wide, got %q", got)
	}
	if strings.Contains(strings.ToLower(capRejectionMarker), "fable") {
		t.Fatal("fixture drift: the whole defect is that this payload names NO model")
	}
}

// TestCapRejectionOnFableDescendsInsteadOfCoolingTheFleetDown is acceptance
// criterion 1: a rate_limit_event rejection while running fable, with other
// tiers available, descends to opus and continues — no global cooldown.
func TestCapRejectionOnFableDescendsInsteadOfCoolingTheFleetDown(t *testing.T) {
	d := DecideCapRecovery(CapRecoveryInput{
		Kind:          ClassifyTerminalKind(capRejectionMarker),
		DispatchModel: "fable",
		ServedModel:   "claude-fable-5-1",
		Engine:        NewRetryEngine(DefaultRetryConfig()),
		AdapterUsable: alwaysUsable,
	})
	if d.Verdict != CapRecoveryDescendTier {
		t.Fatalf("verdict = %q, want descend_tier (%s)", d.Verdict, d.Why)
	}
	if d.Downgrade.NewTier != "opus" {
		t.Fatalf("descent target = %q, want opus", d.Downgrade.NewTier)
	}
	// The kind the caller routes on is what decides the cooldown. Attributing
	// the rejection to the model in flight is the whole point: a cap on fable
	// is not evidence the account is out.
	if d.EffectiveKind != TerminalKindModelUnavailable {
		t.Fatalf("effective kind = %q, want %q — an attributed cap must not take the account-wide branch",
			d.EffectiveKind, TerminalKindModelUnavailable)
	}
}

// TestDescentWalksTheWholeLadderBeforeAnythingElse pins that the ladder is
// spent in full — fable → opus → sonnet → haiku — before the provider walk is
// even consulted. The cheapest recovery is exhausted first.
func TestDescentWalksTheWholeLadderBeforeAnythingElse(t *testing.T) {
	engine := NewRetryEngine(DefaultRetryConfig())
	kind := ClassifyTerminalKind(capRejectionMarker)
	model := "fable"

	for _, want := range []string{"opus", "sonnet", "haiku"} {
		d := DecideCapRecovery(CapRecoveryInput{
			Kind:          kind,
			DispatchModel: model,
			Engine:        engine,
			Chain:         []string{"codex"},
			AdapterUsable: alwaysUsable,
		})
		if d.Verdict != CapRecoveryDescendTier {
			t.Fatalf("from %s: verdict = %q, want descend_tier while a tier remains (%s)", model, d.Verdict, d.Why)
		}
		if d.Downgrade.NewTier != want {
			t.Fatalf("from %s: descended to %q, want %q", model, d.Downgrade.NewTier, want)
		}
		engine.RecordDowngrade(model, d.Downgrade)
		model = d.Downgrade.NewTier
	}

	// Only now, with the ladder spent, may the walk run.
	d := DecideCapRecovery(CapRecoveryInput{
		Kind:          kind,
		DispatchModel: model,
		Engine:        engine,
		Chain:         []string{"codex"},
		AdapterUsable: alwaysUsable,
	})
	if d.Verdict != CapRecoveryHopProvider {
		t.Fatalf("with the ladder spent: verdict = %q, want hop_provider (%s)", d.Verdict, d.Why)
	}
}

// TestExhaustedLadderHopsToTheNextAuthenticatedProvider is acceptance criterion
// 2: a rejection that persists across the whole tier ladder walks
// adapter_fallback_chain to the next installed, authenticated provider.
func TestExhaustedLadderHopsToTheNextAuthenticatedProvider(t *testing.T) {
	probed := []string{}
	d := DecideCapRecovery(CapRecoveryInput{
		Kind:          ClassifyTerminalKind(capRejectionMarker),
		DispatchModel: "haiku", // the bottom rung: nothing weaker exists
		ServedModel:   "claude-haiku-4-5-20251001",
		Engine:        NewRetryEngine(DefaultRetryConfig()),
		Chain:         []string{"claude", "codex", "grok"},
		AdapterUsable: func(a string) (bool, string) {
			probed = append(probed, a)
			return a != "codex", "not signed in" // codex is logged out
		},
	})
	if d.Verdict != CapRecoveryHopProvider {
		t.Fatalf("verdict = %q, want hop_provider (%s)", d.Verdict, d.Why)
	}
	if d.NextAdapter != "grok" {
		t.Fatalf("hopped to %q, want grok — codex is installed but logged out and must be skipped", d.NextAdapter)
	}
	// `claude` is the capped account itself. Skipping it by PROVIDER rather
	// than by adapter name is what stops the walk buying a second full-price
	// failure against the same exhausted quota.
	for _, p := range probed {
		if p == "claude" {
			t.Fatal("the capped provider must be skipped before it is even probed")
		}
	}
	if d.EffectiveKind != TerminalKindModelUnavailable {
		t.Fatalf("effective kind = %q — a run that is still going must not cool the fleet down", d.EffectiveKind)
	}
}

// TestTheWalkNeverRevisitsAProviderThisRunAlreadyTried pins the cycle guard:
// once a run has hopped onto codex and been capped there too, the walk moves
// past it rather than back onto it.
func TestTheWalkNeverRevisitsAProviderThisRunAlreadyTried(t *testing.T) {
	d := DecideCapRecovery(CapRecoveryInput{
		Kind:          TerminalKindRateLimitQuotaExhausted,
		DispatchModel: "haiku",
		Engine:        NewRetryEngine(DefaultRetryConfig()),
		Chain:         []string{"codex", "grok"},
		Tried:         map[string]bool{"codex": true},
		AdapterUsable: alwaysUsable,
	})
	if d.Verdict != CapRecoveryHopProvider || d.NextAdapter != "grok" {
		t.Fatalf("verdict/target = %q/%q, want hop_provider/grok (%s)", d.Verdict, d.NextAdapter, d.Why)
	}
}

// TestAccountWideRejectionWithAnEmptyChainStillCoolsDown is acceptance
// criterion 3 and the named regression guard: the global cooldown is not
// removed, it is DEFERRED. With the ladder spent and no chain configured — the
// default configuration — behaviour is exactly what it was before #1545.
func TestAccountWideRejectionWithAnEmptyChainStillCoolsDown(t *testing.T) {
	d := DecideCapRecovery(CapRecoveryInput{
		Kind:          ClassifyTerminalKind(capRejectionMarker),
		DispatchModel: "haiku",
		Engine:        NewRetryEngine(DefaultRetryConfig()),
		Chain:         nil,
		AdapterUsable: alwaysUsable,
	})
	if d.Verdict != CapRecoveryCoolDown {
		t.Fatalf("verdict = %q, want cool_down (%s)", d.Verdict, d.Why)
	}
	if d.EffectiveKind != TerminalKindRateLimitQuotaExhausted {
		t.Fatalf("effective kind = %q, want %q — this is the branch that applies the global cooldown",
			d.EffectiveKind, TerminalKindRateLimitQuotaExhausted)
	}
	if !strings.Contains(d.Why, "no pipeline.adapter_fallback_chain configured") {
		t.Fatalf("the cooldown reason must tell the operator the chain is unset, got %q", d.Why)
	}
}

// TestAChainOfUnreachableProvidersAlsoCoolsDown covers the other exhaustion
// shape: a chain IS configured, but nothing in it is usable. The fleet still
// gets its cooldown rather than spinning through dead candidates.
func TestAChainOfUnreachableProvidersAlsoCoolsDown(t *testing.T) {
	d := DecideCapRecovery(CapRecoveryInput{
		Kind:          TerminalKindRateLimitQuotaExhausted,
		DispatchModel: "haiku",
		Engine:        NewRetryEngine(DefaultRetryConfig()),
		Chain:         []string{"codex", "grok"},
		AdapterUsable: neverUsable,
	})
	if d.Verdict != CapRecoveryCoolDown {
		t.Fatalf("verdict = %q, want cool_down (%s)", d.Verdict, d.Why)
	}
	if !strings.Contains(d.Why, "codex, grok") {
		t.Fatalf("the reason must name the chain it failed to place, got %q", d.Why)
	}
}

// TestAnUnprobedChainIsNotAUsableChain pins the conservative default: with no
// health probe wired, no candidate is assumed reachable. An unprobed provider
// is not a provider we know we can reach, and guessing costs a full stage.
func TestAnUnprobedChainIsNotAUsableChain(t *testing.T) {
	d := DecideCapRecovery(CapRecoveryInput{
		Kind:          TerminalKindRateLimitQuotaExhausted,
		DispatchModel: "haiku",
		Engine:        NewRetryEngine(DefaultRetryConfig()),
		Chain:         []string{"codex"},
		AdapterUsable: nil,
	})
	if d.Verdict != CapRecoveryCoolDown {
		t.Fatalf("verdict = %q, want cool_down with no probe wired (%s)", d.Verdict, d.Why)
	}
}

// TestNonCapFailuresAreUntouched pins the blast radius. Everything that is not
// one of the two cap kinds returns not-applicable with its kind unchanged, so
// the caller's existing routing is bit-for-bit what it was.
func TestNonCapFailuresAreUntouched(t *testing.T) {
	for _, kind := range []string{
		TerminalKindStallKill,
		TerminalKindSubagentCrash,
		TerminalKindApiOverloaded,
		TerminalKindAdapterAuthFailed,
		TerminalKindGitHubQuotaLow,
		TerminalKindValidationError,
	} {
		d := DecideCapRecovery(CapRecoveryInput{
			Kind:          kind,
			DispatchModel: "fable",
			Engine:        NewRetryEngine(DefaultRetryConfig()),
			Chain:         []string{"codex"},
			AdapterUsable: alwaysUsable,
		})
		if d.Verdict != CapRecoveryNotApplicable {
			t.Fatalf("%s: verdict = %q, want not-applicable", kind, d.Verdict)
		}
		if d.EffectiveKind != kind {
			t.Fatalf("%s: effective kind = %q, want it unchanged", kind, d.EffectiveKind)
		}
	}
}

// TestAPlainModelUnavailableNeverCoolsTheFleetDown pins the #1545 defect's
// INVERSE. A model_unavailable that survives both recoveries (an unknown model
// id, a plan that does not offer the band) keeps its own kind, so it takes the
// existing no-cooldown branch. A plan restriction says nothing about the
// account's quota, and halting every repo over one would be this issue's bug
// with the kinds swapped.
func TestAPlainModelUnavailableNeverCoolsTheFleetDown(t *testing.T) {
	d := DecideCapRecovery(CapRecoveryInput{
		Kind:          TerminalKindModelUnavailable,
		DispatchModel: "haiku",
		Engine:        NewRetryEngine(DefaultRetryConfig()),
		Chain:         nil,
		AdapterUsable: alwaysUsable,
	})
	if d.Verdict != CapRecoveryCoolDown {
		t.Fatalf("verdict = %q, want cool_down (%s)", d.Verdict, d.Why)
	}
	if d.EffectiveKind != TerminalKindModelUnavailable {
		t.Fatalf("effective kind = %q, want %q — this kind must NOT reach applyQuotaCooldownLocked",
			d.EffectiveKind, TerminalKindModelUnavailable)
	}
}

// TestEveryVerdictExplainsItselfNamingTierProviderAndWhy is acceptance
// criterion 4's substrate: the same sentence reaches the run record, the log
// line and the Action Center card, so all three tell one story. A decision the
// operator cannot read is a decision they will debug from go-backend.log at 3am,
// which is the failure mode the card exists to remove.
func TestEveryVerdictExplainsItselfNamingTierProviderAndWhy(t *testing.T) {
	descend := DecideCapRecovery(CapRecoveryInput{
		Kind: TerminalKindRateLimitQuotaExhausted, DispatchModel: "fable",
		ServedModel: "claude-fable-5-1", Engine: NewRetryEngine(DefaultRetryConfig()),
		AdapterUsable: alwaysUsable,
	})
	for _, want := range []string{"claude-fable-5-1", "anthropic", "opus", "usage cap"} {
		if !strings.Contains(descend.Why, want) {
			t.Fatalf("descent reason %q must name %q", descend.Why, want)
		}
	}

	hop := DecideCapRecovery(CapRecoveryInput{
		Kind: TerminalKindRateLimitQuotaExhausted, DispatchModel: "haiku",
		ServedModel: "claude-haiku-4-5-20251001", Engine: NewRetryEngine(DefaultRetryConfig()),
		Chain: []string{"grok"}, AdapterUsable: alwaysUsable,
	})
	for _, want := range []string{"claude-haiku-4-5-20251001", "tier ladder is spent", "grok", "adapter_fallback_chain"} {
		if !strings.Contains(hop.Why, want) {
			t.Fatalf("hop reason %q must name %q", hop.Why, want)
		}
	}
}

// TestAttributionPrefersServedEvidenceOverGoSideInference pins that the
// provider comes from what the adapter REPORTED serving, not from what Go
// guessed. On the IPC path Go holds no adapter at all, and a re-derivation that
// guessed wrong would walk an xai rejection down the anthropic ladder.
func TestAttributionPrefersServedEvidenceOverGoSideInference(t *testing.T) {
	d := DecideCapRecovery(CapRecoveryInput{
		Kind:          TerminalKindRateLimitQuotaExhausted,
		DispatchModel: "fable",
		ServedModel:   "grok-4.6", // evidence: an xai dispatch
		Adapter:       "claude",   // stale/absent Go-side inference
		Engine:        NewRetryEngine(DefaultRetryConfig()),
		AdapterUsable: alwaysUsable,
	})
	if d.Verdict != CapRecoveryDescendTier {
		t.Fatalf("verdict = %q, want descend_tier (%s)", d.Verdict, d.Why)
	}
	if d.Provider != "xai" {
		t.Fatalf("provider = %q, want xai — served evidence outranks the adapter hint", d.Provider)
	}
	if !d.Downgrade.SameModelDescent {
		t.Fatalf("an xai descent is the same model at a lower effort rung, got %+v", d.Downgrade)
	}
}

// TestGoDirectPathAttributesFromItsOwnAdapter covers the other half: with no
// served report (the Go-direct path, where nothing rides back over a wire),
// execMgr's adapter names the provider.
func TestGoDirectPathAttributesFromItsOwnAdapter(t *testing.T) {
	d := DecideCapRecovery(CapRecoveryInput{
		Kind:          TerminalKindRateLimitQuotaExhausted,
		DispatchModel: "fable",
		Adapter:       "grok",
		Engine:        NewRetryEngine(DefaultRetryConfig()),
		AdapterUsable: alwaysUsable,
	})
	if d.Provider != "xai" {
		t.Fatalf("provider = %q, want xai from the executing adapter", d.Provider)
	}
}

// TestClearDowngradesGivesTheHoppedProviderItsOwnLadder pins the state reset a
// provider hop performs. The downgrade map is keyed by TIER, so carrying it
// across a hop would start the new provider partway down a ladder it never
// refused — a worse dispatch bought with the recovery meant to help.
func TestClearDowngradesGivesTheHoppedProviderItsOwnLadder(t *testing.T) {
	engine := NewRetryEngine(DefaultRetryConfig())
	first := engine.EvaluateDowngrade("fable")
	engine.RecordDowngrade("fable", first)
	if got := engine.ApplyDowngrades("fable"); got == "fable" {
		t.Fatal("precondition: the descent must be sticky before it can be cleared")
	}

	engine.ClearDowngrades()

	if got := engine.ApplyDowngrades("fable"); got != "fable" {
		t.Fatalf("after ClearDowngrades, fable resolves to %q — the hopped provider must start at the top of its own ladder", got)
	}
	// Everything else Reset() clears is genuinely per-RUN and the run is still
	// in flight, so ClearDowngrades must not touch it.
	if d := engine.EvaluateDowngrade("fable"); !d.ShouldDowngrade || d.NewTier != "opus" {
		t.Fatalf("the ladder itself must be intact after a clear, got %+v", d)
	}
}
