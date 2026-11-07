package main

import (
	"fmt"
	"io"

	"github.com/nightgauge/nightgauge/internal/orchestrator"
)

// haltedSuffix is appended after the status word of the `autonomous status`
// headline, so "Running" never reads as "every repository is dispatching"
// while a repository halt stops one of them (#2748).
func haltedSuffix(n int) string {
	switch {
	case n <= 0:
		return ""
	case n == 1:
		return " (1 repository halted)"
	default:
		return fmt.Sprintf(" (%d repositories halted)", n)
	}
}

// printHaltedRepos lists the repository-scoped halts. source is "" for a live
// answer and " (from state file)" when read from disk, so a persisted halt is
// not mistaken for a live one. Prints nothing when recs is empty.
func printHaltedRepos(w io.Writer, recs []orchestrator.RepoPauseRecord, source string) {
	if len(recs) == 0 {
		return
	}
	fmt.Fprintf(w, "\nHalted repositories (%d)%s — no new dispatch until resumed:\n", len(recs), source)
	for _, r := range recs {
		line := "  " + r.Repo
		if r.Issue > 0 {
			line += fmt.Sprintf("  #%d", r.Issue)
			if r.Stage != "" {
				line += " at " + r.Stage
			}
		} else if r.Stage != "" {
			line += "  at " + r.Stage
		}
		if r.Reason != "" {
			line += " — " + r.Reason
		}
		if r.PausedAt != "" {
			line += fmt.Sprintf(" (halted %s, %s ago)", r.PausedAt, formatElapsedSince(r.PausedAt))
		}
		fmt.Fprintln(w, line)
	}
	fmt.Fprintln(w, "  Act on the card in `nightgauge attention list`, or resume the repository there.")
}

// printArchitectureParked lists issues held by the architecture-approval
// gate: In progress with no run, by design. Prints nothing when empty.
func printArchitectureParked(w io.Writer, items []orchestrator.FailedItem, source string) {
	if len(items) == 0 {
		return
	}
	fmt.Fprintf(w, "\nAwaiting architecture approval (%d)%s — In progress with no run, by design:\n", len(items), source)
	for _, it := range items {
		line := fmt.Sprintf("  %s#%d", it.Repo, it.Number)
		if it.Title != "" {
			line += "  " + it.Title
		}
		if it.FailedAt != "" {
			line += fmt.Sprintf(" (parked %s)", it.FailedAt)
		}
		fmt.Fprintln(w, line)
	}
	fmt.Fprintln(w, "  Approve via `nightgauge attention list`.")
}
