package doctor

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/nightgauge/nightgauge/internal/config"
	"github.com/nightgauge/nightgauge/internal/execution"
	"github.com/nightgauge/nightgauge/internal/reclaim"
)

// Leaked machine state — worktrees and stashes the pipeline created and never
// took back (#330, #332).
//
// `doctor` reported NONE of the nine leaked worktrees found by a workspace
// audit on 2026-08-04, and none of the five leaked stashes. Both were found by
// running `git worktree list` and `git stash list` by hand in each repo, months
// after the fact. That is the failure this file exists to end: reclamation
// tooling that cannot see a leak is indistinguishable from a workspace that has
// none, and the operator has no way to tell which they are looking at.
//
// Everything here is housekeeping (ADR-025): a leaked worktree is untidy, not
// broken, and never changes doctor's exit code.

// mergedPRDoorFactory hands a repo root's merged-PR second door to the scans
// below (#916). Returning nil is the closed door — an unauthenticated or
// offline machine, or a root with no GitHub origin — and every scan here works
// exactly as it did before the door existed.
//
// Passed in rather than constructed here so `doctor`'s own client is the one
// used, and so these scans stay drivable from tests with no network at all.
type mergedPRDoorFactory func(repoRoot string) execution.MergedPRLookup

// maxLeaksReported caps how many entries each check names. The list is
// evidence; the counts carry the magnitude.
const maxLeaksReported = 8

// staleWorktreeAge is how long a registered pipeline worktree may sit
// unreclaimable before `doctor` mentions it. Sized so a worktree belonging to a
// run that finished minutes ago — or one whose stage is between dispatches —
// never produces a warning; the leaks that mattered were weeks to months old.
const staleWorktreeAge = 24 * time.Hour

// leakedWorktree is one registered worktree that the sweep cannot reclaim.
type leakedWorktree struct {
	Path     string
	Repo     string
	RepoRoot string
	Branch   string
	Reason   execution.SkipReason
	// Blocking names what stood in the way of a SkipDirty verdict, so an
	// operator can tell "my work is in there" from "the pipeline scaffolded a
	// README into it" without opening the directory.
	Blocking []string
	Age      time.Duration
}

// reclaimableWorktree is one worktree the sweep would remove right now.
type reclaimableWorktree struct {
	RepoRoot string
	execution.ReclaimedWorktree
}

// scanLeakedWorktrees classifies every pipeline worktree across the workspace's
// repo roots, returning the ones that are registered, stale, and not
// reclaimable, the ones the sweep would reclaim now, and whether the scan was
// DETERMINED.
//
// determined=false is not "there are no leaks" (#296, #323). With no readable
// root set the answer is meaningless, and reporting a clean bill of health from
// a scan that never ran is precisely how nine worktrees stayed invisible.
//
// A live run's worktree is excluded by AGE, not by the active-worktree set.
// execution.ActiveWorktreeIssues answers "which issues have a worktree
// registered on disk?", which is every worktree this scan can see — feeding it
// back in as WorktreeSweepOptions.ActiveIssues (whose contract is "issues with
// a run IN FLIGHT") makes every candidate skip as active-run and the check
// reports nothing, forever. There is no readable in-flight set here, so the
// honest substitute is staleWorktreeAge: a run's own worktree is minutes old,
// and the leaks that mattered were weeks to months old. A run still going after
// a day is surfaced, which is itself worth an operator's attention.
func scanLeakedWorktrees(startDir string, now time.Time, door mergedPRDoorFactory) (leaks []leakedWorktree, reclaimable []reclaimableWorktree, determined bool) {
	roots := config.WorkspaceRepoRoots(startDir)
	if len(roots) == 0 {
		return nil, nil, false
	}

	for _, root := range roots {
		res, err := execution.SweepMergedWorktrees(execution.WorktreeSweepOptions{
			RepoRoot: root,
			DryRun:   true,
			// #916: without this a worktree whose PR merged remotely is
			// reported as a permanent `unmerged-content` leak rather than as
			// something the sweep can reclaim.
			MergedPRLookup: doorFor(door, root),
		})
		if err != nil {
			// One unreadable root undetermines the whole answer: a partial
			// scan is indistinguishable from a complete one at the call site.
			return nil, nil, false
		}
		for _, r := range res.Reclaimed {
			reclaimable = append(reclaimable, reclaimableWorktree{RepoRoot: root, ReclaimedWorktree: r})
		}
		for _, s := range res.Skipped {
			if !isLeakReason(s.Reason) {
				continue
			}
			age := worktreeAge(s.Path, now)
			if age < staleWorktreeAge {
				continue
			}
			leaks = append(leaks, leakedWorktree{
				Path: s.Path, Repo: filepath.Base(root), RepoRoot: root, Branch: s.Branch,
				Reason: s.Reason, Blocking: s.Blocking, Age: age,
			})
		}
	}
	sort.Slice(leaks, func(i, j int) bool { return leaks[i].Age > leaks[j].Age })
	sort.Slice(reclaimable, func(i, j int) bool { return reclaimable[i].Path < reclaimable[j].Path })
	return leaks, reclaimable, true
}

