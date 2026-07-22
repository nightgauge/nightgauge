package survival

import (
	"context"
	"fmt"
	"time"
)

// Detector performs the deterministic, GitHub-backed observation for one pending
// record. It is the only non-pure surface in this package; the verdict logic
// (DecideVerdict) consumes its Observation. Implementations live outside this
// package (internal/github) so the state machine stays unit-testable with a mock.
type Detector interface {
	// Observe returns the revert/breakage signals for a merged record. It MUST be
	// conservative: when ancestry or green-at-merge cannot be positively
	// established, report no breakage (never "any main failure"). Errors are
	// returned so the sweep can leave the record pending and retry next sweep.
	Observe(ctx context.Context, rec Record) (Observation, error)
}

// SweepResult summarizes one finalization sweep for logging/diagnostics.
type SweepResult struct {
	Scanned   int             `json:"scanned"`   // pending records considered
	Due       int             `json:"due"`       // records whose window had elapsed
	Finalized int             `json:"finalized"` // records transitioned to terminal
	ByVerdict map[Verdict]int `json:"byVerdict"` // terminal verdict histogram
	Errors    int             `json:"errors"`    // detection errors (record left pending)

	// FinalizedRecords holds the actual records that transitioned to a
	// terminal verdict during this sweep (#4152/#4153). It exists so a caller
	// can feed just-finalized verdicts straight into calibration without a
	// second full-journal scan — this package remains capture+detection only
	// and does no calibration math itself. Excluded from JSON (`json:"-"`) so
	// CLI/diagnostic output shape is unchanged; it's an in-process handoff.
	FinalizedRecords []Record `json:"-"`
}

// Sweep finalizes due pending survival records. For each pending record whose
// observation window has elapsed it runs detection, applies DecideVerdict, and
// appends the terminal line on a transition. It is best-effort and non-fatal:
// a detection error leaves that record pending for the next sweep. Records not
// yet due are skipped untouched (no API spend).
//
// This is invoked from the autonomous reconcile sweep (poll-on-reconcile; no new
// cron, per spike #4134 §1.4) and from the `survival sweep` CLI command.
func Sweep(ctx context.Context, store *Store, det Detector, now time.Time, windowDays int) (SweepResult, error) {
	res := SweepResult{ByVerdict: map[Verdict]int{}}
	if windowDays <= 0 {
		windowDays = DefaultWindowDays
	}

	pending, err := store.Pending()
	if err != nil {
		return res, fmt.Errorf("survival sweep: load pending: %w", err)
	}
	res.Scanned = len(pending)

	for _, rec := range pending {
		if !IsDue(rec, now, windowDays) {
			continue
		}
		res.Due++

		obs, obsErr := det.Observe(ctx, rec)
		if obsErr != nil {
			// Leave the record pending; the next reconcile sweep retries.
			res.Errors++
			continue
		}

		updated, changed := DecideVerdict(rec, now, windowDays, obs)
		if !changed {
			continue
		}
		if finErr := store.Finalize(updated); finErr != nil {
			res.Errors++
			continue
		}
		res.Finalized++
		res.ByVerdict[updated.Verdict]++
		res.FinalizedRecords = append(res.FinalizedRecords, updated)
	}

	return res, nil
}
