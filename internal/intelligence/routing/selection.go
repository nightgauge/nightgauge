package routing

import (
	"github.com/nightgauge/nightgauge/internal/models"
)

// selection.go — the selection query over the registry axes (selection-query
// cutover, #581 / spike #568 §4.1).
//
// The unit of selection is the dispatch envelope (model_id, effort, thinking),
// chosen from a provider-scoped, envelope-valued candidate ladder. The ladder
// derives from the ONE registry (internal/models, mirroring the canonical SDK
// file): which model serves a band, whether the dispatching transport can
// reach it, and which effort rungs it declares are all registry facts. The
// band ORDER stays the single TierBandsStrongestFirst declaration
// (performance_mode.go) — order is declared, not data, in this phase: the
// registry-axis-schema deliberately added no ordering field and no measured
// capability evidence exists yet.
//
// Envelope-valued means the rungs are (model_id, effort) points, not model
// names. On anthropic the rungs span models at their declared default
// efforts; on xai — where all four bands map to grok-4.6 and a band downgrade
// is a declared cost no-op (#532) — the rungs descend through EFFORT within
// the one model (grok-4.6@xhigh → high → medium → low), a real cost/latency
// ladder the band vocabulary structurally could not express.
//
// CAPABILITY DISCIPLINE (spike §4.3): the capability axis participates as
// ABSENT — rungs carry no capability field, ordering is the declared one, and
// nothing here invents a capability fact. Measured evidence arrives through
// the routing-advice file (advice.go), never by editing this derivation.
//
// Cost is NEVER inferred from ladder rank — it always comes from the
// transport rate card (the #532 lesson): nothing in this file reads rates.
//
// TS pair: packages/nightgauge-sdk/src/eval/selectionQuery.ts — the ladder
// tests pin the two derivations to identical rungs.

// EnvelopeRung is one rung of the provider-scoped candidate ladder: the
// dispatch envelope a band-vocabulary input resolves to. Effort and Thinking
// are the registry's DECLARED facts (behavior.effort_default /
// behavior.thinking_default) and stay "" when the registry declares nothing —
// absent, never a fabricated default. There is deliberately NO capability
// field.
type EnvelopeRung struct {
	// Band is the band this rung answers for — the query INPUT vocabulary
	// (band names remain the user-facing config vocabulary until #582).
	Band string
	// ModelID is the concrete registry model id serving the band.
	ModelID string
	// Effort is the declared effort rung, or "" (undeclared).
	Effort string
	// Thinking is the declared default thinking state ("on"/"off"), or "".
	Thinking string
}

// TierBandsAscending is the band ladder weakest → strongest, derived from
// TierBandsStrongestFirst (never a second declaration).
func TierBandsAscending() []string {
	out := make([]string, len(TierBandsStrongestFirst))
	for i, b := range TierBandsStrongestFirst {
		out[len(out)-1-i] = b
	}
	return out
}

// IsTierBand reports membership in the band vocabulary — the query that
// replaces per-file hand-listed four-case switches (#581).
func IsTierBand(band string) bool {
	for _, t := range TierBandsStrongestFirst {
		if band == t {
			return true
		}
	}
	return false
}

func declaredThinking(desc models.ModelDescriptor) string {
	if desc.Behavior == nil {
		return ""
	}
	if td := desc.Behavior.ThinkingDefault; td == "on" || td == "off" {
		return td
	}
	return ""
}

// topSupportedEffort is the highest declared effort rung, or "" for a model
// with no effort axis.
func topSupportedEffort(supported []string) string {
	for i := len(models.EffortOrder) - 1; i >= 0; i-- {
		if containsString(supported, models.EffortOrder[i]) {
			return models.EffortOrder[i]
		}
	}
	return ""
}

// nextLowerSupportedEffort is the next declared rung strictly below effort
// ("" input means "below the top"), or "" when none exists.
func nextLowerSupportedEffort(supported []string, effort string) string {
	start := len(models.EffortOrder) - 1
	for i, e := range models.EffortOrder {
		if e == effort {
			start = i - 1
			break
		}
	}
	for i := start; i >= 0; i-- {
		if containsString(supported, models.EffortOrder[i]) {
			return models.EffortOrder[i]
		}
	}
	return ""
}

func containsString(list []string, v string) bool {
	for _, s := range list {
		if s == v {
			return true
		}
	}
	return false
}

