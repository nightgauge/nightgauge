// Package reclaim answers one question about every kind of machine state the
// pipeline leaves behind — worktrees, stashes, scaffolded files: did the
// pipeline create this, and may the pipeline take it back?
//
// Before #330/#332 each reclamation tool answered that for itself, and every
// one of them answered it wrong in the same direction. `worktree sweep` asked
// git "is this tree dirty?", got yes because the pipeline had scaffolded
// the knowledge index into it (`README.md` at the time of #332, `index.md`
// since #1370), and refused to reclaim — forever,
// since nothing ever removes that file. Nine worktrees deadlocked on the
// pipeline's own exhaust, and because each held a branch, `git branch -D`
// refused too: cleanup blocked at both ends by a file the pipeline itself
// wrote.
//
// The fix is not "ignore .nightgauge". `.worktrees/issue-701` held 209 staged
// deletions under `.nightgauge/pipeline/assessments/` — that IS the deliverable
// of an open issue, and a sweep that ignored `.nightgauge` would have destroyed
// it while reporting success. #237/#248 taught the dev gate that a
// bookkeeping-only deliverable is real work; the reclamation tools never
// learned it.
//
// What separates the two cases is not the path — both are under
// `.nightgauge/` — but whether git tracks the file. Exhaust is UNTRACKED
// bookkeeping: nobody committed it, nothing references it, the pipeline
// rewrites it on the next run. A tracked bookkeeping file that a stage
// modified or deleted is content someone decided to keep, and changing it is
// work. That single distinction is the whole of this package's status half,
// and it is why one rule can serve both a destructive sweep and a rescue.
package reclaim

import (
	"strings"

	"github.com/nightgauge/nightgauge/internal/ci"
)

// StatusEntry is one line of `git status --porcelain` (v1).
type StatusEntry struct {
	// XY is the two-character status field: index status, worktree status.
	// "??" is untracked, "!!" ignored (only present with --ignored).
	XY string
	// Path is the file path; for a rename, the destination.
	Path string
}

// Untracked reports whether git has never recorded this path — the property
// that separates the pipeline's own exhaust from content someone committed.
func (e StatusEntry) Untracked() bool { return e.XY == "??" }

// ParseStatus parses `git status --porcelain` (v1) output.
//
// This is the ONE porcelain parser in the tree. Format is `XY <path>`, with a
// rename rendered as `XY <old> -> <new>` — the destination is the path that
// exists. Callers must pass `--untracked-files=all`: porcelain's default
// collapses an untracked directory to a single entry, so ten scaffolded files
// in one new directory read as one path, and any count derived from it
// understates the work. Worse, it is ambient — a machine with
// `status.showUntrackedFiles=all` configured disagrees with CI (#223).
func ParseStatus(porcelain string) []StatusEntry {
	var entries []StatusEntry
	for _, line := range strings.Split(porcelain, "\n") {
		line = strings.TrimRight(line, "\r")
		// "XY " plus at least one path character.
		if len(line) < 4 {
			continue
		}
		xy := line[:2]
		p := strings.TrimSpace(line[3:])
		if _, after, found := strings.Cut(p, " -> "); found {
			p = after
		}
		if p = strings.Trim(p, `"`); p == "" {
			continue
		}
		entries = append(entries, StatusEntry{XY: xy, Path: p})
	}
	return entries
}

// StatusReport splits a worktree's changes into what the pipeline owns and
// what it must not touch.
type StatusReport struct {
	// Exhaust is untracked bookkeeping the pipeline wrote itself: the
	// knowledge scaffold, containment records, stage context files. Reclaiming
	// a worktree destroys these, and that is correct — the next run rewrites
	// them.
	Exhaust []string
	// Blocking is everything else: any deliverable change, and any TRACKED
	// bookkeeping change. Tracked means someone committed the file, so
	// modifying or deleting it is a decision, not exhaust.
	Blocking []string
}

// Blocked reports whether anything in this worktree must survive.
func (r StatusReport) Blocked() bool { return len(r.Blocking) > 0 }

// ClassifyStatus splits `git status --porcelain --untracked-files=all` output
// into exhaust and blocking changes.
//
// Fail direction: everything that is not provably the pipeline's own untracked
// exhaust blocks. An unrecognised path, a tracked bookkeeping file, a
// deliverable — all block. The cost of a wrong "blocking" is a worktree the
// operator removes by hand; the cost of a wrong "exhaust" is deleted work.
func ClassifyStatus(porcelain string) StatusReport {
	var r StatusReport
	for _, e := range ParseStatus(porcelain) {
		if e.Untracked() && ci.IsBookkeepingPath(e.Path) {
			r.Exhaust = append(r.Exhaust, e.Path)
			continue
		}
		r.Blocking = append(r.Blocking, e.Path)
	}
	return r
}

// StatusPaths returns every path in the status output regardless of class.
// Callers that only need "which files changed" use this instead of reaching
// for a second parser.
func StatusPaths(porcelain string) []string {
	entries := ParseStatus(porcelain)
	paths := make([]string, 0, len(entries))
	for _, e := range entries {
		paths = append(paths, e.Path)
	}
	return paths
}

// TrackedBookkeeping returns the tracked bookkeeping paths in the status
// output — the #237 case: a stage whose entire deliverable is under
// `.nightgauge/` (untracking a directory from the index, rewriting recorded
// assessments). The dev gate already treats these as real work; `worktree
// recover` needs the same answer so its rescue commit does not strip exactly
// the thing being rescued.
func TrackedBookkeeping(porcelain string) []string {
	var out []string
	for _, e := range ParseStatus(porcelain) {
		if !e.Untracked() && ci.IsBookkeepingPath(e.Path) {
			out = append(out, e.Path)
		}
	}
	return out
}