// isLeakReason reports whether a skip describes a worktree that is STUCK, as
// opposed to one the sweep is correctly leaving alone.
//
// The primary checkout, a hand-made worktree, a locked one, and a live run's
// are all healthy states that recur on every scan; reporting them would bury
// the real leaks in noise the operator learns to skim past. What remains is the
// set that cannot clear itself: something in the tree blocks it, or its branch
// carries work no one has landed.
func isLeakReason(r execution.SkipReason) bool {
	switch r {
	case execution.SkipDirty, execution.SkipUnmergedContent, execution.SkipNoOwnCommits:
		return true
	default:
		return false
	}
}

// worktreeAge uses the directory's own modification time. Deliberately not the
// branch's last commit: a worktree stranded with uncommitted changes has no
// commit to date it by, and that is exactly the case being reported.
func worktreeAge(path string, now time.Time) time.Duration {
	info, err := os.Stat(path)
	if err != nil {
		return 0
	}
	if d := now.Sub(info.ModTime()); d > 0 {
		return d
	}
	return 0
}

// Remedy verbs the hygiene checks declare. They name entries in the closed Go
// verb registry the remedy engine owns (ADR-025 § 3); they are never shell.
const (
	verbWorktreeSweep     = "worktree.sweep"
	verbStashSweep        = "stash.sweep"
	verbBranchDelete      = "branch.delete"
	verbWipPrune          = "wip.prune"
	verbProcessTerminate  = "process.terminate"
	verbServeLeaseReclaim = "serve_lease.reclaim"
	verbComposeCleanup    = "compose.cleanup"
)

