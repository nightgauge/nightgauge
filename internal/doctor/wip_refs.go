package doctor

import (
	"fmt"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/nightgauge/nightgauge/internal/config"
	"github.com/nightgauge/nightgauge/internal/reclaim"
)

// preservedWip is one WIP anchor, in one repo, that nothing has claimed.
type preservedWip struct {
	Repo string
	Ref  reclaim.WipRef
	Days int
}

// checkPreservedWip builds the doctor entry for preserved-WIP refs (#1105).
//
// The fourth reclamation arm, and the one whose absence was hardest to notice:
// unlike a leaked worktree or stash, a WIP ref leaves NOTHING on disk and
// nothing in any listing an operator habitually reads. `git status`, `git
// worktree list`, `git stash list` and `git branch` are all silent about it.
// The only trace is a line in the session log at kill time, in a file the
// operator opens only if they already suspect work was lost — which is exactly
// the state this arm ends.
//
// Reported as a WARNING and never a required failure, like every other leak
// arm: preserved work is a salvage opportunity, not a broken workspace, and an
// arm that exits 2 on a healthy machine teaches operators to stop reading
// doctor's output.
//
// No age threshold, deliberately. A leaked worktree gets one because a live
// run's worktree is indistinguishable from a stale one; a WIP ref has no such
// ambiguity — it is only ever written when a stage was KILLED with uncommitted
// work, so every one of them describes work no run is still doing. Waiting a
// day before mentioning it would only mean the operator re-runs the issue
// first, which is precisely the sequence observed on 2026-08-28.
func checkPreservedWip(startDir string, now time.Time) (CheckItem, string) {
	roots := config.WorkspaceRepoRoots(startDir)
	if len(roots) == 0 {
		msg := "preserved WIP unverifiable: no repo roots resolved — not inside a git repository or workspace"
		return CheckItem{OK: false, Detail: "could not scan for preserved WIP refs", Error: msg}, msg
	}

	var found []preservedWip
	for _, root := range roots {
		refs, err := reclaim.ListWipRefs(root)
		if err != nil {
			// A root whose refs could not be read is unreadable, not empty.
			// Undetermine rather than under-report (#296, #323).
			msg := fmt.Sprintf("preserved WIP unverifiable in %s: %v", root, err)
			return CheckItem{OK: false, Detail: "could not read a repo's WIP refs", Error: msg}, msg
		}
		for _, r := range refs {
			found = append(found, preservedWip{Repo: filepath.Base(root), Ref: r, Days: r.AgeDays(now)})
		}
	}
	if len(found) == 0 {
		return CheckItem{OK: true, Detail: "no preserved WIP refs"}, ""
	}

	sort.Slice(found, func(i, j int) bool {
		if found[i].Days != found[j].Days {
			return found[i].Days > found[j].Days
		}
		return found[i].Ref.Ref < found[j].Ref.Ref
	})

	oldest := found[0].Days
	entries := make([]string, 0, len(found))
	for i, w := range found {
		if i == maxLeaksReported {
			entries = append(entries, fmt.Sprintf("… and %d more", len(found)-maxLeaksReported))
			break
		}
		issue := "unknown issue"
		if w.Ref.Issue > 0 {
			issue = fmt.Sprintf("#%d", w.Ref.Issue)
		}
		commit := w.Ref.Commit
		if len(commit) > 8 {
			commit = commit[:8]
		}
		entries = append(entries, fmt.Sprintf("%s %s %s %d path(s) (%dd)",
			w.Repo, issue, commit, w.Ref.FilesChanged, w.Days))
	}
	msg := fmt.Sprintf("preserved work from killed stages (oldest %dd): %s — inspect with `nightgauge wip list`, reclaim with `nightgauge wip prune`",
		oldest, strings.Join(entries, "; "))
	return CheckItem{
		OK:     false,
		Detail: fmt.Sprintf("%d preserved WIP ref(s), oldest %dd", len(found), oldest),
		Error:  msg,
	}, msg
}
