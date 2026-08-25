package doctor

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
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
// Everything here is warning-only. A leaked worktree is untidy, not broken, and
// a required failure would make `doctor` exit 2 on a workspace that runs
// perfectly well — which teaches operators to stop reading its output.

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
	Path   string
	Repo   string
	Reason execution.SkipReason
	// Blocking names what stood in the way of a SkipDirty verdict, so an
	// operator can tell "my work is in there" from "the pipeline scaffolded a
	// README into it" without opening the directory.
	Blocking []string
	Age      time.Duration
}

// scanLeakedWorktrees classifies every pipeline worktree across the workspace's
// repo roots, returning the ones that are registered, stale, and not
// reclaimable — plus whether the scan was DETERMINED.
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
func scanLeakedWorktrees(startDir string, now time.Time) (leaks []leakedWorktree, reclaimable int, determined bool) {
	roots := config.WorkspaceRepoRoots(startDir)
	if len(roots) == 0 {
		return nil, 0, false
	}

	for _, root := range roots {
		res, err := execution.SweepMergedWorktrees(execution.WorktreeSweepOptions{
			RepoRoot: root,
			DryRun:   true,
		})
		if err != nil {
			// One unreadable root undetermines the whole answer: a partial
			// scan is indistinguishable from a complete one at the call site.
			return nil, 0, false
		}
		reclaimable += len(res.Reclaimed)
		for _, s := range res.Skipped {
			if !isLeakReason(s.Reason) {
				continue
			}
			age := worktreeAge(s.Path, now)
			if age < staleWorktreeAge {
				continue
			}
			leaks = append(leaks, leakedWorktree{
				Path: s.Path, Repo: filepath.Base(root),
				Reason: s.Reason, Blocking: s.Blocking, Age: age,
			})
		}
	}
	sort.Slice(leaks, func(i, j int) bool { return leaks[i].Age > leaks[j].Age })
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

// checkLeakedWorktrees builds the doctor entry for registered-but-stale
// pipeline worktrees (#332 AC4).
func checkLeakedWorktrees(startDir string, now time.Time) (CheckItem, string) {
	leaks, reclaimable, determined := scanLeakedWorktrees(startDir, now)
	if !determined {
		msg := "leaked worktrees unverifiable: could not read the worktree set across the workspace's repo roots — not inside a git repository or workspace, or `git worktree list` failed. A clean report here would be an assertion about a scan that never ran"
		return CheckItem{OK: false, Detail: "could not scan for leaked worktrees", Error: msg}, msg
	}
	if len(leaks) == 0 && reclaimable == 0 {
		return CheckItem{OK: true, Detail: "no stale pipeline worktrees"}, ""
	}

	var parts []string
	for i, l := range leaks {
		if i == maxLeaksReported {
			parts = append(parts, fmt.Sprintf("… and %d more", len(leaks)-maxLeaksReported))
			break
		}
		entry := fmt.Sprintf("%s (%s, %dd, %s)", l.Path, l.Repo, int(l.Age.Hours()/24), l.Reason)
		if len(l.Blocking) > 0 {
			entry += " blocked by: " + strings.Join(l.Blocking, ", ")
		}
		parts = append(parts, entry)
	}
	if reclaimable > 0 {
		parts = append(parts, fmt.Sprintf("%d reclaimable now — run `nightgauge worktree sweep`", reclaimable))
	}
	msg := "stale pipeline worktrees: " + strings.Join(parts, "; ")
	return CheckItem{
		OK:     false,
		Detail: fmt.Sprintf("%d stale, %d reclaimable", len(leaks), reclaimable),
		Error:  msg,
	}, msg
}

// strandedBranch is one merged branch, in one repo, that no worktree holds.
type strandedBranch struct {
	Repo   string
	Branch string
}

// checkStrandedBranches builds the doctor entry for merged branches that no
// worktree holds (#912 AC4) — the leak the worktree arm above structurally
// cannot see, because it drives off `git worktree list` and these branches
// have no worktree left.
//
// Report-only, and stated as such in the message: the check names the branches
// and the command, and deletes nothing. execution.ScanStrandedBranches explains
// why deleting is not a follow-up but a design decision.
//
// This arm does NOT fetch, and does not need the worktree arm's fetch to be
// correct. A stale origin/<default> makes a just-merged branch read as
// unmerged content, so the branch is KEPT: staleness costs timeliness, never
// safety, and the arm never over-reports because of it. (In practice the
// worktree arm has already fetched every root by the time this runs, so the
// base ref is current — but nothing here depends on that ordering.)
//
// No age threshold, unlike the worktree arms. A branch whose content is
// already in the default branch cannot become un-merged by waiting, and a
// merged branch is stranded from the moment its PR lands — there is no
// "probably from the run that just finished" reading to guard against.
func checkStrandedBranches(startDir string) (CheckItem, string) {
	roots := config.WorkspaceRepoRoots(startDir)
	if len(roots) == 0 {
		msg := "stranded branches unverifiable: no repo roots resolved — not inside a git repository or workspace"
		return CheckItem{OK: false, Detail: "could not scan for stranded branches", Error: msg}, msg
	}

	var found []strandedBranch
	for _, root := range roots {
		scan, err := execution.ScanStrandedBranches(execution.StrandedBranchOptions{RepoRoot: root})
		if err != nil {
			// One unreadable root undetermines the answer, exactly as in
			// scanLeakedWorktrees: a partial scan and a complete one print
			// identically at the call site (#296, #323).
			msg := fmt.Sprintf("stranded branches unverifiable in %s: %v", root, err)
			return CheckItem{OK: false, Detail: "could not scan for stranded branches", Error: msg}, msg
		}
		for _, b := range scan.Stranded {
			found = append(found, strandedBranch{Repo: filepath.Base(root), Branch: b.Name})
		}
	}
	if len(found) == 0 {
		return CheckItem{OK: true, Detail: "no stranded merged branches"}, ""
	}

	sort.Slice(found, func(i, j int) bool {
		if found[i].Repo != found[j].Repo {
			return found[i].Repo < found[j].Repo
		}
		return found[i].Branch < found[j].Branch
	})
	names := make([]string, 0, len(found))
	for i, b := range found {
		if i == maxLeaksReported {
			names = append(names, fmt.Sprintf("… and %d more", len(found)-maxLeaksReported))
			break
		}
		names = append(names, b.Repo+" "+b.Branch)
	}
	msg := fmt.Sprintf("merged branches no worktree holds (report only, nothing deleted): %s — verify with `scripts/branch-merged-check.sh` and delete by hand",
		strings.Join(names, "; "))
	return CheckItem{
		OK:     false,
		Detail: fmt.Sprintf("%d stranded merged branch(es)", len(found)),
		Error:  msg,
	}, msg
}

// checkPipelineStashes builds the doctor entry for stashes the pipeline created
// and never reclaimed (#330 AC3).
//
// Age is the point. Every one of the five stashes the audit found was months
// old, and none of them was reported by anything — a leak with no age attached
// reads as "probably from the run that just finished" and gets ignored.
func checkPipelineStashes(startDir string, now time.Time) (CheckItem, string) {
	roots := config.WorkspaceRepoRoots(startDir)
	if len(roots) == 0 {
		msg := "pipeline stashes unverifiable: no repo roots resolved — not inside a git repository or workspace"
		return CheckItem{OK: false, Detail: "could not scan for pipeline stashes", Error: msg}, msg
	}

	var found []string
	oldest := 0
	for _, root := range roots {
		entries, err := reclaim.ListStashes(root)
		if err != nil {
			// A root that is not a git repository (or a git that failed) is
			// unreadable, not empty. Undetermine rather than under-report.
			msg := fmt.Sprintf("pipeline stashes unverifiable in %s: %v", root, err)
			return CheckItem{OK: false, Detail: "could not read a repo's stash list", Error: msg}, msg
		}
		for _, e := range reclaim.PipelineStashes(entries, 0) {
			days := int(e.Age(now).Hours() / 24)
			if days > oldest {
				oldest = days
			}
			found = append(found, fmt.Sprintf("%s %s #%d %s (%dd)",
				filepath.Base(root), e.Ref, e.Issue, e.Stage, days))
		}
	}
	if len(found) == 0 {
		return CheckItem{OK: true, Detail: "no unreclaimed pipeline stashes"}, ""
	}
	if len(found) > maxLeaksReported {
		found = append(found[:maxLeaksReported], fmt.Sprintf("… and %d more", len(found)-maxLeaksReported))
	}
	msg := fmt.Sprintf("unreclaimed pipeline stashes (oldest %dd): %s — run `nightgauge stash sweep`",
		oldest, strings.Join(found, "; "))
	return CheckItem{
		OK:     false,
		Detail: fmt.Sprintf("%d pipeline stash(es), oldest %dd", len(found), oldest),
		Error:  msg,
	}, msg
}
