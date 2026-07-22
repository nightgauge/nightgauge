package gates

import (
	"time"

	"github.com/nightgauge/nightgauge/internal/state"
)

// AnomalyKind identifies a specific anomaly detector. New detectors append a
// constant here and a row to docs/PIPELINE_ANOMALIES.md.
type AnomalyKind string

const (
	// AnomalyAtomicLLMOverrun fires when an atomic-eligible stage (one with a
	// deterministic Go-side runner — pr-merge, pr-create) ran through the LLM
	// path and burned more than the configured floor while the post-condition
	// gate still reported passed=true. The work shipped, but it cost more than
	// the deterministic path would have. The detector flags this as a hint to
	// investigate why the deterministic path didn't fire (binary missing,
	// fallback condition tripped, etc.).
	AnomalyAtomicLLMOverrun AnomalyKind = "atomic_llm_overrun"
)

// DefaultAnomalyFloorUSD is the default cost floor for the atomic-LLM-overrun
// detector. Configurable via `pipeline.anomaly_floor_usd`. The "10× the
// deterministic-path estimate" framing in Issue #3267 is operationally
// undefined because the deterministic path costs $0; the floor is the
// observable proxy.
const DefaultAnomalyFloorUSD = 0.01

// Anomaly is the in-process value produced by an anomaly detector. The
// scheduler appends Anomaly.ToState() onto V2StageDetail.Anomalies.
type Anomaly struct {
	Kind                   AnomalyKind
	Stage                  string
	ExecutionPath          string
	StageCostUSD           float64
	DeterministicPredicate string
	Timestamp              string
}

// ToState copies into the persisted state.Anomaly shape so the runtime can
// write it onto V2StageDetail.Anomalies without dragging the gates package
// into the state import graph.
func (a Anomaly) ToState() state.Anomaly {
	return state.Anomaly{
		Kind:                   string(a.Kind),
		Stage:                  a.Stage,
		ExecutionPath:          a.ExecutionPath,
		StageCostUSD:           a.StageCostUSD,
		DeterministicPredicate: a.DeterministicPredicate,
		Timestamp:              a.Timestamp,
	}
}

// atomicEligibleStages enumerates stages whose deterministic Go-side runner
// is the canonical execution path. The map value is the human-readable
// predicate that names the deterministic path that should have matched —
// surfaced in the Anomaly record and in dashboard output.
//
// Today: pr-merge (deterministic-first runner from #3259) and pr-create
// (deterministic-first runner from #3264 / Wave 2 #4). To add a new
// atomic-eligible stage, append an entry here and a row to
// docs/PIPELINE_ANOMALIES.md.
var atomicEligibleStages = map[state.PipelineStage]string{
	state.StagePRMerge:  "deterministic gh pr merge available",
	state.StagePRCreate: "deterministic gh pr create available",
}

// IsAtomicEligible reports whether the given stage has a deterministic
// runner registered. Exported so the scheduler can short-circuit the
// detector call when it isn't needed.
func IsAtomicEligible(stage state.PipelineStage) bool {
	_, ok := atomicEligibleStages[stage]
	return ok
}

// DetectAtomicLLMOverrun returns a non-nil Anomaly when an atomic-eligible
// stage ran through the LLM path, the gate still passed, and the stage cost
// exceeded floorUsd. Returns nil otherwise. Pure function — no IO, no
// clock dependence beyond the timestamp it stamps on its return value.
//
// All four conditions must hold:
//
//   - stage is atomic-eligible (pr-merge / pr-create today)
//   - executionPath == "llm" (the LLM path ran instead of the deterministic one)
//   - gatePassed is true (the work shipped — failures are already on the
//     failure path, no need for an anomaly to amplify them)
//   - stageCostUsd > floorUsd (the LLM path actually spent money worth
//     flagging — small per-stage costs are expected noise)
//
// floorUsd ≤ 0 falls back to DefaultAnomalyFloorUSD so callers don't need to
// guard against an unset config value.
func DetectAtomicLLMOverrun(stage state.PipelineStage, executionPath string,
	stageCostUsd float64, gatePassed bool, floorUsd float64,
) *Anomaly {
	predicate, eligible := atomicEligibleStages[stage]
	if !eligible {
		return nil
	}
	if executionPath != "llm" {
		return nil
	}
	if !gatePassed {
		return nil
	}
	floor := floorUsd
	if floor <= 0 {
		floor = DefaultAnomalyFloorUSD
	}
	if stageCostUsd <= floor {
		return nil
	}
	return &Anomaly{
		Kind:                   AnomalyAtomicLLMOverrun,
		Stage:                  string(stage),
		ExecutionPath:          executionPath,
		StageCostUSD:           stageCostUsd,
		DeterministicPredicate: predicate,
		Timestamp:              nowUTC(),
	}
}

// AtomicEligibleStages exposes a copy of the stage → predicate map for
// docs / dashboard introspection. The returned map is a fresh copy so
// callers can't mutate the package's source of truth.
func AtomicEligibleStages() map[state.PipelineStage]string {
	out := make(map[state.PipelineStage]string, len(atomicEligibleStages))
	for k, v := range atomicEligibleStages {
		out[k] = v
	}
	return out
}

// _ keeps time imported in the rare case nowUTC ever gets removed; the
// detector itself doesn't need time directly today.
var _ = time.RFC3339
