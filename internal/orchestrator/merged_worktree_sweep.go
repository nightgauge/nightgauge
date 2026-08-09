package orchestrator

import (
	"log"

	"github.com/nightgauge/nightgauge/internal/execution"
)

// runMergedWorktreeSweep is the ONE merged-worktree sweep (#403). Both
// schedulers route through it: the autonomous reconcile pass (the copy that
// runs in production) and the plain scheduler's startup pass. They differ only
// in where their in-flight set comes from and what they call themselves in the
// log — everything that decides whether a directory is removed lives here, so
// the two paths cannot drift apart on protection again.
//
// # What protects a worktree, and what does not
//
// The protected set is exactly the CALLER'S IN-FLIGHT RUNS — `state.Running`
// for the autonomous scheduler, the queue for the plain one. It covers the one
// race the content check cannot: a run whose PR has just landed still has
// stages to execute in that directory, so its branch reads fully merged while
// the worktree is very much alive.
//
// Everything else is protected by the merge test itself, which is the primary
// guard and not a fallback: a worktree is only reclaimed once its branch
// content is already represented on the default branch. Adopted, rehydrating,
// cross-repo, and mid-re-key runs (ADR 017) are unmerged by definition — they
// have work in the directory that has not landed — so the sweep never reaches
// them. `execution.SweepMergedWorktrees` additionally refuses anything locked,
// detached, dirty, or not pipeline-named.
//
// The active-worktree scan is therefore NOT unioned into the protected set on
// either path, and the `determined` bit is the only thing it contributes.
// Before #403 the plain Scheduler did union it, which made that sweep a
// structural no-op: `execution.ActiveWorktreeIssues` walks the same roots with
// the same `git worktree list` and the same `issue-NNN` parser as the sweep's
// own candidate enumeration, so every candidate protected itself by
// construction. Restoring that path's function means dropping the union, not
// exporting it to the path that still works.
//
// # Why an undetermined answer stops the sweep
//
// `determined=false` means "I could not read the worktree list", which is not
// "there are no worktrees" (#296). The sweep enumerates its candidates from
// exactly the listing that just failed, so an unreadable root does not shrink
// the protection — it makes the whole enumeration untrustworthy, and this
// function REMOVES DIRECTORIES. Skip, and say so loudly: a silent skip is
// indistinguishable from a clean sweep, and this pass is the only thing that
// notices leaked worktrees (#110/#302).
//
// Best-effort and strictly non-blocking throughout: a per-repo failure is
// logged and those worktrees stay for the next reconcile. Unlike the
// neighbouring sweeps this one spends no forge quota — it is local git only.
func runMergedWorktreeSweep(roots []string, inFlight map[int]bool, determined bool, logPrefix string) {
	if len(roots) == 0 {
		// Not a benign "nothing to do": even a single-repo workspace resolves
		// its primary root, so an empty set means the root lookup failed. This
		// IS the leak-detection pass — a bare return makes worktree
		// accumulation invisible for as long as the misconfiguration lasts,
		// and every cycle reads as a clean sweep (#302).
		log.Printf("%s: WARN no repo scan roots resolved — skipping the merged-worktree sweep; leaked worktrees stay undetected until the root lookup is fixed", logPrefix)
		return
	}
	if !determined {
		log.Printf("%s: WARN active-worktree set is undetermined — skipping the merged-worktree sweep", logPrefix)
		return
	}

	for _, root := range roots {
		res, err := execution.SweepMergedWorktrees(execution.WorktreeSweepOptions{
			RepoRoot:     root,
			ActiveIssues: inFlight,
		})
		if err != nil {
			log.Printf("%s: %s: %v", logPrefix, root, err)
			continue
		}
		for _, wt := range res.Reclaimed {
			log.Printf("%s: reclaimed %s (branch %s, issue #%d — content already on %s)",
				logPrefix, wt.Path, wt.Branch, wt.IssueNumber, res.BaseRef)
		}
		if len(res.Errors) > 0 {
			log.Printf("%s: %s: %d removal failure(s): %v", logPrefix, root, len(res.Errors), res.Errors)
		}
	}
}
