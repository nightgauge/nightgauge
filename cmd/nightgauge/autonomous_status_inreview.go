package main

import (
	"fmt"
	"io"

	"github.com/nightgauge/nightgauge/internal/orchestrator"
)

// printInReviewWithoutPR lists the board items the last scan found in "In
// review" with no open PR behind them (#1496). Nothing dispatches such an
// item and nothing else records it, so this section is how an operator sees
// one at all. Prints nothing when the list is empty.
func printInReviewWithoutPR(w io.Writer, items []orchestrator.InReviewWithoutPRItem) {
	if len(items) == 0 {
		return
	}
	fmt.Fprintf(w, "\nIn review with no open PR (%d) — not dispatched; move to Ready to re-run, or Done:\n", len(items))
	for _, it := range items {
		fmt.Fprintf(w, "  %s#%d  %s\n", it.Repo, it.Number, it.Title)
	}
}

// inReviewWithoutPRJSON renders the list for --json, as [] rather than null
// so a script can tell "none" without a nil check.
func inReviewWithoutPRJSON(items []orchestrator.InReviewWithoutPRItem) []orchestrator.InReviewWithoutPRItem {
	if items == nil {
		return []orchestrator.InReviewWithoutPRItem{}
	}
	return items
}
