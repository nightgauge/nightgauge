package doctor

import (
	"fmt"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/nightgauge/nightgauge/internal/config"
	"github.com/nightgauge/nightgauge/internal/reclaim"
)

// preservedWip is one WIP anchor, in one repo, that nothing has claimed.
type preservedWip struct {
	Repo     string
	RepoRoot string
	Ref      reclaim.WipRef
	Days     int
}

// maxWipPreviewPaths caps the paths a wip-prune preview names per ref.
const maxWipPreviewPaths = 20

// preservedWipFindings reports preserved-WIP refs (#1105), one finding per ref.
//
// The fourth reclamation arm, and the one whose absence was hardest to notice:
// unlike a leaked worktree or stash, a WIP ref leaves NOTHING on disk and
// nothing in any listing an operator habitually reads. `git status`, `git
// worktree list`, `git stash list` and `git branch` are all silent about it.
//
// Housekeeping, never a failure: preserved work is a salvage opportunity, not
// a broken workspace.
//
// No age threshold, deliberately: a WIP ref is only ever written when a stage
// was KILLED with uncommitted work, so every one of them describes work no run
// is still doing.
//
// Each finding declares a confirm `wip prune` remedy whose preview lists the
// paths the preserved commit touches. Prune only removes a ref whose content
// already landed in the base ref; unlanded work is kept.
func preservedWipFindings(startDir string, now time.Time) ([]Finding, string) {
	const check, code = "preserved_wip", "NGD020"
	roots := config.WorkspaceRepoRoots(startDir)
	if len(roots) == 0 {
		return []Finding{unverifiableFinding(check, code, SeverityHousekeeping, "preserved WIP",
			"no repo roots resolved — not inside a git repository or workspace")}, "could not scan for preserved WIP refs"
	}

	var found []preservedWip
	for _, root := range roots {
		refs, err := reclaim.ListWipRefs(root)
		if err != nil {
			// A root whose refs could not be read is unreadable, not empty.
			return []Finding{unverifiableFinding(check, code, SeverityHousekeeping, "preserved WIP",
				fmt.Sprintf("%s: %v", root, err))}, "could not read a repo's WIP refs"
		}
		for _, r := range refs {
			found = append(found, preservedWip{Repo: filepath.Base(root), RepoRoot: root, Ref: r, Days: r.AgeDays(now)})
		}
	}
	if len(found) == 0 {
		return nil, "no preserved WIP refs"
	}

	sort.Slice(found, func(i, j int) bool {
		if found[i].Days != found[j].Days {
			return found[i].Days > found[j].Days
		}
		return found[i].Ref.Ref < found[j].Ref.Ref
	})

	out := make([]Finding, 0, len(found))
	for _, w := range found {
		issue := "unknown issue"
		if w.Ref.Issue > 0 {
			issue = fmt.Sprintf("#%d", w.Ref.Issue)
		}
		paths := wipCommitPaths(w.RepoRoot, w.Ref.Commit)
		ev := map[string]string{
			"repo_root": w.RepoRoot, "ref": w.Ref.Ref, "commit": w.Ref.Commit, "branch": w.Ref.Branch,
			"issue": strconv.Itoa(w.Ref.Issue), "stage": w.Ref.Stage,
			"files_changed": strconv.Itoa(w.Ref.FilesChanged), "age": days(w.Days),
		}
		out = append(out, newFinding(check, code, SeverityHousekeeping,
			fmt.Sprintf("preserved work from a killed stage: %s %s %s, %d path(s), %s old",
				w.Repo, issue, shortSHA(w.Ref.Commit), w.Ref.FilesChanged, days(w.Days)),
			"a stage was killed with uncommitted work; the ref is the only anchor for it",
			ev, []string{w.RepoRoot, w.Ref.Ref},
			Remedy{ID: "prune", Kind: RemedyConfirm, Verb: verbWipPrune, Verify: check,
				Summary: "Prune the ref with `nightgauge wip prune` once its content has landed",
				Preview: fmt.Sprintf("delete %s (%s) in %s only if its content is already in the base ref; paths: %s",
					w.Ref.Ref, shortSHA(w.Ref.Commit), w.RepoRoot, paths)},
			manualRemedy("salvage", "Inspect and salvage the preserved work", check,
				fmt.Sprintf("Inspect it: git -C %s show --stat %s", w.RepoRoot, w.Ref.Commit),
				"List every preserved ref with `nightgauge wip list`")))
	}
	return out, fmt.Sprintf("%d preserved WIP ref(s), oldest %dd", len(found), found[0].Days)
}

func shortSHA(s string) string {
	if len(s) > 8 {
		return s[:8]
	}
	return s
}

// wipCommitPaths lists the paths a preserved commit touches, capped, for the
// wip-prune preview. An unreadable commit says so rather than listing nothing.
func wipCommitPaths(repoRoot, commit string) string {
	if commit == "" {
		return "(commit unknown)"
	}
	out, err := exec.Command("git", "-C", repoRoot, "show", "--no-renames", "--name-only",
		"--pretty=format:", commit).Output()
	if err != nil {
		return "(paths unreadable)"
	}
	var paths []string
	for _, l := range strings.Split(string(out), "\n") {
		if l = strings.TrimSpace(l); l != "" {
			paths = append(paths, l)
		}
	}
	if len(paths) == 0 {
		return "(none)"
	}
	if len(paths) > maxWipPreviewPaths {
		paths = append(paths[:maxWipPreviewPaths], fmt.Sprintf("… and %d more", len(paths)-maxWipPreviewPaths))
	}
	return strings.Join(paths, ", ")
}
