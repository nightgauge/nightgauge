package orchestrator

import (
	"context"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"

	"github.com/nightgauge/nightgauge/internal/config"
	"github.com/nightgauge/nightgauge/internal/state"
)

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
// THE IN-FLIGHT SET IS A UNION of three sources, and each covers what the others
// cannot:
//
//   - as.state.Running is authoritative for the runs THIS process dispatched,
//     and nothing else protects a run whose worktree has already been reclaimed
//     (post-merge cleanup ran, the stack has not been torn down yet);
//   - the active-worktree scan covers runs this process did NOT dispatch: a
//     cross-repo worktree registered in a sibling root, and a run left behind by
//     a previous orchestrator incarnation. state.Running is empty for those —
//     startup recovery clears it;
//   - the per-root SNAPSHOT scan (state.ActiveIssuesFromSnapshots, the same
//     machine-wide source the CLI worktree sweep uses) covers a live run that has
//     NEITHER: not dispatched here, and its worktree legitimately gone. That is
//     not hypothetical — the merged-worktree sweep in this very cycle reclaims
//     exactly the worktrees state.Running does not cover, so without this half the
//     protection would depend on a directory another pass is allowed to delete.
//
// Taking any of them alone is a live run's volumes. `down -v` removes named
// volumes and nothing recovers them, which is why an unreadable source vetoes the
// whole pass (the undetermined bail in reconcileOrphanedComposeProjects) instead
// of shrinking the protected set.
//
// THE CANDIDATE SET IS BOUNDED TO THE SAME ROOTS (#442). `docker compose ls
// --all` lists every `issue-N` project on the host, while all three protection
// halves are bounded by s.repoScanRoots() — so before #442 a live run in a repo
// this workspace never registered was protected by nothing and its stack was
// torn down. The bound is the project's own compose ConfigFiles: a project whose
// files do not resolve inside a scanned root is skipped, loudly, by
// reconcileOrphanedComposeProjects (composeProjectWithinRoots has the rule).
//
// ORDERING INVARIANT, since the protection snapshot is taken before the candidate
// list is fetched (inside the reconcile): safe only while dispatch happens on the
// cycle goroutine AFTER this pass. runCycle is serialized by the single Start
// loop and the only Running-append site (enqueueItem) runs later in the same
// cycle, so no stack can appear between the two reads. A dispatcher that appends
// to Running from another goroutine must be unioned in here before this ordering
// is trusted.
//
// PACING — this runs under the same graph-TTL gate as the other reconciles, so a
// stopped, cooling-down or fully saturated fleet reconciles nothing until a slot
// frees. `nightgauge cleanup` is the operator door in the meantime.
func (as *AutonomousScheduler) sweepOrphanedComposeProjects(ctx context.Context) {
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

	// One resolution of the roots feeds the determined bit, the worktree scan AND
	// the snapshot scan, so the verdict describes exactly the root set the union
	// was built from (repoRootsResolver is a live callback over workspace
	// registration).
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
	if snapshotIssues, ok := as.snapshotInFlightIssues(roots); ok {
		for n := range snapshotIssues {
			inFlight[n] = true
		}
	} else {
		determined = false
	}

	as.scheduler.reconcileOrphanedComposeProjects(ctx, inFlight, determined, roots)
}

// snapshotInFlightIssues reads the machine-wide in-flight set from every root's
// runtime snapshots, plus whether that answer is DETERMINED.
//
// Each root is canonicalized to its main checkout first (config.MainCheckoutRoot):
// a linked worktree's `.nightgauge/pipeline` exists and is always empty, so an
// un-canonicalized root answers "nothing is running" with no error — the exact
// silent-blindness this pass cannot afford (#410). A root that does not exist is
// skipped, mirroring execution.ActiveWorktreeIssues: a deleted sibling holds no
// runs and must not permanently disable reconciliation. A root that exists but
// canonicalizes to nothing, or whose state dir cannot be read, is a FAILED READ
// and undetermines the whole answer.
//
// A paused run's snapshot protects its stack here, which is the intended
// semantics of a pause: `--resume later` means the containers are still that
// run's, not debris. THAT PROTECTION IS BOUNDED (#443): every snapshot arm
// except the unstattable one stops vouching once the file is older than
// runstate.SnapshotRetention, so a pause nobody resumed in fourteen days no
// longer appears in res.Issues and its stack becomes an orphan candidate here
// as well as a reclaimable worktree in the CLI sweep. This consumer is named
// deliberately rather than left to inherit the change silently: its action is
// `docker compose down -v`, which destroys named volumes.
//
// Under `serve` this is not even a behaviour change: the IPC orphan reconciler
// runs in that same process and already COLLECTS a paused snapshot past the same
// runstate.SnapshotRetention, so the file this arm used to read was already
// being deleted underneath it. Under `autonomous run`, where no orphan
// reconciler exists, the bound is the intended semantics — a pause nobody
// resumed in a fortnight is not a pending decision, it is a stack holding
// volumes and a worktree against a run that will never continue. Either way the
// scan's warnings are logged just below, so an aged-out pause is named before
// anything it used to protect is torn down.
func (as *AutonomousScheduler) snapshotInFlightIssues(roots []string) (map[int]bool, bool) {
	out := map[int]bool{}
	for _, root := range roots {
		if _, err := os.Stat(root); err != nil {
			if os.IsNotExist(err) {
				continue
			}
			log.Printf("%s: WARN cannot stat repo root %s: %v — the snapshot half of the in-flight set is UNDETERMINED", composeReconcileLogPrefix, root, err)
			return nil, false
		}
		main := config.MainCheckoutRoot(root)
		if main == "" {
			log.Printf("%s: WARN %s resolves to no main checkout (not a git work tree) — the snapshot half of the in-flight set is UNDETERMINED", composeReconcileLogPrefix, root)
			return nil, false
		}
		res, err := state.ActiveIssuesFromSnapshots(state.PipelineStateDir(main))
		if err != nil {
			log.Printf("%s: WARN snapshot scan failed at %s: %v — the snapshot half of the in-flight set is UNDETERMINED", composeReconcileLogPrefix, main, err)
			return nil, false
		}
		for _, w := range res.Warnings {
			log.Printf("%s: %s: %s", composeReconcileLogPrefix, main, w)
		}
		for n := range res.Issues {
			out[n] = true
		}
	}
	return out, true
}

