package routing

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
)

// advice.go — the Go reader of the routing-advice data file (#581 / spike
// #568 §4.2). The SDK eval lane materializes advisor aggregates to
// .nightgauge/model-evals/routing-advice.json and BOTH resolvers read that
// file, exactly as both read the model registry: a data-file handoff keeps
// the extension out of the routing business (threading TS advice over the
// wire would reintroduce the dual-path drift #340 removed).
//
// Consumption is opt-in (model_routing.use_eval_recommendations, default
// false) and clamp-bounded: advice may re-pick WITHIN the candidate set and
// the stage's routed-tier envelope, never outside them. The Go dispatch path
// has no job-class attribution (job class is an eval-lane dimension the
// scheduler does not compute), so Go consumes at the (model, *, *) backoff
// level of spike §4.3's hierarchy — advisable entries pooled across job
// classes per model — and the pooled evidence must still name a band inside
// the envelope before it changes anything.

// RoutingAdviceSchemaVersion pins the file shape this reader understands.
// Mirrors ROUTING_ADVICE_SCHEMA_VERSION (routingAdvice.ts).
const RoutingAdviceSchemaVersion = 1

// RoutingAdviceRelativePath mirrors ROUTING_ADVICE_RELATIVE_PATH.
const RoutingAdviceRelativePath = ".nightgauge/model-evals/routing-advice.json"

// RoutingAdviceEntry mirrors RoutingAdviceEntrySchema (routingAdvice.ts).
type RoutingAdviceEntry struct {
	JobClass string `json:"job_class"`
	ModelID  string `json:"model_id"`
	// Effort/Thinking are absent ("") on model-backoff aggregates.
	Effort           string  `json:"effort,omitempty"`
	Thinking         string  `json:"thinking,omitempty"`
	Backoff          string  `json:"backoff"`
	Samples          int     `json:"samples"`
	PassRate         float64 `json:"pass_rate"`
	MeanQuality      float64 `json:"mean_quality"`
	MeanCostUsd      float64 `json:"mean_cost_usd"`
	QualityPerDollar float64 `json:"quality_per_dollar"`
	Advisable        bool    `json:"advisable"`
}

// RoutingAdvice mirrors RoutingAdviceFileSchema (routingAdvice.ts).
type RoutingAdvice struct {
	SchemaVersion int    `json:"schema_version"`
	GeneratedAt   string `json:"generated_at"`
	MinSamples    int    `json:"min_samples"`
	// QualityFloor is the quality floor the file was BUILT with, stamped by
	// the builder like MinSamples so both consumption-side pickers apply the
	// same floor. AdviseBand falls back to the historical default (70) when
	// the field is absent/zero — fail-open, matching the reader's contract.
	QualityFloor           float64              `json:"quality_floor"`
	MinHonestSchemaVersion int                  `json:"min_honest_schema_version"`
	Entries                []RoutingAdviceEntry `json:"entries"`
}

// LoadRoutingAdvice reads the advice file under workspaceRoot. ok=false when
// the file is absent, unparseable, or a schema version this reader does not
// understand — ALWAYS fail-open: routing must never be blocked by advice, and
// an absent file just means the declared ladder rules (today's behavior).
func LoadRoutingAdvice(workspaceRoot string) (RoutingAdvice, bool) {
	if workspaceRoot == "" {
		return RoutingAdvice{}, false
	}
	data, err := os.ReadFile(filepath.Join(workspaceRoot, filepath.FromSlash(RoutingAdviceRelativePath)))
	if err != nil {
		return RoutingAdvice{}, false
	}
	var advice RoutingAdvice
	if err := json.Unmarshal(data, &advice); err != nil {
		return RoutingAdvice{}, false
	}
	if advice.SchemaVersion != RoutingAdviceSchemaVersion {
		return RoutingAdvice{}, false
	}
	return advice, true
}