// worktreeLeakFindings reports registered-but-stale pipeline worktrees
// (#332 AC4): one finding per worktree. A worktree the sweep would reclaim
// now gets an auto `worktree sweep` remedy; one the sweep refuses gets a
// manual remedy that says why it was not offered as a fix.
func worktreeLeakFindings(startDir string, now time.Time, door mergedPRDoorFactory) ([]Finding, string) {
	const check, code = "worktree_leaks", "NGD017"
	leaks, reclaimable, determined := scanLeakedWorktrees(startDir, now, door)
	if !determined {
		return []Finding{unverifiableFinding(check, code, SeverityHousekeeping, "leaked worktrees",
				"could not read the worktree set across the workspace's repo roots — not inside a git repository or workspace, or `git worktree list` failed")},
			"could not scan for leaked worktrees"
	}
	if len(leaks) == 0 && len(reclaimable) == 0 {
		return nil, "no stale pipeline worktrees"
	}

	var out []Finding
	for _, r := range reclaimable {
		ev := map[string]string{"path": r.Path, "repo_root": r.RepoRoot, "branch": r.Branch, "door": string(r.Door)}
		out = append(out, newFinding(check, code, SeverityHousekeeping,
			fmt.Sprintf("reclaimable pipeline worktree %s", r.Path),
			fmt.Sprintf("the worktree's branch %s carries nothing the default branch lacks (%s), so it is left-over state", r.Branch, r.Door),
			ev, []string{r.Path},
			Remedy{ID: "sweep", Kind: RemedyAuto, Verb: verbWorktreeSweep, Verify: check,
				Summary: "Reclaim merged pipeline worktrees with `nightgauge worktree sweep`",
				Preview: fmt.Sprintf("remove worktree %s (branch %s, authorized by %s) in %s", r.Path, r.Branch, r.Door, r.RepoRoot)}))
	}
	for _, l := range leaks {
		ev := map[string]string{
			"path": l.Path, "repo_root": l.RepoRoot, "branch": l.Branch,
			"reason": string(l.Reason), "age": days(int(l.Age.Hours() / 24)),
		}
		if len(l.Blocking) > 0 {
			ev["blocking"] = strings.Join(l.Blocking, ", ")
		}
		out = append(out, newFinding(check, code, SeverityHousekeeping,
			fmt.Sprintf("stale pipeline worktree %s (%s, %s)", l.Path, l.Repo, l.Reason),
			fmt.Sprintf("the sweep cannot reclaim it: %s", leakCause(l)),
			ev, []string{l.Path},
			manualRemedy("inspect", "Salvage or discard the worktree by hand", check,
				fmt.Sprintf("Not offered as a fix: the worktree holds %s the sweep cannot prove is safe to remove", leakWhat(l.Reason)),
				fmt.Sprintf("Inspect it: git -C %s status && git -C %s log --oneline origin/HEAD..HEAD", l.Path, l.Path),
				"Land or discard the work, then run `nightgauge worktree sweep`")))
	}
	return out, fmt.Sprintf("%d stale, %d reclaimable", len(leaks), len(reclaimable))
}

func leakCause(l leakedWorktree) string {
	switch l.Reason {
	case execution.SkipDirty:
		c := "it has uncommitted changes"
		if len(l.Blocking) > 0 {
			c += " (blocked by: " + strings.Join(l.Blocking, ", ") + ")"
		}
		return c
	case execution.SkipUnmergedContent:
		return "its branch carries commits the default branch does not have and no merged PR covers them"
	case execution.SkipNoOwnCommits:
		return "its branch has no commits of its own, so the sweep cannot tell a finished run from one that never committed"
	default:
		return string(l.Reason)
	}
}

func leakWhat(r execution.SkipReason) string {
	switch r {
	case execution.SkipDirty:
		return "uncommitted changes"
	case execution.SkipNoOwnCommits:
		return "a branch with no commits"
	}
	return "unlanded work"
}

// strandedBranch is one local branch, in one repo, that no worktree holds.
type strandedBranch struct {
	Repo     string
	RepoRoot string
	Branch   string
	Tip      string
	Merged   bool
	BaseRef  string
}

