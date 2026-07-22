package github

import (
	"fmt"
	"testing"

	"github.com/nightgauge/nightgauge/internal/intelligence/survival"
)

// survivalRec builds a terminal survival record with the given verdict and a
// unique merge commit SHA (derived from n) so batches of records dedupe
// correctly against the calibration ledger.
func survivalRec(n int, verdict survival.Verdict) survival.Record {
	rec := survival.NewPending("nightgauge/nightgauge", 4150+n, 4200+n, fmt.Sprintf("sha-%d", n), "2026-06-01T12:00:00Z", "main")
	rec.Verdict = verdict
	return rec
}

func TestApplySurvivalVerdicts_SingleReverted_NoPenaltyBelowFloor(t *testing.T) {
	dir := t.TempDir()
	makeTestModel(t, dir)
	svc := NewOutcomeService(dir)

	res := svc.ApplySurvivalVerdicts([]survival.Record{survivalRec(1, survival.Reverted)})

	if !res.Recorded {
		t.Fatalf("expected Recorded=true, err=%q", res.Error)
	}
	if res.PenaltiesApplied != 0 {
		t.Errorf("PenaltiesApplied = %d, want 0 (below minObservationsForAdjust)", res.PenaltiesApplied)
	}
	if res.Confidence != defaultSurvivalConfidence {
		t.Errorf("Confidence = %v, want unchanged default %v", res.Confidence, defaultSurvivalConfidence)
	}

	model := loadModel(t, dir)
	if model.PredictionAccuracy.Survival == nil {
		t.Fatal("survival calibration state not persisted")
	}
	if model.PredictionAccuracy.Survival.NegativeObservations != 1 {
		t.Errorf("negative_observations = %d, want 1", model.PredictionAccuracy.Survival.NegativeObservations)
	}
}

func TestApplySurvivalVerdicts_RevertedAtFloor_TriggersPenalty(t *testing.T) {
	dir := t.TempDir()
	makeTestModel(t, dir)
	svc := NewOutcomeService(dir)

	var last SurvivalCalibrationResult
	for i := 1; i <= minObservationsForAdjust; i++ {
		last = svc.ApplySurvivalVerdicts([]survival.Record{survivalRec(i, survival.Reverted)})
	}

	if last.PenaltiesApplied != 1 {
		t.Errorf("PenaltiesApplied on the 5th reverted record = %d, want 1", last.PenaltiesApplied)
	}
	wantConfidence := defaultSurvivalConfidence - confidencePenalty
	if last.Confidence != wantConfidence {
		t.Errorf("Confidence = %v, want %v (one penalty applied)", last.Confidence, wantConfidence)
	}

	model := loadModel(t, dir)
	sc := model.PredictionAccuracy.Survival
	if sc.NegativeObservations != minObservationsForAdjust {
		t.Errorf("negative_observations = %d, want %d", sc.NegativeObservations, minObservationsForAdjust)
	}
	if sc.PenaltiesApplied != 1 {
		t.Errorf("penalties_applied = %d, want 1", sc.PenaltiesApplied)
	}
}

func TestApplySurvivalVerdicts_SingleSurvived_NoRewardBelowFloor(t *testing.T) {
	dir := t.TempDir()
	makeTestModel(t, dir)
	svc := NewOutcomeService(dir)

	res := svc.ApplySurvivalVerdicts([]survival.Record{survivalRec(1, survival.Survived)})

	if res.RewardsApplied != 0 {
		t.Errorf("RewardsApplied = %d, want 0 (fewer than %d finalized survivals)", res.RewardsApplied, minObservationsForAdjust)
	}
	if res.Confidence != defaultSurvivalConfidence {
		t.Errorf("Confidence = %v, want unchanged default %v — a lone survived record must never be rewarded (spike #4134 bias-safety rule)", res.Confidence, defaultSurvivalConfidence)
	}
}

