package routing

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"strings"
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
// the stage's routed-tier envelope, never outside them. Since #606 the Go
// dispatch path attributes a JOB CLASS at issue pickup (JobClassForLabels —
// the same conservative type-label mapping the TS jobClassForIssue applies)
// and consumes advice at exact job-class keys with exact-over-model backoff,
// mirroring the TS pickAdvice selection step for step — the (model, *, *)
// cross-class pooling this file used to run was the dual-path family #340
// removed, retired with it. No job class ⇒ no advice: the axis query alone
// decides, exactly as on the TS resolver.

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

// JobClassForLabels returns the eval job class an issue's labels DIRECTLY
// name, or "" — the Go pair of the TS jobClassForIssue (skillRunner.ts),
// attributed once at issue pickup where labels are already read (#606).
//
// Deliberately CONSERVATIVE (#581): only the type labels whose vocabulary
// coincides with the eval lane's JOB_CLASSES map (docs, bug/bugfix,
// refactor); everything else returns "" and the axis query alone decides.
// Inventing an equivalence (e.g. feature → backend-logic) would apply
// measurements to work they never measured. Mirrors the TS `.find()`
// semantics: the FIRST type-prefixed label decides, matched or not.
func JobClassForLabels(labels []string) string {
	for _, label := range labels {
		lowered := strings.ToLower(strings.TrimSpace(label))
		if !strings.HasPrefix(lowered, "type:") {
			continue
		}
		switch strings.TrimPrefix(lowered, "type:") {
		case "docs":
			return "docs"
		case "bug", "bugfix":
			return "bugfix"
		case "refactor":
			return "refactor"
		}
		return ""
	}
	return ""
}

// pickForMode selects the winning entry for a mode's posture — the Go mirror
// of pickForMode (routingAdvisor.ts), consuming the entry's own stamped
// aggregates (quality_per_dollar included) rather than recomputing them.
func pickForMode(pool []RoutingAdviceEntry, mode PerformanceMode) RoutingAdviceEntry {
	sorted := make([]RoutingAdviceEntry, len(pool))
	copy(sorted, pool)
	sort.SliceStable(sorted, func(i, j int) bool {
		a, b := sorted[i], sorted[j]
		switch mode {
		case ModeEfficiency:
			if a.MeanCostUsd != b.MeanCostUsd {
				return a.MeanCostUsd < b.MeanCostUsd
			}
			return a.MeanQuality > b.MeanQuality
		case ModeElevated: // the advisor's `balanced` posture
			return a.QualityPerDollar > b.QualityPerDollar
		default: // maximum, frontier: highest quality, ties cheaper
			if a.MeanQuality != b.MeanQuality {
				return a.MeanQuality > b.MeanQuality
			}
			return a.MeanCostUsd < b.MeanCostUsd
		}
	})
	return sorted[0]
}

// AdviseBand returns the band the eval evidence recommends for the issue's
// job class under a performance mode, or "" when the evidence does not
// justify (or cannot express) a re-pick. The selection mirrors the TS
// pickAdvice (routingAdvice.ts) step for step — the #340 convergence #606
// landed — then applies the band/envelope gate the wire vocabulary needs:
//
//   - no job class ⇒ no advice (the TS resolver's `if (!jobClass) return`);
//   - only ADVISABLE entries for exactly that job class participate (the
//     sample floor was enforced at materialization; sparse combos are
//     visible in the file but never applied);
//   - exact-backoff entries are preferred over (model, *, *) aggregates;
//   - the stamped quality floor binds the cost-driven modes
//     (efficiency/elevated); maximum/frontier consider everything;
//   - the winning model must map onto a registry band (TierBand) and that
//     band must already sit INSIDE the caller's envelope — advice re-picks
//     within the clamps, never outside them;
//   - an empty result means the axis query alone decides, which reproduces
//     pre-advice behavior exactly.
func AdviseBand(advice RoutingAdvice, jobClass string, mode PerformanceMode, envelope ModeEnvelope) string {
	if jobClass == "" {
		return ""
	}
	forClass := make([]RoutingAdviceEntry, 0, len(advice.Entries))
	for _, e := range advice.Entries {
		if e.Advisable && e.JobClass == jobClass {
			forClass = append(forClass, e)
		}
	}
	if len(forClass) == 0 {
		return ""
	}
	pool := make([]RoutingAdviceEntry, 0, len(forClass))
	for _, e := range forClass {
		if e.Backoff == "exact" {
			pool = append(pool, e)
		}
	}
	if len(pool) == 0 {
		pool = forClass
	}
	eligible := pool
	if mode == ModeEfficiency || mode == ModeElevated {
		floor := qualityFloorFor(advice)
		floored := make([]RoutingAdviceEntry, 0, len(pool))
		for _, e := range pool {
			if e.MeanQuality >= floor {
				floored = append(floored, e)
			}
		}
		if len(floored) > 0 {
			eligible = floored
		}
	}
	winner := pickForMode(eligible, mode)

	band := TierBand(winner.ModelID)
	if band == "" {
		return "" // a model outside the band vocabulary cannot be dispatched here
	}
	if ClampToEnvelope(band, envelope) != band {
		return "" // advice may re-pick within the envelope, never outside it
	}
	return band
}
