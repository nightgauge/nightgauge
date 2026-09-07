package orchestrator

import (
	"fmt"
	"log"
	"strings"

	"github.com/nightgauge/nightgauge/internal/doctor"
	"github.com/nightgauge/nightgauge/internal/models"
)

// CAP RECOVERY — what a usage-cap rejection should do next, decided once.
//
// THE DEFECT THIS CLOSES (#1545). The classifier's two rate-limit kinds already
// draw the right distinction:
//
//	model_unavailable          → a MODEL-scoped cap. Descend the tier ladder
//	                             (fable → opus → sonnet → haiku), sticky for
//	                             the run. No global cooldown.
//	rate_limit_quota_exhausted → the ACCOUNT's bucket is empty. Same model,
//	                             backoff, plus a GLOBAL cooldown that suspends
//	                             dispatch for every repo.
//
// Free text picks between them correctly because the model is IN the string
// (`Claude Opus 4.5 usage limit reached` satisfies model-unavailable's
// `@mentions_registry_model` predicate). The structured stream event does not
// carry a model at all —
//
//	{"type":"rate_limit_event","rate_limit_info":{"status":"rejected",
//	 "rateLimitType":"seven_day_overage_included","overageStatus":"rejected",…}}
//
// — only a bucket name. skillRunner stamps `[rate-limit-quota-exhausted]` from
// it, that stamp is an explicit marker so it outranks the model-name heuristic,
// and the account-wide reading wins by default. Observed cost: a Fable cap on a
// Max plan idled the whole fleet for an hour with opus, sonnet, haiku, codex and
// grok all available and nothing tried.
//
// THE ATTRIBUTION, AND WHY IT LIVES HERE RATHER THAN IN THE STAMP. The model in
// flight is already known on this side of the wire, first-hand:
// StageResultParams.ServedModel is the CONCRETE id the adapter process was
// spawned with, read out of the adapter's own env after model preflight
// (#91/#340) — after the extension's auto-router picked the adapter and after
// walkAdapterFallback had its chance to replace that pick. So the rejection can
// be attributed without teaching the TS stamp a new vocabulary, without a second
// classifier, and without widening the terminal-kind table: the table keeps
// answering "what did the text say", and this file answers the separate question
// "given what was running, what should happen next".
//
// THE ORDER, WHICH IS THE POINT. A rejection is read as account-wide only when
// it SURVIVES every cheaper recovery — that is the only evidence the account
// itself is out:
//
//  1. descend_tier  — a weaker tier remains on this provider's ladder.
//  2. hop_provider  — the ladder is spent; pipeline.adapter_fallback_chain
//     names another installed, authenticated provider.
//  3. cool_down     — both are exhausted. NOW the global cooldown is the
//     honest answer, and it is a last resort rather than a
//     first move.
//
// An empty chain collapses 2 into 3, which is why an account-wide rejection with
// no configured fallback still cools down exactly as it did before.
//
// MODEL-UNAVAILABLE FLOWS THROUGH THE SAME DECISION. A rejection that classified
// model_unavailable on its own (an unknown model id, a plan restriction) reaches
// step 2 when its ladder is spent, so the provider walk serves both kinds. Its
// step-3 verdict keeps kind model_unavailable and therefore still takes no global
// cooldown — a plan that does not offer fable says nothing about the account's
// quota, and cooling the fleet down over it would be the #1545 defect inverted.
type CapRecoveryVerdict string

const (
	// CapRecoveryNotApplicable is the verdict for every failure that is not a
	// usage-cap rejection. The caller keeps its existing classification and
	// routing untouched — this file changes nothing outside the two kinds it
	// names.
	CapRecoveryNotApplicable CapRecoveryVerdict = ""
	// CapRecoveryDescendTier — retry the stage on the next-weaker tier of the
	// SAME provider, via the existing sticky RetryEngine downgrade.
	CapRecoveryDescendTier CapRecoveryVerdict = "descend_tier"
	// CapRecoveryHopProvider — the tier ladder is spent; re-run the stage on
	// the next installed, authenticated provider in the fallback chain.
	CapRecoveryHopProvider CapRecoveryVerdict = "hop_provider"
	// CapRecoveryCoolDown — nothing cheaper is left. The caller keeps the
	// incoming kind, which is what routes an account-wide exhaustion to the
	// global cooldown.
	CapRecoveryCoolDown CapRecoveryVerdict = "cool_down"
)

