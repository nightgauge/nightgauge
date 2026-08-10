package orchestrator

import "log"

// composeReconcileLogPrefix labels every line the autonomous compose reconcile
// emits, including the guards that decline to run it. A skip logged under a
// different name is a skip nobody greps for.
const composeReconcileLogPrefix = "autonomous: compose reconcile"

// sweepOrphanedComposeProjects tears down per-issue docker compose stacks whose
// run is gone, folded into the autonomous reconcile pass the same way the
// merged-worktree sweep is — poll-on-reconcile, no new cron.
//
// This is the reconcile's ONLY production caller (#410). It used to ride
// NewScheduler → loadQueue, which is the construction path of every `nightgauge
// queue …` invocation and of the deps-gate / baseline-gate promote commands: so
// `queue list`, a printf loop, ran `docker compose down -v --remove-orphans`
// plus image removal as a construction side effect. #403 moved the worktree
// sweep off that path and left this one behind; a constructor is the wrong place
// to destroy state no matter how good the protection is.
//
// THE IN-FLIGHT SET IS A UNION, and each half covers what the other cannot:
//
//   - as.state.Running is authoritative for the runs THIS process dispatched,
//     and it is the only thing that protects a run whose worktree has already
//     been reclaimed (post-merge cleanup ran, the stack has not been torn down
//     yet) — a worktree scan sees nothing for it;
//   - the active-worktree scan covers runs this process did NOT dispatch: a
//     cross-repo worktree registered in a sibling root, and a run left behind by
//     a previous orchestrator incarnation. state.Running is empty for those.
//
// Taking either half alone is a live run's volumes. `down -v` removes named
// volumes and nothing recovers them, which is why the undetermined bail (in
// reconcileOrphanedComposeProjects) refuses to act on a set it could not read.
func (as *AutonomousScheduler) sweepOrphanedComposeProjects() {
	if as.scheduler == nil {
		return
	}

	// A nil state is not an empty one — it is an in-flight set we could not
	// read, and acting on it tears down the stacks of every run this process
	// dispatched. Fail open, loudly (#302): "could not look" is never "nothing
	// is running".
	as.mu.Lock()
	if as.state == nil {
		as.mu.Unlock()
		log.Printf("%s: WARN autonomous state unavailable — skipping compose teardown; the in-flight set is half of this reconcile's protection", composeReconcileLogPrefix)
		return
	}
	inFlight := make(map[int]bool, len(as.state.Running))
	for _, item := range as.state.Running {
		inFlight[item.Number] = true
	}
	as.mu.Unlock()

	// One resolution of the roots feeds both the determined bit and the scan, so
	// the verdict describes exactly the root set the union was built from
	// (repoRootsResolver is a live callback over workspace registration).
	//
	// as.mu is released above before anything touches the scheduler, and neither
	// repoScanRoots nor activeWorktreeIssuesFor takes s.mu — the two locks are
	// acquired strictly sequentially and never nested, so there is no ordering
	// between them to invert.
	roots := as.scheduler.repoScanRoots()
	active, determined := as.scheduler.activeWorktreeIssuesFor(roots)
	if len(roots) == 0 {
		// Zero resolved roots is not a benign "nothing to do": even a
		// single-repo workspace resolves its primary root, so an empty set means
		// the root lookup failed — and activeWorktreeIssuesFor reports
		// UNDETERMINED for it, which the bail below already honours. Say so
		// under this prefix anyway, because a silent skip of a destructive pass
		// is indistinguishable from a clean one (#302).
		log.Printf("%s: WARN no repo scan roots resolved — the active-worktree half of the in-flight set is unavailable", composeReconcileLogPrefix)
	}
	for n := range active {
		inFlight[n] = true
	}

	as.scheduler.reconcileOrphanedComposeProjects(inFlight, determined)
}
