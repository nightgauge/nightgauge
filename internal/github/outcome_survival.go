// Package github — outcome_survival.go materializes the SAFE half (#4152) and
// the deferred weak-reward half (#4153) of spike #4134 §1.2: a bias-safe
// calibration rule fed by finalized post-merge survival verdicts
// (internal/intelligence/survival).
//
// A survival.Record carries no issue-type or pattern attribution — only the
// merge commit SHA, so unlike adjustTypeModifiers (per-type) this is a single,
// coarse-grained calibration dimension: how much the model's merge-time
// predictions should be trusted given REAL post-merge outcomes, as opposed to
// the size-at-merge proxy the rest of outcome.go calibrates on.
//
// Bias-safe rule (spike #4134 §1.2, mirrored from the existing asymmetric
// confidenceBoost/confidencePenalty rule):
//   - reverted / broke (proven negative)      → confidence PENALTY, gated
//     behind minObservationsForAdjust cumulative negative observations (#4152).
//   - survived (weak positive, terminal only) → confidence BOOST, gated
//     behind minObservationsForAdjust cumulative *finalized* survived
//     observations (#4153) — deliberately separate and weaker than the
//     penalty, and never applied on unproven/pending survival.
//   - pending / unobserved                    → no signal, ignored.
package github

import (
	"fmt"
	"math"
	"time"

	"github.com/nightgauge/nightgauge/internal/intelligence/survival"
)

// maxProcessedSurvivalSHAs bounds the dedup ledger the same way
// maxRecentOutcomes bounds recent_outcomes — old entries roll off rather than
// growing the model file unboundedly.
const maxProcessedSurvivalSHAs = 500

// survivalCalibration is the persisted calibration state derived from
// finalized survival verdicts. Confidence starts at a neutral prior (0.5,
// consistent with the seeded pattern-confidence range used elsewhere in the
// complexity model — see skills/nightgauge-repo-init's bootstrap
// defaults) and moves toward 0 on proven negatives / toward 1 on finalized
// positives, always clamped to [0, 1]. A neutral (not maxed-out) starting
// point matters: it leaves headroom in both directions so the weak reward
// (#4153) is actually observable rather than being permanently absorbed by a
// ceiling clamp.
type survivalCalibration struct {
	Confidence           float64  `yaml:"confidence"`
	NegativeObservations int      `yaml:"negative_observations"`    // cumulative finalized reverted+broke
	PositiveObservations int      `yaml:"positive_observations"`    // cumulative finalized survived
	PenaltiesApplied     int      `yaml:"penalties_applied"`        // times confidencePenalty actually fired
	RewardsApplied       int      `yaml:"rewards_applied"`          // times confidenceBoost actually fired
	ProcessedSHAs        []string `yaml:"processed_shas,omitempty"` // dedup ledger, bounded
}

// defaultSurvivalConfidence is the starting trust level before any survival
// ground truth has been observed — a neutral prior, not a ceiling (see
// survivalCalibration doc comment).
const defaultSurvivalConfidence = 0.5

// SurvivalCalibrationResult is the JSON-serializable result of applying a
// batch of survival verdicts to calibration.
type SurvivalCalibrationResult struct {
	Recorded         bool    `json:"recorded"`
	Processed        int     `json:"processed"`
	PenaltiesApplied int     `json:"penaltiesApplied"`
	RewardsApplied   int     `json:"rewardsApplied"`
	Confidence       float64 `json:"confidence"`
	Error            string  `json:"error,omitempty"`
}

