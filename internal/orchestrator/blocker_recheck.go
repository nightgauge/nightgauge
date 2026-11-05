package orchestrator

import (
	"context"
	"fmt"
	"log"
	"regexp"
	"strconv"
	"strings"

	gitpkg "github.com/nightgauge/nightgauge/internal/git"
	pmstages "github.com/nightgauge/nightgauge/internal/orchestrator/stages"
)

// refreshResumedBase merges origin/<base> into a resumed run worktree (#2897).
// A seam so scheduler tests do not fetch from fixture remotes.
var refreshResumedBase = pmstages.RefreshResumedBase

// maxNamedBlockers bounds the forge reads one blocked outcome can cost.
const maxNamedBlockers = 10

// namedRefPattern matches `#N` and `owner/repo#N` references in a stage's
// outcome text.
var namedRefPattern = regexp.MustCompile(`(?:\b([A-Za-z0-9_.-]+/[A-Za-z0-9_.-]+))?#(\d+)\b`)

// refreshRunBase brings a resumed run worktree up to date with its base before
// the first stage runs (#2897), and reports how far behind it was. It returns
// a non-empty reason when the run must stop because the merge conflicts. A
// refresh that could not be attempted (offline, no branch) is logged and the
// run continues on the tree as found, which is what every run did before.
func refreshRunBase(ctx context.Context, issueNumber int, workdir string) (pmstages.BaseRefresh, string) {
	res, err := refreshResumedBase(ctx, workdir, issueNumber)
	if err != nil {
		log.Printf("#%d: base refresh skipped: %v", issueNumber, err)
		return res, ""
	}
	log.Printf("#%d: worktree is %d commit(s) behind origin/%s — %s", issueNumber, res.Behind, res.Base, res.Outcome)
	if res.Outcome != gitpkg.BaseMergeConflict {
		return res, ""
	}
	return res, baseConflictReason(res)
}

// baseConflictReason is the stop reason for a conflicting refresh. The
// [merge-conflict] marker makes every re-classification of the text agree
// with the merge_conflict kind the caller books.
func baseConflictReason(res pmstages.BaseRefresh) string {
	where := strings.Join(res.ConflictFiles, ", ")
	if where == "" {
		where = "the worktree"
	}
	reason := fmt.Sprintf("[merge-conflict] resumed worktree conflicts with origin/%s in %s", res.Base, where)
	if res.Reason != "" {
		reason += " (" + res.Reason + ")"
	}
	return reason + " — resolve the merge by hand"
}

// closedNamedBlockers returns the references in a stage's blocked outcome
// that the forge reads CLOSED (a merged PR reads closed too), excluding the
// issue itself. An unreadable reference is treated as open: the re-check only
// ever runs on positive evidence that a blocker went away.
func closedNamedBlockers(ctx context.Context, repo string, self int, detail string) []string {
	var closed []string
	seen := map[string]bool{}
	for _, m := range namedRefPattern.FindAllStringSubmatch(detail, -1) {
		if len(seen) >= maxNamedBlockers {
			break
		}
		target, num := m[1], m[2]
		if target == "" {
			target = repo
		}
		n, err := strconv.Atoi(num)
		if err != nil || n <= 0 || target == "" || (strings.EqualFold(target, repo) && n == self) {
			continue
		}
		key := strings.ToLower(target) + "#" + num
		if seen[key] {
			continue
		}
		seen[key] = true
		out, err := reconcileExecGh(ctx, "api", fmt.Sprintf("repos/%s/issues/%d", target, n), "--jq", ".state")
		if err != nil || !strings.EqualFold(strings.TrimSpace(string(out)), "closed") {
			continue
		}
		ref := "#" + num
		if !strings.EqualFold(target, repo) {
			ref = target + ref
		}
		closed = append(closed, ref)
	}
	return closed
}

// blockerRecheck is the answer to "does a blocked outcome stand?" (#2897).
type blockerRecheck struct {
	// Ran reports that a closed named blocker triggered a base refresh.
	Ran bool
	// Rerun says the base advanced, so the stage runs again on it.
	Rerun bool
	// Note is appended to the outcome text when the verdict stands, so the
	// record shows the re-check ran.
	Note string
	// ConflictReason is set when the refresh conflicted; it replaces the
	// verdict and the run books merge_conflict.
	ConflictReason string
}

// recheckBlockedOutcome re-checks a stage-declared blocked outcome whose named
// blockers include a CLOSED issue or PR (#2897): the run may be working on a
// base from before the blocker merged. It refreshes the base once. When the
// base advanced, the stage re-runs on it; when it did not, the verdict stands
// with a note that the re-check ran.
func recheckBlockedOutcome(ctx context.Context, repo string, issueNumber int, workdir, detail string) blockerRecheck {
	var verdict blockerRecheck
	if workdir == "" || !IsStageDeclaredBlockedOutcome(detail) {
		return verdict
	}
	closed := closedNamedBlockers(ctx, repo, issueNumber, detail)
	if len(closed) == 0 {
		return verdict
	}
	verdict.Ran = true
	names := strings.Join(closed, ", ")
	res, reason := refreshRunBase(ctx, issueNumber, workdir)
	switch {
	case reason != "":
		verdict.ConflictReason = reason
	case res.Advanced():
		verdict.Rerun = true
		log.Printf("#%d: named blocker(s) %s closed and base advanced — re-running the stage on the refreshed base", issueNumber, names)
	case res.Outcome == gitpkg.BaseMergeUpToDate:
		verdict.Note = fmt.Sprintf(" (named blocker(s) %s closed; base already contains origin/%s)", names, res.Base)
	default:
		verdict.Note = fmt.Sprintf(" (named blocker(s) %s closed; base refresh could not run)", names)
	}
	return verdict
}
