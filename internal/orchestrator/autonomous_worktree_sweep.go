package orchestrator

// sweepMergedWorktrees reclaims pipeline-created worktrees whose branch is
// already fully represented on the default branch, folded into the autonomous
// reconcile pass (#110) the same way the survival sweep is — poll-on-reconcile,
// no new cron.
//
// Inline post-merge cleanup cannot cover this on its own: a run swept mid-flight
// (window reload, crash, kill) never reaches its cleanup step, so its worktree
// outlives the merge that retired its branch. Only a reconcile pass sees those.
//
// The decision logic lives in runMergedWorktreeSweep, shared with the plain
// scheduler's startup pass (#403); this receiver only supplies the autonomous
// in-flight set and log prefix.
func (as *AutonomousScheduler) sweepMergedWorktrees() {
	if as.scheduler == nil {
		return
	}

	// Runs in flight are off-limits regardless of how merged their branch
	// looks: a run whose PR has just landed may still have stages to execute
	// in that directory.
	as.mu.Lock()
	inFlight := make(map[int]bool, len(as.state.Running))
	for _, item := range as.state.Running {
		inFlight[item.Number] = true
	}
	as.mu.Unlock()

	// as.mu is released above before anything touches the scheduler, and
	// neither repoScanRoots nor activeWorktreeIssues takes s.mu — the two locks
	// are acquired strictly sequentially and never nested, so there is no
	// ordering between them to invert.
	_, determined := as.scheduler.activeWorktreeIssues()
	runMergedWorktreeSweep(as.scheduler.repoScanRoots(), inFlight, determined, "autonomous: worktree sweep")
}