// ApplySurvivalVerdicts applies the bias-safe survival calibration rule to a
// batch of survival records — normally the records a reconcile/CLI sweep just
// finalized (internal/intelligence/survival.Sweep's SweepResult.FinalizedRecords),
// so the calibration adjustment happens right after finalization rather than
// re-scanning the whole survival journal. It is safe to call with any subset
// (including a full store reload) because each record is deduplicated by
// merge commit SHA against a persisted ledger, so re-processing the same
// terminal record is always a no-op.
//
// Only terminal verdicts carry signal: reverted/broke apply a penalty (#4152)
// once minObservationsForAdjust negative observations have accrued; survived
// applies a weak boost (#4153) once minObservationsForAdjust *finalized*
// survived observations have accrued. pending/unobserved never move
// calibration. Non-terminal or empty-SHA records are skipped entirely.
//
// Best-effort: errors are returned on the result (never a panic/crash) so
// callers — the autonomous reconcile loop and the `survival sweep` CLI — can
// log and continue without blocking the sweep itself.
func (s *OutcomeService) ApplySurvivalVerdicts(records []survival.Record) SurvivalCalibrationResult {
	if len(records) == 0 {
		return SurvivalCalibrationResult{}
	}

	model, err := s.loadModel()
	if err != nil {
		return SurvivalCalibrationResult{Error: fmt.Sprintf("load model: %v", err)}
	}

	if model.PredictionAccuracy == nil {
		model.PredictionAccuracy = &predictionAccuracy{
			ByType:         map[string]typeStats{},
			BySize:         map[string]typeStats{},
			RecentOutcomes: []recentOutcome{},
		}
	}
	sc := model.PredictionAccuracy.Survival
	if sc == nil {
		sc = &survivalCalibration{Confidence: defaultSurvivalConfidence}
	}

	alreadyProcessed := make(map[string]bool, len(sc.ProcessedSHAs))
	for _, sha := range sc.ProcessedSHAs {
		alreadyProcessed[sha] = true
	}

	result := SurvivalCalibrationResult{}
	changed := false

	for _, rec := range records {
		if rec.MergeCommitSHA == "" || !rec.Verdict.IsTerminal() || alreadyProcessed[rec.MergeCommitSHA] {
			continue
		}

		switch rec.Verdict {
		case survival.Reverted, survival.Broke:
			// Proven ground truth — highest-value, bias-free negative signal
			// (spike #4134 §1.2). Gated on the same 5-observation floor used
			// everywhere else in this file before any modifier moves.
			sc.NegativeObservations++
			if sc.NegativeObservations >= minObservationsForAdjust {
				sc.Confidence = clampConfidence(sc.Confidence - confidencePenalty)
				sc.PenaltiesApplied++
				result.PenaltiesApplied++
			}
		case survival.Survived:
			// Weak positive — deliberately separate from and weaker than the
			// penalty path, and gated on finalized survived observations only
			// (never on pending/unproven survival, per the spike's explicit
			// bias-safety rule).
			sc.PositiveObservations++
			if sc.PositiveObservations >= minObservationsForAdjust {
				sc.Confidence = clampConfidence(sc.Confidence + confidenceBoost)
				sc.RewardsApplied++
				result.RewardsApplied++
			}
		case survival.Unobserved:
			// Terminal, but explicitly no signal (censored data) — falls
			// through to the bookkeeping below without touching Confidence.
		}

		alreadyProcessed[rec.MergeCommitSHA] = true
		sc.ProcessedSHAs = append(sc.ProcessedSHAs, rec.MergeCommitSHA)
		if len(sc.ProcessedSHAs) > maxProcessedSurvivalSHAs {
			sc.ProcessedSHAs = sc.ProcessedSHAs[len(sc.ProcessedSHAs)-maxProcessedSurvivalSHAs:]
		}
		result.Processed++
		changed = true
	}

	result.Confidence = sc.Confidence
	if !changed {
		return result
	}

	model.PredictionAccuracy.Survival = sc
	model.LastUpdated = time.Now().UTC().Format("2006-01-02")
	if err := s.saveModel(model); err != nil {
		return SurvivalCalibrationResult{Error: fmt.Sprintf("save model: %v", err)}
	}
	result.Recorded = true
	return result
}

// clampConfidence bounds a confidence value to [0, 1], mirroring the clamp
// OutcomeRecorder.ts applies to pattern confidence.
func clampConfidence(v float64) float64 {
	return math.Max(0, math.Min(1, v))
}