// CapRecoveryInput is everything the decision reads. It is a plain struct with
// no scheduler reference so the whole ladder-then-walk-then-cooldown order is
// unit-testable without a live pipeline.
type CapRecoveryInput struct {
	// Kind is the terminal kind the classifier answered for this failure.
	Kind string
	// DispatchModel is the registry BAND this stage was dispatched on — the
	// value the tier ladder is keyed by (#340).
	DispatchModel string
	// ServedModel is the concrete id the adapter reported actually serving the
	// stage. Evidence, not inference: it names the executing provider on the
	// IPC path, where Go holds no adapter of its own (#611). Empty is honest
	// silence and falls back to Adapter.
	ServedModel string
	// Adapter is the adapter Go itself is executing on — execMgr's, on the
	// Go-direct path. Empty on the IPC path.
	Adapter string
	// Chain is pipeline.adapter_fallback_chain, in operator order. Empty means
	// no provider walk is configured, which collapses hop_provider into
	// cool_down.
	Chain []string
	// Tried names the adapters this RUN has already hopped to for a cap, so a
	// walk cannot cycle back onto a provider that already refused us.
	Tried map[string]bool
	// Engine owns the sticky tier ladder. nil disables the descent arm (tests
	// and the Go-direct paths that construct no engine).
	Engine *RetryEngine
	// AdapterUsable reports whether a candidate adapter is installed AND
	// authenticated, with a reason when it is not. Injected rather than called
	// directly because the real probe shells the vendor CLI: a decision that
	// spawns subprocesses is not a decision a test can pin. nil rejects every
	// candidate, which is the conservative reading — an unprobed provider is
	// not a provider we know we can reach.
	AdapterUsable func(adapter string) (bool, string)
}

// CapRecoveryDecision is the verdict plus everything the caller needs to act on
// it without re-deriving anything.
type CapRecoveryDecision struct {
	Verdict CapRecoveryVerdict
	// EffectiveKind is the terminal kind the caller should route on. For
	// descend_tier and hop_provider it is TerminalKindModelUnavailable — the
	// rejection has been attributed to the model in flight and the run
	// continues, so it must NOT take the account-wide cooldown branch. For
	// cool_down it is the incoming Kind, unchanged.
	EffectiveKind string
	// Downgrade is the descent the RetryEngine resolved. Only meaningful for
	// descend_tier; the caller records it so the substitution is sticky.
	Downgrade DowngradeDecision
	// NextAdapter is the provider to re-run the stage on. Only meaningful for
	// hop_provider.
	NextAdapter string
	// Provider is the provider the rejection was attributed to.
	Provider string
	// Why is the operator-facing sentence: what was tried, what is happening
	// next, and on what evidence. It reaches the run record, the log line and
	// the Action Center card, so all three tell the same story.
	Why string
}

// isCapRejection reports whether a terminal kind is one of the two the cap
// ladder governs. Everything else returns not-applicable untouched.
func isCapRejection(kind string) bool {
	return kind == TerminalKindRateLimitQuotaExhausted || kind == TerminalKindModelUnavailable
}