// composeProjectWithinRoots decides whether a compose project is one the
// workspace can vouch for: every one of its compose files must resolve inside a
// scanned root (#442). It returns (true, "") for a candidate and (false, reason)
// for a skip; the reason is the operator-facing text of the skip line.
//
// Containment is a path relation, never a string prefix — `/ws/repo` must not
// vouch for `/ws/repo-other`. Each root and each file is cleaned and resolved
// through symlinks first, so a file reached through a link that leaves the root
// is outside, and a root that is itself a link still contains its own files.
//
// A file whose tail no longer exists is resolved through its DEEPEST EXISTING
// ANCESTOR, with the missing suffix appended unchanged. That is deliberate: the
// population this pass exists to reap is a stack whose worktree the post-merge
// cleanup already removed — the compose file went with it. Refusing every
// missing file would make the reconcile skip exactly its own orphans, and
// nothing about a missing tail widens what the resolution can vouch for: a
// symlink anywhere in the existing part of the chain is still followed, and a
// path with no existing ancestor under any root resolves outside them all.
//
// What is conservative: no ConfigFiles at all (docker reported none, or an older
// docker omitted the field) is a skip, and so is any root or file the OS cannot
// resolve — "could not tell" is never "inside".
func composeProjectWithinRoots(files []string, roots []string) (bool, string) {
	if len(files) == 0 {
		return false, "no resolvable compose files"
	}
	resolvedRoots := make([]string, 0, len(roots))
	for _, root := range roots {
		if root == "" {
			continue
		}
		r, err := filepath.EvalSymlinks(filepath.Clean(root))
		if err != nil {
			// A root that does not exist holds no files; one that exists but
			// cannot be resolved cannot vouch for anything either way.
			continue
		}
		resolvedRoots = append(resolvedRoots, r)
	}
	var outside []string
	for _, f := range files {
		resolved, err := resolveDeepestExisting(f)
		if err != nil {
			return false, "no resolvable compose files"
		}
		if !pathInsideAny(resolved, resolvedRoots) {
			outside = append(outside, resolved)
		}
	}
	if len(outside) > 0 {
		return false, fmt.Sprintf("compose files outside scanned roots (%s)", strings.Join(outside, ", "))
	}
	return true, ""
}

// resolveDeepestExisting evaluates symlinks along the longest existing prefix
// of p and re-appends the missing remainder. A relative path is not resolvable:
// docker records absolute compose paths, and a relative one cannot be placed
// under any root without guessing a working directory.
func resolveDeepestExisting(p string) (string, error) {
	p = filepath.Clean(p)
	if !filepath.IsAbs(p) {
		return "", fmt.Errorf("compose path %q is not absolute", p)
	}
	var suffix []string
	for {
		if resolved, err := filepath.EvalSymlinks(p); err == nil {
			parts := append([]string{resolved}, suffix...)
			return filepath.Join(parts...), nil
		} else if !os.IsNotExist(err) {
			return "", err
		}
		parent := filepath.Dir(p)
		if parent == p {
			return "", fmt.Errorf("no existing ancestor for %q", p)
		}
		suffix = append([]string{filepath.Base(p)}, suffix...)
		p = parent
	}
}

// pathInsideAny reports whether p is root itself or below it, for any root, by
// path relation: filepath.Rel must not climb out. Both sides are already
// cleaned and symlink-resolved by the caller.
func pathInsideAny(p string, roots []string) bool {
	for _, root := range roots {
		rel, err := filepath.Rel(root, p)
		if err != nil {
			continue
		}
		if rel == "." || (rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))) {
			return true
		}
	}
	return false
}
