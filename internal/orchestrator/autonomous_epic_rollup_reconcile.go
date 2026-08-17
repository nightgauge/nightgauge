package orchestrator

import (
	"context"
	"log"
	"time"

	"github.com/nightgauge/nightgauge/internal/depgraph"
	gh "github.com/nightgauge/nightgauge/internal/github"
)

// epicRollupReconcileInterval paces the board-wide epic-rollup backstop
// (github.EpicService.ReconcileBoard) on the autonomous cycle.
//
// The cycle itself ticks at AutonomousConfig.ScanInterval (30s active, up to
// 10m under rate pressure) and the neighbouring sweeps pace themselves to the
// graph TTL (5m). This sweep is deliberately far slower than either, because
// it is a different shape of work: ReconcileBoard paginates EVERY item on the
// board — closed ones included, so it cannot use the cheap `is:open` filtered
// query the graph build uses — and then issues per-epic sub-issue re-checks
// and mutations on top. Running it at the graph TTL would spend a
// board-sized GraphQL bill twelve times an hour to observe a fact that only
// changes when a human merges something.
//
// 30 minutes is chosen against the latency that actually matters: pipeline
// merges are already rolled up within seconds by hooks.EvaluatePostMerge on
// the post-merge hot path, so this sweep only ever catches HAND-merged work —
// a human-cadence event where half an hour of rollup lag is invisible. It
// bounds the extra cost to at most two board sweeps per hour per board
// (against a 5000/hr GraphQL budget) while still converging inside a single
// working session.
const epicRollupReconcileInterval = 30 * time.Minute

// boardIdentity is the (owner, project, ownerType) triple that identifies one
// project board. Several repos routinely share a single board (the N:1
// workspace topology), and ReconcileBoard sweeps a BOARD, not a repo — so the
// sweep de-duplicates on this key to avoid paying for the same pagination
// once per member repo.
type boardIdentity struct {
	owner     string
	project   int
	ownerType gh.OwnerType
}

// reconcileEpicRollup is the automatic trigger for the board-wide post-merge
// backstop (internal/github/reconcile.go). Its rules — R1 stale-Status→Done,
// R2 open-epic-with-all-subs-closed→closed, R3 orphaned subs of a completed
// epic→closed — are implemented exactly once, in EpicService.ReconcileBoard;
// this function only decides WHEN they run, never what they do.
//
// Why it must exist (#656): before this wiring, the only production caller of
// ReconcileBoard was the `nightgauge project reconcile` CLI verb, which has no
// automatic trigger. Rollup therefore happened only as a side effect of the
// pipeline performing the merge itself (the post-merge hook), and AGENTS.md
// mandates manual `gh pr merge --squash` as the ROUTINE path. Every
// hand-merged PR left its parent epic open forever — epic #342 sat open with
// every child closed after a 27-PR hand-merged session. Rollup was a property
// of who merged; it needs to be a property of the board.
//
// Every failure here is non-fatal and logged. A backstop that can wedge the
// autonomous loop is worse than no backstop, so a board that errors is skipped
// and retried on the next interval rather than aborting the cycle.
func (as *AutonomousScheduler) reconcileEpicRollup(ctx context.Context) {
	if as.reconcileBoardFn == nil {
		return // no reconciler wired (constructed without a forge client)
	}
	if len(as.repos) == 0 {
		return
	}

	// Pace gate. A zero timestamp runs immediately: a scheduler starting up
	// right after a hand-merged session is precisely the #656 case, and making
	// it wait half an hour to notice would defeat the point.
	as.mu.Lock()
	if last := as.lastEpicRollupSweepAt; !last.IsZero() && time.Since(last) < epicRollupReconcileInterval {
		as.mu.Unlock()
		return
	}
	as.lastEpicRollupSweepAt = time.Now()
	as.mu.Unlock()

	// as.repos already carries the per-repo board number resolved through
	// config.ResolveRepoProject (see schedulerRepoConfig in cmd/nightgauge) —
	// the runtime authority. Nothing here re-reads the workspace YAML.
	seen := make(map[boardIdentity]bool, len(as.repos))
	for _, rc := range as.repos {
		if rc.Owner == "" || rc.Project <= 0 {
			// Explicit skip, never a guess. Defaulting to the primary board
			// here would sweep — and CLOSE ISSUES ON — a board this repo does
			// not belong to.
			log.Printf("autonomous: epic-rollup reconcile: skipping %s — no project board resolved (owner=%q project=%d)",
				repoConfigLabel(rc), rc.Owner, rc.Project)
			continue
		}
		id := boardIdentity{owner: rc.Owner, project: rc.Project, ownerType: rc.OwnerType}
		if seen[id] {
			continue // shared board, already swept this pass
		}
		seen[id] = true

		res, err := as.reconcileBoardFn(ctx, rc.Owner, rc.Project, rc.OwnerType)
		if err != nil {
			log.Printf("autonomous: epic-rollup reconcile: board %s/%d failed (non-fatal, retrying in %s): %v",
				rc.Owner, rc.Project, epicRollupReconcileInterval, err)
			continue
		}
		if res == nil {
			continue
		}
		// Only speak when something actually changed. A converged board is the
		// steady state and must not print a line every half hour.
		if res.EpicsClosed > 0 || res.IssuesSyncedToDone > 0 || res.OrphanSubsClosed > 0 {
			log.Printf("autonomous: epic-rollup reconcile: board %s/%d checked=%d epics_closed=%d synced_to_done=%d orphan_subs_closed=%d",
				rc.Owner, rc.Project, res.Checked, res.EpicsClosed, res.IssuesSyncedToDone, res.OrphanSubsClosed)
		}
		for _, w := range res.Warnings {
			log.Printf("autonomous: epic-rollup reconcile: board %s/%d warning: %s", rc.Owner, rc.Project, w)
		}
	}
}

// repoConfigLabel renders a RepoConfig for logging without producing the misleading
// "/name" that FullName() emits for an empty owner.
func repoConfigLabel(rc depgraph.RepoConfig) string {
	if rc.Owner == "" {
		if rc.Name == "" {
			return "(unnamed repo)"
		}
		return rc.Name
	}
	return rc.FullName()
}
