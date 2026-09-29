package doctor

import (
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/nightgauge/nightgauge/internal/intelligence/survival"
)

// checkSurvivalBacklog reports pending survival records whose observation
// window elapsed so long ago that their verdict is at risk (#992).
//
// This is the FIRST doctor arm that detects an absence rather than residue.
// Every other arm — leaked worktrees, stale stashes, orphaned processes —
// reports something that exists and should not. None answers "did the thing
// that was supposed to run, run?", which is exactly how the only automatic
// survival finalizer stayed stopped for weeks: the writer ran on every merge,
// the reader was triple-gated inside a loop nobody had started, and the failure
// mode was silence.
//
// The threshold is 2 x window because that is where `survival.Sweep` folds a
// record to `unobserved` — a verdict that is explicitly no evidence. Past that
// point the delay has not postponed the answer, it has destroyed it, so the arm
// fires BEFORE that line rather than after.
func survivalBacklogFindings(workspaceRoot string, now time.Time, windowDays int) ([]Finding, string) {
	const check, code = "survival_backlog", "NGD026"
	if workspaceRoot == "" {
		return nil, "survival backlog not checked (no workspace root)"
	}
	if windowDays <= 0 {
		windowDays = survival.DefaultWindowDays
	}

	store := survival.NewStore(workspaceRoot)
	pending, err := store.Pending()
	if err != nil {
		// Unreadable is not empty. Undetermine rather than report a clean bill.
		return []Finding{unverifiableFinding(check, code, SeverityInfo, "survival backlog", err.Error())},
			"could not read the survival store"
	}
	if len(pending) == 0 {
		return nil, "no pending survival records"
	}

	window := time.Duration(windowDays) * 24 * time.Hour
	var overdue []string
	oldestDays := 0
	oldestMerged := ""
	for _, rec := range pending {
		merged, parseErr := time.Parse(time.RFC3339, rec.MergedAt)
		if parseErr != nil {
			// A record whose timestamp cannot be read can never be finalized
			// either — surface it rather than silently skipping it.
			overdue = append(overdue, fmt.Sprintf("%s#%d (unparseable merged_at %q)",
				rec.Repo, rec.IssueNumber, rec.MergedAt))
			continue
		}
		age := now.Sub(merged)
		if age < 2*window {
			continue
		}
		days := int(age.Hours() / 24)
		if days > oldestDays {
			oldestDays = days
			oldestMerged = rec.MergedAt
		}
		overdue = append(overdue, fmt.Sprintf("%s#%d (%dd)", rec.Repo, rec.IssueNumber, days))
	}

	if len(overdue) == 0 {
		return nil, fmt.Sprintf("%d pending survival record(s), none past %dd", len(pending), 2*windowDays)
	}
	count := len(overdue)
	if len(overdue) > maxLeaksReported {
		overdue = append(overdue[:maxLeaksReported], fmt.Sprintf("… and %d more", len(overdue)-maxLeaksReported))
	}

	ev := map[string]string{
		"overdue":          strconv.Itoa(count),
		"pending":          strconv.Itoa(len(pending)),
		"window":           fmt.Sprintf("%dd", windowDays),
		"threshold":        fmt.Sprintf("%dd", 2*windowDays),
		"oldest":           days(oldestDays),
		"oldest_merged":    oldestMerged,
		"records":          strings.Join(overdue, "; "),
		"expected_cadence": "survival sweep finalizes records inside the window",
	}
	title := fmt.Sprintf("survival-backlog-stale: %d record(s) pending past 2x the %dd window (oldest %dd)",
		count, windowDays, oldestDays)
	f := newFinding(check, code, SeverityInfo, title,
		"nothing has finalized these records; past 2x the window they fold to \"unobserved\" and stop being evidence that merged work survived",
		ev, []string{workspaceRoot},
		Remedy{ID: "sweep", Kind: RemedyAuto, Verb: verbSurvivalSweep, Verify: check,
			Summary: "Finalize pending survival records with `nightgauge survival sweep`",
			Preview: fmt.Sprintf("finalize %d overdue survival record(s) in %s", count, workspaceRoot)})
	return []Finding{f}, fmt.Sprintf("%d survival record(s) overdue, oldest %dd", count, oldestDays)
}
