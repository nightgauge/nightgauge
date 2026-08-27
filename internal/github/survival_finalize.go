package github

import (
	"context"
	"log"
	"time"

	"github.com/nightgauge/nightgauge/internal/intelligence/survival"
)

// FinalizeResult reports what one finalization pass did. It wraps the sweep's
// own result so callers that only want "did anything happen" do not have to
// reach into two structs.
type FinalizeResult struct {
	survival.SweepResult
	// CalibrationError is non-empty when the verdicts were finalized but the
	// calibration feed failed. The two are reported separately on purpose:
	// finalization is the durable half and must not be reported as failed
	// because a best-effort downstream step did.
	CalibrationError string
}

// FinalizeDueSurvivalRecords finalizes every pending survival record whose
// observation window has elapsed, then feeds the newly-finalized verdicts into
// bias-safe calibration.
//
// ONE implementation, three callers (#992). This sequence — sweep, then
// ApplySurvivalVerdicts on SweepResult.FinalizedRecords, best-effort — was
// written out three times: in the autonomous reconcile pass, in the
// `nightgauge survival sweep` verb, and now on the post-merge hook path. Two
// copies had already drifted in what they logged. A fourth caller is likely
// (a scheduled workflow), so the sequence lives here.
//
// It does ZERO GitHub work when nothing is pending, which is what makes it safe
// to call from the post-merge hook on every merge.
//
// Errors: a load or sweep failure is returned. Calibration failure is NOT —
// it lands in CalibrationError, because the verdicts are already durably
// written by then and reporting the whole pass as failed would invite a caller
// to retry a completed finalization.
func FinalizeDueSurvivalRecords(ctx context.Context, workspaceRoot string, now time.Time, windowDays int) (FinalizeResult, error) {
	return FinalizeDueSurvivalRecordsWith(ctx, workspaceRoot, NewSurvivalDetector(), now, windowDays)
}

// FinalizeDueSurvivalRecordsWith is FinalizeDueSurvivalRecords with the detector
// injected.
//
// The detector is the only surface in the whole path that talks to GitHub, so
// injecting it is what makes "does finalization actually happen off the
// autonomous path?" a testable question rather than one you answer by reading
// call sites. That question had gone unasked: the package's own Sweep had full
// unit coverage and the CLI verb was covered too — both exercise the CALLEE,
// neither asserted that any production scheduler REACHES it (#992).
func FinalizeDueSurvivalRecordsWith(ctx context.Context, workspaceRoot string, det survival.Detector, now time.Time, windowDays int) (FinalizeResult, error) {
	var out FinalizeResult
	if det == nil {
		det = NewSurvivalDetector()
	}
	if workspaceRoot == "" {
		return out, nil
	}
	if windowDays <= 0 {
		windowDays = survival.DefaultWindowDays
	}

	store := survival.NewStore(workspaceRoot)

	// Cheap exit before any GitHub call. The post-merge hook runs on every
	// merge, so "nothing pending" must cost a file read and nothing else.
	pending, err := store.Pending()
	if err != nil {
		return out, err
	}
	if len(pending) == 0 {
		return out, nil
	}

	res, err := survival.Sweep(ctx, store, det, now, windowDays)
	if err != nil {
		return out, err
	}
	out.SweepResult = res

	if len(res.FinalizedRecords) > 0 {
		cal := NewOutcomeService(workspaceRoot).ApplySurvivalVerdicts(res.FinalizedRecords)
		out.CalibrationError = cal.Error
		if cal.Error != "" {
			log.Printf("survival: calibration error after finalizing %d record(s): %v",
				len(res.FinalizedRecords), cal.Error)
		}
	}
	return out, nil
}