// DecideCapRecovery answers, once, what a usage-cap rejection should do next.
//
// It is deliberately the ONE authority for the order: both dispatch paths (the
// Go-direct scheduler arm and IpcStageRunner) call it, so a cap cannot descend
// on one path and cool the fleet down on the other. See the package-level
// commentary above for why the order is ladder → provider walk → cooldown.
func DecideCapRecovery(in CapRecoveryInput) CapRecoveryDecision {
	if !isCapRejection(in.Kind) {
		return CapRecoveryDecision{Verdict: CapRecoveryNotApplicable, EffectiveKind: in.Kind}
	}

	// Attribute the rejection to a provider from the strongest evidence
	// available, in the same precedence descentProviderForDispatch uses: the
	// served id the process reported, then Go's own adapter.
	//
	// TWO provider values, deliberately, because the two consumers ask
	// different questions and one answer cannot serve both:
	//
	//	descentHint — what the retry engine keys its ladder on. It is
	//	              scopeDescentProvider-narrowed, so everything but xai
	//	              collapses to "" and keeps the historical anthropic
	//	              inference bit for bit (#611). Passing anything wider here
	//	              would re-point ladders this issue has no business moving.
	//	provider    — the TRUE provider name. The walk needs it to skip
	//	              candidates belonging to the account that just refused us,
	//	              and the operator-facing sentence needs it to say who
	//	              capped the run. The narrowed hint reads "" for anthropic,
	//	              which would silently disable both.
	descentHint := DowngradeProviderForServedModel(in.ServedModel)
	if descentHint == "" {
		descentHint = DowngradeProviderForAdapter(in.Adapter)
	}
	provider := capProviderOf(in.ServedModel, in.Adapter)

	// 1. The tier ladder.
	if in.Engine != nil && in.DispatchModel != "" {
		if dg := in.Engine.EvaluateDowngradeForProvider(in.DispatchModel, descentHint); dg.ShouldDowngrade {
			return CapRecoveryDecision{
				Verdict:       CapRecoveryDescendTier,
				EffectiveKind: TerminalKindModelUnavailable,
				Downgrade:     dg,
				Provider:      provider,
				Why: fmt.Sprintf(
					"usage cap hit while running %s on %s; descending to %s (sticky for the run) — a cap on one tier says nothing about the tiers below it",
					capModelLabel(in.DispatchModel, in.ServedModel), providerLabel(provider), dg.NewTier),
			}
		}
	}

	// 2. The provider walk. Reached only once the ladder is spent, which is the
	//    evidence that no tier of THIS provider will serve us.
	if next, why, ok := nextCapProvider(in, provider); ok {
		return CapRecoveryDecision{
			Verdict:       CapRecoveryHopProvider,
			EffectiveKind: TerminalKindModelUnavailable,
			NextAdapter:   next,
			Provider:      provider,
			Why: fmt.Sprintf(
				"usage cap hit while running %s on %s and the whole tier ladder is spent; re-running the stage on %s per pipeline.adapter_fallback_chain (%s)",
				capModelLabel(in.DispatchModel, in.ServedModel), providerLabel(provider), next, why),
		}
	}

	// 3. Last resort. The incoming kind is preserved verbatim, which is what
	//    sends an account-wide exhaustion — and only an account-wide
	//    exhaustion that survived both recoveries — to the global cooldown.
	return CapRecoveryDecision{
		Verdict:       CapRecoveryCoolDown,
		EffectiveKind: in.Kind,
		Provider:      provider,
		Why: fmt.Sprintf(
			"usage cap hit while running %s on %s; the tier ladder and pipeline.adapter_fallback_chain are both exhausted (%s) — nothing cheaper is left to try",
			capModelLabel(in.DispatchModel, in.ServedModel), providerLabel(provider), capChainSummary(in)),
	}
}

// nextCapProvider walks pipeline.adapter_fallback_chain for the first entry
// that is neither the capped provider nor one this run already hopped to, and
// that a live probe says is installed and authenticated.
//
// A candidate is skipped by PROVIDER, not by adapter name: the capped provider
// may be reachable under several adapter names (claude / claude-headless /
// claude-sdk all resolve to anthropic), and hopping onto a second name for the
// same exhausted account buys a second failure at full stage price.
func nextCapProvider(in CapRecoveryInput, cappedProvider string) (string, string, bool) {
	if in.AdapterUsable == nil {
		return "", "", false
	}
	for _, candidate := range in.Chain {
		candidate = strings.TrimSpace(candidate)
		if candidate == "" {
			continue
		}
		if in.Tried[candidate] {
			continue
		}
		if cappedProvider != "" && models.ProviderForAdapter(candidate) == cappedProvider {
			continue // same account that just refused us
		}
		if ok, reason := in.AdapterUsable(candidate); !ok {
			log.Printf("cap-recovery: skipping fallback adapter %q — %s", candidate, reason)
			continue
		}
		return candidate, "installed and authenticated", true
	}
	return "", "", false
}