// strandedBranchFindings reports local branches no worktree holds (#912 AC4)
// — the leak the worktree arm above structurally cannot see, because it drives
// off `git worktree list` and these branches have no worktree left.
//
// A branch merged by the same proof `scripts/branch-merged-check.sh` applies
// (content already in the base ref, or a merged PR at its head) gets a confirm
// `branch.delete` remedy; the verb re-derives that proof at apply time. A
// branch with unique commits and no merged PR cannot be proven safe, so it
// gets a manual remedy and never an auto or confirm one.
//
// This arm does NOT fetch. A stale origin/<default> makes a just-merged branch
// read as unmerged content, so the branch is KEPT behind a manual remedy:
// staleness costs timeliness, never safety.
func strandedBranchFindings(startDir string, door mergedPRDoorFactory) ([]Finding, string) {
	const check, code = "stranded_branches", "NGD018"
	roots := config.WorkspaceRepoRoots(startDir)
	if len(roots) == 0 {
		return []Finding{unverifiableFinding(check, code, SeverityHousekeeping, "stranded branches",
			"no repo roots resolved — not inside a git repository or workspace")}, "could not scan for stranded branches"
	}

	var found []strandedBranch
	for _, root := range roots {
		scan, err := execution.ScanStrandedBranches(execution.StrandedBranchOptions{
			RepoRoot: root,
			// #916: without this the report goes quiet on any branch whose
			// files the default branch has since touched.
			MergedPRLookup: doorFor(door, root),
		})
		if err != nil {
			// One unreadable root undetermines the answer (#296, #323).
			return []Finding{unverifiableFinding(check, code, SeverityHousekeeping, "stranded branches",
				fmt.Sprintf("%s: %v", root, err))}, "could not scan for stranded branches"
		}
		for _, b := range scan.Stranded {
			found = append(found, strandedBranch{Repo: filepath.Base(root), RepoRoot: root,
				Branch: b.Name, Tip: b.Tip, Merged: true, BaseRef: scan.BaseRef})
		}
		for _, k := range scan.Kept {
			if k.Reason == execution.KeepUnmergedContent {
				found = append(found, strandedBranch{Repo: filepath.Base(root), RepoRoot: root,
					Branch: k.Name, BaseRef: scan.BaseRef})
			}
		}
	}
	if len(found) == 0 {
		return nil, "no stranded branches"
	}
	sort.Slice(found, func(i, j int) bool {
		if found[i].RepoRoot != found[j].RepoRoot {
			return found[i].RepoRoot < found[j].RepoRoot
		}
		return found[i].Branch < found[j].Branch
	})

	var out []Finding
	merged := 0
	for _, b := range found {
		ev := map[string]string{"repo_root": b.RepoRoot, "branch": b.Branch, "base_ref": b.BaseRef}
		if b.Tip != "" {
			ev["tip"] = b.Tip
		}
		if b.Merged {
			merged++
			out = append(out, newFinding(check, code, SeverityHousekeeping,
				fmt.Sprintf("merged branch %s in %s is held by no worktree", b.Branch, b.Repo),
				fmt.Sprintf("its content is already in %s (or a merged PR covers its head) and no worktree holds it, so it is left-over state", b.BaseRef),
				ev, []string{b.RepoRoot, b.Branch},
				Remedy{ID: "delete", Kind: RemedyConfirm, Verb: verbBranchDelete, Verify: check,
					Summary: "Delete the merged local branch",
					Preview: fmt.Sprintf("delete local branch %s at %s in %s; the merged proof is re-derived first, and a branch checked out in any worktree, main or master is refused", b.Branch, orUnknown(b.Tip), b.RepoRoot)}))
			continue
		}
		out = append(out, newFinding(check, code, SeverityHousekeeping,
			fmt.Sprintf("unmerged branch %s in %s is held by no worktree", b.Branch, b.Repo),
			fmt.Sprintf("it carries commits %s does not have and no merged PR covers its head", b.BaseRef),
			ev, []string{b.RepoRoot, b.Branch},
			manualRemedy("review", "Land or deliberately discard the branch", check,
				"Not offered as a fix: the branch has unique commits and no merged PR, so deleting it could lose work",
				fmt.Sprintf("Inspect it: git -C %s log --oneline %s..%s", b.RepoRoot, b.BaseRef, b.Branch),
				"Verify with `scripts/branch-merged-check.sh` before deleting anything by hand")))
	}
	return out, fmt.Sprintf("%d stranded branch(es): %d merged, %d unmerged", len(found), merged, len(found)-merged)
}

func orUnknown(s string) string {
	if s == "" {
		return "(unknown tip)"
	}
	return s
}

