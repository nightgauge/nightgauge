package routing

import (
	"fmt"
	"strings"
)

// exclusions.go — `model_routing.excluded_models` / `excluded_efforts` (#2063).
//
// The candidate ladder is the ONE source of eligibility: CandidateLadderExcluding
// drops excluded model ids and excluded effort rungs, and a band is eligible
// on a provider iff that filtered ladder still has a rung for it. Every
// band-level consumer (the routed envelope, the minimum_model floor, the
// run.retryWithEscalation forced tier, the escalation ladder) asks
// EligibleBand rather than filtering on its own.
//
// TS pair: packages/nightgauge-sdk/src/eval/selectionQuery.ts
// (eligibleBand / applyExclusions); the shared golden fixture pins both.

// Exclusions is the workspace's excluded model ids and effort levels. The zero
// value excludes nothing and is the no-op path — no caller branches on it.
type Exclusions struct {
	Models  []string
	Efforts []string
}

// IsZero reports whether nothing is excluded.
func (e Exclusions) IsZero() bool { return len(e.Models) == 0 && len(e.Efforts) == 0 }

// ExcludesModel reports whether a registry model id is excluded.
func (e Exclusions) ExcludesModel(id string) bool { return containsString(e.Models, id) }

// ExcludesEffort reports whether an effort level is excluded. "" (a model with
// no effort axis) is never excluded.
func (e Exclusions) ExcludesEffort(effort string) bool {
	return effort != "" && containsString(e.Efforts, effort)
}

// allowedEfforts is supported minus the excluded efforts.
func (e Exclusions) allowedEfforts(supported []string) []string {
	if len(e.Efforts) == 0 {
		return supported
	}
	out := make([]string, 0, len(supported))
	for _, s := range supported {
		if !e.ExcludesEffort(s) {
			out = append(out, s)
		}
	}
	return out
}

// ResolveExclusions reads the workspace's exclusion lists. Every read failure
// is "nothing excluded", the same fail-open rule as resolveMaxModel: config
// load validates the lists, so a value that reaches here unreadable has
// already been rejected where an operator sees it.
func ResolveExclusions(workspaceRoot string) Exclusions {
	mr := readModelRouting(workspaceRoot)
	return Exclusions{Models: trimAll(mr.ExcludedModels), Efforts: trimAll(mr.ExcludedEfforts)}
}

func trimAll(in []string) []string {
	var out []string
	for _, s := range in {
		if s = strings.TrimSpace(s); s != "" {
			out = append(out, s)
		}
	}
	return out
}

// EligibleBand returns band when the exclusion-filtered ladder has a rung for
// it, else the strongest eligible band BELOW it — never above, so an
// exclusion cannot silently widen spend. ok=false when no eligible band
// remains at or below band.
//
// A value outside the band vocabulary (a user-defined local model) and a band
// the provider does not serve at all are returned unchanged: there is no rung
// to exclude, and judging them would reroute what exclusions never named.
func EligibleBand(provider, band string, ex Exclusions) (string, bool) {
	if ex.IsZero() || !IsTierBand(band) || !ladderHasBand(CandidateLadder(provider, ""), band) {
		return band, true
	}
	eligible := CandidateLadderExcluding(provider, "", ex)
	rank := tierRank(band)
	for _, b := range TierBandsStrongestFirst {
		if tierRank(b) <= rank && ladderHasBand(eligible, b) {
			return b, true
		}
	}
	return "", false
}

func ladderHasBand(rungs []EnvelopeRung, band string) bool {
	for _, r := range rungs {
		if r.Band == band {
			return true
		}
	}
	return false
}

// ApplyExclusions lowers the envelope's ceiling and floor to the strongest
// eligible band at or below each. A `minimum_model: fable` floor with Fable
// excluded resolves to opus: the floor is automatic routing, not a pin. A
// bound with nothing eligible below it is left alone — the dispatch-time
// EligibleBand check refuses the stage with a NoEligibleRungError there.
func ApplyExclusions(env ModeEnvelope, provider string, ex Exclusions) ModeEnvelope {
	if band, ok := EligibleBand(provider, env.Ceiling, ex); ok {
		env.Ceiling = band
	}
	if band, ok := EligibleBand(provider, env.Floor, ex); ok {
		env.Floor = band
	}
	return env
}

// EligibleEffort returns effort when it is not excluded, else the next lower
// supported non-excluded effort of the model serving band, else that band's
// eligible rung effort. "" passes through (no effort resolved: the model's
// declared default, which the ladder already vetted, rules).
func EligibleEffort(provider, band, effort string, ex Exclusions) string {
	if !ex.ExcludesEffort(effort) {
		return effort
	}
	for _, r := range CandidateLadderExcluding(provider, "", ex) {
		if r.Band != band {
			continue
		}
		if desc, ok := resolveProviderModel(provider, band); ok {
			if lower := nextLowerSupportedEffort(ex.allowedEfforts(desc.SupportedEfforts), effort); lower != "" {
				return lower
			}
		}
		return r.Effort
	}
	return effort
}

// NoEligibleRungError is the dispatch failure when exclusions leave a stage
// no model and effort to run on. It names everything an operator needs to
// fix the config: the stage, the provider, and both lists.
type NoEligibleRungError struct {
	Stage           string
	Provider        string
	ExcludedModels  []string
	ExcludedEfforts []string
}

func (e *NoEligibleRungError) Error() string {
	return fmt.Sprintf("no eligible model for stage %s on provider %s: "+
		"model_routing.excluded_models=[%s] excluded_efforts=[%s] leave no rung at or below the routed band",
		e.Stage, e.Provider, strings.Join(e.ExcludedModels, ", "), strings.Join(e.ExcludedEfforts, ", "))
}
