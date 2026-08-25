package orchestrator

import (
	"context"
	"log"
	"time"
)

// baselineDeferralSweepInterval paces the baseline-deferral promote sweep on
// the autonomous cycle.
//
// The sweep asks GitHub for the recent run history of one workflow per
// deferred item, so its cost scales with the number of paused items rather
// than with the board — far cheaper than the epic-rollup sweep next door, and
// far more expensive than the in-memory `blocked_dependency` resume.
//
// Five minutes is chosen against what it is waiting for: a red baseline goes
// green when someone lands a fix on `main` and CI finishes, which is a
// human-plus-CI cadence measured in tens of minutes. Sweeping at the 30s
// active cycle would issue the same query sixty times per baseline repair to
// observe a fact that changed once. Matching the graph TTL keeps the item's
// resume inside the same working session that fixed the baseline without
// paying for a query nobody's answer has changed.
const baselineDeferralSweepInterval = 5 * time.Minute

// sweepBaselineDeferrals is the automatic trigger that `baseline_ci_red`
// items never had (#885).
//
// The sibling `blocked_dependency` kind has been resumed automatically since
// #231 (resumeBlockedDependencyPause, called from both triage-promotion
// passes). `baseline_ci_red` had no resumer running anywhere: the promote
// logic existed and worked, but its only caller was the
// `nightgauge baseline-gate promote` CLI verb, which nothing invoked on a
// schedule. Documentation claimed a `.github/workflows/baseline-defer-sweep.yml`
// cron did it; that workflow never existed and could not — the queue is
// local-first and gitignored, so a runner has no queue to promote and
// anything it wrote would die with the runner (#881).
//
// The daemon is therefore the only correct home: it is the one process with
// both a periodic tick and the local filesystem the queue lives on.
//
// It decides only WHEN the sweep runs. WHAT it does is
// PromoteBaselineDeferrals, shared verbatim with the CLI verb — the two must
// not drift apart the way this kind and its sibling already did.
//
// Every failure is non-fatal and logged. A sweep that can wedge the
// autonomous loop is worse than no sweep, so an erroring repo is skipped and
// retried on the next interval.
func (as *AutonomousScheduler) sweepBaselineDeferrals(ctx context.Context) {
	if as.scheduler == nil {
		return // delegated-dispatch mode: no local queue to promote
	}
	if as.baselineEvaluatorFn == nil {
		return // no evaluator wired (constructed without a forge client)
	}
	if len(as.repos) == 0 {
		return
	}

	// Pace gate. A zero timestamp sweeps immediately: a daemon starting up
	// after the operator fixed a red baseline is precisely the case that
	// should not wait, and it is the case #881 left the operator to handle by
	// hand.
	as.mu.Lock()
	if last := as.lastBaselineSweepAt; !last.IsZero() && time.Since(last) < baselineDeferralSweepInterval {
		as.mu.Unlock()
		return
	}
	as.lastBaselineSweepAt = time.Now()
	as.mu.Unlock()

	// Cheap exit before any forge call: nothing is deferred, so there is
	// nothing to ask GitHub about. ListPausedByKind reads the in-memory queue.
	if len(as.scheduler.ListPausedByKind("baseline_ci_red")) == 0 {
		return
	}

	for _, rc := range as.repos {
		if rc.Owner == "" || rc.Name == "" {
			continue
		}
		eval, branch, enabled, err := as.baselineEvaluatorFn(rc.Owner, rc.Name)
		if err != nil {
			log.Printf("autonomous: baseline-deferral sweep: %s/%s: %v", rc.Owner, rc.Name, err)
			continue
		}
		if !enabled {
			continue
		}
		summary := PromoteBaselineDeferrals(ctx, as.scheduler, eval,
			rc.Owner, rc.Name, branch, as.baselineGreenThreshold, true)

		if len(summary.Promoted) > 0 {
			for _, p := range summary.Promoted {
				log.Printf("autonomous: resumed baseline_ci_red pause for #%d — %s is green again on %s",
					p.IssueNumber, p.Workflow, branch)
			}
			// A promotion put work back in the queue. Snap the cadence back to
			// base so the newly-eligible item is dispatched on the next tick
			// rather than after an idle backoff.
			as.TriggerRescan()
		}
		for _, e := range summary.Errors {
			log.Printf("autonomous: baseline-deferral sweep: #%d: %s", e.IssueNumber, e.Error)
		}
	}
}
