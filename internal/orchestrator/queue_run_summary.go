package orchestrator

import (
	"fmt"
	"sort"
	"strings"
)

// QueueOutcomeKind names why a queued entry did not complete, coarsely enough
// that the CLI's exit code can be derived from it without re-reading prose.
type QueueOutcomeKind string

const (
	// QueueOutcomeCompleted — the pipeline ran to a successful terminal state.
	QueueOutcomeCompleted QueueOutcomeKind = "completed"
	// QueueOutcomeFailed — the pipeline ran and reached a terminal FAILURE.
	// The per-issue failure is swallowed so the rest of the batch continues
	// (that part is deliberate), but it is not success and must not be
	// reported as one (#875).
	QueueOutcomeFailed QueueOutcomeKind = "failed"
	// QueueOutcomeNotDispatched — the entry never reached runPipeline because
	// the queue run could not resolve it: the board fetch failed, or the issue
	// is not on the board. The operator asked for this issue and did not get
	// it, so it counts against the exit status exactly like a failure.
	QueueOutcomeNotDispatched QueueOutcomeKind = "not-dispatched"
	// QueueOutcomeBlocked — the entry was deliberately skipped because it has
	// open `blockedBy` dependencies. A controlled hold, not a failure: it does
	// NOT affect the exit status. Reported so the summary still accounts for
	// every entry.
	QueueOutcomeBlocked QueueOutcomeKind = "blocked"
)

// QueueOutcome is one queued entry's terminal accounting.
type QueueOutcome struct {
	Repo         string
	IssueNumber  int
	Kind         QueueOutcomeKind
	TerminalKind string // terminal_failure_kind when Kind == QueueOutcomeFailed
	Detail       string // why, for the non-dispatched and blocked kinds
}

// QueueRunSummary accounts for every entry a RunQueue pass consumed.
//
// It exists because RunQueue's error return cannot carry this (#875): a
// per-issue terminal failure is deliberately swallowed so the batch continues,
// which before this type meant `queue run` printed "Processing 1 queued
// issues..." and exited 0 on a run where nothing succeeded. Swallowing the
// failure is defensible; reporting overall success is not, and the exit code
// was the only signal.
type QueueRunSummary struct {
	Outcomes []QueueOutcome
}

// Completed counts entries whose pipeline succeeded.
func (s QueueRunSummary) Completed() int {
	n := 0
	for _, o := range s.Outcomes {
		if o.Kind == QueueOutcomeCompleted {
			n++
		}
	}
	return n
}

// HasFailures reports whether any entry failed or was never dispatched — the
// condition `queue run` exits non-zero on. Blocked entries are excluded: a
// dependency hold is the queue working as designed.
func (s QueueRunSummary) HasFailures() bool {
	for _, o := range s.Outcomes {
		if o.Kind == QueueOutcomeFailed || o.Kind == QueueOutcomeNotDispatched {
			return true
		}
	}
	return false
}

// Format renders the end-of-run summary: a count line plus one line per entry
// that did not complete, naming the issue and its terminal kind. Always ends
// with a newline; returns "" for an empty run.
func (s QueueRunSummary) Format() string {
	if len(s.Outcomes) == 0 {
		return ""
	}
	var b strings.Builder
	fmt.Fprintf(&b, "%d of %d queued issues completed.\n", s.Completed(), len(s.Outcomes))
	rest := make([]QueueOutcome, 0, len(s.Outcomes))
	for _, o := range s.Outcomes {
		if o.Kind != QueueOutcomeCompleted {
			rest = append(rest, o)
		}
	}
	sort.SliceStable(rest, func(i, j int) bool {
		return queueOutcomeRank(rest[i].Kind) < queueOutcomeRank(rest[j].Kind)
	})
	for _, o := range rest {
		label := string(o.Kind)
		switch {
		case o.Kind == QueueOutcomeFailed && o.TerminalKind != "":
			label = "failed (" + o.TerminalKind + ")"
		case o.Kind == QueueOutcomeFailed:
			// A terminal failure the run could not classify. Say that
			// explicitly rather than printing a bare "failed" that reads like
			// the kind was simply omitted.
			label = "failed (terminal kind unclassified)"
		}
		line := fmt.Sprintf("  %s#%d: %s", o.Repo, o.IssueNumber, label)
		if o.Detail != "" {
			line += " — " + o.Detail
		}
		b.WriteString(line + "\n")
	}
	return b.String()
}

func queueOutcomeRank(k QueueOutcomeKind) int {
	switch k {
	case QueueOutcomeFailed:
		return 0
	case QueueOutcomeNotDispatched:
		return 1
	default:
		return 2
	}
}

// QueueRunFailedError is returned to the CLI when a queue pass finished but at
// least one entry failed or was never dispatched. It carries the summary so
// the caller can render it; Error() stays a single line because cobra prints it
// to stderr on top of whatever the command already printed to stdout.
type QueueRunFailedError struct {
	Summary QueueRunSummary
}

func (e *QueueRunFailedError) Error() string {
	failed, undispatched := 0, 0
	for _, o := range e.Summary.Outcomes {
		switch o.Kind {
		case QueueOutcomeFailed:
			failed++
		case QueueOutcomeNotDispatched:
			undispatched++
		}
	}
	parts := make([]string, 0, 2)
	if failed > 0 {
		parts = append(parts, fmt.Sprintf("%d failed", failed))
	}
	if undispatched > 0 {
		parts = append(parts, fmt.Sprintf("%d never dispatched", undispatched))
	}
	return fmt.Sprintf("queue run did not complete cleanly: %s of %d queued issues",
		strings.Join(parts, " and "), len(e.Summary.Outcomes))
}
