package orchestrator

import "log"

// worktreeSweepLogPrefix labels every line the autonomous merged-worktree sweep
// emits, including the guards that decline to run it. A skip logged under a
// different name is a skip nobody greps for.
const worktreeSweepLogPrefix = "autonomous: worktree sweep"

// sweepMergedWorktrees reclaims pipeline-created worktrees whose branch is
// already fully represented on the default branch, folded into the autonomous
// reconcile pass (#110) the same way the survival sweep is — poll-on-reconcile,
// no new cron.
//
// Inline post-merge cleanup cannot cover this on its own: a run swept mid-flight
// (window reload, crash, kill) never reaches its cleanup step, so its worktree
// outlives the merge that retired its branch. Only a reconcile pass sees those.
//
// This is the sweep's ONLY production caller (#403), and the reason is
// as.state.Running: it is the authoritative in-flight set for this process, and
// this process is the one dispatching the runs. The decision logic lives in
// runMergedWorktreeSweep — read its doc before changing what is protected here.
func (as *AutonomousScheduler) sweepMergedWorktrees() {
	if as.scheduler == nil {
		return
	}

	// Runs in flight are off-limits regardless of how merged their branch
	// looks: a run whose PR has just landed may still have stages to execute
	// in that directory. A nil state is not an empty one — it is an in-flight
	// set we could not read, and acting on it would remove every merged
	// worktree in the workspace including the live ones. Fail open, loudly
	// (#302): "could not look" is never "nothing is running".
	as.mu.Lock()
	if as.state == nil {
		as.mu.Unlock()
		log.Printf("%s: WARN autonomous state unavailable — skipping the merged-worktree sweep; the in-flight set is this sweep's only protection for a just-merged run", worktreeSweepLogPrefix)
		return
	}
	inFlight := make(map[int]bool, len(as.state.Running))
	for _, item := range as.state.Running {
		inFlight[item.Number] = true
	}
	as.mu.Unlock()

	// One resolution of the roots feeds both the determined bit and the sweep,
	// so the verdict describes exactly the root set that is then acted on
	// (repoRootsResolver is a live callback over workspace registration).
	//
	// as.mu is released above before anything touches the scheduler, and
	// neither repoScanRoots nor activeWorktreeIssuesFor takes s.mu — the two
	// locks are acquired strictly sequentially and never nested, so there is no
	// ordering between them to invert.
	roots := as.scheduler.repoScanRoots()
	_, determined := as.scheduler.activeWorktreeIssuesFor(roots)
	runMergedWorktreeSweep(roots, inFlight, determined, worktreeSweepLogPrefix)
}