// adviceModelPool is the (model, *, *) aggregate of advisable entries.
type adviceModelPool struct {
	modelID          string
	samples          int
	meanQuality      float64
	meanCostUsd      float64
	qualityPerDollar float64
}

// adviceQualityFloor is the FALLBACK quality floor — the advisor's default —
// applied only when the advice file predates the stamped `quality_floor`
// field (or carries a non-positive one). Efficiency and balanced picks must
// clear the floor; maximum/frontier consider everything.
const adviceQualityFloor = 70

// qualityFloorFor returns the floor the file was built with, or the default.
func qualityFloorFor(advice RoutingAdvice) float64 {
	if advice.QualityFloor > 0 {
		return advice.QualityFloor
	}
	return adviceQualityFloor
}

// AdviseBand returns the band the eval evidence recommends for a performance
// mode, or "" when the evidence does not justify (or cannot express) a
// re-pick:
//
//   - only advisable entries participate (the sample floor was enforced at
//     materialization; sparse combos are visible in the file but never
//     applied);
//   - entries pool per model across job classes — the (model, *, *) backoff
//     level, recorded as such in this function's contract because the Go
//     dispatch path has no job-class key;
//   - the winning model must map onto a registry band (TierBand) and that
//     band must already sit INSIDE the caller's envelope — advice re-picks
//     within the clamps, never outside them;
//   - an empty result means the axis query alone decides, which reproduces
//     pre-advice behavior exactly.
func AdviseBand(advice RoutingAdvice, mode PerformanceMode, envelope ModeEnvelope) string {
	pools := map[string]*adviceModelPool{}
	order := []string{}
	for _, e := range advice.Entries {
		if !e.Advisable {
			continue
		}
		p, ok := pools[e.ModelID]
		if !ok {
			p = &adviceModelPool{modelID: e.ModelID}
			pools[e.ModelID] = p
			order = append(order, e.ModelID)
		}
		total := float64(p.samples + e.Samples)
		p.meanQuality = (p.meanQuality*float64(p.samples) + e.MeanQuality*float64(e.Samples)) / total
		p.meanCostUsd = (p.meanCostUsd*float64(p.samples) + e.MeanCostUsd*float64(e.Samples)) / total
		p.samples += e.Samples
	}
	if len(pools) == 0 {
		return ""
	}
	candidates := make([]adviceModelPool, 0, len(pools))
	for _, id := range order {
		p := pools[id]
		if p.meanCostUsd > 0 {
			p.qualityPerDollar = p.meanQuality / p.meanCostUsd
		} else {
			p.qualityPerDollar = p.meanQuality
		}
		candidates = append(candidates, *p)
	}

	// Quality floor for the cost-driven modes — the floor the FILE was built
	// with (stamped quality_floor), mirroring pickAdvice.
	if mode == ModeEfficiency || mode == ModeElevated {
		floor := qualityFloorFor(advice)
		floored := make([]adviceModelPool, 0, len(candidates))
		for _, c := range candidates {
			if c.meanQuality >= floor {
				floored = append(floored, c)
			}
		}
		if len(floored) > 0 {
			candidates = floored
		}
	}

	sort.SliceStable(candidates, func(i, j int) bool {
		a, b := candidates[i], candidates[j]
		switch mode {
		case ModeEfficiency:
			if a.meanCostUsd != b.meanCostUsd {
				return a.meanCostUsd < b.meanCostUsd
			}
			return a.meanQuality > b.meanQuality
		case ModeElevated:
			return a.qualityPerDollar > b.qualityPerDollar
		default: // maximum, frontier: highest quality, ties cheaper
			if a.meanQuality != b.meanQuality {
				return a.meanQuality > b.meanQuality
			}
			return a.meanCostUsd < b.meanCostUsd
		}
	})

	band := TierBand(candidates[0].modelID)
	if band == "" {
		return "" // a model outside the band vocabulary cannot be dispatched here
	}
	if ClampToEnvelope(band, envelope) != band {
		return "" // advice may re-pick within the envelope, never outside it
	}
	return band
}
