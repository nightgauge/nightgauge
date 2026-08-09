package orchestrator

import (
	"log"

	"github.com/nightgauge/nightgauge/internal/execution"
)

// sweepMergedWorktrees reclaims pipeline-created worktrees whose branch is
// already fully represented on the default branch, folded into the autonomous
// reconcile pass (#110) the same way the survival sweep is — poll-on-reconcile,
// no new cron.
//
// Inline post-merge cleanup cannot cover this on its own: a run swept mid-flight
// (window reload, crash, kill) never reaches its cleanup step, so its worktree
// outlives the merge that retired its branch. Only a reconcile pass sees those.
//
// Best-effort and strictly non-blocking: a per-repo failure is logged and the
// worktrees stay for the next reconcile. Unlike the neighbouring sweeps this one
// spends no GitHub quota — it is local git only.
func (as *AutonomousScheduler) sweepMergedWorktrees() {
	if as.scheduler == nil {
		return
	}
	roots := as.scheduler.repoScanRoots()
	if len(roots) == 0 {
		// Not a benign "nothing to do": even a single-repo workspace resolves
		// its primary root, so an empty set means the root lookup failed. This
		// IS the leak-detection pass — a bare return makes worktree
		// accumulation invisible for as long as the misconfiguration lasts,
		// and every cycle reads as a clean sweep (#302).
		log.Printf("autonomous: worktree sweep: WARN no repo scan roots resolved — skipping the merged-worktree sweep; leaked worktrees stay undetected until the root lookup is fixed")
		return
	}

	// Runs in flight are off-limits regardless of how merged their branch
	// looks: a run whose PR has just landed may still have stages to execute
	// in that directory.
	as.mu.Lock()
	active := make(map[int]bool, len(as.state.Running))
	for _, item := range as.state.Running {
		active[item.Number] = true
	}
	as.mu.Unlock()

	for _, root := range roots {
		res, err := execution.SweepMergedWorktrees(execution.WorktreeSweepOptions{
			RepoRoot:     root,
			ActiveIssues: active,
		})
		if err != nil {
			log.Printf("autonomous: worktree sweep: %s: %v", root, err)
			continue
		}
		for _, wt := range res.Reclaimed {
			log.Printf("autonomous: worktree sweep: reclaimed %s (branch %s, issue #%d — content already on %s)",
				wt.Path, wt.Branch, wt.IssueNumber, res.BaseRef)
		}
		if len(res.Errors) > 0 {
			log.Printf("autonomous: worktree sweep: %s: %d removal failure(s): %v", root, len(res.Errors), res.Errors)
		}
	}
}
