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

// survivalSweepInterval paces the sweep independently of the cycle cadence.
//
// Survival detection makes a GitHub call per DUE record, so it must not run at
// the 30s active tick. It used to be paced by riding `graphWasFresh`, which
// coupled it to graph-TTL decisions it has nothing to do with — and, worse,
// stacked a third condition on top of two others. Five minutes matches the
// baseline-deferral sweep: the fact it observes (has this merge been reverted?)
// changes on a human timescale.
const survivalSweepInterval = 5 * time.Minute

// finalizeDueSurvivalRecords is the indirection point for the `gh`-backed
// survival sweep, mirroring reconcileExecGh (#492). gh.FinalizeDueSurvivalRecords
// shells out to `gh api .../commits` once per DUE record, so any test that seeds
// a pending record and then drives a cycle reaches the network — three real
// GitHub round-trips per run of this package's suite, measured with a PATH shim,
// in tests that assert only on the sweep's PACING and never look at what the
// forge said. The seam lets the test binary refuse the call outright.
var finalizeDueSurvivalRecords = gh.FinalizeDueSurvivalRecords

// sweepSurvivalRecords finalizes due post-merge survival records. Best-effort
// and strictly non-blocking: a load/detection error is logged and the records
// stay pending for the next pass. When there are no pending records it does
// zero GitHub work.
//
// Called from the autonomous reconcile pass ABOVE the slot gate (#992) — it
// dispatches nothing, and a busy fleet is exactly when merges accumulate. It is
// no longer the ONLY automatic caller: FinalizeDueSurvivalRecords runs on the
// post-merge hook path, which does not require autonomous mode at all.
func (as *AutonomousScheduler) sweepSurvivalRecords(ctx context.Context) {
	if as.workspaceRoot == "" {
		return
	}

	// Pace gate. A zero timestamp sweeps immediately: a daemon starting up after
	// a long stop is precisely the case with the biggest backlog of due records,
	// and the one that must not wait another interval.
	as.mu.Lock()
	if last := as.lastSurvivalSweepAt; !last.IsZero() && time.Since(last) < survivalSweepInterval {
		as.mu.Unlock()
		return
	}
	as.lastSurvivalSweepAt = time.Now()
	as.mu.Unlock()

	window := as.survivalWindowDays
	if window <= 0 {
		window = survival.DefaultWindowDays
	}

	res, err := finalizeDueSurvivalRecords(ctx, as.workspaceRoot, time.Now(), window)
	if err != nil {
		log.Printf("autonomous: survival sweep error: %v", err)
		return
	}
	if res.Finalized > 0 || res.Errors > 0 {
		log.Printf("autonomous: survival sweep: scanned=%d due=%d finalized=%d errors=%d verdicts=%v",
			res.Scanned, res.Due, res.Finalized, res.Errors, res.ByVerdict)
	}
}