// pipelineStashFindings reports stashes the pipeline created and never
// reclaimed (#330 AC3), one finding per stash, each with its age.
//
// A stash recorded on a branch whose work is already landed (the default
// branch, or a branch merged and held by no worktree) gets an auto `stash
// sweep` remedy: the sweep restores it only onto that branch on a clean tree
// and skips it otherwise. Any other stash cannot be proven safe to restore and
// gets a manual remedy.
func pipelineStashFindings(startDir string, now time.Time, door mergedPRDoorFactory) ([]Finding, string) {
	const check, code = "pipeline_stashes", "NGD019"
	roots := config.WorkspaceRepoRoots(startDir)
	if len(roots) == 0 {
		return []Finding{unverifiableFinding(check, code, SeverityHousekeeping, "pipeline stashes",
			"no repo roots resolved — not inside a git repository or workspace")}, "could not scan for pipeline stashes"
	}

	var out []Finding
	oldest := 0
	for _, root := range roots {
		entries, err := reclaim.ListStashes(root)
		if err != nil {
			// Unreadable is not empty. Undetermine rather than under-report.
			return []Finding{unverifiableFinding(check, code, SeverityHousekeeping, "pipeline stashes",
				fmt.Sprintf("%s: %v", root, err))}, "could not read a repo's stash list"
		}
		owned := reclaim.PipelineStashes(entries, 0)
		if len(owned) == 0 {
			continue
		}
		landed := landedBranches(root, door)
		for _, e := range owned {
			age := int(e.Age(now).Hours() / 24)
			if age > oldest {
				oldest = age
			}
			ev := map[string]string{
				"repo_root": root, "stash_ref": e.Ref, "message": e.Message,
				"branch": e.Branch, "issue": strconv.Itoa(e.Issue), "stage": e.Stage, "age": days(age),
			}
			// A stash ref shifts as others are removed, so the identity is the
			// stash's own message and creation time, not stash@{N}.
			identity := []string{root, e.Message, e.CreatedAt.UTC().Format(time.RFC3339)}
			title := fmt.Sprintf("pipeline stash %s in %s (issue #%d %s, %s old)", e.Ref, filepath.Base(root), e.Issue, e.Stage, days(age))
			cause := "a stage stashed work and was killed before it restored it"
			var rem Remedy
			if landed[e.Branch] {
				rem = Remedy{ID: "sweep", Kind: RemedyAuto, Verb: verbStashSweep, Verify: check,
					Summary: "Restore pipeline stashes with `nightgauge stash sweep`",
					Preview: fmt.Sprintf("restore %s (%q) onto %s in %s; skipped if that branch is not checked out on a clean tree", e.Ref, e.Message, e.Branch, root)}
			} else {
				rem = manualRemedy("review", "Restore or drop the stash by hand", check,
					fmt.Sprintf("Not offered as a fix: branch %q is not proven merged, so restoring the stash could conflict with unlanded work", e.Branch),
					fmt.Sprintf("Inspect it: git -C %s stash show -p %s", root, e.Ref),
					fmt.Sprintf("Restore it on its branch with `nightgauge stash sweep --issue %d`", e.Issue))
			}
			out = append(out, newFinding(check, code, SeverityHousekeeping, title, cause, ev, identity, rem))
		}
	}
	if len(out) == 0 {
		return nil, "no unreclaimed pipeline stashes"
	}
	return out, fmt.Sprintf("%d pipeline stash(es), oldest %dd", len(out), oldest)
}

// landedBranches is the set of branches in root whose work is already in the
// default branch: the default branch itself and every stranded (merged,
// unheld) branch. Best effort: a failed scan proves nothing, so it yields
// only what was proven.
func landedBranches(root string, door mergedPRDoorFactory) map[string]bool {
	landed := map[string]bool{}
	scan, err := execution.ScanStrandedBranches(execution.StrandedBranchOptions{RepoRoot: root, MergedPRLookup: doorFor(door, root)})
	if err != nil {
		return landed
	}
	for _, b := range scan.Stranded {
		landed[b.Name] = true
	}
	for _, k := range scan.Kept {
		if k.Reason == execution.KeepDefaultBranch || k.Reason == execution.KeepNoOwnCommits {
			landed[k.Name] = true
		}
	}
	return landed
}

// doorFor applies the factory, tolerating a nil factory so every caller does
// not repeat the check. A nil factory and a factory returning nil mean the
// same thing: no second door, content test only.
func doorFor(f mergedPRDoorFactory, root string) execution.MergedPRLookup {
	if f == nil {
		return nil
	}
	return f(root)
}