// capProviderOf names the provider that actually refused this dispatch, in the
// same evidence-first order the descent hint uses: the concrete id the adapter
// reported serving, then the adapter Go itself is executing on.
//
// It returns the registry's own provider name — anthropic, openai, google, xai —
// rather than the descent machinery's narrowed hint, because the walk's skip
// rule and the operator's sentence both need to distinguish anthropic from
// "unknown". scopeDescentProvider collapses the two, which is correct for the
// ladder it guards and wrong for everything here.
func capProviderOf(servedModel, adapter string) string {
	if id := strings.TrimSpace(servedModel); id != "" {
		if m, ok := models.Get(id); ok && m.Provider != "" {
			return m.Provider
		}
	}
	if a := strings.TrimSpace(adapter); a != "" {
		if p := models.ProviderForAdapter(a); p != "" && p != "other" {
			return p
		}
	}
	return ""
}

// capChainSummary describes the chain the walk just failed to place, so the
// cooldown's reason says whether the operator has no chain configured at all
// (the common case, and a fixable one) or a chain whose every entry was
// unreachable.
func capChainSummary(in CapRecoveryInput) string {
	if len(in.Chain) == 0 {
		return "no pipeline.adapter_fallback_chain configured"
	}
	return fmt.Sprintf("chain [%s] offered no installed, authenticated alternative provider",
		strings.Join(in.Chain, ", "))
}

// capModelLabel names the model in flight for a human: the concrete served id
// when the adapter reported one, the dispatched band otherwise. The served id
// is the more useful half of the pair — it is what the provider actually
// capped — so it leads when both are known.
func capModelLabel(dispatchModel, servedModel string) string {
	served := strings.TrimSpace(servedModel)
	band := strings.TrimSpace(dispatchModel)
	switch {
	case served != "" && band != "" && served != band:
		return fmt.Sprintf("%s (band %s)", served, band)
	case served != "":
		return served
	case band != "":
		return band
	default:
		return "an unreported model"
	}
}

// providerLabel keeps an unattributed rejection readable rather than printing a
// bare empty string into an operator-facing sentence.
func providerLabel(provider string) string {
	if strings.TrimSpace(provider) == "" {
		return "its provider"
	}
	return provider
}

// AdapterUsableForCapHop is the production CapRecoveryInput.AdapterUsable
// probe: the adapter doctor's own health check, which is the single place that
// already knows what "installed and authenticated" means per adapter kind (a
// CLI on PATH at or above its version floor, an SDK adapter's API key, a local
// HTTP server actually answering).
//
// Reusing the doctor rather than writing a second, cheaper check is deliberate.
// The cheap version — "is the binary on PATH" — is exactly the check that would
// hop a capped run onto a `grok` that is installed and logged OUT, buying a
// second full-price stage failure to learn what `nightgauge doctor --adapters`
// could have said for free.
//
// It shells the vendor CLI, so it is called ONCE per hop candidate on a
// recovery path that has already lost a stage — never on the hot dispatch
// path — and it is injected through the input struct so the decision itself
// stays a pure function.
func AdapterUsableForCapHop(adapter string) (bool, string) {
	health := doctor.CheckAdapters([]string{adapter})
	if len(health) == 0 {
		return false, "adapter doctor returned no reading"
	}
	h := health[0]
	if !h.OK {
		reason := h.Remediation
		if reason == "" {
			reason = "not usable"
		}
		return false, reason
	}
	return true, ""
}
