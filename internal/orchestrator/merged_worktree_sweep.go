package orchestrator

import (
	"log"

	"github.com/nightgauge/nightgauge/internal/execution"
)

// runMergedWorktreeSweep is the ONE merged-worktree sweep (#403), and it has
// exactly ONE production caller: (*AutonomousScheduler).sweepMergedWorktrees.
// Everything that decides whether a directory is removed lives here, so no
// second copy can drift apart from it on protection again.
//
// # What protects a worktree, and what does not
//
// Read this before adding a caller. The protection is weaker and narrower than
// "we only delete merged worktrees" suggests.
//
// The merge/content test is one of SEVERAL reclaim doors in
// `execution.classifyWorktree`, not a gate every candidate must pass. A clean
// pipeline-named worktree sitting ON the default branch is reclaimed on the
// strength of that alone, with no content comparison at all — the default
// branch is the comparison base, so the merge test structurally cannot apply
// there. "The merge test protects everything" is false for that door.
//
// Where the merge test does apply, it protects a run only until that run's PR
// LANDS — not until the run finishes. From the merge onward the branch reads
// fully merged while the worktree is still executing stages, and the ONLY thing
// standing between it and `git worktree remove --force` is `inFlight`. That
// window is not instantaneous: the terminal path runs ~160 lines of bookkeeping
// between `onPipelineComplete` and `execution.Manager.CleanupWorktree` finally
// removing the directory, including a telemetry emit and a remote-branch
// cleanup that both talk to the network.
//
// And `inFlight` covers only the runs the CALLING PROCESS knows about. It is
// not a machine-global registry: a bare Scheduler built by a CLI command
// (`queue list`, the promote gates) has an empty queue and no visibility into
// an autonomous fleet running in another process, so a sweep from there would
// see every live run as unprotected. That is why the autonomous reconcile is
// the only caller — `state.Running` is authoritative for the process that
// dispatched those runs, in that process. Any new caller has to answer the same
// question first: is your in-flight set authoritative for the runs whose
// directories you are about to delete?
//
// One known race remains and is benign. `onPipelineComplete` vacates the item
// from `state.Running` at the top of the terminal path, ~160 lines before
// `CleanupWorktree` removes the directory, so a reconcile landing in that tail
// sees a merged worktree with nothing protecting it and may reclaim it first.
// That is the correct outcome — the run is done with the directory — and both
// deleters tolerate losing the race: `execution.reclaimWorktree` falls back to
// `os.RemoveAll` + `git worktree prune` when `git worktree remove` fails, and
// CleanupWorktree treats an already-gone worktree as success.
//
// The active-worktree scan is NOT unioned into the protected set; the
// `determined` bit is the only thing it contributes. Before #403 the plain
// Scheduler unioned it, which made that sweep a structural no-op:
// `execution.ActiveWorktreeIssues` walks the same roots with the same `git
// worktree list` and the same `issue-NNN` parser as the sweep's own candidate
// enumeration, so every candidate protected itself by construction. That
// receiver was DELETED rather than armed. It rode `NewScheduler` → `loadQueue`,
// which is the construction path of every `nightgauge queue …` invocation and
// of the deps-gate / baseline-gate promote commands. A constructor is the wrong
// place to remove directories no matter how good the protection is — being a
// no-op was the only reason `queue list`, a printf loop, never destroyed a live
// run's worktree.
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