func TestApplySurvivalVerdicts_FiveSurvived_TriggersWeakReward(t *testing.T) {
	dir := t.TempDir()
	makeTestModel(t, dir)
	svc := NewOutcomeService(dir)

	var last SurvivalCalibrationResult
	for i := 1; i <= minObservationsForAdjust; i++ {
		last = svc.ApplySurvivalVerdicts([]survival.Record{survivalRec(i, survival.Survived)})
	}

	if last.RewardsApplied != 1 {
		t.Errorf("RewardsApplied on the 5th survived record = %d, want 1", last.RewardsApplied)
	}
	wantConfidence := defaultSurvivalConfidence + confidenceBoost
	if last.Confidence != wantConfidence {
		t.Errorf("Confidence = %v, want %v (one weak reward applied)", last.Confidence, wantConfidence)
	}

	// Bias-safety: the reward must stay strictly smaller than the penalty
	// magnitude (spike #4134 §1.2 — "preserve the penalty > boost asymmetry").
	if confidenceBoost >= confidencePenalty {
		t.Fatalf("confidenceBoost (%v) must be < confidencePenalty (%v)", confidenceBoost, confidencePenalty)
	}
}

func TestApplySurvivalVerdicts_PendingAndUnobserved_NeverAffectCalibration(t *testing.T) {
	dir := t.TempDir()
	makeTestModel(t, dir)
	svc := NewOutcomeService(dir)

	pending := survivalRec(1, survival.Pending)
	unobserved := survivalRec(2, survival.Unobserved)

	res := svc.ApplySurvivalVerdicts([]survival.Record{pending, unobserved})

	if res.PenaltiesApplied != 0 || res.RewardsApplied != 0 {
		t.Errorf("pending/unobserved must never move calibration, got penalties=%d rewards=%d", res.PenaltiesApplied, res.RewardsApplied)
	}
	if res.Confidence != defaultSurvivalConfidence {
		t.Errorf("Confidence = %v, want unchanged default %v", res.Confidence, defaultSurvivalConfidence)
	}

	model := loadModel(t, dir)
	sc := model.PredictionAccuracy.Survival
	if sc == nil {
		t.Fatal("expected survival state to be persisted (unobserved is terminal and gets ledgered)")
	}
	if sc.NegativeObservations != 0 || sc.PositiveObservations != 0 {
		t.Errorf("expected no observation counters to move, got negative=%d positive=%d", sc.NegativeObservations, sc.PositiveObservations)
	}

	// The pending record (non-terminal) must not even be ledgered — it should
	// still be eligible for processing once it actually finalizes.
	for _, sha := range sc.ProcessedSHAs {
		if sha == pending.MergeCommitSHA {
			t.Errorf("pending record %q must not be marked processed", pending.MergeCommitSHA)
		}
	}
}

func TestApplySurvivalVerdicts_DedupBySHA_IsIdempotent(t *testing.T) {
	dir := t.TempDir()
	makeTestModel(t, dir)
	svc := NewOutcomeService(dir)

	rec := survivalRec(1, survival.Reverted)

	first := svc.ApplySurvivalVerdicts([]survival.Record{rec})
	if first.Processed != 1 {
		t.Fatalf("first call Processed = %d, want 1", first.Processed)
	}

	second := svc.ApplySurvivalVerdicts([]survival.Record{rec})
	if second.Processed != 0 {
		t.Errorf("second call with the same SHA Processed = %d, want 0 (idempotent dedup)", second.Processed)
	}
	if second.Recorded {
		t.Error("second call should be a no-op (Recorded=false) — nothing changed to persist")
	}

	model := loadModel(t, dir)
	if model.PredictionAccuracy.Survival.NegativeObservations != 1 {
		t.Errorf("negative_observations = %d, want 1 (no double count)", model.PredictionAccuracy.Survival.NegativeObservations)
	}
}

func TestApplySurvivalVerdicts_EmptyBatchIsNoOp(t *testing.T) {
	dir := t.TempDir()
	makeTestModel(t, dir)
	svc := NewOutcomeService(dir)

	res := svc.ApplySurvivalVerdicts(nil)
	if res.Recorded || res.Processed != 0 {
		t.Errorf("expected no-op on empty batch, got %+v", res)
	}
}