// CandidateLadder is the provider-scoped candidate ladder, strongest rung
// first. transport "" skips the reachability filter.
//
// Derivation, all from registry facts (TS pair: candidateLadder):
//
//   - each band resolves to the provider's current non-deprecated model
//     serving it (registry tiers — membership);
//   - a model whose transports[transport].served is explicitly false is
//     excluded (fail-closed, #579); an ABSENT transport fact passes through —
//     the unexpressed/pending state must not be read as unserved;
//   - a model serving ONE band rungs at its declared behavior.effort_default
//     ("" when undeclared);
//   - a model serving SEVERAL bands descends through its declared
//     supported_efforts: strongest band at the TOP declared effort, each
//     weaker band one declared rung lower; a band that cannot descend further
//     yields no rung (a duplicate rung would re-create the #532 "downgrade is
//     a no-op" lie).
//
// PROVENANCE of the multi-band descent rule: spike #568 §4.1 derives it for
// the FULLY-collapsed provider (xai, all four bands on grok-4.6), where the
// effort rungs are the only ladder the provider has. Applying the same rule
// to PARTIALLY-collapsed providers (google's pro/flash pairs, openai's
// gpt-5.6-sol) is this file's generalization, not a spike mandate: it
// synthesizes effort points (gemini-2.5-pro@medium, gpt-5.6-sol@high) that
// no registry field declares as a band's serving envelope. Those synthesized
// rungs are DECLARED-ladder shape only — nothing dispatches them today (the
// wire effort is chain-resolved, and EvaluateDowngrade skips same-model
// rungs; see retry_engine.go's SAME-MODEL RUNGS note) — and any consumer
// that starts executing rung efforts on a partially-collapsed provider must
// first decide whether these synthesized points are wanted behavior or the
// descent should be restricted to fully-collapsed providers. The twin ladder
// tests pin the current shape so that decision cannot happen by accident.
//
// Local providers (ollama/lm-studio) have no registry entries by design, so
// their ladder is empty and callers keep the configured local model.
func CandidateLadder(provider, transport string) []EnvelopeRung {
	rungs := make([]EnvelopeRung, 0, len(TierBandsStrongestFirst))
	// Last emitted effort per model id (multi-band descent). A model can
	// legitimately rung with effort "" — track emission separately.
	lastEffort := map[string]string{}

	for _, band := range TierBandsStrongestFirst {
		desc, ok := models.Resolve(provider, band)
		if !ok || desc.Provider != provider {
			continue
		}
		if transport != "" {
			if served, known := desc.ServedByTransport(transport); known && !served {
				continue
			}
		}

		var effort string
		if prev, seen := lastEffort[desc.ID]; seen {
			lower := nextLowerSupportedEffort(desc.SupportedEfforts, prev)
			if lower == "" {
				continue // cannot descend further — no rung
			}
			effort = lower
		} else if len(desc.Tiers) > 1 {
			effort = topSupportedEffort(desc.SupportedEfforts)
		} else {
			effort = desc.EffortDefault()
		}
		lastEffort[desc.ID] = effort
		rungs = append(rungs, EnvelopeRung{
			Band:     band,
			ModelID:  desc.ID,
			Effort:   effort,
			Thinking: declaredThinking(desc),
		})
	}
	return rungs
}

// ResolveBandEnvelope resolves one band input to its dispatch envelope for a
// provider — the query that replaces "band → hardcoded ladder position".
// ok=false when the provider has no (reachable) model for the band.
func ResolveBandEnvelope(provider, band, transport string) (EnvelopeRung, bool) {
	for _, rung := range CandidateLadder(provider, transport) {
		if rung.Band == band {
			return rung, true
		}
	}
	return EnvelopeRung{}, false
}

// EscalationCeilingBand is the band an automatic escalation may not exceed. A
// POLICY constant, not a registry fact: Fable (the frontier tier at ~2× Opus)
// is reachable only by explicit opt-in — the frontier mode envelope, a
// per-run override, or an explicit per-stage model — never by the
// post-failure escalation walk. Pins the pre-cutover [haiku sonnet opus]
// escalation ceiling. Mirrors ESCALATION_CEILING_BAND (selectionQuery.ts).
const EscalationCeilingBand = TierOpus

// EscalationLadder is the bands the post-failure escalation walk may
// traverse, weakest first — membership derived from the registry (a band
// with no live model for the provider is no escalation target), order from
// the band ladder, ceiling from EscalationCeilingBand. Replaces the
// hand-inlined ["haiku", "sonnet", "opus"] escalation ladders (#581).
func EscalationLadder(provider string) []string {
	ceiling := tierRank(EscalationCeilingBand)
	out := []string{}
	for _, band := range TierBandsAscending() {
		if tierRank(band) > ceiling {
			continue
		}
		if desc, ok := models.Resolve(provider, band); ok && desc.Provider == provider {
			out = append(out, band)
		}
	}
	return out
}
