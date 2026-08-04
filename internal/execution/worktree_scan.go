package execution

import (
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// ActiveWorktreeIssues parses `git worktree list --porcelain` across every
// supplied repo root and returns the issue numbers held by an active worktree,
// plus whether that answer is DETERMINED.
//
// This is the ONE implementation of "which issues are live on disk?" — the
// scheduler's compose reconcile, `nightgauge doctor`, and `nightgauge cleanup`
// all route through it. Three independent copies existed before #323, and they
// had already drifted into three different `issue-NNN` parsers and two
// different answers to the question below (#296 fixed one of them).
//
// determined=false means "I could not find out", which is not the same as "no
// worktrees exist" — and the difference decides whether a destructive caller
// may act. Three paths used to produce an indistinguishable empty map (#296):
//
//  1. no roots supplied at all;
//  2. `git worktree list` failed;
//  3. a cross-repo run registers its worktree in the TARGET repo
//     (ensureWorktree runs git with cmd.Dir = repoRoot), so listing only the
//     launch root never sees it — the same root cause as #163/#229.
//
// Case 3 made a live cross-repo run look orphaned, and the scheduler answered
// that by running `docker compose down -v --remove-orphans` on its stack,
// destroying the running pipeline's named volumes. Failing toward the
// destructive answer is what makes this class worth fixing on sight (#165, and
// the same shape as #297's preserveVerdict).
//
// A root that no longer exists on disk is skipped rather than undetermining
// everything: it genuinely holds no worktrees, and a deleted sibling repo must
// not permanently disable reconciliation. Any other git failure undetermines
// the whole answer — a partial set is indistinguishable from a complete one at
// the call site, and acting on it tears down whatever the unreadable root held.
//
// Scope note: the answer covers exactly the roots it is given. A repo absent
// from the caller's root set (e.g. one deliberately left out of the workspace
// manifest) contributes nothing and is not an error — callers choose the roots,
// this function only reports what they contain.
func ActiveWorktreeIssues(roots []string) (map[int]bool, bool) {
	if len(roots) == 0 {
		return nil, false
	}
	out := map[int]bool{}
	for _, root := range roots {
		if _, err := os.Stat(root); err != nil {
			if os.IsNotExist(err) {
				continue
			}
			log.Printf("worktree-scan: cannot stat repo root %s: %v — active-worktree set is UNDETERMINED", root, err)
			return nil, false
		}
		cmd := exec.Command("git", "worktree", "list", "--porcelain")
		cmd.Dir = root
		data, err := cmd.Output()
		if err != nil {
			log.Printf("worktree-scan: git worktree list failed in %s: %v — active-worktree set is UNDETERMINED", root, err)
			return nil, false
		}
		for _, line := range strings.Split(strings.TrimSpace(string(data)), "\n") {
			if !strings.HasPrefix(line, "worktree ") {
				continue
			}
			path := strings.TrimSpace(strings.TrimPrefix(line, "worktree "))
			if n, ok := IssueNumberFromWorktreeDir(filepath.Base(path)); ok {
				out[n] = true
			}
		}
	}
	return out, true
}
