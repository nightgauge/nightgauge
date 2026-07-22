package orchestrator

import (
	"context"
	"log"
	"time"

	gh "github.com/nightgauge/nightgauge/internal/github"
	"github.com/nightgauge/nightgauge/internal/intelligence/survival"
)

// SetSurvivalWindowDays sets the post-merge survival observation window (#4151),
// normally resolved from pipeline.survival.window_days at construction. A value
// ≤ 0 leaves the default (survival.DefaultWindowDays) in effect.
func (as *AutonomousScheduler) SetSurvivalWindowDays(days int) {
	if days > 0 {
		as.survivalWindowDays = days
	}
}

// sweepSurvivalRecords finalizes due post-merge survival records by folding the
// detection sweep into the reconcile pass (#4151, spike #4134 §1.4 —
// poll-on-reconcile, no new cron). It is best-effort and strictly non-blocking:
// a load/detection error is logged and the records stay pending for the next
// reconcile. When there are no pending records it does zero GitHub work.
func (as *AutonomousScheduler) sweepSurvivalRecords(ctx context.Context) {
	if as.workspaceRoot == "" {
		return
	}
	store := survival.NewStore(as.workspaceRoot)
	pending, err := store.Pending()
	if err != nil {
		log.Printf("autonomous: survival sweep: load pending failed: %v", err)
		return
	}
	if len(pending) == 0 {
		return // nothing captured yet — skip all GitHub calls
	}

	window := as.survivalWindowDays
	if window <= 0 {
		window = survival.DefaultWindowDays
	}

	res, err := survival.Sweep(ctx, store, gh.NewSurvivalDetector(), time.Now(), window)
	if err != nil {
		log.Printf("autonomous: survival sweep error: %v", err)
		return
	}
	if res.Finalized > 0 || res.Errors > 0 {
		log.Printf("autonomous: survival sweep: scanned=%d due=%d finalized=%d errors=%d verdicts=%v",
			res.Scanned, res.Due, res.Finalized, res.Errors, res.ByVerdict)
	}

	// (#4152/#4153) Feed the verdicts this sweep just finalized into bias-safe
	// calibration — penalize proven reverted/broke, weak-reward finalized
	// survived once enough real data has accrued. Applied right after
	// Finalize() rather than re-scanning the whole journal. Best-effort and
	// non-blocking: a calibration error never fails the reconcile cycle.
	if len(res.FinalizedRecords) > 0 {
		calRes := gh.NewOutcomeService(as.workspaceRoot).ApplySurvivalVerdicts(res.FinalizedRecords)
		if calRes.Error != "" {
			log.Printf("autonomous: survival calibration error: %v", calRes.Error)
		} else if calRes.Recorded {
			log.Printf("autonomous: survival calibration: processed=%d penalties=%d rewards=%d confidence=%.3f",
				calRes.Processed, calRes.PenaltiesApplied, calRes.RewardsApplied, calRes.Confidence)
		}
	}
}
