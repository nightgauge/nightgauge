package orchestrator

// Cache-affinity routing (#55). When the previous stage warmed a prompt cache
// on this provider and the router now picks a cheaper model, the switch pays
// the warm prefix again as a cache write. ApplyCacheAffinity keeps the previous
// model only when that cold-start penalty exceeds what the cheaper model
// saves. Mirrored by applyCacheAffinity (packages/nightgauge/src/utils/
// skillRunner.ts); both are driven by testdata/cache_affinity_cases.json.

// AffinityRates are a model's USD per 1M token rates.
type AffinityRates struct {
	Input         float64 `json:"input"`
	CacheRead     float64 `json:"cache_read"`
	CacheCreate5m float64 `json:"cache_creation_5m"`
}

// AffinityInput is everything the decision reads. Rates answers ok=false for
// a model it cannot price, which leaves the candidate untouched.
type AffinityInput struct {
	Stage            string
	Candidate        string
	Previous         string
	SameProvider     bool
	PrevWithinExpiry bool
	ImplicitCache    bool
	WarmPrefixTokens int
	Rates            func(model string) (AffinityRates, bool)
}

// AffinityDecision is the model to dispatch and why. PenaltyUSD is the
// estimated cost of starting the candidate's cache cold.
type AffinityDecision struct {
	Model      string
	Kept       bool
	PenaltyUSD float64
	SavingUSD  float64
	Reason     string
}

// Affinity decision reasons. Shared with the TS mirror through the fixture.
const (
	AffinityNoPrevious      = "no_previous"
	AffinitySameModel       = "same_model"
	AffinityNoImplicitCache = "no_implicit_cache"
	AffinityExpired         = "expired"
	AffinityUnknownRates    = "unknown_rates"
	AffinityProviderChange  = "provider_change"
	AffinityMechanicalStage = "mechanical_stage"
	AffinityNotCheaper      = "candidate_not_cheaper"
	AffinitySwitchSaves     = "switch_saves"
	AffinityKeptWarm        = "kept_warm"
	// AffinityBounded: a keep the dispatch refused because an explicit
	// choice, a pin, an escalation, the label cap or the mode ceiling binds.
	// Set by the scheduler, never by ApplyCacheAffinity.
	AffinityBounded = "bounded"
)

// affinityExemptStages are the stages a keep never applies to: short,
// mechanical stages whose prompt shares little with the stage before.
var affinityExemptStages = map[string]bool{
	"issue-pickup": true,
	"pr-create":    true,
	"pr-merge":     true,
}

// ApplyCacheAffinity decides whether to keep the previous model. It never
// raises the dispatch above the previous stage's model and never keeps across
// a provider change; it reports the cold-start penalty either way.
func ApplyCacheAffinity(in AffinityInput) AffinityDecision {
	d := AffinityDecision{Model: in.Candidate}
	switch {
	case in.Previous == "" || in.WarmPrefixTokens <= 0:
		d.Reason = AffinityNoPrevious
		return d
	case in.Previous == in.Candidate && in.SameProvider:
		d.Reason = AffinitySameModel
		return d
	case !in.ImplicitCache:
		d.Reason = AffinityNoImplicitCache
		return d
	case !in.PrevWithinExpiry:
		d.Reason = AffinityExpired
		return d
	}
	if in.Rates == nil {
		d.Reason = AffinityUnknownRates
		return d
	}
	cand, ok := in.Rates(in.Candidate)
	if !ok {
		d.Reason = AffinityUnknownRates
		return d
	}
	warm := float64(in.WarmPrefixTokens) / 1e6
	d.PenaltyUSD = warm * (cand.CacheCreate5m - cand.CacheRead)
	if !in.SameProvider {
		d.Reason = AffinityProviderChange
		return d
	}
	if affinityExemptStages[in.Stage] {
		d.Reason = AffinityMechanicalStage
		return d
	}
	prev, ok := in.Rates(in.Previous)
	if !ok {
		d.Reason = AffinityUnknownRates
		return d
	}
	d.SavingUSD = warm * (prev.Input - cand.Input)
	switch {
	case d.SavingUSD <= 0:
		d.Reason = AffinityNotCheaper
	case d.PenaltyUSD > d.SavingUSD:
		d.Model, d.Kept, d.Reason = in.Previous, true, AffinityKeptWarm
	default:
		d.Reason = AffinitySwitchSaves
	}
	return d
}
